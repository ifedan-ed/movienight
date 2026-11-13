package profile_test

import (
	"strings"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
	"movienight/server/internal/testenv/fakes"
)

func TestFirstVisitCreatesThePerson(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	me := s.Client("alice", true).Do(t, "GET", "/api/v1/me", nil).Map(t)
	stats := me["stats"].(map[string]any)
	if me["user"] != "alice" || me["email"] != "alice@example.org" || me["is_admin"] != false || stats["movies"] != 0.0 {
		t.Fatal(me)
	}
	limits := me["limits"].(map[string]any)
	features := me["features"].(map[string]any)
	if limits["text_max_chars"] != 5000.0 || limits["batch_max_items"] != 1000.0 || features["requests"] != true ||
		features["dry_run"] != true || len(me["admin_notices"].([]any)) != 0 {
		t.Fatal(me)
	}
}

func TestOwnerIsAdminAndTheAliasIsTheSamePerson(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	owner := s.Client("owner", true)
	if owner.Do(t, "GET", "/api/v1/me", nil).Map(t)["is_admin"] != true {
		t.Fatal("owner is not an admin")
	}
	owner.Do(t, "POST", "/api/v1/title/movie/603/watchlist", nil)
	me := s.Client("owner-alt", true).Do(t, "GET", "/api/v1/me", nil).Map(t)
	if me["user"] != "owner" || me["stats"].(map[string]any)["watchlist"] != 1.0 {
		t.Fatal(me)
	}
}

func TestAdminsSeeTheNoticesAndOthersDont(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	s.Exec(t, `INSERT INTO app_settings (key, value) VALUES ('status:library_sync',
		'{"ok": false, "problem": "Jellyfin didn''t answer", "at": "2026-10-09T18:30:00+00:00", "kept": 1200, "last_good_at": "2026-10-08T18:30:00+00:00"}')`)
	notices := s.Client("owner", true).Do(t, "GET", "/api/v1/me", nil).Map(t)["admin_notices"].([]any)
	if len(notices) != 1 || !strings.Contains(notices[0].(string), "1,200 titles") {
		t.Fatal(notices)
	}
	if n := s.Client("alice", true).Do(t, "GET", "/api/v1/me", nil).Map(t)["admin_notices"].([]any); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestHistoryPagesWithCardsAndEachEpisodesOwnMarks(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	alice.Do(t, "GET", "/api/v1/me", nil)
	// 45 films on 45 days, and one episode with its own heart and rating.
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT u.id, 'movie', 600 + n, 'Movie ' || (600 + n), timestamptz '2026-09-01T20:00:00Z' + n * interval '1 day', 'manual'
		FROM users u, generate_series(1, 45) n WHERE u.username = 'alice'`)
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source)
		SELECT id, 'episode', 1399, 1, 2, 'Show 1399', '2026-11-01T20:00:00Z', 'manual' FROM users WHERE username = 'alice'`)
	alice.Do(t, "POST", "/api/v1/title/tv/1399/episode/1/2/favorite", nil)
	alice.Do(t, "PUT", "/api/v1/title/tv/1399/episode/1/2/rating", `{"rating": 8}`)
	var first struct {
		Items []struct {
			MediaType       string `json:"media_type"`
			Card            *struct{ Title string }
			EpisodeFavorite bool `json:"episode_favorite"`
			EpisodeRating   *int `json:"episode_rating"`
		}
		More  bool
		Total int
	}
	alice.Do(t, "GET", "/api/v1/history", nil).JSON(t, &first)
	if len(first.Items) != 40 || !first.More || first.Total != 46 {
		t.Fatalf("%d %v %d", len(first.Items), first.More, first.Total)
	}
	ep := first.Items[0]
	if ep.MediaType != "episode" || !ep.EpisodeFavorite || ep.EpisodeRating == nil || *ep.EpisodeRating != 8 || ep.Card.Title != "Show 1399" {
		t.Fatalf("%+v", ep)
	}
	var second struct {
		Items []any
		More  bool
	}
	alice.Do(t, "GET", "/api/v1/history?page=2", nil).JSON(t, &second)
	if len(second.Items) != 6 || second.More {
		t.Fatal(len(second.Items), second.More)
	}
	var shows struct{ Total int }
	alice.Do(t, "GET", "/api/v1/history?media=tv", nil).JSON(t, &shows)
	if shows.Total != 1 {
		t.Fatal(shows.Total)
	}
}

func TestRequestsShowAsCardsAndSayWhatCantBeShown(t *testing.T) {
	s, f := testenv.StartFaked(t, testbase.BaseEnv(t))
	f.Seerr.Reqs = []fakes.JSON{
		{"createdAt": "2026-11-01T10:00:00Z", "status": 2, "requestedBy": fakes.JSON{"displayName": "Bob"},
			"media": fakes.JSON{"tmdbId": 603, "mediaType": "movie", "status": 3,
				"downloadStatus": []fakes.JSON{{"size": 10, "sizeLeft": 5, "status": "downloading"}}}},
		{"createdAt": "2026-11-01T09:00:00Z", "status": 1, "media": fakes.JSON{"mediaType": "movie"}},
	}
	r := s.Client("alice", true).Do(t, "GET", "/api/v1/requests", nil)
	var got struct {
		Items []struct {
			Key           string
			RequestStatus string `json:"request_status"`
			MediaStatus   string `json:"media_status"`
			RequestedBy   string `json:"requested_by"`
			Progress      struct{ Percent int }
		}
		Total    int
		NotShown int `json:"not_shown"`
	}
	r.JSON(t, &got)
	if len(got.Items) != 1 || got.Items[0].RequestStatus != "approved" || got.Items[0].MediaStatus != "processing" ||
		got.Items[0].RequestedBy != "Bob" || got.Items[0].Progress.Percent != 50 || got.NotShown != 1 || got.Total != 2 {
		t.Fatal(r.Body)
	}
	f.Seerr.Fail("GET /api/v1/request", 500)
	if r := s.Client("alice", true).Do(t, "GET", "/api/v1/requests", nil); r.StatusCode != 502 || r.Map(t)["detail"] != "Couldn't reach Seerr" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestTheRatePromptAsksOnceAndNotNowIsKept(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	if r := alice.Do(t, "GET", "/api/v1/rate-prompt", nil); r.Body != `{"item":null}` {
		t.Fatal(r.Body)
	}
	alice.Do(t, "POST", "/api/v1/title/movie/603/watched", `{}`)
	item := alice.Do(t, "GET", "/api/v1/rate-prompt", nil).Map(t)["item"].(map[string]any)
	if item["key"] != "movie:603" || item["title"] != "Movie 603" || item["poster"] != "/p603.jpg" {
		t.Fatal(item)
	}
	alice.Do(t, "POST", "/api/v1/rate-prompt/dismiss", `{"key": "movie:603"}`)
	if r := alice.Do(t, "GET", "/api/v1/rate-prompt", nil); r.Body != `{"item":null}` {
		t.Fatal(r.Body)
	}
	if r := alice.Do(t, "POST", "/api/v1/rate-prompt/dismiss", `{"key": "nonsense"}`); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
}
