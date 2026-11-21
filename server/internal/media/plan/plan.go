// Package plan decides how a library file reaches a browser: as the file
// itself (direct), as HLS with the picture copied and only the container
// changed (remux), or as HLS with the picture encoded again (transcode).
//
// The API decides (Decide) from the file's probe and what the browser says
// it decodes, and puts the plan in the stream ticket; the media engine
// reads it back (Parse) and builds ffmpeg's arguments from it. The plan is
// the whole contract between the two, so it is small, explicit and
// checked on both ends.
package plan

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"movienight/server/internal/media/probe"
)

// Mode is how the file is sent.
type Mode string

const (
	Direct    Mode = "direct"
	Remux     Mode = "remux"
	Transcode Mode = "transcode"
)

// SegmentSeconds is the length of every HLS segment. Six seconds is the
// common choice: short enough that a seek lands quickly, long enough that
// a two-hour film is 1,200 requests.
const SegmentSeconds = 6

// Caps is what the browser decodes, in the player's vocabulary (the same
// as Jellyfin's device profile is built from: jfplay.Caps).
type Caps struct {
	DirectContainers []string
	DirectVideo      []string
	DirectAudio      []string
	HLSVideo         []string
	HLSAudio         []string
	// AudioChannels is how many channels the browser's audio output has
	// (AudioContext's maxChannelCount); 0 when it did not say. Six or more
	// keeps a transcode's surround sound as AAC 5.1; otherwise it is
	// mixed down to stereo.
	AudioChannels int
}

// Plan is one way of sending a file.
type Plan struct {
	Mode Mode
	// Audio is the ffprobe index of the audio stream sent; -1 for none.
	Audio int
	// AudioCopy keeps the audio as it is; otherwise it becomes AAC with
	// Channels channels (2 or 6).
	AudioCopy bool
	Channels  int
	// Burn is the ffprobe index of a picture subtitle drawn into the video
	// (a transcode); -1 for none.
	Burn int
	// ToneMap turns HDR into SDR while transcoding, so the picture is not
	// washed out on an ordinary screen.
	ToneMap bool
}

// What the HLS segments (fragmented MP4) and the file containers hold.
var (
	// fMP4 segments carry these codecs as they are; anything else is
	// transcoded.
	segmentVideo = []string{"h264", "hevc", "av1"}
	segmentAudio = []string{"aac", "mp3", "ac3", "eac3", "opus", "flac", "alac"}
	// What a browser plays inside each container it plays as a file.
	containerVideo = map[string][]string{"mp4": {"h264", "hevc", "av1", "vp9"}, "webm": {"vp8", "vp9", "av1"}}
	containerAudio = map[string][]string{"mp4": {"aac", "mp3", "opus", "flac", "ac3", "eac3", "alac"}, "webm": {"opus", "vorbis"}}
	// Picture subtitle codecs: only burning them in shows them.
	pictureSubs = []string{"hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub", "pgssub"}
)

// IsPictureSub reports whether a subtitle codec is a picture (PGS,
// VobSub, DVB), shown only by burning it into the video.
func IsPictureSub(codec string) bool { return slices.Contains(pictureSubs, codec) }

// Errors Decide refuses with.
var (
	ErrNoAudio    = errors.New("the file has no such audio track")
	ErrNoSubtitle = errors.New("the file has no such picture subtitle")
)

// Decide picks the cheapest way the browser can play the file: direct when
// it plays the file as it is, remux when it takes the picture as it is,
// transcode otherwise. audio picks an audio track (nil: the file's
// default); burn a picture subtitle to draw in (nil: none), which always
// means a transcode.
func Decide(p *probe.Probe, c Caps, audio, burn *int) (Plan, error) {
	v, ok := p.Video()
	if !ok {
		return Plan{}, errors.New("the file has no picture")
	}
	pl := Plan{Audio: -1, Burn: -1}
	def, hasAudio := p.DefaultAudio()
	var a probe.Stream
	switch {
	case audio != nil:
		s, ok := p.Stream(*audio)
		if !ok || s.Type != "audio" {
			return Plan{}, ErrNoAudio
		}
		a, pl.Audio = s, s.Index
	case hasAudio:
		a, pl.Audio = def, def.Index
	}
	if burn != nil {
		s, ok := p.Stream(*burn)
		if !ok || s.Type != "subtitle" || !IsPictureSub(s.Codec) {
			return Plan{}, ErrNoSubtitle
		}
		pl.Burn = s.Index
	}

	// Direct: the container, the picture and the default audio as they
	// are, and no other audio track asked for (a browser plays only the
	// file's default one).
	if pl.Burn < 0 && (pl.Audio < 0 || pl.Audio == def.Index) && playsFile(p.Container, v, a, pl.Audio >= 0, c) {
		pl.Mode = Direct
		return pl, nil
	}

	pl.Mode = Transcode
	if pl.Burn < 0 && copiesVideo(v, c) {
		pl.Mode = Remux
	}
	if pl.Mode == Transcode {
		pl.ToneMap = isHDR(v)
	}
	if pl.Audio >= 0 {
		pl.AudioCopy = slices.Contains(segmentAudio, a.Codec) && slices.Contains(c.HLSAudio, a.Codec)
		if !pl.AudioCopy {
			pl.Channels = 2
			if a.Channels >= 6 && c.AudioChannels >= 6 {
				pl.Channels = 6
			}
		}
	}
	return pl, nil
}

