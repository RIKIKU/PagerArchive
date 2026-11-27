package stacktest

// The app switcher follows the Authentik group (R77f, A158; ADR 0055),
// against the real Authentik: the blueprint's group and the API's rights
// on it, the admin's mark adding and removing the membership, a change made
// in Authentik showing within 30 seconds, and standalone unchanged.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const switcherGroup = "radioarchive-preview"

// akGroup is a group as the app's API token sees it: its ID and members' IDs.
func (s *stack) akGroup(t *testing.T, name string) (pk string, members []int64) {
	t.Helper()
	q := url.Values{"name": {name}, "include_users": {"false"}}
	code, body := s.akAPI(t, http.MethodGet, "/api/v3/core/groups/?"+q.Encode(), nil)
	var out struct {
		Results []struct {
			PK    string  `json:"pk"`
			Name  string  `json:"name"`
			Users []int64 `json:"users"`
		} `json:"results"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &out) != nil {
		t.Fatalf("group %s: %d %s", name, code, body)
	}
	for _, g := range out.Results {
		if g.Name == name {
			return g.PK, g.Users
		}
	}
	t.Fatalf("the app's token can't see a group %s", name)
	return "", nil
}

// akAPI calls Authentik's API with the app's token.
func (s *stack) akAPI(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	p, q, _ := strings.Cut(path, "?")
	u := s.authentik.JoinPath(p)
	u.RawQuery = q
	req, _ := http.NewRequest(method, u.String(), rd)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func (s *stack) inSwitcherGroup(t *testing.T, pk string) bool {
	t.Helper()
	id, _ := strconv.ParseInt(pk, 10, 64)
	_, members := s.akGroup(t, switcherGroup)
	return slices.Contains(members, id)
}

// akGroupChange adds the user with this Authentik ID to the switcher's group,
// or takes them out, in Authentik itself (its admin pages' action).
func akGroupChange(t *testing.T, pk string, add bool) {
	t.Helper()
	op := "add"
	if !add {
		op = "remove"
	}
	akShell(t, fmt.Sprintf(`from authentik.core.models import Group, User; Group.objects.get(name=%q).users.%s(User.objects.get(pk=%s))`, switcherGroup, op, pk))
}

func (b *browser) showSwitcher() bool {
	b.t.Helper()
	var m struct {
		ShowSwitcher bool `json:"show_switcher"`
	}
	if code, msg := b.api(http.MethodGet, "/api/v1/me", nil, &m); code != http.StatusOK {
		b.t.Fatalf("%s: GET /api/v1/me: %d %s", b.email, code, msg)
	}
	return m.ShowSwitcher
}

// showsWithin waits up to within for b's show_switcher to be want, and
// returns how long it took, or -1.
func (b *browser) showsWithin(want bool, within time.Duration) time.Duration {
	b.t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if b.showSwitcher() == want {
			return time.Since(start)
		}
		time.Sleep(time.Second)
	}
	return -1
}

func (b *browser) markSwitcher(id int64, sees bool) (int, string) {
	b.t.Helper()
	return b.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/switcher", id), map[string]bool{"sees_switcher": sees}, nil)
}

// A158 (R77f, R92e) beside RadioArchive.
func TestA158SwitcherFollowsTheGroup(t *testing.T) {
	s := setup(t, "combined")
	o := s.owner(t)

	// The blueprint's group, and only the rights the API needs on it.
	gpk, _ := s.akGroup(t, switcherGroup)
	if code, body := s.akAPI(t, http.MethodPatch, "/api/v3/core/groups/"+gpk+"/", map[string]string{"name": "renamed-by-the-app"}); code != http.StatusForbidden {
		t.Errorf("the app's token renamed the group: %d %s, want 403", code, body)
	}
	upk, _ := s.akGroup(t, "pagerarchive-users")
	owner := authentikPK(t, "owner@example.org")
	ownerID, _ := strconv.ParseInt(owner, 10, 64)
	if code, body := s.akAPI(t, http.MethodPost, "/api/v3/core/groups/"+upk+"/remove_user/", map[string]int64{"pk": ownerID}); code != http.StatusForbidden {
		t.Errorf("the app's token removed a member of pagerarchive-users: %d %s, want 403", code, body)
	}
	if !s.inUsersGroup(t, "owner@example.org") {
		t.Fatal("the owner left pagerarchive-users")
	}
	t.Log("A158: the group exists; the app's token can't rename it or remove a member of pagerarchive-users (403)")

	// The radio's own service account in the group, no PagerArchive user.
	operator := akShell(t, `from authentik.core.models import Group, User, UserTypes
u, _ = User.objects.get_or_create(username="a158-radio-operator", defaults={"name": "Radio operator", "type": UserTypes.SERVICE_ACCOUNT, "path": "users/service"})
Group.objects.get(name="`+switcherGroup+`").users.add(u)
print(f"PK={u.pk}=PK")`)
	if _, rest, ok := strings.Cut(operator, "PK="); ok {
		operator, _, _ = strings.Cut(rest, "=PK")
	}
	if _, err := strconv.ParseInt(operator, 10, 64); err != nil {
		t.Fatalf("the radio's service account: %q", operator)
	}
	if !s.inSwitcherGroup(t, operator) {
		t.Fatal("the radio's service account isn't in the group")
	}

	m := s.inviteAndEnrol(t, o, "a158m@example.com")
	n := s.inviteAndEnrol(t, o, "a158n@example.com")
	mpk := authentikPK(t, m.email)
	mid := s.userID(t, o, m.email)
	if s.inSwitcherGroup(t, mpk) || m.showSwitcher() {
		t.Fatal("M, just enrolled: in the group, or shown the switcher")
	}

	// The admin's mark adds and removes the membership.
	if code, msg := o.markSwitcher(mid, true); code != http.StatusNoContent {
		t.Fatalf("mark M: %d %s", code, msg)
	}
	if !s.inSwitcherGroup(t, mpk) || !m.showSwitcher() {
		t.Fatalf("marked: in the group %v, shown %v", s.inSwitcherGroup(t, mpk), m.showSwitcher())
	}
	var users []struct {
		ID           int64 `json:"id"`
		SeesSwitcher bool  `json:"sees_switcher"`
	}
	o.api(http.MethodGet, "/api/v1/admin/users", nil, &users)
	for _, u := range users {
		if u.ID == mid && !u.SeesSwitcher {
			t.Error("the admin's list doesn't say M sees the switcher")
		}
	}
	if code, msg := o.markSwitcher(mid, false); code != http.StatusNoContent {
		t.Fatalf("unmark M: %d %s", code, msg)
	}
	if s.inSwitcherGroup(t, mpk) || m.showSwitcher() {
		t.Fatalf("unmarked: in the group %v, shown %v", s.inSwitcherGroup(t, mpk), m.showSwitcher())
	}
	if !s.inSwitcherGroup(t, operator) {
		t.Error("the radio's service account left the group")
	}
	t.Log("A158: the admin's mark put M in the group in Authentik and showed the switcher; clearing it took M out; the radio's service account stayed")

	// A change made in Authentik shows within 35 s, with no new sign-in.
	akGroupChange(t, mpk, true)
	took := m.showsWithin(true, 35*time.Second)
	if took < 0 {
		t.Fatal("added in Authentik: M's show_switcher still false after 35 s")
	}
	t.Logf("A158: added in Authentik: shown %v later", took.Round(time.Second))
	akGroupChange(t, mpk, false)
	if took = m.showsWithin(false, 35*time.Second); took < 0 {
		t.Fatal("taken out in Authentik: M's show_switcher still true after 35 s")
	}
	t.Logf("A158: taken out in Authentik: hidden %v later", took.Round(time.Second))

	// Applying the blueprint again keeps the members.
	akGroupChange(t, mpk, true)
	composeExec(t, "authentik-worker", "ak", "apply_blueprint", "pagerarchive/pagerarchive.yaml")
	if !s.inSwitcherGroup(t, mpk) || !s.inSwitcherGroup(t, operator) {
		t.Errorf("the blueprint applied again: M in the group %v, the radio's service account %v", s.inSwitcherGroup(t, mpk), s.inSwitcherGroup(t, operator))
	}
	t.Log("A158: the blueprint applied again: the group's members unchanged")

	// Everyone on: N, not in the group, sees it.
	if n.showSwitcher() {
		t.Fatal("N sees the switcher before everyone is on")
	}
	if code, msg := o.api(http.MethodPut, "/api/v1/admin/switcher", map[string]bool{"everyone": true}, nil); code != http.StatusOK {
		t.Fatalf("everyone on: %d %s", code, msg)
	}
	defer o.api(http.MethodPut, "/api/v1/admin/switcher", map[string]bool{"everyone": false}, nil)
	if !n.showSwitcher() {
		t.Error("everyone on: N doesn't see the switcher")
	}
	t.Log("A158: everyone on: N, not in the group, sees the switcher")
}

// A158, standalone: the group is never read, and a mark is refused.
func TestA158StandaloneHasNoSwitcher(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	owner := authentikPK(t, "owner@example.org")
	akGroupChange(t, owner, true)
	defer akGroupChange(t, owner, false)
	time.Sleep(2 * time.Second)
	if o.showSwitcher() {
		t.Error("standalone: a member of the group is shown the switcher")
	}
	if code, msg := o.markSwitcher(o.me().ID, false); code != http.StatusConflict {
		t.Errorf("standalone mark: %d %s, want 409", code, msg)
	}
	if !s.inSwitcherGroup(t, owner) {
		t.Error("standalone: the refused mark changed the group")
	}
	t.Log("A158: standalone: a member of the group sees no switcher, and a mark is refused (409)")
}
