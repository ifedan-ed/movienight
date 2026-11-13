// Package testenv gives tests the API built exactly as cmd/api builds it,
// served over real HTTP, on the real database and Valkey of package
// testbase (scripts/test.sh starts both containers).
//
// Packages share the one database, so test.sh runs packages one at a time
// (go test -p 1); within a package, tests that use the database do not run
// in parallel.
package testenv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/logging"
	"movienight/server/internal/server"
	"movienight/server/internal/testbase"
)

// Server is the API served over real HTTP for one test.
type Server struct {
	*httptest.Server
	App  *server.App
	Pool *pgxpool.Pool
	KV   valkey.Client
	Logs *strings.Builder
}

// Start builds the app with env's settings on emptied connections.
func Start(t testing.TB, env testbase.Env) *Server {
	t.Helper()
	p, k := testbase.Conns(t)
	logs := &strings.Builder{}
	var mu sync.Mutex
	log := logging.New(lockedWriter{w: logs, mu: &mu}, slog.LevelInfo)
	app, err := server.New(env.Config(t), p, k, log)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(app.Handler)
	t.Cleanup(s.Close)
	return &Server{Server: s, App: app, Pool: p, KV: k, Logs: logs}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// Client is one caller (a browser, a phone, a TV): its own cookies and
// headers, and it never follows redirects (tests look at them).
type Client struct {
	*http.Client
	Base    string
	Headers http.Header
}

// Client makes a caller. user "" is anonymous; csrf adds the API's
// X-Requested-With header (a browser's form posts do not carry it).
func (s *Server) Client(user string, csrf bool) *Client {
	jar, _ := cookiejar.New(nil)
	h := http.Header{}
	if user != "" {
		h.Set("X-Dev-User", user)
	}
	if csrf {
		h.Set("X-Requested-With", "movienight")
	}
	return &Client{
		Client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Base:   s.URL, Headers: h,
	}
}

// Do sends one request; body may be nil, a string (sent as is) or
// url.Values (sent as a form).
func (c *Client) Do(t testing.TB, method, path string, body any, extra ...string) *Response {
	t.Helper()
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd, ctype = strings.NewReader(b), "application/json"
	case url.Values:
		rd, ctype = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	default:
		t.Fatalf("unsupported body %T", body)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range c.Headers {
		req.Header[k] = vs
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req.Header.Set(extra[i], extra[i+1])
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Trimmed: Huma ends a JSON body with a newline, which no client reads.
	return &Response{Response: resp, Body: strings.TrimSpace(string(raw))}
}

// Response is a finished response with its body read.
type Response struct {
	*http.Response
	Body string
}

// JSON decodes the body into v, failing the test when it is not JSON.
func (r *Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		t.Fatalf("%d, not JSON (%v): %s", r.StatusCode, err, r.Body)
	}
}

// Map decodes the body as a JSON object.
func (r *Response) Map(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	r.JSON(t, &m)
	return m
}

// Exec runs SQL on the test database (seeding rows), failing on error.
func (s *Server) Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

// SeedLibrary fills library_items as the Python tests' sync_library did
// from the fake Jellyfin: movie 603 ("a"×32) and show 1399 ("b"×32).
func (s *Server) SeedLibrary(t testing.TB) {
	t.Helper()
	s.Exec(t, `INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, year, date_added, synced_at) VALUES
		($1, 'movie', 603, 'Movie 603', 2000, '2026-09-01T00:00:00Z', now()),
		($2, 'tv', 1399, 'Show 1399', 2011, '2026-09-02T00:00:00Z', now())`,
		strings.Repeat("a", 32), strings.Repeat("b", 32))
}

// Explain is Postgres's plan for a query (EXPLAIN, without running it), so
// a test can check that a hot query can use its index. Sequential scans
// are switched off for the plan: a test database holds a handful of rows,
// where a scan is always cheapest, so without this the plan says nothing
// about the indexes. With it, a "Seq Scan" left in the plan means no index
// could serve that table at all.
func (s *Server) Explain(t testing.TB, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
