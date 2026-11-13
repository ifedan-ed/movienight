// Package addons serves the two pieces of Movie Night that other apps
// install by address, without signing in (they cannot, and nothing here is
// private: each signs in with a code or a token afterwards):
//
//   - /kodi/: the Kodi add-on (kodi/service.movienight), as a directory
//     listing Kodi's "Install from zip file" can browse, and its zip;
//   - /jellyfin/: the Jellyfin plugin's repository (jellyfin-plugin/), the
//     manifest Jellyfin reads, the plugin's zip and its picture.
//
// Both zips are built once at start-up, from the files in the image, with a
// fixed date on every entry: the same files always give the same bytes, so
// every replica serves an identical zip and the checksum in the Jellyfin
// manifest holds across restarts. (The Python's Kodi zip took each file's
// modification time, so two replicas could serve two different zips.)
package addons

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// KodiAddonID is the add-on's id (its folder name in the zip).
const KodiAddonID = "service.movienight"

// The plugin's files in the Jellyfin plugin directory.
const (
	pluginDLL   = "out/Jellyfin.Plugin.MovieNight.dll"
	pluginMeta  = "meta.json"
	pluginImage = "movie-night.png"
)

// zipDate is every zip entry's date (the Python's Jellyfin zip used it too).
var zipDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Addons holds the built zips. A nil part (its directory not configured)
// answers 503 saying which setting is missing.
type Addons struct {
	publicURL string
	kodi      *kodi
	jellyfin  *plugin
}

type kodi struct {
	name string // service.movienight-1.0.3.zip
	zip  []byte
}

type plugin struct {
	manifest []byte
	zipName  string
	zip      []byte
	image    []byte
}

// New builds what is configured. kodiDir is the add-on's folder
// (KODI_ADDON_DIR), pluginDir the plugin's (JELLYFIN_PLUGIN_DIR); "" leaves
// that part off. A configured folder that cannot be read is an error, so a
// broken image fails at start, not at the first download.
func New(publicURL, kodiDir, pluginDir string) (*Addons, error) {
	a := &Addons{publicURL: strings.TrimRight(publicURL, "/")}
	if kodiDir != "" {
		k, err := buildKodi(kodiDir)
		if err != nil {
			return nil, fmt.Errorf("the Kodi add-on (KODI_ADDON_DIR): %w", err)
		}
		a.kodi = k
	}
	if pluginDir != "" {
		p, err := buildPlugin(pluginDir, a.publicURL)
		if err != nil {
			return nil, fmt.Errorf("the Jellyfin plugin (JELLYFIN_PLUGIN_DIR): %w", err)
		}
		a.jellyfin = p
	}
	return a, nil
}

var versionRe = regexp.MustCompile(`<addon\b[^>]*\bversion="([^"]+)"`)

func buildKodi(dir string) (*kodi, error) {
	xml, err := os.ReadFile(filepath.Join(dir, "addon.xml"))
	if err != nil {
		return nil, err
	}
	m := versionRe.FindSubmatch(xml)
	if m == nil {
		return nil, fmt.Errorf("addon.xml has no version")
	}
	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "__pycache__" {
			return filepath.SkipDir
		}
		if !d.IsDir() && !strings.HasSuffix(path, ".pyc") {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	entries := make([]entry, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{name: KodiAddonID + "/" + f, data: b})
	}
	z, err := makeZip(entries)
	if err != nil {
		return nil, err
	}
	return &kodi{name: fmt.Sprintf("%s-%s.zip", KodiAddonID, m[1]), zip: z}, nil
}

type entry struct {
	name string
	data []byte
}

func makeZip(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: zipDate})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// meta is jellyfin-plugin/meta.json, the fields the manifest repeats.
type meta struct {
	GUID        string `json:"guid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Overview    string `json:"overview"`
	Owner       string `json:"owner"`
	Category    string `json:"category"`
	Version     string `json:"version"`
	Changelog   string `json:"changelog"`
	TargetAbi   string `json:"targetAbi"`
	Timestamp   string `json:"timestamp"`
}

func buildPlugin(dir, publicURL string) (*plugin, error) {
	raw, err := os.ReadFile(filepath.Join(dir, pluginMeta))
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	dll, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(pluginDLL)))
	if err != nil {
		return nil, err
	}
	img, err := os.ReadFile(filepath.Join(dir, pluginImage))
	if err != nil {
		return nil, err
	}
	z, err := makeZip([]entry{{name: filepath.Base(pluginDLL), data: dll}})
	if err != nil {
		return nil, err
	}
	sum := md5.Sum(z) // what Jellyfin checks a download against
	p := &plugin{zipName: fmt.Sprintf("movie-night_%s.zip", m.Version), zip: z, image: img}
	base := publicURL + "/jellyfin"
	p.manifest, err = json.Marshal([]map[string]any{{
		"guid": m.GUID, "name": m.Name, "description": m.Description, "overview": m.Overview, "owner": m.Owner,
		"category": m.Category, "imageUrl": base + "/" + pluginImage,
		"versions": []map[string]any{{
			"version": m.Version, "changelog": m.Changelog, "targetAbi": m.TargetAbi,
			"sourceUrl": base + "/" + p.zipName, "checksum": hex.EncodeToString(sum[:]), "timestamp": m.Timestamp,
		}},
	}})
	return p, err
}

// Register adds the routes. Outside /api, so the API's JSON 404 and its
// sign-in redirect do not apply; these are plain files.
func (a *Addons) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /kodi", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/kodi/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /kodi/{name...}", a.kodiFile)
	mux.HandleFunc("GET /jellyfin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jellyfin/manifest.json", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /jellyfin/{name}", a.pluginFile)
}

func notSetUp(w http.ResponseWriter, what, setting string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `{"detail":"%s isn't part of this install (%s)"}`, what, setting)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"detail":"Not found"}`))
}

func (a *Addons) kodiFile(w http.ResponseWriter, r *http.Request) {
	if a.kodi == nil {
		notSetUp(w, "The Kodi add-on", "KODI_ADDON_DIR")
		return
	}
	name := r.PathValue("name")
	w.Header().Set("Cache-Control", "no-cache")
	switch name {
	case "":
		// A plain directory listing: Kodi's file browser reads links whose
		// text matches the address.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Index of /kodi/</title></head>`+
			`<body><h1>Index of /kodi/</h1><pre><a href="%[1]s">%[1]s</a></pre></body></html>`, a.kodi.name)
	case a.kodi.name:
		sendZip(w, name, a.kodi.zip)
	default:
		notFound(w)
	}
}

func sendZip(w http.ResponseWriter, name string, b []byte) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (a *Addons) pluginFile(w http.ResponseWriter, r *http.Request) {
	if a.jellyfin == nil {
		notSetUp(w, "The Jellyfin plugin", "JELLYFIN_PLUGIN_DIR")
		return
	}
	switch name := r.PathValue("name"); name {
	case "manifest.json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(a.jellyfin.manifest)
	case pluginImage:
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(a.jellyfin.image)
	case a.jellyfin.zipName:
		sendZip(w, name, a.jellyfin.zip)
	default:
		notFound(w)
	}
}
