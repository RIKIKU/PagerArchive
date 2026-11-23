package neardup

import "strings"

// Voting rebuilds a page's text from three or more copies (one per
// receiver) by a majority at each position. It isn't used to store
// anything: dedup-report simulates it (R19b, ADR 0050's "measuring
// voting"), to count how often it would change a message's text.
//
// Each copy is cleaned as Compare cleans it (its trailing junk stripped).
// The medoid copy, the one with the smallest total edit distance to the
// others (whole copy against whole copy, so a truncated one is far), is the
// frame: every other copy is aligned to it by edit distance, and votes at
// each of its positions with the character it has there, or with nothing
// (it lost the character), and in each gap between them with what it has
// there that the medoid doesn't. A copy that is shorter than the medoid
// is aligned against the medoid's start: it votes only on the positions it
// covers (a truncated copy says nothing about the rest). A copy longer than
// the medoid runs on after the medoid's end, and votes there.
//
// A position's majority is more than half of the copies voting there. With
// none, the medoid's character (or nothing, in a gap) is kept, and the
// position is a tie.

// Voted is a simulated vote's result.
type Voted struct {
	// Text is the voted text (cleaned: no trailing junk).
	Text string
	// Medoid is the index of the copy the others were aligned to.
	Medoid int
	// Ties are the positions with no majority.
	Ties int
	// Unredacted: at a position where a copy holds a character of a
	// redaction marker, the vote holds something else (another character, or
	// nothing). A vote must never undo a redaction (P1); this counts how
	// often a naive one would.
	Unredacted bool
}

// gap is a copy's vote for "no character here".
const gap = -1

// alignment is one copy aligned to the medoid.
type alignment struct {
	// at[j] is the index in the copy of the character aligned to the
	// medoid's character j, or gap.
	at []int
	// covered is how many of the medoid's positions, from its start, the copy
	// votes on.
	covered int
	// ins[s] are the indices in the copy of the characters it has before the
	// medoid's character s (ins[len(medoid)]: after its end).
	ins [][]int
}

// align aligns c to m by edit distance (a substitution, an insertion or a
// deletion each cost 1). The shorter of the two is matched against the
// start of the longer, whose end is free, as PrefixDistance does: so a
// truncated copy covers only the medoid's start, and a longer copy's end is
// inserted after the medoid's.
func align(c, m []rune) alignment {
	n, l := len(c), len(m)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, l+1)
		d[i][0] = i
	}
	for j := 0; j <= l; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= l; j++ {
			cost := 1
			if c[i-1] == m[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j-1]+cost, d[i-1][j]+1, d[i][j-1]+1)
		}
	}
	// Where the alignment ends: the whole of the shorter one, against the
	// start of the longer that matches it best (the most of it, on a tie).
	ei, ej := n, l
	if n <= l {
		for j := l; j >= 0; j-- {
			if d[n][j] < d[n][ej] {
				ej = j
			}
		}
	} else {
		for i := n; i >= 0; i-- {
			if d[i][l] < d[ei][l] {
				ei = i
			}
		}
	}
	a := alignment{at: make([]int, l), covered: ej, ins: make([][]int, l+1)}
	for j := range a.at {
		a.at[j] = gap
	}
	for i := ei; i < n; i++ {
		a.ins[l] = append(a.ins[l], i) // the copy runs on after the medoid's end
	}
	// Back from the end: a match or substitution first, then a lost
	// character, then an added one.
	i, j := ei, ej
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && d[i][j] == d[i-1][j-1]+boolCost(c[i-1] != m[j-1]):
			a.at[j-1] = i - 1
			i, j = i-1, j-1
		case j > 0 && d[i][j] == d[i][j-1]+1:
			a.at[j-1] = gap
			j--
		default:
			a.ins[j] = append([]int{i - 1}, a.ins[j]...)
			i--
		}
	}
	return a
}

func boolCost(b bool) int {
	if b {
		return 1
	}
	return 0
}

