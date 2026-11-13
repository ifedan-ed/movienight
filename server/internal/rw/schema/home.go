package schema

import (
	"encoding/json"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// UpNextItem is a Jellyfin "continue watching" or "next up" entry.
type UpNextItem struct {
	Key        *string    `json:"key" required:"false" nullable:"true"`
	MediaType  *MediaType `json:"media_type" required:"false" nullable:"true" enum:"movie,tv"`
	TMDBID     *int       `json:"tmdb_id" required:"false" nullable:"true"`
	JellyfinID *string    `json:"jellyfin_id" required:"false" nullable:"true"`
	Title      string     `json:"title" required:"false"`
	Subtitle   *string    `json:"subtitle" required:"false" nullable:"true"`
	Progress   int        `json:"progress" required:"false" default:"0"`
	ImageItem  *string    `json:"image_item" required:"false" nullable:"true"`
	SeriesItem *string    `json:"series_item" required:"false" nullable:"true"`
	RuntimeMin *int       `json:"runtime_min" required:"false" nullable:"true"`
	Season     *int       `json:"season" required:"false" nullable:"true"`
	Episode    *int       `json:"episode" required:"false" nullable:"true"`
	Backdrop   *string    `json:"backdrop" required:"false" nullable:"true"`
	Poster     *string    `json:"poster" required:"false" nullable:"true"`
}

// RowMore is where a row's "See all" goes.
type RowMore struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// PosterRow is a row of title cards.
type PosterRow struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kicker   *string  `json:"kicker" required:"false" nullable:"true"`
	Subtitle *string  `json:"subtitle" required:"false" nullable:"true"`
	Style    string   `json:"style" enum:"poster" default:"poster" required:"false"`
	Items    []Card   `json:"items"`
	More     *RowMore `json:"more" required:"false"`
	ListID   *int32   `json:"list_id" required:"false" nullable:"true"`
	Group    *string  `json:"group" required:"false" nullable:"true" enum:"mine,shared,mdblist"`
}

// LandscapeRow is the Up next row (wide Jellyfin stills).
type LandscapeRow struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Kicker   *string      `json:"kicker" required:"false" nullable:"true"`
	Subtitle *string      `json:"subtitle" required:"false" nullable:"true"`
	Style    string       `json:"style" enum:"landscape" default:"landscape" required:"false"`
	Items    []UpNextItem `json:"items"`
}

// Row is a PosterRow or a LandscapeRow, told apart by "style" (Pydantic's
// discriminated union). Exactly one of the two is set; it is sent as that
// row's own JSON, and described as oneOf the two with a discriminator, as
// FastAPI described it.
type Row struct {
	Poster    *PosterRow
	Landscape *LandscapeRow
}

// MarshalJSON sends the row that is set.
func (r Row) MarshalJSON() ([]byte, error) {
	if r.Landscape != nil {
		return json.Marshal(r.Landscape)
	}
	return json.Marshal(r.Poster)
}

// Schema is Huma's SchemaProvider: oneOf the two rows, by "style".
func (Row) Schema(reg huma.Registry) *huma.Schema {
	poster := reg.Schema(reflect.TypeOf(PosterRow{}), true, "")
	land := reg.Schema(reflect.TypeOf(LandscapeRow{}), true, "")
	return &huma.Schema{
		OneOf: []*huma.Schema{poster, land},
		Discriminator: &huma.Discriminator{PropertyName: "style", Mapping: map[string]string{
			"poster": poster.Ref, "landscape": land.Ref,
		}},
	}
}

// RowsOut is one Home row key's rows (a "because" key gives several).
type RowsOut struct {
	Rows []Row `json:"rows"`
	// When a row key has nothing to show for a reason the person should
	// know ("Picked for you" being made, or TMDB not answering).
	Note *string `json:"note" required:"false" nullable:"true"`
}

// HomeRowSpec is one row of a tab, before it is loaded.
type HomeRowSpec struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Multi bool   `json:"multi"`
	Style string `json:"style" enum:"poster,landscape"`
}

// HomeOut is a tab's enabled rows, in order.
type HomeOut struct {
	Tab  string        `json:"tab"`
	Rows []HomeRowSpec `json:"rows"`
}

// HomeConfigRow is one row on the Home rows screen.
type HomeConfigRow struct {
	Key      string  `json:"key"`
	On       bool    `json:"on"`
	Title    *string `json:"title" required:"false" nullable:"true"`
	List     *bool   `json:"list" required:"false" nullable:"true"`
	Subtitle *string `json:"subtitle" required:"false" nullable:"true"`
	Group    *string `json:"group" required:"false" nullable:"true" enum:"mine,shared,mdblist"`
}

// HomeConfigOut is a tab's rows, every one, on or off.
type HomeConfigOut struct {
	Tab  string          `json:"tab"`
	Rows []HomeConfigRow `json:"rows"`
}

// HomeConfigIn is a tab's rows as the person ordered them (BATCH_MAX_ITEMS).
type HomeConfigIn struct {
	_    struct{}        `additionalProperties:"true"`
	Rows []HomeConfigRow `json:"rows" maxItems:"1000"`
}

// Genre is a TMDB genre.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GenresOut is every genre of a media type, by name.
type GenresOut struct {
	Genres []Genre `json:"genres"`
}

// Provider is a streaming service.
type Provider struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ProvidersOut is the streaming services Home and Discover know.
type ProvidersOut struct {
	Providers []Provider `json:"providers"`
}
