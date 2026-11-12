package search

import (
	"errors"
	"slices"
	"time"
)

// Mark is one mark on the scrubber (R81b): a span of time, [Start, End).
type Mark struct {
	Start, End time.Time
	// Kind is "hour", "day", "week", "month" or, zoomed in, "block" (5 minutes).
	Kind string
}

// Scrubber's sizes (R81b, R81d; owner, 2026-11-09).
const (
	// ScrubberDays is how many days the scrubber covers with no date range.
	ScrubberDays = 30
	// WeeksOver and MonthsOver: a range of more days than these is shown by
	// weeks, or by months.
	WeeksOver  = 90
	MonthsOver = 365
	// BlockMinutes is a zoomed-in block's length, twelve to the hour.
	BlockMinutes = 5
)

// Marks are the scrubber's marks for a list at now, newest first (R81b): the
// last ScrubberDays days, or the range [from, to) when one is set (either
// end alone counts from the other), never past the current hour. One mark a
// day, a week when the range is over WeeksOver days, or a month when it's
// over MonthsOver; today and yesterday are split into hours. Days, weeks
// (from Monday) and months are calendar ones in loc, the deployment's time
// zone (R35a); a mark at either end of a range is cut to it.
func Marks(now time.Time, loc *time.Location, from, to *time.Time) []Mark {
	now = now.In(loc)
	today := midnight(now, loc)
	yesterday := today.AddDate(0, 0, -1)
	hourEnd := hourStart(now).Add(time.Hour)
	end := hourEnd
	if to != nil && to.Before(end) {
		end = to.In(loc)
	}
	var start time.Time
	switch {
	case from != nil:
		start = from.In(loc)
	case to != nil:
		start = midnight(end.Add(-time.Nanosecond), loc).AddDate(0, 0, 1-ScrubberDays)
	default:
		start = today.AddDate(0, 0, 1-ScrubberDays)
	}
	if !start.Before(end) {
		return []Mark{}
	}
	kind, next := "day", func(t time.Time) time.Time { return midnight(t, loc).AddDate(0, 0, 1) }
	switch days := daysBetween(midnight(start, loc), midnight(end.Add(-time.Nanosecond), loc)) + 1; {
	case days > MonthsOver:
		kind, next = "month", func(t time.Time) time.Time { return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc) }
	case days > WeeksOver:
		kind, next = "week", func(t time.Time) time.Time {
			d := midnight(t, loc)
			return d.AddDate(0, 0, 7-(int(d.Weekday())+6)%7)
		}
	}
	var marks []Mark
	for t := start; t.Before(end) && t.Before(yesterday); {
		n := next(t)
		if n.After(yesterday) {
			n = yesterday
		}
		if n.After(end) {
			n = end
		}
		marks = append(marks, Mark{Start: t, End: n, Kind: kind})
		t = n
	}
	// Today and yesterday by the hour: an hour on the clock, so a day when
	// the clocks change has 23 or 25.
	for t := maxTime(start, yesterday); t.Before(end); {
		n := hourStart(t).Add(time.Hour)
		if n.After(end) {
			n = end
		}
		marks = append(marks, Mark{Start: t, End: n, Kind: "hour"})
		t = n
	}
	slices.Reverse(marks)
	return marks
}

// ErrNoZoom: only an hour of today or yesterday zooms in (R81d).
var ErrNoZoom = errors.New("hour must be the start of an hour of today or yesterday")

// Zoom is an hour of today or yesterday as twelve 5-minute blocks, newest
// first (R81d).
func Zoom(now time.Time, loc *time.Location, hour time.Time) ([]Mark, error) {
	now, hour = now.In(loc), hour.In(loc)
	yesterday := midnight(now, loc).AddDate(0, 0, -1)
	if hour.Minute() != 0 || hour.Second() != 0 || hour.Nanosecond() != 0 || hour.Before(yesterday) || hour.After(now) {
		return nil, ErrNoZoom
	}
	marks := make([]Mark, 0, 60/BlockMinutes)
	for i := 60/BlockMinutes - 1; i >= 0; i-- {
		s := hour.Add(time.Duration(i*BlockMinutes) * time.Minute)
		marks = append(marks, Mark{Start: s, End: s.Add(BlockMinutes * time.Minute), Kind: "block"})
	}
	return marks, nil
}

// Bounds are the marks' edges, oldest first, for counting (Store.CountMessages).
func Bounds(marks []Mark) []time.Time {
	if len(marks) == 0 {
		return nil
	}
	out := make([]time.Time, 0, len(marks)+1)
	for i := len(marks) - 1; i >= 0; i-- {
		out = append(out, marks[i].Start)
	}
	return append(out, marks[0].End)
}

func midnight(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// daysBetween counts calendar days from a to b, both local midnights.
func daysBetween(a, b time.Time) int {
	ad := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bd := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(bd.Sub(ad).Hours() / 24)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// hourStart is the start of t's hour on the clock, worked out from its
// minutes and seconds, so it's right when the clocks go back too.
func hourStart(t time.Time) time.Time {
	return t.Add(-(time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())))
}
