// Command test-receiver is a stand-in endpoint for trying integrations by
// hand on a test or LAN-only copy, never in a deployment. It accepts webhook
// POSTs on any path, subscribes to an MQTT broker's topics, and lists what
// arrived on a web page that refreshes itself. It holds the last 200 in
// memory only. It shows what it received, page text included, so use it
// with made-up pages (the example profile) only.
//
//	LISTEN=:8097 MQTT_BROKER=tcp://mosquitto:1883 test-receiver
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type event struct {
	At      time.Time
	Kind    string // "webhook" or "mqtt"
	Where   string // path or topic
	Detail  string // headers, or "retained"
	Body    string
	TestRun bool
}

type store struct {
	mu     sync.Mutex
	events []event
	status int // what webhooks are answered with
}

func (s *store) add(e event) {
	var probe struct {
		Test bool `json:"test"`
	}
	if json.Unmarshal([]byte(e.Body), &probe) == nil {
		e.TestRun = probe.Test
	}
	var pretty map[string]any
	if json.Unmarshal([]byte(e.Body), &pretty) == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			e.Body = string(b)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append([]event{e}, s.events...)
	if len(s.events) > 200 {
		s.events = s.events[:200]
	}
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>Test receiver</title>
<style>
:root{--bg:#fff;--fg:#1a1c20;--muted:#555b66;--border:#d7dbe0;--surface:#f6f7f9;--accent:#0b5cad}
@media (prefers-color-scheme:dark){:root{--bg:#121417;--fg:#e8eaed;--muted:#a4abb6;--border:#343941;--surface:#1b1e23;--accent:#7cb7ff}}
body{margin:0 auto;max-width:60rem;padding:0 1rem;background:var(--bg);color:var(--fg);font-family:system-ui,sans-serif}
li{border-bottom:1px solid var(--border);padding:.5rem 0;list-style:none}ul{padding:0}
.meta{color:var(--muted);font-size:.85rem}.kind{font-weight:600;color:var(--accent)}
pre{background:var(--surface);padding:.5rem;overflow-x:auto;font-size:.8rem;white-space:pre-wrap}
</style></head><body>
<h1>Test receiver</h1>
<p class="meta">Webhooks: POST to <code>http://&lt;this host&gt;:{{.Port}}/&lt;any path&gt;</code> (answering HTTP {{.Status}}). MQTT: every topic on <code>{{.Broker}}</code>. Last {{len .Events}} of up to 200, newest first. Refreshes every 3 s.</p>
<ul>{{range .Events}}<li><span class="kind">{{.Kind}}{{if .TestRun}} (test){{end}}</span> <code>{{.Where}}</code>
<span class="meta">{{.At.Format "15:04:05.000"}} {{.Detail}}</span><pre>{{.Body}}</pre></li>{{else}}<li>Nothing yet.</li>{{end}}</ul>
</body></html>`))

func main() {
	listen := getenv("LISTEN", ":8097")
	broker := os.Getenv("MQTT_BROKER")
	s := &store{status: http.StatusOK}

	if broker != "" {
		opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID("test-receiver").
			SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(5 * time.Second).
			SetOnConnectHandler(func(c mqtt.Client) {
				c.Subscribe("#", 1, func(_ mqtt.Client, m mqtt.Message) {
					detail := ""
					if m.Retained() {
						detail = "retained"
					}
					s.add(event{At: time.Now(), Kind: "mqtt", Where: m.Topic(), Detail: detail, Body: string(m.Payload())})
				})
			})
		mqtt.NewClient(opts).Connect()
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			s.mu.Lock()
			data := struct {
				Port, Broker string
				Status       int
				Events       []event
			}{strings.TrimPrefix(listen, ":"), broker, s.status, append([]event(nil), s.events...)}
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = page.Execute(w, data)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var hdrs []string
		for k, v := range r.Header {
			if k == "Content-Type" || k == "User-Agent" || strings.HasPrefix(k, "X-") || k == "Authorization" {
				hdrs = append(hdrs, k+": "+strings.Join(v, ","))
			}
		}
		sort.Strings(hdrs)
		s.add(event{At: time.Now(), Kind: "webhook", Where: r.Method + " " + r.URL.Path, Detail: strings.Join(hdrs, " · "), Body: string(body)})
		s.mu.Lock()
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
		fmt.Fprintln(w, "received")
	})
	// GET /status/503 makes webhooks fail until GET /status/200: for trying
	// retries, "skipped: too old" and pausing.
	http.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		var code int
		if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/status/"), "%d", &code); err != nil || code < 200 || code > 599 {
			http.Error(w, "use /status/200 or e.g. /status/503", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.status = code
		s.mu.Unlock()
		fmt.Fprintf(w, "webhooks are now answered with HTTP %d\n", code)
	})
	log.Printf("test receiver on %s, MQTT %q", listen, broker)
	log.Fatal(http.ListenAndServe(listen, nil))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
