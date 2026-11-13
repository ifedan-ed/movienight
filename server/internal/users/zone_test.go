package users_test

import (
	"errors"
	"testing"
	"time"

	"movienight/server/internal/store"
	"movienight/server/internal/users"
)

func TestAPersonsZoneIsTheOneTheyReportedElseTheInstalls(t *testing.T) {
	install, _ := time.LoadLocation("America/New_York")
	zone := func(tz *string) string { return users.Location(store.User{TimeZone: tz}, install).String() }
	auckland := "Pacific/Auckland"
	if got := zone(&auckland); got != "Pacific/Auckland" {
		t.Errorf("reported: %s", got)
	}
	if got := zone(nil); got != "America/New_York" {
		t.Errorf("never reported: %s", got)
	}
	// Only a newer build could have stored a name this one doesn't know:
	// treated as never reported, as documented.
	unknown := "Mars/Olympus_Mons"
	if got := zone(&unknown); got != "America/New_York" {
		t.Errorf("unknown stored name: %s", got)
	}
}

func TestOnlyRealZoneNamesAreAccepted(t *testing.T) {
	for _, ok := range []string{"Europe/Berlin", "Pacific/Auckland", "UTC", "America/Argentina/Buenos_Aires", " Asia/Kolkata "} {
		if _, err := users.ParseTimeZone(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus_Mons", "../../etc/passwd", "europe/berlin", string(make([]byte, 65))} {
		if _, err := users.ParseTimeZone(bad); !errors.Is(err, users.ErrBadTimeZone) {
			t.Errorf("%q accepted", bad)
		}
	}
}
