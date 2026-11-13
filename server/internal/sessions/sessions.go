// Package sessions keeps browser sign-ins on the server (table web_sessions,
// migration 0017; the reasons are written there).
//
// The cookie carries a random 32-byte id; the table keeps its sha256. A
// session is good while it is unexpired AND was made under the person's
// current session_epoch: a password change or reset raises the epoch, which
// ends every older session even where its row was not deleted.
//
// Lifetime: OIDC_SESSION_DAYS from sign-in, fixed (not sliding), as
// docs/configuration.md describes it: "how long a session lasts before
// signing in again".
package sessions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/crypto"
	"movienight/server/internal/store"
)

// CookieName is the session cookie. Not the Python's "session": an old
// Starlette cookie is simply ignored, and the 4 people sign in once more.
const CookieName = "mn_session"

// touchEvery: last_seen_at is written at most this often per session.
const touchEvery = 10 * time.Minute

// Store creates, resolves and ends sessions.
type Store struct {
	q        *store.Queries
	lifetime time.Duration
	secure   bool
	now      func() time.Time
}

// New makes the store. lifetime is OIDC_SESSION_DAYS; secure marks the
// cookie Secure (an https PUBLIC_URL).
func New(q *store.Queries, lifetime time.Duration, secure bool) *Store {
	return &Store{q: q, lifetime: lifetime, secure: secure, now: time.Now}
}

// Session is a resolved, valid session.
type Session struct {
	IDHash string
	User   store.User
}

// Create starts a session for u and sets its cookie on w. Expired sessions
// of anyone are deleted on the way (cheap, indexed), so the table does not
// grow without the worker's clean-up.
func (s *Store) Create(ctx context.Context, w http.ResponseWriter, u store.User) (string, error) {
	if _, err := s.q.DeleteExpiredWebSessions(ctx); err != nil {
		return "", err
	}
	id := crypto.NewToken(32)
	hash := crypto.TokenHash(id)
	expires := s.now().Add(s.lifetime)
	err := s.q.CreateWebSession(ctx, store.CreateWebSessionParams{
		IDHash: hash, UserID: u.ID, Epoch: u.SessionEpoch, ExpiresAt: expires,
	})
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: id, Path: "/", Expires: expires, MaxAge: int(s.lifetime.Seconds()),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	return hash, nil
}

// Resolve returns the valid session behind a request's cookie, or nil.
// A session made under an older epoch is deleted and reported as nil
// (signed out). Errors are database errors only.
func (s *Store) Resolve(ctx context.Context, r *http.Request) (*Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" || len(c.Value) > 100 {
		return nil, nil
	}
	hash := crypto.TokenHash(c.Value)
	row, err := s.q.SessionUser(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.WebSession.Epoch != row.User.SessionEpoch {
		return nil, s.q.DeleteWebSession(ctx, hash)
	}
	if s.now().Sub(row.WebSession.LastSeenAt) > touchEvery {
		if err := s.q.TouchWebSession(ctx, hash); err != nil {
			return nil, err
		}
	}
	return &Session{IDHash: hash, User: row.User}, nil
}

// End deletes a session and clears its cookie.
func (s *Store) End(ctx context.Context, w http.ResponseWriter, idHash string) error {
	s.ClearCookie(w)
	if idHash == "" {
		return nil
	}
	return s.q.DeleteWebSession(ctx, idHash)
}

// ClearCookie removes the session cookie from the browser.
func (s *Store) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

// KeepOnlyThis is for after a password change: this browser's session takes
// the new epoch and every other session of the person is deleted.
func (s *Store) KeepOnlyThis(ctx context.Context, idHash string, u store.User) error {
	if err := s.q.SetWebSessionEpoch(ctx, store.SetWebSessionEpochParams{IDHash: idHash, Epoch: u.SessionEpoch}); err != nil {
		return err
	}
	return s.q.DeleteOtherWebSessions(ctx, store.DeleteOtherWebSessionsParams{UserID: u.ID, IDHash: idHash})
}
