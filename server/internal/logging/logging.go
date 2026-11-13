// Package logging sets up the server's log: JSON lines on stdout (Loki
// collects them), one access line per request, and the owner's rule that the
// log keeps only what debugging needs:
//
//   - People are named by their numeric id only; never usernames, names or
//     email addresses. Titles, search text and query strings never appear.
//   - Each request gets an id (12 hex characters, sent back as X-Request-ID)
//     and every line written while it runs carries it as "rid".
//   - As a safety net, every string value of every line is redacted: query
//     strings are cut from anything that looks like a URL or a path, email
//     addresses are masked, and the row values a database error quotes are
//     cut. So an upstream error that quotes its URL (TMDB's search, with its
//     api_key and the words searched) cannot leak them.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"regexp"
	"sync/atomic"
)

// New returns the JSON logger every part of the server uses.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Value.Kind() {
			case slog.KindString:
				a.Value = slog.StringValue(Redact(a.Value.String()))
			case slog.KindAny:
				// An error value: log its redacted text, not the value itself.
				if err, ok := a.Value.Any().(error); ok {
					a.Value = slog.StringValue(Redact(err.Error()))
				}
			}
			return a
		},
	})
	return slog.New(&contextHandler{Handler: h})
}

// contextHandler adds the request id (and the person's id once known) to
// every line logged with a request's context.
type contextHandler struct{ slog.Handler }

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if info := FromContext(ctx); info != nil {
		r.AddAttrs(slog.String("rid", info.RID))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{h.Handler.WithGroup(name)}
}

// RequestInfo is what the access line needs to know about one request,
// filled in while it runs: the id, and the person once auth knows them.
type RequestInfo struct {
	RID string
	// Route is the pattern the mux matched (httpx.Route sets it).
	Route string
	uid   atomic.Int64 // 0: nobody signed in (ids start at 1)
}

// SetUser records the signed-in person's numeric id for the access line.
func (i *RequestInfo) SetUser(id int64) { i.uid.Store(id) }

// User is the recorded person's id, or 0.
func (i *RequestInfo) User() int64 { return i.uid.Load() }

type infoKey struct{}

// WithRequest starts a request's info with a fresh id.
func WithRequest(ctx context.Context) (context.Context, *RequestInfo) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	info := &RequestInfo{RID: hex.EncodeToString(b)}
	return context.WithValue(ctx, infoKey{}, info), info
}

// FromContext returns the request's info, or nil outside a request.
func FromContext(ctx context.Context) *RequestInfo {
	info, _ := ctx.Value(infoKey{}).(*RequestInfo)
	return info
}

// SetUser records the person on the request in ctx, if there is one.
func SetUser(ctx context.Context, id int64) {
	if info := FromContext(ctx); info != nil {
		info.SetUser(id)
	}
}

// The patterns below are the Python's, which its tests
// pinned down; see there for the examples each one is for.
var (
	// A URL or an absolute path followed by a query string: keep the path.
	queryRe = regexp.MustCompile(`((?:https?://|/)[^\s'"<>?]*)\?[^\s'"<>]*`)
	emailRe = regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	// "Key (username)=(alice) already exists".
	keyValuesRe = regexp.MustCompile(`((?i:key) \([^)]*\)=)\((?:[^()]|\([^()]*\))*\)`)
	// "Failing row contains (1, alice, ...)": pgx puts DETAIL on its own
	// field, so the value list ends at the closing bracket of the line.
	failingRowRe = regexp.MustCompile(`(Failing row contains )\(.*\)`)
	// `invalid input syntax for type integer: "abc"`.
	badInputRe = regexp.MustCompile(`(invalid input (?:syntax|value) for (?:type|enum) [^:\n]+: )"(?:[^"\n]|"")*"`)
)

// Redact removes what must never reach the log from one string.
func Redact(s string) string {
	s = queryRe.ReplaceAllString(s, "$1?…")
	s = emailRe.ReplaceAllString(s, "[email]")
	s = failingRowRe.ReplaceAllString(s, "$1(…)")
	s = keyValuesRe.ReplaceAllString(s, "$1(…)")
	return badInputRe.ReplaceAllString(s, `$1"…"`)
}
