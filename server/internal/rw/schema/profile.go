package schema

// ---- Players (GET/PUT /me/players; the Play sheet reads the same) ----

// Player is one player the Play sheet can offer.
type Player struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Builtin  bool    `json:"builtin"`
	Template *string `json:"template" required:"false" nullable:"true"`
	Enabled  bool    `json:"enabled" default:"true" required:"false"`
}

// PlayerSettingsOut is the person's players and choices.
type PlayerSettingsOut struct {
	Players          []Player `json:"players"`
	DefaultPlayer    string   `json:"default_player"`
	SubtitleLanguage *string  `json:"subtitle_language" required:"false" nullable:"true"`
	AskFinished      bool     `json:"ask_finished" default:"true" required:"false"`
}

// CustomPlayerIn is a player of one's own as typed.
type CustomPlayerIn struct {
	_        struct{} `additionalProperties:"true"`
	Name     string   `json:"name" minLength:"1" maxLength:"300"`
	Template string   `json:"template" minLength:"4" maxLength:"5000"`
}

// PlayerSettingsIn saves the choices. Up to 20 players of one's own and 30
// ticked: limits the owner keeps (docs/limits.md), refused with 422 in
// plain words.
type PlayerSettingsIn struct {
	_                struct{}         `additionalProperties:"true"`
	Custom           []CustomPlayerIn `json:"custom" required:"false" maxItems:"20" default:"[]"`
	DefaultPlayer    *string          `json:"default_player" required:"false" nullable:"true" pattern:"^[a-z0-9_]{1,40}$"`
	Enabled          []string         `json:"enabled" required:"false" nullable:"true" maxItems:"30"`
	SubtitleLanguage *string          `json:"subtitle_language" required:"false" nullable:"true" maxLength:"3"`
	AskFinished      *bool            `json:"ask_finished" required:"false" nullable:"true"`
}

// ---- The Jellyfin account ----

// JellyfinLink is the person's Jellyfin connection.
type JellyfinLink struct {
	Linked       bool    `json:"linked"`
	CanPlay      bool    `json:"can_play" required:"false" default:"false"`
	UserName     *string `json:"user_name" required:"false" nullable:"true"`
	LinkedAt     *string `json:"linked_at" required:"false" nullable:"true"`
	Server       string  `json:"server"`
	QuickConnect *bool   `json:"quick_connect" required:"false" nullable:"true"`
	// A plain sentence when the person's plays couldn't be read from
	// Jellyfin the last time, or Jellyfin lists plays without a date.
	PlaysNotice *string `json:"plays_notice" required:"false" nullable:"true"`
}

// QuickConnectOut is the code to type into a signed-in Jellyfin app.
type QuickConnectOut struct {
	Code   string `json:"code"`
	Secret string `json:"secret"`
}

// QuickConnectIn is the secret to check.
type QuickConnectIn struct {
	_      struct{} `additionalProperties:"true"`
	Secret string   `json:"secret" minLength:"8" maxLength:"200"`
}

// QuickConnectStateOut says whether the account is connected now.
type QuickConnectStateOut struct {
	Ok       bool    `json:"ok" default:"true" required:"false"`
	Linked   bool    `json:"linked"`
	UserName *string `json:"user_name" required:"false" nullable:"true"`
}

// PasswordLinkIn is a Jellyfin sign-in, typed once (the password is never kept).
type PasswordLinkIn struct {
	_        struct{} `additionalProperties:"true"`
	Username string   `json:"username" minLength:"1" maxLength:"300"`
	Password string   `json:"password" maxLength:"5000"`
}

// ---- Scrobble token ----

// TokenOut is a new token, shown once.
type TokenOut struct {
	Ok    bool   `json:"ok" default:"true" required:"false"`
	Token string `json:"token"`
}

// ---- /me ----

// Features says what this install has switched on.
type Features struct {
	Requests bool `json:"requests"`
	MDBList  bool `json:"mdblist"`
	AI       bool `json:"ai"`
	Email    bool `json:"email"`
	DryRun   bool `json:"dry_run"`
	// Jellyfin is set up on this install (JELLYFIN_*), and Movie Night's own
	// media libraries are (MEDIA_URL).
	Jellyfin  bool `json:"jellyfin" required:"false"`
	Libraries bool `json:"libraries" required:"false"`
}

