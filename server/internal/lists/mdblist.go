package lists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"movienight/server/internal/clients"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// Source is MDBList's list calls: the client itself, or WithCopy around it.
type Source interface {
	Enabled() bool
	TopLists(ctx context.Context) ([]clients.JSON, error)
	SearchLists(ctx context.Context, query string) ([]clients.JSON, error)
	GetList(ctx context.Context, ref clients.ListRef) (clients.JSON, error)
	ListItems(ctx context.Context, listID string) ([]clients.ListItem, int, error)
}

// ---------------------------------------------------------------------------
// Movie Night's own copy of MDBList's lists
// ---------------------------------------------------------------------------
//
// The top lists, every list opened or searched for, and each one's titles
// are saved as they pass (app_settings, the Python's keys and JSON shapes,
// so an install's existing copy keeps working):
//
//	mdblist:copy:top          {"lists": [summary, ...]}
//	mdblist:copy:list:<id>    {"summary": summary, "entries": [[media, tmdb_id, title, year], ...], "no_tmdb": n}
//
// so the MDBList pages, a list's titles and Follow keep working while
// MDBList is down, refuses Movie Night (its own limit for the key), or the
// install has no key any more. Answering from the copy is the deliberate
// degrade; without a copy MDBList's own error goes on.

const (
	copyTop  = "mdblist:copy:top"
	copyList = "mdblist:copy:list:"
)

type savedList struct {
	Summary clients.JSON `json:"summary,omitempty"`
	Entries [][]any      `json:"entries,omitempty"`
	NoTMDB  int          `json:"no_tmdb,omitempty"`
}

// WithCopy is a Source that saves what MDBList sends and answers from the
// saved copy when MDBList cannot.
type WithCopy struct {
	mdb Source
	s   *Service
}

// WithCopy wraps MDBList's client.
func (s *Service) WithCopy(mdb Source) *WithCopy { return &WithCopy{mdb: mdb, s: s} }

// HasCopy reports whether any copy was saved (the top lists are the first).
func (s *Service) HasCopy(ctx context.Context) (bool, error) {
	var v map[string]any
	return s.settings.App(ctx, copyTop, &v)
}

// Enabled is the client's.
func (w *WithCopy) Enabled() bool { return w.mdb.Enabled() }

// updateList merges patch ("summary", or "entries" and "no_tmdb") into a
// list's saved copy, in one statement (settings.MergeApp): a page saving a
// summary and the nightly refresh saving the titles cannot drop each
// other's half, which the read-then-write before could.
func (w *WithCopy) updateList(ctx context.Context, id any, patch map[string]any) {
	n, ok := idOf(id)
	if !ok {
		return
	}
	if err := w.s.settings.MergeApp(ctx, copyList+strconv.Itoa(n), patch); err != nil {
		// The answer itself is good; only the copy for a later outage is lost.
		w.s.log.WarnContext(ctx, "MDBList copy not saved", "list", n, "err", err)
	}
}

func idOf(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), x > 0
	case int:
		return x, x > 0
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil && n > 0
	}
	return 0, false
}

// summaries is every saved list's details, top lists first, each once.
func (w *WithCopy) summaries(ctx context.Context) ([]clients.JSON, error) {
	var out []clients.JSON
	seen := map[string]bool{}
	add := func(s clients.JSON) {
		k := fmt.Sprint(s["mdblist_id"])
		if s != nil && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	var top struct {
		Lists []clients.JSON `json:"lists"`
	}
	if _, err := w.s.settings.App(ctx, copyTop, &top); err != nil {
		return nil, err
	}
	for _, s := range top.Lists {
		add(s)
	}
	rows, err := w.s.q.AppSettingsWithPrefix(ctx, copyList)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		var sl savedList
		if json.Unmarshal([]byte(r.Value), &sl) == nil && sl.Summary != nil {
			add(sl.Summary)
		}
	}
	return out, nil
}

