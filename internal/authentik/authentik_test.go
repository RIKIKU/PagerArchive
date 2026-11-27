package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
)

// The client reaches Authentik on its internal URL while Authentik sees the
// public host and scheme, and query strings survive.
func TestClientThroughInternalURL(t *testing.T) {
	var seen []*http.Request
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/core/users/":
			_, _ = w.Write([]byte(`{"results":[{"pk":42,"is_active":true}]}`))
		case "/api/v3/flows/instances/":
			_, _ = w.Write([]byte(`{"results":[{"pk":"flow-1"}]}`))
		case "/api/v3/stages/invitation/invitations/":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"pk":"inv-1"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	public, _ := url.Parse("https://auth.example.org")
	internal, _ := url.Parse(srv.URL)
	c := &Client{URL: public, Token: "tok", HTTP: &http.Client{Transport: Transport(public, internal, nil)}}
	ctx := context.Background()

	u, found, err := c.UserByUsername(ctx, "b+x@example.com")
	if err != nil || !found || u.Subject != "42" || !u.Active {
		t.Fatalf("UserByUsername: %+v %v %v", u, found, err)
	}
	r := seen[0]
	if r.URL.Query().Get("username") != "b+x@example.com" || r.Host != "auth.example.org" ||
		r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("request: %s host %s proto %s", r.URL, r.Host, r.Header.Get("X-Forwarded-Proto"))
	}

	id, err := c.CreateInvitation(ctx, accounts.DirectoryInvitation{AppID: 7, Address: "B+x@example.com", Key: "b@example.com", Expires: time.Now()})
	if err != nil || id != "inv-1" {
		t.Fatalf("CreateInvitation: %q %v", id, err)
	}
	if seen[1].URL.Query().Get("slug") != EnrolmentFlow {
		t.Errorf("flow lookup: %s", seen[1].URL)
	}
	fixed := bodies[2]["fixed_data"].(map[string]any)
	if bodies[2]["flow"] != "flow-1" || bodies[2]["single_use"] != false || fixed["email"] != "B+x@example.com" || fixed["username"] != "b@example.com" ||
		fixed["attributes"].(map[string]any)["pagerarchive_invitation"] != "7" {
		t.Errorf("invitation body: %v", bodies[2])
	}
	if err := c.DeleteInvitation(ctx, "inv-1"); err != nil {
		t.Error(err)
	}
	if got := c.EnrolmentURL("inv-1"); got != "https://auth.example.org/if/flow/pagerarchive-enrolment/?itoken=inv-1" {
		t.Errorf("EnrolmentURL: %s", got)
	}
}

