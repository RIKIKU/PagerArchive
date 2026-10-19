package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// Milestone M7: the invite tree, its deactivation preview, trusted users
// and deactivation records (R42a-R42d, ADR 0008).

func (e *env) preview(m *member, root int64) server.DeactivationPreview {
	e.t.Helper()
	var p server.DeactivationPreview
	if code, pr := m.call(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d/deactivation-preview", root), nil, &p); code != http.StatusOK {
		e.t.Fatalf("preview of %d's tree: %d %s", root, code, pr.Error)
	}
	return p
}

// confirm confirms a preview with these ticks. A refusal comes back in
// refused, with the updated preview if the tree changed.
func (e *env) confirm(m *member, root int64, version string, ticked []int64, reason string) (code int, res server.TreeDeactivateResult, refused server.TreeChanged) {
	e.t.Helper()
	resp, err := m.do(http.MethodPost, fmt.Sprintf("%s/api/v1/admin/users/%d/deactivate-tree", e.url, root),
		server.TreeDeactivateRequest{Version: version, Ticked: ticked, Reason: reason})
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		err = json.NewDecoder(resp.Body).Decode(&res)
	} else {
		err = json.NewDecoder(resp.Body).Decode(&refused)
	}
	if err != nil {
		e.t.Fatalf("confirm: %d: %v", resp.StatusCode, err)
	}
	return resp.StatusCode, res, refused
}

// deactivateTree is what the browser does by default: preview, then
// confirm with the starting ticks.
func (e *env) deactivateTree(m *member, root int64) server.TreeDeactivateResult {
	e.t.Helper()
	p := e.preview(m, root)
	code, res, refused := e.confirm(m, root, p.Version, startTicked(p.Root), "")
	if code != http.StatusOK {
		e.t.Fatalf("deactivate %d's tree: %d %s", root, code, refused.Error)
	}
	return res
}

func walk(n *server.TreeNode, fn func(*server.TreeNode)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

// startTicked is who a preview starts ticked.
func startTicked(root *server.TreeNode) []int64 {
	var ids []int64
	walk(root, func(n *server.TreeNode) {
		if n.Tick.Start {
			ids = append(ids, n.ID)
		}
	})
	return ids
}

func node(root *server.TreeNode, id int64) *server.TreeNode {
	var found *server.TreeNode
	walk(root, func(n *server.TreeNode) {
		if n.ID == id {
			found = n
		}
	})
	return found
}

// shape writes a tree as "a(b(d) c)", by the part of each label before
// "@", so a test can compare it with the tree it built.
func shape(n *server.TreeNode) string {
	s, _, _ := strings.Cut(n.Label, "@")
	if len(n.Children) == 0 {
		return s
	}
	var kids []string
	for _, c := range n.Children {
		kids = append(kids, shape(c))
	}
	return s + "(" + strings.Join(kids, " ") + ")"
}

// totals is what a preview's ticks would do: people deactivated and unused
// invitations cancelled (R42b).
func totals(root *server.TreeNode, ticked []int64) (people int, invitations int64) {
	walk(root, func(n *server.TreeNode) {
		if slices.Contains(ticked, n.ID) {
			people++
			invitations += n.Invitations
		}
	})
	return people, invitations
}

func without(ids []int64, drop ...int64) []int64 {
	return slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return slices.Contains(drop, id) })
}

func (e *env) adminUser(m *member, id int64) server.AdminUser {
	e.t.Helper()
	var u server.AdminUser
	if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d", id), nil, &u); code != http.StatusOK {
		e.t.Fatalf("admin page of %d: %d %s", id, code, p.Error)
	}
	return u
}

// a48Tree is A48's tree: the admin invited A, A invited B and C, B invited
// D, and D has an unused invitation. tag keeps each copy's addresses apart.
type a48Tree struct {
	a, b, c, d *member
	dInvite    string // the address D invited
}

func (e *env) a48Tree(tag string) a48Tree {
	e.t.Helper()
	t := a48Tree{a: e.inviteAndEnrol(e.admin, "a"+tag+"@example.com")}
	t.b = e.inviteAndEnrol(t.a, "b"+tag+"@example.com")
	t.c = e.inviteAndEnrol(t.a, "c"+tag+"@example.com")
	t.d = e.inviteAndEnrol(t.b, "d"+tag+"@example.com")
	t.dInvite = "x" + tag + "@example.com"
	if code, p := t.d.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: t.dInvite}, nil); code != http.StatusCreated {
		e.t.Fatalf("D invites %s: %d %s", t.dInvite, code, p.Error)
	}
	return t
}