// TopLists is MDBList's top lists (an empty answer never replaces the copy).
func (w *WithCopy) TopLists(ctx context.Context) ([]clients.JSON, error) {
	top, err := w.mdb.TopLists(ctx)
	if err != nil {
		var saved struct {
			Lists []clients.JSON `json:"lists"`
		}
		found, rerr := w.s.settings.App(ctx, copyTop, &saved)
		if rerr != nil || !found {
			return nil, err
		}
		w.s.log.InfoContext(ctx, "MDBList top lists answered from the copy", "err", err)
		return saved.Lists, nil
	}
	if len(top) > 0 {
		if err := w.s.settings.SetApp(ctx, copyTop, map[string]any{"lists": top}); err != nil {
			w.s.log.WarnContext(ctx, "MDBList copy not saved", "err", err)
		}
	}
	return top, nil
}

// SearchLists searches MDBList, or the saved lists (every word in the
// name, description or owner) when it cannot.
func (w *WithCopy) SearchLists(ctx context.Context, query string) ([]clients.JSON, error) {
	found, err := w.mdb.SearchLists(ctx, query)
	if err != nil {
		saved, rerr := w.summaries(ctx)
		if rerr != nil || len(saved) == 0 {
			return nil, err
		}
		words := strings.Fields(strings.ToLower(query))
		var out []clients.JSON
		for _, s := range saved {
			text := strings.ToLower(fmt.Sprint(s["name"], " ", s["description"], " ", s["user"]))
			all := true
			for _, wd := range words {
				all = all && strings.Contains(text, wd)
			}
			if all {
				out = append(out, s)
			}
		}
		return out, nil
	}
	for _, s := range found {
		s := s
		w.updateList(ctx, s["mdblist_id"], map[string]any{"summary": s})
	}
	return found, nil
}

// GetList is one list's details, from MDBList or the copy.
func (w *WithCopy) GetList(ctx context.Context, ref clients.ListRef) (clients.JSON, error) {
	summary, err := w.mdb.GetList(ctx, ref)
	if err != nil {
		saved, rerr := w.summaries(ctx)
		if rerr == nil {
			for _, s := range saved {
				if (ref.ID != "" && fmt.Sprint(s["mdblist_id"]) == ref.ID) ||
					(ref.Slug != "" && s["slug"] == ref.Slug && s["user"] == ref.User) {
					return s, nil
				}
			}
		}
		return nil, err
	}
	if summary != nil {
		w.updateList(ctx, summary["mdblist_id"], map[string]any{"summary": summary})
	}
	return summary, nil
}

// copyEntries is a list's titles as the copy keeps them: [media, tmdb_id,
// title, year], the Python's tuple shape, so either app reads the other's.
func copyEntries(items []clients.ListItem) [][]any {
	out := make([][]any, len(items))
	for i, it := range items {
		out[i] = []any{it.Media, it.TMDBID, it.Title, it.Year}
	}
	return out
}

