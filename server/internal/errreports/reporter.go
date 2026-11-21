// Package errreports sends crashes and errors to a Sentry-compatible
// collector (GlitchTip, Sentry) without ever handing its keys to a client.
//
// Two halves:
//
//   - Reporter: the api's and the worker's own errors. It hooks into the
//     log rather than into each handler: every place that fails already
//     logs at ERROR (a panic too, in httpx and the job runner), so a log
//     handler (LogHandler) turns those lines into reports and nothing else
//     has to know reporting exists.
//   - Relay: POST /api/v1/errors/web and /api/v1/errors/app, the "tunnel"
//     the browser and phone SDKs send their envelopes to. It checks where
//     they come from, limits how many, strips anything personal and
//     forwards them with the DSN only this server knows.
//
// What a report may carry is the same everywhere: the error, its stack, the
// environment and versions. Never a person (no user, no id, no address), a
// request body, a cookie, a header but the User-Agent, a token or a query
// string. Strings pass through logging.Redact, the log's own safety net.
package errreports

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"

	"movienight/server/internal/logging"
)

// flushTimeout bounds how long shutdown waits for reports still queued.
const flushTimeout = 3 * time.Second

// Reporter sends the server's own errors. A nil *Reporter is valid and
// reports nothing, so callers never branch on whether reporting is on.
type Reporter struct {
	client    *sentry.Client
	component string
}

// NewReporter makes the reporter for one process ("api" or "worker"). An
// empty dsn turns it off: the result is nil.
func NewReporter(dsn, environment, component string) (*Reporter, error) {
	if dsn == "" {
		return nil, nil
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: environment,
		Release:     buildRevision(),
		// Nothing the SDK could collect by itself about a person or a
		// request: no user fields, cookies, headers, bodies or queries.
		// SendDefaultPII is the older switch and is set too, so the intent
		// survives an SDK that reads only one of them.
		SendDefaultPII: false,
		DataCollection: &sentry.DataCollection{
			UserInfo:    sentry.Set(false),
			Cookies:     &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			HTTPHeaders: &sentry.HeaderCollectionConfig{Request: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff}, Response: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff}},
			HTTPBodies:  []sentry.BodyType{},
			QueryParams: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
		},
		BeforeSend: func(ev *sentry.Event, _ *sentry.EventHint) *sentry.Event { return scrubEvent(ev) },
		// The module list is every dependency's version on every report:
		// go.sum says the same once.
		Integrations: func(in []sentry.Integration) []sentry.Integration {
			out := in[:0]
			for _, i := range in {
				if i.Name() != "Modules" {
					out = append(out, i)
				}
			}
			return out
		},
		// Errors only: no tracing, no client reports about dropped events.
		DisableClientReports: true,
	})
	if err != nil {
		return nil, fmt.Errorf("ERROR_REPORTS_SERVER_DSN: %w", err)
	}
	return &Reporter{client: client, component: component}, nil
}

// Flush waits (briefly) for queued reports to go: on shutdown, so the
// error that stopped a process is not lost with it.
func (r *Reporter) Flush() {
	if r != nil {
		r.client.Flush(flushTimeout)
	}
}

// buildRevision is the commit the binary was built from, when the build
// recorded one (go build inside a git checkout). The image is built from a
// copy without .git, so there it is empty and reports carry no release
// rather than a made-up one.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}
	return ""
}

// detailsContext holds a log line's other attributes (job, kind, ...).
const detailsContext = "log"

// identityKeys are log attributes that name a person (by numeric id here,
// as the log allows) and stay out of reports, which name no one.
var identityKeys = map[string]bool{"user": true, "member": true, "username": true, "email": true, "uid": true}

