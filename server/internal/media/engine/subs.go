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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/media/sidecar"
)

// extractTimeout bounds one extraction. Reading every subtitle of a film
// means reading the whole file once (subtitles are spread through it), so
// a big file on a slow disk takes a while.
const extractTimeout = 10 * time.Minute

// fileKey names a file's cache folder: its path, size and time, so a
// replaced file is extracted again.
func fileKey(file string) (string, error) {
	st, err := os.Stat(file)
	if err != nil {
		return "", ErrNotFound
	}
	sum := sha256.Sum256([]byte(file + "\x00" + strconv.FormatInt(st.Size(), 10) + "\x00" + strconv.FormatInt(st.ModTime().UnixNano(), 10)))
	return hex.EncodeToString(sum[:16]), nil
}

// textSub reports whether a subtitle codec is drawn by the player (as
// WebVTT, or as ASS by JASSUB) rather than burnt in.
func textSub(codec string) bool { return !plan.IsPictureSub(codec) }

// Subtitle is the path of embedded subtitle index as WebVTT ("vtt") or,
// for an ASS or SSA track, as the original ASS ("ass"). The first request
// for any of a file's subtitles extracts all of its text subtitles in one
// read of the file, so switching tracks afterwards is instant.
func (e *Engine) Subtitle(ctx context.Context, file string, index int, format string) (string, error) {
	p, err := e.Probe(ctx, file)
	if err != nil {
		return "", err
	}
	s, ok := p.Stream(index)
	if !ok || s.Type != "subtitle" || !textSub(s.Codec) {
		return "", ErrNotFound
	}
	if format == "ass" && s.Codec != "ass" && s.Codec != "ssa" {
		return "", ErrNotFound
	}
	key, err := fileKey(file)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(e.cfg.CacheDir, subsDir, key)
	want := filepath.Join(dir, strconv.Itoa(index)+"."+format)
	if exists(want) {
		touch(dir)
		return want, nil
	}
	_, err, _ = e.extract.Do("subs:"+key, func() (any, error) {
		if exists(want) {
			return nil, nil
		}
		// Detached from the request: a second request (or the player's
		// retry) for the same file waits for this one instead of starting
		// over.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractTimeout)
		defer cancel()
		return nil, e.extractSubs(ctx, file, p, dir)
	})
	if err != nil {
		return "", err
	}
	if !exists(want) {
		return "", ErrNotFound
	}
	return want, nil
}

