// Package devices signs in the phone app and TV apps.
//
// Phones: the app cannot use the browser's session, so it opens /app/connect
// in a browser; the person confirms; the page hands the app a one-time code
// (5 minutes, one use) on its own address (movienight://auth?code=...); the
// app swaps the code for its own token, proving with its PKCE verifier that
// it is the app that asked. Where PASSWORD_SIGNIN is on, the app may instead
// send a username and password (POST /api/v1/auth/password) and get the same
// token (Issue). The token ("mn_app_...") opens the whole API.
//
// TVs (Kodi): the TV shows a short code (ABCD-2345, 10 minutes) and polls
// with its own secret; the person types the code on the web; the TV's next
// poll collects a token ("mn_kodi_...") that may only report what was
// watched.
//
// Every token and code is stored as its sha256 only. A device token stops
// working after DEVICE_IDLE_DAYS unused or DEVICE_MAX_DAYS after it signed
// in, whichever comes first: the sign-in provider never sees these tokens,
// so a lost phone, or someone removed there, must not keep access for good.
package devices

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/store"
)

// Token prefixes: a phone's token opens the whole API; a TV's only reports.
const (
	AppPrefix      = "mn_app_"
	ScrobblePrefix = "mn_kodi_"
	ScopeApp       = "app"
	ScopeScrobble  = "scrobble"
	// MaxTokenLength: a longer "token" is refused before any lookup.
	MaxTokenLength = 200
)

// Lifetimes (docs/limits.md, "Sign-in, phones and TVs").
const (
	CodeTTL = 5 * time.Minute  // a phone's one-time code
	FormTTL = 10 * time.Minute // a connect page stays valid
	PairTTL = 10 * time.Minute // a TV's code
	// PollInterval is how often a TV polls (seconds), sent to it.
	PollInterval = 3
	// touchEvery: "last used" is written at most this often, so a busy
	// device does not cost a database write per request.
	touchEvery = 10 * time.Minute
)

// PairAlphabet leaves out 0/O and 1/I/L, which people misread on a TV.
const PairAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

var (
	// ChallengeRe: a PKCE S256 challenge (43 chars) or a plain verifier-length value.
	ChallengeRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)
	appRedirect  = regexp.MustCompile(`^movienight://[A-Za-z0-9/_-]*$`)
	expoRedirect = regexp.MustCompile(`^exp://[A-Za-z0-9.:_-]+(/--)?/[A-Za-z0-9/_-]*$`)
	notCodeChar  = regexp.MustCompile(`[^A-Z0-9]`)
)

// Service is the device sign-in store.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	cfg  *config.Config
	keys *crypto.Keys
	now  func() time.Time
}

// New makes the service.
func New(pool *pgxpool.Pool, cfg *config.Config, keys *crypto.Keys) *Service {
	return &Service{pool: pool, q: store.New(pool), cfg: cfg, keys: keys, now: time.Now}
}

// RedirectOK: the app's sign-in may only return to the app itself (and Expo
// Go while developing, APP_DEV_REDIRECTS).
func (s *Service) RedirectOK(url string) bool {
	if appRedirect.MatchString(url) {
		return true
	}
	return s.cfg.AppDevRedirects && expoRedirect.MatchString(url)
}

// Expiry is when a device's sign-in runs out, and why then: "unused" (that
// many days without use) or "age" (that many days after it signed in).
func (s *Service) Expiry(t store.AppToken) (time.Time, string) {
	used := t.CreatedAt
	if t.LastUsedAt != nil {
		used = *t.LastUsedAt
	}
	idleEnd := used.Add(days(s.cfg.DeviceIdleDays))
	ageEnd := t.CreatedAt.Add(days(s.cfg.DeviceMaxDays))
	if !idleEnd.After(ageEnd) {
		return idleEnd, "unused"
	}
	return ageEnd, "age"
}

