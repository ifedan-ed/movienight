// Package scrobble is other apps telling Movie Night what was watched
// (app/api/v1/scrobble.py), server to server:
//
//   - Record: a finished watch from another player (the Kodi add-on, a phone
//     shortcut), sent with the person's scrobble token or a TV's token;
//   - Webhook: the Jellyfin Webhook plugin naming a Jellyfin user, which
//     asks for that person's plays sooner (jfsync's queue; a worker reads
//     them, never the API);
//   - PluginEvent: the Movie Night plugin for Jellyfin, for every app that
//     plays through Jellyfin: start, progress, pause and stop keep watch
//     progress; played and unplayed keep history.
//
// The API routes (internal/rw/devices) authenticate and validate; this package
// decides what is recorded.
package scrobble

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/crypto"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// Service records what other apps report.
type Service struct {
	q       *store.Queries
	clients *clients.Set
	titles  *titles.Service
	cache   *cache.Cache
	sync    *jfsync.Service
	log     *slog.Logger
	now     func() time.Time
}

// New makes the service.
func New(pool *pgxpool.Pool, cl *clients.Set, t *titles.Service, c *cache.Cache, sync *jfsync.Service, log *slog.Logger) *Service {
	return &Service{q: store.New(pool), clients: cl, titles: t, cache: c, sync: sync, log: log, now: time.Now}
}

// Report is one finished watch (the API's ScrobbleIn, validated).
type Report struct {
	MediaType string // "movie" or "episode"
	TMDBID    *int
	IMDbID    string
	TVDBID    *int
	Title     string
	Season    *int
	Episode   *int
	WatchedAt string
	Source    string
	Ref       string
}

// Recorded is the answer: whether the watch is new, and its title.
type Recorded struct {
	Recorded bool
	Detail   *string
}

// FutureSlack: a sender's clock or time zone may be a day out; a time later
// than that is refused.
const FutureSlack = 24 * time.Hour

// parseWhen reads watched_at as Python's datetime.fromisoformat did (a date
// alone, a time with or without a zone; no zone means UTC); "" is now.
func (s *Service) parseWhen(v string) (time.Time, error) {
	now := s.now()
	if v == "" {
		return now.UTC(), nil
	}
	v = strings.Replace(v, "Z", "+00:00", 1)
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04-07:00",
		"2006-01-02T15:04", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02",
	} {
		if t, err := time.Parse(layout, v); err == nil {
			if t.After(now.Add(FutureSlack)) {
				return time.Time{}, apierr.New(http.StatusUnprocessableEntity, "watched_at is in the future")
			}
			return t, nil
		}
	}
	return time.Time{}, apierr.New(http.StatusUnprocessableEntity, "watched_at must be ISO 8601")
}

// refOf is the reference kept with the watch (history.source_ref, 100
// characters, part of a unique index): as sent when it fits, else its
// sha256, so a longer one is still recorded once and never cut.
func refOf(ref string) string {
	if utf8.RuneCountInString(ref) <= 100 {
		return ref
	}
	return "sha256:" + crypto.TokenHash(ref)
}

// pyOpt writes an optional number as Python's f-string wrote None, so the
// made-up reference of a report without one is the same one the Python app
// recorded (a resent report must still be "already recorded").
func pyOpt(n *int) string {
	if n == nil {
		return "None"
	}
	return strconv.Itoa(*n)
}

