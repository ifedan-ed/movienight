package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/store"
)

// Job is one scheduled piece of work.
type Job struct {
	// Name is the job's id in job_runs and the log ("plays", "imdb", ...).
	Name     string
	Schedule Schedule
	// Run does the work. An error is recorded with the run and logged; the
	// next due time comes as usual.
	Run func(ctx context.Context) error

	// CatchUp says what happens at start-up when the latest due time
	// before now has no run recorded (the worker was down then, or this is
	// the first start under Go):
	//
	//   - within MaxLate of that due time, it is run now (claimed under the
	//     due time it stands in for, so it still runs only once);
	//   - otherwise, when Missing reports true (the IMDb ratings were never
	//     loaded, the catalog never built), it is run now too;
	//   - otherwise it waits for its next due time.
	//
	// MaxLate 0 and no Missing: never caught up (the digests, which keep
	// their own catch-up job, and interval jobs whose next run is near).
	MaxLate time.Duration
	Missing func(ctx context.Context) (bool, error)

	// Group: jobs of one group never run at the same time in this worker
	// (the digest kinds and their catch-up: run together, two would build
	// the same digest twice, a wasted model call). "" runs alone.
	Group string
}

// Runner runs the jobs of a worker that holds the schedule (see
// cmd/worker: one leader at a time). Every run is claimed in job_runs
// first, so even two runners at once (a leader change while the old one's
// run was still going) never run one due time twice.
type Runner struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	log    *slog.Logger
	worker string
	now    func() time.Time

	mu     sync.Mutex
	groups map[string]*sync.Mutex
	wg     sync.WaitGroup
}

// NewRunner makes the runner.
func NewRunner(pool *pgxpool.Pool, log *slog.Logger) *Runner {
	host, _ := os.Hostname()
	return &Runner{pool: pool, q: store.New(pool), log: log, worker: fmt.Sprintf("%s/%d", host, os.Getpid()),
		now: time.Now, groups: map[string]*sync.Mutex{}}
}

func (r *Runner) group(name string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.groups[name] == nil {
		r.groups[name] = &sync.Mutex{}
	}
	return r.groups[name]
}

// Start runs every job on its schedule until ctx ends; Wait then returns
// once the runs under way have finished (their context is ctx too, so a
// shutdown cancels them and each stops at its next check).
func (r *Runner) Start(ctx context.Context, jobs []Job) {
	for _, j := range jobs {
		r.log.Info("job scheduled", "job", j.Name, "schedule", j.Schedule.String(),
			"next", j.Schedule.Next(r.now()).Format(time.RFC3339))
		r.wg.Add(1)
		go func(j Job) {
			defer r.wg.Done()
			r.loop(ctx, j)
		}(j)
	}
}

// Wait blocks until every job loop has stopped.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) loop(ctx context.Context, j Job) {
	if due, ok := r.catchUpDue(ctx, j); ok {
		r.RunOnce(ctx, j, due)
	}
	for {
		due := j.Schedule.Next(r.now())
		t := time.NewTimer(time.Until(due))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		// A run that took longer than the interval makes the next due time
		// come from now, so missed slots are skipped, not queued up
		// (APScheduler's coalesce).
		r.RunOnce(ctx, j, due)
	}
}

// catchUpDue decides the start-up catch-up (Job.CatchUp).
func (r *Runner) catchUpDue(ctx context.Context, j Job) (time.Time, bool) {
	now := r.now()
	prev := j.Schedule.Prev(now)
	latest, err := r.q.JobRunLatest(ctx, j.Name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		r.log.Error("job runs unreadable; no catch-up", "job", j.Name, "err", err)
		return time.Time{}, false
	}
	if err == nil && !latest.Before(prev) {
		return time.Time{}, false // the latest due run was made (or is being made)
	}
	if j.MaxLate > 0 && now.Sub(prev) <= j.MaxLate {
		r.log.Info("job catching up a run missed while no worker ran", "job", j.Name, "due", prev.Format(time.RFC3339))
		return prev, true
	}
	if j.Missing != nil {
		missing, err := j.Missing(ctx)
		if err != nil {
			r.log.Error("job's start-up check failed; waiting for its schedule", "job", j.Name, "err", err)
			return time.Time{}, false
		}
		if missing {
			r.log.Info("job running now: what it makes is missing", "job", j.Name)
			return prev, true
		}
	}
	return time.Time{}, false
}

// RunOnce claims the job's run for due and makes it; false when another
// worker (or an earlier start) has it. Exported for the tests and for an
// admin "run now" (which passes the due time it stands for).
func (r *Runner) RunOnce(ctx context.Context, j Job, due time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	if j.Group != "" {
		g := r.group(j.Group)
		g.Lock()
		defer g.Unlock()
	}
	_, err := r.q.JobRunClaim(ctx, store.JobRunClaimParams{Job: j.Name, DueAt: due, Worker: r.worker})
	if errors.Is(err, pgx.ErrNoRows) {
		r.log.Info("job run already claimed elsewhere", "job", j.Name, "due", due.Format(time.RFC3339))
		return false
	}
	if err != nil {
		r.log.Error("job run could not be claimed; skipped", "job", j.Name, "err", err)
		return false
	}
	start := r.now()
	runErr := safeRun(ctx, j.Run)
	var msg *string
	if runErr != nil {
		s := runErr.Error()
		msg = &s
		r.log.Error("job failed", "job", j.Name, "due", due.Format(time.RFC3339), "err", runErr,
			"seconds", int(r.now().Sub(start).Seconds()))
	} else {
		r.log.Info("job done", "job", j.Name, "seconds", int(r.now().Sub(start).Seconds()))
	}
	// Recorded on a fresh context: a shutdown that cancelled the run must
	// still be able to say so.
	fin, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.q.JobRunFinish(fin, store.JobRunFinishParams{Job: j.Name, DueAt: due, Error: msg}); err != nil {
		r.log.Error("job run's end could not be recorded", "job", j.Name, "err", err)
	}
	return true
}

// safeRun turns a panic in a job into its error: one broken job must not
// take the worker, and every other job, down with it.
func safeRun(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return run(ctx)
}
