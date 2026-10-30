package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// integrationsError answers a failed integrations operation: a refusal
// with its message, anything else as a server error. Neither holds page
// text or secrets.
func (s *Server) integrationsError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var refusal *integrations.Error
	if errors.As(err, &refusal) {
		problem(w, refusal.Status, refusal.Msg)
		return
	}
	s.Log.ErrorContext(r.Context(), "request failed", "op", op, "error", err.Error())
	problem(w, http.StatusInternalServerError, "something went wrong")
}

func decodeIntegration(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// IntegrationsPage is the owner's integrations and whether they can be used.
type IntegrationsPage struct {
	Enabled      bool                `json:"enabled"` // the server has INTEGRATION_SECRETS_KEY
	Integrations []integrations.View `json:"integrations"`
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	out := IntegrationsPage{Enabled: s.Integrations.Enabled(), Integrations: []integrations.View{}}
	if s.Integrations != nil {
		views, err := s.Integrations.List(r.Context(), currentUser(r))
		if err != nil {
			s.integrationsError(w, r, "integrations", err)
			return
		}
		out.Integrations = views
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) integrationsOff(w http.ResponseWriter) bool {
	if s.Integrations == nil {
		problem(w, http.StatusServiceUnavailable, integrations.ErrNoKey.Error()+".")
		return true
	}
	return false
}

func (s *Server) createIntegration(w http.ResponseWriter, r *http.Request) {
	var req integrations.Request
	if s.integrationsOff(w) || !decodeIntegration(w, r, &req) {
		return
	}
	v, err := s.Integrations.Create(r.Context(), currentUser(r), req)
	if err != nil {
		s.integrationsError(w, r, "create integration", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.integrationsOff(w) {
		return
	}
	v, err := s.Integrations.Get(r.Context(), currentUser(r), id)
	if err != nil {
		s.integrationsError(w, r, "integration", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	var req integrations.Request
	if !ok || s.integrationsOff(w) || !decodeIntegration(w, r, &req) {
		return
	}
	v, err := s.Integrations.Update(r.Context(), currentUser(r), id, req)
	if err != nil {
		s.integrationsError(w, r, "update integration", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.integrationsOff(w) {
		return
	}
	if err := s.Integrations.Delete(r.Context(), currentUser(r), id); err != nil {
		s.integrationsError(w, r, "delete integration", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.integrationsOff(w) {
		return
	}
	res, err := s.Integrations.Test(r.Context(), currentUser(r), id)
	if err != nil {
		s.integrationsError(w, r, "test integration", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) pauseIntegration(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok || s.integrationsOff(w) {
			return
		}
		if err := s.Integrations.Pause(r.Context(), currentUser(r), id, admin); err != nil {
			s.integrationsError(w, r, "pause integration", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) resumeIntegration(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok || s.integrationsOff(w) {
			return
		}
		if err := s.Integrations.Resume(r.Context(), currentUser(r), id, admin); err != nil {
			s.integrationsError(w, r, "resume integration", err)
			return
		}
		s.Integrations.Wake()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) integrationDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.integrationsOff(w) {
		return
	}
	out, err := s.Integrations.Deliveries(r.Context(), currentUser(r), id)
	if err != nil {
		s.integrationsError(w, r, "deliveries", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// suggestSelection is what the selection field offers for ?q= (R48).
func (s *Server) suggestSelection(w http.ResponseWriter, r *http.Request) {
	if s.integrationsOff(w) {
		return
	}
	out, err := s.Integrations.Suggest(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.integrationsError(w, r, "selection", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminUser is one user's page in Administration (R44b): the user, and
// their integrations without destinations or secrets.
type AdminUser struct {
	User         UserRow                  `json:"user"`
	Integrations []integrations.AdminView `json:"integrations"`
	// R42a, R42d: their invite tree, and the tree deactivations naming them.
	Tree          InviteTree           `json:"tree"`
	Deactivations []DeactivationRecord `json:"deactivations"`
}

func (s *Server) adminUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	users, err := s.userRows(r.Context())
	if err != nil {
		s.Log.Error("users", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	i := slices.IndexFunc(users, func(u UserRow) bool { return u.ID == id })
	if i < 0 {
		problem(w, http.StatusNotFound, "no such user")
		return
	}
	out := AdminUser{User: users[i], Integrations: []integrations.AdminView{}}
	if out.Tree, out.Deactivations, err = s.treeAndRecords(r.Context(), id); err != nil {
		s.accountsError(w, r, "invite tree", err)
		return
	}
	if s.Integrations != nil {
		if out.Integrations, err = s.Integrations.AdminList(r.Context(), id); err != nil {
			s.integrationsError(w, r, "admin integrations", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// AllowListBody is the admins' list of private hosts and ranges
// integrations may reach (R51).
type AllowListBody struct {
	Entries []string `json:"entries"`
}

func (s *Server) getAllowList(w http.ResponseWriter, r *http.Request) {
	if s.integrationsOff(w) {
		return
	}
	a, err := s.Integrations.AllowList(r.Context())
	if err != nil {
		s.integrationsError(w, r, "allow list", err)
		return
	}
	writeJSON(w, http.StatusOK, AllowListBody{Entries: a.Entries()})
}

func (s *Server) putAllowList(w http.ResponseWriter, r *http.Request) {
	var body AllowListBody
	if s.integrationsOff(w) || !decodeIntegration(w, r, &body) {
		return
	}
	a, err := s.Integrations.SetAllowList(r.Context(), currentUser(r), body.Entries)
	if err != nil {
		s.integrationsError(w, r, "allow list", err)
		return
	}
	writeJSON(w, http.StatusOK, AllowListBody{Entries: a.Entries()})
}
