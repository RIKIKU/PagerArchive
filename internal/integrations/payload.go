package integrations

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// PayloadVersion is the payload format's version (docs/integrations.md).
const PayloadVersion = 1

// page is a message as integrations see it, built when it's sent.
type page struct {
	ID         *int64
	Test       bool
	URL        *string
	SentAt     time.Time
	Capcode    string
	Alias      *string
	Agency     *string
	Priority   *string
	Heartbeat  bool
	Category   *string
	EventLabel *string
	JobNumber  *string
	Text       string // without the priority marker (R50)
	Fields     Fields
	Extra      map[string]any
	Labels     map[string]string
	Location   *Location
	Flags      *Flags // admins' integrations only (R49)
}

// Fields are the parsed standard fields (R8).
type Fields struct {
	EventType   *string  `json:"event_type"`
	Description *string  `json:"description"`
	Address     *string  `json:"address"`
	Locality    *string  `json:"locality"`
	GridRef     *string  `json:"grid_ref"`
	Units       []string `json:"units"`
	Group       *string  `json:"group"`
}

// Location is where the message is placed (R24-R25).
type Location struct {
	Lon         float64 `json:"lon"`
	Lat         float64 `json:"lat"`
	Approximate bool    `json:"approximate"`
	Source      string  `json:"source"`
}

// Flags are admin-only (R35c).
type Flags struct {
	ParseFailed      bool `json:"parse_failed"`
	AddressUnsplit   bool `json:"address_unsplit"`
	LocationMismatch bool `json:"location_mismatch"`
}

// pageOf builds the page for a stored message. admin is the owner's role
// when it's sent.
func (s *Service) pageOf(m db.SearchMessagesRow, admin bool) page {
	id := m.ID
	p := page{
		ID: &id, SentAt: m.SentAt.UTC(), Capcode: m.Capcode, Alias: m.AliasName, Agency: m.Agency,
		Priority: m.Priority, Heartbeat: m.Heartbeat, JobNumber: m.JobNumber, Text: m.Text,
		Fields: Fields{EventType: m.EventType, Description: m.Description, Address: m.Address, Locality: m.Locality,
			GridRef: m.GridRef, Units: m.Units, Group: m.PagingGroup},
		Extra: map[string]any{}, Labels: map[string]string{},
	}
	if m.Priority != nil {
		p.Text = pipeline.Body(pipeline.Prepared{Text: m.Text, Priority: profile.Priority(*m.Priority)})
	}
	if s.AppURL != nil {
		u := s.AppURL.JoinPath("messages", strconv.FormatInt(m.ID, 10)).String()
		p.URL = &u
	}
	if m.EventType != nil {
		c := pipeline.Category(s.Network, *m.EventType)
		p.Category = &c
	}
	_ = json.Unmarshal(m.Extra, &p.Extra)
	if m.AgencyProfile != nil {
		if a := s.Agencies[*m.AgencyProfile]; a != nil {
			values := map[string]*string{"event_type": m.EventType, "group": m.PagingGroup, "format": m.Format}
			for k, v := range p.Extra {
				if str, ok := v.(string); ok {
					values[k] = &str
				}
			}
			for field, table := range a.Labels {
				if v := values[field]; v != nil {
					if label, ok := table[*v]; ok {
						p.Labels[field] = label
					}
				}
			}
		}
	}
	if l, ok := p.Labels["event_type"]; ok {
		p.EventLabel = &l
	}
	if m.Lon != nil && m.Lat != nil && m.LocationSource != nil {
		p.Location = &Location{Lon: *m.Lon, Lat: *m.Lat, Source: *m.LocationSource, Approximate: *m.LocationSource == "street"}
	}
	if admin {
		p.Flags = &Flags{ParseFailed: m.ParseFailed, AddressUnsplit: m.AddressUnsplit, LocationMismatch: m.LocationMismatch}
	}
	return p
}

// testPage is a Send test's made-up page (R51c): marked as a test, with no
// ID and no real page text.
func testPage(now time.Time, capcode string, alias *string, appURL *string) page {
	str := func(s string) *string { return &s }
	return page{
		Test: true, URL: appURL, SentAt: now.UTC().Truncate(time.Second), Capcode: capcode, Alias: alias,
		Priority: str("Emergency"), Category: str("Test"), EventLabel: str("PagerArchive test delivery"),
		JobNumber: str("TEST"), Text: "PagerArchive test delivery. This isn't a real page.",
		Fields: Fields{EventType: str("TEST"), Description: str("PagerArchive test delivery")},
		Extra:  map[string]any{}, Labels: map[string]string{},
	}
}

type ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// common is in every payload, Summary included (R50a).
type common struct {
	Version     int       `json:"version"`
	EventType   string    `json:"event_type"` // "page" or "test": Home Assistant's event format
	Test        bool      `json:"test"`
	ID          *int64    `json:"id"`
	URL         *string   `json:"url"`
	Integration ref       `json:"integration"`
	Content     string    `json:"content"`
	SentAt      time.Time `json:"sent_at"`
	Capcode     string    `json:"capcode"`
	Alias       *string   `json:"alias"`
	Priority    *string   `json:"priority"`
	Category    *string   `json:"category"`
	EventLabel  *string   `json:"event_label"`
	JobNumber   *string   `json:"job_number"`
}

type fullPayload struct {
	common
	Agency    *string           `json:"agency"`
	Heartbeat bool              `json:"heartbeat"`
	Text      string            `json:"text"`
	Fields    Fields            `json:"fields"`
	Extra     map[string]any    `json:"extra"`
	Labels    map[string]string `json:"labels"`
	Location  *Location         `json:"location"`
	Flags     *Flags            `json:"flags,omitempty"`
}

// summaryPayload has no page text, address, location or extra fields (R50a).
type summaryPayload struct {
	common
	Fields struct {
		EventType *string `json:"event_type"`
	} `json:"fields"`
}

// payload is the webhook and MQTT JSON (ADR 0006 §6).
func payload(l loaded, p page) ([]byte, error) {
	kind := "page"
	if p.Test {
		kind = "test"
	}
	c := common{
		Version: PayloadVersion, EventType: kind, Test: p.Test, ID: p.ID, URL: p.URL,
		Integration: ref{ID: l.row.Uid, Name: l.row.Name}, Content: l.row.Content, SentAt: p.SentAt,
		Capcode: p.Capcode, Alias: p.Alias, Priority: p.Priority, Category: p.Category, EventLabel: p.EventLabel, JobNumber: p.JobNumber,
	}
	if l.row.Content == Summary {
		out := summaryPayload{common: c}
		out.Fields.EventType = p.Fields.EventType
		return json.Marshal(out)
	}
	return json.Marshal(fullPayload{common: c, Agency: p.Agency, Heartbeat: p.Heartbeat, Text: p.Text, Fields: p.Fields,
		Extra: p.Extra, Labels: p.Labels, Location: p.Location, Flags: p.Flags})
}
