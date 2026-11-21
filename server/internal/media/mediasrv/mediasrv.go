// Package mediasrv is the media service's HTTP side (cmd/media): scans,
// the file itself, HLS, subtitles and fonts, and the play stop, each on a
// route of package protocol behind the shared secret.
//
// It reads only files inside its media folders (MEDIA_LIBRARIES), whatever
// a caller asks for: the secret proves the API is calling, the folders
// bound what even the API can reach.
package mediasrv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/media/engine"
	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/protocol"
	"movienight/server/internal/media/scan"
)

// Server serves the media service's routes.
type Server struct {
	Secret  string
	Allowed []string // MEDIA_LIBRARIES
	Scanner *scan.Scanner
	Engine  *engine.Engine
	Log     *slog.Logger
}

// maxScanRequest caps a scan's body: the known files of a big library
// (100,000 files at ~200 bytes each is 20 MB).
const maxScanRequest = 64 << 20

// writeIdle: a scan line the API does not take for this long ends the
// scan (the server itself has no write timeout: a film is a long answer).
const writeIdle = 2 * time.Minute

var (
	segmentRe  = regexp.MustCompile(`^(\d{1,6})\.m4s$`)
	subtitleRe = regexp.MustCompile(`^(\d{1,5})\.(vtt|ass)$`)
	fontRe     = regexp.MustCompile(`^(\d{1,5})$`)
)

// Handler is every route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+protocol.Health, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("POST "+protocol.Scan, s.auth(s.scan))
	mux.HandleFunc("GET "+protocol.File, s.auth(s.file))
	mux.HandleFunc("GET "+protocol.Playlist, s.auth(s.playlist))
	mux.HandleFunc("GET "+protocol.Init, s.auth(s.init))
	mux.HandleFunc("GET "+protocol.SegmentDir+"{name}", s.auth(s.segment))
	mux.HandleFunc("GET "+protocol.SubtitleDir+"{name}", s.auth(s.subtitle))
	mux.HandleFunc("GET "+protocol.FontDir+"{name}", s.auth(s.font))
	mux.HandleFunc("POST "+protocol.Stop, s.auth(s.stop))
	mux.HandleFunc("GET "+protocol.Status, s.auth(func(w http.ResponseWriter, _ *http.Request) {
		// The folders too: the Libraries page offers them when adding one.
		writeJSON(w, struct {
			engine.Status
			Folders []string `json:"folders"`
		}{s.Engine.Status(), s.Allowed})
	}))
	return mux
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !protocol.Authorized(r, s.Secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// fileParam is the request's video, refused unless it is a file inside
// the media folders.
func (s *Server) fileParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	f := r.URL.Query().Get(protocol.KeyFile)
	if f == "" || !filepath.IsAbs(f) {
		http.Error(w, "no file", http.StatusBadRequest)
		return "", false
	}
	f = filepath.Clean(f)
	inside := false
	for _, a := range s.Allowed {
		if scan.Under(f, a) {
			inside = true
		}
	}
	if !inside || !scan.VideoExts[strings.ToLower(filepath.Ext(f))] {
		http.Error(w, "outside the media folders", http.StatusForbidden)
		return "", false
	}
	return f, true
}

func (s *Server) request(w http.ResponseWriter, r *http.Request) (engine.Request, bool) {
	f, ok := s.fileParam(w, r)
	if !ok {
		return engine.Request{}, false
	}
	p, err := plan.Parse(r.URL.Query().Get(protocol.KeyPlan))
	if err != nil || p.Mode == plan.Direct {
		http.Error(w, "bad plan", http.StatusBadRequest)
		return engine.Request{}, false
	}
	play := r.URL.Query().Get(protocol.KeyPlay)
	if play == "" || len(play) > 64 {
		http.Error(w, "no play", http.StatusBadRequest)
		return engine.Request{}, false
	}
	return engine.Request{File: f, Plan: p, Play: play}, true
}

// scan streams a scan's events as JSON lines, flushed one by one.
func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	var req scan.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, maxScanRequest)).Decode(&req); err != nil {
		http.Error(w, "the scan request isn't JSON", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w)
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	err := s.Scanner.Scan(r.Context(), req, func(e scan.Event) error {
		_ = rc.SetWriteDeadline(time.Now().Add(writeIdle))
		if err := enc.Encode(e); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		return rc.Flush()
	})
	if err != nil && r.Context().Err() == nil {
		s.Log.Warn("media: scan stopped", "err", err)
	}
}

// file is the video itself, with Range (http.ServeContent): seeking in a
// direct play is the browser asking for another range.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileParam(w, r)
	if !ok {
		return
	}
	fh, err := os.Open(f)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType(f))
	// The writer is passed as it is, so the copy can use sendfile.
	http.ServeContent(w, r, filepath.Base(f), st.ModTime(), fh)
}

func contentType(f string) string {
	switch strings.ToLower(filepath.Ext(f)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	}
	return "application/octet-stream"
}

func (s *Server) playlist(w http.ResponseWriter, r *http.Request) {
	req, ok := s.request(w, r)
	if !ok {
		return
	}
	body, err := s.Engine.Playlist(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

func (s *Server) init(w http.ResponseWriter, r *http.Request) {
	req, ok := s.request(w, r)
	if !ok {
		return
	}
	path, err := s.Engine.Init(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serveFile(w, r, path, "video/mp4")
}

func (s *Server) segment(w http.ResponseWriter, r *http.Request) {
	m := segmentRe.FindStringSubmatch(r.PathValue("name"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	req, ok := s.request(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(m[1])
	path, err := s.Engine.Segment(r.Context(), req, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serveFile(w, r, path, "video/iso.segment")
}

func (s *Server) subtitle(w http.ResponseWriter, r *http.Request) {
	m := subtitleRe.FindStringSubmatch(r.PathValue("name"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	f, ok := s.fileParam(w, r)
	if !ok {
		return
	}
	ctype := "text/vtt; charset=utf-8"
	if m[2] == "ass" {
		ctype = "text/x-ssa; charset=utf-8"
	}
	if name := r.URL.Query().Get(protocol.KeyName); name != "" {
		body, err := s.Engine.External(r.Context(), f, name, m[2])
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	index, _ := strconv.Atoi(m[1])
	path, err := s.Engine.Subtitle(r.Context(), f, index, m[2])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serveFile(w, r, path, ctype)
}

func (s *Server) font(w http.ResponseWriter, r *http.Request) {
	m := fontRe.FindStringSubmatch(r.PathValue("name"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	f, ok := s.fileParam(w, r)
	if !ok {
		return
	}
	index, _ := strconv.Atoi(m[1])
	path, mime, err := s.Engine.Attachment(r.Context(), f, index)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serveFile(w, r, path, mime)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	play := r.URL.Query().Get(protocol.KeyPlay)
	if play == "" {
		http.Error(w, "no play", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]int{"stopped": s.Engine.Stop(play)})
}

// fail answers an engine error: 404 for what is not there, 503 when the
// transcodes are all taken, 502 for ffmpeg failing (logged).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, engine.ErrBusy):
		w.Header().Set("Retry-After", "10")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, context.Canceled) || r.Context().Err() != nil:
		// The player moved on (a seek cancels the segment in flight).
	default:
		s.Log.Warn("media: request failed", "path", r.URL.Path, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, path, ctype string) {
	fh, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	http.ServeContent(w, r, "", st.ModTime(), fh)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
