package schema

// ListSummary is one list on the Lists tab.
type ListSummary struct {
	ID          int32    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description" required:"false"`
	Kind        string   `json:"kind"`
	Source      string   `json:"source"`
	Origin      *string  `json:"origin" required:"false" nullable:"true"`
	Group       string   `json:"group" enum:"mine,shared,mdblist"`
	By          string   `json:"by"`
	Likes       *int32   `json:"likes" required:"false" nullable:"true"`
	Count       *int     `json:"count" required:"false" nullable:"true"`
	Movies      int      `json:"movies" required:"false" default:"0"`
	Shows       int      `json:"shows" required:"false" default:"0"`
	Posters     []string `json:"posters" required:"false" default:"[]"`
	Pinned      bool     `json:"pinned" required:"false" default:"false"`
	Starred     bool     `json:"starred" required:"false" default:"false"`
	Editable    bool     `json:"editable" required:"false" default:"false"`
	Deletable   bool     `json:"deletable" required:"false" default:"false"`
}

// PersonalCollection is the watchlist, favourites or ratings tile.
type PersonalCollection struct {
	Count   int      `json:"count"`
	Posters []string `json:"posters"`
}

// ListsOut is the Lists tab.
type ListsOut struct {
	Watchlist PersonalCollection `json:"watchlist"`
	Favorites PersonalCollection `json:"favorites"`
	Ratings   PersonalCollection `json:"ratings"`
	Lists     []ListSummary      `json:"lists"`
}

// ListIn is a new list (NAME_MAX_CHARS, TEXT_MAX_CHARS).
type ListIn struct {
	_           struct{} `additionalProperties:"true"`
	Name        string   `json:"name" minLength:"1" maxLength:"300"`
	Description *string  `json:"description,omitempty" required:"false" nullable:"true" maxLength:"5000"`
}

// SmartFilters are a smart list's (and Discover's) TMDB filters.
type SmartFilters struct {
	_          struct{}  `additionalProperties:"true"`
	Media      MediaType `json:"media,omitempty" enum:"movie,tv" default:"movie" required:"false"`
	Genre      *string   `json:"genre,omitempty" required:"false" nullable:"true" pattern:"^\\d{1,6}$"`
	Provider   *string   `json:"provider,omitempty" required:"false" nullable:"true" pattern:"^\\d{1,6}$"`
	YearFrom   *int      `json:"year_from,omitempty" required:"false" nullable:"true" minimum:"1870" maximum:"2100"`
	YearTo     *int      `json:"year_to,omitempty" required:"false" nullable:"true" minimum:"1870" maximum:"2100"`
	MinRating  *float64  `json:"min_rating,omitempty" required:"false" nullable:"true" minimum:"0" maximum:"10"`
	RuntimeMin *int      `json:"runtime_min,omitempty" required:"false" nullable:"true" minimum:"0" maximum:"1000"`
	RuntimeMax *int      `json:"runtime_max,omitempty" required:"false" nullable:"true" minimum:"0" maximum:"1000"`
	Language   *string   `json:"language,omitempty" required:"false" nullable:"true" pattern:"^[a-zA-Z]{2,3}$"`
	Sort       *string   `json:"sort,omitempty" required:"false" nullable:"true" maxLength:"300"`
}

// SmartIn is a new smart list.
type SmartIn struct {
	_       struct{}     `additionalProperties:"true"`
	Name    string       `json:"name" minLength:"1" maxLength:"300"`
	Filters SmartFilters `json:"filters"`
}

// ListPatch renames a list (own lists), shows it on Home, or stars it.
type ListPatch struct {
	_           struct{} `additionalProperties:"true"`
	Name        *string  `json:"name,omitempty" required:"false" nullable:"true" maxLength:"300"`
	Description *string  `json:"description,omitempty" required:"false" nullable:"true" maxLength:"5000"`
	Pinned      *bool    `json:"pinned,omitempty" required:"false" nullable:"true"`
	Starred     *bool    `json:"starred,omitempty" required:"false" nullable:"true"`
}

// CreatedOut is a new list's id.
type CreatedOut struct {
	Ok bool  `json:"ok" default:"true" required:"false"`
	ID int32 `json:"id"`
}

// ListFlagsOut is a list's Home pin and star after a change.
type ListFlagsOut struct {
	Ok      bool `json:"ok" default:"true" required:"false"`
	Pinned  bool `json:"pinned"`
	Starred bool `json:"starred"`
}

