// Package stream is the in-browser player's media proxy: the bytes of one
// Jellyfin item (the file itself, Jellyfin's HLS playlists and segments,
// subtitle tracks and embedded fonts) passed through Movie Night, so the
// browser never holds a Jellyfin key.
//
// Every request carries a ticket in its path. A ticket is a signed,
// short-lived set of claims (Claims): whose play it is, which item and media
// source, and Jellyfin's play session. The proxy checks the signature and
// the expiry and nothing else: no session cookie, no database read, so a
// segment costs one HMAC and one upstream request. The routes that change
// state (reporting progress) are signed-in API calls that also check the
// ticket belongs to the person calling (Signer.Owned).
package stream

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TicketLife is how long a ticket opens its stream: long enough for a long
// film with pauses, short enough that a copied link soon stops working. The
// player asks for a fresh one when a ticket runs out mid-film.
const TicketLife = 6 * time.Hour

// Claims are what a ticket says. The short JSON names keep the URL short:
// every segment request carries the whole ticket.
type Claims struct {
	Member      int32  `json:"u"` // the Movie Night person the play belongs to
	Item        string `json:"i"` // Jellyfin item id
	Source      string `json:"m"` // Jellyfin media source id
	PlaySession string `json:"p"` // Jellyfin's PlaySessionId (its transcode job)
	Device      string `json:"d"` // the Jellyfin device id the play runs under
	Play        string `json:"s"` // Movie Night's play session (play_sessions.id)
	Runtime     int    `json:"r,omitempty"` // seconds; for the watched-at-the-end rule
	Credits     int    `json:"c,omitempty"` // seconds where the credits start; 0 when unknown
	Expires     int64  `json:"e"`           // Unix seconds
}

// Errors a ticket can fail with. Both mean "ask for a new one": the
// proxy answers 403 either way.
var (
	ErrInvalid = errors.New("stream ticket is not valid")
	ErrExpired = errors.New("stream ticket has expired")
	ErrMember  = errors.New("stream ticket belongs to someone else")
)

// Signer makes and checks tickets with a MAC whose key it never sees
// (crypto.Keys.StreamMAC).
type Signer struct {
	mac func([]byte) []byte
	now func() time.Time
}

// NewSigner makes a signer. now is time.Now outside tests.
func NewSigner(mac func([]byte) []byte, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{mac: mac, now: now}
}

var b64 = base64.RawURLEncoding

// Sign makes a ticket for c, valid for TicketLife from now (c.Expires is
// set here, whatever it held).
func (s *Signer) Sign(c Claims) string {
	c.Expires = s.now().Add(TicketLife).Unix()
	body, _ := json.Marshal(c) // plain strings and numbers: cannot fail
	payload := b64.EncodeToString(body)
	return payload + "." + b64.EncodeToString(s.mac([]byte(payload)))
}

// Verify checks a ticket's signature and expiry and returns its claims.
func (s *Signer) Verify(ticket string) (Claims, error) { return s.verify(ticket, 0) }

func (s *Signer) verify(ticket string, grace time.Duration) (Claims, error) {
	payload, sig, ok := strings.Cut(ticket, ".")
	if !ok || payload == "" || sig == "" {
		return Claims{}, ErrInvalid
	}
	got, err := b64.DecodeString(sig)
	// The MAC is over the encoded payload, so it is checked before the
	// payload is decoded at all: nothing unsigned is ever parsed.
	if err != nil || !hmac.Equal(got, s.mac([]byte(payload))) {
		return Claims{}, ErrInvalid
	}
	body, err := b64.DecodeString(payload)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var c Claims
	if err := json.Unmarshal(body, &c); err != nil || c.Item == "" || c.Member == 0 {
		return Claims{}, ErrInvalid
	}
	if s.now().Add(-grace).Unix() >= c.Expires {
		return Claims{}, ErrExpired
	}
	return c, nil
}

// ReportGrace is how long after its expiry a ticket still identifies its
// play to the signed-in person's own reports: a film paused overnight is
// still reported (and recorded) when it is stopped in the morning.
const ReportGrace = 48 * time.Hour

// Owned checks a ticket for the signed-in person's own calls (reports,
// switching tracks): the signature, the person, and an expiry stretched by
// ReportGrace. It opens no stream: only Verify does that.
func (s *Signer) Owned(ticket string, member int32) (Claims, error) {
	c, err := s.verify(ticket, ReportGrace)
	if err != nil {
		return Claims{}, err
	}
	if c.Member != member {
		return Claims{}, ErrMember
	}
	return c, nil
}
