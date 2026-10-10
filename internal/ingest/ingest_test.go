package ingest

import (
	"testing"
)

// R27: an exact capcode wins, then fewer wildcards; patterns starting with a
// wildcard come last.
func TestBestAlias(t *testing.T) {
	rows := func(patterns ...string) []Candidate {
		var r []Candidate
		for i, p := range patterns {
			r = append(r, Candidate{ID: int64(i + 1), Pattern: p})
		}
		return r
	}
	tests := []struct {
		patterns []string
		want     string
	}{
		{[]string{"%", "0100%", "0100001"}, "0100001"},
		{[]string{"%", "01000_1", "0100%"}, "01000_1"},
		{[]string{"%0001", "01%01"}, "01%01"},
		{[]string{"_100001", "01__0%1"}, "01__0%1"},
		{[]string{"%"}, "%"},
	}
	for _, tt := range tests {
		got := BestAlias(rows(tt.patterns...))
		if got == nil || got.Pattern != tt.want {
			t.Errorf("BestAlias(%v) = %v, want %s", tt.patterns, got, tt.want)
		}
	}
	if BestAlias(nil) != nil {
		t.Error("no aliases should give nil")
	}
}
