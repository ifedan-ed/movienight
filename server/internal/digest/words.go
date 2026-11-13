package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// aiSystem is the model's brief for the email's words.
const aiSystem = "You write the short text of a personal film-and-TV email for one person, from their own viewing data. " +
	"Write like a friend who knows films well: specific, warm, plain, no hype, no emoji, no exclamation marks, " +
	"no clichés ('cinematic journey', 'binge-worthy'), never invent facts about the person beyond the data. " +
	"Mention real titles from the data. Second person. Return strict JSON only: " +
	`{"subject": "under 70 characters, specific to this period", ` +
	`"intro": "2 to 4 sentences about what they watched and what stood out", ` +
	`"film_notes": {"<exact film title>": "one sentence, at most 22 words, about that film or why it fits their taste"}, ` +
	`"picks_line": "one sentence introducing the suggestions"}`

// aiInput is what the model reads: the period's data, slimmed. Never the
// person's name or address.
func aiInput(d Data) string {
	films := d.Films
	if d.Kind == "year" && d.Year != nil {
		// The year's best, then every other film of the year by name only (a
		// year of films is too many to describe one by one, but none is left
		// out: names alone stay small).
		best := map[string]bool{}
		films = append([]Film(nil), d.Year.BestFilms...)
		for _, f := range d.Year.BestFilms {
			best[f.Title.Title] = true
		}
		for _, f := range d.Films {
			if !best[f.Title.Title] {
				films = append(films, Film{Title: Title{Title: f.Title.Title}})
			}
		}
	}
	slimFilms := make([]map[string]any, 0, len(films))
	for _, f := range films {
		m := map[string]any{"title": f.Title.Title}
		if f.Year != nil {
			m["year"] = *f.Year
		}
		if f.WatchedOn != "" {
			m["watched_on"] = f.WatchedOn
		}
		if f.Rating != nil {
			m["rating"] = *f.Rating
		}
		if f.Review != nil {
			m["review"] = *f.Review
		}
		if f.Genres != nil {
			m["genres"] = f.Genres
		}
		slimFilms = append(slimFilms, m)
	}
	shows := make([]map[string]any, 0, len(d.Shows))
	for _, s := range d.Shows {
		shows = append(shows, map[string]any{"title": s.Title.Title, "episodes": s.Episodes, "span": s.Span})
	}
	sugg := make([]map[string]any, 0, len(d.Picks))
	for _, p := range d.Picks {
		sugg = append(sugg, map[string]any{"title": p.Title, "year": p.Year, "in_library": p.InLibrary})
	}
	coming := make([]map[string]any, 0, len(d.Coming))
	for _, c := range d.Coming {
		coming = append(coming, map[string]any{"title": c.Title.Title, "date": c.Date})
	}
	slim := map[string]any{
		"period": d.Kind + " of " + d.Label, "films": slimFilms, "shows": shows, "totals": d.Totals,
		"ratings_given": d.Rated, "top_genres": d.TopGenres, "suggestions": sugg, "coming_from_watchlist": coming,
	}
	if d.Kind == "year" && d.Year != nil {
		byMonth := make([]int, 12)
		for i, m := range d.Year.Months {
			byMonth[i] = m.Films + m.Episodes
		}
		var first, last *string
		if d.Year.FirstFilm != nil {
			first = &d.Year.FirstFilm.Title.Title
		}
		if d.Year.LastFilm != nil {
			last = &d.Year.LastFilm.Title.Title
		}
		slim["year_in_numbers"] = map[string]any{"films_and_episodes_by_month": byMonth, "busiest_month": d.Year.Busiest,
			"favourite_genres": d.Year.Genres, "first_film": first, "last_film": last}
	}
	b, _ := json.Marshal(slim)
	return string(b)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// PlainWords are the email's words without a model.
func PlainWords(d Data) Words {
	var parts []string
	if d.Totals.Films > 0 {
		parts = append(parts, plural(d.Totals.Films, "film"))
	}
	if d.Totals.Episodes > 0 {
		parts = append(parts, plural(d.Totals.Episodes, "episode"))
	}
	what := d.Kind
	if len(parts) == 0 {
		return Words{Subject: "Your " + what + " in film: a quiet one",
			Intro:     "Nothing watched this " + what + ". Here is what is waiting for you.",
			FilmNotes: map[string]string{}, PicksLine: "A few ideas for next time:"}
	}
	both := strings.Join(parts, " and ")
	return Words{Subject: "Your " + what + " in film: " + both, Intro: "This " + what + " you watched " + both + ".",
		FilmNotes: map[string]string{}, PicksLine: "Up next, maybe:"}
}

// writeWords asks the install's model (through LiteLLM) for the words, and
// falls back to the plain ones when there is no model or it fails: a digest
// still goes out without AI, and the fallback is logged, never silent. The
// second value is the model's name when its words were used.
func (s *Service) writeWords(ctx context.Context, d Data) (Words, *string) {
	words := PlainWords(d)
	ai := s.cl.AI
	if ai == nil || !ai.Enabled() {
		return words, nil
	}
	raw, err := ai.ChatJSON(ctx, aiSystem, aiInput(d), 2)
	if err != nil {
		s.log.WarnContext(ctx, "digest words from the model failed; using plain wording", "kind", d.Kind, "err", err)
		return words, nil
	}
	if got, ok := raw.(map[string]any); ok {
		for k, dst := range map[string]*string{"subject": &words.Subject, "intro": &words.Intro, "picks_line": &words.PicksLine} {
			if v, ok := got[k].(string); ok && strings.TrimSpace(v) != "" {
				*dst = strings.TrimSpace(v)
			}
		}
		if notes, ok := got["film_notes"].(map[string]any); ok {
			for k, v := range notes {
				if text := strings.TrimSpace(fmt.Sprint(v)); v != nil && text != "" {
					words.FilmNotes[k] = text
				}
			}
		}
	}
	words.Subject = short(words.Subject, 120) // a model's long subject ends with "…"
	model := s.cfg.LiteLLMModel
	return words, &model
}
