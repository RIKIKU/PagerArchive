package acceptance

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// row135 names two made-up fireground channels, one with a leading zero.
const row135 = "@@EX0000135 GRS3 GRASS FIRE FGND 041 FGND 7 @ SAMPLE RD, MADEUPTON #STN1"

// A135. Fireground links (owner, 2026-11-24; R5, R6, R92, R92a; #106), at
// the API level: the numbers stored with a page and on its message, never
// in a payload; a changed pattern re-parses every page as a long job,
// sending nothing; and meta's radioarchive_url only when it's set. (The
// extraction is in internal/pipeline, the golden column in internal/golden,
// validate-profile's compile check in internal/profile, the setting's
// check in cmd/pagerarchive and the chips in web/tests/e2e.spec.ts.)
func TestA135FiregroundLinks(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()

	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, webhookTo("Station 1, Full", hook, "0100001"))
	id := e.mustPost("a", "0100001", row135, e.now).MessageID
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) >= 1 }) {
		t.Fatal("no webhook request within 5 s")
	}

	// The API's message, and what's stored.
	byJob := func(job string) server.Message {
		t.Helper()
		if l := e.list("job=" + job); len(l) > 0 {
			return l[0]
		}
		t.Fatalf("no message on %s", job)
		return server.Message{}
	}
	m := byJob("EX0000135")
	if m.ID != id || !slices.Equal(m.FiregroundChannels, []int32{41, 7}) || m.DisplayText != strings.TrimPrefix(row135, "@@") {
		t.Errorf("the message: id %d, fireground_channels %v, text %q", m.ID, m.FiregroundChannels, m.DisplayText)
	}
	var stored []int32
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT fireground_channels FROM messages WHERE id = $1", id).Scan(&stored); err != nil || !slices.Equal(stored, []int32{41, 7}) {
		t.Errorf("stored fireground_channels %v (%v)", stored, err)
	}
	// A page naming none has null.
	e.mustPost("a", "0100002", "@@EX0000136 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now)
	if other := byJob("EX0000136"); other.FiregroundChannels != nil {
		t.Errorf("a page with none: %v", other.FiregroundChannels)
	}
	// The raw JSON names the field on every message.
	if code, body := e.rawGet("/api/v1/messages?job=EX0000136"); code != http.StatusOK || !strings.Contains(body, `"fireground_channels":null`) {
		t.Errorf("the API's JSON (%d): %s", code, body)
	}

	// No payload carries them.
	for _, req := range hook.Requests() {
		if strings.Contains(string(req.Body), "fireground") {
			t.Errorf("a payload names fireground channels: %s", req.Body)
		}
	}

	// The pattern changes and the API starts: every page is re-parsed, as a
	// long job, and nothing is sent; the numbers follow the new pattern.
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "example-network.json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(b), `"fireground_channel_pattern": "\\bFGND ?(?P<number>[0-9A-Z]+)\\b"`, `"fireground_channel_pattern": "\\bFGND (?P<number>[0-9]{2,})\\b"`, 1)
	if changed == string(b) {
		t.Fatal("the example network profile's pattern wasn't found")
	}
	if err := os.WriteFile(file, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	hash, legacy, err := reparse.ProfileHashes(set, n)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // anything the posts would send has been sent
	sent := len(hook.Requests())
	e.worker.Network, e.worker.Agencies = n, set.Agencies
	e.worker.ProfileHash, e.worker.LegacyProfileHash = hash, legacy
	if err := e.worker.CheckProfiles(e.ctx); err != nil {
		t.Fatal(err)
	}
	re, err := e.worker.RunOnce(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if re != 2 {
		t.Errorf("the pattern changed: re-parsed %d, want every page (2)", re)
	}
	var state string
	var total, done int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT state, total, done FROM reparse_runs").Scan(&state, &total, &done); err != nil || state != "finished" || total != 2 || done != 2 {
		t.Errorf("the re-parse's run: %s, %d of %d (%v)", state, done, total, err)
	}
	if got := byJob("EX0000135").FiregroundChannels; !slices.Equal(got, []int32{41}) {
		t.Errorf("re-parsed with the new pattern: %v, want [41]", got)
	}
	time.Sleep(time.Second)
	if got := len(hook.Requests()); got != sent {
		t.Errorf("the re-parse sent %d requests", got-sent)
	}

	// Meta: radioarchive_url only when it's set.
	if code, body := e.rawGet("/api/v1/meta"); code != http.StatusOK || strings.Contains(body, "radioarchive_url") {
		t.Errorf("standalone meta (%d) names the radio: %s", code, body)
	}
	e.srv.RadioArchiveURL = "https://radio.example.test"
	var meta server.Meta
	if code, _ := e.user.call(http.MethodGet, "/api/v1/meta", nil, &meta); code != http.StatusOK || meta.RadioArchiveURL != "https://radio.example.test" {
		t.Errorf("meta with RADIOARCHIVE_URL set (%d): %q", code, meta.RadioArchiveURL)
	}
	t.Logf("A135: fireground_channels %v stored and in the API (null for none), in no payload; a changed pattern re-parsed both pages (%s, %d of %d), sent nothing, and gave [41]; meta's radioarchive_url %q only when set",
		m.FiregroundChannels, state, done, total, meta.RadioArchiveURL)
}

// rawGet is the user's GET of path: its status and body.
func (e *env) rawGet(path string) (int, string) {
	e.t.Helper()
	resp, err := e.user.do(http.MethodGet, e.url+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	if _, err := io.Copy(&b, resp.Body); err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, b.String()
}
