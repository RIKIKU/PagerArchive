package acceptance

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// invitationTo is m's one invitation to address, from their own list.
func (e *env) invitationTo(m *member, address string) server.Invitation {
	e.t.Helper()
	var list []server.Invitation
	if code, p := m.call(http.MethodGet, "/api/v1/invitations", nil, &list); code != http.StatusOK {
		e.t.Fatalf("list invitations: %d %s", code, p.Error)
	}
	var found []server.Invitation
	for _, i := range list {
		if i.Email == address {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		e.t.Fatalf("%s's invitations to %s: %+v", m.email, address, found)
	}
	return found[0]
}

func (e *env) invite(m *member, address string) string {
	e.t.Helper()
	if code, p := m.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: address}, nil); code != http.StatusCreated {
		e.t.Fatalf("%s invites %s: %d %s", m.email, address, code, p.Error)
	}
	link, ok := e.dir.invitationFor(address)
	if !ok {
		e.t.Fatalf("no invitation emailed to %s", address)
	}
	return link
}

func resendPath(id int64) string { return fmt.Sprintf("/api/v1/invitations/%d/resend", id) }

// A81's resending, at the API (R43d). The browser half, with a real
// Authentik and link scanners, is in web/tests/stack.spec.ts.
func TestA81Resending(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	b := e.inviteAndEnrol(e.admin, "b@example.com")

	// A's invitation to h expires; A resends it.
	old := e.invite(a, "h@example.com")
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE invitations SET expires_at = now() - interval '1 minute' WHERE email = 'h@example.com'`); err != nil {
		t.Fatal(err)
	}
	h := e.invitationTo(a, "h@example.com")
	if h.Status != "expired" {
		t.Fatalf("before the resend: %s", h.Status)
	}
	var resent server.Invitation
	if code, p := a.call(http.MethodPost, resendPath(h.ID), nil, &resent); code != http.StatusOK {
		t.Fatalf("A resends: %d %s", code, p.Error)
	}
	link, _ := e.dir.invitationFor("h@example.com")
	if link == old {
		t.Fatal("the same link was sent again")
	}
	if _, err := e.dir.enrol(old, ""); err == nil {
		t.Error("the old link still enrols")
	}
	h = e.invitationTo(a, "h@example.com")
	if h.ID != resent.ID || h.Status != "pending" || h.ResentAt == nil {
		t.Errorf("after the resend: %+v", h)
	}
	if left := time.Until(h.ExpiresAt); left < accounts.InvitationLifetime-time.Minute || left > accounts.InvitationLifetime {
		t.Errorf("the resent invitation works for another %s", left)
	}
	c, err := e.dir.enrol(link, "")
	if err != nil {
		t.Fatalf("H enrols with the new link: %v", err)
	}
	hm, err := e.signIn(c)
	if err != nil {
		t.Fatal(err)
	}
	if me := e.me(hm); me.InvitedBy == nil || *me.InvitedBy != "a@example.com" {
		t.Errorf("H's inviter: %v", me.InvitedBy)
	}

	// The admin resends B's waiting invitation; B stays its inviter.
	old = e.invite(b, "j@example.com")
	j := e.invitationTo(b, "j@example.com")
	if code, p := e.admin.call(http.MethodPost, resendPath(j.ID), nil, nil); code != http.StatusOK {
		t.Fatalf("the admin resends B's invitation: %d %s", code, p.Error)
	}
	if _, err := e.dir.enrol(old, ""); err == nil {
		t.Error("B's old link still enrols")
	}
	var admins []server.Invitation
	if code, _ := e.admin.call(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d/invitations", b.id), nil, &admins); code != http.StatusOK || len(admins) != 1 ||
		admins[0].ID != j.ID || admins[0].InvitedBy != "b@example.com" {
		t.Errorf("B's invitations on B's page: %+v", admins)
	}

	// Refused: a used invitation, a cancelled one, someone else's, and to an
	// address that has since become a member.
	if code, p := a.call(http.MethodPost, resendPath(h.ID), nil, nil); code != http.StatusConflict || p.Code != "not_pending" {
		t.Errorf("resending a used invitation: %d %+v", code, p)
	}
	e.invite(a, "k@example.com")
	k := e.invitationTo(a, "k@example.com")
	if code, p := b.call(http.MethodPost, resendPath(k.ID), nil, nil); code != http.StatusNotFound {
		t.Errorf("B resends A's invitation: %d %+v", code, p)
	}
	if code, _ := a.call(http.MethodDelete, fmt.Sprintf("/api/v1/invitations/%d", k.ID), nil, nil); code != http.StatusNoContent {
		t.Fatal("cancel")
	}
	if code, p := a.call(http.MethodPost, resendPath(k.ID), nil, nil); code != http.StatusConflict || p.Code != "not_pending" {
		t.Errorf("resending a cancelled invitation: %d %+v", code, p)
	}
	t.Log("A81 (API): an expired invitation resent with a new link and 7 days, the old link refused, one invitation, H enrolled with A as inviter; the admin resent B's, B still its inviter; used, cancelled and others' invitations refused")
}

// ADR 0021: an invitation whose link Authentik lost (opened by a link
// scanner while invitations were single-use) is shown as needing a resend,
// not as waiting, and resending it works. One that finished enrolment in
// Authentik, whose account is made at the first sign-in, is left alone.
func TestInvitationLinksChecked(t *testing.T) {
	e := setup(t)
	lostLink := e.invite(e.user, "lost@example.com")
	e.dir.mu.Lock()
	delete(e.dir.invites, lostLink) // what Authentik did when the scanner opened it
	e.dir.mu.Unlock()
	finished := e.invite(e.user, "finished@example.com")
	c, err := e.dir.enrol(finished, "") // enrolled, not yet signed in to the app
	if err != nil {
		t.Fatal(err)
	}
	e.invite(e.user, "fine@example.com")

	lost, err := e.acc.CheckInvitations(e.ctx)
	if err != nil || lost != 1 {
		t.Fatalf("check: %d lost, %v", lost, err)
	}
	if lost, err := e.acc.CheckInvitations(e.ctx); err != nil || lost != 0 {
		t.Errorf("checked again: %d lost, %v", lost, err)
	}
	for address, want := range map[string]string{"lost@example.com": "link_lost", "finished@example.com": "pending", "fine@example.com": "pending"} {
		if got := e.invitationTo(e.user, address).Status; got != want {
			t.Errorf("%s: %s, want %s", address, got, want)
		}
	}
	if _, err := e.signIn(c); err != nil {
		t.Errorf("the finished enrolment signs in: %v", err)
	}

	inv := e.invitationTo(e.user, "lost@example.com")
	if code, p := e.user.call(http.MethodPost, resendPath(inv.ID), nil, nil); code != http.StatusOK {
		t.Fatalf("resend: %d %s", code, p.Error)
	}
	if got := e.invitationTo(e.user, "lost@example.com").Status; got != "pending" {
		t.Errorf("after the resend: %s", got)
	}
	link, _ := e.dir.invitationFor("lost@example.com")
	if c, err := e.dir.enrol(link, ""); err != nil {
		t.Errorf("the resent link: %v", err)
	} else if _, err := e.signIn(c); err != nil {
		t.Errorf("signing in after the resent link: %v", err)
	}
	if logs := e.logs.String(); !strings.Contains(logs, "an invitation's link no longer works in Authentik") || strings.Contains(logs, "lost@example.com") {
		t.Errorf("log: the warning, without the address:\n%s", logs)
	}
}
