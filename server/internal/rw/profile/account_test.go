package profile_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func TestPlayersOfYourOwnStopAt20And30Ticked(t *testing.T) {
	// Limits the owner keeps (docs/limits.md, "Limits the owner keeps").
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	custom := make([]string, 0, 21)
	for n := 1; n <= 21; n++ {
		custom = append(custom, fmt.Sprintf(`{"name": "Player %d", "template": "p%d-{url_raw}"}`, n, n))
	}
	r := alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": [`+strings.Join(custom, ",")+`]}`)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "You can have up to 20 players of your own." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	enabled := []string{}
	for n := 1; n <= 20; n++ {
		enabled = append(enabled, fmt.Sprintf(`"custom%d"`, n))
	}
	twenty := `[` + strings.Join(custom[:20], ",") + `]`
	r = alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": `+twenty+`, "enabled": [`+strings.Join(enabled, ",")+strings.Repeat(`, "vlc"`, 11)+`]}`)
	if r.StatusCode != 422 || r.Map(t)["detail"] != "Up to 30 players can be ticked." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	r = alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": `+twenty+`, "enabled": ["vlc", `+strings.Join(enabled, ",")+`]}`)
	var got struct {
		Players []struct {
			ID      string
			Builtin bool
			Enabled bool
		}
		DefaultPlayer string `json:"default_player"`
	}
	r.JSON(t, &got)
	own, on := 0, 0
	for _, p := range got.Players {
		if !p.Builtin {
			own++
		}
		if p.Enabled {
			on++
		}
	}
	if r.StatusCode != 200 || own != 20 || on != 21 || got.DefaultPlayer != "vlc" {
		t.Fatalf("%d own %d on %d %s", r.StatusCode, own, on, r.Body)
	}
}

func TestAPlayerLinkMustBeAnAppLink(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	for tmpl, want := range map[string]string{
		"javascript:alert({url})": "A player link starts with the app's scheme, like vlc:// or infuse://",
		"vlc://{nope}{url}":       "Unknown placeholder: {nope}",
		"vlc://nothing":           "The link needs {url} (the video) somewhere",
	} {
		r := alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": [{"name": "Mine", "template": "`+tmpl+`"}]}`)
		if r.StatusCode != 422 || r.Map(t)["detail"] != want {
			t.Errorf("%s: %d %s", tmpl, r.StatusCode, r.Body)
		}
	}
	// A refused link changes nothing that was saved before.
	alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": [{"name": "Mine", "template": "nplayer-{url_raw}"}], "default_player": "custom1"}`)
	alice.Do(t, "PUT", "/api/v1/me/players", `{"custom": [{"name": "Bad", "template": "data:{url}"}]}`)
	r := alice.Do(t, "GET", "/api/v1/me/players", nil)
	if !strings.Contains(r.Body, `"name":"Mine"`) || r.Map(t)["default_player"] != "custom1" {
		t.Fatal(r.Body)
	}
}

func TestEmailsAreOnUnlessSwitchedOffAndTheAddressIsChecked(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	r := alice.Do(t, "GET", "/api/v1/me/emails", nil)
	if r.Body != `{"weekly":true,"monthly":true,"yearly":true,"address":"alice@example.org"}` {
		t.Fatal(r.Body)
	}
	if r := alice.Do(t, "PUT", "/api/v1/me/emails", `{"weekly": false, "address": "not an address"}`); r.StatusCode != 422 ||
		r.Map(t)["detail"] != "That doesn't look like an email address" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// Nothing of the refused change was kept.
	if r := alice.Do(t, "GET", "/api/v1/me/emails", nil); !strings.Contains(r.Body, `"weekly":true`) {
		t.Fatal(r.Body)
	}
	r = alice.Do(t, "PUT", "/api/v1/me/emails", `{"monthly": false, "address": " me@home.test "}`)
	if r.Body != `{"weekly":true,"monthly":false,"yearly":true,"address":"me@home.test"}` {
		t.Fatal(r.Body)
	}
	// An empty address goes back to the account's.
	if r := alice.Do(t, "PUT", "/api/v1/me/emails", `{"address": ""}`); !strings.Contains(r.Body, `"address":"alice@example.org"`) {
		t.Fatal(r.Body)
	}
}

func TestTheScrobbleTokenIsShownOnceAndReplacesTheOldOne(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	first := alice.Do(t, "POST", "/api/v1/me/scrobble-token", nil).Map(t)["token"].(string)
	second := alice.Do(t, "POST", "/api/v1/me/scrobble-token", nil).Map(t)["token"].(string)
	if !strings.HasPrefix(first, "mn_") || first == second {
		t.Fatal(first, second)
	}
	var hash *string
	_ = s.Pool.QueryRow(t.Context(), `SELECT scrobble_token_hash FROM users WHERE username = 'alice'`).Scan(&hash)
	if hash == nil || strings.Contains(*hash, second) {
		t.Fatal("the token must be kept hashed")
	}
	alice.Do(t, "DELETE", "/api/v1/me/scrobble-token", nil)
	_ = s.Pool.QueryRow(t.Context(), `SELECT scrobble_token_hash FROM users WHERE username = 'alice'`).Scan(&hash)
	if hash != nil {
		t.Fatal("not dropped")
	}
}

