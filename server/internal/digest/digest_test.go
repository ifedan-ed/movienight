package digest_test

// Digest emails (tests/test_digest_send.py, test_digest_year.py and the
// digest parts of test_units.py): on by default for everyone with an
// address, never sent empty, every one with a way to turn it off, a list
// cut short says how many more, and above all never one email twice: two
// runs at once, a catch-up after a failure, a send whose fate is unknown.
// Against the real Postgres and Valkey; TMDB, the model and the mail server
// are stand-ins.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/digest"
	"movienight/server/internal/mail"
	"movienight/server/internal/mail/mailtest"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
)

// ---- stand-ins ----

// recorder is a mailer that keeps what it was given; fail, when set,
// decides a send's outcome per address.
type recorder struct {
	mu    sync.Mutex
	sent  []sentMail
	delay time.Duration
	fail  func(to string) error
}

type sentMail struct {
	To, Subject, HTML string
	Headers           map[string]string
}

func (r *recorder) Enabled() bool { return true }

func (r *recorder) Send(_ context.Context, to, subject, html, _ string, headers map[string]string) (string, error) {
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(to); err != nil {
			return "", err
		}
	}
	r.sent = append(r.sent, sentMail{to, subject, html, headers})
	return "<id@example.org>", nil
}

func (r *recorder) to() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.sent {
		out = append(out, m.To)
	}
	return out
}

// tmdbStub answers every title: "Movie <id>" or "Show <id>", 100 minutes.
func tmdbStub(t *testing.T) *clients.TMDB {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		id, _ := strconv.Atoi(parts[1])
		body := map[string]any{"id": id, "poster_path": fmt.Sprintf("/p%d.jpg", id), "runtime": 100,
			"genres": []map[string]any{{"id": 18, "name": "Drama"}}}
		if parts[0] == "tv" {
			body["name"], body["first_air_date"], body["episode_run_time"] = fmt.Sprintf("Show %d", id), "2020-01-01", []int{30}
		} else {
			body["title"], body["release_date"] = fmt.Sprintf("Movie %d", id), "2020-01-01"
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return clients.NewTMDB(srv.Client(), srv.URL, "k", "en-US", "US", false)
}

// aiStub is the install's model behind LiteLLM; reply is its JSON answer.
type aiStub struct {
	calls atomic.Int32
	reply string
	url   string
}

func newAI(t *testing.T, reply string) *aiStub {
	a := &aiStub{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.calls.Add(1)
		content, _ := json.Marshal(a.reply)
		fmt.Fprintf(w, `{"choices": [{"message": {"content": %s}, "finish_reason": "stop"}]}`, content)
	}))
	t.Cleanup(srv.Close)
	a.url = srv.URL
	return a
}

// ---- the install ----

// now is the test's "today": Tuesday 10 November 2026, noon in New York.
// The last week is Mon 2 – Sun 8 November; its run was due Monday 9
// November 09:00.
var ny, _ = time.LoadLocation("America/New_York")
var now = time.Date(2026, 11, 10, 12, 0, 0, 0, ny)

type install struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *store.Queries
	cfg  *config.Config
	svc  *digest.Service
	mail *recorder
	logs *bytes.Buffer
}

type lockedBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func setup(t *testing.T, extra map[string]string, m mail.Mailer) *install {
	pool, kv := testbase.Conns(t)
	env := testbase.BaseEnv(t)
	env["TIMEZONE"] = "America/New_York"
	for k, v := range extra {
		env[k] = v
	}
	cfg := env.Config(t)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(&lockedBuf{b: logs}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cl := clients.NewSet(cfg, kv, log)
	cl.TMDB = tmdbStub(t)
	rec, _ := m.(*recorder)
	if m == nil {
		rec = &recorder{}
		m = rec
	}
	svc := digest.New(cfg, pool, cl, m, log)
	svc.SetClock(func() time.Time { return now })
	return &install{t: t, pool: pool, q: store.New(pool), cfg: cfg, svc: svc, mail: rec, logs: logs}
}

// user makes a person who joined on 1 December 2025, before every period
// the tests look at (a period due before someone joined is not theirs).
func (in *install) user(name string) store.User {
	in.t.Helper()
	email := name + "@example.org"
	u, err := in.q.CreateUser(context.Background(), store.CreateUserParams{Username: name, Email: &email, DisplayName: &name})
	if err != nil {
		in.t.Fatal(err)
	}
	in.exec(`UPDATE users SET created_at = '2025-12-01T00:00:00Z' WHERE id = $1`, u.ID)
	return u
}

// userIn is user, in a time zone their browser reported.
func (in *install) userIn(name, zone string) store.User {
	in.t.Helper()
	u := in.user(name)
	in.exec(`UPDATE users SET time_zone = $2 WHERE id = $1`, u.ID, zone)
	u.TimeZone = &zone
	return u
}

func (in *install) exec(sql string, args ...any) {
	in.t.Helper()
	if _, err := in.pool.Exec(context.Background(), sql, args...); err != nil {
		in.t.Fatal(err)
	}
}

// inLastWeek is a moment inside the last full week (Wed 4 Nov, 20:00).
func inLastWeek() time.Time { return time.Date(2026, 11, 4, 20, 0, 0, 0, ny) }

func (in *install) watch(u store.User, tmdbID int, when time.Time) {
	in.exec(`INSERT INTO history (user_id, media_type, tmdb_id, title, watched_at, source) VALUES ($1, 'movie', $2, $3, $4, 'manual')`,
		u.ID, tmdbID, fmt.Sprintf("Movie %d", tmdbID), when)
}

func (in *install) digests() []store.Digest {
	in.t.Helper()
	rows, err := in.pool.Query(context.Background(), `SELECT id FROM digests ORDER BY id`)
	if err != nil {
		in.t.Fatal(err)
	}
	defer rows.Close()
	var out []store.Digest
	for rows.Next() {
		var id int32
		_ = rows.Scan(&id)
		var uid int32
		_ = in.pool.QueryRow(context.Background(), `SELECT user_id FROM digests WHERE id = $1`, id).Scan(&uid)
		d, err := in.q.DigestGet(context.Background(), store.DigestGetParams{ID: id, UserID: uid})
		if err != nil {
			in.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func (in *install) run(kind string) int {
	in.t.Helper()
	n, err := in.svc.RunScheduled(context.Background(), kind)
	if err != nil {
		in.t.Fatalf("%s run: %v", kind, err)
	}
	return n
}

// weekBeforeDone records, for everyone, the week before last as the last
// week done (Monday's run was missed, or is the one now going) and the latest
// month and year as done, each in the person's own zone.
func (in *install) weekBeforeDone() {
	in.t.Helper()
	people, err := in.q.ListUsers(context.Background())
	if err != nil {
		in.t.Fatal(err)
	}
	for _, u := range people {
		loc := ny
		if u.TimeZone != nil {
			loc, _ = time.LoadLocation(*u.TimeZone)
		}
		week, _ := digest.LastPeriod("week", now, loc)
		month, _ := digest.LastPeriod("month", now, loc)
		year, _ := digest.LastPeriod("year", now, loc)
		for kind, start := range map[string]time.Time{"week": week.AddDate(0, 0, -7), "month": month, "year": year} {
			if err := in.svc.MarkDone(context.Background(), u.ID, kind, start); err != nil {
				in.t.Fatal(err)
			}
		}
	}
}

// ---- periods ----

func TestPeriodsAreTheInstallsLocalWeeksMonthsAndYears(t *testing.T) {
	at := time.Date(2026, 10, 2, 15, 0, 0, 0, ny)
	s, e := digest.LastPeriod("week", at, ny)
	if s.Format("2006-01-02") != "2026-09-21" || e.Format("2006-01-02") != "2026-09-28" {
		t.Errorf("week %v %v", s, e)
	}
	s, e = digest.LastPeriod("month", at, ny)
	if s.Format("2006-01-02") != "2026-09-01" || e.Format("2006-01-02") != "2026-10-01" || digest.PeriodLabel("month", s, e) != "September 2026" {
		t.Errorf("month %v %v", s, e)
	}
	s, e = digest.LastPeriod("year", time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC), ny)
	if s.Format("2006-01-02") != "2025-01-01" || e.Format("2006-01-02") != "2026-01-01" || digest.PeriodLabel("year", s, e) != "2025" {
		t.Errorf("year %v %v", s, e)
	}
	// Across the end of summer time (1 November 2026): both ends are local
	// midnights, the week 169 hours long.
	s, e = digest.LastPeriod("week", time.Date(2026, 11, 3, 8, 0, 0, 0, ny), ny)
	if s.Format(time.RFC3339) != "2026-10-26T00:00:00-04:00" || e.Format(time.RFC3339) != "2026-11-02T00:00:00-05:00" {
		t.Errorf("DST week %v %v", s, e)
	}
	if l := digest.PeriodLabel("week", s, e); l != "Oct 26 – Nov 1" {
		t.Errorf("label %q", l)
	}
	// The time zone is the install's: 04:00 UTC on a Monday is still Sunday
	// in New York, so the last week there is the one before.
	s, _ = digest.LastPeriod("week", time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC), ny)
	if s.Format("2006-01-02") != "2026-10-26" {
		t.Errorf("local week %v", s)
	}
	for _, c := range []struct {
		kind  string
		after time.Time
		want  string
	}{
		{"week", time.Date(2026, 11, 9, 9, 0, 0, 0, ny), "2026-11-16T09:00:00-05:00"},
		{"week", time.Date(2026, 11, 9, 8, 59, 0, 0, ny), "2026-11-09T09:00:00-05:00"},
		{"month", time.Date(2026, 11, 15, 0, 0, 0, 0, ny), "2026-12-01T09:30:00-05:00"},
		{"year", time.Date(2026, 11, 15, 0, 0, 0, 0, ny), "2027-01-01T10:30:00-05:00"},
	} {
		if got := digest.NextRun(c.kind, c.after, ny).Format(time.RFC3339); got != c.want {
			t.Errorf("NextRun(%s, %v) = %s, want %s", c.kind, c.after, got, c.want)
		}
	}
}

// ---- what is sent ----

func TestAnEmptyDigestIsNeverSentNorKept(t *testing.T) {
	in := setup(t, nil, nil)
	in.user("alice")
	if in.run("week") != 0 || in.run("month") != 0 || len(in.mail.sent) != 0 || len(in.digests()) != 0 {
		t.Fatal("an empty digest went out or was kept")
	}
}

func TestAPreviewMadeEarlierDoesntMakeAnEmptyOneGoOut(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	if _, err := in.svc.Build(context.Background(), u, "week", false); err != nil {
		t.Fatal(err)
	}
	if in.run("week") != 0 || len(in.mail.sent) != 0 {
		t.Fatal("sent")
	}
}

func TestAWeekWithAWatchIsSentWithAWayToTurnItOff(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	if in.run("week") != 1 || in.mail.sent[0].To != "alice@example.org" {
		t.Fatalf("%+v", in.mail.sent)
	}
	off := "http://mn.test/settings/emails?off=week"
	if got := in.mail.sent[0].Headers["List-Unsubscribe"]; got != "<"+off+">" {
		t.Errorf("List-Unsubscribe %q", got)
	}
	d := in.digests()[0]
	if !strings.Contains(d.Html, `href="`+off+`"`) || !strings.Contains(d.Html, "Turn this email off") ||
		!strings.Contains(d.Html, "Movie 603") || d.SentTo == nil || *d.SentTo != "alice@example.org" {
		t.Errorf("digest %+v", d)
	}
	// Made twice: nothing sent twice.
	if in.run("week") != 0 || len(in.mail.sent) != 1 {
		t.Fatal("sent twice")
	}
}

func TestSomethingNewInTheLibraryIsWorthAnEmail(t *testing.T) {
	in := setup(t, nil, nil)
	in.user("alice")
	in.exec(`INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, date_added, synced_at) VALUES ($1, 'movie', 604, 'x', $2, now())`,
		strings.Repeat("d", 32), inLastWeek())
	if in.run("week") != 1 {
		t.Fatal("not sent")
	}
	if !strings.Contains(in.mail.sent[0].HTML, "New in the library") || !strings.Contains(in.mail.sent[0].HTML, "Movie 604") {
		t.Error("the library addition is not in it")
	}
}

func TestSwitchedOffMeansNotSent(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	off := false
	if _, err := in.svc.SetSettings(context.Background(), u, digest.Change{Weekly: &off}); err != nil {
		t.Fatal(err)
	}
	in.watch(u, 603, inLastWeek())
	if in.run("week") != 0 || len(in.mail.sent) != 0 {
		t.Fatal("sent")
	}
	got, _ := in.svc.GetSettings(context.Background(), u)
	if got.Weekly || !got.Monthly || !got.Yearly || got.Address == nil || *got.Address != "alice@example.org" {
		t.Errorf("%+v", got)
	}
}

func TestAListCutShortSaysHowManyMore(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	for n := 0; n < 15; n++ {
		in.exec(`INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source) VALUES ($1, 'episode', $2, 1, 1, 'x', $3, 'jellyfin')`,
			u.ID, 1000+n, inLastWeek())
	}
	start, end := digest.LastPeriod("week", now, ny)
	data, err := in.svc.Gather(context.Background(), u, "week", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Shows) != 12 || data.More.Shows != 3 || data.Totals.Episodes != 15 {
		t.Fatalf("%d shows, %d more", len(data.Shows), data.More.Shows)
	}
	page, text := in.svc.Render(data, digest.PlainWords(data))
	if !strings.Contains(page, "…and 3 more shows") || !strings.Contains(text, "...and 3 more shows") {
		t.Error("the cut is not said")
	}
}

func TestTheSameWatchTwiceInOneEveningCountsOnce(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	// 23:30 and 00:10 the next day in UTC terms are both the same New York
	// evening (Nov 4, 18:30 and 19:10).
	in.watch(u, 603, time.Date(2026, 11, 4, 23, 30, 0, 0, time.UTC))
	in.watch(u, 603, time.Date(2026, 11, 5, 0, 10, 0, 0, time.UTC))
	start, end := digest.LastPeriod("week", now, ny)
	data, _ := in.svc.Gather(context.Background(), u, "week", start, end)
	if data.Totals.Films != 1 {
		t.Fatalf("%d films", data.Totals.Films)
	}
}

func TestALongCommentIsShortenedWithAMark(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	in.exec(`INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, review, rated_at, source) VALUES ($1, 'movie', 603, 9, $2, $3, 'app')`,
		u.ID, strings.Repeat("wörd ", 200), inLastWeek())
	start, end := digest.LastPeriod("week", now, ny)
	data, _ := in.svc.Gather(context.Background(), u, "week", start, end)
	r := []rune(*data.Films[0].Review)
	if r[len(r)-1] != '…' || len(r) > 401 {
		t.Fatalf("%q", string(r))
	}
	if len(data.Rated) != 1 || data.Rated[0].Title != "Movie 603" || data.Best == nil {
		t.Errorf("%+v", data.Rated)
	}
}

// "Coming from your watchlist" counts from today in TIMEZONE and is in date
// order (the Python sorted "Dec 1" before "Nov 30", as text).
func TestComingFromTheWatchlistIsInDateOrderFromTheInstallsToday(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	for id, date := range map[int]string{701: "2026-12-01", 702: "2026-11-30", 703: "2026-11-09", 704: "2026-11-10"} {
		in.exec(`INSERT INTO titles (key, media_type, tmdb_id, title, release_date, detailed, fetched_at) VALUES ($1, 'movie', $2, $3, $4, true, now())`,
			fmt.Sprintf("movie:%d", id), id, fmt.Sprintf("Soon %d", id), date)
		in.exec(`INSERT INTO watchlist (user_id, media_type, tmdb_id, added_at, source) VALUES ($1, 'movie', $2, now(), 'app')`, u.ID, id)
	}
	start, end := digest.LastPeriod("week", now, ny)
	data, _ := in.svc.Gather(context.Background(), u, "week", start, end)
	var got []string
	for _, c := range data.Coming {
		got = append(got, c.Title.Title)
	}
	if strings.Join(got, ",") != "Soon 704,Soon 702,Soon 701" {
		t.Fatalf("%v", got)
	}
}

func TestAYearWithOnlyALibraryAdditionIsNotSent(t *testing.T) {
	// The year in review has no "New in the library" section: a year in
	// which the person watched, rated and favourited nothing is empty,
	// however much the library grew. No email, no model call, nothing kept.
	ai := newAI(t, `{"subject": "x"}`)
	in := setup(t, map[string]string{"LITELLM_BASE_URL": ai.url, "LITELLM_API_KEY": "k", "LITELLM_MODEL": "test-model"}, nil)
	in.user("alice")
	start, _ := digest.LastPeriod("year", now, ny)
	in.exec(`INSERT INTO library_items (jellyfin_id, media_type, tmdb_id, title, date_added, synced_at) VALUES ($1, 'movie', 604, 'x', $2, now())`,
		strings.Repeat("e", 32), start.AddDate(0, 0, 40))
	if in.run("year") != 0 || len(in.mail.sent) != 0 || ai.calls.Load() != 0 || len(in.digests()) != 0 {
		t.Fatal("an empty year went out, called the model or was kept")
	}
}

func TestTheYearSummedUp(t *testing.T) {
	ai := newAI(t, `{"subject": "Your year in film: three films", "intro": "A good year."}`)
	in := setup(t, map[string]string{"LITELLM_BASE_URL": ai.url, "LITELLM_API_KEY": "k", "LITELLM_MODEL": "test-model"}, nil)
	u := in.user("alice")
	at := func(month, day int) time.Time { return time.Date(2025, time.Month(month), day, 20, 0, 0, 0, time.UTC) }
	in.watch(u, 603, at(2, 10))
	in.watch(u, 604, at(3, 10))
	in.watch(u, 605, at(11, 10))
	for e := 1; e <= 5; e++ {
		in.exec(`INSERT INTO history (user_id, media_type, tmdb_id, season, episode, title, watched_at, source) VALUES ($1, 'episode', 1399, 1, $2, 'Show 1399', $3, 'jellyfin')`,
			u.ID, e, at(3, 10+e))
	}
	// The years before and after are not in it.
	in.watch(u, 606, time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC))
	in.watch(u, 607, time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	in.exec(`INSERT INTO user_ratings (user_id, media_type, tmdb_id, rating, rated_at, source) VALUES ($1, 'movie', 604, 9, $2, 'app'), ($1, 'movie', 603, 7, $3, 'app')`,
		u.ID, at(3, 10), at(2, 10))
	// 11:00 in New York: after the year's 10:30 there.
	in.svc.SetClock(func() time.Time { return time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC) })

	if in.run("year") != 1 {
		t.Fatal("not sent")
	}
	d := in.digests()[0]
	if d.Subject != "Your year in film: three films" || d.Model == nil || *d.Model != "test-model" {
		t.Errorf("subject %q model %v", d.Subject, d.Model)
	}
	var st struct {
		Data digest.Data `json:"data"`
		Text string      `json:"text"`
	}
	if err := json.Unmarshal([]byte(d.DataJson), &st); err != nil {
		t.Fatal(err)
	}
	y := st.Data.Year
	var months []int
	for _, m := range y.Months {
		months = append(months, m.Films+m.Episodes)
	}
	if fmt.Sprint(months) != "[0 1 6 0 0 0 0 0 0 0 1 0]" || *y.Busiest != "March" {
		t.Errorf("months %v busiest %v", months, *y.Busiest)
	}
	if st.Data.Totals.Films != 3 || st.Data.Totals.Episodes != 5 || len(y.BestFilms) != 2 ||
		y.BestFilms[0].Title.Title != "Movie 604" || y.BestFilms[1].Title.Title != "Movie 603" {
		t.Errorf("%+v", st.Data.Totals)
	}
	if y.FirstFilm.Title.Title != "Movie 603" || y.LastFilm.Title.Title != "Movie 605" || y.TopShows[0].Episodes != 5 {
		t.Error("first/last/shows")
	}
	for _, want := range []string{"Your year in review", "Month by month", "Your best films", "Most watched shows", "First and last"} {
		if !strings.Contains(d.Html, want) {
			t.Errorf("no %q", want)
		}
	}
	if strings.Contains(d.Html, "Movie 606") || strings.Contains(d.Html, "Movie 607") {
		t.Error("another year's film is in it")
	}
	if !strings.Contains(st.Text, "Busiest month: March") || !strings.Contains(st.Text, "- Movie 604: 9/10") {
		t.Errorf("text:\n%s", st.Text)
	}
}

func TestALongSubjectFromTheModelEndsWithAMark(t *testing.T) {
	ai := newAI(t, `{"subject": "A `+strings.Repeat("very ", 40)+`long subject"}`)
	in := setup(t, map[string]string{"LITELLM_BASE_URL": ai.url, "LITELLM_API_KEY": "k", "LITELLM_MODEL": "test-model"}, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	in.run("week")
	s := []rune(in.mail.sent[0].Subject)
	if s[len(s)-1] != '…' || len(s) > 121 {
		t.Fatalf("%q", string(s))
	}
}

func TestAFailingModelStillSendsWithPlainWordsAndSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	in := setup(t, map[string]string{"LITELLM_BASE_URL": srv.URL, "LITELLM_API_KEY": "k", "LITELLM_MODEL": "m"}, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	if in.run("week") != 1 || in.mail.sent[0].Subject != "Your week in film: 1 film" {
		t.Fatalf("%+v", in.mail.sent)
	}
	if !strings.Contains(in.logs.String(), "using plain wording") {
		t.Error("the fallback was silent")
	}
}

// ---- never twice ----

func TestARunMissedWhileNoWorkerRanIsMadeUpOnce(t *testing.T) {
	in := setup(t, nil, nil)
	ctx := context.Background()
	// The first look takes the latest periods as done: nothing old is sent.
	if got, _ := in.svc.Missed(ctx, now); len(got) != 0 {
		t.Fatalf("missed %v", got)
	}
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	in.weekBeforeDone()
	_, due := digest.Due("week", now, ny)
	if got, _ := in.svc.Missed(ctx, due.Add(-time.Minute)); len(got) != 0 {
		t.Fatalf("not due yet, missed %v", got)
	}
	if got, _ := in.svc.Missed(ctx, due.Add(time.Minute)); fmt.Sprint(got) != "[week]" {
		t.Fatalf("missed %v", got)
	}
	if err := in.svc.CatchUp(ctx, due.Add(time.Minute)); err != nil || len(in.mail.sent) != 1 {
		t.Fatalf("%v %d", err, len(in.mail.sent))
	}
	if got, _ := in.svc.Missed(ctx, due.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("still missed %v", got)
	}
	in.weekBeforeDone() // made twice: nothing is sent twice
	if err := in.svc.CatchUp(ctx, due.Add(time.Minute)); err != nil || len(in.mail.sent) != 1 {
		t.Fatalf("%v %d", err, len(in.mail.sent))
	}
}

// The Python kept two runs apart with an asyncio lock in one process; two
// workers (or the catch-up beside the schedule elsewhere) had nothing to
// stop them. Here the send claim does: two runs at once, every digest once.
func TestTwoRunsAtOnceSendEachDigestOnce(t *testing.T) {
	rec := &recorder{delay: 200 * time.Millisecond}
	in := setup(t, nil, rec)
	for _, name := range []string{"alice", "bob", "carol"} {
		in.watch(in.user(name), 603, inLastWeek())
	}
	in.weekBeforeDone()
	_, due := digest.Due("week", now, ny)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = in.svc.RunScheduled(context.Background(), "week") }()
		go func() { defer wg.Done(); _ = in.svc.CatchUp(context.Background(), due.Add(time.Minute)) }()
	}
	wg.Wait()
	got := rec.to()
	if len(got) != 3 {
		t.Fatalf("sent %v", got)
	}
	// Whatever the overlap left not done, the next catch-up finishes
	// without sending anything again.
	if err := in.svc.CatchUp(context.Background(), due.Add(time.Hour)); err != nil || len(rec.to()) != 3 {
		t.Fatalf("%v %v", err, rec.to())
	}
	if m, _ := in.svc.Missed(context.Background(), due.Add(time.Hour)); len(m) != 0 {
		t.Fatalf("still missed %v", m)
	}
}