// ListItemIn is a title to add to a list.
type ListItemIn struct {
	_         struct{}  `additionalProperties:"true"`
	MediaType MediaType `json:"media_type" enum:"movie,tv"`
	TMDBID    int       `json:"tmdb_id" exclusiveMinimum:"0"`
}

// AddedOut says whether the title was added (false: it was there).
type AddedOut struct {
	Ok    bool `json:"ok" default:"true" required:"false"`
	Added bool `json:"added"`
}

// ListOut is one list's page.
type ListOut struct {
	ID          int32          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description" required:"false"`
	Kind        string         `json:"kind"`
	Source      string         `json:"source"`
	Origin      *string        `json:"origin" required:"false" nullable:"true"`
	Group       string         `json:"group" enum:"mine,shared,mdblist"`
	By          string         `json:"by"`
	OriginURL   *string        `json:"origin_url" required:"false" nullable:"true"`
	Likes       *int32         `json:"likes" required:"false" nullable:"true"`
	Count       *int           `json:"count" required:"false" nullable:"true"`
	Movies      int            `json:"movies" required:"false" default:"0"`
	Shows       int            `json:"shows" required:"false" default:"0"`
	NotShown    int            `json:"not_shown" required:"false" default:"0"`
	Filters     map[string]any `json:"filters" required:"false" nullable:"true"`
	Pinned      bool           `json:"pinned"`
	Starred     bool           `json:"starred"`
	Editable    bool           `json:"editable"`
	Deletable   bool           `json:"deletable"`
	SyncedAt    *string        `json:"synced_at" required:"false" nullable:"true"`
	Media       *MediaType     `json:"media" required:"false" nullable:"true" enum:"movie,tv"`
	Sort        string         `json:"sort"`
	Items       []Card         `json:"items"`
	More        bool           `json:"more"`
	Page        int            `json:"page"`
	Note        *string        `json:"note" required:"false" nullable:"true"`
}

// CountOut is how many titles a sync left in a list.
type CountOut struct {
	Ok    bool `json:"ok" default:"true" required:"false"`
	Count int  `json:"count"`
}

// CollectionOut is the watchlist, favourites or ratings as a grid.
type CollectionOut struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Items []Card `json:"items"`
	More  bool   `json:"more"`
	Page  int    `json:"page"`
}

// ListMembership is one of the person's own lists and whether a title is in it.
type ListMembership struct {
	ID     int32  `json:"id"`
	Name   string `json:"name"`
	InList bool   `json:"in_list"`
	Source string `json:"source"`
}

// MDBListSummary is an MDBList list as the explorer shows it.
type MDBListSummary struct {
	MDBListID   *int       `json:"mdblist_id" required:"false" nullable:"true"`
	Name        string     `json:"name"`
	Slug        *string    `json:"slug" required:"false" nullable:"true"`
	User        string     `json:"user" required:"false"`
	Description string     `json:"description" required:"false"`
	Media       *MediaType `json:"media" required:"false" nullable:"true" enum:"movie,tv"`
	ItemCount   *int       `json:"item_count" required:"false" nullable:"true"`
	Likes       *int       `json:"likes" required:"false" nullable:"true"`
	Dynamic     bool       `json:"dynamic" required:"false" default:"false"`
	FollowedID  *int32     `json:"followed_id" required:"false" nullable:"true"`
}

// MDBListExploreOut is the explorer's answer: top lists, a search, or a link.
type MDBListExploreOut struct {
	Mode    string           `json:"mode" enum:"browse,search,link"`
	Results []MDBListSummary `json:"results" required:"false" default:"[]"`
	Error   *string          `json:"error" required:"false" nullable:"true"`
	Enabled bool             `json:"enabled" default:"true" required:"false"`
}

// MDBListPreviewOut is a list before following it.
type MDBListPreviewOut struct {
	List     MDBListSummary `json:"list"`
	Count    int            `json:"count"`
	Items    []Card         `json:"items"`
	NotShown int            `json:"not_shown" required:"false" default:"0"`
}

// FollowIn is an MDBList list id or link (NAME_MAX_CHARS).
type FollowIn struct {
	_   struct{} `additionalProperties:"true"`
	Ref string   `json:"ref" minLength:"1" maxLength:"300" doc:"MDBList list id or mdblist.com/lists/<user>/<slug> link"`
}

// FollowOut is the followed list and its size.
type FollowOut struct {
	Ok    bool  `json:"ok" default:"true" required:"false"`
	ID    int32 `json:"id"`
	Count int   `json:"count"`
}
