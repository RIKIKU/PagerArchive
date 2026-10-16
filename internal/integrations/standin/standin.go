// Package standin has stand-in servers for the services PagerArchive's
// presets post to (Discord, Telegram, Pushover, ntfy) and a plain webhook
// receiver. Each checks every request against its service's documented
// format (read 2026-10-16, ADR 0006 §8) and answers as the service does.
//
// Test code only: nothing in the server imports it, and tests never reach
// the real services (CI's dialer refuses anything outside 127.0.0.0/8).
package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Request is one request a stand-in accepted.
type Request struct {
	Method  string
	Path    string
	Header  http.Header
	Body    []byte
	JSON    map[string]any // JSON bodies
	Form    url.Values     // form bodies
	Arrived time.Time
}

// Server is a stand-in.
type Server struct {
	*httptest.Server
	Name string

	mu         sync.Mutex
	accepted   []Request
	violations []string
	status     int // non-zero: answer every request with this status (an endpoint that's failing)
	down       bool
	check      func(r *http.Request, body []byte) (Request, []string)
	ok         func(w http.ResponseWriter, req Request)
}

// Requests is every request accepted so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.accepted...)
}

// Violations is every way a request broke the service's format.
func (s *Server) Violations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

// FailWith answers every request with status (0 goes back to normal).
func (s *Server) FailWith(status int) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

// Down makes the endpoint answer 503, as a reverse proxy does for a
// backend that's down, or come back.
func (s *Server) Down(down bool) {
	s.mu.Lock()
	s.down = down
	s.mu.Unlock()
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	down, status := s.down, s.status
	s.mu.Unlock()
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	req, problems := s.check(r, body)
	req.Method, req.Path, req.Header, req.Body, req.Arrived = r.Method, r.URL.Path, r.Header.Clone(), body, time.Now()
	s.mu.Lock()
	if len(problems) > 0 {
		for _, p := range problems {
			s.violations = append(s.violations, s.Name+": "+p)
		}
	} else {
		s.accepted = append(s.accepted, req)
	}
	s.mu.Unlock()
	if len(problems) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": strings.Join(problems, "; ")})
		return
	}
	s.ok(w, req)
}

