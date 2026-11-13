// Package auth answers "who is this request?" (app/api/deps.py and the token
// checks of app/api/v1/scrobble.py). Nothing but these signs a request in:
//
//   - a phone's device token (Authorization: Bearer mn_app_...), for the API;
//   - a browser session (the mn_session cookie, internal/sessions);
//   - a dev user (AUTH_DEV_ALLOWED, tests and local development only);
//
// and for the scrobble endpoints, which other servers call:
//
//   - a person's own scrobble token or a device token (TV or phone), sent as
//     Bearer or X-MovieNight-Token;
//   - the Jellyfin plugin's shared secret (JELLYFIN_WEBHOOK_SECRET).
//
// No identity header is believed (the forward-auth proxy path was removed
// in the Python on 2026-10-16 and stays gone).
//
// Resolution is lazy: Middleware only notes the request; a handler that
// needs the person calls User (or Scrobbler), which costs one query, and the
// answer is kept for the rest of the request.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/apierr"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/devices"
	"movienight/server/internal/logging"
	"movienight/server/internal/sessions"
	"movienight/server/internal/store"
	"movienight/server/internal/users"
)

// Via says how a request was signed in.
type Via string

const (
	ViaApp     Via = "app"     // a phone's device token
	ViaSession Via = "session" // a browser session
	ViaDev     Via = "dev"     // AUTH_DEV_ALLOWED
)

// Identity is a signed-in request.
type Identity struct {
	User store.User
	Via  Via
	// SessionHash is the browser session's id hash (ViaSession only), for
	// signing this browser out or keeping it in after a password change.
	SessionHash string
}

// Authenticator resolves identities.
type Authenticator struct {
	cfg      *config.Config
	q        *store.Queries
	users    *users.Service
	devices  *devices.Service
	sessions *sessions.Store
}

// New wires the authenticator.
func New(cfg *config.Config, q *store.Queries, u *users.Service, d *devices.Service, s *sessions.Store) *Authenticator {
	return &Authenticator{cfg: cfg, q: q, users: u, devices: d, sessions: s}
}

type state struct {
	r        *http.Request
	resolved bool
	id       *Identity
	err      error
}

type stateKey struct{}

// Middleware notes each request so User can resolve it later.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &state{}
		r = r.WithContext(context.WithValue(r.Context(), stateKey{}, st))
		st.r = r
		next.ServeHTTP(w, r)
	})
}

func stateOf(ctx context.Context) *state {
	st, _ := ctx.Value(stateKey{}).(*state)
	return st
}

// errNotSignedIn is the plain 401.
func errNotSignedIn() *apierr.Error { return apierr.Unauthorized("Not signed in") }

// User returns the signed-in person, or a 401 *apierr.Error saying why.
func (a *Authenticator) User(ctx context.Context) (*Identity, error) {
	st := stateOf(ctx)
	if st == nil {
		return nil, errors.New("auth.Middleware is not installed")
	}
	if !st.resolved {
		st.id, st.err = a.resolve(ctx, st.r)
		st.resolved = true
		if st.id != nil {
			logging.SetUser(ctx, int64(st.id.User.ID)) // the access line names people by id only
		}
	}
	return st.id, st.err
}

// Optional is User without the 401: (nil, nil) for nobody.
func (a *Authenticator) Optional(ctx context.Context) (*Identity, error) {
	id, err := a.User(ctx)
	var ae *apierr.Error
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		return nil, nil
	}
	return id, err
}

func (a *Authenticator) resolve(ctx context.Context, r *http.Request) (*Identity, error) {
	if token, ok := bearer(r); ok {
		// The phone app: its device token, which must be a phone's (a TV's
		// token may only report plays, through Scrobbler).
		u, err := a.devices.UserForToken(ctx, token, devices.ScopeApp)
		if err != nil {
			return nil, err // a 401 saying the sign-in ran out, or a database error
		}
		if u == nil {
			return nil, apierr.Unauthorized("This device was signed out; sign in again")
		}
		seen, err := a.users.Seen(ctx, *u, "", "")
		if err != nil {
			return nil, err
		}
		return &Identity{User: seen, Via: ViaApp}, nil
	}
	sess, err := a.sessions.Resolve(ctx, r)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		seen, err := a.users.Seen(ctx, sess.User, "", "")
		if err != nil {
			return nil, err
		}
		return &Identity{User: seen, Via: ViaSession, SessionHash: sess.IDHash}, nil
	}
	if username := a.devUsername(r); username != "" {
		u, err := a.devUser(ctx, username)
		if err != nil {
			return nil, err
		}
		return &Identity{User: u, Via: ViaDev}, nil
	}
	return nil, errNotSignedIn()
}

