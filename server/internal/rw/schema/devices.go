package schema

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// ---- Scrobbles and the Jellyfin plugin (rw/devices) ----

// ScrobbleIn is a finished watch reported by another app (the Kodi add-on,
// etc.).
type ScrobbleIn struct {
	_         struct{} `additionalProperties:"true"`
	MediaType string   `json:"media_type" enum:"movie,episode"`
	TMDBID    *int     `json:"tmdb_id,omitempty" exclusiveMinimum:"0" nullable:"true" doc:"movie id, or the show's id for an episode"`
	IMDbID    *string  `json:"imdb_id,omitempty" pattern:"^tt\\d{5,10}$" nullable:"true"`
	TVDBID    *int     `json:"tvdb_id,omitempty" exclusiveMinimum:"0" nullable:"true" doc:"the show's TheTVDB id, for libraries scraped from TheTVDB"`
	Title     *string  `json:"title,omitempty" maxLength:"500" nullable:"true"`
	Season    *int     `json:"season,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Episode   *int     `json:"episode,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	WatchedAt *string  `json:"watched_at,omitempty" maxLength:"40" nullable:"true"`
	Source    string   `json:"source,omitempty" default:"api" pattern:"^[a-z0-9_-]{1,30}$"`
	Ref       *string  `json:"ref,omitempty" maxLength:"500" nullable:"true" doc:"the sender's own id for this play, to avoid duplicates"`
}

// ScrobbleOut says whether the watch was new.
type ScrobbleOut struct {
	Ok       bool    `json:"ok" default:"true" required:"false"`
	Recorded bool    `json:"recorded"`
	Detail   *string `json:"detail" required:"false" nullable:"true"`
}

// JellyfinPluginItem is a film or episode as the Movie Night plugin for
// Jellyfin sends it. Texts up to REPORTED_MAX_CHARS (500), whole numbers up
// to the database's range: the plugin never retries a refusal, so nothing
// real may be refused.
type JellyfinPluginItem struct {
	_          struct{} `additionalProperties:"true"`
	ID         string   `json:"id" pattern:"^[0-9a-fA-F-]{32,36}$"`
	Type       string   `json:"type" enum:"Movie,Episode"`
	Name       string   `json:"name,omitempty" maxLength:"500"`
	SeriesID   *string  `json:"series_id,omitempty" pattern:"^[0-9a-fA-F-]{32,36}$" nullable:"true"`
	SeriesName *string  `json:"series_name,omitempty" maxLength:"500" nullable:"true"`
	Season     *int     `json:"season,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Episode    *int     `json:"episode,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Year       *int     `json:"year,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	RuntimeS   *int     `json:"runtime_s,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	TMDB       *string  `json:"tmdb,omitempty" maxLength:"500" nullable:"true"`
	IMDb       *string  `json:"imdb,omitempty" maxLength:"500" nullable:"true"`
	TVDB       *string  `json:"tvdb,omitempty" maxLength:"500" nullable:"true"`
	SeriesTMDB *string  `json:"series_tmdb,omitempty" maxLength:"500" nullable:"true"`
	SeriesIMDb *string  `json:"series_imdb,omitempty" maxLength:"500" nullable:"true"`
	SeriesTVDB *string  `json:"series_tvdb,omitempty" maxLength:"500" nullable:"true"`
}

// JellyfinPluginEvent is one playback or played-state event.
type JellyfinPluginEvent struct {
	_                  struct{}           `additionalProperties:"true"`
	Event              string             `json:"event" enum:"start,progress,pause,stop,played,unplayed"`
	UserID             string             `json:"user_id" pattern:"^[0-9a-fA-F-]{32,36}$" doc:"the Jellyfin user"`
	Item               JellyfinPluginItem `json:"item"`
	PositionS          *int               `json:"position_s,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	PlayedToCompletion bool               `json:"played_to_completion,omitempty" default:"false"`
	LastPlayed         *string            `json:"last_played,omitempty" maxLength:"40" nullable:"true"`
	Client             *string            `json:"client,omitempty" maxLength:"500" nullable:"true" doc:"the app, e.g. Kodi or Jellyfin Web"`
	Device             *string            `json:"device,omitempty" maxLength:"500" nullable:"true"`
}

// ---- Playing a title and "Finished it?" (rw/devices) ----

// SubtitleTrack is one subtitle track of the file.
type SubtitleTrack struct {
	Index    int     `json:"index"`
	Label    string  `json:"label"`
	Language *string `json:"language" required:"false" nullable:"true"`
	External bool    `json:"external"`
	Text     bool    `json:"text"`
	Default  bool    `json:"default"`
}

