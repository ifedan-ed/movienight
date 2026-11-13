// Package users finds and creates people, and keeps what the sign-in tells
// about them current.
package users

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/config"
	"movienight/server/internal/store"
)

// SeenEvery: last_seen_at is written at most this often, so an active
// person does not cost a database write on every request (docs/limits.md).
const SeenEvery = 10 * time.Minute

// MaxUsernameLength is the users.username column's length.
const MaxUsernameLength = 200

// Service is the people store.
type Service struct {
	q   *store.Queries
	cfg *config.Config
	now func() time.Time
}

// New makes the service. now is time.Now outside tests.
func New(q *store.Queries, cfg *config.Config) *Service {
	return &Service{q: q, cfg: cfg, now: time.Now}
}

// ErrOIDCAccountTaken: the sign-in names a username whose account is
// already linked to a different OIDC subject.
var ErrOIDCAccountTaken = errors.New("that username belongs to another sign-in account; ask the person who runs this Movie Night")

// CleanUsername lower-cases and trims a username, or returns "" when it is
// empty, too long or holds control characters.
func CleanUsername(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || len(name) > MaxUsernameLength {
		return ""
	}
	for _, r := range name {
		if r < 32 || r == unicode.ReplacementChar {
			return ""
		}
	}
	return name
}

// ByID returns a person, or pgx.ErrNoRows.
func (s *Service) ByID(ctx context.Context, id int32) (store.User, error) {
	return s.q.GetUserByID(ctx, id)
}

// ByUsername returns a person by their (clean) username, or pgx.ErrNoRows.
func (s *Service) ByUsername(ctx context.Context, username string) (store.User, error) {
	return s.q.GetUserByUsername(ctx, username)
}

// GetOrCreate returns the person with this username, creating them on their
// first visit (the dev sign-in path). email and name, when given, update
// what is kept.
func (s *Service) GetOrCreate(ctx context.Context, username, email, name string) (store.User, error) {
	u, err := s.q.GetUserByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.q.CreateUser(ctx, store.CreateUserParams{
			Username: username, Email: ptr(email), DisplayName: ptr(name), IsAdmin: s.cfg.IsAdmin(username),
		})
		if errors.Is(err, pgx.ErrNoRows) { // two first requests at once: the other made it
			return s.q.GetUserByUsername(ctx, username)
		}
		return u, err
	}
	if err != nil {
		return u, err
	}
	return s.Seen(ctx, u, email, name)
}

// Seen brings a known person's row up to date on a visit: the email and
// name the sign-in reported (when given), admin rights (ADMIN_USERS and
// OWNER_USERNAME, followed both ways: someone taken off loses them on their
// next request) and last_seen_at (at most every SeenEvery). It writes only
// when something changed.
func (s *Service) Seen(ctx context.Context, u store.User, email, name string) (store.User, error) {
	now := s.now()
	newEmail, newName, admin := u.Email, u.DisplayName, s.cfg.IsAdmin(u.Username)
	changed := false
	if email != "" && (u.Email == nil || *u.Email != email) {
		newEmail, changed = &email, true
	}
	if name != "" && (u.DisplayName == nil || *u.DisplayName != name) {
		newName, changed = &name, true
	}
	if admin != u.IsAdmin {
		changed = true
	}
	seen := u.LastSeenAt
	if seen == nil || now.Sub(*seen) > SeenEvery {
		seen, changed = &now, true
	}
	if !changed {
		return u, nil
	}
	return s.q.UpdateUserSeen(ctx, store.UpdateUserSeenParams{
		ID: u.ID, Email: newEmail, DisplayName: newName, IsAdmin: admin, LastSeenAt: seen,
	})
}

// OIDCClaims is what a sign-in at the provider tells about the person.
type OIDCClaims struct {
	Subject      string
	Email        string
	Name         string
	UsernameHint string // the configured claim, or preferred_username
}

// SignInOIDC returns the person behind an OIDC sign-in, matched by the
// provider's subject ("sub"), so a changed email or username keeps the same
// account. A first sign-in creates the person, or links an existing account
// of the same username that has no OIDC subject yet (one made with a
// password, or before OIDC).
//
// Fixed from the Python: it also re-linked an account already linked to a
// DIFFERENT subject, just because the username matched. Anyone the provider
// let pick a preferred_username of "alice" would then have taken over
// alice's Movie Night. Such a sign-in is now refused (ErrOIDCAccountTaken);
// an admin can clear oidc_sub if the provider account really changed.
func (s *Service) SignInOIDC(ctx context.Context, c OIDCClaims) (store.User, error) {
	if c.Subject == "" {
		return store.User{}, errors.New("an OIDC subject is required")
	}
	u, err := s.q.GetUserByOIDCSub(ctx, &c.Subject)
	if err == nil {
		return s.Seen(ctx, u, c.Email, c.Name)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return u, err
	}
	username := CleanUsername(c.UsernameHint)
	if username == "" {
		local, _, _ := strings.Cut(c.Email, "@")
		username = CleanUsername(local)
	}
	if username == "" {
		sub := c.Subject
		if len(sub) > 24 {
			sub = sub[:24]
		}
		username = "user-" + strings.ToLower(sub)
	}
	existing, err := s.q.GetUserByUsername(ctx, username)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		u, err := s.q.CreateUser(ctx, store.CreateUserParams{
			Username: username, Email: ptr(c.Email), DisplayName: ptr(c.Name),
			IsAdmin: s.cfg.IsAdmin(username), OidcSub: &c.Subject,
		})
		if errors.Is(err, pgx.ErrNoRows) { // a racing first sign-in won
			return s.q.GetUserByOIDCSub(ctx, &c.Subject)
		}
		return u, err
	case err != nil:
		return existing, err
	}
	if existing.OidcSub != nil { // linked to another subject: never re-link
		return store.User{}, ErrOIDCAccountTaken
	}
	u, err = s.q.LinkOIDCSub(ctx, store.LinkOIDCSubParams{
		ID: existing.ID, OidcSub: &c.Subject, Email: ptr(c.Email), DisplayName: ptr(c.Name),
	})
	if errors.Is(err, pgx.ErrNoRows) { // linked by a racing sign-in in between
		return s.q.GetUserByOIDCSub(ctx, &c.Subject)
	}
	return u, err
}

// SetPassword stores a new password hash (nil removes the password) and
// raises session_epoch, which signs out every browser session made before.
// Validation and hashing happen in package passwords.
func (s *Service) SetPassword(ctx context.Context, id int32, hash *string) (store.User, error) {
	return s.q.SetPassword(ctx, store.SetPasswordParams{ID: id, PasswordHash: hash})
}

// DisplayName is the name to greet someone by: their display name, else
// their username.
func DisplayName(u store.User) string {
	if u.DisplayName != nil && *u.DisplayName != "" {
		return *u.DisplayName
	}
	return u.Username
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
