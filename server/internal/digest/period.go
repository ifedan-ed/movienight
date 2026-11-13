package digest

import (
	"fmt"
	"time"
)

// Kinds are the three digests, in the order the catch-up looks at them.
var Kinds = []string{"week", "month", "year"}

// ValidKind reports whether kind is one of Kinds.
func ValidKind(kind string) bool { return kind == "week" || kind == "month" || kind == "year" }

// Clock is a wall-clock time of day: in the install's zone for the worker's
// schedule, in each person's own zone for when their digest is due.
type Clock struct{ Hour, Minute int }

// RunAt is when each kind goes out, local time, on the day after its period
// ends: Monday 09:00, the 1st at 09:30, 1 January at 10:30. The worker's
// schedule is built from these in the install's TIMEZONE (NextRun); a
// person's digest is due at the same times in their own zone (Due).
var RunAt = map[string]Clock{"week": {9, 0}, "month": {9, 30}, "year": {10, 30}}

// midnight is the start of t's day in loc. time.Date normalises days past a
// month's end and lands on the right offset across a DST change (a local
// midnight, not "24 hours before").
func midnight(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

// LastPeriod is the last full week (Monday to Sunday), calendar month or
// calendar year before now, as [start, end) local midnights.
func LastPeriod(kind string, now time.Time, loc *time.Location) (start, end time.Time) {
	m := midnight(now, loc)
	switch kind {
	case "week":
		back := (int(m.Weekday()) + 6) % 7 // days since Monday
		end = time.Date(m.Year(), m.Month(), m.Day()-back, 0, 0, 0, 0, loc)
		start = time.Date(end.Year(), end.Month(), end.Day()-7, 0, 0, 0, 0, loc)
	case "year":
		end = time.Date(m.Year(), time.January, 1, 0, 0, 0, 0, loc)
		start = time.Date(m.Year()-1, time.January, 1, 0, 0, 0, 0, loc)
	default: // month
		end = time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, loc)
		start = time.Date(m.Year(), m.Month()-1, 1, 0, 0, 0, 0, loc)
	}
	return start, end
}

// Due is the latest period of this kind and when its run was due (the
// period's end day at RunAt).
func Due(kind string, now time.Time, loc *time.Location) (start, dueAt time.Time) {
	start, end := LastPeriod(kind, now, loc)
	c := RunAt[kind]
	return start, time.Date(end.Year(), end.Month(), end.Day(), c.Hour, c.Minute, 0, 0, loc)
}

// NextRun is the first scheduled run of kind strictly after `after`.
func NextRun(kind string, after time.Time, loc *time.Location) time.Time {
	// The run due for the period that ends next: one period after the
	// latest due run, found by asking Due a period ahead.
	_, due := Due(kind, after, loc)
	if due.After(after) {
		return due
	}
	l := after.In(loc)
	var ahead time.Time
	switch kind {
	case "week":
		ahead = time.Date(l.Year(), l.Month(), l.Day()+7, 12, 0, 0, 0, loc)
	case "year":
		ahead = time.Date(l.Year()+1, time.January, 1, 12, 0, 0, 0, loc)
	default:
		ahead = time.Date(l.Year(), l.Month()+1, 1, 12, 0, 0, 0, loc)
	}
	_, due = Due(kind, ahead, loc)
	return due
}

// PeriodLabel names a period: "2025", "September 2026", "Oct 6–12",
// "Sep 29 – Oct 5".
func PeriodLabel(kind string, start, end time.Time) string {
	last := end.AddDate(0, 0, -1)
	switch kind {
	case "year":
		return fmt.Sprint(start.Year())
	case "month":
		return start.Format("January 2006")
	}
	if start.Month() == last.Month() {
		return fmt.Sprintf("%s %d–%d", start.Format("Jan"), start.Day(), last.Day())
	}
	return fmt.Sprintf("%s %d – %s %d", start.Format("Jan"), start.Day(), last.Format("Jan"), last.Day())
}
