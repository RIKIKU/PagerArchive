package accounts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Who sees the app switcher (R77f, ADR 0055): the members of one Authentik
// group, the one RadioArchive's gate admits while it's in preview. The group
// is the only list: an admin's mark adds or removes the membership.

// DefaultSwitcherGroup is RADIOARCHIVE_GROUP's default.
const DefaultSwitcherGroup = "radioarchive-preview"

// SwitcherReadEvery is how often the group's members are read: a change made
// in Authentik shows within this long (R77f).
const SwitcherReadEvery = 30 * time.Second

// ErrNoGroup means Authentik has no group of that name.
var ErrNoGroup = errors.New("no such group")

// GroupEditor reads one Authentik group's members and changes them.
type GroupEditor interface {
	// GroupMembers finds the group by name, returning its ID and the
	// subjects of its members; ErrNoGroup (wrapped) when there's none.
	GroupMembers(ctx context.Context, group string) (id string, members map[string]bool, err error)
	// SetGroupMember adds the user with this subject to the group, or
	// removes them. Adding a member or removing someone who isn't one is
	// no change, and no error.
	SetGroupMember(ctx context.Context, groupID, subject string, member bool) error
}

// Problems reading the group, for the Users page (GET /api/v1/admin/switcher).
const (
	GroupMissing    = "missing"    // Authentik has no group of that name
	GroupUnreadable = "unreadable" // the last read failed
)

// SwitcherGroup is what the API last read of the group, read again every
// SwitcherReadEvery by Run. Requests only ever look at what was read, so none
// waits on Authentik.
type SwitcherGroup struct {
	Name string
	Dir  GroupEditor
	Log  *slog.Logger

	mu      sync.Mutex
	id      string
	members map[string]bool
	problem string
	failing bool   // the last read failed: logged once, as is the recovery
	writes  uint64 // changes made here; a read that started before one is dropped
}

// Run reads the group now and then every SwitcherReadEvery, until ctx ends.
func (g *SwitcherGroup) Run(ctx context.Context) {
	g.run(ctx, SwitcherReadEvery)
}

func (g *SwitcherGroup) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		g.Read(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Read reads the group's members once. A failed read keeps the members read
// before (Authentik restarting shouldn't hide the switcher from everyone);
// a missing group has none.
func (g *SwitcherGroup) Read(ctx context.Context) {
	g.mu.Lock()
	writes := g.writes
	g.mu.Unlock()
	id, members, err := g.Dir.GroupMembers(ctx, g.Name)
	if ctx.Err() != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.writes != writes {
		return // a change made here meanwhile: this read may not have it
	}
	switch {
	case err == nil:
		g.id, g.members, g.problem = id, members, ""
	case errors.Is(err, ErrNoGroup):
		g.id, g.members, g.problem = "", nil, GroupMissing
	default:
		g.problem = GroupUnreadable
	}
	switch {
	case err != nil && !g.failing:
		g.failing = true
		g.Log.Warn("authentik: couldn't read the switcher's group; reading it again every 30 s", "group", g.Name, "error", err.Error())
	case err == nil && g.failing:
		g.failing = false
		g.Log.Info("authentik: the switcher's group is read again", "group", g.Name, "members", len(members))
	}
}

// Member reports whether the user with this subject was in the group when it
// was last read, or changed here since.
func (g *SwitcherGroup) Member(subject string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.members[subject]
}

// Problem is GroupMissing, GroupUnreadable or "".
func (g *SwitcherGroup) Problem() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.problem
}

func errNoGroup(name string) error {
	return refuse(http.StatusConflict, "no_group",
		"Authentik has no group %s, so nobody can be added to it. PagerArchive's Authentik blueprint makes it: check that Authentik has applied it.", name)
}

var errGroupUnavailable = refuse(http.StatusServiceUnavailable, "directory_unavailable",
	"The sign-in service (Authentik) couldn't be reached, or refused the change, so nothing was changed. Try again shortly.")

