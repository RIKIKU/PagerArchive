package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/jackc/pgx/v5"
)

const (
	pageSize = 50
	jobLimit = 500
)

func (s *Server) isAdmin(r *http.Request) bool { return currentUser(r).Role == accounts.RoleAdmin }

func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			problem(w, http.StatusForbidden, "admins only")
			return
		}
		h(w, r)
	}
}

// Message is a message as the browser sees it.
type Message struct {
	ID          int64     `json:"id"`
	Capcode     string    `json:"capcode"`
	Text        string    `json:"text"`         // stored text, marker included
	DisplayText string    `json:"display_text"` // as a pager shows it: no marker (R31, A10)
	Priority    *string   `json:"priority"`
	Heartbeat   bool      `json:"heartbeat"`
	SentAt      time.Time `json:"sent_at"`
	AliasID     *int64    `json:"alias_id"` // for the alias view (R35d)
	Alias       *string   `json:"alias"`
	Agency      *string   `json:"agency"`
	Icon        *string   `json:"icon"`
	Colour      *string   `json:"colour"`
	Ignored     bool      `json:"ignored"`
	Category    *string   `json:"category"` // report category of event_type (R38)

	Format      *string           `json:"format"`
	JobNumber   *string           `json:"job_number"`
	EventType   *string           `json:"event_type"`
	Description *string           `json:"description"`
	Address     *string           `json:"address"`
	Locality    *string           `json:"locality"`
	GridRef     *string           `json:"grid_ref"`
	Group       *string           `json:"group"`
	Units       []string          `json:"units"`
	Extra       map[string]any    `json:"extra"`
	Labels      map[string]string `json:"labels"` // field -> display label (R9, P3)

	Flags      *Flags      `json:"flags,omitempty"`      // admins only (R35c)
	Receptions []Reception `json:"receptions,omitempty"` // detail view only
}

// Flags are shown to admins only (R35c).
type Flags struct {
	ParseFailed      bool `json:"parse_failed"`
	AddressUnsplit   bool `json:"address_unsplit"`
	LocationMismatch bool `json:"location_mismatch"`
}

// Reception is one client's receive time. Client names may reveal where
// receivers are, so only admins see them (R39; ADR 0003).
type Reception struct {
	Client     string    `json:"client"`
	ReceivedAt time.Time `json:"received_at"`
}

func (s *Server) toMessage(m db.SearchMessagesRow, admin bool) Message {
	out := Message{
		ID: m.ID, Capcode: m.Capcode, Text: m.Text, DisplayText: m.Text, Priority: m.Priority,
		Heartbeat: m.Heartbeat, SentAt: m.SentAt.UTC(), AliasID: m.AliasID, Alias: m.AliasName, Agency: m.Agency,
		Icon: m.Icon, Colour: m.Colour, Ignored: m.AliasIgnored,
		Format: m.Format, JobNumber: m.JobNumber, EventType: m.EventType, Description: m.Description,
		Address: m.Address, Locality: m.Locality, GridRef: m.GridRef, Group: m.PagingGroup,
		Units: m.Units, Extra: map[string]any{}, Labels: map[string]string{},
	}
	if m.Priority != nil {
		out.DisplayText = pipeline.Body(pipeline.Prepared{Text: m.Text, Priority: profile.Priority(*m.Priority)})
	}
	if m.EventType != nil {
		c := pipeline.Category(s.Ingest.Network, *m.EventType)
		out.Category = &c
	}
	_ = json.Unmarshal(m.Extra, &out.Extra)
	if m.AgencyProfile != nil {
		if a := s.Ingest.Agencies[*m.AgencyProfile]; a != nil {
			values := map[string]*string{"event_type": m.EventType, "group": m.PagingGroup, "format": m.Format}
			for k, v := range out.Extra {
				if str, ok := v.(string); ok {
					values[k] = &str
				}
			}
			for field, table := range a.Labels {
				if v := values[field]; v != nil {
					if label, ok := table[*v]; ok {
						out.Labels[field] = label
					}
				}
			}
		}
	}
	if admin {
		out.Flags = &Flags{ParseFailed: m.ParseFailed, AddressUnsplit: m.AddressUnsplit, LocationMismatch: m.LocationMismatch}
	}
	return out
}