// devUsername is the dev sign-in's user: X-Dev-User, else AUTH_DEV_USER,
// only while AUTH_DEV_ALLOWED is on. (The Python's shell also counted
// AUTH_DEV_USER as signed in with AUTH_DEV_ALLOWED off; it no longer does.)
func (a *Authenticator) devUsername(r *http.Request) string {
	if !a.cfg.AuthDevAllowed {
		return ""
	}
	if name := users.CleanUsername(r.Header.Get("X-Dev-User")); name != "" {
		return name
	}
	return users.CleanUsername(a.cfg.AuthDevUser)
}

func (a *Authenticator) devUser(ctx context.Context, username string) (store.User, error) {
	if main, ok := a.cfg.UserAliases[username]; ok {
		// A second account of the same person: it acts as the main one.
		return a.users.GetOrCreate(ctx, main, "", "")
	}
	return a.users.GetOrCreate(ctx, username, username+"@example.org", username)
}

// HasIdentity reports cheaply-but-truly whether a browser request is signed
// in (the app shell decides between the page and the sign-in redirect).
// Unlike the Python, which only looked for a session key in the cookie, it
// checks the session is valid, so an expired one goes to sign-in instead of
// loading a shell whose every call then fails.
func (a *Authenticator) HasIdentity(ctx context.Context) (bool, error) {
	id, err := a.Optional(ctx)
	return id != nil, err
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:]), true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Server-to-server: scrobbling and the Jellyfin plugin
// ---------------------------------------------------------------------------

// scrobbleToken is the token a scrobble call carries: Bearer, else the
// X-MovieNight-Token header. Never the URL.
func scrobbleToken(r *http.Request) (string, error) {
	got, ok := bearer(r)
	if !ok {
		got = strings.TrimSpace(r.Header.Get("X-MovieNight-Token"))
	}
	if got == "" || len(got) > devices.MaxTokenLength {
		return "", apierr.Unauthorized("Bad or missing token")
	}
	return got, nil
}

// Scrobbler returns the person behind a scrobble call: their own scrobble
// token (users.scrobble_token_hash), or a TV's or phone's device token.
// The TV shows the 401's words as they are.
func (a *Authenticator) Scrobbler(ctx context.Context) (*store.User, error) {
	st := stateOf(ctx)
	if st == nil {
		return nil, errors.New("auth.Middleware is not installed")
	}
	token, err := scrobbleToken(st.r)
	if err != nil {
		return nil, err
	}
	hash := crypto.TokenHash(token)
	u, err := a.q.GetUserByScrobbleTokenHash(ctx, &hash)
	if err == nil {
		logging.SetUser(ctx, int64(u.ID))
		return &u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	dev, err := a.devices.UserForToken(ctx, token, devices.ScopeScrobble, devices.ScopeApp)
	if err != nil {
		return nil, err
	}
	if dev == nil {
		return nil, apierr.Unauthorized("This device was signed out of Movie Night; sign in again")
	}
	logging.SetUser(ctx, int64(dev.ID))
	return dev, nil
}

// JellyfinSecret checks the shared secret of the Jellyfin webhook and the
// Movie Night plugin for Jellyfin. Unset: 503, the documented "not set up"
// answer (the Python's), so a plugin pointed here by mistake says so plainly.
func (a *Authenticator) JellyfinSecret(ctx context.Context) error {
	if a.cfg.JellyfinWebhookSecret == "" {
		return apierr.New(http.StatusServiceUnavailable, "The Jellyfin webhook isn't set up (JELLYFIN_WEBHOOK_SECRET)")
	}
	st := stateOf(ctx)
	if st == nil {
		return errors.New("auth.Middleware is not installed")
	}
	token, err := scrobbleToken(st.r)
	if err != nil {
		return err
	}
	if !crypto.Same(token, a.cfg.JellyfinWebhookSecret) {
		return apierr.Unauthorized("Bad or missing token")
	}
	return nil
}

// RequireAdmin is the 403 for install-wide actions.
func RequireAdmin(id *Identity) error {
	if !id.User.IsAdmin {
		return apierr.New(http.StatusForbidden, "Only the install's admins can do that")
	}
	return nil
}
