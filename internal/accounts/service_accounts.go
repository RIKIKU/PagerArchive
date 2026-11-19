package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Service accounts (R62a, ADR 0048): read-only accounts for tools, not
// people. Each is an Authentik service-account user with an app password,
// which the tool trades for short-lived access tokens (client credentials).
// They're made, listed, revoked and re-keyed only from the server's shell
// (pagerarchive service-account), as receiving clients are (R59).

// KindService is a service account's users.kind; a person's is "person".
const KindService = "service"

// ServiceAccountLifetime is how long a service account's credential works
// (R62a): rotating it starts a new one.
const ServiceAccountLifetime = 90 * 24 * time.Hour

// IsServiceAccount reports whether u is a service account.
func IsServiceAccount(u db.User) bool { return u.Kind == KindService }

var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,39}$`)

// ValidServiceAccountName checks a service account's name: 3 to 40 lower-case
// letters, digits and hyphens, starting with a letter or digit. It's also the
// Authentik username, so it can't look like an address (people's usernames
// are their addresses) or one of Authentik's own ("ak-…").
func ValidServiceAccountName(name string) bool {
	return serviceNameRe.MatchString(name) && !strings.HasPrefix(name, "ak-")
}

// ServiceDirectory is what service accounts need of the identity provider.
type ServiceDirectory interface {
	// CreateServiceAccount makes a service-account user named username,
	// allowed to sign in only with client credentials, whose app password
	// expires at expires. It returns the user's subject and the password.
	CreateServiceAccount(ctx context.Context, username string, expires time.Time) (subject, secret string, err error)
}

func (s *Service) serviceDir() (ServiceDirectory, error) {
	d, ok := s.Dir.(ServiceDirectory)
	if !ok {
		return nil, errors.New("the identity provider can't make service accounts")
	}
	return d, nil
}

// ServiceAccountCredential is what a tool signs in with: its name and its
// secret, shown once, never stored by the app or logged.
type ServiceAccountCredential struct {
	Name      string
	Secret    string
	ExpiresAt time.Time
}

// ownerOf finds the owning admin by user ID or email address: an active
// admin, not a service account.
func (s *Service) ownerOf(ctx context.Context, q *db.Queries, owner string) (db.User, error) {
	var u db.User
	var err error
	if id, perr := strconv.ParseInt(owner, 10, 64); perr == nil {
		u, err = q.UserByID(ctx, id)
	} else if e, ok := ParseEmail(owner); ok {
		u, err = q.UserByEmailKey(ctx, &e.Key)
	} else {
		return db.User{}, fmt.Errorf("the owner %q is neither a user ID nor an email address", owner)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, errors.New("no user is the owner given")
	}
	if err != nil {
		return db.User{}, dbErr("owner", err)
	}
	if u.Kind != "person" || u.Role != RoleAdmin || u.Status != "active" {
		return db.User{}, errors.New("the owner must be an active admin")
	}
	return u, nil
}

// CreateServiceAccount makes a service account named name, owned by the
// admin owner (an ID or email address), and returns its credential.
func (s *Service) CreateServiceAccount(ctx context.Context, name, owner string) (db.User, ServiceAccountCredential, error) {
	if !ValidServiceAccountName(name) {
		return db.User{}, ServiceAccountCredential{}, fmt.Errorf("a service account's name is 3 to 40 lower-case letters, digits and hyphens, starting with a letter or digit, and not ak-…")
	}
	dir, err := s.serviceDir()
	if err != nil {
		return db.User{}, ServiceAccountCredential{}, err
	}
	o, err := s.ownerOf(ctx, s.Store.Queries, owner)
	if err != nil {
		return db.User{}, ServiceAccountCredential{}, err
	}
	if _, err := s.Store.ServiceAccountByName(ctx, name); err == nil {
		return db.User{}, ServiceAccountCredential{}, fmt.Errorf("a service account is already called %s", name)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, ServiceAccountCredential{}, dbErr("service account", err)
	}
	expires := s.now().Add(ServiceAccountLifetime).Truncate(time.Second)
	subject, secret, err := dir.CreateServiceAccount(ctx, name, expires)
	if err != nil {
		return db.User{}, ServiceAccountCredential{}, fmt.Errorf("authentik: %w", err)
	}
	u, err := s.Store.CreateServiceAccount(ctx, db.CreateServiceAccountParams{Subject: &subject, Name: name, Owner: o.ID, ExpiresAt: &expires})
	if err != nil {
		if derr := s.Dir.DeleteUser(ctx, subject); derr != nil {
			s.Log.Error("authentik: delete user", "error", derr.Error())
		}
		if isUniqueViolation(err) {
			return db.User{}, ServiceAccountCredential{}, fmt.Errorf("a service account is already called %s", name)
		}
		return db.User{}, ServiceAccountCredential{}, dbErr("create service account", err)
	}
	s.Log.Info("service account created", "user", u.ID, "service_account", name, "owner", o.ID)
	return u, ServiceAccountCredential{Name: name, Secret: secret, ExpiresAt: expires}, nil
}

// ListServiceAccounts lists every service account, revoked ones too.
func (s *Service) ListServiceAccounts(ctx context.Context) ([]db.ListServiceAccountsRow, error) {
	rows, err := s.Store.ListServiceAccounts(ctx)
	if err != nil {
		return nil, dbErr("service accounts", err)
	}
	return rows, nil
}

// liveServiceAccount is the service account called name that isn't revoked.
func (s *Service) liveServiceAccount(ctx context.Context, name string) (db.User, error) {
	u, err := s.Store.ServiceAccountByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, fmt.Errorf("no service account is called %s (or it was revoked)", name)
	}
	if err != nil {
		return db.User{}, dbErr("service account", err)
	}
	return u, nil
}

// RevokeServiceAccount ends a service account at once: the app refuses its
// tokens from now on, and its Authentik user is deleted, so it can't get
// more.
func (s *Service) RevokeServiceAccount(ctx context.Context, name string) error {
	u, err := s.liveServiceAccount(ctx, name)
	if err != nil {
		return err
	}
	if _, err := s.Store.RevokeServiceAccount(ctx, u.ID); err != nil {
		return dbErr("revoke service account", err)
	}
	s.closeLive([]int64{u.ID})
	s.Log.Info("service account revoked", "user", u.ID, "service_account", name)
	if u.Subject != nil {
		if err := s.Dir.DeleteUser(ctx, *u.Subject); err != nil {
			return fmt.Errorf("revoked in PagerArchive, which refuses it from now on, but its Authentik user couldn't be deleted (delete %s in Authentik): %w", name, err)
		}
	}
	return nil
}

// RotateServiceAccount gives an active service account a new credential and
// ends the old one at once: its Authentik user is replaced, so tokens made
// with the old credential name a subject the app no longer knows.
func (s *Service) RotateServiceAccount(ctx context.Context, name string) (ServiceAccountCredential, error) {
	u, err := s.liveServiceAccount(ctx, name)
	if err != nil {
		return ServiceAccountCredential{}, err
	}
	if u.Status != "active" {
		return ServiceAccountCredential{}, fmt.Errorf("%s was deactivated by an admin: revoke it and create a new one", name)
	}
	dir, err := s.serviceDir()
	if err != nil {
		return ServiceAccountCredential{}, err
	}
	if u.Subject != nil {
		if err := s.Dir.DeleteUser(ctx, *u.Subject); err != nil {
			return ServiceAccountCredential{}, fmt.Errorf("authentik: the old credential couldn't be ended, so nothing changed: %w", err)
		}
	}
	expires := s.now().Add(ServiceAccountLifetime).Truncate(time.Second)
	subject, secret, err := dir.CreateServiceAccount(ctx, name, expires)
	if err != nil {
		return ServiceAccountCredential{}, fmt.Errorf("authentik: the old credential has ended, but a new one couldn't be made; run rotate again: %w", err)
	}
	if _, err := s.Store.RekeyServiceAccount(ctx, db.RekeyServiceAccountParams{ID: u.ID, Subject: &subject, ExpiresAt: &expires}); err != nil {
		if derr := s.Dir.DeleteUser(ctx, subject); derr != nil {
			s.Log.Error("authentik: delete user", "error", derr.Error())
		}
		return ServiceAccountCredential{}, dbErr("rotate service account", err)
	}
	s.closeLive([]int64{u.ID})
	s.Log.Info("service account rotated", "user", u.ID, "service_account", name)
	return ServiceAccountCredential{Name: name, Secret: secret, ExpiresAt: expires}, nil
}

// errServiceShellOnly refuses changing a service account in the browser:
// only deactivating it is allowed there (R62a).
var errServiceShellOnly = &Error{Status: http.StatusConflict, Code: "service_account_shell_only",
	Msg: "That's a service account: it's created, revoked and re-keyed only on the server (pagerarchive service-account). In Administration you can only deactivate it."}
