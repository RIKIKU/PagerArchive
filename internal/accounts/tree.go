package accounts

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// MaxReasonLength limits a tree deactivation's reason (ADR 0008 §2).
const MaxReasonLength = 500

// Why a person in a preview can't be ticked (R42b, ADR 0008 §3).
const (
	WhyLastAdmin   = "last admin"
	WhyYou         = "you"
	WhyDeactivated = "already deactivated"
	WhyDeleted     = "deleted"
)

// TreePerson is one person in an invite tree (R42a), in pre-order: each
// person comes before everyone they invited.
type TreePerson struct {
	ID          int64
	Parent      *int64 // nil for the root
	Email       *string
	Name        *string
	Role        string
	Status      string // active, deactivated or deleted
	Joined      time.Time
	Trusted     bool
	Invitations int64 // unused invitations that still work
	// For previews (R42b): whether they can be ticked, why not, and
	// whether they start ticked.
	CanTick bool
	WhyNot  string
	Ticked  bool

	subject *string
}

// Tree is a user's invite tree: who invited them, and everyone below them.
type Tree struct {
	Inviter *db.User // nil for the first admin
	People  []TreePerson
}

// Preview is a tree deactivation's preview (R42b).
type Preview struct {
	Tree
	// Version changes when anyone joins or leaves the tree, or anyone's
	// status, role or trusted mark changes (ADR 0008 §5).
	Version string
}

func (s *Service) loadTree(ctx context.Context, q *db.Queries, root int64) ([]TreePerson, error) {
	rows, err := q.InviteTree(ctx, db.InviteTreeParams{Root: root, Now: s.now()})
	if err != nil {
		return nil, dbErr("invite tree", err)
	}
	people := make([]TreePerson, len(rows))
	for i, r := range rows {
		people[i] = TreePerson{ID: r.ID, Email: r.Email, Name: r.Name, Role: r.Role, Status: r.Status,
			Joined: r.CreatedAt, Trusted: r.TrustedAt != nil, Invitations: r.Invitations, subject: r.Subject}
		if i > 0 {
			people[i].Parent = r.InvitedBy
		}
	}
	return people, nil
}

// Tree returns a user's invite tree (R42a).
func (s *Service) Tree(ctx context.Context, root int64) (Tree, error) {
	u, err := s.Store.UserByID(ctx, root)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted") {
		return Tree{}, refuse(http.StatusNotFound, "no_user", "No such user.")
	}
	if err != nil {
		return Tree{}, dbErr("user", err)
	}
	t := Tree{}
	if u.InvitedBy != nil {
		inv, err := s.Store.UserByID(ctx, *u.InvitedBy)
		if err != nil {
			return Tree{}, dbErr("inviter", err)
		}
		t.Inviter = &inv
	}
	if t.People, err = s.loadTree(ctx, s.Store.Queries, root); err != nil {
		return Tree{}, err
	}
	return t, nil
}

// Preview returns what deactivating root's invite tree would do, as actor
// would start it (R42b, R42c).
func (s *Service) Preview(ctx context.Context, actor db.User, root int64) (Preview, error) {
	t, err := s.Tree(ctx, root)
	if err != nil {
		return Preview{}, err
	}
	admins, err := s.Store.ActiveAdminIDs(ctx)
	if err != nil {
		return Preview{}, dbErr("admins", err)
	}
	tickRules(actor, admins, t.People)
	return Preview{Tree: t, Version: treeVersion(root, t.People)}, nil
}

// tickRules sets who can be ticked, and who starts ticked (ADR 0008 §3).
func tickRules(actor db.User, admins []int64, people []TreePerson) {
	for i := range people {
		p := &people[i]
		switch {
		case p.Status == "deleted":
			p.WhyNot = WhyDeleted
		case p.Status != "active":
			p.WhyNot = WhyDeactivated
		case p.Role == RoleAdmin && lastAdmin(admins, []int64{p.ID}):
			p.WhyNot = WhyLastAdmin
		case p.ID == actor.ID:
			p.WhyNot = WhyYou
		default:
			p.CanTick = true
			p.Ticked = !p.Trusted
		}
	}
}

