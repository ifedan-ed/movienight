package scrobble

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/cache"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/store"
)

// PluginItem is a film or episode as the Movie Night plugin for Jellyfin
// sends it (jellyfin-plugin/, the API's JellyfinPluginItem, validated).
type PluginItem struct {
	ID, Type, Name, SeriesID, SeriesName string
	Season, Episode, RuntimeS            *int
	TMDB, IMDb, TVDB                     string
	SeriesTMDB, SeriesIMDb, SeriesTVDB   string
}

// PluginEvent is one plugin event (the API's JellyfinPluginEvent).
type PluginEvent struct {
	Event              string // start, progress, pause, stop, played, unplayed
	UserID             string
	Item               PluginItem
	PositionS          *int
	PlayedToCompletion bool
	LastPlayed         string
	Client, Device     string
}

// DoneShare: past this share of the runtime a stop counts as finished.
const DoneShare = 0.9

// ProgressKey is a watch_progress key: one row per person and film or
// episode.
func ProgressKey(media string, tmdbID int32, season, episode *int32) string {
	if media == "movie" {
		return "movie:" + strconv.Itoa(int(tmdbID))
	}
	n := func(p *int32) string {
		if p == nil {
			return "0"
		}
		return strconv.Itoa(int(*p))
	}
	return "tv:" + strconv.Itoa(int(tmdbID)) + ":" + n(season) + ":" + n(episode)
}

func digits(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// entry is the film, or the show plus season and episode, by TMDB id (from
// the item's own ids, the library, or IMDb/TheTVDB through TMDB); nil when
// none can be found.
func (s *Service) entry(ctx context.Context, it PluginItem) (*jfsync.Entry, error) {
	if it.Type == "Movie" {
		id := digits(it.TMDB)
		var err error
		if id == nil {
			if id, err = s.tmdbByIMDb(ctx, it.IMDb, "movie"); err != nil || id == nil {
				return nil, err
			}
		}
		v := int32(*id)
		return &jfsync.Entry{MediaType: "movie", TMDBID: &v, Title: it.Name}, nil
	}
	id := digits(it.SeriesTMDB)
	if id == nil && it.SeriesID != "" {
		got, err := s.q.SyncLibraryTMDBForItem(ctx, jfsync.NormalizeID(it.SeriesID))
		if err == nil && got != nil {
			n := int(*got)
			id = &n
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	var err error
	if id == nil {
		if id, err = s.tmdbByIMDb(ctx, it.SeriesIMDb, "tv"); err != nil {
			return nil, err
		}
	}
	if id == nil && it.SeriesTVDB != "" {
		if id, err = s.find(ctx, it.SeriesTVDB, "tvdb_id", "tv"); err != nil {
			return nil, err
		}
	}
	if id == nil {
		return nil, nil
	}
	v := int32(*id)
	e := &jfsync.Entry{MediaType: "episode", TMDBID: &v, Season: i32(it.Season), Episode: i32(it.Episode), Title: it.SeriesName}
	if it.Name != "" {
		e.EpisodeTitle = &it.Name
	}
	return e, nil
}

// PluginEvent applies one event. A Jellyfin user nobody here is linked to,
// or an item no TMDB id can be found for, is accepted and ignored (logged),
// as the plugin never retries.
func (s *Service) PluginEvent(ctx context.Context, ev PluginEvent) (err error) {
	jf := jfsync.NormalizeID(ev.UserID)
	u, err := s.q.SyncUserByJellyfinID(ctx, &jf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := s.entry(ctx, ev.Item)
	if err != nil {
		return err
	}
	if e == nil {
		s.log.InfoContext(ctx, "plugin event: no TMDB id for the Jellyfin item", "user", u.ID, "item", ev.Item.ID)
		return nil
	}
	// Every kept event changes Home's Up next for the person (a position, a
	// finished episode), so it is forgotten once the event is written.
	defer func() {
		if err == nil {
			s.cache.Forget(ctx, cache.ContinueKey(u.ID))
		}
	}()
	item := jfsync.NormalizeID(ev.Item.ID)
	key := ProgressKey(e.MediaType, *e.TMDBID, e.Season, e.Episode)
	clear := func() error { return s.q.ProgressClear(ctx, store.ProgressClearParams{UserID: u.ID, Key: key}) }
	save := func(state string) error {
		pos := 0
		if ev.PositionS != nil {
			pos = *ev.PositionS
		}
		return s.q.ProgressSave(ctx, store.ProgressSaveParams{UserID: u.ID, Key: key, MediaType: e.MediaType,
			TmdbID: *e.TMDBID, Season: e.Season, Episode: e.Episode, Title: e.Title, EpisodeTitle: e.EpisodeTitle,
			PositionS: int32(pos), DurationS: i32(ev.Item.RuntimeS), State: state, Client: opt(ev.Client),
			Device: opt(ev.Device), JellyfinItemID: &item})
	}
	switch ev.Event {
	case "start", "progress":
		return save("playing")
	case "pause":
		return save("paused")
	case "stop":
		if ev.PlayedToCompletion || finished(ev.PositionS, ev.Item.RuntimeS) {
			return clear()
		}
		return save("stopped")
	case "played":
		if err := clear(); err != nil {
			return err
		}
		when, ok := jfsync.ParseTime(ev.LastPlayed)
		if !ok {
			when = s.now().UTC()
		}
		added, err := s.sync.RecordPlay(ctx, u.ID, when, jfsync.PlayRef(item, when), *e)
		if added {
			s.log.InfoContext(ctx, "jellyfin plugin: a play recorded", "user", u.ID)
		}
		return err
	default: // unplayed: the history stays, the progress goes
		return clear()
	}
}

func finished(pos, dur *int) bool {
	return pos != nil && dur != nil && *pos > 0 && *dur > 0 && float64(*pos) >= DoneShare*float64(*dur)
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
