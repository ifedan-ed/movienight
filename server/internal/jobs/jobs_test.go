package jobs_test

// The schedule's arithmetic (local times, daylight saving) and the runner's
// promises against a real Postgres: a due run happens once however many
// runners claim it, a missed run is caught up at start within its window or
// when what it makes is missing, a failing or panicking job is recorded and
// never takes the others down.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"movienight/server/internal/jobs"
	"movienight/server/internal/testbase"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func must(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestCalendarSchedulesAreLocalTimes(t *testing.T) {
	ny := must(t, "America/New_York")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name     string
		s        jobs.Schedule
		from     string
		next     string
		prevFrom string
		prev     string
	}{
		{"daily before", jobs.Daily(2, 30, ny), "2026-11-12 01:00", "2026-11-12 02:30", "2026-11-12 01:00", "2026-11-11 02:30"},
		{"daily at is not next", jobs.Daily(2, 30, ny), "2026-11-12 02:30", "2026-11-13 02:30", "2026-11-12 02:30", "2026-11-12 02:30"},
		{"weekly Monday", jobs.Weekly(time.Monday, 9, 0, ny), "2026-11-12 10:00", "2026-11-16 09:00", "2026-11-12 10:00", "2026-11-09 09:00"},
		{"monthly 1st", jobs.Monthly(1, 9, 30, ny), "2026-11-12 10:00", "2026-12-01 09:30", "2026-11-12 10:00", "2026-11-01 09:30"},
		{"yearly 1 Jan", jobs.Yearly(time.January, 1, 10, 30, ny), "2026-11-12 10:00", "2027-01-01 10:30", "2026-11-12 10:00", "2026-01-01 10:30"},
		{"Sunday catalog", jobs.Weekly(time.Sunday, 3, 0, ny), "2026-11-15 02:59", "2026-11-15 03:00", "2026-11-15 02:59", "2026-11-08 03:00"},
	}
	for _, c := range cases {
		if got := c.s.Next(at(c.from)); !got.Equal(at(c.next)) {
			t.Errorf("%s: Next(%s) = %s, want %s", c.name, c.from, got.In(ny), c.next)
		}
		if got := c.s.Prev(at(c.prevFrom)); !got.Equal(at(c.prev)) {
			t.Errorf("%s: Prev(%s) = %s, want %s", c.name, c.prevFrom, got.In(ny), c.prev)
		}
	}
}

func TestDaylightSavingNeitherSkipsNorDoublesANightlyRun(t *testing.T) {
	ny := must(t, "America/New_York")
	s := jobs.Daily(2, 30, ny)
	// 8 March 2026: clocks jump 02:00 -> 03:00, so 02:30 does not exist. The
	// run still happens that night (time.Date moves it to 03:30 EDT).
	before := time.Date(2026, 3, 8, 1, 0, 0, 0, ny)
	n := s.Next(before)
	if n.Day() != 8 || n.Sub(before) > 3*time.Hour {
		t.Fatalf("spring-forward night: next run %s", n)
	}
	// 1 November 2026: 01:00-02:00 happens twice; 02:30 once. Walking a
	// week of nights gives exactly seven runs, a day apart in wall time.
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, ny)
	var runs []time.Time
	for cur := t0; len(runs) < 7; {
		cur = s.Next(cur)
		runs = append(runs, cur)
	}
	for i, r := range runs {
		if l := r.In(ny); l.Hour() != 2 || l.Minute() != 30 || l.YearDay() != t0.YearDay()+i+1 {
			t.Fatalf("run %d at %s", i, l)
		}
	}
	// 31 October 02:30 EDT to 1 November 02:30 EST.
	if runs[2].Sub(runs[1]) != 25*time.Hour {
		t.Fatalf("the fall-back night is 25 hours: %s", runs[2].Sub(runs[1]))
	}
}

func TestEveryIsAlignedToTheClockForEveryWorker(t *testing.T) {
	e := jobs.Every{Interval: 5 * time.Minute}
	got := e.Next(time.Date(2026, 11, 12, 10, 7, 13, 0, time.UTC))
	if want := time.Date(2026, 11, 12, 10, 10, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
	h := jobs.Every{Interval: time.Hour, Offset: 4 * time.Minute}
	if got := h.Prev(time.Date(2026, 11, 12, 10, 3, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 12, 9, 4, 0, 0, time.UTC)) {
		t.Fatalf("Prev with offset = %s", got)
	}
}

func TestADueRunIsMadeOnceHoweverManyRunnersClaimIt(t *testing.T) {
	pool, _ := testbase.Conns(t)
	var runs atomic.Int32
	j := jobs.Job{Name: "once", Schedule: jobs.Every{Interval: time.Hour},
		Run: func(context.Context) error { runs.Add(1); time.Sleep(20 * time.Millisecond); return nil }}
	due := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	var made atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if jobs.NewRunner(pool, quiet).RunOnce(context.Background(), j, due) {
				made.Add(1)
			}
		}()
	}
	wg.Wait()
	if runs.Load() != 1 || made.Load() != 1 {
		t.Fatalf("ran %d times (%d claimed), want once", runs.Load(), made.Load())
	}
	// The next due time is its own run.
	if !jobs.NewRunner(pool, quiet).RunOnce(context.Background(), j, due.Add(time.Hour)) || runs.Load() != 2 {
		t.Fatal("the next due time did not run")
	}
}

