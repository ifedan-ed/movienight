// Package search is finding titles: search (library, catalog and TMDB,
// ranked together, with spelling correction), the AI's best guess for a
// described plot, a person's page, poster back-fill (/titles/fill) and the
// Jellyfin image proxy (app/api/v1/catalog.py).
//
// What changed from the Python, on purpose:
//
//   - The caches (TMDB's search pages, MDBList's search, people, the AI's
//     guesses) are in Valkey with a TTL and hashed keys, shared by every
//     replica, instead of an unbounded per-process dict keyed by the raw
//     text someone typed.
//   - A person's state is read for the candidate titles only, once.
//   - Titles passing through are written to the cache only when that
//     changes something (titles.Remember), not upserted on every search.
//   - /titles/fill asks TMDB for at most FillMaxFetch titles per call (the
//     Python could fire 1,000).
//   - A Jellyfin failure behind an image is a 502 that says so, not a
//     "No image" 404.
package search

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/matching"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/spelling"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
	"movienight/server/internal/userstate"
)

// Search answers in pages of matches, best first: page N holds the best
// LibraryPage × N library titles, OthersPage × N other titles (of
// CatalogPage × N read from the catalog index, plus TMDB's own search pages
// 1 to N) and every person on those TMDB pages. `more` says, per section,
// that there are more, and the apps offer "Show more" (page N + 1): nothing
// is cut without a word (docs/limits.md, Search). The last page is
// SEARCH_MAX_PAGES (25): page N needs TMDB's pages 1 to N for each wording,
// so a deep page the cache does not hold costs up to 2 × N TMDB calls.
const (
	LibraryPage = 24
	OthersPage  = 30
	CatalogPage = 40
	// TMDBPagesAtOnce: TMDB's pages of one search are read at most this many
	// at a time (both wordings together; a cached page does not wait), so
	// one deep "Show more" cannot burst dozens of calls at TMDB at once.
	TMDBPagesAtOnce = 4
	// FillMaxFetch is the most titles one /titles/fill asks TMDB for; the
	// rest of its keys are answered from the title cache only. The website
	// sends 40 a call, so in practice every key is filled.
	FillMaxFetch = 60

	tmdbSearchTTL = 10 * time.Minute
	mdbSearchTTL  = time.Hour
	personTTL     = 6 * time.Hour
)

type service struct {
	d     *rw.Deps
	vocab *spelling.Vocab
}

// Register adds the routes.
func Register(d *rw.Deps) {
	s := &service{d: d, vocab: spelling.NewVocab(d.Q)}
	api := d.Huma()

	huma.Register(api, rw.Op("search_api_v1_search_get", http.MethodGet, "/api/v1/search", "Search", "catalog",
		"Your library, every rated title on TMDB (the catalog index: by meaning and keywords, so a described plot\n"+
			"works) and TMDB's live title search (new releases, exact names), ranked on one scale. `first` says which\n"+
			"section holds the best match. `page` 2, 3, ... answers with more of each section (the matches before\n"+
			"included); `more` says which have more."), s.search)
	d.LimitQuery(http.MethodGet, "/api/v1/search", "q", rw.SearchChars)
	// The last page is a setting (SEARCH_MAX_PAGES): set on the parameter's
	// schema, so validation refuses a deeper page in plain words ("Page:
	// Input should be less than or equal to 25.") and the description says
	// the same number.
	if item := api.OpenAPI().Paths["/api/v1/search"]; item != nil && item.Get != nil {
		for _, p := range item.Get.Parameters {
			if p.Name == "page" && p.Schema != nil {
				last := float64(d.Cfg.RW.SearchMaxPages)
				p.Schema.Maximum = &last
				p.Schema.PrecomputeMessages()
			}
		}
	}

	huma.Register(api, rw.Op("search_ai_api_v1_search_ai_get", http.MethodGet, "/api/v1/search/ai", "Search Ai", "catalog",
		"The AI's best guess for a described plot (only when SEARCH_AI_MODEL is set): the films it names, as\n"+
			"cards, each with a few words on why."), s.searchAI)
	d.LimitQuery(http.MethodGet, "/api/v1/search/ai", "q", rw.SearchChars)

	huma.Register(api, rw.Op("person_api_v1_person__person_id__get", http.MethodGet, "/api/v1/person/{person_id}",
		"Person", "catalog"), s.person)

	huma.Register(api, rw.Op("fill_api_v1_titles_fill_post", http.MethodPost, "/api/v1/titles/fill", "Fill", "catalog",
		"Posters for cards that arrived without one (big lists)."), s.fill)
	d.Limit("KeysIn", "keys", rw.BatchItems)

	huma.Register(api, rw.Op("jellyfin_image_api_v1_img_jf__item_id__get", http.MethodGet, "/api/v1/img/jf/{item_id}",
		"Jellyfin Image", "catalog"), s.image)
}

