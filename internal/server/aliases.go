package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Aliases in the browser (R53-R58, ADR 0007). Handlers log ids and
// outcomes only: never alias names, notes or feedback.

// catalogError answers a refused change with its message and anything
// else as a server error.
func (s *Server) catalogError(w http.ResponseWriter, op string, err error) {
	var refusal *catalog.Refusal
	if errors.As(err, &refusal) {
		writeJSON(w, refusal.Status, Problem{Error: refusal.Msg, Code: refusal.Code})
		return
	}
	var dup *catalog.DuplicateAliasError
	if errors.As(err, &dup) {
		r := dup.ForAdmin()
		writeJSON(w, r.Status, Problem{Error: r.Msg, Code: r.Code})
		return
	}
	// Catalog errors carry SQLSTATEs and capcodes, never names (store.SafeError).
	s.Log.Error(op, "error", err.Error())
	problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
}

func (s *Server) by(r *http.Request) catalog.By {
	id := currentUser(r).ID
	return catalog.By{Source: catalog.SourceAdmin, UserID: &id}
}

// TemplateChoice is a template users can choose in a request (R54).
type TemplateChoice struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Agency string  `json:"agency"`
	Icon   *string `json:"icon"`
	Colour *string `json:"colour"`
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListTemplates(r.Context())
	if err != nil {
		s.Log.Error("templates", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := make([]TemplateChoice, len(rows))
	for i, t := range rows {
		out[i] = TemplateChoice{ID: t.ID, Name: t.Name, Agency: t.Agency, Icon: iconURL(t.IconSha256), Colour: t.Colour}
	}
	writeJSON(w, http.StatusOK, out)
}

// My requests (R54, R56).

func (s *Server) myRequests(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r).ID
	out, err := s.Catalog.Requests(r.Context(), catalog.RequestFilter{RequestedBy: &uid, Which: "all", Max: 200})
	if err != nil {
		s.catalogError(w, "my requests", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Created is the id of something just made.
type Created struct {
	ID int64 `json:"id"`
}

func (s *Server) submitRequest(w http.ResponseWriter, r *http.Request) {
	var in catalog.RequestInput
	if !decodeBody(w, r, &in) {
		return
	}
	u := currentUser(r)
	id, err := s.Catalog.SubmitRequest(r.Context(), u.ID, in)
	if err != nil {
		s.catalogError(w, "submit alias request", err)
		return
	}
	s.Log.Info("alias request submitted", "request", id, "user", u.ID, "kind", in.Kind)
	writeJSON(w, http.StatusCreated, Created{ID: id})
}

func (s *Server) editRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in catalog.RequestInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Catalog.EditRequest(r.Context(), currentUser(r).ID, id, in); err != nil {
		s.catalogError(w, "edit alias request", err)
		return
	}
	s.Log.Info("alias request edited", "request", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) withdrawRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Catalog.WithdrawRequest(r.Context(), currentUser(r).ID, id); err != nil {
		s.catalogError(w, "withdraw alias request", err)
		return
	}
	s.Log.Info("alias request withdrawn", "request", id)
	w.WriteHeader(http.StatusNoContent)
}

// Review (R55).

func (s *Server) adminRequests(w http.ResponseWriter, r *http.Request) {
	which := r.URL.Query().Get("status")
	f := catalog.RequestFilter{Which: "waiting", Max: 500, Admin: true}
	switch which {
	case "", "waiting":
	case "closed":
		f.Which, f.Max = "closed", 100
	default:
		problem(w, http.StatusBadRequest, `status is "waiting" or "closed"`)
		return
	}
	out, err := s.Catalog.Requests(r.Context(), f)
	if err != nil {
		s.catalogError(w, "alias requests", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) approveRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	admin := currentUser(r).ID
	if err := s.Catalog.ApproveRequest(r.Context(), admin, id); err != nil {
		s.catalogError(w, "approve alias request", err)
		return
	}
	s.Log.Info("alias request approved", "request", id, "by", admin)
	w.WriteHeader(http.StatusNoContent)
}

// Feedback is an admin's reason for rejecting a request (R55).
type Feedback struct {
	Feedback string `json:"feedback"`
}

func (s *Server) rejectRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in Feedback
	if !decodeBody(w, r, &in) {
		return
	}
	admin := currentUser(r).ID
	if err := s.Catalog.RejectRequest(r.Context(), admin, id, in.Feedback); err != nil {
		s.catalogError(w, "reject alias request", err)
		return
	}
	s.Log.Info("alias request rejected", "request", id, "by", admin)
	w.WriteHeader(http.StatusNoContent)
}

// Admins manage aliases (R53).

// AdminAliasRow is an alias in Administration's list.
type AdminAliasRow struct {
	ID         int64   `json:"id"`
	Pattern    string  `json:"pattern"`
	Name       string  `json:"name"`
	TemplateID *int64  `json:"template_id"`
	Template   *string `json:"template"`
	Colour     *string `json:"colour"` // the template's
	Ignored    bool    `json:"ignored"`
	Waiting    int64   `json:"waiting_requests"`
}

// AdminAliasList is a page of aliases; More says there are more than shown,
// and the next page starts after the last one's pattern (?after=).
type AdminAliasList struct {
	Aliases []AdminAliasRow `json:"aliases"`
	More    bool            `json:"more"`
}

const adminAliasLimit = 500

func (s *Server) adminAliases(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := adminAliasLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > adminAliasLimit {
			problem(w, http.StatusBadRequest, fmt.Sprintf("limit must be 1 to %d", adminAliasLimit))
			return
		}
		limit = n
	}
	rows, err := s.Store.AdminAliases(r.Context(), db.AdminAliasesParams{Q: q, After: r.URL.Query().Get("after"), MaxRows: int32(limit + 1)})
	if err != nil {
		s.Log.Error("admin aliases", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := AdminAliasList{Aliases: []AdminAliasRow{}}
	for i, a := range rows {
		if i == limit {
			out.More = true
			break
		}
		out.Aliases = append(out.Aliases, AdminAliasRow{ID: a.ID, Pattern: a.Pattern, Name: a.Name, TemplateID: a.TemplateID, Template: a.TemplateName, Colour: a.Colour, Ignored: a.Ignored, Waiting: a.Waiting})
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminAlias is one alias on its Administration page: its settings, its
// history, its waiting requests and how many integrations select it.
type AdminAlias struct {
	AliasView
	Requests     []catalog.RequestView `json:"requests"`
	Integrations int64                 `json:"integrations"`
}

func (s *Server) adminAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, found, err := s.aliasView(r, id, true)
	if err != nil {
		s.catalogError(w, "admin alias", err)
		return
	}
	if !found {
		problem(w, http.StatusNotFound, "no such alias")
		return
	}
	out := AdminAlias{AliasView: v}
	if out.Requests, err = s.Catalog.Requests(r.Context(), catalog.RequestFilter{AliasID: &id, Which: "waiting", Max: 100, Admin: true}); err != nil {
		s.catalogError(w, "admin alias", err)
		return
	}
	if out.Integrations, err = s.Store.CountIntegrationsSelectingAlias(r.Context(), id); err != nil {
		s.catalogError(w, "admin alias", fmt.Errorf("integrations: %s", store.SafeError(err)))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAlias(w http.ResponseWriter, r *http.Request) {
	var in catalog.AliasInput
	if !decodeBody(w, r, &in) {
		return
	}
	id, err := s.Catalog.CreateAlias(r.Context(), in, s.by(r))
	if err != nil {
		s.catalogError(w, "add alias", err)
		return
	}
	s.Log.Info("alias added", "alias", id, "by", currentUser(r).ID)
	writeJSON(w, http.StatusCreated, Created{ID: id})
}

func (s *Server) editAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in catalog.AliasInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Catalog.EditAlias(r.Context(), id, in, s.by(r)); err != nil {
		s.catalogError(w, "edit alias", err)
		return
	}
	s.Log.Info("alias changed", "alias", id, "by", currentUser(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Catalog.RemoveAlias(r.Context(), id, s.by(r))
	if err != nil {
		s.catalogError(w, "delete alias", err)
		return
	}
	s.Log.Info("alias deleted", "alias", id, "by", currentUser(r).ID, "requests_closed", d.Requests, "integrations", d.Integrations)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) exportAliases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pagerarchive-aliases-%s.csv"`, s.Now().In(s.Location).Format("2006-01-02")))
	w.Header().Set("Cache-Control", "no-store")
	n, err := s.Catalog.ExportCSV(r.Context(), w)
	if err != nil {
		// Usually the query, before anything was written.
		s.Log.Error("export aliases", "error", err.Error())
		problem(w, http.StatusInternalServerError, "export failed")
		return
	}
	s.Log.Info("aliases exported", "by", currentUser(r).ID, "aliases", n)
}
