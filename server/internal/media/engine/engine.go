// Package engine is the media service's playback: the file itself for a
// browser that plays it, HLS made by ffmpeg for one that does not (the
// picture copied when the browser takes its codec, encoded again
// otherwise), subtitles as WebVTT or ASS, and a file's attached fonts.
//
// An HLS play is a job: one ffmpeg writing segments into its own folder.
// Jobs start when their first segment is asked for and restart at a
// segment far ahead of what they made (a seek), so a seek never waits for
// everything before it. A job pauses ffmpeg when it is far ahead of the
// player, and stops when the player stops asking or the API says the play
// ended; its folder goes with it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"movienight/server/internal/media/probe"
)

// Config is the engine's settings.
type Config struct {
	FFmpeg string // the ffmpeg binary ("ffmpeg": looked up on PATH)
	Prober probe.Runner
	// CacheDir holds the jobs' segments and the extracted subtitles and
	// fonts (MEDIA_CACHE_DIR).
	CacheDir string
	// MaxTranscodes is how many transcodes (picture encoded again) run at
	// once (MEDIA_MAX_TRANSCODES). Remuxes are cheap and not counted.
	MaxTranscodes int
	HW            HW
	// IdleAfter: a job no segment was asked of for this long is stopped
	// (the player went away without saying so).
	IdleAfter time.Duration
	Log       *slog.Logger
}

// Errors the routes turn into answers.
var (
	ErrBusy     = errors.New("too many films are being converted right now; try again in a moment")
	ErrNotFound = errors.New("not found")
)

// Engine plays files.
type Engine struct {
	cfg    Config
	probes *probeCache
	log    *slog.Logger

	mu         sync.Mutex
	jobs       map[string]*job
	transcodes int // jobs with a transcode ffmpeg running

	extract singleflight.Group // subtitle and font extraction, one per file
	stop    chan struct{}
	wg      sync.WaitGroup
}

// New makes the engine and starts its housekeeping (idle jobs, throttling,
// old cache files). Close stops it and every ffmpeg.
func New(cfg Config) (*Engine, error) {
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.MaxTranscodes < 1 {
		cfg.MaxTranscodes = 1
	}
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = 2 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	for _, d := range []string{jobsDir, subsDir, fontsDir} {
		if err := os.MkdirAll(filepath.Join(cfg.CacheDir, d), 0o755); err != nil {
			return nil, fmt.Errorf("MEDIA_CACHE_DIR can't be written: %w", err)
		}
	}
	// Segments left by an earlier run of the service belong to no job.
	if err := clearDir(filepath.Join(cfg.CacheDir, jobsDir)); err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, probes: newProbeCache(cfg.Prober, 512), log: cfg.Log, jobs: map[string]*job{},
		stop: make(chan struct{})}
	e.wg.Add(1)
	go e.housekeeping()
	return e, nil
}

// Close stops every job and the housekeeping.
func (e *Engine) Close() {
	close(e.stop)
	e.wg.Wait()
	e.mu.Lock()
	jobs := make([]*job, 0, len(e.jobs))
	for k, j := range e.jobs {
		jobs = append(jobs, j)
		delete(e.jobs, k)
	}
	e.mu.Unlock()
	for _, j := range jobs {
		e.end(j)
	}
}

// Status is what the engine is doing.
type Status struct {
	Jobs          int    `json:"jobs"`
	Transcodes    int    `json:"transcodes"`
	MaxTranscodes int    `json:"max_transcodes"`
	HWAccel       string `json:"hwaccel"`
}

// Status says how busy the engine is.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{Jobs: len(e.jobs), Transcodes: e.transcodes, MaxTranscodes: e.cfg.MaxTranscodes, HWAccel: e.cfg.HW.Kind}
}

// Probe is the file's probe, cached by its size and modification time.
func (e *Engine) Probe(ctx context.Context, file string) (*probe.Probe, error) {
	return e.probes.get(ctx, file)
}

const (
	jobsDir  = "jobs"
	subsDir  = "subtitles"
	fontsDir = "fonts"
	// cacheKeep: extracted subtitles and fonts unused this long are removed.
	cacheKeep = 3 * 24 * time.Hour
)

func (e *Engine) housekeeping() {
	defer e.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	e.sweepCache()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
			e.tend()
		case <-sweep.C:
			e.sweepCache()
		}
	}
}

// sweepCache removes extracted subtitles and fonts not used for cacheKeep
// (each use touches its folder).
func (e *Engine) sweepCache() {
	cut := time.Now().Add(-cacheKeep)
	for _, d := range []string{subsDir, fontsDir} {
		root := filepath.Join(e.cfg.CacheDir, d)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, en := range entries {
			info, err := en.Info()
			if err == nil && info.ModTime().Before(cut) {
				_ = os.RemoveAll(filepath.Join(root, en.Name()))
			}
		}
	}
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, en := range entries {
		if err := os.RemoveAll(filepath.Join(dir, en.Name())); err != nil {
			return err
		}
	}
	return nil
}

// probeCache keeps recent probes, keyed by path and checked against the
// file's size and time on every use, so a replaced file is probed again.
type probeCache struct {
	runner probe.Runner
	max    int
	mu     sync.Mutex
	m      map[string]cached
	group  singleflight.Group
}

type cached struct {
	size  int64
	mtime time.Time
	p     *probe.Probe
	used  time.Time
}

func newProbeCache(r probe.Runner, max int) *probeCache {
	return &probeCache{runner: r, max: max, m: map[string]cached{}}
}

func (c *probeCache) get(ctx context.Context, file string) (*probe.Probe, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, ErrNotFound
	}
	c.mu.Lock()
	if v, ok := c.m[file]; ok && v.size == st.Size() && v.mtime.Equal(st.ModTime()) {
		v.used = time.Now()
		c.m[file] = v
		c.mu.Unlock()
		return v.p, nil
	}
	c.mu.Unlock()
	got, err, _ := c.group.Do(file, func() (any, error) {
		return c.runner.Run(context.WithoutCancel(ctx), file)
	})
	if err != nil {
		return nil, err
	}
	p := got.(*probe.Probe)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		// Drop the least recently used.
		var oldK string
		var oldT time.Time
		for k, v := range c.m {
			if oldK == "" || v.used.Before(oldT) {
				oldK, oldT = k, v.used
			}
		}
		delete(c.m, oldK)
	}
	c.m[file] = cached{size: st.Size(), mtime: st.ModTime(), p: p, used: time.Now()}
	return p, nil
}
