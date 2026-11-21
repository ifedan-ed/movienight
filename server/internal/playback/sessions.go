// Package playback is playing a title in an outside player (VLC, Infuse,
// Outplayer, Jellyfin's apps, or one the person adds) and the "Finished
// it?" question afterwards, and playing it in Movie Night's own player in
// the browser (browser.go), which records the watch itself.
//
// The player is handed the original file from Jellyfin (the person's own
// Jellyfin token in the link, as Jellyfin's apps do) and the chosen
// subtitle as its own file. Each Play starts a play session. "Finished it?"
// (a yes records the watch and marks it played in Jellyfin) is asked once at
// most per Play, and only once the film could have ended: DueShare of its
// runtime after Play, at least MinDue. Starting another film closes the one
// before without a question, and so does the player sending the person back
// sooner. It can be switched off.
//
// What is better than the Python: asking and finishing are single
// conditional UPDATEs, so a double tap or two open tabs ask once and record
// one watch, where the Python read, checked and wrote in three steps.
package playback

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/history"
	"movienight/server/internal/jfplay"
	"movienight/server/internal/players"
	"movienight/server/internal/store"
	"movienight/server/internal/stream"
	"movienight/server/internal/titles"
)

// The timings (docs/limits.md).
const (
	// Grace: a return this soon after Play is the player still opening.
	Grace = 3 * time.Minute
	// MinDue and DueShare: when the film could have ended.
	MinDue   = 10 * time.Minute
	DueShare = 0.6
	// AskWithin: only a Play started this recently is asked about; an older
	// open Play stays open until the next Play closes it.
	AskWithin = 12 * time.Hour
)

// defaultMinutes is the runtime assumed when TMDB has none.
var defaultMinutes = map[string]int{"movie": 100, "episode": 30}

// Service is the playback side.
type Service struct {
	cfg     *config.Config
	q       *store.Queries
	clients *clients.Set
	keys    *crypto.Keys
	titles  *titles.Service
	cache   *cache.Cache
	players *players.Service
	history *history.Service
	jf      *jfplay.Client
	tickets *stream.Signer
	log     *slog.Logger
	now     func() time.Time
}

// Deps is what playback builds on: the shared title cache and answer
// cache, the person's player settings (package players, also behind
// /me/players) and their history (package history, also behind "Mark
// watched"), so a Play's watch is recorded exactly as one marked by hand.
//
// The in-browser player ("Play here", browser.go) also needs Jellyfin's
// playback calls (package jfplay) and the stream tickets the proxy checks
// (package stream).
type Deps struct {
	Titles  *titles.Service
	Cache   *cache.Cache
	Players *players.Service
	History *history.Service
	JFPlay  *jfplay.Client
	Tickets *stream.Signer
}

// New makes the service.
func New(cfg *config.Config, pool *pgxpool.Pool, cl *clients.Set, keys *crypto.Keys, d Deps, log *slog.Logger) *Service {
	return &Service{cfg: cfg, q: store.New(pool), clients: cl, keys: keys, titles: d.Titles, cache: d.Cache,
		players: d.Players, history: d.History, jf: d.JFPlay, tickets: d.Tickets, log: log, now: time.Now}
}

// Session is a Play as the API shows it.
type Session struct {
	store.PlaySession
	Key    *string
	Poster *string
}

func (s *Service) mediaOf(ps store.PlaySession) string {
	if ps.MediaType == "movie" {
		return "movie"
	}
	return "tv"
}

// describe adds the title's key and poster.
func (s *Service) describe(ctx context.Context, ps store.PlaySession) (Session, error) {
	out := Session{PlaySession: ps}
	if ps.TmdbID == nil {
		return out, nil
	}
	k := titles.Key(s.mediaOf(ps), int(*ps.TmdbID))
	out.Key = &k
	meta, err := s.titles.One(ctx, s.mediaOf(ps), int(*ps.TmdbID))
	if err != nil {
		return out, err
	}
	if meta != nil {
		out.Poster = meta.PosterPath
	}
	return out, nil
}

var errNotFound = apierr.New(http.StatusNotFound, "Play not found")

