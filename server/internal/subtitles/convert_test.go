package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func mustLang(t *testing.T, code string) Language {
	t.Helper()
	l, ok := LanguageByCode(code)
	if !ok {
		t.Fatalf("no language %s", code)
	}
	return l
}

func TestSRTBecomesCleanWebVTT(t *testing.T) {
	srt := "\ufeff1\r\n00:00:01,000 --> 00:00:03,500\r\n{\\an8}Hello <i>there</i>\r\n\r\n" +
		"2\r\n0:00:04,5 --> 00:00:06,000\r\nA --> B\r\nsecond line\r\n\r\n" +
		"3\r\n00:00:09,000 --> 00:00:08,000\r\nends before it starts\r\n\r\n" +
		"4\r\n00:01:02,345 --> 00:01:03,000\r\n\r\n"
	format, body, err := Prepare([]byte(srt), mustLang(t, "en"), 0, 0)
	if err != nil || format != FormatVTT {
		t.Fatal(format, err)
	}
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.500\nHello <i>there</i>\n\n2\n00:00:04.500 --> 00:00:06.000\nA → B\nsecond line\n"
	if body != want {
		t.Fatalf("got\n%q\nwant\n%q", body, want)
	}
}

func TestASSStaysASSAndWebVTTIsKept(t *testing.T) {
	ass := "[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,Hi\n"
	if f, body, err := Prepare([]byte(ass), mustLang(t, "en"), 0, 0); err != nil || f != FormatASS || !strings.Contains(body, "Dialogue: 0,0:00:01.00") {
		t.Fatal(f, body, err)
	}
	vtt := "WEBVTT\n\nNOTE made by hand\n\n00:01.000 --> 00:02.000 align:start\nShort stamps\n"
	f, body, err := Prepare([]byte(vtt), mustLang(t, "en"), 0, 0)
	if err != nil || f != FormatVTT || body != "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nShort stamps\n" {
		t.Fatalf("%q %v", body, err)
	}
}