// extractSubs writes every text subtitle of the file into dir, in one
// ffmpeg run, through a temporary folder renamed into place.
func (e *Engine) extractSubs(ctx context.Context, file string, p *probe.Probe, dir string) error {
	tmp := dir + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", file}
	n := 0
	for _, s := range p.Of("subtitle") {
		if !textSub(s.Codec) {
			continue
		}
		i := strconv.Itoa(s.Index)
		args = append(args, "-map", "0:"+i, "-c:s", "webvtt", "-f", "webvtt", filepath.Join(tmp, i+".vtt"))
		if s.Codec == "ass" || s.Codec == "ssa" {
			args = append(args, "-map", "0:"+i, "-c:s", "copy", "-f", "ass", filepath.Join(tmp, i+".ass"))
		}
		n++
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := e.ffmpeg(ctx, args...); err != nil {
		return fmt.Errorf("the subtitles couldn't be read: %w", err)
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

// External is an external subtitle file of a video, by its name relative
// to the video's folder (as sidecar.Match names it), as WebVTT ("vtt") or
// as ASS ("ass", for an ASS or SSA file). It answers the bytes: these
// files are small.
func (e *Engine) External(ctx context.Context, video, name, format string) ([]byte, error) {
	dir := filepath.Dir(video)
	f, ok := findSidecar(video, name)
	if !ok {
		return nil, ErrNotFound
	}
	path := filepath.Join(dir, filepath.FromSlash(f.Name))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrNotFound
	}
	switch {
	case format == "ass" && (f.Codec == "ass" || f.Codec == "ssa"):
		return raw, nil
	case format == "ass":
		return nil, ErrNotFound
	case f.Codec == "webvtt":
		return raw, nil
	}
	// SubRip and ASS become WebVTT. Old SubRip files are often not UTF-8
	// (Windows-1252 is by far the commonest otherwise); ffmpeg is told so
	// rather than writing broken characters.
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if !utf8.Valid(raw) {
		args = append(args, "-sub_charenc", "WINDOWS-1252")
	}
	args = append(args, "-i", path, "-f", "webvtt", "-")
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, e.cfg.FFmpeg, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("the subtitle file couldn't be read: %s", firstLine(errOut.String(), err))
	}
	return out.Bytes(), nil
}

// findSidecar checks name is one of video's own subtitle files, listed the
// way the scanner lists them, so a request can never reach another file.
func findSidecar(video, name string) (sidecar.File, bool) {
	dir := filepath.Dir(video)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sidecar.File{}, false
	}
	var names, subNames []string
	videos := 0
	for _, en := range entries {
		if en.IsDir() {
			if sidecar.Folders[strings.ToLower(en.Name())] {
				if list, err := os.ReadDir(filepath.Join(dir, en.Name())); err == nil {
					for _, s := range list {
						if !s.IsDir() {
							subNames = append(subNames, en.Name()+"/"+s.Name())
						}
					}
				}
			}
			continue
		}
		names = append(names, en.Name())
		if isVideoName(en.Name()) {
			videos++
		}
	}
	for _, f := range sidecar.Match(filepath.Base(video), names, subNames, videos == 1) {
		if f.Name == name {
			return f, true
		}
	}
	return sidecar.File{}, false
}

// videoExts mirrors scan.VideoExts (the engine does not depend on the
// scanner).
var videoExts = []string{".mkv", ".mp4", ".m4v", ".mov", ".avi", ".webm", ".ts", ".m2ts", ".mts", ".wmv", ".mpg",
	".mpeg", ".flv", ".ogv", ".3gp", ".vob", ".divx"}

func isVideoName(n string) bool { return slices.Contains(videoExts, strings.ToLower(filepath.Ext(n))) }

// Attachment is the path of an attached file (a font) of the video.
func (e *Engine) Attachment(ctx context.Context, file string, index int) (string, string, error) {
	p, err := e.Probe(ctx, file)
	if err != nil {
		return "", "", err
	}
	s, ok := p.Stream(index)
	if !ok || s.Type != "attachment" {
		return "", "", ErrNotFound
	}
	key, err := fileKey(file)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(e.cfg.CacheDir, fontsDir, key)
	out := filepath.Join(dir, strconv.Itoa(index))
	mime := s.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	if exists(out) {
		touch(dir)
		return out, mime, nil
	}
	_, err, _ = e.extract.Do("font:"+key+":"+strconv.Itoa(index), func() (any, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		tmp := out + ".tmp"
		_ = os.Remove(tmp)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		// Attachments sit in the file's header, so this reads only its
		// start; the null output is there because ffmpeg wants one.
		if err := e.ffmpeg(ctx, "-nostdin", "-hide_banner", "-loglevel", "error", "-dump_attachment:"+strconv.Itoa(index), tmp,
			"-i", file, "-t", "0", "-f", "null", "-"); err != nil && !exists(tmp) {
			return nil, fmt.Errorf("the font couldn't be read: %w", err)
		}
		return nil, os.Rename(tmp, out)
	})
	if err != nil {
		return "", "", err
	}
	return out, mime, nil
}

func (e *Engine) ffmpeg(ctx context.Context, args ...string) error {
	var errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, e.cfg.FFmpeg, args...)
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return errors.New("it took too long")
		}
		return errors.New(firstLine(errOut.String(), err))
	}
	return nil
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// touch marks a cache folder used, for the sweep.
func touch(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}
