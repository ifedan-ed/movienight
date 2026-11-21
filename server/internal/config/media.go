package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Media is the API's and the worker's side of the media libraries: where
// the media service is and the secret both ends share. Both empty: there
// are no libraries on this install (Jellyfin may still be).
type Media struct {
	// MEDIA_URL: the media service's address as the API reaches it
	// (http://media:8100 in the shipped compose file, or another machine's).
	URL string
	// MEDIA_SECRET: shared with the media service; every call carries it.
	Secret string
}

// Enabled reports whether libraries are on.
func (m Media) Enabled() bool { return m.URL != "" }

func (r *reader) media() Media {
	m := Media{URL: strings.TrimRight(r.optional("MEDIA_URL"), "/"), Secret: r.optional("MEDIA_SECRET")}
	if (m.URL == "") != (m.Secret == "") {
		r.fail("MEDIA_URL and MEDIA_SECRET must be set together (or both left out for no media libraries)")
	}
	r.absoluteURL("MEDIA_URL", m.URL)
	if m.Secret != "" && len(m.Secret) < 16 {
		r.fail("MEDIA_SECRET must be at least 16 characters")
	}
	return m
}

// MediaService is the media command's own settings (cmd/media). It reads
// nothing else: it runs next to the files, often on another machine, and
// needs neither the database nor Valkey.
type MediaService struct {
	// MEDIA_SECRET: the same value as the API's.
	Secret string
	// MEDIA_LIBRARIES: the folders the service may read, as it sees them
	// (comma-separated full paths, e.g. /media). A library's folders must be
	// inside one of them; nothing outside is ever opened.
	Libraries []string
	// MEDIA_CACHE_DIR: transcoded segments and extracted subtitles and
	// fonts. Default: movienight-media in the system's temporary folder.
	CacheDir string
	// MEDIA_MAX_TRANSCODES: transcodes at once (a picture encoded again);
	// one more is refused with "try again in a moment". Default 2.
	MaxTranscodes int
	// MEDIA_PROBE_WORKERS: files ffprobe reads at once while scanning.
	// Default 4.
	ProbeWorkers int
	// MEDIA_HWACCEL: none (default), vaapi, qsv or nvenc.
	HWAccel string
	// MEDIA_HWACCEL_DEVICE: the GPU render node for vaapi and qsv.
	// Default /dev/dri/renderD128.
	HWDevice string
}

// LoadMediaService reads the media command's settings through lookup.
func LoadMediaService(lookup func(string) (string, bool)) (*MediaService, error) {
	r := reader{lookup: lookup}
	m := &MediaService{Secret: r.required("MEDIA_SECRET")}
	if m.Secret != "" && len(m.Secret) < 16 {
		r.fail("MEDIA_SECRET must be at least 16 characters")
	}
	for _, d := range r.list("MEDIA_LIBRARIES", false) {
		if !filepath.IsAbs(d) {
			r.fail(fmt.Sprintf("MEDIA_LIBRARIES: %q is not a full path", d))
			continue
		}
		m.Libraries = append(m.Libraries, filepath.Clean(d))
	}
	if len(m.Libraries) == 0 {
		r.fail("MEDIA_LIBRARIES is required and not set (the folders the media service may read, e.g. /media)")
	}
	m.CacheDir = r.withDefault("MEDIA_CACHE_DIR", filepath.Join(os.TempDir(), "movienight-media"))
	m.MaxTranscodes = r.positive("MEDIA_MAX_TRANSCODES", 2)
	m.ProbeWorkers = r.positive("MEDIA_PROBE_WORKERS", 4)
	m.HWAccel = strings.ToLower(r.withDefault("MEDIA_HWACCEL", "none"))
	if !slices.Contains([]string{"none", "vaapi", "qsv", "nvenc"}, m.HWAccel) {
		r.fail(fmt.Sprintf("MEDIA_HWACCEL must be none, vaapi, qsv or nvenc, not %q", m.HWAccel))
	}
	m.HWDevice = r.withDefault("MEDIA_HWACCEL_DEVICE", "/dev/dri/renderD128")
	if len(r.problems) > 0 {
		return nil, errors.New("settings are not usable:\n  - " + strings.Join(r.problems, "\n  - "))
	}
	return m, nil
}