// treeVersion fingerprints what a preview shows that would change the
// admin's choices: who's in the tree, and each person's status, role and
// trusted mark (ADR 0008 §5).
func treeVersion(root int64, people []TreePerson) string {
	sorted := slices.Clone(people)
	slices.SortFunc(sorted, func(a, b TreePerson) int { return cmp.Compare(a.ID, b.ID) })
	h := sha256.New()
	fmt.Fprintf(h, "root %d\n", root)
	for _, p := range sorted {
		fmt.Fprintf(h, "%d %s %s %t\n", p.ID, p.Status, p.Role, p.Trusted)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ErrTreeChanged means the tree changed since the preview; nothing was
// changed and a new preview is needed (R42b).
var ErrTreeChanged = &Error{Status: http.StatusConflict, Code: "tree_changed",
	Msg: "The invite tree changed since this preview, so nothing was deactivated. Check the updated preview and confirm again."}

// TreeRequest is a confirmed preview: who's ticked, and why (R42b).
type TreeRequest struct {
	Version string
	Ticked  []int64
	Reason  string
}

// TreeResult is what a tree deactivation did.
type TreeResult struct {
	Record               int64
	Deactivated          int
	Spared               int
	InvitationsCancelled int
}

// DeactivateTree deactivates exactly the ticked people in root's invite
// tree and records it (R42b, R42d). It's refused with ErrTreeChanged if the
// tree changed since the preview with req.Version.
func (s *Service) DeactivateTree(ctx context.Context, actor db.User, root int64, req TreeRequest) (TreeResult, error) {
	reason := strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(reason) > MaxReasonLength {
		return TreeResult{}, refuse(http.StatusUnprocessableEntity, "reason_too_long", "Keep the reason to %d characters.", MaxReasonLength)
	}
	if len(req.Ticked) == 0 {
		return TreeResult{}, refuse(http.StatusUnprocessableEntity, "none_ticked", "Nobody is ticked, so there's nobody to deactivate.")
	}
	var res TreeResult
	var ids []int64
	var subjects, cancelled []*string
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.LockActiveAdmins(ctx)
		if err != nil {
			return dbErr("lock admins", err)
		}
		u, err := q.UserByID(ctx, root)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted") {
			return refuse(http.StatusNotFound, "no_user", "No such user.")
		}
		if err != nil {
			return dbErr("user", err)
		}
		// Lock the tree, then read it again: a join that committed in between
		// is seen, and one that hasn't waits for this transaction (ADR 0008 §5).
		people, err := s.loadTree(ctx, q, root)
		if err != nil {
			return err
		}
		all := make([]int64, len(people))
		for i, p := range people {
			all[i] = p.ID
		}
		if err := q.LockUsers(ctx, all); err != nil {
			return dbErr("lock tree", err)
		}
		if people, err = s.loadTree(ctx, q, root); err != nil {
			return err
		}
		if treeVersion(root, people) != req.Version {
			return ErrTreeChanged
		}
		tickRules(actor, admins, people)
		byID := map[int64]TreePerson{}
		for _, p := range people {
			byID[p.ID] = p
		}
		for _, id := range req.Ticked {
			p, ok := byID[id]
			switch {
			case !ok:
				return refuse(http.StatusUnprocessableEntity, "not_in_tree", "Someone ticked isn't in this invite tree.")
			case p.WhyNot == WhyLastAdmin:
				return refuse(http.StatusConflict, "last_admin", "That would leave PagerArchive with no active admin. Make someone else an admin first.")
			case p.WhyNot == WhyYou:
				return refuse(http.StatusConflict, "self", "You can't deactivate yourself.")
			case !p.CanTick:
				return refuse(http.StatusUnprocessableEntity, "not_tickable", "Someone ticked can't be deactivated: they're %s.", p.WhyNot)
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
				subjects = append(subjects, p.subject)
			}
		}
		if lastAdmin(admins, ids) { // R44, whatever was ticked
			return refuse(http.StatusConflict, "last_admin", "That would leave PagerArchive with no active admin. Make someone else an admin first.")
		}
		live, err := q.CountLiveInvitationsBy(ctx, db.CountLiveInvitationsByParams{Ids: ids, Now: s.now()})
		if err != nil {
			return dbErr("count invitations", err)
		}
		if _, cancelled, err = deactivateIn(ctx, q, ids); err != nil {
			return err
		}
		// R42d: who was deactivated, and who was spared (active, unticked).
		var who []int64
		var outcomes []string
		for _, p := range people {
			switch {
			case slices.Contains(ids, p.ID):
				who, outcomes = append(who, p.ID), append(outcomes, "deactivated")
			case p.Status == "active":
				who, outcomes = append(who, p.ID), append(outcomes, "spared")
				res.Spared++
			}
		}
		var why *string
		if reason != "" {
			why = &reason
		}
		if res.Record, err = q.CreateTreeDeactivation(ctx, db.CreateTreeDeactivationParams{
			RootID: root, ActorID: actor.ID, Reason: why, InvitationsCancelled: int32(live),
		}); err != nil {
			return dbErr("record", err)
		}
		if err := q.AddTreeDeactivationPeople(ctx, db.AddTreeDeactivationPeopleParams{
			DeactivationID: res.Record, UserIds: who, Outcomes: outcomes,
		}); err != nil {
			return dbErr("record people", err)
		}
		res.Deactivated, res.InvitationsCancelled = len(ids), int(live)
		return nil
	})
	if err != nil {
		return TreeResult{}, err
	}
	s.Log.Info("tree deactivated", "by", actor.ID, "user", root, "record", res.Record,
		"deactivated", res.Deactivated, "spared", res.Spared, "invitations", res.InvitationsCancelled)
	if failed := s.afterDeactivate(ctx, ids, subjects, cancelled); failed > 0 {
		return res, refuse(http.StatusBadGateway, "directory_partial",
			"Deactivated in PagerArchive (they can't use it any more), but Authentik couldn't be updated for %d of them. Deactivate each of them on their own page to retry.", failed)
	}
	return res, nil
}

