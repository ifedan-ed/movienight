package stream

import (
	"io"
	"log/slog"
	"net/url"
	"testing"

	"movienight/server/internal/media/mediaclient"
)

// A library ticket's paths map to the media service's routes with the
// file, plan and play from the ticket alone; the browser's query reaches
// it only as an external subtitle's name.
func TestLibraryTargets(t *testing.T) {
	mc, err := mediaclient.New("http://media:8100", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProxy(nil, NewClient(), "", "", mc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := Claims{Member: 1, Item: "lib-7", File: "/media/Films/A (2001)/A.mkv", Plan: "m=remux&a=1&ac=1", Play: "play1"}
	evil := url.Values{"file": {"/etc/passwd"}, "plan": {"m=transcode"}, "name": {"A.en.srt"}}
	for rel, want := range map[string]string{
		"stream.mkv":      "/v1/file?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv",
		"main.m3u8":       "/v1/hls/main.m3u8?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv&plan=m%3Dremux%26a%3D1%26ac%3D1&play=play1",
		"hls/init.mp4":    "/v1/hls/init.mp4?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv&plan=m%3Dremux%26a%3D1%26ac%3D1&play=play1",
		"hls/12.m4s":      "/v1/hls/12.m4s?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv&plan=m%3Dremux%26a%3D1%26ac%3D1&play=play1",
		"subtitles/3.vtt": "/v1/subtitles/3.vtt?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv&name=A.en.srt",
		"attachments/5":   "/v1/attachments/5?file=%2Fmedia%2FFilms%2FA+%282001%29%2FA.mkv",
	} {
		u, _, ok := p.libraryTarget(c, rel, evil)
		if !ok {
			t.Errorf("%s: not served", rel)
			continue
		}
		if got := u.RequestURI(); got != want {
			t.Errorf("%s: %s, want %s", rel, got, want)
		}
	}
	for _, rel := range []string{"../x", "hls/../../etc", "Videos/abc/stream", "hls/1.ts", "master.m3u8"} {
		if _, _, ok := p.libraryTarget(c, rel, nil); ok {
			t.Errorf("%s was served", rel)
		}
	}
}
