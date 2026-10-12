package accounts

import (
	"slices"
	"testing"
)

func TestParseEmail(t *testing.T) {
	for _, tt := range []struct {
		in, key, domain string
		ok              bool
	}{
		{"b@example.com", "b@example.com", "example.com", true},
		{"  B@Example.COM ", "b@example.com", "example.com", true},
		{"b+anything@example.com", "b@example.com", "example.com", true}, // owner, 2026-10-13
		{"b+@example.com", "b@example.com", "example.com", true},
		{"+tag@example.com", "+tag@example.com", "example.com", true}, // no base to keep
		{"first.last@members.example.com", "first.last@members.example.com", "members.example.com", true},
		{"Someone <s@example.com>", "", "", false},
		{"s@example.com, t@example.com", "", "", false},
		{"not an address", "", "", false},
		{"s@localhost", "", "", false},
		{"", "", "", false},
	} {
		e, ok := ParseEmail(tt.in)
		if ok != tt.ok || e.Key != tt.key || e.Domain != tt.domain {
			t.Errorf("ParseEmail(%q) = %+v, %v; want key %q domain %q ok %v", tt.in, e, ok, tt.key, tt.domain, tt.ok)
		}
	}
}

func TestParseDomains(t *testing.T) {
	got := ParseDomains(" Example.com, @members.example.com  other.org,")
	want := []string{"example.com", "members.example.com", "other.org"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLastAdmin(t *testing.T) {
	if !lastAdmin([]int64{1}, []int64{1, 2}) || lastAdmin([]int64{1, 3}, []int64{1}) || lastAdmin(nil, []int64{1}) {
		t.Error("lastAdmin")
	}
}
