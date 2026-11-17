package acceptance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
)

// A121 at the API level (R8, ADR 0040): the call sign is call_sign in the
// API's answers and integrations' payloads, never group; the old spellings
// group and group_like still search, and an integration filter sent as group
// is saved and returned as call_sign and still selects by it. (The browser
// half is in web/tests/e2e.spec.ts; the upgrade half is A77's.)
func TestA121CallSign(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()

	// An integration whose filter says group, as a script from before the
	// rename would send it.
	code, body := e.user.raw(http.MethodPost, "/api/v1/integrations", map[string]any{
		"name": "Station 1's call sign", "kind": "webhook", "capcodes": []string{"0100001"},
		"filter": map[string]any{"group": "stn1"}, "secrets": map[string]any{"url": hook.URL + "/hook"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create with the old spelling: %d %s", code, body)
	}
	var v integrations.View
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if v.Filter.CallSign != "stn1" || strings.Contains(body, `"group"`) {
		t.Errorf("created: filter %+v, body %s", v.Filter, body)
	}
	var stored string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT filter::text FROM integrations WHERE id = $1", v.ID).Scan(&stored); err != nil || stored != `{"call_sign": "stn1"}` {
		t.Errorf("stored filter %s (%v)", stored, err)
	}

	start := time.Now()
	e.mustPost("a", "0100001", "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN2", e.now)
	stn1 := e.mustPost("a", "0100001", row1Text, e.now).MessageID
	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hook.Requests()) >= 1 }) {
		t.Fatal("no request within 5 s")
	}
	time.Sleep(time.Second) // the STN2 page would have arrived by now
	reqs := hook.Requests()
	if len(reqs) != 1 || reqs[0].JSON["id"] != float64(stn1) {
		t.Fatalf("webhook got %d requests, want only STN1's page", len(reqs))
	}
	fields, _ := reqs[0].JSON["fields"].(map[string]any)
	if reqs[0].JSON["version"] != 2.0 || fields["call_sign"] != "STN1" {
		t.Errorf("payload version %v, fields %v", reqs[0].JSON["version"], fields)
	}
	if _, old := fields["group"]; old {
		t.Errorf("the payload still says group: %v", fields)
	}

	// Answers say call_sign, never group.
	for _, path := range []string{"/api/v1/messages", "/api/v1/search/values", "/api/v1/integrations"} {
		code, body := e.user.raw(http.MethodGet, path, nil)
		if code != http.StatusOK || !strings.Contains(body, `"call_sign"`) || strings.Contains(body, `"group"`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}

	// The new and the old spellings find the same pages.
	for q, want := range map[string]int{
		"call_sign=STN1": 1, "call_sign=stn2": 1, "call_sign_like=STN%25": 2,
		"group=STN1": 1, "group_like=STN%25": 2, "call_sign=STN1&group=STN2": 2,
	} {
		if got := len(e.list(q)); got != want {
			t.Errorf("%s: %d pages, want %d", q, got, want)
		}
	}
	t.Logf("A121: a filter sent as group is stored as %s and selects STN1; answers and the payload say call_sign; group= and group_like= still search", stored)
}
