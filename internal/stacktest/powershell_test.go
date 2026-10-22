package stacktest

// The PowerShell module against the real stack (A20, A21 and A51-A55
// through the module; ADR 0009 §11). Each "window" is a pwsh process kept
// open and fed commands on stdin; the test plays the browser, through
// Authentik's flow executor, at the URL the module opens. Skipped unless
// PA_STACK_PWSH is set; scripts/stack-checks.sh runs them twice, with
// PA_STACK_PS_VAULT=secretstore (a registered SecretStore vault) and
// PA_STACK_PS_VAULT=none (no vault: this session only).

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type psStack struct {
	*stack
	pwsh, modules, vault string
	module               string // the module's manifest
	bin                  string // a fake xdg-open, first on PATH
	prefix               string // capcodes and addresses unique to this vault run
	run                  string // and to this test run, so a kept stack can run them again
}

func setupPS(t *testing.T) *psStack {
	t.Helper()
	s := setup(t, "restarted")
	pwsh, vault := os.Getenv("PA_STACK_PWSH"), os.Getenv("PA_STACK_PS_VAULT")
	if pwsh == "" || vault == "" {
		t.Skip("PA_STACK_PWSH and PA_STACK_PS_VAULT are not set (run scripts/stack-checks.sh)")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ps := &psStack{stack: s, pwsh: pwsh, modules: os.Getenv("PA_STACK_PS_MODULES"), vault: vault,
		module: filepath.Join(root, "powershell", "PagerArchive", "PagerArchive.psd1"), bin: t.TempDir()}
	ps.prefix = map[string]string{"secretstore": "1", "none": "2"}[vault]
	if ps.prefix == "" {
		t.Fatalf("PA_STACK_PS_VAULT is secretstore or none, not %q", vault)
	}
	ps.run = fmt.Sprintf("%02d", time.Now().Unix()%100)
	// The module opens the browser with xdg-open; this one writes the URL
	// to $PA_TEST_URL_FILE for the test to visit.
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" > \"$PA_TEST_URL_FILE\"\n"
	if err := os.WriteFile(filepath.Join(ps.bin, "xdg-open"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ps
}

// psHome is a user's home directory: their saved sign-ins, and their vault
// when this run has one.
func (ps *psStack) psHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if ps.vault == "secretstore" {
		w := ps.window(t, home)
		out := w.do(`Register-SecretVault -Name TestStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault
Reset-SecretStore -Scope CurrentUser -Authentication None -Interaction None -Force -Confirm:$false 3>$null
(Get-SecretVault -Name TestStore).IsDefault`)
		if strings.Join(out, "") != "True" {
			t.Fatalf("vault: %q", out)
		}
		w.close()
	}
	return home
}

// psWindow is one PowerShell window.
type psWindow struct {
	t       *testing.T
	cmd     *exec.Cmd
	in      io.WriteCloser
	lines   chan string
	dir     string
	urlFile string
	n       int
}

// The window's prelude: the module, plain text, and Out-Line, which writes
// every stream as one line: ERROR <id> <message>, WARNING, VERBOSE, DEBUG,
// INFO, strings as they are, and other objects as JSON.
const psPrelude = `$ProgressPreference = 'SilentlyContinue'
$PSStyle.OutputRendering = 'PlainText'
Import-Module -Name '%s' -ErrorAction Stop
function Out-Line {
    param([Parameter(ValueFromPipeline)] $InputObject)
    process {
        switch ($InputObject) {
            { $_ -is [System.Management.Automation.ErrorRecord] } { 'ERROR ' + $_.FullyQualifiedErrorId + ' ' + $_.Exception.Message; break }
            { $_ -is [System.Management.Automation.WarningRecord] } { 'WARNING ' + $_.Message; break }
            { $_ -is [System.Management.Automation.VerboseRecord] } { 'VERBOSE ' + $_.Message; break }
            { $_ -is [System.Management.Automation.DebugRecord] } { 'DEBUG ' + $_.Message; break }
            { $_ -is [System.Management.Automation.InformationRecord] } { 'INFO ' + $_.MessageData; break }
            { $null -eq $_ } { break }
            { $_ -is [string] -or $_ -is [ValueType] } { [string]$_; break }
            default { 'JSON ' + ($_ | ConvertTo-Json -Compress -Depth 6) }
        }
    }
}
`

func (ps *psStack) window(t *testing.T, home string) *psWindow {
	t.Helper()
	dir := t.TempDir()
	w := &psWindow{t: t, lines: make(chan string, 1000), dir: dir, urlFile: filepath.Join(dir, "browser-url")}
	w.cmd = exec.Command(ps.pwsh, "-NoProfile", "-NonInteractive", "-Command", "-")
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "PSModulePath=") && !strings.HasPrefix(kv, "XDG_") {
			env = append(env, kv)
		}
	}
	w.cmd.Env = append(env, "HOME="+home, "PATH="+ps.bin+":"+os.Getenv("PATH"), "PSModulePath="+ps.modules,
		"PA_TEST_URL_FILE="+w.urlFile, "TERM=dumb", "NO_COLOR=1", "POWERSHELL_TELEMETRY_OPTOUT=1")
	var err error
	if w.in, err = w.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := w.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	w.cmd.Stderr = w.cmd.Stdout
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		for sc.Scan() {
			w.lines <- strings.TrimRight(ansi.ReplaceAllString(sc.Text(), ""), "\r ")
		}
		close(w.lines)
	}()
	t.Cleanup(w.close)
	// Dot-sourced as it is, so Out-Line stays defined for later steps.
	prelude := filepath.Join(dir, "prelude.ps1")
	if err := os.WriteFile(prelude, []byte(fmt.Sprintf(psPrelude, ps.module)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w.in, ". '%s'; '##END##'\n", prelude); err != nil {
		t.Fatal(err)
	}
	if got := w.result(); len(got) != 0 {
		t.Fatalf("prelude: %q", got)
	}
	return w
}

