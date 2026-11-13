package schema

// The title page and everything done on it. Lists that Pydantic defaulted to [] are
// always sent as arrays, never null: the handlers fill them (an empty Go
// slice, not nil), so a client's `.length` keeps working.

// CastMember is one person of the cast.
type CastMember struct {
	ID        *int    `json:"id" required:"false" nullable:"true"`
	Name      *string `json:"name" required:"false" nullable:"true"`
	Character *string `json:"character" required:"false" nullable:"true"`
	Photo     *string `json:"photo" required:"false" nullable:"true"`
}

// StreamProvider is one streaming, rental or purchase service.
type StreamProvider struct {
	ID   *int    `json:"id" required:"false" nullable:"true"`
	Name *string `json:"name" required:"false" nullable:"true"`
	Logo *string `json:"logo" required:"false" nullable:"true"`
}

// Providers is where to watch a title in WATCH_REGION.
type Providers struct {
	Stream []StreamProvider `json:"stream" required:"false" default:"[]"`
	Rent   []StreamProvider `json:"rent" required:"false" default:"[]"`
	Buy    []StreamProvider `json:"buy" required:"false" default:"[]"`
	Link   *string          `json:"link" required:"false" nullable:"true"`
}

// Collection is the film series a movie belongs to.
type Collection struct {
	ID       *int    `json:"id" required:"false" nullable:"true"`
	Name     *string `json:"name" required:"false" nullable:"true"`
	Poster   *string `json:"poster" required:"false" nullable:"true"`
	Backdrop *string `json:"backdrop" required:"false" nullable:"true"`
}

// Season is one season on a show's page.
type Season struct {
	Number   int     `json:"number"`
	Name     *string `json:"name" required:"false" nullable:"true"`
	Episodes *int    `json:"episodes" required:"false" nullable:"true"`
	AirDate  *string `json:"air_date" required:"false" nullable:"true"`
	Poster   *string `json:"poster" required:"false" nullable:"true"`
	Status   *string `json:"status" required:"false" nullable:"true"`
	Watched  int     `json:"watched" required:"false" default:"0"`
}

// EpisodeRef names one episode (the last and next to air).
type EpisodeRef struct {
	Season  *int    `json:"season" required:"false" nullable:"true"`
	Episode *int    `json:"episode" required:"false" nullable:"true"`
	Name    *string `json:"name" required:"false" nullable:"true"`
	AirDate *string `json:"air_date" required:"false" nullable:"true"`
}

// Watch is one watch as shown: the history rows of one title (or episode)
// on one day, merged (ids: every row's id, sources: where they came from).
type Watch struct {
	IDs          []int    `json:"ids"`
	MediaType    string   `json:"media_type"`
	Key          *string  `json:"key" required:"false" nullable:"true"`
	Title        string   `json:"title"`
	Season       *int     `json:"season" required:"false" nullable:"true"`
	Episode      *int     `json:"episode" required:"false" nullable:"true"`
	EpisodeTitle *string  `json:"episode_title" required:"false" nullable:"true"`
	WatchedAt    string   `json:"watched_at"`
	Sources      []string `json:"sources"`
}

// Scores are the outside scores (IMDb, Rotten Tomatoes, Metacritic,
// Letterboxd, Trakt, MDBList).
type Scores struct {
	IMDbRating   *float64 `json:"imdb_rating" required:"false" nullable:"true"`
	IMDbVotes    *int     `json:"imdb_votes" required:"false" nullable:"true"`
	RTCritics    *int     `json:"rt_critics" required:"false" nullable:"true"`
	RTAudience   *int     `json:"rt_audience" required:"false" nullable:"true"`
	RTURL        *string  `json:"rt_url" required:"false" nullable:"true"`
	Metacritic   *int     `json:"metacritic" required:"false" nullable:"true"`
	Letterboxd   *float64 `json:"letterboxd" required:"false" nullable:"true"`
	TraktRating  *float64 `json:"trakt_rating" required:"false" nullable:"true"`
	TraktVotes   *int     `json:"trakt_votes" required:"false" nullable:"true"`
	MDBListScore *int     `json:"mdblist_score" required:"false" nullable:"true"`
}

