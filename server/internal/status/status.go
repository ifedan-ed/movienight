// Package status keeps what the install's admins (and each person) should
// know about work Movie Night does by itself, so it can be shown instead of
// only logged: the last library sync, each linked
// person's last plays read from Jellyfin, the search catalog's last
// refresh, and MDBList refusing Movie Night's requests.
//
// Each status is a small JSON object in app_settings under "status:<name>".
// The worker writes them (Record); GET /me reads them (Notices for admins,
// PlaysNotice and Undated for the person). Nothing here stops anything working.
//
// Shared: the library and plays syncs (internal/jfsync, run by the worker
// and by "Sync now") call Record; disconnecting Jellyfin calls Forget; the
// catalog build keeps its own record under CatalogKey.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/settings"
)

const (
	prefix  = "status:"
	Library = "library_sync"
	// CatalogKey is where the catalog build keeps its last run (not under
	// "status:", as the Python had it).
	CatalogKey = "catalog_status"
)

// Plays is the name of one person's plays-read status.
func Plays(userID int32) string { return fmt.Sprintf("plays:%d", userID) }

// Value is one status: free-form fields (ok, at, problem, last_good_at,
// status, kept, undated, undated_items, ...).
type Value map[string]any

func (v Value) str(k string) string { s, _ := v[k].(string); return s }

func (v Value) num(k string) int { f, _ := v[k].(float64); return int(f) }

// failed is ok == false exactly (a status without "ok" is not a failure).
func (v Value) failed() bool { ok, has := v["ok"].(bool); return has && !ok }

// Service reads and writes statuses.
type Service struct {
	settings *settings.Service
	mdblist  func() *clients.MDBList
	log      *slog.Logger
}

// New makes the service. mdblist is read per call (tests swap clients).
func New(s *settings.Service, mdblist func() *clients.MDBList, log *slog.Logger) *Service {
	return &Service{settings: s, mdblist: mdblist, log: log}
}

// Get reads a status; nil when there is none.
func (s *Service) Get(ctx context.Context, name string) (Value, error) {
	var v Value
	found, err := s.settings.App(ctx, prefix+name, &v)
	if err != nil || !found {
		return nil, err
	}
	return v, nil
}

// Record merges fields into a status and saves it, in one statement
// (settings.MergeApp): the library sync and a person's plays read can both
// write at once without losing each other's fields. A nil value stores
// JSON null, which the readers take as "no problem".
func (s *Service) Record(ctx context.Context, name string, fields Value) error {
	return s.settings.MergeApp(ctx, prefix+name, fields)
}

// Forget drops a status (a person who disconnects Jellyfin).
func (s *Service) Forget(ctx context.Context, name string) error {
	return s.settings.DeleteApp(ctx, prefix+name)
}

// Now is a timestamp as the records keep it: Python's isoformat() of a UTC
// time, so records the Python wrote and these read the same (When parses
// both).
func Now() string { return ISO(time.Now()) }

// ISO formats t as Python's datetime.isoformat() of a UTC time.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000-07:00") }

// When is "Oct 9, 14:30" in loc, the zone of the person reading the
// sentence (rw.Deps.Zone); "never" for none.
func When(iso string, loc *time.Location) string {
	if iso == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		// The Python wrote isoformat() with microseconds and +00:00.
		if t, err = time.Parse("2006-01-02T15:04:05.999999-07:00", iso); err != nil {
			return iso
		}
	}
	return t.In(loc).Format("Jan 2, 15:04")
}

// PlaysProblem is a sentence when a person's last plays read failed, its
// times in loc (the reader's zone).
func PlaysProblem(v Value, whose string, loc *time.Location) string {
	if v == nil || !v.failed() {
		return ""
	}
	hint := ""
	if v.num("status") == 404 {
		hint = " A 404 usually means the Jellyfin account was deleted or renamed: connect it again in Profile → Jellyfin."
	}
	last := ""
	if l := v.str("last_good_at"); l != "" {
		last = " (last complete read: " + When(l, loc) + ")"
	}
	return fmt.Sprintf("%s Jellyfin plays couldn't be read at %s: %s. Nothing was added; Movie Night tries again every 5 minutes by itself%s.%s",
		whose, When(v.str("at"), loc), v.str("problem"), last, hint)
}

// UndatedItem is one play Jellyfin lists without a date: it was marked
// played there by hand, so there is no day to put it on in the history.
// The person sees each by name in Profile, with a way to add it with a
// date or to say it wasn't watched.
type UndatedItem struct {
	ID           string  `json:"id"`         // Jellyfin's item id
	MediaType    string  `json:"media_type"` // "movie" or "tv" (an episode is its show)
	TMDBID       *int32  `json:"tmdb_id"`    // nil when Jellyfin has no TMDB id for it
	Title        string  `json:"title"`      // the film, or the show
	Season       *int32  `json:"season,omitempty"`
	Episode      *int32  `json:"episode,omitempty"`
	EpisodeTitle *string `json:"episode_title,omitempty"`
}

// MaxUndated is how many undated plays a status keeps by name. A library
// marked played wholesale could list thousands; past this many, the list
// is the first ones and Profile says how many more there are.
const MaxUndated = 100

// UndatedHidden is the person's setting (user_settings) holding the
// undated plays they dealt with: a JSON object of Jellyfin item id to the
// time it was hidden. It is kept apart from the plays status, which every
// sync rewrites and a disconnect drops, so an answer holds for good.
const UndatedHidden = "plays_undated_hidden"

