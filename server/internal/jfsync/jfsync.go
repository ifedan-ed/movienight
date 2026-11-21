// Package jfsync brings Jellyfin's state into Movie Night, additively:
//
//   - the install's library -> library_items and the library search index.
//     Only a complete, successful read of the whole library changes it, and
//     even then one that lists nothing never empties it (Library);
//   - each linked person's Jellyfin plays -> their history, source
//     "jellyfin" (Plays). A play that matches a watch already marked here
//     (same title or episode, within ClaimWindow) is folded into that row
//     instead of becoming a second one.
//
// The worker calls both on its schedule; the Jellyfin webhook asks for one
// person's plays sooner through a queue (webhook.go), never from the API
// process itself.
//
// Each person's sync is one transaction (the Python committed once at the
// end too, but the plugin's events committed per event and a failure midway
// through a sync of many people could leave others half done). And every
// write is a pgx batch: one round trip for a whole library or a whole
// history of plays.
package jfsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
	"movienight/server/internal/tasks"
)

// ClaimWindow: a Jellyfin play this close to a watch marked here (by hand
// or "Finished it?") is that same watch. ClaimHorizon: only marks this
// recent are looked at; an older Jellyfin play becomes its own row.
// Both are in docs/limits.md.
const (
	ClaimWindow  = 36 * time.Hour
	ClaimHorizon = 60 * 24 * time.Hour
)

// Service runs the syncs.
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	clients *clients.Set
	status  *status.Service
	cache   *cache.Cache
	ownerJF string // JELLYFIN_USER_ID: whose view of the library is read
	kv      valkey.Client
	queue   *tasks.Queue // a person's plays sync, soon (webhook.go)
	marks   *tasks.Queue // a person's episode marks for Jellyfin (marks.go)
	log     *slog.Logger
}

// New makes the service. st records each sync's outcome for Profile; c is
// the shared cache, whose Jellyfin answers a sync makes stale.
func New(pool *pgxpool.Pool, kv valkey.Client, cl *clients.Set, st *status.Service, c *cache.Cache,
	ownerJellyfinUser string, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), clients: cl, status: st, cache: c, ownerJF: ownerJellyfinUser,
		kv: kv, queue: tasks.New(kv, QueueName), marks: tasks.New(kv, MarksQueue), log: log}
}

// ParseTime reads a Jellyfin or ISO 8601 time; a time without a zone is
// UTC (the Python's parse_time). ok false for "" or anything unreadable.
func ParseTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05.999999999Z07"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// PyISO writes t as Python's datetime.isoformat() did: microseconds (only
// when not zero) and the offset as +HH:MM. Play references written by the
// Python app ("<item>@<isoformat>") must be written the same way here, or
// every play it recorded would be added a second time by the first Go sync.
func PyISO(t time.Time) string {
	t = t.Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05-07:00")
	}
	return t.Format("2006-01-02T15:04:05.000000-07:00")
}

// PlayRef is one Jellyfin play: the item and when it was last played. The
// plugin and the plays sync build the same reference, so a play is recorded
// once.
func PlayRef(itemID string, when time.Time) string {
	return NormalizeID(itemID) + "@" + PyISO(when)
}

// NormalizeID is a Jellyfin id as kept here: no dashes, lower case.
func NormalizeID(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }

func tmdbOf(item jsonx.Obj) *int32 {
	raw := jsonx.Str(jsonx.Child(item, "ProviderIds"), "Tmdb")
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil
	}
	v := int32(n)
	return &v
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

// NotRead is a library read that was not complete (or listed nothing at
// all), so the library was left exactly as it was. Its text says why in
// plain words; admins see it in Profile.
type NotRead struct{ Msg string }

func (e *NotRead) Error() string { return e.Msg }

// LibraryResult is what one sync did.
type LibraryResult struct {
	Items, Removed, Indexed, WithoutID int
}

func (r LibraryResult) String() string {
	return fmt.Sprintf("%d titles, %d removed, %d indexed, %d without an id", r.Items, r.Removed, r.Indexed, r.WithoutID)
}

func (s *Service) notRead(ctx context.Context, problem string) error {
	kept, err := s.q.SyncLibraryCount(ctx)
	if err != nil {
		return err
	}
	if err := s.status.Record(ctx, status.Library, status.Value{
		"ok": false, "at": status.Now(), "problem": problem, "kept": kept,
	}); err != nil {
		return err
	}
	s.log.WarnContext(ctx, "library sync: nothing changed", "problem", problem, "kept", kept)
	return &NotRead{Msg: fmt.Sprintf("%s. Nothing was removed; the library was left as it was (%s titles)", problem, thousands(int(kept)))}
}

