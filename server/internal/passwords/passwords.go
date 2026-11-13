// Package passwords is sign-in with a username and password (PASSWORD_SIGNIN):
// hashing on a bounded pool, checking, and the
// lockout against guessing.
package passwords

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/crypto"
	"movienight/server/internal/ratelimit"
	"movienight/server/internal/store"
	"movienight/server/internal/users"
)

// MaxLength: a longer password is refused (setting) or never checked
// (signing in), so a huge one cannot be used to load the server.
const MaxLength = 200

// Lockout: after FailLimit wrong passwords in FailWindow for one username
// from one caller address, that username is refused to that address until
// the window has passed. Counting per username alone let anyone lock anyone
// out with five wrong guesses; counting per (username, address) leaves the
// real person, at their own address, unaffected.
const (
	FailLimit  = 5
	FailWindow = 15 * time.Minute
)

// Workers bounds how many scrypt computations run at once in one process.
// Each takes ~0.1 s of CPU and 32 MiB of memory, so unbounded goroutines
// under a burst of sign-in attempts would mean unbounded memory: 4 at once
// caps it at 128 MiB and still clears 40 checks a second. Callers wait their
// turn (bounded by their request's context).
const Workers = 4

// Hasher runs scrypt on a fixed pool of slots.
type Hasher struct {
	slots chan struct{}
	dummy func() string
}

// NewHasher makes a pool of Workers slots.
func NewHasher() *Hasher {
	h := &Hasher{slots: make(chan struct{}, Workers)}
	// Checked against when a username does not exist or has no password, so
	// a wrong username takes as long as a wrong password (no telling which
	// usernames exist from the timing). Made once, on first use.
	h.dummy = sync.OnceValue(func() string {
		v, _ := crypto.HashPassword(crypto.NewToken(16))
		return v
	})
	return h
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash hashes a new password.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return crypto.HashPassword(password)
}

// Check reports whether password matches stored (nil: compare against the
// dummy, and report false).
func (h *Hasher) Check(ctx context.Context, password string, stored *string) (bool, error) {
	target := ""
	if stored != nil {
		target = *stored
	} else {
		target = h.dummy()
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	return crypto.CheckPassword(password, target) && stored != nil, nil
}

// Error is a password that cannot be set, in words a person can act on.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// ErrTooManyTries: this username is locked out for this caller just now.
var ErrTooManyTries = errors.New("too many wrong passwords")

// Service checks and sets passwords.
type Service struct {
	hasher    *Hasher
	users     *users.Service
	fails     *ratelimit.Window
	minLength int
}

// NewService wires the hasher, the people store and the lockout counter
// (a ratelimit.Window of FailLimit per FailWindow).
func NewService(h *Hasher, u *users.Service, fails *ratelimit.Window, minLength int) *Service {
	return &Service{hasher: h, users: u, fails: fails, minLength: minLength}
}

// Validate refuses a password too short or too long to set.
func (s *Service) Validate(password string) error {
	n := len([]rune(password))
	if n < s.minLength {
		return &Error{fmt.Sprintf("Use at least %d characters.", s.minLength)}
	}
	if n > MaxLength {
		return &Error{fmt.Sprintf("Use at most %d characters.", MaxLength)}
	}
	return nil
}

// Authenticate returns the person whose username and password these are,
// or (nil, nil) for a wrong pair. ErrTooManyTries while locked out. Any
// other error (Valkey or Postgres down) is returned as is: sign-in then
// fails closed rather than skipping the lockout.
func (s *Service) Authenticate(ctx context.Context, rawUsername, password, caller string) (*store.User, error) {
	username := users.CleanUsername(rawUsername)
	key := username + "\x00" + caller
	blocked, err := s.fails.Blocked(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("lockout check: %w", err)
	}
	if blocked {
		return nil, ErrTooManyTries
	}
	var u *store.User
	if username != "" {
		found, err := s.users.ByUsername(ctx, username)
		switch {
		case err == nil:
			u = &found
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
	}
	var stored *string
	if u != nil {
		stored = u.PasswordHash
	}
	ok := false
	if len([]rune(password)) <= MaxLength {
		if ok, err = s.hasher.Check(ctx, password, stored); err != nil {
			return nil, err
		}
	}
	if !ok {
		if err := s.fails.Add(ctx, key); err != nil {
			return nil, fmt.Errorf("lockout count: %w", err)
		}
		return nil, nil
	}
	if err := s.fails.Reset(ctx, key); err != nil {
		return nil, fmt.Errorf("lockout reset: %w", err)
	}
	return u, nil
}

// Matches reports whether password is the person's current one (the
// account page asks before a change).
func (s *Service) Matches(ctx context.Context, u store.User, password string) (bool, error) {
	if len([]rune(password)) > MaxLength {
		return false, nil
	}
	return s.hasher.Check(ctx, password, u.PasswordHash)
}

// Set validates and stores a new password, raising the session epoch (every
// browser session made before is signed out; the caller re-stamps the one
// that made the change).
func (s *Service) Set(ctx context.Context, u store.User, password string) (store.User, error) {
	if err := s.Validate(password); err != nil {
		return u, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return u, err
	}
	return s.users.SetPassword(ctx, u.ID, &hash)
}
