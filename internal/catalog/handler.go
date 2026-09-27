package catalog

import (
	"net/http"
	"strings"

	"github.com/heismyke/aebello/svc/internal/httpx"
)

type Handler struct{ Catalog *Catalog }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/destinations", httpx.HandlerFunc(h.list))
	mux.Handle("GET /v1/destinations/{code}", httpx.HandlerFunc(h.get))
	mux.Handle("GET /v1/plans/{slug}", httpx.HandlerFunc(h.plan))
}

var errLoading = httpx.Unavailable("catalog_loading", "Plans are loading. Try again in a moment.")

// list returns every destination, optionally filtered by ?q= (name or code).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	if !h.Catalog.Ready() {
		return errLoading
	}
	all := h.Catalog.Destinations()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		return httpx.OK(w, map[string]any{"destinations": all})
	}
	matches := []Destination{}
	for _, d := range all {
		if strings.Contains(strings.ToLower(d.Name), q) || strings.EqualFold(d.Code, q) {
			matches = append(matches, d)
		}
	}
	return httpx.OK(w, map[string]any{"destinations": matches})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	if !h.Catalog.Ready() {
		return errLoading
	}
	d, plans, ok := h.Catalog.Destination(r.PathValue("code"))
	if !ok {
		return httpx.NotFound("We don't sell eSIMs for this destination yet.")
	}
	return httpx.OK(w, map[string]any{"destination": d, "plans": plans})
}

// plan returns one plan. ?country= fills in the networks for that country.
func (h *Handler) plan(w http.ResponseWriter, r *http.Request) error {
	if !h.Catalog.Ready() {
		return errLoading
	}
	p, ok := h.Catalog.Plan(r.PathValue("slug"))
	if !ok {
		return httpx.NotFound("This plan is no longer available.")
	}
	return httpx.OK(w, map[string]any{"plan": p.For(r.URL.Query().Get("country"))})
}
