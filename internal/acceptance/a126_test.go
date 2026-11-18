package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// levelLabels are the example agency's made-up alarm level labels (SPEC.md
// section 10): shown in the integration form only (R49a, R87b).
var levelLabels = []string{"First alarm", "Second alarm", "Third alarm", "Fourth alarm", "Fifth alarm", "Sixth alarm", "Seventh alarm", "Eighth alarm", "Ninth alarm"}

// A126. An integration fires at or above a level (owner, 2026-11-18; R7,
// R49a, R87b). Given the example profile's alarm levels, user B's webhook
// (Full) and ntfy (Summary) integrations at "Alarm level at or above
// Second alarm" on Station 1, when pages at levels 1, none, 2 and 3 (the
// last an escalation of the level 2 page's job) arrive, then each fires
// for the level 2 and 3 pages only, once each, and no delivery names a
// level's label; the ntfy notification has no level at all.
func TestA126LevelFilter(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()

	// The form's levels: the example agency's alarm level, by label, lowest first.
	var offered []integrations.LevelChoice
	if code, p := e.user.call(http.MethodGet, "/api/v1/integrations/levels", nil, &offered); code != http.StatusOK {
		t.Fatalf("levels: %d %s", code, p.Error)
	}
	if len(offered) != 1 || offered[0].Profile != "example-fire" || offered[0].Agency != "Example Fire Service" || offered[0].Field != "alarm_level" || offered[0].Label != "Alarm level" {
		t.Fatalf("levels offered: %+v", offered)
	}
	var labels []string
	for i, l := range offered[0].Levels {
		labels = append(labels, l.Label)
		if want := string(rune('1' + i)); l.Value != want {
			t.Errorf("level %d's value: %q, want %q", i, l.Value, want)
		}
	}
	if !slices.Equal(labels, levelLabels) {
		t.Errorf("labels: %q, want %q", labels, levelLabels)
	}

	second := &integrations.LevelFilter{Profile: "example-fire", Field: "alarm_level", AtLeast: "2"}
	// A level the profile doesn't have, or a field without levels, is refused.
	for name, l := range map[string]integrations.LevelFilter{
		"level 10":  {Profile: "example-fire", Field: "alarm_level", AtLeast: "10"},
		"call sign": {Profile: "example-fire", Field: "call_sign", AtLeast: "2"},
		"profile":   {Profile: "made-up", Field: "alarm_level", AtLeast: "2"},
	} {
		hook := standin.Webhook()
		req := webhookTo("bad "+name, hook, "0100001")
		req.Filter.Level = &l
		if code, _ := e.user.call(http.MethodPost, "/api/v1/integrations", req, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", name, code)
		}
		hook.Close()
	}

	hook, ntfy := standin.Webhook(), standin.Ntfy()
	defer hook.Close()
	defer ntfy.Close()
	wreq := webhookTo("Bigger jobs", hook, "0100001")
	wreq.Filter.Level = second
	w := e.createIntegration(e.user, wreq)
	if w.Filter.Level == nil || *w.Filter.Level != *second || w.Content != integrations.Full {
		t.Fatalf("saved: %+v, content %s", w.Filter.Level, w.Content)
	}
	n := e.createIntegration(e.user, integrations.Request{Name: "ntfy bigger jobs", Kind: integrations.Ntfy, Capcodes: []string{"0100001"},
		Filter: integrations.Filter{Level: second}, Config: integrations.Config{Server: ntfy.URL}, Secrets: integrations.Secrets{Topic: "pager-levels"}})
	if n.Content != integrations.Summary {
		t.Fatalf("ntfy content %s, want summary", n.Content)
	}

	now := time.Now().UTC().Truncate(time.Second)
	e.mustPost("a", "0100001", alarmPage("EX0000501", "1", "STN1"), now.Add(-4*time.Second))
	e.mustPost("a", "0100001", alarmPage("EX0000502", "", "STN1"), now.Add(-3*time.Second))
	l2 := e.mustPost("a", "0100001", alarmPage("EX0000503", "2", "STN1"), now.Add(-2*time.Second)).MessageID
	l3 := e.mustPost("a", "0100001", alarmPage("EX0000503", "3", "STN1"), now.Add(-time.Second)).MessageID
	if !eventually(10*time.Second, func() bool { return len(hook.Requests()) >= 2 && len(ntfy.Requests()) >= 2 }) {
		t.Fatalf("deliveries: webhook %d, ntfy %d, want 2 each", len(hook.Requests()), len(ntfy.Requests()))
	}
	time.Sleep(time.Second) // nothing more comes
	if len(hook.Requests()) != 2 || len(ntfy.Requests()) != 2 {
		t.Fatalf("deliveries: webhook %d, ntfy %d, want 2 each", len(hook.Requests()), len(ntfy.Requests()))
	}
	var got []int64
	for _, r := range hook.Requests() {
		id, _ := r.JSON["id"].(float64)
		got = append(got, int64(id))
		body := string(r.Body)
		for _, l := range levelLabels {
			if strings.Contains(body, l) {
				t.Errorf("the webhook delivery names the level %q: %s", l, body)
			}
		}
		// The payload is as before: the level is an extra field (R8), with no label.
		if labels, _ := r.JSON["labels"].(map[string]any); labels["alarm_level"] != nil {
			t.Errorf("the payload labels the level: %v", labels)
		}
		if extra, _ := r.JSON["extra"].(map[string]any); extra["alarm_level"] == nil {
			t.Errorf("the payload's extra fields lost the level: %v", r.JSON["extra"])
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []int64{l2, l3}) {
		t.Errorf("webhook delivered %v, want the level 2 and 3 pages %v", got, []int64{l2, l3})
	}
	for _, r := range ntfy.Requests() {
		if body := strings.ToLower(string(r.Body)); strings.Contains(body, "alarm") {
			t.Errorf("the notification names the level: %s", r.Body)
		}
	}
	if v := append(hook.Violations(), ntfy.Violations()...); len(v) > 0 {
		t.Errorf("stand-in violations: %q", v)
	}
	t.Logf("A126: levels offered %q; at or above Second alarm, the webhook got %v and ntfy 2 (levels 2 and 3 of 1, none, 2, 3), none naming a level", labels, got)
}

// A126, ADR 0045. The profile hash leaves out what changes nothing a page
// says: changing only an agency's labels or levels queues no re-parse,
// changing a format does, and an install upgraded with a hash saved the old
// way (from the whole files), with the profiles unchanged, isn't re-parsed.
func TestA126ProfileHashLeavesOutLabelsAndLevels(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1, e.now)
	dir := t.TempDir()
	entries, err := os.ReadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		b, err := os.ReadFile(filepath.Join("../../profiles/example", f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fire := filepath.Join(dir, "example-fire.json")
	edit := func(old, new string) {
		t.Helper()
		b, err := os.ReadFile(fire)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("the example has no %s", old)
		}
		if err := os.WriteFile(fire, []byte(strings.Replace(string(b), old, new, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// start is an API start with the profiles in dir: how many it re-parses.
	start := func(saved string) int {
		t.Helper()
		set, err := profile.LoadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		hash, legacy, err := reparse.ProfileHashes(set, set.Networks["example-network"])
		if err != nil {
			t.Fatal(err)
		}
		if saved == "legacy" {
			if err := e.st.SetSetting(e.ctx, db.SetSettingParams{Key: "profile_hash", Value: legacy}); err != nil {
				t.Fatal(err)
			}
		}
		e.worker.ProfileHash, e.worker.LegacyProfileHash = hash, legacy
		if err := e.worker.CheckProfiles(e.ctx); err != nil {
			t.Fatal(err)
		}
		n, err := e.worker.RunOnce(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := e.st.GetSetting(e.ctx, "profile_hash"); got != hash {
			t.Errorf("saved hash %s, want %s", got, hash)
		}
		return n
	}

	if n := start(""); n != 1 {
		t.Fatalf("first start: re-parsed %d, want 1", n)
	}
	if n := start(""); n != 0 {
		t.Errorf("unchanged: re-parsed %d, want 0", n)
	}
	edit(`"Second alarm"`, `"Second alarm call"`)
	edit(`"HSF1": "House fire"`, `"HSF1": "Building fire"`)
	if n := start(""); n != 0 {
		t.Errorf("only labels and levels changed: re-parsed %d, want 0", n)
	}
	edit(`(?: GRID (?P<grid_ref>[0-9]{6}))?`, `(?: GRID (?P<grid_ref>[0-9]{5,6}))?`)
	if n := start(""); n != 1 {
		t.Errorf("a format changed: re-parsed %d, want 1", n)
	}
	// The upgrade: the hash was saved the old way, from these very files.
	if n := start("legacy"); n != 0 {
		t.Errorf("upgraded with the profiles unchanged: re-parsed %d, want 0", n)
	}
	// Saved the old way from other files: a change, as ever.
	if err := e.st.SetSetting(e.ctx, db.SetSettingParams{Key: "profile_hash", Value: "an older profile's hash"}); err != nil {
		t.Fatal(err)
	}
	if n := start(""); n != 1 {
		t.Errorf("upgraded with changed profiles: re-parsed %d, want 1", n)
	}
	t.Log("A126: labels and levels changed, nothing re-parsed; a format changed, re-parsed; an old-style hash of the same files, nothing re-parsed")
}
