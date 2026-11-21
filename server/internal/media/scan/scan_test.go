package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/media/probe"
)

// fakeProber records what it probed; a file named "broken.mkv" fails.
type fakeProber struct {
	mu   sync.Mutex
	runs []string
}

func (f *fakeProber) Run(_ context.Context, file string) (*probe.Probe, error) {
	f.mu.Lock()
	f.runs = append(f.runs, file)
	f.mu.Unlock()
	if filepath.Base(file) == "broken.mkv" {
		return nil, os.ErrInvalid
	}
	return &probe.Probe{Container: "mkv", Duration: 60}, nil
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, s *Scanner, req Request) []Event {
	t.Helper()
	var out []Event
	if err := s.Scan(context.Background(), req, func(e Event) error { out = append(out, e); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func files(evs []Event) map[string]Event {
	out := map[string]Event{}
	for _, e := range evs {
		if e.Type == EventFile {
			out[e.Path] = e
		}
	}
	return out
}

func known(evs []Event) []Known {
	var out []Known
	for _, e := range evs {
		if e.Type == EventFile {
			out = append(out, Known{Path: e.Path, Size: e.Size, MTime: e.MTime, Subs: e.Subs})
		}
	}
	return out
}

func TestScanIncremental(t *testing.T) {
	root := t.TempDir()
	film := filepath.Join(root, "Film (2001)", "Film (2001).mkv")
	write(t, film, "a")
	write(t, filepath.Join(root, "Film (2001)", "Film (2001).en.srt"), "1")
	write(t, filepath.Join(root, "Other (2002)", "Other.mp4"), "b")
	write(t, filepath.Join(root, "Other (2002)", "notes.nfo"), "x")
	write(t, filepath.Join(root, ".hidden", "Secret.mkv"), "c")
	write(t, filepath.Join(root, "@eaDir", "thumb.mkv"), "d")
	write(t, filepath.Join(root, "Broken", "broken.mkv"), "e")

	fp := &fakeProber{}
	s := &Scanner{Prober: fp, Workers: 2, Allowed: []string{root}}
	first := run(t, s, Request{Roots: []string{root}})
	got := files(first)
	if len(got) != 3 {
		t.Fatalf("first scan found %d files: %v", len(got), got)
	}
	if e := got[film]; e.Probe == nil || len(e.Probe.External) != 1 || e.Probe.External[0].Language != "eng" || e.Subs == "" {
		t.Fatalf("film event %+v", e)
	}
	if e := got[filepath.Join(root, "Broken", "broken.mkv")]; e.Probe != nil || e.Error == "" {
		t.Fatalf("a broken file must come back with its error: %+v", e)
	}
	if last := first[len(first)-1]; last.Type != EventDone || len(last.Missing) != 0 || last.Seen != 3 {
		t.Fatalf("done %+v", last)
	}

	// Nothing changed: nothing is probed again.
	fp.runs = nil
	second := run(t, s, Request{Roots: []string{root}, Known: known(first)})
	if len(files(second)) != 0 || len(fp.runs) != 0 {
		t.Fatalf("an unchanged library was probed again: %v", fp.runs)
	}

	// A changed file, a new subtitle next to an unchanged one, a deleted file.
	write(t, filepath.Join(root, "Other (2002)", "Other.mp4"), "bigger")
	write(t, filepath.Join(root, "Film (2001)", "Film (2001).fr.srt"), "2")
	if err := os.Remove(filepath.Join(root, "Broken", "broken.mkv")); err != nil {
		t.Fatal(err)
	}
	fp.runs = nil
	third := run(t, s, Request{Roots: []string{root}, Known: known(first)})
	changed := files(third)
	if len(changed) != 2 || changed[film].Probe == nil || len(changed[film].Probe.External) != 2 {
		t.Fatalf("changed files: %+v", changed)
	}
	done := third[len(third)-1]
	if !slices.Equal(done.Missing, []string{filepath.Join(root, "Broken", "broken.mkv")}) {
		t.Fatalf("missing %v", done.Missing)
	}
}

func TestScanNeverRemovesWhatItCannotSee(t *testing.T) {
	root := t.TempDir()
	film := filepath.Join(root, "Film.mkv")
	write(t, film, "a")
	s := &Scanner{Prober: &fakeProber{}, Workers: 1, Allowed: []string{root}}
	before := known(run(t, s, Request{Roots: []string{root}}))

	// The folder emptied (an unmounted disk): nothing missing.
	if err := os.Remove(film); err != nil {
		t.Fatal(err)
	}
	evs := run(t, s, Request{Roots: []string{root}, Known: before})
	if evs[0].Type != EventRoot || len(evs[len(evs)-1].Missing) != 0 {
		t.Fatalf("an empty folder removed files: %+v", evs)
	}

	// A folder that is gone, and one outside MEDIA_LIBRARIES.
	gone := filepath.Join(root, "gone")
	evs = run(t, s, Request{Roots: []string{gone, "/etc"}, Known: []Known{{Path: filepath.Join(gone, "x.mkv"), Size: 1}}})
	roots := 0
	for _, e := range evs {
		if e.Type == EventRoot {
			roots++
		}
	}
	if roots != 2 || len(evs[len(evs)-1].Missing) != 0 {
		t.Fatalf("bad roots: %+v", evs)
	}
}

func TestScanStopsWhenTheAPIGoesAway(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		write(t, filepath.Join(root, n), n)
	}
	s := &Scanner{Prober: &fakeProber{}, Workers: 1, Allowed: []string{root}, progressEvery: time.Hour}
	err := s.Scan(context.Background(), Request{Roots: []string{root}}, func(e Event) error {
		if e.Type == EventFile {
			return context.Canceled
		}
		return nil
	})
	if err == nil {
		t.Fatal("a failing emit did not stop the scan")
	}
}