// send runs a script without waiting for it; result collects its lines.
func (w *psWindow) send(script string) {
	w.t.Helper()
	w.n++
	file := filepath.Join(w.dir, fmt.Sprintf("step%d.ps1", w.n))
	body := "try {\n& {\n" + script + "\n} *>&1 | Out-Line\n} catch {\n$_ | Out-Line\n}\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w.in, ". '%s'; '##END##'\n", file); err != nil {
		w.t.Fatal(err)
	}
}

func (w *psWindow) result() []string {
	w.t.Helper()
	var out []string
	timeout := time.After(3 * time.Minute)
	for {
		select {
		case l, ok := <-w.lines:
			if !ok {
				w.t.Fatalf("pwsh ended: %q", out)
			}
			if l == "##END##" {
				return out
			}
			if strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		case <-timeout:
			w.t.Fatalf("pwsh didn't finish: %q", out)
		}
	}
}

func (w *psWindow) do(script string) []string {
	w.t.Helper()
	w.send(script)
	return w.result()
}

// line waits for the next line, e.g. the device code's warning.
func (w *psWindow) line(match func(string) bool) string {
	w.t.Helper()
	timeout := time.After(2 * time.Minute)
	var seen []string
	for {
		select {
		case l, ok := <-w.lines:
			if !ok {
				w.t.Fatalf("pwsh ended: %q", seen)
			}
			if match(l) {
				return l
			}
			seen = append(seen, l)
		case <-timeout:
			w.t.Fatalf("no such line from pwsh: %q", seen)
		}
	}
}

func (w *psWindow) close() {
	if w.cmd.ProcessState == nil && w.cmd.Process != nil {
		_, _ = io.WriteString(w.in, "exit\n")
		done := make(chan struct{})
		go func() { _ = w.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = w.cmd.Process.Kill()
		}
	}
}

