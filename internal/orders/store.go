package orders

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type Order struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"` // pending, paid, processing, completed, failed
	PlanSlug    string     `json:"planSlug"`
	PlanName    string     `json:"planName"`
	CountryCode string     `json:"countryCode"`
	DataBytes   int64      `json:"dataBytes"`
	Days        int        `json:"days"`
	Amount      int64      `json:"amount"`    // charged, minor units of Currency
	ListPrice   int64      `json:"listPrice"` // shown price, USD cents
	Currency    string     `json:"currency"`
	CreatedAt   time.Time  `json:"createdAt"`
	PaidAt      *time.Time `json:"paidAt"`

	UserID          string  `json:"-"`
	CostUnits       int64   `json:"-"`
	ProviderOrderNo *string `json:"-"`
}

type Esim struct {
	ID             string     `json:"id"`
	OrderID        string     `json:"orderId"`
	PlanName       string     `json:"planName"`
	CountryCode    string     `json:"countryCode"`
	ICCID          string     `json:"iccid"`
	ActivationCode string     `json:"activationCode"`
	QRCodeURL      string     `json:"qrCodeUrl"`
	Status         string     `json:"status"` // see friendlyStatus
	TotalBytes     int64      `json:"totalBytes"`
	UsedBytes      int64      `json:"usedBytes"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	SyncedAt       time.Time  `json:"syncedAt"`
	CreatedAt      time.Time  `json:"createdAt"`

	EsimTranNo string `json:"-"`
	UserID     string `json:"-"`
	esimStatus string
	smdpStatus string
}

const orderColumns = `id, user_id, status, plan_slug, plan_name, country_code, data_bytes, duration_days,
	cost_units, amount, currency, list_price, provider_order_no, created_at, paid_at`

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.UserID, &o.Status, &o.PlanSlug, &o.PlanName, &o.CountryCode, &o.DataBytes, &o.Days,
		&o.CostUnits, &o.Amount, &o.Currency, &o.ListPrice, &o.ProviderOrderNo, &o.CreatedAt, &o.PaidAt)
	return o, err
}

func (s *Service) insertOrder(ctx context.Context, o Order) (Order, error) {
	return scanOrder(s.DB.QueryRow(ctx, `
		INSERT INTO orders (user_id, plan_slug, plan_name, country_code, data_bytes, duration_days, cost_units, amount, currency, list_price)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+orderColumns,
		o.UserID, o.PlanSlug, o.PlanName, o.CountryCode, o.DataBytes, o.Days, o.CostUnits, o.Amount, o.Currency, o.ListPrice))
}

func (s *Service) order(ctx context.Context, id string) (Order, error) {
	return scanOrder(s.DB.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1`, id))
}

func (s *Service) ordersOf(ctx context.Context, userID string) ([]Order, error) {
	return s.queryOrders(ctx, `SELECT `+orderColumns+` FROM orders WHERE user_id = $1 ORDER BY created_at DESC`, userID)
}

func (s *Service) ordersWithStatus(ctx context.Context, status string) ([]Order, error) {
	return s.queryOrders(ctx, `SELECT `+orderColumns+` FROM orders WHERE status = $1 ORDER BY created_at LIMIT 50`, status)
}

func (s *Service) queryOrders(ctx context.Context, sql string, args ...any) ([]Order, error) {
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	return list, rows.Err()
}

// setStatus moves an order from one status to another and reports whether it
// did. The "from" check makes each step happen once even if the webhook, the
// app and the worker all try at the same time.
func (s *Service) setStatus(ctx context.Context, id, from, to string) (bool, error) {
	paid := ""
	if to == "paid" {
		paid = ", paid_at = now()"
	}
	tag, err := s.DB.Exec(ctx, `UPDATE orders SET status = $3, updated_at = now()`+paid+` WHERE id = $1 AND status = $2`, id, from, to)
	return tag.RowsAffected() == 1, err
}

func (s *Service) fail(ctx context.Context, id, reason string) error {
	_, err := s.DB.Exec(ctx,
		`UPDATE orders SET status = 'failed', failure_reason = $2, updated_at = now() WHERE id = $1`, id, reason)
	return err
}

const esimColumns = `e.id, e.order_id, e.user_id, o.plan_name, o.country_code, e.esim_tran_no, e.iccid,
	e.activation_code, e.qr_code_url, e.esim_status, e.smdp_status, e.total_bytes, e.used_bytes,
	e.expires_at, e.synced_at, e.created_at`

func scanEsim(row pgx.Row) (Esim, error) {
	var e Esim
	err := row.Scan(&e.ID, &e.OrderID, &e.UserID, &e.PlanName, &e.CountryCode, &e.EsimTranNo, &e.ICCID,
		&e.ActivationCode, &e.QRCodeURL, &e.esimStatus, &e.smdpStatus, &e.TotalBytes, &e.UsedBytes,
		&e.ExpiresAt, &e.SyncedAt, &e.CreatedAt)
	e.Status = friendlyStatus(e.esimStatus, e.smdpStatus, e.UsedBytes)
	return e, err
}

func (s *Service) esim(ctx context.Context, id string) (Esim, error) {
	return scanEsim(s.DB.QueryRow(ctx, `SELECT `+esimColumns+` FROM esims e JOIN orders o ON o.id = e.order_id WHERE e.id = $1`, id))
}

// esimsWhere lists eSIMs matching a condition on the esims table.
func (s *Service) esimsWhere(ctx context.Context, cond string, arg any) ([]Esim, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+esimColumns+` FROM esims e JOIN orders o ON o.id = e.order_id
		WHERE `+cond+` ORDER BY e.created_at DESC`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Esim{}
	for rows.Next() {
		e, err := scanEsim(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}

func (s *Service) saveSync(ctx context.Context, e Esim) error {
	_, err := s.DB.Exec(ctx, `
		UPDATE esims SET esim_status = $2, smdp_status = $3, used_bytes = $4, total_bytes = $5,
			expires_at = $6, synced_at = now()
		WHERE id = $1`, e.ID, e.esimStatus, e.smdpStatus, e.UsedBytes, e.TotalBytes, e.ExpiresAt)
	return err
}

// friendlyStatus turns the provider's two status fields into one the apps
// can show. See "Understanding eSIM Profile Status" in the eSIM Access docs.
func friendlyStatus(esimStatus, smdpStatus string, used int64) string {
	switch {
	case esimStatus == "CANCEL":
		return "cancelled"
	case esimStatus == "REVOKED":
		return "revoked"
	case esimStatus == "SUSPENDED":
		return "suspended"
	case esimStatus == "USED_EXPIRED" || esimStatus == "UNUSED_EXPIRED":
		return "expired"
	case esimStatus == "USED_UP":
		return "depleted"
	case smdpStatus == "DELETED":
		return "deleted"
	case smdpStatus == "RELEASED":
		return "ready" // created, waiting to be installed
	case used > 0:
		return "active"
	default:
		return "installed"
	}
}
