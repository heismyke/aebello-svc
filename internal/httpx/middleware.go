package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = iota

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Logging assigns a request id, recovers panics, and logs one line per request.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-Id", id)

		rec := &statusRecorder{ResponseWriter: w, status: 200}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				slog.ErrorContext(ctx, "panic", "panic", p, "requestId", id)
				WriteJSON(rec, 500, map[string]any{"error": "internal_error", "message": "Something went wrong on our side.", "requestId": id})
			}
			slog.InfoContext(ctx, "http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"ms", time.Since(start).Milliseconds(), "requestId", id)
		}()
		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// CORS lets the listed web origins call the API from the browser. The mobile
// app is not a browser, so it needs no entry here.
func CORS(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(origins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "3600")
			h.Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
