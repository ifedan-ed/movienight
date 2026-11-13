// Package lists is lists.
//
// A person's own lists (owner_id = them): hand-made lists they edit, and
// smart lists (saved TMDB filters, run live by package rows).
//
// Catalog lists everyone on the install sees (owner_id NULL):
//   - Shared lists: fixed copies of curated lists (source "copy"); nobody
//     edits them.
//   - MDBList lists: followed on MDBList (source "follow") and kept in step
//     with the original (mdblist.go; the worker's nightly sync calls
//     SyncAllMDBList).
//
// Anyone can star a list (a filter on the Lists tab) and show it on Home
// (package homecfg).
//
// What changed from the Python, on purpose:
//   - Adding a title takes the next position under a lock on the list's row
//     (AddItem), so two adds at once can no longer take the same position.
//   - A list's page is cut in SQL (ItemsPage), not after reading every item.
//   - Replacing a catalog list's items (a sync) is one transaction.
package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/apierr"
	"movienight/server/internal/homecfg"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/settings"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// Sorts a list's page can take.
var Sorts = []string{"list", "added", "title", "year", "rating"}

// Service is lists.
type Service struct {
	pool     *pgxpool.Pool
	q        *store.Queries
	titles   *titles.Service
	settings *settings.Service
	home     *homecfg.Service
	log      *slog.Logger
}

