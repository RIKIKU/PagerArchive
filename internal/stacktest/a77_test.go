package stacktest

// A77, upgrade from the previous release (ADR 0019). scripts/a77.sh runs two
// phases around the upgrade: TestA77Seed on the previous release writes what it
// made to a file, and TestA77Check compares that with the upgraded stack.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	a77Owner    = "owner@example.org"
	a77User     = "a77-user@example.com"
	a77Template = "A77 Fire"
	a77Hook     = "14323"
	// The example agency profile's format; A77PROBE is what search looks for.
	a77Probe = "@@EX0000077 HSF1 A77PROBE ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1"
)

// a77State is what must survive: only fields a release shouldn't change, so a
// later release may add others.
type a77State struct {
	Messages     []a77Message     `json:"messages"`
	Aliases      []a77Alias       `json:"aliases"`
	Templates    []a77Template_   `json:"templates"`
	IconSHA256   string           `json:"icon_sha256"`
	Integrations []a77Integration `json:"integrations"`
	Users        []string         `json:"users"`
	// ADR 0021: the user's two invitations waiting on the previous release,
	// one of them opened there by a link scanner. Not compared field by field.
	WaitingLink string `json:"waiting_link,omitempty"`
	ScannedLink string `json:"scanned_link,omitempty"`
	// Whether the scan used it up: on a release from before ADR 0021, yes.
	ScannedUsedUp bool `json:"scanned_used_up,omitempty"`
}

const (
	a77Waiting = "a77-waiting@example.com"
	a77Scanned = "a77-scanned@example.com"
)

// a77Scan is a mail provider's link scanner: a browser with no cookies runs
// the enrolment page and goes no further.
func a77Scan(t *testing.T, s *stack, link string) challenge {
	t.Helper()
	_, c := s.browser(t, "scanner@example.net").walk(link, func(challenge) map[string]any { return nil })
	return c
}

// a77Invitation is the user's invitation to address, from their list.
func a77Invitation(t *testing.T, u *browser, address string) (id int64, status string) {
	t.Helper()
	var list []struct {
		ID     int64  `json:"id"`
		Email  string `json:"email"`
		Status string `json:"status"`
	}
	u.a77JSON("/api/v1/invitations", &list)
	for _, i := range list {
		if i.Email == address {
			return i.ID, i.Status
		}
	}
	t.Fatalf("no invitation to %s in %s's list", address, u.email)
	return 0, ""
}

type a77Message struct {
	ID        int64   `json:"id"`
	Capcode   string  `json:"capcode"`
	Text      string  `json:"text"`
	Alias     *string `json:"alias"`
	JobNumber *string `json:"job_number"`
}

type a77Alias struct {
	ID         int64  `json:"id"`
	Pattern    string `json:"pattern"`
	Name       string `json:"name"`
	TemplateID *int64 `json:"template_id"`
}

type a77Template_ struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Agency string  `json:"agency"`
	Icon   *string `json:"icon"`
}

type a77Integration struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (b *browser) a77JSON(path string, out any) {
	b.t.Helper()
	resp, body := b.get(path)
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		b.t.Fatalf("GET %s: %v", path, err)
	}
}

