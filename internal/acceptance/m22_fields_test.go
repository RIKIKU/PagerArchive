package acceptance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// R87 (owner, 2026-11-14; ADR 0029): a field's value opens a large event.
// The example agency's pages may carry a made-up alarm level, ALARM <digit>
// (field alarm_level), and its large_event_fields lists 3 to 9 (SPEC.md
// section 10).

// alarmPage is an Emergency page of job at alarm level (none for "").
func alarmPage(job, level, group string) string {
	alarm := ""
	if level != "" {
		alarm = " ALARM " + level
	}
	return fmt.Sprintf("@@%s HSF1 FACTORY FIRE @ 12 FICTION ST, MADEUPTON%s #%s", job, alarm, group)
}

// largeEventField is the stored flag (migration 00021) of message id.
func (e *env) largeEventField(id int64) bool {
	e.t.Helper()
	var v bool
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT large_event_field FROM messages WHERE id = $1`, id).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// A88. A field's value opens an event (owner, 2026-11-14).
func TestA88FieldValueOpensAnEvent(t *testing.T) {
	e := setup(t)
	e.clocked()
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})

	// 09:30: job EX0000301 at alarm level 2, below the listed 3: no event.
	t0930 := e.clock().Add(-30 * time.Minute).Truncate(time.Second)
	low := e.mustPost("a", "0100001", alarmPage("EX0000301", "2", "STN1"), t0930).MessageID
	// Level 3 on a capcode with no alias, and as a Non-emergency page: none.
	e.mustPost("a", "0999999", alarmPage("EX0000301", "3", "STN1"), t0930.Add(time.Minute))
	e.mustPost("a", "0100002", "Hb"+alarmPage("EX0000301", "3", "STN2")[2:], t0930.Add(time.Minute))
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("a lower level, an unaliased or a Non-emergency page opened %d events", len(evs))
	}
	if e.largeEventField(low) {
		t.Error("level 2 is stored as a listed value")
	}

	// 10:00: the job goes to level 3 on an aliased capcode: one event, holding
	// the job's pages that count (its level 2 page too), from 09:30. The
	// webhook is told once, within 5 seconds.
	t10 := e.clock().Truncate(time.Second)
	start := time.Now()
	opener := e.mustPost("a", "0100003", alarmPage("EX0000301", "3", "STN3"), t10).MessageID
	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hook.Requests()) == 1 }) {
		t.Fatalf("within 5 s: %d deliveries, want 1", len(hook.Requests()))
	}
	if !e.largeEventField(opener) {
		t.Error("level 3 isn't stored as a listed value")
	}
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || !evs[0].Start.Equal(t0930) || !evs[0].End.Equal(t10) || evs[0].Jobs != 1 || evs[0].EmergencyPages != 2 {
		t.Fatalf("after level 3: %+v, want one event from %v to %v, 1 job, 2 Emergency pages", evs, t0930, t10)
	}
	id := evs[0].ID
	e.noLevelsOrCodes(e.user, id)
	e.eventDelivery("B", hook.Requests()[0], id, opener, t10)
	if body := string(hook.Requests()[0].Body); strings.Contains(strings.ToLower(body), "alarm") {
		t.Errorf("the delivery names the alarm level: %s", body)
	}

	// 15:00: a page with a code on another job and a level 5 page on a third
	// join the event, with a second receiver's copy: nothing more is sent.
	e.advance(5 * time.Hour)
	t15 := e.clock().Truncate(time.Second)
	e.mustPost("a", "0100004", dgPage("EX0000302", "DG0811", "STN4"), t15.Add(-2*time.Minute))
	e.mustPost("a", "0100001", alarmPage("EX0000303", "5", "STN1"), t15.Add(-time.Minute))
	e.mustPost("b", "0100001", alarmPage("EX0000303", "5", "STN1"), t15.Add(-time.Minute+2*time.Second))
	time.Sleep(1500 * time.Millisecond)
	if n := len(hook.Requests()); n != 1 {
		t.Fatalf("after a code and another level joined: %d deliveries, want still 1", n)
	}
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != id || evs[0].Jobs != 3 || evs[0].EmergencyPages != 4 {
		t.Fatalf("at 15:00: %+v, want event %d with 3 jobs and 4 Emergency pages", evs, id)
	}

	// After 12 quiet hours, a level 4 page opens a new event, told of in turn.
	e.advance(13 * time.Hour)
	t04 := e.clock().Truncate(time.Second)
	second := e.mustPost("a", "0100002", alarmPage("EX0000304", "4", "STN2"), t04).MessageID
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 2 }) {
		t.Fatalf("the new event: %d deliveries, want 2", len(hook.Requests()))
	}
	evs = e.largeEvents(e.user)
	if len(evs) != 2 || evs[1].ID != id || evs[0].EmergencyPages != 1 {
		t.Fatalf("after 12 quiet hours: %+v, want a new event beside the old", evs)
	}
	e.eventDelivery("B", hook.Requests()[1], evs[0].ID, second, t04)
	if ds := e.deliveries(e.user, v.ID); len(ds) != 2 {
		t.Errorf("deliveries %+v, want one per event", ds)
	}
	t.Logf("A88: level 2 opened nothing; level 3 opened event %d (2 pages, from the job's level 2 page), told once; a code and level 5 joined it; level 4 after 12 quiet hours opened event %d", id, evs[0].ID)
}

// R87, R87a: an event a code opened is joined by a listed level on another
// job, and an event a level opened by a code, the same as two codes; a level
// 2 page of an unrelated job joins neither.
func TestFieldValueJoinsCodeEvents(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-3 * time.Hour)
	e.mustPost("a", "0100001", dgPage("EX0000311", "DG0812", "STN1"), at)
	e.mustPost("a", "0100002", alarmPage("EX0000312", "6", "STN2"), at.Add(time.Hour))
	e.mustPost("a", "0100003", alarmPage("EX0000313", "2", "STN3"), at.Add(90*time.Minute))
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].Jobs != 2 || evs[0].EmergencyPages != 2 {
		t.Fatalf("a code, then a level: %+v, want one event of 2 jobs and 2 pages", evs)
	}
	// A level 3 page 20 hours earlier opens an event of its own; a code page
	// between the two, within 12 hours of each, merges them (R87a).
	e.mustPost("a", "0100004", alarmPage("EX0000314", "3", "STN4"), at.Add(-20*time.Hour))
	if evs = e.largeEvents(e.user); len(evs) != 2 {
		t.Fatalf("a level 20 hours earlier: %d events, want 2", len(evs))
	}
	e.mustPost("a", "0100001", dgPage("EX0000315", "DG0813", "STN1"), at.Add(-10*time.Hour))
	if evs = e.largeEvents(e.user); len(evs) != 1 || evs[0].Jobs != 4 || evs[0].EmergencyPages != 4 {
		t.Fatalf("after the merge: %+v, want one event of 4 jobs and 4 pages", evs)
	}
}

// reparseAll queues a re-parse of every page, as a changed profile does at
// start-up (R23a), and runs it, rebuilding the events around them (R87f).
func (e *env) reparseAll() {
	e.t.Helper()
	if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: "%", Reason: "profiles changed"}); err != nil {
		e.t.Fatal(err)
	}
	e.reparseNow()
}

// withoutRule makes ingest and the re-parse worker use the example agency
// without its large_event_fields, and returns the agencies they had.
func (e *env) withoutRule() map[string]*profile.Agency {
	e.t.Helper()
	was := e.srv.Ingest.Agencies
	fire := *was["example-fire"]
	fire.LargeEventFields = nil
	none := map[string]*profile.Agency{"example-fire": &fire}
	e.srv.Ingest.Agencies, e.worker.Agencies = none, none
	return was
}

// R87f: a rebuild (here after every page is re-parsed) keeps an event a
// field value opened, with its ID and pages, and sends nothing.
func TestRebuildKeepsFieldValueEvents(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})
	at := e.now.Add(-time.Minute)
	opener := e.mustPost("a", "0100001", alarmPage("EX0000331", "3", "STN1"), at).MessageID
	// Level 2 on another job: in no event, before the rebuild or after.
	e.mustPost("a", "0100002", alarmPage("EX0000332", "2", "STN2"), at.Add(10*time.Second))
	evs := e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("before the re-parse: %+v, want one event", evs)
	}
	id := evs[0].ID
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 1 }) {
		t.Fatalf("the event's delivery: %d, want 1", len(hook.Requests()))
	}

	e.reparseAll()
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != id || evs[0].EmergencyPages != 1 || evs[0].Jobs != 1 {
		t.Fatalf("after the rebuild: %+v, want event %d still, with 1 page", evs, id)
	}
	if held := e.heldBy(id); len(held) != 1 || held[0] != opener {
		t.Errorf("after the rebuild the event holds %v, want [%d]", held, opener)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := len(hook.Requests()); n != 1 {
		t.Errorf("after the rebuild: %d deliveries, want still 1", n)
	}
}

// R23a, R87f: pages stored before the profile listed their level open no
// event; once the rule is added and the pages re-parsed (a changed profile
// re-parses everything at start-up), the rebuild forms the events live
// ingest would have made, from the listed levels only, and tells no
// integration (R87d: only live ingest does).
func TestRuleAddedThenReparseFormsEvents(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})
	withRule := e.withoutRule()

	at := e.now.Add(-30 * time.Hour)
	l3 := e.mustPost("a", "0100001", alarmPage("EX0000341", "3", "STN1"), at).MessageID
	l2 := e.mustPost("a", "0100002", alarmPage("EX0000342", "2", "STN2"), at.Add(time.Hour)).MessageID
	l5 := e.mustPost("a", "0100003", alarmPage("EX0000343", "5", "STN3"), at.Add(20*time.Hour)).MessageID
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("with no rule: %d events, want none", len(evs))
	}
	if e.largeEventField(l3) || e.largeEventField(l5) {
		t.Fatal("with no rule, pages are flagged")
	}

	e.worker.Agencies = withRule
	e.reparseAll()
	if !e.largeEventField(l3) || e.largeEventField(l2) || !e.largeEventField(l5) {
		t.Errorf("after the re-parse: flags %v %v %v, want true false true", e.largeEventField(l3), e.largeEventField(l2), e.largeEventField(l5))
	}
	// Level 3 and level 5 are 20 hours apart: two events, each of one page.
	evs := e.largeEvents(e.user)
	if len(evs) != 2 || evs[0].EmergencyPages != 1 || evs[1].EmergencyPages != 1 {
		t.Fatalf("after the re-parse: %+v, want two events of one page each", evs)
	}
	if a, b := e.heldBy(evs[1].ID), e.heldBy(evs[0].ID); len(a) != 1 || a[0] != l3 || len(b) != 1 || b[0] != l5 {
		t.Errorf("the events hold %v and %v, want [%d] and [%d]", a, b, l3, l5)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := len(hook.Requests()); n != 0 {
		t.Errorf("the rebuild sent %d deliveries, want none", n)
	}
	if ds := e.deliveries(e.user, v.ID); len(ds) != 0 {
		t.Errorf("deliveries %+v, want none", ds)
	}
}
