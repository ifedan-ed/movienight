package errreports

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/httpx"
	"movienight/server/internal/testbase"
)

const siteOrigin = "https://mn.example"

// collector stands in for GlitchTip: it records every envelope it is sent.
type collector struct {
	*httptest.Server
	mu   sync.Mutex
	got  []received
	code int
}

type received struct {
	path, auth, ctype string
	body              []byte
}

func newCollector(t *testing.T) *collector {
	c := &collector{code: 200}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.got = append(c.got, received{r.URL.Path, r.Header.Get("X-Sentry-Auth"), r.Header.Get("Content-Type"), body})
		code := c.code
		c.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(c.Close)
	return c
}

// dsn is the collector's DSN for project 7.
func (c *collector) dsn() string {
	return strings.Replace(c.URL, "http://", "http://realkey@", 1) + "/7"
}

func (c *collector) received() []received {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]received{}, c.got...)
}

type logBuf struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.String()
}

// newRelay builds a relay on an emptied Valkey.
func newRelay(t *testing.T, kind, dsn string) (*Relay, *logBuf) {
	t.Helper()
	_, kv := testbase.Conns(t)
	return buildRelay(t, kv, kind, dsn)
}

func buildRelay(t *testing.T, kv valkey.Client, kind, dsn string) (*Relay, *logBuf) {
	t.Helper()
	logs := &logBuf{}
	r, err := NewRelay(RelayOptions{Kind: kind, DSN: dsn, Origin: siteOrigin, Environment: "staging",
		Callers: httpx.NewCallers(nil), KV: kv, Log: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return r, logs
}

// event is an envelope as the browser SDK sends one: a placeholder DSN,
// tracing context, one event full of what must not go on, and a session.
func event() string {
	ev := `{"event_id":"0123456789abcdef0123456789abcdef","level":"error",` +
		`"exception":{"values":[{"type":"TypeError","value":"x is undefined for ann@example.org",` +
		`"stacktrace":{"frames":[{"filename":"https://mn.example/static/js/app.js?v=abc123","lineno":3}]}}]},` +
		`"user":{"id":"42","email":"ann@example.org","ip_address":"{{auto}}"},` +
		`"server_name":"Ann's iPhone","environment":"production","sdk":{"name":"sentry.javascript.browser","settings":{"infer_ip":"auto"}},` +
		`"contexts":{"device":{"name":"Ann's iPhone","model":"iPhone16,1"},"os":{"name":"iOS","version":"26.1"}},` +
		`"request":{"url":"https://mn.example/search?q=kids+treasure","headers":{"User-Agent":"Mozilla/5.0","Referer":"https://mn.example/search?q=kids","Cookie":"mn_session=s3cret"},"cookies":{"mn_session":"s3cret"},"data":"pw=hunter2"},` +
		`"breadcrumbs":[{"category":"fetch","data":{"method":"GET","url":"/api/v1/search?q=kids","status_code":200,"request_headers":{"Authorization":"Bearer mn_app_SECRET"}}}]}`
	return `{"event_id":"0123456789abcdef0123456789abcdef","dsn":"https://public@mn.example/0","sent_at":"2026-11-21T10:00:00Z",` +
		`"trace":{"public_key":"public","trace_id":"abc"}}` + "\n" +
		`{"type":"event","length":` + strconv.Itoa(len(ev)) + "}\n" + ev + "\n" +
		`{"type":"session"}` + "\n" + `{"sid":"s","status":"ok"}` + "\n"
}

// sessionOnly is a valid envelope with nothing to forward: the limit tests
// use it so they count requests without calling the collector.
const sessionOnly = `{"dsn":"https://public@mn.example/0"}` + "\n" + `{"type":"session"}` + "\n" + `{"sid":"s"}` + "\n"

func post(r *Relay, body, addr string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/errors/"+r.kind, strings.NewReader(body))
	req.RemoteAddr = net.JoinHostPort(addr, "5555")
	if r.kind == Web {
		req.Header.Set("Origin", siteOrigin)
	}
	req.Header.Set("X-Requested-With", "movienight")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestARelayWithoutItsDSNIsNotFound(t *testing.T) {
	r, _ := newRelay(t, Web, "")
	if w := post(r, event(), "192.0.2.1"); w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var none *Relay // the description's stand-in
	if w := post(&Relay{kind: App}, event(), "192.0.2.1"); w.Code != 404 || none.Enabled() {
		t.Fatalf("%d", w.Code)
	}
}

func TestOversizeReportsAreRefused(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, App, c.dsn())
	big := sessionOnly + strings.Repeat("x", MaxEnvelopeBytes)
	if w := post(r, big, "192.0.2.1"); w.Code != 413 {
		t.Fatalf("with a length: %d", w.Code)
	}
	// No Content-Length (chunked): the read itself stops at the cap.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/errors/app", io.MultiReader(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("X-Requested-With", "movienight")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatalf("chunked: %d", w.Code)
	}
}

func TestOnlyEnvelopesAreTaken(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, App, c.dsn())
	for name, body := range map[string]string{
		"empty":               "",
		"plain JSON":          `{"message":"hi"}` + "x",
		"not JSON":            "hello\nworld",
		"item without a type": `{}` + "\n" + `{"length":2}` + "\n{}",
		"length past the end": `{}` + "\n" + `{"type":"event","length":500}` + "\n{}",
		"event not JSON":      `{}` + "\n" + `{"type":"event"}` + "\nnope",
		"too many items":      `{}` + "\n" + strings.Repeat(`{"type":"session"}`+"\n{}\n", maxItems+1),
	} {
		if w := post(r, body, "192.0.2.1"); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	r.Wait()
	if n := len(c.received()); n != 0 {
		t.Fatalf("%d forwarded", n)
	}
}

func TestTheWebRelayTakesOnlyThisSitesPages(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, Web, c.dsn())
	for _, origin := range []string{"", "https://evil.example", "http://mn.example", "https://mn.example:8443"} {
		if w := post(r, sessionOnly, "192.0.2.1", "Origin", origin); w.Code != 403 {
			t.Errorf("Origin %q: %d", origin, w.Code)
		}
	}
	if w := post(r, sessionOnly, "192.0.2.1"); w.Code != 200 {
		t.Fatalf("own origin: %d %s", w.Code, w.Body)
	}
}

