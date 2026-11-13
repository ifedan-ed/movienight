package mail_test

// The mailers tell three endings apart: sent, a clear failure (safe to try
// again) and a send whose fate is unknown because the connection broke after
// the email was handed over (MaybeSent: counted as sent, never repeated).
// Tested against a real SMTP
// conversation on loopback and a stand-in for SMTP2GO's API.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/config"
	"movienight/server/internal/mail"
	"movienight/server/internal/mail/mailtest"
)

func smtpMailer(port int) *mail.SMTP {
	return mail.NewSMTP("127.0.0.1", port, "", "", "Movie Night <mn@example.org>", false, time.Second)
}

func send(m mail.Mailer) (string, error) {
	return m.Send(context.Background(), "alice@example.org", "Your week · 3 films", "<p>Hi</p>", "Hi",
		map[string]string{"List-Unsubscribe": "<https://mn.test/settings/emails?off=week>"})
}

func TestAPlainSMTPSend(t *testing.T) {
	srv := mailtest.Start(t, "ok")
	id, err := send(smtpMailer(srv.Port))
	if err != nil || !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.org>") {
		t.Fatalf("%q %v", id, err)
	}
	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d", len(got))
	}
	for _, want := range []string{"To: alice@example.org", "Message-ID: " + id,
		"List-Unsubscribe: <https://mn.test/settings/emails?off=week>", "text/html", "<p>Hi</p>"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the email lacks %q:\n%s", want, got[0])
		}
	}
	// A subject that is not plain ASCII is encoded, never sent raw.
	if strings.Contains(got[0], "Subject: Your week · 3 films") || !strings.Contains(got[0], "Subject: =?utf-8?") {
		t.Errorf("subject not encoded:\n%s", got[0])
	}
}

// Before: a connection lost after DATA came out as a failure, and the
// digest was sent again at every hourly catch-up.
func TestAConnectionLostAfterTheHandoverMayHaveSentIt(t *testing.T) {
	for _, mode := range []string{"drop_after_data", "quiet_after_data"} {
		t.Run(mode, func(t *testing.T) {
			srv := mailtest.Start(t, mode)
			_, err := send(smtpMailer(srv.Port))
			if !mail.IsMaybeSent(err) {
				t.Fatalf("want MaybeSent, got %v", err)
			}
			if len(srv.Received()) != 1 {
				t.Fatal("the server did not get the email")
			}
		})
	}
}

// A bad answer to QUIT after the 250 is still a send.
func TestABadQuitAfterTheEmailWasAcceptedIsASend(t *testing.T) {
	srv := mailtest.Start(t, "bad_quit")
	if _, err := send(smtpMailer(srv.Port)); err != nil {
		t.Fatal(err)
	}
}

// The server answered with an error code: nothing was accepted, so it may
// be tried again.
func TestARefusalIsAClearFailure(t *testing.T) {
	for _, mode := range []string{"refuse_rcpt", "refuse_data"} {
		t.Run(mode, func(t *testing.T) {
			_, err := send(smtpMailer(mailtest.Start(t, mode).Port))
			if err == nil || mail.IsMaybeSent(err) {
				t.Fatalf("want a clear failure, got %v", err)
			}
		})
	}
}

func TestNoServerIsAClearFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens there
	_, err := send(smtpMailer(port))
	if err == nil || mail.IsMaybeSent(err) {
		t.Fatalf("want a clear failure, got %v", err)
	}
}

func TestNotSetUpSendsNothing(t *testing.T) {
	m := mail.NewSMTP("", 587, "", "", "x <a@b>", true, time.Second)
	if _, err := send(m); m.Enabled() || !errors.Is(err, mail.ErrNotSetUp) {
		t.Fatalf("%v %v", m.Enabled(), err)
	}
}