func TestOnePersonsFailureDoesntStopTheOthersAndIsTriedAgainEachHour(t *testing.T) {
	rec := &recorder{fail: func(to string) error {
		if strings.HasPrefix(to, "alice") {
			return errors.New("SMTP refused the address")
		}
		return nil
	}}
	in := setup(t, nil, rec)
	in.watch(in.user("alice"), 603, inLastWeek())
	in.watch(in.user("bob"), 603, inLastWeek())
	in.weekBeforeDone()
	ctx := context.Background()
	_, due := digest.Due("week", now, ny)

	if n, err := in.svc.RunScheduled(ctx, "week"); n != 1 || err == nil {
		t.Fatalf("%d %v", n, err)
	}
	if fmt.Sprint(rec.to()) != "[bob@example.org]" {
		t.Fatalf("%v", rec.to())
	}
	if m, _ := in.svc.Missed(ctx, due.Add(time.Hour)); fmt.Sprint(m) != "[week]" {
		t.Fatalf("not left for the catch-up: %v", m)
	}
	_ = in.svc.CatchUp(ctx, due.Add(time.Hour)) // still failing: tried, not done
	rec.mu.Lock()
	rec.fail = nil // the failure clears
	rec.mu.Unlock()
	if err := in.svc.CatchUp(ctx, due.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rec.to()) != "[bob@example.org alice@example.org]" {
		t.Fatalf("%v", rec.to()) // Bob's not twice
	}
	if m, _ := in.svc.Missed(ctx, due.Add(3*time.Hour)); len(m) != 0 {
		t.Fatalf("still missed %v", m)
	}
}

