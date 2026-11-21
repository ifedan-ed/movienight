// Package crypto holds every primitive whose output is stored or exchanged,
// written to be byte-compatible with the Python app so that nothing
// already in the database or on a device has to be redone at cutover:
//
//   - password hashes "scrypt$N$r$p$salt_b64$hash_b64" (users.password_hash)
//   - Fernet tokens keyed from SECRET_KEY (users.jellyfin_token_enc)
//   - sha256-hex hashes of tokens and codes (app_tokens, app_codes, ...)
//   - the phone connect form's HMAC, and the PKCE S256 challenge
//
// testdata/python.json holds values the Python code produced; the tests check Go against them.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken returns nbytes of randomness as unpadded URL-safe base64: the
// same shape as Python's secrets.token_urlsafe(nbytes), which made every
// token and code now in the database.
func NewToken(nbytes int) string {
	b := make([]byte, nbytes)
	// crypto/rand.Read never fails on supported platforms (Go 1.24+ panics
	// internally instead of returning an error), so no error path is needed.
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenHash is how a token or code is stored: the lower-case hex sha256 of
// its text. Tokens are long random strings, so a plain (unsalted) hash is
// enough: there is nothing to guess.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Same compares two secrets in constant time (a shared secret from a request
// against the configured one).
func Same(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// PKCEChallenge is the S256 challenge of a PKCE verifier: unpadded URL-safe
// base64 of its sha256 (RFC 7636), as the phone app computes it.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Keys derives every key from SECRET_KEY once, each with its own label, so
// no two uses ever share a key.
type Keys struct {
	fernet  *Fernet
	connect []byte
	form    []byte
	stream  []byte
}

// NewKeys derives the keys. secret must not be empty (config refuses that
// earlier; this guards a caller that skipped config).
func NewKeys(secret string) (*Keys, error) {
	if secret == "" {
		return nil, fmt.Errorf("SECRET_KEY is empty")
	}
	f := sha256.Sum256([]byte("movienight:fernet:" + secret))
	return &Keys{
		fernet:  newFernet(f[:]),
		connect: []byte("movienight:connect:" + secret),
		// New in Go: the browser forms' anti-forgery token (internal/web).
		// The Python kept a random token in the signed session cookie; with
		// sessions on the server, an HMAC of a per-browser cookie does the
		// same job without storing anything.
		form: []byte("movienight:form:" + secret),
		// The in-browser player's stream tickets (internal/stream): signed
		// links to one item's media, checked without a database read.
		stream: []byte("movienight:stream:" + secret),
	}, nil
}

// Fernet is the cipher for per-person secrets at rest.
func (k *Keys) Fernet() *Fernet { return k.fernet }

// ConnectSig signs the phone connect form (/app/connect) so the POST can only
// carry what the page showed this person, within FORM_TTL. Same message and
// key as the Python app used.
func (k *Keys) ConnectSig(username, challenge, redirect, device string, ts int64) string {
	m := hmac.New(sha256.New, k.connect)
	fmt.Fprintf(m, "%s|%s|%s|%s|%d", username, challenge, redirect, device, ts)
	return hex.EncodeToString(m.Sum(nil))
}

// FormToken is the anti-forgery token a server-rendered form carries for the
// browser holding binder (the random value of its form cookie).
func (k *Keys) FormToken(binder string) string {
	m := hmac.New(sha256.New, k.form)
	m.Write([]byte(binder))
	return hex.EncodeToString(m.Sum(nil))
}

// StreamMAC is the HMAC-SHA256 of a stream ticket's claims. The key stays
// in here; package stream only ever sees MACs.
func (k *Keys) StreamMAC(msg []byte) []byte {
	m := hmac.New(sha256.New, k.stream)
	m.Write(msg)
	return m.Sum(nil)
}

// SameHex compares two hex MACs in constant time.
func SameHex(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