func TestAFailureOrAPanicIsRecordedAndTheRunnerCarriesOn(t *testing.T) {
	pool, _ := testbase.Conns(t)
	r := jobs.NewRunner(pool, quiet)
	due := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	r.RunOnce(context.Background(), jobs.Job{Name: "fails", Run: func(context.Context) error { return errors.New("TMDB down") }}, due)
	r.RunOnce(context.Background(), jobs.Job{Name: "panics", Run: func(context.Context) error { panic("bug") }}, due)
	for job, want := range map[string]string{"fails": "TMDB down", "panics": "panic: bug"} {
		var msg *string
		var finished *time.Time
		if err := pool.QueryRow(context.Background(), "SELECT error, finished_at FROM job_runs WHERE job = $1", job).Scan(&msg, &finished); err != nil {
			t.Fatal(err)
		}
		if msg == nil || finished == nil || len(*msg) < len(want) || (*msg)[:len(want)] != want {
			t.Fatalf("%s: recorded %v at %v", job, msg, finished)
		}
	}
}

// runFor starts a runner on jobs for d and waits for it to stop.
func runFor(t *testing.T, r *jobs.Runner, list []jobs.Job, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	r.Start(ctx, list)
	<-ctx.Done()
	r.Wait()
}

func TestAMissedRunIsCaughtUpAtStartOnlyWithinItsWindowOrWhenMissing(t *testing.T) {
	pool, _ := testbase.Conns(t)
	utc := time.UTC
	var late, old, missing, notMissing atomic.Int32
	// A daily job due 00:00 UTC: at any moment "now" it is at most a day late.
	list := []jobs.Job{
		{Name: "late", Schedule: jobs.Daily(0, 0, utc), MaxLate: 25 * time.Hour,
			Run: func(context.Context) error { late.Add(1); return nil }},
		{Name: "too-old", Schedule: jobs.Weekly(time.Weekday((int(time.Now().UTC().Weekday())+1)%7), 0, 0, utc), MaxLate: time.Hour,
			Run: func(context.Context) error { old.Add(1); return nil }},
		{Name: "missing", Schedule: jobs.Weekly(time.Weekday((int(time.Now().UTC().Weekday())+1)%7), 0, 0, utc),
			Missing: func(context.Context) (bool, error) { return true, nil },
			Run:     func(context.Context) error { missing.Add(1); return nil }},
		{Name: "not-missing", Schedule: jobs.Weekly(time.Weekday((int(time.Now().UTC().Weekday())+1)%7), 0, 0, utc),
			Missing: func(context.Context) (bool, error) { return false, nil },
			Run:     func(context.Context) error { notMissing.Add(1); return nil }},
	}
	runFor(t, jobs.NewRunner(pool, quiet), list, 300*time.Millisecond)
	if late.Load() != 1 || old.Load() != 0 || missing.Load() != 1 || notMissing.Load() != 0 {
		t.Fatalf("late %d, too old %d, missing %d, not missing %d", late.Load(), old.Load(), missing.Load(), notMissing.Load())
	}
	// A second start finds the catch-up made: nothing runs again.
	runFor(t, jobs.NewRunner(pool, quiet), list, 300*time.Millisecond)
	if late.Load() != 1 || missing.Load() != 1 {
		t.Fatalf("caught up again: late %d, missing %d", late.Load(), missing.Load())
	}
}

func TestJobsOfOneGroupNeverOverlap(t *testing.T) {
	pool, _ := testbase.Conns(t)
	r := jobs.NewRunner(pool, quiet)
	var inside, most atomic.Int32
	run := func(context.Context) error {
		n := inside.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inside.Add(-1)
		return nil
	}
	due := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for _, name := range []string{"digest-week", "digest-month", "digest-catch-up"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			r.RunOnce(context.Background(), jobs.Job{Name: name, Group: "digest", Run: run}, due)
		}(name)
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Fatalf("%d digest jobs ran at once", most.Load())
	}
}