// Before: a connection that broke after the email was handed over counted
// as a failed send, and the hourly catch-up sent it again every hour.
// Through the real SMTP mailer and a real SMTP conversation.
func TestASendTheServerMayHaveTakenIsNeverSentAgain(t *testing.T) {
	srv := mailtest.Start(t, "drop_after_data")
	m := mail.NewSMTP("127.0.0.1", srv.Port, "", "", "Movie Night <mn@example.org>", false, time.Second)
	in := setup(t, nil, m)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	in.weekBeforeDone()
	ctx := context.Background()
	_, due := digest.Due("week", now, ny)
	if _, err := in.svc.RunScheduled(ctx, "week"); err != nil {
		t.Fatal(err)
	}
	for h := 1; h <= 3; h++ {
		_ = in.svc.CatchUp(ctx, due.Add(time.Duration(h)*time.Hour))
	}
	if len(srv.Received()) != 1 {
		t.Fatalf("received %d", len(srv.Received()))
	}
	d := in.digests()[0]
	if d.SentAt == nil || d.SentTo == nil || *d.SentTo != "alice@example.org" || d.SendingSince != nil {
		t.Fatalf("%+v", d)
	}
	if m, _ := in.svc.Missed(ctx, due.Add(4*time.Hour)); len(m) != 0 {
		t.Fatalf("not done: %v", m)
	}
}

