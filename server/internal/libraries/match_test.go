package libraries

import (
	"context"
	"errors"
	"testing"

	"movienight/server/internal/clients"
	"movienight/server/internal/media/naming"
)

// fakeFinder answers searches from a fixed list and counts the calls.
type fakeFinder struct {
	movies, shows []clients.JSON
	external      map[string]clients.JSON
	calls         int
	fail          bool
}

func (f *fakeFinder) SearchMovie(_ context.Context, _ string, _ int) ([]clients.JSON, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("down")
	}
	return f.movies, nil
}

func (f *fakeFinder) SearchTV(_ context.Context, _ string, _ int) ([]clients.JSON, error) {
	f.calls++
	return f.shows, nil
}

func (f *fakeFinder) FindExternal(_ context.Context, id, _ string) (clients.JSON, error) {
	f.calls++
	return f.external[id], nil
}

func hit(id int, title, date string, votes int) clients.JSON {
	return clients.JSON{"id": float64(id), "title": title, "release_date": date, "vote_count": float64(votes)}
}

func TestMatchPicksTheRightTitle(t *testing.T) {
	f := &fakeFinder{movies: []clients.JSON{
		hit(1, "Dune", "1984-12-14", 3000), hit(2, "Dune", "2021-09-15", 12000), hit(3, "Dune: Part Two", "2024-02-27", 6000),
	}}
	m := newMatcher(f, nil)
	ctx := context.Background()
	for _, c := range []struct {
		p    naming.Parsed
		want int
	}{
		{naming.Parsed{Title: "Dune", Year: 1984}, 1},
		{naming.Parsed{Title: "Dune", Year: 2021}, 2},
		{naming.Parsed{Title: "Dune", Year: 2020}, 2}, // a year off: still the 2021 film
		{naming.Parsed{Title: "Dune"}, 2},             // no year: the one more people know
		{naming.Parsed{Title: "Dune", TMDB: 438631}, 438631},
	} {
		got, problem, err := m.match(ctx, c.p, naming.Movies)
		if err != nil || problem != "" || got.TMDBID != c.want {
			t.Errorf("%+v: got %+v %q %v, want %d", c.p, got, problem, err, c.want)
		}
	}
	// A year far off, or other words: no match, said plainly.
	if _, problem, _ := m.match(ctx, naming.Parsed{Title: "Dune", Year: 1950}, naming.Movies); problem == "" {
		t.Error("a film from 1950 matched")
	}
	if _, problem, _ := m.match(ctx, naming.Parsed{Title: "Arrival"}, naming.Movies); problem != "No match on TMDB for “Arrival”" {
		t.Errorf("problem %q", problem)
	}
}

func TestMatchAsksOncePerName(t *testing.T) {
	f := &fakeFinder{shows: []clients.JSON{{"id": float64(1399), "name": "Game of Thrones", "first_air_date": "2011-04-17"}}}
	m := newMatcher(f, nil)
	for range 10 {
		got, _, err := m.match(context.Background(), naming.Parsed{Title: "Game of Thrones"}, naming.Shows)
		if err != nil || got.TMDBID != 1399 || got.By != "search" {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("%d calls for ten episodes of one show", f.calls)
	}
}

func TestMatchByOtherIDsAndByHand(t *testing.T) {
	f := &fakeFinder{external: map[string]clients.JSON{
		"tt0137523": {"id": float64(550), "media_type": "movie"},
		"81189":     {"id": float64(1396), "media_type": "tv"},
	}}
	m := newMatcher(f, map[string]int{handKey("Home Video", 0): 42})
	ctx := context.Background()
	if got, _, _ := m.match(ctx, naming.Parsed{Title: "Fight Club", IMDb: "tt0137523"}, naming.Movies); got.TMDBID != 550 || got.By != "name" {
		t.Errorf("imdb: %+v", got)
	}
	if got, _, _ := m.match(ctx, naming.Parsed{Title: "Breaking Bad", TVDB: 81189}, naming.Shows); got.TMDBID != 1396 {
		t.Errorf("tvdb: %+v", got)
	}
	if got, _, _ := m.match(ctx, naming.Parsed{Title: "Home Video"}, naming.Movies); got.TMDBID != 42 || got.By != "hand" {
		t.Errorf("hand: %+v", got)
	}
}

func TestMatchTMDBDownIsAnErrorNotAProblem(t *testing.T) {
	m := newMatcher(&fakeFinder{fail: true}, nil)
	if _, problem, err := m.match(context.Background(), naming.Parsed{Title: "X"}, naming.Movies); err == nil || problem != "" {
		t.Fatalf("problem %q err %v", problem, err)
	}
}
