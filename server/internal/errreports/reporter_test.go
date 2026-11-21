package errreports

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"

	"movienight/server/internal/logging"
)

func TestNoDSNNoReporterAndTheLogIsUntouched(t *testing.T) {
	r, err := NewReporter("", "production", "api")
	if r != nil || err != nil {
		t.Fatal(r, err)
	}
	r.Flush() // nil-safe
	inner := slog.NewJSONHandler(io.Discard, nil)
	if LogHandler(inner, nil) != slog.Handler(inner) {
		t.Fatal("the log was wrapped with reporting off")
	}
}

func TestBeforeSendLeavesNoPersonRequestOrSecret(t *testing.T) {
	ev := sentry.NewEvent()
	ev.User = sentry.User{ID: "42", Email: "ann@example.org", IPAddress: "192.0.2.1"}
	ev.Request = &sentry.Request{URL: "https://mn.example/search?q=kids", Cookies: "mn_session=s", Headers: map[string]string{"Authorization": "Bearer x"}}
	ev.Message = "digest for ann@example.org failed"
	ev.Exception = []sentry.Exception{{Type: "request failed", Value: `GET https://api.themoviedb.org/3/search?api_key=K&query=kids: token mn_app_abc refused`}}
	ev.Contexts = map[string]sentry.Context{detailsContext: {"user": 42, "job": "digests", "note": "Bearer abc.def"}}
	ev.Breadcrumbs = []*sentry.Breadcrumb{{Message: "to ann@example.org", Data: map[string]any{"url": "/x?q=1"}}}

	out := scrubEvent(ev)
	if !reflect.DeepEqual(out.User, sentry.User{}) || out.Request != nil {
		t.Fatalf("user or request kept: %+v %+v", out.User, out.Request)
	}
	all := out.Message + out.Exception[0].Value + out.Breadcrumbs[0].Message
	for _, gone := range []string{"ann@example.org", "api_key", "kids", "mn_app_abc"} {
		if strings.Contains(all, gone) {
			t.Errorf("%q kept: %s", gone, all)
		}
	}
	d := out.Contexts[detailsContext]
	if _, ok := d["user"]; ok || d["job"] != "digests" || d["note"] != "Bearer …" || out.Breadcrumbs[0].Data != nil {
		t.Fatalf("details: %v, crumb data %v", d, out.Breadcrumbs[0].Data)
	}
}

// An ERROR line becomes a report sent to the collector: titled by the log
// message, valued by its error, with its stack, its other details and none
// of the person's.
func TestAnErrorLineIsReported(t *testing.T) {
	c := newCollector(t)
	r, err := NewReporter(c.dsn(), "staging", "worker")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(LogHandler(slog.NewJSONHandler(io.Discard, nil), r)).With("job", "plays")
	ctx, info := logging.WithRequest(context.Background())
	log.InfoContext(ctx, "fine")                   // not reported
	log.WarnContext(ctx, "collector slow", "n", 1) // not reported
	log.ErrorContext(ctx, "plays sync failed", "user", 42, "err", errors.New("jellyfin said no to ann@example.org"))
	r.Flush()

	got := c.received()
	if len(got) != 1 {
		t.Fatalf("%d reports", len(got))
	}
	if got[0].path != "/api/7/envelope/" {
		t.Fatalf("%+v", got[0])
	}
	body := string(got[0].body)
	for _, want := range []string{`"type":"plays sync failed"`, `"value":"jellyfin said no to [email]"`, `"environment":"staging"`,
		`"component":"worker"`, `"rid":"` + info.RID + `"`, `"job":"plays"`, `"frames":[`, "TestAnErrorLineIsReported"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing: %s", want, body)
		}
	}
	for _, gone := range []string{"ann@example.org", `"user"`, "log/slog"} {
		if strings.Contains(body, gone) {
			t.Errorf("%s sent: %s", gone, body)
		}
	}
}

// A panic recovered by httpx (or the job runner) is logged at ERROR with
// "panic" and "stack"; the report carries the frames of where it panicked.
func TestARecoveredPanicIsReportedWithWhereItHappened(t *testing.T) {
	c := newCollector(t)
	r, _ := NewReporter(c.dsn(), "production", "api")
	log := slog.New(LogHandler(slog.NewJSONHandler(io.Discard, nil), r))
	func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic while serving a request", "panic", p, "stack", "goroutine 1 [running]: ...")
			}
		}()
		panicHere()
	}()
	r.Flush()
	got := c.received()
	if len(got) != 1 {
		t.Fatalf("%d reports", len(got))
	}
	body := string(got[0].body)
	if !strings.Contains(body, `"value":"nil map"`) || !strings.Contains(body, "panicHere") || strings.Contains(body, "goroutine 1") {
		t.Fatalf("%s", body)
	}
}

func panicHere() { panic("nil map") }
