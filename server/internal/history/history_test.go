package history_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/history"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func setup(t *testing.T) (*testenv.Server, *history.Service) {
	t.Helper()
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	s.Exec(t, `INSERT INTO users (id, username, is_admin, created_at) VALUES (1, 'alice', false, now()), (2, 'bob', false, now())`)
	return s, history.New(s.App.RW.Q, s.App.RW.Settings)
}

func TestThePageIsGroupedAndPagedInSQL(t *testing.T) {
	s, h := setup(t)
	ctx := context.Background()
	// 95 films on 95 days, each watched twice the same evening (a manual mark
	// and a Jellyfin play), and three episodes; bob's watches are his own.
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT 1, 'movie', 1000 + n, 'Movie ' || (1000 + n), timestamptz '2026-01-01 20:00 America/New_York' + n * interval '1 day', src
		FROM generate_series(1, 95) n, (VALUES ('manual'), ('jellyfin')) v(src)`)
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source) VALUES
		(1, 'episode', 1399, 1, 1, 'Show 1399', '2026-11-01T01:00:00Z', 'manual'),
		(1, 'episode', 1399, 1, 2, 'Show 1399', '2026-11-01T01:30:00Z', 'manual'),
		(1, 'episode', 1399, 1, 2, 'Show 1399', '2026-11-03T01:30:00Z', 'manual'),
		(2, 'movie', 603, NULL, NULL, 'Movie 603', now(), 'manual')`)
	page1, total, err := h.Page(ctx, 1, s.App.RW.Cfg.Location, "", 1, 40)
	if err != nil {
		t.Fatal(err)
	}
	if total != 98 || len(page1) != 40 {
		t.Fatal(total, len(page1))
	}
	// Newest first: the second watch of S1E2, then S1E2, S1E1, then films.
	if *page1[0].Episode != 2 || *page1[1].Episode != 2 || *page1[2].Episode != 1 || *page1[0].Key != "tv:1399" {
		t.Fatalf("%+v", page1[:3])
	}
	film := page1[3]
	if *film.Key != "movie:1095" || len(film.IDs) != 2 || strings.Join(film.Sources, ",") != "jellyfin,manual" && strings.Join(film.Sources, ",") != "manual,jellyfin" {
		t.Fatalf("%+v", film)
	}
	page3, total, _ := h.Page(ctx, 1, s.App.RW.Cfg.Location, "", 3, 40)
	if len(page3) != 18 || total != 98 {
		t.Fatal(len(page3), total)
	}
	past, total, _ := h.Page(ctx, 1, s.App.RW.Cfg.Location, "", 9, 40)
	if len(past) != 0 || total != 98 {
		t.Fatal(len(past), total)
	}
	shows, total, _ := h.Page(ctx, 1, s.App.RW.Cfg.Location, "tv", 1, 40)
	if len(shows) != 3 || total != 3 {
		t.Fatal(len(shows))
	}
	films, total, _ := h.Page(ctx, 1, s.App.RW.Cfg.Location, "movie", 1, 40)
	if total != 95 || films[0].MediaType != "movie" {
		t.Fatal(total)
	}
}

