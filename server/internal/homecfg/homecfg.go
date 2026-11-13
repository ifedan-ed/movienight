// Package homecfg is which rows each Home tab shows a person, in what order
// (the "Home rows" screen), and how lists are named and grouped.
// Each tab's choice is a list of {key, on}
// in the person's settings under "home_rows:<tab>".
//
// Row keys are read per tab: "trending" on Movies is trending films, on For
// You films and shows together. Lists are "list:<id>" rows, For You only.
package homecfg

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"movienight/server/internal/settings"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// Tabs, in order.
var Tabs = []string{"for_you", "movies", "shows"}

// TabMedia is the media a tab shows ("" for both).
func TabMedia(tab string) string {
	switch tab {
	case "movies":
		return "movie"
	case "shows":
		return "tv"
	}
	return ""
}

// Provider is a TMDB watch provider (US ids).
type Provider struct {
	ID   int
	Name string
}

// Providers, in the order the Home rows screen and /providers list them.
var Providers = []Provider{
	{8, "Netflix"}, {337, "Disney+"}, {9, "Prime Video"}, {350, "Apple TV+"}, {1899, "Max"},
	{15, "Hulu"}, {531, "Paramount+"}, {386, "Peacock"},
}

func providerName(id int) string {
	for _, p := range Providers {
		if p.ID == id {
			return p.Name
		}
	}
	return "streaming"
}

// baseRow is a row every tab may show, with its title.
type baseRow struct{ Key, Title string }

var baseRows = []baseRow{
	{"continue", "Up next"}, {"recent", "Recently added"}, {"watchlist", "Plan to watch"},
	{"because", "Because you watched"}, {"ai", "Picked for you"}, {"trending", "Trending"},
	{"popular", "Popular"}, {"upcoming", "Coming soon"}, {"top_rated", "Top rated"},
	{"unwatched", "In your library, not watched yet"}, {"favorites", "Favorites"}, {"rated", "Your top rated"},
}

// Genre ids in TMDB's own order (the order the Python's tables were
// written in, which the Home rows screen shows).
var (
	movieGenreOrder = []int{28, 12, 16, 35, 80, 99, 18, 10751, 14, 36, 27, 10402, 9648, 10749, 878, 10770, 53, 10752, 37}
	tvGenreOrder    = []int{10759, 16, 35, 80, 99, 18, 10751, 10762, 9648, 10763, 10764, 10765, 10766, 10767, 10768, 37}
)

// defaults are each tab's rows when the person never chose (all on).
var defaults = map[string][]string{
	"for_you": {"continue", "recent", "watchlist", "because", "ai", "trending", "upcoming",
		"provider:8", "provider:337", "provider:9", "provider:350", "provider:1899", "unwatched", "favorites"},
	"movies": {"continue", "recent", "watchlist", "because", "trending", "popular", "upcoming", "top_rated",
		"genre:28", "genre:35", "genre:53", "genre:18", "genre:878", "genre:27", "genre:16", "genre:99",
		"provider:8", "provider:337", "provider:9", "provider:350", "provider:1899"},
	"shows": {"continue", "recent", "watchlist", "because", "trending", "popular", "upcoming", "top_rated",
		"genre:18", "genre:35", "genre:80", "genre:10765", "genre:16", "genre:99", "genre:10764",
		"provider:8", "provider:337", "provider:9", "provider:350", "provider:1899"},
}

// ---------------------------------------------------------------------------
// Lists: groups and by-lines
// ---------------------------------------------------------------------------

// ListGroup: "mine" (the person's own), "mdblist" (kept in step with
// MDBList) or "shared" (fixed copies of curated lists).
func ListGroup(l *store.List) string {
	if l.OwnerID != nil {
		return "mine"
	}
	if l.Source == "follow" || (l.Origin != nil && *l.Origin == "mdblist") {
		return "mdblist"
	}
	return "shared"
}

