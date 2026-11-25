package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/noise"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A139: "probably noise" (R35h). Every page is made up; each case has its
// own hour and its own capcodes, so the cases' 10-minute windows don't meet.

// garble is a made-up garbled decode: no format, no alias.
const garble = "QX7#ZK.. 4R!P"

// noisy is whether message id is marked, read through the API as a user
// sees it, and in the database.
func (e *env) noisy(id int64) bool {
	e.t.Helper()
	var m server.Message
	if code := e.getJSON(fmt.Sprintf("/api/v1/messages/%d", id), &m); code != 200 {
		e.t.Fatalf("message %d: %d", id, code)
	}
	stored := e.count(`SELECT probably_noise::int FROM messages WHERE id = $1`, id) == 1
	if stored != m.ProbablyNoise {
		e.t.Fatalf("message %d: stored %v, API %v", id, stored, m.ProbablyNoise)
	}
	return m.ProbablyNoise
}

func TestA139ProbablyNoise(t *testing.T) {
	e := setup(t)
	key, err := e.st.AddClient(e.ctx, "c", "Client C")
	if err != nil {
		t.Fatal(err)
	}
	e.keys["c"] = key
	hour := func(n int) time.Time { return e.now.Add(time.Duration(n-30) * time.Hour) }

	t.Run("marked, and the API says so", func(t *testing.T) {
		t0 := hour(0)
		e.mustPost("b", "0100001", "@@EX0013501 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0)
		p := e.mustPost("a", "0910001", garble, t0.Add(5*time.Minute))
		if p.Outcome != ingest.Stored || !e.noisy(p.MessageID) {
			t.Fatalf("a garbled one-off heard by A only, B receiving: %+v, want probably noise", p)
		}
		var list server.MessagePage
		e.getJSON("/api/v1/messages?capcode=0910001", &list)
		if len(list.Messages) != 1 || !list.Messages[0].ProbablyNoise {
			t.Errorf("the list: %+v, want the page marked", list.Messages)
		}
		var raw map[string]any
		e.getJSON(fmt.Sprintf("/api/v1/messages/%d", p.MessageID), &raw)
		if v, ok := raw["probably_noise"].(bool); !ok || !v {
			t.Errorf("API field probably_noise = %v", raw["probably_noise"])
		}
		// Another page, from both, has the field false.
		var other server.MessagePage
		e.getJSON("/api/v1/messages?capcode=0100001", &other)
		if len(other.Messages) == 0 || other.Messages[0].ProbablyNoise {
			t.Errorf("the other page: %+v", other.Messages)
		}
	})

	t.Run("(a) a second receiver's copy clears it", func(t *testing.T) {
		t0 := hour(1)
		e.mustPost("b", "0100001", "@@EX0013502 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0)
		p := e.mustPost("a", "0910002", garble, t0.Add(5*time.Minute))
		if !e.noisy(p.MessageID) {
			t.Fatal("not marked before the second copy")
		}
		c := e.mustPost("c", "0910002", garble, t0.Add(5*time.Minute+300*time.Millisecond))
		if c.Outcome != ingest.Duplicate || c.MessageID != p.MessageID {
			t.Fatalf("second copy: %+v", c)
		}
		if e.noisy(p.MessageID) {
			t.Error("heard by two receivers, still marked")
		}
	})

	t.Run("(b) another receiver within 10 minutes, either side", func(t *testing.T) {
		t0 := hour(2)
		e.mustPost("b", "0100001", "@@EX0013503 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0.Add(-11*time.Minute))
		p := e.mustPost("a", "0910003", garble, t0)
		if e.noisy(p.MessageID) {
			t.Fatal("B's only copy 11 minutes before: marked")
		}
		// A copy of another page from B, 9 minutes after: B was receiving.
		e.mustPost("b", "0100002", "@@EX0013504 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN2", t0.Add(9*time.Minute))
		if !e.noisy(p.MessageID) {
			t.Error("B's copy 9 minutes after: not marked")
		}
	})

	t.Run("(c) a format matched isn't noise; parse_failed is", func(t *testing.T) {
		t0 := hour(3)
		e.adminAddAlias("0920001", "Noise test 1")
		e.adminAddAlias("0920002", "Noise test 2")
		e.mustPost("b", "0100001", "@@EX0013505 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0)
		ok := e.mustPost("a", "0920001", "@@EX0013506 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0.Add(time.Minute))
		if e.noisy(ok.MessageID) {
			t.Error("a page matching a format is marked")
		}
		cut := e.mustPost("a", "0920002", "@@GARBLED 123", t0.Add(2*time.Minute))
		if _, failed := e.messageText(cut.MessageID); !failed {
			t.Fatal("the cut page isn't parse_failed")
		}
		if !e.noisy(cut.MessageID) {
			t.Error("a parse_failed one-off isn't marked")
		}
	})

	t.Run("(d) a later page to the capcode clears it, from any client or an import", func(t *testing.T) {
		t0 := hour(4)
		e.mustPost("b", "0100001", "@@EX0013507 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0)
		p := e.mustPost("a", "0910004", garble, t0.Add(time.Minute))
		q := e.mustPost("a", "0910005", garble, t0.Add(2*time.Minute))
		if !e.noisy(p.MessageID) || !e.noisy(q.MessageID) {
			t.Fatal("not marked")
		}
		// An hour later, from another client: the capcode is known now.
		later := e.mustPost("c", "0910004", "ANOTHER MADE-UP PAGE", t0.Add(70*time.Minute))
		if later.MessageID == p.MessageID || e.noisy(p.MessageID) {
			t.Errorf("a later page to the capcode: %+v, first still marked %v", later, e.noisy(p.MessageID))
		}
		if e.noisy(later.MessageID) {
			t.Error("the later page is marked")
		}
		b, _ := json.Marshal(map[string]string{"received_at": t0.Add(-48 * time.Hour).Format(time.RFC3339Nano), "capcode": "0910005", "text": "AN OLD MADE-UP PAGE"})
		e.mustImport(string(b))
		if e.noisy(q.MessageID) {
			t.Error("an imported page to the capcode left it marked")
		}
	})

	t.Run("a re-parse that gives it a format clears it", func(t *testing.T) {
		t0 := hour(5)
		e.mustPost("b", "0100001", "@@EX0013508 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN1", t0)
		p := e.mustPost("a", "0930001", "@@EX0013509 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 #STN3", t0.Add(time.Minute))
		if !e.noisy(p.MessageID) {
			t.Fatal("a text-only page heard once isn't marked")
		}
		e.adminAddAlias("0930001", "Noise test 3")
		e.reparseNow()
		if e.noisy(p.MessageID) {
			t.Error("re-parsed with a format, still marked")
		}
	})

	t.Run("the pass over pages stored before", func(t *testing.T) {
		marked := e.count(`SELECT count(*) FROM messages WHERE probably_noise`)
		if marked == 0 {
			t.Fatal("nothing marked to start with")
		}
		// As before the upgrade: nothing marked, the pass not run.
		if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET probably_noise = false; DELETE FROM settings WHERE key = 'probably_noise_backfill'`); err != nil {
			t.Fatal(err)
		}
		var secondFirst time.Time
		if err := e.st.Pool.QueryRow(e.ctx, `SELECT min(received_at) FROM receptions GROUP BY client_id ORDER BY 1 OFFSET 1 LIMIT 1`).Scan(&secondFirst); err != nil {
			t.Fatal(err)
		}
		start, ok, err := noise.Start(e.ctx, e.st)
		if err != nil || !ok || !start.Equal(secondFirst.Add(-10*time.Minute)) {
			t.Errorf("start %v %v %v, want %v", start, ok, err, secondFirst.Add(-10*time.Minute))
		}
		if err := noise.Backfill(e.ctx, e.st, nil, 0); err != nil {
			t.Fatal(err)
		}
		if n := e.count(`SELECT count(*) FROM messages WHERE probably_noise`); n != marked {
			t.Errorf("the pass marked %d, live marked %d", n, marked)
		}
		if n := e.count(`SELECT count(*) FROM settings WHERE key = 'probably_noise_backfill' AND value = 'done'`); n != 1 {
			t.Error("the pass isn't recorded as done")
		}
		// Done once: run again, it changes nothing.
		if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET probably_noise = false`); err != nil {
			t.Fatal(err)
		}
		if err := noise.Backfill(e.ctx, e.st, nil, 0); err != nil {
			t.Fatal(err)
		}
		if n := e.count(`SELECT count(*) FROM messages WHERE probably_noise`); n != 0 {
			t.Errorf("a second pass marked %d", n)
		}
	})
}

// A139: an install with one receiver never marks a page, live or in the
// pass over pages stored before.
func TestA139OneReceiver(t *testing.T) {
	e := setup(t)
	t0 := e.now.Add(-time.Hour)
	var ids []int64
	for i, text := range []string{garble, "@@GARBLED 123", strings.ToLower(garble)} {
		ids = append(ids, e.mustPost("a", fmt.Sprintf("091100%d", i+1), text, t0.Add(time.Duration(i)*time.Minute)).MessageID)
	}
	for _, id := range ids {
		if e.noisy(id) {
			t.Errorf("message %d marked with one receiver", id)
		}
	}
	if _, ok, err := noise.Start(e.ctx, e.st); ok || err != nil {
		t.Errorf("start: %v %v, want none", ok, err)
	}
	if err := noise.Backfill(e.ctx, e.st, nil, 0); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE probably_noise`); n != 0 {
		t.Errorf("the pass marked %d with one receiver", n)
	}
}