// browserURL waits for the URL the module opened.
func (w *psWindow) browserURL() string {
	w.t.Helper()
	for range 300 {
		if b, err := os.ReadFile(w.urlFile); err == nil && len(b) > 0 {
			_ = os.Remove(w.urlFile)
			return strings.TrimSpace(string(b))
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.t.Fatal("the module didn't open the browser")
	return ""
}

// follow visits start as b's browser: Authentik's flows through the
// executor with answers, and redirects anywhere, including the module's
// loopback listener. It returns the last URL and the challenge it stopped
// at, if any.
func (b *browser) follow(start string, answers answer) (*url.URL, challenge) {
	b.t.Helper()
	next, _ := url.Parse(start)
	for range 40 {
		if os.Getenv("PA_DEBUG_FOLLOW") != "" {
			b.t.Logf("follow: %s", next)
		}
		if m := flowPath.FindStringSubmatch(next.Path); m != nil && next.Host == b.s.authentik.Host {
			c := b.executor(m[1], next.RawQuery, nil)
			for c.Component != "xak-flow-redirect" {
				if os.Getenv("PA_DEBUG_FOLLOW") != "" {
					b.t.Logf("follow: challenge %s %.300s", c.Component, c.raw)
				}
				body := answers(c)
				if body == nil {
					return next, c
				}
				body["component"] = c.Component
				c = b.executor(m[1], next.RawQuery, body)
			}
			to, err := next.Parse(c.To)
			if err != nil {
				b.t.Fatalf("bad redirect %q", c.To)
			}
			next = to
			continue
		}
		resp, err := b.client.Get(next.String())
		if err != nil {
			b.t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			return next, challenge{}
		}
		if next, err = next.Parse(loc); err != nil {
			b.t.Fatal(err)
		}
	}
	b.t.Fatal("too many redirects")
	return nil, challenge{}
}

// connect signs w in as b, in the browser as normal (A51).
func (ps *psStack) connect(w *psWindow, b *browser, extra string) []string {
	w.t.Helper()
	return ps.connectTo(w, b, ps.app.String(), extra)
}

// connectTo is connect with the app at uri (e.g. through a proxy).
func (ps *psStack) connectTo(w *psWindow, b *browser, uri, extra string) []string {
	w.t.Helper()
	w.send("Connect-PagerArchive -Uri '" + uri + "' -AllowUnencryptedAuthentication " + extra)
	end, c := b.follow(w.browserURL(), b.signInAnswers())
	if end == nil || end.Hostname() != "127.0.0.1" {
		w.t.Fatalf("%s's browser stopped at %v %s %.300s", b.email, end, c.errorText(), c.raw)
	}
	return w.result()
}

// tokens are the window's access and refresh tokens, read from the module
// for this test only: to search files and output for, and to try with
// Authentik. They're never logged.
func (w *psWindow) tokens() (access, refresh string) {
	w.t.Helper()
	out := w.do(`$c = & (Get-Module PagerArchive) { $script:Connection }
$r = if ($c.Store -eq 'session') { $c.RefreshToken } else { (& (Get-Module PagerArchive) { param($o) Read-PaRefreshToken -Origin $o } $c.Uri).RefreshToken }
'TOKENS ' + ($c.AccessToken | ConvertFrom-SecureString -AsPlainText) + ' ' + ($r | ConvertFrom-SecureString -AsPlainText)`)
	if len(out) != 1 || !strings.HasPrefix(out[0], "TOKENS ") {
		w.t.Fatalf("tokens: %d lines", len(out))
	}
	f := strings.Fields(out[0])
	return f[1], f[2]
}

// renew tries a refresh token with Authentik, as the module would.
func (ps *psStack) renew(t *testing.T, refresh string) (int, string) {
	t.Helper()
	resp, err := http.PostForm(ps.authentik.JoinPath("/application/o/token/").String(), url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"pagerarchive-powershell"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Error
}

// forms are the ways a secret could be written to a file unencrypted.
func forms(s string) []string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return []string{s, string(b), base64.StdEncoding.EncodeToString([]byte(s)), base64.StdEncoding.EncodeToString(b),
		hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b))}
}

// filesContaining lists files under dirs that hold any secret unencrypted.
func filesContaining(t *testing.T, secrets []string, dirs ...string) []string {
	t.Helper()
	var hits []string
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 50<<20 {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, s := range secrets {
				for _, f := range forms(s) {
					if strings.Contains(string(data), f) {
						hits = append(hits, p)
						return nil
					}
				}
			}
			return nil
		})
	}
	return hits
}

func hasLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// jsonLines decodes the window's JSON lines.
func jsonLines[T any](t *testing.T, lines []string) []T {
	t.Helper()
	var out []T
	for _, l := range lines {
		if s, ok := strings.CutPrefix(l, "JSON "); ok {
			var v T
			if err := json.Unmarshal([]byte(s), &v); err != nil {
				t.Fatalf("%v: %.200s", err, s)
			}
			out = append(out, v)
		}
	}
	return out
}

