package probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata files are ffprobe 7.1's answers for two small clips made with
// ffmpeg: an HEVC Matroska file with two audio tracks (AC-3 5.1 English,
// AAC German), an SRT and a forced ASS subtitle, two chapters and a font,
// and an H.264/AAC MP4.

func load(t *testing.T, file, name string) *Probe {
	t.Helper()
	data, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(data, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMatroska(t *testing.T) {
	p := load(t, "hevc_mkv.json", "Film.mkv")
	if p.Container != "mkv" || p.Duration < 5.9 || p.Duration > 6.2 {
		t.Fatalf("container %q duration %v", p.Container, p.Duration)
	}
	v, ok := p.Video()
	if !ok || v.Codec != "hevc" || v.Width != 320 || v.Height != 180 || v.FrameRate != 25 {
		t.Fatalf("video %+v", v)
	}
	audio := p.Of("audio")
	if len(audio) != 2 || audio[0].Codec != "ac3" || audio[0].Channels != 6 || audio[0].Language != "eng" ||
		audio[0].Title != "Surround" || audio[1].Codec != "aac" || audio[1].Language != "ger" {
		t.Fatalf("audio %+v", audio)
	}
	if d, _ := p.DefaultAudio(); d.Index != audio[0].Index {
		t.Fatalf("default audio %+v", d)
	}
	subs := p.Of("subtitle")
	if len(subs) != 2 || subs[0].Codec != "subrip" || subs[0].Language != "eng" ||
		subs[1].Codec != "ass" || subs[1].Language != "fre" || !subs[1].Forced {
		t.Fatalf("subtitles %+v", subs)
	}
	fonts := p.Of("attachment")
	if len(fonts) != 1 || fonts[0].FileName != "Font.ttf" || fonts[0].MimeType != "font/ttf" {
		t.Fatalf("attachments %+v", fonts)
	}
	if len(p.Chapters) != 2 || p.Chapters[0].Title != "Intro" || p.Chapters[1].Start != 3 {
		t.Fatalf("chapters %+v", p.Chapters)
	}
	if p.ExternalIndex(0) != 6 {
		t.Fatalf("first external index %d", p.ExternalIndex(0))
	}
}

func TestParseMP4(t *testing.T) {
	p := load(t, "h264_mp4.json", "film.mp4")
	if p.Container != "mp4" {
		t.Fatalf("container %q", p.Container)
	}
	if v, _ := p.Video(); v.Codec != "h264" || v.Profile == "" {
		t.Fatalf("video %+v", v)
	}
	if a := p.Of("audio"); len(a) != 1 || a[0].Codec != "aac" || a[0].Language != "eng" {
		t.Fatalf("audio %+v", a)
	}
	if len(p.Chapters) != 0 || len(p.Of("subtitle")) != 0 {
		t.Fatalf("unexpected chapters or subtitles: %+v", p)
	}
	// The same family named by extension.
	if p := load(t, "h264_mp4.json", "clip.MOV"); p.Container != "mov" {
		t.Fatalf("mov container %q", p.Container)
	}
}

func TestParseRefusesNonVideo(t *testing.T) {
	if _, err := Parse([]byte(`{"format":{"format_name":"mp3","duration":"3"},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}]}`), "a.mp3"); err == nil {
		t.Fatal("an audio file was read as a video")
	}
	if _, err := Parse([]byte(`not json`), "x.mkv"); err == nil {
		t.Fatal("garbage was read")
	}
	// Cover art is not the picture.
	if _, err := Parse([]byte(`{"format":{"format_name":"mov,mp4","duration":"3"},"streams":[{"index":0,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}]}`), "a.m4a"); err == nil {
		t.Fatal("cover art was read as a video")
	}
}

// A subtitle that runs past the picture's end doesn't make the film longer.
func TestDurationIsThePictures(t *testing.T) {
	p, err := Parse([]byte(`{"format":{"format_name":"matroska,webm","duration":"60.0"},"streams":[
		{"index":0,"codec_type":"video","codec_name":"h264","tags":{"DURATION":"00:00:30.040000000"}},
		{"index":1,"codec_type":"subtitle","codec_name":"ass","tags":{"DURATION":"00:00:59.000000000"}}]}`), "a.mkv")
	if err != nil || p.Duration < 30 || p.Duration > 30.1 {
		t.Fatalf("%v %v", p.Duration, err)
	}
}

func TestRunSaysWhyInAWord(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Broken.mkv")
	if err := os.WriteFile(file, []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Runner{}.Run(context.Background(), file)
	if err == nil || strings.Contains(err.Error(), file) || strings.Contains(err.Error(), "@ 0x") {
		t.Fatalf("%v", err)
	}
}
