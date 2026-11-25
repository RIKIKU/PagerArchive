package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// UsersGroup is the Authentik group every PagerArchive user is in (R92e):
// the companion app admits only its members. Authentik's blueprint makes it,
// and enrolment adds each new user to it.
const UsersGroup = "pagerarchive-users"

// EndDirectorySession ends this app's browser sessions that came from one
// Authentik session, when Authentik says it ended (R92d: OIDC back-channel
// logout). sid names the Authentik session, as the ID token each session
// signed in with does; with no sid, every browser session of the user ends.
// The user's live connections are closed, so a list open on an ended session
// stops (its other browsers reconnect). An unknown subject, or no matching
// session, ends nothing and is no error.
func (s *Service) EndDirectorySession(ctx context.Context, subject, sid string) (int64, error) {
	u, err := s.Store.UserBySubject(ctx, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, dbErr("user by subject", err)
	}
	rows, err := s.Store.UserSessionIDTokens(ctx, u.ID)
	if err != nil {
		return 0, dbErr("sessions", err)
	}
	var hashes [][]byte
	for _, r := range rows {
		if sid == "" || (r.IDToken != nil && IDTokenSID(*r.IDToken) == sid) {
			hashes = append(hashes, r.TokenHash)
		}
	}
	if len(hashes) == 0 {
		return 0, nil
	}
	n, err := s.Store.DeleteSessionsByHash(ctx, hashes)
	if err != nil {
		return 0, dbErr("end sessions", err)
	}
	s.closeLive([]int64{u.ID})
	return n, nil
}

// IDTokenSID is the sid claim of an ID token this app verified when it
// signed the session in, or "" when it has none. It isn't verified again:
// the token is the app's own record, never one a request sent.
func IDTokenSID(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		SID string `json:"sid"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return ""
	}
	return c.SID
}

// GroupDirectory adds users to an Authentik group (R92e).
type GroupDirectory interface {
	// GroupMembers finds the group by name, returning its ID and the
	// subjects of its members.
	GroupMembers(ctx context.Context, group string) (id string, members map[string]bool, err error)
	// AddToGroup adds the user with this subject to the group.
	AddToGroup(ctx context.Context, groupID, subject string) error
}

// JoinUsersGroup puts everyone with an account who isn't in UsersGroup yet
// into it (R92e): the users who enrolled before enrolment added them. It
// runs at every start and changes nothing once they're in, so it's safe to
// repeat. Authentik can take minutes to answer on a first start, and the
// blueprint makes the group, so it asks again every interval, up to tries
// times; a user it couldn't add is tried again on the next start.
func (s *Service) JoinUsersGroup(ctx context.Context, d GroupDirectory, every time.Duration, tries int) {
	var err error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
		var added, failed int
		if added, failed, err = s.joinUsersGroup(ctx, d); err != nil {
			continue
		}
		if failed > 0 {
			s.Log.Warn("some users couldn't be added to Authentik's "+UsersGroup+" group; trying again on the next start", "added", added, "failed", failed)
		} else {
			s.Log.Info("everyone with an account is in Authentik's "+UsersGroup+" group", "added", added)
		}
		return
	}
	if ctx.Err() == nil {
		s.Log.Warn("couldn't check Authentik's "+UsersGroup+" group; checking again on the next start", "error", err.Error())
	}
}

func (s *Service) joinUsersGroup(ctx context.Context, d GroupDirectory) (added, failed int, err error) {
	people, err := s.Store.DirectoryPeople(ctx)
	if err != nil {
		return 0, 0, dbErr("people", err)
	}
	id, members, err := d.GroupMembers(ctx, UsersGroup)
	if err != nil {
		return 0, 0, err
	}
	for _, sub := range people {
		if members[sub] {
			continue
		}
		if err := d.AddToGroup(ctx, id, sub); err != nil {
			s.Log.Warn("authentik: add a user to the "+UsersGroup+" group", "error", err.Error())
			failed++
			continue
		}
		added++
	}
	return added, failed, nil
}
