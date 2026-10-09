package streets

import "testing"

var types = map[string]string{"STREET": "ST", "ROAD": "RD", "HIGHWAY": "HWY"}

func TestSplit(t *testing.T) {
	ix := NewIndex([]Entry{
		{"FICTION ST", "MADEUPTON"},
		{"MILL RD", "MADEUPTON"},
		{"OLD MILL RD", "MADEUPTON"},
		{"VALLEY HWY", "MADEUPTON"},
		{"EXAMPLE VALLEY HWY", "MADEUPTON"},
		{"SAMPLE RD", "TESTVILLE"},
		{"Fiction Street", "North Testville"},
	}, types)
	tests := []struct {
		span, desc, addr string
		ok               bool
	}{
		// Unmarked boundary, no house number: the lazy-regex failure case.
		{"BIN FIRE OLD MILL RD MADEUPTON", "BIN FIRE", "OLD MILL RD MADEUPTON", true},
		// Unmarked boundary with a house number: the greedy-regex failure case.
		{"SMOKE SEEN 4 OLD MILL RD MADEUPTON", "SMOKE SEEN", "4 OLD MILL RD MADEUPTON", true},
		// Longest street wins over the shorter decoy that ends it.
		{"GRASS FIRE EXAMPLE VALLEY HWY MADEUPTON", "GRASS FIRE", "EXAMPLE VALLEY HWY MADEUPTON", true},
		// Street type normalised; literal text kept.
		{"ALARM 12A FICTION STREET MADEUPTON", "ALARM", "12A FICTION STREET MADEUPTON", true},
		// Multi-word locality, dataset written in full and mixed case.
		{"ALARM 3/5 FICTION ST NORTH TESTVILLE", "ALARM", "3/5 FICTION ST NORTH TESTVILLE", true},
		// Address-like phrase inside the description is not a suffix.
		{"ENT VIA 7 SAMPLE RD TESTVILLE GATE 2 FICTION ST MADEUPTON", "ENT VIA 7 SAMPLE RD TESTVILLE GATE", "2 FICTION ST MADEUPTON", true},
		// Same street name in another locality doesn't count.
		{"FIRE FICTION ST TESTVILLE", "FIRE FICTION ST TESTVILLE", "", false},
		// Unknown street: whole span is the description.
		{"FIRE 9 NOWHERE LANE MADEUPTON", "FIRE 9 NOWHERE LANE MADEUPTON", "", false},
		// The whole span may be the address.
		{"FICTION ST MADEUPTON", "", "FICTION ST MADEUPTON", true},
		// A locality alone is not an address.
		{"FIRE MADEUPTON", "FIRE MADEUPTON", "", false},
	}
	for _, tt := range tests {
		d, a, ok := ix.Split(tt.span)
		if d != tt.desc || a != tt.addr || ok != tt.ok {
			t.Errorf("Split(%q) = %q, %q, %v; want %q, %q, %v", tt.span, d, a, ok, tt.desc, tt.addr, tt.ok)
		}
	}
}

// Known limit (ADR 0001, 2.4): with OLD MILL RD missing, the shorter street
// that ends it is chosen, without a flag.
func TestSplitMissingStreetUsesShorterDecoy(t *testing.T) {
	ix := NewIndex([]Entry{{"MILL RD", "MADEUPTON"}}, types)
	d, a, ok := ix.Split("BIN FIRE OLD MILL RD MADEUPTON")
	if d != "BIN FIRE OLD" || a != "MILL RD MADEUPTON" || !ok {
		t.Errorf("got %q, %q, %v", d, a, ok)
	}
}

func TestSplitEmptyIndex(t *testing.T) {
	d, a, ok := NewIndex(nil, types).Split("BIN FIRE OLD MILL RD MADEUPTON")
	if d != "BIN FIRE OLD MILL RD MADEUPTON" || a != "" || ok {
		t.Errorf("got %q, %q, %v", d, a, ok)
	}
}
