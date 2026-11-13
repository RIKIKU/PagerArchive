package integrations

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// R87d: a large event's delivery is a summary, whatever the integration's
// content, and every preset's message holds the summary and the link only:
// no page text, appliances, codes or levels (owner, 2026-11-13).
func TestEventPayloadIsASummary(t *testing.T) {
	app, _ := url.Parse("http://pager.example.test")
	s := &Service{AppURL: app, Location: time.UTC}
	id := int64(42)
	opened := time.Date(2026, 11, 10, 10, 20, 0, 0, time.UTC)
	ev := EventSummary{ID: 7, StartedAt: opened.Add(-20 * time.Minute), OpenedBy: OpenedBy{ID: &id, SentAt: opened},
		Jobs: []string{"EX0000101", "EX0000102"}, EmergencyPages: 3, URL: s.eventURL(7)}
	p := page{ID: &id, SentAt: opened, Event: &ev}
	for _, content := range []string{Full, Summary} {
		l := loaded{row: db.Integration{Uid: "abcdefghijkl", Name: "Big days", Content: content}, kind: Webhook}
		body, err := payload(l, p)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		le, _ := got["large_event"].(map[string]any)
		if got["event_type"] != "large_event" || got["content"] != Summary || got["id"] != float64(42) || got["url"] != "http://pager.example.test/large-events/7" || le == nil {
			t.Fatalf("%s: %s", content, body)
		}
		ob, _ := le["opened_by"].(map[string]any)
		if le["id"] != float64(7) || le["emergency_pages"] != float64(3) || le["started_at"] != "2026-11-10T10:00:00Z" ||
			ob["id"] != float64(42) || ob["sent_at"] != "2026-11-10T10:20:00Z" || len(le["jobs"].([]any)) != 2 {
			t.Errorf("%s: summary %v", content, le)
		}
		// Exactly these keys: nothing else can creep in.
		var keys []string
		for k := range le {
			keys = append(keys, k)
		}
		if len(keys) != 6 {
			t.Errorf("%s: the summary's keys are %v, want id, started_at, opened_by, jobs, emergency_pages and url", content, keys)
		}
		for _, k := range []string{"text", "fields", "extra", "location", "labels", "agency", "capcode", "alias"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s: the payload has %q", content, k)
			}
		}
		for _, k := range []string{"units", "appliances", "level", "level_label", "reached_at", "deployment_codes"} {
			if _, ok := le[k]; ok {
				t.Errorf("%s: the summary has %q", content, k)
			}
		}
		if strings.Contains(strings.ToLower(string(body)), "deploy") {
			t.Errorf("%s: the payload says deployment: %s", content, body)
		}
	}
	r := s.render(loaded{row: db.Integration{Content: Full}}, p)
	if r.Title != "Large event" || r.Link == nil || *r.Link != "http://pager.example.test/large-events/7" {
		t.Errorf("rendered %+v", r)
	}
	for _, want := range []string{"Started 10 Nov 2026, 10:00:00", "opened 10 Nov 2026, 10:20:00", "2 jobs (EX0000101, EX0000102)", "3 Emergency pages"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body %q lacks %q", r.Body, want)
		}
	}
	if b := strings.ToLower(r.Body); strings.Contains(b, "unit") || strings.Contains(b, "appliance") || strings.Contains(b, "deploy") || strings.Contains(b, "level") {
		t.Errorf("body %q names appliances, codes or levels", r.Body)
	}
	// Send test (R51c): marked as a test, with no real event or page.
	tp := s.testEventPage(opened)
	body, _ := payload(loaded{row: db.Integration{Content: Summary}}, tp)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	le, _ := got["large_event"].(map[string]any)
	ob, _ := le["opened_by"].(map[string]any)
	if got["test"] != true || got["event_type"] != "test" || got["id"] != nil || ob == nil || ob["id"] != nil {
		t.Errorf("test payload %s", body)
	}
	if r := s.render(loaded{row: db.Integration{Content: Summary}}, tp); r.Title != "Test · Large event" {
		t.Errorf("test title %q", r.Title)
	}
}
