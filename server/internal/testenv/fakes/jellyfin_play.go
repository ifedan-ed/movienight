package fakes

// The fake Jellyfin's playback side, for the in-browser player's browser
// tests: a real clip (e2e/run.sh makes it with ffmpeg) served as the file
// itself and as HLS, with its subtitle tracks, chapters and an intro. Only
// with MediaDir set; without it these addresses answer 404.
//
// MediaDir holds clip.mp4 (60 s, H.264 with two AAC tracks: English, then
// German), hls-1/ and hls-2/ (the clip as HLS with the first or the second
// audio track: main.m3u8 and 0.ts, 1.ts, ...), subs-3.vtt and subs-4.vtt
// (two text tracks) and subs-5.ass (a styled one).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ClipSeconds is the clip's length.
const ClipSeconds = 60

func ticks(s float64) int64 { return int64(s * 10_000_000) }

// clipSource is the clip's media source, as PlaybackInfo describes it.
func clipSource(id string) JSON {
	return JSON{"Id": id, "Container": "mp4", "RunTimeTicks": ticks(ClipSeconds), "SupportsDirectPlay": true,
		"SupportsTranscoding": true, "DefaultAudioStreamIndex": 1,
		"MediaStreams": []JSON{
			{"Type": "Video", "Index": 0, "Codec": "h264"},
			{"Type": "Audio", "Index": 1, "Codec": "aac", "Language": "eng", "DisplayTitle": "English", "IsDefault": true, "Channels": 2},
			{"Type": "Audio", "Index": 2, "Codec": "aac", "Language": "ger", "DisplayTitle": "Deutsch", "Channels": 2},
			{"Type": "Subtitle", "Index": 3, "Codec": "subrip", "Language": "eng", "DisplayTitle": "English", "IsTextSubtitleStream": true},
			{"Type": "Subtitle", "Index": 4, "Codec": "subrip", "Language": "fre", "DisplayTitle": "Français", "IsTextSubtitleStream": true, "IsExternal": true},
			{"Type": "Subtitle", "Index": 5, "Codec": "ass", "Language": "eng", "DisplayTitle": "English (styled)", "IsTextSubtitleStream": true},
			{"Type": "Subtitle", "Index": 6, "Codec": "PGSSUB", "Language": "eng", "DisplayTitle": "English (picture)"},
		}}
}

// ClipChapters are the clip's chapters.
var ClipChapters = []JSON{
	{"StartPositionTicks": 0, "Name": "Opening"},
	{"StartPositionTicks": ticks(20), "Name": "Middle"},
	{"StartPositionTicks": ticks(40), "Name": "Ending"},
}

var (
	segmentLine = regexp.MustCompile(`^(\d+)\.ts$`)
	hlsSegment  = regexp.MustCompile(`^/videos/[^/]+/hls1/main/(\d+)\.ts$`)
	subtitleURL = regexp.MustCompile(`^/Videos/[^/]+/[^/]+/Subtitles/(\d+)/0/Stream\.(vtt|ass)$`)
)

// audioDir is the HLS copy with the asked-for audio track.
func audioDir(r *http.Request) string {
	if r.URL.Query().Get("AudioStreamIndex") == "2" {
		return "hls-2"
	}
	return "hls-1"
}

// play answers the playback addresses; false for any other.
func (j *Jellyfin) play(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	parts := strings.Split(strings.Trim(p, "/"), "/")
	dir := j.MediaDir
	switch {
	case len(parts) == 3 && parts[0] == "Items" && parts[2] == "PlaybackInfo" && r.Method == http.MethodPost:
		var body JSON
		_ = jsonDecode(r, &body)
		src := clipSource(parts[1])
		j.playSessions++
		ps := fmt.Sprintf("play-session-%d", j.playSessions)
		if body["EnableDirectPlay"] == false {
			src["SupportsDirectPlay"] = false
			audio := body["AudioStreamIndex"]
			if audio == nil {
				audio = 1
			}
			src["TranscodingUrl"] = fmt.Sprintf("/videos/%s/master.m3u8?DeviceId=dev&MediaSourceId=%s&PlaySessionId=%s&ApiKey=user-token"+
				"&VideoCodec=h264&AudioCodec=aac&AudioStreamIndex=%v&SubtitleStreamIndex=%v&SegmentContainer=ts",
				parts[1], parts[1], ps, audio, body["SubtitleStreamIndex"])
		}
		Reply(w, JSON{"MediaSources": []JSON{src}, "PlaySessionId": ps})
	case len(parts) == 3 && parts[0] == "Videos" && strings.HasPrefix(parts[2], "stream"):
		http.ServeFile(w, r, filepath.Join(dir, "clip.mp4"))
	case len(parts) == 3 && parts[0] == "videos" && parts[2] == "master.m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=320x180\nmain.m3u8?%s\n", r.URL.RawQuery)
	case len(parts) == 3 && parts[0] == "videos" && parts[2] == "main.m3u8":
		f, err := os.Open(filepath.Join(dir, audioDir(r), "main.m3u8"))
		if err != nil {
			notFound(w)
			return true
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			// Jellyfin's segments carry the transcode's query, its key too.
			if segmentLine.MatchString(line) {
				line = "hls1/main/" + line + "?" + r.URL.RawQuery
			}
			fmt.Fprintln(w, line)
		}
	case hlsSegment.MatchString(p):
		n := hlsSegment.FindStringSubmatch(p)[1]
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeFile(w, r, filepath.Join(dir, audioDir(r), n+".ts"))
	case subtitleURL.MatchString(p):
		m := subtitleURL.FindStringSubmatch(p)
		http.ServeFile(w, r, filepath.Join(dir, "subs-"+m[1]+"."+m[2]))
	case len(parts) == 2 && parts[0] == "MediaSegments":
		Reply(w, JSON{"Items": []JSON{{"Type": "Intro", "StartTicks": 0, "EndTicks": ticks(10)}}})
	case strings.HasPrefix(p, "/Sessions/Playing"), p == "/Videos/ActiveEncodings":
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[0] == "UserItems" && parts[2] == "UserData":
		Reply(w, JSON{})
	default:
		return false
	}
	return true
}

func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
