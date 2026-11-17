package monitortest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// fakeRadio is a made-up receiving client whose numbers the test writes
// straight into Prometheus, as Alloy would from a client running the radio:
// pages decoded and audio received (counters), and seconds since audio
// last came. Its plan says, for any time, how many pages a minute it
// decodes and whether its radio gives audio; times outside its history
// it has no numbers for.
type fakeRadio struct {
	id   string
	plan func(at time.Time) (perMin float64, audio bool)

	mu             sync.Mutex
	last           time.Time // the last sample written; zero to start a stretch afresh
	decoded, bytes float64
	audioLast      time.Time
	extra          float64 // pages to add with the next sample
}

const fakeStep = 15 * time.Second

// samples moves the client on to each step up to until, from its last
// sample (or from from, for its first), and returns its three series'
// samples.
func (c *fakeRadio) samples(from, until time.Time) map[string][][2]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][][2]float64{}
	at := from
	if !c.last.IsZero() {
		at = c.last.Add(fakeStep)
	}
	for ; !at.After(until); at = at.Add(fakeStep) {
		perMin, audio := c.plan(at)
		if c.audioLast.IsZero() {
			c.audioLast = at
		}
		if !c.last.IsZero() {
			c.decoded += perMin * fakeStep.Minutes()
			if audio {
				c.bytes += 44100 * fakeStep.Seconds()
			}
		}
		c.decoded += c.extra
		c.extra = 0
		if audio {
			c.audioLast = at
		}
		ms := float64(at.UnixMilli())
		out["pagerarchive_client_pages_decoded_total"] = append(out["pagerarchive_client_pages_decoded_total"], [2]float64{ms, c.decoded})
		out["pagerarchive_client_radio_audio_bytes_total"] = append(out["pagerarchive_client_radio_audio_bytes_total"], [2]float64{ms, c.bytes})
		out["pagerarchive_client_radio_silent_seconds"] = append(out["pagerarchive_client_radio_silent_seconds"], [2]float64{ms, max(0.05, at.Sub(c.audioLast).Seconds())})
		c.last = at
	}
	return out
}

func (c *fakeRadio) add(pages float64) {
	c.mu.Lock()
	c.extra += pages
	c.mu.Unlock()
}

// remoteWrite sends the samples to Prometheus' remote-write receiver
// (protobuf, snappy), each series under the labels the client's own would
// have: instance is the client's ID.
func remoteWrite(prometheus, instance string, series map[string][][2]float64) error {
	var req []byte
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range series {
			if !yield(k) {
				return
			}
		}
	}) {
		var ts []byte
		// Sorted by name, as remote write wants them.
		for _, l := range [][2]string{{"__name__", name}, {"instance", instance}, {"job", "pagerarchive-client"}, {"otel_scope_name", "pagerarchive-client"}} {
			var lb []byte
			lb = protowire.AppendTag(lb, 1, protowire.BytesType)
			lb = protowire.AppendString(lb, l[0])
			lb = protowire.AppendTag(lb, 2, protowire.BytesType)
			lb = protowire.AppendString(lb, l[1])
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, lb)
		}
		for _, s := range series[name] {
			var sb []byte
			sb = protowire.AppendTag(sb, 1, protowire.Fixed64Type)
			sb = protowire.AppendFixed64(sb, math.Float64bits(s[1]))
			sb = protowire.AppendTag(sb, 2, protowire.VarintType)
			sb = protowire.AppendVarint(sb, uint64(int64(s[0])))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, sb)
		}
		req = protowire.AppendTag(req, 1, protowire.BytesType)
		req = protowire.AppendBytes(req, ts)
	}
	// Snappy's block format with literals only: the length, then the bytes
	// in pieces of at most 64 KiB, each after a tag holding its length.
	body := binary.AppendUvarint(nil, uint64(len(req)))
	for b := req; len(b) > 0; {
		n := min(len(b), 1<<16)
		body = append(body, 61<<2, byte(n-1), byte((n-1)>>8))
		body = append(body, b[:n]...)
		b = b[n:]
	}
	r, _ := http.NewRequest(http.MethodPost, prometheus+"/api/v1/write", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("Content-Encoding", "snappy")
	r.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return fmt.Errorf("remote write for %s: %w", instance, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote write for %s: %d %s", instance, resp.StatusCode, b)
	}
	return nil
}