// ListByLine is the small line under a list's name.
func ListByLine(l *store.List) string {
	switch ListGroup(l) {
	case "mine":
		if l.Kind == "smart" {
			return "Smart list"
		}
		return "Your list"
	case "mdblist":
		if l.OriginUser != nil && *l.OriginUser != "" {
			return "MDBList · " + *l.OriginUser
		}
		return "MDBList"
	}
	return "Shared list"
}

// ---------------------------------------------------------------------------
// Row titles and the catalog of rows
// ---------------------------------------------------------------------------

// RowTitle is a row key's title on a tab; names are the visible lists'.
func RowTitle(tab, key string, names map[int32]string) string {
	kind, arg, _ := strings.Cut(key, ":")
	switch kind {
	case "provider":
		id, _ := strconv.Atoi(arg)
		return "Popular on " + providerName(id)
	case "genre":
		id, _ := strconv.Atoi(arg)
		table := titles.MovieGenres
		if tab == "shows" {
			table = titles.TVGenres
		}
		if n, ok := table[id]; ok {
			return n
		}
		return "Genre"
	case "list":
		id, _ := strconv.Atoi(arg)
		if n, ok := names[int32(id)]; ok {
			return n
		}
		return "List"
	case "recs":
		return "More like this"
	}
	if key == "trending" && tab == "for_you" {
		return "Trending now"
	}
	for _, b := range baseRows {
		if b.Key == key {
			return b.Title
		}
	}
	return key
}

// Catalog is every row key a tab can show.
func Catalog(tab string, lists []store.List) []string {
	var keys []string
	for _, b := range baseRows {
		if (tab == "movies" || tab == "shows") && (b.Key == "ai" || b.Key == "favorites" || b.Key == "rated") {
			continue
		}
		keys = append(keys, b.Key)
	}
	if tab == "movies" || tab == "shows" {
		order := movieGenreOrder
		if tab == "shows" {
			order = tvGenreOrder
		}
		for _, g := range order {
			keys = append(keys, fmt.Sprintf("genre:%d", g))
		}
	}
	for _, p := range Providers {
		keys = append(keys, fmt.Sprintf("provider:%d", p.ID))
	}
	if tab == "for_you" {
		for _, l := range lists {
			keys = append(keys, fmt.Sprintf("list:%d", l.ID))
		}
	}
	return keys
}

// Row is one entry of a tab's rows.
type Row struct {
	Key string `json:"key"`
	On  bool   `json:"on"`
}

