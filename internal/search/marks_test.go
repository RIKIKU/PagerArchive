package search

import (
	"testing"
	"time"
)

func kinds(ms []Mark) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		out[m.Kind]++
	}
	return out
}

// contiguous checks the marks run newest first, each ending where the next
// newer one starts.
func contiguous(t *testing.T, ms []Mark) {
	t.Helper()
	for i := range ms {
		if !ms[i].Start.Before(ms[i].End) {
			t.Fatalf("mark %d is empty: %v to %v", i, ms[i].Start, ms[i].End)
		}
		if i > 0 && !ms[i].End.Equal(ms[i-1].Start) {
			t.Fatalf("mark %d ends %v, the next newer starts %v", i, ms[i].End, ms[i-1].Start)
		}
	}
}

// R81b: the last 30 days, one mark a day, with today and yesterday by the
// hour, never past the current hour; newest at the top.
func TestMarksLast30Days(t *testing.T) {
	mel, _ := time.LoadLocation("Australia/Melbourne")
	now := time.Date(2026, time.November, 12, 14, 25, 0, 0, mel)
	ms := Marks(now, mel, nil, nil)
	contiguous(t, ms)
	k := kinds(ms)
	if k["day"] != 28 || k["hour"] != 24+15 || len(k) != 2 {
		t.Errorf("kinds %v, want 28 days and 39 hours (yesterday's 24, today's 15 to 15:00)", k)
	}
	if first := ms[0]; !first.Start.Equal(time.Date(2026, 11, 12, 14, 0, 0, 0, mel)) || !first.End.Equal(time.Date(2026, 11, 12, 15, 0, 0, 0, mel)) {
		t.Errorf("newest mark %v to %v, want this hour", first.Start, first.End)
	}
	if last := ms[len(ms)-1]; !last.Start.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, mel)) {
		t.Errorf("oldest mark starts %v, want 14 Oct (30 days with today)", last.Start)
	}
	// The day before yesterday is a day; yesterday starts the hours.
	if d := ms[39]; d.Kind != "day" || !d.Start.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, mel)) || !d.End.Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, mel)) {
		t.Errorf("mark 39: %+v, want 10 Nov", d)
	}
}

// R81b: a chosen range: days up to 90, weeks from Monday over 90, months over
// a year, today and yesterday still by the hour; a range in the past has no
// hours.
func TestMarksRange(t *testing.T) {
	mel, _ := time.LoadLocation("Australia/Melbourne")
	now := time.Date(2026, time.November, 12, 9, 10, 0, 0, mel)
	day := func(y int, m time.Month, d int) *time.Time { t := time.Date(y, m, d, 0, 0, 0, 0, mel); return &t }
	tests := []struct {
		name     string
		from, to *time.Time
		want     map[string]int
	}{
		{"one day in the past", day(2026, 10, 1), day(2026, 10, 2), map[string]int{"day": 1}},
		{"90 days in the past", day(2026, 6, 1), day(2026, 8, 30), map[string]int{"day": 90}},
		// 1 Jun (a Monday) to 29 Aug 2026 inclusive is 90 days; one more is weeks.
		{"91 days in the past", day(2026, 6, 1), day(2026, 8, 31), map[string]int{"week": 13}},
		// 6 months ending today: weeks, then yesterday's and today's hours.
		{"six months to today", day(2026, 5, 13), day(2026, 11, 13), map[string]int{"week": 27, "hour": 34}},
		// Two years ending today: months, and the hours.
		{"two years to today", day(2024, 11, 13), day(2026, 11, 13), map[string]int{"month": 25, "hour": 34}},
		{"366 days", day(2025, 1, 1), day(2026, 1, 2), map[string]int{"month": 13}},
		{"in the future", day(2026, 12, 1), day(2026, 12, 2), map[string]int{}},
	}
	for _, tt := range tests {
		ms := Marks(now, mel, tt.from, tt.to)
		contiguous(t, ms)
		got := kinds(ms)
		if len(got) != len(tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
			continue
		}
		for k, n := range tt.want {
			if got[k] != n {
				t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
			}
		}
		if len(ms) > 0 && tt.from != nil && !ms[len(ms)-1].Start.Equal(*tt.from) {
			t.Errorf("%s: starts %v, want %v", tt.name, ms[len(ms)-1].Start, *tt.from)
		}
	}
	// Weeks start on Mondays, cut to the range at either end.
	ms := Marks(now, mel, day(2026, 6, 3), day(2026, 9, 10))
	if w := ms[len(ms)-1]; !w.End.Equal(*day(2026, 6, 8)) {
		t.Errorf("first week ends %v, want Monday 8 June", w.End)
	}
	if w := ms[0]; !w.Start.Equal(*day(2026, 9, 7)) || !w.End.Equal(*day(2026, 9, 10)) {
		t.Errorf("last week %v to %v, want Monday 7 to 10 September", w.Start, w.End)
	}
}