func TestConnectingJellyfinWithQuickConnectOrPassword(t *testing.T) {
	// DRY_RUN off: the old session's sign-out is checked, and DRY_RUN holds
	// it back (TestDryRunChangesNothingOutside).
	base := testbase.BaseEnv(t)
	base["DRY_RUN"] = "false"
	s, f := testenv.StartFaked(t, base)
	alice, bob := s.Client("alice", true), s.Client("bob", true)
	r := alice.Do(t, "GET", "/api/v1/me/jellyfin", nil)
	if m := r.Map(t); m["linked"] != false || m["quick_connect"] != true || m["server"] != "https://jf.test" {
		t.Fatal(r.Body)
	}
	start := alice.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect", nil).Map(t)
	if start["code"] != "123456" {
		t.Fatal(start)
	}
	if r := alice.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect/check", `{"secret": "secret-123456"}`); r.Map(t)["linked"] != false {
		t.Fatal(r.Body)
	}
	f.Jellyfin.QCReady = true
	r = alice.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect/check", `{"secret": "secret-123456"}`)
	if m := r.Map(t); m["linked"] != true || m["user_name"] != "alice-jf" {
		t.Fatal(r.Body)
	}
	var enc string
	_ = s.Pool.QueryRow(t.Context(), `SELECT jellyfin_token_enc FROM users WHERE username = 'alice'`).Scan(&enc)
	if enc == "" || strings.Contains(enc, "user-token") {
		t.Fatal("the Jellyfin token must be kept encrypted")
	}
	// The same Jellyfin account can't belong to bob too.
	if r := bob.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect/check", `{"secret": "secret-123456"}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// A person's own code signs in their own account (the fake Jellyfin gives each
	// code its own); so two people connecting in turn don't collide.
	carol := s.Client("carol", true)
	own := carol.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect", nil).Map(t)["secret"].(string)
	if r := carol.Do(t, "POST", "/api/v1/me/jellyfin/quick-connect/check", `{"secret": "`+own+`"}`); r.Map(t)["linked"] != true || r.Map(t)["user_name"] != "jf-user-2" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// A wrong password is a refused request (422), never a 401 (that would
	// sign the phone out of Movie Night).
	if r := bob.Do(t, "POST", "/api/v1/me/jellyfin/password", `{"username": "bob", "password": "wrong"}`); r.StatusCode != 422 ||
		r.Map(t)["detail"] != "Wrong Jellyfin username or password" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := bob.Do(t, "POST", "/api/v1/me/jellyfin/password", `{"username": "bob", "password": "right"}`); r.Map(t)["user_name"] != "bob" {
		t.Fatal(r.Body)
	}
	if r := alice.Do(t, "DELETE", "/api/v1/me/jellyfin", nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if r := alice.Do(t, "GET", "/api/v1/me/jellyfin", nil); r.Map(t)["linked"] != false {
		t.Fatal(r.Body)
	}
	if f.Jellyfin.Count("POST /Sessions/Logout") != 1 {
		t.Fatal("the old Jellyfin session was not signed out")
	}
}

