package config

import (
	"fmt"
	"strconv"
)

// ReadWrite is the settings the read and write routes (phases 2 and 3:
// home, titles, search, lists, profile) read. They sit in their own struct,
// read by their own function, so the phase that adds them touches
// Config/LoadFrom in two lines only.
type ReadWrite struct {
	// Request-shape limits (docs/limits.md, "Technical limits"): how long a
	// text, and how many items, one call may carry. Longer or more is
	// refused with a 422 and a plain sentence. Technical limits, not usage
	// caps; the apps read them from GET /me (`limits`) and count down to
	// them.
	TextMaxChars   int // TEXT_MAX_CHARS, 5000: a comment, a list description, a player link, a password
	SearchMaxChars int // SEARCH_MAX_CHARS, 200: a search or a described plot
	NameMaxChars   int // NAME_MAX_CHARS, 300: a list name, an MDBList link, a sort, a player name
	BatchMaxItems  int // BATCH_MAX_ITEMS, 1000: watches, episodes, Home rows, seasons, keys in one call

	// Search (docs/limits.md, "Search").
	MeiliSemanticRatio float64 // MEILI_SEMANTIC_RATIO, 0.8: meaning vs keywords in Meilisearch's hybrid search
	SearchMaxPages     int     // SEARCH_MAX_PAGES, 25 (1 to 500): the last page of one search
	// CATALOG_ENABLED (search the "catalog" index too), the AI rows' and
	// the mail settings are read by the worker as well: they are in
	// JobSettings (jobs.go), read once.
}

func (r *reader) readWrite() ReadWrite {
	var rw ReadWrite
	rw.TextMaxChars = r.positive("TEXT_MAX_CHARS", 5000)
	rw.SearchMaxChars = r.positive("SEARCH_MAX_CHARS", 200)
	rw.NameMaxChars = r.positive("NAME_MAX_CHARS", 300)
	rw.BatchMaxItems = r.positive("BATCH_MAX_ITEMS", 1000)

	rw.MeiliSemanticRatio = 0.8
	if v := r.optional("MEILI_SEMANTIC_RATIO"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			r.fail(fmt.Sprintf("MEILI_SEMANTIC_RATIO must be a number from 0 to 1, not %q", v))
		} else {
			rw.MeiliSemanticRatio = f
		}
	}
	rw.SearchMaxPages = r.positive("SEARCH_MAX_PAGES", 25)
	if rw.SearchMaxPages > 500 {
		// TMDB serves 500 pages of one search at most.
		r.fail("SEARCH_MAX_PAGES must be at most 500 (the last page TMDB serves)")
	}
	return rw
}

// ReadWriteDefaults is ReadWrite with every documented default, for
// building the API description without an install (cmd/api -openapi).
func ReadWriteDefaults() ReadWrite {
	r := reader{lookup: func(string) (string, bool) { return "", false }}
	return r.readWrite()
}
