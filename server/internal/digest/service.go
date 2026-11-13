// Package digest makes and sends the weekly digest, the month in review and
// the year in review.
//
// Each one is built from the person's own records for the period (what they
// watched, rated and loved, what arrived in the library, what is coming from
// their watchlist, a few picks) and the install's AI model (through LiteLLM)
// writes the words around it. Without a model it still goes out, with plain
// wording. A year is too many films to list, so its email sums the year up.
//
// Who gets them: everyone with an email address, on by default; each kind
// is switched off in Profile → Emails, or with the link at the foot of every
// digest (also its List-Unsubscribe header). The schedule never sends an
// empty one (IsEmpty).
//
// Each person's periods are their own: a week is Monday to Sunday in the
// zone their browser or phone reported (users.Location; the install's
// TIMEZONE for someone who never reported one), and their digest is due at
// RunAt on the day after it ends, there. The worker's jobs keep the
// install's clock (Monday 09:00 TIMEZONE, and the hourly catch-up); each run
// sends whatever is due by then for each person, so someone in Auckland gets
// theirs at the first run after 09:00 on their Monday, someone in Berlin at
// the first after 09:00 on theirs.
//
// Never one email twice. The Python serialised its runs with an asyncio lock
// in one process; two workers (or a catch-up beside the scheduled run in
// another process) could both find a digest unsent and send it. Here every
// send first claims the digest in Postgres (DigestClaimSend: one UPDATE that
// only one caller can win), and the claim is committed before the email
// goes, so a send that never reports back is visible and is settled as sent
// (it may have arrived) rather than sent again.
package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/mail"
	"movienight/server/internal/settings"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/users"
)

// Each kind's on/off setting and the address setting (user_settings keys,
// the Python's).
var settingKey = map[string]string{"week": "digest_weekly", "month": "digest_monthly", "year": "digest_yearly"}

const emailKey = "digest_email"

// legacyDoneKey (app_settings) is the install-wide marker the Python (and
// this server before per-person zones) kept: the start of the last period of
// each kind run for everyone. It is only read now, for a person with no
// digest_done row of their own yet (the first run after migration 0021): a
// period on or before it was done for everyone then, so it is not sent
// again.
const legacyDoneKey = "digest_done:"

// StaleClaim: a send claimed longer ago than this and still without an
// outcome was cut off (the process stopped mid-send). Well above a send's
// longest honest duration (a few SMTP steps of mail.Timeout each), so a send
// still under way in another worker is never taken for a dead one.
const StaleClaim = 10 * time.Minute

// Errors the API turns into answers (409, as the Python's RuntimeErrors).
var (
	ErrEmailOff  = apierr.New(409, "Email isn't set up on this install (SMTP_HOST)")
	ErrNoAddress = apierr.New(409, "No email address for this person")
	ErrSending   = apierr.New(409, "This digest is being sent right now; check your inbox in a minute")
	ErrNotFound  = apierr.New(404, "Not found")
)

// Service builds and sends digests.
type Service struct {
	cfg      *config.Config
	pool     *pgxpool.Pool
	q        *store.Queries
	cl       *clients.Set
	mailer   mail.Mailer
	log      *slog.Logger
	titles   *titles.Service
	settings *settings.Service
	now      func() time.Time
}

// New makes the service. cl is read at each use (cl.TMDB, cl.AI), so the
// clients can be swapped after construction (tests point them at stand-ins).
func New(cfg *config.Config, pool *pgxpool.Pool, cl *clients.Set, m mail.Mailer, log *slog.Logger) *Service {
	q := store.New(pool)
	return &Service{cfg: cfg, pool: pool, q: q, cl: cl, mailer: m, log: log,
		titles: titles.New(q, func() titles.Fetcher { return cl.TMDB }, log), settings: settings.New(q), now: time.Now}
}

// EmailEnabled reports whether the install can send digests at all.
func (s *Service) EmailEnabled() bool { return s.mailer.Enabled() }

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// zone is the person's own zone, which their periods follow.
func (s *Service) zone(u store.User) *time.Location { return users.Location(u, s.cfg.Location) }

// ---------------------------------------------------------------------------
// The person's settings
// ---------------------------------------------------------------------------

// Wants reports whether the person gets this kind: on unless switched off.
func (s *Service) Wants(ctx context.Context, userID int32, kind string) (bool, error) {
	return s.settings.UserBool(ctx, userID, settingKey[kind], true)
}

