package authentik

import (
	"context"
	"encoding/json"
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
			_, _ = w.Write([]byte(`{"results":[{"pk":42}]}`))
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

	sub, found, err := c.UserByUsername(ctx, "b+x@example.com")
	if err != nil || !found || sub != "42" {
		t.Fatalf("UserByUsername: %q %v %v", sub, found, err)
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
	if bodies[2]["flow"] != "flow-1" || bodies[2]["single_use"] != true || fixed["email"] != "B+x@example.com" || fixed["username"] != "b@example.com" ||
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
