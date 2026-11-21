// Package mediatest makes small real video files with ffmpeg for the media
// tests: nothing binary is committed, and every test plays files ffmpeg
// itself wrote.
package mediatest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Clip lengths, in seconds: long enough for several HLS segments.
const ClipSeconds = 20

// The stream indexes of HEVCWithTracks, as ffprobe numbers them.
const (
	HEVCVideo   = 0
	HEVCAudioEN = 1 // AAC stereo, English, default
	HEVCAudioDE = 2 // AC-3 5.1, German
	HEVCSubSRT  = 3 // SubRip, English
	HEVCSubASS  = 4 // ASS, French
	HEVCFont    = 5 // an attached "font"
)

// FFmpeg fails the test unless ffmpeg and ffprobe are installed (the
// test image has both: server/scripts/go.sh). It never skips: a skipped
// media test would read as a passing one.
func FFmpeg(t testing.TB) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is not installed; run the tests with server/scripts/test.sh (its image has ffmpeg)", bin)
		}
	}
}

func run(t testing.TB, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

func write(t testing.TB, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SRT is a short SubRip subtitle.
const SRT = "1\n00:00:01,000 --> 00:00:04,000\nHello there\n\n2\n00:00:05,000 --> 00:00:19,000\nA longer line\n"

// ASS is a short styled subtitle.
const ASS = `[Script Info]
ScriptType: v4.00+
PlayResX: 320
PlayResY: 180

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,18,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:19.00,Default,,0,0,0,,{\an8}Styled line
`

// H264MP4 writes an H.264/AAC MP4 (a browser plays it as it is) to path.
func H264MP4(t testing.TB, path string) {
	t.Helper()
	FFmpeg(t)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	run(t, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20", "-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main", "-pix_fmt", "yuv420p",
		"-g", "50", "-b:v", "150k", "-c:a", "aac", "-b:a", "48k", "-ac", "2", "-metadata:s:a:0", "language=eng",
		"-movflags", "+faststart", path)
}

// H264MKV writes H.264/AAC in Matroska (a browser takes the codecs, not the
// container: a remux) to path.
func H264MKV(t testing.TB, path string) {
	t.Helper()
	FFmpeg(t)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	run(t, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20", "-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-g", "50",
		"-b:v", "150k", "-c:a", "aac", "-b:a", "48k", "-metadata:s:a:0", "language=eng", path)
}

// HEVCWithTracks writes an HEVC Matroska file with two audio tracks
// (AAC English, AC-3 5.1 German), an embedded SRT and ASS, two chapters
// ("Intro" 0-5 s, "Story", and "Credits" from 16 s) and an attached font
// to path. Chromium does not decode HEVC: a transcode.
func HEVCWithTracks(t testing.TB, path string) {
	t.Helper()
	FFmpeg(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.srt"), SRT)
	write(t, filepath.Join(dir, "a.ass"), ASS)
	write(t, filepath.Join(dir, "chapters.txt"), ";FFMETADATA1\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=5000\ntitle=Intro\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=5000\nEND=16000\ntitle=Story\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=16000\nEND=20000\ntitle=Credits\n")
	// Not a real font: the tests check it is served byte for byte.
	write(t, filepath.Join(dir, "font.ttf"), "not really a font, but bytes all the same")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	run(t, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20", "-f", "lavfi", "-i", "sine=frequency=880:duration=20",
		"-i", filepath.Join(dir, "a.srt"), "-i", filepath.Join(dir, "a.ass"), "-i", filepath.Join(dir, "chapters.txt"),
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3", "-map", "4", "-map_chapters", "5",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error:keyint=50", "-pix_fmt", "yuv420p",
		"-c:a:0", "aac", "-b:a:0", "48k", "-ac:a:0", "2", "-c:a:1", "ac3", "-ac:a:1", "6", "-b:a:1", "192k",
		"-c:s:0", "srt", "-c:s:1", "ass",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=ger", "-metadata:s:a:1", "title=Surround",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=fre",
		"-disposition:a:0", "default", "-disposition:a:1", "0",
		"-attach", filepath.Join(dir, "font.ttf"), "-metadata:s:t", "mimetype=font/ttf", "-metadata:s:t", "filename=Font.ttf",
		path)
}

// Sidecar writes an external SubRip file next to video, as "<stem>.<lang>.srt".
func Sidecar(t testing.TB, video, lang string) string {
	t.Helper()
	ext := filepath.Ext(video)
	path := video[:len(video)-len(ext)] + "." + lang + ".srt"
	write(t, path, SRT)
	return path
}
