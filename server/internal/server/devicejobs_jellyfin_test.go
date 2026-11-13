package server_test

// Jellyfin into Movie Night: the Webhook plugin (debounced through the
// queue a worker serves), the Movie Night plugin's events, the plays sync
// and the library sync.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/jfsync"
	"movienight/server/internal/status"
	"movienight/server/internal/tasks"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func hook(s *testenv.Server, token string) *testenv.Client {
	c := s.Client("", false)
	c.Headers.Set("X-MovieNight-Token", token)
	return c
}

func played(id string, tmdb int, when string) map[string]any {
	m := movie(id, tmdb)
	m["UserData"] = map[string]any{"LastPlayedDate": when}
	return m
}

func TestTheWebhookNeedsItsSecret(t *testing.T) {
	env := testbase.BaseEnv(t)
	delete(env, "JELLYFIN_WEBHOOK_SECRET")
	s, _ := startUp(t, env)
	r := want(t, hook(s, "anything").Do(t, "POST", "/api/v1/scrobble/jellyfin", `{}`), 503)
	if !strings.Contains(r.Body, "JELLYFIN_WEBHOOK_SECRET") {
		t.Fatal(r.Body)
	}
	want(t, hook(s, "anything").Do(t, "POST", "/api/v1/scrobble/jellyfin/events",
		`{"event":"start","user_id":"`+strings.Repeat("a", 32)+`","item":{"id":"`+strings.Repeat("b", 32)+`","type":"Movie"}}`), 503)
}

func TestTheWebhookAsksForOneSyncPerWatchAndAWorkerMakesIt(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	when := "2026-11-10T20:15:03.1234567Z"
	u.set(func(u *up) { u.played[jf] = []map[string]any{played("a1"+strings.Repeat("0", 30), 603, when)} })

	want(t, hook(s, "wrong").Do(t, "POST", "/api/v1/scrobble/jellyfin", `{}`), 401)
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", `not json`), 400)
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", `[1]`), 400)
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", ``), 202) // an empty body is {}
	queue := tasks.New(s.KV, jfsync.QueueName)
	pending := func() bool {
		ok, err := queue.Pending(context.Background(), fmt.Sprint(alice))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Events that cannot mean a play, and Jellyfin users nobody links, ask
	// for nothing.
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", `{"NotificationType":"ItemAdded","UserId":"`+jf+`"}`), 202)
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", `{"NotificationType":"PlaybackStop","UserId":"`+strings.Repeat("9", 32)+`"}`), 202)
	if pending() {
		t.Fatal("an unrelated event asked for a sync")
	}
	// Three events for one watch (the plugin's content type varies, and its
	// ids come with dashes and capitals): one sync.
	dashed := strings.ToUpper(jf[:8] + "-" + jf[8:12] + "-" + jf[12:16] + "-" + jf[16:20] + "-" + jf[20:])
	for _, ev := range []string{"PlaybackStop", "UserDataSaved", "ItemMarkedPlayed"} {
		c := hook(s, "hook-secret")
		c.Headers.Set("Content-Type", "text/plain")
		want(t, c.Do(t, "POST", "/api/v1/scrobble/jellyfin", `{"NotificationType":"`+ev+`","UserId":"`+dashed+`"}`), 202)
	}
	if !pending() {
		t.Fatal("no sync asked for")
	}
	if n, _ := s.KV.Do(context.Background(), s.KV.B().Zcard().Key("task:"+jfsync.QueueName).Build()).AsInt64(); n != 1 {
		t.Fatalf("%d queued, want one", n)
	}
	if c := count(t, s, "SELECT count(*) FROM history"); c != 0 {
		t.Fatal("the API synced by itself")
	}
	// Two workers serve the queue; the play lands once, one read.
	ctx, cancel := context.WithTimeout(context.Background(), jfsync.WebhookDelay+4*time.Second)
	defer cancel()
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { s.App.RW.JFSync.RunQueued(ctx); done <- struct{}{} }()
	}
	deadline := time.Now().Add(jfsync.WebhookDelay + 3*time.Second)
	for time.Now().Before(deadline) && count(t, s, "SELECT count(*) FROM history") == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	<-done
	if c := count(t, s, "SELECT count(*) FROM history WHERE source = 'jellyfin' AND tmdb_id = 603"); c != 1 {
		t.Fatalf("%d plays recorded", c)
	}
	u.set(func(u *up) {
		if u.playedRead != 1 {
			t.Errorf("%d Jellyfin reads for one watch", u.playedRead)
		}
	})
	if pending() {
		t.Fatal("the request was not released")
	}
	// A later event asks again.
	want(t, hook(s, "hook-secret").Do(t, "POST", "/api/v1/scrobble/jellyfin", `{"Event":"MarkPlayed","UserId":"`+jf+`"}`), 202)
	if !pending() {
		t.Fatal("a new event after the sync asked for nothing")
	}
}

