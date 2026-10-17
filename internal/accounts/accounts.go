// Package accounts is who may use PagerArchive: invitations, sign-in,
// sessions, roles, deactivation and deletion (SPEC.md R40-R44, ADR 0004).
//
// Authentik holds credentials and sends the emails; this package holds the
// roles, the invite tree and the sessions, and is the source of truth for
// whether a user is active. Authentik is reached through Directory.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

const (
	// InvitationLifetime is how long an invitation works (R43a).
	InvitationLifetime = 7 * 24 * time.Hour
	// FirstAdminLifetime is how long the first admin's set-up link works.
	FirstAdminLifetime = time.Hour
	// SessionLifetime is how long a sign-in lasts without use; each use
	// renews it (owner, 2026-10-13).
	SessionLifetime = 30 * 24 * time.Hour
	// sessionRenewAfter limits session writes to one an hour per browser.
	sessionRenewAfter = time.Hour
)

// Roles (R44).
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// Directory is the identity provider: Authentik in production.
type Directory interface {
	// CreateInvitation creates a single-use enrolment invitation that only
	// enrols email (username key) and carries the app's invitation ID.
	CreateInvitation(ctx context.Context, inv DirectoryInvitation) (id string, err error)
	// SendInvitation emails the invitation's link to the address.
	SendInvitation(ctx context.Context, id, address string) error
	// DeleteInvitation makes an invitation unusable. A missing one is fine.
	DeleteInvitation(ctx context.Context, id string) error
	// EnrolmentURL is the link that starts enrolment with an invitation.
	EnrolmentURL(id string) string
	// SetActive enables or disables a user. Disabling ends their sessions.
	SetActive(ctx context.Context, subject string, active bool) error
	// DeleteUser deletes a user. A missing one is fine.
	DeleteUser(ctx context.Context, subject string) error
	// UserByUsername finds a user's subject by username.
	UserByUsername(ctx context.Context, username string) (subject string, found bool, err error)
}

// DirectoryInvitation is what the identity provider needs to know.
type DirectoryInvitation struct {
	AppID   int64
	Address string // where the code is sent
	Key     string // the username
	Expires time.Time
}

// Error is a refusal the user can act on. Msg is shown to them.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, code, format string, args ...any) error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ErrNoSession means the cookie names no live session of an active user.
var ErrNoSession = errors.New("not signed in")

// Service is the accounts logic.
type Service struct {
	Store          *store.Store
	Dir            Directory
	AllowedDomains []string // lower case, matched exactly (R41)
	InitialAdmin   string   // INITIAL_ADMIN_EMAIL, may be empty (R44)
	Now            func() time.Time
	Log            *slog.Logger
	// SignedOut is told which users lost their sessions, so their live
	// connections can be closed (R43b).
	SignedOut func(userIDs []int64)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) signedOut(ids []int64) {
	if s.SignedOut != nil && len(ids) > 0 {
		s.SignedOut(ids)
	}
}

func dbErr(op string, err error) error {
	return fmt.Errorf("%s: %s", op, store.SafeError(err))
}

func (s *Service) allowed(domain string) bool { return slices.Contains(s.AllowedDomains, domain) }

// Display names a user for others: their name and address, or "deleted
// user" (R43c).
func Display(name, email *string, status string) string {
	switch {
	case status == "deleted" || email == nil:
		return "deleted user"
	case name != nil && *name != "":
		return *name + " (" + *email + ")"
	default:
		return *email
	}
}

