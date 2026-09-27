package orders

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/heismyke/aebello/svc/internal/auth"
	"github.com/heismyke/aebello/svc/internal/db"
	"github.com/heismyke/aebello/svc/internal/httpx"
)

type Handler struct {
	Service *Service
	Auth    *auth.Handler
}

func (h *Handler) Routes(mux *http.ServeMux) {
	signedIn := func(fn httpx.HandlerFunc) http.Handler { return h.Auth.Require(fn) }
	mux.Handle("POST /v1/orders", signedIn(h.create))
	mux.Handle("GET /v1/orders", signedIn(h.list))
	mux.Handle("GET /v1/orders/{id}", signedIn(h.get))
	mux.Handle("GET /v1/esims", signedIn(h.esims))
	mux.Handle("GET /v1/esims/{id}", signedIn(h.esim))
	mux.Handle("POST /v1/webhooks/paystack", httpx.HandlerFunc(h.paystackWebhook))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PlanSlug string `json:"planSlug"`
		Country  string `json:"country"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if in.PlanSlug == "" {
		return httpx.Invalid("Choose a plan.")
	}
	user := auth.CurrentUser(r.Context())
	o, payURL, err := h.Service.Create(r.Context(), user, in.PlanSlug, strings.ToUpper(in.Country))
	if err != nil {
		return err
	}
	return httpx.Created(w, map[string]any{"order": o, "paymentUrl": payURL})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	list, err := h.Service.ordersOf(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"orders": list})
}

// get returns an order with its eSIMs. A pending order is checked with
// Paystack first, so the app sees "paid" as soon as the customer returns from
// the payment page, even before the webhook arrives.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	o, err := h.ownOrder(r, id)
	if err != nil {
		return err
	}
	if o.Status == "pending" {
		if err := h.Service.Confirm(r.Context(), id); err != nil {
			slog.Warn("confirm on view", "order", id, "err", err)
		}
		if o, err = h.ownOrder(r, id); err != nil {
			return err
		}
	}
	esims, err := h.Service.esimsWhere(r.Context(), "e.order_id = $1", id)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"order": o, "esims": esims})
}

func (h *Handler) ownOrder(r *http.Request, id string) (Order, error) {
	o, err := h.Service.order(r.Context(), id)
	if db.IsNoRows(err) || (err == nil && o.UserID != auth.CurrentUser(r.Context()).ID) {
		return Order{}, errNotFound
	}
	return o, err
}

func (h *Handler) esims(w http.ResponseWriter, r *http.Request) error {
	list, err := h.Service.esimsWhere(r.Context(), "e.user_id = $1", auth.CurrentUser(r.Context()).ID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"esims": h.Service.sync(r.Context(), list)})
}

func (h *Handler) esim(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	e, err := h.Service.esim(r.Context(), id)
	if db.IsNoRows(err) || (err == nil && e.UserID != auth.CurrentUser(r.Context()).ID) {
		return httpx.NotFound("eSIM not found.")
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"esim": h.Service.sync(r.Context(), []Esim{e})[0]})
}

// paystackWebhook is called by Paystack after a payment. The event is only a
// hint: Confirm re-checks the payment with Paystack before acting on it.
func (h *Handler) paystackWebhook(w http.ResponseWriter, r *http.Request) error {
	ps := h.Service.Paystack
	if ps == nil {
		return httpx.NotFound("Not found.")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if !ps.ValidSignature(body, r.Header.Get("x-paystack-signature")) {
		return httpx.Unauthorized("Invalid signature.")
	}
	var event struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return httpx.BadRequest("Invalid event.")
	}
	if event.Event == "charge.success" {
		if err := h.Service.Confirm(r.Context(), event.Data.Reference); err != nil {
			slog.Warn("paystack webhook", "reference", event.Data.Reference, "err", err)
		}
	}
	return httpx.OK(w, map[string]any{"received": true})
}
