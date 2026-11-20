package neardup

import (
	"fmt"
	"io"
)

// Report counts how near duplicates differ, for an operator tuning the
// ratio on a real archive (R19b, the dedup-report command). It holds counts
// only: no text, capcode or anything else from a page (P1).
type Report struct {
	Ratio float64 // the ratio in use
	// Kept are the copies kept with their own text: near duplicates as they
	// were merged.
	Kept struct {
		Pairs int
		// Shapes, compared now at Ratio; NoLonger counts those no longer near
		// at it (the ratio changed since).
		Shapes   map[Shape]int
		NoLonger int
		// Of the pairs whose lengths differ, after cleaning: which parses.
		LongerParses, ShorterParses, BothParse, NeitherParses int
		// Of every pair: does the message's copy parse better, or the kept one?
		MessageBetter, VariantBetter, AsWell int
		Edits, PerChar                       histogram
	}
	// Close are pairs of messages to one capcode sent within 10 s of each
	// other: pages kept apart, near or not. OtherJobs name different job
	// numbers, which are never merged (ADR 0050): they're counted, and left
	// out of the rest.
	Close struct {
		Pairs, OtherJobs int
		Edits, PerChar   histogram
	}
	// AtRatio is, for each of the ratios tried, how many kept pairs would be
	// kept apart and how many close pairs would be merged.
	AtRatio []ratioCount
}

type ratioCount struct {
	Ratio                  float64
	KeptApart, CloseMerged int
}

// The ratios a report tries beside the one in use.
var tryRatios = []float64{0.05, 0.1, 0.15, 0.2, 0.3}

// NewReport starts a report for ratio.
func NewReport(ratio float64) *Report {
	r := &Report{Ratio: ratio}
	r.Kept.Shapes = map[Shape]int{}
	r.Kept.Edits, r.Close.Edits = editBuckets(), editBuckets()
	r.Kept.PerChar, r.Close.PerChar = perCharBuckets(), perCharBuckets()
	for _, t := range tryRatios {
		r.AtRatio = append(r.AtRatio, ratioCount{Ratio: t})
	}
	return r
}

// distance is how far apart two copies are, uncapped, and per character of
// the shorter (cleaned).
func distance(a, b string) (edits int, perChar float64, ca, cb []rune) {
	ca, cb = Clean(a), Clean(b)
	short, long := ca, cb
	if len(ca) > len(cb) {
		short, long = cb, ca
	}
	edits = PrefixDistance(short, long, len(short))
	if len(short) > 0 {
		perChar = float64(edits) / float64(len(short))
	}
	return edits, perChar, ca, cb
}

// AddKept counts a copy kept with its own text (variant) and its message's
// text (message), with how each parses.
func (r *Report) AddKept(message, variant string, qm, qv Quality) {
	k := &r.Kept
	k.Pairs++
	m := Compare(message, variant, r.Ratio)
	if m.Near {
		k.Shapes[m.Shape]++
	} else {
		k.NoLonger++
	}
	edits, perChar, cm, cv := distance(message, variant)
	k.Edits.add(float64(edits))
	k.PerChar.add(perChar)
	if len(cm) != len(cv) {
		ql, qs := qm, qv
		if len(cv) > len(cm) {
			ql, qs = qv, qm
		}
		switch {
		case ql.Matched && qs.Matched:
			k.BothParse++
		case ql.Matched:
			k.LongerParses++
		case qs.Matched:
			k.ShorterParses++
		default:
			k.NeitherParses++
		}
	}
	switch {
	case qm.Better(qv):
		k.MessageBetter++
	case qv.Better(qm):
		k.VariantBetter++
	default:
		k.AsWell++
	}
	for i := range r.AtRatio {
		if !Compare(message, variant, r.AtRatio[i].Ratio).Near {
			r.AtRatio[i].KeptApart++
		}
	}
}

// AddClose counts two messages to one capcode sent within 10 s of each
// other; otherJob says they name different job numbers.
func (r *Report) AddClose(a, b string, otherJob bool) {
	r.Close.Pairs++
	if otherJob {
		r.Close.OtherJobs++
		return
	}
	edits, perChar, _, _ := distance(a, b)
	r.Close.Edits.add(float64(edits))
	r.Close.PerChar.add(perChar)
	for i := range r.AtRatio {
		if a != b && Compare(a, b, r.AtRatio[i].Ratio).Near {
			r.AtRatio[i].CloseMerged++
		}
	}
}

// Write prints the report: counts and histograms only.
func (r *Report) Write(w io.Writer) {
	line := func(name string, n int) { fmt.Fprintf(w, "  %-58s %d\n", name, n) }
	k := &r.Kept
	fmt.Fprintf(w, "near duplicates kept as copies with their own text (ratio in use %.2f)\n", r.Ratio)
	line("pairs", k.Pairs)
	for _, s := range Shapes {
		line("  "+string(s), k.Shapes[s])
	}
	line("  no longer near at the ratio in use", k.NoLonger)
	fmt.Fprintln(w, "of those whose lengths differ, after trailing junk is stripped")
	line("the longer parses, the shorter doesn't", k.LongerParses)
	line("the shorter parses, the longer doesn't", k.ShorterParses)
	line("both parse", k.BothParse)
	line("neither parses", k.NeitherParses)
	fmt.Fprintln(w, "which copy parses better")
	line("the message's", k.MessageBetter)
	line("the copy kept with its own text", k.VariantBetter)
	line("as well as each other", k.AsWell)
	k.Edits.write(w, "edits between them")
	k.PerChar.write(w, "edits per character of the shorter")

	fmt.Fprintln(w)
	fmt.Fprintln(w, "messages to one capcode sent within 10 s of each other (kept apart)")
	line("pairs", r.Close.Pairs)
	line("  naming different job numbers (never merged; not counted below)", r.Close.OtherJobs)
	r.Close.Edits.write(w, "edits between them")
	r.Close.PerChar.write(w, "edits per character of the shorter")

	fmt.Fprintln(w)
	fmt.Fprintln(w, "at other ratios (DEDUP_NEAR_RATIO)")
	fmt.Fprintf(w, "  %-10s %-26s %s\n", "ratio", "kept pairs that'd be apart", "close pairs that'd merge")
	for _, a := range r.AtRatio {
		fmt.Fprintf(w, "  %-10.2f %-26d %d\n", a.Ratio, a.KeptApart, a.CloseMerged)
	}
}

// histogram counts values into buckets, each up to and including its bound.
type histogram struct {
	bounds []float64 // the last bucket is everything above the last bound
	labels []string
	counts []int
}

func editBuckets() histogram {
	return histogram{bounds: []float64{0, 1, 2, 3, 6, 10, 20},
		labels: []string{"0", "1", "2", "3", "4-6", "7-10", "11-20", "over 20"}, counts: make([]int, 8)}
}

func perCharBuckets() histogram {
	return histogram{bounds: []float64{0, 0.02, 0.05, 0.1, 0.15, 0.2, 0.3},
		labels: []string{"0", "up to 0.02", "up to 0.05", "up to 0.10", "up to 0.15", "up to 0.20", "up to 0.30", "over 0.30"}, counts: make([]int, 8)}
}

func (h *histogram) add(v float64) {
	for i, b := range h.bounds {
		if v <= b+1e-9 {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.counts)-1]++
}

func (h *histogram) write(w io.Writer, name string) {
	fmt.Fprintln(w, "  "+name)
	for i, l := range h.labels {
		fmt.Fprintf(w, "    %-56s %d\n", l, h.counts[i])
	}
}