// Get is one of the person's Plays.
func (s *Service) Get(ctx context.Context, userID int32, id string) (Session, error) {
	ps, err := s.q.PlaySessionGet(ctx, store.PlaySessionGetParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, errNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return s.describe(ctx, ps)
}

// dueAfter is how long after Play the film could have ended.
func (s *Service) dueAfter(ctx context.Context, ps store.PlaySession) (time.Duration, error) {
	minutes := 0
	if ps.TmdbID != nil {
		meta, err := s.titles.One(ctx, s.mediaOf(ps), int(*ps.TmdbID))
		if err != nil {
			return 0, err
		}
		if meta != nil && meta.Runtime != nil {
			minutes = int(*meta.Runtime) // for a show, its episode length
		}
	}
	if minutes == 0 {
		minutes = defaultMinutes[ps.MediaType]
		if minutes == 0 {
			minutes = 30
		}
	}
	d := time.Duration(math.Round(float64(minutes)*DueShare*60)) * time.Second
	return max(MinDue, d), nil
}

// Returned is the person back from a player: the Play, and whether to ask
// "Finished it?" now. With id, the player sent them back from that Play (its
// x-success link), so playback has stopped. Without it they simply came
// back: the latest open Play counts, once Grace has passed.
func (s *Service) Returned(ctx context.Context, userID int32, id string) (*store.PlaySession, bool, error) {
	now := s.now().UTC()
	var ps store.PlaySession
	var err error
	if id != "" {
		ps, err = s.q.PlaySessionGet(ctx, store.PlaySessionGetParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, errNotFound
		}
		if err != nil {
			return nil, false, err
		}
		if ps.FinishedAt != nil {
			return &ps, false, nil
		}
	} else {
		ps, err = s.q.PlaySessionLatestOpen(ctx, store.PlaySessionLatestOpenParams{UserID: userID,
			JellyfinApps: players.NoQuestion, Since: now.Add(-AskWithin)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if now.Sub(ps.StartedAt) < Grace {
			return &ps, false, nil
		}
	}
	if ps.AskedAt != nil {
		return &ps, false, nil
	}
	asks, err := s.players.Asks(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if !asks {
		closed, err := s.close(ctx, ps, "dismissed", now)
		return closed, false, err
	}
	due, err := s.dueAfter(ctx, ps)
	if err != nil {
		return nil, false, err
	}
	if now.Sub(ps.StartedAt) >= due {
		_, err := s.q.PlaySessionAsk(ctx, store.PlaySessionAskParams{ID: ps.ID, UserID: userID, AskedAt: &now})
		if errors.Is(err, pgx.ErrNoRows) {
			return &ps, false, nil // asked elsewhere a moment ago
		}
		if err != nil {
			return nil, false, err
		}
		ps.AskedAt = &now
		return &ps, true, nil
	}
	if id != "" {
		// The player stopped before the film could have ended.
		closed, err := s.close(ctx, ps, "not_finished", now)
		return closed, false, err
	}
	return &ps, false, nil
}

// close finishes a Play with outcome unless it was finished meanwhile.
func (s *Service) close(ctx context.Context, ps store.PlaySession, outcome string, now time.Time) (*store.PlaySession, error) {
	got, err := s.q.PlaySessionFinish(ctx, store.PlaySessionFinishParams{ID: ps.ID, UserID: ps.UserID, FinishedAt: &now, Outcome: &outcome})
	if errors.Is(err, pgx.ErrNoRows) {
		return &ps, nil
	}
	if err != nil {
		return nil, err
	}
	return &got, nil
}

// Finish is the answer to "Finished it?": "watched" records the watch (and
// marks it played in the person's Jellyfin); the others only close the Play.
// A Play finished already changes nothing.
func (s *Service) Finish(ctx context.Context, u store.User, id, outcome string) error {
	now := s.now().UTC()
	ps, err := s.q.PlaySessionFinish(ctx, store.PlaySessionFinishParams{ID: id, UserID: u.ID, FinishedAt: &now, Outcome: &outcome})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.q.PlaySessionGet(ctx, store.PlaySessionGetParams{ID: id, UserID: u.ID}); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		return nil // finished before: once only
	}
	if err != nil || outcome != "watched" {
		return err
	}
	media := s.mediaOf(ps)
	title := ps.Title
	if ps.TmdbID != nil {
		meta, err := s.titles.One(ctx, media, int(*ps.TmdbID))
		if err != nil {
			return err
		}
		if meta != nil && meta.Title != "" {
			title = meta.Title
		}
	}
	mark := history.Mark{Media: media, Title: title, When: now, Season: intp(ps.Season), Episode: intp(ps.Episode),
		Source: "player"}
	if ps.TmdbID != nil {
		mark.TMDBID = int(*ps.TmdbID)
	}
	if ps.Episode != nil {
		// The session's title is "Show · S01E02 · Episode name".
		parts := strings.Split(ps.Title, " · ")
		mark.EpisodeTitle = &parts[len(parts)-1]
	}
	// Recorded as any watch marked here (a film also leaves the watchlist).
	if _, err := s.history.MarkWatched(ctx, u.ID, mark); err != nil {
		return err
	}
	if u.JellyfinUserID != nil {
		// The watch is recorded here either way; Jellyfin's played mark is a
		// courtesy (its Next Up), so its failure is logged, not returned.
		if err := s.clients.Jellyfin.MarkPlayed(ctx, *u.JellyfinUserID, ps.JellyfinItemID, now); err != nil {
			s.log.WarnContext(ctx, "marking a finished Play played in Jellyfin failed", "user", u.ID, "err", err)
		}
		s.cache.Forget(ctx, cache.ContinueKey(u.ID))
	}
	return nil
}

func intp(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}
