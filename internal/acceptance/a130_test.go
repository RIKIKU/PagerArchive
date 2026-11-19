package acceptance

import (
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A130 at API level: service accounts (R62a, ADR 0048), with tokens from a
// local issuer standing in for Authentik's service-accounts client. The
// stack test (internal/stacktest, TestPowerShellA130ServiceAccount) repeats
// the main cases with real Authentik and the module.

const saClient = "pagerarchive-service"

// useServiceAccounts makes the server trust iss as the service accounts'
// issuer.
func (e *env) useServiceAccounts(iss *testIssuer) {
	e.srv.ServiceAccounts = &server.PowerShell{
		Issuer: iss.url, ClientID: saClient,
		Keys: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&iss.key.PublicKey}},
	}
}

// saToken is what Authentik's client-credentials grant gives a service
// account.
func (i *testIssuer) saToken(t *testing.T, subject string) string {
	t.Helper()
	return i.accessToken(t, subject, func(c map[string]any) {
		c["aud"], c["azp"], c["scope"] = saClient, saClient, "profile openid"
	})
}

// bearerJSON is bearer with a JSON reply decoded into out.
func (e *env) bearerJSON(method, path, token string, out any) (int, server.Problem) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var p server.Problem
	if resp.StatusCode >= 400 {
		_ = json.NewDecoder(resp.Body).Decode(&p)
	} else if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			e.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode, p
}

func (e *env) subjectOf(id int64) string {
	e.t.Helper()
	u, err := e.st.UserByID(e.ctx, id)
	if err != nil || u.Subject == nil {
		e.t.Fatalf("user %d's subject: %v", id, err)
	}
	return *u.Subject
}

