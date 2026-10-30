package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// Templates and the one-batch import for admins (R63, R64; ADR 0009 §8): what
// the removed template and alias commands did, for the PowerShell module and
// the template screens in Administration (ADR 0011).

// AdminTemplate is a template with how many aliases use it.
type AdminTemplate struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Agency        string  `json:"agency"`
	Colour        *string `json:"colour"`
	AgencyProfile *string `json:"agency_profile"`
	Icon          *string `json:"icon"` // its URL, or null (R65)
	Aliases       int64   `json:"aliases"`
}

// profileNames are the agency profiles loaded at start-up.
func (s *Server) profileNames() []string {
	out := make([]string, 0, len(s.Ingest.Agencies))
	for name := range s.Ingest.Agencies {
		out = append(out, name)
	}
	return out
}

func (s *Server) adminTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListTemplates(r.Context())
	if err != nil {
		s.Log.Error("templates", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := make([]AdminTemplate, len(rows))
	for i, t := range rows {
		out[i] = AdminTemplate{ID: t.ID, Name: t.Name, Agency: t.Agency, Colour: t.Colour, AgencyProfile: t.AgencyProfile, Icon: iconURL(t.IconSha256), Aliases: t.AliasCount}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var in catalog.TemplateInput
	if !decodeBody(w, r, &in) {
		return
	}
	id, err := s.Catalog.CreateTemplate(r.Context(), in, s.profileNames())
	if err != nil {
		s.catalogError(w, r, "add template", err)
		return
	}
	s.Log.Info("template added", "template", id, "by", currentUser(r).ID)
	writeJSON(w, http.StatusCreated, Created{ID: id})
}

func (s *Server) editTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in catalog.TemplateChange
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Catalog.EditTemplate(r.Context(), id, in, s.profileNames()); err != nil {
		s.catalogError(w, r, "edit template", err)
		return
	}
	s.Log.Info("template changed", "template", id, "by", currentUser(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Catalog.RemoveTemplate(r.Context(), id); err != nil {
		s.catalogError(w, r, "delete template", err)
		return
	}
	s.Log.Info("template deleted", "template", id, "by", currentUser(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

// ImportRequest is the body of the one-batch import (R27a, R63).
type ImportRequest struct {
	Rows           []catalog.ImportRow `json:"rows"`
	AgencyProfiles map[string]string   `json:"agency_profiles"`
	Update         bool                `json:"update"`
	DryRun         bool                `json:"dry_run"`
	Plan           string              `json:"plan"`
}

// ImportProblem is a refused import: the reason and the full report, so the
// module can show every conflicting row (guidelines §7).
type ImportProblem struct {
	Problem
	Report catalog.ImportReport `json:"report"`
}

// maxImportBytes caps the import's body; 20,000 rows fit comfortably.
const maxImportBytes = 8 << 20

func (s *Server) importAliases(w http.ResponseWriter, r *http.Request) {
	var in ImportRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, Problem{Error: "An import is at most 8 MB.", Code: "too_large"})
			return
		}
		// Decoder errors can quote the body, so they're never echoed or logged.
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u := currentUser(r)
	rep, err := s.Catalog.Import(r.Context(), in.Rows, catalog.ImportOptions{
		AgencyProfiles: in.AgencyProfiles, Profiles: s.profileNames(),
		Update: in.Update, DryRun: in.DryRun, Plan: in.Plan, By: s.by(r),
	})
	var refusal *catalog.Refusal
	if (catalog.IsConflict(err) || catalog.IsPlanChanged(err)) && errors.As(err, &refusal) {
		s.Log.Info("alias import refused", "by", u.ID, "code", refusal.Code, "rows", len(in.Rows), "conflicts", len(rep.Conflicts))
		writeJSON(w, refusal.Status, ImportProblem{Problem: Problem{Error: refusal.Msg, Code: refusal.Code}, Report: rep})
		return
	}
	if err != nil {
		s.catalogError(w, r, "import aliases", err)
		return
	}
	counts := map[string]int{}
	for _, row := range rep.Rows {
		counts[row.Result]++
	}
	s.Log.Info("alias import", "by", u.ID, "dry_run", in.DryRun, "update", in.Update, "rows", len(in.Rows),
		"added", counts[catalog.Added], "updated", counts[catalog.Updated], "different", counts[catalog.Different],
		"squashed", counts[catalog.Squashed], "invalid", counts[catalog.Invalid], "templates_created", len(rep.TemplatesCreated))
	writeJSON(w, http.StatusOK, rep)
}
