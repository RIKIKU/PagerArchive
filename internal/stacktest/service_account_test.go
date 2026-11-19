package stacktest

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A130 against the real stack, through the module (R62a, ADR 0048): a
// service account made on the server with `pagerarchive service-account`,
// connected with Connect-PagerArchive -ServiceAccount, which trades its
// credential with real Authentik's client-credentials grant. Its credential
// is never logged: the test only checks it's in no file it shouldn't be.

// serviceAccountCmd runs `pagerarchive service-account` in the API's
// container, as an operator would.
func serviceAccountCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	all := append([]string{"compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml", "exec", "-T", "api", "/usr/local/bin/pagerarchive", "service-account"}, args...)
	cmd := exec.Command("docker", all...)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// credential is the line after "The credential:" in create's or rotate's
// output (its log lines, on stderr, can come before or after it).
func credential(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	c := ""
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), "The credential:") && i+1 < len(lines) {
			c = strings.TrimSpace(lines[i+1])
		}
	}
	if len(c) < 32 || strings.Contains(c, " ") {
		t.Fatalf("no credential in the output (%d lines)", len(lines))
	}
	return c
}

func TestPowerShellA130ServiceAccount(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)
	name := "sa" + ps.prefix + "-" + ps.run
	out, err := serviceAccountCmd(t, "create", name, "--owner", fmt.Sprint(owner.me().ID))
	if err != nil {
		t.Fatalf("create: %v %s", err, firstLine(out))
	}
	secret := credential(t, out)
	t.Logf("A130: created: %s", firstLine(out))

	home := ps.psHome(t)
	w := ps.window(t, home)
	// The credential is given as Get-Credential would give it, from a file
	// only this test reads.
	secretFile := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	connect := fmt.Sprintf(`$c = [pscredential]::new('%s', (ConvertTo-SecureString -AsPlainText -Force -String (Get-Content -Raw -LiteralPath '%s').Trim()))
Connect-PagerArchive -Uri '%s' -AllowUnencryptedAuthentication -ServiceAccount $c | Select-Object Name, Role`, name, secretFile, ps.app.String())
	if got := w.do(connect); !hasLine(got, `JSON {"Name":"`+name+`","Role":"service account"}`) {
		t.Fatalf("connect: %q", got)
	}

	// Reads work; a write is refused, saying why.
	if got := w.do(`(Get-PagerArchiveTemplate | Measure-Object).Count -ge 0`); !hasLine(got, "True") {
		t.Errorf("read: %q", got)
	}
	if got := w.do(`(Get-PagerArchiveMessage -First 1 | Measure-Object).Count -le 1`); !hasLine(got, "True") {
		t.Errorf("messages: %q", got)
	}
	got := w.do(`New-PagerArchiveTemplate -Name 'A130 made up' -Agency 'Made-up agency' -Confirm:$false`)
	if !hasLine(got, "ERROR service_account_read_only,New-PagerArchiveTemplate This is a read-only service account") {
		t.Errorf("write: %q", got)
	}
	t.Logf("A130: module: a read works, a write: %q", got)

	// A new window uses the saved credential (with no vault, nothing is
	// saved: R61).
	saved := ps.vault != "none"
	if got := ps.window(t, home).do(`(Get-PagerArchiveTemplate | Measure-Object).Count -ge 0`); saved && !hasLine(got, "True") || !saved && !hasLine(got, "ERROR not_connected") {
		t.Errorf("new window: %q", got)
	}
	if hits := filesContaining(t, []string{secret}, home); len(hits) > 0 {
		t.Errorf("the credential is unencrypted in %v", hits)
	}

	// Rotating ends the old credential at once, though its access token
	// hasn't expired.
	out, err = serviceAccountCmd(t, "rotate", name)
	if err != nil {
		t.Fatalf("rotate: %v %s", err, firstLine(out))
	}
	newSecret := credential(t, out)
	if newSecret == secret {
		t.Fatal("rotate gave the same credential")
	}
	if got := w.do(`Get-PagerArchiveTemplate`); !hasLine(got, "ERROR not_signed_in,Get-PagerArchiveTemplate") {
		t.Errorf("after rotating: %q", got)
	}
	if got := ps.window(t, home).do(`Get-PagerArchiveTemplate`); saved && !hasLine(got, "ERROR service_account_refused,Get-PagerArchiveTemplate") || !saved && !hasLine(got, "ERROR not_connected") {
		t.Errorf("a new window, old credential: %q", got)
	}

	// The new one works; revoking ends it at once.
	if err := os.WriteFile(secretFile, []byte(newSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	w = ps.window(t, ps.psHome(t))
	if got := w.do(connect); !hasLine(got, `JSON {"Name":"`+name+`"`) {
		t.Fatalf("connect with the new credential: %q", got)
	}
	if out, err := serviceAccountCmd(t, "revoke", name); err != nil {
		t.Fatalf("revoke: %v %s", err, firstLine(out))
	}
	if got := w.do(`Get-PagerArchiveTemplate`); !hasLine(got, "ERROR not_signed_in,Get-PagerArchiveTemplate") {
		t.Errorf("after revoking: %q", got)
	}
	out, err = serviceAccountCmd(t, "list")
	if err != nil || !strings.Contains(out, name) || !strings.Contains(out, "revoked") {
		t.Errorf("list: %v %q", err, out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, newSecret) {
		t.Error("list shows a credential")
	}

	// People's enrolment is unchanged (R41): an address outside the allowed
	// domains can't be invited.
	if code, msg := owner.api(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "a130@not-allowed.test"}, nil); code < 400 {
		t.Errorf("invitation outside the allowed domains: %d %s", code, msg)
	}
	t.Logf("A130: rotated and revoked: refused at once; list says revoked")
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
