package integrations

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// R87d: a large event's delivery is a summary, whatever the integration's
// content, and every preset's message holds the summary and the link only.
func TestEventPayloadIsASummary(t *testing.T) {
	app, _ := url.Parse("http://pager.example.test")
	s := &Service{AppURL: app, Location: time.UTC}
	id := int64(42)
	reached := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	ev := EventSummary{ID: 7, Level: string(events.SeveralDeployments), LevelLabel: events.SeveralDeployments.Label(), ReachedAt: reached,
		StartedAt: reached.Add(-5 * time.Hour), DeploymentCodes: []string{"DG0801", "DG0802"}, Jobs: []string{"EX0000101", "EX0000102"}, Units: 3, URL: s.eventURL(7)}
	p := page{ID: &id, SentAt: reached, Event: &ev}
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
		if le["level"] != "several_deployments" || le["units"] != float64(3) || le["started_at"] != "2026-11-10T10:00:00Z" || le["reached_at"] != "2026-11-10T15:00:00Z" {
			t.Errorf("%s: summary %v", content, le)
		}
		for _, k := range []string{"text", "fields", "extra", "location", "labels", "agency", "capcode", "alias"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s: the payload has %q", content, k)
			}
		}
	}
	r := s.render(loaded{row: db.Integration{Content: Full}}, p)
	if r.Title != "Large event · Several deployments" || r.Link == nil || *r.Link != "http://pager.example.test/large-events/7" {
		t.Errorf("rendered %+v", r)
	}
	for _, want := range []string{"Reached 10 Nov 2026, 15:00:00", "started 10 Nov 2026, 10:00:00", "DG0801, DG0802", "2 jobs (EX0000101, EX0000102)", "3 units"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body %q lacks %q", r.Body, want)
		}
	}
	// Send test (R51c): marked as a test, with no real event.
	tp := s.testEventPage(reached, events.BigJob)
	body, _ := payload(loaded{row: db.Integration{Content: Summary}}, tp)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["test"] != true || got["event_type"] != "test" || got["id"] != nil {
		t.Errorf("test payload %s", body)
	}
	if r := s.render(loaded{row: db.Integration{Content: Summary}}, tp); !strings.HasPrefix(r.Title, "Test · Large event · Big job") {
		t.Errorf("test title %q", r.Title)
	}
}
