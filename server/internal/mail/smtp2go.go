package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// SMTP2GOURL is SMTP2GO's send endpoint.
const SMTP2GOURL = "https://api.smtp2go.com/v3/email/send"

// SMTP2GOError is SMTP2GO answering and not sending the email (a bad key, a
// sender it won't send for, a refused address): a clear failure.
type SMTP2GOError struct{ Msg string }

func (e *SMTP2GOError) Error() string { return "SMTP2GO: " + e.Msg }

// SMTP2GO sends through SMTP2GO's HTTP API (MAIL_SENDER=smtp2go). The API
// key is read from a file (SMTP2GO_API_KEY_FILE) at each send, never kept
// in the environment or in memory between sends, so a rotated key is used
// at once.
type SMTP2GO struct {
	keyFile, sender, url string
	http                 *http.Client
}

// NewSMTP2GO makes the mailer. url is SMTP2GOURL outside tests.
func NewSMTP2GO(keyFile, sender, url string, timeout time.Duration) *SMTP2GO {
	return &SMTP2GO{keyFile: keyFile, sender: sender, url: url, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether SMTP2GO_API_KEY_FILE is set.
func (m *SMTP2GO) Enabled() bool { return m.keyFile != "" }

func (m *SMTP2GO) key() (string, error) {
	raw, err := os.ReadFile(m.keyFile)
	if err != nil {
		return "", &SMTP2GOError{Msg: "the key file couldn't be read: " + err.Error()}
	}
	k := strings.TrimSpace(string(raw))
	if k == "" {
		return "", &SMTP2GOError{Msg: m.keyFile + " is empty"}
	}
	return k, nil
}

type customHeader struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

// Send posts the email. Which failure is which is decided by whether the
// request was written to the connection (httptrace's WroteRequest): before
// that (no connection, TLS failed) nothing reached SMTP2GO, a clear failure;
// after it, a broken connection or a timeout leaves the outcome unknown
// (MaybeSent). The Python told these apart by httpx's exception classes,
// which counted a request cut off while being written as a clear failure.
func (m *SMTP2GO) Send(ctx context.Context, to, subject, html, text string, headers map[string]string) (string, error) {
	if !m.Enabled() {
		return "", ErrNotSetUp
	}
	key, err := m.key()
	if err != nil {
		return "", err
	}
	id := newMessageID(m.sender)
	custom := []customHeader{{Header: "Message-ID", Value: id}}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		custom = append(custom, customHeader{Header: k, Value: headers[k]})
	}
	body, err := json.Marshal(map[string]any{
		"sender": m.sender, "to": []string{to}, "subject": subject, "html_body": html, "text_body": text,
		"custom_headers": custom,
	})
	if err != nil {
		return "", err
	}

	// Atomic: the transport reports the write from its own goroutine, which
	// may still be running when Do gives up on a timeout.
	var wrote atomic.Bool
	// A write that failed partway is not a handover: SMTP2GO cannot act on a
	// request whose body never arrived whole.
	trace := &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Smtp2go-Api-Key", key)
	resp, err := m.http.Do(req)
	if err != nil {
		if wrote.Load() {
			return "", &MaybeSent{Why: "no answer from SMTP2GO after the email was sent (" + err.Error() + ")"}
		}
		return "", fmt.Errorf("SMTP2GO couldn't be reached: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// The answer started and broke off: it may say "succeeded".
		return "", &MaybeSent{Why: "SMTP2GO's answer broke off (" + err.Error() + ")"}
	}
	var got struct {
		Data struct {
			Succeeded int    `json:"succeeded"`
			Error     string `json:"error"`
			Failures  any    `json:"failures"`
			EmailID   string `json:"email_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &got) // a body that isn't JSON leaves Succeeded 0: a refusal
	if resp.StatusCode != http.StatusOK || got.Data.Succeeded != 1 {
		why := got.Data.Error
		if why == "" && got.Data.Failures != nil {
			b, _ := json.Marshal(got.Data.Failures)
			why = string(b)
		}
		if why == "" {
			why = http.StatusText(resp.StatusCode)
		}
		return "", &SMTP2GOError{Msg: fmt.Sprintf("answered %d: %s", resp.StatusCode, why)}
	}
	return id, nil
}
