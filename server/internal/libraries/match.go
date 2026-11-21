package libraries

import (
	"context"
	"fmt"
	"strconv"

	"movienight/server/internal/clients"
	"movienight/server/internal/matching"
	"movienight/server/internal/media/naming"
)

// Finder is what matching asks TMDB (clients.TMDB).
type Finder interface {
	SearchMovie(ctx context.Context, query string, year int) ([]clients.JSON, error)
	SearchTV(ctx context.Context, query string, year int) ([]clients.JSON, error)
	FindExternal(ctx context.Context, externalID, source string) (clients.JSON, error)
}

// Match is the film or show a name was matched to.
type Match struct {
	TMDBID int
	By     string // "name" (an id in the name), "search", "hand"
}

// minTitleScore: a search hit counts only when its title is the name's
// (1.0), starts with it (0.9) or holds every word of it (0.85), the same
// scale search uses (matching.TitleMatch).
const minTitleScore = 0.85

// matcher matches names to TMDB titles, each distinct name once per scan
// (a show's episodes, a film's parts all ask the same question).
type matcher struct {
	find Finder
	// hand: matches an admin made in this library, by title and year.
	hand map[string]int
	memo map[string]memoized
}

type memoized struct {
	m       Match
	problem string
	err     error
}

func newMatcher(find Finder, hand map[string]int) *matcher {
	return &matcher{find: find, hand: hand, memo: map[string]memoized{}}
}

func handKey(title string, year int) string { return title + "\x00" + strconv.Itoa(year) }

// match finds the title of p in a library of kind. A name no title fits is
// a problem, said in words for the Libraries page; err is TMDB failing,
// which leaves the file to try again on the next scan.
func (m *matcher) match(ctx context.Context, p naming.Parsed, kind naming.Kind) (Match, string, error) {
	if id, ok := m.hand[handKey(p.Title, p.Year)]; ok {
		return Match{TMDBID: id, By: "hand"}, "", nil
	}
	if p.TMDB > 0 {
		return Match{TMDBID: p.TMDB, By: "name"}, "", nil
	}
	key := fmt.Sprintf("%s|%s|%d|%s|%d", kind, p.Title, p.Year, p.IMDb, p.TVDB)
	if v, ok := m.memo[key]; ok {
		return v.m, v.problem, v.err
	}
	mt, problem, err := m.lookup(ctx, p, kind)
	if err == nil {
		m.memo[key] = memoized{m: mt, problem: problem}
	}
	return mt, problem, err
}

func (m *matcher) lookup(ctx context.Context, p naming.Parsed, kind naming.Kind) (Match, string, error) {
	media := "movie"
	if kind == naming.Shows {
		media = "tv"
	}
	for _, ext := range [][2]string{{p.IMDb, "imdb_id"}, {tvdbString(p.TVDB), "tvdb_id"}} {
		if ext[0] == "" {
			continue
		}
		hit, err := m.find.FindExternal(ctx, ext[0], ext[1])
		if err != nil {
			return Match{}, "", err
		}
		if id, _ := hit["id"].(float64); id > 0 && hit["media_type"] == media {
			return Match{TMDBID: int(id), By: "name"}, "", nil
		}
	}
	if p.Title == "" {
		return Match{}, "The name says nothing about the title", nil
	}
	search := m.find.SearchMovie
	if kind == naming.Shows {
		search = m.find.SearchTV
	}
	hits, err := search(ctx, p.Title, p.Year)
	if err != nil {
		return Match{}, "", err
	}
	if len(hits) == 0 && p.Year > 0 {
		// The year in the name may be off (a release's year, not the film's):
		// the search without it, where the year still has to be close.
		if hits, err = search(ctx, p.Title, 0); err != nil {
			return Match{}, "", err
		}
	}
	if id := best(p, hits); id > 0 {
		return Match{TMDBID: id, By: "search"}, "", nil
	}
	name := p.Title
	if p.Year > 0 {
		name = fmt.Sprintf("%s (%d)", p.Title, p.Year)
	}
	return Match{}, fmt.Sprintf("No match on TMDB for “%s”", name), nil
}

// best is the hit that is the name's title: title words first, then a
// year that fits (exactly, or a year either way, as release dates vary by
// country), then how many people rated it. 0 when none fits.
func best(p naming.Parsed, hits []clients.JSON) int {
	bestID, bestScore := 0, 0.0
	for _, h := range hits {
		id, _ := h["id"].(float64)
		if id <= 0 {
			continue
		}
		title := matching.TitleMatch(p.Title, str(h, "title", "name"), str(h, "original_title", "original_name"))
		if title < minTitleScore {
			continue
		}
		score := title
		if p.Year > 0 {
			y := year(str(h, "release_date", "first_air_date"))
			switch d := y - p.Year; {
			case y == 0:
				score -= 0.2
			case d == 0:
				score += 0.2
			case d == 1 || d == -1:
				score += 0.1
			default:
				continue
			}
		}
		votes, _ := h["vote_count"].(float64)
		score += matching.PopularityBonus(votes)
		if score > bestScore {
			bestID, bestScore = int(id), score
		}
	}
	return bestID
}

func str(h clients.JSON, keys ...string) string {
	for _, k := range keys {
		if v, _ := h[k].(string); v != "" {
			return v
		}
	}
	return ""
}

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}

func tvdbString(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}
