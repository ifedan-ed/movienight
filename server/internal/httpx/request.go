package httpx

import (
	"context"
	"net/http"
)

type requestKey struct{}

// KeepRequest puts the request into its own context, for handlers that are
// given only a context (Huma operations) but need the connection's address
// or a header (the TV pairing limit counts by caller address).
func KeepRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
	})
}

// RequestFrom returns the request KeepRequest stored. It panics without
// KeepRequest installed: a wiring mistake, caught by any test of the route.
func RequestFrom(ctx context.Context) *http.Request {
	r, ok := ctx.Value(requestKey{}).(*http.Request)
	if !ok {
		panic("httpx.KeepRequest is not installed")
	}
	return r
}
