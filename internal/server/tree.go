package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Invite trees, their deactivation preview, trusted users and deactivation
// records (R42a-R42d, ADR 0008). Admins only.

// TreeNode is one person in an invite tree, with everyone they invited.
type TreeNode struct {
	ID          int64     `json:"id"`
	Label       string    `json:"label"` // "name (email)", or "deleted user"
	Name        *string   `json:"name"`
	Email       *string   `json:"email"`
	Role        string    `json:"role"`
	Status      string    `json:"status"` // active, deactivated or deleted
	Joined      time.Time `json:"joined"`
	Trusted     bool      `json:"trusted"`
	Invitations int64     `json:"invitations"` // unused invitations that still work
	// Previews only (R42b): whether they can be ticked, why not, and
	// whether they start ticked.
	Tick     *Tick       `json:"tick,omitempty"`
	Children []*TreeNode `json:"children"`
}

// Tick is a person's checkbox in a preview.
type Tick struct {
	Can   bool   `json:"can"`
	Why   string `json:"why,omitempty"` // last admin, you, already deactivated, deleted
	Start bool   `json:"start"`
}

// PersonRef names a person, with whether they can still be looked at.
type PersonRef struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Deleted bool   `json:"deleted"`
}

// InviteTree is who invited a user, and the user with everyone below them
// (R42a).
type InviteTree struct {
	Inviter *PersonRef `json:"inviter"` // null for the first admin
	Root    *TreeNode  `json:"root"`
}

// DeactivationPreview is what deactivating a tree would do (R42b).
type DeactivationPreview struct {
	InviteTree
	Version string `json:"version"`
}

// TreeDeactivateRequest is the body of POST
// /api/v1/admin/users/{id}/deactivate-tree.
type TreeDeactivateRequest struct {
	Version string  `json:"version"`
	Ticked  []int64 `json:"ticked"`
	Reason  string  `json:"reason"`
}

// TreeDeactivateResult is what was done.
type TreeDeactivateResult struct {
	Record               int64 `json:"record"`
	Deactivated          int   `json:"deactivated"`
	Spared               int   `json:"spared"`
	InvitationsCancelled int   `json:"invitations_cancelled"`
}

// TreeChanged refuses a confirmation whose tree changed, with the updated
// preview.
type TreeChanged struct {
	Problem
	Preview DeactivationPreview `json:"preview"`
}

// TrustedRequest is the body of PUT /api/v1/admin/users/{id}/trusted.
type TrustedRequest struct {
	Trusted bool `json:"trusted"`
}

// DeactivationRecord is a tree deactivation's record (R42d).
type DeactivationRecord struct {
	ID                   int64       `json:"id"`
	At                   time.Time   `json:"at"`
	By                   string      `json:"by"`
	Root                 PersonRef   `json:"root"`
	Reason               *string     `json:"reason"`
	Deactivated          []PersonRef `json:"deactivated"`
	Spared               []PersonRef `json:"spared"`
	InvitationsCancelled int         `json:"invitations_cancelled"`
}

func ref(u db.User) *PersonRef {
	return &PersonRef{ID: u.ID, Label: accounts.Display(u.Name, u.Email, u.Status), Deleted: u.Status == "deleted"}
}

// nest turns a tree in pre-order into nested nodes.
func nest(t accounts.Tree, preview bool) InviteTree {
	out := InviteTree{}
	if t.Inviter != nil {
		out.Inviter = ref(*t.Inviter)
	}
	nodes := map[int64]*TreeNode{}
	for _, p := range t.People {
		n := &TreeNode{ID: p.ID, Label: accounts.Display(p.Name, p.Email, p.Status), Name: p.Name, Email: p.Email,
			Role: p.Role, Status: p.Status, Joined: p.Joined.UTC(), Trusted: p.Trusted, Invitations: p.Invitations,
			Children: []*TreeNode{}}
		if preview {
			n.Tick = &Tick{Can: p.CanTick, Why: p.WhyNot, Start: p.Ticked}
		}
		nodes[p.ID] = n
		if p.Parent == nil {
			out.Root = n
		} else if parent := nodes[*p.Parent]; parent != nil {
			parent.Children = append(parent.Children, n)
		}
	}
	return out
}

func refs(ns []accounts.Named) []PersonRef {
	out := make([]PersonRef, len(ns))
	for i, n := range ns {
		out[i] = PersonRef{ID: n.ID, Label: n.Label, Deleted: n.Deleted}
	}
	return out
}

// treeAndRecords is a user's invite tree and the records naming them, for
// their page in Administration.
func (s *Server) treeAndRecords(ctx context.Context, id int64) (InviteTree, []DeactivationRecord, error) {
	t, err := s.Accounts.Tree(ctx, id)
	if err != nil {
		return InviteTree{}, nil, err
	}
	recs, err := s.Accounts.Records(ctx, id)
	if err != nil {
		return InviteTree{}, nil, err
	}
	out := make([]DeactivationRecord, len(recs))
	for i, r := range recs {
		out[i] = DeactivationRecord{ID: r.ID, At: r.At, By: r.By, Reason: r.Reason, InvitationsCancelled: r.InvitationsCancelled,
			Root: PersonRef{ID: r.Root.ID, Label: r.Root.Label, Deleted: r.Root.Deleted}, Deactivated: refs(r.Deactivated), Spared: refs(r.Spared)}
	}
	return nest(t, false), out, nil
}

func (s *Server) preview(ctx context.Context, actor db.User, id int64) (DeactivationPreview, error) {
	p, err := s.Accounts.Preview(ctx, actor, id)
	if err != nil {
		return DeactivationPreview{}, err
	}
	return DeactivationPreview{InviteTree: nest(p.Tree, true), Version: p.Version}, nil
}

func (s *Server) deactivationPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.preview(r.Context(), currentUser(r), id)
	if err != nil {
		s.accountsError(w, "deactivation preview", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deactivateTree(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req TreeDeactivateRequest
	// A large tree's ticks and a 500-character reason need more than 4 KiB.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	res, err := s.Accounts.DeactivateTree(r.Context(), currentUser(r), id,
		accounts.TreeRequest{Version: req.Version, Ticked: req.Ticked, Reason: req.Reason})
	if errors.Is(err, accounts.ErrTreeChanged) {
		p, perr := s.preview(r.Context(), currentUser(r), id)
		if perr != nil {
			s.accountsError(w, "deactivation preview", perr)
			return
		}
		writeJSON(w, http.StatusConflict, TreeChanged{Problem: Problem{Error: accounts.ErrTreeChanged.Msg, Code: accounts.ErrTreeChanged.Code}, Preview: p})
		return
	}
	out := TreeDeactivateResult{Record: res.Record, Deactivated: res.Deactivated, Spared: res.Spared, InvitationsCancelled: res.InvitationsCancelled}
	var refusal *accounts.Error
	if errors.As(err, &refusal) && refusal.Code == "directory_partial" {
		// Done in the app; Authentik needs another go. Say both.
		writeJSON(w, refusal.Status, struct {
			Problem
			TreeDeactivateResult
		}{Problem{Error: refusal.Msg, Code: refusal.Code}, out})
		return
	}
	if err != nil {
		s.accountsError(w, "deactivate tree", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setTrusted(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req TrustedRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Accounts.SetTrusted(r.Context(), currentUser(r), id, req.Trusted); err != nil {
		s.accountsError(w, "set trusted", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
