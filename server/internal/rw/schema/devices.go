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
