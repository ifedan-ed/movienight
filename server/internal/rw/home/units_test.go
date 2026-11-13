package home

// Ported from the Python's tests/test_units.py: the row sets' rotation and
// "Because you watched" dealing, and the line saying what the AI read.

import (
	"slices"
	"testing"
	"time"
)

func pool(n int, recent int, now time.Time) []seed {
	out := make([]seed, n)
	for i := range out {
		last := now.Add(-time.Duration(48+i) * time.Hour)
		if i < recent {
			last = now.Add(-time.Hour)
		}
		out[i] = seed{"movie", i, "", last}
	}
	return out
}

func ids(s []seed) []int {
	var out []int
	for _, x := range s {
		out = append(out, x.id)
	}
	return out
}

func TestBecauseSetsMixRecentAndOlderAndPinTheLastDay(t *testing.T) {
	now := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
	p := pool(15, 0, now)
	want := [][]int{{0, 3, 6, 9, 12}, {1, 4, 7, 10, 13}, {2, 5, 8, 11, 14}}
	for k := 0; k < 3; k++ {
		if got := ids(becauseSet(p, k, now)); !slices.Equal(got, want[k]) {
			t.Fatal(k, got)
		}
	}
	p = pool(15, 1, now) // title 0 watched an hour ago leads every set
	for k := 0; k < 3; k++ {
		if got := becauseSet(p, k, now); got[0].id != 0 || len(got) != 5 {
			t.Fatal(k, ids(got))
		}
	}
}

func TestRotationMorningNoonNightMovesOnEachDay(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, ny) }
	var day1, day2 []int
	for _, h := range []int{8, 14, 21} {
		day1 = append(day1, rotationIndex(at(2, h), ny))
		day2 = append(day2, rotationIndex(at(3, h), ny))
	}
	s1, s2 := slices.Clone(day1), slices.Clone(day2)
	slices.Sort(s1)
	slices.Sort(s2)
	if !slices.Equal(s1, []int{0, 1, 2}) || !slices.Equal(s2, []int{0, 1, 2}) {
		t.Fatal(day1, day2)
	}
	for i := range day1 {
		if day2[i] != (day1[i]+1)%3 {
			t.Fatal(day1, day2)
		}
	}
	if rotationIndex(at(3, 2), ny) != rotationIndex(at(2, 23), ny) {
		t.Fatal("the small hours belong to the evening before")
	}
	nights := map[int]bool{}
	for _, d := range []int{2, 3, 4} {
		nights[rotationIndex(at(d, 21), ny)] = true
	}
	if len(nights) != 3 {
		t.Fatal(nights)
	}
	// The install's zone decides, not the server's: 03:00 UTC is still
	// the evening before in New York.
	if rotationIndex(time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC), ny) != rotationIndex(at(2, 23), ny) {
		t.Fatal("not the install's clock")
	}
}
