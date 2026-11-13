// Package status keeps what the install's admins (and each person) should
// know about work Movie Night does by itself, so it can be shown instead of
// only logged: the last library sync, each linked
// person's last plays read from Jellyfin, the search catalog's last
// refresh, and MDBList refusing Movie Night's requests.
//
// Each status is a small JSON object in app_settings under "status:<name>".
// The worker writes them (Record); GET /me reads them (Notices for admins,
// PlaysNotice for the person). Nothing here stops anything working.
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
	"strings"
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
// status, kept, undated, ...).
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

// PlaysUndated is a sentence when Jellyfin lists plays without a date (a
// play needs its date to go into the history, so those are not added).
func PlaysUndated(v Value) string {
	n := v.num("undated")
	switch {
	case n == 0:
		return ""
	case n == 1:
		return "1 of your Jellyfin plays has no date in Jellyfin (it was marked played without one), so it isn't in " +
			"your history here. Mark it watched here, with a date, to add it."
	}
	return fmt.Sprintf("%s of your Jellyfin plays have no date in Jellyfin (they were marked played without one), so they "+
		"aren't in your history here. Mark them watched here, with a date, to add them.", thousands(n))
}

// PlaysNotice is what a linked person should know about their plays
// coming in from Jellyfin, or "".
// Its times are in loc, the person's zone.
func (s *Service) PlaysNotice(ctx context.Context, userID int32, linked bool, loc *time.Location) (string, error) {
	if !linked {
		return "", nil
	}
	v, err := s.Get(ctx, Plays(userID))
	if err != nil {
		return "", err
	}
	var parts []string
	for _, p := range []string{PlaysProblem(v, "Your", loc), PlaysUndated(v)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " "), nil
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