// ---------------------------------------------------------------------------
// GET /search
// ---------------------------------------------------------------------------

type searchIn struct {
	Q     string `query:"q" required:"true" maxLength:"200"`
	Media string `query:"media" enum:"movie,tv"`
	Page  int    `query:"page" default:"1" minimum:"1" maximum:"25"`
}

// scores is a map that remembers insertion order, so equal scores rank in
// the order the searches found them (the Python's dicts did the same).
type scores struct {
	order []string
	val   map[string]float64
}

func newScores() *scores { return &scores{val: map[string]float64{}} }

func (s *scores) get(k string, def float64) float64 {
	if v, ok := s.val[k]; ok {
		return v
	}
	return def
}

func (s *scores) set(k string, v float64) {
	if _, ok := s.val[k]; !ok {
		s.order = append(s.order, k)
	}
	s.val[k] = v
}

func (s *scores) has(k string) bool { _, ok := s.val[k]; return ok }

// pop removes k (only ever called while walking a copy of order).
func (s *scores) pop(k string) float64 {
	v := s.val[k]
	delete(s.val, k)
	for i, o := range s.order {
		if o == k {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return v
}

// ranked is the keys best first (ties in the order found).
func (s *scores) ranked() []string {
	out := append([]string(nil), s.order...)
	sort.SliceStable(out, func(i, j int) bool { return s.val[out[i]] > s.val[out[j]] })
	return out
}

// tmdbPage is one TMDB search page as cached.
type tmdbPage struct {
	Results []clients.JSON `json:"r"`
	Pages   int            `json:"p"`
}

// wording is what the three searches found for one wording of the query.
type wording struct {
	lib, cat, tmdb []clients.JSON
	tmdbMore       bool
}

func num(m map[string]any, k string) float64 { f, _ := m[k].(float64); return f }

func (s *service) search(ctx context.Context, in *searchIn) (*rw.Body[schema.SearchOut], error) {
	id, err := s.d.User(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return rw.Out(schema.SearchOut{Library: []schema.Card{}, TMDB: []schema.Card{}, People: []schema.PersonHit{},
			First: "library", Page: 1}), nil
	}
	page, m := in.Page, in.Media
	lastPage, maxHits := s.d.Cfg.RW.SearchMaxPages, s.d.Cfg.SearchMaxHits
	libLimit, catLimit, shown := LibraryPage*page, CatalogPage*page, OthersPage*page

	// Typos fixed against the catalog's own words; both wordings are searched.
	vocab, err := s.vocab.Get(ctx)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	fixed := vocab.Correct(q)
	queries := []string{q}
	if fixed != q {
		queries = append(queries, fixed)
	}

	// Every search of every wording at once; TMDB's pages share one gate.
	gate := make(chan struct{}, TMDBPagesAtOnce)
	got := make([]wording, len(queries))
	var wg sync.WaitGroup
	for i, text := range queries {
		wg.Add(3)
		go func() { defer wg.Done(); got[i].lib = s.meili(ctx, text, clients.LibraryIndex, libLimit, m) }()
		go func() {
			defer wg.Done()
			if s.d.Cfg.CatalogEnabled {
				got[i].cat = s.meili(ctx, text, clients.CatalogIndex, catLimit, m)
			}
		}()
		go func() { defer wg.Done(); got[i].tmdb, got[i].tmdbMore = s.tmdbSearch(ctx, text, page, gate) }()
	}
	wg.Wait()

	var hits, chits, results []clients.JSON
	tmdbMore, libFull, catFull := false, false, false
	for _, g := range got {
		hits, chits, results = append(hits, g.lib...), append(chits, g.cat...), append(results, g.tmdb...)
		tmdbMore = tmdbMore || g.tmdbMore
		// A Meilisearch search that came back full may have more behind it.
		libFull = libFull || len(g.lib) >= min(libLimit, maxHits)
		catFull = catFull || (s.d.Cfg.CatalogEnabled && len(g.cat) >= min(catLimit, maxHits))
	}
	named := func(names ...string) float64 {
		best := 0.0
		for _, text := range queries {
			best = max(best, matching.TitleMatch(text, names...))
		}
		return best
	}

	// Library: Meilisearch's score, or the title match if that is better.
	libScores := newScores()
	for _, h := range hits {
		mt, _ := h["media_type"].(string)
		if num(h, "tmdb_id") <= 0 || (mt != "movie" && mt != "tv") {
			continue
		}
		k := titles.Key(mt, int(num(h, "tmdb_id")))
		score := max(num(h, "_rankingScore"), named(str(h, "title"), str(h, "original_title")))
		libScores.set(k, max(libScores.get(k, 0), score))
	}

	// Everything else: catalog hits and TMDB's title search, best score wins.
	scored := newScores()
	raw := map[string]clients.JSON{}
	for _, h := range chits {
		r, _ := h["tmdb"].(map[string]any)
		mt, _ := h["media_type"].(string)
		if num(r, "id") <= 0 || (mt != "movie" && mt != "tv") {
			continue
		}
		k := titles.Key(mt, int(num(r, "id")))
		score := max(num(h, "_rankingScore"), named(str(h, "title"), str(h, "original_title"))) +
			matching.PopularityBonus(num(h, "votes"))
		scored.set(k, max(scored.get(k, -1), score))
		one := clients.JSON{}
		for kk, v := range r {
			one[kk] = v
		}
		one["media_type"] = mt
		raw[k] = one
	}
	i := 0
	for _, r := range results {
		mt, _ := r["media_type"].(string)
		if mt != "movie" && mt != "tv" {
			continue
		}
		k := titles.Key(mt, int(num(r, "id")))
		// TMDB's own order counts for a little (0.5, less 0.01 a place).
		score := max(named(str(r, "title", "name"), str(r, "original_title", "original_name")), 0.5-0.01*float64(i)) +
			matching.PopularityBonus(num(r, "vote_count"))
		i++
		if score > scored.get(k, -1) {
			scored.set(k, score)
		}
		if _, ok := raw[k]; !ok {
			raw[k] = r
		}
	}

	// The person's state for every candidate at once (one query).
	candidates := append(append([]string(nil), libScores.order...), scored.order...)
	st, err := s.d.State.Load(ctx, id.User, candidates)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	// Whatever is in the library goes in the library section, however it
	// was found, with the best score any of the searches gave it.
	for _, k := range append([]string(nil), scored.order...) {
		key, _ := userstate.KeyOf(k)
		if _, inLib := st.Library[key]; libScores.has(k) || inLib {
			libScores.set(k, max(libScores.get(k, 0), scored.pop(k)))
		}
	}
	var libRanked []string
	for _, k := range libScores.ranked() {
		if m == "" || strings.HasPrefix(k, m+":") {
			libRanked = append(libRanked, k)
		}
	}
	libKeys := libRanked[:min(len(libRanked), libLimit)]
	var passing []*titles.Meta
	for _, k := range libKeys {
		if r, ok := raw[k]; ok {
			if mt := titles.Normalize(r, ""); mt != nil {
				passing = append(passing, mt)
			}
		}
	}
	s.d.Titles.Remember(ctx, passing)
	metas, err := s.d.Titles.Ensure(ctx, libKeys, len(libKeys))
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	library := st.Cards(ordered(metas, libKeys))
	owned := map[string]bool{}
	for _, c := range library {
		owned[c.Key] = true
	}
	for _, k := range libScores.order {
		owned[k] = true
	}
	var order []string
	for _, k := range scored.ranked() {
		if !owned[k] && (m == "" || strings.HasPrefix(k, m+":")) {
			order = append(order, k)
		}
	}
	order = order[:min(len(order), catLimit)]
	others := make([]*titles.Meta, 0, len(order))
	for _, k := range order {
		if mt := titles.Normalize(raw[k], ""); mt != nil {
			others = append(others, mt)
		}
	}
	s.d.Titles.Remember(ctx, others)
	found := st.Cards(others)

	// MDBList's search can find titles the others miss; asked only when the
	// rest come up short (and the query has 3 or more characters).
	if len(found) < 3 && s.d.Clients.MDBList.Enabled() && utf8.RuneCountInString(q) >= 3 {
		extra, err := s.mdblist(ctx, id.User, q, m, owned, found)
		if err != nil {
			// Deliberate degrade (as the Python): MDBList is only a
			// fallback; the page has the other sections.
			s.d.Log.InfoContext(ctx, "MDBList search fallback unavailable", "err", err)
		}
		found = append(found, extra...)
		for _, c := range extra {
			if !scored.has(c.Key) {
				scored.set(c.Key, 0.45)
			}
		}
	}

	// Every person on the TMDB pages read (no cut): page N + 1 reads more.
	people := []schema.PersonHit{}
	at := map[int]int{}
	for _, r := range results {
		if r["media_type"] != "person" {
			continue
		}
		pid := int(num(r, "id"))
		hit := schema.PersonHit{ID: &pid, Name: strp(r, "name"), Photo: strp(r, "profile_path"), KnownFor: strp(r, "known_for_department")}
		if j, ok := at[pid]; ok {
			people[j] = hit
			continue
		}
		at[pid] = len(people)
		people = append(people, hit)
	}

	bestNamed := 0.0
	for _, r := range results {
		if mt, _ := r["media_type"].(string); mt == "movie" || mt == "tv" {
			bestNamed = max(bestNamed, named(str(r, "title", "name"), str(r, "original_title", "original_name")))
		}
	}
	for _, h := range hits {
		bestNamed = max(bestNamed, named(str(h, "title"), str(h, "original_title")))
	}
	for _, h := range chits {
		bestNamed = max(bestNamed, named(str(h, "title"), str(h, "original_title")))
	}
	bestLib, bestOther := -1.0, -1.0
	for _, c := range library {
		bestLib = max(bestLib, libScores.get(c.Key, 0))
	}
	for _, c := range found {
		bestOther = max(bestOther, scored.get(c.Key, 0))
	}
	more := schema.SearchMore{
		Library: len(libRanked) > libLimit || (libFull && libLimit < maxHits),
		TMDB:    len(found) > shown || tmdbMore || (catFull && catLimit < maxHits),
		People:  tmdbMore,
	}
	// The last page offers no "Show more" (a page past it is refused), and
	// says so when a section had more.
	cut := page >= lastPage && (more.Library || more.TMDB || more.People)
	if page >= lastPage {
		more = schema.SearchMore{}
	}
	// Meilisearch gives at most its SEARCH_MAX_HITS best hits for one
	// search: say so where a section stopped there.
	var stopped, why []string
	if libFull && libLimit >= maxHits {
		stopped = append(stopped, "your library")
	}
	if catFull && catLimit >= maxHits {
		stopped = append(stopped, "other titles")
	}
	if len(stopped) > 0 {
		why = append(why, "These are the "+thousands(maxHits)+" best matches in "+strings.Join(stopped, " and ")+
			" (the most one search gives)")
	}
	if cut {
		if len(why) > 0 {
			why = append(why, "page "+strconv.Itoa(lastPage)+" is as far as one search goes")
		} else {
			why = append(why, "That's as far as one search goes (page "+strconv.Itoa(lastPage)+")")
		}
	}
	out := schema.SearchOut{Library: library, TMDB: found[:min(len(found), shown)], People: people, First: "library",
		AI: s.worthAsking(q, bestNamed), Page: page, More: more}
	if bestOther > bestLib {
		out.First = "tmdb"
	}
	if fixed != q {
		out.Corrected = &fixed
	}
	if len(why) > 0 {
		note := strings.Join(why, ", and ") + "; add words to narrow it down."
		out.Note = &note
	}
	return rw.Out(out), nil
}

// meili is one Meilisearch search. A failure is logged and the section is
// empty: a deliberate degrade (as the Python), so one index being down
// leaves the rest of the search working. (The client itself falls back to
// keyword search, logged, while the embedder is not ready.)
func (s *service) meili(ctx context.Context, text, index string, limit int, media string) []clients.JSON {
	hits, err := s.d.Clients.Meili.Search(ctx, text, clients.SearchOptions{Index: index,
		SemanticRatio: s.d.Cfg.RW.MeiliSemanticRatio, Limit: limit, MediaType: media, Scores: true})
	if err != nil {
		s.d.Log.WarnContext(ctx, "search failed; that section is empty", "index", index, "err", err)
		return nil
	}
	return hits
}

// tmdbSearch is TMDB's pages 1 to page of one wording (each cached ten
// minutes, install-wide), never past its last, at most TMDBPagesAtOnce read
// at a time across the whole search. Returns whether TMDB has more pages.
// A failure is logged and the section is empty (deliberate degrade, as the
// Python).
func (s *service) tmdbSearch(ctx context.Context, text string, page int, gate chan struct{}) ([]clients.JSON, bool) {
	one := func(n int) (tmdbPage, error) {
		key := cache.Key(true, "search", strconv.Itoa(n), text)
		return cache.Cached(ctx, s.d.Cache, key, cache.Options{TTL: tmdbSearchTTL}, func(ctx context.Context) (tmdbPage, error) {
			gate <- struct{}{}
			defer func() { <-gate }()
			r, pages, err := s.d.Clients.TMDB.SearchMultiPage(ctx, text, n)
			return tmdbPage{Results: r, Pages: pages}, err
		})
	}
	first, err := one(1)
	if err != nil {
		s.d.Log.WarnContext(ctx, "TMDB search failed; that section is empty", "err", err)
		return nil, false
	}
	last := min(page, first.Pages)
	rest := make([]tmdbPage, max(0, last-1))
	errs := make([]error, len(rest))
	var wg sync.WaitGroup
	for n := 2; n <= last; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); rest[n-2], errs[n-2] = one(n) }()
	}
	wg.Wait()
	out := append([]clients.JSON(nil), first.Results...)
	for i, p := range rest {
		if errs[i] != nil {
			s.d.Log.WarnContext(ctx, "TMDB search failed; that section is empty", "err", errs[i])
			return nil, false
		}
		out = append(out, p.Results...)
	}
	return out, first.Pages > page
}