func TestTheAppRelayNeedsTheAppsHeader(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, App, c.dsn())
	if w := post(r, sessionOnly, "192.0.2.1", "X-Requested-With", ""); w.Code != 403 {
		t.Fatalf("no header: %d", w.Code)
	}
	if w := post(r, sessionOnly, "192.0.2.1", "X-Requested-With", "XMLHttpRequest"); w.Code != 403 {
		t.Fatalf("wrong header: %d", w.Code)
	}
	// No Origin at all is what a phone sends.
	if w := post(r, sessionOnly, "192.0.2.1"); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestOneAddressGetsTwentyReportsInTenMinutes(t *testing.T) {
	c := newCollector(t)
	_, kv := testbase.Conns(t)
	r, _ := buildRelay(t, kv, App, c.dsn())
	web, _ := buildRelay(t, kv, Web, c.dsn())
	for i := 0; i < perAddressLimit; i++ {
		if w := post(r, sessionOnly, "192.0.2.1"); w.Code != 200 {
			t.Fatalf("report %d: %d", i+1, w.Code)
		}
	}
	w := post(r, sessionOnly, "192.0.2.1")
	if w.Code != 429 || w.Header().Get("Retry-After") != "600" {
		t.Fatalf("over the limit: %d %q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := post(r, sessionOnly, "198.51.100.7"); w.Code != 200 {
		t.Fatalf("another address: %d", w.Code)
	}
	// The website's reports are counted apart from the app's.
	if w := post(web, sessionOnly, "192.0.2.1"); w.Code != 200 {
		t.Fatalf("the web relay: %d", w.Code)
	}
	// An IPv6 /64 is one address: one household or one server.
	for i := 0; i < perAddressLimit; i++ {
		if w := post(r, sessionOnly, "2001:db8:1:2::"+strconv.Itoa(i+1)); w.Code != 200 {
			t.Fatalf("v6 report %d: %d", i+1, w.Code)
		}
	}
	if w := post(r, sessionOnly, "2001:db8:1:2::ff"); w.Code != 429 {
		t.Fatalf("the same /64: %d", w.Code)
	}
}

func TestEveryoneTogetherGetsThreeHundredAnHour(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, Web, c.dsn())
	for i := 0; i < overallLimit; i++ {
		addr := "10.0." + strconv.Itoa(i/perAddressLimit) + ".1"
		if w := post(r, sessionOnly, addr); w.Code != 200 {
			t.Fatalf("report %d from %s: %d", i+1, addr, w.Code)
		}
	}
	w := post(r, sessionOnly, "203.0.113.9") // a new address, the endpoint full
	if w.Code != 429 || w.Header().Get("Retry-After") != "3600" {
		t.Fatalf("over the overall limit: %d %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestAReportIsScrubbedAndForwardedWithTheConfiguredDSN(t *testing.T) {
	c := newCollector(t)
	r, _ := newRelay(t, Web, c.dsn())
	w := post(r, event(), "192.0.2.1")
	if w.Code != 200 || w.Body.String() != "{}" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r.Wait()
	got := c.received()
	if len(got) != 1 {
		t.Fatalf("%d forwarded", len(got))
	}
	f := got[0]
	if f.path != "/api/7/envelope/" || f.ctype != "application/x-sentry-envelope" ||
		!strings.Contains(f.auth, "sentry_key=realkey") || strings.Contains(f.auth, "public") {
		t.Fatalf("%+v", f)
	}
	lines := strings.Split(strings.TrimSuffix(string(f.body), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header, item header, event (the session dropped): %q", f.body)
	}
	var head map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatal(err)
	}
	if head["dsn"] != c.dsn() || head["trace"] != nil || head["event_id"] == nil {
		t.Fatalf("header: %v", head)
	}
	var item struct {
		Type   string `json:"type"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &item); err != nil || item.Type != "event" || item.Length != len(lines[2]) {
		t.Fatalf("item header %q (%d)", lines[1], len(lines[2]))
	}
	ev := lines[2]
	for _, gone := range []string{"ann@example.org", `"user"`, "ip_address", "Ann's iPhone", "kids", "treasure",
		"s3cret", "hunter2", "Cookie", "Referer", "mn_app_SECRET", "Authorization", "v=abc123", `"production"`} {
		if strings.Contains(ev, gone) {
			t.Errorf("%q went on: %s", gone, ev)
		}
	}
	for _, kept := range []string{`"environment":"staging"`, `"infer_ip":"never"`, `"url":"https://mn.example/search"`, `"User-Agent":"Mozilla/5.0"`,
		`"model":"iPhone16,1"`, `"url":"/api/v1/search"`, `"status_code":200`, "x is undefined for [email]",
		`"filename":"https://mn.example/static/js/app.js?…"`} {
		if !strings.Contains(ev, kept) {
			t.Errorf("%q missing: %s", kept, ev)
		}
	}
}

func TestAFailedForwardIsLoggedWithoutTheReport(t *testing.T) {
	c := newCollector(t)
	c.code = 500
	r, logs := newRelay(t, App, c.dsn())
	if w := post(r, event(), "192.0.2.1"); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	r.Wait()
	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"status":500`) || strings.Contains(out, "undefined") {
		t.Fatalf("log: %s", out)
	}
	// A collector that is not there at all: the same, and no wait beyond
	// the forward's own timeout.
	c.Close()
	logs.Reset()
	if w := post(r, event(), "192.0.2.1"); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	r.Wait()
	if out := logs.String(); !strings.Contains(out, "forward failed") || strings.Contains(out, "undefined") || strings.Contains(out, "realkey") {
		t.Fatalf("log: %s", out)
	}
}
