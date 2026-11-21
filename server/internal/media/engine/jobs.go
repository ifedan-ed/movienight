package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/probe"
)

// How a job paces itself.
const (
	// lookahead: a segment this far past the last one made is waited for;
	// further is a seek, and the job restarts there.
	lookahead = 4
	// pauseAhead and resumeAhead: ffmpeg is paused when it is this many
	// segments ahead of the last one the player asked for (ten minutes)
	// and carries on when the player is within resumeAhead of it (six
	// minutes). The player itself reads up to a few minutes ahead of what
	// it shows (player.js), so this keeps ffmpeg further ahead than that:
	// the player never waits on a segment, and a seek within the next ten
	// minutes finds its segment made. A paused film costs no CPU; its
	// segments on disk are the price (docs/limits.md).
	pauseAhead  = 100
	resumeAhead = 60
	// segmentWait is the longest a request waits for its segment (a slow
	// first transcode segment of a 4K file).
	segmentWait = 90 * time.Second
	pollEvery   = 100 * time.Millisecond
)

// job is one play's HLS: its folder and the ffmpeg run writing into it.
type job struct {
	key, play, file string
	plan            plan.Plan
	probe           *probe.Probe
	dir             string

	// mu guards the fields below and every start and kill of a run, so two
	// requests for one play never start two ffmpegs. Engine.mu is never
	// waited for while holding it, except briefly to count a transcode.
	mu      sync.Mutex
	run     *run
	last    int // the last segment asked for
	made    int // the highest segment known to be complete in this run
	touched time.Time
	ended   bool // stopped; a request that still holds it starts nothing
}

// run is one ffmpeg process of a job.
type run struct {
	cmd       *exec.Cmd
	start     int // its first segment
	done      chan struct{}
	err       error       // set before done closes
	paused    bool        // stopped with SIGSTOP by the throttle
	killing   atomic.Bool // killed on purpose: its exit is no failure
	transcode bool        // holds one of Engine's transcode slots
}

func jobKey(play string, p plan.Plan) string { return play + "|" + p.Key() }

// Request names one HLS play: the file, the plan and the Movie Night play
// it belongs to (one job per play and plan).
type Request struct {
	File string
	Plan plan.Plan
	Play string
}

// Playlist is the play's HLS playlist.
func (e *Engine) Playlist(ctx context.Context, r Request) ([]byte, error) {
	p, err := e.Probe(ctx, r.File)
	if err != nil {
		return nil, err
	}
	return Playlist(p.Duration), nil
}

// jobFor finds or makes the play's job, locked.
func (e *Engine) jobFor(r Request, p *probe.Probe) *job {
	key := jobKey(r.Play, r.Plan)
	e.mu.Lock()
	j := e.jobs[key]
	if j == nil {
		sum := sha256.Sum256([]byte(key))
		j = &job{key: key, play: r.Play, file: r.File, plan: r.Plan, probe: p,
			dir: filepath.Join(e.cfg.CacheDir, jobsDir, hex.EncodeToString(sum[:12])), made: -1, last: -1}
		e.jobs[key] = j
	}
	e.mu.Unlock()
	j.mu.Lock()
	j.touched = time.Now()
	return j
}

// Init is the path of the play's fMP4 init segment, starting the job when
// it has not run yet.
func (e *Engine) Init(ctx context.Context, r Request) (string, error) {
	p, err := e.Probe(ctx, r.File)
	if err != nil {
		return "", err
	}
	j := e.jobFor(r, p)
	path := filepath.Join(j.dir, "init.mp4")
	if j.run == nil || (closed(j.run.done) && !exists(path)) {
		if err := e.start(j, max(j.last, 0)); err != nil {
			e.forgetIdle(j)
			j.mu.Unlock()
			return "", err
		}
	}
	j.mu.Unlock()
	// ffmpeg creates init.mp4 at once but fills it in only as the first
	// segment is finished: the init is complete once that segment is. The
	// player asks for the init and its first segment together, so a seek
	// may restart ffmpeg meanwhile; then the new run's init is waited for.
	for range 4 {
		j.mu.Lock()
		rn := j.run
		j.mu.Unlock()
		first := filepath.Join(j.dir, strconv.Itoa(rn.start)+".m4s")
		_, err := e.wait(ctx, j, rn, first)
		if err == nil {
			return e.wait(ctx, j, rn, path)
		}
		j.mu.Lock()
		replaced := j.run != rn
		j.mu.Unlock()
		if !replaced {
			return "", err
		}
	}
	return "", errors.New("the play kept moving")
}