// mdblist is MDBList's search (cached an hour) for titles the others
// missed, as cards.
func (s *service) mdblist(ctx context.Context, u store.User, q, media string, owned map[string]bool, found []schema.Card) ([]schema.Card, error) {
	hits, err := cache.Cached(ctx, s.d.Cache, cache.Key(true, "mdbsearch", q), cache.Options{TTL: mdbSearchTTL},
		func(ctx context.Context) ([]clients.SearchHit, error) { return s.d.Clients.MDBList.Search(ctx, q, 8) })
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for k := range owned {
		seen[k] = true
	}
	for _, c := range found {
		seen[c.Key] = true
	}
	var keys []string
	for _, h := range hits {
		k := titles.Key(h.Media, h.TMDBID)
		if (media == "" || h.Media == media) && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	metas, err := s.d.Titles.Ensure(ctx, keys, 8)
	if err != nil {
		return nil, err
	}
	st, err := s.d.State.Load(ctx, u, keys)
	if err != nil {
		return nil, err
	}
	return st.Cards(ordered(metas, keys)), nil
}

// ordered is metas in the order of keys (missing ones skipped).
func ordered(metas map[string]*titles.Meta, keys []string) []*titles.Meta {
	out := make([]*titles.Meta, 0, len(keys))
	for _, k := range keys {
		if m := metas[k]; m != nil {
			out = append(out, m)
		}
	}
	return out
}

func strp(r map[string]any, k string) *string {
	if s, ok := r[k].(string); ok {
		return &s
	}
	return nil
}

// thousands writes 1000 as "1,000", as the Python's {:,} did.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ---------------------------------------------------------------------------
// GET /search/ai
// ---------------------------------------------------------------------------

type searchAIIn struct {
	Q     string `query:"q" required:"true" maxLength:"200"`
	Media string `query:"media" enum:"movie,tv"`
}

func (s *service) searchAI(ctx context.Context, in *searchAIIn) (*rw.Body[schema.AiGuessOut], error) {
	id, err := s.d.User(ctx)
	if err != nil {
		return nil, err
	}
	// No cap of any kind here (the owner's rule: no usage limits), and no
	// "worth asking" check: that is only the hint /search gives the apps,
	// so any text sent straight here is asked.
	q := strings.TrimSpace(in.Q)
	enabled := s.d.Clients.SearchAI.Enabled()
	if q == "" || !enabled {
		return rw.Out(schema.AiGuessOut{Enabled: enabled, Matches: []schema.AiGuess{}}), nil
	}
	got, note, err := s.bestGuesses(ctx, q, in.Media)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	metas := make([]*titles.Meta, len(got))
	var keys []string
	var keep []*titles.Meta
	for i, g := range got {
		if metas[i] = titles.Normalize(g.result, ""); metas[i] != nil {
			keys, keep = append(keys, metas[i].Key), append(keep, metas[i])
		}
	}
	s.d.Titles.Remember(ctx, keep)
	st, err := s.d.State.Load(ctx, id.User, keys)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	out := schema.AiGuessOut{Enabled: true, Matches: []schema.AiGuess{}}
	for i, mt := range metas {
		if mt != nil {
			out.Matches = append(out.Matches, schema.AiGuess{Card: st.Card(mt), Why: got[i].why})
		}
	}
	// No matches: say why in plain words (the page shows it instead of
	// hiding the section).
	if len(out.Matches) == 0 {
		if note == "" {
			note = aiNothing
		}
		out.Note = &note
	}
	return rw.Out(out), nil
}

// ---------------------------------------------------------------------------
// GET /person/{person_id}
// ---------------------------------------------------------------------------

type personIn struct {
	PersonID int `path:"person_id" exclusiveMinimum:"0" maximum:"100000000"`
}

// creditJobs are the crew jobs a person's page lists beside their roles.
var creditJobs = map[string]bool{"Director": true, "Screenplay": true, "Writer": true, "Creator": true}

func (s *service) person(ctx context.Context, in *personIn) (*rw.Body[schema.PersonOut], error) {
	id, err := s.d.User(ctx)
	if err != nil {
		return nil, err
	}
	p, err := cache.Cached(ctx, s.d.Cache, "person:"+strconv.Itoa(in.PersonID), cache.Options{TTL: personTTL},
		func(ctx context.Context) (clients.JSON, error) { return s.d.Clients.TMDB.Person(ctx, in.PersonID) })
	if err != nil {
		s.d.Log.WarnContext(ctx, "TMDB person failed", "err", err)
		return nil, rw.Err(http.StatusBadGateway, "Couldn't reach TMDB")
	}
	if p == nil {
		return nil, rw.NotFound("Person not found")
	}
	credits, _ := p["combined_credits"].(map[string]any)
	ranked := asList(credits["cast"])
	for _, x := range asList(credits["crew"]) {
		if job, _ := x["job"].(string); creditJobs[job] {
			ranked = append(ranked, x)
		}
	}
	// The most rated first (stable: TMDB's order among equals).
	sort.SliceStable(ranked, func(i, j int) bool { return num(ranked[i], "vote_count") > num(ranked[j], "vote_count") })
	metas := titles.NormalizeAll(ranked, "")
	s.d.Titles.Remember(ctx, metas)
	keys := make([]string, len(metas))
	for i, mt := range metas {
		keys[i] = mt.Key
	}
	st, err := s.d.State.Load(ctx, id.User, keys)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	out := schema.PersonOut{Name: strp(p, "name"), Photo: strp(p, "profile_path"), Biography: strp(p, "biography"),
		Birthday: strp(p, "birthday"), PlaceOfBirth: strp(p, "place_of_birth"), KnownFor: strp(p, "known_for_department"),
		Items: st.Cards(metas)}
	if pid := int(num(p, "id")); pid > 0 {
		out.ID = &pid
	}
	return rw.Out(out), nil
}

func asList(v any) []clients.JSON {
	arr, _ := v.([]any)
	out := make([]clients.JSON, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// POST /titles/fill
// ---------------------------------------------------------------------------

func (s *service) fill(ctx context.Context, in *rw.Body[schema.KeysIn]) (*rw.Body[schema.FillOut], error) {
	if _, err := s.d.User(ctx); err != nil {
		return nil, err
	}
	// At most FillMaxFetch TMDB calls (8 at a time, titles.Ensure); keys
	// past that are answered from the cache only. The Python fetched every
	// missing key of the call: up to BATCH_MAX_ITEMS (1,000) TMDB calls.
	metas, err := s.d.Titles.Ensure(ctx, in.Body.Keys, FillMaxFetch)
	if err != nil {
		return nil, s.d.Fail(ctx, err)
	}
	out := schema.FillOut{Titles: make(map[string]schema.FilledTitle, len(metas))}
	for k, m := range metas {
		title := m.Title
		out.Titles[k] = schema.FilledTitle{Poster: m.PosterPath, Backdrop: m.BackdropPath, Year: m.Year, Title: &title, Vote: m.Vote()}
	}
	return rw.Out(out), nil
}

// ---------------------------------------------------------------------------
// GET /img/jf/{item_id}
// ---------------------------------------------------------------------------

type imageIn struct {
	ItemID string `path:"item_id" pattern:"^[0-9a-f]{32}$"`
	Kind   string `query:"kind" default:"Primary" pattern:"^(Primary|Backdrop|Thumb)$"`
	W      int    `query:"w" default:"400" minimum:"80" maximum:"1280"`
}

type imageOut struct {
	ContentType  string `header:"Content-Type"`
	CacheControl string `header:"Cache-Control"`
	Body         []byte
}

// image proxies a Jellyfin image, so the Jellyfin API key never reaches
// the browser. The client uses its separate image pool (a page of posters
// cannot use up the connections the API calls need).
func (s *service) image(ctx context.Context, in *imageIn) (*imageOut, error) {
	if _, err := s.d.User(ctx); err != nil {
		return nil, err
	}
	body, ctype, err := s.d.Clients.Jellyfin.Image(ctx, in.ItemID, in.Kind, in.W)
	if err != nil {
		// Jellyfin failed, which is not a missing image: said as such.
		s.d.Log.WarnContext(ctx, "Jellyfin image failed", "err", err)
		return nil, rw.Err(http.StatusBadGateway, "Jellyfin didn't send the image")
	}
	if body == nil {
		return nil, rw.NotFound("No image")
	}
	if !strings.HasPrefix(ctype, "image/") {
		return nil, rw.Err(http.StatusBadGateway, "Not an image")
	}
	return &imageOut{ContentType: ctype, CacheControl: "private, max-age=604800", Body: body}, nil
}
