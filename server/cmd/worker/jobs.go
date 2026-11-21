package main

// The schedule: every job of the Python's APScheduler setup,
// at the same local times (TIMEZONE), with what each does at start-up when
// its latest due run was missed (jobs.Job.CatchUp). The Python's
// first-runs-soon-after-start ("library 20 s after start", "IMDb 3 min 20 s
// after start when there are none yet", "catalog when never built") are the
// catch-up rules here: the interval jobs make their missed slot at once, the
// IMDb load and the catalog build run when what they make is missing.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/airows"
	"movienight/server/internal/cache"
	"movienight/server/internal/catalog"
	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/digest"
	"movienight/server/internal/homecfg"
	"movienight/server/internal/imdb"
	"movienight/server/internal/jfsync"
	"movienight/server/internal/jobs"
	"movienight/server/internal/libraries"
	"movienight/server/internal/lists"
	"movienight/server/internal/mail"
	"movienight/server/internal/rw"
	"movienight/server/internal/settings"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// services is everything the worker runs, built from the same packages
// the API uses (internal/rw.NewDeps builds the API's), so a job and a route
// read and write through one implementation each.
type services struct {
	sync    *jfsync.Service
	ai      *airows.Service
	imdb    *imdb.Service
	catalog *catalog.Service
	lists   *lists.Service
	digests *digest.Service
	titles  *titles.Service
	libs    *libraries.Service
	q       *store.Queries
	clients *clients.Set
}

func build(cfg *config.Config, pool *pgxpool.Pool, kv valkey.Client, cl *clients.Set, log *slog.Logger) *services {
	q := store.New(pool)
	c := cache.New(kv, log)
	st := settings.New(q)
	t := titles.New(q, func() titles.Fetcher { return cl.TMDB }, log)
	return &services{
		sync:    jfsync.New(pool, kv, cl, status.New(st, func() *clients.MDBList { return cl.MDBList }, log), c, cfg.JellyfinUserID, log),
		ai:      airows.New(cfg, pool, kv, cl, t, log),
		imdb:    imdb.New(pool, cl.Upstream(), imdb.URL),
		catalog: catalog.New(cfg, pool, cl, log),
		lists:   lists.New(pool, q, t, st, homecfg.New(q, st), log),
		digests: digest.New(cfg, pool, cl, mail.New(cfg), log),
		titles:  t,
		libs:    rw.NewLibraries(cfg, pool, kv, cl, t, log),
		q:       q,
		clients: cl,
	}
}

// keepRuns is how long job_runs rows are kept.
const keepRuns = 30 * 24 * time.Hour

// schedule is the job list for this install (a job whose feature is off is
// left out, and the log says so at start-up).
func schedule(cfg *config.Config, s *services, log *slog.Logger) []jobs.Job {
	var list []jobs.Job
	off := func(name, why string) { log.Info("job not scheduled", "job", name, "why", why) }

	// Every 30 minutes: Movie Night's own media libraries, rescanned (only
	// new and changed files are probed). "Scan now" is the queue.
	if cfg.Media.Enabled() {
		list = append(list, jobs.Job{Name: "media-scan", Schedule: jobs.Every{Interval: 30 * time.Minute, Offset: 2 * time.Minute},
			MaxLate: 30 * time.Minute, Run: s.libs.ScanAll})
	} else {
		off("media-scan", "MEDIA_URL is not set")
	}
	if !cfg.JellyfinEnabled() {
		off("plays, library", "Jellyfin is not set up")
		return append(list, common(cfg, s, log)...)
	}

	// Every 5 minutes: each linked person's Jellyfin plays (the webhook
	// makes this instant; this catches the rest). Under each person's sync
	// lock, shared with the webhook queue and the manual sync.
	list = append(list, jobs.Job{Name: "plays", Schedule: jobs.Every{Interval: 5 * time.Minute}, MaxLate: 5 * time.Minute,
		Run: func(ctx context.Context) error {
			added, failed, err := s.sync.AllPlays(ctx, s.sync.WithLock)
			if err != nil {
				return err
			}
			if len(added) > 0 {
				log.InfoContext(ctx, "jellyfin plays: new per person id", "added", fmt.Sprint(added))
			}
			if failed > 0 {
				// Each failure is logged and kept in the person's status; the
				// run is recorded failed so job_runs shows it too.
				return fmt.Errorf("%d people's plays could not be read", failed)
			}
			return nil
		}})

	// Every 30 minutes: the library and its search index.
	list = append(list, jobs.Job{Name: "library", Schedule: jobs.Every{Interval: 30 * time.Minute}, MaxLate: 30 * time.Minute,
		Run: func(ctx context.Context) error {
			res, err := s.sync.Library(ctx)
			var nr *jfsync.NotRead
			if errors.As(err, &nr) {
				return err // already logged and shown to admins in Profile
			}
			if err != nil {
				return err
			}
			log.InfoContext(ctx, "library sync", "result", res.String())
			return nil
		}})

	return append(list, common(cfg, s, log)...)
}