// exampleFire makes sure the template "Example Fire" exists, through the
// module.
func exampleFire(t *testing.T, w *psWindow) {
	t.Helper()
	out := w.do(`if (-not (Get-PagerArchiveTemplate | Where-Object -Property Name -EQ 'Example Fire')) {
    $null = New-PagerArchiveTemplate -Name 'Example Fire' -Agency 'Example Fire Service' -AgencyProfile example-fire
}`)
	if len(out) != 0 {
		t.Fatalf("Example Fire: %q", out)
	}
}

// A51. Signing in from PowerShell, a new window, no tokens in files,
// disconnecting; on Linux with no vault, this session only.
func TestPowerShellA51SigningIn(t *testing.T) {
	ps := setupPS(t)
	owner := ps.browser(t, "owner@example.org")
	home := ps.psHome(t)
	w := ps.window(t, home)
	out := ps.connect(w, owner, "")
	t.Logf("A51 (%s): Connect-PagerArchive: %q", ps.vault, out)
	conn := jsonLines[struct{ Email, Role, SavedIn string }](t, out)
	if len(conn) != 1 || conn[0].Email != "owner@example.org" || conn[0].Role != "admin" {
		t.Fatalf("connection: %q", out)
	}
	if !hasLine(out, "WARNING http://localhost:8080 uses plain HTTP") {
		t.Errorf("no plain-HTTP warning: %q", out)
	}
	session := ps.vault == "none"
	if want := map[bool]string{true: "This session only", false: "Vault TestStore"}[session]; conn[0].SavedIn != want {
		t.Errorf("saved in %q, want %q", conn[0].SavedIn, want)
	}
	if session != hasLine(out, "WARNING No SecretManagement vault is registered") {
		t.Errorf("no-vault warning shown %v, want %v", !session, session)
	}
	exampleFire(t, w)
	if out := w.do(`(Get-PagerArchiveTemplate).Name`); !hasLine(out, "Example Fire") {
		t.Fatalf("templates: %q", out)
	}

	// Every command's streams, with -Verbose and -Debug, in a transcript,
	// and $Error: no token, page text or address (guidelines §6, §9).
	transcript := filepath.Join(w.dir, "transcript.txt")
	errs := filepath.Join(w.dir, "errors.txt")
	streams := w.do(`Start-Transcript -Path '` + transcript + `' | Out-Null
Get-PagerArchiveTemplate -Verbose -Debug | Out-Null
Get-PagerArchiveAlias -Verbose -Debug | Out-Null
Get-PagerArchiveMessage -Text 'SMOKE PS-P1 MARKER' -First 3 -Verbose -Debug | Out-Null
Remove-PagerArchiveTemplate -Name 'No such template' -Confirm:$false -Verbose -Debug
Stop-Transcript | Out-Null
$Error | ForEach-Object { $_ | Format-List * -Force | Out-String -Width 4096; $_.Exception | Format-List * -Force | Out-String -Width 4096 } | Set-Content -Path '` + errs + `'`)
	access, refresh := w.tokens()
	for _, l := range streams {
		for _, bad := range []string{access, refresh, "SMOKE", "PS-P1"} {
			if strings.Contains(l, bad) {
				t.Errorf("a stream line holds a token or page text: %.60s…", l)
			}
		}
	}
	if !hasLine(streams, "VERBOSE GET /api/v1/messages (q, receptions, limit)") {
		t.Errorf("verbose doesn't name the call: %q", streams)
	}
	if hits := filesContaining(t, []string{access, refresh, "SMOKE PS-P1"}, transcript, errs); len(hits) > 0 {
		t.Errorf("transcript or $Error holds a token or page text: %v", hits)
	}
	if hits := filesContaining(t, []string{access, refresh}, home, w.dir); len(hits) > 0 {
		t.Errorf("files hold a token unencrypted: %v", hits)
	}
	t.Logf("A51: streams, transcript, $Error and every file under the home directory searched: no token")

	// A new window.
	w2 := ps.window(t, home)
	out = w2.do(`(Get-PagerArchiveTemplate -ErrorAction Stop).Name`)
	if session {
		if !hasLine(out, "ERROR not_connected,Get-PagerArchiveTemplate") {
			t.Errorf("new window without a vault: %q", out)
		}
	} else if !hasLine(out, "Example Fire") {
		t.Errorf("new window: %q", out)
	}
	t.Logf("A51: a new window lists templates: %q", out)

	// Disconnect: the next command asks to sign in, and the old sign-in
	// can't be renewed.
	last := map[bool]*psWindow{true: w, false: w2}[session]
	_, refresh = last.tokens()
	if out := last.do(`Disconnect-PagerArchive`); len(out) != 0 {
		t.Errorf("disconnect: %q", out)
	}
	if out := ps.window(t, home).do(`Get-PagerArchiveTemplate`); !hasLine(out, "ERROR not_connected,Get-PagerArchiveTemplate Not signed in") {
		t.Errorf("after disconnect: %q", out)
	}
	if code, e := ps.renew(t, refresh); code != http.StatusBadRequest || e != "invalid_grant" {
		t.Errorf("the disconnected sign-in renews: %d %s", code, e)
	}
	if ps.vault == "secretstore" {
		if out := ps.window(t, home).do(`@(Get-SecretInfo -Vault TestStore).Count`); strings.Join(out, "") != "0" {
			t.Errorf("vault after disconnect: %q", out)
		}
	}
	t.Logf("A51: disconnected; the old refresh token is refused (invalid_grant)")
}