func TestAClearFailureIsTriedAtMostThreeTimesThenTheRunIsDone(t *testing.T) {
	tried := 0
	rec := &recorder{fail: func(string) error { tried++; return errors.New("SMTP down") }}
	in := setup(t, nil, rec)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	in.weekBeforeDone()
	ctx := context.Background()
	_, due := digest.Due("week", now, ny)
	_, _ = in.svc.RunScheduled(ctx, "week") // try 1
	if m, _ := in.svc.Missed(ctx, due.Add(time.Hour)); fmt.Sprint(m) != "[week]" {
		t.Fatalf("%v", m)
	}
	for h := 1; h <= 4; h++ { // tries 2 and 3, then nothing
		_ = in.svc.CatchUp(ctx, due.Add(time.Duration(h)*time.Hour))
	}
	if tried != 3 {
		t.Fatalf("tried %d times", tried)
	}
	if n := strings.Count(in.logs.String(), "not sent after 3 tries"); n != 1 || !strings.Contains(in.logs.String(), "level=ERROR") {
		t.Fatalf("gave-up log %d:\n%s", n, in.logs.String())
	}
	d := in.digests()[0]
	if d.SendTries != 3 || d.SentAt != nil || d.SendingSince != nil {
		t.Fatalf("%+v", d)
	}
	if m, _ := in.svc.Missed(ctx, due.Add(5*time.Hour)); len(m) != 0 {
		t.Fatalf("given up, yet not done: %v", m)
	}
	// "Send it to me now" is the person's own: it still goes, uncounted.
	rec.mu.Lock()
	rec.fail = nil
	rec.mu.Unlock()
	if to, err := in.svc.Send(ctx, u, d, "", false); err != nil || to != "alice@example.org" {
		t.Fatalf("%q %v", to, err)
	}
	if in.digests()[0].SendTries != 3 {
		t.Error("the person's send was counted")
	}
}

