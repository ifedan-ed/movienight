package digest

// The digest's data, as it is stored in digests.data_json ({"data": ...,
// "words": ..., "text": ...}). Field names are the Python's dict keys, so a
// digest the Python wrote reads here (IsEmpty, the catch-up) and the other
// way round.

// Title is one film, show or suggestion as the email shows it.
type Title struct {
	Key       *string  `json:"key"`
	MediaType string   `json:"media_type,omitempty"`
	TMDBID    int      `json:"tmdb_id,omitempty"`
	Title     string   `json:"title"`
	Year      *int     `json:"year"`
	Poster    *string  `json:"poster"`
	Genres    []string `json:"genres"`
	Runtime   *int     `json:"runtime"`
	Overview  string   `json:"overview,omitempty"`
}

// Film is a film watched in the period.
type Film struct {
	Title
	WatchedOn string  `json:"watched_on"`
	Rating    *int32  `json:"rating"`
	Review    *string `json:"review"`
}

// Show is a show watched in the period: its episodes and their span.
type Show struct {
	Title
	Episodes int    `json:"episodes"`
	Span     string `json:"span"`
}

// Rated is one rating given in the period.
type Rated struct {
	Title  string `json:"title"`
	Rating int32  `json:"rating"`
}

// Coming is a watchlist title out soon.
type Coming struct {
	Title
	Date string `json:"date"`
	iso  string // for sorting by date (the Python sorted "Nov 3" as text)
}

// Pick is a suggestion for "Up next".
type Pick struct {
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	Year      *int    `json:"year"`
	Poster    *string `json:"poster"`
	InLibrary bool    `json:"in_library"`
	Overview  string  `json:"overview"`
}

// Totals are the period's numbers.
type Totals struct {
	Films    int `json:"films"`
	Episodes int `json:"episodes"`
	Hours    int `json:"hours"`
}

// More says how many a list cut short left out.
type More struct {
	Shows   int `json:"shows"`
	Library int `json:"library"`
	Coming  int `json:"coming"`
}

// Month is one month of the year in review.
type Month struct {
	Films    int `json:"films"`
	Episodes int `json:"episodes"`
}

// Year sums a year up.
type Year struct {
	Months    []Month  `json:"months"`
	Busiest   *string  `json:"busiest"`
	BestFilms []Film   `json:"best_films"`
	TopShows  []Show   `json:"top_shows"`
	FirstFilm *Film    `json:"first_film"`
	LastFilm  *Film    `json:"last_film"`
	Genres    []string `json:"genres"`
	Rated     int      `json:"rated"`
	Loved     int      `json:"loved"`
}

// Data is everything one digest is made of.
type Data struct {
	Kind         string   `json:"kind"`
	Label        string   `json:"label"`
	Name         string   `json:"name"`
	Films        []Film   `json:"films"`
	Shows        []Show   `json:"shows"`
	Totals       Totals   `json:"totals"`
	Rated        []Rated  `json:"rated"`
	RatedTotal   int      `json:"rated_total"`
	NewFavorites int      `json:"new_favorites"`
	Library      []Title  `json:"library"`
	Coming       []Coming `json:"coming"`
	More         More     `json:"more"`
	TopGenres    []string `json:"top_genres"`
	Best         *Film    `json:"best"`
	Picks        []Pick   `json:"picks"`
	Year         *Year    `json:"year"`
}

// Words are the email's own sentences: from the model, or plain.
type Words struct {
	Subject   string            `json:"subject"`
	Intro     string            `json:"intro"`
	FilmNotes map[string]string `json:"film_notes"`
	PicksLine string            `json:"picks_line"`
}

// stored is digests.data_json.
type stored struct {
	Data  Data   `json:"data"`
	Words Words  `json:"words"`
	Text  string `json:"text"`
}

// IsEmpty: nothing watched, rated or favourited in the period, and (for a
// week or a month) nothing new in the library. The schedule doesn't send
// such a digest (picks and "coming from your watchlist" alone don't make
// one worth an email). The year in review has no "New in the library"
// section, so for a year only the person's own watching, rating and
// favourites count.
func IsEmpty(d Data) bool {
	own := d.Totals.Films > 0 || d.Totals.Episodes > 0 || d.RatedTotal > 0 || len(d.Rated) > 0 || d.NewFavorites > 0
	if d.Kind == "year" {
		return !own
	}
	return !(own || len(d.Library) > 0 || d.More.Library > 0)
}
