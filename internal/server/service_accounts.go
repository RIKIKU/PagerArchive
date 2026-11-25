package server

import (
	"net/http"
	"slices"
	"strings"

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

// serviceAccess is what a service account may do with a read.
type serviceAccess int

const (
	// serviceAllowed: it may read the route. What it gets names no person
	// but itself, or the people in it are hidden (hidePeople).
	serviceAllowed serviceAccess = iota + 1
	// servicePersonal: the route holds people's personal details, so it's
	// refused.
	servicePersonal
)

// serviceReads classifies every signed-in read for service accounts, so a
// new one can't open to them by default: a read missing here is refused as
// personal, and TestServiceAccountReadsClassified fails until it's added
// (R62a, ADR 0048). Personal: the user list and each user's page (emails,
// invite trees, deactivation records), invitations (addresses), and alias
// requests (who asked, their notes and the feedback they got).
var serviceReads = map[string]serviceAccess{
	// People's personal details.
	"GET /api/v1/admin/users":                           servicePersonal,
	"GET /api/v1/admin/users/{id}":                      servicePersonal,
	"GET /api/v1/admin/users/{id}/invitations":          servicePersonal,
	"GET /api/v1/admin/users/{id}/deactivation-preview": servicePersonal,
	"GET /api/v1/admin/invitations":                     servicePersonal,
	"GET /api/v1/invitations":                           servicePersonal,
	"GET /api/v1/admin/alias-requests":                  servicePersonal,
	"GET /api/v1/alias-requests":                        servicePersonal,

	// Its own account: who invited it (its owner) is hidden.
	"GET /api/v1/me": serviceAllowed,
	// R77c: its own new-large-event dot, about events, which it may read.
	"GET /api/v1/me/new-large-event":        serviceAllowed,
	"GET /api/v1/me/new-large-event/stream": serviceAllowed,
	// Pages and what's derived from them: no people (flags are booleans;
	// receive times name receiving clients, not people).
	"GET /api/v1/meta":                    serviceAllowed,
	"GET /api/v1/messages":                serviceAllowed,
	"GET /api/v1/messages/{id}":           serviceAllowed,
	"GET /api/v1/messages/by-id":          serviceAllowed,
	"GET /api/v1/messages/counts":         serviceAllowed,
	"GET /api/v1/stream":                  serviceAllowed,
	"GET /api/v1/jobs/{job}":              serviceAllowed,
	"GET /api/v1/jobs/{job}/areas":        serviceAllowed,
	"GET /api/v1/large-events":            serviceAllowed,
	"GET /api/v1/large-events/{id}":       serviceAllowed,
	"GET /api/v1/large-events/{id}/areas": serviceAllowed,
	"GET /api/v1/search/values":           serviceAllowed,
	"GET /api/v1/map":                     serviceAllowed,
	"GET /api/v1/map/tiles.pmtiles":       serviceAllowed,
	"GET /api/v1/map/areas/{layer}":       serviceAllowed,
	"GET /api/v1/icons/{file}":            serviceAllowed,
	"GET /api/v1/templates":               serviceAllowed,
	"GET /api/v1/capcodes/unaliased":      serviceAllowed,
	// A capcode's and an alias's views: the alias history's people are
	// hidden.
	"GET /api/v1/capcodes/{capcode}": serviceAllowed,
	"GET /api/v1/aliases/{id}":       serviceAllowed,
	// Its own albums and integrations, of which it has none: each person's
	// are theirs alone.
	"GET /api/v1/albums":                       serviceAllowed,
	"GET /api/v1/albums/{id}":                  serviceAllowed,
	"GET /api/v1/messages/{id}/albums":         serviceAllowed,
	"GET /api/v1/integrations":                 serviceAllowed,
	"GET /api/v1/integrations/{id}":            serviceAllowed,
	"GET /api/v1/integrations/{id}/deliveries": serviceAllowed,
	"GET /api/v1/integrations/selection":       serviceAllowed,
	"GET /api/v1/integrations/levels":          serviceAllowed,
	// Reports: counts of pages, jobs, appliances and places, no people.
	"GET /api/v1/reports/capcode":      serviceAllowed,
	"GET /api/v1/reports/options":      serviceAllowed,
	"GET /api/v1/reports/busiest":      serviceAllowed,
	"GET /api/v1/reports/localities":   serviceAllowed,
	"GET /api/v1/reports/busy-times":   serviceAllowed,
	"GET /api/v1/reports/event-types":  serviceAllowed,
	"GET /api/v1/reports/job-size":     serviceAllowed,
	"GET /api/v1/reports/long-jobs":    serviceAllowed,
	"GET /api/v1/reports/appliances":   serviceAllowed,
	"GET /api/v1/reports/large-events": serviceAllowed,
	"GET /api/v1/reports/jobs":         serviceAllowed,
	// Admins' settings and jobs: none records who started or changed it
	// (that's in the log, by user ID); receiving clients are machines.
	"GET /api/v1/admin/large-events":      serviceAllowed,
	"GET /api/v1/admin/switcher":          serviceAllowed,
	"GET /api/v1/admin/retention":         serviceAllowed,
	"GET /api/v1/admin/areas":             serviceAllowed,
	"GET /api/v1/admin/backup":            serviceAllowed,
	"GET /api/v1/admin/auto-upgrade":      serviceAllowed,
	"GET /api/v1/admin/reparse":           serviceAllowed,
	"GET /api/v1/admin/redaction":         serviceAllowed,
	"GET /api/v1/admin/clients":           serviceAllowed,
	"GET /api/v1/admin/integration-hosts": serviceAllowed,
	"GET /api/v1/admin/templates":         serviceAllowed,
	"GET /api/v1/admin/agency-profiles":   serviceAllowed,
	// Aliases: an alias's page hides its history's people and its waiting
	// requests' askers, notes and feedback; the list, export and shared
	// names name aliases and templates only.
	"GET /api/v1/admin/aliases":              serviceAllowed,
	"GET /api/v1/admin/aliases/{id}":         serviceAllowed,
	"GET /api/v1/admin/aliases/export":       serviceAllowed,
	"GET /api/v1/admin/aliases/shared-names": serviceAllowed,
}

// UnclassifiedServiceReads lists the signed-in reads serviceReads doesn't
// classify, which service accounts are refused until it does (R62a).
func (s *Server) UnclassifiedServiceReads() []string {
	var out []string
	for pattern := range s.apiRoutes() {
		method, _, _ := strings.Cut(pattern, " ")
		if a := serviceReads[pattern]; readMethod(method) && a != serviceAllowed && a != servicePersonal {
			out = append(out, pattern)
		}
	}
	slices.Sort(out)
	return out
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
		// A read not classified is refused: fail closed.
		if serviceReads[pattern] != serviceAllowed {
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

// hideRequestPeople takes the people out of alias requests for a service
// account: who asked and reviewed, and their words (the note and feedback).
func hideRequestPeople(r *http.Request, requests []catalog.RequestView) {
	if !accounts.IsServiceAccount(currentUser(r)) {
		return
	}
	for i := range requests {
		requests[i].RequestedBy, requests[i].ReviewedBy = hiddenPerson, ""
		for _, words := range []**string{&requests[i].Note, &requests[i].Feedback} {
			if *words != nil {
				h := hiddenPerson
				*words = &h
			}
		}
	}
}