// pluginEvent is one event from the Movie Night plugin.
func pluginEvent(t *testing.T, s *testenv.Server, token string, ev map[string]any) *testenv.Response {
	t.Helper()
	b, _ := json.Marshal(ev)
	return hook(s, token).Do(t, "POST", "/api/v1/scrobble/jellyfin/events", string(b))
}

var (
	pluginMovie   = map[string]any{"id": strings.Repeat("a1", 16), "type": "Movie", "name": "Movie 603", "tmdb": "603", "runtime_s": 6000}
	pluginEpisode = map[string]any{"id": strings.Repeat("e5", 16), "type": "Episode", "name": "Episode 5", "series_id": strings.Repeat("51", 16),
		"series_name": "Show 1399", "season": 1, "episode": 5, "series_tmdb": "1399", "runtime_s": 3300}
)

func ev(event, user string, item map[string]any, extra ...any) map[string]any {
	m := map[string]any{"event": event, "user_id": user, "item": item}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

type progress struct {
	State, Client string
	Position      int
}

func progressOf(t *testing.T, s *testenv.Server, key string) *progress {
	t.Helper()
	var p progress
	var client *string
	err := s.Pool.QueryRow(context.Background(), "SELECT state, client, position_s FROM watch_progress WHERE key = $1", key).
		Scan(&p.State, &client, &p.Position)
	if err != nil {
		return nil
	}
	if client != nil {
		p.Client = *client
	}
	return &p
}

func TestPlaybackEventsKeepWatchProgress(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	_, jf := link(t, s, "alice")
	want(t, pluginEvent(t, s, "hook-secret", ev("start", jf, pluginMovie, "position_s", 0, "client", "Kodi", "device", "Living room")), 202)
	want(t, pluginEvent(t, s, "hook-secret", ev("progress", jf, pluginMovie, "position_s", 1500, "client", "Kodi")), 202)
	if p := progressOf(t, s, "movie:603"); p == nil || p.Position != 1500 || p.State != "playing" || p.Client != "Kodi" {
		t.Fatalf("%+v", p)
	}
	pluginEvent(t, s, "hook-secret", ev("pause", jf, pluginMovie, "position_s", 1600))
	if p := progressOf(t, s, "movie:603"); p.State != "paused" {
		t.Fatalf("%+v", p)
	}
	pluginEvent(t, s, "hook-secret", ev("stop", jf, pluginMovie, "position_s", 1620))
	if p := progressOf(t, s, "movie:603"); p.State != "stopped" || p.Position != 1620 {
		t.Fatalf("%+v", p)
	}
	// Past 90 %: finished, the progress goes.
	pluginEvent(t, s, "hook-secret", ev("stop", jf, pluginMovie, "position_s", 5900))
	if p := progressOf(t, s, "movie:603"); p != nil {
		t.Fatalf("%+v", p)
	}
}

func TestPlayedRecordsTheWatchOnceWhicheverSeesItFirst(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	when := "2026-10-04T20:15:30.1234567Z"
	for range 3 { // Jellyfin can report it more than once
		want(t, pluginEvent(t, s, "hook-secret", ev("played", jf, pluginMovie, "last_played", when)), 202)
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE source = 'jellyfin' AND tmdb_id = 603"); n != 1 {
		t.Fatal(n)
	}
	// The plays sync later reports the same play: still one watch.
	u.set(func(u *up) { u.played[jf] = []map[string]any{played(pluginMovie["id"].(string), 603, when)} })
	res, err := s.App.RW.JFSync.Plays(context.Background(), alice, jf)
	if err != nil || res.Added != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := count(t, s, "SELECT count(*) FROM history"); n != 1 {
		t.Fatal(n)
	}
}

func TestEpisodeEventsByTheShowsIDAndUnplayedKeepsHistory(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	_, jf := link(t, s, "alice")
	pluginEvent(t, s, "hook-secret", ev("progress", jf, pluginEpisode, "position_s", 600, "client", "Swiftfin"))
	var season, episode int
	var epTitle string
	if err := s.Pool.QueryRow(context.Background(), "SELECT season, episode, episode_title FROM watch_progress WHERE key = 'tv:1399:1:5'").
		Scan(&season, &episode, &epTitle); err != nil || season != 1 || episode != 5 || epTitle != "Episode 5" {
		t.Fatal(season, episode, epTitle, err)
	}
	pluginEvent(t, s, "hook-secret", ev("played", jf, pluginEpisode, "last_played", "2026-10-04T21:00:00.0000000Z"))
	if n := count(t, s, "SELECT count(*) FROM history WHERE media_type = 'episode' AND tmdb_id = 1399 AND season = 1 AND episode = 5"); n != 1 {
		t.Fatal(n)
	}
	pluginEvent(t, s, "hook-secret", ev("progress", jf, pluginEpisode, "position_s", 60))
	pluginEvent(t, s, "hook-secret", ev("unplayed", jf, pluginEpisode))
	if n := count(t, s, "SELECT count(*) FROM watch_progress"); n != 0 {
		t.Fatal(n)
	}
	if n := count(t, s, "SELECT count(*) FROM history"); n != 1 {
		t.Fatal(n)
	}
	// The show found through the library when the plugin sends no TMDB id.
	exec(t, s, `INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, synced_at) VALUES ($1, 'tv', 1400, 'Show 1400', now())`,
		strings.Repeat("52", 16))
	noIDs := map[string]any{"id": strings.Repeat("e6", 16), "type": "Episode", "name": "E1", "season": 2, "episode": 1,
		"series_id": strings.ToUpper(strings.Repeat("52", 16))}
	pluginEvent(t, s, "hook-secret", ev("progress", jf, noIDs, "position_s", 10))
	if progressOf(t, s, "tv:1400:2:1") == nil {
		t.Fatal("the show was not found through the library")
	}
}

func TestAWatchMarkedHereIsClaimedNotDoubled(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	marked := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	exec(t, s, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source) VALUES ($1, 'movie', 603, 'Movie 603', $2, 'manual')`,
		alice, marked)
	pluginEvent(t, s, "hook-secret", ev("played", jf, pluginMovie, "last_played", marked.Add(40*time.Minute).Format(time.RFC3339Nano)))
	if n := count(t, s, "SELECT count(*) FROM history WHERE tmdb_id = 603"); n != 1 {
		t.Fatal(n)
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE tmdb_id = 603 AND source = 'jellyfin' AND source_ref IS NOT NULL"); n != 1 {
		t.Fatal(n)
	}
}

func TestPluginEventsNeedTheSecretAndAKnownPerson(t *testing.T) {
	s, _ := startUp(t, testbase.BaseEnv(t))
	_, jf := link(t, s, "alice")
	want(t, pluginEvent(t, s, "wrong", ev("start", jf, pluginMovie)), 401)
	want(t, pluginEvent(t, s, "hook-secret", ev("start", strings.Repeat("0", 32), pluginMovie)), 202)
	noIDs := map[string]any{"id": strings.Repeat("a1", 16), "type": "Movie", "name": "Movie", "imdb": "tt9999999"}
	want(t, pluginEvent(t, s, "hook-secret", ev("start", jf, noIDs)), 202) // no TMDB id: nothing kept
	want(t, pluginEvent(t, s, "hook-secret", ev("rewind", jf, pluginMovie)), 422)
	if n := count(t, s, "SELECT count(*) FROM watch_progress"); n != 0 {
		t.Fatal(n)
	}
}

// ---------------------------------------------------------------------------
// The plays sync
// ---------------------------------------------------------------------------

func TestPythonPlayRefsReadTheSame(t *testing.T) {
	for in, out := range map[string]string{
		"2026-10-04T20:15:30.1234567Z":      "2026-10-04T20:15:30.123456+00:00",
		"2026-10-04T20:15:30.0000000Z":      "2026-10-04T20:15:30+00:00",
		"2026-10-04T20:15:30Z":              "2026-10-04T20:15:30+00:00",
		"2026-10-04T22:15:30.5+02:00":       "2026-10-04T22:15:30.500000+02:00",
		"2026-10-04T20:15:30.000001000000Z": "2026-10-04T20:15:30.000001+00:00",
	} {
		tm, ok := jfsync.ParseTime(in)
		if !ok || jfsync.PyISO(tm) != out {
			t.Errorf("%s: %s, want %s", in, jfsync.PyISO(tm), out)
		}
	}
	tm, _ := jfsync.ParseTime("2026-10-04T20:15:30Z")
	if got := jfsync.PlayRef("A1B2-C3", tm); got != "a1b2c3@2026-10-04T20:15:30+00:00" {
		t.Error(got)
	}
}

func TestEveryPlayComesInOnceAndPythonRecordedPlaysStay(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	// A play the Python app recorded (its isoformat ref, no fraction).
	exec(t, s, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source, source_ref)
		VALUES ($1, 'movie', 600, 'Movie 600', '2026-10-01T20:00:00Z', 'jellyfin', $2)`, alice, strings.Repeat("b", 32)+"@2026-10-01T20:00:00+00:00")
	var plays []map[string]any
	plays = append(plays, played(strings.Repeat("b", 32), 600, "2026-10-01T20:00:00.0000000Z"))
	for i := range 2500 { // past the Python's old 2,000 cap: pages of 1,000
		plays = append(plays, played(fmt.Sprintf("%032x", i+1), 1000+i, "2026-10-02T20:00:00.0000000Z"))
	}
	ep := map[string]any{"Id": strings.Repeat("e", 32), "Type": "Episode", "Name": "Pilot", "SeriesId": strings.Repeat("5", 32),
		"SeriesName": "Show", "ParentIndexNumber": 1, "IndexNumber": 1, "UserData": map[string]any{"LastPlayedDate": "2026-10-03T20:00:00Z"}}
	undated := movie(strings.Repeat("d", 32), 9)
	plays = append(plays, ep, undated)
	exec(t, s, `INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, synced_at) VALUES ($1, 'tv', 1399, 'Show', now())`, strings.Repeat("5", 32))
	u.set(func(u *up) { u.played[jf] = plays })
	res, err := s.App.RW.JFSync.Plays(context.Background(), alice, jf)
	if err != nil || res.Added != 2501 || res.Undated != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE tmdb_id = 1399 AND episode_title = 'Pilot'"); n != 1 {
		t.Fatal("the episode did not take its show's TMDB id", n)
	}
	again, err := s.App.RW.JFSync.Plays(context.Background(), alice, jf)
	if err != nil || again.Added != 0 {
		t.Fatalf("%+v %v", again, err)
	}
	if n := count(t, s, "SELECT count(*) FROM history"); n != 2502 {
		t.Fatal(n)
	}
	// The person is told about the undated play (the manual sync's note).
	r := want(t, s.Client("alice", true).Do(t, "POST", "/api/v1/sync/plays", nil), 200)
	if !strings.Contains(r.Body, `"plays":0`) || !strings.Contains(r.Body, "1 of your Jellyfin plays has no date") {
		t.Fatal(r.Body)
	}
}