// Library makes library_items (and the library search index) match
// Jellyfin. The whole library is read first, page by page with no cap; only
// after a complete read are titles Jellyfin no longer lists removed. A read
// that fails or comes back incomplete changes nothing, and nor does a
// complete read listing no titles while titles are kept here (far likelier
// a broken answer, or a JELLYFIN_USER_ID who lost access, than a library
// emptied on purpose): *NotRead then. The search index is updated after the
// table; its failure is returned (the table stands), never swallowed.
func (s *Service) Library(ctx context.Context) (LibraryResult, error) {
	var res LibraryResult
	items, err := s.clients.Jellyfin.LibraryItems(ctx, s.ownerJF)
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		var je *clients.JellyfinError
		why := "Jellyfin couldn't be read (" + err.Error() + ")"
		var inc *clients.IncompleteRead
		if errors.As(err, &inc) {
			why = inc.Msg
		} else if errors.As(err, &je) {
			why = je.Msg
		}
		return res, s.notRead(ctx, "the library read was incomplete: "+why)
	}
	kept, err := s.q.SyncLibraryCount(ctx)
	if err != nil {
		return res, err
	}
	now := time.Now().UTC()
	params := make([]store.SyncLibraryUpsertParams, 0, len(items))
	seen := make([]string, 0, len(items))
	for _, it := range items {
		id := jsonx.Str(it, "Id")
		if id == "" {
			res.WithoutID++ // cannot be stored without its id; counted and logged
			continue
		}
		media := "movie"
		if jsonx.Str(it, "Type") == "Series" {
			media = "tv"
		}
		var added *time.Time
		if t, ok := ParseTime(jsonx.Str(it, "DateCreated")); ok {
			added = &t
		}
		params = append(params, store.SyncLibraryUpsertParams{JellyfinID: id, MediaType: media, TmdbID: tmdbOf(it),
			Title: jsonx.Str(it, "Name"), Year: jsonx.IntPtr(it, "ProductionYear"), DateAdded: added, SyncedAt: now})
		seen = append(seen, id)
	}
	if kept > 0 && len(seen) == 0 {
		if len(items) == 0 {
			return res, s.notRead(ctx, "Jellyfin listed no movies or series at all")
		}
		return res, s.notRead(ctx, fmt.Sprintf("Jellyfin listed %s items, none of them with an id", thousands(len(items))))
	}
	removed, err := s.applyLibrary(ctx, params, seen)
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		s.log.ErrorContext(ctx, "library sync: saving failed", "err", err)
		return res, s.notRead(ctx, "saving the library failed ("+err.Error()+")")
	}
	res.Items, res.Removed = len(seen), int(removed)
	if res.WithoutID > 0 {
		s.log.WarnContext(ctx, "library sync: items without an id were not stored", "count", res.WithoutID)
	}
	at := status.Now()
	if err := s.status.Record(ctx, status.Library, status.Value{"ok": true, "at": at, "last_good_at": at,
		"problem": nil, "items": res.Items, "removed": res.Removed, "no_id": res.WithoutID, "kept": res.Items}); err != nil {
		return res, err
	}
	// The library changed: every cached Jellyfin answer may be out of date
	// (Up next, a season's episodes), as the Python's cache.invalidate("jf:").
	s.cache.ForgetPrefix(ctx, cache.JellyfinPrefix)
	n, err := s.index(ctx, items, seen)
	res.Indexed = n
	if err != nil {
		return res, fmt.Errorf("the library was saved, but the search index update failed: %w", err)
	}
	return res, nil
}

// applyLibrary writes a complete read in one transaction: every title, and
// what Jellyfin no longer lists removed. A failure changes nothing.
func (s *Service) applyLibrary(ctx context.Context, params []store.SyncLibraryUpsertParams, seen []string) (int64, error) {
	var removed int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := execBatch(q.SyncLibraryUpsert(ctx, params)); err != nil {
			return err
		}
		n, err := q.SyncLibraryDeleteMissing(ctx, seen)
		removed = n
		return err
	})
	return removed, err
}

// index sends only new or changed documents to the search index (the
// embedder then works only on what changed) and drops what is gone.
func (s *Service) index(ctx context.Context, items []jsonx.Obj, seen []string) (int, error) {
	m := s.clients.Meili
	if err := m.EnsureIndex(ctx, clients.LibraryIndex); err != nil {
		return 0, err
	}
	current, err := m.FieldValues(ctx, clients.LibraryIndex, "searchText")
	if err != nil {
		return 0, err
	}
	var changed []clients.JSON
	for _, it := range items {
		d := clients.LibraryDoc(it)
		if d == nil {
			continue
		}
		if cur, ok := current[jsonx.Str(d, "id")]; !ok || cur != d["searchText"] {
			changed = append(changed, d)
		}
	}
	for i := 0; i < len(changed); i += 500 {
		if _, err := m.ReplaceDocuments(ctx, clients.LibraryIndex, changed[i:min(i+500, len(changed))]); err != nil {
			return i, err
		}
	}
	keep := make(map[string]bool, len(seen))
	for _, id := range seen {
		keep[id] = true
	}
	var gone []string
	for id := range current {
		// Movie Night's own libraries' documents are theirs to keep.
		if !keep[id] && !strings.HasPrefix(id, clients.LibraryOwnPrefix) {
			gone = append(gone, id)
		}
	}
	return len(changed), m.DeleteDocuments(ctx, clients.LibraryIndex, gone)
}

// execBatch runs a sqlc batch and returns its first error.
func execBatch(br interface {
	Exec(func(int, error))
	Close() error
}) error {
	var first error
	br.Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	if err := br.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// thousands writes 12345 as "12,345" (the status texts are the Python's).
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