// A48. Invite tree and preview.
func TestA48InviteTreeAndPreview(t *testing.T) {
	e := setup(t)
	tr := e.a48Tree("")
	page := e.adminUser(e.admin, tr.a.id)
	if page.Tree.Inviter == nil || page.Tree.Inviter.Label != "admin@example.com" {
		t.Errorf("A's inviter: %+v", page.Tree.Inviter)
	}
	if got := shape(page.Tree.Root); got != "a(b(d) c)" {
		t.Errorf("A's tree: %s, want a(b(d) c)", got)
	}
	walk(page.Tree.Root, func(n *server.TreeNode) {
		if n.Role != "user" || n.Status != "active" || n.Joined.IsZero() || n.Email == nil || n.Tick != nil {
			t.Errorf("tree node %+v", n)
		}
	})

	p := e.preview(e.admin, tr.a.id)
	ticked := startTicked(p.Root)
	people, invitations := totals(p.Root, ticked)
	if len(ticked) != 4 || people != 4 || invitations != 1 {
		t.Errorf("preview: %d ticked, %d people, %d invitations; want 4, 4, 1", len(ticked), people, invitations)
	}
	t.Logf("A48: tree %s under %s; preview: %d people will be deactivated and %d unused invitation cancelled",
		shape(page.Tree.Root), page.Tree.Inviter.Label, people, invitations)
}

// A49. Sparing.
func TestA49Sparing(t *testing.T) {
	e := setup(t)

	// Untick only B.
	tr := e.a48Tree("1")
	p := e.preview(e.admin, tr.a.id)
	code, res, refused := e.confirm(e.admin, tr.a.id, p.Version, without(startTicked(p.Root), tr.b.id), "")
	if code != http.StatusOK || res.Deactivated != 3 || res.Spared != 1 {
		t.Fatalf("untick B: %d %+v %s", code, res, refused.Error)
	}
	for _, m := range []*member{tr.a, tr.c, tr.d} {
		if e.canSignIn(m) {
			t.Errorf("%s can still sign in", m.email)
		}
	}
	if !e.canSignIn(tr.b) {
		t.Error("B can't sign in")
	}
	bPage := e.adminUser(e.admin, tr.b.id)
	if bPage.User.InvitedBy == nil || *bPage.User.InvitedBy != tr.a.email || bPage.Tree.Inviter == nil || bPage.Tree.Inviter.ID != tr.a.id {
		t.Errorf("B's inviter: %v, tree inviter %+v", bPage.User.InvitedBy, bPage.Tree.Inviter)
	}

	// "Spare this branch" on B: B and D unticked.
	tr2 := e.a48Tree("2")
	p = e.preview(e.admin, tr2.a.id)
	code, res, refused = e.confirm(e.admin, tr2.a.id, p.Version, without(startTicked(p.Root), tr2.b.id, tr2.d.id), "")
	if code != http.StatusOK || res.Deactivated != 2 || res.Spared != 2 || res.InvitationsCancelled != 0 {
		t.Fatalf("spare B's branch: %d %+v %s", code, res, refused.Error)
	}
	for _, m := range []*member{tr2.a, tr2.c} {
		if e.canSignIn(m) {
			t.Errorf("%s can still sign in", m.email)
		}
	}
	for _, m := range []*member{tr2.b, tr2.d} {
		if !e.canSignIn(m) {
			t.Errorf("%s can't sign in", m.email)
		}
	}
	id, ok := e.dir.invitationFor(tr2.dInvite)
	if !ok {
		t.Fatal("D's invitation is gone")
	}
	claims, err := e.dir.enrol(id, "")
	if err != nil {
		t.Fatalf("D's invitation doesn't work: %v", err)
	}
	x, err := e.signIn(claims)
	if err != nil {
		t.Fatalf("D's invitee signs in: %v", err)
	}
	if me := e.me(x); me.InvitedBy == nil || *me.InvitedBy != tr2.d.email {
		t.Errorf("D's invitee's inviter: %v", me.InvitedBy)
	}
	t.Log("A49: unticking B deactivated A, C and D, B active with A as inviter; sparing B's branch deactivated A and C, B and D active, D's invitation used")
}

