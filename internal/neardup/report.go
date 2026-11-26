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
	// out of the rest. OneReceiver are those of the rest whose texts differ
	// and one client has a copy of both: its second copy is another
	// transmission, never merged as near (ADR 0050 decision 5). They stay
	// in the histograms (how near different pages come), not in what would
	// merge.
	Close struct {
		Pairs, OtherJobs, OneReceiver int
		Edits, PerChar                histogram
	}
	// AtRatio is, for each of the ratios tried, how many kept pairs would be
	// kept apart, how many close pairs would be merged, and how many more are
	// as near but one receiver heard both.
	AtRatio []ratioCount
	// Heard is ADR 0050's "measuring voting": messages by how many clients
	// heard them, and for those heard by three or more, how their copies
	// differ and what a simulated vote (Vote) would do.
	Heard Heard
	// RedactionMarker is how a redaction shows in stored text
	// (pipeline.Redacted), for the vote's redaction check.
	RedactionMarker string
}

// Heard counts messages by the clients that heard them (dedup-report's
// voting section). Counts only (P1).
type Heard struct {
	// ByClients: heard by 1, 2, 3, and 4 or more clients.
	ByClients [4]int
	// Of those heard by 3 or more: every copy the same; all but one the same;
	// two texts split otherwise (2-2 and the like, 4 or more copies); 3 or
	// more texts.
	Identical, OneOdd, TwoSplit, ThreeOrMore int
	// Vote is the simulated vote, for those whose copies differ.
	Vote struct {
		// Same: the voted text is the kept one's (cleaned). Differs: it isn't;
		// of those, AsWell parse as well as the kept text or better, Worse
		// parse worse, and Rescued are those whose kept text fails to parse
		// and the voted one parses.
		Same, Differs, AsWell, Worse, Rescued int
		// Ties are the positions with no majority, per message.
		Ties histogram
		// Unredacted: voted texts that would hold something else where a copy
		// holds a redaction marker.
		Unredacted int
	}
	// Receivers, in order of copies heard (receiver 1 heard the most): the
	// copies each heard, and how often its copy was the one that differs
	// when all but one are the same. Indexes, never client IDs.
	Receivers []Receiver
}

// Receiver is one client's counts, known by its index, never its ID.
type Receiver struct {
	Copies, OddOneOut int
}

