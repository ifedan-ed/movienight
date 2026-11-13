package userstate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
	"movienight/server/internal/userstate"
)

func TestStateIsReadForThePagesTitlesOnly(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	s.SeedLibrary(t)
	s.Exec(t, `INSERT INTO users (id, username, is_admin, created_at) VALUES (1, 'alice', false, now()), (2, 'bob', false, now())`)
	// Two watches of movie 603 on one New York evening (a manual mark and a
	// Jellyfin play: 23:30 and 03:00 UTC the next day are the same local
	// day), one more a week later; three distinct episodes of show 1399, one
	// of them twice; bob's watch does not count for alice.
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source) VALUES
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '2026-10-01T23:30:00Z', 'manual'),
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '2026-10-02T03:00:00Z', 'jellyfin'),
		(1, 'movie', 603, NULL, NULL, 'Movie 603', '2026-10-09T20:00:00Z', 'manual'),
		(1, 'episode', 1399, 1, 1, 'Show 1399', '2026-10-01T20:00:00Z', 'manual'),
		(1, 'episode', 1399, 1, 1, 'Show 1399', '2026-10-05T20:00:00Z', 'manual'),
		(1, 'episode', 1399, 1, 2, 'Show 1399', '2026-10-01T20:00:00Z', 'manual'),
		(1, 'episode', 1399, 2, 1, 'Show 1399', '2026-10-01T20:00:00Z', 'manual'),
		(2, 'movie', 604, NULL, NULL, 'Movie 604', '2026-10-01T20:00:00Z', 'manual')`)
	s.Exec(t, `INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source, removed_at) VALUES
		(1, 'movie', 604, now(), 'app', NULL), (1, 'movie', 605, now(), 'app', now())`)
	s.Exec(t, `INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source) VALUES (1, 'tv', 1399, 9, now(), 'app')`)
	s.Exec(t, `INSERT INTO favorites (user_id, media_type, tmdb_id, added_at, source) VALUES (1, 'movie', 603, now(), 'app')`)

	st, err := s.App.RW.State.Load(context.Background(), store.User{ID: 1}, []string{"movie:603", "tv:1399", "movie:604", "movie:605", "nonsense"})
	if err != nil {
		t.Fatal(err)
	}
	m603, s1399, m604, m605 := st.Of("movie", 603), st.Of("tv", 1399), st.Of("movie", 604), st.Of("movie", 605)
	if m603.Watched != 2 || !m603.Favorite || !m603.InLibrary || *m603.JellyfinID != strings.Repeat("a", 32) {
		t.Fatalf("movie 603: %+v", m603)
	}
	if !strings.HasSuffix(*m603.JellyfinURL, "/web/#/details?id="+strings.Repeat("a", 32)) {
		t.Fatal(*m603.JellyfinURL)
	}
	if s1399.Watched != 3 || s1399.Rating == nil || *s1399.Rating != 9 || !s1399.InLibrary {
		t.Fatalf("show 1399: %+v", s1399)
	}
	if !m604.Watchlist || m604.Watched != 0 || m605.Watchlist {
		t.Fatalf("604 %+v 605 %+v", m604, m605)
	}
}

func TestTheStateQueryUsesItsIndexes(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	plan := s.Explain(t, store.StateForTitlesSQL, []string{"movie", "tv"}, []int32{603, 1399}, int32(1), "America/New_York")
	for _, table := range []string{"history", "watchlist", "user_ratings", "favorites", "library_items"} {
		if strings.Contains(plan, "Seq Scan on "+table) {
			t.Errorf("%s is scanned whole:\n%s", table, plan)
		}
	}
	if !strings.Contains(plan, "ix_history_user_tmdb") {
		t.Errorf("history is not read by person and title:\n%s", plan)
	}
}

func TestSeerrStatusIsPulledOnceAndSharedByEveryPage(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	s.Exec(t, `INSERT INTO users (id, username, is_admin, created_at) VALUES (1, 'alice', false, now())`)
	f.Seerr.Media = []fakes.JSON{
		{"tmdbId": 603, "mediaType": "movie", "status": 3, "downloadStatus": []fakes.JSON{{"size": 100, "sizeLeft": 25, "status": "downloading", "estimatedCompletionTime": "2026-11-12T20:00:00Z"}}},
		{"tmdbId": 1399, "mediaType": "tv", "status": 5, "jellyfinMediaId": strings.Repeat("d", 32)},
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		st, err := s.App.RW.State.Load(ctx, store.User{ID: 1}, []string{"movie:603", "tv:1399", "movie:1"})
		if err != nil {
			t.Fatal(err)
		}
		m, show := st.Of("movie", 603), st.Of("tv", 1399)
		if m.Request == nil || *m.Request != "processing" || m.Progress == nil || m.Progress.Percent != 75 {
			t.Fatalf("603: %+v", m)
		}
		// Available in Seerr before the library sync knows it: in the library.
		if !show.InLibrary || show.Request != nil || *show.JellyfinID != strings.Repeat("d", 32) {
			t.Fatalf("1399: %+v", show)
		}
	}
	if n := f.Seerr.Count("GET /api/v1/media"); n != 1 {
		t.Fatalf("Seerr's media table was pulled %d times for five pages", n)
	}
}

func TestAnEmptySeerrPullKeepsTheMapAndJustRequestedTitles(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	s.Exec(t, `INSERT INTO users (id, username, is_admin, created_at) VALUES (1, 'alice', false, now())`)
	f.Seerr.Media = []fakes.JSON{{"tmdbId": 603, "mediaType": "movie", "status": 2}}
	ctx := context.Background()
	seerr := s.App.RW.State.Seerr()
	seerr.TTL = 0 // every read pulls again (in the background once a map exists)
	if got := seerr.Get(ctx, []userstate.Key{{Media: "movie", ID: 603}}); len(got) != 1 {
		t.Fatalf("first pull: %v", got)
	}
	// A request made here, then Seerr answers with no media at all.
	seerr.Put(ctx, userstate.Key{Media: "movie", ID: 604}, map[string]any{"status": 2.0})
	f.Seerr.SetMedia(nil)
	before := f.Seerr.Count("GET /api/v1/media")
	seerr.Get(ctx, []userstate.Key{{Media: "movie", ID: 603}}) // starts a background pull
	for i := 0; i < 100 && f.Seerr.Count("GET /api/v1/media") == before; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // the pull's writes after its read
	got := seerr.Get(ctx, []userstate.Key{{Media: "movie", ID: 603}, {Media: "movie", ID: 604}})
	if len(got) != 2 {
		t.Fatalf("an empty pull dropped the map: %v", got)
	}
}