// SetTrusted marks a user trusted, or removes the mark (R42c).
func (s *Service) SetTrusted(ctx context.Context, actor db.User, targetID int64, trusted bool) error {
	var by *int64
	if trusted {
		by = &actor.ID
	}
	u, err := s.Store.UserByID(ctx, targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted"):
		return refuse(http.StatusNotFound, "no_user", "No such user.")
	case err != nil:
		return dbErr("user", err)
	case (u.TrustedAt != nil) == trusted:
		return nil // already so: keep who set it and when
	}
	n, err := s.Store.SetUserTrusted(ctx, db.SetUserTrustedParams{ID: targetID, By: by})
	if err != nil {
		return dbErr("set trusted", err)
	}
	if n != 1 {
		return refuse(http.StatusNotFound, "no_user", "No such user.")
	}
	s.Log.Info("trusted mark", "by", actor.ID, "user", targetID, "trusted", trusted)
	return nil
}

// Named is a person as admins see them now: "name (email)", or "deleted
// user" (R43c).
type Named struct {
	ID      int64
	Label   string
	Deleted bool
}

// Record is a tree deactivation's record (R42d).
type Record struct {
	ID                   int64
	At                   time.Time
	By                   string
	Root                 Named
	Reason               *string
	Deactivated, Spared  []Named
	InvitationsCancelled int
}

// Records returns the tree deactivations that name a user, newest first.
func (s *Service) Records(ctx context.Context, userID int64) ([]Record, error) {
	rows, err := s.Store.TreeDeactivationsOf(ctx, userID)
	if err != nil {
		return nil, dbErr("records", err)
	}
	out := make([]Record, len(rows))
	index := map[int64]int{}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = Record{ID: r.ID, At: r.CreatedAt.UTC(), By: Display(r.ActorName, r.ActorEmail, r.ActorStatus),
			Root: Named{ID: r.RootID, Label: Display(r.RootName, r.RootEmail, r.RootStatus), Deleted: r.RootStatus == "deleted"}, Reason: r.Reason,
			Deactivated: []Named{}, Spared: []Named{}, InvitationsCancelled: int(r.InvitationsCancelled)}
		index[r.ID], ids[i] = i, r.ID
	}
	if len(ids) == 0 {
		return out, nil
	}
	people, err := s.Store.TreeDeactivationPeople(ctx, ids)
	if err != nil {
		return nil, dbErr("record people", err)
	}
	for _, p := range people {
		r := &out[index[p.DeactivationID]]
		n := Named{ID: p.UserID, Label: Display(p.Name, p.Email, p.Status), Deleted: p.Status == "deleted"}
		if p.Outcome == "deactivated" {
			r.Deactivated = append(r.Deactivated, n)
		} else {
			r.Spared = append(r.Spared, n)
		}
	}
	return out, nil
}
