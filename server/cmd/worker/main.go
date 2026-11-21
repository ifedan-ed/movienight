// Command worker runs Movie Night's background work: the scheduled jobs
// (library and plays sync, MDBList copies, the catalog build, IMDb ratings,
// stale titles, digests: jobs.go) and the queued work the API asks for (a
// person's Jellyfin plays after a webhook event, their AI rows).
//
// Design, for any number of workers:
//
//   - The schedule runs on one worker at a time, the leader: whoever holds
//     a Postgres session-level advisory lock on one dedicated connection
//     (phase 1). The others retry every few seconds; if the leader dies, its
//     connection closes and the lock is free at once. The leader pings that
//     connection, and a lost connection (a Postgres restart) ends its
//     leadership before another can take it over without its knowing.
//   - Each run of a job is also claimed in job_runs under (job, due time),
//     so even two leaders at once (a hung old leader and its successor)
//     never make one due run twice, and a run missed while no worker ran is
//     visible and caught up at start (internal/jobs).
//   - Digests are claimed one send at a time in the digests table
//     (internal/digest): no email goes twice, whatever runs at once.
//   - The queues (webhook plays syncs, AI rows) run on every worker: each
//     request is popped by exactly one (internal/tasks), so more workers
//     mean more of that work done at once.
//
// `worker -run plays,library` runs the named scheduled jobs once, in that
// order, and exits (runOnce): for an operator, or a check of a copy of the
// database. No schedule, no queues, nothing claimed in job_runs.
//
// The API never runs any of this (the Python ran the jobs inside every web
// process: double runs with two replicas, and memory spikes in the web
// process from the IMDb and catalog work).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/errreports"
	"movienight/server/internal/jobs"
	"movienight/server/internal/kv"
	"movienight/server/internal/logging"
	"movienight/server/internal/migrate"
	"movienight/server/internal/pg"
)

// leaderLock is the worker's advisory lock id ("mnwork" in ASCII).
const leaderLock int64 = 0x6d6e776f726b

// retryLeadership: how often a standby asks for the lock (a variable only
// so the tests need not wait five seconds).
var retryLeadership = 5 * time.Second

func main() {
	once := flag.String("run", "", "run these scheduled jobs once (comma separated, in order) and exit")
	flag.Parse()
	log := logging.New(os.Stdout, slog.LevelInfo)
	if flag.NArg() > 0 {
		log.Error("worker takes no arguments besides -run", "args", flag.Args())
		os.Exit(2)
	}
	if err := run(log, *once); err != nil {
		os.Exit(1) // run has logged (and reported) why
	}
}

// run works until a signal (or, with once, until those jobs are done). It
// logs its own fatal error, so that one is reported too, then flushes the
// reporter.
func run(base *slog.Logger, once string) (err error) {
	log := base
	var reporter *errreports.Reporter
	defer func() {
		if err != nil {
			log.Error("the worker stopped", "err", err)
		}
		reporter.Flush()
	}()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	er := cfg.ErrorReports
	if reporter, err = errreports.NewReporter(er.ServerDSN, er.Environment, "worker"); err != nil {
		return err
	}
	log = slog.New(errreports.LogHandler(base.Handler(), reporter))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrate.Check(ctx, pool); err != nil {
		return err
	}
	valkey, err := kv.Open(ctx, cfg.ValkeyURL)
	if err != nil {
		return err
	}
	defer valkey.Close()

	cl := clients.NewSet(cfg, valkey, log)
	defer cl.Close()
	svc := build(cfg, pool, valkey, cl, log)
	if once != "" {
		return runOnce(ctx, log, schedule(cfg, svc, log), strings.Split(once, ","))
	}

	// Queued work runs on every worker, leader or not; tracked, so the
	// shutdown waits for a sync or an AI build under way.
	var work sync.WaitGroup
	for _, run := range []func(context.Context){svc.sync.RunQueued, svc.ai.RunQueued} {
		work.Add(1)
		go func(run func(context.Context)) {
			defer work.Done()
			run(ctx)
		}(run)
	}

	if cfg.RunJobs {
		lead(ctx, pool, log, func(leading context.Context) {
			runner := jobs.NewRunner(pool, log)
			runner.Start(leading, schedule(cfg, svc, log))
			runner.Wait()
		})
	} else {
		log.Info("RUN_JOBS is false: no schedule; the queues still run")
		<-ctx.Done()
	}
	work.Wait()
	log.Info("worker shut down")
	return nil
}

// runOnce runs the named jobs once each, in the order given, with each
// one's time and the process's peak memory in the log. Every name is
// checked before anything runs; a job that is off here (its feature not set
// up) is an unknown name. All are run even when one fails; the error names
// the failed ones.
func runOnce(ctx context.Context, log *slog.Logger, list []jobs.Job, names []string) error {
	byName := map[string]jobs.Job{}
	var known []string
	for _, j := range list {
		byName[j.Name] = j
		known = append(known, j.Name)
	}
	for _, n := range names {
		if _, ok := byName[n]; !ok {
			return fmt.Errorf("no job %q on this install (scheduled here: %s)", n, strings.Join(known, ", "))
		}
	}
	var failed []string
	for _, n := range slices.Compact(names) {
		start := time.Now()
		err := byName[n].Run(ctx)
		log.Info("job run once", "job", n, "seconds", time.Since(start).Round(time.Millisecond).Seconds(),
			"peak_rss_mb", peakRSSMB(), "ok", err == nil, "err", err)
		if err != nil {
			failed = append(failed, n)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if len(failed) > 0 {
		return errors.New("failed: " + strings.Join(failed, ", "))
	}
	return nil
}

// peakRSSMB is the process's peak resident memory so far (VmHWM), in MB;
// 0 where /proc is not there.
func peakRSSMB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			var kb int
			fmt.Sscan(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), &kb)
			return kb / 1024
		}
	}
	return 0
}

// leaderPing: how often the leader checks that its lock's connection lives.
const leaderPing = 15 * time.Second

// lead runs schedule while this worker holds the leader lock, until ctx
// ends. Losing the lock's connection cancels the schedule's context (its
// runs stop at their next check) and the worker goes back to asking.
func lead(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, schedule func(context.Context)) {
	for ctx.Err() == nil {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("no database connection for the leader lock; retrying", "err", err)
				wait(ctx, retryLeadership)
			}
			continue
		}
		var leader bool
		err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", leaderLock).Scan(&leader)
		if err != nil || !leader {
			conn.Release()
			if err != nil && ctx.Err() == nil {
				log.Error("leader lock unreadable; retrying", "err", err)
			}
			wait(ctx, retryLeadership)
			continue
		}
		log.Info("this worker runs the schedule")
		leading, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			schedule(leading)
		}()
		t := time.NewTicker(leaderPing)
	watch:
		for {
			select {
			case <-done:
				break watch
			case <-t.C:
				pc, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pc)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Error("the leader lock's connection is gone; stopping the schedule", "err", err)
					stop()
				}
			}
		}
		t.Stop()
		stop()
		// The lock's connection is closed rather than returned to the pool:
		// a pooled connection would keep holding the lock for whoever takes
		// it next.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		conn.Release()
	}
}

func wait(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