// set adds the subject to the group or removes them, in Authentik, and
// keeps the change in what was read.
func (g *SwitcherGroup) set(ctx context.Context, subject string, member bool) error {
	g.mu.Lock()
	id := g.id
	g.mu.Unlock()
	if id == "" {
		// Not read yet, or missing when last read: ask now.
		var err error
		if id, _, err = g.Dir.GroupMembers(ctx, g.Name); errors.Is(err, ErrNoGroup) {
			return errNoGroup(g.Name)
		} else if err != nil {
			g.Log.Error("authentik", "op", "find the switcher's group", "error", err.Error())
			return errGroupUnavailable
		}
	}
	if err := g.Dir.SetGroupMember(ctx, id, subject, member); err != nil {
		g.Log.Error("authentik", "op", "change the switcher's group", "error", err.Error())
		return errGroupUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writes++
	if g.id == "" {
		g.id = id
	}
	if g.members == nil {
		g.members = map[string]bool{}
	}
	if member {
		g.members[subject] = true
	} else {
		delete(g.members, subject)
	}
	if g.problem == GroupMissing {
		g.problem = ""
	}
	return nil
}

// SetSeesSwitcher shows a user the app switcher or hides it (R77f): it adds
// them to the switcher's Authentik group or removes them, so RadioArchive's
// gate follows too. Only that user's membership changes. A service account,
// which never sees the web app, has none; someone who has never signed in has
// no Authentik user yet. There's no audit trail of user changes, so it's
// logged, as the trusted mark is.
func (s *Service) SetSeesSwitcher(ctx context.Context, actor db.User, targetID int64, sees bool) error {
	if s.Switcher == nil {
		return refuse(http.StatusConflict, "no_radioarchive", "There's no RadioArchive beside PagerArchive (RADIOARCHIVE_URL), so there's no switcher.")
	}
	u, err := s.Store.UserByID(ctx, targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted"):
		return refuse(http.StatusNotFound, "no_user", "No such user.")
	case err != nil:
		return dbErr("user", err)
	case IsServiceAccount(u):
		return errServiceShellOnly
	case u.Subject == nil:
		return refuse(http.StatusConflict, "never_signed_in",
			"They haven't signed in yet, so they have no Authentik user to add to %s. Try again once they have.", s.Switcher.Name)
	}
	if err := s.Switcher.set(ctx, *u.Subject, sees); err != nil {
		return err
	}
	s.Log.Info("switcher mark", "by", actor.ID, "user", targetID, "sees_switcher", sees)
	return nil
}

// MoveSwitcherMarks adds the users an admin marked to see the switcher before
// the mark became the group's membership (migration 41) to the group, once
// (R77f, ADR 0055). It only adds: nobody already in the group is removed.
// Authentik can take minutes to answer on a first start, so it asks again
// every interval, up to tries times; a user it couldn't add, or who hasn't
// signed in yet, waits for the next start.
func (s *Service) MoveSwitcherMarks(ctx context.Context, every time.Duration, tries int) {
	if s.Switcher == nil {
		return
	}
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
		moved, waiting, err := s.moveSwitcherMarks(ctx)
		if err != nil {
			continue
		}
		if moved > 0 || waiting > 0 {
			s.Log.Info("switcher marks moved into Authentik's group", "group", s.Switcher.Name, "moved", moved, "waiting", waiting)
		}
		return
	}
	if ctx.Err() == nil {
		s.Log.Warn("couldn't move the switcher marks into Authentik's group; trying again on the next start", "group", s.Switcher.Name)
	}
}

// moveSwitcherMarks makes one pass. err is set when nobody could be added
// because the group couldn't be reached, so the pass is worth trying again.
func (s *Service) moveSwitcherMarks(ctx context.Context) (moved, waiting int, err error) {
	rows, err := s.Store.SwitcherMarksToMove(ctx)
	if err != nil {
		return 0, 0, dbErr("switcher marks", err)
	}
	var failed int
	for _, r := range rows {
		switch {
		case r.Status == "deleted" || r.Kind == KindService:
			// Nobody to add.
		case r.Subject == nil:
			waiting++
			continue
		default:
			if err := s.Switcher.set(ctx, *r.Subject, true); err != nil {
				failed++
				continue
			}
			moved++
		}
		if err := s.Store.DeleteSwitcherMarkToMove(ctx, r.UserID); err != nil {
			return moved, waiting, dbErr("switcher mark moved", err)
		}
	}
	if failed > 0 && moved == 0 {
		return 0, waiting + failed, errGroupUnavailable
	}
	return moved, waiting + failed, nil
}