// R43a, ADR 0021: an invitation made while invitations were single-use is
// made reusable; a missing one is reported, not an error.
func TestRepairInvitation(t *testing.T) {
	var patched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["single_use"] != false {
				t.Errorf("patch body: %v", b)
			}
			patched = append(patched, r.URL.Path)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/old/"):
			_, _ = w.Write([]byte(`{"single_use":true}`))
		case strings.HasSuffix(r.URL.Path, "/new/"):
			_, _ = w.Write([]byte(`{"single_use":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{URL: u, Token: "tok", HTTP: srv.Client()}
	for id, want := range map[string]bool{"old": true, "new": true, "gone": false} {
		if found, err := c.RepairInvitation(context.Background(), id); err != nil || found != want {
			t.Errorf("%s: found %v, error %v", id, found, err)
		}
	}
	if len(patched) != 1 || patched[0] != "/api/v3/stages/invitation/invitations/old/" {
		t.Errorf("patched: %v", patched)
	}
}

// ADR 0021: only the user an unfinished enrolment with this invitation made
// is deleted; one that signed in, is active, or came from another invitation
// is left alone.
func TestDeleteUnfinishedUser(t *testing.T) {
	var deleted []string
	users := map[string]string{
		"half@example.com":      `{"pk":1,"is_active":false,"last_login":null,"path":"users/pagerarchive","attributes":{"pagerarchive_invitation":"7"}}`,
		"joined@example.com":    `{"pk":2,"is_active":true,"last_login":null,"path":"users/pagerarchive","attributes":{"pagerarchive_invitation":"7"}}`,
		"off@example.com":       `{"pk":3,"is_active":false,"last_login":"2026-11-01T00:00:00Z","path":"users/pagerarchive","attributes":{"pagerarchive_invitation":"7"}}`,
		"other@example.com":     `{"pk":4,"is_active":false,"last_login":null,"path":"users/pagerarchive","attributes":{"pagerarchive_invitation":"8"}}`,
		"elsewhere@example.com": `{"pk":5,"is_active":false,"last_login":null,"path":"users","attributes":{"pagerarchive_invitation":"7"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		u, ok := users[r.URL.Query().Get("username")]
		if !ok {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[` + u + `]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{URL: u, Token: "tok", HTTP: srv.Client()}
	for name, want := range map[string]bool{"half@example.com": true, "joined@example.com": false, "off@example.com": false,
		"other@example.com": false, "elsewhere@example.com": false, "none@example.com": false} {
		if got, err := c.DeleteUnfinishedUser(context.Background(), name, 7); err != nil || got != want {
			t.Errorf("%s: deleted %v, error %v", name, got, err)
		}
	}
	if len(deleted) != 1 || deleted[0] != "/api/v3/core/users/1/" {
		t.Errorf("deleted: %v", deleted)
	}
}

// R44c: holders are matched as the sign-in form matches, ignoring case; partial
// matches don't count; every page is read.
func TestEmailHolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "owner@example.org" {
			t.Errorf("search: %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"pagination":{"next":2},"results":[{"email":"Owner@Example.org","is_superuser":true},{"email":"coowner@example.org"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"pagination":{"next":0},"results":[{"email":"owner@example.org","is_superuser":false}]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{URL: u, Token: "tok", HTTP: srv.Client()}
	got, err := c.EmailHolders(context.Background(), "owner@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || !got.Administrator {
		t.Errorf("holders: %+v", got)
	}
}

// R73: a failed request's error doesn't quote the URL, whose query holds the
// address.
func TestErrorsLeaveOutTheQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL)
	srv.Close() // nothing listens there now
	c := &Client{URL: u, Token: "tok", HTTP: &http.Client{}}
	_, err := c.EmailHolders(context.Background(), "owner@example.org")
	if err == nil {
		t.Fatal("no error from a closed server")
	}
	if strings.Contains(err.Error(), "example.org") || strings.Contains(err.Error(), "owner") {
		t.Errorf("the address is in the error: %v", err)
	}
}

// R77f: the switcher's group is read by name (its members' IDs are the
// subjects), a missing one is ErrNoGroup, and one person is added or
// removed by ID; a subject that isn't an Authentik ID, or a refusal, is an
// error.
func TestSwitcherGroupCalls(t *testing.T) {
	var calls []string
	missing := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.URL.RawQuery+" "+strings.TrimSpace(toJSON(b)))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/core/groups/" && missing:
			_, _ = w.Write([]byte(`{"results":[{"pk":"g-2","name":"radioarchive-preview-old","users":[1]}]}`))
		case r.URL.Path == "/api/v3/core/groups/":
			_, _ = w.Write([]byte(`{"results":[{"pk":"g-1","name":"radioarchive-preview","users":[7,12]}]}`))
		case strings.HasSuffix(r.URL.Path, "/remove_user/") && b["pk"] == float64(99):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{URL: u, Token: "tok", HTTP: srv.Client()}
	ctx := context.Background()

	id, members, err := c.GroupMembers(ctx, "radioarchive-preview")
	if err != nil || id != "g-1" || len(members) != 2 || !members["7"] || !members["12"] {
		t.Fatalf("GroupMembers: %q %v %v", id, members, err)
	}
	if want := "GET /api/v3/core/groups/ include_users=false&name=radioarchive-preview null"; calls[0] != want {
		t.Errorf("read: %s, want %s", calls[0], want)
	}
	if err := c.SetGroupMember(ctx, "g-1", "12", false); err != nil {
		t.Error(err)
	}
	if err := c.SetGroupMember(ctx, "g-1", "13", true); err != nil {
		t.Error(err)
	}
	if got := calls[1:3]; got[0] != `POST /api/v3/core/groups/g-1/remove_user/  {"pk":12}` || got[1] != `POST /api/v3/core/groups/g-1/add_user/  {"pk":13}` {
		t.Errorf("changes: %q", got)
	}
	if err := c.SetGroupMember(ctx, "g-1", "99", false); err == nil {
		t.Error("a refused removal isn't an error")
	}
	n := len(calls)
	if err := c.SetGroupMember(ctx, "g-1", "fixture-someone", true); err == nil || len(calls) != n {
		t.Errorf("a subject that isn't an Authentik ID: %v, %d calls", err, len(calls)-n)
	}
	missing = true
	if _, _, err := c.GroupMembers(ctx, "radioarchive-preview"); !errors.Is(err, accounts.ErrNoGroup) {
		t.Errorf("a missing group: %v, want ErrNoGroup", err)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