// capture turns one ERROR log line into a report. The line's message is
// the report's title, its "err" (or "panic") the value, so reports group by
// where they were logged rather than by each error's changing text.
func (r *Reporter) capture(ctx context.Context, rec slog.Record, attrs []slog.Attr) {
	ev := sentry.NewEvent()
	ev.Level = sentry.LevelError
	ev.Logger = r.component
	ev.Tags = map[string]string{"component": r.component}
	if info := logging.FromContext(ctx); info != nil {
		// The request id finds the same request's lines in the log.
		ev.Tags["rid"] = info.RID
	}
	value := rec.Message
	extra := map[string]any{}
	add := func(a slog.Attr) {
		key := a.Key
		v := a.Value.Resolve()
		switch {
		case key == "stack":
			// The panic's stack as text: the report has it as frames.
		case key == "err" || key == "panic":
			value = attrText(v)
		case identityKeys[key]:
		default:
			extra[key] = attrValue(v)
		}
	}
	for _, a := range attrs {
		add(a)
	}
	rec.Attrs(func(a slog.Attr) bool { add(a); return true })
	if len(extra) > 0 {
		ev.Contexts = map[string]sentry.Context{detailsContext: extra}
	}
	ev.Exception = []sentry.Exception{{
		Type:       rec.Message,
		Value:      value,
		Stacktrace: ownStack(),
	}}
	r.client.CaptureEvent(ev, nil, nil)
}

func attrText(v slog.Value) string {
	if v.Kind() == slog.KindAny {
		if err, ok := v.Any().(error); ok {
			return err.Error()
		}
	}
	return v.String()
}

func attrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339)
	}
	return attrText(v)
}

// ownModule is this package, whose frames start every report's stack.
const ownModule = "movienight/server/internal/errreports"

// ownStack is where the error was logged, or, inside a recovered panic,
// where it panicked (the SDK trims to the panic's origin). The frames of the
// log call itself are dropped: every report would otherwise start in slog.
func ownStack() *sentry.Stacktrace {
	st := sentry.NewStacktrace()
	if st == nil {
		return nil
	}
	kept := st.Frames[:0]
	for _, f := range st.Frames {
		if f.Module == "log/slog" || (f.Module == ownModule && (strings.HasPrefix(f.Function, "(*logHandler).") ||
			strings.HasPrefix(f.Function, "(*Reporter).") || f.Function == "ownStack")) {
			continue
		}
		kept = append(kept, f)
	}
	st.Frames = kept
	return st
}

// scrubEvent is the last word on what leaves the server: whatever the SDK
// or a log line put in, no person, no request and no secret goes out.
func scrubEvent(ev *sentry.Event) *sentry.Event {
	ev.User = sentry.User{}
	ev.Request = nil
	ev.Message = logging.Redact(ev.Message)
	for i := range ev.Exception {
		ev.Exception[i].Value = logging.Redact(ev.Exception[i].Value)
		ev.Exception[i].Type = logging.Redact(ev.Exception[i].Type)
	}
	for k, v := range ev.Contexts[detailsContext] {
		if identityKeys[k] {
			delete(ev.Contexts[detailsContext], k)
			continue
		}
		if s, ok := v.(string); ok {
			ev.Contexts[detailsContext][k] = logging.Redact(s)
		}
	}
	for _, b := range ev.Breadcrumbs {
		b.Message = logging.Redact(b.Message)
		b.Data = nil
	}
	return ev
}

// LogHandler wraps a log handler so every line at ERROR or above is also
// reported. With a nil reporter it returns next unchanged: no cost at all
// when reporting is off.
func LogHandler(next slog.Handler, r *Reporter) slog.Handler {
	if r == nil {
		return next
	}
	return &logHandler{next: next, r: r}
}

type logHandler struct {
	next   slog.Handler
	r      *Reporter
	attrs  []slog.Attr
	prefix string // the open groups, "a.b."
}

func (h *logHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *logHandler) Handle(ctx context.Context, rec slog.Record) error {
	err := h.next.Handle(ctx, rec)
	if rec.Level >= slog.LevelError {
		if h.prefix != "" {
			// Attributes inside groups keep their dotted names in the report.
			grouped := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
			rec.Attrs(func(a slog.Attr) bool {
				a.Key = h.prefix + a.Key
				grouped.AddAttrs(a)
				return true
			})
			rec = grouped
		}
		h.r.capture(ctx, rec, h.attrs)
	}
	return err
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.next = h.next.WithAttrs(attrs)
	out.attrs = append([]slog.Attr{}, h.attrs...)
	for _, a := range attrs {
		a.Key = h.prefix + a.Key
		out.attrs = append(out.attrs, a)
	}
	return &out
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	out := *h
	out.next = h.next.WithGroup(name)
	if name != "" {
		out.prefix = h.prefix + name + "."
	}
	return &out
}