// A50. Trusted, record and changes.
func TestA50TrustedRecordAndChanges(t *testing.T) {
	e := setup(t)
	tr := e.a48Tree("1")
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/trusted", tr.c.id), server.TrustedRequest{Trusted: true}, nil); code != http.StatusNoContent {
		t.Fatalf("mark C trusted: %d %s", code, p.Error)
	}
	if u := e.adminUser(e.admin, tr.c.id).User; u.Trusted == nil || u.Trusted.By != "admin@example.com" || u.Trusted.At.IsZero() {
		t.Errorf("C's mark: %+v", u.Trusted)
	}
	p := e.preview(e.admin, tr.a.id)
	if c := node(p.Root, tr.c.id); c == nil || c.Tick.Start || !c.Tick.Can || !c.Trusted {
		t.Fatalf("C in the preview: %+v", c)
	}
	code, res, refused := e.confirm(e.admin, tr.a.id, p.Version, startTicked(p.Root), "spam")
	if code != http.StatusOK || res.Deactivated != 3 {
		t.Fatalf("confirm: %d %+v %s", code, res, refused.Error)
	}
	labels := func(rs []server.PersonRef) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Label)
		}
		return out
	}
	for _, m := range []*member{tr.a, tr.c, tr.d} {
		recs := e.adminUser(e.admin, m.id).Deactivations
		if len(recs) != 1 {
			t.Fatalf("%s's page: %d records", m.email, len(recs))
		}
		r := recs[0]
		if r.Reason == nil || *r.Reason != "spam" || r.By != "admin@example.com" || r.Root.ID != tr.a.id ||
			!slices.Equal(labels(r.Deactivated), []string{tr.a.email, tr.b.email, tr.d.email}) ||
			!slices.Equal(labels(r.Spared), []string{tr.c.email}) || r.InvitationsCancelled != 1 {
			t.Errorf("record on %s's page: %+v", m.email, r)
		}
	}
	if strings.Contains(e.logs.String(), "spam") {
		t.Error("the reason is in the log")
	}
	// The users' own API never says who's trusted.
	if b, _ := json.Marshal(e.me(tr.c)); strings.Contains(string(b), "trust") {
		t.Errorf("C's /me: %s", b)
	}

	// A fresh tree and an open preview; D's invitation is accepted.
	tr2 := e.a48Tree("2")
	p = e.preview(e.admin, tr2.a.id)
	id, _ := e.dir.invitationFor(tr2.dInvite)
	claims, err := e.dir.enrol(id, "")
	if err != nil {
		t.Fatal(err)
	}
	x, err := e.signIn(claims)
	if err != nil {
		t.Fatal(err)
	}
	code, _, refused = e.confirm(e.admin, tr2.a.id, p.Version, startTicked(p.Root), "")
	if code != http.StatusConflict || refused.Code != "tree_changed" {
		t.Fatalf("confirm after a join: %d %+v", code, refused.Problem)
	}
	if got := shape(refused.Preview.Root); got != "a2(b2(d2(x2)) c2)" {
		t.Errorf("updated preview: %s", got)
	}
	if n := node(refused.Preview.Root, x.id); n == nil || !n.Tick.Start {
		t.Errorf("the new person in the updated preview: %+v", n)
	}
	if !e.canSignIn(tr2.a) {
		t.Error("the refused confirmation changed something")
	}
	// Confirming the updated preview works, and includes the new person.
	code, res, _ = e.confirm(e.admin, tr2.a.id, refused.Preview.Version, startTicked(refused.Preview.Root), "")
	if code != http.StatusOK || res.Deactivated != 5 || e.canSignIn(x) {
		t.Errorf("confirm the updated preview: %d %+v", code, res)
	}
	t.Logf("A50: C started unticked; record: reason %q, deactivated A, B and D, spared C; a join during the preview was refused (%q) and the updated preview had %s",
		"spam", refused.Error, shape(refused.Preview.Root))
}

