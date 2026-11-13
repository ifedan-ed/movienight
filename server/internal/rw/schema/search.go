package schema

// PersonHit is one person TMDB's search found.
type PersonHit struct {
	ID       *int    `json:"id" required:"false" nullable:"true"`
	Name     *string `json:"name" required:"false" nullable:"true"`
	Photo    *string `json:"photo" required:"false" nullable:"true"`
	KnownFor *string `json:"known_for" required:"false" nullable:"true"`
}

// SearchMore says which sections of a search have more matches on the
// next page.
type SearchMore struct {
	Library bool `json:"library" required:"false" default:"false"`
	TMDB    bool `json:"tmdb" required:"false" default:"false"`
	People  bool `json:"people" required:"false" default:"false"`
}

// SearchOut is one page of a search: the library, everything else, and
// people, each best match first.
type SearchOut struct {
	Library []Card      `json:"library"`
	TMDB    []Card      `json:"tmdb"`
	People  []PersonHit `json:"people"`
	// Which section holds the best match, so it can be shown first.
	First string `json:"first" required:"false" default:"library" enum:"library,tmdb"`
	// The search as it was run after fixing typos, when that changed it.
	Corrected *string `json:"corrected" required:"false" nullable:"true"`
	// It reads like a described plot and the AI is on: ask /search/ai too.
	AI bool `json:"ai" required:"false" default:"false"`
	// The page asked for (each holds the matches before it too), and which
	// sections have more: the apps offer "Show more" (page + 1).
	Page int        `json:"page" required:"false" default:"1"`
	More SearchMore `json:"more" required:"false"`
	// Why a section stopped (Meilisearch's most hits, or the last page),
	// in plain words.
	Note *string `json:"note" required:"false" nullable:"true"`
}

// AiGuess is one title the AI named for a described plot.
type AiGuess struct {
	Card Card    `json:"card"`
	Why  *string `json:"why" required:"false" nullable:"true"`
}

// AiGuessOut is the AI's best guesses, or why there are none.
type AiGuessOut struct {
	Enabled bool      `json:"enabled"`
	Matches []AiGuess `json:"matches"`
	Note    *string   `json:"note" required:"false" nullable:"true"`
}

// PersonOut is a person's page: who they are and their films and shows,
// the most rated first.
type PersonOut struct {
	ID           *int    `json:"id" required:"false" nullable:"true"`
	Name         *string `json:"name" required:"false" nullable:"true"`
	Photo        *string `json:"photo" required:"false" nullable:"true"`
	Biography    *string `json:"biography" required:"false" nullable:"true"`
	Birthday     *string `json:"birthday" required:"false" nullable:"true"`
	PlaceOfBirth *string `json:"place_of_birth" required:"false" nullable:"true"`
	KnownFor     *string `json:"known_for" required:"false" nullable:"true"`
	Items        []Card  `json:"items"`
}
