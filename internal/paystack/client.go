// Package paystack is a small client for the Paystack payments API: start a
// checkout, verify a payment, and check webhook signatures.
package paystack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const baseURL = "https://api.paystack.co"

type Client struct {
	secretKey string
	http      *http.Client
}

// New returns nil when no key is configured, so callers can check
// `client == nil` to know payments are switched off.
func New(secretKey string) *Client {
	if secretKey == "" {
		return nil
	}
	return &Client{secretKey: secretKey, http: &http.Client{Timeout: 20 * time.Second}}
}

// Checkout starts a payment and returns the hosted page the customer pays on.
// amount is in minor units (kobo, cents). reference is our order id, so the
// same order can never be charged under two references.
func (c *Client) Checkout(ctx context.Context, email string, amount int64, currency, reference, callbackURL string) (string, error) {
	var out struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	err := c.call(ctx, http.MethodPost, "/transaction/initialize", map[string]any{
		"email":        email,
		"amount":       amount,
		"currency":     currency,
		"reference":    reference,
		"callback_url": callbackURL,
	}, &out)
	return out.AuthorizationURL, err
}

// Transaction is the part of a verified payment we act on.
type Transaction struct {
	Status   string `json:"status"` // "success" once paid
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Verify asks Paystack for the current state of a payment.
func (c *Client) Verify(ctx context.Context, reference string) (Transaction, error) {
	var out Transaction
	err := c.call(ctx, http.MethodGet, "/transaction/verify/"+reference, nil, &out)
	return out, err
}

// ValidSignature checks the x-paystack-signature header of a webhook, an
// HMAC-SHA512 of the raw body keyed with the secret key.
func (c *Client) ValidSignature(body []byte, signature string) bool {
	mac := hmac.New(sha512.New, []byte(c.secretKey))
	mac.Write(body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paystack %s: %w", path, err)
	}
	defer resp.Body.Close()

	var env struct {
		Status  bool            `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("paystack %s: decode: %w", path, err)
	}
	if !env.Status {
		return fmt.Errorf("paystack %s: %s", path, env.Message)
	}
	return json.Unmarshal(env.Data, out)
}
