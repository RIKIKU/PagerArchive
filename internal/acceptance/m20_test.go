package acceptance

import (
	"errors"
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

// linkOf is the invitation's current Authentik link, from the app's record.
func (e *env) linkOf(id int64) string {
	e.t.Helper()
	var link string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT authentik_id FROM invitations WHERE id = $1`, id).Scan(&link); err != nil {
		e.t.Fatal(err)
	}
	return link
}

// R43d (owner's review, 2026-11-08): a resend changes the links first and
// emails last. If the old link can't be deleted, nothing changes and nothing
// is emailed; if the email can't be sent, the new link stays and works, and
// the error says so.
func TestResendEmailsLast(t *testing.T) {
	e := setup(t)
	old := e.invite(e.user, "r@example.com")
	inv := e.invitationTo(e.user, "r@example.com")
	links, emailed := len(e.dir.invites), len(e.dir.sent)

	e.dir.mu.Lock()
	e.dir.failDelete = old
	e.dir.mu.Unlock()
	if code, p := e.user.call(http.MethodPost, resendPath(inv.ID), nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("old link not deleted: %d %+v", code, p)
	}
	e.dir.mu.Lock()
	e.dir.failDelete = ""
	e.dir.mu.Unlock()
	if got := e.linkOf(inv.ID); got != old {
		t.Errorf("the record changed: %s, was %s", got, old)
	}
	if len(e.dir.invites) != links || len(e.dir.sent) != emailed {
		t.Errorf("the new link was left, or emailed: %d links, %d emailed, was %d and %d", len(e.dir.invites), len(e.dir.sent), links, emailed)
	}

	e.dir.mu.Lock()
	e.dir.sendErr = errors.New("smtp is down")
	e.dir.mu.Unlock()
	code, p := e.user.call(http.MethodPost, resendPath(inv.ID), nil, nil)
	e.dir.mu.Lock()
	e.dir.sendErr = nil
	e.dir.mu.Unlock()
	if code != http.StatusServiceUnavailable || p.Code != "not_emailed" || !strings.Contains(p.Error, "has a new link") {
		t.Fatalf("not emailed: %d %+v", code, p)
	}
	link := e.linkOf(inv.ID)
	if link == old {
		t.Fatal("the link didn't change")
	}
	if _, err := e.dir.enrol(old, ""); err == nil {
		t.Error("the old link still enrols")
	}
	if got := e.invitationTo(e.user, "r@example.com"); got.Status != "pending" || got.ResentAt == nil {
		t.Errorf("after the resend: %+v", got)
	}
	c, err := e.dir.enrol(link, "")
	if err != nil {
		t.Fatalf("the new link: %v", err)
	}
	if _, err := e.signIn(c); err != nil {
		t.Fatal(err)
	}
	t.Logf("old link not deleted: nothing changed or emailed; email failed: %q, the new link enrolled", p.Error)
}

// R43d, ADR 0021 (owner's review, 2026-11-08): a sign-in and a resend take
// the invitation's lock. A sign-in waits for a resend under way, then deletes
// the resend's link, the current one; a resend after joining is refused.
func TestSignInWaitsForResend(t *testing.T) {
	e := setup(t)
	old := e.invite(e.user, "s@example.com")
	inv := e.invitationTo(e.user, "s@example.com")
	actor, err := e.st.UserByID(e.ctx, e.user.id)
	if err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	e.dir.onCreate = func() { close(entered); <-release }
	resent := make(chan error, 1)
	go func() {
		_, err := e.acc.Resend(e.ctx, actor, inv.ID)
		resent <- err
	}()
	<-entered // the resend holds the lock, making its new link
	e.dir.onCreate = nil
	c, err := e.dir.enrol(old, "") // finished enrolment with the old link just before
	if err != nil {
		t.Fatal(err)
	}
	signedIn := make(chan error, 1)
	go func() {
		_, err := e.signIn(c)
		signedIn <- err
	}()
	select {
	case err := <-signedIn:
		t.Fatalf("the sign-in didn't wait for the resend: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-resent; err != nil {
		t.Fatalf("resend: %v", err)
	}
	if err := <-signedIn; err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	link := e.linkOf(inv.ID)
	e.dir.mu.Lock()
	_, live := e.dir.invites[link]
	e.dir.mu.Unlock()
	if link == old || live {
		t.Errorf("the resend's link %s is still live in Authentik (old %s)", link, old)
	}
	if code, p := e.user.call(http.MethodPost, resendPath(inv.ID), nil, nil); code != http.StatusConflict || p.Code != "not_pending" {
		t.Errorf("resend after joining: %d %+v", code, p)
	}

	// Finished in Authentik, not yet signed in to the app: refused too.
	link = e.invite(e.user, "t@example.com")
	if _, err := e.dir.enrol(link, ""); err != nil {
		t.Fatal(err)
	}
	tInv := e.invitationTo(e.user, "t@example.com")
	if code, p := e.user.call(http.MethodPost, resendPath(tInv.ID), nil, nil); code != http.StatusConflict || p.Code != "already_joined" {
		t.Errorf("resend after enrolling in Authentik: %d %+v", code, p)
	}
}

// ADR 0021 (owner's review, 2026-11-08): the inactive user an unfinished
// enrolment left goes when its invitation is cancelled (at once), expires
// or is cancelled with its inviter's tree (within the hour); a waiting one's
// stays, and the next run with any of its links, resent ones too, finishes
// with it.
func TestUnfinishedUsersCleared(t *testing.T) {
	e := setup(t)
	users := func(address string) int {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		n := 0
		for _, u := range e.dir.users {
			if u.username == address {
				n++
			}
		}
		return n
	}
	for _, address := range []string{"cancelled@example.com", "expired@example.com", "waiting@example.com", "tree@example.com"} {
		inviter := e.user
		if address == "tree@example.com" {
			inviter = e.admin
		}
		link := e.invite(inviter, address)
		e.dir.start(link)
	}
	cancelled := e.invitationTo(e.user, "cancelled@example.com")
	if code, _ := e.user.call(http.MethodDelete, fmt.Sprintf("/api/v1/invitations/%d", cancelled.ID), nil, nil); code != http.StatusNoContent {
		t.Fatal("cancel")
	}
	if n := users("cancelled@example.com"); n != 0 {
		t.Errorf("cancelled: %d users left", n)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE invitations SET expires_at = now() - interval '1 minute' WHERE email = 'expired@example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE invitations SET status = 'cancelled', cancelled_at = now() WHERE email = 'tree@example.com'`); err != nil {
		t.Fatal(err)
	}
	removed, err := e.acc.ClearUnfinishedUsers(e.ctx)
	if err != nil || removed != 2 {
		t.Fatalf("cleared: %d, %v", removed, err)
	}
	if again, err := e.acc.ClearUnfinishedUsers(e.ctx); err != nil || again != 0 {
		t.Errorf("cleared again: %d, %v", again, err)
	}
	for address, want := range map[string]int{"expired@example.com": 0, "tree@example.com": 0, "waiting@example.com": 1} {
		if n := users(address); n != want {
			t.Errorf("%s: %d users, want %d", address, n, want)
		}
	}

	// The waiting one is resent; its new link finishes with the same user.
	w := e.invitationTo(e.user, "waiting@example.com")
	if code, p := e.user.call(http.MethodPost, resendPath(w.ID), nil, nil); code != http.StatusOK {
		t.Fatalf("resend: %d %+v", code, p)
	}
	c, err := e.dir.enrol(e.linkOf(w.ID), "")
	if err != nil {
		t.Fatalf("the resent link: %v", err)
	}
	if _, err := e.signIn(c); err != nil {
		t.Fatal(err)
	}
	if n := users("waiting@example.com"); n != 1 {
		t.Errorf("waiting: %d users after joining", n)
	}
}
