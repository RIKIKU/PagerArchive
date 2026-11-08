package accounts

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Resend sends an invitation again (R43d): the inviter's own, or any for an
// admin, while it's waiting or expired. It gets a new link and a new 7 days,
// and the old link stops working. It's still the same invitation, from the
// same inviter, and it follows a new invitation's rules (R41, R43).
func (s *Service) Resend(ctx context.Context, actor db.User, id int64) (db.Invitation, error) {
	var inv db.Invitation
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		var err error
		inv, err = q.LockInvitation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.Role != RoleAdmin && (inv.InvitedBy == nil || *inv.InvitedBy != actor.ID)) {
			return refuse(http.StatusNotFound, "no_invitation", "No such invitation.")
		}
		if err != nil {
			return dbErr("invitation", err)
		}
		if inv.Status != "pending" {
			return refuse(http.StatusConflict, "not_pending", "That invitation has already been used or cancelled, so it can't be resent. Send a new invitation instead.")
		}
		if inv.FirstAdmin || inv.Email == nil {
			return refuse(http.StatusConflict, "first_admin", "The first admin's set-up link can't be resent. Start the set-up again from the sign-in page.")
		}
		e, ok := ParseEmail(*inv.Email)
		if !ok {
			return refuse(http.StatusConflict, "invalid_email", "That invitation's address can't be used. Cancel it and send a new invitation.")
		}
		if !s.allowed(e.Domain) {
			return refuse(http.StatusUnprocessableEntity, "domain_not_allowed",
				"Invitations can only be sent to addresses at %s.", strings.Join(s.AllowedDomains, " or "))
		}
		existing, err := q.UserByEmailKey(ctx, &e.Key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return dbErr("user by email", err)
		case existing.Status == "active":
			return refuse(http.StatusConflict, "already_member", "%s already has an account.", e.Address)
		case existing.Status == "deactivated" && actor.Role != RoleAdmin:
			return refuse(http.StatusForbidden, "deactivated",
				"%s was deactivated, so only an admin can invite them again.", e.Address)
		}

		old := inv.AuthentikID
		expires := s.now().Add(InvitationLifetime)
		link, err := s.Dir.CreateInvitation(ctx, DirectoryInvitation{AppID: inv.ID, Address: e.Address, Key: e.Key, Expires: expires})
		if err != nil {
			return s.unavailable("create invitation", err)
		}
		// From here a failure removes the new link, and the transaction leaves
		// the invitation as it was.
		fail := func(err error) error {
			s.deleteDirectoryInvitations(ctx, []*string{&link})
			return err
		}
		if err := q.ResendInvitation(ctx, db.ResendInvitationParams{ID: inv.ID, AuthentikID: &link, ExpiresAt: expires}); err != nil {
			return fail(dbErr("resend invitation", err))
		}
		if err := s.Dir.SendInvitation(ctx, link, e.Address); err != nil {
			return fail(s.unavailable("send invitation", err))
		}
		if old != nil {
			if err := s.Dir.DeleteInvitation(ctx, *old); err != nil {
				return fail(s.unavailable("replace invitation", err))
			}
		}
		now := s.now()
		inv.AuthentikID, inv.ExpiresAt, inv.ResentAt, inv.LinkLostAt = &link, expires, &now, nil
		return nil
	})
	if err != nil {
		return db.Invitation{}, err
	}
	s.Log.Info("invitation resent", "by", actor.ID, "invitation", inv.ID)
	return inv, nil
}

// CheckInvitations makes the app's waiting invitations agree with Authentik
// (R43a, ADR 0021). One made while invitations were single-use is made
// reusable. One whose link Authentik no longer has, though nobody used or
// cancelled it (a mail provider's link scanner opened it while that used it
// up), is marked as needing a resend, so it isn't shown as waiting. It
// returns how many it marked.
func (s *Service) CheckInvitations(ctx context.Context) (int, error) {
	rows, err := s.Store.InvitationsToCheck(ctx)
	if err != nil {
		return 0, dbErr("invitations to check", err)
	}
	var lost int
	for _, r := range rows {
		found, err := s.Dir.RepairInvitation(ctx, *r.AuthentikID)
		if err != nil {
			return lost, err
		}
		if found {
			continue
		}
		// Enrolment finished in Authentik, which used the link up; the account
		// is made at the first sign-in.
		u, found, err := s.Dir.UserByUsername(ctx, *r.EmailKey)
		if err != nil {
			return lost, err
		}
		if found && u.Active {
			continue
		}
		n, err := s.Store.MarkInvitationLinkLost(ctx, db.MarkInvitationLinkLostParams{ID: r.ID, AuthentikID: r.AuthentikID})
		if err != nil {
			return lost, dbErr("mark invitation's link lost", err)
		}
		if n == 1 {
			lost++
			s.Log.Warn("an invitation's link no longer works in Authentik, though nobody used or cancelled it; its inviter or an admin can resend it", "invitation", r.ID)
		}
	}
	return lost, nil
}

// WatchInvitations runs CheckInvitations on start, beside the API, and then
// every hour. Authentik may not answer for minutes after a start, so a
// failed check is tried again after retry, up to tries times, before it
// waits for the next hour. Its messages never hold an address (R73).
func (s *Service) WatchInvitations(ctx context.Context, retry time.Duration, tries int) {
	for failures := 0; ; {
		wait := time.Hour
		if _, err := s.CheckInvitations(ctx); err != nil && ctx.Err() == nil {
			if failures++; failures < tries {
				wait = retry
			} else {
				s.Log.Warn("couldn't check invitations' links in Authentik; trying again in an hour", "error", err.Error())
				failures = 0
			}
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