func playsFile(container string, v, a probe.Stream, hasAudio bool, c Caps) bool {
	if !slices.Contains(c.DirectContainers, container) || !slices.Contains(c.DirectVideo, v.Codec) ||
		!slices.Contains(containerVideo[container], v.Codec) || !browserPicture(v) {
		return false
	}
	return !hasAudio || (slices.Contains(c.DirectAudio, a.Codec) && slices.Contains(containerAudio[container], a.Codec))
}

func copiesVideo(v probe.Stream, c Caps) bool {
	return slices.Contains(segmentVideo, v.Codec) && slices.Contains(c.HLSVideo, v.Codec) && browserPicture(v)
}

// browserPicture is false for pictures browsers do not decode even when
// they take the codec: 10-bit H.264 (Hi10P, common in anime releases) and
// 4:2:2 or 4:4:4 H.264.
func browserPicture(v probe.Stream) bool {
	if v.Codec != "h264" {
		return true
	}
	return v.BitDepth <= 8 && (v.PixFmt == "" || v.PixFmt == "yuv420p" || v.PixFmt == "yuvj420p")
}

func isHDR(v probe.Stream) bool { return v.Transfer == "smpte2084" || v.Transfer == "arib-std-b67" }

// String is the plan as it travels in a ticket: short query-style keys.
func (p Plan) String() string {
	q := url.Values{}
	q.Set("m", string(p.Mode))
	q.Set("a", strconv.Itoa(p.Audio))
	if p.AudioCopy {
		q.Set("ac", "1")
	} else if p.Channels > 0 {
		q.Set("ch", strconv.Itoa(p.Channels))
	}
	if p.Burn >= 0 {
		q.Set("b", strconv.Itoa(p.Burn))
	}
	if p.ToneMap {
		q.Set("tm", "1")
	}
	return q.Encode()
}

// Parse reads a plan written by String, refusing anything else.
func Parse(s string) (Plan, error) {
	q, err := url.ParseQuery(s)
	if err != nil {
		return Plan{}, fmt.Errorf("plan: %w", err)
	}
	for k := range q {
		if !slices.Contains([]string{"m", "a", "ac", "ch", "b", "tm"}, k) {
			return Plan{}, fmt.Errorf("plan: unknown key %q", k)
		}
	}
	p := Plan{Mode: Mode(q.Get("m")), Audio: -1, Burn: -1, AudioCopy: q.Get("ac") == "1", ToneMap: q.Get("tm") == "1"}
	if p.Mode != Direct && p.Mode != Remux && p.Mode != Transcode {
		return Plan{}, fmt.Errorf("plan: unknown mode %q", p.Mode)
	}
	num := func(key string, lo, hi int) (int, error) {
		v := q.Get(key)
		if v == "" {
			return -1, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("plan: bad %s %q", key, v)
		}
		return n, nil
	}
	if p.Audio, err = num("a", -1, 9999); err != nil {
		return Plan{}, err
	}
	if p.Burn, err = num("b", 0, 9999); err != nil {
		return Plan{}, err
	}
	ch, err := num("ch", 1, 8)
	if err != nil {
		return Plan{}, err
	}
	if ch > 0 {
		p.Channels = ch
	}
	if p.Audio >= 0 && !p.AudioCopy && p.Channels <= 0 && p.Mode != Direct {
		p.Channels = 2
	}
	if p.Burn >= 0 && p.Mode != Transcode {
		return Plan{}, errors.New("plan: a burnt-in subtitle needs a transcode")
	}
	return p, nil
}

// Key is the plan's identity for a running job: two requests with the
// same play and key share one ffmpeg.
func (p Plan) Key() string { return strings.ReplaceAll(p.String(), "&", ".") }