// RefreshCopy is the nightly copy (the worker's mdblist job): MDBList's
// top lists and every title of each, saved so the lists pages can answer
// from the copy while MDBList is down or refuses the key. mdb is the plain
// client (never WithCopy: a refresh answered from the copy would only
// rewrite it). Returns how many lists were copied. An empty top-lists
// answer never replaces the copy. It stops at the first failure, keeping
// the rest of the copy as it was: a refusal (MDBList's own limit for the
// key) is expected and only logged; anything else is returned.
func (s *Service) RefreshCopy(ctx context.Context, mdb Source) (int, error) {
	w := s.WithCopy(mdb)
	top, err := mdb.TopLists(ctx)
	if err != nil {
		return 0, s.copyStopped(ctx, 0, err)
	}
	if len(top) > 0 {
		if err := s.settings.SetApp(ctx, copyTop, map[string]any{"lists": top}); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, summary := range top {
		id, ok := idOf(summary["mdblist_id"])
		if !ok {
			continue
		}
		// Every title of each top list (no cap; docs/limits.md).
		items, noTMDB, err := mdb.ListItems(ctx, strconv.Itoa(id))
		if err != nil {
			return n, s.copyStopped(ctx, n, err)
		}
		patch := map[string]any{"summary": summary}
		if len(items) > 0 {
			patch["entries"], patch["no_tmdb"] = copyEntries(items), noTMDB
		}
		w.updateList(ctx, id, patch)
		n++
	}
	return n, nil
}

// refused reports whether err is MDBList refusing the key (or its shared
// breaker saying it recently did): asking again tonight would only be
// refused again.
func refused(err error) bool {
	var r *clients.MDBListRefused
	return errors.As(err, &r)
}

func (s *Service) copyStopped(ctx context.Context, n int, err error) error {
	if refused(err) {
		s.log.WarnContext(ctx, "MDBList copy stopped: MDBList refused (its own limit for the key); the rest of the copy is kept",
			"lists_copied", n)
		return nil
	}
	return fmt.Errorf("MDBList copy stopped after %d lists (the rest of the copy is kept): %w", n, err)
}

// ListItems is a list's titles, from MDBList or the copy.
func (w *WithCopy) ListItems(ctx context.Context, listID string) ([]clients.ListItem, int, error) {
	items, noTMDB, err := w.mdb.ListItems(ctx, listID)
	if err != nil {
		n, ok := idOf(listID)
		if !ok {
			return nil, 0, err
		}
		var sl savedList
		found, rerr := w.s.settings.App(ctx, copyList+strconv.Itoa(n), &sl)
		if rerr != nil || !found || sl.Entries == nil {
			return nil, 0, err
		}
		out := make([]clients.ListItem, 0, len(sl.Entries))
		for _, e := range sl.Entries {
			if len(e) < 4 {
				continue
			}
			media, _ := e[0].(string)
			id, _ := idOf(e[1])
			name, _ := e[2].(string)
			out = append(out, clients.ListItem{Media: media, TMDBID: id, Title: name, Year: e[3]})
		}
		return out, sl.NoTMDB, nil
	}
	if len(items) > 0 {
		w.updateList(ctx, listID, map[string]any{"entries": copyEntries(items), "no_tmdb": noTMDB})
	}
	return items, noTMDB, nil
}

// FollowedIDs maps MDBList ids to this install's list ids.
func (s *Service) FollowedIDs(ctx context.Context) (map[string]int32, error) {
	rows, err := s.q.FollowedMDBListIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int32, len(rows))
	for _, r := range rows {
		out[deref(r.OriginID)] = r.ID
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Following and keeping in step
// ---------------------------------------------------------------------------

type itemRow struct {
	MediaType string `json:"media_type"`
	TMDBID    int    `json:"tmdb_id"`
	Position  int    `json:"position"`
}

// ReplaceItems makes a catalog list's items exactly entries, in order (a
// title twice keeps its first place), in one transaction with the list's
// row locked: the titles gone are deleted, the rest inserted or moved, in
// (media, id) order so concurrent writers lock rows in one order. noTMDB
// is how many titles MDBList sent without a TMDB id (shown as "not shown").
// The titles' names seed the title cache. Returns the list's size.
func (s *Service) ReplaceItems(ctx context.Context, listID int32, entries []clients.ListItem, noTMDB int) (int, error) {
	seen := map[string]bool{}
	var rows []itemRow
	var names []*titles.Meta
	for _, e := range entries {
		if e.Media != "movie" && e.Media != "tv" || e.TMDBID <= 0 {
			continue
		}
		k := titles.Key(e.Media, e.TMDBID)
		if seen[k] {
			continue
		}
		seen[k] = true
		rows = append(rows, itemRow{MediaType: e.Media, TMDBID: e.TMDBID, Position: len(rows)})
		m := &titles.Meta{Key: k, MediaType: e.Media, TMDBID: e.TMDBID, Title: e.Title}
		if y, ok := idOf(e.Year); ok {
			m.Year = &y
		}
		names = append(names, m)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].MediaType != rows[j].MediaType {
			return rows[i].MediaType < rows[j].MediaType
		}
		return rows[i].TMDBID < rows[j].TMDBID
	})
	body, err := json.Marshal(rows)
	if err != nil {
		return 0, err
	}
	if rows == nil {
		body = []byte("[]")
	}
	var shown *int32
	if noTMDB > 0 {
		n := int32(noTMDB)
		shown = &n
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.LockList(ctx, listID); err != nil {
			return err
		}
		if err := q.DropListItemsExcept(ctx, store.DropListItemsExceptParams{ListID: listID, Rows: body}); err != nil {
			return err
		}
		if err := q.ReplaceListItems(ctx, store.ReplaceListItemsParams{ListID: listID, Rows: body}); err != nil {
			return err
		}
		return q.MarkListSynced(ctx, store.MarkListSyncedParams{ID: listID, NotShown: shown})
	})
	if err != nil {
		return 0, err
	}
	s.titles.Remember(ctx, names)
	return len(rows), nil
}

