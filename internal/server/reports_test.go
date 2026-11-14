package server

import "testing"

// R38f: percentiles by nearest rank, from a histogram (size: jobs).
func TestPercentile(t *testing.T) {
	h := map[int64]int64{1: 4, 2: 1, 5: 1} // 1 1 1 1 2 5
	for _, c := range []struct {
		p    float64
		want int64
	}{{25, 1}, {50, 1}, {75, 2}, {83, 2}, {84, 5}, {100, 5}} {
		if got := percentile(h, c.p); got != c.want {
			t.Errorf("percentile %v: %d, want %d", c.p, got, c.want)
		}
	}
	if got := percentile(map[int64]int64{}, 50); got != 0 {
		t.Errorf("no jobs: %d, want 0", got)
	}
	if got := stat(map[int64]int64{2: 1, 5: 1}); got != (SizeStat{Median: 2, Low: 2, High: 5, Max: 5}) {
		t.Errorf("two jobs: %+v", got)
	}
}

// R38i: the median is the lower middle one of an even number.
func TestMedian(t *testing.T) {
	if m := median(nil); m != nil {
		t.Errorf("none: %v, want nil", *m)
	}
	for _, c := range []struct {
		v    []int64
		want int64
	}{{[]int64{5}, 5}, {[]int64{9, 1}, 1}, {[]int64{3, 1, 2}, 2}, {[]int64{18000, 0, 3600, 0}, 0}} {
		if m := median(c.v); m == nil || *m != c.want {
			t.Errorf("median %v: %v, want %d", c.v, m, c.want)
		}
	}
}
