// Package jobs runs the worker's scheduled jobs: when each is due, that it
// runs once per due time however many workers there are, what happens to a
// run missed while no worker was running, and the record of every run.
//
// Why not a cron library: the schedules here are five shapes (every N
// minutes, daily, weekly, monthly, yearly at a local time), and the part
// that matters, claiming a run in Postgres and catching up a missed one, is
// ours either way. A Schedule answers two questions, the next due time after
// a moment and the latest one at or before it, both in the install's time
// zone (TIMEZONE), so "Monday 09:00" means 09:00 where the people are,
// across daylight-saving changes.
package jobs

import (
	"strconv"
	"time"
)

// Schedule is when a job is due.
type Schedule interface {
	// Next is the first due time strictly after t.
	Next(t time.Time) time.Time
	// Prev is the latest due time at or before t.
	Prev(t time.Time) time.Time
	// String describes it for the log ("daily 02:30").
	String() string
}

// Every is due every Interval, at whole multiples of it (counted, as
// time.Truncate does, from Go's zero time, so 5 minutes falls on :00, :05,
// ... and an hour on the hour, UTC) plus Offset. Aligned to the clock,
// not to the worker's start, so every worker computes the same due times and
// the run claims (job_runs) mean the same thing on each.
type Every struct {
	Interval time.Duration
	Offset   time.Duration
}

func (e Every) Prev(t time.Time) time.Time {
	base := t.Add(-e.Offset).Truncate(e.Interval)
	return base.Add(e.Offset)
}

func (e Every) Next(t time.Time) time.Time {
	p := e.Prev(t)
	return p.Add(e.Interval)
}

func (e Every) String() string { return "every " + e.Interval.String() }

// Calendar is due at Hour:Minute local time, on the days Day picks.
type Calendar struct {
	Hour, Minute int
	Loc          *time.Location
	// Kind: "daily", "weekly" (on Weekday), "monthly" (on MonthDay),
	// "yearly" (on Month, MonthDay).
	Kind     string
	Weekday  time.Weekday
	MonthDay int
	Month    time.Month
	label    string
}

// Daily, Weekly, Monthly and Yearly build calendar schedules.
func Daily(h, m int, loc *time.Location) Calendar {
	return Calendar{Hour: h, Minute: m, Loc: loc, Kind: "daily"}
}

func Weekly(d time.Weekday, h, m int, loc *time.Location) Calendar {
	return Calendar{Hour: h, Minute: m, Loc: loc, Kind: "weekly", Weekday: d}
}

func Monthly(day, h, m int, loc *time.Location) Calendar {
	return Calendar{Hour: h, Minute: m, Loc: loc, Kind: "monthly", MonthDay: day}
}

func Yearly(month time.Month, day, h, m int, loc *time.Location) Calendar {
	return Calendar{Hour: h, Minute: m, Loc: loc, Kind: "yearly", Month: month, MonthDay: day}
}

// at is the due time on the local calendar day of d. time.Date normalises a
// wall time that a daylight-saving jump skips (02:30 on the spring-forward
// night) to a real instant, so a nightly job still runs that night.
func (c Calendar) at(y int, mo time.Month, d int) time.Time {
	return time.Date(y, mo, d, c.Hour, c.Minute, 0, 0, c.Loc)
}

// matches reports whether the local day y-mo-d is one of the schedule's.
func (c Calendar) matches(y int, mo time.Month, d int) bool {
	day := time.Date(y, mo, d, 12, 0, 0, 0, c.Loc)
	switch c.Kind {
	case "weekly":
		return day.Weekday() == c.Weekday
	case "monthly":
		return d == c.MonthDay
	case "yearly":
		return mo == c.Month && d == c.MonthDay
	}
	return true
}

// Next walks local days forward from t's day (at most a year and a day:
// the rarest schedule, yearly, has one match in that span).
func (c Calendar) Next(t time.Time) time.Time {
	local := t.In(c.Loc)
	y, mo, d := local.Date()
	for i := 0; i <= 367; i++ {
		day := time.Date(y, mo, d+i, 12, 0, 0, 0, c.Loc)
		dy, dm, dd := day.Date()
		if c.matches(dy, dm, dd) {
			if due := c.at(dy, dm, dd); due.After(t) {
				return due
			}
		}
	}
	panic("jobs: calendar schedule never due: " + c.String())
}

// Prev walks local days backwards from t's day.
func (c Calendar) Prev(t time.Time) time.Time {
	local := t.In(c.Loc)
	y, mo, d := local.Date()
	for i := 0; i <= 367; i++ {
		day := time.Date(y, mo, d-i, 12, 0, 0, 0, c.Loc)
		dy, dm, dd := day.Date()
		if c.matches(dy, dm, dd) {
			if due := c.at(dy, dm, dd); !due.After(t) {
				return due
			}
		}
	}
	panic("jobs: calendar schedule never due: " + c.String())
}

func (c Calendar) String() string {
	hm := time.Date(2000, 1, 1, c.Hour, c.Minute, 0, 0, time.UTC).Format("15:04")
	switch c.Kind {
	case "weekly":
		return c.Weekday.String() + "s " + hm
	case "monthly":
		return "monthly on day " + strconv.Itoa(c.MonthDay) + " " + hm
	case "yearly":
		return "yearly on " + c.Month.String() + " " + strconv.Itoa(c.MonthDay) + " " + hm
	}
	return "daily " + hm
}