// Segment is the path of segment n of the play, made when needed: waited
// for when the running ffmpeg is about to write it, otherwise ffmpeg is
// (re)started at n.
func (e *Engine) Segment(ctx context.Context, r Request, n int) (string, error) {
	p, err := e.Probe(ctx, r.File)
	if err != nil {
		return "", err
	}
	if n < 0 || n >= Segments(p.Duration) {
		return "", ErrNotFound
	}
	j := e.jobFor(r, p)
	j.last = n
	path := filepath.Join(j.dir, strconv.Itoa(n)+".m4s")
	if exists(path) {
		j.mu.Unlock()
		return path, nil
	}
	rn := j.run
	running := rn != nil && !closed(rn.done)
	if !running || n < rn.start || n > j.madeLocked()+lookahead {
		if err := e.start(j, n); err != nil {
			e.forgetIdle(j)
			j.mu.Unlock()
			return "", err
		}
		rn = j.run
	} else if rn.paused {
		resume(rn)
	}
	j.mu.Unlock()
	return e.wait(ctx, j, rn, path)
}

// wait waits for path to appear while rn runs.
func (e *Engine) wait(ctx context.Context, j *job, rn *run, path string) (string, error) {
	deadline := time.NewTimer(segmentWait)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		if exists(path) {
			return path, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("ffmpeg made nothing in %s", segmentWait)
		case <-rn.done:
			if exists(path) {
				return path, nil
			}
			if rn.err != nil {
				return "", rn.err
			}
			// ffmpeg finished without it: past the end, or the run was
			// replaced by a seek elsewhere.
			j.mu.Lock()
			replaced := j.run != rn
			j.mu.Unlock()
			if replaced {
				return "", errors.New("the play moved elsewhere")
			}
			return "", ErrNotFound
		case <-tick.C:
		}
	}
}

// madeLocked is the highest complete segment of the job's current run,
// found by looking for the next ones on disk (segments appear in order).
func (j *job) madeLocked() int {
	if j.run == nil {
		return -1
	}
	if j.made < j.run.start-1 {
		j.made = j.run.start - 1
	}
	for exists(filepath.Join(j.dir, strconv.Itoa(j.made+1)+".m4s")) {
		j.made++
	}
	return j.made
}

// start replaces the job's ffmpeg with one starting at segment n. j.mu is
// held.
func (e *Engine) start(j *job, n int) error {
	if j.ended {
		return ErrNotFound
	}
	if old := j.run; old != nil {
		kill(old)
	}
	transcode := j.plan.Mode == plan.Transcode
	if transcode {
		e.mu.Lock()
		full := e.transcodes >= e.cfg.MaxTranscodes
		if !full {
			e.transcodes++
		}
		e.mu.Unlock()
		if full {
			return ErrBusy
		}
	}
	release := func() {
		if transcode {
			e.mu.Lock()
			e.transcodes--
			e.mu.Unlock()
		}
	}
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		release()
		return err
	}
	// Every run writes the init segment again; one from a run killed half
	// way must not be served meanwhile.
	_ = os.Remove(filepath.Join(j.dir, "init.mp4"))
	cmd := exec.Command(e.cfg.FFmpeg, hlsArgs(j.file, j.probe, j.plan, n, j.dir, e.cfg.HW)...)
	tail := &tailBuffer{max: 4096}
	cmd.Stderr = tail
	// Its own process group, so a kill or a pause reaches ffmpeg alone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		release()
		return fmt.Errorf("ffmpeg didn't start: %w", err)
	}
	rn := &run{cmd: cmd, start: n, done: make(chan struct{}), transcode: transcode}
	j.run, j.made = rn, n-1
	e.log.Info("media: ffmpeg started", "play", j.play, "mode", j.plan.Mode, "segment", n)
	go func() {
		err := cmd.Wait()
		release()
		if err != nil && !rn.killing.Load() {
			msg := strings.TrimSpace(tail.String())
			if msg == "" {
				msg = err.Error()
			}
			rn.err = fmt.Errorf("ffmpeg failed: %s", msg)
			e.log.Warn("media: ffmpeg failed", "play", j.play, "mode", j.plan.Mode, "err", msg)
		}
		close(rn.done)
	}()
	return nil
}