// DRY_RUN: a copy of an install sends no email, set up or not; the send
// still succeeds so the digest is recorded once and not retried hourly.
func TestDryRunSendsNoEmail(t *testing.T) {
	srv := mailtest.Start(t, "ok")
	cfg := &config.Config{MailSender: "smtp", SMTPHost: "127.0.0.1", SMTPPort: srv.Port,
		SMTPFrom: "Movie Night <mn@example.org>", DryRun: true}
	m := mail.New(cfg)
	id, err := send(m)
	if err != nil || !m.Enabled() || !strings.Contains(id, "dry-run") || len(srv.Received()) != 0 {
		t.Fatalf("%q %v %v %d", id, err, m.Enabled(), len(srv.Received()))
	}
	cfg.DryRun = false
	if _, err := send(mail.New(cfg)); err != nil || len(srv.Received()) != 1 {
		t.Fatalf("a real send: %v %d", err, len(srv.Received()))
	}
	off := mail.New(&config.Config{MailSender: "smtp", DryRun: true})
	if _, err := send(off); off.Enabled() || !errors.Is(err, mail.ErrNotSetUp) {
		t.Fatalf("not set up under DRY_RUN: %v %v", off.Enabled(), err)
	}
}

// ---- SMTP2GO ----

func keyFile(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "smtp2go-key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestASMTP2GOSend(t *testing.T) {
	var gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Smtp2go-Api-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"data": {"succeeded": 1, "failed": 0, "email_id": "1abc"}}`))
	}))
	defer srv.Close()
	id, err := send(mail.NewSMTP2GO(keyFile(t, "api-test-key\n"), "Movie Night <mn@example.org>", srv.URL, time.Second))
	if err != nil || !strings.HasSuffix(id, "@example.org>") {
		t.Fatalf("%q %v", id, err)
	}
	if gotKey != "api-test-key" {
		t.Errorf("key %q", gotKey)
	}
	raw, _ := json.Marshal(body)
	for _, want := range []string{`"to":["alice@example.org"]`, `"sender":"Movie Night \u003cmn@example.org\u003e"`,
		`"html_body":"\u003cp\u003eHi\u003c/p\u003e"`, `"text_body":"Hi"`,
		`{"header":"List-Unsubscribe","value":"\u003chttps://mn.test/settings/emails?off=week\u003e"}`,
		`{"header":"Message-ID","value":"` + strings.NewReplacer("<", `\u003c`, ">", `\u003e`).Replace(id) + `"}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("body lacks %s: %s", want, raw)
		}
	}
}

func TestASMTP2GORefusalIsAClearFailure(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{{401, `{"data": {"error": "bad key"}}`}, {200, `{"data": {"succeeded": 0, "failed": 1, "failures": ["x"]}}`}, {503, `{}`}, {200, `not json`}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		_, err := send(mail.NewSMTP2GO(keyFile(t, "k"), "x <a@b.org>", srv.URL, time.Second))
		var refused *mail.SMTP2GOError
		if !errors.As(err, &refused) {
			t.Errorf("%d %s: want a refusal, got %v", c.status, c.body, err)
		}
		srv.Close()
	}
}

func TestNoConnectionToSMTP2GOIsAClearFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := send(mail.NewSMTP2GO(keyFile(t, "k"), "x <a@b.org>", url, time.Second))
	if err == nil || mail.IsMaybeSent(err) {
		t.Fatalf("want a clear failure, got %v", err)
	}
}

func TestNoAnswerFromSMTP2GOAfterSendingIsMaybeSent(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	_, err := send(mail.NewSMTP2GO(keyFile(t, "k"), "x <a@b.org>", srv.URL, 300*time.Millisecond))
	if !mail.IsMaybeSent(err) {
		t.Fatalf("want MaybeSent, got %v", err)
	}
}

func TestAnEmptyKeyFileSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	_, err := send(mail.NewSMTP2GO(keyFile(t, ""), "x <a@b.org>", srv.URL, time.Second))
	var refused *mail.SMTP2GOError
	if !errors.As(err, &refused) || calls != 0 {
		t.Fatalf("%v, %d calls", err, calls)
	}
}
