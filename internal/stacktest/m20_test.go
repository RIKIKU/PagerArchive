package stacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

const enrolmentSlug = "pagerarchive-enrolment"

// toCode walks an invitation link to the code page, which emails the code,
// and returns the flow's query and the code.
func (b *browser) toCode(link string) (query, code string) {
	b.t.Helper()
	since := time.Now()
	_, c := b.walk(link, func(c challenge) map[string]any {
		if c.Component == "ak-stage-prompt" {
			return map[string]any{"name": "Test " + b.email, "password": b.pw, "password_repeat": b.pw}
		}
		return nil
	})
	if c.Component != "ak-stage-authenticator-email" {
		b.t.Fatalf("%s: not at the code page: %s %s %.300s", b.email, c.Component, c.errorText(), c.raw)
	}
	return c.query, b.s.mailpit.code(b.t, b.email, since)
}

// invitationID is inviter's one invitation to address.
func (b *browser) invitationID(address string) int64 {
	b.t.Helper()
	var list []server.Invitation
	if code, msg := b.api(http.MethodGet, "/api/v1/invitations", nil, &list); code != http.StatusOK {
		b.t.Fatalf("invitations: %d %s", code, msg)
	}
	for _, i := range list {
		if i.Email == address {
			return i.ID
		}
	}
	b.t.Fatalf("no invitation to %s", address)
	return 0
}

// ADR 0021 (owner's review, 2026-11-08): the code was accepted, but the
// request after it never arrived (the invitee lost signal), leaving an
// inactive user with a confirmed code. The next run of the same link
// finishes; so does the next run of a resent link, when the resend came in
// that gap. Each asks for a new code.
func TestEnrolmentAfterLostRedirect(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	for _, resend := range []bool{false, true} {
		address := "m20-lost@example.com"
		if resend {
			address = "m20-lost-resent@example.com"
		}
		link := o.invite(address)
		first := s.browser(t, address)
		query, code := first.toCode(link)
		if _, answered := first.exchange(enrolmentSlug, query, map[string]any{"component": "ak-stage-authenticator-email", "code": code}); !answered {
			t.Fatalf("%s: the code wasn't taken", address)
		}
		// The executor redirected to itself; that request is never made.
		if exists, active := s.authentikUser(t, address); !exists || active {
			t.Fatalf("%s after the lost request: exists %v, active %v", address, exists, active)
		}
		if resend {
			since := time.Now()
			if code, msg := o.api(http.MethodPost, fmt.Sprintf("/api/v1/invitations/%d/resend", o.invitationID(address)), nil, nil); code != http.StatusOK {
				t.Fatalf("resend: %d %s", code, msg)
			}
			link = s.mailpit.invitationLink(t, address, since)
		}
		again := s.browser(t, address)
		again.enrol(link)
		if m := again.me(); m.InvitedBy == nil || !strings.Contains(*m.InvitedBy, "owner@example.org") {
			t.Errorf("%s: %+v", address, m)
		}
		t.Logf("%s: the code was taken and the next request lost; the next run of the %s link enrolled", address, map[bool]string{false: "same", true: "resent"}[resend])
	}
}

// R41, R44a: the code page sends the code only to the invitation's address.
// An answer giving another address is refused and sends nothing.
func TestCodeOnlyToTheInvitedAddress(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	address, elsewhere := "m20-addr@example.com", "m20-elsewhere@example.com"
	link := o.invite(address)
	b := s.browser(t, address)
	query, _ := b.toCode(link)
	since := time.Now()
	c := b.executor(enrolmentSlug, query, map[string]any{"component": "ak-stage-authenticator-email", "email": elsewhere})
	if c.Component != "ak-stage-access-denied" || !strings.Contains(c.errorText(), "only go to the address the invitation was sent to") {
		t.Fatalf("another address: %s %s %.300s", c.Component, c.errorText(), c.raw)
	}
	time.Sleep(15 * time.Second) // longer than a code email takes
	if n := s.mailpit.count(t, elsewhere, since); n != 0 {
		t.Errorf("%d emails to %s", n, elsewhere)
	}
	t.Logf("another address: %q; nothing sent to it", c.errorText())
}

// ADR 0021 (owner's review, 2026-11-08): cancelling an invitation removes
// the inactive user its unfinished enrolment left, so nothing is left behind.
func TestCancelRemovesUnfinishedUser(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	address := "m20-cancel@example.com"
	link := o.invite(address)
	s.browser(t, address).toCode(link)
	if exists, active := s.authentikUser(t, address); !exists || active {
		t.Fatalf("before the cancel: exists %v, active %v", exists, active)
	}
	if code, msg := o.api(http.MethodDelete, fmt.Sprintf("/api/v1/invitations/%d", o.invitationID(address)), nil, nil); code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", code, msg)
	}
	if exists, _ := s.authentikUser(t, address); exists {
		t.Error("the unfinished user is still in Authentik after the cancel")
	}
}

// count is how many emails to address arrived after since.
func (m mailpit) count(t *testing.T, address string, since time.Time) int {
	t.Helper()
	var list struct {
		Messages []mailMessage `json:"messages"`
	}
	q := url.Values{"query": {fmt.Sprintf(`to:"%s"`, address)}}
	resp, err := http.Get(m.url.JoinPath("/api/v1/search").String() + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, msg := range list.Messages {
		if msg.Created.After(since) {
			n++
		}
	}
	return n
}

// R44a (owner, 2026-11-08): an invitation's enrolment sends at most 5 code
// emails an hour; a sixth run is told when to try again and sends nothing.
// A81 checks the same in a browser, with a resend among the runs.
func TestCodeEmailsLimited(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	address := "m20-limit@example.com"
	link := o.invite(address)
	for i := 1; i <= 5; i++ {
		s.browser(t, address).toCode(link)
	}
	b := s.browser(t, address)
	since := time.Now()
	_, c := b.walk(link, b.enrolAnswers("", nil))
	if c.Component != "ak-stage-access-denied" || !strings.Contains(c.errorText(), "Try again after") {
		t.Fatalf("the sixth run: %s %s %.300s", c.Component, c.errorText(), c.raw)
	}
	time.Sleep(15 * time.Second) // longer than a code email takes
	if n := s.mailpit.count(t, address, since); n != 0 {
		t.Errorf("the sixth run sent %d emails", n)
	}
	t.Logf("the sixth run: %q", c.errorText())
}