func TestStatsAreCountedFromMergedWatches(t *testing.T) {
	s, h := setup(t)
	year := time.Now().In(s.App.RW.Cfg.Location).Year()
	s.Exec(t, `INSERT INTO titles (key, media_type, tmdb_id, title, runtime, detailed, fetched_at) VALUES
		('movie:603', 'movie', 603, 'Movie 603', 120, true, now()), ('tv:1399', 'tv', 1399, 'Show 1399', NULL, true, now())`)
	s.Exec(t, fmt.Sprintf(`INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source) VALUES
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '%d-03-01T20:00:00Z', 'manual'),
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '%d-03-01T21:00:00Z', 'jellyfin'),
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '%d-03-01T20:00:00Z', 'manual'),
		(1, 'movie', NULL, NULL, NULL, 'A home video', '%d-04-01T20:00:00Z', 'kodi'),
		(1, 'episode', 1399, 1, 1, 'Show 1399', now(), 'manual'),
		(1, 'episode', 1399, 1, 2, 'Show 1399', now(), 'manual')`, year, year, year-1, year))
	s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source) VALUES (1, 'movie', 603, 8, now(), 'app')`)
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source) VALUES (1, 'movie', 604, now(), 'app')`)
	st, err := h.Stats(context.Background(), 1, s.App.RW.Cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	// Films: 603 and the home video (2); plays: 603 this year (merged), 603
	// last year, the video (3); hours: 2×120 + 0 + 2×30 minutes = 5 h.
	if st.Movies != 2 || st.MoviePlays != 3 || st.Episodes != 2 || st.Shows != 1 || st.Hours != 5 ||
		st.ThisYear != 2 || st.Ratings != 1 || st.Watchlist != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestTheRatePromptLooksAtEveryRecentWatch(t *testing.T) {
	s, h := setup(t)
	ctx := context.Background()
	// 25 films watched and rated since; one older one this fortnight unrated.
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		VALUES (1, 'movie', 603, 'Movie 603', now() - interval '10 days', 'manual')`)
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT 1, 'movie', 1000 + n, 'Movie', now() - interval '5 days' - n * interval '1 minute', 'manual' FROM generate_series(0, 24) n`)
	s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source)
		SELECT 1, 'movie', 1000 + n, 7, now(), 'app' FROM generate_series(0, 24) n`)
	p, err := h.RatePrompt(ctx, 1)
	if err != nil || p == nil || p.TMDBID != 603 || p.Key != "movie:603" || p.Title != "Movie 603" {
		t.Fatal(p, err)
	}
	if err := h.DismissRatePrompt(ctx, 1, "movie:603"); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.RatePrompt(ctx, 1); p != nil {
		t.Fatal("dismissed but asked again", p)
	}
	if p, _ := h.RatePrompt(ctx, 2); p != nil {
		t.Fatal(p)
	}
}

func TestEveryNotNowIsRemembered(t *testing.T) {
	s, h := setup(t)
	ctx := context.Background()
	for n := 1; n <= 601; n++ {
		if err := h.DismissRatePrompt(ctx, 1, fmt.Sprintf("movie:%d", n)); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.DismissRatePrompt(ctx, 1, "movie:1") // again: kept once
	var kept []string
	if _, err := s.App.RW.Settings.User(ctx, 1, history.RateDismissedKey, &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 601 || kept[0] != "movie:1" {
		t.Fatal(len(kept))
	}
}

func TestMarkWatchedTakesAFilmOffTheWatchlist(t *testing.T) {
	s, h := setup(t)
	ctx := context.Background()
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source) VALUES (1, 'movie', 603, now(), 'app'), (1, 'tv', 1399, now(), 'app')`)
	if _, err := h.MarkWatched(ctx, 1, history.Mark{Media: "movie", TMDBID: 603, Title: "Movie 603"}); err != nil {
		t.Fatal(err)
	}
	season, episode := 1, 1
	if _, err := h.MarkWatched(ctx, 1, history.Mark{Media: "tv", TMDBID: 1399, Title: "Show 1399", Season: &season, Episode: &episode}); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM watchlist WHERE removed_at IS NULL`).Scan(&left)
	if left != 1 {
		t.Fatal("a show stays on the watchlist after one episode; the film leaves:", left)
	}
	eps, _ := h.EpisodeWatches(ctx, 1, s.App.RW.Cfg.Location, 1399)
	if len(eps[[2]int{1, 1}]) != 1 {
		t.Fatal(eps)
	}
}

func TestTheHistoryPageQueryReadsOnePersonsRowsByIndex(t *testing.T) {
	s, _ := setup(t)
	plan := s.Explain(t, store.HistoryPageSQL, int32(1), "", "America/New_York", int32(0), int32(40))
	if strings.Contains(plan, "Seq Scan on history") || !strings.Contains(plan, "Index") {
		t.Fatal(plan)
	}
}
