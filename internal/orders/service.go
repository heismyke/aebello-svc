// Package orders sells plans and delivers the eSIMs. An order's life:
//
//  1. Create: the customer picks a plan; we save a pending order and open a
//     Paystack checkout whose reference is the order id.
//  2. Confirm: the Paystack webhook (or the app checking back) verifies the
//     payment with Paystack and marks the order paid.
//  3. Fulfil: we buy the eSIM from eSIM Access (processing), then collect the
//     allocated profile a few seconds later (completed).
//
// A background worker retries steps 2-3 so nothing stalls if a call fails.
package orders

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/heismyke/aebello/svc/internal/auth"
	"github.com/heismyke/aebello/svc/internal/catalog"
	"github.com/heismyke/aebello/svc/internal/db"
	"github.com/heismyke/aebello/svc/internal/esimaccess"
	"github.com/heismyke/aebello/svc/internal/fx"
	"github.com/heismyke/aebello/svc/internal/httpx"
	"github.com/heismyke/aebello/svc/internal/paystack"
)

// syncAfter is how stale an eSIM's status may be before we ask the provider
// again. Usage only updates every 2-3 hours on their side anyway.
const syncAfter = 10 * time.Minute

type Service struct {
	DB       *pgxpool.Pool
	Catalog  *catalog.Catalog
	ESIM     *esimaccess.Client
	Paystack *paystack.Client // nil when payments are not configured
	FX       *fx.Converter    // USD price -> charged currency
	WebURL   string
	// OrderingEnabled gates spending real eSIM Access balance.
	OrderingEnabled bool
}

var errNotFound = httpx.NotFound("Order not found.")

// Create saves a pending order and returns the page where the customer pays.
func (s *Service) Create(ctx context.Context, user auth.User, slug, country string) (Order, string, error) {
	if s.Paystack == nil {
		return Order{}, "", httpx.Unavailable("payments_unavailable", "Payments aren't switched on yet. Please try again soon.")
	}
	plan, ok := s.Catalog.Plan(slug)
	if !ok {
		return Order{}, "", httpx.NotFound("This plan is no longer available.")
	}
	if country != "" && !plan.Covers(country) {
		return Order{}, "", httpx.Invalid("This plan doesn't cover that destination.")
	}
	charge, err := s.FX.Charge(plan.Price)
	if err != nil {
		return Order{}, "", err
	}
	o, err := s.insertOrder(ctx, Order{
		UserID: user.ID, PlanSlug: plan.Slug, PlanName: plan.Name, CountryCode: country,
		DataBytes: plan.DataBytes, Days: plan.Days, CostUnits: plan.CostUnits,
		Amount: charge.Amount, Currency: charge.Currency, ListPrice: plan.Price,
	})
	if err != nil {
		return Order{}, "", err
	}
	url, err := s.Paystack.Checkout(ctx, user.Email, o.Amount, o.Currency, o.ID, s.WebURL+"/account/order?id="+o.ID)
	if err != nil {
		_ = s.fail(ctx, o.ID, "checkout: "+err.Error())
		return Order{}, "", err
	}
	return o, url, nil
}

// Confirm checks a pending order's payment with Paystack and, once paid,
// starts fulfilment. It is safe to call any number of times.
func (s *Service) Confirm(ctx context.Context, id string) error {
	o, err := s.order(ctx, id)
	if db.IsNoRows(err) {
		return errNotFound
	}
	if err != nil || o.Status != "pending" || s.Paystack == nil {
		return err
	}
	tx, err := s.Paystack.Verify(ctx, o.ID)
	if err != nil {
		return err
	}
	if tx.Status != "success" {
		return nil // not paid (yet)
	}
	if tx.Amount < o.Amount || tx.Currency != o.Currency {
		slog.Error("payment does not match order", "order", o.ID, "paid", tx.Amount, "currency", tx.Currency)
		return s.fail(ctx, o.ID, "payment amount mismatch")
	}
	if ok, err := s.setStatus(ctx, o.ID, "pending", "paid"); err != nil || !ok {
		return err
	}
	slog.Info("order paid", "order", o.ID, "amount", o.Amount)
	go s.fulfil(context.WithoutCancel(ctx), o.ID)
	return nil
}

// fulfil buys the eSIM for a paid order and waits for it to be allocated.
func (s *Service) fulfil(ctx context.Context, id string) {
	orderNo, ok := s.buy(ctx, id)
	if !ok {
		return
	}
	// Profiles are usually ready within 30 seconds; the worker picks up
	// anything slower.
	for range 6 {
		time.Sleep(5 * time.Second)
		if done, _ := s.collect(ctx, id, orderNo); done {
			return
		}
	}
}

