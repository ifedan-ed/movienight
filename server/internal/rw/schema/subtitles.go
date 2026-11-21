package schema

// ---- Online subtitles in the player (rw/subtitles) ----

// SubtitleLanguage is one language "Search online" offers.
type SubtitleLanguage struct {
	Code string `json:"code" doc:"ISO 639-1, or pt-br / zh-tw"`
	Name string `json:"name"`
}

// SubtitleLanguagesOut is the languages, and the person's own.
type SubtitleLanguagesOut struct {
	Enabled   bool               `json:"enabled" doc:"false: no subtitle source is set up, and the player hides Search online"`
	Default   string             `json:"default" doc:"the person's subtitle language (Profile → Players), else en"`
	Languages []SubtitleLanguage `json:"languages"`
}

// SubtitleResult is one subtitle a source offers.
type SubtitleResult struct {
	Source          string  `json:"source" doc:"opensubtitles, subdl, subsource, jimaku, podnapisi or gestdown"`
	SourceLabel     string  `json:"source_label"`
	ID              string  `json:"id" doc:"the source's id; POST it back with source to add the subtitle"`
	Language        string  `json:"language"`
	Release         string  `json:"release" doc:"the release(s) it was made for, one per line"`
	HearingImpaired bool    `json:"hearing_impaired"`
	Machine         bool    `json:"machine_translated"`
	HashMatch       bool    `json:"hash_match" doc:"made for this very file (OpenSubtitles' moviehash)"`
	Downloads       int     `json:"downloads"`
	Rating          float64 `json:"rating"`
	Available       bool    `json:"available" doc:"false while the source's download allowance is used up"`
}

// SubtitleSearchOut is a search's answer, best first.
type SubtitleSearchOut struct {
	Results []SubtitleResult `json:"results"`
	Hidden  int              `json:"hidden" doc:"machine-translated results left out (ask again with machine=true)"`
	Notes   []string         `json:"notes" doc:"sources that failed or are out of downloads, in plain words"`
}

// SubtitleAddIn picks a search result.
type SubtitleAddIn struct {
	_      struct{} `additionalProperties:"true"`
	Ticket string   `json:"ticket" minLength:"1" maxLength:"2000" doc:"the play's stream ticket: which file the subtitle is for"`
	Source string   `json:"source" pattern:"^[a-z0-9]{1,20}$"`
	ID     string   `json:"id" minLength:"1" maxLength:"300"`
}

// OnlineSubtitle is a stored subtitle, a track of one file for everyone.
type OnlineSubtitle struct {
	ID              int64   `json:"id"`
	Label           string  `json:"label" doc:"the language and the source"`
	Language        string  `json:"language"`
	Source          string  `json:"source"`
	Release         string  `json:"release"`
	HearingImpaired bool    `json:"hearing_impaired"`
	Kind            string  `json:"kind" doc:"text (WebVTT) or ass, as the Watch tracks"`
	URL             string  `json:"url"`
	DelayS          float64 `json:"delay_s" doc:"seconds; positive shows the lines later"`
	CanRemove       bool    `json:"can_remove" doc:"the person who added it, or an admin"`
}

// OnlineSubtitlesOut is a file's stored subtitles.
type OnlineSubtitlesOut struct {
	Tracks []OnlineSubtitle `json:"tracks"`
}

// SubtitleDelayIn sets a stored subtitle's delay for every device.
type SubtitleDelayIn struct {
	_      struct{} `additionalProperties:"true"`
	DelayS float64  `json:"delay_s" minimum:"-600" maximum:"600"`
}