func start(name string, check func(*http.Request, []byte) (Request, []string), ok func(http.ResponseWriter, Request)) *Server {
	s := &Server{Name: name, check: check, ok: ok}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func jsonBody(r *http.Request, body []byte) (map[string]any, []string) {
	var probs []string
	if r.Method != http.MethodPost {
		probs = append(probs, "method must be POST, got "+r.Method)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		probs = append(probs, "Content-Type must be application/json, got "+ct)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		probs = append(probs, "body isn't a JSON object")
	}
	return m, probs
}

func str(m map[string]any, k string) (string, bool) {
	v, ok := m[k].(string)
	return v, ok
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// Webhook is a plain receiver: it accepts any JSON POST and answers 200
// with a short body (for Send test to show).
func Webhook() *Server {
	return start("webhook", func(r *http.Request, body []byte) (Request, []string) {
		m, probs := jsonBody(r, body)
		return Request{JSON: m}, probs
	}, func(w http.ResponseWriter, _ Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "received")
	})
}

var discordPath = regexp.MustCompile(`^/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$`)

// Discord is Execute Webhook: POST /api/webhooks/{id}/{token}, JSON, at
// least one of content, embeds, components, file or poll; content ≤ 2,000
// characters; ≤ 10 embeds (title ≤ 256, description ≤ 4,096, footer text
// ≤ 2,048, ≤ 25 fields, 6,000 characters in all); username ≤ 80 and
// without "discord" or "clyde". Answers 204 (no ?wait). PagerArchive also
// requires mentions to be off: allowed_mentions.parse = [].
func Discord() *Server {
	return start("discord", func(r *http.Request, body []byte) (Request, []string) {
		m, probs := jsonBody(r, body)
		if !discordPath.MatchString(r.URL.Path) {
			probs = append(probs, "path must be /api/webhooks/{id}/{token}")
		}
		if m == nil {
			return Request{}, probs
		}
		content, hasContent := str(m, "content")
		embeds, hasEmbeds := m["embeds"].([]any)
		if !(hasContent && content != "") && !(hasEmbeds && len(embeds) > 0) && m["components"] == nil && m["file"] == nil && m["poll"] == nil {
			probs = append(probs, "needs at least one of content, embeds, components, file or poll")
		}
		if runes(content) > 2000 {
			probs = append(probs, "content is longer than 2000 characters")
		}
		if u, ok := str(m, "username"); ok {
			lu := strings.ToLower(u)
			if runes(u) > 80 || strings.Contains(lu, "discord") || strings.Contains(lu, "clyde") {
				probs = append(probs, "username isn't allowed")
			}
		}
		if len(embeds) > 10 {
			probs = append(probs, "more than 10 embeds")
		}
		total := 0
		for _, e := range embeds {
			em, ok := e.(map[string]any)
			if !ok {
				probs = append(probs, "an embed isn't an object")
				continue
			}
			title, _ := str(em, "title")
			desc, _ := str(em, "description")
			total += runes(title) + runes(desc)
			if runes(title) > 256 {
				probs = append(probs, "embed title is longer than 256")
			}
			if runes(desc) > 4096 {
				probs = append(probs, "embed description is longer than 4096")
			}
			if f, ok := em["footer"].(map[string]any); ok {
				t, _ := str(f, "text")
				total += runes(t)
				if runes(t) > 2048 {
					probs = append(probs, "footer text is longer than 2048")
				}
			}
			if fields, ok := em["fields"].([]any); ok && len(fields) > 25 {
				probs = append(probs, "more than 25 fields")
			}
			if ts, ok := str(em, "timestamp"); ok {
				if _, err := time.Parse(time.RFC3339, ts); err != nil {
					probs = append(probs, "timestamp isn't ISO 8601")
				}
			}
			if u, ok := em["url"]; ok {
				if s, ok := u.(string); !ok || !strings.HasPrefix(s, "http") {
					probs = append(probs, "embed url isn't a URL")
				}
			}
		}
		if total > 6000 {
			probs = append(probs, "embeds have more than 6000 characters in all")
		}
		am, ok := m["allowed_mentions"].(map[string]any)
		if parse, isList := am["parse"].([]any); !ok || !isList || len(parse) != 0 {
			probs = append(probs, "allowed_mentions.parse must be [] (mentions off)")
		}
		return Request{JSON: m}, probs
	}, func(w http.ResponseWriter, _ Request) { w.WriteHeader(http.StatusNoContent) })
}

var telegramPath = regexp.MustCompile(`^/bot[0-9]+:[A-Za-z0-9_-]+/sendMessage$`)

// Telegram is the Bot API's sendMessage: POST /bot<token>/sendMessage with
// JSON; chat_id (integer or @channel string) and text (1-4,096 characters)
// required. Answers {"ok": true, "result": Message}.
func Telegram() *Server {
	return start("telegram", func(r *http.Request, body []byte) (Request, []string) {
		m, probs := jsonBody(r, body)
		if !telegramPath.MatchString(r.URL.Path) {
			probs = append(probs, "path must be /bot<token>/sendMessage")
		}
		if m == nil {
			return Request{}, probs
		}
		switch c := m["chat_id"].(type) {
		case string:
			if _, err := strconv.ParseInt(c, 10, 64); err != nil && !strings.HasPrefix(c, "@") {
				probs = append(probs, "chat_id must be an integer or @channelusername")
			}
		case float64:
		default:
			probs = append(probs, "chat_id is required")
		}
		text, ok := str(m, "text")
		if !ok || text == "" || runes(text) > 4096 {
			probs = append(probs, "text must be 1-4096 characters")
		}
		if pm, ok := str(m, "parse_mode"); ok && pm != "" && pm != "HTML" && pm != "Markdown" && pm != "MarkdownV2" {
			probs = append(probs, "unknown parse_mode")
		}
		return Request{JSON: m}, probs
	}, func(w http.ResponseWriter, req Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1, "date": time.Now().Unix(), "chat": map[string]any{"id": req.JSON["chat_id"]}}})
	})
}

var pushoverKey = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)