func (s *Service) expiredMessage(t store.AppToken) string {
	what := "This phone"
	if t.Scope == ScopeScrobble {
		what = "This TV"
	}
	if _, why := s.Expiry(t); why == "unused" {
		return fmt.Sprintf("%s's sign-in ran out: it wasn't used for %d days. Sign in again.", what, s.cfg.DeviceIdleDays)
	}
	return fmt.Sprintf("%s's sign-in ran out: it signed in %d days ago. Sign in again.", what, s.cfg.DeviceMaxDays)
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// ---------------------------------------------------------------------------
// Phones
// ---------------------------------------------------------------------------

// FormSig signs what the connect page shows, so its POST can carry nothing
// else (and only within FormTTL).
func (s *Service) FormSig(username, challenge, redirect, device string, ts int64) string {
	return s.keys.ConnectSig(username, challenge, redirect, device, ts)
}

// CheckForm verifies a connect form's POST.
func (s *Service) CheckForm(username, challenge, redirect, device string, ts int64, sig string) *apierr.Error {
	if !ChallengeRe.MatchString(challenge) || !s.RedirectOK(redirect) {
		return apierr.New(http.StatusBadRequest, "This sign-in link isn't valid")
	}
	age := s.now().Unix() - ts
	if age < 0 {
		age = -age
	}
	if age > int64(FormTTL.Seconds()) || !crypto.SameHex(sig, s.FormSig(username, challenge, redirect, device, ts)) {
		return apierr.New(http.StatusBadRequest, "This sign-in page has expired; start again from the app")
	}
	return nil
}

// MakeCode creates the one-time code the connect page hands the app.
func (s *Service) MakeCode(ctx context.Context, userID int32, challenge, device string) (string, error) {
	if err := s.q.DeleteExpiredAppCodes(ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(device) == "" {
		device = "Movie Night app"
	}
	code := crypto.NewToken(32)
	err := s.q.CreateAppCode(ctx, store.CreateAppCodeParams{
		CodeHash: crypto.TokenHash(code), UserID: userID, Challenge: challenge, Device: device,
		ExpiresAt: s.now().Add(CodeTTL),
	})
	return code, err
}

// Redeemed is a phone's new sign-in.
type Redeemed struct {
	Token    string
	Username string
	DeviceID int32
}

// Redeem swaps a code and its PKCE verifier for a device token. The code is
// consumed and the token made in one transaction.
func (s *Service) Redeem(ctx context.Context, code, verifier string) (*Redeemed, error) {
	codeHash := crypto.TokenHash(code)
	challenge := crypto.PKCEChallenge(verifier)
	var out *Redeemed
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.TakeAppCode(ctx, store.TakeAppCodeParams{CodeHash: codeHash, Challenge: challenge})
		if errors.Is(err, pgx.ErrNoRows) {
			// Why not: gone or expired, or the wrong app's verifier.
			kept, err := q.GetAppCode(ctx, codeHash)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !kept.ExpiresAt.After(s.now())) {
				return apierr.New(http.StatusBadRequest, "That sign-in code expired; sign in again")
			}
			if err != nil {
				return err
			}
			return apierr.New(http.StatusBadRequest, "That sign-in code was meant for another app")
		}
		if err != nil {
			return err
		}
		token, dev, err := createAppToken(ctx, q, row.UserID, row.Device)
		if err != nil {
			return err
		}
		u, err := q.GetUserByID(ctx, row.UserID)
		if err != nil {
			return err
		}
		out = &Redeemed{Token: token, Username: u.Username, DeviceID: dev.ID}
		return nil
	})
	return out, err
}

// Issue gives a phone that proved who it is some other way (its username and
// password, checked by the caller: POST /api/v1/auth/password) the same kind
// of device token the connect flow ends with, so everything after sign-in
// (expiry, Signed-in devices, signing out) is one path for both.
func (s *Service) Issue(ctx context.Context, u store.User, device string) (*Redeemed, error) {
	token, dev, err := createAppToken(ctx, s.q, u.ID, device)
	if err != nil {
		return nil, err
	}
	return &Redeemed{Token: token, Username: u.Username, DeviceID: dev.ID}, nil
}

