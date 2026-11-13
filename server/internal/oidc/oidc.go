// Package oidc is sign-in with the install's OIDC provider (the OIDC_*
// settings; app/api/v1/auth_oidc.py). Register this exact redirect URL on
// the provider: {PUBLIC_URL}/api/v1/auth/oidc/callback (or OIDC_REDIRECT_URL).
//
// The flow is the authorization code flow with PKCE (S256), a state and a
// nonce:
//
//  1. Login makes a random state, nonce and PKCE verifier and keeps them,
//     with where to land afterwards (`next`), in Valkey for 10 minutes under
//     the state's hash. A random "binder" goes into a short-lived cookie
//     scoped to the OIDC paths, and its hash into the same record.
//  2. The provider sends the browser back with the code and the state.
//     Callback takes the record (GETDEL: one use), checks the binder cookie
//     against it, swaps the code (with the verifier) for tokens, verifies the
//     ID token's signature, issuer, audience, expiry and nonce, and returns
//     the person's claims.
//
// The binder stops login CSRF: without it, an attacker could start a flow,
// stop at the callback, and send a victim that callback link, signing the
// victim into the attacker's account. Only the browser that started a flow
// can finish it.
//
// The Python (authlib) kept the state, nonce and verifier in its signed
// session cookie. Keeping them server-side in Valkey works the same on every
// replica and leaves nothing to replay in the cookie.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/valkey-io/valkey-go"
	"golang.org/x/oauth2"

	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/users"
)

// CallbackPath and LoginPath are the routes (under the API, as in the Python).
const (
	LoginPath    = "/api/v1/auth/oidc/login"
	CallbackPath = "/api/v1/auth/oidc/callback"
	cookiePath   = "/api/v1/auth/oidc/"
	binderCookie = "mn_oidc"
	// FlowTTL: how long a started sign-in may take at the provider.
	FlowTTL = 10 * time.Minute
)

// Failed is a sign-in that did not work, worded for the person. The reason
// for the log is kept apart (Why) and never shown.
type Failed struct {
	Msg string
	Why string
}

func (e *Failed) Error() string { return e.Msg + " (" + e.Why + ")" }

func failed(why string) *Failed { return &Failed{Msg: "Signing in didn't work. Try again.", Why: why} }

// ErrNotConfigured: OIDC sign-in is off on this install.
var ErrNotConfigured = errors.New("OIDC sign-in is not configured")

// Flow runs the sign-in.
type Flow struct {
	cfg    *config.Config
	kv     valkey.Client
	client *http.Client
	log    *slog.Logger

	mu       sync.Mutex
	provider *gooidc.Provider
}

// New makes the flow. client is used for every call to the provider
// (discovery, keys, the token exchange); it carries the upstream timeouts.
func New(cfg *config.Config, kv valkey.Client, client *http.Client, log *slog.Logger) *Flow {
	return &Flow{cfg: cfg, kv: kv, client: client, log: log}
}

// providerFor discovers the provider once (its /.well-known document) and
// keeps it. Discovery is lazy, not at startup: a provider that is down must
// not stop the server (passwords and tokens still work); a failed discovery
// is retried on the next sign-in.
func (f *Flow) providerFor(ctx context.Context) (*gooidc.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.provider != nil {
		return f.provider, nil
	}
	dctx, cancel := context.WithTimeout(gooidc.ClientContext(ctx, f.client), 15*time.Second)
	defer cancel()
	p, err := gooidc.NewProvider(dctx, f.cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	f.provider = p
	return p, nil
}

func (f *Flow) oauth(p *gooidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     f.cfg.OIDCClientID,
		ClientSecret: f.cfg.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  f.cfg.OIDCRedirectURL,
		Scopes:       f.cfg.OIDCScopes,
	}
}

type record struct {
	Nonce      string `json:"nonce"`
	Verifier   string `json:"verifier"`
	Next       string `json:"next"`
	BinderHash string `json:"binder"`
}

func flowKey(state string) string { return "oidc:flow:" + crypto.TokenHash(state) }