// forgetIdle drops a job left with no ffmpeg running (its start was
// refused as busy) and its folder, so it is not counted as a play. j.mu is
// held.
func (e *Engine) forgetIdle(j *job) {
	if j.run != nil && !closed(j.run.done) {
		return
	}
	e.mu.Lock()
	if e.jobs[j.key] == j {
		delete(e.jobs, j.key)
	}
	e.mu.Unlock()
	j.ended = true
	_ = os.RemoveAll(j.dir)
}

// kill kills a run and waits for it to be gone, so its last writes cannot
// land in the folder after the next run started. The owning job's mu is
// held.
func kill(rn *run) {
	if closed(rn.done) {
		return
	}
	rn.killing.Store(true)
	// SIGKILL reaches a paused process too.
	_ = syscall.Kill(-rn.cmd.Process.Pid, syscall.SIGKILL)
	<-rn.done
}

func resume(rn *run) {
	if rn.paused && !closed(rn.done) {
		_ = syscall.Kill(-rn.cmd.Process.Pid, syscall.SIGCONT)
		rn.paused = false
	}
}

// Stop ends every job of a play (the API says the play stopped, or moved
// to another audio track) and removes its folder.
func (e *Engine) Stop(play string) int {
	e.mu.Lock()
	var gone []*job
	for k, j := range e.jobs {
		if j.play == play {
			gone = append(gone, j)
			delete(e.jobs, k)
		}
	}
	e.mu.Unlock()
	for _, j := range gone {
		e.end(j)
	}
	return len(gone)
}

// end kills a job's run and removes its folder; the job is already out of
// the map.
func (e *Engine) end(j *job) {
	j.mu.Lock()
	j.ended = true
	if j.run != nil {
		kill(j.run)
	}
	j.mu.Unlock()
	_ = os.RemoveAll(j.dir)
	e.log.Info("media: play ended", "play", j.play, "mode", j.plan.Mode)
}

// tend runs every second: idle jobs end, and a run far ahead of its
// player pauses until the player catches up.
func (e *Engine) tend() {
	now := time.Now()
	e.mu.Lock()
	all := make([]*job, 0, len(e.jobs))
	for _, j := range e.jobs {
		all = append(all, j)
	}
	e.mu.Unlock()
	for _, j := range all {
		j.mu.Lock()
		idle := now.Sub(j.touched) > e.cfg.IdleAfter
		if rn := j.run; !idle && rn != nil && !closed(rn.done) {
			switch pace(rn.paused, j.madeLocked()-j.last) {
			case pauseRun:
				if syscall.Kill(-rn.cmd.Process.Pid, syscall.SIGSTOP) == nil {
					rn.paused = true
				}
			case resumeRun:
				resume(rn)
			}
		}
		j.mu.Unlock()
		if idle {
			e.mu.Lock()
			if e.jobs[j.key] == j {
				delete(e.jobs, j.key)
			}
			e.mu.Unlock()
			e.end(j)
		}
	}
}

// What tend does with a running ffmpeg.
type paceAction int

const (
	keepRun paceAction = iota
	pauseRun
	resumeRun
)

// pace decides from how many segments the run is ahead of the player's
// last request. The gap between pauseAhead and resumeAhead keeps it from
// stopping and starting ffmpeg every second around one line.
func pace(paused bool, ahead int) paceAction {
	switch {
	case !paused && ahead >= pauseAhead:
		return pauseRun
	case paused && ahead < resumeAhead:
		return resumeRun
	}
	return keepRun
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// tailBuffer keeps the last max bytes written (ffmpeg's messages).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b.Write(p)
	if over := t.b.Len() - t.max; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}
