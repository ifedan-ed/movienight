package main

// Leadership against a real Postgres: of two workers only one runs the
// schedule; when it stops, the other takes over; and the whole schedule is
// built for a full install without panicking.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/jobs"
	"movienight/server/internal/testbase"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestOneLeaderAtATimeAndAStandbyTakesOver(t *testing.T) {
	pool, _ := testbase.Conns(t)
	retryLeadership = 50 * time.Millisecond
	var leaders, most atomic.Int32
	schedule := func(name string, started chan<- string) func(context.Context) {
		return func(ctx context.Context) {
			n := leaders.Add(1)
			if n > most.Load() {
				most.Store(n)
			}
			started <- name
			<-ctx.Done()
			leaders.Add(-1)
		}
	}
	started := make(chan string, 4)
	ctxA, stopA := context.WithCancel(context.Background())
	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	doneA := make(chan struct{})
	go func() { lead(ctxA, pool, quiet, schedule("a", started)); close(doneA) }()
	first := <-started
	go lead(ctxB, pool, quiet, schedule("b", started))
	time.Sleep(300 * time.Millisecond)
	if most.Load() != 1 || first != "a" {
		t.Fatalf("%d leaders at once (first %s)", most.Load(), first)
	}
	stopA()
	<-doneA
	select {
	case got := <-started:
		if got != "b" {
			t.Fatalf("taken over by %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the standby never took over")
	}
	if most.Load() != 1 {
		t.Fatalf("%d leaders at once", most.Load())
	}
}

func TestTheScheduleOfAFullInstall(t *testing.T) {
	pool, kv := testbase.Conns(t)
	env := testbase.BaseEnv(t)
	env["SMTP_HOST"], env["MDBLIST_API_KEY"] = "smtp.test", "k"
	env["LITELLM_API_KEY"], env["LITELLM_MODEL"], env["LITELLM_BASE_URL"] = "k", "m", "http://litellm.test"
	cfg := env.Config(t)
	svc := build(cfg, pool, kv, clients.NewSet(cfg, kv, quiet), quiet)
	got := map[string]bool{}
	for _, j := range schedule(cfg, svc, quiet) {
		if got[j.Name] {
			t.Fatalf("%s twice", j.Name)
		}
		got[j.Name] = true
		if next := j.Schedule.Next(time.Now()); !next.After(time.Now()) {
			t.Fatalf("%s: next run %s is not ahead", j.Name, next)
		}
	}
	for _, name := range []string{"plays", "library", "imdb", "mdblist", "titles", "catalog", "digest-week",
		"digest-month", "digest-year", "digest-catch-up", "cleanup"} {
		if !got[name] {
			t.Errorf("%s is not scheduled", name)
		}
	}
	// Switched off, a job is left out.
	env["IMDB_RATINGS_ENABLED"], env["CATALOG_ENABLED"] = "false", "false"
	delete(env, "SMTP_HOST")
	delete(env, "MDBLIST_API_KEY")
	cfg = env.Config(t)
	for _, j := range schedule(cfg, build(cfg, pool, kv, clients.NewSet(cfg, kv, quiet), quiet), quiet) {
		switch j.Name {
		case "imdb", "catalog", "mdblist", "digest-week", "digest-catch-up":
			t.Errorf("%s scheduled while off", j.Name)
		}
	}
}

// -run: the named jobs run once in the order given; an unknown name stops
// everything before anything runs; a failure does not stop the rest.
func TestRunOnce(t *testing.T) {
	var order []string
	job := func(name string, err error) jobs.Job {
		return jobs.Job{Name: name, Run: func(context.Context) error { order = append(order, name); return err }}
	}
	list := []jobs.Job{job("a", nil), job("b", errors.New("broke")), job("c", nil)}
	ctx := context.Background()
	if err := runOnce(ctx, quiet, list, []string{"a", "x"}); err == nil || !strings.Contains(err.Error(), `"x"`) || len(order) != 0 {
		t.Fatal(err, order)
	}
	if err := runOnce(ctx, quiet, list, []string{"c", "b", "a"}); err == nil || err.Error() != "failed: b" ||
		strings.Join(order, "") != "cba" {
		t.Fatal(err, order)
	}
	if peakRSSMB() <= 0 {
		t.Fatal("no peak memory read from /proc")
	}
}
