package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// accountsError answers a failed account operation: a refusal with its
// message, anything else as a server error.
func (s *Server) accountsError(w http.ResponseWriter, op string, err error) {
	var refusal *accounts.Error
	if errors.As(err, &refusal) {
		writeJSON(w, refusal.Status, Problem{Error: refusal.Msg, Code: refusal.Code})
		return
	}
	s.Log.Error(op, "error", err.Error())
	problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}

// Me is the signed-in user.
type Me struct {
	ID        int64   `json:"id"`
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	Role      string  `json:"role"`
	InvitedBy *string `json:"invited_by"` // name, or "deleted user"; null for the first admin
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	me := Me{ID: u.ID, Email: deref(u.Email), Name: u.Name, Role: u.Role}
	if u.InvitedBy != nil {
		inv, err := s.Store.UserByID(r.Context(), *u.InvitedBy)
		if err != nil {
			s.Log.Error("inviter", "error", store.SafeError(err))
		} else {
			d := accounts.Display(inv.Name, inv.Email, inv.Status)
			me.InvitedBy = &d
		}
	}
	writeJSON(w, http.StatusOK, me)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Server) deleteMe(w http.ResponseWriter, r *http.Request) {
	// Their integrations go with the account; their Home Assistant
	// entities are removed afterwards, best effort.
	cleanup := s.Integrations.DiscoveryCleanup(r.Context(), currentUser(r).ID)
	if err := s.Accounts.DeleteAccount(r.Context(), currentUser(r)); err != nil {
		s.accountsError(w, "delete account", err)
		return
	}
	go cleanup()
	s.setCookie(w, sessionCookie, "", "/", -1)
	w.WriteHeader(http.StatusNoContent)
}

// Invitation is an invitation as its inviter (or an admin) sees it.
type Invitation struct {
	ID        int64      `json:"id"`
	Email     string     `json:"email"`
	Status    string     `json:"status"` // pending, expired, used or cancelled
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	InvitedBy string     `json:"invited_by"` // admins' list only
	Reinvite  bool       `json:"reinvite"`   // an admin's re-invitation of a deactivated user
}

func (s *Server) invitationStatus(status string, expires time.Time) string {
	if status == "pending" && !expires.After(s.Now()) {
		return "expired"
	}
	return status
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request, mine bool) {
	var inviter *int64
	if mine {
		id := currentUser(r).ID
		inviter = &id
	}
	rows, err := s.Store.ListInvitations(r.Context(), inviter)
	if err != nil {
		s.Log.Error("invitations", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []Invitation{}
	for _, i := range rows {
		inv := Invitation{ID: i.ID, Email: deref(i.Email), Status: s.invitationStatus(i.Status, i.ExpiresAt),
			CreatedAt: i.CreatedAt.UTC(), ExpiresAt: i.ExpiresAt.UTC(), UsedAt: i.UsedAt, Reinvite: i.Reactivates != nil}
		if !mine {
			inv.InvitedBy = "first-admin set-up"
			if i.InvitedBy != nil {
				status := "active"
				if i.InviterStatus != nil {
					status = *i.InviterStatus
				}
				inv.InvitedBy = accounts.Display(i.InviterName, i.InviterEmail, status)
			}
		}
		out = append(out, inv)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) myInvitations(w http.ResponseWriter, r *http.Request) {
	s.listInvitations(w, r, true)
}

func (s *Server) allInvitations(w http.ResponseWriter, r *http.Request) {
	s.listInvitations(w, r, false)
}

// InviteRequest is the body of POST /api/v1/invitations.
type InviteRequest struct {
	Email string `json:"email"`
}

func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	var req InviteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	inv, err := s.Accounts.Invite(r.Context(), currentUser(r), req.Email)
	if err != nil {
		s.accountsError(w, "invite", err)
		return
	}
	s.Log.Info("invitation sent", "by", currentUser(r).ID, "invitation", inv.ID)
	writeJSON(w, http.StatusCreated, Invitation{ID: inv.ID, Email: deref(inv.Email), Status: "pending",
		CreatedAt: inv.CreatedAt.UTC(), ExpiresAt: inv.ExpiresAt.UTC(), Reinvite: inv.Reactivates != nil})
}

func (s *Server) cancelInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Accounts.CancelInvitation(r.Context(), currentUser(r), id); err != nil {
		s.accountsError(w, "cancel invitation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UserRow is a user in the admins' list.
type UserRow struct {
	ID            int64      `json:"id"`
	Email         string     `json:"email"`
	Name          *string    `json:"name"`
	Role          string     `json:"role"`
	Status        string     `json:"status"` // active or deactivated
	CreatedAt     time.Time  `json:"created_at"`
	DeactivatedAt *time.Time `json:"deactivated_at"`
	InvitedByID   *int64     `json:"invited_by_id"`
	InvitedBy     *string    `json:"invited_by"` // null for the first admin
	// R44b: their integrations, and how many are paused for failing.
	Integrations        int64 `json:"integrations"`
	IntegrationsFailing int64 `json:"integrations_failing"`
	// R42c: null unless trusted. Admins only.
	Trusted *TrustedMark `json:"trusted"`
}

// TrustedMark is who marked a user trusted, and when (R42c).
type TrustedMark struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	out, err := s.userRows(r.Context())
	if err != nil {
		s.Log.Error("users", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// userRows is every user who isn't deleted, as admins see them.
func (s *Server) userRows(ctx context.Context) ([]UserRow, error) {
	rows, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := []UserRow{}
	for _, u := range rows {
		row := UserRow{ID: u.ID, Email: deref(u.Email), Name: u.Name, Role: u.Role, Status: u.Status,
			CreatedAt: u.CreatedAt.UTC(), DeactivatedAt: u.DeactivatedAt, InvitedByID: u.InvitedBy,
			Integrations: u.Integrations, IntegrationsFailing: u.IntegrationsFailing}
		if u.InvitedBy != nil {
			status := "active"
			if u.InviterStatus != nil {
				status = *u.InviterStatus
			}
			d := accounts.Display(u.InviterName, u.InviterEmail, status)
			row.InvitedBy = &d
		}
		if u.TrustedAt != nil {
			status := "active"
			if u.TrustedByStatus != nil {
				status = *u.TrustedByStatus
			}
			row.Trusted = &TrustedMark{By: accounts.Display(u.TrustedByName, u.TrustedByEmail, status), At: u.TrustedAt.UTC()}
		}
		out = append(out, row)
	}
	return out, nil
}

// DeactivateRequest is the body of POST /api/v1/admin/users/{id}/deactivate,
// which deactivates one user. An invite tree goes through its preview
// (R42b): see deactivationPreview and deactivateTree.
type DeactivateRequest struct{}

// DeactivateResult says how many users were deactivated.
type DeactivateResult struct {
	Deactivated int `json:"deactivated"`
}

func (s *Server) deactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req DeactivateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	n, err := s.Accounts.Deactivate(r.Context(), currentUser(r), id)
	if err != nil {
		s.accountsError(w, "deactivate", err)
		return
	}
	writeJSON(w, http.StatusOK, DeactivateResult{Deactivated: n})
}

func (s *Server) reactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Accounts.Reactivate(r.Context(), currentUser(r), id); err != nil {
		s.accountsError(w, "reactivate", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RoleRequest is the body of PUT /api/v1/admin/users/{id}/role.
type RoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req RoleRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Accounts.SetRole(r.Context(), currentUser(r), id, req.Role); err != nil {
		s.accountsError(w, "set role", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
