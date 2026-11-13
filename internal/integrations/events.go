package integrations

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// EventSummary is a large event as a large-event integration sends it
// (R87d): the event's start, the page that opened it, its jobs and Emergency
// pages so far, and a link to its view. It never holds page text, an
// address, a location, appliances or the codes in its pages.
type EventSummary struct {
	ID             int64     `json:"id"`
	StartedAt      time.Time `json:"started_at"`
	OpenedBy       OpenedBy  `json:"opened_by"`
	Jobs           []string  `json:"jobs"`
	EmergencyPages int64     `json:"emergency_pages"`
	URL            *string   `json:"url"`
}

// OpenedBy is the page that opened a large event: its ID (null in a Send
// test) and sent time.
type OpenedBy struct {
	ID     *int64    `json:"id"`
	SentAt time.Time `json:"sent_at"`
}

// eventPayload is the webhook and MQTT JSON of a large-event delivery
// (docs/integrations.md). Its header matches a page's, so a receiver can
// tell them apart by event_type alone.
type eventPayload struct {
	Version     int          `json:"version"`
	EventType   string       `json:"event_type"` // "large_event", or "test"
	Test        bool         `json:"test"`
	ID          *int64       `json:"id"` // the page that opened the event (R51b)
	URL         *string      `json:"url"`
	Integration ref          `json:"integration"`
	Content     string       `json:"content"` // always summary
	SentAt      time.Time    `json:"sent_at"` // that page's sent time
	LargeEvent  EventSummary `json:"large_event"`
}

func eventJSON(l loaded, p page) ([]byte, error) {
	kind := "large_event"
	if p.Test {
		kind = "test"
	}
	return json.Marshal(eventPayload{Version: PayloadVersion, EventType: kind, Test: p.Test, ID: p.ID, URL: p.Event.URL,
		Integration: ref{ID: l.row.Uid, Name: l.row.Name}, Content: Summary, SentAt: p.SentAt, LargeEvent: *p.Event})
}

// renderEvent formats a large event for a preset: no page text, ever.
func (s *Service) renderEvent(p page) rendered {
	e := p.Event
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	when := func(t time.Time) string { return t.In(loc).Format("2 Jan 2006, 15:04:05") }
	title := "Large event"
	if p.Test {
		title = "Test · " + title
	}
	parts := []string{"Started " + when(e.StartedAt), "opened " + when(e.OpenedBy.SentAt)}
	if len(e.Jobs) > 0 {
		parts = append(parts, plural(len(e.Jobs), "job", "jobs")+" ("+strings.Join(e.Jobs, ", ")+")")
	}
	parts = append(parts, plural(int(e.EmergencyPages), "Emergency page", "Emergency pages"))
	return rendered{Title: title, Body: strings.Join(parts, " · "), Link: e.URL}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// EnqueueEvent queues a large event's delivery for every active integration
// selecting large events (R87d), in the ingest transaction q, when the event
// opens: the only time it's called (events.Detector.Stored), so each
// integration is told of an event once. It's carried by messageID, the page
// that opened the event: the delivery is on time within R51a's cut-off from
// that page, and never sent twice (R51b).
func (s *Service) EnqueueEvent(ctx context.Context, q *db.Queries, eventID, messageID int64, sentAt time.Time) error {
	if !s.Enabled() {
		return nil
	}
	cands, err := q.LargeEventIntegrations(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	late := !now.Before(sentAt.Add(Cutoff))
	for _, c := range cands {
		p := db.InsertEventDeliveryParams{IntegrationID: c.ID, MessageID: messageID, Status: statusPending, NextAttemptAt: &now, EventID: eventID}
		if late {
			p.Status, p.NextAttemptAt = statusTooOld, nil
			s.Stats.add(c.Kind, statusTooOld)
		}
		if err := q.InsertEventDelivery(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// eventSummary reads an event as it is now, for a delivery carried by the
// page that opened it. ok is false when it's gone (deleted meanwhile).
func (s *Service) eventSummary(ctx context.Context, q *db.Queries, id, messageID int64, sentAt time.Time) (EventSummary, bool, error) {
	e, err := q.LargeEventByID(ctx, id)
	if err != nil {
		return EventSummary{}, false, nil
	}
	mid := messageID
	out := EventSummary{ID: e.ID, StartedAt: e.FirstAt.UTC(), OpenedBy: OpenedBy{ID: &mid, SentAt: sentAt.UTC()}, Jobs: []string{}}
	jobs, err := q.LargeEventJobs(ctx, id)
	if err != nil {
		return out, false, err
	}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, j.JobNumber)
	}
	if out.EmergencyPages, err = q.LargeEventPages(ctx, id); err != nil {
		return out, false, err
	}
	out.URL = s.eventURL(id)
	return out, true, nil
}

func (s *Service) eventURL(id int64) *string {
	if s.AppURL == nil {
		return nil
	}
	u := s.AppURL.JoinPath("large-events", strconv.FormatInt(id, 10)).String()
	return &u
}

// testEventPage is a Send test's made-up large event (R51c), marked as a test.
func (s *Service) testEventPage(now time.Time) page {
	now = now.UTC().Truncate(time.Second)
	var link *string
	if s.AppURL != nil {
		u := s.AppURL.JoinPath("large-events").String()
		link = &u
	}
	return page{Test: true, SentAt: now, URL: link, Event: &EventSummary{
		StartedAt: now.Add(-time.Hour), OpenedBy: OpenedBy{SentAt: now}, Jobs: []string{"TEST"}, EmergencyPages: 0, URL: link,
	}}
}