// Empty reports whether no score is set.
func (s *Scores) Empty() bool {
	return s.IMDbRating == nil && s.IMDbVotes == nil && s.RTCritics == nil && s.RTAudience == nil && s.RTURL == nil &&
		s.Metacritic == nil && s.Letterboxd == nil && s.TraktRating == nil && s.TraktVotes == nil && s.MDBListScore == nil
}

// ScoresOut is the scores and, when MDBList couldn't refresh them, why.
type ScoresOut struct {
	Scores *Scores `json:"scores" required:"false"`
	Note   *string `json:"note" required:"false" nullable:"true"`
}

// RatingEntry is one person's rating and comment on a title.
type RatingEntry struct {
	Name    string  `json:"name"`
	Rating  int     `json:"rating"`
	Review  *string `json:"review" required:"false" nullable:"true"`
	RatedAt string  `json:"rated_at"`
	Mine    bool    `json:"mine" required:"false" default:"false"`
}

// Community is everyone's ratings of a title on this install.
type Community struct {
	Average *float64      `json:"average" required:"false" nullable:"true"`
	Count   int           `json:"count" required:"false" default:"0"`
	Mine    *RatingEntry  `json:"mine" required:"false"`
	Others  []RatingEntry `json:"others" required:"false" default:"[]"`
}

// Resume is where the person is in the film, or in the show's latest
// started episode (from the Movie Night plugin for Jellyfin).
type Resume struct {
	Season       *int    `json:"season" required:"false" nullable:"true"`
	Episode      *int    `json:"episode" required:"false" nullable:"true"`
	EpisodeTitle *string `json:"episode_title" required:"false" nullable:"true"`
	PositionS    int     `json:"position_s"`
	DurationS    *int    `json:"duration_s" required:"false" nullable:"true"`
	Percent      *int    `json:"percent" required:"false" nullable:"true"`
	State        string  `json:"state"`
	Client       *string `json:"client" required:"false" nullable:"true"`
	Device       *string `json:"device" required:"false" nullable:"true"`
	UpdatedAt    string  `json:"updated_at"`
}

// TitleDetail is a title's whole page.
type TitleDetail struct {
	Key              string           `json:"key"`
	MediaType        MediaType        `json:"media_type" enum:"movie,tv"`
	TMDBID           int              `json:"tmdb_id"`
	Title            string           `json:"title"`
	OriginalTitle    *string          `json:"original_title" required:"false" nullable:"true"`
	Year             *int             `json:"year" required:"false" nullable:"true"`
	ReleaseDate      *string          `json:"release_date" required:"false" nullable:"true"`
	Poster           *string          `json:"poster" required:"false" nullable:"true"`
	Backdrop         *string          `json:"backdrop" required:"false" nullable:"true"`
	Overview         *string          `json:"overview" required:"false" nullable:"true"`
	Genres           []string         `json:"genres" required:"false" default:"[]"`
	Runtime          *int             `json:"runtime" required:"false" nullable:"true"`
	Vote             *float64         `json:"vote" required:"false" nullable:"true"`
	VoteCount        *int             `json:"vote_count" required:"false" nullable:"true"`
	IMDbID           *string          `json:"imdb_id" required:"false" nullable:"true"`
	Tagline          *string          `json:"tagline" required:"false" nullable:"true"`
	StatusText       *string          `json:"status_text" required:"false" nullable:"true"`
	Ongoing          bool             `json:"ongoing" required:"false" default:"false"`
	Certification    *string          `json:"certification" required:"false" nullable:"true"`
	Directors        []string         `json:"directors" required:"false" default:"[]"`
	DirectorLabel    string           `json:"director_label"`
	Writers          []string         `json:"writers" required:"false" default:"[]"`
	Cast             []CastMember     `json:"cast" required:"false" default:"[]"`
	Studios          []string         `json:"studios" required:"false" default:"[]"`
	Networks         []string         `json:"networks" required:"false" default:"[]"`
	Languages        []string         `json:"languages" required:"false" default:"[]"`
	Trailer          *string          `json:"trailer" required:"false" nullable:"true"`
	Providers        Providers        `json:"providers"`
	Collection       *Collection      `json:"collection" required:"false"`
	Seasons          []Season         `json:"seasons" required:"false" default:"[]"`
	NumberOfSeasons  *int             `json:"number_of_seasons" required:"false" nullable:"true"`
	NumberOfEpisodes *int             `json:"number_of_episodes" required:"false" nullable:"true"`
	LastEpisode      *EpisodeRef      `json:"last_episode" required:"false"`
	NextEpisode      *EpisodeRef      `json:"next_episode" required:"false"`
	IMDbURL          *string          `json:"imdb_url" required:"false" nullable:"true"`
	TMDBURL          string           `json:"tmdb_url"`
	JellyfinURL      *string          `json:"jellyfin_url" required:"false" nullable:"true"`
	State            TitleState       `json:"state"`
	Scores           *Scores          `json:"scores" required:"false"`
	Community        *Community       `json:"community" required:"false"`
	Watches          []Watch          `json:"watches" required:"false" default:"[]"`
	Lists            []ListMembership `json:"lists" required:"false" default:"[]"`
	Resume           *Resume          `json:"resume" required:"false"`
}

