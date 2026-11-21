package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactCutsQueriesEmailsAndRowValues(t *testing.T) {
	tmdb := "Client error '401 Unauthorized' for url 'https://api.themoviedb.org/3/search/multi?query=kids+treasure+map&api_key=SECRETKEY'"
	out := Redact(tmdb)
	if strings.Contains(out, "treasure") || strings.Contains(out, "SECRETKEY") || !strings.Contains(out, "https://api.themoviedb.org/3/search/multi?…") {
		t.Errorf("query not cut: %s", out)
	}
	if got := Redact("GET /api/v1/search?q=kids%20treasure HTTP/1.1"); got != "GET /api/v1/search?… HTTP/1.1" {
		t.Errorf("path query: %s", got)
	}
	if got := Redact("digest sent to alice@example.org"); got != "digest sent to [email]" {
		t.Errorf("email: %s", got)
	}
	if got := Redact(`duplicate key: Key (username)=(alice) already exists`); strings.Contains(got, "alice") {
		t.Errorf("key values: %s", got)
	}
	if got := Redact(`Failing row contains (1, alice, Alice Smith, null).`); strings.Contains(got, "alice") {
		t.Errorf("failing row: %s", got)
	}
	if got := Redact(`invalid input syntax for type integer: "my secret"`); strings.Contains(got, "secret") {
		t.Errorf("bad input: %s", got)
	}
	if got := Redact(`token mn_app_Ab-9_x refused; Authorization: Bearer mn_kodi_zz9`); got != `token mn_app_… refused; Authorization: Bearer …` {
		t.Errorf("tokens: %s", got)
	}
}

func TestEveryLineIsRedactedAndCarriesTheRequestID(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo)
	ctx, info := WithRequest(context.Background())
	log.InfoContext(ctx, "upstream failed", "url", "https://x.test/a?api_key=K", "err", errors.New("mail to bob@example.org failed"))
	line := buf.String()
	if strings.Contains(line, "api_key=K") || strings.Contains(line, "bob@example.org") {
		t.Errorf("not redacted: %s", line)
	}
	if !strings.Contains(line, `"rid":"`+info.RID+`"`) || len(info.RID) != 12 {
		t.Errorf("no request id: %s", line)
	}
}