// TextLimits are the install's request limits, so the apps count down to
// them instead of cutting text without a word.
type TextLimits struct {
	TextMaxChars     int `json:"text_max_chars" required:"false" default:"5000"`
	SearchMaxChars   int `json:"search_max_chars" required:"false" default:"200"`
	NameMaxChars     int `json:"name_max_chars" required:"false" default:"300"`
	ReportedMaxChars int `json:"reported_max_chars" required:"false" default:"500"`
	BatchMaxItems    int `json:"batch_max_items" required:"false" default:"1000"`
}

// RequestsOut is a page of Seerr requests as cards.
type RequestsOut struct {
	Items    []Card `json:"items"`
	Page     int    `json:"page" default:"1" required:"false"`
	More     bool   `json:"more" required:"false" default:"false"`
	Total    int    `json:"total" required:"false" default:"0"`
	NotShown int    `json:"not_shown" required:"false" default:"0"`
}

// MeOut is who is signed in, with their stats, link and the install's
// features and limits.
type MeOut struct {
	User             string       `json:"user"`
	DisplayName      *string      `json:"display_name" required:"false" nullable:"true"`
	Email            *string      `json:"email" required:"false" nullable:"true"`
	IsAdmin          bool         `json:"is_admin"`
	Stats            Stats        `json:"stats"`
	Jellyfin         JellyfinLink `json:"jellyfin"`
	Features         Features     `json:"features"`
	ScrobbleTokenSet bool         `json:"scrobble_token_set"`
	Limits           TextLimits   `json:"limits" required:"false"`
	// Admins only (empty for everyone else): what needs their attention.
	AdminNotices []string `json:"admin_notices" required:"false" default:"[]"`
	// The zone the person's days follow: the one their browser or phone last
	// reported (PUT /me/time-zone), or the one they picked in Profile; null
	// when neither has happened (their days follow the install's TIMEZONE).
	TimeZone *string `json:"time_zone" required:"false" nullable:"true"`
	// Who sets time_zone: "automatic" (their devices report it; the apps
	// compare it with their own and report theirs when it differs) or
	// "manual" (picked in Profile; the apps report nothing).
	TimeZoneMode string `json:"time_zone_mode" enum:"automatic,manual"`
}

// TimeZoneIn is the zone the person's browser or phone reports: an IANA
// name (Intl.DateTimeFormat().resolvedOptions().timeZone), never a guess
// from their address. 64: users.MaxTimeZoneLength.
type TimeZoneIn struct {
	_        struct{} `additionalProperties:"true"`
	TimeZone string   `json:"time_zone" minLength:"1" maxLength:"64"`
}

// TimeZoneChoiceIn is the person's choice in Profile. "manual": time_zone
// is the zone they picked, kept until they choose again. "automatic": their
// devices set it again; time_zone is the zone of the device they chose this
// on, stored at once.
type TimeZoneChoiceIn struct {
	_        struct{} `additionalProperties:"true"`
	Mode     string   `json:"mode" enum:"automatic,manual"`
	TimeZone string   `json:"time_zone" minLength:"1" maxLength:"64"`
}

// TimeZoneOut is the zone now stored and who sets it (as in /me).
type TimeZoneOut struct {
	TimeZone string `json:"time_zone"`
	Mode     string `json:"mode" enum:"automatic,manual"`
}

// TimeZoneItem is one zone Profile offers: its IANA name and its offset
// from UTC now ("UTC+05:30"), which changes with daylight saving.
type TimeZoneItem struct {
	Name   string `json:"name"`
	Offset string `json:"offset"`
}

// TimeZonesOut is every zone a person can pick, sorted by name.
type TimeZonesOut struct {
	Zones []TimeZoneItem `json:"zones"`
}

// HistoryItem is one merged watch with its card, and for an episode that
// episode's own heart and rating (never the show's).
type HistoryItem struct {
	Watch
	Card            *Card `json:"card" required:"false"`
	EpisodeFavorite bool  `json:"episode_favorite" required:"false" default:"false"`
	EpisodeRating   *int  `json:"episode_rating" required:"false" nullable:"true"`
}

// HistoryOut is one page of the history.
type HistoryOut struct {
	Items []HistoryItem `json:"items"`
	More  bool          `json:"more"`
	Page  int           `json:"page"`
	Total int           `json:"total"`
}

// RatePromptItem is the film the rate prompt asks about.
type RatePromptItem struct {
	Key       string    `json:"key"`
	MediaType MediaType `json:"media_type" enum:"movie,tv"`
	TMDBID    int       `json:"tmdb_id"`
	Title     string    `json:"title"`
	Poster    *string   `json:"poster" required:"false" nullable:"true"`
}

// RatePromptOut is the prompt, or null.
type RatePromptOut struct {
	Item *RatePromptItem `json:"item" required:"false"`
}