// RelatedCollection is the rest of a movie's collection, as cards.
type RelatedCollection struct {
	Name  *string `json:"name" required:"false" nullable:"true"`
	Items []Card  `json:"items"`
}

// RelatedOut is "More like this" and, for movies, the collection.
type RelatedOut struct {
	Recommendations []Card             `json:"recommendations"`
	Collection      *RelatedCollection `json:"collection" required:"false"`
}

// RequestIn is a request's seasons (shows only; none: all).
type RequestIn struct {
	_       struct{} `additionalProperties:"true"`
	Seasons []int    `json:"seasons,omitempty" required:"false" nullable:"true" maxItems:"1000"`
}

// RequestOut is the request's outcome and the title's state after it.
type RequestOut struct {
	Ok      bool       `json:"ok" default:"true" required:"false"`
	Already bool       `json:"already"`
	State   TitleState `json:"state"`
}

// WatchlistOut is the watchlist mark after a change.
type WatchlistOut struct {
	Ok        bool `json:"ok" default:"true" required:"false"`
	Watchlist bool `json:"watchlist"`
}

// FavoriteOut is the favourite (or episode heart) after a change.
type FavoriteOut struct {
	Ok       bool `json:"ok" default:"true" required:"false"`
	Favorite bool `json:"favorite"`
}

// WatchedIn is when a title was watched (ISO date or datetime; none: now).
type WatchedIn struct {
	_         struct{} `additionalProperties:"true"`
	WatchedAt *string  `json:"watched_at,omitempty" required:"false" nullable:"true" maxLength:"40" doc:"ISO date or datetime; default now"`
}

// WatchesOut is a title's watches after a change (id: the new one).
type WatchesOut struct {
	Ok      bool    `json:"ok" default:"true" required:"false"`
	ID      *int    `json:"id" required:"false" nullable:"true"`
	Watches []Watch `json:"watches"`
}

// RatingIn is a rating, 1 to 10, with an optional comment.
type RatingIn struct {
	_      struct{} `additionalProperties:"true"`
	Rating int      `json:"rating" minimum:"1" maximum:"10"`
	Review *string  `json:"review,omitempty" required:"false" nullable:"true" maxLength:"5000"`
}

// RatingOut is the rating after a change.
type RatingOut struct {
	Ok     bool    `json:"ok" default:"true" required:"false"`
	Rating *int    `json:"rating" nullable:"true"`
	Review *string `json:"review" required:"false" nullable:"true"`
}

// EpisodeRatingIn is an episode's rating: a title's scale, stars only.
type EpisodeRatingIn struct {
	_      struct{} `additionalProperties:"true"`
	Rating int      `json:"rating" minimum:"1" maximum:"10"`
}