// Invite creates an invitation from inviter to address (R41, R43, R43a) and
// has Authentik email it.
func (s *Service) Invite(ctx context.Context, inviter db.User, address string) (db.Invitation, error) {
	e, ok := ParseEmail(address)
	if !ok {
		return db.Invitation{}, refuse(http.StatusUnprocessableEntity, "invalid_email", "Enter one email address, like name@%s.", firstOr(s.AllowedDomains, "example.com"))
	}
	if !s.allowed(e.Domain) {
		return db.Invitation{}, refuse(http.StatusUnprocessableEntity, "domain_not_allowed",
			"Invitations can only be sent to addresses at %s.", strings.Join(s.AllowedDomains, " or "))
	}
	var inv db.Invitation
	var stale []*string
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		var reactivates *int64
		existing, err := q.UserByEmailKey(ctx, &e.Key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return dbErr("user by email", err)
		case existing.Status == "active":
			return refuse(http.StatusConflict, "already_member", "%s already has an account.", e.Address)
		case existing.Status == "deactivated" && inviter.Role != RoleAdmin:
			return refuse(http.StatusForbidden, "deactivated",
				"%s was deactivated, so only an admin can invite them again.", e.Address)
		default:
			reactivates = &existing.ID
		}
		pending, err := q.PendingInvitationTo(ctx, &e.Key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return dbErr("pending invitation", err)
		default:
			by := "the first admin set-up"
			if pending.InvitedBy != nil {
				by = Display(pending.InviterName, pending.InviterEmail, "active")
			}
			return refuse(http.StatusConflict, "already_invited", "%s already has an invitation from %s.", e.Address, by)
		}
		// Expired invitations to this address are finished with.
		if stale, err = q.CancelPendingInvitationsTo(ctx, &e.Key); err != nil {
			return dbErr("cancel expired invitations", err)
		}
		if err := s.clearStaleDirectoryUser(ctx, q, e.Key, reactivates); err != nil {
			return err
		}
		inv, err = q.CreateInvitation(ctx, db.CreateInvitationParams{
			Email: &e.Address, EmailKey: &e.Key, InvitedBy: &inviter.ID, Reactivates: reactivates,
			ExpiresAt: s.now().Add(InvitationLifetime),
		})
		if err != nil {
			if isUniqueViolation(err) { // another invitation to this address, made just now
				return refuse(http.StatusConflict, "already_invited", "%s already has an invitation.", e.Address)
			}
			return dbErr("create invitation", err)
		}
		return s.createInDirectory(ctx, q, &inv, e, true)
	})
	s.deleteDirectoryInvitations(ctx, stale)
	return inv, err
}

// createInDirectory creates the Authentik invitation for inv and, if send,
// emails it. Called inside the transaction that created inv, so a failure
// leaves no invitation behind.
func (s *Service) createInDirectory(ctx context.Context, q *db.Queries, inv *db.Invitation, e Email, send bool) error {
	id, err := s.Dir.CreateInvitation(ctx, DirectoryInvitation{AppID: inv.ID, Address: e.Address, Key: e.Key, Expires: inv.ExpiresAt})
	if err != nil {
		return s.unavailable("create invitation", err)
	}
	inv.AuthentikID = &id
	if err := q.SetInvitationAuthentikID(ctx, db.SetInvitationAuthentikIDParams{ID: inv.ID, AuthentikID: &id}); err != nil {
		s.deleteDirectoryInvitations(ctx, []*string{&id})
		return dbErr("save invitation", err)
	}
	if send {
		if err := s.Dir.SendInvitation(ctx, id, e.Address); err != nil {
			s.deleteDirectoryInvitations(ctx, []*string{&id})
			return s.unavailable("send invitation", err)
		}
	}
	return nil
}

// clearStaleDirectoryUser removes an Authentik user with this username that
// no active account owns: left by an enrolment abandoned before its code
// was entered, or (for an admin's re-invitation) the deactivated user's old
// Authentik user, which the new enrolment replaces (owner, 2026-10-13).
func (s *Service) clearStaleDirectoryUser(ctx context.Context, q *db.Queries, key string, reactivates *int64) error {
	sub, found, err := s.Dir.UserByUsername(ctx, key)
	if err != nil {
		return s.unavailable("look up user", err)
	}
	if !found {
		return nil
	}
	owner, err := q.UserBySubject(ctx, &sub)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return dbErr("user by subject", err)
	case reactivates != nil && owner.ID == *reactivates:
		if err := q.ClearUserSubject(ctx, owner.ID); err != nil {
			return dbErr("clear subject", err)
		}
	default:
		return refuse(http.StatusConflict, "already_member", "That address already has an account.")
	}
	if err := s.Dir.DeleteUser(ctx, sub); err != nil {
		return s.unavailable("delete stale user", err)
	}
	return nil
}

func (s *Service) unavailable(op string, err error) error {
	s.Log.Error("authentik", "op", op, "error", err.Error())
	return refuse(http.StatusServiceUnavailable, "directory_unavailable",
		"The sign-in service (Authentik) couldn't be reached, so nothing was changed. Try again shortly.")
}

func (s *Service) deleteDirectoryInvitations(ctx context.Context, ids []*string) {
	for _, id := range ids {
		if id == nil {
			continue
		}
		if err := s.Dir.DeleteInvitation(ctx, *id); err != nil {
			s.Log.Error("authentik: cancel invitation", "invitation", *id, "error", err.Error())
		}
	}
}

