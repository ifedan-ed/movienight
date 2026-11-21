// Package jfplay is the Jellyfin side of the in-browser player: asking
// Jellyfin how a browser can play an item (PlaybackInfo, with a device
// profile built from what the browser says it can decode), the item's
// chapters and media segments (intro, credits), the next episode, and
// reporting playback start, progress and stop so Jellyfin's resume points
// and played marks stay right.
//
// It is apart from package clients' Jellyfin, which reads the library and
// links accounts with the install's key: everything here runs as one
// person, with their own Jellyfin token when they linked one (Auth).
// The bytes themselves go through package stream.
package jfplay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"movienight/server/internal/clients"
)

// TicksPerSecond is Jellyfin's time unit (100 ns).
const TicksPerSecond = 10_000_000

// callTimeout bounds one call. PlaybackInfo can probe the file on a cold
// cache, so it is not the API pool's 25 s.
const callTimeout = 40 * time.Second

// maxAnswer caps an answer read into memory (a PlaybackInfo with many
// streams is a few hundred KB).
const maxAnswer = 8 << 20

// Client calls Jellyfin for playback.
type Client struct {
	http   *http.Client
	base   string
	apiKey string
	// dryRun (DRY_RUN): reports and saved positions are not sent, as with
	// every other call that changes Jellyfin. Playing still works.
	dryRun bool
}

// New makes the client on a pooled HTTP client (clients.Upstream's).
func New(c *http.Client, base, apiKey string, dryRun bool) *Client {
	return &Client{http: c, base: strings.TrimRight(base, "/"), apiKey: apiKey, dryRun: dryRun}
}

// Auth is whose session a call runs in: a person's own Jellyfin token, or
// the install's key when Token is empty. Device is the Jellyfin device id
// the play runs under, so Jellyfin can tie reports to the transcode.
type Auth struct {
	Token  string
	Device string
}

// Error is Jellyfin failing or refusing a call.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("Jellyfin: %s (HTTP %d)", e.Msg, e.Status) }

