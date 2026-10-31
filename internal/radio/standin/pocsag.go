package main

import (
	"math"
	"math/bits"
	"math/rand/v2"
)

// POCSAG (ITU-R M.584): 32-bit codewords, a preamble of 576 alternating
// bits, then batches of a sync codeword and eight two-codeword frames. A
// page's address codeword goes in the frame its capcode's last three bits
// name; its message follows in 20-bit chunks, 7-bit characters sent least
// significant bit first.
const (
	syncWord     = 0x7CD215D8
	idleWord     = 0x7A89C197
	preambleBits = 576
	alphaFunc    = 3
)

// codeword adds BCH(31,21) check bits and even parity to 21 data bits.
func codeword(data uint32) uint32 {
	const poly = 0x769 // x^10+x^9+x^8+x^6+x^5+x^3+1
	cw := (data & 0x1FFFFF) << 10
	r := cw
	for i := 30; i >= 10; i-- {
		if r&(1<<i) != 0 {
			r ^= poly << (i - 10)
		}
	}
	w := (cw | r&0x3FF) << 1
	if bits.OnesCount32(w)%2 == 1 {
		w |= 1
	}
	return w
}

// encode is one alphanumeric page to capcode, as the bits sent on air.
func encode(capcode uint32, text string) []bool {
	var chars []bool
	for _, c := range []byte(text) {
		for i := 0; i < 7; i++ {
			chars = append(chars, c>>i&1 == 1)
		}
	}
	var words []uint32
	for len(words) < int(capcode&7)*2 {
		words = append(words, idleWord)
	}
	words = append(words, codeword(capcode>>3<<2|alphaFunc))
	for i := 0; i < len(chars); i += 20 {
		var d uint32
		for j := 0; j < 20; j++ {
			d <<= 1
			if i+j < len(chars) && chars[i+j] {
				d |= 1
			}
		}
		words = append(words, codeword(1<<20|d)) // the top bit marks a message codeword
	}
	for len(words)%16 != 0 || words[len(words)-1] != idleWord {
		words = append(words, idleWord) // to the end of the batch, and a batch of idle codewords after it
	}

	var out []bool
	for i := 0; i < preambleBits; i++ {
		out = append(out, i%2 == 0)
	}
	for i, w := range words {
		if i%16 == 0 {
			out = appendWord(out, syncWord)
		}
		out = appendWord(out, w)
	}
	return out
}

func appendWord(out []bool, w uint32) []bool {
	for i := 31; i >= 0; i-- {
		out = append(out, w>>i&1 == 1)
	}
	return out
}

// audio is what rtl_fm -M fm makes of bits sent at baud: the deviation as
// a level, a 1 below the carrier, softened a little, with some noise.
func audio(bitsOnAir []bool, baud, rate int, rng *rand.Rand) []int16 {
	per := float64(rate) / float64(baud)
	n := int(math.Ceil(float64(len(bitsOnAir)) * per))
	out := make([]int16, n)
	var y float64
	for i := range out {
		b := bitsOnAir[min(int(float64(i)/per), len(bitsOnAir)-1)]
		x := 12000.0
		if b {
			x = -12000
		}
		y += 0.5 * (x - y)
		out[i] = int16(y + rng.NormFloat64()*600)
	}
	return out
}

// noise is what rtl_fm -M fm writes from a quiet channel without squelch:
// loud hiss.
func noise(n int, rng *rand.Rand) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(max(-32767, min(32767, rng.NormFloat64()*5000)))
	}
	return out
}