// FirstAdminAvailable reports whether the first admin may still enrol
// without an invitation (R44).
func (s *Service) FirstAdminAvailable(ctx context.Context) (bool, error) {
	if s.InitialAdmin == "" {
		return false, nil
	}
	n, err := s.Store.CountActiveAdmins(ctx)
	if err != nil {
		return false, dbErr("count admins", err)
	}
	return n == 0, nil
}

// FirstAdminEnrolment starts the first admin's enrolment: while no admin
// exists, INITIAL_ADMIN_EMAIL may enrol without an invitation from anyone,
// still verifying the emailed code, and is exempt from the allowed domains
// (R44). It returns the enrolment link. Each call replaces the last link.
func (s *Service) FirstAdminEnrolment(ctx context.Context) (string, error) {
	e, ok := ParseEmail(s.InitialAdmin)
	if !ok {
		return "", refuse(http.StatusNotFound, "no_first_admin", "First-admin set-up isn't available.")
	}
	var url string
	var stale []*string
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.LockActiveAdmins(ctx)
		if err != nil {
			return dbErr("lock admins", err)
		}
		if len(admins) > 0 {
			return refuse(http.StatusConflict, "admin_exists", "PagerArchive already has an admin. Ask an existing user for an invitation.")
		}
		if stale, err = q.CancelFirstAdminInvitations(ctx); err != nil {
			return dbErr("cancel set-up links", err)
		}
		more, err := q.CancelPendingInvitationsTo(ctx, &e.Key)
		if err != nil {
			return dbErr("cancel invitations", err)
		}
		stale = append(stale, more...)
		if err := s.clearStaleDirectoryUser(ctx, q, e.Key, nil); err != nil {
			return err
		}
		inv, err := q.CreateInvitation(ctx, db.CreateInvitationParams{
			Email: &e.Address, EmailKey: &e.Key, FirstAdmin: true, ExpiresAt: s.now().Add(FirstAdminLifetime),
		})
		if err != nil {
			return dbErr("create set-up invitation", err)
		}
		if err := s.createInDirectory(ctx, q, &inv, e, false); err != nil {
			return err
		}
		url = s.Dir.EnrolmentURL(*inv.AuthentikID)
		return nil
	})
	s.deleteDirectoryInvitations(ctx, stale)
	return url, err
}

// Claims are what the identity provider says about a signed-in user.
type Claims struct {
	Subject    string
	Email      string
	Name       string
	Invitation string // the app's invitation ID, set at enrolment
}

