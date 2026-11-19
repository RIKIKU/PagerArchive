package server

import (
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// Service accounts (R62a, ADR 0048): read-only, non-person accounts for
// tools. One reads what an admin may read, except people's personal
// details, and changes nothing.

// Codes for refusing a service account.
const (
	CodeServiceReadOnly = "service_account_read_only"
	CodeServicePersonal = "service_account_personal_details"
	CodeServiceExpired  = "service_account_expired"
	msgServiceReadOnly  = "This is a read-only service account: it can't change anything in PagerArchive. Sign in as an admin to make changes."
	msgServicePersonal  = "Service accounts can't read people's personal details (the user list, emails, invite trees and deactivation records)."
	msgServiceExpired   = "This service account's credential has expired. Ask the server's operator to run: pagerarchive service-account rotate <name>."
)

// servicePersonal are the reads that hold people's personal details: the
// user list and each user's page (emails, invite trees, deactivation
// records), invitations (addresses), and alias requests (who asked, and
// their notes). A service account is refused them.
var servicePersonal = map[string]bool{
	"GET /api/v1/admin/users":                           true,
	"GET /api/v1/admin/users/{id}":                      true,
	"GET /api/v1/admin/users/{id}/invitations":          true,
	"GET /api/v1/admin/users/{id}/deactivation-preview": true,
	"GET /api/v1/admin/invitations":                     true,
	"GET /api/v1/invitations":                           true,
	"GET /api/v1/admin/alias-requests":                  true,
	"GET /api/v1/alias-requests":                        true,
}

// readMethod reports whether a request only reads.
func readMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// serviceGate lets a service account through only to reads that aren't
// personal details, and logs and traces each of its requests with its name
// (and only that, R70). Anyone else passes untouched.
func (s *Server) serviceGate(pattern string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !accounts.IsServiceAccount(u) {
			h(w, r)
			return
		}
		name := deref(u.Name)
		telemetry.SetServiceAccount(r.Context(), name)
		if !readMethod(r.Method) {
			s.Log.InfoContext(r.Context(), "service account refused", "reason", CodeServiceReadOnly, "user", u.ID, "service_account", name, "route", pattern)
			writeJSON(w, http.StatusForbidden, Problem{Error: msgServiceReadOnly, Code: CodeServiceReadOnly})
			return
		}
		if servicePersonal[pattern] {
			s.Log.InfoContext(r.Context(), "service account refused", "reason", CodeServicePersonal, "user", u.ID, "service_account", name, "route", pattern)
			writeJSON(w, http.StatusForbidden, Problem{Error: msgServicePersonal, Code: CodeServicePersonal})
			return
		}
		s.Log.InfoContext(r.Context(), "service account request", "user", u.ID, "service_account", name, "route", pattern)
		h(w, r)
	}
}

// hiddenPerson stands in, for a service account, for who made or asked for
// a change in an alias's history: a person's name and address.
const hiddenPerson = "(hidden from service accounts)"

// hidePeople takes the people out of an alias history for a service account.
func hidePeople(r *http.Request, history []catalog.HistoryEntry) {
	if !accounts.IsServiceAccount(currentUser(r)) {
		return
	}
	for i := range history {
		history[i].MadeBy = hiddenPerson
		if history[i].RequestedBy != nil {
			h := hiddenPerson
			history[i].RequestedBy = &h
		}
	}
}
