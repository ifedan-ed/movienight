// Package scan walks a library's folders on the machine that holds the
// files and reports what changed since the API last looked: new and
// changed videos (probed with ffprobe, with their subtitle files), and
// videos that are gone.
//
// The API sends what it already knows of each file (its size, modification
// time and subtitle files); an unchanged file costs one stat and is never
// probed again. Probes run in a bounded pool, and results stream back as
// they finish, so a first scan of a big library shows progress at once.
// The scanner keeps no state of its own: the API's database is the record.
package scan

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"movienight/server/internal/media/naming"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/media/sidecar"
)

// VideoExts are the files taken as videos.
var VideoExts = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".webm": true,
	".ts": true, ".m2ts": true, ".mts": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".ogv": true,
	".3gp": true, ".vob": true, ".divx": true}

// skipDirs are folders that never hold a library's videos: NAS and
// desktop housekeeping.
var skipDirs = map[string]bool{"@eadir": true, "#recycle": true, "$recycle.bin": true, "lost+found": true,
	"system volume information": true, "#snapshot": true, ".snapshots": true}

// Known is what the API has on record for one file.
type Known struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix nanoseconds
	Subs  string `json:"subs"`  // SubsSig of its subtitle files
}

// Request is one scan: the library's folders, what they hold (films or
// shows: extras are told apart by name) and what is known of them.
type Request struct {
	Roots []string    `json:"roots"`
	Kind  naming.Kind `json:"kind"`
	Known []Known     `json:"known"`
}

// Event types, in the order a scan sends them: "file" and "progress" as the
// walk and the probes go, "root" for a folder that could not be read, and
// one "done" at the end.
const (
	EventFile     = "file"
	EventProgress = "progress"
	EventRoot     = "root"
	EventDone     = "done"
)

// Event is one line of a scan's answer.
type Event struct {
	Type string `json:"type"`

	// file: a new or changed video. Probe is nil when ffprobe could not read
	// it; Error says why.
	Path  string       `json:"path,omitempty"`
	Size  int64        `json:"size,omitempty"`
	MTime int64        `json:"mtime,omitempty"`
	Subs  string       `json:"subs,omitempty"`
	Probe *probe.Probe `json:"probe,omitempty"`

	// root: a folder that could not be read. Nothing under it is reported
	// missing.
	Root  string `json:"root,omitempty"`
	Error string `json:"error,omitempty"`

	// progress and done.
	Seen    int `json:"seen,omitempty"`     // videos found so far
	ToProbe int `json:"to_probe,omitempty"` // new or changed among them
	Probed  int `json:"probed,omitempty"`

	// done: known files that are no longer there.
	Missing []string `json:"missing,omitempty"`
}

// Prober probes one file (probe.Runner outside tests).
type Prober interface {
	Run(ctx context.Context, file string) (*probe.Probe, error)
}

// Scanner scans.
type Scanner struct {
	Prober Prober
	// Workers is how many probes run at once.
	Workers int
	// Allowed are the folders the media service may read
	// (MEDIA_LIBRARIES); a root outside them is refused.
	Allowed []string
	// progressEvery is how often progress is sent while probing.
	progressEvery time.Duration
}

// found is one video the walk found.
type found struct {
	path  string
	size  int64
	mtime int64
	subs  []sidecar.File
	sig   string
}

