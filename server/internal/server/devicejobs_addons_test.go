package server_test

// The Kodi add-on and the Jellyfin plugin, downloaded without signing in
// (tests/test_kodi.py's download test, tests/test_jellyfin_catalog.py).

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/addons"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

const fakeDLL = "MZ stand-in for the built plugin"

// pluginDir is a Jellyfin plugin folder as the image holds it, with a
// stand-in for the built DLL.
func pluginDir(t *testing.T) string {
	t.Helper()
	root, dir := testbase.RepoRoot(t), t.TempDir()
	for _, f := range []string{"meta.json", "movie-night.png"} {
		b, err := os.ReadFile(filepath.Join(root, "jellyfin-plugin", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "Jellyfin.Plugin.MovieNight.dll"), []byte(fakeDLL), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func addonsEnv(t *testing.T) testbase.Env {
	env := testbase.BaseEnv(t)
	env["KODI_ADDON_DIR"] = filepath.Join(testbase.RepoRoot(t), "kodi", "service.movienight")
	env["JELLYFIN_PLUGIN_DIR"] = pluginDir(t)
	return env
}

func unzip(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func TestKodiCanDownloadTheAddOnWithoutSigningIn(t *testing.T) {
	s := testenv.Start(t, addonsEnv(t))
	tv := s.Client("", false)
	index := want(t, tv.Do(t, "GET", "/kodi/", nil), 200)
	m := regexp.MustCompile(`href="(service\.movienight-[\d.]+\.zip)"`).FindStringSubmatch(index.Body)
	if m == nil || !strings.HasPrefix(index.Header.Get("Content-Type"), "text/html") {
		t.Fatal(index.Body)
	}
	z := want(t, tv.Do(t, "GET", "/kodi/"+m[1], nil), 200)
	if z.Header.Get("Content-Type") != "application/zip" {
		t.Fatal(z.Header)
	}
	var names []string
	for _, f := range unzip(t, []byte(z.Body)).File {
		names = append(names, f.Name)
		if strings.HasSuffix(f.Name, ".pyc") || strings.Contains(f.Name, "__pycache__") {
			t.Errorf("compiled file in the zip: %s", f.Name)
		}
	}
	all := strings.Join(names, "\n")
	if !strings.Contains(all, "service.movienight/addon.xml") || !strings.Contains(all, "service.movienight/resources/lib/service.py") {
		t.Fatal(all)
	}
	want(t, tv.Do(t, "GET", "/kodi/other.zip", nil), 404)
	if r := want(t, tv.Do(t, "GET", "/kodi", nil), 301); r.Header.Get("Location") != "/kodi/" {
		t.Fatal(r.Header)
	}
}

// copyTree copies the add-on to a new folder with every file dated when.
func copyTree(t *testing.T, from string, when time.Time) string {
	t.Helper()
	to := t.TempDir()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(dst, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
	return to
}

func TestTheZipsAreTheSameBytesOnEveryReplica(t *testing.T) {
	kodi := filepath.Join(testbase.RepoRoot(t), "kodi", "service.movienight")
	plugin := pluginDir(t)
	var zips, manifests []string
	// Two replicas whose images were unpacked at different times.
	for _, when := range []time.Time{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Now()} {
		env := testbase.BaseEnv(t)
		env["KODI_ADDON_DIR"] = copyTree(t, kodi, when)
		env["JELLYFIN_PLUGIN_DIR"] = copyTree(t, plugin, when)
		s := testenv.Start(t, env)
		c := s.Client("", false)
		name := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(c.Do(t, "GET", "/kodi/", nil).Body)[1]
		zips = append(zips, want(t, c.Do(t, "GET", "/kodi/"+name, nil), 200).Body)
		manifests = append(manifests, want(t, c.Do(t, "GET", "/jellyfin/manifest.json", nil), 200).Body)
	}
	if zips[0] != zips[1] {
		t.Fatal("two replicas serve different Kodi zips")
	}
	if manifests[0] != manifests[1] {
		t.Fatal("two replicas give the plugin different checksums")
	}
	if _, err := addons.New("https://mn.test", filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("a missing add-on folder started anyway")
	}
	if _, err := addons.New("https://mn.test", "", t.TempDir()); err == nil {
		t.Fatal("a plugin folder without the plugin started anyway")
	}
}

func TestTheJellyfinCatalogListsThePluginAndServesIt(t *testing.T) {
	s := testenv.Start(t, addonsEnv(t))
	jf := s.Client("", false) // no sign-in, as Jellyfin calls it
	r := want(t, jf.Do(t, "GET", "/jellyfin/manifest.json", nil), 200)
	var packages []struct {
		GUID, Name, Owner, Category, ImageURL string
		Versions                              []struct {
			Version, TargetAbi, SourceURL, Checksum string
		}
	}
	if err := json.Unmarshal([]byte(strings.NewReplacer(`"imageUrl"`, `"ImageURL"`, `"sourceUrl"`, `"SourceURL"`).Replace(r.Body)), &packages); err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || len(packages[0].Versions) != 1 {
		t.Fatal(r.Body)
	}
	p, v := packages[0], packages[0].Versions[0]
	if p.GUID != "5bf59830-f994-4e33-8e31-0b4570f490b2" || p.Name != "Movie Night" || p.Owner != "movienight" || p.Category != "General" ||
		p.ImageURL != "http://mn.test/jellyfin/movie-night.png" || v.TargetAbi != "12.1.0.0" ||
		v.SourceURL != "http://mn.test/jellyfin/movie-night_"+v.Version+".zip" {
		t.Fatalf("%+v", p)
	}
	got := want(t, jf.Do(t, "GET", strings.TrimPrefix(v.SourceURL, "http://mn.test"), nil), 200)
	sum := md5.Sum([]byte(got.Body))
	if got.Header.Get("Content-Type") != "application/zip" || hex.EncodeToString(sum[:]) != v.Checksum {
		t.Fatal("the zip does not match the manifest's checksum")
	}
	z := unzip(t, []byte(got.Body))
	if len(z.File) != 1 || z.File[0].Name != "Jellyfin.Plugin.MovieNight.dll" {
		t.Fatal(z.File)
	}
	rc, _ := z.File[0].Open()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(rc)
	if buf.String() != fakeDLL {
		t.Fatal(buf.String())
	}
	pic := want(t, jf.Do(t, "GET", "/jellyfin/movie-night.png", nil), 200)
	if pic.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(pic.Body, "\x89PNG\r\n\x1a\n") {
		t.Fatal(pic.Header)
	}
	want(t, jf.Do(t, "GET", "/jellyfin/other.zip", nil), 404)
	if r := want(t, jf.Do(t, "GET", "/jellyfin", nil), 301); r.Header.Get("Location") != "/jellyfin/manifest.json" {
		t.Fatal(r.Header)
	}
}

func TestTheDownloadsSayWhenTheyAreNotPartOfTheInstall(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	c := s.Client("", false)
	if r := want(t, c.Do(t, "GET", "/kodi/", nil), 503); !strings.Contains(r.Body, "KODI_ADDON_DIR") {
		t.Fatal(r.Body)
	}
	if r := want(t, c.Do(t, "GET", "/jellyfin/manifest.json", nil), 503); !strings.Contains(r.Body, "JELLYFIN_PLUGIN_DIR") {
		t.Fatal(r.Body)
	}
}
