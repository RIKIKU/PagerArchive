// Package radiotest checks the client image's radio pipeline against a
// stand-in for rtl_fm (internal/radio/standin): A153. It needs Docker and
// the client image, so it runs in CI's images job; elsewhere it's skipped.
package radiotest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

// server is a stand-in for the PagerArchive server: it takes every page.
type server struct {
	mu    sync.Mutex
	pages []time.Time // when each arrived
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Capcode, Text string
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/pages" || json.NewDecoder(r.Body).Decode(&p) != nil || p.Text == "" {
		http.Error(w, "no", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.pages = append(s.pages, time.Now())
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"outcome":"stored"}`))
}

// since is how many pages arrived after t.
func (s *server) since(t time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, at := range s.pages {
		if at.After(t) {
			n++
		}
	}
	return n
}

type client struct {
	t         *testing.T
	container string
	dir       string
}

func (c *client) control(name string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, name), nil, 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) remove(name string) {
	c.t.Helper()
	if err := os.Remove(filepath.Join(c.dir, name)); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) starts() int {
	b, _ := os.ReadFile(filepath.Join(c.dir, "starts"))
	return strings.Count(string(b), "\n")
}

func (c *client) inspect(format string) string {
	out, _ := exec.Command("docker", "inspect", "-f", format, c.container).Output()
	return strings.TrimSpace(string(out))
}

func (c *client) restartCount() int {
	n, _ := strconv.Atoi(c.inspect("{{.RestartCount}}"))
	return n
}

func (c *client) logs() string {
	out, _ := exec.Command("docker", "logs", c.container).CombinedOutput()
	return string(out)
}

// processes is the container's processes: their states by command.
func (c *client) processes() map[string][]string {
	out, err := exec.Command("docker", "top", c.container, "-eo", "pid,stat,comm").Output()
	got := map[string][]string{}
	if err != nil {
		return got
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n")[1:] {
		f := strings.Fields(l)
		if len(f) == 3 {
			got[f[2]] = append(got[f[2]], f[1])
		}
	}
	return got
}

// oneEach: one rtl_fm and one multimon-ng, and nothing defunct.
func (c *client) oneEach() (bool, map[string][]string) {
	ps := c.processes()
	for _, stats := range ps {
		for _, s := range stats {
			if strings.HasPrefix(s, "Z") {
				return false, ps
			}
		}
	}
	return len(ps["rtl_fm"]) == 1 && len(ps["multimon-ng"]) == 1, ps
}

func waitFor(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return f()
}

// A153. The client recovers its radio by itself (R13b): the client image,
// rtl_fm replaced by the stand-in, Docker's restart policy on, posting to
// a stand-in server and exporting to a test collector. A quiet channel
// restarts nothing; a hang after "cb transfer status", a stall with nothing
// said, a dongle missing for a moment and one missing for longer each end
// with pages flowing again, the last by exiting so Docker starts it afresh,
// once when the dongle that worked is lost and once more when it's back
// but can't be opened. After each recovery one rtl_fm and one multimon-ng
// run and nothing is defunct; the restarts are counted by reason, and the
// silence across restarts.
func TestA153ClientRecoversItsRadio(t *testing.T) {
	image := os.Getenv("PA_RADIO_CLIENT_IMAGE")
	if image == "" {
		t.Skip("PA_RADIO_CLIENT_IMAGE is not set: CI's images job runs this against the client image")
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "rtl_fm"), "../radio/standin")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the stand-in: %v\n%s", err, out)
	}
	srv := &server{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, srv)
	defer l.Close()
	col := telemetrytest.New()
	defer col.Close()

	dir, err := os.MkdirTemp("", "a153-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := &client{t: t, container: fmt.Sprintf("pa-a153-%d", time.Now().UnixNano()), dir: dir}
	c.control("quiet") // a quiet channel first: hiss, no pages
	args := []string{"run", "-d", "--name", c.container, "--network", "host", "--restart", "on-failure:5",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "SERVER_URL=http://" + l.Addr().String(), "-e", "CLIENT_ID=a153-receiver", "-e", "CLIENT_KEY=a153-key",
		"-e", "RADIO_FREQUENCY=148.8125M", "-e", "STANDIN_EVERY=5s",
		"-e", "OTEL_EXPORTER_OTLP_ENDPOINT=" + col.URL, "-e", "OTEL_METRIC_EXPORT_INTERVAL=2000",
		"-v", filepath.Join(bin, "rtl_fm") + ":/usr/local/bin/rtl_fm:ro", "-v", dir + ":/run/rtl_fm-standin", image}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	defer func() {
		if t.Failed() {
			t.Logf("the client's log:\n%s", c.logs())
		}
		_ = exec.Command("docker", "rm", "-f", c.container).Run()
	}()

	// recovered: rtl_fm started again (starts past before), one of each
	// runs, and a page reaches the server.
	recovered := func(what string, from time.Time, before int, within time.Duration) {
		t.Helper()
		if !waitFor(within, func() bool { return c.starts() > before }) {
			t.Fatalf("%s: rtl_fm wasn't started again within %v (%d starts)", what, within, c.starts())
		}
		restartedAt := time.Now()
		restarted := restartedAt.Sub(from)
		if !waitFor(20*time.Second, func() bool { return srv.since(restartedAt) > 0 }) {
			t.Fatalf("%s: no page reached the server within 20 s of rtl_fm starting again", what)
		}
		paged := time.Since(from)
		var ps map[string][]string
		if !waitFor(5*time.Second, func() bool { ok, p := c.oneEach(); ps = p; return ok }) {
			t.Errorf("%s: the container runs %v, want one rtl_fm and one multimon-ng and nothing defunct", what, ps)
		}
		t.Logf("A153: %s: rtl_fm started again %v later, a page reached the server %v later; running %v",
			what, restarted.Round(100*time.Millisecond), paged.Round(100*time.Millisecond), ps)
	}

	// A quiet channel, 75 s: started once, nothing restarted.
	time.Sleep(75 * time.Second)
	if n := c.starts(); n != 1 {
		t.Errorf("a quiet channel: rtl_fm started %d times in 75 s, want 1", n)
	}
	if strings.Contains(c.logs(), "radio pipeline restarting") {
		t.Errorf("a quiet channel restarted the pipeline")
	}
	if n := srv.since(time.Time{}); n != 0 {
		t.Errorf("a quiet channel sent %d pages", n)
	}
	t.Log("A153: 75 s of a quiet channel: rtl_fm started once, nothing restarted")
	from := time.Now()
	c.remove("quiet")
	if !waitFor(20*time.Second, func() bool { return srv.since(from) > 0 }) {
		t.Fatal("no pages once the channel wasn't quiet")
	}

	// rtl_fm hangs as after a USB dropout, ignoring SIGTERM.
	from, before := time.Now(), c.starts()
	c.control("hang")
	recovered("hang after 15 \"cb transfer status\" lines", from, before, 15*time.Second)
	if !strings.Contains(c.logs(), "radio pipeline didn't stop when asked: killed") {
		t.Error("the hung rtl_fm wasn't killed")
	}

	// The audio stops with nothing said.
	from, before = time.Now(), c.starts()
	c.control("stall")
	recovered("stall with nothing said", from, before, 45*time.Second)

	// The dongle is missing for 5 seconds.
	from, before = time.Now(), c.starts()
	c.control("gone")
	c.control("hang")
	if !waitFor(20*time.Second, func() bool { return c.starts() > before }) {
		t.Fatalf("missing: no failed start within 20 s (%d starts)", c.starts()-before)
	}
	time.Sleep(5 * time.Second) // missing for 5 seconds from its first failed start
	c.remove("gone")
	from = time.Now()
	recovered("missing for 5 seconds", from, before, 30*time.Second)
	if n := c.restartCount(); n != 0 {
		t.Errorf("missing for 5 seconds: Docker restarted the client %d times, want 0", n)
	}
	t.Logf("A153: missing for 5 seconds: %d starts, no exit", c.starts()-before)

	// Missing for longer: the client exits, Docker starts it again, and the
	// new one keeps retrying.
	from = time.Now()
	c.control("gone")
	c.control("hang")
	if !waitFor(90*time.Second, func() bool { return c.restartCount() == 1 }) {
		t.Fatalf("missing for longer: the client didn't exit to be started afresh (restarts %d)", c.restartCount())
	}
	t.Logf("A153: missing: the client exited %v after the dongle went, and Docker started it again", time.Since(from).Round(time.Second))
	time.Sleep(60 * time.Second)
	if n, running := c.restartCount(), c.inspect("{{.State.Running}}"); n != 1 || running != "true" {
		t.Fatalf("still missing: Docker restarted the client %d times (running %s), want 1: the new client retries", n, running)
	}
	if !strings.Contains(c.logs(), "radio pipeline keeps failing to start") {
		t.Error("the client's exit isn't logged")
	}
	// Back, but this container can't open it.
	c.remove("gone")
	c.control("unopenable")
	if !waitFor(90*time.Second, func() bool { return c.restartCount() == 2 }) {
		t.Fatalf("back but unopenable: the client didn't exit again (restarts %d)", c.restartCount())
	}
	from, before = time.Now(), c.starts()
	c.remove("unopenable") // a fresh container opens it
	recovered("back, in a fresh start", from, before, 60*time.Second)
	t.Logf("A153: back but unopenable: the client exited once more and the next one receives")
	time.Sleep(5 * time.Second)
	if n := c.restartCount(); n != 2 {
		t.Errorf("Docker restarted the client %d times, want 2", n)
	}

	// The collector: restarts by reason, and silence past 30 s.
	restarts := map[string]float64{}
	silent := 0.0
	for _, p := range col.Points() {
		switch p.Metric {
		case "pagerarchive.client.radio.restarts":
			for _, kv := range p.Attributes {
				if kv.Key == "reason" && p.Value > restarts[kv.Value.GetStringValue()] {
					restarts[kv.Value.GetStringValue()] = p.Value
				}
			}
		case "pagerarchive.client.radio.silent":
			silent = max(silent, p.Value)
		}
	}
	if restarts["lost_device"] < 1 || restarts["stalled"] < 1 {
		t.Errorf("the collector's restarts by reason: %v, want lost_device and stalled", restarts)
	}
	if silent <= 30 {
		t.Errorf("the collector's longest radio silence: %.0f s, want past 30 s, counted across restarts", silent)
	}
	t.Logf("A153: the collector has restarts by reason (most in one run) %v and radio silent up to %.0f s", restarts, silent)
}