// PlayInfo is what the Play sheet shows.
type PlayInfo struct {
	Available        bool            `json:"available"`
	InJellyfin       bool            `json:"in_jellyfin" required:"false" doc:"Jellyfin has it: the outside players can play it"`
	InLibrary        bool            `json:"in_library" required:"false" doc:"Movie Night's own library has it: Play here plays that copy"`
	Linked           bool            `json:"linked"`
	JellyfinItemID   *string         `json:"jellyfin_item_id" required:"false" nullable:"true"`
	WebURL           *string         `json:"web_url" required:"false" nullable:"true"`
	Title            *string         `json:"title" required:"false" nullable:"true"`
	Season           *int            `json:"season" required:"false" nullable:"true"`
	Episode          *int            `json:"episode" required:"false" nullable:"true"`
	Subtitles        []SubtitleTrack `json:"subtitles" required:"false" nullable:"false" default:"[]"`
	ChosenSubtitle   *int            `json:"chosen_subtitle" required:"false" nullable:"true"`
	ResumeSeconds    int             `json:"resume_seconds" required:"false" default:"0"`
	Players          []Player        `json:"players" nullable:"false"`
	DefaultPlayer    string          `json:"default_player"`
	SubtitleLanguage *string         `json:"subtitle_language" required:"false" nullable:"true"`
}

// PlayIn starts a Play.
type PlayIn struct {
	_        struct{} `additionalProperties:"true"`
	Player   string   `json:"player" pattern:"^[a-z0-9_]{1,40}$"`
	ReturnTo string   `json:"return_to,omitempty" enum:"web,app" default:"web"`
	Season   *int     `json:"season,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Episode  *int     `json:"episode,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Subtitle *int     `json:"subtitle,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
}

// PlayOut is the player's link.
type PlayOut struct {
	SessionID string  `json:"session_id"`
	URL       string  `json:"url"`
	WebURL    *string `json:"web_url" required:"false" nullable:"true"`
}

// PlaySessionOut is one Play.
type PlaySessionOut struct {
	ID        string  `json:"id"`
	MediaType string  `json:"media_type"`
	TMDBID    *int32  `json:"tmdb_id" required:"false" nullable:"true"`
	Season    *int32  `json:"season" required:"false" nullable:"true"`
	Episode   *int32  `json:"episode" required:"false" nullable:"true"`
	Title     string  `json:"title"`
	Player    string  `json:"player"`
	StartedAt string  `json:"started_at"`
	Key       *string `json:"key" required:"false" nullable:"true"`
	Poster    *string `json:"poster" required:"false" nullable:"true"`
}

// OpenSessionOut is the Play to ask about now, if any.
type OpenSessionOut struct {
	Session *PlaySessionOut `json:"session"`
}

// Schema says "a PlaySessionOut, or null" (FastAPI's anyOf, which the phone
// app's types expect): Huma's tags cannot mark a struct field nullable.
// Registered under its own name, so the response still points at
// "OpenSessionOut" as the Python's did.
func (OpenSessionOut) Schema(r huma.Registry) *huma.Schema {
	ref := r.Schema(reflect.TypeOf(PlaySessionOut{}), true, "PlaySessionOut")
	r.Map()["OpenSessionOut"] = &huma.Schema{Type: huma.TypeObject, Title: "OpenSessionOut",
		Properties: map[string]*huma.Schema{"session": {AnyOf: []*huma.Schema{ref, {Type: "null"}}}}}
	return &huma.Schema{Ref: "#/components/schemas/OpenSessionOut"}
}

// BackOut says whether to ask "Finished it?" now.
type BackOut struct {
	Ask     bool           `json:"ask"`
	Session PlaySessionOut `json:"session"`
}

// FinishIn is the answer to "Finished it?".
type FinishIn struct {
	_       struct{} `additionalProperties:"true"`
	Outcome string   `json:"outcome" enum:"watched,not_finished,dismissed"`
}

// ---- Movie Night's own player: "Play here" (rw/devices) ----

// WatchCaps is what the browser can decode, as it measured it: the file
// itself in a <video> element, and HLS through hls.js or Safari.
type WatchCaps struct {
	DirectContainers []string `json:"direct_containers" enum:"mp4,webm" maxItems:"4" doc:"containers the video element plays as a file"`
	DirectVideo      []string `json:"direct_video" enum:"h264,hevc,av1,vp9,vp8" maxItems:"8"`
	DirectAudio      []string `json:"direct_audio" enum:"aac,mp3,opus,flac,ac3,eac3,vorbis,alac" maxItems:"10"`
	HLSVideo         []string `json:"hls_video" enum:"h264,hevc,av1,vp9,vp8" maxItems:"8"`
	HLSAudio         []string `json:"hls_audio" enum:"aac,mp3,opus,flac,ac3,eac3,vorbis,alac" maxItems:"10"`
	AudioChannels    int      `json:"audio_channels,omitempty" required:"false" minimum:"0" maximum:"32" doc:"the audio output's channels (AudioContext maxChannelCount); 6 or more keeps surround sound in a library transcode"`
}

