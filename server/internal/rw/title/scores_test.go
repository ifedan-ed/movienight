package title_test

// Outside scores on a title page.

import (
	"net/http"
	"strings"
	"testing"

	"movienight/server/internal/testenv/fakes"
)

func scores(t *testing.T, e *env, path string) (map[string]any, any) {
	t.Helper()
	r := ok(t, e.alice, "GET", path)
	s, _ := r["scores"].(map[string]any)
	return s, r["note"]
}

// staleScores: a week on, the kept scores are due to be asked for again.
func staleScores(t *testing.T, e *env) {
	e.s.Exec(t, `UPDATE title_extras SET fetched_at = now() - interval '8 days'`)
}

func TestScoresComeFromMDBList(t *testing.T) {
	e := start(t)
	s, note := scores(t, e, "/api/v1/title/movie/603/scores")
	if s["imdb_rating"] != 8.7 || s["rt_critics"] != 83.0 || s["rt_url"] != "https://www.rottentomatoes.com/m/x" ||
		s["trakt_rating"] != 8.7 || s["mdblist_score"] != 82.0 || note != nil {
		t.Fatal(s, note)
	}
	// Kept a week: not asked again.
	e.f.MDBList.Reset()
	scores(t, e, "/api/v1/title/movie/603/scores")
	if n := len(e.f.MDBList.Calls()); n != 0 {
		t.Fatal(e.f.MDBList.Calls())
	}
}

func TestIMDbsOwnRatingWinsAndNeedsNoMDBList(t *testing.T) {
	e := start(t)
	e.s.Exec(t, `INSERT INTO imdb_ratings (tconst, rating, votes) VALUES ('tt0000603', 7.9, 123456)`)
	e.f.TMDB.Override("/movie/603", func(w http.ResponseWriter, r *http.Request) {
		m := fakes.Movie(603)
		m["imdb_id"] = "tt0000603"
		fakes.Reply(w, m)
	})
	d := ok(t, e.alice, "GET", "/api/v1/title/movie/603")["scores"].(map[string]any)
	if d["imdb_rating"] != 7.9 || d["imdb_votes"] != 123456.0 {
		t.Fatal(d)
	}
	s, _ := scores(t, e, "/api/v1/title/movie/603/scores")
	if s["imdb_rating"] != 7.9 || s["rt_critics"] != 83.0 {
		t.Fatal(s)
	}
	// With MDBList down and nothing kept, IMDb's own rating still shows.
	e.f.MDBList.Fail("/tmdb/movie/604", 503)
	e.s.Exec(t, `INSERT INTO titles (key, media_type, tmdb_id, title, imdb_id, detailed, fetched_at)
		VALUES ('movie:604', 'movie', 604, 'Movie 604', 'tt0000603', true, now())`)
	s, _ = scores(t, e, "/api/v1/title/movie/604/scores")
	if s["imdb_rating"] != 7.9 || s["rt_critics"] != nil {
		t.Fatal(s)
	}
}

func TestWhenMDBListIsDownTheKeptScoresShowWithANote(t *testing.T) {
	e := start(t)
	scores(t, e, "/api/v1/title/movie/603/scores")
	staleScores(t, e)
	e.f.MDBList.Fail("/tmdb/movie/603", 503)
	s, note := scores(t, e, "/api/v1/title/movie/603/scores")
	if s["rt_critics"] != 83.0 || s["imdb_rating"] != 8.7 {
		t.Fatal(s)
	}
	if n, _ := note.(string); !strings.Contains(n, "didn't answer") || !strings.Contains(n, "showing the ones kept from") {
		t.Fatal(note)
	}
}

func TestAScoreMDBListLeavesOutIsKept(t *testing.T) {
	e := start(t)
	scores(t, e, "/api/v1/title/movie/603/scores")
	staleScores(t, e)
	e.f.MDBList.Override("/tmdb/movie/603", func(w http.ResponseWriter, _ *http.Request) {
		fakes.Reply(w, fakes.JSON{"score": 80, "ratings": []fakes.JSON{{"source": "imdb", "value": 8.8, "votes": 120}}})
	})
	s, _ := scores(t, e, "/api/v1/title/movie/603/scores")
	if s["imdb_rating"] != 8.8 || s["mdblist_score"] != 80.0 || s["rt_critics"] != 83.0 || s["rt_url"] != "https://www.rottentomatoes.com/m/x" {
		t.Fatal(s)
	}
}

func TestWhenMDBListRefusesThePageSaysWhy(t *testing.T) {
	e := start(t)
	scores(t, e, "/api/v1/title/movie/603/scores")
	staleScores(t, e)
	e.f.MDBList.Fail("/tmdb/movie/603", 429)
	s, note := scores(t, e, "/api/v1/title/movie/603/scores")
	n, _ := note.(string)
	if s["rt_critics"] != 83.0 || !strings.Contains(n, "refused") || !strings.Contains(n, "showing the ones kept from") {
		t.Fatal(s, note)
	}
	// Never fetched before: none kept (MDBList is left alone a while after a refusal).
	_, note = scores(t, e, "/api/v1/title/movie/604/scores")
	if n, _ := note.(string); !strings.Contains(n, "refused") || !strings.Contains(n, "none kept") {
		t.Fatal(note)
	}
}

func TestALibraryTitleWithoutRottenTomatoesUsesJellyfins(t *testing.T) {
	e := start(t)
	e.s.SeedLibrary(t)
	e.f.MDBList.Override("/tmdb/movie/603", func(w http.ResponseWriter, _ *http.Request) {
		fakes.Reply(w, fakes.JSON{"score": 70, "ratings": []fakes.JSON{}})
	})
	e.f.Jellyfin.Override("/Users/"+strings.Repeat("c", 32)+"/Items/"+strings.Repeat("a", 32), func(w http.ResponseWriter, _ *http.Request) {
		fakes.Reply(w, fakes.JSON{"Id": strings.Repeat("a", 32), "CriticRating": 91})
	})
	s, _ := scores(t, e, "/api/v1/title/movie/603/scores")
	if s["rt_critics"] != 91.0 || s["mdblist_score"] != 70.0 {
		t.Fatal(s)
	}
}
