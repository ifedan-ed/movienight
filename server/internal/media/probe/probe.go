// Package probe reads what is inside a video file with ffprobe: its
// container and length, its video, audio and subtitle streams with their
// codecs and languages, its chapters and its attached fonts.
//
// The result (Probe) is what the scanner reports to the API, what the API
// stores per file and decides playback from, and what the media engine
// builds ffmpeg's arguments from. It is plain data with JSON names, so the
// three agree on one shape.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/media/sidecar"
)

// Stream is one stream of the file. Index is ffprobe's stream index, the
// number ffmpeg's -map 0:N takes and the player's track index.
type Stream struct {
	Index    int    `json:"index"`
	Type     string `json:"type"` // video, audio, subtitle, attachment, data
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Language string `json:"language,omitempty"` // ISO 639-2/B ("eng"), "" when untagged
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	HI       bool   `json:"hi,omitempty"` // hearing impaired

	// Video.
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	PixFmt    string  `json:"pix_fmt,omitempty"`
	FrameRate float64 `json:"fps,omitempty"`
	// HDR: the transfer characteristic ("smpte2084" is HDR10, "arib-std-b67"
	// HLG). A transcode of HDR has to tone-map, or the picture is grey.
	Transfer string `json:"transfer,omitempty"`
	BitDepth int    `json:"bit_depth,omitempty"`

	// Audio.
	Channels int    `json:"channels,omitempty"`
	Layout   string `json:"layout,omitempty"`

	BitRate int64 `json:"bit_rate,omitempty"`

	// Attachments (fonts).
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// Chapter is one chapter, in seconds.
type Chapter struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title,omitempty"`
}

// Probe is everything read from one file.
type Probe struct {
	// Container is the file's container in the browser's terms: mp4, webm,
	// mkv, mov, avi, ts, ...
	Container string    `json:"container"`
	Duration  float64   `json:"duration"` // seconds
	BitRate   int64     `json:"bit_rate,omitempty"`
	Streams   []Stream  `json:"streams"`
	Chapters  []Chapter `json:"chapters"`
	// External subtitle files next to the video; filled by the scanner.
	External []sidecar.File `json:"external,omitempty"`
}

// Of returns the streams of one type, in file order.
func (p *Probe) Of(kind string) []Stream {
	var out []Stream
	for _, s := range p.Streams {
		if s.Type == kind {
			out = append(out, s)
		}
	}
	return out
}

// Stream is the stream with ffprobe index i.
func (p *Probe) Stream(i int) (Stream, bool) {
	for _, s := range p.Streams {
		if s.Index == i {
			return s, true
		}
	}
	return Stream{}, false
}

// Video is the main picture: the first video stream that is not cover art.
func (p *Probe) Video() (Stream, bool) {
	for _, s := range p.Streams {
		if s.Type == "video" {
			return s, true
		}
	}
	return Stream{}, false
}

// DefaultAudio is the track a player plays from the file: the first
// flagged default, else the first; ok false when there is no audio.
func (p *Probe) DefaultAudio() (Stream, bool) {
	audio := p.Of("audio")
	for _, s := range audio {
		if s.Default {
			return s, true
		}
	}
	if len(audio) > 0 {
		return audio[0], true
	}
	return Stream{}, false
}

// ExternalIndex is the track index of the n-th external subtitle: after
// every stream of the file, so embedded and external indexes never clash.
func (p *Probe) ExternalIndex(n int) int {
	top := -1
	for _, s := range p.Streams {
		top = max(top, s.Index)
	}
	return top + 1 + n
}

// ffprobe's JSON, the parts used.
type raw struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		Index         int               `json:"index"`
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		Profile       string            `json:"profile"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		PixFmt        string            `json:"pix_fmt"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		RFrameRate    string            `json:"r_frame_rate"`
		ColorTransfer string            `json:"color_transfer"`
		BitsPerRaw    string            `json:"bits_per_raw_sample"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		BitRate       string            `json:"bit_rate"`
		Duration      string            `json:"duration"`
		Disposition   map[string]int    `json:"disposition"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
}