// SignIn returns the account for a successful OIDC sign-in, creating it on
// first sign-in from the invitation it enrolled with.
func (s *Service) SignIn(ctx context.Context, c Claims) (db.User, error) {
	u, err := s.Store.UserBySubject(ctx, &c.Subject)
	switch {
	case err == nil:
		if u.Status != "active" {
			return db.User{}, refuse(http.StatusForbidden, "deactivated", "Your account has been deactivated. Ask an admin if you think this is a mistake.")
		}
		if c.Name != "" && (u.Name == nil || *u.Name != c.Name) {
			if err := s.Store.SetUserName(ctx, db.SetUserNameParams{ID: u.ID, Name: &c.Name}); err != nil {
				return db.User{}, dbErr("set name", err)
			}
			u.Name = &c.Name
		}
		return u, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return db.User{}, dbErr("user by subject", err)
	}

	invID, err := strconv.ParseInt(c.Invitation, 10, 64)
	if err != nil {
		return db.User{}, refuse(http.StatusForbidden, "not_invited", "This sign-in isn't linked to a PagerArchive invitation.")
	}
	e, ok := ParseEmail(c.Email)
	if !ok {
		return db.User{}, refuse(http.StatusForbidden, "not_invited", "This sign-in has no usable email address.")
	}
	var cancelled bool
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		inv, err := q.InvitationByID(ctx, invID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (inv.EmailKey == nil || *inv.EmailKey != e.Key)) {
			return refuse(http.StatusForbidden, "not_invited", "This sign-in doesn't match its invitation.")
		}
		if err != nil {
			return dbErr("invitation", err)
		}
		if inv.Status == "used" {
			return refuse(http.StatusConflict, "invitation_used", "That invitation has already been used.")
		}
		if inv.Status != "pending" {
			cancelled = true
			return refuse(http.StatusForbidden, "invitation_cancelled", "Your invitation was cancelled before you finished enrolling.")
		}
		// Authentik checks expiry when enrolment starts; the app checks it again
		// here, so its own record decides (R43a).
		if !inv.ExpiresAt.After(s.now()) {
			cancelled = true
			return refuse(http.StatusForbidden, "invitation_expired", "Your invitation expired before you finished enrolling. Ask for a new one.")
		}
		name := &c.Name
		if c.Name == "" {
			name = nil
		}
		switch {
		case inv.FirstAdmin:
			admins, err := q.LockActiveAdmins(ctx)
			if err != nil {
				return dbErr("lock admins", err)
			}
			if len(admins) > 0 {
				cancelled = true
				return refuse(http.StatusForbidden, "admin_exists", "PagerArchive already has an admin. Ask an existing user for an invitation.")
			}
			u, err = q.CreateUser(ctx, db.CreateUserParams{Subject: &c.Subject, Email: &e.Address, EmailKey: &e.Key, Name: name, Role: RoleAdmin})
		case inv.Reactivates != nil:
			if err = q.RelinkUser(ctx, db.RelinkUserParams{ID: *inv.Reactivates, Subject: &c.Subject, Name: name, InvitedBy: inv.InvitedBy}); err == nil {
				u, err = q.UserByID(ctx, *inv.Reactivates)
			}
		default:
			u, err = q.CreateUser(ctx, db.CreateUserParams{Subject: &c.Subject, Email: &e.Address, EmailKey: &e.Key, Name: name, Role: RoleUser, InvitedBy: inv.InvitedBy})
		}
		if err != nil {
			return dbErr("create user", err)
		}
		n, err := q.UseInvitation(ctx, db.UseInvitationParams{ID: inv.ID, UsedBy: &u.ID})
		if err != nil {
			return dbErr("use invitation", err)
		}
		if n != 1 {
			return refuse(http.StatusConflict, "invitation_used", "That invitation has already been used.")
		}
		return nil
	})
	if cancelled {
		// The Authentik user enrolled with an invitation that no longer
		// counts: remove it, so the address can be invited again.
		if err := s.Dir.DeleteUser(ctx, c.Subject); err != nil {
			s.Log.Error("authentik: delete user from a cancelled invitation", "error", err.Error())
		}
	}
	if err != nil {
		return db.User{}, err
	}
	s.Log.Info("account created", "user", u.ID, "role", u.Role, "invited_by", u.InvitedBy)
	return u, nil
}

// NewSession signs a user in and returns the cookie's token.
func (s *Service) NewSession(ctx context.Context, userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := s.Store.CreateSession(ctx, db.CreateSessionParams{
		TokenHash: store.HashKey(token), UserID: userID, ExpiresAt: s.now().Add(SessionLifetime),
	}); err != nil {
		return "", dbErr("create session", err)
	}
	return token, nil
}

// Session returns the active user a session token belongs to, renewing
// the session at most once an hour.
func (s *Service) Session(ctx context.Context, token string) (db.User, error) {
	if token == "" {
		return db.User{}, ErrNoSession
	}
	hash := store.HashKey(token)
	r, err := s.Store.SessionUser(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, ErrNoSession
	}
	if err != nil {
		return db.User{}, dbErr("session", err)
	}
	if now := s.now(); now.Sub(r.SessionUsedAt) > sessionRenewAfter {
		if err := s.Store.TouchSession(ctx, db.TouchSessionParams{TokenHash: hash, ExpiresAt: now.Add(SessionLifetime)}); err != nil {
			s.Log.Error("renew session", "error", store.SafeError(err))
		}
	}
	return db.User{ID: r.ID, Subject: r.Subject, Email: r.Email, EmailKey: r.EmailKey, Name: r.Name, Role: r.Role,
		Status: r.Status, InvitedBy: r.InvitedBy, CreatedAt: r.CreatedAt}, nil
}

// EndSession signs one browser out.
func (s *Service) EndSession(ctx context.Context, token string) error {
	if err := s.Store.DeleteSession(ctx, store.HashKey(token)); err != nil {
		return dbErr("end session", err)
	}
	return nil
}

// lastAdmin reports whether removing the users in ids would leave no active
// admin (R44). admins must have been locked in this transaction.
func lastAdmin(admins []int64, ids []int64) bool {
	for _, a := range admins {
		if !slices.Contains(ids, a) {
			return false
		}
	}
	return len(admins) > 0
}