// Pushover is messages.json: POST /1/messages.json, form-encoded; token
// and user (30 letters and digits) and message (≤ 1,024 characters)
// required; title ≤ 250, url ≤ 512, url_title ≤ 100; priority -2..2, and
// priority 2 needs retry ≥ 30 and expire ≤ 10,800. Answers
// {"status": 1, "request": …}.
func Pushover() *Server {
	return start("pushover", func(r *http.Request, body []byte) (Request, []string) {
		var probs []string
		if r.Method != http.MethodPost {
			probs = append(probs, "method must be POST")
		}
		if r.URL.Path != "/1/messages.json" {
			probs = append(probs, "path must be /1/messages.json")
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			probs = append(probs, "Content-Type must be application/x-www-form-urlencoded")
		}
		f, err := url.ParseQuery(string(body))
		if err != nil {
			return Request{}, append(probs, "body isn't form-encoded")
		}
		if !pushoverKey.MatchString(f.Get("token")) {
			probs = append(probs, "token must be 30 letters and digits")
		}
		if !pushoverKey.MatchString(f.Get("user")) {
			probs = append(probs, "user must be 30 letters and digits")
		}
		if msg := f.Get("message"); msg == "" || runes(msg) > 1024 {
			probs = append(probs, "message must be 1-1024 characters")
		}
		for k, n := range map[string]int{"title": 250, "url": 512, "url_title": 100} {
			if runes(f.Get(k)) > n {
				probs = append(probs, fmt.Sprintf("%s is longer than %d", k, n))
			}
		}
		if p := f.Get("priority"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < -2 || n > 2 {
				probs = append(probs, "priority must be -2 to 2")
			}
			if n == 2 {
				retry, e1 := strconv.Atoi(f.Get("retry"))
				expire, e2 := strconv.Atoi(f.Get("expire"))
				if e1 != nil || e2 != nil || retry < 30 || expire > 10800 || expire < 1 {
					probs = append(probs, "priority 2 needs retry (≥ 30) and expire (≤ 10800)")
				}
			}
		}
		if ts := f.Get("timestamp"); ts != "" {
			if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
				probs = append(probs, "timestamp must be a Unix time")
			}
		}
		return Request{Form: f}, probs
	}, func(w http.ResponseWriter, _ Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":1,"request":"5042853c-402d-4a18-abcb-168734a801de"}`)
	})
}

var ntfyTopic = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// Ntfy is publishing as JSON: POST to the server's root; topic required;
// message ≤ 4,096 bytes; title a string; priority 1-5; tags an array of
// strings; click a URL. Answers the message as JSON.
func Ntfy() *Server {
	return start("ntfy", func(r *http.Request, body []byte) (Request, []string) {
		m, probs := jsonBody(r, body)
		if r.URL.Path != "/" {
			probs = append(probs, "JSON publishing posts to the server's root")
		}
		if m == nil {
			return Request{}, probs
		}
		if t, _ := str(m, "topic"); !ntfyTopic.MatchString(t) {
			probs = append(probs, "topic is required (letters, digits, - and _)")
		}
		if msg, ok := m["message"]; ok {
			if s, isStr := msg.(string); !isStr || len(s) > 4096 {
				probs = append(probs, "message must be a string of at most 4096 bytes")
			}
		}
		if t, ok := m["title"]; ok {
			if _, isStr := t.(string); !isStr {
				probs = append(probs, "title must be a string")
			}
		}
		if p, ok := m["priority"]; ok {
			if n, isNum := p.(float64); !isNum || n < 1 || n > 5 || n != float64(int(n)) {
				probs = append(probs, "priority must be an integer 1-5")
			}
		}
		if tags, ok := m["tags"]; ok {
			list, isList := tags.([]any)
			if !isList {
				probs = append(probs, "tags must be an array of strings")
			}
			for _, t := range list {
				if _, isStr := t.(string); !isStr {
					probs = append(probs, "tags must be an array of strings")
				}
			}
		}
		if c, ok := m["click"]; ok {
			if s, isStr := c.(string); !isStr || !strings.HasPrefix(s, "http") {
				probs = append(probs, "click must be a URL")
			}
		}
		return Request{JSON: m}, probs
	}, func(w http.ResponseWriter, req Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sPs71M8A2T", "time": time.Now().Unix(), "event": "message", "topic": req.JSON["topic"]})
	})
}