// New makes the service.
func New(pool *pgxpool.Pool, q *store.Queries, t *titles.Service, s *settings.Service, h *homecfg.Service, log *slog.Logger) *Service {
	return &Service{pool: pool, q: q, titles: t, settings: s, home: h, log: log}
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func errList(status int, msg string) error { return apierr.New(status, msg) }

// GetVisible is a list the person may see (their own, or a catalog list);
// 404 for anyone else's, so its existence is not given away.
func (s *Service) GetVisible(ctx context.Context, userID, listID int32) (*store.List, error) {
	l, err := s.q.GetList(ctx, listID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && l.OwnerID != nil && *l.OwnerID != userID) {
		return nil, errList(http.StatusNotFound, "List not found")
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// CanEditItems: only the owner, and only a hand-made list.
func CanEditItems(l *store.List, userID int32) bool {
	return l.OwnerID != nil && *l.OwnerID == userID && l.Kind == "list"
}

// CanDelete: the owner, or an admin for a catalog list.
func CanDelete(l *store.List, u *store.User) bool {
	if l.OwnerID != nil {
		return *l.OwnerID == u.ID
	}
	return u.IsAdmin
}

// Count is a list's films and shows.
type Count struct{ Movies, Shows int }

// Counts is each list's films and shows.
func (s *Service) Counts(ctx context.Context, ids []int32) (map[int32]Count, error) {
	out := make(map[int32]Count, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		c := out[r.ListID]
		if r.MediaType == "tv" {
			c.Shows = int(r.N)
		} else {
			c.Movies = int(r.N)
		}
		out[r.ListID] = c
	}
	return out, nil
}

// Starred is the lists the person starred.
func (s *Service) Starred(ctx context.Context, userID int32) (map[int32]bool, error) {
	ids, err := s.q.StarredLists(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[int32]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// SetStar stars or unstars a list for the person.
func (s *Service) SetStar(ctx context.Context, userID, listID int32, on bool) error {
	if on {
		return s.q.StarList(ctx, store.StarListParams{UserID: userID, ListID: listID})
	}
	return s.q.UnstarList(ctx, store.UnstarListParams{UserID: userID, ListID: listID})
}

func set(ids []int32) map[int32]bool {
	out := make(map[int32]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// Summaries is every list the person sees, in Lists-tab order, and the
// collage posters' title keys the title cache still lacks (the caller
// fetches those and asks again).
func (s *Service) Summaries(ctx context.Context, u *store.User) ([]schema.ListSummary, []string, error) {
	lists, err := s.home.VisibleLists(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}
	pinned, err := s.home.Pinned(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}
	stars, err := s.Starred(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int32, len(lists))
	for i, l := range lists {
		ids[i] = l.ID
	}
	counts, err := s.Counts(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	first := map[int32][]string{}
	var all []string
	if len(ids) > 0 {
		rows, err := s.q.ListCollageKeys(ctx, ids)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			k := titles.Key(r.MediaType, int(r.TmdbID))
			first[r.ListID] = append(first[r.ListID], k)
			all = append(all, k)
		}
	}
	metas, err := s.titles.GetMany(ctx, all)
	if err != nil {
		return nil, nil, err
	}
	pin := set(pinned)
	var missing []string
	out := make([]schema.ListSummary, 0, len(lists))
	for i := range lists {
		l := &lists[i]
		posters := []string{}
		for _, k := range first[l.ID] {
			m, ok := metas[k]
			if !ok {
				missing = append(missing, k)
				continue
			}
			if m.PosterPath != nil {
				posters = append(posters, *m.PosterPath)
			}
		}
		c := counts[l.ID]
		out = append(out, schema.ListSummary{
			ID: l.ID, Name: l.Name, Description: deref(l.Description), Kind: l.Kind, Source: l.Source, Origin: l.Origin,
			Group: homecfg.ListGroup(l), By: homecfg.ListByLine(l), Likes: l.Likes, Count: CountOf(l, c),
			Movies: c.Movies, Shows: c.Shows, Posters: posters, Pinned: pin[l.ID], Starred: stars[l.ID],
			Editable: CanEditItems(l, u.ID), Deletable: CanDelete(l, u),
		})
	}
	return out, missing, nil
}

// CountOf is a list's size; nil for a smart list (TMDB's discover has no end).
func CountOf(l *store.List, c Count) *int {
	if l.Kind == "smart" {
		return nil
	}
	n := c.Movies + c.Shows
	return &n
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func trimmedOrNil(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	if t == "" {
		return nil
	}
	return &t
}

// ---------------------------------------------------------------------------
// A person's own lists
// ---------------------------------------------------------------------------

// CreateList makes a hand-made list.
func (s *Service) CreateList(ctx context.Context, userID int32, name string, description *string) (*store.List, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errList(http.StatusUnprocessableEntity, "A list needs a name")
	}
	l, err := s.q.CreateList(ctx, store.CreateListParams{OwnerID: &userID, Name: name, Description: trimmedOrNil(description), Kind: "list"})
	return &l, err
}

// CreateSmart makes a smart list from TMDB filters (cleaned first).
func (s *Service) CreateSmart(ctx context.Context, userID int32, name string, filters map[string]any) (*store.List, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errList(http.StatusUnprocessableEntity, "A list needs a name")
	}
	raw, err := jsonString(CleanFilters(filters))
	if err != nil {
		return nil, err
	}
	l, err := s.q.CreateList(ctx, store.CreateListParams{OwnerID: &userID, Name: name, Kind: "smart", FiltersJson: &raw})
	return &l, err
}

// Rename changes an own list's name (when not blank) and description.
func (s *Service) Rename(ctx context.Context, l *store.List, name, description *string) error {
	newName := l.Name
	if name != nil && strings.TrimSpace(*name) != "" {
		newName = strings.TrimSpace(*name)
	}
	desc := l.Description
	if description != nil {
		desc = trimmedOrNil(description)
	}
	return s.q.RenameList(ctx, store.RenameListParams{ID: l.ID, Name: newName, Description: desc})
}

// Delete removes a list with its items and stars.
func (s *Service) Delete(ctx context.Context, listID int32) error { return s.q.DeleteList(ctx, listID) }

// AddItem appends a title to a list; false when it was already there.
// The list's row is locked for the transaction, so concurrent adds take
// one position each, one after the other (the Python's max+1 race let two
// adds share a position). A unique (list_id, position) index was not used:
// lists the Python wrote may already hold repeated positions, which would
// make that migration fail on a live install.
func (s *Service) AddItem(ctx context.Context, listID int32, media string, tmdbID int) (bool, error) {
	var added int32
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.LockList(ctx, listID); err != nil {
			return err
		}
		var err error
		added, err = q.AddListItem(ctx, store.AddListItemParams{ListID: listID, MediaType: media, TmdbID: int32(tmdbID)})
		return err
	})
	return added > 0, err
}

// RemoveItem takes a title out of a list.
func (s *Service) RemoveItem(ctx context.Context, listID int32, media string, tmdbID int) error {
	_, err := s.q.RemoveListItem(ctx, store.RemoveListItemParams{ListID: listID, MediaType: media, TmdbID: int32(tmdbID)})
	return err
}

// CopyToMine makes the person's own editable copy of any list they see.
func (s *Service) CopyToMine(ctx context.Context, userID int32, l *store.List) (*store.List, error) {
	var mine store.List
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		mine, err = q.CreateList(ctx, store.CreateListParams{OwnerID: &userID, Name: l.Name, Description: l.Description, Kind: "list"})
		if err != nil {
			return err
		}
		return q.CopyListItems(ctx, store.CopyListItemsParams{ToList: mine.ID, FromList: l.ID})
	})
	return &mine, err
}

// Memberships is the person's own hand-made lists and whether the title
// is in each (the title page's "Add to list"; curated lists are fixed).
func (s *Service) Memberships(ctx context.Context, userID int32, media string, tmdbID int) ([]schema.ListMembership, error) {
	rows, err := s.q.Memberships(ctx, store.MembershipsParams{UserID: &userID, Media: media, TmdbID: int32(tmdbID)})
	if err != nil {
		return nil, err
	}
	out := make([]schema.ListMembership, len(rows))
	for i, r := range rows {
		out[i] = schema.ListMembership{ID: r.ID, Name: r.Name, InList: r.InList, Source: r.Source}
	}
	return out, nil
}

// ItemsPage is one page of a hand-made or catalog list's title keys
// (media "" for both, sort one of Sorts) and how many there are in all.
func (s *Service) ItemsPage(ctx context.Context, listID int32, media, sort string, limit, offset int) ([]string, int, error) {
	ok := false
	for _, v := range Sorts {
		ok = ok || v == sort
	}
	if !ok {
		sort = "list"
	}
	rows, err := s.q.ListItemsPage(ctx, store.ListItemsPageParams{ListID: listID, Media: media, Sort: sort,
		Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, 0, err
	}
	keys := make([]string, len(rows))
	total := 0
	for i, r := range rows {
		keys[i] = titles.Key(r.MediaType, int(r.TmdbID))
		total = int(r.Total)
	}
	// An empty page (past the end) has no window count to read: total is
	// then 0, and the caller's "more" is false either way.
	return keys, total, nil
}

// ---------------------------------------------------------------------------
// Smart-list filters
// ---------------------------------------------------------------------------

// SmartSorts are the TMDB orders a smart list (and Discover) may use.
var SmartSorts = map[string]bool{"popularity.desc": true, "vote_average.desc": true, "primary_release_date.desc": true,
	"first_air_date.desc": true, "revenue.desc": true}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CleanFilters keeps only what a smart list (or Discover) understands, in
// the types it uses: whatever was saved or sent, a filter that is not one
// is dropped rather than passed on to TMDB.
func CleanFilters(f map[string]any) map[string]any {
	out := map[string]any{"media": "movie"}
	if f["media"] == "tv" {
		out["media"] = "tv"
	}
	for _, k := range []string{"genre", "provider"} {
		if s := fmt.Sprint(f[k]); f[k] != nil && digits(s) {
			out[k] = s
		}
	}
	for _, k := range []string{"year_from", "year_to", "runtime_min", "runtime_max"} {
		if f[k] != nil && f[k] != "" {
			if n, ok := asInt(f[k]); ok {
				out[k] = n
			}
		}
	}
	if f["min_rating"] != nil && f["min_rating"] != "" {
		if r, ok := asFloat(f["min_rating"]); ok {
			out["min_rating"] = r
		}
	}
	if lang, ok := f["language"].(string); ok && lang != "" && isAlpha(lang) {
		if len(lang) > 5 {
			lang = lang[:5]
		}
		out["language"] = strings.ToLower(lang)
	}
	if sort, ok := f["sort"].(string); ok && SmartSorts[sort] {
		out["sort"] = sort
	}
	return out
}

func isAlpha(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// TMDBFilters turns cleaned filters into TMDB discover parameters.
func TMDBFilters(f map[string]any, watchRegion string) map[string]string {
	tv := f["media"] == "tv"
	p := map[string]string{}
	if g, ok := f["genre"].(string); ok && g != "" {
		p["with_genres"] = g
	}
	if pr, ok := f["provider"].(string); ok && pr != "" {
		p["with_watch_providers"] = pr
		p["watch_region"] = watchRegion
	}
	date := "primary_release_date"
	if tv {
		date = "first_air_date"
	}
	num := func(k string) (int, bool) { n, ok := asInt(f[k]); return n, ok && n != 0 }
	if y, ok := num("year_from"); ok {
		p[date+".gte"] = fmt.Sprintf("%d-01-01", y)
	}
	if y, ok := num("year_to"); ok {
		p[date+".lte"] = fmt.Sprintf("%d-12-31", y)
	}
	if n, ok := num("runtime_min"); ok {
		p["with_runtime.gte"] = strconv.Itoa(n)
	}
	if n, ok := num("runtime_max"); ok {
		p["with_runtime.lte"] = strconv.Itoa(n)
	}
	if r, ok := asFloat(f["min_rating"]); ok && r != 0 {
		p["vote_average.gte"] = strconv.FormatFloat(r, 'f', -1, 64)
		// A minimum rating also needs 100 votes, so a 9/10 from three
		// people doesn't count (docs/limits.md).
		p["vote_count.gte"] = "100"
	}
	if l, ok := f["language"].(string); ok && l != "" {
		p["with_original_language"] = l
	}
	sort, _ := f["sort"].(string)
	if sort == "" {
		sort = "popularity.desc"
	}
	if tv && sort == "primary_release_date.desc" {
		sort = "first_air_date.desc"
	}
	if !tv && sort == "first_air_date.desc" {
		sort = "primary_release_date.desc"
	}
	p["sort_by"] = sort
	return p
}
