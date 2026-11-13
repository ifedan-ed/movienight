package jfsync

import (
	"context"
	"strconv"
	"time"

	"errors"
	"movienight/server/internal/clients"
)

// The Jellyfin webhook (and the Movie Night plugin's events) ask for one
// person's plays sooner than the 5-minute sync. Several events arrive for
// one watch (stop, user data saved, marked played) within a second or two,
// so the first one asks for a sync WebhookDelay later and the rest join it:
// one Jellyfin read per watch, not four.
//
// The Python kept this in a dict in the web process ("_pending"), so each
// replica debounced on its own and a restart dropped the waiting syncs; the
// sync itself ran in the web process. Here the API only records the request
// in Valkey (tasks.Queue, shared by every replica) and a worker runs it.
const (
	QueueName    = "jf-plays"
	WebhookDelay = 5 * time.Second
	// webhookHold: the longest a request waits for a worker before a new
	// event may ask again (a worker that died after taking it).
	webhookHold = 2 * time.Minute
	// lockFor: the longest one person's plays sync may hold their lock (a
	// very long history on a slow Jellyfin); a crashed holder frees it then.
	lockFor = 10 * time.Minute
	// pollEvery: how often a worker looks for due syncs.
	pollEvery = time.Second
)

// AskSoon asks for this person's plays sync WebhookDelay from now (API).
// false: one is already waiting, which this event joins.
func (s *Service) AskSoon(ctx context.Context, userID int32) (bool, error) {
	return s.queue.Enqueue(ctx, UserIDString(userID), WebhookDelay, webhookHold)
}

// WithLock runs one person's sync under their run lock, so the scheduled
// sync and a webhook sync of the same person never run at once (both would
// read the same new plays and race to add them). Waits for a held lock up to
// lockFor, polling.
func (s *Service) WithLock(ctx context.Context, userID int32, run func() error) error {
	member := UserIDString(userID)
	for {
		ok, err := s.queue.Lock(ctx, member, lockFor)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
	defer func() {
		// Unlocked on a fresh context: a cancelled run must still free it.
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.queue.Unlock(c, member)
	}()
	return run()
}

// RunQueued serves the webhook queue and the Jellyfin marks queue
// (marks.go) until ctx ends (every worker runs it; each request goes to
// exactly one).
func (s *Service) RunQueued(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		due, err := s.queue.Due(ctx, 16)
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "webhook queue unreadable", "err", err)
			}
			continue
		}
		for _, member := range due {
			s.runOne(ctx, member)
		}
		due, err = s.marks.Due(ctx, 16)
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "Jellyfin marks queue unreadable", "err", err)
			}
			continue
		}
		for _, member := range due {
			s.runMarks(ctx, member)
		}
	}
}

func (s *Service) runOne(ctx context.Context, member string) {
	n, err := strconv.Atoi(member)
	if err != nil {
		s.log.ErrorContext(ctx, "webhook queue: not a person id; dropped", "member", member)
		return
	}
	id := int32(n)
	// Released before the sync: an event arriving during it asks for
	// another sync after it (the Python dropped those until the sync ended,
	// so a play reported mid-sync waited for the 5-minute job).
	if err := s.queue.Release(ctx, member); err != nil {
		s.log.ErrorContext(ctx, "webhook queue: release failed", "err", err)
	}
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		// Deleted since the event: nothing to do. Anything else is logged.
		s.log.WarnContext(ctx, "webhook plays sync: person unreadable", "user", id, "err", err)
		return
	}
	if u.JellyfinUserID == nil {
		return
	}
	err = s.WithLock(ctx, id, func() error {
		r, err := s.Plays(ctx, id, *u.JellyfinUserID)
		if r.Added > 0 {
			s.log.InfoContext(ctx, "jellyfin webhook: new plays", "user", id, "added", r.Added)
		}
		return err
	})
	var inc *clients.IncompleteRead
	switch {
	case err == nil:
	case errors.As(err, &inc):
		s.log.WarnContext(ctx, "webhook plays sync: nothing added", "user", id, "why", inc.Msg)
	case ctx.Err() != nil:
	default:
		s.log.ErrorContext(ctx, "webhook plays sync failed", "user", id, "err", err)
	}
}
