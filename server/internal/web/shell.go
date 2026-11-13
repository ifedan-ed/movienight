package web

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/apierr"
	"movienight/server/internal/store"
)

type userRow = store.User

func nowUnix() int64 { return time.Now().Unix() }

func parseInt(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }

// Shell is the website: web/index.html (the one page every route draws) and
// web/static/, served from WEB_DIR unchanged, so the website and this server
// can be updated independently and the Python and Go serve the same files.
type Shell struct {
	page   []byte
	static string
}

// LoadShell reads index.html once and fills in its two values, which the
// Jinja template wrote as {{ v }} (the asset version, so phones never keep
// stale JS after a deploy) and {{ jf }} (the Jellyfin address links point
// at). Any other {{ left over means index.html changed in a way this code
// does not know, so startup stops rather than serving a broken page.
func LoadShell(webDir, jellyfinPublic string) (*Shell, error) {
	static := filepath.Join(webDir, "static")
	raw, err := os.ReadFile(filepath.Join(webDir, "index.html"))
	if err != nil {
		return nil, fmt.Errorf("WEB_DIR: %w", err)
	}
	version, err := assetVersion(static)
	if err != nil {
		return nil, fmt.Errorf("WEB_DIR: %w", err)
	}
	page := strings.ReplaceAll(string(raw), "{{ v }}", html.EscapeString(version))
	page = strings.ReplaceAll(page, "{{ jf }}", html.EscapeString(strings.TrimRight(jellyfinPublic, "/")))
	if strings.Contains(page, "{{") || strings.Contains(page, "{%") {
		return nil, errors.New("WEB_DIR/index.html has template code this server does not fill in")
	}
	return &Shell{page: []byte(page), static: static}, nil
}

// assetVersion changes whenever a static file changes: the first 10 hex
// characters of a sha1 over every file's path and bytes, in path order (the
// Python's _asset_version, so both servers give the same ?v=).
func assetVersion(static string) (string, error) {
	var files []string
	err := filepath.WalkDir(static, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	rels := make([]string, len(files))
	for i, f := range files {
		rel, _ := filepath.Rel(static, f)
		rels[i] = filepath.ToSlash(rel)
	}
	// Python sorted pathlib Paths, which compare part by part; sorting the
	// slash-joined strings gives the same order for these names.
	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return rels[idx[a]] < rels[idx[b]] })
	h := sha1.New()
	for _, i := range idx {
		body, err := os.ReadFile(files[i])
		if err != nil {
			return "", err
		}
		h.Write([]byte(rels[i]))
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))[:10], nil
}

// Static serves web/static/ with Cache-Control: no-cache, so the browser
// re-checks (cheap 304s) and a module imported without ?v= never runs stale
// code after a deploy. No directory listings.
func (s *Shell) Static() http.Handler {
	files := http.StripPrefix("/static/", http.FileServerFS(os.DirFS(s.static)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			apierr.Write(w, apierr.NotFound())
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// ServiceWorker serves /sw.js from the root, so it may look after every page.
func (s *Shell) ServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	http.ServeFile(w, r, filepath.Join(s.static, "sw.js"))
}

// appShell answers every other GET: the app shell, whose router (app.js)
// draws the page. A signed-out browser goes to sign-in first and comes back
// to this same page.
func (w *Web) appShell(rw http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/static/") {
		apierr.Write(rw, apierr.NotFound())
		return
	}
	if w.Config.SigninEnabled() {
		ok, err := w.Auth.HasIdentity(r.Context())
		if err != nil {
			w.Log.ErrorContext(r.Context(), "checking the session failed", "err", err)
			apierr.Write(rw, apierr.Internal())
			return
		}
		if !ok {
			http.Redirect(rw, r, w.SignInURL(r), http.StatusFound)
			return
		}
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-cache")
	_, _ = rw.Write(w.shell.page)
}
