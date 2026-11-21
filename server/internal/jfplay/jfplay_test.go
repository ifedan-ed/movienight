package jfplay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type call struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// fake is a Jellyfin that records every call and answers from routes.
type fake struct {
	*httptest.Server
	mu     sync.Mutex
	calls  []call
	routes map[string]any
}

func newFake(t *testing.T, routes map[string]any) *fake {
	f := &fake{routes: routes}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		_ = json.NewDecoder(r.Body).Decode(&c.Body)
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		ans, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ans == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) only(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("%d calls, want 1: %+v", len(f.calls), f.calls)
	}
	return f.calls[0]
}

func ptr(n int) *int { return &n }

func TestReport(t *testing.T) {
	f := newFake(t, map[string]any{"POST /Sessions/Playing": nil, "POST /Sessions/Playing/Progress": nil, "POST /Sessions/Playing/Stopped": nil})
	c := New(http.DefaultClient, f.URL, "SERVERKEY", false)
	p := Progress{ItemID: "item", MediaSourceID: "src", PlaySessionID: "ps", PositionTicks: Ticks(61.5), Paused: true,
		AudioIndex: ptr(2), Transcoded: true}
	for ev, path := range map[Event]string{Started: "/Sessions/Playing", Progressed: "/Sessions/Playing/Progress", Stopped: "/Sessions/Playing/Stopped"} {
		f.calls = nil
		if err := c.Report(context.Background(), Auth{Token: "usertoken", Device: "movienight-3"}, ev, p); err != nil {
			t.Fatal(err)
		}
		got := f.only(t)
		if got.Path != path {
			t.Fatalf("event %d went to %s", ev, got.Path)
		}
		if !strings.Contains(got.Auth, `Token="usertoken"`) || !strings.Contains(got.Auth, `DeviceId="movienight-3"`) {
			t.Fatalf("Authorization %q", got.Auth)
		}
		b := got.Body
		if b["ItemId"] != "item" || b["MediaSourceId"] != "src" || b["PlaySessionId"] != "ps" || b["PositionTicks"] != float64(615_000_000) ||
			b["IsPaused"] != true || b["AudioStreamIndex"] != float64(2) || b["SubtitleStreamIndex"] != float64(-1) || b["PlayMethod"] != "Transcode" {
			t.Fatalf("body %v", b)
		}
	}
	// Without a person's token the install's key is used.
	f.calls = nil
	if err := c.Report(context.Background(), Auth{Device: "d"}, Started, p); err != nil {
		t.Fatal(err)
	}
	if a := f.only(t).Auth; !strings.Contains(a, `Token="SERVERKEY"`) {
		t.Fatalf("Authorization %q", a)
	}
}

func TestReportDryRun(t *testing.T) {
	f := newFake(t, map[string]any{})
	c := New(http.DefaultClient, f.URL, "k", true)
	if err := c.Report(context.Background(), Auth{}, Stopped, Progress{}); err != nil {
		t.Fatal(err)
	}
	if err := c.SavePosition(context.Background(), "u", "i", 5); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("DRY_RUN sent %+v", f.calls)
	}
}

func TestSavePosition(t *testing.T) {
	f := newFake(t, map[string]any{"POST /UserItems/item/UserData": map[string]any{}})
	c := New(http.DefaultClient, f.URL, "k", false)
	if err := c.SavePosition(context.Background(), "juser", "item", 42); err != nil {
		t.Fatal(err)
	}
	got := f.only(t)
	if got.Query != "userId=juser" || got.Body["PlaybackPositionTicks"] != float64(42) {
		t.Fatalf("%+v", got)
	}
}