func TestASendThatNeverReportedBackIsTakenAsSentOnlyOnceItIsStale(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	d, err := in.svc.Build(context.Background(), u, "week", true)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	// Claimed a minute ago: another worker is sending it now. Not sent
	// here, not settled, and the run is not done.
	in.exec(`UPDATE digests SET sending_since = now() - interval '1 minute', send_tries = 1 WHERE id = $1`, d.ID)
	if n, err := in.svc.RunScheduled(context.Background(), "week"); n != 0 || err == nil || len(in.mail.sent) != 0 {
		t.Fatalf("%d %v %d", n, err, len(in.mail.sent))
	}
	if in.digests()[0].SentAt != nil {
		t.Fatal("a send under way was settled")
	}
	// Claimed a quarter of an hour ago and never finished: the worker died
	// midway. Taken as sent, at the time it started.
	in.exec(`UPDATE digests SET sending_since = now() - interval '15 minutes' WHERE id = $1`, d.ID)
	if in.run("week") != 0 || len(in.mail.sent) != 0 {
		t.Fatal("sent again")
	}
	got := in.digests()[0]
	if got.SentAt == nil || got.SendingSince != nil || *got.SentTo != "alice@example.org" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(in.logs.String(), "never reported back") {
		t.Error("not logged")
	}
}

