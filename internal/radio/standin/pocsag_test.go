package main

import "testing"

// The sync and idle codewords are valid codewords: the encoder's check bits
// and parity match POCSAG's.
func TestCodeword(t *testing.T) {
	for name, w := range map[string]uint32{"sync": syncWord, "idle": idleWord} {
		if got := codeword(w >> 11); got != w {
			t.Errorf("%s: codeword(%#x) = %#x, want %#x", name, w>>11, got, w)
		}
	}
	// A page: preamble, then whole batches of a sync and 16 codewords.
	b := encode(100001, "QDTest")
	if (len(b)-preambleBits)%(17*32) != 0 {
		t.Errorf("%d bits after the preamble, not whole batches", len(b)-preambleBits)
	}
}