// EpisodeRatingOut is the episode's rating after a change.
type EpisodeRatingOut struct {
	Ok     bool `json:"ok" default:"true" required:"false"`
	Rating *int `json:"rating" nullable:"true"`
}

// ListsForTitleOut is the person's own lists and whether the title is in each.
type ListsForTitleOut struct {
	Lists []ListMembership `json:"lists"`
}

// Episode is one episode on a season page.
type Episode struct {
	Number        int      `json:"number"`
	Name          string   `json:"name"`
	Overview      *string  `json:"overview" required:"false" nullable:"true"`
	AirDate       *string  `json:"air_date" required:"false" nullable:"true"`
	Aired         bool     `json:"aired"`
	Still         *string  `json:"still" required:"false" nullable:"true"`
	Runtime       *int     `json:"runtime" required:"false" nullable:"true"`
	Vote          *float64 `json:"vote" required:"false" nullable:"true"`
	Watched       int      `json:"watched" required:"false" default:"0"`
	WatchIDs      []int    `json:"watch_ids" required:"false" default:"[]"`
	LastWatchedAt *string  `json:"last_watched_at" required:"false" nullable:"true"`
	InLibrary     bool     `json:"in_library" required:"false" default:"false"`
	JellyfinID    *string  `json:"jellyfin_id" required:"false" nullable:"true"`
	Favorite      bool     `json:"favorite" required:"false" default:"false"` // this person liked this episode (the heart)
	Rating        *int     `json:"rating" required:"false" nullable:"true"`
}

// SeasonOut is a season page.
type SeasonOut struct {
	ShowID   int       `json:"show_id"`
	Number   int       `json:"number"`
	Name     *string   `json:"name" required:"false" nullable:"true"`
	Overview *string   `json:"overview" required:"false" nullable:"true"`
	AirDate  *string   `json:"air_date" required:"false" nullable:"true"`
	Poster   *string   `json:"poster" required:"false" nullable:"true"`
	Episodes []Episode `json:"episodes"`
}

// NextEpisode is the episode to play next, with its own heart and rating.
type NextEpisode struct {
	Season   int    `json:"season"`
	Episode  int    `json:"episode"`
	Label    string `json:"label"`
	Favorite bool   `json:"favorite" required:"false" default:"false"`
	Rating   *int   `json:"rating" required:"false" nullable:"true"`
}

// NextEpisodeOut is the next episode, or none when caught up.
type NextEpisodeOut struct {
	Next *NextEpisode `json:"next" required:"false"`
}

// EpisodeMark is one episode to tick or untick.
type EpisodeMark struct {
	_       struct{} `additionalProperties:"true"`
	Season  int      `json:"season" minimum:"0" maximum:"2147483647"`
	Episode int      `json:"episode" minimum:"0" maximum:"2147483647"`
}

// EpisodesIn ticks (watched true) or unticks episodes.
type EpisodesIn struct {
	_        struct{}      `additionalProperties:"true"`
	Episodes []EpisodeMark `json:"episodes" minItems:"1" maxItems:"1000"`
	// A pointer so "left out" (tick, the default) differs from false.
	Watched   *bool   `json:"watched,omitempty" default:"true" required:"false"`
	WatchedAt *string `json:"watched_at,omitempty" required:"false" nullable:"true" maxLength:"40"`
}

// ChangedOut is how many things changed.
type ChangedOut struct {
	Ok      bool `json:"ok" default:"true" required:"false"`
	Changed int  `json:"changed"`
}

// Stats is a person's watching in numbers (Profile).
type Stats struct {
	Movies     int `json:"movies"`
	MoviePlays int `json:"movie_plays"`
	Episodes   int `json:"episodes"`
	Shows      int `json:"shows"`
	Hours      int `json:"hours"`
	ThisYear   int `json:"this_year"`
	Ratings    int `json:"ratings"`
	Watchlist  int `json:"watchlist"`
}
