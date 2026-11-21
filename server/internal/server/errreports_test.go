package server_test

// Error reports through the whole server: the public switch the clients
// read before sign-in, and the relays behind the API's own protections
// (the write header, the size cap) with no sign-in.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func TestErrorReportsAreOffUntilTheirDSNIsSet(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	m := s.Client("", false).Do(t, "GET", "/api/v1/auth/methods", nil).Map(t)
	if er, _ := m["error_reports"].(map[string]any); er["web"] != false || er["app"] != false {
		t.Fatalf("%v", m)
	}
	for _, kind := range []string{"web", "app"} {
		if r := s.Client("", true).Do(t, "POST", "/api/v1/errors/"+kind, "{}\n", "Origin", "http://mn.test"); r.StatusCode != 404 {
			t.Errorf("%s: %d %s", kind, r.StatusCode, r.Body)
		}
	}
	// The page loads no reporter either.
	page := s.Client("", false).Do(t, "GET", "/", nil)
	if strings.Contains(page.Body, "sentry") || strings.Contains(page.Body, "{{") {
		t.Fatalf("%s", page.Body)
	}
}

func TestReportsReachTheCollectorWithoutSigningIn(t *testing.T) {
	var mu sync.Mutex
	var got []string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer collector.Close()
	dsn := strings.Replace(collector.URL, "http://", "http://k@", 1) + "/"
	env := testbase.BaseEnv(t)
	env["ERROR_REPORTS_WEB_DSN"], env["ERROR_REPORTS_APP_DSN"] = dsn+"1", dsn+"2"
	s := testenv.Start(t, env)

	m := s.Client("", false).Do(t, "GET", "/api/v1/auth/methods", nil).Map(t)
	if er, _ := m["error_reports"].(map[string]any); er["web"] != true || er["app"] != true {
		t.Fatalf("%v", m)
	}
	page := s.Client("", false).Do(t, "GET", "/", nil).Body
	if !strings.Contains(page, `src="/static/vendor/sentry.min.js?v=`) || !strings.Contains(page, `src="/static/js/errors.js?v=`) ||
		strings.Index(page, "errors.js") > strings.Index(page, "app.js") {
		t.Fatalf("the page does not load the reporter before the app: %s", page)
	}

	envelope := `{"dsn":"https://public@mn.test/0"}` + "\n" + `{"type":"event"}` + "\n" + `{"message":"boom","user":{"id":"9"}}` + "\n"
	// The website: its own origin, and the write header every /api POST needs.
	if r := s.Client("", false).Do(t, "POST", "/api/v1/errors/web", envelope, "Origin", "http://mn.test"); r.StatusCode != 403 {
		t.Fatalf("without the write header: %d", r.StatusCode)
	}
	if r := s.Client("", true).Do(t, "POST", "/api/v1/errors/web", envelope, "Origin", "https://elsewhere.test"); r.StatusCode != 403 {
		t.Fatalf("another origin: %d", r.StatusCode)
	}
	if r := s.Client("", true).Do(t, "POST", "/api/v1/errors/web", envelope, "Origin", "http://mn.test"); r.StatusCode != 200 {
		t.Fatalf("web: %d %s", r.StatusCode, r.Body)
	}
	// The app, signed in or not: no identity goes with the report.
	if r := s.Client("owner", true).Do(t, "POST", "/api/v1/errors/app", envelope, "Authorization", "Bearer mn_app_whatever"); r.StatusCode != 200 {
		t.Fatalf("app: %d %s", r.StatusCode, r.Body)
	}
	s.App.Relays.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("%d forwarded: %v", len(got), got)
	}
	// The two forwards run on their own, so in either order.
	slices.Sort(got)
	for i, path := range []string{"/api/1/envelope/", "/api/2/envelope/"} {
		if !strings.HasPrefix(got[i], path+" ") || !strings.Contains(got[i], `"message":"boom"`) ||
			strings.Contains(got[i], `"user"`) || strings.Contains(got[i], "owner") || !strings.Contains(got[i], `"environment":"production"`) {
			t.Errorf("%s", got[i])
		}
	}
}