// a77Read reads everything through the API, as an admin and the integration's owner.
func a77Read(t *testing.T, admin, user *browser) a77State {
	t.Helper()
	var st a77State
	var msgs struct {
		Messages []a77Message `json:"messages"`
	}
	admin.a77JSON("/api/v1/messages?limit=200", &msgs)
	st.Messages = msgs.Messages
	var aliases struct {
		Aliases []a77Alias `json:"aliases"`
	}
	admin.a77JSON("/api/v1/admin/aliases", &aliases)
	st.Aliases = aliases.Aliases
	admin.a77JSON("/api/v1/admin/templates", &st.Templates)
	for _, tpl := range st.Templates {
		if tpl.Name == a77Template {
			if tpl.Icon == nil {
				t.Fatalf("%s has no icon", tpl.Name)
			}
			resp, body := admin.get(*tpl.Icon)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("icon: %d", resp.StatusCode)
			}
			sum := sha256.Sum256(body)
			st.IconSHA256 = hex.EncodeToString(sum[:])
		}
	}
	var integ struct {
		Integrations []a77Integration `json:"integrations"`
	}
	user.a77JSON("/api/v1/integrations", &integ)
	st.Integrations = integ.Integrations
	var users []struct {
		Email string `json:"email"`
	}
	resp, body := admin.get("/api/v1/admin/users")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("users: %d", resp.StatusCode)
	}
	var wrapped struct {
		Users *[]struct {
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Users != nil {
		users = *wrapped.Users
	} else if err := json.Unmarshal(body, &users); err != nil {
		t.Fatalf("users: %v", err)
	}
	for _, u := range users {
		st.Users = append(st.Users, u.Email)
	}
	sort.Strings(st.Users)
	return st
}

func a77Post(t *testing.T, s *stack, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"capcode": "0100077", "text": text, "received_at": time.Now().UTC(), "baud": 1200})
	req, _ := http.NewRequest(http.MethodPost, s.app.JoinPath("/api/v1/pages").String(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("PA_STACK_CLIENT_KEY"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("post page: %d %s", resp.StatusCode, b)
	}
}

func TestA77Seed(t *testing.T) {
	s := setup(t, "a77-seed")
	o := s.browser(t, a77Owner)
	if end, c := o.walk(o.postForRedirect("/auth/first-admin"), o.enrolAnswers("", nil)); end == nil || end.Path == "/signin" {
		t.Fatalf("the first admin didn't enrol: %v %s", end, c.errorText())
	}
	u := s.inviteAndEnrol(t, o, a77User)

	var tpl struct {
		ID int64 `json:"id"`
	}
	profile, colour := "example-fire", "#b3261e"
	if code, msg := o.api(http.MethodPost, "/api/v1/admin/templates", map[string]any{
		"name": a77Template, "agency": "A77 Fire Service", "colour": colour, "agency_profile": profile,
	}, &tpl); code != http.StatusCreated {
		t.Fatalf("template: %d %s", code, msg)
	}
	png, err := os.ReadFile(filepath.Join("..", "..", "testdata", "icons", "example-icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, s.app.JoinPath(fmt.Sprintf("/api/v1/admin/templates/%d/icon", tpl.ID)).String(), bytes.NewReader(png))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("icon: %d", resp.StatusCode)
	}
	if code, msg := o.api(http.MethodPost, "/api/v1/admin/aliases", map[string]any{
		"pattern": "0100077", "name": "A77 Station", "template_id": tpl.ID,
	}, nil); code != http.StatusCreated {
		t.Fatalf("alias: %d %s", code, msg)
	}
	for i := 1; i <= 5; i++ {
		a77Post(t, s, fmt.Sprintf("@@EX000010%d HSF1 BEFORE UPGRADE %d @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1", i, i))
	}
	// After the pages, so they don't queue deliveries to a webhook not yet there.
	if code, msg := o.api(http.MethodPut, "/api/v1/admin/integration-hosts", map[string]any{"entries": []string{os.Getenv("PA_A77_HOOK_HOST") + "/32"}}, nil); code >= 300 {
		t.Fatalf("integration hosts: %d %s", code, msg)
	}
	if code, msg := u.api(http.MethodPost, "/api/v1/integrations", map[string]any{
		"name": "A77 webhook", "kind": "webhook", "content": "full", "capcodes": []string{"0100077"},
		"filter": map[string]any{}, "config": map[string]any{},
		"secrets": map[string]any{"url": "http://" + os.Getenv("PA_A77_HOOK_HOST") + ":" + a77Hook + "/hook"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("integration: %d %s", code, msg)
	}

	st := a77Read(t, o, u)
	if len(st.Messages) != 5 || len(st.Aliases) == 0 || len(st.Templates) == 0 || st.IconSHA256 == "" || len(st.Integrations) != 1 || len(st.Users) != 2 {
		t.Fatalf("the made-up data isn't all there: %+v", st)
	}
	// ADR 0021: two waiting invitations. A scanner opens one; on a release
	// whose invitations are single-use, that uses it up.
	st.WaitingLink = u.invite(a77Waiting)
	st.ScannedLink = u.invite(a77Scanned)
	c := a77Scan(t, s, st.ScannedLink)
	// A second run of the page shows whether the first used it up.
	st.ScannedUsedUp = a77Scan(t, s, st.ScannedLink).Component != "ak-stage-prompt"
	t.Logf("A77: on the previous release, the scanner's run of one invitation stopped at %q; used up: %v", c.Component, st.ScannedUsedUp)
	data, _ := json.MarshalIndent(st, "", " ")
	if err := os.WriteFile(os.Getenv("PA_A77_STATE"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("A77: on the previous release: %d pages, %d aliases, %d templates with an icon, %d integration, %d users", len(st.Messages), len(st.Aliases), len(st.Templates), len(st.Integrations), len(st.Users))
}

func TestA77Check(t *testing.T) {
	s := setup(t, "a77-check")
	// A user signs in, with the password set on the previous release.
	o := s.browser(t, a77Owner)
	if ok, c := o.signIn(); !ok {
		t.Fatalf("the admin can't sign in after the upgrade: %s %s", c.errorText(), c.raw)
	}
	u := s.browser(t, a77User)
	if ok, c := u.signIn(); !ok {
		t.Fatalf("the user can't sign in after the upgrade: %s %s", c.errorText(), c.raw)
	}
	t.Log("A77: the admin and the user signed in with their old passwords")

	var want a77State
	data, err := os.ReadFile(os.Getenv("PA_A77_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	got := a77Read(t, o, u)
	for name, pair := range map[string][2]any{
		"pages": {want.Messages, got.Messages}, "aliases": {want.Aliases, got.Aliases}, "templates": {want.Templates, got.Templates},
		"integrations": {want.Integrations, got.Integrations}, "users": {want.Users, got.Users}, "the icon": {want.IconSHA256, got.IconSHA256},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s changed in the upgrade:\nbefore %+v\nafter  %+v", name, pair[0], pair[1])
		}
	}
	t.Logf("A77: still there: %d pages, %d aliases, %d templates, the icon, %d integration, %d users", len(got.Messages), len(got.Aliases), len(got.Templates), len(got.Integrations), len(got.Users))

	// The migrations have run: the newest one in this checkout is applied.
	files, err := filepath.Glob(filepath.Join("..", "store", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	sort.Strings(files)
	newest, err := strconv.Atoi(strings.SplitN(filepath.Base(files[len(files)-1]), "_", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml",
		"exec", "-T", "db", "psql", "-qtA", "-U", "postgres", "-c", "SELECT max(version_id) FROM goose_db_version WHERE is_applied")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("migrations applied: %v %s", err, out)
	}
	if applied := strings.TrimSpace(string(out)); applied != strconv.Itoa(newest) {
		t.Errorf("the newest migration applied is %s, this release's is %d", applied, newest)
	} else {
		t.Logf("A77: migrations applied up to %s, this release's newest", applied)
	}

	// The role the script dropped: the API made it again, with no hand step.
	if !lockedLine.MatchString(s.scrape(t)) {
		t.Error("no pagerarchive_accounts_locked in the metrics: the API didn't create the metrics role")
	} else {
		t.Log("A77: the metrics role is back and the locked-accounts metric is there")
	}

	// No map mounted, so the example built into the image.
	resp, tiles := o.get("/api/v1/map/tiles.pmtiles")
	example, err := os.ReadFile(filepath.Join("..", "..", "deploy", "example-map", "example-map.pmtiles"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(tiles, example) {
		t.Errorf("the map: %d, %d bytes, want the %d bytes of the example map", resp.StatusCode, len(tiles), len(example))
	}

	// A new page is stored, parsed, found by search and delivered.
	hook := make(chan string, 8)
	l, err := net.Listen("tcp", "0.0.0.0:"+a77Hook)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hook <- string(b)
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	a77Post(t, s, a77Probe)
	var found struct {
		Messages []a77Message `json:"messages"`
	}
	o.a77JSON("/api/v1/messages?q=A77PROBE&limit=10", &found)
	if len(found.Messages) != 1 || found.Messages[0].JobNumber == nil || *found.Messages[0].JobNumber != "EX0000077" {
		t.Errorf("searching for the new page found %+v", found.Messages)
	} else {
		t.Logf("A77: the new page is stored, parsed (job %s) and found by search", *found.Messages[0].JobNumber)
	}
	select {
	case body := <-hook:
		if !strings.Contains(body, "A77PROBE") {
			t.Errorf("the webhook got a body without the page: %d bytes", len(body))
		}
		t.Log("A77: the new page reached the integration set up before the upgrade")
	case <-time.After(60 * time.Second):
		t.Error("the new page didn't reach the integration within 60 s")
	}

	a77CheckInvitations(t, s, u, want)
}

// a77CheckInvitations: no invitation that was waiting before the upgrade is
// left broken (ADR 0021). The API's check on start makes the working one
// reusable and marks the one the scanner used up; until it has run, the
// list may still show both as waiting.
func a77CheckInvitations(t *testing.T, s *stack, u *browser, want a77State) {
	t.Helper()
	if want.WaitingLink == "" {
		t.Fatal("the seed recorded no invitations")
	}
	scannedID, status := a77Invitation(t, u, a77Scanned)
	if want.ScannedUsedUp {
		deadline := time.Now().Add(2 * time.Minute)
		for status != "link_lost" && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			_, status = a77Invitation(t, u, a77Scanned)
		}
		if status != "link_lost" {
			t.Fatalf("the invitation the scanner used up on the previous release shows %q, not link_lost", status)
		}
		t.Log("A77: after the upgrade, the invitation the scanner used up shows that its link no longer works")
	} else if status != "pending" {
		// A80 seeds and checks on one release, whose scans use nothing up.
		t.Errorf("the scanned invitation, not used up, shows %q", status)
	}
	if _, status := a77Invitation(t, u, a77Waiting); status != "pending" {
		t.Errorf("the working invitation shows %q after the upgrade", status)
	}

	// The working one: two scans, then its invitee enrols with the same link.
	for range 2 {
		if c := a77Scan(t, s, want.WaitingLink); c.Component != "ak-stage-prompt" {
			t.Fatalf("a scan of the upgraded invitation stopped at %q %s", c.Component, c.errorText())
		}
	}
	w := s.browser(t, a77Waiting)
	w.enrol(want.WaitingLink)
	if m := w.me(); m.InvitedBy == nil || !strings.Contains(*m.InvitedBy, a77User) {
		t.Errorf("the waiting invitation's invitee: invited by %v", m.InvitedBy)
	}

	// The used-up one: resent, and its invitee enrols with the new link.
	since := time.Now()
	if code, msg := u.api(http.MethodPost, fmt.Sprintf("/api/v1/invitations/%d/resend", scannedID), nil, nil); code != http.StatusOK {
		t.Fatalf("resend: %d %s", code, msg)
	}
	sc := s.browser(t, a77Scanned)
	sc.enrol(s.mailpit.invitationLink(t, a77Scanned, since))
	if m := sc.me(); m.InvitedBy == nil || !strings.Contains(*m.InvitedBy, a77User) {
		t.Errorf("the resent invitation's invitee: invited by %v", m.InvitedBy)
	}
	t.Log("A77: the waiting invitation survived two scans and enrolled; the used-up one was resent and enrolled, both with the user as inviter")
}
