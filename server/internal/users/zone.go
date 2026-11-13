package users

import (
	"context"
	_ "embed" // zone.tab
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the zone names are checked against the database built into the binary, the same in every image

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"movienight/server/internal/store"
)

// MaxTimeZoneLength bounds a reported zone name. The longest IANA name is
// about 30 characters ("America/Argentina/ComodRivadavia").
const MaxTimeZoneLength = 64

// invalidParameterValue is Postgres's error code for a time zone it does
// not know (22023).
const invalidParameterValue = "22023"

// ErrBadTimeZone: the name is not an IANA time zone this server knows.
var ErrBadTimeZone = errors.New("time_zone must be an IANA time zone name (e.g. Europe/London)")

// zones caches loaded zones by name. time.LoadLocation parses the zone's
// data on every call, and a page may ask for the person's zone several
// times; there are only a few hundred names, so the cache stays small.
var zones sync.Map // name -> *time.Location

// ParseTimeZone checks a reported zone name and loads it. "Local" (the
// server's own zone, whatever the container has) and "" are refused:
// they name no place, and accepting them would make a person's days depend
// on where the server runs.
func ParseTimeZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" || len(name) > MaxTimeZoneLength {
		return nil, ErrBadTimeZone
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrBadTimeZone
	}
	zones.Store(name, loc)
	return loc, nil
}

// Location is the zone a person's own dates and times are in: their days in
// history, "this year", the daily shuffles and row rotation, a watch date
// typed without a time, the times written in sentences shown to them, and
// their digest periods. It is users.time_zone: the zone their browser or
// phone last reported (PUT /api/v1/me/time-zone), or the one they picked in
// Profile (PUT /api/v1/me/time-zone/choice, time_zone_manual), which
// reports then leave alone.
//
// The install's TIMEZONE (install) is the documented default for a person
// who has never reported one (time_zone NULL: a new account before its first
// page load, or someone who only uses Kodi). It is chosen here, in one
// place, and nowhere else; docs/configuration.md says so.
//
// A stored name this build cannot load is treated the same as never
// reported. Every name is checked on write, so this happens only when a
// newer build (a newer zone database) stored a zone that was added since;
// the person's next page load reports their zone again.
func Location(u store.User, install *time.Location) *time.Location {
	return LocationOf(u.TimeZone, install)
}

// LocationOf is Location for a users.time_zone value read on its own (the
// digest schedule reads it without the rest of the row).
func LocationOf(timeZone *string, install *time.Location) *time.Location {
	if timeZone == nil {
		return install
	}
	loc, err := ParseTimeZone(*timeZone)
	if err != nil {
		return install
	}
	return loc
}

// ErrTimeZoneChosen: the person picked their zone by hand in Profile, so a
// device's automatic report does not replace it (they switch back to
// "Automatic" for that).
var ErrTimeZoneChosen = errors.New("your time zone was chosen in Profile; set it back to Automatic there to follow this device")

// ReportTimeZone stores the zone a person's browser or phone reports, after
// checking it (ErrBadTimeZone, nothing changed). A zone they chose by hand
// is kept (ErrTimeZoneChosen): the report is the device's opinion, the
// choice is the person's.
func (s *Service) ReportTimeZone(ctx context.Context, id int32, name string) (store.User, error) {
	loc, err := ParseTimeZone(name)
	if err != nil {
		return store.User{}, err
	}
	u, err := s.q.ReportUserTimeZone(ctx, store.ReportUserTimeZoneParams{ID: id, TimeZone: loc.String()})
	if errors.Is(err, pgx.ErrNoRows) {
		// The person is signed in, so their row exists: only the
		// time_zone_manual condition can have excluded it.
		return store.User{}, ErrTimeZoneChosen
	}
	return u, zoneErr(err)
}

// ChooseTimeZone is the person's choice in Profile: manual true keeps name
// until they choose again, whatever their devices report; manual false is
// "Automatic" again, starting from name, the zone the device they chose it
// on runs in (so their days are right at once, not after the next report).
func (s *Service) ChooseTimeZone(ctx context.Context, id int32, name string, manual bool) (store.User, error) {
	loc, err := ParseTimeZone(name)
	if err != nil {
		return store.User{}, err
	}
	u, err := s.q.ChooseUserTimeZone(ctx, store.ChooseUserTimeZoneParams{ID: id, TimeZone: loc.String(), Manual: manual})
	return u, zoneErr(err)
}

// zoneErr maps Postgres refusing a zone name to ErrBadTimeZone: Go knows the
// name, Postgres doesn't.
func zoneErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == invalidParameterValue {
		return ErrBadTimeZone
	}
	return err
}

// zoneTab is the IANA's list of zones "intended as an aid for users, to
// help them select timezones" (zone.tab, tzdata 2026b, public domain): one
// name per place, in its current spelling. The full database also holds old
// spellings and compatibility names (Asia/Calcutta beside Asia/Kolkata,
// "US/Eastern", "Etc/GMT+5" whose sign is inverted), which a device may
// still report and which are still accepted, just not offered. Refresh it
// from a newer tzdata when a new zone appears; until then a zone missing
// here can still be followed with Automatic.
//
//go:embed zone.tab
var zoneTab string

// ZoneChoice is one zone Profile offers, with its UTC offset now.
type ZoneChoice struct {
	Name   string
	Offset string // "UTC+05:30"
}

// zoneNames is the list of names offered, read once per process: the zone
// database changes only with a new image (Go's) or a Postgres upgrade,
// which restarts this. A failed read is not kept, so the next asks again.
var zoneNames struct {
	sync.Mutex
	names []string
}

// Zones is every zone a person can pick in Profile (zone.tab, and UTC),
// sorted by name, with its offset at now. Only names both Go and Postgres
// know are offered, so every one is accepted by ChooseTimeZone.
func (s *Service) Zones(ctx context.Context, now time.Time) ([]ZoneChoice, error) {
	names, err := s.zoneNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ZoneChoice, len(names))
	for i, name := range names {
		loc, _ := ParseTimeZone(name) // checked when the list was read
		_, off := now.In(loc).Zone()
		out[i] = ZoneChoice{Name: name, Offset: formatOffset(off)}
	}
	return out, nil
}

func (s *Service) zoneNames(ctx context.Context) ([]string, error) {
	zoneNames.Lock()
	defer zoneNames.Unlock()
	if zoneNames.names != nil {
		return zoneNames.names, nil
	}
	all, err := s.q.TimeZoneNames(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(all))
	for _, name := range all {
		known[name] = true
	}
	// UTC is not a place, so not in zone.tab, but people living by it pick it.
	names := []string{"UTC"}
	for _, line := range strings.Split(zoneTab, "\n") {
		f := strings.Split(line, "\t") // code, coordinates, name, comment
		if strings.HasPrefix(line, "#") || len(f) < 3 {
			continue
		}
		// Offered only when both zone databases know it: Postgres (this
		// install's copy may be older) and Go.
		if _, err := ParseTimeZone(f[2]); err == nil && known[f[2]] {
			names = append(names, f[2])
		}
	}
	slices.Sort(names) // zone.tab is ordered by country code
	names = slices.Compact(names)
	zoneNames.names = names
	return names, nil
}

// formatOffset writes seconds east of UTC as "UTC+05:30" / "UTC-03:00".
func formatOffset(sec int) string {
	sign := '+'
	if sec < 0 {
		sign, sec = '-', -sec
	}
	return fmt.Sprintf("UTC%c%02d:%02d", sign, sec/3600, sec%3600/60)
}
