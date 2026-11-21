package plan

import (
	"testing"

	"movienight/server/internal/media/probe"
)

func file(container string, streams ...probe.Stream) *probe.Probe {
	return &probe.Probe{Container: container, Duration: 60, Streams: streams}
}

func video(i int, codec string) probe.Stream {
	return probe.Stream{Index: i, Type: "video", Codec: codec, PixFmt: "yuv420p", BitDepth: 8}
}

func audio(i int, codec string, ch int) probe.Stream {
	return probe.Stream{Index: i, Type: "audio", Codec: codec, Channels: ch}
}

// chrome is what desktop Chrome on Linux reports: no HEVC, no AC-3.
var chrome = Caps{DirectContainers: []string{"mp4", "webm"}, DirectVideo: []string{"h264", "vp9", "vp8", "av1"},
	DirectAudio: []string{"aac", "mp3", "opus", "flac"}, HLSVideo: []string{"h264", "vp9", "av1"},
	HLSAudio: []string{"aac", "mp3", "opus", "flac"}, AudioChannels: 2}

// safari plays HEVC and AC-3.
var safari = Caps{DirectContainers: []string{"mp4"}, DirectVideo: []string{"h264", "hevc"},
	DirectAudio: []string{"aac", "mp3", "ac3", "eac3", "alac", "flac"}, HLSVideo: []string{"h264", "hevc"},
	HLSAudio: []string{"aac", "mp3", "ac3", "eac3", "flac"}, AudioChannels: 2}

func ip(n int) *int { return &n }

func TestDecide(t *testing.T) {
	pgs := probe.Stream{Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle"}
	srt := probe.Stream{Index: 4, Type: "subtitle", Codec: "subrip"}
	hdr := video(0, "hevc")
	hdr.Transfer, hdr.PixFmt, hdr.BitDepth = "smpte2084", "yuv420p10le", 10
	hi10 := video(0, "h264")
	hi10.PixFmt, hi10.BitDepth = "yuv420p10le", 10
	second := audio(2, "aac", 2)
	second.Default = true

	cases := []struct {
		name        string
		p           *probe.Probe
		c           Caps
		audio, burn *int
		want        Plan
	}{
		{"h264 aac mp4 plays as it is", file("mp4", video(0, "h264"), audio(1, "aac", 2)), chrome, nil, nil,
			Plan{Mode: Direct, Audio: 1, Burn: -1}},
		{"a webm plays as it is", file("webm", video(0, "vp9"), audio(1, "opus", 2)), chrome, nil, nil,
			Plan{Mode: Direct, Audio: 1, Burn: -1}},
		{"no audio at all", file("mp4", video(0, "h264")), chrome, nil, nil, Plan{Mode: Direct, Audio: -1, Burn: -1}},
		{"h264 in mkv is remuxed with the audio copied", file("mkv", video(0, "h264"), audio(1, "aac", 2)), chrome, nil, nil,
			Plan{Mode: Remux, Audio: 1, AudioCopy: true, Burn: -1}},
		{"ac3 in mp4 for chrome: picture copied, sound made AAC", file("mp4", video(0, "h264"), audio(1, "ac3", 6)), chrome, nil, nil,
			Plan{Mode: Remux, Audio: 1, Channels: 2, Burn: -1}},
		{"ac3 for safari is kept", file("mkv", video(0, "hevc"), audio(1, "ac3", 6)), safari, nil, nil,
			Plan{Mode: Remux, Audio: 1, AudioCopy: true, Burn: -1}},
		{"hevc for chrome is transcoded", file("mkv", video(0, "hevc"), audio(1, "aac", 2)), chrome, nil, nil,
			Plan{Mode: Transcode, Audio: 1, AudioCopy: true, Burn: -1}},
		{"hevc mp4 plays as it is in safari", file("mp4", video(0, "hevc"), audio(1, "aac", 2)), safari, nil, nil,
			Plan{Mode: Direct, Audio: 1, Burn: -1}},
		{"another audio track is never direct", file("mp4", video(0, "h264"), audio(1, "aac", 2), audio(2, "aac", 2)), chrome, ip(2), nil,
			Plan{Mode: Remux, Audio: 2, AudioCopy: true, Burn: -1}},
		{"the default flag picks the track", file("mp4", video(0, "h264"), audio(1, "aac", 2), second), chrome, nil, nil,
			Plan{Mode: Direct, Audio: 2, Burn: -1}},
		{"a burnt-in picture subtitle is a transcode", file("mkv", video(0, "h264"), audio(1, "aac", 2), pgs), chrome, nil, ip(3),
			Plan{Mode: Transcode, Audio: 1, AudioCopy: true, Burn: 3}},
		{"HDR is tone-mapped when transcoded", file("mkv", hdr, audio(1, "eac3", 6)), chrome, nil, nil,
			Plan{Mode: Transcode, Audio: 1, Channels: 2, Burn: -1, ToneMap: true}},
		{"surround stays surround on a surround output", file("mkv", video(0, "hevc"), audio(1, "dts", 6)),
			func() Caps { c := chrome; c.AudioChannels = 8; return c }(), nil, nil,
			Plan{Mode: Transcode, Audio: 1, Channels: 6, Burn: -1}},
		{"10-bit h264 is transcoded", file("mkv", hi10, audio(1, "aac", 2)), chrome, nil, nil,
			Plan{Mode: Transcode, Audio: 1, AudioCopy: true, Burn: -1}},
		{"an old codec is transcoded", file("avi", video(0, "mpeg4"), audio(1, "mp3", 2)), chrome, nil, nil,
			Plan{Mode: Transcode, Audio: 1, AudioCopy: true, Burn: -1}},
		{"a text subtitle never forces a transcode", file("mp4", video(0, "h264"), audio(1, "aac", 2), srt), chrome, nil, nil,
			Plan{Mode: Direct, Audio: 1, Burn: -1}},
	}
	for _, c := range cases {
		got, err := Decide(c.p, c.c, c.audio, c.burn)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDecideRefuses(t *testing.T) {
	p := file("mkv", video(0, "h264"), audio(1, "aac", 2), probe.Stream{Index: 2, Type: "subtitle", Codec: "subrip"})
	if _, err := Decide(p, chrome, ip(5), nil); err != ErrNoAudio {
		t.Errorf("missing audio track: %v", err)
	}
	if _, err := Decide(p, chrome, nil, ip(2)); err != ErrNoSubtitle {
		t.Errorf("burning a text subtitle: %v", err)
	}
}

func TestStringParse(t *testing.T) {
	for _, p := range []Plan{
		{Mode: Direct, Audio: 1, Burn: -1},
		{Mode: Remux, Audio: 2, AudioCopy: true, Burn: -1},
		{Mode: Transcode, Audio: 1, Channels: 6, Burn: 4, ToneMap: true},
		{Mode: Transcode, Audio: -1, Burn: -1},
	} {
		got, err := Parse(p.String())
		if err != nil || got != p {
			t.Errorf("%+v: round trip gave %+v, %v", p, got, err)
		}
	}
	for _, bad := range []string{"", "m=x", "m=remux&b=3", "m=remux&a=x", "m=remux&zz=1", "m=transcode&ch=40", "%zz"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