// A130. A service account reads pages and what admins read, changes
// nothing, never sees people's personal details, and ends at once when
// its credential expires, is rotated or revoked, or its owner's tree is
// deactivated. People's enrolment is unchanged.
func TestA130ServiceAccounts(t *testing.T) {
	e := setup(t)
	e.usePowerShell(newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/"))
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-service/")
	e.useServiceAccounts(iss)
	if code, _ := e.post("a", "1234567", "A130 MADE UP STRUCTURE FIRE", e.now); code != http.StatusOK {
		t.Fatalf("post: %d", code)
	}

	// Made from the shell, owned by an admin; the name's rules hold.
	for _, bad := range []string{"AB", "has space", "ak-own", "x@example.com"} {
		if _, _, err := e.acc.CreateServiceAccount(e.ctx, bad, e.admin.email); err == nil {
			t.Errorf("name %q was accepted", bad)
		}
	}
	if _, _, err := e.acc.CreateServiceAccount(e.ctx, "report-bot", e.user.email); err == nil {
		t.Error("a user who isn't an admin owns a service account")
	}
	sa, cred, err := e.acc.CreateServiceAccount(e.ctx, "report-bot", e.admin.email)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.acc.CreateServiceAccount(e.ctx, "report-bot", e.admin.email); err == nil {
		t.Error("a second report-bot was made")
	}
	if cred.Secret == "" || cred.ExpiresAt.Sub(time.Now()) < 89*24*time.Hour || cred.ExpiresAt.Sub(time.Now()) > 90*24*time.Hour {
		t.Errorf("credential: expires %v", cred.ExpiresAt)
	}
	tok := iss.saToken(t, e.subjectOf(sa.ID))

	// It reads pages and admins' reads.
	var me server.Me
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/me", tok, &me); code != http.StatusOK || !me.ServiceAccount || me.Email != "" {
		t.Fatalf("me: %d %+v %+v", code, p, me)
	}
	var page struct {
		Messages []server.Message `json:"messages"`
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/messages", tok, &page); code != http.StatusOK || len(page.Messages) != 1 {
		t.Errorf("messages: %d %+v %d", code, p, len(page.Messages))
	}
	for _, path := range []string{"/api/v1/meta", "/api/v1/admin/templates", "/api/v1/admin/aliases", "/api/v1/admin/retention",
		"/api/v1/admin/clients", "/api/v1/admin/reparse", "/api/v1/admin/redaction", "/api/v1/capcodes/1234567"} {
		if code, p := e.bearerJSON(http.MethodGet, path, tok, nil); code != http.StatusOK {
			t.Errorf("GET %s: %d %+v", path, code, p)
		}
	}

	// Every write is refused, with the code, and changes nothing.
	writes := [][2]string{
		{http.MethodPost, "/api/v1/admin/templates"}, {http.MethodPut, "/api/v1/admin/retention"},
		{http.MethodPost, "/api/v1/invitations"}, {http.MethodPatch, "/api/v1/me"}, {http.MethodDelete, "/api/v1/me"},
		{http.MethodPost, "/api/v1/albums"}, {http.MethodPost, "/api/v1/integrations"},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", e.user.id)},
		{http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", e.user.id)},
		{http.MethodPost, "/api/v1/admin/aliases/import"}, {http.MethodPost, "/api/v1/admin/backup"},
		{http.MethodPost, "/api/v1/admin/reparse/start"}, {http.MethodPut, "/api/v1/admin/integration-hosts"},
	}
	for _, w := range writes {
		if code, p := e.bearerJSON(w[0], w[1], tok, nil); code != http.StatusForbidden || p.Code != server.CodeServiceReadOnly {
			t.Errorf("%s %s: %d %+v", w[0], w[1], code, p)
		}
	}
	if u, _ := e.st.UserByID(e.ctx, e.user.id); u.Status != "active" {
		t.Error("the user was deactivated")
	}

	// People's personal details are refused.
	for _, path := range []string{"/api/v1/admin/users", fmt.Sprintf("/api/v1/admin/users/%d", e.user.id),
		fmt.Sprintf("/api/v1/admin/users/%d/invitations", e.admin.id), fmt.Sprintf("/api/v1/admin/users/%d/deactivation-preview", e.admin.id),
		"/api/v1/admin/invitations", "/api/v1/admin/alias-requests"} {
		if code, p := e.bearerJSON(http.MethodGet, path, tok, nil); code != http.StatusForbidden || p.Code != server.CodeServicePersonal {
			t.Errorf("GET %s: %d %+v", path, code, p)
		}
	}

	// Each client is for its own kind of account.
	ps := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(ps)
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/me", ps.accessToken(t, e.subjectOf(sa.ID), nil), nil); code != http.StatusUnauthorized || p.Code != server.CodeInvalidToken {
		t.Errorf("the module's client, as the service account: %d %+v", code, p)
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/me", iss.saToken(t, e.admin.claims.Subject), nil); code != http.StatusUnauthorized || p.Code != server.CodeInvalidToken {
		t.Errorf("the service client, as the admin: %d %+v", code, p)
	}
	if code, _ := e.bearerJSON(http.MethodGet, "/api/v1/me", ps.accessToken(t, e.admin.claims.Subject, nil), nil); code != http.StatusOK {
		t.Errorf("the admin's module token: %d", code)
	}
	// It can't sign in with a browser.
	var refusal *accounts.Error
	if _, err := e.signIn(accounts.Claims{Subject: e.subjectOf(sa.ID)}); !errors.As(err, &refusal) || refusal.Code != "service_account" {
		t.Errorf("browser sign-in: %v", err)
	}

	// Expired: refused at once.
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE users SET credential_expires_at = now() - interval '1 second' WHERE id = $1", sa.ID); err != nil {
		t.Fatal(err)
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", tok, nil); code != http.StatusUnauthorized || p.Code != server.CodeServiceExpired {
		t.Errorf("expired: %d %+v", code, p)
	}

	// Rotated: a new credential and expiry; the old one's token is refused
	// at once, though it hasn't expired.
	cred2, err := e.acc.RotateServiceAccount(e.ctx, "report-bot")
	if err != nil {
		t.Fatal(err)
	}
	if cred2.Secret == cred.Secret || !cred2.ExpiresAt.After(time.Now().Add(89*24*time.Hour)) {
		t.Errorf("rotated: %v", cred2.ExpiresAt)
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", tok, nil); code != http.StatusUnauthorized || p.Code != server.CodeNotSignedIn {
		t.Errorf("old credential after rotating: %d %+v", code, p)
	}
	tok = iss.saToken(t, e.subjectOf(sa.ID))
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", tok, nil); code != http.StatusOK {
		t.Errorf("new credential: %d %+v", code, p)
	}

	// Admins see it, marked, and can only deactivate it.
	var users []server.UserRow
	if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/users", nil, &users); code != http.StatusOK {
		t.Fatalf("users: %d %+v", code, p)
	}
	found := false
	for _, u := range users {
		if u.ID == sa.ID {
			found = u.ServiceAccount && u.Email == "" && u.InvitedByID != nil && *u.InvitedByID == e.admin.id
		}
	}
	if !found {
		t.Errorf("the users list doesn't show report-bot as the admin's service account: %+v", users)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", sa.ID), map[string]string{"role": "admin"}},
		{http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/trusted", sa.ID), map[string]bool{"trusted": true}},
	} {
		if code, p := e.admin.call(c.method, c.path, c.body, nil); code != http.StatusConflict || p.Code != "service_account_shell_only" {
			t.Errorf("%s %s: %d %+v", c.method, c.path, code, p)
		}
	}

	// Deactivating the owner's tree takes it: shown, marked, in the preview.
	owner := e.inviteAndEnrol(e.admin, "a130-owner@example.com")
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", owner.id), map[string]string{"role": "admin"}, nil); code >= 300 {
		t.Fatalf("make owner admin: %d %+v", code, p)
	}
	treeSA, _, err := e.acc.CreateServiceAccount(e.ctx, "tree-bot", owner.email)
	if err != nil {
		t.Fatal(err)
	}
	treeTok := iss.saToken(t, e.subjectOf(treeSA.ID))
	p := e.preview(e.admin, owner.id)
	var node *server.TreeNode
	walk(p.Root, func(n *server.TreeNode) {
		if n.ID == treeSA.ID {
			node = n
		}
	})
	if node == nil || !node.ServiceAccount || node.Tick == nil || !node.Tick.Start || !strings.Contains(node.Label, "service account") {
		t.Fatalf("tree-bot in the preview: %+v", node)
	}
	e.deactivateTree(e.admin, owner.id)
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", treeTok, nil); code != http.StatusUnauthorized || p.Code != server.CodeDeactivated {
		t.Errorf("after the tree: %d %+v", code, p)
	}
	if e.dir.signInAllowed(e.subjectOf(treeSA.ID)) {
		t.Error("tree-bot is still active in Authentik")
	}
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/reactivate", treeSA.ID), nil, nil); code != http.StatusConflict || p.Code != "service_account_shell_only" {
		t.Errorf("reactivate: %d %+v", code, p)
	}

	// Revoked: refused at once, and gone from Authentik.
	revokedSub := e.subjectOf(sa.ID)
	if err := e.acc.RevokeServiceAccount(e.ctx, "report-bot"); err != nil {
		t.Fatal(err)
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", tok, nil); code != http.StatusUnauthorized || p.Code != server.CodeNotSignedIn {
		t.Errorf("after revoking: %d %+v", code, p)
	}
	if e.dir.signInAllowed(revokedSub) {
		t.Error("report-bot's Authentik user is still there")
	}
	list, err := e.acc.ListServiceAccounts(e.ctx)
	if err != nil || len(list) != 2 || list[0].Status != "deleted" || list[0].Name == nil || *list[0].Name != "report-bot" {
		t.Errorf("list: %v %+v", err, list)
	}

	// An owner deleting their account takes their service accounts.
	leaver := e.inviteAndEnrol(e.admin, "a130-leaver@example.com")
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", leaver.id), map[string]string{"role": "admin"}, nil); code >= 300 {
		t.Fatalf("make leaver admin: %d %+v", code, p)
	}
	leaverSA, _, err := e.acc.CreateServiceAccount(e.ctx, "leaver-bot", leaver.email)
	if err != nil {
		t.Fatal(err)
	}
	leaverTok := iss.saToken(t, e.subjectOf(leaverSA.ID))
	if code, p := leaver.call(http.MethodDelete, "/api/v1/me", nil, nil); code >= 300 {
		t.Fatalf("delete account: %d %+v", code, p)
	}
	if code, p := e.bearerJSON(http.MethodGet, "/api/v1/meta", leaverTok, nil); code != http.StatusUnauthorized || p.Code != server.CodeNotSignedIn {
		t.Errorf("after the owner left: %d %+v", code, p)
	}

	// People's enrolment is unchanged (R41): an invitation, to an allowed
	// domain, is still needed.
	if code, _ := e.admin.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "a130@not-allowed.test"}, nil); code < 400 {
		t.Errorf("invitation outside the allowed domains: %d", code)
	}
	if _, err := e.signIn(accounts.Claims{Subject: "a130-uninvited", Email: "a130-uninvited@example.com"}); !errors.As(err, &refusal) || refusal.Code != "not_invited" {
		t.Errorf("sign-in without an invitation: %v", err)
	}
	if n := e.inviteAndEnrol(e.admin, "a130-person@example.com"); n.id == 0 {
		t.Error("a person couldn't enrol with an invitation")
	}

	// Its requests are logged with its name; its credential never is.
	logs := e.logs.String()
	for _, want := range []string{`"service_account":"report-bot"`, "signed in as a service account", "service account refused"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q", want)
		}
	}
	for _, secret := range []string{cred.Secret, cred2.Secret} {
		if strings.Contains(logs, secret) {
			t.Error("a credential is in the logs")
		}
	}
	t.Logf("A130: %d writes refused; personal details refused; expired, rotated, revoked, tree and owner deletion end it", len(writes))
}
