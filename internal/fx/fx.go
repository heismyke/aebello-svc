// Package fx converts the USD prices shown in the apps into the currency
// customers are charged in. The Paystack account only settles in naira, so
// a USD price is turned into NGN at checkout using a daily exchange rate plus
// a small buffer that protects the margin against the naira moving.
package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/heismyke/aebello/svc/internal/httpx"
)

// rateURL is ExchangeRate-API's free open endpoint, updated once a day.
const rateURL = "https://open.er-api.com/v6/latest/USD"

// Money is an amount in minor units (cents, kobo) with its currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type Converter struct {
	currency string  // charge currency, e.g. NGN
	buffer   float64 // e.g. 0.03 adds 3% on top of the market rate
	http     *http.Client

	mu   sync.RWMutex
	rate float64 // market rate, units of currency per USD
}

// New returns a converter into currency. fallbackRate (0 for none) is used
// until the first successful fetch, so checkout works even if the rate feed
// is down when the API starts.
func New(currency string, buffer, fallbackRate float64) *Converter {
	return &Converter{currency: currency, buffer: buffer, rate: fallbackRate, http: &http.Client{Timeout: 20 * time.Second}}
}

// Currency is what customers are charged in.
func (c *Converter) Currency() string { return c.currency }

// Run fetches the rate now and every 6 hours until ctx ends.
func (c *Converter) Run(ctx context.Context) {
	if c.currency == "USD" {
		return // no conversion needed
	}
	for {
		wait := 6 * time.Hour
		if err := c.refresh(ctx); err != nil {
			slog.Error("exchange rate refresh failed", "err", err)
			wait = 5 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Converter) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rateURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Result string             `json:"result"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	rate := body.Rates[c.currency]
	if body.Result != "success" || rate <= 0 {
		return fmt.Errorf("no %s rate in response", c.currency)
	}
	c.mu.Lock()
	c.rate = rate
	c.mu.Unlock()
	slog.Info("exchange rate updated", "currency", c.currency, "rate", rate)
	return nil
}

// Charge converts a USD price in cents into what the customer pays.
// NGN amounts are rounded up to the next ₦10 so they read cleanly.
func (c *Converter) Charge(usdCents int64) (Money, error) {
	if c.currency == "USD" {
		return Money{Amount: usdCents, Currency: "USD"}, nil
	}
	c.mu.RLock()
	rate := c.rate
	c.mu.RUnlock()
	if rate <= 0 {
		return Money{}, httpx.Unavailable("fx_unavailable", "Checkout is briefly unavailable. Please try again in a few minutes.")
	}
	major := float64(usdCents) / 100 * rate * (1 + c.buffer)
	return Money{Amount: int64(math.Ceil(major/10)) * 10 * 100, Currency: c.currency}, nil
}