func TestPlaybackInfo(t *testing.T) {
	f := newFake(t, map[string]any{"POST /Items/item/PlaybackInfo": map[string]any{
		"PlaySessionId": "ps9",
		"MediaSources": []map[string]any{{"Id": "src", "Container": "mkv", "RunTimeTicks": 60 * TicksPerSecond,
			"SupportsDirectPlay": false, "TranscodingUrl": "/videos/item/master.m3u8?ApiKey=x",
			"MediaStreams": []map[string]any{{"Index": 1, "Type": "Audio", "Codec": "ac3"}}}},
	}})
	c := New(http.DefaultClient, f.URL, "k", false)
	info, err := c.PlaybackInfo(context.Background(), Auth{Token: "t", Device: "d"}, "item",
		InfoRequest{UserID: "u", Caps: Caps{DirectContainers: []string{"mp4"}, DirectVideo: []string{"h264"}, HLSVideo: []string{"h264"}, HLSAudio: []string{"aac"}},
			AudioIndex: ptr(1), NoDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.PlaySessionID != "ps9" || info.Source.ID != "src" || info.Source.TranscodingURL == "" || len(info.Source.MediaStreams) != 1 {
		t.Fatalf("%+v", info)
	}
	b := f.only(t).Body
	if b["SubtitleStreamIndex"] != float64(-1) || b["AudioStreamIndex"] != float64(1) || b["EnableDirectPlay"] != false || b["UserId"] != "u" {
		t.Fatalf("request %v", b)
	}
	if b["DeviceProfile"] == nil {
		t.Fatal("no device profile sent")
	}
}

func TestSegmentsNone(t *testing.T) {
	f := newFake(t, map[string]any{})
	c := New(http.DefaultClient, f.URL, "k", false)
	segs, err := c.Segments(context.Background(), Auth{}, "item")
	if err != nil || len(segs) != 0 {
		t.Fatalf("no segment API: %v %v", segs, err)
	}
}

func TestNextEpisode(t *testing.T) {
	f := newFake(t, map[string]any{"GET /Shows/series/Episodes": map[string]any{"Items": []map[string]any{
		{"Id": "e1", "ParentIndexNumber": 1, "IndexNumber": 1},
		{"Id": "e2", "ParentIndexNumber": 1, "IndexNumber": 2, "Name": "Two"},
	}}})
	c := New(http.DefaultClient, f.URL, "k", false)
	n, err := c.NextEpisode(context.Background(), Auth{}, "u", "series", "e1")
	if err != nil || n == nil || *n.Season != 1 || *n.Episode != 2 || n.Name != "Two" {
		t.Fatalf("%+v %v", n, err)
	}
	if q := f.only(t).Query; !strings.Contains(q, "StartItemId=e1") {
		t.Fatalf("query %q", q)
	}
}

func TestProfile(t *testing.T) {
	p := Profile(Caps{DirectContainers: []string{"mp4", "webm"}, DirectVideo: []string{"h264", "vp9"}, DirectAudio: []string{"aac", "opus"},
		HLSVideo: []string{"h264"}, HLSAudio: []string{"aac", "ac3", "opus"}})
	raw, _ := json.Marshal(p)
	var got struct {
		DirectPlayProfiles  []map[string]any
		TranscodingProfiles []map[string]any
	}
	_ = json.Unmarshal(raw, &got)
	if len(got.DirectPlayProfiles) != 2 {
		t.Fatalf("direct %v", got.DirectPlayProfiles)
	}
	mp4 := got.DirectPlayProfiles[0]
	if mp4["Container"] != "mp4,m4v,mov" || mp4["VideoCodec"] != "h264,vp9" || mp4["AudioCodec"] != "aac,opus" {
		t.Fatalf("mp4 %v", mp4)
	}
	if webm := got.DirectPlayProfiles[1]; webm["VideoCodec"] != "vp9" || webm["AudioCodec"] != "opus" {
		t.Fatalf("webm %v", webm)
	}
	// H.264 only in HLS: MPEG-TS, and opus (not in TS) left out.
	tp := got.TranscodingProfiles[0]
	if tp["Container"] != "ts" || tp["VideoCodec"] != "h264" || tp["AudioCodec"] != "aac,ac3" || tp["Protocol"] != "hls" {
		t.Fatalf("transcode %v", tp)
	}
	// HEVC in HLS: fMP4, so HEVC can be copied.
	p = Profile(Caps{HLSVideo: []string{"h264", "hevc"}, HLSAudio: []string{"aac", "opus"}})
	raw, _ = json.Marshal(p)
	_ = json.Unmarshal(raw, &got)
	if tp := got.TranscodingProfiles[0]; tp["Container"] != "mp4" || tp["VideoCodec"] != "h264,hevc" || tp["AudioCodec"] != "aac,opus" {
		t.Fatalf("fmp4 %v", tp)
	}
}

func TestSubtitleKind(t *testing.T) {
	for codec, want := range map[string]string{"subrip": "text", "webvtt": "text", "mov_text": "text", "ass": "ass", "SSA": "ass",
		"PGSSUB": "image", "dvdsub": "image", "dvbsub": "image"} {
		isText := want != "image"
		if got := SubtitleKind(codec, isText); got != want {
			t.Errorf("%s: %s, want %s", codec, got, want)
		}
	}
	if SubtitleKind("unknown", false) != "image" {
		t.Error("an unknown non-text codec must not be treated as text")
	}
}
