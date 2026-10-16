package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// integrationsError answers a failed integrations operation: a refusal
// with its message, anything else as a server error. Neither holds page
// text or secrets.
func (s *Server) integrationsError(w http.ResponseWriter, op string, err error) {
	var refusal *integrations.Error
	if errors.As(err, &refusal) {
		problem(w, refusal.Status, refusal.Msg)
		return
	}
	s.Log.Error(op, "error", err.Error())
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
			s.integrationsError(w, "integrations", err)
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
		s.integrationsError(w, "create integration", err)
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
		s.integrationsError(w, "integration", err)
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
		s.integrationsError(w, "update integration", err)
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
		s.integrationsError(w, "delete integration", err)
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
		s.integrationsError(w, "test integration", err)
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
			s.integrationsError(w, "pause integration", err)
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
			s.integrationsError(w, "resume integration", err)
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
		s.integrationsError(w, "deliveries", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AliasBrief is an alias for the integration form's picker.
type AliasBrief struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Name    string `json:"name"`
}

func (s *Server) listAliases(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListAliasesBrief(r.Context())
	if err != nil {
		s.Log.Error("aliases", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []AliasBrief{}
	for _, a := range rows {
		out = append(out, AliasBrief{ID: a.ID, Pattern: a.Pattern, Name: a.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) adminIntegrations(w http.ResponseWriter, r *http.Request) {
	if s.integrationsOff(w) {
		return
	}
	out, err := s.Integrations.AdminList(r.Context())
	if err != nil {
		s.integrationsError(w, "admin integrations", err)
		return
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
		s.integrationsError(w, "allow list", err)
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
		s.integrationsError(w, "allow list", err)
		return
	}
	writeJSON(w, http.StatusOK, AllowListBody{Entries: a.Entries()})
}