// Parse reads ffprobe's JSON (-print_format json -show_format
// -show_streams -show_chapters). name is the file's name, for the
// container ffprobe names only as a family ("mov,mp4,m4a,...").
func Parse(data []byte, name string) (*Probe, error) {
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("ffprobe's answer isn't JSON: %w", err)
	}
	if r.Format.FormatName == "" {
		return nil, errors.New("ffprobe found no container")
	}
	p := &Probe{Container: container(r.Format.FormatName, name), Duration: num(r.Format.Duration),
		BitRate: int64(num(r.Format.BitRate)), Streams: []Stream{}, Chapters: []Chapter{}}
	videoLen := 0.0
	for _, s := range r.Streams {
		st := Stream{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile,
			Language: sidecar.Language(tag(s.Tags, "language")), Title: tag(s.Tags, "title"),
			Default: s.Disposition["default"] == 1, Forced: s.Disposition["forced"] == 1,
			HI: s.Disposition["hearing_impaired"] == 1, BitRate: int64(num(s.BitRate))}
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 {
				// Cover art is a picture, not the film: left out entirely.
				continue
			}
			st.Width, st.Height, st.PixFmt, st.Transfer = s.Width, s.Height, s.PixFmt, s.ColorTransfer
			st.FrameRate = rate(s.AvgFrameRate)
			if st.FrameRate == 0 {
				st.FrameRate = rate(s.RFrameRate)
			}
			st.BitDepth = int(num(s.BitsPerRaw))
			// The picture's own length: the file's can be longer, when a
			// subtitle or audio track runs past the end of the film, and the
			// playlist must end where the picture does.
			if d := num(s.Duration); d > 0 {
				videoLen = d
			} else if d := clock(tag(s.Tags, "DURATION")); d > 0 {
				videoLen = d
			}
			if st.BitDepth == 0 && strings.Contains(s.PixFmt, "10") {
				st.BitDepth = 10
			}
		case "audio":
			st.Channels, st.Layout = s.Channels, s.ChannelLayout
		case "attachment":
			st.FileName, st.MimeType = tag(s.Tags, "filename"), tag(s.Tags, "mimetype")
			if st.Codec == "" {
				st.Codec = "attachment"
			}
		case "subtitle":
		default:
			st.Type = "data"
		}
		p.Streams = append(p.Streams, st)
	}
	for _, c := range r.Chapters {
		p.Chapters = append(p.Chapters, Chapter{Start: num(c.StartTime), End: num(c.EndTime), Title: tag(c.Tags, "title")})
	}
	if videoLen > 0 && (p.Duration <= 0 || videoLen < p.Duration) {
		p.Duration = videoLen
	}
	if p.Duration <= 0 {
		return nil, errors.New("ffprobe found no length")
	}
	if _, ok := p.Video(); !ok {
		return nil, errors.New("the file has no picture")
	}
	return p, nil
}

// Tags come in any case ("language", "LANGUAGE"), depending on the muxer.
func tag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return strings.TrimSpace(v)
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func num(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// clock reads Matroska's per-stream DURATION tag ("00:00:30.040000000").
func clock(s string) float64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	return num(parts[0])*3600 + num(parts[1])*60 + num(parts[2])
}

func rate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return num(s)
	}
	if d := num(b); d > 0 {
		return num(a) / d
	}
	return 0
}

// container names the container as the browser and the decision know
// it. ffprobe's MP4 family ("mov,mp4,m4a,3gp,3g2,mj2") and its Matroska
// one ("matroska,webm") cover several; the file's extension tells them
// apart.
func container(format, name string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	switch {
	case strings.HasPrefix(format, "mov,mp4"):
		if ext == "mov" {
			return "mov"
		}
		return "mp4"
	case strings.HasPrefix(format, "matroska"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case format == "mpegts":
		return "ts"
	}
	f, _, _ := strings.Cut(format, ",")
	return f
}

// Runner runs ffprobe.
type Runner struct {
	// Bin is the ffprobe binary ("ffprobe": looked up on PATH).
	Bin string
	// Timeout bounds one probe; a file on a sleeping disk or a broken one
	// must not hold a scan worker forever.
	Timeout time.Duration
}

// Run probes the file at file.
func (r Runner) Run(ctx context.Context, file string) (*Probe, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bin := r.Bin
	if bin == "" {
		bin = "ffprobe"
	}
	// -analyzeduration and -probesize stay at ffprobe's defaults: enough to
	// find every stream's codec, without reading far into a big file.
	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-print_format", "json", "-show_format", "-show_streams",
		"-show_chapters", "-i", file)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ffprobe took longer than %s", timeout)
		}
		return nil, errors.New(lastLine(errOut.String(), file, err))
	}
	return Parse(out.Bytes(), file)
}

// lastLine is what ffprobe said in the end ("Invalid data found when
// processing input"), without the file's path in front: the line before
// it is usually a demuxer's detail, of no use to whoever reads the
// Libraries page.
func lastLine(stderr, file string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	msg := strings.TrimSpace(lines[len(lines)-1])
	msg = strings.TrimPrefix(msg, file+": ")
	if msg == "" {
		return err.Error()
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