// WatchIn starts a play in the browser, or restarts one with another audio
// track or a burnt-in picture subtitle (replace: the play's ticket).
type WatchIn struct {
	_            struct{}  `additionalProperties:"true"`
	Season       *int      `json:"season,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Episode      *int      `json:"episode,omitempty" minimum:"0" maximum:"2147483647" nullable:"true"`
	Caps         WatchCaps `json:"caps"`
	Audio        *int      `json:"audio,omitempty" minimum:"0" maximum:"100000" nullable:"true" doc:"audio track index; the file's default when left out"`
	BurnSubtitle *int      `json:"burn_subtitle,omitempty" minimum:"0" maximum:"100000" nullable:"true" doc:"a picture subtitle (PGS, VobSub) to burn into the video"`
	Replace      *string   `json:"replace,omitempty" maxLength:"2000" nullable:"true" doc:"the ticket of the play this continues"`
}

// WatchTrack is one audio or subtitle track.
type WatchTrack struct {
	Index    int     `json:"index"`
	Label    string  `json:"label"`
	Language *string `json:"language" required:"false" nullable:"true"`
	Codec    string  `json:"codec"`
	Kind     string  `json:"kind,omitempty" required:"false" doc:"subtitles: text, ass or image"`
	Default  bool    `json:"default"`
	Forced   bool    `json:"forced,omitempty" required:"false"`
	External bool    `json:"external,omitempty" required:"false"`
	Channels int     `json:"channels,omitempty" required:"false"`
	URL      *string `json:"url,omitempty" required:"false" nullable:"true" doc:"subtitles: where the player reads a text or ASS track"`
}

// WatchChapter is one chapter.
type WatchChapter struct {
	Start float64 `json:"start_s"`
	Name  string  `json:"name"`
}

// WatchSegment is a marked stretch (Jellyfin's media segments).
type WatchSegment struct {
	Kind  string  `json:"kind" enum:"intro,credits,recap,preview,commercial"`
	Start float64 `json:"start_s"`
	End   float64 `json:"end_s"`
}

// WatchNext is the episode after this one.
type WatchNext struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Title   string `json:"title"`
}

// WatchOut is everything the player needs.
type WatchOut struct {
	Ticket     string         `json:"ticket" doc:"the stream ticket: in every stream address and every report"`
	PlayID     string         `json:"play_id"`
	Mode       string         `json:"mode" enum:"direct,hls"`
	URL        string         `json:"url"`
	DurationS  float64        `json:"duration_s"`
	ResumeS    float64        `json:"resume_s" doc:"where the person left off; 0 from the start"`
	Title      string         `json:"title"`
	MediaType  string         `json:"media_type"`
	TMDBID     int            `json:"tmdb_id"`
	Season     *int           `json:"season" required:"false" nullable:"true"`
	Episode    *int           `json:"episode" required:"false" nullable:"true"`
	Key        string         `json:"key"`
	ItemID     string         `json:"jellyfin_item_id"`
	Backdrop   string         `json:"backdrop"`
	Audio      []WatchTrack   `json:"audio" nullable:"false"`
	AudioIndex *int           `json:"audio_index" required:"false" nullable:"true"`
	Subtitles  []WatchTrack   `json:"subtitles" nullable:"false"`
	Subtitle   *int           `json:"subtitle_index" required:"false" nullable:"true" doc:"the subtitle to show at the start"`
	BurntIndex *int           `json:"burnt_subtitle" required:"false" nullable:"true"`
	Chapters   []WatchChapter `json:"chapters" nullable:"false"`
	Segments   []WatchSegment `json:"segments" nullable:"false"`
	Next       *WatchNext     `json:"next,omitempty" required:"false" doc:"the episode after this one; left out at the end"`
	Fonts      []string       `json:"fonts" nullable:"false" doc:"fonts embedded in the file, for styled subtitles"`
	Source     string         `json:"source,omitempty" required:"false" enum:"library,jellyfin" doc:"where the file comes from: Movie Night's own library or Jellyfin"`
}

// WatchReportIn is the player saying where it is.
type WatchReportIn struct {
	_          struct{} `additionalProperties:"true"`
	Ticket     string   `json:"ticket" minLength:"10" maxLength:"2000"`
	Event      string   `json:"event" enum:"start,progress,pause,stop"`
	PositionS  float64  `json:"position_s" minimum:"0" maximum:"1000000"`
	DurationS  float64  `json:"duration_s,omitempty" minimum:"0" maximum:"1000000" doc:"the player's own length, used when the file's is unknown"`
	Paused     bool     `json:"paused,omitempty"`
	Audio      *int     `json:"audio,omitempty" minimum:"0" maximum:"100000" nullable:"true"`
	Subtitle   *int     `json:"subtitle,omitempty" minimum:"0" maximum:"100000" nullable:"true"`
	Transcoded bool     `json:"transcoded,omitempty" doc:"playing Jellyfin's HLS rather than the file"`
}

// WatchReportOut says whether the stop recorded the watch.
type WatchReportOut struct {
	Ok      bool `json:"ok" default:"true" required:"false"`
	Watched bool `json:"watched"`
}
