package users

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the zone names are checked against the database built into the binary, the same in every image

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
// their digest periods. It is the zone their browser or phone last reported
// (users.time_zone, PUT /api/v1/me/time-zone).
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

// SetTimeZone stores the zone a person's browser or phone reports, after
// checking it (ErrBadTimeZone, nothing changed).
func (s *Service) SetTimeZone(ctx context.Context, id int32, name string) (store.User, error) {
	loc, err := ParseTimeZone(name)
	if err != nil {
		return store.User{}, err
	}
	u, err := s.q.SetUserTimeZone(ctx, store.SetUserTimeZoneParams{ID: id, TimeZone: loc.String()})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == invalidParameterValue {
		return store.User{}, ErrBadTimeZone // Go knows the name, Postgres doesn't
	}
	return u, err
}