func TestSendItToMeNowWhileAnotherSendIsUnderWayIsRefused(t *testing.T) {
	in := setup(t, nil, nil)
	u := in.user("alice")
	in.watch(u, 603, inLastWeek())
	d, _ := in.svc.Build(context.Background(), u, "week", false)
	in.exec(`UPDATE digests SET sending_since = now() WHERE id = $1`, d.ID)
	if _, err := in.svc.Send(context.Background(), u, *d, "", false); !errors.Is(err, digest.ErrSending) {
		t.Fatalf("%v", err)
	}
	if len(in.mail.sent) != 0 {
		t.Fatal("sent beside the other send")
	}
}

func TestTheYearsAIReadsEveryFilmOfTheYear(t *testing.T) {
	// The Python once read the year's best and only 40 more by name.
	var seen []byte
	rec := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = []byte(body.Messages[1].Content)
		fmt.Fprint(w, `{"choices": [{"message": {"content": "{}"}}]}`)
	})
	srv := httptest.NewServer(rec)
	defer srv.Close()
	in := setup(t, map[string]string{"LITELLM_BASE_URL": srv.URL, "LITELLM_API_KEY": "k", "LITELLM_MODEL": "m"}, nil)
	u := in.user("alice")
	for n := 0; n < 120; n++ {
		in.watch(u, 2000+n, time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(n)*time.Hour))
	}
	in.svc.SetClock(func() time.Time { return time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC) }) // after 10:30 in New York
	in.run("year")
	var input struct {
		Films []struct{ Title string } `json:"films"`
	}
	if err := json.Unmarshal(seen, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Films) != 120 {
		t.Fatalf("the model read %d films", len(input.Films))
	}
}

// ---- each person's own zone ----

// Two people a whole day apart: Alice's browser says Pacific/Auckland
// (UTC+13 in November), Bob's Europe/Berlin (UTC+1). The install runs on New
// York time. Each gets the week Monday to Sunday of their own zone, at 09:00
// on their own Monday (the first hourly run after it), and never twice.
var (
	auckland, _ = time.LoadLocation("Pacific/Auckland")
	berlin, _   = time.LoadLocation("Europe/Berlin")
)

func TestEachPersonsWeekIsTheirOwnAndArrivesOnTheirMonday(t *testing.T) {
	in := setup(t, nil, nil)
	ctx := context.Background()
	alice, bob := in.userIn("alice", "Pacific/Auckland"), in.userIn("bob", "Europe/Berlin")
	// Sunday 8 November 23:30 in Auckland: her week. Half an hour later
	// it is Monday there, her next week, though still Sunday in Berlin.
	in.watch(alice, 603, time.Date(2026, 11, 8, 23, 30, 0, 0, auckland))
	in.watch(alice, 604, time.Date(2026, 11, 9, 0, 30, 0, 0, auckland))
	// Sunday 8 November 12:30 in Berlin (the same moment as Alice's 00:30
	// Monday): his week.
	in.watch(bob, 605, time.Date(2026, 11, 8, 12, 30, 0, 0, berlin))
	in.weekBeforeDone()

	aliceDue := time.Date(2026, 11, 9, 9, 0, 0, 0, auckland) // Sunday 20:00 UTC
	bobDue := time.Date(2026, 11, 9, 9, 0, 0, 0, berlin)     // Monday 08:00 UTC
	catchUp := func(at time.Time) {
		t.Helper()
		if err := in.svc.CatchUp(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	catchUp(aliceDue.Add(-time.Minute))
	if len(in.mail.sent) != 0 {
		t.Fatalf("sent before 09:00 on anyone's Monday: %v", in.mail.to())
	}
	catchUp(aliceDue.Add(4 * time.Minute)) // the hourly run at :04
	if fmt.Sprint(in.mail.to()) != "[alice@example.org]" {
		t.Fatalf("after Alice's Monday 09:00: %v", in.mail.to())
	}
	d := in.digests()[0]
	if got := d.PeriodStart.In(auckland).Format(time.RFC3339); got != "2026-11-02T00:00:00+13:00" ||
		!d.PeriodEnd.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, auckland)) {
		t.Fatalf("Alice's week %v to %v", d.PeriodStart, d.PeriodEnd)
	}
	if !strings.Contains(d.Html, "Movie 603") || strings.Contains(d.Html, "Movie 604") {
		t.Fatal("Alice's week is not Monday to Sunday in Auckland")
	}
	catchUp(bobDue.Add(-time.Minute))
	if len(in.mail.sent) != 1 {
		t.Fatalf("Bob's went before his Monday 09:00, or Alice's twice: %v", in.mail.to())
	}
	catchUp(bobDue.Add(4 * time.Minute))
	if fmt.Sprint(in.mail.to()) != "[alice@example.org bob@example.org]" {
		t.Fatalf("after Bob's Monday 09:00: %v", in.mail.to())
	}
	bd := in.digests()[1]
	if !bd.PeriodStart.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, berlin)) || !strings.Contains(bd.Html, "Movie 605") {
		t.Fatalf("Bob's week %v", bd.PeriodStart)
	}
	// The install's own Monday 09:00 run, and every hour after: nothing new.
	if n, err := in.svc.RunScheduled(ctx, "week"); n != 0 || err != nil {
		t.Fatalf("the scheduled run sent %d (%v)", n, err)
	}
	for h := 1; h <= 3; h++ {
		catchUp(bobDue.Add(time.Duration(h) * time.Hour))
	}
	if len(in.mail.sent) != 2 {
		t.Fatalf("sent again: %v", in.mail.to())
	}
	if m, _ := in.svc.Missed(ctx, bobDue.Add(4*time.Hour)); len(m) != 0 {
		t.Fatalf("still missed %v", m)
	}
}

