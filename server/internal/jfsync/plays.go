package jfsync

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
)

// Entry is what one play records: the film, or the show and its episode.
type Entry struct {
	MediaType    string // "movie" or "episode"
	TMDBID       *int32
	Season       *int32
	Episode      *int32
	EpisodeTitle *string
	Title        string
}

// unclaimed is a watch marked here that a Jellyfin play may claim.
type unclaimed struct {
	id      int32
	media   string
	tmdb    *int32
	season  *int32
	episode *int32
	when    time.Time
	taken   bool
}

func eqPtr(a, b *int32) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// twin is the marked watch this play is, or nil: same title (a title is
// needed: two unknown titles are not the same watch), same episode, within
// ClaimWindow. The first such mark in time order, so the outcome is the same
// on every run.
func twin(marks []*unclaimed, e Entry, when time.Time) *unclaimed {
	if e.TMDBID == nil {
		return nil
	}
	for _, h := range marks {
		if h.taken || h.media != e.MediaType || !eqPtr(h.tmdb, e.TMDBID) || !eqPtr(h.season, e.Season) || !eqPtr(h.episode, e.Episode) {
			continue
		}
		if d := h.when.Sub(when); d <= ClaimWindow && d >= -ClaimWindow {
			return h
		}
	}
	return nil
}

func (s *Service) unclaimedMarks(ctx context.Context, q *store.Queries, userID int32) ([]*unclaimed, error) {
	rows, err := q.SyncUnclaimed(ctx, store.SyncUnclaimedParams{UserID: userID, WatchedAt: time.Now().Add(-ClaimHorizon)})
	if err != nil {
		return nil, err
	}
	out := make([]*unclaimed, len(rows))
	for i, r := range rows {
		out[i] = &unclaimed{id: r.ID, media: r.MediaType, tmdb: r.TmdbID, season: r.Season, episode: r.Episode, when: r.WatchedAt}
	}
	return out, nil
}

// placement collects one sync's writes, sent as two batches.
type placement struct {
	claims []store.SyncClaimWatchParams
	adds   []store.SyncAddPlayParams
}

func (p *placement) place(userID int32, when time.Time, ref string, e Entry, marks []*unclaimed) bool {
	r := ref
	if h := twin(marks, e, when); h != nil {
		h.taken = true
		p.claims = append(p.claims, store.SyncClaimWatchParams{Ref: &r, ID: h.id, UserID: userID})
		return false
	}
	p.adds = append(p.adds, store.SyncAddPlayParams{UserID: userID, MediaType: e.MediaType, TmdbID: e.TMDBID,
		Season: e.Season, Episode: e.Episode, EpisodeTitle: e.EpisodeTitle, Title: e.Title, WatchedAt: when, SourceRef: &r})
	return true
}

func (p *placement) write(ctx context.Context, q *store.Queries) error {
	if len(p.claims) > 0 {
		if err := execBatch(q.SyncClaimWatch(ctx, p.claims)); err != nil {
			return err
		}
	}
	if len(p.adds) > 0 {
		return execBatch(q.SyncAddPlay(ctx, p.adds))
	}
	return nil
}

// RecordPlay records one Jellyfin play (the plugin's "played" event): a
// matching watch marked here takes the play's reference, otherwise it is a
// new history row; a reference recorded already changes nothing. One
// transaction. added reports a new row.
func (s *Service) RecordPlay(ctx context.Context, userID int32, when time.Time, ref string, e Entry) (added bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		refs, err := q.SyncPlayRefs(ctx, userID)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if r == ref {
				return nil
			}
		}
		marks, err := s.unclaimedMarks(ctx, q, userID)
		if err != nil {
			return err
		}
		var p placement
		added = p.place(userID, when, ref, e, marks)
		return p.write(ctx, q)
	})
	return added, err
}

// PlaysResult is one person's sync. Undated counts the plays without a
// date the person hasn't dealt with yet (status.HideUndated).
type PlaysResult struct {
	Added, Undated int
}

