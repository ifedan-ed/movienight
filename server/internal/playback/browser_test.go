package playback

import (
	"testing"

	"movienight/server/internal/jfplay"
)

func TestWatchedAtEnd(t *testing.T) {
	for _, c := range []struct {
		pos, runtime, credits float64
		want                  bool
	}{
		{89.9, 100, 0, false},
		{90, 100, 0, true}, // 90 % of the runtime
		{100, 100, 0, true},
		{80, 100, 80, true}, // the credits marker
		{79.9, 100, 80, false},
		{0, 100, 0, false},
		{50, 0, 0, false}, // no runtime and no marker: never by guess
		{50, 0, 45, true},
	} {
		if got := WatchedAtEnd(c.pos, c.runtime, c.credits); got != c.want {
			t.Errorf("WatchedAtEnd(%v, %v, %v) = %v", c.pos, c.runtime, c.credits, got)
		}
	}
}

func ip(n int) *int { return &n }

func source() jfplay.Source {
	return jfplay.Source{ID: "ms", MediaStreams: []jfplay.MediaStream{
		{Type: "Video", Index: 0},
		{Type: "Audio", Index: 1, Language: "ENG"},
		{Type: "Audio", Index: 2, IsDefault: true, DisplayTitle: "Deutsch"},
		{Type: "Subtitle", Index: 3, Codec: "subrip", IsTextSubtitleStream: true, Language: "eng", IsExternal: true},
		{Type: "Subtitle", Index: 4, Codec: "ass", IsTextSubtitleStream: true, Language: "eng"},
		{Type: "Subtitle", Index: 5, Codec: "pgssub", Language: "eng"},
	}}
}

func TestTracks(t *testing.T) {
	src := source()
	if defaultAudio(src) != 2 {
		t.Fatalf("default audio %d", defaultAudio(src))
	}
	src.DefaultAudioStreamIndex = ip(1)
	if defaultAudio(src) != 1 {
		t.Fatal("Jellyfin's default wins")
	}
	audio, subs := tracks(src, "T")
	if len(audio) != 2 || audio[0].Label != "ENG" || *audio[0].Language != "eng" || !audio[0].Default || audio[1].Label != "Deutsch" {
		t.Fatalf("audio %+v", audio)
	}
	if subs[0].Kind != "text" || *subs[0].URL != "/api/v1/stream/T/subtitles/3.vtt" ||
		subs[1].Kind != "ass" || *subs[1].URL != "/api/v1/stream/T/subtitles/4.ass" ||
		subs[2].Kind != "image" || subs[2].URL != nil {
		t.Fatalf("subs %+v", subs)
	}
	// The person's language: the external file first; a picture subtitle
	// is never shown by itself (it would need a burn-in).
	if got := pickShown(subs, "eng"); got == nil || *got != 3 {
		t.Fatalf("picked %v", got)
	}
	if got := pickShown(subs[2:], "eng"); got != nil {
		t.Fatalf("picked a picture subtitle: %v", *got)
	}
}

func TestCheckTracks(t *testing.T) {
	src := source()
	if err := checkTracks(src, ip(2), ip(5)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][2]*int{"no such audio": {ip(9), nil}, "a text burn-in": {nil, ip(3)}, "an ASS burn-in": {nil, ip(4)},
		"no such subtitle": {nil, ip(9)}, "audio as subtitle": {nil, ip(1)}} {
		if checkTracks(src, c[0], c[1]) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