// Deactivate deactivates one user (R43b): at once in the app (sessions
// ended, live connections closed, unused invitations cancelled), then in
// Authentik. A tree goes through Preview and DeactivateTree (R42b).
func (s *Service) Deactivate(ctx context.Context, actor db.User, targetID int64) (int, error) {
	var target db.User
	var changed []db.DeactivateUsersRow
	var cancelled []*string
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.LockActiveAdmins(ctx)
		if err != nil {
			return dbErr("lock admins", err)
		}
		target, err = q.UserByID(ctx, targetID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && target.Status == "deleted") {
			return refuse(http.StatusNotFound, "no_user", "No such user.")
		}
		if err != nil {
			return dbErr("user", err)
		}
		ids := []int64{target.ID}
		if lastAdmin(admins, ids) {
			return refuse(http.StatusConflict, "last_admin", "That would leave PagerArchive with no active admin. Make someone else an admin first.")
		}
		if target.ID == actor.ID {
			return refuse(http.StatusConflict, "self", "You can't deactivate yourself.")
		}
		changed, cancelled, err = deactivateIn(ctx, q, ids)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.Log.Info("deactivated", "by", actor.ID, "user", targetID, "users", len(changed))
	// Authentik is synced even if they were already deactivated, so
	// repeating a failed request repairs it.
	if failed := s.afterDeactivate(ctx, []int64{target.ID}, []*string{target.Subject}, cancelled); failed > 0 {
		return len(changed), refuse(http.StatusBadGateway, "directory_partial",
			"Deactivated in PagerArchive (they can't use it any more), but Authentik couldn't be updated. Deactivate again to retry.")
	}
	return len(changed), nil
}

// deactivateIn deactivates ids in the app, inside q's transaction: status,
// sessions, unused invitations (R43b), integrations (R51) and waiting alias
// requests (R56). It returns who changed and the cancelled invitations'
// Authentik IDs.
func deactivateIn(ctx context.Context, q *db.Queries, ids []int64) ([]db.DeactivateUsersRow, []*string, error) {
	changed, err := q.DeactivateUsers(ctx, ids)
	if err != nil {
		return nil, nil, dbErr("deactivate", err)
	}
	if err := q.DeleteUserSessions(ctx, ids); err != nil {
		return nil, nil, dbErr("end sessions", err)
	}
	cancelled, err := q.CancelInvitationsBy(ctx, ids)
	if err != nil {
		return nil, nil, dbErr("cancel invitations", err)
	}
	if _, err := q.CancelPendingDeliveriesOfOwners(ctx, ids); err != nil {
		return nil, nil, dbErr("stop integrations", err)
	}
	if _, err := q.WithdrawRequestsOf(ctx, ids); err != nil {
		return nil, nil, dbErr("withdraw alias requests", err)
	}
	return changed, cancelled, nil
}

// afterDeactivate follows a committed deactivation: live connections are
// closed, the users are disabled in Authentik and their cancelled
// invitations deleted there. It returns how many couldn't be disabled.
func (s *Service) afterDeactivate(ctx context.Context, ids []int64, subjects []*string, cancelled []*string) int {
	s.signedOut(ids)
	var failed int
	for i, sub := range subjects {
		if sub == nil {
			continue
		}
		if err := s.Dir.SetActive(ctx, *sub, false); err != nil {
			s.Log.Error("authentik: disable user", "user", ids[i], "error", err.Error())
			failed++
		}
	}
	s.deleteDirectoryInvitations(ctx, cancelled)
	return failed
}

// Reactivate reactivates one user, not their invite tree (R43b).
func (s *Service) Reactivate(ctx context.Context, actor db.User, targetID int64) error {
	u, err := s.Store.UserByID(ctx, targetID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted") {
		return refuse(http.StatusNotFound, "no_user", "No such user.")
	}
	if err != nil {
		return dbErr("user", err)
	}
	if u.Status != "deactivated" {
		return refuse(http.StatusConflict, "not_deactivated", "That user isn't deactivated.")
	}
	if u.Subject == nil {
		return refuse(http.StatusConflict, "reinvited", "They have an invitation to enrol again; their account is reactivated when they use it.")
	}
	if err := s.Dir.SetActive(ctx, *u.Subject, true); err != nil {
		return s.unavailable("enable user", err)
	}
	if _, err := s.Store.ReactivateUser(ctx, targetID); err != nil {
		return dbErr("reactivate", err)
	}
	s.Log.Info("reactivated", "by", actor.ID, "user", targetID)
	return nil
}

