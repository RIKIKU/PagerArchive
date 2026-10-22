package stacktest

// A59 against the real stack: Watch-PaMessage in PowerShell windows signed
// in through real Authentik (ADR 0010 §9). The main watch reaches the app
// through a TCP proxy in this test, which is stopped and started to cut the
// connection for real: its connections are closed, and new ones are
// refused while it's stopped.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tcpProxy forwards connections to the app until stopped.
type tcpProxy struct {
	t      *testing.T
	target string
	addr   string // host:port, the same after a restart
	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]bool
	active atomic.Int32 // client connections open now
}

func newProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	p := &tcpProxy{t: t, target: target, addr: "127.0.0.1:0", conns: map[net.Conn]bool{}}
	p.start()
	t.Cleanup(p.stop)
	return p
}

func (p *tcpProxy) url() string { return "http://" + p.addr }

func (p *tcpProxy) start() {
	p.t.Helper()
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.t.Fatalf("proxy: %v", err)
	}
	p.mu.Lock()
	p.ln, p.addr = ln, ln.Addr().String()
	p.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.pipe(c)
		}
	}()
}

func (p *tcpProxy) track(c net.Conn, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		p.conns[c] = true
	} else {
		delete(p.conns, c)
	}
}

func (p *tcpProxy) pipe(c net.Conn) {
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		c.Close()
		return
	}
	p.track(c, true)
	p.track(up, true)
	p.active.Add(1)
	var once sync.Once
	end := func() {
		once.Do(func() {
			c.Close()
			up.Close()
			p.track(c, false)
			p.track(up, false)
			p.active.Add(-1)
		})
	}
	go func() { _, _ = io.Copy(up, c); end() }()
	_, _ = io.Copy(c, up)
	end()
}

// stop closes the listener and every connection: the watch sees its
// connection drop, and new ones are refused.
func (p *tcpProxy) stop() {
	p.mu.Lock()
	ln := p.ln
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, c := range conns {
		c.Close()
	}
}

// watchLog is a watching window's output, line by line, with when each
// line arrived.
type watchLog struct {
	t     *testing.T
	mu    sync.Mutex
	lines []stamped
	done  chan struct{}
}

type stamped struct {
	at   time.Time
	line string
}