// common is every job that needs no Jellyfin.
func common(cfg *config.Config, s *services, log *slog.Logger) []jobs.Job {
	loc := cfg.Location
	var list []jobs.Job
	off := func(name, why string) { log.Info("job not scheduled", "job", name, "why", why) }

	// Daily 02:30: IMDb's ratings. Run at start-up when none are loaded, or
	// when the night's load was missed within the day.
	if cfg.ImdbRatingsEnabled {
		list = append(list, jobs.Job{Name: "imdb", Schedule: jobs.Daily(2, 30, loc), MaxLate: 20 * time.Hour,
			Missing: func(ctx context.Context) (bool, error) { ok, err := s.imdb.Loaded(ctx); return !ok, err },
			Run: func(ctx context.Context) error {
				n, err := s.imdb.Refresh(ctx)
				if err != nil {
					return fmt.Errorf("IMDb ratings load failed; the ones already here stay: %w", err)
				}
				log.InfoContext(ctx, "IMDb ratings loaded", "titles", n)
				return nil
			}})
	} else {
		off("imdb", "IMDB_RATINGS_ENABLED is false")
	}

	// Daily 04:30: MDBList lists kept in step, and Movie Night's own copy of
	// MDBList's top lists (package lists, the same code as "Sync now" and the
	// lists pages' copy). Both with the plain client: answered from the copy
	// they would only rewrite it. The copy runs even when the sync failed (as
	// in the Python); both failures are reported.
	if cfg.MDBListEnabled() {
		list = append(list, jobs.Job{Name: "mdblist", Schedule: jobs.Daily(4, 30, loc), MaxLate: 12 * time.Hour,
			Run: func(ctx context.Context) error {
				n, syncErr := s.lists.SyncAll(ctx, s.clients.MDBList)
				if syncErr == nil {
					log.InfoContext(ctx, "MDBList lists synced", "lists", n)
				}
				c, copyErr := s.lists.RefreshCopy(ctx, s.clients.MDBList)
				if copyErr == nil {
					log.InfoContext(ctx, "MDBList top lists copied", "lists", c)
				}
				return errors.Join(syncErr, copyErr)
			}})
	} else {
		off("mdblist", "MDBLIST_API_KEY is not set")
	}

	// Daily 05:00: every stale title's details refreshed from TMDB (no cap).
	list = append(list, jobs.Job{Name: "titles", Schedule: jobs.Daily(5, 0, loc), MaxLate: 12 * time.Hour,
		Run: func(ctx context.Context) error {
			refreshed, missed, err := s.titles.RefreshStale(ctx, time.Now())
			log.InfoContext(ctx, "stale titles refreshed", "refreshed", refreshed, "missed", missed)
			return err
		}})

	// Sunday 03:00: the search catalog. Built at start-up when it never was.
	if cfg.CatalogEnabled {
		list = append(list, jobs.Job{Name: "catalog", Schedule: jobs.Weekly(time.Sunday, 3, 0, loc), MaxLate: 12 * time.Hour,
			Missing: func(ctx context.Context) (bool, error) { ok, err := s.catalog.Built(ctx); return !ok, err },
			Run: func(ctx context.Context) error {
				sum, err := s.catalog.Sync(ctx)
				if err != nil {
					return err
				}
				log.InfoContext(ctx, "search catalog refreshed", "titles", sum.Titles, "changed", sum.Changed,
					"removed", sum.Removed, "failed_pages", sum.FailedPages)
				return nil
			}})
	} else {
		off("catalog", "CATALOG_ENABLED is false")
	}

	// The digests: Monday 09:00, the 1st at 09:30, 1 January at 10:30 in the
	// install's TIMEZONE (scheduling is the install's clock), and every hour
	// the catch-up. Each person's period is their own (a week in their zone,
	// due at 09:00 on their Monday), so the hourly run is what sends to
	// people whose zone is ahead of or behind the install's, as well as
	// anything missed while no worker ran or whose send failed. One digest
	// job at a time (Group): run together, two would build the same digest
	// twice. Sending twice is impossible either way (each send is claimed in
	// Postgres first: internal/digest).
	if cfg.EmailEnabled() {
		for _, kind := range []struct {
			name string
			s    jobs.Schedule
		}{
			{"week", jobs.Weekly(time.Monday, digest.RunAt["week"].Hour, digest.RunAt["week"].Minute, loc)},
			{"month", jobs.Monthly(1, digest.RunAt["month"].Hour, digest.RunAt["month"].Minute, loc)},
			{"year", jobs.Yearly(time.January, 1, digest.RunAt["year"].Hour, digest.RunAt["year"].Minute, loc)},
		} {
			kind := kind
			list = append(list, jobs.Job{Name: "digest-" + kind.name, Schedule: kind.s, Group: "digest",
				Run: func(ctx context.Context) error {
					n, err := s.digests.RunScheduled(ctx, kind.name)
					log.InfoContext(ctx, "digests sent", "kind", kind.name, "sent", n)
					return err
				}})
		}
		list = append(list, jobs.Job{Name: "digest-catch-up", Schedule: jobs.Every{Interval: time.Hour, Offset: 4 * time.Minute},
			MaxLate: time.Hour, Group: "digest",
			Run: func(ctx context.Context) error { return s.digests.CatchUp(ctx, time.Now()) }})
	} else {
		off("digests", "email is not set up (SMTP_HOST, or MAIL_SENDER=smtp2go with SMTP2GO_API_KEY_FILE)")
	}

	// Hourly housekeeping: expired browser sessions, sign-in codes and TV
	// pairing codes, and run records older than keepRuns. The Python never
	// removed most of these; they only grew.
	list = append(list, jobs.Job{Name: "cleanup", Schedule: jobs.Every{Interval: time.Hour, Offset: 7 * time.Minute},
		MaxLate: time.Hour,
		Run: func(ctx context.Context) error {
			n, err := s.q.DeleteExpiredWebSessions(ctx)
			if err != nil {
				return err
			}
			if err := s.q.DeleteExpiredAppCodes(ctx); err != nil {
				return err
			}
			if err := s.q.DeleteExpiredPairCodes(ctx); err != nil {
				return err
			}
			runs, err := s.q.JobRunPrune(ctx, time.Now().Add(-keepRuns))
			log.InfoContext(ctx, "cleanup", "sessions", n, "job_runs", runs)
			return err
		}})
	return list
}