// SetRole promotes or demotes a user (R44). The last active admin stays.
func (s *Service) SetRole(ctx context.Context, actor db.User, targetID int64, role string) error {
	if role != RoleUser && role != RoleAdmin {
		return refuse(http.StatusUnprocessableEntity, "bad_role", `Role must be "user" or "admin".`)
	}
	return s.Store.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.LockActiveAdmins(ctx)
		if err != nil {
			return dbErr("lock admins", err)
		}
		u, err := q.UserByID(ctx, targetID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.Status == "deleted") {
			return refuse(http.StatusNotFound, "no_user", "No such user.")
		}
		if err != nil {
			return dbErr("user", err)
		}
		if u.Status != "active" {
			return refuse(http.StatusConflict, "not_active", "Reactivate them first.")
		}
		if role == RoleUser && lastAdmin(admins, []int64{u.ID}) {
			return refuse(http.StatusConflict, "last_admin", "They're the last admin. Make someone else an admin first.")
		}
		if _, err := q.SetUserRole(ctx, db.SetUserRoleParams{ID: u.ID, Role: role}); err != nil {
			return dbErr("set role", err)
		}
		s.Log.Info("role changed", "by", actor.ID, "user", u.ID, "role", role)
		return nil
	})
}

// DeleteAccount deletes the signed-in user's own account (R43c). People
// they invited keep their accounts, credited to "deleted user"; their
// unused invitations are cancelled. The last active admin can't (R44).
func (s *Service) DeleteAccount(ctx context.Context, u db.User) error {
	var cancelled []*string
	err := s.Store.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.LockActiveAdmins(ctx)
		if err != nil {
			return dbErr("lock admins", err)
		}
		if lastAdmin(admins, []int64{u.ID}) {
			return refuse(http.StatusConflict, "last_admin", "You're the last admin. Make someone else an admin before deleting your account.")
		}
		ids := []int64{u.ID}
		if err := q.DeleteUserSessions(ctx, ids); err != nil {
			return dbErr("end sessions", err)
		}
		if cancelled, err = q.CancelInvitationsBy(ctx, ids); err != nil {
			return dbErr("cancel invitations", err)
		}
		if err := q.ScrubInvitationsTo(ctx, &u.ID); err != nil {
			return dbErr("forget invitation", err)
		}
		// Integrations hold secrets and destinations: they go with the account.
		if err := q.DeleteIntegrationsByOwner(ctx, u.ID); err != nil {
			return dbErr("delete integrations", err)
		}
		// R56: waiting alias requests are withdrawn. The requests stay,
		// credited to "deleted user" (R57), but not their notes (ADR 0007 §5).
		if _, err := q.WithdrawRequestsOf(ctx, ids); err != nil {
			return dbErr("withdraw alias requests", err)
		}
		if err := q.ClearRequestNotesOf(ctx, u.ID); err != nil {
			return dbErr("clear request notes", err)
		}
		if err := q.DeleteUserAccount(ctx, u.ID); err != nil {
			return dbErr("delete account", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.signedOut([]int64{u.ID})
	s.Log.Info("account deleted", "user", u.ID)
	if u.Subject != nil {
		if err := s.Dir.DeleteUser(ctx, *u.Subject); err != nil {
			// The app already refuses them; the Authentik user is left
			// disabled at worst and cleared by the next invitation.
			s.Log.Error("authentik: delete user", "user", u.ID, "error", err.Error())
		}
	}
	s.deleteDirectoryInvitations(ctx, cancelled)
	return nil
}

// CancelInvitation cancels an unused invitation: the inviter's own, or any
// for an admin.
func (s *Service) CancelInvitation(ctx context.Context, actor db.User, id int64) error {
	inv, err := s.Store.InvitationByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.Role != RoleAdmin && (inv.InvitedBy == nil || *inv.InvitedBy != actor.ID)) {
		return refuse(http.StatusNotFound, "no_invitation", "No such invitation.")
	}
	if err != nil {
		return dbErr("invitation", err)
	}
	authentikID, err := s.Store.CancelInvitation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse(http.StatusConflict, "not_pending", "That invitation has already been used or cancelled.")
	}
	if err != nil {
		return dbErr("cancel invitation", err)
	}
	s.deleteDirectoryInvitations(ctx, []*string{authentikID})
	return nil
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// isUniqueViolation reports a unique-constraint failure (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
