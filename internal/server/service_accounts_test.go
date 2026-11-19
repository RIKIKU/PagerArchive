package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

var wildcard = regexp.MustCompile(`{[^}]+}`)

func serviceAccount() db.User {
	name := "report-bot"
	return db.User{ID: 7, Kind: "service", Name: &name, Role: "user", Status: "active"}
}

// asUser is r as the signed-in user u, as signedIn leaves it.
func asUser(r *http.Request, u db.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey{}, u))
}

// R62a: every route that changes anything refuses a service account before
// its handler runs, every read of people's details does too, and every
// other read reaches its handler. A person passes untouched.
func TestServiceAccountGate(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	routes := s.apiRoutes()
	var writes, reads, personal int
	for pattern := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		reached := false
		h := s.serviceGate(pattern, func(w http.ResponseWriter, r *http.Request) { reached = true })
		w := httptest.NewRecorder()
		h(w, asUser(httptest.NewRequest(method, wildcard.ReplaceAllString(path, "1"), nil), serviceAccount()))
		var p Problem
		_ = json.NewDecoder(w.Body).Decode(&p)
		switch {
		case !readMethod(method):
			writes++
			if reached || w.Code != http.StatusForbidden || p.Code != CodeServiceReadOnly {
				t.Errorf("%s: reached %v, %d %q", pattern, reached, w.Code, p.Code)
			}
		case servicePersonal[pattern]:
			personal++
			if reached || w.Code != http.StatusForbidden || p.Code != CodeServicePersonal {
				t.Errorf("%s: reached %v, %d %q", pattern, reached, w.Code, p.Code)
			}
		default:
			reads++
			if !reached {
				t.Errorf("%s: a read didn't reach its handler (%d %q)", pattern, w.Code, p.Code)
			}
		}

		// A person's request goes straight to the handler.
		reached = false
		name := "Alex"
		email := "alex@example.test"
		h(httptest.NewRecorder(), asUser(httptest.NewRequest(method, wildcard.ReplaceAllString(path, "1"), nil), db.User{ID: 1, Kind: "person", Name: &name, Email: &email, Role: "admin", Status: "active"}))
		if !reached {
			t.Errorf("%s: a person's request didn't reach its handler", pattern)
		}
	}
	for pattern := range servicePersonal {
		if _, ok := routes[pattern]; !ok {
			t.Errorf("servicePersonal names %s, which isn't a route", pattern)
		}
	}
	if writes < 50 || reads < 40 || personal != len(servicePersonal) {
		t.Errorf("writes %d, reads %d, personal %d", writes, reads, personal)
	}
	if !strings.Contains(logs.String(), `"service_account":"report-bot"`) {
		t.Error("the service account's requests aren't logged with its name")
	}
	t.Logf("R62a: %d writes and %d personal reads refused, %d reads allowed", writes, personal, reads)
}

func TestServiceAccountIsAdminForReads(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if !s.isAdmin(asUser(r, serviceAccount())) {
		t.Error("a service account doesn't read as an admin")
	}
	name := "A"
	if s.isAdmin(asUser(r, db.User{Kind: "person", Name: &name, Role: "user"})) {
		t.Error("a user reads as an admin")
	}
}

func TestHidePeople(t *testing.T) {
	asked := "Alex (alex@example.test)"
	history := []catalog.HistoryEntry{{MadeBy: "Sam (sam@example.test)", RequestedBy: &asked}, {MadeBy: "cli"}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	email := "a@example.test"
	hidePeople(asUser(r, db.User{Kind: "person", Email: &email, Role: "admin"}), history)
	if history[0].MadeBy != "Sam (sam@example.test)" {
		t.Fatal("an admin's history was changed")
	}
	hidePeople(asUser(r, serviceAccount()), history)
	b, _ := json.Marshal(history)
	if strings.Contains(string(b), "example.test") || strings.Contains(string(b), "Sam") || strings.Contains(string(b), "Alex") {
		t.Errorf("a service account sees people: %s", b)
	}
}

func TestTokenClient(t *testing.T) {
	ps := &PowerShell{Issuer: "https://auth.example.test/application/o/pagerarchive-powershell/", ClientID: "pagerarchive-powershell"}
	sa := &PowerShell{Issuer: "https://auth.example.test/application/o/pagerarchive-service/", ClientID: "pagerarchive-service"}
	s := &Server{PowerShell: ps, ServiceAccounts: sa}
	jwt := func(iss string) string {
		b, _ := json.Marshal(map[string]string{"iss": iss})
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	if c, service := s.tokenClient(jwt(sa.Issuer)); c != sa || !service {
		t.Error("a service token went to the module's client")
	}
	for _, raw := range []string{jwt(ps.Issuer), jwt("https://elsewhere.test/"), "not a token", "a.!!!.c"} {
		if c, service := s.tokenClient(raw); c != ps || service {
			t.Errorf("%q went to the service accounts' client", raw)
		}
	}
	s.ServiceAccounts = nil
	if c, service := s.tokenClient(jwt(sa.Issuer)); c != ps || service {
		t.Error("with service accounts off, a token went to their client")
	}
}