// Merge is the saved order merged with the catalog: unknown keys dropped (a
// deleted list), repeats dropped, rows that did not exist when the order
// was saved appended, off. Nothing saved: the tab's defaults on, the rest off.
func Merge(saved []Row, tab string, available []string) []Row {
	if len(saved) == 0 {
		on := map[string]bool{}
		for _, k := range defaults[tab] {
			saved = append(saved, Row{k, true})
			on[k] = true
		}
		for _, k := range available {
			if !on[k] {
				saved = append(saved, Row{k, false})
			}
		}
	}
	allowed := make(map[string]bool, len(available))
	for _, k := range available {
		allowed[k] = true
	}
	seen := map[string]bool{}
	out := make([]Row, 0, len(available))
	for _, r := range saved {
		if allowed[r.Key] && !seen[r.Key] {
			seen[r.Key] = true
			out = append(out, r)
		}
	}
	for _, k := range available {
		if !seen[k] {
			out = append(out, Row{k, false})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The person's choices
// ---------------------------------------------------------------------------

// Service reads and writes the choices.
type Service struct {
	q        *store.Queries
	settings *settings.Service
}

// New makes the service.
func New(q *store.Queries, s *settings.Service) *Service { return &Service{q: q, settings: s} }

// CleanTab is tab when it is one, else "for_you".
func CleanTab(tab string) string {
	for _, t := range Tabs {
		if t == tab {
			return tab
		}
	}
	return "for_you"
}

func settingKey(tab string) string { return "home_rows:" + tab }

// VisibleLists is every list the person sees, in Lists-tab order.
func (s *Service) VisibleLists(ctx context.Context, userID int32) ([]store.List, error) {
	return s.q.VisibleLists(ctx, &userID)
}

// Names maps list ids to names.
func Names(lists []store.List) map[int32]string {
	out := make(map[int32]string, len(lists))
	for _, l := range lists {
		out[l.ID] = l.Name
	}
	return out
}

// Config is a tab's rows for the person. lists may be nil (read here).
func (s *Service) Config(ctx context.Context, userID int32, tab string, lists []store.List) ([]Row, error) {
	tab = CleanTab(tab)
	if lists == nil {
		var err error
		if lists, err = s.VisibleLists(ctx, userID); err != nil {
			return nil, err
		}
	}
	var saved []Row
	if _, err := s.settings.User(ctx, userID, settingKey(tab), &saved); err != nil {
		return nil, err
	}
	return Merge(saved, tab, Catalog(tab, lists)), nil
}

// Save keeps a tab's rows (keys the tab cannot show are dropped).
func (s *Service) Save(ctx context.Context, userID int32, tab string, rows []Row) error {
	tab = CleanTab(tab)
	lists, err := s.VisibleLists(ctx, userID)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, k := range Catalog(tab, lists) {
		allowed[k] = true
	}
	clean := make([]Row, 0, len(rows))
	for _, r := range rows {
		if allowed[r.Key] {
			clean = append(clean, r)
		}
	}
	return s.settings.SetUser(ctx, userID, settingKey(tab), clean)
}

// Reset puts a tab back to its defaults, keeping pinned lists pinned.
func (s *Service) Reset(ctx context.Context, userID int32, tab string) error {
	tab = CleanTab(tab)
	var pinned []int32
	if tab == "for_you" {
		var err error
		if pinned, err = s.Pinned(ctx, userID); err != nil {
			return err
		}
	}
	if err := s.settings.DeleteUser(ctx, userID, settingKey(tab)); err != nil {
		return err
	}
	for _, id := range pinned {
		if err := s.SetPinned(ctx, userID, id, true); err != nil {
			return err
		}
	}
	return nil
}

// Pinned is the lists shown on For You, in their order.
func (s *Service) Pinned(ctx context.Context, userID int32) ([]int32, error) {
	rows, err := s.Config(ctx, userID, "for_you", nil)
	if err != nil {
		return nil, err
	}
	var out []int32
	for _, r := range rows {
		if id, ok := strings.CutPrefix(r.Key, "list:"); ok && r.On {
			n, _ := strconv.Atoi(id)
			out = append(out, int32(n))
		}
	}
	return out, nil
}

// SetPinned shows or hides a list on For You. A newly pinned list goes
// right after the last pinned list, or after "Plan to watch", so it is near
// the top; an unpinned one goes to the end, off.
func (s *Service) SetPinned(ctx context.Context, userID, listID int32, on bool) error {
	key := fmt.Sprintf("list:%d", listID)
	cur, err := s.Config(ctx, userID, "for_you", nil)
	if err != nil {
		return err
	}
	rows := make([]Row, 0, len(cur)+1)
	for _, r := range cur {
		if r.Key != key {
			rows = append(rows, r)
		}
	}
	entry := Row{key, on}
	if !on {
		return s.Save(ctx, userID, "for_you", append(rows, entry))
	}
	anchor := -1
	for i, r := range rows {
		if strings.HasPrefix(r.Key, "list:") && r.On {
			anchor = i
		}
	}
	if anchor == -1 {
		for i, r := range rows {
			if r.Key == "watchlist" {
				anchor = i
				break
			}
		}
	}
	rows = append(rows[:anchor+1], append([]Row{entry}, rows[anchor+1:]...)...)
	return s.Save(ctx, userID, "for_you", rows)
}
