package title

import (
	"net/http"
	"strings"
	"time"

	"movienight/server/internal/rw"
)

// The forms of an ISO date or time a watch date may take, as Python's
// datetime.fromisoformat read them (a date, a time with or without
// seconds and fraction, "T" or a space between, with or without an offset).
var whenLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02T15",
	"2006-01-02 15:04:05.999999999", "2006-01-02 15:04", "2006-01-02",
}

// parseWhen reads a watch date: an ISO date or datetime. A date or time
// without an offset is in loc, the person's own zone (rw.Deps.Zone); a bare
// date means 20:00 that evening (a film watched "on the 30th" was most
// likely watched in the evening, and the day must not slide when shown in
// their zone). nil for none. A time more than a day ahead, or before 1900, is
// refused with the Python's words.
func parseWhen(value *string, loc *time.Location, now time.Time) (time.Time, error) {
	if value == nil || *value == "" {
		return time.Time{}, nil
	}
	v := strings.Replace(*value, "Z", "+00:00", 1)
	var when time.Time
	parsed := false
	for _, layout := range whenLayouts {
		var t time.Time
		var err error
		if strings.Contains(layout, "Z07") {
			t, err = time.Parse(layout, v)
		} else {
			t, err = time.ParseInLocation(layout, v, loc)
		}
		if err == nil {
			when, parsed = t, true
			if layout == "2006-01-02" {
				when = time.Date(t.Year(), t.Month(), t.Day(), 20, 0, 0, 0, loc)
			}
			break
		}
	}
	if !parsed {
		return time.Time{}, rw.Err(http.StatusUnprocessableEntity, "watched_at must be an ISO date")
	}
	if when.After(now.Add(24 * time.Hour)) {
		return time.Time{}, rw.Err(http.StatusUnprocessableEntity, "watched_at is in the future")
	}
	if when.Year() < 1900 {
		return time.Time{}, rw.Err(http.StatusUnprocessableEntity, "watched_at is too early")
	}
	return when, nil
}