// Meta tells the browser how to present data.
type Meta struct {
	Role        string   `json:"role"`
	AccountURL  string   `json:"account_url"`           // Authentik's page for name and password
	Domains     []string `json:"allowed_email_domains"` // where invitations may go (R41)
	TimeZone    string   `json:"time_zone"`
	Categories  []string `json:"categories"`
	Attribution string   `json:"attribution"`
}

func (s *Server) getMeta(w http.ResponseWriter, r *http.Request) {
	m := Meta{Role: currentUser(r).Role, AccountURL: s.AccountURL, Domains: s.Accounts.AllowedDomains, TimeZone: s.Location.String(), Attribution: s.Ingest.Network.Attribution}
	for _, c := range s.Ingest.Network.Categories {
		m.Categories = append(m.Categories, c.Name)
	}
	m.Categories = append(m.Categories, "Other")
	writeJSON(w, http.StatusOK, m)
}

// MessagePage is one page of the message list.
type MessagePage struct {
	Messages []Message `json:"messages"`
	Next     string    `json:"next,omitempty"` // cursor for the following page
}

func (s *Server) search(r *http.Request, f search.Filter, before *search.Cursor, limit int, onlyID *int64) ([]db.SearchMessagesRow, error) {
	p, err := f.Params(s.Ingest.Network, before, limit)
	if err != nil {
		return nil, err
	}
	p.OnlyID = onlyID
	rows, err := s.Store.SearchMessages(r.Context(), p)
	if err != nil {
		return nil, fmt.Errorf("search: %s", store.SafeError(err))
	}
	return rows, nil
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(r)
	f, err := search.Parse(r.URL.Query(), admin, s.Location)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	before, err := search.DecodeCursor(r.URL.Query().Get("before"))
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := f.Params(s.Ingest.Network, nil, 1); err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.search(r, f, before, pageSize+1, nil)
	if err != nil {
		s.Log.Error("list messages", "error", err.Error())
		problem(w, http.StatusInternalServerError, "search failed")
		return
	}
	page := MessagePage{Messages: []Message{}}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.Next = search.EncodeCursor(search.Cursor{SentAt: last.SentAt, ID: last.ID})
		rows = rows[:pageSize]
	}
	for _, m := range rows {
		page.Messages = append(page.Messages, s.toMessage(m, admin))
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "no such message")
		return
	}
	admin := s.isAdmin(r)
	// A detail view shows the message whatever the list's filters (ignored included).
	rows, err := s.search(r, search.Filter{Ignored: true}, nil, 1, &id)
	if err != nil {
		s.Log.Error("get message", "error", err.Error())
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if len(rows) == 0 {
		problem(w, http.StatusNotFound, "no such message")
		return
	}
	msg := s.toMessage(rows[0], admin)
	recs, err := s.Store.MessageReceptions(r.Context(), id)
	if err != nil {
		s.Log.Error("receptions", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	names := map[string]string{}
	for _, rc := range recs {
		label := rc.ClientName
		if !admin {
			if _, seen := names[rc.ClientID]; !seen {
				names[rc.ClientID] = fmt.Sprintf("Receiver %d", len(names)+1)
			}
			label = names[rc.ClientID]
		}
		msg.Receptions = append(msg.Receptions, Reception{Client: label, ReceivedAt: rc.ReceivedAt.UTC()})
	}
	writeJSON(w, http.StatusOK, msg)
}

// Job is every message with one job number (R34).
type Job struct {
	JobNumber string    `json:"job_number"`
	Messages  []Message `json:"messages"`
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job := r.PathValue("job")
	admin := s.isAdmin(r)
	rows, err := s.search(r, search.Filter{Job: job, Ignored: true}, nil, jobLimit, nil)
	if err != nil {
		s.Log.Error("job", "error", err.Error())
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := Job{JobNumber: job, Messages: []Message{}}
	for _, m := range rows {
		out.Messages = append(out.Messages, s.toMessage(m, admin))
	}
	writeJSON(w, http.StatusOK, out)
}

// UnaliasedCapcode is a capcode with no alias (R33).
type UnaliasedCapcode struct {
	Capcode   string    `json:"capcode"`
	Messages  int64     `json:"messages"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

func (s *Server) unaliased(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.UnaliasedCapcodes(r.Context())
	if err != nil {
		s.Log.Error("unaliased", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []UnaliasedCapcode{}
	for _, c := range rows {
		out = append(out, UnaliasedCapcode{Capcode: c.Capcode, Messages: c.Messages, FirstSeen: c.FirstSeen.UTC(), LastSeen: c.LastSeen.UTC()})
	}
	writeJSON(w, http.StatusOK, out)
}

// ClientStatus is a receiving client's status (admins only, R39).
type ClientStatus struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Revoked    bool       `json:"revoked"`
	ClockAhead bool       `json:"clock_ahead"`
	LastSeen   *time.Time `json:"last_seen"`
}

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListClients(r.Context())
	if err != nil {
		s.Log.Error("clients", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []ClientStatus{}
	for _, c := range rows {
		out = append(out, ClientStatus{ID: c.ID, Name: c.Name, Revoked: c.RevokedAt != nil, ClockAhead: c.ClockAheadSince != nil, LastSeen: c.LastSeenAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// stream sends new messages that match the viewer's filter as Server-Sent
// Events (R32). The browser reloads the list when it reconnects.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(r)
	f, err := search.Parse(r.URL.Query(), admin, s.Location)
	if err == nil {
		_, err = f.Params(s.Ingest.Network, nil, 1)
	}
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	sub := s.Hub.Subscribe(currentUser(r).ID)
	defer s.Hub.Unsubscribe(sub)
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Dropped:
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
		case id := <-sub.C:
			rows, err := s.search(r, f, nil, 1, &id)
			if err != nil {
				s.Log.Error("stream", "error", err.Error())
				return
			}
			if len(rows) == 0 {
				continue // doesn't match this viewer's filter (A4)
			}
			data, _ := json.Marshal(s.toMessage(rows[0], admin))
			fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", id, data)
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// AliasView is an alias as the alias view shows it (R35e). Read-only in v1.
type AliasView struct {
	ID       int64   `json:"id"`
	Pattern  string  `json:"pattern"` // capcode, or pattern with % and _
	Name     string  `json:"name"`
	Template *string `json:"template"`
	Agency   *string `json:"agency"`
	Icon     *string `json:"icon"`
	Colour   *string `json:"colour"`
	Ignored  bool    `json:"ignored"`
}

func (s *Server) getAlias(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "no such alias")
		return
	}
	a, err := s.Store.AliasView(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "no such alias")
		return
	}
	if err != nil {
		s.Log.Error("alias", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, AliasView{ID: a.ID, Pattern: a.Pattern, Name: a.Name, Template: a.TemplateName,
		Agency: a.Agency, Icon: a.Icon, Colour: a.Colour, Ignored: a.Ignored})
}

// CapcodeView is a capcode as the alias view shows it when the capcode has
// no alias (R35e). AliasID is set instead when an alias now covers it.
type CapcodeView struct {
	Capcode   string     `json:"capcode"`
	AliasID   *int64     `json:"alias_id"`
	Messages  int64      `json:"messages"`
	FirstSeen *time.Time `json:"first_seen"`
	LastSeen  *time.Time `json:"last_seen"`
}

func (s *Server) getCapcode(w http.ResponseWriter, r *http.Request) {
	capcode, err := catalog.NormalisePattern(r.PathValue("capcode"))
	if err != nil || strings.ContainsAny(capcode, "%_") {
		problem(w, http.StatusNotFound, "no such capcode")
		return
	}
	out := CapcodeView{Capcode: capcode}
	cands, err := s.Store.AliasesMatching(r.Context(), capcode)
	if err == nil {
		c := make([]ingest.Candidate, len(cands))
		for i, a := range cands {
			c[i] = ingest.Candidate{ID: a.ID, Pattern: a.Pattern, AgencyProfile: a.AgencyProfile}
		}
		if best := ingest.BestAlias(c); best != nil {
			out.AliasID = &best.ID
		}
	}
	var rows []db.UnaliasedCapcodeRow
	if err == nil {
		rows, err = s.Store.UnaliasedCapcode(r.Context(), capcode)
	}
	if err != nil {
		s.Log.Error("capcode", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if len(rows) > 0 {
		first, last := rows[0].FirstSeen.UTC(), rows[0].LastSeen.UTC()
		out.Messages, out.FirstSeen, out.LastSeen = rows[0].Messages, &first, &last
	} else if out.AliasID == nil {
		problem(w, http.StatusNotFound, "no messages to this capcode")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