// A52. Signing in without a browser: a device code entered elsewhere.
func TestPowerShellA52DeviceCode(t *testing.T) {
	ps := setupPS(t)
	w := ps.window(t, ps.psHome(t))
	w.send("Connect-PagerArchive -Uri '" + ps.app.String() + "' -AllowUnencryptedAuthentication -UseDeviceCode")
	re := regexp.MustCompile(`open (\S+) on any device and enter the code (\S+)`)
	m := re.FindStringSubmatch(w.line(func(l string) bool { return re.MatchString(l) }))
	t.Logf("A52: the module says: open %s and enter the code (%d characters)", m[1], len(m[2]))
	other := ps.browser(t, "owner@example.org") // another device, not signed in
	sign := other.signInAnswers()
	end, c := other.follow(m[1], func(c challenge) map[string]any {
		if c.Component == "ak-provider-oauth2-device-code" {
			return map[string]any{"code": m[2]}
		}
		return sign(c)
	})
	if c.Component != "ak-provider-oauth2-device-code-finish" {
		t.Fatalf("other device stopped at %v %s %.300s", end, c.errorText(), c.raw)
	}
	out := w.result()
	if conn := jsonLines[struct{ Email string }](t, out); len(conn) != 1 || conn[0].Email != "owner@example.org" {
		t.Fatalf("device sign-in: %q", out)
	}
	exampleFire(t, w)
	if out := w.do(`(Get-PagerArchiveTemplate).Name`); !hasLine(out, "Example Fire") {
		t.Errorf("templates: %q", out)
	}
	t.Logf("A52: signed in with the device code; templates listed")
}