// Login starts a sign-in and returns the provider's authorize URL. next is
// where to land afterwards (already made local by the caller).
func (f *Flow) Login(ctx context.Context, w http.ResponseWriter, next string) (string, error) {
	if !f.cfg.OIDCEnabled() {
		return "", ErrNotConfigured
	}
	p, err := f.providerFor(ctx)
	if err != nil {
		return "", fmt.Errorf("discovering the OIDC provider: %w", err)
	}
	state, nonce, binder := crypto.NewToken(24), crypto.NewToken(24), crypto.NewToken(24)
	verifier := oauth2.GenerateVerifier()
	body, _ := json.Marshal(record{Nonce: nonce, Verifier: verifier, Next: next, BinderHash: crypto.TokenHash(binder)})
	if err := f.kv.Do(ctx, f.kv.B().Set().Key(flowKey(state)).Value(string(body)).Px(FlowTTL).Build()).Error(); err != nil {
		return "", fmt.Errorf("keeping the sign-in state: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: binderCookie, Value: binder, Path: cookiePath, MaxAge: int(FlowTTL.Seconds()),
		// Lax, not Strict: the provider's redirect back is a top-level
		// cross-site navigation, which Strict would strip the cookie from.
		HttpOnly: true, Secure: f.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return f.oauth(p).AuthCodeURL(state, gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Result is a finished sign-in.
type Result struct {
	Claims users.OIDCClaims
	Next   string // from the flow record; "" when it had none
}

// Callback finishes a sign-in from the provider's redirect.
func (f *Flow) Callback(ctx context.Context, w http.ResponseWriter, r *http.Request) (*Result, error) {
	if !f.cfg.OIDCEnabled() {
		return nil, ErrNotConfigured
	}
	// The binder cookie has done its job either way.
	http.SetCookie(w, &http.Cookie{Name: binderCookie, Value: "", Path: cookiePath, MaxAge: -1,
		HttpOnly: true, Secure: f.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return nil, failed("the provider answered error=" + truncate(e, 60))
	}
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" || len(state) > 200 || len(code) > 2000 {
		return nil, failed("no state or code")
	}
	raw, err := f.kv.Do(ctx, f.kv.B().Getdel().Key(flowKey(state)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, &Failed{Msg: "That sign-in took too long or was already used. Try again.", Why: "unknown or used state"}
	}
	if err != nil {
		return nil, fmt.Errorf("reading the sign-in state: %w", err)
	}
	var rec record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("reading the sign-in state: %w", err)
	}
	binder, err := r.Cookie(binderCookie)
	if err != nil || !crypto.Same(crypto.TokenHash(binder.Value), rec.BinderHash) {
		return nil, failed("binder cookie missing or different: not the browser that started")
	}

	p, err := f.providerFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovering the OIDC provider: %w", err)
	}
	xctx, cancel := context.WithTimeout(gooidc.ClientContext(ctx, f.client), 20*time.Second)
	defer cancel()
	tok, err := f.oauth(p).Exchange(xctx, code, oauth2.VerifierOption(rec.Verifier))
	if err != nil {
		return nil, failed("code exchange: " + oauthReason(err))
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, failed("no id_token in the token answer")
	}
	idt, err := p.Verifier(&gooidc.Config{ClientID: f.cfg.OIDCClientID}).Verify(xctx, rawID)
	if err != nil {
		return nil, failed("id_token did not verify: " + err.Error())
	}
	if !crypto.Same(idt.Nonce, rec.Nonce) {
		return nil, failed("nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, failed("unreadable claims")
	}
	c := users.OIDCClaims{
		Subject: strings.TrimSpace(idt.Subject),
		Email:   claimString(claims, "email"),
		Name:    claimString(claims, "name"),
	}
	if c.Name == "" {
		c.Name = claimString(claims, "preferred_username")
	}
	if f.cfg.OIDCUsernameClaim != "" {
		c.UsernameHint = claimString(claims, f.cfg.OIDCUsernameClaim)
	}
	if c.UsernameHint == "" {
		c.UsernameHint = claimString(claims, "preferred_username")
	}
	if c.Subject == "" {
		return nil, &Failed{Msg: "The sign-in provider sent no subject.", Why: "empty sub"}
	}
	return &Result{Claims: c, Next: rec.Next}, nil
}

func claimString(claims map[string]any, name string) string {
	v, _ := claims[name].(string)
	return strings.TrimSpace(v)
}

// oauthReason keeps the provider's error code, never its full body (which
// could echo the code or the client secret).
func oauthReason(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return fmt.Sprintf("HTTP %d %s", re.Response.StatusCode, truncate(re.ErrorCode, 60))
	}
	return truncate(err.Error(), 120)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