// watch runs script (a watch) in w without waiting; its lines are kept as
// they arrive. The window can be used again once the watch has ended.
func (w *psWindow) watch(script string) *watchLog {
	w.t.Helper()
	w.send(script)
	l := &watchLog{t: w.t, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for line := range w.lines {
			if line == "##END##" {
				return
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			l.mu.Lock()
			l.lines = append(l.lines, stamped{time.Now(), line})
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *watchLog) all() []stamped {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]stamped(nil), l.lines...)
}

// wait waits for a line starting with prefix and returns it.
func (l *watchLog) wait(prefix string, d time.Duration) stamped {
	l.t.Helper()
	deadline := time.Now().Add(d)
	for {
		for _, s := range l.all() {
			if strings.HasPrefix(s.line, prefix) {
				return s
			}
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("no %q line within %v: %q", prefix, d, l.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (l *watchLog) count(prefix string) int {
	n := 0
	for _, s := range l.all() {
		if strings.HasPrefix(s.line, prefix) {
			n++
		}
	}
	return n
}

func (l *watchLog) text() []string {
	var out []string
	for _, s := range l.all() {
		line := s.line
		if len(line) > 160 {
			line = line[:160] + "…"
		}
		out = append(out, line)
	}
	return out
}

// watched is a message the watch output.
type watched struct {
	ID      int64
	Capcode string
	Text    string
	at      time.Time
}

func (l *watchLog) messages() []watched {
	var out []watched
	for _, s := range l.all() {
		if j, ok := strings.CutPrefix(s.line, "JSON "); ok {
			var m watched
			if json.Unmarshal([]byte(j), &m) == nil && m.ID != 0 {
				m.at = s.at
				out = append(out, m)
			}
		}
	}
	return out
}

// ended waits for the watch to end.
func (l *watchLog) ended(d time.Duration) bool {
	select {
	case <-l.done:
		return true
	case <-time.After(d):
		return false
	}
}

// posted is a page a client posted.
type posted struct {
	id   int64
	text string
	at   time.Time // just before posting
}

// post posts a page as the stack's receiving client.
func (ps *psStack) post(t *testing.T, capcode, text string) posted {
	t.Helper()
	key := os.Getenv("PA_STACK_CLIENT_KEY")
	if key == "" {
		t.Fatal("PA_STACK_CLIENT_KEY is not set")
	}
	at := time.Now()
	body := fmt.Sprintf(`{"capcode":%q,"text":%q,"received_at":%q}`, capcode, text, at.UTC().Format(time.RFC3339Nano))
	req, _ := http.NewRequest(http.MethodPost, ps.app.JoinPath("/api/v1/pages").String(), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var pr struct {
		Outcome   string `json:"outcome"`
		MessageID int64  `json:"message_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil || resp.StatusCode != http.StatusOK || pr.Outcome != "stored" {
		t.Fatalf("post page: %d %+v %v", resp.StatusCode, pr, err)
	}
	return posted{id: pr.MessageID, text: text[2:], at: at}
}

// poster posts a page to capcode every 100 ms until stopped.
type poster struct {
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	list []posted
}

func (ps *psStack) startPosting(t *testing.T, capcode, label string) *poster {
	p := &poster{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for n := 1; ; n++ {
			select {
			case <-p.stop:
				return
			default:
			}
			pg := ps.post(t, capcode, fmt.Sprintf("QDA59 %s %s %03d", ps.run, label, n))
			p.mu.Lock()
			p.list = append(p.list, pg)
			p.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return p
}

func (p *poster) finish() []posted {
	close(p.stop)
	<-p.done
	return p.list
}

// exactlyOnce checks that every posted page came out once, in order, and
// returns the slowest post-to-output time.
func exactlyOnce(t *testing.T, what string, pages []posted, got []watched) time.Duration {
	t.Helper()
	byID := map[int64]int{}
	for _, m := range got {
		byID[m.ID]++
	}
	var slowest time.Duration
	for _, p := range pages {
		if byID[p.id] != 1 {
			t.Errorf("%s: page %d (%q) came out %d times", what, p.id, p.text, byID[p.id])
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("%s: message %d came out after %d", what, got[i].ID, got[i-1].ID)
		}
	}
	at := map[int64]time.Time{}
	for _, m := range got {
		at[m.ID] = m.at
	}
	for _, p := range pages {
		if a, ok := at[p.id]; ok && a.Sub(p.at) > slowest {
			slowest = a.Sub(p.at)
		}
	}
	return slowest
}

// accessExpiry is when an access token expires (its exp claim).
func accessExpiry(t *testing.T, access string) time.Time {
	t.Helper()
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		t.Fatal("the access token isn't a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return time.Unix(c.Exp, 0)
}

// revoke revokes a refresh token in Authentik (RFC 7009), as another
// window's Disconnect-PagerArchive does.
func (ps *psStack) revoke(t *testing.T, refresh string) {
	t.Helper()
	resp, err := http.PostForm(ps.authentik.JoinPath("/application/o/revoke/").String(), url.Values{
		"token": {refresh}, "token_type_hint": {"refresh_token"}, "client_id": {"pagerarchive-powershell"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
}

// disableInAuthentik disables a user in Authentik's own API, without the
// app hearing of it (as an Authentik admin might).
func (ps *psStack) disableInAuthentik(t *testing.T, username string) {
	t.Helper()
	call := func(method, path, query string, body any, out any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, ps.authentik.JoinPath(path).String()+query, rd)
		req.Header.Set("Authorization", "Bearer "+ps.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("authentik %s %s: %d", method, path, resp.StatusCode)
		}
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
	}
	var users struct {
		Results []struct {
			PK int64 `json:"pk"`
		} `json:"results"`
	}
	call(http.MethodGet, "/api/v3/core/users/", "?username="+url.QueryEscape(username), nil, &users)
	if len(users.Results) != 1 {
		t.Fatalf("authentik user %s: %d found", username, len(users.Results))
	}
	call(http.MethodPatch, fmt.Sprintf("/api/v3/core/users/%d/", users.Results[0].PK), "", map[string]bool{"is_active": false}, nil)
}

// A59. Watching from PowerShell: only matching pages, within 2 seconds; a
// cut connection is retried with one warning and every page comes out
// once; deactivation and demotion stop it with an error; a token expiry
// mid-watch changes nothing; revocations in Authentik stop it by the next
// expiry; Ctrl+C closes the connection.
func TestPowerShellA59Watch(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)
	w := ps.window(t, ps.psHome(t))
	ps.connect(w, owner, "")
	exampleFire(t, w)
	if out := w.do(`foreach ($n in 1, 2) {
    if (-not (Get-PagerArchiveAlias -Pattern "010000$n")) { $null = New-PagerArchiveAlias -Pattern "010000$n" -Name "Station $n" -Template 'Example Fire' }
}`); len(out) != 0 {
		t.Fatalf("aliases: %q", out)
	}
	stops := map[string]string{} // what was measured, for the log
	watchScript := `Watch-PaMessage -Capcode 0100001 -Verbose -InformationAction Continue`

	// The watchers whose account changes, each an admin with their own window.
	admin := func(tag string) (*browser, int64, *psWindow) {
		email := fmt.Sprintf("w%s-%s-%s@example.com", ps.prefix, tag, ps.run)
		b := ps.inviteAndEnrol(t, owner, email)
		id := ps.userID(t, owner, email)
		if code, msg := owner.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", id), map[string]string{"role": "admin"}, nil); code >= 300 {
			t.Fatalf("make %s an admin: %d %s", email, code, msg)
		}
		win := ps.window(t, ps.psHome(t))
		if out := ps.connect(win, b, ""); !hasLine(out, "JSON ") {
			t.Fatalf("%s connects: %q", email, out)
		}
		return b, id, win
	}
	_, demoteID, demoteWin := admin("demote")
	_, deactivateID, deactivateWin := admin("deactivate")
	disabled, _, disabledWin := admin("disabled")

	// The main watch, through the proxy, and the owner's second sign-in,
	// whose refresh token is revoked.
	proxy := newProxy(t, ps.app.Host)
	main := ps.window(t, ps.psHome(t))
	ps.connectTo(main, owner, proxy.url(), "")
	mainAccess, _ := main.tokens()
	mainExpiry := accessExpiry(t, mainAccess)
	revokeWin := ps.window(t, ps.psHome(t))
	ps.connect(revokeWin, owner, "")
	revokeAccess, revokeRefresh := revokeWin.tokens()
	disabledAccess, _ := disabledWin.tokens()

	logs := map[string]*watchLog{}
	for name, win := range map[string]*psWindow{"main": main, "demote": demoteWin, "deactivate": deactivateWin, "revoke": revokeWin, "disabled": disabledWin} {
		logs[name] = win.watch(watchScript)
	}
	for _, l := range logs {
		l.wait("VERBOSE GET /api/v1/stream (capcode, receptions)", 30*time.Second)
	}
	time.Sleep(500 * time.Millisecond) // every stream is ready
	mainLog := logs["main"]

	// Only the matching page, within 2 seconds.
	match := ps.post(t, "0100001", "QDA59 "+ps.run+" match")
	other := ps.post(t, "0100002", "QDA59 "+ps.run+" other")
	got := mainLog.wait(`JSON {"Id":`+fmt.Sprint(match.id), 2*time.Second)
	latency := got.at.Sub(match.at)
	if latency > 2*time.Second {
		t.Errorf("A59: the page came out %v after it was posted", latency)
	}
	time.Sleep(time.Second)
	for _, m := range mainLog.messages() {
		if m.ID == other.id || m.Capcode != "0100001" {
			t.Errorf("A59: a page to %s came out", m.Capcode)
		}
	}
	t.Logf("A59: -Capcode 0100001: the 0100001 page came out %v after it was posted; the 0100002 page didn't", latency.Round(time.Millisecond))

	// The connection is cut: pages keep arriving before, during and after
	// the outage, and while the watch reconnects and catches up.
	before := len(mainLog.messages())
	feed := ps.startPosting(t, "0100001", "cut")
	time.Sleep(time.Second)
	cut := time.Now()
	proxy.stop()
	mainLog.wait("WARNING Lost the connection to PagerArchive", 5*time.Second)
	time.Sleep(4 * time.Second)
	proxy.start()
	back := mainLog.wait("INFO Connected to PagerArchive again", 40*time.Second)
	time.Sleep(2 * time.Second)
	pages := feed.finish()
	time.Sleep(2 * time.Second)
	slowest := exactlyOnce(t, "A59 cut", pages, mainLog.messages()[before:])
	if n := mainLog.count("WARNING"); n != 1 {
		t.Errorf("A59: %d warnings, want 1: %q", n, mainLog.text())
	}
	retries := mainLog.count("VERBOSE Reconnecting to PagerArchive")
	if retries == 0 {
		t.Errorf("A59: no verbose retry lines")
	}
	t.Logf("A59: connection cut for %v: 1 warning (%q), %d verbose retries, %q; all %d pages posted every 100 ms around the outage came out once, in order (slowest %v, from those posted during the outage)",
		back.at.Sub(cut).Round(100*time.Millisecond), mainLog.wait("WARNING", 0).line, retries, back.line, len(pages), slowest.Round(time.Millisecond))

	// Demotion and deactivation stop their watches with the API's code.
	start := time.Now()
	if code, msg := owner.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", demoteID), map[string]string{"role": "user"}, nil); code >= 300 {
		t.Fatalf("demote: %d %s", code, msg)
	}
	l := logs["demote"].wait("ERROR ", 10*time.Second)
	if !strings.HasPrefix(l.line, "ERROR powershell_admins_only,Watch-PagerArchiveMessage PowerShell access is for admins.") {
		t.Errorf("A59: demoted: %q", l.line)
	}
	stops["demotion"] = l.at.Sub(start).Round(time.Millisecond).String()
	start = time.Now()
	if code, msg := owner.api(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", deactivateID), map[string]any{}, nil); code >= 300 {
		t.Fatalf("deactivate: %d %s", code, msg)
	}
	l = logs["deactivate"].wait("ERROR ", 10*time.Second)
	if !strings.HasPrefix(l.line, "ERROR deactivated,Watch-PagerArchiveMessage") {
		t.Errorf("A59: deactivated: %q", l.line)
	}
	stops["deactivation"] = l.at.Sub(start).Round(time.Millisecond).String()
	for _, name := range []string{"demote", "deactivate"} {
		if !logs[name].ended(5*time.Second) || logs[name].count("WARNING") != 0 || logs[name].count("VERBOSE Reconnecting") != 0 {
			t.Errorf("A59: %s's watch retried or didn't end: %q", name, logs[name].text())
		}
	}
	t.Logf("A59: demoted, the watch stopped %s later (powershell_admins_only); deactivated, %s later (deactivated); neither retried", stops["demotion"], stops["deactivation"])

	// Revoked in Authentik, and disabled in Authentik only: the API never
	// hears of either; each watch stops when its access token expires.
	revoked := time.Now()
	ps.revoke(t, revokeRefresh)
	ps.disableInAuthentik(t, disabled.email)
	disabledAt := time.Now()

	// Ctrl+C, the way the console does it (PowerShell.Stop()), on a watch
	// through its own proxy: no error, and the connection closes.
	ctrlProxy := newProxy(t, ps.app.Host)
	ctrl := ps.window(t, ps.psHome(t))
	ps.connectTo(ctrl, owner, ctrlProxy.url(), "")
	ctrl.send(`$child = [powershell]::Create()
$null = $child.AddScript({
    param($Module, $Connection)
    Import-Module -Name $Module
    & (Get-Module -Name PagerArchive) { param($c) $script:Connection = $c } $Connection
    Watch-PagerArchiveMessage -Capcode 0100002
}).AddParameters(@{ Module = (Join-Path -Path (Get-Module -Name PagerArchive).ModuleBase -ChildPath 'PagerArchive.psd1'); Connection = (& (Get-Module -Name PagerArchive) { $script:Connection }) })
$output = [System.Management.Automation.PSDataCollection[psobject]]::new()
$null = $child.BeginInvoke([System.Management.Automation.PSDataCollection[psobject]]::new(), $output)
$deadline = (Get-Date).AddSeconds(60)
while ($output.Count -lt 1 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 50 }
"GOT $($output.Count)"
$child.Stop()
"STOPPED $($child.InvocationStateInfo.State) errors $($child.Streams.Error.Count)"`)
	for deadline := time.Now().Add(30 * time.Second); ctrlProxy.active.Load() == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("A59: the Ctrl+C watch never connected")
		}
	}
	time.Sleep(500 * time.Millisecond)
	ps.post(t, "0100002", "QDA59 "+ps.run+" ctrl")
	out := ctrl.result()
	if strings.Join(out, ",") != "GOT 1,STOPPED Stopped errors 0" {
		t.Errorf("A59: Ctrl+C: %q", out)
	}
	for deadline := time.Now().Add(3 * time.Second); ctrlProxy.active.Load() != 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Error("A59: the connection stayed open after Ctrl+C")
			break
		}
	}
	t.Logf("A59: Ctrl+C: %q, and the proxy saw the connection close", out)

	// The main watch's access token expires: pages posted every 100 ms
	// across the expiry all come out once, with no warning.
	if wait := time.Until(mainExpiry.Add(-3 * time.Second)); wait > 0 {
		t.Logf("A59: waiting %v for the main watch's access token to expire", wait.Round(time.Second))
		time.Sleep(wait)
	}
	before = len(mainLog.messages())
	warnings := mainLog.count("WARNING")
	streams := mainLog.count("VERBOSE GET /api/v1/stream")
	feed = ps.startPosting(t, "0100001", "expiry")
	time.Sleep(time.Until(mainExpiry.Add(4 * time.Second)))
	pages = feed.finish()
	time.Sleep(2 * time.Second)
	slowest = exactlyOnce(t, "A59 expiry", pages, mainLog.messages()[before:])
	if mainLog.count("WARNING") != warnings || mainLog.count("INFO") != 1 {
		t.Errorf("A59: the token's expiry was noticed: %q", mainLog.text())
	}
	if mainLog.count("VERBOSE GET /api/v1/stream") != streams+1 {
		t.Errorf("A59: the watch didn't reconnect once at the expiry: %q", mainLog.text())
	}
	if slowest > 2*time.Second {
		t.Errorf("A59: a page came out %v after it was posted, across the expiry", slowest)
	}
	t.Logf("A59: the access token expired at %s: the watch renewed and reconnected once, quietly; %d pages posted every 100 ms across it came out once, the slowest %v after posting",
		mainExpiry.Format("15:04:05"), len(pages), slowest.Round(time.Millisecond))

	// The revoked and the disabled sign-ins stop by their token's expiry.
	for name, at := range map[string]time.Time{"revoke": revoked, "disabled": disabledAt} {
		access := map[string]string{"revoke": revokeAccess, "disabled": disabledAccess}[name]
		expiry := accessExpiry(t, access)
		l := logs[name].wait("ERROR ", time.Until(expiry)+30*time.Second)
		if !strings.HasPrefix(l.line, "ERROR sign_in_ended,Watch-PagerArchiveMessage Your PagerArchive sign-in has ended. Run Connect-PagerArchive.") {
			t.Errorf("A59: %s: %q", name, l.line)
		}
		if logs[name].count("WARNING") != 0 {
			t.Errorf("A59: %s warned: %q", name, logs[name].text())
		}
		stops[name] = fmt.Sprintf("%v after the change, %v after its access token expired", l.at.Sub(at).Round(time.Second), l.at.Sub(expiry).Round(time.Millisecond))
	}
	t.Logf("A59: refresh token revoked in Authentik: the watch stopped %s (sign_in_ended)", stops["revoke"])
	t.Logf("A59: disabled in Authentik only: the watch stopped %s (sign_in_ended)", stops["disabled"])

	// Every window's lines: no token, and no page text in a warning,
	// verbose, information or error line.
	secrets := []string{mainAccess, revokeAccess, revokeRefresh, disabledAccess}
	names := make([]string, 0, len(logs))
	for name := range logs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, s := range logs[name].all() {
			for _, secret := range secrets {
				if strings.Contains(s.line, secret) {
					t.Errorf("A59: %s's output holds a token", name)
				}
			}
			if !strings.HasPrefix(s.line, "JSON ") && strings.Contains(s.line, "A59 "+ps.run) {
				t.Errorf("A59: %s: page text in %.60s", name, s.line)
			}
		}
	}
}