func TestPlaysNoticesFollowTheLink(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	alice.Do(t, "POST", "/api/v1/me/jellyfin/password", `{"username": "alice", "password": "right"}`)
	s.Exec(t, `INSERT INTO app_settings (key, value) SELECT 'status:plays:' || id, '{"ok": false, "status": 404, "problem": "HTTP 404", "at": "2026-10-09T18:30:00+00:00", "undated": 2}' FROM users WHERE username = 'alice'`)
	n, _ := alice.Do(t, "GET", "/api/v1/me/jellyfin", nil).Map(t)["plays_notice"].(string)
	if !strings.Contains(n, "connect it again") || !strings.Contains(n, "Oct 9, 14:30") || strings.Contains(n, "no date") {
		t.Fatal(n)
	}
	alice.Do(t, "DELETE", "/api/v1/me/jellyfin", nil)
	var left int
	_ = s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM app_settings WHERE key LIKE 'status:plays:%'`).Scan(&left)
	if left != 0 {
		t.Fatal("an unlinked person's plays status stays")
	}
}

// Plays Jellyfin has without a date are listed by name, with the title to
// link to. Each one hidden (added here with a date, or not watched) stays
// hidden through later reads; only a listed play can be hidden.
func TestUndatedPlaysAreListedUntilEachIsDealtWith(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	alice.Do(t, "POST", "/api/v1/me/jellyfin/password", `{"username": "alice", "password": "right"}`)
	read := `{"ok": true, "at": "2026-11-20T18:30:00+00:00", "undated": 3, "undated_items": [
		{"id": "aa11", "media_type": "movie", "tmdb_id": 345, "title": "Eyes Wide Shut"},
		{"id": "bb22", "media_type": "movie", "tmdb_id": 1003, "title": "In Your Absence"},
		{"id": "cc33", "media_type": "tv", "tmdb_id": 1399, "title": "Show", "season": 1, "episode": 2, "episode_title": "Two"}]}`
	s.Exec(t, `INSERT INTO app_settings (key, value) SELECT 'status:plays:' || id, $1 FROM users WHERE username = 'alice'`, read)
	listed := func() []string {
		t.Helper()
		var out struct {
			Notice  *string          `json:"plays_notice"`
			Undated []map[string]any `json:"plays_undated"`
			More    int              `json:"plays_undated_more"`
		}
		r := alice.Do(t, "GET", "/api/v1/me/jellyfin", nil)
		if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Notice != nil || out.More != 0 {
			t.Fatalf("a good read with undated plays: %s", r.Body)
		}
		var ids []string
		for _, u := range out.Undated {
			ids = append(ids, fmt.Sprint(u["id"], " ", u["media_type"], " ", u["tmdb_id"], " ", u["title"]))
		}
		return ids
	}
	if got := strings.Join(listed(), ", "); got != "aa11 movie 345 Eyes Wide Shut, bb22 movie 1003 In Your Absence, cc33 tv 1399 Show" {
		t.Fatal(got)
	}
	// Profile reads the same list from /me.
	if me := alice.Do(t, "GET", "/api/v1/me", nil).Map(t); len(me["jellyfin"].(map[string]any)["plays_undated"].([]any)) != 3 {
		t.Fatal(me["jellyfin"])
	}
	hide := func(id string) {
		t.Helper()
		if r := alice.Do(t, "POST", "/api/v1/me/jellyfin/undated/"+id+"/hide", nil); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.Body)
		}
	}
	hide("aa11")
	hide("cc33")
	if r := alice.Do(t, "POST", "/api/v1/me/jellyfin/undated/zz99/hide", nil); r.StatusCode != 404 {
		t.Fatal("a play that isn't listed was hidden", r.StatusCode)
	}
	// The next read writes the status again, with the same plays.
	s.Exec(t, `UPDATE app_settings SET value = $1 WHERE key LIKE 'status:plays:%'`, read)
	if got := strings.Join(listed(), ", "); got != "bb22 movie 1003 In Your Absence" {
		t.Fatal(got)
	}
	hide("bb22")
	if got := listed(); len(got) != 0 {
		t.Fatal(got)
	}
	// Someone else's hidden plays are their own.
	bob := s.Client("bob", true)
	if r := bob.Do(t, "POST", "/api/v1/me/jellyfin/undated/bb22/hide", nil); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestTheExportHoldsEverythingKept(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	alice.Do(t, "GET", "/api/v1/me/emails", nil) // first visit makes alice
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT id, 'movie', 603, 'Movie 603', '2026-10-01T20:00:00Z', 'manual' FROM users WHERE username = 'alice'`)
	s.Exec(t, `INSERT INTO episode_favorites (user_id, tmdb_id, season, episode, added_at, source)
		SELECT id, 1399, 1, 2, now(), 'app' FROM users WHERE username = 'alice'`)
	s.Exec(t, `INSERT INTO lists (id, name, kind, source, created_at, owner_id) SELECT 5, 'Mine', 'list', 'app', now(), id FROM users WHERE username = 'alice'`)
	s.Exec(t, `INSERT INTO list_items (list_id, media_type, tmdb_id, position, added_at) VALUES (5, 'tv', 1399, 2, now()), (5, 'movie', 603, 1, now())`)
	r := alice.Do(t, "GET", "/api/v1/export", nil)
	if r.Header.Get("Content-Disposition") != "attachment; filename=movienight-export.json" {
		t.Fatal(r.Header)
	}
	var got struct {
		User             string
		History          []map[string]any
		EpisodeFavorites []map[string]any `json:"episode_favorites"`
		Lists            []struct {
			Name  string
			Items []map[string]any
		}
		Watchlist []any
	}
	r.JSON(t, &got)
	if got.User != "alice" || len(got.History) != 1 || got.EpisodeFavorites[0]["episode"] != 2.0 ||
		got.Lists[0].Items[0]["tmdb_id"] != 603.0 || got.Watchlist == nil {
		t.Fatal(r.Body)
	}
}