// Recipient is where the person's digests go: the address they set, else
// the one from their sign-in; "" when there is none.
func (s *Service) Recipient(ctx context.Context, u store.User) (string, error) {
	a, err := s.settings.UserString(ctx, u.ID, emailKey)
	if err != nil || a != "" {
		return a, err
	}
	if u.Email != nil {
		return *u.Email, nil
	}
	return "", nil
}

// Settings is the person's email settings (Profile → Emails).
type Settings struct {
	Weekly, Monthly, Yearly bool
	Address                 *string
}

// GetSettings reads them.
func (s *Service) GetSettings(ctx context.Context, u store.User) (Settings, error) {
	var out Settings
	var err error
	for kind, dst := range map[string]*bool{"week": &out.Weekly, "month": &out.Monthly, "year": &out.Yearly} {
		if *dst, err = s.Wants(ctx, u.ID, kind); err != nil {
			return out, err
		}
	}
	a, err := s.Recipient(ctx, u)
	if a != "" {
		out.Address = &a
	}
	return out, err
}

// emailRe is a digest address a person types: something@something, no
// spaces (the Python's).
var emailRe = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,255}$`)

// Change is a settings update; nil fields stay as they are, an empty
// address goes back to the sign-in's.
type Change struct {
	Weekly, Monthly, Yearly *bool
	Address                 *string
}

// SetSettings applies a change (a bad address: 422, nothing changed).
func (s *Service) SetSettings(ctx context.Context, u store.User, c Change) (Settings, error) {
	var address string
	if c.Address != nil {
		address = strings.TrimSpace(*c.Address)
		if address != "" && !emailRe.MatchString(address) {
			return Settings{}, apierr.New(422, "That doesn't look like an email address")
		}
	}
	for kind, v := range map[string]*bool{"week": c.Weekly, "month": c.Monthly, "year": c.Yearly} {
		if v != nil {
			if err := s.settings.SetUser(ctx, u.ID, settingKey[kind], *v); err != nil {
				return Settings{}, err
			}
		}
	}
	if c.Address != nil {
		var err error
		if address != "" {
			err = s.settings.SetUser(ctx, u.ID, emailKey, address)
		} else {
			err = s.settings.DeleteUser(ctx, u.ID, emailKey)
		}
		if err != nil {
			return Settings{}, err
		}
	}
	return s.GetSettings(ctx, u)
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// Get is one of the person's digests (ErrNotFound for anyone else's).
func (s *Service) Get(ctx context.Context, userID, id int32) (store.Digest, error) {
	d, err := s.q.DigestGet(ctx, store.DigestGetParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return d, ErrNotFound
	}
	return d, err
}

// Build is this period's digest of kind for the person, made now if it
// doesn't exist yet (built once per person and period, then kept for good).
// skipEmpty (the schedule): nil for an empty one, which is then neither
// written (no model call) nor kept.
func (s *Service) Build(ctx context.Context, u store.User, kind string, skipEmpty bool) (*store.Digest, error) {
	start, end := LastPeriod(kind, s.now(), s.zone(u))
	return s.build(ctx, u, kind, start, end, skipEmpty)
}

func (s *Service) build(ctx context.Context, u store.User, kind string, start, end time.Time, skipEmpty bool) (*store.Digest, error) {
	existing, err := s.q.DigestByPeriod(ctx, store.DigestByPeriodParams{UserID: u.ID, Kind: kind, PeriodStart: start})
	switch {
	case err == nil:
		if skipEmpty {
			var st stored
			if json.Unmarshal([]byte(existing.DataJson), &st) == nil && IsEmpty(st.Data) {
				return nil, nil
			}
		}
		return &existing, nil
	case !isNoRows(err):
		return nil, err
	}
	data, err := s.Gather(ctx, u, kind, start, end)
	if err != nil {
		return nil, err
	}
	if skipEmpty && IsEmpty(data) {
		return nil, nil
	}
	words, model := s.writeWords(ctx, data)
	page, text := s.Render(data, words)
	raw, err := json.Marshal(stored{Data: data, Words: words, Text: text})
	if err != nil {
		return nil, err
	}
	d, err := s.q.DigestInsert(ctx, store.DigestInsertParams{UserID: u.ID, Kind: kind, PeriodStart: start, PeriodEnd: end,
		Subject: words.Subject, Html: page, DataJson: string(raw), Model: model})
	if isNoRows(err) {
		// Another run wrote it meanwhile: theirs is the one kept and sent.
		d, err = s.q.DigestByPeriod(ctx, store.DigestByPeriodParams{UserID: u.ID, Kind: kind, PeriodStart: start})
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ---------------------------------------------------------------------------
// Send
// ---------------------------------------------------------------------------

// errClaimed: the schedule could not claim a digest (another send of it is
// under way, or it no longer needs sending). Not a failure of this run's
// own, but the run is not done until that other send has an outcome.
var errClaimed = errors.New("another send of this digest is under way")

// Send emails the digest to the person (to "", their Recipient) and returns
// the address. scheduled: the schedule's own try, which needs the digest
// unsent and under DIGEST_SEND_TRIES and counts the try; the person's "Send
// it to me now" always may (and is not counted), but never while another
// send of the same digest is under way.
//
// A send whose fate is unknown (mail.MaybeSent) is recorded as sent, then
// returned as the error: it is never sent again by the schedule.
func (s *Service) Send(ctx context.Context, u store.User, d store.Digest, to string, scheduled bool) (string, error) {
	if !s.mailer.Enabled() {
		return "", ErrEmailOff
	}
	address := to
	if address == "" {
		var err error
		if address, err = s.Recipient(ctx, u); err != nil {
			return "", err
		}
	}
	if address == "" {
		return "", ErrNoAddress
	}
	var st stored
	_ = json.Unmarshal([]byte(d.DataJson), &st)
	text := st.Text
	if text == "" {
		text = d.Subject
	}

	claim, err := s.q.DigestClaimSend(ctx, store.DigestClaimSendParams{ID: d.ID, Scheduled: scheduled, MaxTries: int32(s.cfg.DigestSendTries)})
	if isNoRows(err) {
		if scheduled {
			return "", errClaimed
		}
		return "", ErrSending
	}
	if err != nil {
		return "", err
	}
	// The outcome must be written whatever happens to ctx (a shutdown in
	// the middle of a send): an unrecorded outcome is settled as "sent"
	// only after StaleClaim, and a clear failure would be lost.
	keep := context.WithoutCancel(ctx)
	_, err = s.mailer.Send(keep, address, d.Subject, d.Html, text,
		// Mail apps show an "Unsubscribe" button for this header.
		map[string]string{"List-Unsubscribe": "<" + s.UnsubscribeURL(d.Kind) + ">"})
	switch {
	case mail.IsMaybeSent(err):
		// The connection broke after the email was handed over: it may have
		// arrived, so it is never sent again (at most once, not twice).
		if rerr := s.q.DigestMarkSent(keep, store.DigestMarkSentParams{ID: d.ID, SendingSince: claim.SendingSince, SentTo: &address}); rerr != nil {
			return "", errors.Join(err, rerr)
		}
		s.log.WarnContext(ctx, "the mail server didn't answer after it was handed the email; counted as sent, not sent again",
			"kind", d.Kind, "digest", d.ID, "user", u.ID, "err", err)
		return address, err
	case err != nil:
		// A clear failure: nothing was accepted, so a later try is safe.
		if rerr := s.q.DigestReleaseClaim(keep, store.DigestReleaseClaimParams{ID: d.ID, SendingSince: claim.SendingSince}); rerr != nil {
			return "", errors.Join(err, rerr)
		}
		return "", err
	}
	if err := s.q.DigestMarkSent(keep, store.DigestMarkSentParams{ID: d.ID, SendingSince: claim.SendingSince, SentTo: &address}); err != nil {
		return "", fmt.Errorf("the email was sent, recording it failed: %w", err)
	}
	s.log.InfoContext(ctx, "digest sent", "kind", d.Kind, "digest", d.ID, "user", u.ID) // never the address
	return address, nil
}

// ---------------------------------------------------------------------------
// The schedule
// ---------------------------------------------------------------------------

// RunScheduled is the scheduled run of kind (Monday 09:00 and so on, in the
// install's TIMEZONE): it builds and sends, to everyone whose latest period
// of kind is due by now in their own zone and not done (pending), that
// period's digest, unless it would be empty. It returns how many were sent;
// the error is non-nil when any digest couldn't be built or sent (or another
// send of one is still under way), and that person's period is then left not
// done, so the hourly catch-up tries again. One person failing never stops
// the others.
func (s *Service) RunScheduled(ctx context.Context, kind string) (int, error) {
	return s.runDue(ctx, kind, s.now())
}

// CatchUp runs every kind for everyone whose latest period is due at now and
// not done: every hour, so a person whose zone is ahead of or behind the
// install's gets theirs within the hour after it is due for them, a run
// missed while no worker ran is made, and a send that failed is tried again
// once the failure clears. Never one digest twice (digest_done, sent_at, and
// the send claim). The error joins the failed kinds' errors.
func (s *Service) CatchUp(ctx context.Context, now time.Time) error {
	var errs []error
	for _, kind := range Kinds {
		n, err := s.runDue(ctx, kind, now)
		if n > 0 {
			s.log.InfoContext(ctx, "digests sent by the hourly run", "kind", kind, "sent", n)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// duePeriod is one person's period of a kind that is due and not done.
type duePeriod struct {
	userID     int32
	start, end time.Time // local midnights in the person's zone
}

// pending is everyone whose latest period of kind is due at now and not done
// yet, each with that period in their own zone.
//
// "Due": now is at or after RunAt on the day after the period ends, in the
// person's zone. Only the latest period counts: once a newer one has ended,
// a digest of the one before that never went out stays unsent.
//
// "Done", for a person with a digest_done row: the period starts on or
// before the date it holds. Without one (nobody has had a run since
// migration 0021, or they are new): done when it was due before they had an
// account (a new person is not sent the week before they joined), or when
// it is on or before the install-wide marker the Python left
// (legacyDoneKey), so switching over sends nothing twice.
func (s *Service) pending(ctx context.Context, kind string, now time.Time) ([]duePeriod, error) {
	people, err := s.q.DigestPeople(ctx, kind)
	if err != nil {
		return nil, err
	}
	legacy, err := s.legacyDone(ctx, kind)
	if err != nil {
		return nil, err
	}
	var out []duePeriod
	for _, p := range people {
		loc := users.LocationOf(p.TimeZone, s.cfg.Location)
		start, dueAt := Due(kind, now, loc)
		if now.Before(dueAt) {
			continue
		}
		day := start.Format(time.DateOnly) // the period's first day, in their zone
		done := p.Done
		if done == "" {
			if !dueAt.After(p.CreatedAt) {
				continue
			}
			done = legacy
		}
		if done != "" && day <= done { // YYYY-MM-DD compares as dates
			continue
		}
		_, end := LastPeriod(kind, now, loc)
		out = append(out, duePeriod{userID: p.ID, start: start, end: end})
	}
	return out, nil
}

// Missed is the kinds with anyone whose latest period is due at now and not
// done (pending): what the next run would send or try.
func (s *Service) Missed(ctx context.Context, now time.Time) ([]string, error) {
	var out []string
	for _, kind := range Kinds {
		due, err := s.pending(ctx, kind, now)
		if err != nil {
			return nil, err
		}
		if len(due) > 0 {
			out = append(out, kind)
		}
	}
	return out, nil
}

// runDue builds and sends each pending person's digest of kind and records
// their period done unless it failed or another send of it is under way.
func (s *Service) runDue(ctx context.Context, kind string, now time.Time) (int, error) {
	if !ValidKind(kind) {
		return 0, fmt.Errorf("no such digest kind %q", kind)
	}
	due, err := s.pending(ctx, kind, now)
	if err != nil {
		return 0, err
	}
	sent, failed, busy := 0, 0, 0
	for _, p := range due {
		if err := ctx.Err(); err != nil {
			return sent, err // shutting down: the next hourly run finishes these
		}
		ok, err := s.runOne(ctx, p.userID, kind, p.start, p.end)
		switch {
		case errors.Is(err, errClaimed):
			busy++
			continue
		case err != nil:
			failed++
			s.log.ErrorContext(ctx, "digest couldn't be built or sent", "kind", kind, "user", p.userID, "err", err)
			continue
		case ok:
			sent++
		}
		// Sent, nothing to send (empty, switched off, no address) or given
		// up on after DIGEST_SEND_TRIES: this person's period is done.
		if err := s.MarkDone(ctx, p.userID, kind, p.start); err != nil {
			return sent, err
		}
	}
	if failed > 0 || busy > 0 {
		return sent, fmt.Errorf("%s digests: %d couldn't be built or sent and %d are being sent elsewhere; tried again at the next hourly catch-up",
			kind, failed, busy)
	}
	return sent, nil
}

// runOne is one person's digest of the run: true when it was sent now.
func (s *Service) runOne(ctx context.Context, id int32, kind string, start, end time.Time) (bool, error) {
	u, err := s.q.GetUserByID(ctx, id)
	if isNoRows(err) {
		return false, nil // gone since the list was read
	}
	if err != nil {
		return false, err
	}
	wants, err := s.Wants(ctx, id, kind)
	if err != nil || !wants {
		return false, err
	}
	to, err := s.Recipient(ctx, u)
	if err != nil || to == "" {
		return false, err
	}
	d, err := s.build(ctx, u, kind, start, end, true)
	if err != nil {
		return false, err
	}
	if d == nil {
		s.log.InfoContext(ctx, "digest not sent: nothing watched or new", "kind", kind, "user", id)
		return false, nil
	}
	if d.SentAt != nil {
		return false, nil
	}
	if d.SendingSince != nil {
		return false, s.settle(ctx, u, *d, to)
	}
	tries := int32(s.cfg.DigestSendTries)
	if d.SendTries >= tries {
		return false, nil // given up on for this period (logged when it happened)
	}
	_, err = s.Send(ctx, u, *d, to, true)
	switch {
	case err == nil, mail.IsMaybeSent(err):
		return true, nil // a MaybeSent is recorded as sent, and logged, by Send
	case errors.Is(err, errClaimed):
		// Lost the claim: another run sent it meanwhile (then all is well),
		// or is sending it now (then this run is not done yet).
		now, rerr := s.q.DigestByPeriod(ctx, store.DigestByPeriodParams{UserID: id, Kind: kind, PeriodStart: start})
		if rerr == nil && now.SentAt != nil {
			return false, nil
		}
		return false, err
	case d.SendTries+1 >= tries:
		// That was the last try: give up for the period, loudly, so the run
		// can be done. "Send it to me now" in Profile still works.
		s.log.ErrorContext(ctx, fmt.Sprintf("digest not sent after %d tries; not tried again for this period", tries),
			"kind", kind, "digest", d.ID, "user", id, "err", err)
		return false, nil
	}
	return false, err
}

// settle handles a digest whose send was claimed and has no outcome: older
// than StaleClaim, it was cut off and is taken as sent (it may have gone
// out); newer, another worker is sending it now, and this run is not done
// until it finishes.
func (s *Service) settle(ctx context.Context, u store.User, d store.Digest, to string) error {
	// Real elapsed time, not the schedule's clock: the claim was stamped by
	// Postgres's now().
	before := time.Now().Add(-StaleClaim)
	got, err := s.q.DigestSettleStale(ctx, store.DigestSettleStaleParams{ID: d.ID, SentTo: &to, Before: &before})
	if isNoRows(err) {
		return errClaimed
	}
	if err != nil {
		return err
	}
	s.log.WarnContext(ctx, "a digest send started and never reported back (the worker stopped midway?); counted as sent, not sent again",
		"kind", d.Kind, "digest", d.ID, "user", u.ID, "started", got.SentAt)
	return nil
}

// legacyDone is the install-wide marker's period date (legacyDoneKey), ""
// when there is none.
func (s *Service) legacyDone(ctx context.Context, kind string) (string, error) {
	raw, err := s.q.GetAppSetting(ctx, legacyDoneKey+kind)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		// Unreadable: as if there were none (sent_at still keeps anything
		// already sent from going again).
		s.log.WarnContext(ctx, "the old digest run marker is unreadable; not using it", "kind", kind, "value", raw)
		return "", nil
	}
	// The date as it was written, in the offset it was written with: the
	// install's local midnight that started the period.
	return t.Format(time.DateOnly), nil
}

// MarkDone records a person's period of kind, starting at start (a local
// midnight in their zone), as done: the schedule does not look at it again.
func (s *Service) MarkDone(ctx context.Context, userID int32, kind string, start time.Time) error {
	return s.q.DigestMarkDone(ctx, store.DigestMarkDoneParams{UserID: userID, Kind: kind, PeriodStart: start.Format(time.DateOnly)})
}

// SetClock replaces the service's clock (tests run a schedule "at" a
// chosen moment).
func (s *Service) SetClock(now func() time.Time) { s.now = now }