type ratioCount struct {
	Ratio                                 float64
	KeptApart, CloseMerged, CloseOneHeard int
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
	r.Heard.Vote.Ties = tieBuckets()
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
// other; otherJob says they name different job numbers, and oneReceiver
// that one client has a copy of both.
func (r *Report) AddClose(a, b string, otherJob, oneReceiver bool) {
	r.Close.Pairs++
	if otherJob {
		r.Close.OtherJobs++
		return
	}
	// Equal texts are R19's (exact) to merge, whoever heard them.
	oneReceiver = oneReceiver && a != b
	if oneReceiver {
		r.Close.OneReceiver++
	}
	edits, perChar, _, _ := distance(a, b)
	r.Close.Edits.add(float64(edits))
	r.Close.PerChar.add(perChar)
	for i := range r.AtRatio {
		if a == b || !Compare(a, b, r.AtRatio[i].Ratio).Near {
			continue
		}
		if oneReceiver {
			r.AtRatio[i].CloseOneHeard++
		} else {
			r.AtRatio[i].CloseMerged++
		}
	}
}

// AddHeardBy counts messages heard by clients clients.
func (r *Report) AddHeardBy(clients, messages int) {
	if clients < 1 {
		return
	}
	r.Heard.ByClients[min(clients, 4)-1] += messages
}

// AddReceiver adds the next receiver (in order of copies heard, the most
// first) with the copies it heard.
func (r *Report) AddReceiver(copies int) {
	r.Heard.Receivers = append(r.Heard.Receivers, Receiver{Copies: copies})
}

// AddCopies counts a message heard by three or more clients: its text
// (message, the copy kept), each client's copy (copies, as stored) and each
// copy's receiver (receivers, indexes into Heard.Receivers). quality says
// how a text parses, as R19a judges it.
func (r *Report) AddCopies(message string, copies []string, receivers []int, quality func(text string) Quality) {
	h := &r.Heard
	count := map[string]int{}
	for _, c := range copies {
		count[c]++
	}
	switch len(count) {
	case 1:
		h.Identical++
		return
	case 2:
		// Two texts: one copy differs (3-1, 2-1), or they split (2-2).
		odd := -1
		for i, c := range copies {
			if count[c] == 1 {
				odd = i
			}
		}
		if odd >= 0 {
			h.OneOdd++
			if x := receivers[odd]; x >= 0 && x < len(h.Receivers) {
				h.Receivers[x].OddOneOut++
			}
		} else {
			h.TwoSplit++
		}
	default:
		h.ThreeOrMore++
	}
	prefer := -1
	for i, c := range copies {
		if c == message {
			prefer = i
			break
		}
	}
	v := Vote(copies, prefer, r.RedactionMarker)
	vt := &h.Vote
	vt.Ties.add(float64(v.Ties))
	if v.Unredacted {
		vt.Unredacted++
	}
	if v.Text == string(Clean(message)) {
		vt.Same++
		return
	}
	vt.Differs++
	qk, qv := quality(message), quality(v.Text)
	if qk.Better(qv) {
		vt.Worse++
	} else {
		vt.AsWell++
	}
	if !qk.Matched && qv.Matched {
		vt.Rescued++
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
	line("  one receiver heard both, texts differ (never merged as near)", r.Close.OneReceiver)
	r.Close.Edits.write(w, "edits between them")
	r.Close.PerChar.write(w, "edits per character of the shorter")

	fmt.Fprintln(w)
	fmt.Fprintln(w, "at other ratios (DEDUP_NEAR_RATIO)")
	fmt.Fprintf(w, "  %-10s %-26s %-26s %s\n", "ratio", "kept pairs that'd be apart", "close pairs that'd merge", "as near, one receiver heard both")
	for _, a := range r.AtRatio {
		fmt.Fprintf(w, "  %-10.2f %-26d %-26d %d\n", a.Ratio, a.KeptApart, a.CloseMerged, a.CloseOneHeard)
	}

	h := &r.Heard
	fmt.Fprintln(w)
	fmt.Fprintln(w, "messages by how many clients heard them (measuring voting)")
	line("heard by 1", h.ByClients[0])
	line("heard by 2", h.ByClients[1])
	line("heard by 3", h.ByClients[2])
	line("heard by 4 or more", h.ByClients[3])
	fmt.Fprintln(w, "of those heard by 3 or more")
	line("every copy the same", h.Identical)
	line("all but one the same (2 agree, 1 differs)", h.OneOdd)
	line("two texts, split otherwise (4 or more copies)", h.TwoSplit)
	line("3 or more texts", h.ThreeOrMore)
	vt := &h.Vote
	fmt.Fprintln(w, "a simulated vote, of those whose copies differ")
	line("the vote gives the kept text", vt.Same)
	line("the vote gives another text", vt.Differs)
	line("  it parses as well as the kept text, or better", vt.AsWell)
	line("  it parses worse", vt.Worse)
	line("  the kept text fails to parse, the vote's parses", vt.Rescued)
	vt.Ties.write(w, "positions with no majority, per message")
	line("votes holding something else where a copy has REDACTED", vt.Unredacted)
	fmt.Fprintln(w, "by receiver, in order of copies heard")
	fmt.Fprintf(w, "  %-12s %-14s %s\n", "receiver", "copies heard", "the one that differs (2 agree, 1 differs)")
	for i, x := range h.Receivers {
		fmt.Fprintf(w, "  %-12d %-14d %d\n", i+1, x.Copies, x.OddOneOut)
	}
}

func tieBuckets() histogram {
	return histogram{bounds: []float64{0, 1, 3}, labels: []string{"0", "1", "2-3", "4 or more"}, counts: make([]int, 4)}
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
