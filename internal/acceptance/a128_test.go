package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/golden"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// row128 is section 10's proposed row for A128 (ADR 0047): STN2 is one of
// the example's call signs, DO01 a duty officer's role, STN2T an appliance.
const row128 = "@@EX0000006 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON UNITS T1 STN2 STN2T DO01 #STN1"

// asList is a JSON list as strings, nil for null.
func asList(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// A128. Call signs apart from appliances (owner, 2026-11-18; R7, R8, R38h;
// ADR 0047). The example profile's into gives the proposed row's units and
// call_signs; the API, a Full webhook payload (version 2) and Appliance
// activity keep them apart; and a change to the values file alone re-parses
// every page, as a long job, sending nothing. (The detail view is in
// web/tests/e2e.spec.ts, the module's CallSigns in powershell/tests, and
// into's problems in internal/profile and cmd/pagerarchive.)
func TestA128CallSignsApartFromAppliances(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()

	// validate-profile's check: the proposed row passes with the example's into.
	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	ds, err := streets.LoadFile(n.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := golden.LoadFile("../../profiles/example/golden-example-fire.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := golden.Runner{Set: set, Network: n, Streets: streets.NewIndex(ds.Streets, n.StreetTypes)}.Run(tbl)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res.Rows {
		if strings.HasSuffix(r.Case.Raw, row128) {
			found = true
			if len(r.Diffs) > 0 {
				t.Errorf("the proposed row: %q", r.Diffs)
			}
		}
	}
	if !found {
		t.Fatal("no golden row for A128")
	}

	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, webhookTo("Station 1, Full", hook, "0100001"))
	id := e.mustPost("a", "0100001", row128, e.now).MessageID
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) >= 1 }) {
		t.Fatal("no webhook request within 5 s")
	}

	// The API's message: call_signs beside units, and one call sign.
	var m server.Message
	for _, x := range e.list("job=EX0000006") {
		if x.ID == id {
			m = x
		}
	}
	if !slices.Equal(m.Units, []string{"T1", "STN2T"}) || !slices.Equal(m.CallSigns, []string{"STN2", "DO01"}) || s(m.CallSign) != "STN1" {
		t.Errorf("the message: units %q, call_signs %q, call_sign %s", m.Units, m.CallSigns, s(m.CallSign))
	}
	if _, ok := m.Extra["call_signs"]; ok {
		t.Errorf("call_signs is an extra field too: %v", m.Extra)
	}
	var stored []string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT call_signs FROM messages WHERE id = $1", id).Scan(&stored); err != nil || !slices.Equal(stored, m.CallSigns) {
		t.Errorf("stored call_signs %q (%v)", stored, err)
	}

	// The Full payload: version 2, fields.call_signs beside fields.units.
	req := hook.Requests()[0]
	fields, _ := req.JSON["fields"].(map[string]any)
	if req.JSON["version"] != 2.0 || !slices.Equal(asList(fields["units"]), []string{"T1", "STN2T"}) ||
		!slices.Equal(asList(fields["call_signs"]), []string{"STN2", "DO01"}) || fields["call_sign"] != "STN1" {
		t.Errorf("payload version %v, fields %v", req.JSON["version"], fields)
	}

	// Appliance activity counts T1 and STN2T, not STN2 or DO01.
	day := "from=" + e.now.Format(time.DateOnly) + "&to=" + e.now.Format(time.DateOnly)
	var a server.AppliancesReport
	e.report("appliances", day, &a)
	var counted []string
	for _, r := range a.Rows {
		counted = append(counted, r.Appliance)
	}
	slices.Sort(counted)
	if !slices.Equal(counted, []string{"STN2T", "T1"}) {
		t.Errorf("Appliance activity counts %q, want STN2T and T1", counted)
	}

	// Only the values file changes, and the API starts: every page is
	// re-parsed, as a long job, and nothing is sent. With T1 a call sign
	// too, the page's lists change.
	e.mustPost("a", "0100002", "@@EX0000007 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON UNITS T1 #STN2", e.now)
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	start := func() int {
		t.Helper()
		set, err := profile.LoadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		hash, legacy, err := reparse.ProfileHashes(set, set.Networks["example-network"])
		if err != nil {
			t.Fatal(err)
		}
		e.worker.Agencies = set.Agencies
		e.worker.ProfileHash, e.worker.LegacyProfileHash = hash, legacy
		if err := e.worker.CheckProfiles(e.ctx); err != nil {
			t.Fatal(err)
		}
		n, err := e.worker.RunOnce(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := start(); n != 2 {
		t.Fatalf("first start: re-parsed %d, want 2", n)
	}
	if n := start(); n != 0 {
		t.Fatalf("unchanged: re-parsed %d, want 0", n)
	}
	time.Sleep(time.Second) // anything the posts would send has been sent
	sent := len(hook.Requests())
	file := filepath.Join(dir, "example-call-signs.txt")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(b, "T1\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := start(); n != 2 {
		t.Errorf("the values file changed: re-parsed %d, want every page (2)", n)
	}
	var state, reasons string
	var total, done int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT state, reasons, total, done FROM reparse_runs").Scan(&state, &reasons, &total, &done); err != nil || state != "finished" || total != 2 || done != 2 {
		t.Errorf("the re-parse's run: %s (%s), %d of %d (%v)", state, reasons, done, total, err)
	}
	for _, x := range e.list("job=EX0000006") {
		if x.ID == id && (!slices.Equal(x.Units, []string{"STN2T"}) || !slices.Equal(x.CallSigns, []string{"T1", "STN2", "DO01"})) {
			t.Errorf("re-parsed: units %q, call_signs %q", x.Units, x.CallSigns)
		}
	}
	if other := e.list("job=EX0000007"); len(other) != 1 || other[0].Units != nil || !slices.Equal(other[0].CallSigns, []string{"T1"}) {
		t.Errorf("re-parsed EX0000007: %+v", other)
	}
	time.Sleep(time.Second)
	if got := len(hook.Requests()); got != sent {
		t.Errorf("the re-parse sent %d requests", got-sent)
	}
	t.Logf("A128: units %q, call_signs %q, call_sign %s in the API and the payload (version 2); Appliance activity %q; a changed values file re-parsed both pages (%s, %d of %d) and sent nothing",
		m.Units, m.CallSigns, s(m.CallSign), counted, state, done, total)
}
