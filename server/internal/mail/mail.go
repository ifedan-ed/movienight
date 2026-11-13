// Package mail sends Movie Night's outgoing email (the weekly digest, the
// month in review, the year in review), over SMTP or over SMTP2GO's HTTP API
// (MAIL_SENDER), the Python's app/clients/mailer.py and smtp2go.py.
//
// A send ends one of three ways, and the digests treat each differently
// (internal/digest):
//
//   - sent (nil error): the server accepted the email. A failure after that
//     (its answer to QUIT, the connection closing) changes nothing.
//   - *MaybeSent: the connection broke after the email was handed over and
//     before the server answered. It may well have been accepted and
//     delivered, so it is counted as sent and never sent again: at most
//     once, never twice.
//   - any other error: a clear failure, nothing was accepted (no
//     connection, TLS or sign-in failed, the server refused the sender, the
//     address or the email), so trying again later is safe.
//
// DRY_RUN reaches this package too: New then returns a mailer that sends
// nothing (dryRun). DRY_RUN is the switch for running a copy of an install
// (a test install, a check before an upgrade) without anything leaving it:
// no Seerr requests, no Jellyfin played marks or sign-outs, no email. The
// Python sent email under DRY_RUN; a copy of the live database holds the
// live members' addresses and digests, so it must not.
package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"movienight/server/internal/config"
)

// Timeout bounds each step of a send (connecting, every SMTP reply, the
// SMTP2GO answer). docs/limits.md, "Caches and timeouts".
const Timeout = 30 * time.Second

// Mailer sends one email.
type Mailer interface {
	// Enabled reports whether the install has email set up at all.
	Enabled() bool
	// Send delivers one email with a plain-text and an HTML part and the
	// given extra headers (List-Unsubscribe, say). It returns the
	// Message-ID it gave the email. See the package comment for what the
	// error means. Cancelling ctx mid-send turns the send into a MaybeSent
	// once the email was handed over, so callers that must know the outcome
	// (the digests) pass a context that shutdown does not cancel.
	Send(ctx context.Context, to, subject, html, text string, headers map[string]string) (messageID string, err error)
}

// MaybeSent is a send whose fate is unknown: the email had been handed to
// the server when the connection broke. Never retried.
type MaybeSent struct{ Why string }

func (e *MaybeSent) Error() string { return "the email may have been sent: " + e.Why }

// IsMaybeSent reports whether err is (or wraps) a MaybeSent.
func IsMaybeSent(err error) bool {
	var m *MaybeSent
	return errors.As(err, &m)
}

// New builds the mailer MAIL_SENDER names. Without email set up, the
// mailer's Enabled is false and Send refuses (it never pretends to send).
// Under DRY_RUN it is wrapped so that nothing is sent (dryRun).
func New(cfg *config.Config) Mailer {
	var m Mailer
	if cfg.MailSender == "smtp2go" {
		m = NewSMTP2GO(cfg.SMTP2GOAPIKeyFile, cfg.SMTPFrom, SMTP2GOURL, Timeout)
	} else {
		m = NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPFrom, cfg.SMTPStartTLS, Timeout)
	}
	if cfg.DryRun {
		return dryRun{m}
	}
	return m
}

// dryRun is DRY_RUN's mailer: it reports the real one's Enabled (so the
// digests are made and shown as usual) and sends nothing. A send succeeds
// with a made-up Message-ID, so the digest is recorded as sent once, as a
// real send would be, and the catch-up does not try it again every hour.
type dryRun struct{ real Mailer }

func (d dryRun) Enabled() bool { return d.real.Enabled() }

func (d dryRun) Send(_ context.Context, _, _, _, _ string, _ map[string]string) (string, error) {
	if !d.real.Enabled() {
		return "", ErrNotSetUp
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "<dry-run." + hex.EncodeToString(b) + "@movienight.invalid>", nil
}

// ErrNotSetUp is a send on an install without email.
var ErrNotSetUp = errors.New("email isn't set up on this install")

// senderDomain is the domain of a From address such as
// "Movie Night <mn@example.org>" ("example.org"), for Message-IDs.
func senderDomain(sender string) string {
	at := strings.LastIndex(sender, "@")
	if at < 0 {
		return "movienight.invalid"
	}
	d := strings.TrimRight(strings.TrimSpace(sender[at+1:]), ">")
	if d == "" {
		return "movienight.invalid"
	}
	return d
}

// newMessageID makes "<random@domain>": unique per email, so a mail client
// never threads two digests as one message.
func newMessageID(sender string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "<" + hex.EncodeToString(b) + "@" + senderDomain(sender) + ">"
}
