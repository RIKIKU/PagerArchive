package stacktest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// Milestone M7 against the real stack: tree deactivation goes through its
// preview (R42b, ADR 0008), and someone enrolling through Authentik while a
// preview is open makes confirming it fail.

func (b *browser) preview(root int64) server.DeactivationPreview {
	b.t.Helper()
	var p server.DeactivationPreview
	if code, msg := b.api(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d/deactivation-preview", root), nil, &p); code != http.StatusOK {
		b.t.Fatalf("preview of %d's tree: %d %s", root, code, msg)
	}
	return p
}

// confirmTree confirms a preview with these ticks. A refusal comes back in
// refused, with the updated preview if the tree changed.
func (b *browser) confirmTree(root int64, version string, ticked []int64) (code int, res server.TreeDeactivateResult, refused server.TreeChanged) {
	b.t.Helper()
	data, _ := json.Marshal(server.TreeDeactivateRequest{Version: version, Ticked: ticked})
	req, _ := http.NewRequest(http.MethodPost, b.s.app.JoinPath(fmt.Sprintf("/api/v1/admin/users/%d/deactivate-tree", root)).String(), bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		err = json.Unmarshal(body, &res)
	} else {
		err = json.Unmarshal(body, &refused)
	}
	if err != nil {
		b.t.Fatalf("confirm %d's tree: %d: %v", root, resp.StatusCode, err)
	}
	return resp.StatusCode, res, refused
}

func walkTree(n *server.TreeNode, fn func(*server.TreeNode)) {
	fn(n)
	for _, c := range n.Children {
		walkTree(c, fn)
	}
}

// startTicked is who a preview starts ticked, as the browser shows it.
func startTicked(root *server.TreeNode) []int64 {
	var ids []int64
	walkTree(root, func(n *server.TreeNode) {
		if n.Tick.Start {
			ids = append(ids, n.ID)
		}
	})
	return ids
}

// A50, second half, with a real enrolment: D's invitation is accepted
// through Authentik while the admin's preview of A's tree is open.
func TestA50JoinDuringPreview(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	a := s.inviteAndEnrol(t, o, "a50a@example.com")
	d := s.inviteAndEnrol(t, a, "a50d@example.com")
	xLink := d.invite("a50x@example.com")
	aID, dID := a.me().ID, d.me().ID

	p := o.preview(aID)
	x := s.browser(t, "a50x@example.com")
	x.enrol(xLink)
	code, _, refused := o.confirmTree(aID, p.Version, startTicked(p.Root))
	if code != http.StatusConflict || refused.Code != "tree_changed" {
		t.Fatalf("confirm after a join: %d %+v", code, refused.Problem)
	}
	var newcomer *server.TreeNode
	walkTree(refused.Preview.Root, func(n *server.TreeNode) {
		if n.Email != nil && *n.Email == "a50x@example.com" {
			newcomer = n
		}
	})
	if newcomer == nil || !newcomer.Tick.Start {
		t.Fatalf("the updated preview doesn't tick the new person: %+v", newcomer)
	}
	if parent := refused.Preview.Root.Children; len(parent) != 1 || parent[0].ID != dID || len(parent[0].Children) != 1 {
		t.Errorf("updated preview: the new person isn't under D")
	}
	if exists, active := s.authentikUser(t, "a50a@example.com"); !exists || !active {
		t.Error("the refused confirmation changed A")
	}
	code, res, refused := o.confirmTree(aID, refused.Preview.Version, startTicked(refused.Preview.Root))
	if code != http.StatusOK || res.Deactivated != 3 {
		t.Fatalf("confirm the updated preview: %d %+v %s", code, res, refused.Error)
	}
	for _, m := range []*browser{a, d, x} {
		if exists, active := s.authentikUser(t, m.email); !exists || active {
			t.Errorf("%s in Authentik: exists %v, active %v", m.email, exists, active)
		}
	}
	t.Logf("A50: an enrolment through Authentik during the preview refused the confirmation (%q); the updated preview ticked the new person, and confirming it disabled all 3 in Authentik", refused.Error)
}
