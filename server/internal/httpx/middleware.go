// Package httpx holds the request-level protections every route gets, in the
// order Handler applies them (outermost first):
//
//  1. Access log and request id: one JSON line per request with the route's
//     pattern (never the path's ids or the query string), the status, the time
//     taken and the person's numeric id; X-Request-ID on every response; a
//     panic becomes a logged 500 instead of a dropped connection.
//  2. Security headers on every response, including a strict
//     Content-Security-Policy (no inline script; images only from here and
//     TMDB). A page that sets its own CSP keeps it (the sign-in pages forbid
//     script entirely).
//  3. Path tricks refused: dot segments, doubled slashes and encoded
//     slashes/dots/backslashes answer 400, so no path can step from the
//     token-only scrobble prefix into anything else.
//  4. CSRF: any write to /api must carry X-Requested-With: movienight. A
//     custom header cannot be sent cross-site without a CORS preflight, which
//     this app never grants, so a hostile page cannot make a browser write.
//     The scrobble endpoints are exempt: other servers (Jellyfin, Kodi) call
//     them with a token, never a browser.
//  5. Request size: a body over MaxBodyBytes is refused (413). The Python had
//     no overall limit, only per-field ones.
//
// Ported from app/core/security.py and app/core/logs.py.
package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"movienight/server/internal/apierr"
	"movienight/server/internal/logging"
)

// MaxBodyBytes caps any request body. The largest legitimate one is a batch
// of BATCH_MAX_ITEMS (1,000) watches or ids, well under 200 KB; 1 MiB leaves
// room and still keeps one request from carrying unbounded data. Huma applies
// the same figure per operation (internal/api).
const MaxBodyBytes = 1 << 20

// CSP is the policy for the app shell and the API, as in app/core/security.py.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: https://image.tmdb.org; connect-src 'self'; font-src 'self'; " +
	"manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

var securityHeaders = [][2]string{
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "same-origin"},
	{"X-Frame-Options", "DENY"},
	{"Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
}

// The interactive API docs load Swagger UI from a CDN, so they get no CSP
// (as in the Python); they sit behind sign-in.
const docsPrefix = "/api/v1/docs"

// ScrobblePrefix is the token-only part of the API (other servers call it).
const ScrobblePrefix = "/api/v1/scrobble"

// CSRFHeader and CSRFValue are the header every /api write must carry.
const (
	CSRFHeader = "X-Requested-With"
	CSRFValue  = "movienight"
)

// Handler wraps next in every protection above. mux must be the innermost
// handler (the ServeMux), wrapped by any context-adding middleware in
// between: Route reads the pattern the mux matched from the request the mux
// itself received.
func Handler(next http.Handler, log *slog.Logger) http.Handler {
	return accessLog(log, securityAndLimits(next))
}

// Route wraps the ServeMux so the access line can name the matched pattern:
// the mux writes it into the *http.Request it is given, which may be a copy
// made by middleware between the access log and here.
func Route(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if info := logging.FromContext(r.Context()); info != nil {
			info.Route = r.Pattern
		}
	})
}

// recorder notes the status and adds the security headers just before the
// first byte goes out (so a handler's own CSP wins).
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
	isDocs bool
}

func (r *recorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = code
	h := r.Header()
	for _, kv := range securityHeaders {
		if h.Get(kv[0]) == "" {
			h.Set(kv[0], kv[1])
		}
	}
	if !r.isDocs && h.Get("Content-Security-Policy") == "" {
		h.Set("Content-Security-Policy", CSP)
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (Flush, deadlines).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ctx, info := logging.WithRequest(r.Context())
		w.Header().Set("X-Request-ID", info.RID)
		rec := &recorder{ResponseWriter: w, status: http.StatusOK, isDocs: strings.HasPrefix(r.URL.Path, docsPrefix)}
		req := r.WithContext(ctx)
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler { // the client went away; nothing to answer
					panic(p)
				}
				log.ErrorContext(ctx, "panic while serving a request", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if !rec.wrote {
					apierr.Write(rec, apierr.Internal())
				}
				rec.status = http.StatusInternalServerError
			}
			attrs := []any{
				"method", r.Method,
				"route", routeOf(info.Route, req.URL.Path),
				"status", rec.status,
				"ms", time.Since(started).Milliseconds(),
			}
			if uid := info.User(); uid != 0 {
				attrs = append(attrs, "user", uid)
			}
			log.InfoContext(ctx, "request", attrs...)
		}()
		next.ServeHTTP(rec, req)
	})
}

// routeOf is the pattern the mux matched ("/api/v1/me/devices/{device_id}"),
// never the concrete path; a request no route matched keeps only its first
// segment, as app/core/logs.py's _generic did.
func routeOf(pattern, path string) string {
	if p := pattern; p != "" {
		if _, path, ok := strings.Cut(p, " "); ok { // "GET /x" -> "/x"
			p = path
		}
		if p != "/" && p != "/api/" { // the catch-alls say nothing; fall through
			return p
		}
	}
	if strings.HasPrefix(path, "/static/") || path == "/" || path == "/healthz" || path == "/sw.js" {
		return path
	}
	trimmed := strings.Trim(path, "/")
	first, _, more := strings.Cut(trimmed, "/")
	if first == "" {
		return "/"
	}
	if more {
		return "/" + first + "/…"
	}
	return "/" + first
}

func securityAndLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if BadPath(r.URL.Path, r.RequestURI) {
			apierr.Write(w, apierr.New(http.StatusBadRequest, "Bad path"))
			return
		}
		if needsCSRF(r) && r.Header.Get(CSRFHeader) != CSRFValue {
			apierr.Write(w, apierr.New(http.StatusForbidden, "Missing X-Requested-With: movienight header"))
			return
		}
		if r.ContentLength > MaxBodyBytes {
			apierr.Write(w, apierr.New(http.StatusRequestEntityTooLarge, fmt.Sprintf("The request is too large: at most %d bytes.", MaxBodyBytes)))
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func needsCSRF(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/") {
		return false
	}
	// Exactly the scrobble prefix or below it: the Python's plain startswith
	// also exempted any path that merely began with the same letters
	// ("/api/v1/scrobbler").
	return p != ScrobblePrefix && !strings.HasPrefix(p, ScrobblePrefix+"/")
}

// BadPath reports a path with dot segments, doubled slashes, encoded dots,
// slashes or backslashes, or a backslash. path is the decoded path; raw is
// the request target as sent (RequestURI), checked for the encoded forms.
func BadPath(path, raw string) bool {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	low := strings.ToLower(raw)
	return strings.Contains(path, "/../") || strings.Contains(path, "/./") ||
		strings.HasSuffix(path, "/..") || strings.HasSuffix(path, "/.") ||
		strings.Contains(path, "//") || strings.Contains(raw, "//") ||
		strings.Contains(low, "%2e") || strings.Contains(low, "%2f") || strings.Contains(low, "%5c") ||
		strings.Contains(path, `\`)
}
