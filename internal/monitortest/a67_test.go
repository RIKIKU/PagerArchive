package monitortest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// grafanaGet is a GET through Grafana as the admin, decoded into out.
func (s *stack) grafanaGet(t *testing.T, path string, out any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.grafana+path, nil)
	req.SetBasicAuth("admin", s.grafanaPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// values asks Prometheus for an instant vector and returns each series'
// value by the given label.
func (s *stack) values(t *testing.T, promql, label string) map[string]float64 {
	t.Helper()
	var out struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	s.grafanaGet(t, "/api/datasources/proxy/uid/prometheus/api/v1/query?query="+url.QueryEscape(promql), &out)
	m := map[string]float64{}
	for _, r := range out.Data.Result {
		if v, ok := r.Value[1].(string); ok {
			m[r.Metric[label]], _ = strconv.ParseFloat(v, 64)
		}
	}
	return m
}

// panelValues runs a dashboard panel's queries as Grafana does for the
// panel (time range now-6h to now) and returns each query's value by
// client: refId -> instance -> value.
func (s *stack) panelValues(t *testing.T, dashboard, title string) map[string]map[string]float64 {
	t.Helper()
	var d struct {
		Dashboard struct {
			Panels []struct {
				Title   string            `json:"title"`
				Targets []json.RawMessage `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	s.grafanaGet(t, "/api/dashboards/uid/"+dashboard, &d)
	var targets []json.RawMessage
	for _, p := range d.Dashboard.Panels {
		if p.Title == title {
			targets = p.Targets
		}
	}
	if targets == nil {
		t.Fatalf("dashboard %s has no panel %q", dashboard, title)
	}
	body, _ := json.Marshal(map[string]any{"queries": targets, "from": "now-6h", "to": "now"})
	req, _ := http.NewRequest(http.MethodPost, s.grafana+"/api/ds/query", bytes.NewReader(body))
	req.SetBasicAuth("admin", s.grafanaPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Results map[string]struct {
			Error  string `json:"error"`
			Frames []struct {
				Schema struct {
					Fields []struct {
						Name   string            `json:"name"`
						Labels map[string]string `json:"labels"`
					} `json:"fields"`
				} `json:"schema"`
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("panel %q: %v: %s", title, err, b)
	}
	got := map[string]map[string]float64{}
	for ref, r := range out.Results {
		if r.Error != "" {
			t.Errorf("panel %q query %s: %s", title, ref, r.Error)
		}
		got[ref] = map[string]float64{}
		// One frame per series: its value field carries the series' labels
		// (Grafana's browser turns these into the table's rows).
		for _, f := range r.Frames {
			for i, fd := range f.Schema.Fields {
				inst := fd.Labels["instance"]
				if fd.Name == "Time" || inst == "" || i >= len(f.Data.Values) {
					continue
				}
				for _, v := range f.Data.Values[i] {
					if n, ok := v.(float64); ok {
						got[ref][inst] = n
					}
				}
			}
		}
	}
	return got
}

// radioClient is a receiving client in the client image, run on
// PagerArchive's network, with A67's stand-in as rtl_fm (or, with no dir,
// reading stdin that never ends: a client without a radio).
type radioClient struct {
	id, container, dir string
}

func (s *stack) startClient(t *testing.T, id, key string, radio bool) *radioClient {
	t.Helper()
	c := &radioClient{id: id, container: "pa-mon-" + id}
	args := []string{"run", "-d", "--name", c.container, "--network", os.Getenv("PA_MON_NETWORK"), "--label", os.Getenv("PA_MON_LABEL"),
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "SERVER_URL=http://" + os.Getenv("PA_MON_API_ADDRESS"), "-e", "CLIENT_ID=" + id, "-e", "CLIENT_KEY=" + key,
		"-e", "OTEL_EXPORTER_OTLP_ENDPOINT=http://alloy:4318", "-e", "OTEL_METRIC_EXPORT_INTERVAL=10000",
		"-e", "OTEL_BSP_SCHEDULE_DELAY=1000", "-e", "OTEL_BLRP_SCHEDULE_DELAY=1000"}
	if radio {
		dir, err := os.MkdirTemp("", "a67-"+id+"-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		c.dir = dir
		args = append(args, "-e", "RADIO_FREQUENCY=148.8125M", "-e", "STANDIN_EVERY=10s",
			"-v", os.Getenv("PA_MON_STANDIN")+":/usr/local/bin/rtl_fm:ro", "-v", dir+":/run/rtl_fm-standin",
			os.Getenv("PA_MON_CLIENT_IMAGE"))
	} else {
		args = append(args, "--entrypoint", "sh", os.Getenv("PA_MON_CLIENT_IMAGE"), "-c", "tail -f /dev/null | /usr/local/bin/pagerarchive-client")
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v\n%s", id, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "logs", "--tail", "15", c.container).CombinedOutput(); err == nil && t.Failed() {
			t.Logf("%s's last log lines:\n%s", id, out)
		}
		_ = exec.Command("docker", "rm", "-f", c.container).Run()
	})
	return c
}

func (c *radioClient) file(t *testing.T, name string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(c.dir, name))
	return string(b)
}

func (c *radioClient) control(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A67. Told when a radio stalls (owner, 2026-10-31): two clients run the
// radio pipeline in the client image, rtl_fm replaced by a stand-in that
// writes audio holding made-up POCSAG pages and rtl_fm's messages, and a
// third reads stdin. The clients report audio per second matching what the
// stand-in wrote, and a level, and multimon-ng decodes every page; rtl_fm's
// messages are counted by kind, their text never reaching the collector;
// audio stopping fires "Radio stalled" for that client only within 4
// minutes, and not "Client not receiving"; audio resuming resolves it; once
// the client stops reporting, the clients dashboard still shows its last
// values and their age. No audio reaches the collector.
func TestA67ToldWhenARadioStalls(t *testing.T) {
	s := newStack(t)
	if os.Getenv("PA_MON_CLIENT_IMAGE") == "" || os.Getenv("PA_MON_STANDIN") == "" {
		t.Fatal("PA_MON_CLIENT_IMAGE and PA_MON_STANDIN are needed: run scripts/monitoring-checks.sh")
	}
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	run := time.Now().Format("150405")
	a, b, c := "radio-a-"+run, "radio-b-"+run, "stdin-c-"+run
	keys := map[string]string{}
	for _, id := range []string{a, b, c} {
		key, err := s.st.AddClient(context.Background(), id, "A67 "+id)
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = key
		s.post(t, id, key, "QDA67 "+id+" is receiving")
	}
	ra := s.startClient(t, a, keys[a], true)
	rb := s.startClient(t, b, keys[b], true)
	s.startClient(t, c, keys[c], false)
	started := time.Now()

	// A minute of audio.
	time.Sleep(70 * time.Second)
	if args := ra.file(t, "args"); strings.Contains(" "+args, " -l ") || !strings.Contains(args, "-M fm -s 22050") {
		t.Errorf("rtl_fm was started with %q: want -M fm -s 22050 and no squelch (-l)", strings.TrimSpace(args))
	}
	var written, secs float64
	fmt.Sscan(ra.file(t, "written"), &written, &secs)
	standin := written / secs
	var audio, level map[string]float64
	if !waitFor(60*time.Second, func() bool {
		audio = s.values(t, fmt.Sprintf(`rate(pagerarchive_client_radio_audio_bytes_total{instance="%s"}[1m])`, a), "instance")
		level = s.values(t, fmt.Sprintf(`pagerarchive_client_radio_level{instance="%s"}`, a), "instance")
		return audio[a] > 0 && len(level) == 1
	}) {
		t.Fatalf("no audio rate or level for %s in Prometheus: %v %v", a, audio, level)
	}
	if math.Abs(audio[a]-standin)/standin > 0.05 {
		t.Errorf("%s reports %.0f bytes of audio a second; the stand-in wrote %.0f (%.0f bytes in %.1f s)", a, audio[a], standin, written, secs)
	}
	if level[a] > 0 || level[a] < -60 {
		t.Errorf("%s's audio level %.1f dBFS: want the stand-in's hiss, between -60 and 0", a, level[a])
	}
	t.Logf("A67: %s reports %.0f bytes of audio a second (the stand-in wrote %.0f: %.0f bytes in %.1f s) at %.1f dBFS",
		a, audio[a], standin, written, secs, level[a])

	// multimon-ng decoded every page the stand-in wrote: each reached the
	// archive from this client.
	pages := strings.Split(strings.TrimSpace(ra.file(t, "pages")), "\n")
	if len(pages) < 4 {
		t.Fatalf("the stand-in wrote %d pages in a minute, want 4 or more: %q", len(pages), pages)
	}
	time.Sleep(5 * time.Second)
	var missing []string
	for _, p := range pages {
		capcode, text, _ := strings.Cut(p, " ")
		var n int
		if err := s.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM messages m JOIN receptions r ON r.message_id = m.id
			WHERE r.client_id = $1 AND m.capcode = $2 AND strpos(m.text, $3) = 1`, a, capcode, text).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Errorf("multimon-ng didn't decode %d of the %d pages the stand-in wrote: %q", len(missing), len(pages), missing)
	} else {
		t.Logf("A67: multimon-ng decoded all %d pages the stand-in wrote in %v, each stored from %s", len(pages), time.Since(started).Round(time.Second), a)
	}

	// rtl_fm's messages: a failed USB transfer, and one it has never said.
	kinds := func() map[string]float64 {
		return s.values(t, fmt.Sprintf(`pagerarchive_client_radio_messages_total{instance="%s"}`, a), "kind")
	}
	before := kinds()
	unknown := "STANDIN-UNKNOWN-" + run + " device SN 77770001 said something new"
	ra.control(t, "usb-error", "")
	ra.control(t, "message", unknown+"\n")
	var after map[string]float64
	if !waitFor(60*time.Second, func() bool {
		after = kinds()
		return after["usb_transfer_failed"] == before["usb_transfer_failed"]+1 && after["other"] == before["other"]+1
	}) {
		t.Errorf("rtl_fm's messages by kind: %v before, %v after; want usb_transfer_failed and other up by 1", before, after)
	} else {
		t.Logf("A67: rtl_fm's messages by kind went from %v to %v", before, after)
	}

	// Neither message's text, nor any audio, reached the collector: not in
	// Prometheus' labels, Loki's lines or Tempo's spans.
	marker := strings.TrimSpace(ra.file(t, "marker"))
	if marker == "" {
		t.Fatal("the stand-in wrote no audio marker")
	}
	banned := []string{"STANDIN-UNKNOWN", "77770001", "canceling", "cb transfer", marker}
	leaks := s.collectorHas(t, []string{a, b, c}, banned)
	if len(leaks) > 0 {
		t.Errorf("found in the collector's stores: %s", strings.Join(leaks, "; "))
	} else {
		t.Logf("A67: none of %q in Prometheus' series, Loki's lines or Tempo's spans for the three clients", banned)
	}

	// The stand-in stops writing audio but keeps running.
	paused := time.Now()
	ra.control(t, "pause", "")
	firedAt, fired := time.Time{}, false
	if !waitFor(4*time.Minute, func() bool { firedAt, fired = hook.find(paused, "Radio stalled", "firing", a); return fired }) {
		t.Fatalf("no \"Radio stalled\" alert for %s within 4 minutes of its audio stopping; got %d notifications", a, len(hook.got))
	}
	t.Logf("A67: \"Radio stalled\" fired for %s %v after its audio stopped", a, firedAt.Sub(paused).Round(time.Second))
	time.Sleep(35 * time.Second) // one more evaluation, for a wrong one to show
	for _, n := range hook.alerts(paused, "firing") {
		if n.name == "Radio stalled" && n.client != a {
			t.Errorf("\"Radio stalled\" fired for %s too", n.client)
		}
		if n.name == "Client not receiving" && (n.client == a || n.client == b || n.client == c) {
			t.Errorf("\"Client not receiving\" fired for %s", n.client)
		}
	}

	// Audio resumes: the alert resolves.
	resumed := time.Now()
	if err := os.Remove(filepath.Join(ra.dir, "pause")); err != nil {
		t.Fatal(err)
	}
	resolvedAt, resolved := time.Time{}, false
	if !waitFor(3*time.Minute, func() bool {
		resolvedAt, resolved = hook.find(resumed, "Radio stalled", "resolved", a)
		return resolved
	}) {
		t.Errorf("\"Radio stalled\" for %s didn't resolve within 3 minutes of its audio resuming", a)
	} else {
		t.Logf("A67: it resolved %v after the audio resumed", resolvedAt.Sub(resumed).Round(time.Second))
	}
	if rb.file(t, "written") == "" {
		t.Errorf("%s's stand-in wrote nothing", rb.id)
	}

	// Client A stops reporting. Past Prometheus' 5-minute lookback an
	// instant query no longer sees it: the dashboard must still show its
	// last values and how long ago they came.
	stopped := time.Now()
	if out, err := exec.Command("docker", "stop", ra.container).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v\n%s", err, out)
	}
	time.Sleep(6 * time.Minute)
	const panel = "Each client's own view (last reported values in the time range)"
	got := s.panelValues(t, "pa-clients", panel)
	names := map[string]string{"A": "last report", "B": "last page decoded", "C": "last send that worked", "D": "buffered",
		"E": "radio running", "F": "audio", "G": "audio level", "H": "radio silent for"}
	var shown []string
	for _, ref := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		v, ok := got[ref][a]
		if !ok {
			t.Errorf("%s stopped reporting %v ago and the dashboard's %q has no %s for it", a, time.Since(stopped).Round(time.Second), panel, names[ref])
			continue
		}
		shown = append(shown, fmt.Sprintf("%s %.0f", names[ref], v))
	}
	if v := got["A"][a]; v < 300 {
		t.Errorf("%s's last report %.0f s ago: want over 300 s, the time since it stopped", a, v)
	}
	for _, ref := range []string{"E", "F", "G", "H"} {
		if _, ok := got[ref][c]; ok {
			t.Errorf("%s reads stdin but has radio health %s", c, names[ref])
		}
	}
	if _, ok := got["A"][c]; !ok {
		t.Errorf("%s (stdin) isn't on the dashboard", c)
	}
	if !t.Failed() {
		t.Logf("A67: %v after %s stopped, the dashboard shows %s; %s (stdin) has no radio health",
			time.Since(stopped).Round(time.Second), a, strings.Join(shown, ", "), c)
	}
	for _, id := range []string{a, b, c} {
		_, _ = s.st.RevokeClient(context.Background(), id)
	}
}

