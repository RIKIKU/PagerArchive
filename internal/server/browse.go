package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/jackc/pgx/v5"
)

const (
	pageSize    = 50
	maxPageSize = 200
	jobLimit    = 500
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
	Icon        *string   `json:"icon"` // its template's icon URL, or null (R66)
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

	Location *Location `json:"location"` // null when not placed (R24-R25)

	Flags      *Flags      `json:"flags,omitempty"`      // admins only (R35c)
	Receptions []Reception `json:"receptions,omitempty"` // detail view only
}

// Location is where a message is placed and from what: "address",
// "street" (no house number, or the number isn't in the dataset; shown as
// approximate, section 14) or "grid" (R25).
type Location struct {
	Lon         float64 `json:"lon"`
	Lat         float64 `json:"lat"`
	Source      string  `json:"source"`
	Approximate bool    `json:"approximate"`
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
		Icon: iconURL(m.IconSha256), Colour: m.Colour, Ignored: m.AliasIgnored,
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
	if m.Lon != nil && m.Lat != nil && m.LocationSource != nil {
		out.Location = &Location{Lon: *m.Lon, Lat: *m.Lat, Source: *m.LocationSource, Approximate: *m.LocationSource == geocode.FromStreet}
	}
	if admin {
		out.Flags = &Flags{ParseFailed: m.ParseFailed, AddressUnsplit: m.AddressUnsplit, LocationMismatch: m.LocationMismatch}
	}
	return out
}

// Meta tells the browser how to present data.
type Meta struct {
	Role           string   `json:"role"`
	AccountURL     string   `json:"account_url"`           // Authentik's page for name and password
	Domains        []string `json:"allowed_email_domains"` // where invitations may go (R41)
	TimeZone       string   `json:"time_zone"`
	Categories     []string `json:"categories"`
	Attribution    string   `json:"attribution"`     // the address dataset's (R24)
	MapTiles       bool     `json:"map_tiles"`       // a tile extract is configured (R35)
	MapAttribution string   `json:"map_attribution"` // the tiles' credit line
	// Admins only: alias requests waiting for review (R55), for the count
	// on the Admin link.
	WaitingRequests *int64 `json:"waiting_requests,omitempty"`
}

func (s *Server) getMeta(w http.ResponseWriter, r *http.Request) {
	m := Meta{Role: currentUser(r).Role, AccountURL: s.AccountURL, Domains: s.Accounts.AllowedDomains, TimeZone: s.Location.String(), Attribution: s.Ingest.Network.Attribution,
		MapTiles: s.MapTiles != "", MapAttribution: s.MapAttribution}
	for _, c := range s.Ingest.Network.Categories {
		m.Categories = append(m.Categories, c.Name)
	}
	m.Categories = append(m.Categories, "Other")
	if s.isAdmin(r) {
		n, err := s.Store.CountWaitingRequests(r.Context())
		if err != nil {
			s.Log.Error("meta: waiting requests", "error", store.SafeError(err))
		} else {
			m.WaitingRequests = &n
		}
	}
	writeJSON(w, http.StatusOK, m)
}

// MessagePage is one page of the message list.
type MessagePage struct {
	Messages []Message `json:"messages"`
	Next     string    `json:"next,omitempty"` // cursor for the following page
}

// search runs a message search. ids, when not empty, keeps just those messages.
func (s *Server) search(ctx context.Context, f search.Filter, before *search.Cursor, limit int, ids []int64) ([]db.SearchMessagesRow, error) {
	p, err := f.Params(s.Ingest.Network, before, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		p.OnlyIds = ids
	}
	rows, err := s.Store.SearchMessages(ctx, p)
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
	// The PowerShell module asks for as many as it needs (-First), and for
	// every message's receive times (R63a).
	limit := pageSize
	if v := r.URL.Query().Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxPageSize {
			problem(w, http.StatusBadRequest, fmt.Sprintf("limit must be 1 to %d", maxPageSize))
			return
		}
	}
	rows, err := s.search(r.Context(), f, before, limit+1, nil)
	if err != nil {
		s.Log.Error("list messages", "error", err.Error())
		problem(w, http.StatusInternalServerError, "search failed")
		return
	}
	page := MessagePage{Messages: []Message{}}
	if len(rows) > limit {
		last := rows[limit-1]
		page.Next = search.EncodeCursor(search.Cursor{SentAt: last.SentAt, ID: last.ID})
		rows = rows[:limit]
	}
	for _, m := range rows {
		page.Messages = append(page.Messages, s.toMessage(m, admin))
	}
	if r.URL.Query().Get("receptions") == "true" {
		if err := s.addReceptions(r.Context(), page.Messages, admin); err != nil {
			s.Log.Error("receptions", "error", err.Error())
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, page)
}