func TestMicroDVDUsesItsFrameRate(t *testing.T) {
	sub := "{1}{1}25.000\n{25}{75}First|second\n{100}{}{y:i}Open end\n"
	f, body, err := Prepare([]byte(sub), mustLang(t, "en"), 0, 0)
	if err != nil || f != FormatVTT {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.000\nFirst\nsecond\n\n2\n00:00:04.000 --> 00:00:07.000\nOpen end\n"
	if body != want {
		t.Fatalf("got %q", body)
	}
}

func TestCharsetsFollowTheLanguage(t *testing.T) {
	// "Привет" in Windows-1251, "Žluťoučký" in Windows-1250, "Café" in
	// Windows-1252: none of them valid UTF-8.
	cases := []struct {
		code string
		raw  []byte
		want string
	}{
		{"ru", []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}, "Привет"},
		{"cs", []byte{0x8E, 0x6C, 0x75, 0x9D, 0x6F, 0x75, 0xE8, 0x6B, 0xFD}, "Žluťoučký"},
		{"fr", []byte("Caf\xe9"), "Café"},
	}
	for _, c := range cases {
		srt := append([]byte("1\n00:00:01,000 --> 00:00:02,000\n"), c.raw...)
		_, body, err := Prepare(srt, mustLang(t, c.code), 0, 0)
		if err != nil || !strings.Contains(body, "\n"+c.want+"\n") {
			t.Errorf("%s: %q %v", c.code, body, err)
		}
	}
	// UTF-16 with a BOM, and without one.
	utf16 := func(s string, bom bool) []byte {
		var b []byte
		if bom {
			b = []byte{0xFF, 0xFE}
		}
		for _, r := range s {
			b = append(b, byte(r), byte(r>>8))
		}
		return b
	}
	for _, bom := range []bool{true, false} {
		_, body, err := Prepare(utf16("1\n00:00:01,000 --> 00:00:02,000\nWide text\n", bom), mustLang(t, "en"), 0, 0)
		if err != nil || !strings.Contains(body, "\nWide text\n") {
			t.Errorf("utf-16 bom=%v: %q %v", bom, body, err)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAZipGivesTheEpisodeOfAPack(t *testing.T) {
	cue := func(s string) string { return "1\n00:00:01,000 --> 00:00:02,000\n" + s + "\n" }
	z := zipOf(t, map[string]string{
		"Show.S01E01.srt": cue("one"), "Show.S01E02.srt": cue("two"), "Show.S01E03.srt": cue("three"),
		"__MACOSX/._Show.S01E02.srt": "junk", "info.nfo": "not this",
	})
	_, body, err := Prepare(z, mustLang(t, "en"), 1, 2)
	if err != nil || !strings.Contains(body, "\ntwo\n") {
		t.Fatalf("%q %v", body, err)
	}
	// A film's zip: the subtitle, never the .nfo.
	_, body, err = Prepare(zipOf(t, map[string]string{"a.nfo": "x", "film.srt": cue("film")}), mustLang(t, "en"), 0, 0)
	if err != nil || !strings.Contains(body, "\nfilm\n") {
		t.Fatalf("%q %v", body, err)
	}
	if _, _, err := Prepare(zipOf(t, map[string]string{"a.nfo": "x"}), mustLang(t, "en"), 0, 0); !errors.Is(err, ErrArchive) {
		t.Fatalf("a zip with no subtitle: %v", err)
	}
}

func TestWhatCantBeShownIsRefused(t *testing.T) {
	for name, data := range map[string][]byte{
		"rar":     []byte("Rar!\x1a\x07\x00rest"),
		"garbage": []byte("just some words"),
		"empty":   []byte("WEBVTT\n\n"),
	} {
		if _, _, err := Prepare(data, mustLang(t, "en"), 0, 0); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMovieHashReadsTwoRanges(t *testing.T) {
	const n = 200000
	file := make([]byte, n)
	for i := range file {
		file[i] = byte((i*7 + 3) % 256)
	}
	var reads []int64
	read := func(_ context.Context, off int64, size int) ([]byte, int64, error) {
		reads = append(reads, off)
		return file[off : off+int64(size)], n, nil
	}
	got, err := MovieHash(context.Background(), read)
	// The expected value is from an independent implementation of
	// OpenSubtitles' algorithm (Python's struct over the same bytes).
	if err != nil || got != "60a0df1f5fa2cd40" {
		t.Fatal(got, err)
	}
	if len(reads) != 2 || reads[0] != 0 || reads[1] != n-hashChunk {
		t.Fatalf("reads %v", reads)
	}
	small := func(_ context.Context, _ int64, _ int) ([]byte, int64, error) { return make([]byte, 1000), 1000, nil }
	if _, err := MovieHash(context.Background(), small); !errors.Is(err, ErrTooSmall) {
		t.Fatal(err)
	}
}

func TestRankingPutsTheFileFirst(t *testing.T) {
	rs := []Result{
		{ID: "popular", Release: "Film.2010.720p.WEB.x264-OTHER", Downloads: 9000},
		{ID: "close", Release: "Film.2010.1080p.BluRay.x264-GRP", Downloads: 10},
		{ID: "hash", Release: "something else entirely", HashMatch: true},
		{ID: "close-more", Release: "Film 2010 1080p BluRay x264-GRP", Downloads: 50},
		{ID: "far", Release: "Unrelated.Release", Downloads: 100, Rating: 9},
	}
	Rank(rs, "/media/Film (2010)/Film.2010.1080p.BluRay.x264-GRP.mkv")
	var order []string
	for _, r := range rs {
		order = append(order, r.ID)
	}
	if strings.Join(order, ",") != "hash,close-more,close,popular,far" {
		t.Fatal(order)
	}
	// No file name: downloads, then rating.
	rs = []Result{{ID: "a", Downloads: 1}, {ID: "b", Downloads: 5}, {ID: "c", Downloads: 5, Rating: 7}}
	Rank(rs, "")
	if rs[0].ID != "c" || rs[1].ID != "b" {
		t.Fatal(rs)
	}
}

func TestDefaultLanguageFollowsProfile(t *testing.T) {
	for in, want := range map[string]string{"ger": "de", "deu": "de", "fre": "fr", "": "en", "xxx": "en", "pob": "pt-br"} {
		if got := DefaultLanguage(in).Code; got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
