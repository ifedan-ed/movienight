package search

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/matching"
)

// The AI's best guess for a described plot.
//
// "kids with a treasure map trying to save their homes" is a memory, not a
// title. The meaning search matches it against TMDB's short plot summaries;
// a language model knows the films themselves, the way a friend would, and
// names the ones it fits. Each is looked up on TMDB and shown first.
//
// /search suggests it (ai: true) for a description: four or more words, no
// title matched; /search/ai itself does not check. Only with
// SEARCH_AI_MODEL. There is no cap on how often it is asked, per person or
// for the install (the owner's rule: no usage limits).

const aiPrompt = "You identify films and TV series from a viewer's description, which may be vague, misremembered or misspelled. " +
	`Reply with JSON only: {"matches": [{"title": "...", "year": 1985, "type": "movie", "why": "a few words on what fits"}]} ` +
	`("type" is "movie" or "tv"). Up to 4 candidates, the most likely first. Only real, released titles; never invent one. ` +
	`If nothing fits, reply {"matches": []}.`

const (
	// keepAnswer is how long a real answer is kept (shared by every
	// replica, in Valkey); retryAfter how long a failed or empty one is,
	// so the same search a minute later asks the model again.
	keepAnswer = 24 * time.Hour
	retryAfter = time.Minute

	aiFailed    = "The AI didn't answer this time. Search again to ask it again."
	aiNothing   = "The AI had no guess for this description."
	aiOutOfRoom = "The AI ran out of room before it answered (it thought for too long). Search again to ask it again; " +
		"an admin can give it more room (SEARCH_AI_MAX_TOKENS)."
)

// guess is one title the model named.
type guess struct {
	Title string  `json:"title"`
	Year  int     `json:"year,omitempty"`
	Type  string  `json:"type"`
	Why   *string `json:"why,omitempty"`
}

// guesses is the model's answer as kept in the cache: the guesses, or why
// there are none (Fail: aiFailed or aiOutOfRoom).
type guesses struct {
	Matches []guess `json:"matches"`
	Fail    string  `json:"fail,omitempty"`
}

// worthAsking: a description rather than a title, four or more words (in
// any script) and no title matched it closely.
func (s *service) worthAsking(query string, bestTitleMatch float64) bool {
	return s.d.Clients.SearchAI.Enabled() && matching.Words(query) >= 4 && bestTitleMatch < 0.85
}

// ask is the model's guesses for a description, cached per (media,
// normalised description): "Kids with a treasure map, save their homes!"
// and "kids with a treasure map save their homes" share one answer.
func (s *service) ask(ctx context.Context, query, media string) (guesses, error) {
	m := media
	if m == "" {
		m = "all"
	}
	key := cache.Key(true, "ai-guess", m, matching.Norm(query))
	return cache.Cached(ctx, s.d.Cache, key, cache.Options{TTL: keepAnswer, TTLFor: func(raw []byte) time.Duration {
		var g guesses
		if json.Unmarshal(raw, &g) == nil && len(g.Matches) > 0 {
			return keepAnswer
		}
		return retryAfter
	}}, func(ctx context.Context) (guesses, error) { return s.askModel(ctx, query, media), nil })
}

// askModel asks once (no retry on a bad reply). A failure is an answer
// here ("the AI didn't answer"), kept a minute, not an error: the page
// says so in place of the guesses.
func (s *service) askModel(ctx context.Context, query, media string) guesses {
	kind := "film or TV series"
	switch media {
	case "tv":
		kind = "TV series"
	case "movie":
		kind = "film"
	}
	reply, err := s.d.Clients.SearchAI.ChatJSON(ctx, aiPrompt, "A "+kind+" described as: "+query, 1)
	var room *clients.AIOutOfRoom
	switch {
	case errors.As(err, &room):
		s.d.Log.WarnContext(ctx, "search AI ran out of room; raise SEARCH_AI_MAX_TOKENS to give it more", "err", err)
		return guesses{Fail: aiOutOfRoom}
	case err != nil:
		s.d.Log.WarnContext(ctx, "search AI failed", "err", err)
		return guesses{Fail: aiFailed}
	}
	obj, _ := reply.(map[string]any)
	list, _ := obj["matches"].([]any)
	out := guesses{Matches: []guess{}}
	for _, x := range list {
		m, _ := x.(map[string]any)
		title, _ := m["title"].(string)
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		t, _ := m["type"].(string)
		g := guess{Title: short(title, 200), Type: "movie"}
		switch strings.ToLower(t) {
		case "tv", "series", "show", "tv series":
			g.Type = "tv"
		}
		if media != "" && g.Type != media {
			continue
		}
		switch y := m["year"].(type) {
		case float64:
			if y > 0 {
				g.Year = int(y)
			}
		case string:
			g.Year, _ = strconv.Atoi(y)
		}
		if why, _ := m["why"].(string); strings.TrimSpace(why) != "" {
			w := short(strings.TrimSpace(why), 200)
			g.Why = &w
		}
		out.Matches = append(out.Matches, g)
		if len(out.Matches) == 4 {
			break
		}
	}
	return out
}

// short is at most n characters, with "…" when it was longer (never cut
// without a mark).
func short(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return strings.TrimRight(string(r[:n]), " ") + "…"
}

// resolve is the TMDB title a guess names: the same name, in the year it
// gave if any (else any year). A TMDB failure drops that guess (logged):
// the others still show, a deliberate degrade kept from the Python.
func (s *service) resolve(ctx context.Context, g guess) clients.JSON {
	tmdb := s.d.Clients.TMDB
	search := tmdb.SearchMovie
	if g.Type == "tv" {
		search = tmdb.SearchTV
	}
	results, err := search(ctx, g.Title, g.Year)
	if err == nil && len(results) == 0 && g.Year > 0 {
		results, err = search(ctx, g.Title, 0)
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "TMDB lookup of an AI guess failed", "err", err)
		return nil
	}
	if len(results) == 0 {
		return nil
	}
	for _, r := range results {
		if matching.TitleMatch(g.Title, str(r, "title", "name"), str(r, "original_title", "original_name")) >= 0.85 {
			return r
		}
	}
	return results[0]
}

// found is a guess TMDB knows.
type found struct {
	result clients.JSON
	why    *string
}

// bestGuesses is (TMDB result, why) for each guess TMDB knows, in the
// model's order, and a plain note when there is none.
func (s *service) bestGuesses(ctx context.Context, query, media string) ([]found, string, error) {
	g, err := s.ask(ctx, query, media)
	if err != nil {
		return nil, "", err
	}
	if g.Fail != "" {
		return nil, g.Fail, nil
	}
	results := make([]clients.JSON, len(g.Matches))
	var wg sync.WaitGroup
	for i, m := range g.Matches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.resolve(ctx, m)
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	var out []found
	for i, r := range results {
		if r == nil {
			continue
		}
		mt, _ := r["media_type"].(string)
		if mt == "" {
			mt = g.Matches[i].Type
		}
		id, _ := r["id"].(float64)
		k := mt + ":" + strconv.Itoa(int(id))
		if id <= 0 || seen[k] {
			continue
		}
		seen[k] = true
		r["media_type"] = mt
		out = append(out, found{result: r, why: g.Matches[i].Why})
	}
	if len(out) == 0 {
		return nil, aiNothing, nil
	}
	return out, "", nil
}

// str is the first of keys that is a non-empty string in r.
func str(r map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, _ := r[k].(string); s != "" {
			return s
		}
	}
	return ""
}
