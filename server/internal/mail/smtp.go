package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"sort"
	"strconv"
	"time"
)

// SMTP sends over SMTP: port 465 is TLS from the first byte; any other port
// is plain, upgraded with STARTTLS unless SMTP_STARTTLS is off; it signs in
// when SMTP_USER is set.
type SMTP struct {
	host, user, password, sender string
	port                         int
	startTLS                     bool
	timeout                      time.Duration
	// tlsConfig is nil outside tests (the system's roots, the host's name).
	tlsConfig *tls.Config
}

// NewSMTP makes the SMTP mailer; timeout bounds each step.
func NewSMTP(host string, port int, user, password, sender string, startTLS bool, timeout time.Duration) *SMTP {
	return &SMTP{host: host, port: port, user: user, password: password, sender: sender, startTLS: startTLS, timeout: timeout}
}

// Enabled reports whether SMTP_HOST is set.
func (s *SMTP) Enabled() bool { return s.host != "" }

// Send delivers one email. How each failure is classed (package comment):
// everything up to the server's 354 answer to DATA is a clear failure (the
// server cannot have the email yet); once the body is being written, a
// broken connection or a timeout before the final answer is MaybeSent, and
// a coded answer (554) is a clear refusal; after the 250, nothing can undo
// the send (a failing QUIT is logged by nobody and changes nothing).
func (s *SMTP) Send(ctx context.Context, to, subject, html, text string, headers map[string]string) (string, error) {
	if !s.Enabled() {
		return "", ErrNotSetUp
	}
	id := newMessageID(s.sender)
	msg, err := buildMessage(s.sender, to, subject, html, text, id, headers)
	if err != nil {
		return "", err
	}
	from, err := addressOf(s.sender)
	if err != nil {
		return "", fmt.Errorf("SMTP_FROM %q: %w", s.sender, err)
	}

	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	dialer := &net.Dialer{Timeout: s.timeout}
	var conn net.Conn
	tlsCfg := s.tlsConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	}
	if s.port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return "", fmt.Errorf("connecting to the mail server: %w", err)
	}
	defer conn.Close()
	// Every step gets the full timeout (the Python's smtplib timeout was
	// per socket operation too); ctx ending stops the conversation at once.
	step := func() { _ = conn.SetDeadline(time.Now().Add(s.timeout)) }
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	step()
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return "", fmt.Errorf("the mail server's greeting: %w", err)
	}
	step()
	if err := c.Hello("localhost"); err != nil {
		return "", err
	}
	if s.port != 465 && s.startTLS {
		step()
		if err := c.StartTLS(tlsCfg); err != nil {
			return "", fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if s.user != "" {
		step()
		// net/smtp's PLAIN refuses to send a password over a connection that
		// is neither TLS nor to localhost; that refusal stands (the Python
		// would have sent it in the clear).
		if err := c.Auth(smtp.PlainAuth("", s.user, s.password, s.host)); err != nil {
			return "", fmt.Errorf("signing in to the mail server: %w", err)
		}
	}
	step()
	if err := c.Mail(from); err != nil {
		return "", err
	}
	step()
	if err := c.Rcpt(to); err != nil {
		return "", err
	}
	step()
	w, err := c.Data()
	if err != nil {
		return "", err
	}
	// From here the email is going to the server.
	step()
	if _, err := w.Write(msg); err != nil {
		return "", &MaybeSent{Why: "the connection broke while the email was being handed over (" + err.Error() + ")"}
	}
	step()
	if err := w.Close(); err != nil {
		var coded *textproto.Error
		if errors.As(err, &coded) {
			return "", fmt.Errorf("the mail server refused the email: %w", err)
		}
		return "", &MaybeSent{Why: "the connection broke after the email was handed over (" + err.Error() + ")"}
	}
	// Accepted. QUIT's answer, or the connection closing, changes nothing.
	step()
	_ = c.Quit()
	return id, nil
}

// parseAddress reads "Name <a@b>" or "a@b" (RFC 5322).
func parseAddress(s string) (string, error) {
	a, err := netmail.ParseAddress(s)
	if err != nil {
		return "", err
	}
	return a.Address, nil
}

// encodeAddress writes a From address with its name encoded when it is not
// plain ASCII ("Movie Night <mn@example.org>" stays as it is).
func encodeAddress(s string) string {
	a, err := netmail.ParseAddress(s)
	if err != nil {
		return s
	}
	return a.String()
}

// addressOf is the bare address of "Name <a@b>" (or of "a@b").
func addressOf(sender string) (string, error) {
	if a, err := parseAddress(sender); err == nil {
		return a, nil
	}
	return "", errors.New("not an email address")
}

// buildMessage writes the email: headers, then a multipart/alternative body
// with the text part first (mail apps show the last part they can, the
// HTML), both quoted-printable so long lines and non-ASCII survive any relay.
func buildMessage(from, to, subject, html, text, id string, extra map[string]string) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=utf-8", text}, {"text/html; charset=utf-8", html},
	} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", part.ctype)
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		pw, err := mw.CreatePart(h)
		if err != nil {
			return nil, err
		}
		qw := quotedprintable.NewWriter(pw)
		if _, err := qw.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := qw.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	header := func(k, v string) { msg.WriteString(k + ": " + v + "\r\n") }
	header("From", encodeAddress(from))
	header("To", to)
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", id)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the same email the same way every time
	for _, k := range keys {
		header(k, extra[k])
	}
	header("MIME-Version", "1.0")
	header("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}
