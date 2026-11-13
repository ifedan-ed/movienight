// Package schema holds the request and response types of the read and
// write routes, named and shaped as the Python app named them: the
// type names become the OpenAPI component names the phone app's TypeScript
// types are generated from (docs/openapi.json), and the JSON field names are
// what the website, the phone and the Kodi add-on read.
//
// Conventions (Huma tags):
//   - A field with a default in Pydantic is `required:"false"`; one that may
//     be null is `nullable:"true"`. Nulls are sent, not left out, as FastAPI
//     sent them (no omitempty), so a client reading `x === null` still works.
//   - Request bodies allow extra fields (additionalProperties), as FastAPI
//     did: an older client sending a field this version no longer reads keeps
//     working.
//   - Length and count limits that are settings (TEXT_MAX_CHARS and the
//     rest) are set on the registered schemas at startup from the settings
//     (rw.ApplyLimits), so the description and the validation say the same
//     number; the tags carry the defaults.
//
// One package for all of them because Huma names components by Go type
// name: two route packages each declaring a Card would clash.
package schema

import "movienight/server/internal/api"

// MediaType is "movie" or "tv" (a path parameter and a field).
type MediaType string

// Ok is the plain success answer ({"ok": true}); the one type phase 1's
// routes use too (Huma allows one Go type per component name).
type Ok = api.Ok

// Progress is a download's progress (Radarr/Sonarr's queue, through Seerr).
type Progress struct {
	Percent int     `json:"percent"`
	ETA     *string `json:"eta" required:"false" nullable:"true"`
	Status  *string `json:"status" required:"false" nullable:"true"`
}

// TitleState is a title's state for one person: in the library, its request
// in Seerr, and their own watchlist, watches, rating and favourite.
type TitleState struct {
	InLibrary   bool    `json:"in_library" required:"false" default:"false"`
	JellyfinID  *string `json:"jellyfin_id" required:"false" nullable:"true"`
	JellyfinURL *string `json:"jellyfin_url" required:"false" nullable:"true"`
	Request     *string `json:"request" required:"false" nullable:"true" enum:"pending,processing,partial,available,dry_run"`
	// A struct pointer cannot be marked nullable in Huma (3.1 $ref); it is
	// still sent as null when there is none.
	Progress    *Progress `json:"progress" required:"false"`
	RequestedAt *string   `json:"requested_at" required:"false" nullable:"true"`
	Watchlist   bool      `json:"watchlist" required:"false" default:"false"`
	Watched     int       `json:"watched" required:"false" default:"0"`
	Rating      *int      `json:"rating" required:"false" nullable:"true"`
	Favorite    bool      `json:"favorite" required:"false" default:"false"`
}

// Card is a title as every grid and row shows it: its state (embedded, so
// the fields sit side by side in the JSON, as Pydantic's subclass had them)
// and its metadata.
type Card struct {
	TitleState

	Key       string    `json:"key" example:"movie:603"`
	MediaType MediaType `json:"media_type" enum:"movie,tv"`
	TMDBID    int       `json:"tmdb_id"`
	Title     string    `json:"title"`
	Year      *int      `json:"year" required:"false" nullable:"true"`
	Poster    *string   `json:"poster" required:"false" nullable:"true"`
	Backdrop  *string   `json:"backdrop" required:"false" nullable:"true"`
	Vote      *float64  `json:"vote" required:"false" nullable:"true"`
	// Extras on some endpoints.
	Overview      *string  `json:"overview" required:"false" nullable:"true"`
	Genres        []string `json:"genres" required:"false" nullable:"true"`
	HeroLabel     *string  `json:"hero_label" required:"false" nullable:"true"`
	RequestStatus *string  `json:"request_status" required:"false" nullable:"true"`
	MediaStatus   *string  `json:"media_status" required:"false" nullable:"true"`
	RequestedBy   *string  `json:"requested_by" required:"false" nullable:"true"`
}

// CardsOut is a plain list of cards.
type CardsOut struct {
	Items []Card `json:"items"`
}

// PageOut is one page of a grid.
type PageOut struct {
	Title *string `json:"title" required:"false" nullable:"true"`
	Items []Card  `json:"items"`
	More  bool    `json:"more"`
	Page  int     `json:"page"`
	// Why a grid ends where it does, when that isn't the end of the list
	// (TMDB serves only its first 500 pages): shown under the last cards.
	Note *string `json:"note" required:"false" nullable:"true"`
}

// KeysIn is title keys to fill posters for.
type KeysIn struct {
	_    struct{} `additionalProperties:"true"`
	Keys []string `json:"keys" maxItems:"1000" pattern:"^(movie|tv):\\d{1,10}$"`
}

// FilledTitle is what /titles/fill sends for one key.
type FilledTitle struct {
	Poster   *string  `json:"poster" required:"false" nullable:"true"`
	Backdrop *string  `json:"backdrop" required:"false" nullable:"true"`
	Year     *int     `json:"year" required:"false" nullable:"true"`
	Title    *string  `json:"title" required:"false" nullable:"true"`
	Vote     *float64 `json:"vote" required:"false" nullable:"true"`
}

// FillOut maps each key found to its poster and the rest.
type FillOut struct {
	Titles map[string]FilledTitle `json:"titles"`
}

// KeyIn is one title key.
type KeyIn struct {
	_   struct{} `additionalProperties:"true"`
	Key string   `json:"key" pattern:"^(movie|tv):\\d{1,10}$"`
}

// IdsIn is watch ids to remove.
type IdsIn struct {
	_   struct{} `additionalProperties:"true"`
	IDs []int    `json:"ids" minItems:"1" maxItems:"1000"`
}

// SyncOut is a sync's counts.
type SyncOut struct {
	Ok     bool           `json:"ok" default:"true" required:"false"`
	Counts map[string]int `json:"counts"`
	Note   *string        `json:"note" required:"false" nullable:"true"`
}
