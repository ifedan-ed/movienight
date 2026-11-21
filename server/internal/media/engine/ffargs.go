package engine

import (
	"fmt"
	"strconv"
	"strings"

	"movienight/server/internal/media/plan"
	"movienight/server/internal/media/probe"
)

// HW is the hardware encoder a transcode uses (MEDIA_HWACCEL).
type HW struct {
	Kind   string // none, vaapi, qsv, nvenc
	Device string // the render node for vaapi and qsv (/dev/dri/renderD128)
}

// HWKinds are the values MEDIA_HWACCEL takes.
var HWKinds = []string{"none", "vaapi", "qsv", "nvenc"}

// Transcode settings. veryfast is the CPU preset that keeps a 1080p
// encode well ahead of playback on a few cores; CRF 23 is x264's own
// default quality. maxrate caps a burst so a home upload keeps up.
const (
	x264Preset = "veryfast"
	x264CRF    = "23"
	maxRate    = "10M"
	bufSize    = "20M"
	// maxHeight: a 4K source is scaled to 1080p when transcoded. Encoding
	// 4K on a CPU is several times slower than playback.
	maxHeight = 1080
)

// hlsArgs are ffmpeg's arguments for one HLS run of p, starting at
// segment start, writing fragmented MP4 segments into dir.
//
// Timestamps: -ss seeks the input to the segment's start, and -copyts with
// -start_at_zero keeps the file's own timeline (starting at zero), so a run
// started at any segment lines up with the playlist and with segments of
// an earlier run. A transcode forces a keyframe every SegmentSeconds of
// output, so every segment starts with one and is exactly that long; a
// remux can only cut at the file's own keyframes.
func hlsArgs(file string, pr *probe.Probe, p plan.Plan, start int, dir string, hw HW) []string {
	at := start * plan.SegmentSeconds
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	transcode := p.Mode == plan.Transcode
	if transcode {
		args = append(args, hwInit(hw)...)
	} else {
		// Old containers (AVI) can lack timestamps a remux needs.
		args = append(args, "-fflags", "+genpts")
	}
	if at > 0 {
		args = append(args, "-ss", strconv.Itoa(at))
	}
	args = append(args, "-copyts", "-start_at_zero", "-i", file)

	v, _ := pr.Video()
	if transcode {
		args = append(args, "-filter_complex", videoFilter(v, p, hw)+"[v]", "-map", "[v]")
		args = append(args, videoEncoder(hw)...)
		// A keyframe at every segment boundary (t counts from the run's
		// start, which is itself on a boundary).
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", plan.SegmentSeconds))
	} else {
		args = append(args, "-map", "0:"+strconv.Itoa(v.Index), "-c:v", "copy")
		if v.Codec == "hevc" {
			// Browsers (and Apple's HLS) take HEVC in MP4 only as hvc1.
			args = append(args, "-tag:v", "hvc1")
		}
	}

	if p.Audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Audio))
		if p.AudioCopy {
			args = append(args, "-c:a", "copy")
		} else {
			bitrate := "160k"
			if p.Channels >= 6 {
				bitrate = "384k"
			}
			args = append(args, "-c:a", "aac", "-ac", strconv.Itoa(p.Channels), "-b:a", bitrate)
		}
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		// Sparse streams (a subtitle being burnt in) must not stall the muxer.
		"-max_muxing_queue_size", "4096",
		"-f", "hls", "-hls_time", strconv.Itoa(plan.SegmentSeconds), "-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4", "-start_number", strconv.Itoa(start),
		// A segment appears under its name only once it is complete.
		"-hls_flags", "temp_file+independent_segments", "-hls_list_size", "0",
		"-hls_segment_filename", dir+"/%d.m4s", dir+"/ffmpeg.m3u8")
	return args
}

// videoFilter is the transcode's filter graph up to the encoder: the
// burnt-in subtitle, tone mapping, the size, and the pixel format the
// encoder takes (uploaded to the GPU for VAAPI and QSV).
func videoFilter(v probe.Stream, p plan.Plan, hw HW) string {
	var b strings.Builder
	if p.Burn >= 0 {
		// The subtitle drawn over the picture; eof_action=pass keeps the
		// picture going after the last subtitle.
		fmt.Fprintf(&b, "[0:%d][0:%d]overlay=eof_action=pass", v.Index, p.Burn)
	} else {
		fmt.Fprintf(&b, "[0:%d]null", v.Index)
	}
	if p.ToneMap {
		// HDR (PQ or HLG) to SDR BT.709 with the Hable curve.
		b.WriteString(",zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0," +
			"zscale=t=bt709:m=bt709:r=tv")
	}
	if v.Height > maxHeight {
		fmt.Fprintf(&b, ",scale=-2:%d", maxHeight)
	}
	switch hw.Kind {
	case "vaapi":
		b.WriteString(",format=nv12,hwupload")
	case "qsv":
		b.WriteString(",format=nv12,hwupload=extra_hw_frames=64")
	default:
		b.WriteString(",format=yuv420p")
	}
	return b.String()
}

// hwInit opens the hardware device before the input. The picture is
// decoded on the CPU and uploaded: slower than decoding on the GPU too, but
// it works with every filter (burn-in, tone mapping) and every source.
func hwInit(hw HW) []string {
	switch hw.Kind {
	case "vaapi":
		return []string{"-init_hw_device", "vaapi=hw:" + hw.Device, "-filter_hw_device", "hw"}
	case "qsv":
		return []string{"-init_hw_device", "vaapi=va:" + hw.Device, "-init_hw_device", "qsv=hw@va", "-filter_hw_device", "hw"}
	}
	return nil
}

func videoEncoder(hw HW) []string {
	switch hw.Kind {
	case "vaapi":
		return []string{"-c:v", "h264_vaapi", "-rc_mode", "CQP", "-qp", "23", "-profile:v", "high"}
	case "qsv":
		return []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-global_quality", "23", "-profile:v", "high"}
	case "nvenc":
		return []string{"-c:v", "h264_nvenc", "-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0",
			"-maxrate", maxRate, "-bufsize", bufSize, "-profile:v", "high", "-forced-idr", "1"}
	}
	return []string{"-c:v", "libx264", "-preset", x264Preset, "-crf", x264CRF, "-maxrate", maxRate, "-bufsize", bufSize,
		"-profile:v", "high", "-sc_threshold", "0"}
}

// Playlist is the HLS playlist of a file: every segment listed from the
// start (a VOD playlist), so the player knows the length at once and can
// seek anywhere; a segment not made yet is made when it is asked for.
func Playlist(duration float64) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MAP:URI=\"hls/init.mp4\"\n", plan.SegmentSeconds)
	n := Segments(duration)
	for i := range n {
		d := float64(plan.SegmentSeconds)
		if i == n-1 {
			d = duration - float64(i*plan.SegmentSeconds)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nhls/%d.m4s\n", d, i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}

// Segments is how many segments a file of duration seconds has.
func Segments(duration float64) int {
	n := int(duration) / plan.SegmentSeconds
	if float64(n*plan.SegmentSeconds) < duration-0.05 {
		n++
	}
	return max(n, 1)
}
