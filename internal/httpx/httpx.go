// Package httpx holds the small amount of HTTP plumbing every handler shares:
// JSON in and out, one error shape, and middleware.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
)

// Error is an error the client is meant to see. Its JSON is
// {"error": code, "message": ..., "requestId": ...}.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func BadRequest(msg string) *Error   { return &Error{Status: 400, Code: "bad_request", Message: msg} }
func Invalid(msg string) *Error      { return &Error{Status: 422, Code: "validation_error", Message: msg} }
func Unauthorized(msg string) *Error { return &Error{Status: 401, Code: "unauthorized", Message: msg} }
func NotFound(msg string) *Error     { return &Error{Status: 404, Code: "not_found", Message: msg} }
func Conflict(code, msg string) *Error {
	return &Error{Status: 409, Code: code, Message: msg}
}
func Unavailable(code, msg string) *Error {
	return &Error{Status: 503, Code: code, Message: msg}
}

// HandlerFunc is an http.HandlerFunc that returns an error instead of writing
// one, so handlers read top to bottom with plain `return err`.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func (fn HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := fn(w, r)
	if err == nil {
		return
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path, "requestId", RequestID(r.Context()))
		apiErr = &Error{Status: 500, Code: "internal_error", Message: "Something went wrong on our side."}
	}
	WriteJSON(w, apiErr.Status, map[string]any{
		"error": apiErr.Code, "message": apiErr.Message, "requestId": RequestID(r.Context()),
	})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// OK writes a 200 JSON response. Returning it keeps handlers one-liners.
func OK(w http.ResponseWriter, v any) error {
	WriteJSON(w, http.StatusOK, v)
	return nil
}

func Created(w http.ResponseWriter, v any) error {
	WriteJSON(w, http.StatusCreated, v)
	return nil
}

// DecodeJSON reads a JSON body of at most 1 MB into v.
func DecodeJSON(r *http.Request, v any) error {
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		return BadRequest("The request body is not valid JSON.")
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// PathID returns the {name} path segment, or a 404 if it is not a UUID.
// Malformed ids are answered before reaching the database.
func PathID(r *http.Request, name string) (string, error) {
	id := r.PathValue(name)
	if !uuidPattern.MatchString(id) {
		return "", NotFound("Not found.")
	}
	return id, nil
}
