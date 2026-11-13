package profile_test

// Each person's own time zone: reported by their browser or phone (PUT
// /me/time-zone), checked, shown in /me, and used for their days. The
// install runs on New York time (testbase.BaseEnv).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func TestAReportedTimeZoneIsCheckedAndKept(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	if me := alice.Do(t, "GET", "/api/v1/me", nil).Map(t); me["time_zone"] != nil {
		t.Fatalf("a zone before any was reported: %v", me["time_zone"])
	}
	// Not zones: refused in words, the usual 422, nothing kept.
	for _, bad := range []string{"Mars/Olympus_Mons", "Local", "europe/berlin", "../../etc/passwd", "Europe/Berlin\u0000",
		strings.Repeat("A", 65), ""} {
		body, _ := json.Marshal(map[string]string{"time_zone": bad})
		r := alice.Do(t, "PUT", "/api/v1/me/time-zone", string(body))
		if r.StatusCode != 422 || r.Map(t)["detail"] == nil {
			t.Fatalf("%q: %d %s", bad, r.StatusCode, r.Body)
		}
	}
	if r := alice.Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Mars/Olympus_Mons"}`); r.Map(t)["detail"] !=
		"time_zone must be an IANA time zone name (e.g. Europe/London)" {
		t.Fatal(r.Body)
	}
	if me := alice.Do(t, "GET", "/api/v1/me", nil).Map(t); me["time_zone"] != nil {
		t.Fatalf("a refused zone was kept: %v", me["time_zone"])
	}
	// A browser's write needs X-Requested-With like every other.
	if r := s.Client("alice", false).Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Europe/Berlin"}`); r.StatusCode != 403 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	r := alice.Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Pacific/Auckland"}`)
	if r.StatusCode != 200 || r.Map(t)["time_zone"] != "Pacific/Auckland" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if me := alice.Do(t, "GET", "/api/v1/me", nil).Map(t); me["time_zone"] != "Pacific/Auckland" {
		t.Fatal(me["time_zone"])
	}
	// Someone else's stays theirs (none).
	if me := s.Client("bob", true).Do(t, "GET", "/api/v1/me", nil).Map(t); me["time_zone"] != nil {
		t.Fatal(me["time_zone"])
	}
}

// The same four moments, seen by Alice (Auckland, UTC+13 in November), Bob
// (Berlin, UTC+1) and Carol, who never reported a zone and so has the
// install's (New York, UTC-5):
//
//	film 603 at 10:30 and 11:30 UTC on 4 Nov: Auckland 23:30 / 00:30 (two
//	days), Berlin 11:30 / 12:30, New York 05:30 / 06:30 (one day each);
//	film 604 at 22:30 and 23:30 UTC on 4 Nov: Berlin 23:30 / 00:30 (two
//	days), Auckland 11:30 / 12:30 on the 5th, New York 17:30 / 18:30.
func TestHistoryDaysAreEachPersonsOwn(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	people := map[string]*testenv.Client{}
	for _, name := range []string{"alice", "bob", "carol"} {
		people[name] = s.Client(name, true)
		people[name].Do(t, "GET", "/api/v1/me", nil)
	}
	people["alice"].Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Pacific/Auckland"}`)
	people["bob"].Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Europe/Berlin"}`)
	s.Exec(t, `INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT u.id, 'movie', w.tmdb, 'Movie ' || w.tmdb, w.at::timestamptz, 'manual'
		FROM users u, (VALUES (603, '2026-11-04T10:30:00Z'), (603, '2026-11-04T11:30:00Z'),
		                      (604, '2026-11-04T22:30:00Z'), (604, '2026-11-04T23:30:00Z')) w(tmdb, at)`)
	for name, want := range map[string]struct {
		entries map[string]int // film -> merged watches on the History page
		offset  string         // every time is written in the person's zone
	}{
		"alice": {map[string]int{"movie:603": 2, "movie:604": 1}, "+13:00"},
		"bob":   {map[string]int{"movie:603": 1, "movie:604": 2}, "+01:00"},
		"carol": {map[string]int{"movie:603": 1, "movie:604": 1}, "-05:00"},
	} {
		var page struct {
			Items []struct {
				Key       string `json:"key"`
				WatchedAt string `json:"watched_at"`
			}
		}
		people[name].Do(t, "GET", "/api/v1/history", nil).JSON(t, &page)
		got := map[string]int{}
		for _, it := range page.Items {
			got[it.Key]++
			if !strings.HasSuffix(it.WatchedAt, want.offset) {
				t.Errorf("%s: %s not in their zone", name, it.WatchedAt)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want.entries) {
			t.Errorf("%s: history %v, want %v", name, got, want.entries)
		}
		// The title page counts watches by the same days.
		var title struct {
			Watches []any `json:"watches"`
		}
		people[name].Do(t, "GET", "/api/v1/title/movie/603", nil).JSON(t, &title)
		if len(title.Watches) != want.entries["movie:603"] {
			t.Errorf("%s: title page has %d watches, want %d", name, len(title.Watches), want.entries["movie:603"])
		}
	}
}

// New Year's Eve, 12:00 UTC last year: already this year in Auckland, still
// last year in Berlin.
func TestThisYearIsThePersonsYear(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	auckland, _ := time.LoadLocation("Pacific/Auckland")
	year := time.Now().In(auckland).Year()
	alice, bob := s.Client("alice", true), s.Client("bob", true)
	alice.Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Pacific/Auckland"}`)
	bob.Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Europe/Berlin"}`)
	s.Exec(t, fmt.Sprintf(`INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source)
		SELECT id, 'movie', 603, 'Movie 603', '%d-12-31T12:00:00Z', 'manual' FROM users`, year-1))
	thisYear := func(c *testenv.Client) any {
		return c.Do(t, "GET", "/api/v1/me", nil).Map(t)["stats"].(map[string]any)["this_year"]
	}
	if a, b := thisYear(alice), thisYear(bob); a != 1.0 || b != 0.0 {
		t.Fatalf("this year: Auckland %v, Berlin %v", a, b)
	}
}

// A watch date typed without a time is 20:00 that day where the person is.
func TestAWatchDateWithoutATimeIsThatEveningInThePersonsZone(t *testing.T) {
	s, _ := testenv.StartFaked(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	alice.Do(t, "PUT", "/api/v1/me/time-zone", `{"time_zone": "Pacific/Auckland"}`)
	r := alice.Do(t, "POST", "/api/v1/title/movie/603/watched", `{"watched_at": "2026-11-04"}`).Map(t)
	w := r["watches"].([]any)[0].(map[string]any)
	if w["watched_at"] != "2026-11-04T20:00:00+13:00" {
		t.Fatal(w["watched_at"])
	}
}
