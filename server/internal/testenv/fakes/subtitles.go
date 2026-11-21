package fakes

// The subtitle sites for the player's "Search online" (internal/subtitles):
// OpenSubtitles under /opensubtitles/api/v1, Podnapisi under
// /podnapisi/subtitles and Gestdown under /gestdown, in each site's own
// answer shapes. Every title gets the same few subtitles, timed for the
// browser tests' 60-second clip.

import (
	"archive/zip"
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Subtitle lines the fakes serve, for the tests to look for.
const (
	// OnlineLine is the OpenSubtitles subtitle's first line (1 to 3 s);
	// OnlineLongLine its second (4 to 30 s).
	OnlineLine     = "Online hello"
	OnlineLongLine = "Online long line"
	// PodnapisiLine is in Windows-1252 in a zip ("Café"): the test of the
	// charset and the unzip.
	PodnapisiLine = "Café au lait"
)

// SubsRelease is the release the hash-matched subtitle names: the same
// as the fake Jellyfin's clip file.
const SubsRelease = "Movie.603.2000.1080p.BluRay.x264-GRP"

// Subtitles is the fake subtitle sites.
type Subtitles struct{ *Server }

// NewSubtitles starts them.
func NewSubtitles(t testing.TB) *Subtitles {
	s := &Subtitles{}
	s.Server = newServer(t, func(_ *Server, w http.ResponseWriter, r *http.Request) { s.route(w, r) })
	return s
}

func onlineSRT() string {
	return "1\r\n00:00:01,000 --> 00:00:03,000\r\n" + OnlineLine + "\r\n\r\n2\r\n00:00:04,000 --> 00:00:30,000\r\n" +
		OnlineLongLine + "\r\n"
}

// podnapisiZip is a zip holding a Windows-1252 SRT next to an .nfo.
func podnapisiZip() []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	nfo, _ := zw.Create("readme.nfo")
	_, _ = nfo.Write([]byte("not a subtitle"))
	f, _ := zw.Create("Movie.603.2000.DVDRip.srt")
	// "é" is 0xE9 in Windows-1252: not valid UTF-8 on its own.
	_, _ = f.Write([]byte("1\r\n00:00:01,000 --> 00:00:20,000\r\nCaf\xe9 au lait\r\n"))
	_ = zw.Close()
	return b.Bytes()
}

func (s *Subtitles) route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	q := r.URL.Query()
	switch {
	case p == "/opensubtitles/api/v1/subtitles":
		// The hash-matched result first, and a machine translation the
		// player hides by default.
		Reply(w, JSON{"total_count": 2, "data": []JSON{
			{"id": "1", "attributes": JSON{"language": q.Get("languages"), "download_count": 100, "ratings": 8.0,
				"release": SubsRelease, "moviehash_match": q.Get("moviehash") != "",
				"files": []JSON{{"file_id": 1001, "file_name": SubsRelease + ".srt"}}}},
			{"id": "2", "attributes": JSON{"language": q.Get("languages"), "download_count": 5000,
				"release": "Movie.603.2000.WEB", "machine_translated": true,
				"files": []JSON{{"file_id": 1002, "file_name": "machine.srt"}}}},
		}})
	case p == "/opensubtitles/api/v1/download" && r.Method == http.MethodPost:
		Reply(w, JSON{"link": "http://" + r.Host + "/opensubtitles/file/1001.srt", "file_name": "x.srt", "requests": 1,
			"remaining": 19, "message": "", "reset_time": "", "reset_time_utc": ""})
	case strings.HasPrefix(p, "/opensubtitles/file/"):
		w.Header().Set("Content-Type", "application/x-subrip")
		_, _ = w.Write([]byte(onlineSRT()))
	case p == "/podnapisi/subtitles/search/advanced":
		Reply(w, JSON{"page": 1, "all_pages": 1, "data": []JSON{{"id": "pn42", "language": q.Get("language"),
			"flags": []string{"hearing_impaired"}, "releases": []string{"Movie.603.2000.DVDRip"}, "custom_releases": []string{},
			"movie": JSON{"type": "movie", "title": "Movie 603", "year": 2000, "episode_info": JSON{}}, "stats": JSON{"downloads": 10}}}})
	case p == "/podnapisi/subtitles/pn42/download":
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(podnapisiZip())
	case strings.HasPrefix(p, "/gestdown/shows/search/"):
		Reply(w, JSON{"shows": []JSON{{"id": "show-1399", "name": "Show 1399", "tmdbId": 1399}}})
	case strings.HasPrefix(p, "/gestdown/subtitles/get/show-1399/"):
		Reply(w, JSON{"matchingSubtitles": []JSON{{"subtitleId": "gd1", "version": "WEB-GRP", "completed": true,
			"hearingImpaired": false, "downloadUri": "/subtitles/download/gd1", "downloadCount": 7, "language": "English"}}})
	case p == "/gestdown/subtitles/download/gd1":
		_, _ = w.Write([]byte(onlineSRT()))
	default:
		notFound(w)
	}
}