// Plays adds this person's new Jellyfin plays to their history.
//
// Every played item is read, page by page, with no cap. A read that does
// not come back whole adds nothing (the next sync, 5 minutes on, reads
// again). The outcome is kept per person (status "plays:<id>"): a failed
// read is shown to admins and to the person until a later read is complete,
// and so is how many plays Jellyfin lists without a date (they cannot go on
// a timeline, so they are not added). Those are kept by name, up to
// status.MaxUndated, so Profile can list them.
func (s *Service) Plays(ctx context.Context, userID int32, jellyfinUser string) (PlaysResult, error) {
	var res PlaysResult
	if jellyfinUser == "" {
		return res, nil
	}
	name := status.Plays(userID)
	played, err := s.clients.Jellyfin.Played(ctx, jellyfinUser, "Movie,Episode")
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		problem := "Jellyfin couldn't be read (" + err.Error() + ")"
		var code any
		var inc *clients.IncompleteRead
		var je *clients.JellyfinError
		if errors.As(err, &inc) {
			problem, code = inc.Msg, inc.Status
		} else if errors.As(err, &je) {
			problem, code = je.Msg, je.Status
		}
		if rerr := s.status.Record(ctx, name, status.Value{"ok": false, "at": status.Now(), "problem": problem, "status": code}); rerr != nil {
			return res, rerr
		}
		return res, err
	}
	// The ones the person already dealt with are left out from the start,
	// so the list of MaxUndated is always the next ones to answer.
	hidden, err := s.status.HiddenUndated(ctx, userID)
	if err != nil {
		return res, err
	}
	undated := []status.UndatedItem{}
	var undatedCount int
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		series := map[string]int32{}
		rows, err := q.SyncSeriesTMDB(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			series[r.JellyfinID] = *r.TmdbID
		}
		refList, err := q.SyncPlayRefs(ctx, userID)
		if err != nil {
			return err
		}
		refs := make(map[string]bool, len(refList))
		for _, r := range refList {
			refs[r] = true
		}
		marks, err := s.unclaimedMarks(ctx, q, userID)
		if err != nil {
			return err
		}
		var p placement
		undated = undated[:0]
		undatedCount = 0
		for _, it := range played {
			id := jsonx.Str(it, "Id")
			when, ok := ParseTime(jsonx.Str(jsonx.Child(it, "UserData"), "LastPlayedDate"))
			if id == "" || !ok {
				// Marked played without a date (or sent without an id):
				// nothing to put on a timeline. Kept by name; the person
				// is told and decides.
				// One without an id can't be listed or hidden, so it isn't
				// counted either: it would be a notice nobody can clear.
				if id != "" && !hidden[id] {
					undatedCount++
					if len(undated) < status.MaxUndated {
						undated = append(undated, undatedItem(id, entryOf(it, series)))
					}
				}
				continue
			}
			ref := PlayRef(id, when)
			if refs[ref] {
				continue
			}
			refs[ref] = true
			if p.place(userID, when, ref, entryOf(it, series), marks) {
				res.Added++
			}
		}
		return p.write(ctx, q)
	})
	if err != nil {
		return PlaysResult{}, err
	}
	if res.Added > 0 {
		// New plays change Home's Up next (Jellyfin's next episode).
		s.cache.Forget(ctx, cache.ContinueKey(userID))
	}
	at := status.Now()
	if err := s.status.Record(ctx, name, status.Value{"ok": true, "at": at, "last_good_at": at, "problem": nil,
		"status": nil, "undated": undatedCount, "undated_items": undated}); err != nil {
		return res, err
	}
	// The count the person is told: what they haven't dealt with.
	_, res.Undated, err = s.status.Undated(ctx, userID)
	return res, err
}

// undatedItem is how an undated play is listed: an episode under its show,
// which is the title page it links to.
func undatedItem(id string, e Entry) status.UndatedItem {
	it := status.UndatedItem{ID: id, MediaType: "movie", TMDBID: e.TMDBID, Title: e.Title}
	if e.MediaType == "episode" {
		it.MediaType, it.Season, it.Episode, it.EpisodeTitle = "tv", e.Season, e.Episode, e.EpisodeTitle
	}
	return it
}

// entryOf is what a played Jellyfin item records.
func entryOf(it jsonx.Obj, series map[string]int32) Entry {
	if jsonx.Str(it, "Type") == "Episode" {
		e := Entry{MediaType: "episode", Season: jsonx.IntPtr(it, "ParentIndexNumber"),
			Episode: jsonx.IntPtr(it, "IndexNumber"), Title: jsonx.Str(it, "SeriesName")}
		if t, ok := series[jsonx.Str(it, "SeriesId")]; ok {
			e.TMDBID = &t
		}
		if n := jsonx.Str(it, "Name"); n != "" {
			e.EpisodeTitle = &n
		}
		return e
	}
	return Entry{MediaType: "movie", TMDBID: tmdbOf(it), Title: jsonx.Str(it, "Name")}
}

// AllPlays syncs every linked person, one after another, each in its own
// transaction (one person's failure never stops the others: the Python's
// shared session once did). Returns new plays per person id (the log never
// names anyone) and how many reads failed.
func (s *Service) AllPlays(ctx context.Context, lock func(ctx context.Context, userID int32, run func() error) error) (map[int32]int, int, error) {
	users, err := s.q.SyncLinkedUsers(ctx)
	if err != nil {
		return nil, 0, err
	}
	added := map[int32]int{}
	failed := 0
	for _, u := range users {
		if ctx.Err() != nil {
			return added, failed, ctx.Err()
		}
		err := lock(ctx, u.ID, func() error {
			r, err := s.Plays(ctx, u.ID, u.JellyfinUserID)
			if r.Added > 0 {
				added[u.ID] = r.Added
			}
			return err
		})
		if err == nil {
			continue
		}
		failed++
		var inc *clients.IncompleteRead
		if errors.As(err, &inc) {
			// Expected while Jellyfin cannot give a whole answer (a deleted
			// account answers 404 every time): one line, kept in the status.
			s.log.WarnContext(ctx, "plays sync: nothing added", "user", u.ID, "why", inc.Msg)
		} else {
			s.log.ErrorContext(ctx, "plays sync failed", "user", u.ID, "err", err)
		}
	}
	return added, failed, nil
}

// UserIDString is a person's id as a queue member.
func UserIDString(id int32) string { return strconv.Itoa(int(id)) }

// UndatedNote is the sentence for plays Jellyfin lists without a date
// (nil when there are none): a play needs its date to go into the history,
// so Profile lists them to add with a date or to hide.
func UndatedNote(n int) *string {
	var s string
	switch {
	case n <= 0:
		return nil
	case n == 1:
		s = "1 play has no date in Jellyfin. Profile lists it."
	default:
		s = thousands(n) + " plays have no date in Jellyfin. Profile lists them."
	}
	return &s
}