func intp(v any) *int32 {
	if n, ok := idOf(v); ok {
		x := int32(n)
		return &x
	}
	return nil
}

func strpOf(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

// Follow adds an MDBList list to the install's catalog (or picks up the one
// already followed) and fills it.
func (s *Service) Follow(ctx context.Context, mdb Source, ref clients.ListRef) (*store.List, error) {
	summary, err := mdb.GetList(ctx, ref)
	if err != nil {
		return nil, err
	}
	id, ok := idOf(summary["mdblist_id"])
	if summary == nil || !ok {
		return nil, errList(http.StatusNotFound, "That MDBList list wasn't found (it may be private)")
	}
	originID := strconv.Itoa(id)
	l, err := s.q.FollowedMDBList(ctx, &originID)
	if errors.Is(err, pgx.ErrNoRows) {
		name, _ := summary["name"].(string)
		l, err = s.q.CreateMDBList(ctx, store.CreateMDBListParams{Name: name, Description: strpOf(summary["description"]),
			OriginID: &originID, OriginUser: strpOf(summary["user"]), OriginSlug: strpOf(summary["slug"]), Likes: intp(summary["likes"])})
	}
	if err != nil {
		return nil, err
	}
	items, noTMDB, err := mdb.ListItems(ctx, originID)
	if err != nil {
		return nil, err
	}
	if _, err := s.ReplaceItems(ctx, l.ID, items, noTMDB); err != nil {
		return nil, err
	}
	return &l, nil
}

// Sync brings a followed MDBList list in step with MDBList. An empty answer
// for a list that has titles is refused (far likelier a hiccup at MDBList
// than a list emptied on purpose): the titles stay.
func (s *Service) Sync(ctx context.Context, mdb Source, l *store.List) (int, error) {
	if l.Source != "follow" || deref(l.Origin) != "mdblist" || deref(l.OriginID) == "" {
		return 0, errList(http.StatusConflict, "Only MDBList lists are kept in step")
	}
	summary, err := mdb.GetList(ctx, clients.ListRef{ID: *l.OriginID})
	if err != nil {
		return 0, err
	}
	if summary != nil {
		name, _ := summary["name"].(string)
		if name == "" {
			name = l.Name
		}
		desc := strpOf(summary["description"])
		if desc == nil {
			desc = l.Description
		}
		if err := s.q.UpdateMDBListSummary(ctx, store.UpdateMDBListSummaryParams{ID: l.ID, Name: name, Likes: intp(summary["likes"]), Description: desc}); err != nil {
			return 0, err
		}
	}
	items, noTMDB, err := mdb.ListItems(ctx, *l.OriginID)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		has, err := s.q.ListHasItems(ctx, l.ID)
		if err != nil {
			return 0, err
		}
		if has {
			return 0, errList(http.StatusBadGateway, "MDBList sent this list back empty; the titles already here are kept")
		}
	}
	return s.ReplaceItems(ctx, l.ID, items, noTMDB)
}

// SyncAll syncs every followed MDBList list (the nightly job and the admin's
// "Sync now" for lists), one at a time; a list that fails is logged and left
// as it was. Once MDBList refuses, the rest wait for the next run: each
// would only be refused too (the Python asked for every one anyway).
// Returns how many were synced; the error is a database one.
func (s *Service) SyncAll(ctx context.Context, mdb Source) (int, error) {
	ids, err := s.q.FollowedMDBListsToSync(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		l, err := s.q.GetList(ctx, id)
		if err == nil {
			_, err = s.Sync(ctx, mdb, &l)
		}
		if refused(err) {
			s.log.WarnContext(ctx, "MDBList lists sync stopped: MDBList refused (its own limit for the key); the rest wait for the next run",
				"synced", n, "left", len(ids)-n)
			return n, nil
		}
		if err != nil {
			s.log.WarnContext(ctx, "MDBList list sync failed; its titles are kept", "list", id, "err", err)
			continue
		}
		n++
	}
	return n, nil
}
