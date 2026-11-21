package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/media/mediatest"
	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/probe"
)

// These tests run the real ffmpeg on clips it made itself
// (package mediatest).

func newEngine(t *testing.T, maxTranscodes int) *Engine {
	t.Helper()
	mediatest.FFmpeg(t)
	e, err := New(Config{Prober: probe.Runner{}, CacheDir: t.TempDir(), MaxTranscodes: maxTranscodes,
		HW: HW{Kind: "none"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// firstPTS is the first video and audio timestamps of an fMP4 segment
// (joined to its init segment), and the stream codecs, read by ffprobe.
func segmentInfo(t *testing.T, initPath, segPath string) (video float64, codecs string) {
	t.Helper()
	joined := filepath.Join(t.TempDir(), "seg.mp4")
	a, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(joined, append(a, b...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-read_intervals", "%+#1", "-of", "csv=p=0", joined).Output()
	if err != nil {
		t.Fatal(err)
	}
	video, _ = strconv.ParseFloat(strings.TrimSpace(strings.Split(string(out), "\n")[0]), 64)
	out, err = exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name,channels", "-of", "csv=p=0", joined).Output()
	if err != nil {
		t.Fatal(err)
	}
	return video, strings.Join(strings.Fields(strings.ReplaceAll(string(out), "\n", " ")), " ")
}

func TestRemuxSegmentsAndSeek(t *testing.T) {
	e := newEngine(t, 1)
	file := filepath.Join(t.TempDir(), "film.mkv")
	mediatest.H264MKV(t, file)
	ctx := context.Background()
	r := Request{File: file, Plan: plan.Plan{Mode: plan.Remux, Audio: 1, AudioCopy: true, Burn: -1}, Play: "p1"}

	list, err := e.Playlist(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(list), "hls/3.m4s") || strings.Contains(string(list), "hls/4.m4s") ||
		!strings.Contains(string(list), `#EXT-X-MAP:URI="hls/init.mp4"`) {
		t.Fatalf("a 20 s file is 4 segments:\n%s", list)
	}
	init, err := e.Init(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := e.Segment(ctx, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pts, codecs := segmentInfo(t, init, seg); pts > 0.2 || codecs != "h264 aac,1" {
		t.Fatalf("segment 0: pts %v codecs %q", pts, codecs)
	}
	// A remux copies the picture: ffmpeg is far ahead at once, so segment 3
	// is already there or about to be.
	seg3, err := e.Segment(ctx, r, 3)
	if err != nil {
		t.Fatal(err)
	}
	if pts, _ := segmentInfo(t, init, seg3); pts < 17.9 || pts > 18.1 {
		t.Fatalf("segment 3 starts at %v, want 18", pts)
	}
	if _, err := e.Segment(ctx, r, 4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("past the end: %v", err)
	}
	// Remuxes are not counted against the transcode limit.
	if st := e.Status(); st.Transcodes != 0 || st.Jobs != 1 {
		t.Fatalf("status %+v", st)
	}
}

func TestTranscodeSeekRestartsAndLimit(t *testing.T) {
	e := newEngine(t, 1)
	// ffmpeg here is over in a second; the wrapper keeps each run alive
	// afterwards, as a real film's transcode would be, so the limit is
	// tested without a race. A kill ends the wrapper too (its process group).
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nffmpeg \"$@\" || exit $?\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.cfg.FFmpeg = wrapper
	file := filepath.Join(t.TempDir(), "film.mkv")
	mediatest.HEVCWithTracks(t, file)
	ctx := context.Background()
	// The German 5.1 track, mixed down to stereo AAC; the picture H.264.
	r := Request{File: file, Plan: plan.Plan{Mode: plan.Transcode, Audio: mediatest.HEVCAudioDE, Channels: 2, Burn: -1}, Play: "p1"}
	init, err := e.Init(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// The init segment is complete when it is handed out, never the empty
	// file ffmpeg creates first (a browser can't start from that).
	if st, err := os.Stat(init); err != nil || st.Size() < 500 {
		t.Fatalf("init segment %v %v", st, err)
	}
	seg, err := e.Segment(ctx, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pts, codecs := segmentInfo(t, init, seg); pts > 0.2 || codecs != "h264 aac,2" {
		t.Fatalf("segment 0: pts %v codecs %q", pts, codecs)
	}
	if st := e.Status(); st.Transcodes != 1 {
		t.Fatalf("a running transcode is not counted: %+v", st)
	}

	// A second play's transcode is refused while the only slot is taken.
	other := r
	other.Play = "p2"
	if _, err := e.Segment(ctx, other, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("over the limit: %v", err)
	}

	// Seeking to the last segment restarts ffmpeg there: its first
	// timestamp is that segment's start, exactly (a keyframe is forced on
	// every boundary).
	seg3, err := e.Segment(ctx, r, 3)
	if err != nil {
		t.Fatal(err)
	}
	init, _ = e.Init(ctx, r)
	if pts, _ := segmentInfo(t, init, seg3); pts < 17.99 || pts > 18.2 {
		t.Fatalf("segment 3 after a seek starts at %v, want 18", pts)
	}

	// Stopping the play ends ffmpeg, frees the slot and removes the folder.
	dir := filepath.Dir(seg3)
	if n := e.Stop("p1"); n != 1 {
		t.Fatalf("stopped %d jobs", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the play's folder is still there: %v", err)
	}
	if st := e.Status(); st.Transcodes != 0 || st.Jobs != 0 {
		t.Fatalf("after stop: %+v", st)
	}
	if _, err := e.Segment(ctx, other, 0); err != nil {
		t.Fatalf("the freed slot was not given to the next play: %v", err)
	}
}

// Burning in a picture subtitle is checked on its arguments: ffmpeg can
// only make picture subtitles from pictures, so a test file with one cannot
// be made here.
func TestBurnInArgs(t *testing.T) {
	p := &probe.Probe{Duration: 60, Streams: []probe.Stream{{Index: 0, Type: "video", Codec: "h264", Height: 1080},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2}, {Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle"}}}
	args := strings.Join(hlsArgs("/media/f.mkv", p, plan.Plan{Mode: plan.Transcode, Audio: 1, AudioCopy: true, Burn: 2}, 0, "/tmp/j", HW{Kind: "none"}), " ")
	if !strings.Contains(args, "[0:0][0:2]overlay=eof_action=pass,format=yuv420p[v]") || !strings.Contains(args, "-map [v]") ||
		!strings.Contains(args, "-sn") {
		t.Fatalf("burn-in args %s", args)
	}
}

func TestIdlePlayEnds(t *testing.T) {
	e := newEngine(t, 1)
	e.cfg.IdleAfter = 300 * time.Millisecond
	file := filepath.Join(t.TempDir(), "film.mkv")
	mediatest.H264MKV(t, file)
	r := Request{File: file, Plan: plan.Plan{Mode: plan.Remux, Audio: 1, AudioCopy: true, Burn: -1}, Play: "idle"}
	seg, err := e.Segment(context.Background(), r, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.Status().Jobs > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if e.Status().Jobs != 0 {
		t.Fatal("an idle play was kept")
	}
	if _, err := os.Stat(filepath.Dir(seg)); !os.IsNotExist(err) {
		t.Fatal("an idle play's folder was kept")
	}
}

func TestSubtitlesAndFonts(t *testing.T) {
	e := newEngine(t, 1)
	dir := t.TempDir()
	file := filepath.Join(dir, "Film.mkv")
	mediatest.HEVCWithTracks(t, file)
	ext := mediatest.Sidecar(t, file, "en")
	ctx := context.Background()

	vtt, err := e.Subtitle(ctx, file, mediatest.HEVCSubSRT, "vtt")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(vtt); !strings.HasPrefix(string(b), "WEBVTT") || !strings.Contains(string(b), "Hello there") {
		t.Fatalf("SRT as WebVTT:\n%s", b)
	}
	ass, err := e.Subtitle(ctx, file, mediatest.HEVCSubASS, "ass")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ass); !strings.Contains(string(b), "Styled line") || !strings.Contains(string(b), "[V4+ Styles]") {
		t.Fatalf("ASS kept as ASS:\n%s", b)
	}
	if _, err := e.Subtitle(ctx, file, mediatest.HEVCSubSRT, "ass"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an SRT asked for as ASS: %v", err)
	}
	if _, err := e.Subtitle(ctx, file, mediatest.HEVCAudioEN, "vtt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an audio stream asked for as a subtitle: %v", err)
	}

	body, err := e.External(ctx, file, filepath.Base(ext), "vtt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "WEBVTT") || !strings.Contains(string(body), "A longer line") {
		t.Fatalf("external SRT as WebVTT:\n%s", body)
	}
	// Only the video's own subtitle files are reachable.
	if _, err := e.External(ctx, file, "../etc/passwd", "vtt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name outside the video's subtitles: %v", err)
	}

	font, mime, err := e.Attachment(ctx, file, mediatest.HEVCFont)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(font); string(b) != "not really a font, but bytes all the same" || mime != "font/ttf" {
		t.Fatalf("font %q %q", b, mime)
	}
}

func TestPlaylistLengths(t *testing.T) {
	if Segments(18) != 3 || Segments(18.5) != 4 || Segments(0.5) != 1 {
		t.Fatal("segment counts")
	}
	list := string(Playlist(13))
	if !strings.Contains(list, "#EXTINF:1.000,\nhls/2.m4s") || !strings.HasSuffix(list, "#EXT-X-ENDLIST\n") {
		t.Fatalf("playlist:\n%s", list)
	}
}

func TestArgsChooseTheEncoder(t *testing.T) {
	p := &probe.Probe{Duration: 60, Streams: []probe.Stream{{Index: 0, Type: "video", Codec: "hevc", Height: 2160,
		Transfer: "smpte2084"}, {Index: 1, Type: "audio", Codec: "truehd", Channels: 8}}}
	pl := plan.Plan{Mode: plan.Transcode, Audio: 1, Channels: 6, Burn: -1, ToneMap: true}
	for _, c := range []struct {
		hw   HW
		want []string
	}{
		{HW{Kind: "none"}, []string{"libx264", "format=yuv420p", "tonemap=tonemap=hable", "scale=-2:1080", "-ac 6", "-ss 30"}},
		{HW{Kind: "vaapi", Device: "/dev/dri/renderD128"}, []string{"h264_vaapi", "vaapi=hw:/dev/dri/renderD128", "hwupload"}},
		{HW{Kind: "qsv", Device: "/dev/dri/renderD128"}, []string{"h264_qsv", "qsv=hw@va", "extra_hw_frames"}},
		{HW{Kind: "nvenc"}, []string{"h264_nvenc", "-forced-idr 1", "format=yuv420p"}},
	} {
		args := strings.Join(hlsArgs("/media/f.mkv", p, pl, 5, "/tmp/j", c.hw), " ")
		for _, w := range c.want {
			if !strings.Contains(args, w) {
				t.Errorf("%s: %q missing from %s", c.hw.Kind, w, args)
			}
		}
	}
	// A remux copies the picture and tags HEVC as hvc1.
	args := strings.Join(hlsArgs("/media/f.mkv", p, plan.Plan{Mode: plan.Remux, Audio: 1, Channels: 2, Burn: -1}, 0, "/tmp/j", HW{Kind: "none"}), " ")
	if !strings.Contains(args, "-c:v copy -tag:v hvc1") || strings.Contains(args, "-ss") || strings.Contains(args, "libx264") {
		t.Errorf("remux args %s", args)
	}
}