// addReceptions adds each message's receive times, in one query, for the
// PowerShell module (R63a).
func (s *Server) addReceptions(ctx context.Context, msgs []Message, admin bool) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	recs, err := s.Store.ReceptionsForMessages(ctx, ids)
	if err != nil {
		return errors.New(store.SafeError(err))
	}
	byMessage := map[int64][]db.MessageReceptionsRow{}
	for _, rc := range recs {
		byMessage[rc.MessageID] = append(byMessage[rc.MessageID], db.MessageReceptionsRow{ClientID: rc.ClientID, ClientName: rc.ClientName, ReceivedAt: rc.ReceivedAt})
	}
	for i := range msgs {
		msgs[i].Receptions = receptions(byMessage[msgs[i].ID], admin)
	}
	return nil
}

// receptions labels receive times. Client names may reveal where receivers
// are, so users see "Receiver 1", "Receiver 2" (R39).
func receptions(recs []db.MessageReceptionsRow, admin bool) []Reception {
	out := []Reception{}
	names := map[string]string{}
	for _, rc := range recs {
		label := rc.ClientName
		if !admin {
			if _, seen := names[rc.ClientID]; !seen {
				names[rc.ClientID] = fmt.Sprintf("Receiver %d", len(names)+1)
			}
			label = names[rc.ClientID]
		}
		out = append(out, Reception{Client: label, ReceivedAt: rc.ReceivedAt.UTC()})
	}
	return out
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "no such message")
		return
	}
	admin := s.isAdmin(r)
	// A detail view shows the message whatever the list's filters (ignored included).
	rows, err := s.search(r.Context(), search.Filter{Ignored: true}, nil, 1, []int64{id})
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
	msg.Receptions = receptions(recs, admin)
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
	rows, err := s.search(r.Context(), search.Filter{Job: []string{job}, Ignored: true}, nil, jobLimit, nil)
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
	ClockAhead bool       `json:"clock_ahead"`
	LastSeen   *time.Time `json:"last_seen"`
	Receiving  bool       `json:"receiving"` // R39: a message of any kind in the last 30 minutes
}

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListClients(r.Context())
	if err != nil {
		s.Log.Error("clients", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []ClientStatus{}
	now := s.Now()
	for _, c := range rows {
		if c.RevokedAt != nil {
			continue // a revoked client can't send, so it isn't monitored
		}
		out = append(out, ClientStatus{ID: c.ID, Name: c.Name, ClockAhead: c.ClockAheadSince != nil, LastSeen: c.LastSeenAt, Receiving: receiving(c, now)})
	}
	writeJSON(w, http.StatusOK, out)
}

// catchUpBatch is how many stored messages a catch-up reads at a time.
const catchUpBatch = 200

// stream sends new messages that match the viewer's filter as Server-Sent
// Events (R32). The browser reloads the list when it reconnects.
//
// For the PowerShell module's watch (ADR 0010): after_id first sends the
// matching messages stored after that one, oldest first; a ready event then
// says the stream is live and gives the next after_id; receptions=true adds
// receive times. The stream ends with a
// reconnect event when the hub drops it (the user's status or role changed,
// or it fell behind) and, for a bearer token, when the token expires, so a
// stream never outlives the checks a request would get (R60).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := currentUser(r)
	admin := u.Role == accounts.RoleAdmin
	q := r.URL.Query()
	f, err := search.Parse(q, admin, s.Location)
	if err == nil {
		_, err = f.Params(s.Ingest.Network, nil, 1)
	}
	var after *int64
	if v := q.Get("after_id"); err == nil && v != "" {
		id, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || id < 0 {
			err = errors.New("after_id must be a message ID")
		}
		after = &id
	}
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	withReceptions := q.Get("receptions") == "true"
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	reconnect := func(reason string) {
		data, _ := json.Marshal(map[string]string{"reason": reason})
		fmt.Fprintf(w, "event: reconnect\ndata: %s\n\n", data)
		_ = rc.Flush()
	}
	send := func(rows []db.SearchMessagesRow) error {
		msgs := make([]Message, len(rows))
		for i, m := range rows {
			msgs[i] = s.toMessage(m, admin)
		}
		if withReceptions {
			if err := s.addReceptions(ctx, msgs, admin); err != nil {
				return err
			}
		}
		for _, m := range msgs {
			data, _ := json.Marshal(m)
			fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", m.ID, data)
		}
		return nil
	}

	sub := s.Hub.Subscribe(u.ID)
	defer s.Hub.Unsubscribe(sub)
	// The sign-in was checked before subscribing, so a change of status or
	// role in between found no connection to close: look again. A change
	// after this finds the subscription (ADR 0010 §4).
	if now, err := s.Store.UserByID(ctx, u.ID); err != nil || now.Status != "active" || now.Role != u.Role {
		if err != nil {
			s.Log.Error("stream: user", "error", store.SafeError(err))
		}
		reconnect(live.Account)
		return
	}
	var expired <-chan time.Time
	if exp, ok := tokenExpiry(r); ok {
		t := time.NewTimer(exp.Sub(s.Now()))
		defer t.Stop()
		expired = t.C
	}

	// Where the stream starts: after after_id, catching up on what was stored
	// since, or after the newest message now. Either runs after subscribing,
	// so a page stored meanwhile is in the reads, announced afterwards, or
	// both; IDs are announced in the order they're stored (postPage), so the
	// hub's copy of any ID up to covered is skipped (ADR 0010 §3). A catch-up
	// event says how many messages the catch-up sends, before them; the
	// ready event tells the client covered, its next after_id.
	var covered int64
	count := 0
	if after == nil {
		if covered, err = s.Store.LastMessageID(ctx); err != nil {
			s.Log.Error("stream: last message", "error", store.SafeError(err))
			return
		}
	} else {
		// First the IDs that match, so the catch-up event can say how many
		// follow before they do (R63b), then the messages themselves.
		var matching []int64
		for covered = *after; ; {
			ids, err := s.Store.MessageIDsAfter(ctx, db.MessageIDsAfterParams{ID: covered, MaxRows: catchUpBatch})
			if err != nil {
				s.Log.Error("stream: catch-up", "error", store.SafeError(err))
				return
			}
			if len(ids) == 0 {
				break
			}
			rows, err := s.search(ctx, f, nil, len(ids), ids)
			if err != nil {
				s.Log.Error("stream: catch-up", "error", err.Error())
				return
			}
			for _, m := range rows {
				matching = append(matching, m.ID)
			}
			covered = ids[len(ids)-1]
			if len(ids) < catchUpBatch {
				break
			}
		}
		slices.Sort(matching)
		fmt.Fprintf(w, "event: catch-up\ndata: {\"count\":%d}\n\n", len(matching))
		for batch := range slices.Chunk(matching, catchUpBatch) {
			rows, err := s.search(ctx, f, nil, len(batch), batch)
			if err == nil {
				slices.SortFunc(rows, func(a, b db.SearchMessagesRow) int { return cmp.Compare(a.ID, b.ID) })
				err = send(rows)
			}
			if err != nil {
				s.Log.Error("stream: catch-up", "error", err.Error())
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			count += len(rows)
		}
	}
	fmt.Fprintf(w, "event: ready\ndata: {\"caught_up\":%d,\"last_id\":%d}\n\n", count, covered)
	if err := rc.Flush(); err != nil {
		return
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Dropped:
			reconnect(sub.Reason())
			return
		case <-expired:
			reconnect("expired")
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
		case id := <-sub.C:
			if id <= covered {
				continue // before the stream started, or sent by the catch-up
			}
			rows, err := s.search(ctx, f, nil, 1, []int64{id})
			if err == nil && len(rows) > 0 {
				err = send(rows)
			}
			if err != nil {
				s.Log.Error("stream", "error", err.Error())
				return
			}
			if len(rows) == 0 {
				continue // doesn't match this viewer's filter (A4)
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// AliasView is an alias as the alias view shows it (R35e). History is for
// admins only, and left out for everyone else (owner, 2026-10-16).
// MyRequest is the signed-in user's waiting request for it, if any (R56).
type AliasView struct {
	ID         int64                  `json:"id"`
	Pattern    string                 `json:"pattern"` // capcode, or pattern with % and _
	Name       string                 `json:"name"`
	TemplateID *int64                 `json:"template_id"`
	Template   *string                `json:"template"`
	Agency     *string                `json:"agency"`
	Icon       *string                `json:"icon"` // URL, or null (R66)
	Colour     *string                `json:"colour"`
	Ignored    bool                   `json:"ignored"`
	History    []catalog.HistoryEntry `json:"history,omitempty"`
	MyRequest  *catalog.RequestView   `json:"my_request,omitempty"`
}

// aliasView reads an alias, with its history for admins.
func (s *Server) aliasView(r *http.Request, id int64, admin bool) (AliasView, bool, error) {
	a, err := s.Store.AliasView(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return AliasView{}, false, nil
	}
	if err != nil {
		return AliasView{}, false, fmt.Errorf("alias: %s", store.SafeError(err))
	}
	v := AliasView{ID: a.ID, Pattern: a.Pattern, Name: a.Name, TemplateID: a.TemplateID, Template: a.TemplateName,
		Agency: a.Agency, Icon: iconURL(a.IconSha256), Colour: a.Colour, Ignored: a.Ignored}
	if admin {
		if v.History, err = s.Catalog.History(r.Context(), &a.ID, a.Pattern); err != nil {
			return v, true, err
		}
	}
	return v, true, nil
}

func (s *Server) getAlias(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "no such alias")
		return
	}
	v, found, err := s.aliasView(r, id, s.isAdmin(r))
	if err == nil && found {
		uid := currentUser(r).ID
		var mine []catalog.RequestView
		mine, err = s.Catalog.Requests(r.Context(), catalog.RequestFilter{RequestedBy: &uid, AliasID: &id, Which: "waiting", Max: 1})
		if len(mine) > 0 {
			v.MyRequest = &mine[0]
		}
	}
	if err != nil {
		s.Log.Error("alias", "error", err.Error())
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !found {
		problem(w, http.StatusNotFound, "no such alias")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// CapcodeView is a capcode as the alias view shows it when the capcode has
// no alias (R35e). AliasID is set instead when an alias now covers it.
// History holds deleted aliases' entries for this capcode, for admins only.
type CapcodeView struct {
	Capcode   string                 `json:"capcode"`
	AliasID   *int64                 `json:"alias_id"`
	Messages  int64                  `json:"messages"`
	FirstSeen *time.Time             `json:"first_seen"`
	LastSeen  *time.Time             `json:"last_seen"`
	History   []catalog.HistoryEntry `json:"history,omitempty"`
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
	if s.isAdmin(r) {
		if out.History, err = s.Catalog.History(r.Context(), nil, capcode); err != nil {
			s.Log.Error("capcode history", "error", err.Error())
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
	}
	if len(rows) > 0 {
		first, last := rows[0].FirstSeen.UTC(), rows[0].LastSeen.UTC()
		out.Messages, out.FirstSeen, out.LastSeen = rows[0].Messages, &first, &last
	} else if out.AliasID == nil && len(out.History) == 0 {
		problem(w, http.StatusNotFound, "no messages to this capcode")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
