package sidecar

import "testing"

func TestMatch(t *testing.T) {
	names := []string{"Film.mkv", "Film.srt", "Film.en.srt", "Film.eng.forced.ass", "Film.English.SDH.srt",
		"Film.pt-BR.vtt", "Film.default.fr.srt", "Film 2.mkv", "Film 2.en.srt", "Film.nfo", "Film.Commentary.srt"}
	got := Match("Film.mkv", names, nil, false)
	want := []File{
		{Name: "Film.Commentary.srt", Codec: "subrip", Title: "Commentary"},
		{Name: "Film.English.SDH.srt", Codec: "subrip", Language: "eng", Title: "SDH", HI: true},
		{Name: "Film.default.fr.srt", Codec: "subrip", Language: "fre", Default: true},
		{Name: "Film.en.srt", Codec: "subrip", Language: "eng"},
		{Name: "Film.eng.forced.ass", Codec: "ass", Language: "eng", Forced: true},
		{Name: "Film.pt-BR.vtt", Codec: "webvtt", Language: "por"},
		{Name: "Film.srt", Codec: "subrip"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMatchOnlyVideoAndSubsFolder(t *testing.T) {
	got := Match("movie.mkv", []string{"movie.mkv", "English.srt"}, []string{"Subs/2_English.srt", "Subs/3_French.srt", "Subs/notes.txt"}, true)
	if len(got) != 3 || got[0].Name != "English.srt" || got[0].Language != "eng" ||
		got[1].Name != "Subs/2_English.srt" || got[1].Language != "eng" || got[2].Language != "fre" {
		t.Fatalf("got %+v", got)
	}
	// With other videos beside it, a Subs folder belongs to none of them.
	if got := Match("a.mkv", []string{"a.mkv", "b.mkv"}, []string{"Subs/English.srt"}, false); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestLanguage(t *testing.T) {
	for in, want := range map[string]string{"en": "eng", "ENG": "eng", "fra": "fre", "fre": "fre", "deu": "ger",
		"German": "ger", "pt-BR": "por", "zh-Hans": "chi", "und": "", "xx": "", "": ""} {
		if got := Language(in); got != want {
			t.Errorf("Language(%q) = %q, want %q", in, got, want)
		}
	}
}