// A119. Told when a radio stops decoding (owner, 2026-11-17): made-up
// clients whose radio gives audio, with two days' history at this time of
// day. One stopped decoding 20 minutes before the test looks: "Radio not
// decoding" fires for it, and for no other client: not one decoding as
// usual, not one whose radio stalled ("Radio stalled" fires for that one
// instead), not a new client with no history, and not one on a network too
// quiet to tell. "Radio stalled" and "Client not receiving" don't fire for
// it. Decoding back to over a third of the usual doesn't clear it; over half
// does.
//
// The clients are the test's own: it writes their numbers into Prometheus
// as Alloy would (the test stack's Prometheus takes samples up to 9 days
// old), so a client can have days of history, and have stopped decoding
// before the test starts, rather than the test waiting a day and half an
// hour. The rule only reads what's in Prometheus.
func TestA119ToldWhenARadioStopsDecoding(t *testing.T) {
	s := newStack(t)
	prom := os.Getenv("PA_MON_PROMETHEUS_URL")
	if prom == "" {
		t.Fatal("PA_MON_PROMETHEUS_URL is needed: run scripts/monitoring-checks.sh")
	}
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	run := time.Now().Format("150405")
	a, b, c, d, e := "dec-a-"+run, "dec-b-"+run, "dec-c-"+run, "dec-d-"+run, "dec-e-"+run
	// The server has heard from each of them (a garbled page now and then
	// does that), so "Client not receiving" has nothing to say.
	for _, id := range []string{a, b, c, d, e} {
		key, err := s.st.AddClient(context.Background(), id, "A119 "+id)
		if err != nil {
			t.Fatal(err)
		}
		s.post(t, id, key, "QDA119 "+id+" is receiving")
	}
	defer func() {
		for _, id := range []string{a, b, c, d, e} {
			_, _ = s.st.RevokeClient(context.Background(), id)
		}
	}()

	t0 := time.Now().Truncate(fakeStep)
	const usual = 6.0 // pages a minute at this time of day: 180 in 30 minutes
	stoppedAt := t0.Add(-20 * time.Minute)
	var resumeC sync.Mutex
	cStalled := true
	clients := []*fakeRadio{
		{id: a, plan: func(at time.Time) (float64, bool) { // stops decoding; the radio still gives audio
			if at.Before(stoppedAt) {
				return usual, true
			}
			return 0, true
		}},
		{id: b, plan: func(time.Time) (float64, bool) { return usual, true }}, // decodes as usual
		{id: c, plan: func(at time.Time) (float64, bool) { // its radio stalls: no audio, no pages
			resumeC.Lock()
			defer resumeC.Unlock()
			if at.Before(stoppedAt) || !cStalled {
				return usual, true
			}
			return 0, false
		}},
		{id: d, plan: func(at time.Time) (float64, bool) { // new: no history; stops decoding
			if at.Before(stoppedAt) {
				return usual, true
			}
			return 0, true
		}},
		{id: e, plan: func(at time.Time) (float64, bool) { // a quiet network: 15 pages in a usual half hour; stops
			if at.Before(stoppedAt) {
				return 0.5, true
			}
			return 0, true
		}},
	}
	// History, oldest first (each series in time order).
	for _, cl := range clients {
		all := map[string][][2]float64{}
		for _, back := range []time.Duration{48 * time.Hour, 24 * time.Hour, 0} {
			if back > 0 && cl.id == d {
				continue
			}
			from, until := t0.Add(-back-45*time.Minute), t0.Add(-back+30*time.Minute-fakeStep)
			if back == 0 {
				until = t0
			}
			cl.mu.Lock()
			cl.last = time.Time{} // a gap: start this stretch afresh, the counters where they were
			cl.mu.Unlock()
			for k, v := range cl.samples(from, until) {
				all[k] = append(all[k], v...)
			}
		}
		if err := remoteWrite(prom, cl.id, all); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("A119: wrote two days' history at this time of day for %s, %s, %s and %s, none for %s (new); %s and %s stopped decoding at %s, %s's radio stalled then",
		a, b, c, e, d, a, d, stoppedAt.Format("15:04:05"), c)

	// From now on, a sample every 15 s, as the clients report.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(fakeStep)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				for _, cl := range clients {
					if err := remoteWrite(prom, cl.id, cl.samples(t0, now)); err != nil {
						t.Error(err)
					}
				}
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	// Fires for A: its 30 minutes fall below a quarter of the usual about
	// 2.5 minutes after the test starts (10 minutes of decoding left in
	// them, then 7.5), then 5 minutes below the line.
	firedAt, fired := time.Time{}, false
	if !waitFor(13*time.Minute, func() bool { firedAt, fired = hook.find(t0, "Radio not decoding", "firing", a); return fired }) {
		t.Fatalf("no \"Radio not decoding\" alert for %s within 13 minutes; got %d notifications", a, len(hook.got))
	}
	if since := firedAt.Sub(t0); since < 6*time.Minute+30*time.Second {
		t.Errorf("\"Radio not decoding\" fired for %s %v after the test started: before its 30 minutes fell below a quarter of the usual and stayed there for 5 minutes", a, since.Round(time.Second))
	}
	t.Logf("A119: \"Radio not decoding\" fired for %s %v after the test started (%v after it stopped decoding)", a,
		firedAt.Sub(t0).Round(time.Second), firedAt.Sub(stoppedAt).Round(time.Second))

	// The Clients dashboard shows it: the last 30 minutes far under the usual.
	const panel = "Pages decoded: the last 30 minutes against the usual"
	got := s.panelValues(t, "pa-clients", panel)
	if got["A"][a] >= got["C"][a] || got["B"][a] < 170 || got["B"][a] > 190 {
		t.Errorf("%q for %s: last 30 minutes %.0f, usual %.0f, alert line %.0f; want the usual near 180 and the last 30 minutes under the line",
			panel, a, got["A"][a], got["B"][a], got["C"][a])
	} else {
		t.Logf("A119: %q for %s: last 30 minutes %.0f, usual %.0f, alert line %.0f; for %s %.0f against %.0f", panel, a,
			got["A"][a], got["B"][a], got["C"][a], b, got["A"][b], got["B"][b])
	}

	// "Radio stalled" fires for C (its audio stopped): the radio's case, not
	// this one's.
	if !waitFor(2*time.Minute, func() bool { _, ok := hook.find(t0, "Radio stalled", "firing", c); return ok }) {
		t.Errorf("no \"Radio stalled\" alert for %s, whose audio stopped", c)
	}
	for _, n := range hook.alerts(t0, "firing") {
		switch {
		case n.name == "Radio not decoding" && n.client != a:
			t.Errorf("\"Radio not decoding\" fired for %s too", n.client)
		case n.name == "Radio stalled" && n.client != c:
			t.Errorf("\"Radio stalled\" fired for %s", n.client)
		case n.name == "Client not receiving" && slices.Contains([]string{a, b, c, d, e}, n.client):
			t.Errorf("\"Client not receiving\" fired for %s", n.client)
		}
	}

	// Decoding comes back to a third of the usual: still firing (it clears
	// above half).
	third := time.Now()
	clients[0].add(60)
	time.Sleep(90 * time.Second)
	if at, ok := hook.find(third, "Radio not decoding", "resolved", a); ok {
		t.Errorf("\"Radio not decoding\" for %s resolved %v after its 30 minutes came back to a third of the usual: want it firing until over half",
			a, at.Sub(third).Round(time.Second))
	} else {
		t.Logf("A119: with a third of the usual decoded, still firing 90 s later")
	}
	// Then to two thirds: it clears.
	resumed := time.Now()
	clients[0].add(60)
	resolvedAt, resolved := time.Time{}, false
	if !waitFor(3*time.Minute, func() bool {
		resolvedAt, resolved = hook.find(resumed, "Radio not decoding", "resolved", a)
		return resolved
	}) {
		t.Errorf("\"Radio not decoding\" for %s didn't resolve within 3 minutes of its decoding coming back to two thirds of the usual", a)
	} else {
		t.Logf("A119: it resolved %v after decoding came back to two thirds of the usual", resolvedAt.Sub(resumed).Round(time.Second))
	}

	// One more look, then C's audio comes back, so nothing is left firing
	// for the checks after this one.
	for _, n := range hook.alerts(t0, "firing") {
		if n.name == "Radio not decoding" && n.client != a {
			t.Errorf("\"Radio not decoding\" fired for %s", n.client)
		}
	}
	back := time.Now()
	resumeC.Lock()
	cStalled = false
	resumeC.Unlock()
	if !waitFor(3*time.Minute, func() bool { _, ok := hook.find(back, "Radio stalled", "resolved", c); return ok }) {
		t.Errorf("\"Radio stalled\" for %s didn't resolve within 3 minutes of its audio coming back", c)
	}
	if !t.Failed() {
		t.Logf("A119: fired for %s only; not for %s (as usual), %s (radio stalled: \"Radio stalled\" did), %s (no history) or %s (too quiet to tell); no \"Client not receiving\"",
			a, b, c, d, e)
	}
}