// Every kind of person in a preview (ADR 0008 §3): already deactivated,
// a deleted account, an admin who isn't the last, the acting admin, and a
// trusted person ticked deliberately.
func TestM7PreviewFollowsEveryone(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	b := e.inviteAndEnrol(a, "b@example.com")
	c := e.inviteAndEnrol(a, "c@example.com")
	p := e.inviteAndEnrol(a, "p@example.com")
	tt := e.inviteAndEnrol(a, "t@example.com")
	del := e.inviteAndEnrol(a, "e@example.com")
	f := e.inviteAndEnrol(b, "f@example.com")
	g := e.inviteAndEnrol(c, "g@example.com")
	h := e.inviteAndEnrol(del, "h@example.com")
	q := e.inviteAndEnrol(f, "q@example.com")
	path := func(m *member, op string) string { return fmt.Sprintf("/api/v1/admin/users/%d/%s", m.id, op) }
	for _, m := range []*member{p, q} {
		if code, _ := e.admin.call(http.MethodPut, path(m, "role"), server.RoleRequest{Role: "admin"}, nil); code != http.StatusNoContent {
			t.Fatalf("promote %s", m.email)
		}
	}
	if code, _ := e.admin.call(http.MethodPost, path(c, "deactivate"), server.DeactivateRequest{}, nil); code != http.StatusOK {
		t.Fatal("deactivate C")
	}
	if code, _ := del.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatal("E deletes their account")
	}
	if code, _ := e.admin.call(http.MethodPut, path(tt, "trusted"), server.TrustedRequest{Trusted: true}, nil); code != http.StatusNoContent {
		t.Fatal("mark T trusted")
	}

	// P, an admin in A's tree, deactivates it.
	pv := e.preview(p, a.id)
	if got := shape(pv.Root); got != "a(b(f(q)) c(g) p t deleted user(h))" {
		t.Errorf("tree: %s", got)
	}
	want := map[int64]server.Tick{
		a.id: {Can: true, Start: true}, b.id: {Can: true, Start: true}, f.id: {Can: true, Start: true},
		q.id: {Can: true, Start: true}, c.id: {Why: "already deactivated"}, g.id: {Can: true, Start: true},
		p.id: {Why: "you"}, tt.id: {Can: true}, del.id: {Why: "deleted"}, h.id: {Can: true, Start: true},
	}
	walk(pv.Root, func(n *server.TreeNode) {
		if *n.Tick != want[n.ID] {
			t.Errorf("%s: tick %+v, want %+v", n.Label, *n.Tick, want[n.ID])
		}
	})
	if n := node(pv.Root, del.id); n.Email != nil || n.Name != nil || n.Label != "deleted user" {
		t.Errorf("deleted account: %+v", n)
	}

	// What can't be ticked is refused, and changes nothing.
	ticked := append(startTicked(pv.Root), tt.id)
	for name, tc := range map[string]struct {
		ticked []int64
		reason string
		code   int
		refuse string
	}{
		"already deactivated": {append(slices.Clone(ticked), c.id), "", http.StatusUnprocessableEntity, "not_tickable"},
		"deleted":             {append(slices.Clone(ticked), del.id), "", http.StatusUnprocessableEntity, "not_tickable"},
		"yourself":            {append(slices.Clone(ticked), p.id), "", http.StatusConflict, "self"},
		"outside the tree":    {append(slices.Clone(ticked), e.user.id), "", http.StatusUnprocessableEntity, "not_in_tree"},
		"nobody":              {nil, "", http.StatusUnprocessableEntity, "none_ticked"},
		"reason too long":     {ticked, strings.Repeat("x", 501), http.StatusUnprocessableEntity, "reason_too_long"},
	} {
		code, _, refused := e.confirm(p, a.id, pv.Version, tc.ticked, tc.reason)
		if code != tc.code || refused.Code != tc.refuse {
			t.Errorf("%s: %d %+v", name, code, refused.Problem)
		}
	}
	if !e.canSignIn(a) {
		t.Fatal("a refused confirmation changed something")
	}

	// The defaults, plus T ticked deliberately.
	code, res, refused := e.confirm(p, a.id, pv.Version, ticked, "")
	if code != http.StatusOK || res.Deactivated != 7 || res.Spared != 1 {
		t.Fatalf("confirm: %d %+v %s", code, res, refused.Error)
	}
	for _, m := range []*member{a, b, f, q, g, tt, h} {
		if e.canSignIn(m) {
			t.Errorf("%s can still sign in", m.email)
		}
	}
	if !e.canSignIn(p) || e.me(p).Role != "admin" {
		t.Error("P isn't an active admin any more")
	}
	rec := e.adminUser(e.admin, a.id).Deactivations[0]
	if len(rec.Spared) != 1 || rec.Spared[0].ID != p.id || len(rec.Deactivated) != 7 || rec.By != "p@example.com" {
		t.Errorf("record: %+v", rec)
	}
	// C was already deactivated and E deleted: neither is in the record.
	for _, r := range append(rec.Deactivated, rec.Spared...) {
		if r.ID == c.id || r.ID == del.id {
			t.Errorf("record names %s", r.Label)
		}
	}
	t.Logf("M7 people: already deactivated and deleted can't be ticked and the tree continues through them; P (you) locked; Q (admin) and T (trusted, ticked by hand) deactivated; 7 deactivated, P spared")
}