func TestJellyfinPlaysFoldIntoAManualMark(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	exec(t, s, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source) VALUES ($1, 'movie', 603, 'Movie 603', now(), 'manual')`, alice)
	when := time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z")
	u.set(func(u *up) { u.played[jf] = []map[string]any{played(strings.Repeat("a", 32), 603, when)} })
	res, err := s.App.RW.JFSync.Plays(context.Background(), alice, jf)
	if err != nil || res.Added != 0 {
		t.Fatalf("claimed the manual row, didn't add one: %+v %v", res, err)
	}
	if n := count(t, s, "SELECT count(*) FROM history WHERE source = 'jellyfin'"); n != 1 {
		t.Fatal(n)
	}
}

func TestAFailedPlaysReadAddsNothingAndIsShownUntilAGoodOne(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	alice, jf := link(t, s, "alice")
	bob, bjf := link(t, s, "bob")
	u.set(func(u *up) {
		u.played[jf] = []map[string]any{played(strings.Repeat("a", 32), 603, "2026-10-04T20:00:00Z")}
		u.playedErr[jf] = 404
		u.played[bjf] = []map[string]any{played(strings.Repeat("b", 32), 604, "2026-10-04T20:00:00Z")}
	})
	added, failed, err := s.App.RW.JFSync.AllPlays(context.Background(), s.App.RW.JFSync.WithLock)
	if err != nil || failed != 1 || added[bob] != 1 || added[alice] != 0 {
		t.Fatalf("one person's failure must not stop the others: %v %d %v", added, failed, err)
	}
	st := s.App.RW.Status
	v, err := st.Get(context.Background(), status.Plays(alice))
	if err != nil || v["ok"] != false || v["status"] != float64(404) || v["problem"] == nil {
		t.Fatalf("%v %v", v, err)
	}
	// The manual sync says why, plainly.
	r := want(t, s.Client("alice", true).Do(t, "POST", "/api/v1/sync/plays", nil), 502)
	if !strings.Contains(r.Body, "Nothing was added") {
		t.Fatal(r.Body)
	}
	u.set(func(u *up) { delete(u.playedErr, jf) })
	if _, err := s.App.RW.JFSync.Plays(context.Background(), alice, jf); err != nil {
		t.Fatal(err)
	}
	v, _ = st.Get(context.Background(), status.Plays(alice))
	if v["ok"] != true || v["problem"] != nil || v["last_good_at"] == nil {
		t.Fatalf("%v", v)
	}
	want(t, s.Client("carol", true).Do(t, "POST", "/api/v1/sync/plays", nil), 409) // not linked
}

// ---------------------------------------------------------------------------
// The library sync
// ---------------------------------------------------------------------------

func TestTheLibraryChangesOnlyAfterACompleteRead(t *testing.T) {
	s, u := startUp(t, testbase.BaseEnv(t))
	sync := s.App.RW.JFSync
	u.set(func(u *up) {
		for i := range 1200 { // pages of 500
			u.library = append(u.library, movie(fmt.Sprintf("%032x", i+1), 2000+i))
		}
		u.library = append(u.library, map[string]any{"Id": strings.Repeat("f", 32), "Type": "Series", "Name": "A Show",
			"ProviderIds": map[string]any{"Tmdb": "1399"}})
	})
	res, err := sync.Library(context.Background())
	if err != nil || res.Items != 1201 || res.Removed != 0 || res.Indexed != 1201 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := count(t, s, "SELECT count(*) FROM library_items WHERE media_type = 'tv' AND tmdb_id = 1399"); n != 1 {
		t.Fatal(n)
	}
	// Unchanged titles are not sent to the index again; a gone one leaves.
	u.set(func(u *up) { u.library = u.library[1:]; u.docsSent = 0 })
	res, err = sync.Library(context.Background())
	if err != nil || res.Removed != 1 || res.Indexed != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	u.set(func(u *up) {
		if u.docsSent != 0 || u.deletions != 1 {
			t.Errorf("sent %d, deleted %d", u.docsSent, u.deletions)
		}
	})
	// A failed read and an empty answer leave it as it was, and say why.
	for name, f := range map[string]func(u *up){
		"failed": func(u *up) { u.libraryErr = 500 },
		"empty":  func(u *up) { u.libraryErr = 0; u.library = nil },
	} {
		u.set(f)
		_, err := sync.Library(context.Background())
		var nr *jfsync.NotRead
		if !errors.As(err, &nr) || !strings.Contains(nr.Msg, "Nothing was removed") || !strings.Contains(nr.Msg, "1,200 titles") {
			t.Fatalf("%s: %v", name, err)
		}
		if n := count(t, s, "SELECT count(*) FROM library_items"); n != 1200 {
			t.Fatalf("%s: %d", name, n)
		}
	}
	v, _ := s.App.RW.Status.Get(context.Background(), status.Library)
	if v["ok"] != false || v["kept"] != float64(1200) {
		t.Fatal(v)
	}
	// An admin's rescan says why nothing changed; others may not ask.
	r := want(t, s.Client("owner", true).Do(t, "POST", "/api/v1/sync/library", nil), 502)
	if !strings.Contains(r.Body, "Jellyfin listed no movies or series at all") {
		t.Fatal(r.Body)
	}
	want(t, s.Client("alice", true).Do(t, "POST", "/api/v1/sync/library", nil), 403)
	u.set(func(u *up) { u.library = []map[string]any{movie(strings.Repeat("1", 32), 603)} })
	r = want(t, s.Client("owner", true).Do(t, "POST", "/api/v1/sync/library", nil), 200)
	if !strings.Contains(r.Body, `"items":1`) || !strings.Contains(r.Body, `"removed":1200`) {
		t.Fatal(r.Body)
	}
}