// tmdbByIMDb finds a title of one kind by its IMDb id: the title cache
// first, then TMDB. A TMDB hit of the other kind is no match (a show's IMDb
// id must not turn an episode into a film).
func (s *Service) tmdbByIMDb(ctx context.Context, imdb, media string) (*int, error) {
	if imdb == "" {
		return nil, nil
	}
	id, err := s.q.TitleTMDBByIMDb(ctx, store.TitleTMDBByIMDbParams{ImdbID: &imdb, MediaType: media})
	if err == nil {
		n := int(id)
		return &n, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return s.find(ctx, imdb, "imdb_id", media)
}

// find asks TMDB for a title by another site's id; nil when TMDB has none
// of that kind.
func (s *Service) find(ctx context.Context, id, source, media string) (*int, error) {
	hit, err := s.clients.TMDB.FindExternal(ctx, id, source)
	if err != nil || hit == nil || jsonx.Str(hit, "media_type") != media {
		return nil, err
	}
	n, ok := jsonx.Int(hit, "id")
	if !ok {
		return nil, nil
	}
	return &n, nil
}

// Record records one reported watch, once.
func (s *Service) Record(ctx context.Context, userID int32, r Report) (Recorded, error) {
	media := "movie"
	if r.MediaType == "episode" {
		media = "tv"
	}
	tmdbID := r.TMDBID
	var err error
	if tmdbID == nil && r.IMDbID != "" {
		if tmdbID, err = s.tmdbByIMDb(ctx, r.IMDbID, media); err != nil {
			return Recorded{}, err
		}
	}
	if tmdbID == nil && r.TVDBID != nil && r.MediaType == "episode" {
		if tmdbID, err = s.find(ctx, strconv.Itoa(*r.TVDBID), "tvdb_id", "tv"); err != nil {
			return Recorded{}, err
		}
	}
	if tmdbID == nil && r.Title == "" {
		return Recorded{}, apierr.New(http.StatusUnprocessableEntity, "Send tmdb_id, imdb_id or tvdb_id, or at least a title")
	}
	when, err := s.parseWhen(r.WatchedAt)
	if err != nil {
		return Recorded{}, err
	}
	ref := r.Ref
	if ref == "" {
		who := r.Title
		if tmdbID != nil {
			who = strconv.Itoa(*tmdbID)
		}
		ref = fmt.Sprintf("%s:%s:%s:%s:%s", r.MediaType, who, pyOpt(r.Season), pyOpt(r.Episode), when.Format("2006-01-02"))
	}
	ref = refOf(ref)
	title := r.Title
	var tid *int32
	if tmdbID != nil {
		v := int32(*tmdbID)
		tid = &v
		// The name TMDB gives wins over the sender's (Kodi's own scrape can
		// be in another language). A TMDB failure keeps the sender's name:
		// the watch matters more than its label, and the title cache fills
		// the name in on the next view.
		meta, err := s.titles.One(ctx, media, *tmdbID)
		if err != nil {
			return Recorded{}, err
		}
		if meta != nil && meta.Title != "" {
			title = meta.Title
		}
	}
	_, err = s.q.ScrobbleAdd(ctx, store.ScrobbleAddParams{UserID: userID, MediaType: r.MediaType, TmdbID: tid,
		Season: i32(r.Season), Episode: i32(r.Episode), Title: title, WatchedAt: when, Source: r.Source, SourceRef: &ref})
	if errors.Is(err, pgx.ErrNoRows) {
		return Recorded{Recorded: false, Detail: ptr("already recorded")}, nil
	}
	if err != nil {
		return Recorded{}, err
	}
	// Never the title in the log (docs/limits.md, "Logs").
	s.log.InfoContext(ctx, "scrobble recorded", "source", r.Source, "user", userID)
	out := Recorded{Recorded: true}
	if title != "" {
		out.Detail = &title
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

func i32(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n)
	return &v
}

// ---------------------------------------------------------------------------
// The Jellyfin Webhook plugin
// ---------------------------------------------------------------------------

// webhookEvents are the Webhook plugin's events that may mean a new play.
var webhookEvents = map[string]bool{"PlaybackStop": true, "UserDataSaved": true, "ItemMarkedPlayed": true, "MarkPlayed": true}

// Webhook handles one Webhook plugin call (its body as JSON, whatever the
// content type: that depends on the plugin's version). Events that cannot
// mean a play, and Jellyfin users nobody here is linked to, are accepted and
// ignored: the plugin sends every event to one address.
func (s *Service) Webhook(ctx context.Context, payload map[string]any) error {
	event := jsonx.Str(payload, "NotificationType")
	if event == "" {
		event = jsonx.Str(payload, "Event")
	}
	if event != "" && !webhookEvents[event] {
		return nil
	}
	jf := jfsync.NormalizeID(fmt.Sprint(valueOr(payload["UserId"], "")))
	if jf == "" {
		return nil
	}
	u, err := s.q.SyncUserByJellyfinID(ctx, &jf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.sync.AskSoon(ctx, u.ID)
	return err
}

func valueOr(v any, def any) any {
	if v == nil {
		return def
	}
	return v
}