// Alice flies to Los Angeles after her week went out: her phone's new zone
// gives the same week (2 to 8 November) new boundaries, and it is still the
// week she has had.
func TestAPersonWhoseZoneChangesIsNotSentTheSameWeekAgain(t *testing.T) {
	in := setup(t, nil, nil)
	ctx := context.Background()
	alice := in.userIn("alice", "Pacific/Auckland")
	in.watch(alice, 603, time.Date(2026, 11, 5, 20, 0, 0, 0, auckland))
	in.weekBeforeDone()
	if err := in.svc.CatchUp(ctx, time.Date(2026, 11, 9, 9, 4, 0, 0, auckland)); err != nil || len(in.mail.sent) != 1 {
		t.Fatalf("%v %v", err, in.mail.to())
	}
	in.exec(`UPDATE users SET time_zone = 'America/Los_Angeles' WHERE id = $1`, alice.ID)
	la, _ := time.LoadLocation("America/Los_Angeles")
	if err := in.svc.CatchUp(ctx, time.Date(2026, 11, 9, 10, 4, 0, 0, la)); err != nil || len(in.mail.sent) != 1 {
		t.Fatalf("the same week again: %v %v", err, in.mail.to())
	}
}

// The claim holds across zones too: two hourly runs at once (two workers)
// at the moment Alice's week is due send it once.
func TestTwoRunsAtOnceSendAPersonsWeekOnceInTheirZone(t *testing.T) {
	rec := &recorder{delay: 200 * time.Millisecond}
	in := setup(t, nil, rec)
	alice := in.userIn("alice", "Pacific/Auckland")
	in.watch(alice, 603, time.Date(2026, 11, 5, 20, 0, 0, 0, auckland))
	in.weekBeforeDone()
	at := time.Date(2026, 11, 9, 9, 4, 0, 0, auckland)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = in.svc.CatchUp(context.Background(), at) }()
	}
	wg.Wait()
	if err := in.svc.CatchUp(context.Background(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rec.to()) != "[alice@example.org]" {
		t.Fatalf("sent %v", rec.to())
	}
}

// Switching over from one install-wide marker (the Python's, and this
// server's before zones per person): a week it says was done for everyone
// is not sent again; a person who joined after a week was due does not get
// that week.
func TestTheOldInstallWideMarkerAndJoiningLateMeanDone(t *testing.T) {
	in := setup(t, nil, nil)
	ctx := context.Background()
	old := in.user("alice")
	in.watch(old, 603, inLastWeek())
	in.exec(`INSERT INTO app_settings (key, value) VALUES ('digest_done:week', '2026-11-02T00:00:00-05:00')`)
	late := in.user("bob")
	in.exec(`UPDATE users SET created_at = '2026-11-10T00:00:00Z' WHERE id = $1`, late.ID) // after Monday 09:00
	in.watch(late, 603, inLastWeek())
	if n, err := in.svc.RunScheduled(ctx, "week"); n != 0 || err != nil || len(in.mail.sent) != 0 {
		t.Fatalf("%d %v %v", n, err, in.mail.to())
	}
	// The next week is both of theirs: due after the marker and after Bob
	// joined.
	next := time.Date(2026, 11, 16, 9, 4, 0, 0, ny)
	in.watch(old, 604, time.Date(2026, 11, 11, 20, 0, 0, 0, ny))
	in.watch(late, 604, time.Date(2026, 11, 12, 20, 0, 0, 0, ny))
	if err := in.svc.CatchUp(ctx, next); err != nil || fmt.Sprint(in.mail.to()) != "[alice@example.org bob@example.org]" {
		t.Fatalf("%v %v", err, in.mail.to())
	}
}

// A period's days are the person's: the same two moments are one watch for
// someone in Auckland and two for someone in Berlin.
func TestADigestMergesWatchesByThePersonsOwnDay(t *testing.T) {
	in := setup(t, nil, nil)
	alice, bob := in.userIn("alice", "Pacific/Auckland"), in.userIn("bob", "Europe/Berlin")
	// 22:30 and 23:30 UTC on Wednesday 4 November: 11:30 and 12:30 on
	// Thursday in Auckland, 23:30 Wednesday and 00:30 Thursday in Berlin.
	for _, u := range []store.User{alice, bob} {
		in.watch(u, 603, time.Date(2026, 11, 4, 22, 30, 0, 0, time.UTC))
		in.watch(u, 603, time.Date(2026, 11, 4, 23, 30, 0, 0, time.UTC))
	}
	for _, c := range []struct {
		u     store.User
		loc   *time.Location
		films int
	}{{alice, auckland, 1}, {bob, berlin, 2}} {
		start, end := digest.LastPeriod("week", now, c.loc)
		data, err := in.svc.Gather(context.Background(), c.u, "week", start, end)
		if err != nil || data.Totals.Films != c.films {
			t.Fatalf("%s: %d watches (%v)", c.u.Username, data.Totals.Films, err)
		}
	}
}
