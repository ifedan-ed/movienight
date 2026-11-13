package titles_test

import (
	"context"
	"testing"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
	"movienight/server/internal/titles"
)

func TestNormalizeReadsMoviesShowsAndSkipsPeople(t *testing.T) {
	m := titles.Normalize(roundTrip(fakes.Movie(603)), "")
	if m.Key != "movie:603" || m.Title != "Movie 603" || *m.Year != 2000 || m.Genres[0] != "Comedy" || *m.Vote() != 7.0 {
		t.Fatalf("%+v", m)
	}
	show := fakes.Show(1399)
	delete(show, "media_type")
	if s := titles.Normalize(roundTrip(show), ""); s.Key != "tv:1399" || s.Title != "Show 1399" || s.Genres[0] != "Drama" {
		t.Fatalf("%+v", s)
	}
	if titles.Normalize(clients.JSON{"id": 5.0, "media_type": "person", "name": "Some Person"}, "") != nil {
		t.Fatal("a person became a title")
	}
	// TMDB answers are decoded JSON: numbers are float64.
	if titles.Normalize(clients.JSON{"id": 0.0, "title": "x"}, "movie") != nil {
		t.Fatal("no id")
	}
}

func roundTrip(v fakes.JSON) clients.JSON {
	// What the TMDB client hands over: decoded JSON (float64 numbers).
	return clients.JSON(decode(v))
}

func TestRememberWritesOnlyWhatIsNewOrStale(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	ctx := context.Background()
	svc := s.App.RW.Titles
	metas := titles.NormalizeAll([]clients.JSON{roundTrip(fakes.Movie(603)), roundTrip(fakes.Movie(604))}, "movie")
	svc.Remember(ctx, metas)
	var at1 time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT fetched_at FROM titles WHERE key = 'movie:603'`).Scan(&at1); err != nil {
		t.Fatal(err)
	}
	// Seen again at once: nothing to write, the row is as it was.
	svc.Remember(ctx, metas)
	var at2 time.Time
	_ = s.Pool.QueryRow(ctx, `SELECT fetched_at FROM titles WHERE key = 'movie:603'`).Scan(&at2)
	if !at1.Equal(at2) {
		t.Fatal("a fresh title was written again")
	}
	// A list-sourced row a day old is refreshed.
	s.Exec(t, `UPDATE titles SET fetched_at = now() - interval '2 days', vote_average = 1 WHERE key = 'movie:603'`)
	svc.Remember(ctx, metas)
	var vote float64
	_ = s.Pool.QueryRow(ctx, `SELECT vote_average FROM titles WHERE key = 'movie:603'`).Scan(&vote)
	if vote != 7.0 {
		t.Fatalf("stale row not refreshed: %v", vote)
	}
}

func TestEnsureFetchesTheMissingBoundedAndRemembersUnknownTitles(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	ctx := context.Background()
	got, err := s.App.RW.Titles.Ensure(ctx, []string{"movie:603", "tv:1399", "movie:9000001", "movie:700", "bogus"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got["movie:603"].Title != "Movie 603" || got["tv:1399"].Title != "Show 1399" || !got["movie:603"].Detailed {
		t.Fatalf("%+v", got)
	}
	if _, ok := got["movie:700"]; ok {
		t.Fatal("fetched past the bound")
	}
	if n := f.TMDB.Count("GET /movie/") + f.TMDB.Count("GET /tv/"); n != 3 {
		t.Fatalf("%d TMDB calls", n)
	}
	// TMDB has no 9000001: remembered (bare, detailed), not asked again.
	f.TMDB.Reset()
	if _, err := s.App.RW.Titles.Ensure(ctx, []string{"movie:603", "movie:9000001"}, 10); err != nil {
		t.Fatal(err)
	}
	if n := len(f.TMDB.Calls()); n != 0 {
		t.Fatalf("asked TMDB again: %v", f.TMDB.Calls())
	}
}

func TestATMDBFailureLeavesTheTitleOutForNow(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	f.TMDB.Fail("/movie/603", 500)
	got, err := s.App.RW.Titles.Ensure(context.Background(), []string{"movie:603", "movie:604"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["movie:603"]; ok || got["movie:604"] == nil {
		t.Fatalf("%v", got)
	}
}
