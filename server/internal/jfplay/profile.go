package jfplay

import "strings"

// Caps is what the browser says it can decode, in a small fixed vocabulary
// (Codecs below; the API refuses anything else). The browser measures it
// (canPlayType for the file, MediaSource.isTypeSupported for HLS), so
// Jellyfin is told the truth about this browser instead of a guess by name.
type Caps struct {
	// Direct: what the <video> element plays as a file.
	DirectContainers []string
	DirectVideo      []string
	DirectAudio      []string
	// HLS: what the HLS player (hls.js, or Safari's own) plays.
	HLSVideo []string
	HLSAudio []string
}

// The vocabulary of Caps.
var (
	Containers  = []string{"mp4", "webm"}
	VideoCodecs = []string{"h264", "hevc", "av1", "vp9", "vp8"}
	AudioCodecs = []string{"aac", "mp3", "opus", "flac", "ac3", "eac3", "vorbis", "alac"}
)

// What each container can hold. Jellyfin names an MP4 file's container
// "mp4", "m4v" or "mov" depending on the file.
var (
	containerNames = map[string]string{"mp4": "mp4,m4v,mov", "webm": "webm"}
	containerVideo = map[string][]string{"mp4": {"h264", "hevc", "av1", "vp9"}, "webm": {"vp8", "vp9", "av1"}}
	containerAudio = map[string][]string{"mp4": {"aac", "mp3", "opus", "flac", "ac3", "eac3", "alac"}, "webm": {"opus", "vorbis"}}
	// HLS in MPEG-TS holds H.264 and these; fMP4 also takes HEVC and AV1
	// and more audio.
	tsAudio   = []string{"aac", "mp3", "ac3", "eac3"}
	fmp4Video = []string{"h264", "hevc", "av1"}
	fmp4Audio = []string{"aac", "mp3", "ac3", "eac3", "opus", "flac"}
)

// TranscodeChannels is the most audio channels a transcode keeps. Stereo:
// a browser cannot tell whether surround speakers are attached, and a
// 5.1 source the browser can decode is played as it is anyway.
const TranscodeChannels = "2"

// both keeps the codecs of want, in want's order, that have also holds.
func both(want, has []string) []string {
	set := map[string]bool{}
	for _, h := range has {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if set[w] {
			out = append(out, w)
		}
	}
	return out
}

// Profile is the Jellyfin DeviceProfile for a browser with caps:
//
//   - direct play of the containers and codecs the <video> element plays;
//   - otherwise Jellyfin's HLS, remuxing where the codecs fit (cheap) and
//     transcoding to H.264/AAC where they do not. fMP4 segments when the
//     browser takes HEVC or AV1 in HLS (so those can be copied), else
//     MPEG-TS, which every HLS player takes;
//   - subtitles as separate files (WebVTT, ASS/SSA for the styled
//     renderer), never burnt in unless asked for (InfoRequest.BurnSubtitle).
func Profile(c Caps) map[string]any {
	direct := []map[string]any{}
	for _, ct := range both(Containers, c.DirectContainers) {
		v, a := both(containerVideo[ct], c.DirectVideo), both(containerAudio[ct], c.DirectAudio)
		if len(v) == 0 {
			continue
		}
		p := map[string]any{"Type": "Video", "Container": containerNames[ct], "VideoCodec": strings.Join(v, ",")}
		if len(a) > 0 {
			p["AudioCodec"] = strings.Join(a, ",")
		}
		direct = append(direct, p)
	}

	container, video, audio := "ts", []string{"h264"}, both(tsAudio, c.HLSAudio)
	if v := both(fmp4Video, c.HLSVideo); len(v) > 1 {
		container, video, audio = "mp4", v, both(fmp4Audio, c.HLSAudio)
	}
	if len(audio) == 0 {
		audio = []string{"aac"} // every HLS player takes AAC; never an empty list
	}
	transcode := []map[string]any{{
		"Type": "Video", "Container": container, "Protocol": "hls", "Context": "Streaming",
		// The first codec of each list is what a transcode makes; the
		// others are kept as they are when the source has them.
		"VideoCodec": strings.Join(video, ","), "AudioCodec": strings.Join(audio, ","),
		"MaxAudioChannels": TranscodeChannels, "MinSegments": 1, "BreakOnNonKeyFrames": true,
	}}

	return map[string]any{
		"Name":                "Movie Night",
		"MaxStreamingBitrate": MaxBitrate,
		"MaxStaticBitrate":    MaxBitrate,
		"DirectPlayProfiles":  direct,
		"TranscodingProfiles": transcode,
		"ContainerProfiles":   []any{},
		"CodecProfiles":       []any{},
		"SubtitleProfiles": []map[string]any{
			{"Format": "vtt", "Method": "External"},
			{"Format": "ass", "Method": "External"},
			{"Format": "ssa", "Method": "External"},
		},
		// An .m4v file is MP4 to a browser.
		"ResponseProfiles": []map[string]any{{"Type": "Video", "Container": "m4v", "MimeType": "video/mp4"}},
	}
}

// SubtitleKind is how the player shows a subtitle codec: "text" (turned
// into WebVTT and drawn by the player), "ass" (styled, drawn by JASSUB) or
// "image" (PGS, VobSub, DVB: only by burning into the picture).
func SubtitleKind(codec string, isText bool) string {
	switch strings.ToLower(codec) {
	case "ass", "ssa":
		return "ass"
	case "pgssub", "pgs", "hdmv_pgs_subtitle", "dvdsub", "dvd_subtitle", "vobsub", "dvbsub", "dvb_subtitle", "xsub":
		return "image"
	}
	if isText {
		return "text"
	}
	return "image"
}
