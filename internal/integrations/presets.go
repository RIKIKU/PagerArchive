package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Presets are where the preset services live. Production uses
// DefaultPresets; tests point them at stand-in servers. They aren't
// settings, so a deployment can't aim a preset elsewhere (ADR 0006 §5).
type Presets struct {
	DiscordPrefixes []string // a Discord webhook URL must start with one of these
	TelegramBase    string   // the Bot API, without /bot<token>
	PushoverURL     string   // the messages endpoint
	NtfyServer      string   // the default ntfy server
}

// DefaultPresets are the real services (checked against their docs, 2026-10-16).
func DefaultPresets() Presets {
	return Presets{
		DiscordPrefixes: []string{"https://discord.com/api/webhooks/", "https://discordapp.com/api/webhooks/"},
		TelegramBase:    "https://api.telegram.org",
		PushoverURL:     "https://api.pushover.net/1/messages.json",
		NtfyServer:      "https://ntfy.sh",
	}
}

// Service limits, from each service's docs.
const (
	discordTitleMax       = 256
	discordDescriptionMax = 4096
	telegramTextMax       = 4096
	pushoverMessageMax    = 1024
	pushoverTitleMax      = 250
	pushoverURLMax        = 512
	ntfyMessageMaxBytes   = 4096
)

// rendered is a page as a preset shows it.
type rendered struct {
	Title string // "Emergency · Station 1"
	Body  string // Full: the text, then the summary line; Summary: the summary line
	Link  *string
}

func priorityLabel(p page) string {
	if p.Priority != nil {
		return *p.Priority
	}
	return "Page"
}

// render formats a page for a preset. Full content leads with the text
// without its marker; Summary never has text or an address (R50a).
func (s *Service) render(l loaded, p page) rendered {
	who := p.Capcode
	if p.Alias != nil {
		who = *p.Alias
	}
	title := priorityLabel(p) + " · " + who
	if p.Test {
		title = "Test · " + who
	}
	var parts []string
	if et := p.Fields.EventType; et != nil {
		e := *et
		switch {
		case p.EventLabel != nil:
			e += " " + *p.EventLabel
		case p.Category != nil && *p.Category != "Other":
			e += " " + *p.Category
		}
		parts = append(parts, e)
	}
	if p.JobNumber != nil {
		parts = append(parts, "Job "+*p.JobNumber)
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	parts = append(parts, p.SentAt.In(loc).Format("2 Jan 2006, 15:04:05"))
	line := strings.Join(parts, " · ")
	body := line
	if l.row.Content == Full {
		body = p.Text + "\n" + line
	}
	return rendered{Title: title, Body: body, Link: p.URL}
}

// cut shortens s to at most n characters, ending with "…" if it was cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// cutBytes shortens s to at most n bytes on a character boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

// request builds an integration's HTTP request for a page.
func (s *Service) request(ctx context.Context, l loaded, sec Secrets, p page) (*http.Request, error) {
	switch l.kind {
	case Webhook:
		body, err := payload(l, p)
		if err != nil {
			return nil, err
		}
		req, err := newJSON(ctx, sec.URL, body)
		if err == nil && l.config.AuthHeader != "" {
			req.Header.Set(l.config.AuthHeader, sec.AuthValue)
		}
		return req, err
	case Discord:
		return newJSON(ctx, sec.URL, s.discordBody(l, p))
	case Telegram:
		return newJSON(ctx, strings.TrimRight(s.Presets.TelegramBase, "/")+"/bot"+sec.BotToken+"/sendMessage", s.telegramBody(l, p))
	case Pushover:
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Presets.PushoverURL, strings.NewReader(s.pushoverForm(l, sec, p).Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	case Ntfy:
		req, err := newJSON(ctx, strings.TrimRight(l.config.Server, "/")+"/", s.ntfyBody(l, sec, p))
		if err == nil && sec.AccessToken != "" {
			req.Header.Set("Authorization", "Bearer "+sec.AccessToken)
		}
		return req, err
	}
	return nil, errUnknownKind
}

var errUnknownKind = invalid("Unknown kind of integration.")

func newJSON(ctx context.Context, u string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, errBadURL
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

var errBadURL = invalid("The destination URL isn't valid.")

// Discord: Execute Webhook (POST, JSON). Mentions are off, so page text
// can't ping @everyone.
func (s *Service) discordBody(l loaded, p page) []byte {
	r := s.render(l, p)
	embed := map[string]any{
		"title":       cut(r.Title, discordTitleMax),
		"description": cut(r.Body, discordDescriptionMax),
		"timestamp":   p.SentAt.UTC().Format(time.RFC3339),
		"footer":      map[string]string{"text": "PagerArchive"},
	}
	if r.Link != nil {
		embed["url"] = *r.Link
	}
	b, _ := json.Marshal(map[string]any{
		"username":         "PagerArchive",
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	return b
}

// Telegram: sendMessage (POST, JSON), plain text so page text isn't read
// as markup.
func (s *Service) telegramBody(l loaded, p page) []byte {
	r := s.render(l, p)
	text := r.Title + "\n" + r.Body
	if r.Link != nil {
		text += "\n" + *r.Link
	}
	b, _ := json.Marshal(map[string]any{
		"chat_id":              l.config.ChatID,
		"text":                 cut(text, telegramTextMax),
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	return b
}

// pagePriority is a preset's priority: the configured one, or high for
// Emergency pages and normal for the rest.
func pagePriority(cfg Config, p page, high, normal int) int {
	if cfg.Priority != nil {
		return *cfg.Priority
	}
	if p.Priority != nil && *p.Priority == "Emergency" {
		return high
	}
	return normal
}

// Pushover: messages.json (POST, form-encoded, as Pushover documents).
func (s *Service) pushoverForm(l loaded, sec Secrets, p page) url.Values {
	r := s.render(l, p)
	v := url.Values{
		"token":     {sec.AppToken},
		"user":      {sec.UserKey},
		"title":     {cut(r.Title, pushoverTitleMax)},
		"message":   {cut(r.Body, pushoverMessageMax)},
		"timestamp": {strconv.FormatInt(p.SentAt.Unix(), 10)},
	}
	if r.Link != nil && len(*r.Link) <= pushoverURLMax {
		v.Set("url", *r.Link)
		v.Set("url_title", "Open in PagerArchive")
	}
	prio := pagePriority(l.config, p, 1, 0)
	v.Set("priority", strconv.Itoa(prio))
	if prio == 2 { // emergency: repeats until acknowledged
		v.Set("retry", "60")
		v.Set("expire", "3600")
	}
	return v
}

// ntfy: publish as JSON (POST to the server's root).
func (s *Service) ntfyBody(l loaded, sec Secrets, p page) []byte {
	r := s.render(l, p)
	m := map[string]any{
		"topic":    sec.Topic,
		"title":    r.Title,
		"message":  cutBytes(r.Body, ntfyMessageMaxBytes),
		"priority": pagePriority(l.config, p, 4, 3),
	}
	if p.Priority != nil && *p.Priority == "Emergency" {
		m["tags"] = []string{"rotating_light"}
	}
	if r.Link != nil {
		m["click"] = *r.Link
	}
	b, _ := json.Marshal(m)
	return b
}
