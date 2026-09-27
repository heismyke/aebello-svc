// Package auth handles accounts and sign-in. Signing in returns an opaque
// bearer token; the web app and the mobile app both send it as
// "Authorization: Bearer <token>". Only its sha256 is stored.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/heismyke/aebello/svc/internal/db"
	"github.com/heismyke/aebello/svc/internal/httpx"
)

const sessionTTL = 90 * 24 * time.Hour

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Handler struct{ DB *pgxpool.Pool }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("POST /v1/auth/register", httpx.HandlerFunc(h.register))
	mux.Handle("POST /v1/auth/login", httpx.HandlerFunc(h.login))
	mux.Handle("POST /v1/auth/logout", h.Require(httpx.HandlerFunc(h.logout)))
	mux.Handle("GET /v1/me", h.Require(httpx.HandlerFunc(h.me)))
}

type credentials struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	var in credentials
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Email = normalizeEmail(in.Email)
	switch {
	case in.Name == "":
		return httpx.Invalid("Enter your name.")
	case !validEmail(in.Email):
		return httpx.Invalid("Enter a valid email address.")
	case len(in.Password) < 8:
		return httpx.Invalid("Use a password of at least 8 characters.")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return err
	}
	var u User
	err = h.DB.QueryRow(r.Context(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id, email, name`,
		in.Email, in.Name, hash).Scan(&u.ID, &u.Email, &u.Name)
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("email_taken", "An account with this email already exists. Sign in instead.")
	}
	if err != nil {
		return err
	}
	return h.startSession(w, r, u, http.StatusCreated)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var in credentials
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	var u User
	var hash string
	err := h.DB.QueryRow(r.Context(),
		`SELECT id, email, name, password_hash FROM users WHERE email = $1`,
		normalizeEmail(in.Email)).Scan(&u.ID, &u.Email, &u.Name, &hash)
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	if err != nil || !CheckPassword(hash, in.Password) {
		return httpx.Unauthorized("That email and password don't match.")
	}
	return h.startSession(w, r, u, http.StatusOK)
}

func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, u User, status int) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	_, err := h.DB.Exec(r.Context(),
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		u.ID, hashToken(token), time.Now().Add(sessionTTL))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, status, map[string]any{"token": token, "user": u})
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	_, err := h.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, hashToken(bearer(r)))
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	return httpx.OK(w, map[string]any{"user": CurrentUser(r.Context())})
}

type ctxKey struct{}

// Require rejects requests without a valid session token and makes the user
// available to the handler through CurrentUser.
func (h *Handler) Require(next http.Handler) http.Handler {
	return httpx.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		token := bearer(r)
		if token == "" {
			return httpx.Unauthorized("Sign in to continue.")
		}
		var u User
		err := h.DB.QueryRow(r.Context(), `
			SELECT u.id, u.email, u.name FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(token)).Scan(&u.ID, &u.Email, &u.Name)
		if db.IsNoRows(err) {
			return httpx.Unauthorized("Your session has ended. Sign in again.")
		}
		if err != nil {
			return err
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
		return nil
	})
}

// CurrentUser returns the signed-in user. Only valid behind Require.
func CurrentUser(ctx context.Context) User {
	u, _ := ctx.Value(ctxKey{}).(User)
	return u
}

func bearer(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(token)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}
