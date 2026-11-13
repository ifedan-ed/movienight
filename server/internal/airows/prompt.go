package airows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// Pick is one row the model proposed: a theme and "Title (Year)" picks.
type Pick struct {
	Theme  string
	Titles []string
}

// ParseResponse reads the model's rows: at most MaxRows, each with a theme
// (80 characters at most, cut with "…") and at most MaxItemsPerRow picks.
// No usable row is an error.
func ParseResponse(raw any) ([]Pick, error) {
	if s, ok := raw.(string); ok {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, err
		}
	}
	rows, ok := raw.([]any)
	if m, isObj := raw.(map[string]any); isObj {
		rows, ok = m["rows"].([]any)
	}
	if !ok {
		return nil, errors.New("AI response did not contain a 'rows' list")
	}
	var out []Pick
	for i, r := range rows {
		if i == MaxRows {
			break
		}
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		theme := strings.TrimSpace(fmt.Sprint(valueOr(m["theme"], "")))
		var titles []string
		list, _ := m["titles"].([]any)
		for _, t := range list {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				titles = append(titles, strings.TrimSpace(s))
				if len(titles) == MaxItemsPerRow {
					break
				}
			}
		}
		if theme == "" || len(titles) == 0 {
			continue
		}
		if utf8.RuneCountInString(theme) > 80 {
			theme = strings.TrimRight(string([]rune(theme)[:80]), " \t\n") + "…"
		}
		out = append(out, Pick{Theme: theme, Titles: titles})
	}
	if len(out) == 0 {
		return nil, errors.New("AI response had no usable rows")
	}
	return out, nil
}

func valueOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

type rated struct {
	title  string
	rating int
}

type note struct {
	title  string
	rating int
	text   string
}

// summarize is the prompt's view of the person.
func summarize(watched []string, ratings []rated, maxItems int, notes []note, maxNotes int) string {
	var lines []string
	if len(watched) > 0 {
		lines = append(lines, "Recently watched: "+strings.Join(watched[:min(maxItems, len(watched))], ", "))
	}
	if len(ratings) > 0 {
		top := append([]rated(nil), ratings...)
		// Stable, as Python's sorted: equal ratings keep their order (newest first).
		sort.SliceStable(top, func(i, j int) bool { return top[i].rating > top[j].rating })
		top = top[:min(maxItems, len(top))]
		parts := make([]string, len(top))
		for i, r := range top {
			parts[i] = fmt.Sprintf("%s (%d)", r.title, r.rating)
		}
		lines = append(lines, "Ratings out of 10: "+strings.Join(parts, ", "))
	}
	if len(notes) > 0 {
		n := notes[:min(maxNotes, len(notes))]
		parts := make([]string, len(n))
		for i, x := range n {
			parts[i] = fmt.Sprintf("%s (%d/10): %s", x.title, x.rating, x.text)
		}
		lines = append(lines, "Their own comments: "+strings.Join(parts, " | "))
	}
	if len(lines) == 0 {
		return "No history yet."
	}
	return strings.Join(lines, "\n")
}

// historySummary is the prompt's view of the person and what it covers
// (basis, shown under "Picked for you", so nothing is cut without a word).
// Sent: titles from their latest film watches (AI_PROMPT_TITLES newest
// distinct), their best film ratings (as many), and their AI_PROMPT_COMMENTS
// latest comments, each cut to AI_PROMPT_COMMENT_CHARS with "…". Never their
// name or email address.
func (s *Service) historySummary(ctx context.Context, userID int32) (string, map[string]any, error) {
	nTitles, nNotes, nChars := s.cfg.AIPromptTitles, s.cfg.AIPromptComments, s.cfg.AIPromptCommentChars
	recent, err := s.q.AIRowsMovieTitles(ctx, store.AIRowsMovieTitlesParams{UserID: userID, Limit: int32(max(200, nTitles*3))})
	if err != nil {
		return "", nil, err
	}
	var watched []string
	seen := map[string]bool{}
	for _, t := range recent {
		if t != "" && !seen[t] { // two sources can log the same watch
			seen[t] = true
			watched = append(watched, t)
		}
	}
	rows, err := s.q.AIRowsMovieRatings(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = titles.Key("movie", int(r.TmdbID))
	}
	names, err := s.titles.GetMany(ctx, keys)
	if err != nil {
		return "", nil, err
	}
	var ratings []rated
	var notes []note
	for i, r := range rows {
		m, ok := names[keys[i]]
		if !ok {
			continue
		}
		ratings = append(ratings, rated{m.Title, int(r.Rating)})
		if r.Review != nil && strings.TrimSpace(*r.Review) != "" {
			notes = append(notes, note{m.Title, int(r.Rating), strings.Join(strings.Fields(*r.Review), " ")})
		}
	}
	shortened := 0
	for i := range notes {
		if utf8.RuneCountInString(notes[i].text) > nChars {
			if i < nNotes {
				shortened++
			}
			notes[i].text = strings.TrimRight(string([]rune(notes[i].text)[:nChars]), " \t\n") + "…"
		}
	}
	total, err := s.q.AIRowsFilmsTotal(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	basis := map[string]any{"films": min(len(watched), nTitles), "films_total": total,
		"ratings": min(len(ratings), nTitles), "ratings_total": len(ratings),
		"comments": min(len(notes), nNotes), "comments_total": len(notes),
		"comments_shortened": shortened, "comment_chars": nChars}
	return summarize(watched, ratings, nTitles, notes, nNotes), basis, nil
}

// BasisLine is what the AI read, said under the rows: "From your 60 latest
// of 340 films, 60 best of 210 film ratings and 20 latest of 45 comments (3
// shortened to their first 300 characters)". nil without a basis.
func BasisLine(basis map[string]any) *string {
	if basis == nil {
		return nil
	}
	n := func(k string) int {
		switch v := basis[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		}
		return 0
	}
	part := func(have, total int, kind, which string) string {
		if total == 0 {
			return ""
		}
		if total == 1 {
			kind = strings.TrimSuffix(kind, "s") // "your 1 film"
		}
		if have >= total {
			return fmt.Sprintf("your %d %s", total, kind)
		}
		return fmt.Sprintf("your %d %s of %d %s", have, which, total, kind)
	}
	comments := part(n("comments"), n("comments_total"), "comments", "latest")
	if cut := n("comments_shortened"); comments != "" && cut > 0 {
		chars := n("comment_chars")
		if chars == 0 {
			chars = 300
		}
		c := thousands(chars)
		switch {
		case cut == 1:
			comments += fmt.Sprintf(" (1 shortened to its first %s characters)", c)
		case cut == n("comments"):
			comments += fmt.Sprintf(" (all shortened to their first %s characters)", c)
		default:
			comments += fmt.Sprintf(" (%d shortened to their first %s characters)", cut, c)
		}
	}
	var bits []string
	for _, b := range []string{part(n("films"), n("films_total"), "films", "latest"),
		part(n("ratings"), n("ratings_total"), "film ratings", "best"), comments} {
		if b != "" {
			bits = append(bits, b)
		}
	}
	if len(bits) == 0 {
		return nil
	}
	s := "From " + bits[0]
	if len(bits) > 1 {
		s = "From " + strings.Join(bits[:len(bits)-1], ", ") + " and " + bits[len(bits)-1]
	}
	return &s
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