// Scan walks req's roots and calls emit for every event, from one
// goroutine at a time. It returns emit's first error (the API went away)
// or the context's.
func (s *Scanner) Scan(ctx context.Context, req Request, emit func(Event) error) error {
	known := make(map[string]Known, len(req.Known))
	for _, k := range req.Known {
		known[k.Path] = k
	}
	var (
		mu      sync.Mutex
		emitErr error
	)
	send := func(e Event) error {
		mu.Lock()
		defer mu.Unlock()
		if emitErr != nil {
			return emitErr
		}
		emitErr = emit(e)
		return emitErr
	}

	// Walk first (cheap: directory reads and stats), so the progress has a
	// total; then probe what is new.
	seen := map[string]bool{}
	badRoots := []string{}
	var todo []found
	for _, root := range req.Roots {
		root = filepath.Clean(root)
		if err := s.allowed(root); err != nil {
			badRoots = append(badRoots, root)
			if err := send(Event{Type: EventRoot, Root: root, Error: err.Error()}); err != nil {
				return err
			}
			continue
		}
		var files []found
		var bad []Event
		err := walk(ctx, root, root, &files, &bad)
		for _, b := range bad {
			// A folder inside the library that can't be read: its videos are
			// kept as they are, the rest of the library goes on.
			badRoots = append(badRoots, b.Root)
			if err := send(b); err != nil {
				return err
			}
		}
		if err == nil && len(files) == 0 && len(bad) == 0 && knownUnder(known, root) {
			// An empty folder where videos were is far likelier an unmounted
			// disk than a library emptied on purpose: nothing is removed.
			err = errors.New("the folder is empty now; nothing was removed")
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			badRoots = append(badRoots, root)
			if err := send(Event{Type: EventRoot, Root: root, Error: err.Error()}); err != nil {
				return err
			}
			continue
		}
		for _, f := range files {
			if seen[f.path] {
				continue // two roots that overlap
			}
			if rel, err := filepath.Rel(root, f.path); err == nil && naming.Parse(filepath.ToSlash(rel), req.Kind).Skip {
				// A trailer, a sample, an extra: never probed nor reported, so
				// one that was known is now missing and goes.
				continue
			}
			seen[f.path] = true
			k, ok := known[f.path]
			if ok && k.Size == f.size && k.MTime == f.mtime && k.Subs == f.sig {
				continue
			}
			todo = append(todo, f)
		}
	}
	if err := send(Event{Type: EventProgress, Seen: len(seen), ToProbe: len(todo)}); err != nil {
		return err
	}

	workers := max(1, s.Workers)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	var probed int
	last := time.Now()
	every := s.progressEvery
	if every == 0 {
		every = 2 * time.Second
	}
	for _, f := range todo {
		g.Go(func() error {
			ev := Event{Type: EventFile, Path: f.path, Size: f.size, MTime: f.mtime, Subs: f.sig}
			p, err := s.Prober.Run(gctx, f.path)
			if gctx.Err() != nil {
				return gctx.Err()
			}
			if err != nil {
				ev.Error = err.Error()
			} else {
				p.External = f.subs
				ev.Probe = p
			}
			if err := send(ev); err != nil {
				return err
			}
			mu.Lock()
			probed++
			n, due := probed, time.Since(last) >= every
			if due {
				last = time.Now()
			}
			mu.Unlock()
			if due {
				return send(Event{Type: EventProgress, Seen: len(seen), ToProbe: len(todo), Probed: n})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	missing := []string{}
	for p := range known {
		if !seen[p] && !underAny(p, badRoots) && underAny(p, req.Roots) {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	return send(Event{Type: EventDone, Seen: len(seen), ToProbe: len(todo), Probed: len(todo), Missing: missing})
}

// allowed checks root is a readable folder inside MEDIA_LIBRARIES.
func (s *Scanner) allowed(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%s is not a full path", root)
	}
	if !underAny(root, s.Allowed) {
		return fmt.Errorf("%s is outside the media folders (MEDIA_LIBRARIES)", root)
	}
	st, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("the folder can't be read: %s", plainErr(err))
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", root)
	}
	return nil
}

// Under reports whether p is dir or inside it.
func Under(p, dir string) bool {
	dir = filepath.Clean(dir)
	p = filepath.Clean(p)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if Under(p, d) {
			return true
		}
	}
	return false
}

func knownUnder(known map[string]Known, root string) bool {
	for p := range known {
		if Under(p, root) {
			return true
		}
	}
	return false
}

// walk lists every video under dir with its subtitle files. The root
// itself failing is the error; a folder or file below it that can't be
// read is added to bad instead (as a "root" event: nothing under it is
// reported missing, so its videos are not removed).
func walk(ctx context.Context, root, dir string, out *[]found, bad *[]Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if dir == root {
			return fmt.Errorf("the folder can't be read: %s", plainErr(err))
		}
		*bad = append(*bad, Event{Type: EventRoot, Root: dir, Error: "can't be read: " + plainErr(err)})
		return nil
	}
	var videos, names, subDirs, dirs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			// A linked file is followed; a linked folder is not (a link back
			// up the tree would never end).
			st, err := os.Stat(filepath.Join(dir, name))
			if err != nil || st.IsDir() {
				continue
			}
		}
		if isDir {
			if skipDirs[strings.ToLower(name)] {
				continue
			}
			if sidecar.Folders[strings.ToLower(name)] {
				subDirs = append(subDirs, name)
			}
			dirs = append(dirs, name)
			continue
		}
		names = append(names, name)
		if VideoExts[strings.ToLower(filepath.Ext(name))] {
			videos = append(videos, name)
		}
	}
	// A Subs folder's files, named with the folder in front.
	var subNames []string
	if len(videos) == 1 {
		for _, sd := range subDirs {
			list, err := os.ReadDir(filepath.Join(dir, sd))
			if err != nil {
				continue // subtitles are extras: a bad Subs folder costs only them
			}
			for _, e := range list {
				if !e.IsDir() {
					subNames = append(subNames, sd+"/"+e.Name())
				}
			}
		}
	}
	for _, v := range videos {
		full := filepath.Join(dir, v)
		st, err := os.Stat(full)
		if err != nil {
			*bad = append(*bad, Event{Type: EventRoot, Root: full, Error: "can't be read: " + plainErr(err)})
			continue
		}
		subs := sidecar.Match(v, names, subNames, len(videos) == 1)
		*out = append(*out, found{path: full, size: st.Size(), mtime: st.ModTime().UnixNano(), subs: subs,
			sig: SubsSig(dir, subs)})
	}
	for _, d := range dirs {
		if sidecar.Folders[strings.ToLower(d)] && len(videos) == 1 {
			continue
		}
		if err := walk(ctx, root, filepath.Join(dir, d), out, bad); err != nil {
			return err
		}
	}
	return nil
}

// SubsSig is a short signature of a video's subtitle files (names, sizes
// and times), so a subtitle added next to an unchanged video is noticed.
func SubsSig(dir string, subs []sidecar.File) string {
	if len(subs) == 0 {
		return ""
	}
	h := fnv.New64a()
	for _, s := range subs {
		h.Write([]byte(s.Name))
		if st, err := os.Stat(filepath.Join(dir, s.Name)); err == nil {
			h.Write([]byte(strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)))
		}
		h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// plainErr drops the path Go puts in front of a file error (the event
// names the path already).
func plainErr(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