// createAppToken makes a phone's token; only its sha256 is stored.
func createAppToken(ctx context.Context, q *store.Queries, userID int32, device string) (string, store.AppToken, error) {
	token := AppPrefix + crypto.NewToken(32)
	dev, err := q.CreateAppToken(ctx, store.CreateAppTokenParams{
		UserID: userID, Name: device, TokenHash: crypto.TokenHash(token), Scope: ScopeApp,
	})
	return token, dev, err
}

// UserForToken returns the person behind a device token of one of scopes,
// or (nil, nil) when it is not one (unknown, wrong scope, not a device
// token at all). An expired token is deleted and answers 401 saying why.
func (s *Service) UserForToken(ctx context.Context, token string, scopes ...string) (*store.User, error) {
	if !(strings.HasPrefix(token, AppPrefix) || strings.HasPrefix(token, ScrobblePrefix)) || len(token) > MaxTokenLength {
		return nil, nil
	}
	dev, err := s.q.GetAppTokenByHash(ctx, crypto.TokenHash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, sc := range scopes {
		allowed = allowed || dev.Scope == sc
	}
	if !allowed {
		return nil, nil
	}
	now := s.now()
	if end, _ := s.Expiry(dev); !now.Before(end) {
		msg := s.expiredMessage(dev)
		if err := s.q.DeleteAppToken(ctx, dev.ID); err != nil {
			return nil, err
		}
		return nil, apierr.Unauthorized(msg)
	}
	if dev.LastUsedAt == nil || now.Sub(*dev.LastUsedAt) > touchEvery {
		if err := s.q.TouchAppToken(ctx, dev.ID); err != nil {
			return nil, err
		}
	}
	u, err := s.q.GetUserByID(ctx, dev.UserID)
	if err != nil {
		return nil, err // the row is CASCADE-deleted with its person, so this is a real error
	}
	return &u, nil
}

// List returns a person's live devices, newest first, forgetting expired
// ones first.
func (s *Service) List(ctx context.Context, userID int32) ([]store.AppToken, error) {
	now := s.now()
	err := s.q.DropExpiredAppTokens(ctx, store.DropExpiredAppTokensParams{
		UserID: userID, CreatedBefore: now.Add(-days(s.cfg.DeviceMaxDays)), UsedBefore: now.Add(-days(s.cfg.DeviceIdleDays)),
	})
	if err != nil {
		return nil, err
	}
	return s.q.ListUserAppTokens(ctx, userID)
}

// SignOut signs one of the person's own devices out; false when there is no
// such device of theirs.
func (s *Service) SignOut(ctx context.Context, userID, deviceID int32) (bool, error) {
	n, err := s.q.DeleteUserAppToken(ctx, store.DeleteUserAppTokenParams{ID: deviceID, UserID: userID})
	return n > 0, err
}

// SignOutEverywhere is an admin's action: every phone and TV of one person.
func (s *Service) SignOutEverywhere(ctx context.Context, userID int32) (int64, error) {
	return s.q.DeleteAllUserAppTokens(ctx, userID)
}

// ---------------------------------------------------------------------------
// TVs
// ---------------------------------------------------------------------------

// NormalizeCode keeps only letters and digits, upper-cased: people type
// "abcd 2345" or "ABCD-2345" for the same code.
func NormalizeCode(code string) string {
	return notCodeChar.ReplaceAllString(strings.ToUpper(code), "")
}

// NewPairCode returns a fresh code as shown on the TV ("ABCD-2345").
func NewPairCode() string {
	var b [8]byte
	max := big.NewInt(int64(len(PairAlphabet)))
	for i := range b {
		n, _ := rand.Int(rand.Reader, max)
		b[i] = PairAlphabet[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// StartPair makes a new code for a TV and the secret it polls with. The
// per-address limit is the caller's (internal/api); here is the install-wide
// cap on codes waiting at once (PAIR_CODES_WAITING). Count and insert are not
// one statement, so two replicas can each let one more through at the very
// edge: a soft cap, which is all it needs to be.
func (s *Service) StartPair(ctx context.Context, device string) (code, poll string, err error) {
	if err := s.q.DeleteExpiredPairCodes(ctx); err != nil {
		return "", "", err
	}
	n, err := s.q.CountPairCodes(ctx)
	if err != nil {
		return "", "", err
	}
	if n >= int64(s.cfg.PairCodesWaiting) {
		return "", "", apierr.New(http.StatusTooManyRequests, "Too many sign-ins are waiting; try again in a few minutes")
	}
	device = strings.TrimSpace(device)
	if device == "" {
		device = "Kodi"
	}
	code, poll = NewPairCode(), crypto.NewToken(32)
	err = s.q.CreatePairCode(ctx, store.CreatePairCodeParams{
		CodeHash: crypto.TokenHash(NormalizeCode(code)), PollHash: crypto.TokenHash(poll),
		Device: device, ExpiresAt: s.now().Add(PairTTL),
	})
	return code, poll, err
}

// ConfirmPair: the person typed the TV's code. Returns the device's name.
//
// Changed from the Python: the token is no longer made here and kept
// (Fernet-encrypted) in pair_codes.token_enc until the TV collects it. It is
// made when the TV collects (CollectPair), so a code confirmed but never
// collected leaves no phantom "signed-in" TV behind, and no token ever sits
// in the database in any form but its hash. token_enc stays in the schema,
// unused, until a later migration drops it.
func (s *Service) ConfirmPair(ctx context.Context, userID int32, code string) (string, error) {
	codeHash := crypto.TokenHash(NormalizeCode(code))
	device, err := s.q.ConfirmPairCode(ctx, store.ConfirmPairCodeParams{CodeHash: codeHash, UserID: &userID})
	if err == nil {
		return device, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	row, err := s.q.GetPairCode(ctx, codeHash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !row.ExpiresAt.After(s.now())) {
		return "", apierr.New(http.StatusNotFound, "That code isn't right or has run out; check the TV")
	}
	if err != nil {
		return "", err
	}
	return "", apierr.New(http.StatusConflict, "That code was already used")
}

// Collected is what a TV gets once its code is confirmed.
type Collected struct {
	Token       string
	Username    string
	DisplayName string
}

// CollectPair is the TV polling: (nil, nil) while waiting; the token once
// confirmed, handed over exactly once (the row goes in the same transaction
// that makes the token); 410 when the code is gone or expired.
func (s *Service) CollectPair(ctx context.Context, poll string) (*Collected, error) {
	pollHash := crypto.TokenHash(poll)
	var out *Collected
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.CollectPairCode(ctx, pollHash)
		if errors.Is(err, pgx.ErrNoRows) {
			waiting, err := q.GetPairCodeByPoll(ctx, pollHash)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !waiting.ExpiresAt.After(s.now())) {
				return apierr.New(http.StatusGone, "This code ran out; start again on the TV")
			}
			return err // nil: still waiting
		}
		if err != nil {
			return err
		}
		if row.UserID == nil { // excluded by the query; a guard, not a path
			return apierr.New(http.StatusGone, "This code ran out; start again on the TV")
		}
		u, err := q.GetUserByID(ctx, *row.UserID)
		if err != nil {
			return err
		}
		token := ScrobblePrefix + crypto.NewToken(32)
		if _, err := q.CreateAppToken(ctx, store.CreateAppTokenParams{
			UserID: u.ID, Name: row.Device, TokenHash: crypto.TokenHash(token), Scope: ScopeScrobble,
		}); err != nil {
			return err
		}
		name := u.Username
		if u.DisplayName != nil && *u.DisplayName != "" {
			name = *u.DisplayName
		}
		out = &Collected{Token: token, Username: u.Username, DisplayName: name}
		return nil
	})
	return out, err
}