// What changes during a preview refuses it (status, role, trusted mark,
// someone joining) and what doesn't (a new invitation, which is cancelled
// and counted) (ADR 0008 §5).
func TestM7ChangesDuringPreview(t *testing.T) {
	e := setup(t)
	tr := e.a48Tree("")
	path := func(m *member, op string) string { return fmt.Sprintf("/api/v1/admin/users/%d/%s", m.id, op) }
	for name, change := range map[string]func(){
		"trusted mark": func() { e.admin.call(http.MethodPut, path(tr.c, "trusted"), server.TrustedRequest{Trusted: true}, nil) },
		"role":         func() { e.admin.call(http.MethodPut, path(tr.d, "role"), server.RoleRequest{Role: "admin"}, nil) },
		"status":       func() { e.admin.call(http.MethodPost, path(tr.b, "deactivate"), server.DeactivateRequest{}, nil) },
	} {
		p := e.preview(e.admin, tr.a.id)
		change()
		if code, _, refused := e.confirm(e.admin, tr.a.id, p.Version, startTicked(p.Root), ""); code != http.StatusConflict || refused.Code != "tree_changed" {
			t.Errorf("%s changed: %d %+v", name, code, refused.Problem)
		}
	}
	p := e.preview(e.admin, tr.a.id)
	if code, _ := tr.a.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "late@example.com"}, nil); code != http.StatusCreated {
		t.Fatal("A invites late@example.com")
	}
	code, res, refused := e.confirm(e.admin, tr.a.id, p.Version, startTicked(p.Root), "")
	if code != http.StatusOK || res.InvitationsCancelled != 2 {
		t.Fatalf("a new invitation during the preview: %d %+v %s", code, res, refused.Error)
	}
	if _, ok := e.dir.invitationFor("late@example.com"); ok {
		t.Error("the invitation made during the preview still works")
	}
	t.Log("M7 changes: trusted mark, role and status changes refused the confirmation; a new invitation didn't, and was cancelled and counted")
}

// An admin who re-invites the person who invited them makes a cycle
// through the root; the tree still ends (ADR 0008 §1).
func TestM7InviteCycle(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	x := e.inviteAndEnrol(a, "x@example.com")
	if code, _ := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", x.id), server.RoleRequest{Role: "admin"}, nil); code != http.StatusNoContent {
		t.Fatal("promote X")
	}
	if code, _ := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", a.id), server.DeactivateRequest{}, nil); code != http.StatusOK {
		t.Fatal("deactivate A")
	}
	if code, p := x.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "a@example.com"}, nil); code != http.StatusCreated {
		t.Fatalf("X re-invites A: %d %s", code, p.Error)
	}
	id, _ := e.dir.invitationFor("a@example.com")
	claims, err := e.dir.enrol(id, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.signIn(claims); err != nil {
		t.Fatal(err)
	}
	for m, want := range map[*member]string{a: "a(x)", x: "x(a)"} {
		if got := shape(e.adminUser(e.admin, m.id).Tree.Root); got != want {
			t.Errorf("%s's tree: %s, want %s", m.email, got, want)
		}
	}
	if res := e.deactivateTree(e.admin, x.id); res.Deactivated != 2 {
		t.Errorf("deactivate X's tree: %+v", res)
	}
	t.Log("M7 cycle: A and X invited each other; both trees end and deactivating one reaches both")
}

// The tree, its preview and the trusted mark are for admins only; a
// single deactivation no longer takes a tree (ADR 0008 §4, §8).
func TestM7AdminsOnly(t *testing.T) {
	e := setup(t)
	path := func(op string) string { return fmt.Sprintf("/api/v1/admin/users/%d/%s", e.admin.id, op) }
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, path("deactivation-preview"), nil},
		{http.MethodPost, path("deactivate-tree"), server.TreeDeactivateRequest{Version: "x", Ticked: []int64{e.admin.id}}},
		{http.MethodPut, path("trusted"), server.TrustedRequest{Trusted: true}},
		{http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d", e.admin.id), nil},
	} {
		if code, _ := e.user.call(c.method, c.path, c.body, nil); code != http.StatusForbidden {
			t.Errorf("user: %s %s: %d", c.method, c.path, code)
		}
	}
	code, _ := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", e.user.id), map[string]bool{"tree": true}, nil)
	if code != http.StatusBadRequest || !e.canSignIn(e.user) {
		t.Errorf(`deactivate with "tree": %d`, code)
	}
	t.Log(`M7: preview, confirm, trusted mark and user page refused to users (403); "tree" on single deactivation refused (400)`)
}