// A53. Status and role decide, on every command; a deactivated user's
// sign-in can't be renewed; tampered tokens are refused.
func TestPowerShellA53StatusAndRole(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)

	// B isn't an admin: refused, nothing saved.
	b := ps.inviteAndEnrol(t, owner, "ps"+ps.prefix+"-b-"+ps.run+"@example.com")
	home := ps.psHome(t)
	w := ps.window(t, home)
	out := ps.connect(w, b, "")
	if !hasLine(out, "ERROR powershell_admins_only,Connect-PagerArchive PowerShell access is for admins.") {
		t.Errorf("user B: %q", out)
	}
	if out := ps.window(t, home).do(`Get-PagerArchiveTemplate`); !hasLine(out, "ERROR not_connected") {
		t.Errorf("user B's sign-in was saved: %q", out)
	}
	t.Logf("A53: user B: %q", out)

	// C is an admin, signed in with the module.
	cEmail := "ps" + ps.prefix + "-c-" + ps.run + "@example.com"
	c := ps.inviteAndEnrol(t, owner, cEmail)
	cid := ps.userID(t, owner, cEmail)
	role := func(r string) {
		if code, msg := owner.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", cid), map[string]string{"role": r}, nil); code >= 300 {
			t.Fatalf("set C's role to %s: %d %s", r, code, msg)
		}
	}
	role("admin")
	w = ps.window(t, ps.psHome(t))
	if out := ps.connect(w, c, ""); !hasLine(out, "JSON ") {
		t.Fatalf("C connects: %q", out)
	}
	access, refresh := w.tokens()

	// The API itself: the real token works; altered, it's refused.
	call := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, ps.app.JoinPath("/api/v1/me").String(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	parts := strings.Split(access, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	claims["aud"] = "pagerarchive"
	changed, _ := json.Marshal(claims)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(changed) + "." + parts[2]
	if a, b := call(access), call(tampered); a != http.StatusOK || b != http.StatusUnauthorized {
		t.Errorf("real token %d, tampered audience %d", a, b)
	}

	role("user")
	if out := w.do(`Get-PagerArchiveTemplate`); !hasLine(out, "ERROR powershell_admins_only,Get-PagerArchiveTemplate PowerShell access is for admins.") {
		t.Errorf("C demoted: %q", out)
	}
	role("admin")
	if out := w.do(`(Get-PagerArchiveTemplate).Count -ge 0`); !hasLine(out, "True") {
		t.Errorf("C admin again: %q", out)
	}
	if code, msg := owner.api(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", cid), map[string]any{}, nil); code >= 300 {
		t.Fatalf("deactivate C: %d %s", code, msg)
	}
	if out := w.do(`Get-PagerArchiveTemplate`); !hasLine(out, "ERROR deactivated,Get-PagerArchiveTemplate") {
		t.Errorf("C deactivated: %q", out)
	}
	if code, e := ps.renew(t, refresh); code != http.StatusBadRequest || e != "invalid_grant" {
		t.Errorf("deactivated C's sign-in renews: %d %s", code, e)
	}
	t.Logf("A53: C demoted, refused; admin again, works; deactivated, refused and can't renew")
}

// A54, A20 and A21 through the module: the import's rules, -WhatIf,
// -Update and history, and Export-Csv round trips.
func TestPowerShellA54Import(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)
	w := ps.window(t, ps.psHome(t))
	ps.connect(w, owner, "")
	p := "03" + ps.prefix + ps.run // capcodes 03PRRxx, unique to this run
	csv := filepath.Join(w.dir, "a21.csv")
	write := func(name, content string) string {
		f := filepath.Join(w.dir, name)
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	write("a21-first.csv", fmt.Sprintf("id,address,alias,agency,icon,color,ignore\n1,%s01,Brigade,WX,,,0\n", p))
	write("a21.csv", fmt.Sprintf("id,address,alias,agency,icon,color,pluginconf,ignore\n1,%s01,Brigade North,WX,,,{\"apikey\":\"S3CRET\"},0\n2,%s02,Tanker,WX,,,,0\n", p, p))
	type result struct {
		Index   int
		Result  string
		Pattern string
		Changes []string
		Applied bool
	}
	run := func(script string) ([]result, []string) {
		out := w.do(script)
		return jsonLines[result](t, out), out
	}
	if r, out := run(`Import-Csv -Path '` + filepath.Join(w.dir, "a21-first.csv") + `' | Import-PagerArchiveAlias -Confirm:$false`); len(r) != 1 || r[0].Result != "Added" {
		t.Fatalf("first import: %q", out)
	}
	// Without -Update: "different", and nothing changes; new capcodes are added.
	r, out := run(`Import-Csv -Path '` + csv + `' | Import-PagerArchiveAlias -WhatIf`)
	if len(r) != 2 || r[0].Result != "Different" || r[0].Applied || r[1].Result != "Added" || r[1].Applied {
		t.Errorf("-WhatIf: %q", out)
	}
	if aliases := w.do(`(Get-PagerArchiveAlias -Pattern '` + p + `*').Name -join ','`); strings.Join(aliases, "") != "Brigade" {
		t.Errorf("-WhatIf changed something: %q", aliases)
	}
	t.Logf("A54: -WhatIf: %q", out)
	r, out = run(`Import-Csv -Path '` + csv + `' | Import-PagerArchiveAlias -Confirm:$false`)
	if len(r) != 2 || r[0].Result != "Different" || strings.Join(r[0].Changes, "") != `name: "Brigade" → "Brigade North"` || !r[0].Applied || r[1].Result != "Added" {
		t.Errorf("A21 without -Update: %q", out)
	}
	if aliases := w.do(`(Get-PagerArchiveAlias -Pattern '` + p + `01').Name`); strings.Join(aliases, "") != "Brigade" {
		t.Errorf("A21: the import changed an existing alias: %q", aliases)
	}
	t.Logf("A21/A54: without -Update: %q", out)
	r, out = run(`Import-Csv -Path '` + csv + `' | Import-PagerArchiveAlias -Update -Confirm:$false`)
	if len(r) != 2 || r[0].Result != "Updated" || r[1].Result != "Unchanged" {
		t.Errorf("A21 with -Update: %q", out)
	}
	ids := w.do(`(Get-PagerArchiveAlias -Pattern '` + p + `01' | ForEach-Object { "$($_.Name)|$($_.Id)" })`)
	if len(ids) != 1 || !strings.HasPrefix(ids[0], "Brigade North|") {
		t.Fatalf("after -Update: %q", ids)
	}
	var view struct {
		History []struct {
			Source string `json:"source"`
			MadeBy string `json:"made_by"`
		} `json:"history"`
	}
	if code, msg := owner.api(http.MethodGet, "/api/v1/admin/aliases/"+strings.SplitN(ids[0], "|", 2)[1], nil, &view); code != http.StatusOK {
		t.Fatalf("alias view: %d %s", code, msg)
	}
	if len(view.History) == 0 || !strings.Contains(view.History[0].MadeBy, "owner@example.org") {
		t.Errorf("history: %+v", view.History)
	}
	t.Logf("A54: history made by %q", view.History[0].MadeBy)

	// Export-Csv, then import that file: every result "unchanged".
	exported := filepath.Join(w.dir, "export.csv")
	r, out = run(`Get-PagerArchiveAlias | Export-Csv -Path '` + exported + `'
Import-Csv -Path '` + exported + `' | Import-PagerArchiveAlias -Confirm:$false`)
	unchanged := 0
	for _, x := range r {
		if x.Result == "Unchanged" {
			unchanged++
		}
	}
	if len(r) == 0 || unchanged != len(r) || hasLine(out, "ERROR") {
		t.Errorf("export round trip: %d of %d unchanged: %q", unchanged, len(r), out)
	}
	t.Logf("A54: Export-Csv then Import-Csv | Import-PagerArchiveAlias: %d of %d unchanged", unchanged, len(r))

	// A20: two rows for one capcode that differ stop the import.
	a20 := write("a20.csv", fmt.Sprintf("id,address,alias,agency\n10,%s11,Brigade (old name),WX\n11,%s12,Tanker 2,WX\n12,%s,Brigade 2,WX\n", p, p, strings.TrimLeft(p, "0")+"11"))
	r, out = run(`Import-Csv -Path '` + a20 + `' | Import-PagerArchiveAlias -Confirm:$false`)
	conflicts := 0
	for _, l := range out {
		if strings.HasPrefix(l, "ERROR import_conflict,Import-PagerArchiveAlias") {
			conflicts++
		}
	}
	if len(r) != 0 || conflicts != 2 {
		t.Errorf("A20: %q", out)
	}
	if aliases := w.do(`@(Get-PagerArchiveAlias -Pattern '` + p + `1*').Count`); strings.Join(aliases, "") != "0" {
		t.Errorf("A20: a stopped import changed something: %q", aliases)
	}
	t.Logf("A20: %q", out)
	r, out = run(`Import-Csv -Path '` + a20 + `' | Where-Object -Property id -NE '10' | Import-PagerArchiveAlias -Confirm:$false`)
	if len(r) != 2 || r[0].Result != "Added" || r[1].Result != "Added" {
		t.Errorf("A20 with Where-Object: %q", out)
	}
	if aliases := w.do(`(Get-PagerArchiveAlias -Pattern '` + p + `11').Name`); strings.Join(aliases, "") != "Brigade 2" {
		t.Errorf("A20: the kept row's alias: %q", aliases)
	}
	same := write("same.csv", fmt.Sprintf("id,address,alias,agency,color,ignore\n20,%s13,Weather,WX,#1565c0,0\n21,%s13,Weather,WX,#1565c0,0\n", p, p))
	r, out = run(`Import-Csv -Path '` + same + `' | Import-PagerArchiveAlias -Confirm:$false`)
	if len(r) != 2 || r[0].Result != "Added" || r[1].Result != "Squashed" {
		t.Errorf("A20 identical rows: %q", out)
	}
	t.Logf("A20: identical rows: %q", out)
}

// A55. Messages from PowerShell: filters, one request for piped aliases,
// Export-Csv.
func TestPowerShellA55Messages(t *testing.T) {
	ps := setupPS(t)
	key := os.Getenv("PA_STACK_CLIENT_KEY")
	if key == "" {
		t.Fatal("PA_STACK_CLIENT_KEY is not set")
	}
	owner := ps.owner(t)
	w := ps.window(t, ps.psHome(t))
	ps.connect(w, owner, "")
	exampleFire(t, w)
	if out := w.do(`foreach ($n in 1, 2) {
    if (-not (Get-PagerArchiveAlias -Pattern "010000$n")) { $null = New-PagerArchiveAlias -Pattern "010000$n" -Name "Station $n" -Template 'Example Fire' }
}`); len(out) != 0 {
		t.Fatalf("aliases: %q", out)
	}
	post := func(capcode, text string) {
		body := fmt.Sprintf(`{"capcode":%q,"text":%q,"received_at":%q}`, capcode, text, time.Now().UTC().Format(time.RFC3339Nano))
		req, _ := http.NewRequest(http.MethodPost, ps.app.JoinPath("/api/v1/pages").String(), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post page: %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	run := ps.prefix + fmt.Sprint(time.Now().Unix()%100000)
	post("0100001", "@@EX"+run+"1 HSF1 SMOKE SHED FIRE @ 3 FICTION ST, MADEUPTON #TV1")
	post("0100001", "QDSMOKE admin test "+run)
	post("0100002", "@@EX"+run+"2 HSF1 SMOKE SHED FIRE @ 5 FICTION ST, MADEUPTON #TV2")
	post("0100001", "@@EX"+run+"3 HSF1 SMOKE GRASS FIRE @ 7 FICTION ST, MADEUPTON #TV1")

	// What the browser's API lists is what the module must output.
	var want struct {
		Messages []struct {
			ID int64 `json:"id"`
		} `json:"messages"`
	}
	resp, err := owner.client.Get(ps.app.String() + "/api/v1/messages?capcode=0100001&priority=Emergency&limit=200")
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(resp.Body).Decode(&want)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("browser list: %d %v", resp.StatusCode, err)
	}
	type message struct {
		ID       int64
		Capcode  string
		Alias    string
		Priority string
		Text     string
	}
	got := jsonLines[message](t, w.do(`Get-PagerArchiveMessage -Capcode 0100001 -Priority Emergency | Select-Object -Property Id, Capcode, Alias, Priority, Text`))
	if len(got) != len(want.Messages) || len(got) < 2 {
		t.Fatalf("A55: %d messages, the browser lists %d", len(got), len(want.Messages))
	}
	for i, m := range got {
		if m.ID != want.Messages[i].ID || m.Capcode != "0100001" || m.Priority != "Emergency" || m.Alias != "Station 1" || strings.HasPrefix(m.Text, "@@") {
			t.Errorf("A55: message %d: %+v", i, m)
		}
	}
	if !strings.Contains(got[0].Text, "EX"+run+"3 HSF1 SMOKE GRASS FIRE @ 7 FICTION ST, MADEUPTON #TV1") {
		t.Errorf("A55: newest first, in full: %q", got[0].Text)
	}
	t.Logf("A55: -Capcode 0100001 -Priority Emergency: %d messages, newest first, as the browser lists them", len(got))

	out := w.do(`Get-PaAlias -Name 'Station*' | Get-PaMessage -First 2 -Verbose | Select-Object -Property Id, Alias`)
	requests := 0
	for _, l := range out {
		if strings.HasPrefix(l, "VERBOSE GET /api/v1/messages") {
			requests++
		}
	}
	if n := len(jsonLines[message](t, out)); n != 2 || requests != 1 {
		t.Errorf("A55: piped aliases: %d messages, %d requests for messages: %q", n, requests, out)
	}
	t.Logf("A55: Get-PaAlias -Name 'Station*' | Get-PaMessage -First 2: 2 messages, %d request", requests)

	exported := filepath.Join(w.dir, "messages.csv")
	out = w.do(`Get-PagerArchiveMessage -Capcode 0100001 -First 3 | Export-Csv -Path '` + exported + `'
Import-Csv -Path '` + exported + `' | Select-Object -First 1 | ForEach-Object { [bool]$_.Text; [bool]$_.SentAt; $_.Alias }`)
	if strings.Join(out, ",") != "True,True,Station 1" {
		t.Errorf("A55: Export-Csv: %q", out)
	}
}