// call sends one request and decodes a JSON answer into out (when not
// nil). A 404 is (false, nil): the caller decides what "none" means.
func (c *Client) call(ctx context.Context, a Auth, method, path string, q url.Values, body, out any) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := a.Token
	if token == "" {
		token = c.apiKey
	}
	req.Header.Set("Authorization", clients.AuthHeader(token, a.Device, "Movie Night"))
	resp, err := c.http.Do(req)
	if err != nil {
		return false, &Error{Status: http.StatusBadGateway, Msg: "didn't answer"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return false, &Error{Status: http.StatusBadGateway, Msg: "answer cut short"}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 400:
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return false, &Error{Status: resp.StatusCode, Msg: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return false, &Error{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// PlaybackInfo
// ---------------------------------------------------------------------------

// MediaStream is one track of a media source, as Jellyfin describes it.
type MediaStream struct {
	Index                int    `json:"Index"`
	Type                 string `json:"Type"` // Video, Audio, Subtitle, ...
	Codec                string `json:"Codec"`
	Language             string `json:"Language"`
	Title                string `json:"Title"`
	DisplayTitle         string `json:"DisplayTitle"`
	Channels             int    `json:"Channels"`
	IsDefault            bool   `json:"IsDefault"`
	IsForced             bool   `json:"IsForced"`
	IsExternal           bool   `json:"IsExternal"`
	IsTextSubtitleStream bool   `json:"IsTextSubtitleStream"`
}

// Attachment is a file embedded in the source (an MKV's fonts).
type Attachment struct {
	Index    int    `json:"Index"`
	FileName string `json:"FileName"`
	MimeType string `json:"MimeType"`
}

// Source is the media source PlaybackInfo chose, with Jellyfin's verdict
// on how this browser can play it.
type Source struct {
	ID                         string        `json:"Id"`
	Container                  string        `json:"Container"`
	RunTimeTicks               int64         `json:"RunTimeTicks"`
	SupportsDirectPlay         bool          `json:"SupportsDirectPlay"`
	SupportsDirectStream       bool          `json:"SupportsDirectStream"`
	SupportsTranscoding        bool          `json:"SupportsTranscoding"`
	TranscodingURL             string        `json:"TranscodingUrl"`
	DefaultAudioStreamIndex    *int          `json:"DefaultAudioStreamIndex"`
	DefaultSubtitleStreamIndex *int          `json:"DefaultSubtitleStreamIndex"`
	MediaStreams               []MediaStream `json:"MediaStreams"`
	MediaAttachments           []Attachment  `json:"MediaAttachments"`
}

// Info is PlaybackInfo's answer: the play session and the first source.
type Info struct {
	PlaySessionID string
	Source        Source
}

// InfoRequest is what to ask PlaybackInfo for.
type InfoRequest struct {
	UserID string // the person's Jellyfin user (or the install owner's)
	Caps   Caps
	// AudioIndex picks an audio track; nil leaves Jellyfin's default.
	AudioIndex *int
	// BurnSubtitle is an image subtitle (PGS, VobSub) to burn into the
	// picture; nil burns nothing. Text subtitles are never burnt in.
	BurnSubtitle *int
	// NoDirect refuses direct play: a browser plays one audio track of a
	// file, so another track (or a burnt-in subtitle) needs Jellyfin's HLS.
	NoDirect bool
}

// MaxBitrate is the bitrate the player asks Jellyfin for (bits/s). High on
// purpose: the person's own Jellyfin limit still applies, and a remux is
// always cheaper for the server than a transcode down to a guess.
const MaxBitrate = 120_000_000

// PlaybackInfo asks Jellyfin how this browser can play an item.
func (c *Client) PlaybackInfo(ctx context.Context, a Auth, itemID string, r InfoRequest) (Info, error) {
	body := map[string]any{
		"DeviceProfile":        Profile(r.Caps),
		"MaxStreamingBitrate":  MaxBitrate,
		"EnableDirectPlay":     !r.NoDirect,
		"EnableDirectStream":   !r.NoDirect,
		"EnableTranscoding":    true,
		"AllowVideoStreamCopy": true,
		"AllowAudioStreamCopy": true,
		"AutoOpenLiveStream":   true,
		// -1 is "no subtitle": without it Jellyfin takes the file's default
		// subtitle, and an image one would force a burn-in nobody asked for.
		"SubtitleStreamIndex": -1,
	}
	if r.UserID != "" {
		body["UserId"] = r.UserID
	}
	if r.AudioIndex != nil {
		body["AudioStreamIndex"] = *r.AudioIndex
	}
	if r.BurnSubtitle != nil {
		body["SubtitleStreamIndex"] = *r.BurnSubtitle
	}
	q := url.Values{}
	if r.UserID != "" {
		q.Set("userId", r.UserID)
	}
	var out struct {
		MediaSources  []Source `json:"MediaSources"`
		PlaySessionID string   `json:"PlaySessionId"`
		ErrorCode     string   `json:"ErrorCode"`
	}
	found, err := c.call(ctx, a, http.MethodPost, "/Items/"+url.PathEscape(itemID)+"/PlaybackInfo", q, body, &out)
	if err != nil {
		return Info{}, err
	}
	if !found || len(out.MediaSources) == 0 {
		return Info{}, &Error{Status: http.StatusNotFound, Msg: "no media source"}
	}
	if out.ErrorCode != "" {
		return Info{}, &Error{Status: http.StatusUnprocessableEntity, Msg: out.ErrorCode}
	}
	return Info{PlaySessionID: out.PlaySessionID, Source: out.MediaSources[0]}, nil
}

// ---------------------------------------------------------------------------
// The item, its segments, the next episode
// ---------------------------------------------------------------------------

// Chapter is one chapter mark.
type Chapter struct {
	StartTicks int64  `json:"StartPositionTicks"`
	Name       string `json:"Name"`
}

// Item is what the player shows about an item.
type Item struct {
	ID           string    `json:"Id"`
	Name         string    `json:"Name"`
	Type         string    `json:"Type"`
	SeriesID     string    `json:"SeriesId"`
	SeriesName   string    `json:"SeriesName"`
	Season       *int      `json:"ParentIndexNumber"`
	Episode      *int      `json:"IndexNumber"`
	LocationType string    `json:"LocationType"`
	RunTimeTicks int64     `json:"RunTimeTicks"`
	Chapters     []Chapter `json:"Chapters"`
	UserData     struct {
		PlaybackPositionTicks int64 `json:"PlaybackPositionTicks"`
	} `json:"UserData"`
}

// Item reads one item as userID sees it (nil when Jellyfin has none).
func (c *Client) Item(ctx context.Context, a Auth, userID, itemID string) (*Item, error) {
	var it Item
	q := url.Values{"Fields": {"Chapters"}}
	path := "/Items/" + url.PathEscape(itemID)
	if userID != "" {
		path = "/Users/" + url.PathEscape(userID) + path
	}
	found, err := c.call(ctx, a, http.MethodGet, path, q, nil, &it)
	if err != nil || !found {
		return nil, err
	}
	return &it, nil
}

// Segment is a marked stretch of an item: Intro, Outro (credits), Recap,
// Preview or Commercial. Jellyfin has them only once a segment provider
// (an Intro Skipper plugin) has run.
type Segment struct {
	Type       string `json:"Type"`
	StartTicks int64  `json:"StartTicks"`
	EndTicks   int64  `json:"EndTicks"`
}

// Segments reads an item's media segments; none (and no error) when
// Jellyfin has none or has no segment API.
func (c *Client) Segments(ctx context.Context, a Auth, itemID string) ([]Segment, error) {
	var out struct {
		Items []Segment `json:"Items"`
	}
	if _, err := c.call(ctx, a, http.MethodGet, "/MediaSegments/"+url.PathEscape(itemID), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// NextEpisode is the episode after itemID in its series, as the library
// has it (nil at the end of what is there).
func (c *Client) NextEpisode(ctx context.Context, a Auth, userID, seriesID, itemID string) (*Item, error) {
	q := url.Values{"StartItemId": {itemID}, "Limit": {"2"}, "EnableImages": {"false"}}
	if userID != "" {
		q.Set("UserId", userID)
	}
	var out struct {
		Items []Item `json:"Items"`
	}
	if _, err := c.call(ctx, a, http.MethodGet, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, nil, &out); err != nil {
		return nil, err
	}
	// The list starts at the episode itself; a missing ("virtual") next one
	// cannot be played.
	if len(out.Items) < 2 || out.Items[1].LocationType == "Virtual" || out.Items[1].Season == nil || out.Items[1].Episode == nil {
		return nil, nil
	}
	return &out.Items[1], nil
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// Event is a playback report.
type Event int

const (
	Started Event = iota
	Progressed
	Stopped
)

// Progress is where a play is.
type Progress struct {
	ItemID        string
	MediaSourceID string
	PlaySessionID string
	PositionTicks int64
	Paused        bool
	AudioIndex    *int
	SubtitleIndex *int
	// Transcoded: Jellyfin's HLS rather than the original file.
	Transcoded bool
}

var reportPaths = map[Event]string{Started: "/Sessions/Playing", Progressed: "/Sessions/Playing/Progress", Stopped: "/Sessions/Playing/Stopped"}

// Report tells Jellyfin a play started, moved on or stopped. Jellyfin keeps
// the resume point from these, and marks the item played on a stop past
// its own threshold. With a person's token the play is theirs; with the
// install's key it belongs to no user (SavePosition covers that case).
func (c *Client) Report(ctx context.Context, a Auth, ev Event, p Progress) error {
	if c.dryRun {
		return nil
	}
	method := "DirectPlay"
	if p.Transcoded {
		method = "Transcode"
	}
	body := map[string]any{
		"ItemId": p.ItemID, "MediaSourceId": p.MediaSourceID, "PlaySessionId": p.PlaySessionID,
		"PositionTicks": p.PositionTicks, "IsPaused": p.Paused, "CanSeek": true, "PlayMethod": method,
	}
	if p.AudioIndex != nil {
		body["AudioStreamIndex"] = *p.AudioIndex
	}
	if p.SubtitleIndex != nil {
		body["SubtitleStreamIndex"] = *p.SubtitleIndex
	} else {
		body["SubtitleStreamIndex"] = -1
	}
	_, err := c.call(ctx, a, http.MethodPost, reportPaths[ev], nil, body, nil)
	return err
}

// SavePosition sets a user's resume point with the install's key: for a
// person whose Jellyfin account is known but who linked no token, whose
// reports Jellyfin cannot tie to them.
func (c *Client) SavePosition(ctx context.Context, userID, itemID string, ticks int64) error {
	if c.dryRun {
		return nil
	}
	q := url.Values{"userId": {userID}}
	_, err := c.call(ctx, Auth{}, http.MethodPost, "/UserItems/"+url.PathEscape(itemID)+"/UserData", q,
		map[string]any{"PlaybackPositionTicks": ticks}, nil)
	return err
}

// StopEncoding ends a play session's transcode at once (switching audio
// tracks starts a new one), rather than leaving Jellyfin to notice it
// idle.
func (c *Client) StopEncoding(ctx context.Context, a Auth, playSessionID string) error {
	q := url.Values{"deviceId": {a.Device}, "playSessionId": {playSessionID}}
	_, err := c.call(ctx, a, http.MethodDelete, "/Videos/ActiveEncodings", q, nil, nil)
	return err
}

// Seconds turns ticks into whole seconds.
func Seconds(ticks int64) int { return int(ticks / TicksPerSecond) }

// Ticks turns seconds into ticks.
func Ticks(s float64) int64 { return int64(s * TicksPerSecond) }
