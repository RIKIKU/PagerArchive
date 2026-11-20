package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/importer"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A131: near duplicates from a second receiver (R19a, ADR 0050). Two
// clients, A and B, post made-up copies of a page that differ as POCSAG
// copies do; each case is a fresh job, so the cases don't meet.

// a131Page is a made-up page in the example's emergency format, on job.
func a131Page(job string) string {
	return "@@" + job + " HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1"
}

type variantRow struct {
	ClientID string
	Text     *string
}

// variants are a message's receptions in time order, with each one's own text.
func (e *env) variants(id int64) []variantRow {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, `SELECT client_id, text FROM receptions WHERE message_id = $1 ORDER BY received_at, client_id`, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []variantRow
	for rows.Next() {
		var v variantRow
		if err := rows.Scan(&v.ClientID, &v.Text); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func (e *env) messageText(id int64) (text string, parseFailed bool) {
	e.t.Helper()
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT text, parse_failed FROM messages WHERE id = $1`, id).Scan(&text, &parseFailed); err != nil {
		e.t.Fatal(err)
	}
	return text, parseFailed
}

func (e *env) messagesOn(job string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM messages WHERE text LIKE '%' || $1 || '%'`, job)
}

// A131: one message per transmission, with each client's copy.
func TestA131NearDuplicates(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	t0 := e.now

	t.Run("substitution: both parse, the first is kept", func(t *testing.T) {
		full := a131Page("EX0001311")
		garbled := strings.Replace(full, "FROM", "FR}M", 1) // one codeword garbled
		a := e.mustPost("a", "0100001", full, t0)
		b := e.mustPost("b", "0100001", garbled, t0.Add(400*time.Millisecond))
		if a.Outcome != ingest.Stored || b.Outcome != ingest.Duplicate || b.MessageID != a.MessageID {
			t.Fatalf("outcomes %+v, %+v", a, b)
		}
		if text, _ := e.messageText(a.MessageID); text != full {
			t.Errorf("message text %q, want the first copy", text)
		}
		v := e.variants(a.MessageID)
		if len(v) != 2 || v[0].ClientID != "a" || v[0].Text != nil || v[1].ClientID != "b" || deref(v[1].Text) != garbled {
			t.Errorf("receptions %+v", v)
		}
	})

	t.Run("truncated first, then a better copy: the message takes it", func(t *testing.T) {
		full := a131Page("EX0001312")
		cut := full[:40]
		// An integration that wants structure fires, which the cut copy
		// doesn't say it is, and one that wants every page.
		fires := e.createIntegration(e.user, integrations.Request{Name: "Fires", Kind: integrations.Webhook, Capcodes: []string{"0100002"},
			Filter: integrations.Filter{Categories: []string{"Structure fire"}}, Secrets: integrations.Secrets{URL: "http://127.0.0.1:9/hook"}})
		every := e.createIntegration(e.user, integrations.Request{Name: "Every page", Kind: integrations.Webhook, Capcodes: []string{"0100002"},
			Secrets: integrations.Secrets{URL: "http://127.0.0.1:9/hook"}})
		updates := e.updates("")
		a := e.mustPost("a", "0100002", cut, t0)
		if text, failed := e.messageText(a.MessageID); text != cut || !failed {
			t.Fatalf("first copy: %q, parse failed %v", text, failed)
		}
		if n := e.count(`SELECT count(*) FROM deliveries WHERE message_id = $1`, a.MessageID); n != 1 {
			t.Fatalf("deliveries for the cut copy: %d, want 1 (every page)", n)
		}
		b := e.mustPost("b", "0100002", full, t0.Add(400*time.Millisecond))
		if b.Outcome != ingest.Duplicate || b.MessageID != a.MessageID {
			t.Fatalf("second copy: %+v", b)
		}
		// Re-parsed, placed and categorised as a live page.
		var m server.Message
		e.getJSON(fmt.Sprintf("/api/v1/messages/%d", a.MessageID), &m)
		if m.Text != full || s(m.Description) != "SMOKE FROM ROOF" || s(m.JobNumber) != "EX0001312" || m.Location == nil || s(m.Category) != "Structure fire" {
			t.Errorf("message after the swap: %q, description %q, job %q, location %v, category %q", m.Text, s(m.Description), s(m.JobNumber), m.Location, s(m.Category))
		}
		if !m.SentAt.Equal(t0) {
			t.Errorf("sent time %v, want the first copy's %v", m.SentAt, t0)
		}
		// The first copy keeps what A heard.
		v := e.variants(a.MessageID)
		if len(v) != 2 || v[0].ClientID != "a" || deref(v[0].Text) != cut || v[1].ClientID != "b" || v[1].Text != nil {
			t.Errorf("receptions %+v", v)
		}
		// Every signed-in user sees each copy, receivers unnamed.
		if len(m.Receptions) != 2 || m.Receptions[0].Client != "Receiver 1" || deref(m.Receptions[0].Text) != cut || m.Receptions[1].Text != nil {
			t.Errorf("detail receptions %+v", m.Receptions)
		}
		// Integrations: the one already told isn't told again; the one the
		// better copy now matches is.
		got := map[int64]int{}
		for _, id := range e.rowsOf(`SELECT integration_id FROM deliveries WHERE message_id = $1`, a.MessageID) {
			got[id]++
		}
		if len(got) != 2 || got[fires.ID] != 1 || got[every.ID] != 1 {
			t.Errorf("deliveries by integration %v, want one each for %d and %d", got, fires.ID, every.ID)
		}
		// The live list hears of the change.
		select {
		case u := <-updates:
			if u.ID != a.MessageID || u.Text != full {
				t.Errorf("update %d %q", u.ID, u.Text)
			}
		case <-time.After(5 * time.Second):
			t.Error("no live update for the swapped message")
		}
	})

	t.Run("a junk tail: the copy that parses is kept", func(t *testing.T) {
		full := a131Page("EX0001313")
		junk := full + " ÿþ"
		a := e.mustPost("a", "0100001", full, t0)
		b := e.mustPost("b", "0100001", junk, t0.Add(300*time.Millisecond))
		if b.MessageID != a.MessageID {
			t.Fatalf("junk tail stored apart: %+v %+v", a, b)
		}
		if text, _ := e.messageText(a.MessageID); text != full {
			t.Errorf("message %q", text)
		}
	})

	t.Run("a correction stays a page of its own", func(t *testing.T) {
		full := a131Page("EX0001314")
		correction := "@@EX0001314 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON GRID 654321 UNITS P1 #STN2"
		a := e.mustPost("a", "0100003", full, t0)
		c := e.mustPost("a", "0100003", correction, t0.Add(5*time.Second))
		cb := e.mustPost("b", "0100003", correction, t0.Add(5*time.Second+300*time.Millisecond))
		if c.Outcome != ingest.Stored || c.MessageID == a.MessageID || cb.MessageID != c.MessageID {
			t.Fatalf("outcomes %+v %+v %+v", a, c, cb)
		}
		if n := e.messagesOn("EX0001314"); n != 2 {
			t.Errorf("%d messages, want the page and its correction", n)
		}
	})

	t.Run("the same client, nearly the same page again: two pages", func(t *testing.T) {
		// A receiver hears a transmission once: its second copy is another
		// transmission, here the job's alarm raised a level a second later,
		// and stays a page of its own, with its own alert.
		first := strings.Replace(a131Page("EX0001315"), " GRID", " ALARM 2 GRID", 1)
		raised := strings.Replace(first, "ALARM 2", "ALARM 3", 1)
		a := e.mustPost("a", "0100004", first, t0)
		r := e.mustPost("a", "0100004", raised, t0.Add(time.Second))
		if r.Outcome != ingest.Stored || r.MessageID == a.MessageID {
			t.Fatalf("the raised alarm was merged: %+v %+v", a, r)
		}
		// B's copy of the second, a character garbled, is its copy.
		b := e.mustPost("b", "0100004", strings.Replace(raised, "ROOF", "ROOV", 1), t0.Add(time.Second+300*time.Millisecond))
		if b.MessageID != r.MessageID {
			t.Errorf("B's copy of the raised alarm went to message %d, want %d", b.MessageID, r.MessageID)
		}
	})

	t.Run("two jobs in one format, seconds apart: two pages", func(t *testing.T) {
		a := e.mustPost("a", "0100001", a131Page("EX0001321"), t0)
		b := e.mustPost("b", "0100001", a131Page("EX0001322"), t0.Add(2*time.Second))
		if a.Outcome != ingest.Stored || b.Outcome != ingest.Stored || a.MessageID == b.MessageID {
			t.Fatalf("outcomes %+v %+v: a second incident's page was merged into the first's", a, b)
		}
	})

	t.Run("exact copies behave as before", func(t *testing.T) {
		full := a131Page("EX0001319")
		a := e.mustPost("a", "0100001", full, t0)
		b := e.mustPost("b", "0100001", full, t0.Add(time.Second))
		if b.MessageID != a.MessageID {
			t.Fatal("exact copy stored apart")
		}
		for _, v := range e.variants(a.MessageID) {
			if v.Text != nil {
				t.Errorf("an exact copy kept text of its own: %+v", v)
			}
		}
	})

	// The metrics count each kind, each reason and each client's copies.
	body, _ := e.scrape()
	for _, want := range []string{
		`pagerarchive_dedup_total{kind="exact"} 2`,
		`pagerarchive_dedup_total{kind="near"} 4`,
		`pagerarchive_dedup_near_total{kept="first"} 2`,
		`pagerarchive_dedup_near_total{kept="parsed"} 2`,
		`pagerarchive_dedup_near_total{kept="longer"} 0`,
		`pagerarchive_dedup_near_swapped_total 1`,
		`pagerarchive_client_receptions_total{client="a"} 9`,
		`pagerarchive_client_receptions_total{client="b"} 7`,
		`pagerarchive_client_variants_total{client="a"} 1`,
		`pagerarchive_client_variants_total{client="b"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

// A131: a better copy that makes a large event opens it, and its
// integration is told once; a page already in one stays there, told once.
func TestA131NearDuplicateEvents(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	// Two events, more than 12 hours apart, so they stay two (R87a).
	t0 := e.now.Add(-14 * time.Hour)
	e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: "http://127.0.0.1:9/hook"}})

	// The cut copy lost the code that opens an event (R87).
	full := "@@EX0001316 HSF1 SMOKE DG1234 @ 12 FICTION ST, MADEUPTON #STN1"
	a := e.mustPost("a", "0100001", full[:24], t0)
	if n := e.count(`SELECT count(*) FROM large_event_messages`); n != 0 {
		t.Fatalf("the cut copy is in %d events", n)
	}
	e.mustPost("b", "0100001", full, t0.Add(300*time.Millisecond))
	if held := e.heldBy(a.MessageID); len(held) != 1 {
		t.Fatalf("the better copy's message is in events %v, want one", held)
	}
	if n := e.count(`SELECT count(*) FROM deliveries WHERE event_id IS NOT NULL`); n != 1 {
		t.Errorf("event deliveries %d, want 1", n)
	}

	// Already in an event, with a code, but its call sign garbled: the
	// better copy keeps it there, and nobody is told again.
	garbled := "@@EX0001317 HSF1 SMOKE DG1235 @ 12 FICTION ST, MADEUPTON }STN1"
	g := e.mustPost("a", "0100002", garbled, e.now)
	before := e.heldBy(g.MessageID)
	if len(before) != 1 {
		t.Fatalf("the garbled copy is in events %v", before)
	}
	b := e.mustPost("b", "0100002", strings.Replace(garbled, "}STN1", "#STN1", 1), e.now.Add(300*time.Millisecond))
	if b.MessageID != g.MessageID {
		t.Fatalf("stored apart: %+v", b)
	}
	if _, failed := e.messageText(g.MessageID); failed {
		t.Error("the better copy didn't replace the garbled one")
	}
	if after := e.heldBy(g.MessageID); len(after) != 1 {
		t.Errorf("after the better copy, in events %v", after)
	}
	if n := e.count(`SELECT count(*) FROM deliveries WHERE event_id IS NOT NULL`); n != 2 {
		t.Errorf("event deliveries %d, want 2: one per event", n)
	}
}

// A131, P1: a copy's own text is redacted as a message's is, before it's
// stored, and retroactive redaction reaches it.
func TestA131VariantsRedacted(t *testing.T) {
	e := setup(t)
	phone := "0412 345 678"
	first := "QDCheck the shed Saturday. Text " + phone + " if available. EX0000001"
	second := "QDCheck the shxd Saturday. Text " + phone + " if available. EX0000001"
	a := e.mustPost("a", "0100001", first, e.now)
	b := e.mustPost("b", "0100001", second, e.now.Add(300*time.Millisecond))
	if b.MessageID != a.MessageID {
		t.Fatalf("stored apart: %+v %+v", a, b)
	}
	v := e.variants(a.MessageID)
	if len(v) != 2 || deref(v[1].Text) != "QDCheck the shxd Saturday. Text REDACTED if available. EX0000001" {
		t.Fatalf("receptions %+v", v)
	}
	for _, s := range []string{phone, "0412345678"} {
		if hits := e.rowsContaining(s); len(hits) > 0 {
			t.Errorf("%q stored: %v", s, hits)
		}
	}

	// As if B's copy was stored before a rule matched what it holds.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE receptions SET text = $2 WHERE message_id = $1 AND client_id = 'b'`, a.MessageID, second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job.RunOnce(e.ctx, e.now); err != nil {
		t.Fatal(err)
	}
	if hits := e.rowsContaining(phone); len(hits) > 0 {
		t.Errorf("still stored after the nightly run: %v", hits)
	}
	if v := e.variants(a.MessageID); deref(v[1].Text) != "QDCheck the shxd Saturday. Text REDACTED if available. EX0000001" {
		t.Errorf("after the nightly run: %+v", v)
	}
}

// A131, R83a: an import merges near duplicates as live ingest does, and a
// second run of the file stores nothing new (R83c).
func TestA131ImportNearDuplicates(t *testing.T) {
	e := setup(t)
	full := a131Page("EX0001320")
	at := e.now.Add(-time.Hour)
	line := func(at time.Time, text string) string {
		b, _ := json.Marshal(map[string]string{"received_at": at.Format(time.RFC3339Nano), "capcode": "0100001", "text": text})
		return string(b)
	}
	lines := []string{line(at, full[:40]), line(at.Add(500*time.Millisecond), full)}
	dry := e.runImport(strings.Join(lines, "\n")+"\n", func(im *importer.Importer) { im.DryRun = true })
	if dry.err != nil || dry.Report.Stored != 1 || dry.Report.Duplicates != 1 || dry.Report.NearDuplicates != 1 {
		t.Fatalf("dry run: %+v %v", dry.Report, dry.err)
	}
	r := e.mustImport(lines...)
	if r.Report.Stored != 1 || r.Report.Duplicates != 1 || r.Report.NearDuplicates != 1 {
		t.Fatalf("import: %+v", r.Report)
	}
	if !strings.Contains(r.out, "of which near duplicates (R19a)") {
		t.Errorf("report:\n%s", r.out)
	}
	if n := e.messagesOn("EX0001320"); n != 1 {
		t.Fatalf("%d messages", n)
	}
	if n := e.count(`SELECT count(*) FROM import_receptions WHERE text IS NOT NULL`); n != 1 {
		t.Errorf("%d imported copies with their own text, want 1", n)
	}
	again := e.mustImport(lines...)
	if again.Report.Stored != 0 || again.Report.AlreadyImported != 2 {
		t.Errorf("second run: %+v", again.Report)
	}
}

// update is a live update event's message.
type update struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// updates opens a live-update connection and returns its update events
// (R19a): a message that took a better copy's text.
func (e *env) updates(query string) <-chan update {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	e.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/api/v1/stream?"+query, nil)
	req.AddCookie(e.as.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("stream: %v %v", err, resp)
	}
	out := make(chan update, 16)
	ready := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if line == ": connected" {
				close(ready)
			}
			if ev, ok := strings.CutPrefix(line, "event: "); ok {
				event = ev
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok && event == "update" {
				var u update
				if json.Unmarshal([]byte(data), &u) == nil {
					out <- u
				}
			}
		}
	}()
	<-ready
	for e.srv.Hub.Len() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	return out
}

func (e *env) rowsOf(sql string, args ...any) []int64 {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, sql, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}