// buy places the eSIM Access order for a paid order.
func (s *Service) buy(ctx context.Context, id string) (string, bool) {
	if !s.OrderingEnabled {
		slog.Warn("order is paid but ESIM_ORDERING_ENABLED is off; not buying the eSIM", "order", id)
		return "", false
	}
	if ok, err := s.setStatus(ctx, id, "paid", "processing"); err != nil || !ok {
		return "", false // already being handled
	}
	o, err := s.order(ctx, id)
	if err != nil {
		slog.Error("load order", "order", id, "err", err)
		return "", false
	}
	orderNo, err := s.ESIM.Order(ctx, o.ID, o.PlanSlug, o.CostUnits)
	var apiErr *esimaccess.Error
	switch {
	case errors.As(err, &apiErr):
		// e.g. low balance or a changed wholesale price. Needs a person.
		slog.Error("eSIM order refused", "order", id, "err", err)
		_ = s.fail(ctx, id, err.Error())
		return "", false
	case err != nil:
		slog.Warn("eSIM order failed, will retry", "order", id, "err", err)
		_, _ = s.setStatus(ctx, id, "processing", "paid")
		return "", false
	}
	if _, err := s.DB.Exec(ctx, `UPDATE orders SET provider_order_no = $2 WHERE id = $1`, id, orderNo); err != nil {
		slog.Error("save provider order", "order", id, "orderNo", orderNo, "err", err)
		return "", false
	}
	return orderNo, true
}

// collect saves the allocated eSIMs of a processing order.
func (s *Service) collect(ctx context.Context, id, orderNo string) (bool, error) {
	profiles, err := s.ESIM.ProfilesByOrder(ctx, orderNo)
	if errors.Is(err, esimaccess.ErrNotReady) || (err == nil && len(profiles) == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `UPDATE orders SET status = 'completed', updated_at = now()
			WHERE id = $1 AND status = 'processing' RETURNING user_id`, id).Scan(&userID)
		if err != nil {
			return err // no rows: another worker finished it
		}
		for _, p := range profiles {
			_, err := tx.Exec(ctx, `
				INSERT INTO esims (order_id, user_id, esim_tran_no, iccid, activation_code, qr_code_url,
					esim_status, smdp_status, total_bytes, used_bytes, expires_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				id, userID, p.EsimTranNo, p.ICCID, p.AC, p.QRCodeURL, p.EsimStatus, p.SMDPStatus,
				p.TotalVolume, p.OrderUsage, p.Expires())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if db.IsNoRows(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	slog.Info("order completed", "order", id, "esims", len(profiles))
	return true, nil
}

// RunWorker retries orders that are stuck between steps.
func (s *Service) RunWorker(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Payments whose webhook never arrived.
		if pending, err := s.ordersWithStatus(ctx, "pending"); err == nil {
			for _, o := range pending {
				if time.Since(o.CreatedAt) < 24*time.Hour {
					_ = s.Confirm(ctx, o.ID)
				}
			}
		}
		if s.OrderingEnabled {
			paid, _ := s.ordersWithStatus(ctx, "paid")
			for _, o := range paid {
				s.buy(ctx, o.ID) // collected on a later tick
			}
		}
		processing, _ := s.ordersWithStatus(ctx, "processing")
		for _, o := range processing {
			if o.ProviderOrderNo != nil {
				if _, err := s.collect(ctx, o.ID, *o.ProviderOrderNo); err != nil {
					slog.Warn("collect eSIM", "order", o.ID, "err", err)
				}
			}
		}
	}
}

// sync refreshes eSIMs whose status is older than syncAfter. Failures keep
// the last known state; the apps show when it was last updated.
func (s *Service) sync(ctx context.Context, list []Esim) []Esim {
	var stale []int
	for i, e := range list {
		if time.Since(e.SyncedAt) > syncAfter {
			stale = append(stale, i)
		}
	}
	if len(stale) == 0 {
		return list
	}

	var wg sync.WaitGroup
	for _, i := range stale {
		wg.Go(func() {
			p, err := s.ESIM.Profile(ctx, list[i].EsimTranNo)
			if err != nil {
				slog.Warn("sync eSIM", "esim", list[i].ID, "err", err)
				return
			}
			e := &list[i]
			e.esimStatus, e.smdpStatus, e.TotalBytes, e.ExpiresAt = p.EsimStatus, p.SMDPStatus, p.TotalVolume, p.Expires()
			e.UsedBytes = max(e.UsedBytes, p.OrderUsage)
			e.SyncedAt = time.Now()
		})
	}
	wg.Wait()

	// Usage is more current from the usage endpoint (10 eSIMs per call).
	for chunk := range slices.Chunk(stale, 10) {
		var nos []string
		for _, i := range chunk {
			nos = append(nos, list[i].EsimTranNo)
		}
		usage, err := s.ESIM.Usage(ctx, nos)
		if err != nil {
			slog.Warn("sync usage", "err", err)
			continue
		}
		for _, u := range usage {
			for _, i := range chunk {
				if list[i].EsimTranNo == u.EsimTranNo {
					list[i].UsedBytes = u.DataUsage
				}
			}
		}
	}

	for _, i := range stale {
		e := &list[i]
		e.Status = friendlyStatus(e.esimStatus, e.smdpStatus, e.UsedBytes)
		if err := s.saveSync(ctx, *e); err != nil {
			slog.Warn("save eSIM sync", "esim", e.ID, "err", err)
		}
	}
	return list
}