// collectorHas looks for each banned string in what the collector stored
// for the clients: Prometheus' series (every label), Loki's log lines and
// Tempo's spans.
func (s *stack) collectorHas(t *testing.T, clients, banned []string) []string {
	t.Helper()
	var found []string
	check := func(where, text string) {
		for _, b := range banned {
			if strings.Contains(text, b) {
				found = append(found, fmt.Sprintf("%q in %s", b, where))
			}
		}
	}
	end := time.Now()
	for _, id := range clients {
		var series struct {
			Data []map[string]string `json:"data"`
		}
		s.grafanaGet(t, "/api/datasources/proxy/uid/prometheus/api/v1/series?match[]="+url.QueryEscape(fmt.Sprintf(`{instance="%s"}`, id)), &series)
		if len(series.Data) == 0 {
			found = append(found, "no Prometheus series at all for "+id+" (so nothing checked)")
		}
		b, _ := json.Marshal(series.Data)
		check("Prometheus series for "+id, string(b))

		var logs struct {
			Data struct {
				Result []struct {
					Values [][2]string `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		q := fmt.Sprintf(`{service_name="pagerarchive-client", service_instance_id="%s"}`, id)
		s.grafanaGet(t, fmt.Sprintf("/api/datasources/proxy/uid/loki/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=5000",
			url.QueryEscape(q), end.Add(-time.Hour).UnixNano(), end.UnixNano()), &logs)
		n := 0
		for _, r := range logs.Data.Result {
			for _, v := range r.Values {
				n++
				check("Loki line from "+id, v[1])
			}
		}
		if n == 0 {
			found = append(found, "no Loki lines at all for "+id+" (so nothing checked)")
		}

		var traces struct {
			Traces []struct {
				TraceID string `json:"traceID"`
			} `json:"traces"`
		}
		tq := fmt.Sprintf(`{resource.service.instance.id="%s"}`, id)
		s.grafanaGet(t, fmt.Sprintf("/api/datasources/proxy/uid/tempo/api/search?q=%s&limit=20&start=%d&end=%d",
			url.QueryEscape(tq), end.Add(-time.Hour).Unix(), end.Unix()), &traces)
		for _, tr := range traces.Traces {
			var full json.RawMessage
			s.grafanaGet(t, "/api/datasources/proxy/uid/tempo/api/traces/"+tr.TraceID, &full)
			check("Tempo trace "+tr.TraceID, string(full))
		}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

type alertSeen struct{ name, client string }

// alerts lists the alerts with this status since since.
func (w *webhook) alerts(since time.Time, status string) []alertSeen {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []alertSeen
	for _, n := range w.got {
		if n.at.Before(since) {
			continue
		}
		for _, a := range n.Alerts {
			if a.Status == status {
				out = append(out, alertSeen{a.Labels["alertname"], a.Labels["client"]})
			}
		}
	}
	return out
}