// Undated is what the person still has to deal with: the undated plays
// the last read found, less the ones they hid, and how many the status
// counted in all that are not hidden (more than the list when the read
// found over MaxUndated).
func (s *Service) Undated(ctx context.Context, userID int32) ([]UndatedItem, int, error) {
	v, err := s.Get(ctx, Plays(userID))
	if err != nil || v == nil {
		return nil, 0, err
	}
	hidden, err := s.hidden(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	all := undatedItems(v)
	out := make([]UndatedItem, 0, len(all))
	for _, it := range all {
		if _, gone := hidden[it.ID]; !gone {
			out = append(out, it)
		}
	}
	// Those past the list can't be told apart: they all count.
	total := len(out) + max(0, v.num("undated")-len(all))
	return out, total, nil
}

// HideUndated keeps that the person dealt with one undated play (added it
// with a date, or said they didn't watch it), so it is never listed again.
// Only a play the last read listed can be hidden, which keeps the setting
// as small as the person's own list. found is false for any other id.
func (s *Service) HideUndated(ctx context.Context, userID int32, itemID string) (found bool, err error) {
	v, err := s.Get(ctx, Plays(userID))
	if err != nil || v == nil {
		return false, err
	}
	for _, it := range undatedItems(v) {
		if it.ID == itemID {
			// One merge statement: two quick answers can't drop each other.
			return true, s.settings.MergeUser(ctx, userID, UndatedHidden, map[string]any{itemID: Now()})
		}
	}
	return false, nil
}

// HiddenUndated is the set of Jellyfin item ids the person dealt with.
func (s *Service) HiddenUndated(ctx context.Context, userID int32) (map[string]bool, error) {
	h, err := s.hidden(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(h))
	for id := range h {
		out[id] = true
	}
	return out, nil
}

func (s *Service) hidden(ctx context.Context, userID int32) (map[string]any, error) {
	var h map[string]any
	if _, err := s.settings.User(ctx, userID, UndatedHidden, &h); err != nil {
		return nil, err
	}
	return h, nil
}

// undatedItems reads the list a plays read stored. A status written before
// the list existed has only the count: it lists nothing until the next
// read, five minutes on.
func undatedItems(v Value) []UndatedItem {
	raw, ok := v["undated_items"]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []UndatedItem
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// PlaysNotice is what a linked person should know about their plays
// coming in from Jellyfin, or "": a read that failed. Plays without a
// date are listed by name instead (Undated).
// Its times are in loc, the person's zone.
func (s *Service) PlaysNotice(ctx context.Context, userID int32, linked bool, loc *time.Location) (string, error) {
	if !linked {
		return "", nil
	}
	v, err := s.Get(ctx, Plays(userID))
	if err != nil {
		return "", err
	}
	return PlaysProblem(v, "Your", loc), nil
}

// Notices are plain sentences for the admins: only what needs them. Their
// times are in loc, the zone of the admin reading them.
func (s *Service) Notices(ctx context.Context, loc *time.Location) ([]string, error) {
	out := []string{}
	lib, err := s.Get(ctx, Library)
	if err != nil {
		return nil, err
	}
	if lib != nil && lib.failed() {
		out = append(out, fmt.Sprintf("The Jellyfin library sync at %s didn't finish: %s. Nothing was removed; Movie Night's "+
			"library was left as it was (%s titles). Last complete sync: %s.",
			When(lib.str("at"), loc), lib.str("problem"), thousands(lib.num("kept")), When(lib.str("last_good_at"), loc)))
	}
	rows, err := s.settings.Queries().PlaysStatuses(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		var v Value
		if json.Unmarshal([]byte(r.Value), &v) != nil {
			s.log.WarnContext(ctx, "a plays status is not valid JSON", "user", r.ID)
			continue
		}
		// By display name: this is shown to admins, never logged.
		name := r.Username
		if r.DisplayName != nil && *r.DisplayName != "" {
			name = *r.DisplayName
		}
		if p := PlaysProblem(v, fmt.Sprintf("%s's (user %d)", name, r.ID), loc); p != "" {
			out = append(out, p)
		}
	}
	var cat Value
	if found, err := s.settings.App(ctx, CatalogKey, &cat); err != nil {
		return nil, err
	} else if found && cat.num("failed_pages") > 0 {
		n := cat.num("failed_pages")
		reads := "reads"
		if n == 1 {
			reads = "read"
		}
		out = append(out, fmt.Sprintf("The search catalog's refresh at %s couldn't read everything from TMDB (%s %s failed; "+
			"the log says which). New and changed titles were added; none was removed. It runs again on Sunday at 03:00.",
			When(cat.str("at"), loc), thousands(n), reads))
	}
	refused, err := s.mdblist().Refusal(ctx)
	if err != nil {
		return nil, err
	}
	if refused != nil {
		out = append(out, fmt.Sprintf("MDBList refused Movie Night's requests at %s (%s: its own limit for this install's key). "+
			"Title pages show the scores kept from before, with a note; lists keep their titles. Movie Night asks again every "+
			"10 minutes by itself; this goes away once MDBList answers.", When(refused["at"], loc), refused["why"]))
	}
	return out, nil
}

// thousands writes 5000 as "5,000".
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
