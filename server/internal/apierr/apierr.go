// Package apierr is the one error shape every JSON answer uses, so the
// website, the phone app, the Kodi add-on and the Jellyfin plugin keep
// reading errors as they did from the Python (FastAPI):
//
//	{"detail": "That sign-in code expired; sign in again"}
//
// and for a request that fails validation (422), a sentence a person can
// act on plus the details, as app/main.py's validation_error wrote them:
//
//	{"detail": "Code is too long: at most 200 characters.",
//	 "errors": [{"type": "string_too_long", "loc": ["body", "code"], "msg": "...", "ctx": {"max_length": 200}}]}
//
// The website and the phone show `detail` as is.
package apierr

import (
	"encoding/json"
	"net/http"
)

// Error is an HTTP error with a plain-words detail. It satisfies Huma's
// StatusError (so handlers may return it directly) and sets its own content
// type, so Huma sends application/json rather than problem+json.
type Error struct {
	Status int               `json:"-"`
	Detail string            `json:"detail"`
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidationError is one validation problem, in FastAPI's shape (and its
// schema name, as in the Python's OpenAPI description).
type ValidationError struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input,omitempty"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

// New makes an error with a status and a plain sentence.
func New(status int, detail string) *Error { return &Error{Status: status, Detail: detail} }

func (e *Error) Error() string { return e.Detail }

// GetStatus is Huma's StatusError.
func (e *Error) GetStatus() int { return e.Status }

// ContentType is Huma's ContentTypeFilter: always plain JSON.
func (e *Error) ContentType(string) string { return "application/json" }

// Write sends e as the response, for handlers outside Huma.
func Write(w http.ResponseWriter, e *Error) {
	body, _ := json.Marshal(e)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}

// The common ones, worded as the Python's Starlette defaults were.
var (
	NotFound     = func() *Error { return New(http.StatusNotFound, "Not Found") }
	Internal     = func() *Error { return New(http.StatusInternalServerError, "Internal Server Error") }
	Unauthorized = func(detail string) *Error { return New(http.StatusUnauthorized, detail) }
)