// Hours on the clock: the day the clocks go back has 25, the day they go
// forward 23 (Melbourne: 5 April and 4 October 2026).
func TestMarksDaylightSaving(t *testing.T) {
	mel, _ := time.LoadLocation("Australia/Melbourne")
	for _, tt := range []struct {
		yesterday time.Time
		hours     int
	}{
		{time.Date(2026, time.April, 5, 0, 0, 0, 0, mel), 25},
		{time.Date(2026, time.October, 4, 0, 0, 0, 0, mel), 23},
	} {
		now := tt.yesterday.AddDate(0, 0, 1).Add(30 * time.Minute) // 00:30 the next day
		ms := Marks(now, mel, nil, nil)
		contiguous(t, ms)
		if k := kinds(ms); k["hour"] != tt.hours+1 {
			t.Errorf("yesterday %s: %d hours, want %d and today's first", tt.yesterday.Format(time.DateOnly), k["hour"], tt.hours)
		}
	}
}

// R81d: an hour of today or yesterday zooms to twelve 5-minute blocks; older
// days don't zoom.
func TestZoom(t *testing.T) {
	mel, _ := time.LoadLocation("Australia/Melbourne")
	now := time.Date(2026, time.November, 12, 9, 10, 0, 0, mel)
	hour := time.Date(2026, time.November, 11, 14, 0, 0, 0, mel)
	ms, err := Zoom(now, mel, hour)
	if err != nil || len(ms) != 12 {
		t.Fatalf("%d blocks, %v", len(ms), err)
	}
	contiguous(t, ms)
	if !ms[0].Start.Equal(hour.Add(55*time.Minute)) || !ms[11].Start.Equal(hour) || ms[0].Kind != "block" {
		t.Errorf("blocks %+v to %+v", ms[11], ms[0])
	}
	for _, bad := range []time.Time{
		time.Date(2026, time.November, 10, 23, 0, 0, 0, mel), // the day before yesterday
		time.Date(2026, time.November, 11, 14, 5, 0, 0, mel), // not an hour's start
		time.Date(2026, time.November, 12, 10, 0, 0, 0, mel), // the future
	} {
		if _, err := Zoom(now, mel, bad); err == nil {
			t.Errorf("%v zooms", bad)
		}
	}
}

func TestBounds(t *testing.T) {
	mel, _ := time.LoadLocation("Australia/Melbourne")
	ms := Marks(time.Date(2026, time.November, 12, 9, 10, 0, 0, mel), mel, nil, nil)
	b := Bounds(ms)
	if len(b) != len(ms)+1 || !b[0].Equal(ms[len(ms)-1].Start) || !b[len(b)-1].Equal(ms[0].End) {
		t.Fatalf("bounds %v", b)
	}
	for i := 1; i < len(b); i++ {
		if !b[i-1].Before(b[i]) {
			t.Fatalf("bounds not ascending at %d", i)
		}
	}
}