// levenshtein is the plain edit distance between two cleaned copies, the
// whole of each: so a truncated copy is far from the whole ones, and is
// never the medoid while there are whole copies to choose.
func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cur[j] = min(prev[j-1]+boolCost(a[i-1] != b[j-1]), prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// markerMask marks the characters of s that are part of marker.
func markerMask(s []rune, marker string) []bool {
	mask := make([]bool, len(s))
	mr := []rune(marker)
	if len(mr) == 0 {
		return mask
	}
	str := string(s)
	off := 0
	for {
		k := strings.Index(str[off:], marker)
		if k < 0 {
			return mask
		}
		start := len([]rune(str[:off+k]))
		for x := start; x < start+len(mr) && x < len(mask); x++ {
			mask[x] = true
		}
		off += k + len(marker)
	}
}

// Vote simulates a vote among copies (one per receiver, as stored). prefer
// is the copy chosen as the medoid when copies tie for it (the message's
// own, so a vote leans to the text kept); -1 for none. marker is how a
// redaction shows in stored text (pipeline.Redacted).
func Vote(copies []string, prefer int, marker string) Voted {
	cl := make([][]rune, len(copies))
	for i, c := range copies {
		cl[i] = Clean(c)
	}
	med, best := 0, -1
	for i := range cl {
		sum := 0
		for j := range cl {
			if i != j {
				sum += levenshtein(cl[i], cl[j])
			}
		}
		if best < 0 || sum < best || (sum == best && i == prefer) {
			med, best = i, sum
		}
	}
	m := cl[med]
	l := len(m)
	als := make([]alignment, len(cl))
	for k := range cl {
		if k != med {
			als[k] = align(cl[k], m)
		}
	}
	v := Voted{Medoid: med}

	// out[j] is the voted character at the medoid's position j, or gap;
	// slot[s] what's voted before it.
	out := make([]rune, l)
	slot := make([]string, l+1)
	for j := 0; j < l; j++ {
		votes := map[rune]int{m[j]: 1}
		n := 1
		for k, a := range als {
			if k == med || j >= a.covered {
				continue
			}
			n++
			if a.at[j] == gap {
				votes[gap]++
			} else {
				votes[cl[k][a.at[j]]]++
			}
		}
		out[j] = m[j]
		won := false
		for r, c := range votes {
			if 2*c > n {
				out[j], won = r, true
			}
		}
		if !won {
			v.Ties++
		}
	}
	for s := 0; s <= l; s++ {
		votes := map[string]int{"": 1}
		n := 1
		for k, a := range als {
			if k == med || s > a.covered {
				continue
			}
			n++
			votes[runesAt(cl[k], a.ins[s])]++
		}
		won := false
		for t, c := range votes {
			if 2*c > n {
				slot[s], won = t, true
			}
		}
		if !won {
			v.Ties++
		}
	}

	var b strings.Builder
	for j := 0; j <= l; j++ {
		b.WriteString(slot[j])
		if j < l && out[j] != gap {
			b.WriteRune(out[j])
		}
	}
	v.Text = b.String()

	// The redaction check: every character of a marker, in any copy, must
	// be what the vote holds at its place.
	for k := range cl {
		mask := markerMask(cl[k], marker)
		if k == med {
			for j := range m {
				if mask[j] && out[j] != m[j] {
					v.Unredacted = true
				}
			}
			continue
		}
		a := als[k]
		for j := 0; j < a.covered; j++ {
			if x := a.at[j]; x != gap && mask[x] && out[j] != cl[k][x] {
				v.Unredacted = true
			}
		}
		for s, idx := range a.ins {
			for _, x := range idx {
				if mask[x] && slot[s] != runesAt(cl[k], idx) {
					v.Unredacted = true
				}
			}
		}
	}
	return v
}

func runesAt(s []rune, idx []int) string {
	r := make([]rune, len(idx))
	for i, x := range idx {
		r[i] = s[x]
	}
	return string(r)
}
