package integrations

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Limits.
const (
	MaxPerOwner    = 50
	maxSelection   = 100
	maxNameLen     = 80
	testInterval   = 5 * time.Second
	allowListKey   = "integration_allow_list"
	adminRole      = "admin"
	defaultHAPfx   = "homeassistant"
	defaultTopicPf = "pagerarchive/"
)

// Service is the integrations logic and the delivery worker.
type Service struct {
	Store    *store.Store
	Network  *profile.Network
	Agencies map[string]*profile.Agency
	// Box seals secrets. nil means INTEGRATION_SECRETS_KEY isn't set:
	// integrations are off (ADR 0006 §4).
	Box      *Box
	Guard    *Guard
	Presets  Presets
	AppURL   *url.URL       // links to messages in payloads
	Location *time.Location // the deployment's time zone, for presets (R35a)
	Version  string
	Log      *slog.Logger
	Now      func() time.Time

	Stats Stats

	clientOnce sync.Once
	client     *http.Client

	mu       sync.Mutex
	wake     chan struct{}
	busy     map[int64]bool
	inflight sync.WaitGroup
	tests    map[int64]time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func dbErr(op string, err error) error { return fmt.Errorf("%s: %s", op, store.SafeError(err)) }

// Enabled reports whether integrations can be used: the server has a key.
func (s *Service) Enabled() bool { return s != nil && s.Box != nil }

func (s *Service) requireKey() error {
	if !s.Enabled() {
		return &Error{Status: http.StatusServiceUnavailable, Msg: ErrNoKey.Error() + "."}
	}
	return nil
}

func newUID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func (s *Service) secretsOf(row db.Integration) (Secrets, error) {
	var sec Secrets
	if row.Secrets == nil {
		return sec, nil
	}
	plain, err := s.Box.Open(row.Uid, row.Secrets)
	if err != nil {
		return sec, err
	}
	if err := json.Unmarshal(plain, &sec); err != nil {
		return sec, ErrCorrupt
	}
	return sec, nil
}

func (s *Service) seal(uid string, sec Secrets) ([]byte, error) {
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return s.Box.Seal(uid, plain)
}

// prepared is a validated request.
type prepared struct {
	name, content string
	kind          Kind
	config        Config
	secrets       Secrets
	filter        Filter
	capcodes      []string
	aliasIDs      []int64
}

var (
	capcodeRe     = regexp.MustCompile(`^[0-9]{1,7}$`)
	headerRe      = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	topicBadRe    = regexp.MustCompile(`[+#\x00]`)
	haPrefixRe    = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)
	discordPathRe = regexp.MustCompile(`^[0-9]{1,25}/[A-Za-z0-9_-]{1,100}$`)
	botTokenRe    = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{30,100}$`)
	chatIDRe      = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	pushoverKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)
	ntfyTopicRe   = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
)

// Headers a custom auth header can't replace.
var reservedHeaders = []string{"host", "content-length", "content-type", "transfer-encoding", "connection", "user-agent", "te", "upgrade", "trailer", "keep-alive", "proxy-authorization", "proxy-connection"}

// validate checks a request. prev is the saved integration when editing.
func (s *Service) validate(ctx context.Context, req Request, prev *loaded, prevSec Secrets, uid string) (prepared, error) {
	p := prepared{name: strings.TrimSpace(req.Name), kind: req.Kind, content: req.Content}
	if p.name == "" || len([]rune(p.name)) > maxNameLen {
		return p, invalid("Give the integration a name of up to %d characters.", maxNameLen)
	}
	if prev != nil {
		if p.kind == "" {
			p.kind = prev.kind
		}
		if p.kind != prev.kind {
			return p, invalid("An integration's kind can't be changed. Make a new one instead.")
		}
	}
	if !p.kind.valid() {
		return p, invalid("Choose a destination: webhook, MQTT, Discord, Telegram, Pushover or ntfy.")
	}
	switch p.content {
	case "":
		p.content = Full
		if p.kind.Preset() {
			p.content = Summary // third parties (R50a)
		}
	case Full, Summary:
	default:
		return p, invalid("Content must be full or summary.")
	}

	// R48: a selection is required.
	for _, c := range req.Capcodes {
		c = strings.TrimSpace(c)
		if !capcodeRe.MatchString(c) {
			return p, invalid("%q isn't a capcode: use 1 to 7 digits.", c)
		}
		if c = decoder.PadCapcode(c); !slices.Contains(p.capcodes, c) {
			p.capcodes = append(p.capcodes, c)
		}
	}
	for _, id := range req.AliasIDs {
		if !slices.Contains(p.aliasIDs, id) {
			p.aliasIDs = append(p.aliasIDs, id)
		}
	}
	// R48: a capcode that has its own alias is selected as that alias.
	if len(p.capcodes) > 0 {
		exact, err := s.Store.ExactAliases(ctx, p.capcodes)
		if err != nil {
			return p, dbErr("aliases", err)
		}
		for _, a := range exact {
			p.capcodes = slices.DeleteFunc(p.capcodes, func(c string) bool { return a.PatternKey != nil && c == *a.PatternKey })
			if !slices.Contains(p.aliasIDs, a.ID) {
				p.aliasIDs = append(p.aliasIDs, a.ID)
			}
		}
	}
	if len(p.capcodes)+len(p.aliasIDs) == 0 {
		return p, invalid("Choose at least one capcode or alias, so nothing is forwarded by accident.")
	}
	if len(p.capcodes)+len(p.aliasIDs) > maxSelection {
		return p, invalid("Choose at most %d capcodes and aliases.", maxSelection)
	}
	if len(p.aliasIDs) > 0 {
		found, err := s.Store.ExistingAliasIDs(ctx, p.aliasIDs)
		if err != nil {
			return p, dbErr("aliases", err)
		}
		if len(found) != len(p.aliasIDs) {
			return p, invalid("One of the chosen aliases no longer exists.")
		}
	}
	slices.Sort(p.capcodes)

	var err error
	if p.filter, err = s.validFilter(req.Filter); err != nil {
		return p, err
	}
	sec := req.Secrets
	if prev != nil {
		sec = sec.merge(prevSec)
	}
	if p.config, p.secrets, err = s.validDestination(ctx, p.kind, req.Config, sec, uid); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Service) validFilter(f Filter) (Filter, error) {
	out := Filter{Text: strings.TrimSpace(f.Text), Group: strings.TrimSpace(f.Group), Agency: strings.TrimSpace(f.Agency), Job: strings.TrimSpace(f.Job),
		ParseFailed: f.ParseFailed, AddressUnsplit: f.AddressUnsplit, LocationMismatch: f.LocationMismatch}
	known := []string{"Other"}
	for _, c := range s.Network.Categories {
		known = append(known, c.Name)
	}
	for _, c := range f.Categories {
		if !slices.Contains(known, c) {
			return out, invalid("Unknown event category %q.", c)
		}
		if !slices.Contains(out.Categories, c) {
			out.Categories = append(out.Categories, c)
		}
	}
	for _, c := range f.Codes {
		c = strings.TrimSpace(c)
		if c == "" || len(c) > 32 {
			return out, invalid("Event-type codes must be 1 to 32 characters.")
		}
		if !slices.Contains(out.Codes, c) {
			out.Codes = append(out.Codes, c)
		}
	}
	for _, p := range f.Priorities {
		if !slices.Contains([]string{"Emergency", "Non-emergency", "Admin", "none"}, p) {
			return out, invalid("Unknown priority %q.", p)
		}
		if !slices.Contains(out.Priorities, p) {
			out.Priorities = append(out.Priorities, p)
		}
	}
	for _, v := range []string{out.Text, out.Group, out.Agency, out.Job} {
		if len(v) > 200 {
			return out, invalid("Filter values must be at most 200 characters.")
		}
	}
	return out, nil
}

// checkHost checks a destination's host against the address rules (R51).
func (s *Service) checkHost(ctx context.Context, host string) error {
	if _, err := s.Guard.Resolve(ctx, host); err != nil {
		var ae *AddressError
		switch {
		case errors.As(err, &ae):
			return invalid("%s", ae.Error())
		case errors.Is(err, ErrLookup):
			return invalid("%s doesn't resolve to an address.", host)
		}
		return invalid("%s can't be used: %s", host, err.Error())
	}
	return nil
}

// httpURL checks an http(s) URL and returns its scheme and host for display.
func httpURL(raw, what string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return nil, invalid("%s must be an http:// or https:// URL.", what)
	}
	return u, nil
}

func target(u *url.URL) string { return u.Scheme + "://" + u.Host }

// validDestination checks the kind's settings and secrets, and keeps only
// the ones that kind uses.
func (s *Service) validDestination(ctx context.Context, kind Kind, in Config, sec Secrets, uid string) (Config, Secrets, error) {
	var cfg Config
	var out Secrets
	switch kind {
	case Webhook:
		if sec.URL == "" {
			return cfg, out, invalid("Enter the webhook's URL.")
		}
		u, err := httpURL(sec.URL, "The webhook URL")
		if err != nil {
			return cfg, out, err
		}
		cfg.Target, out.URL = target(u), u.String()
		if h := strings.TrimSpace(in.AuthHeader); h != "" {
			if !headerRe.MatchString(h) || slices.Contains(reservedHeaders, strings.ToLower(h)) {
				return cfg, out, invalid("%q can't be used as the auth header's name.", h)
			}
			if sec.AuthValue == "" {
				return cfg, out, invalid("Enter the auth header's value.")
			}
			cfg.AuthHeader, out.AuthValue = h, sec.AuthValue
		}
		return cfg, out, s.checkHost(ctx, u.Hostname())
	case MQTT:
		u, err := url.Parse(strings.TrimSpace(in.Broker))
		if err != nil || (u.Scheme != "mqtt" && u.Scheme != "mqtts") || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.User != nil {
			return cfg, out, invalid("The broker must look like mqtt://broker.example.org or mqtts://broker.example.org:8883.")
		}
		port := u.Port()
		if port == "" {
			port = mqttPort
			if u.Scheme == "mqtts" {
				port = mqttsPort
			}
		}
		cfg.Broker = u.Scheme + "://" + joinHostPort(u.Hostname(), port)
		cfg.Topic = strings.TrimSpace(in.Topic)
		if cfg.Topic == "" {
			cfg.Topic = defaultTopicPf + uid
		}
		if topicBadRe.MatchString(cfg.Topic) || strings.HasPrefix(cfg.Topic, "$") || len(cfg.Topic) > 200 {
			return cfg, out, invalid("The topic can't contain + or #, start with $, or be longer than 200 characters.")
		}
		if cfg.Username = strings.TrimSpace(in.Username); len(cfg.Username) > 100 {
			return cfg, out, invalid("The user name is too long.")
		}
		if cfg.Username != "" {
			out.Password = sec.Password
		}
		if ca := strings.TrimSpace(in.CACert); ca != "" {
			if cfg.Broker[:5] != "mqtts" {
				return cfg, out, invalid("A CA certificate needs an mqtts:// broker.")
			}
			if !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)) {
				return cfg, out, invalid("The CA certificate isn't a PEM certificate.")
			}
			cfg.CACert = ca
		}
		if cfg.HADiscovery = in.HADiscovery; cfg.HADiscovery {
			if cfg.HAPrefix = strings.Trim(strings.TrimSpace(in.HAPrefix), "/"); cfg.HAPrefix == "" {
				cfg.HAPrefix = defaultHAPfx
			}
			if !haPrefixRe.MatchString(cfg.HAPrefix) || len(cfg.HAPrefix) > 64 {
				return cfg, out, invalid("The discovery prefix may only use letters, digits, - and _, separated by /.")
			}
		}
		return cfg, out, s.checkHost(ctx, u.Hostname())
	case Discord:
		raw := strings.TrimSpace(sec.URL)
		ok := false
		for _, prefix := range s.Presets.DiscordPrefixes {
			if rest, found := strings.CutPrefix(raw, prefix); found && discordPathRe.MatchString(rest) {
				ok = true
			}
		}
		if !ok {
			return cfg, out, invalid("Paste the webhook URL from Discord (Server settings → Integrations → Webhooks). It starts with https://discord.com/api/webhooks/.")
		}
		u, err := httpURL(raw, "The Discord webhook URL")
		if err != nil {
			return cfg, out, err
		}
		cfg.Target, out.URL = target(u), raw
		return cfg, out, s.checkHost(ctx, u.Hostname())
	case Telegram:
		if !botTokenRe.MatchString(strings.TrimSpace(sec.BotToken)) {
			return cfg, out, invalid("Enter the bot token BotFather gave you (digits, a colon, then letters and digits).")
		}
		if cfg.ChatID = strings.TrimSpace(in.ChatID); !chatIDRe.MatchString(cfg.ChatID) {
			return cfg, out, invalid("Enter the chat ID: a number (groups start with -) or a channel's @name.")
		}
		out.BotToken = strings.TrimSpace(sec.BotToken)
		return cfg, out, s.checkPresetHost(ctx, s.Presets.TelegramBase)
	case Pushover:
		out.AppToken, out.UserKey = strings.TrimSpace(sec.AppToken), strings.TrimSpace(sec.UserKey)
		if !pushoverKeyRe.MatchString(out.AppToken) || !pushoverKeyRe.MatchString(out.UserKey) {
			return cfg, out, invalid("Enter Pushover's application token and your user key: 30 letters and digits each.")
		}
		// Up to high: repeat-until-acknowledged (2) is operational alerting,
		// which is out of scope (owner, 2026-10-16).
		if in.Priority != nil && (*in.Priority < -2 || *in.Priority > 1) {
			return cfg, out, invalid("Pushover's priority goes from -2 (lowest) to 1 (high).")
		}
		cfg.Priority = in.Priority
		return cfg, out, s.checkPresetHost(ctx, s.Presets.PushoverURL)
	case Ntfy:
		server := strings.TrimRight(strings.TrimSpace(in.Server), "/")
		if server == "" {
			server = s.Presets.NtfyServer
		}
		u, err := httpURL(server, "The ntfy server")
		if err != nil {
			return cfg, out, err
		}
		if u.Path != "" || u.RawQuery != "" || u.User != nil {
			return cfg, out, invalid("The ntfy server is just its address, like https://ntfy.sh.")
		}
		cfg.Server = target(u)
		if out.Topic = strings.TrimSpace(sec.Topic); !ntfyTopicRe.MatchString(out.Topic) {
			return cfg, out, invalid("The ntfy topic may only use letters, digits, - and _ (up to 64).")
		}
		if out.AccessToken = strings.TrimSpace(sec.AccessToken); len(out.AccessToken) > 200 {
			return cfg, out, invalid("The access token is too long.")
		}
		if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 4) {
			return cfg, out, invalid("ntfy's priority goes from 1 (min) to 4 (high).")
		}
		cfg.Priority = in.Priority
		return cfg, out, s.checkHost(ctx, u.Hostname())
	}
	return cfg, out, errUnknownKind
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func (s *Service) checkPresetHost(ctx context.Context, base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	return s.checkHost(ctx, u.Hostname())
}

// load reads an integration for its owner. Another user's integration is
// "not found", so it can't be told apart from one that doesn't exist (R47).
func (s *Service) load(ctx context.Context, owner db.User, id int64) (loaded, error) {
	r, err := s.Store.IntegrationByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return loaded{}, errNotFound
	}
	if err != nil {
		return loaded{}, dbErr("integration", err)
	}
	if r.OwnerID != owner.ID {
		return loaded{}, errNotFound
	}
	return fromByID(r)
}

// view builds what the owner sees.
func (s *Service) view(ctx context.Context, l loaded) (View, error) {
	v := View{ID: l.row.ID, UID: l.row.Uid, Name: l.row.Name, Kind: l.kind, Content: l.row.Content, Filter: l.filter, Config: l.config,
		SecretsSet: map[string]bool{}, Status: l.row.Status, PausedBy: l.row.PausedBy, PausedAt: l.row.PausedAt,
		FailedPages: l.row.FailedPages, LastError: l.row.LastError, LastErrorAt: l.row.LastErrorAt, LastDeliveredAt: l.row.LastDeliveredAt,
		CreatedAt: l.row.CreatedAt, UpdatedAt: l.row.UpdatedAt, Capcodes: []string{}, Aliases: []AliasRef{}}
	if sec, err := s.secretsOf(l.row); err != nil {
		v.SecretsError = err.Error()
	} else {
		v.SecretsSet = sec.Which()
	}
	codes, err := s.Store.IntegrationCapcodes(ctx, l.row.ID)
	if err != nil {
		return v, dbErr("capcodes", err)
	}
	v.Capcodes = append(v.Capcodes, codes...)
	if v.CoveredBy, err = s.coveredBy(ctx, codes); err != nil {
		return v, err
	}
	aliases, err := s.Store.IntegrationAliases(ctx, l.row.ID)
	if err != nil {
		return v, dbErr("aliases", err)
	}
	for _, a := range aliases {
		v.Aliases = append(v.Aliases, AliasRef{ID: a.ID, Pattern: a.Pattern, Name: a.Name})
	}
	return v, nil
}

// coveredBy names the alias each capcode's pages show (R27 precedence), for
// capcodes that a range alias covers (R48).
func (s *Service) coveredBy(ctx context.Context, capcodes []string) (map[string]string, error) {
	out := map[string]string{}
	if len(capcodes) == 0 {
		return out, nil
	}
	rows, err := s.Store.CapcodeAliasCandidates(ctx, capcodes)
	if err != nil {
		return nil, dbErr("aliases", err)
	}
	cands, names := map[string][]ingest.Candidate{}, map[int64]string{}
	for _, r := range rows {
		cands[r.Capcode] = append(cands[r.Capcode], ingest.Candidate{ID: r.ID, Pattern: r.Pattern})
		names[r.ID] = r.Name
	}
	for c, cs := range cands {
		out[c] = names[ingest.BestAlias(cs).ID]
	}
	return out, nil
}

// Suggest is what the selection field offers for what was typed (R48): the
// aliases whose name or capcode contains it and, for digits, the capcode.
// A capcode with its own alias is offered as that alias; one that only a
// range alias covers is offered as the capcode, with the range's name, so
// typing a capcode never selects a range.
func (s *Service) Suggest(ctx context.Context, q string) ([]Suggestion, error) {
	q = strings.TrimSpace(q)
	out := []Suggestion{}
	if q == "" || len(q) > 80 {
		return out, nil
	}
	var first *Suggestion
	if capcodeRe.MatchString(q) {
		c := decoder.PadCapcode(q)
		if a, err := s.Store.AliasByKey(ctx, c); err == nil {
			first = &Suggestion{Kind: "alias", AliasID: a.ID, Name: a.Name, Pattern: a.Pattern}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErr("aliases", err)
		} else {
			covered, err := s.coveredBy(ctx, []string{c})
			if err != nil {
				return nil, err
			}
			first = &Suggestion{Kind: "capcode", Capcode: c, CoveredBy: covered[c]}
		}
	}
	rows, err := s.Store.SearchAliases(ctx, q)
	if err != nil {
		return nil, dbErr("aliases", err)
	}
	for _, a := range rows {
		if first != nil && first.Kind == "alias" && first.AliasID == a.ID {
			continue
		}
		out = append(out, Suggestion{Kind: "alias", AliasID: a.ID, Name: a.Name, Pattern: a.Pattern,
			Range: strings.ContainsAny(a.Pattern, "%_")})
	}
	if first != nil {
		// A whole capcode comes first; a few digits are more likely the
		// start of an alias's capcode.
		if len(q) == 7 || first.Kind == "alias" {
			out = append([]Suggestion{*first}, out...)
		} else {
			out = append(out, *first)
		}
	}
	return out, nil
}

// List is the owner's integrations.
func (s *Service) List(ctx context.Context, owner db.User) ([]View, error) {
	rows, err := s.Store.IntegrationsByOwner(ctx, owner.ID)
	if err != nil {
		return nil, dbErr("integrations", err)
	}
	out := []View{}
	for _, r := range rows {
		l, err := decode(r)
		if err != nil {
			return nil, err
		}
		v, err := s.view(ctx, l)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get is one of the owner's integrations.
func (s *Service) Get(ctx context.Context, owner db.User, id int64) (View, error) {
	l, err := s.load(ctx, owner, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, l)
}

func (s *Service) writeSelection(ctx context.Context, q *db.Queries, id int64, p prepared) error {
	if err := q.ClearIntegrationCapcodes(ctx, id); err != nil {
		return dbErr("capcodes", err)
	}
	if err := q.AddIntegrationCapcodes(ctx, db.AddIntegrationCapcodesParams{ID: id, Capcodes: p.capcodes}); err != nil {
		return dbErr("capcodes", err)
	}
	if err := q.ClearIntegrationAliases(ctx, id); err != nil {
		return dbErr("aliases", err)
	}
	if err := q.AddIntegrationAliases(ctx, db.AddIntegrationAliasesParams{ID: id, AliasIds: p.aliasIDs}); err != nil {
		return dbErr("aliases", err)
	}
	return nil
}

// Create saves a new integration for owner (R47).
func (s *Service) Create(ctx context.Context, owner db.User, req Request) (View, error) {
	if err := s.requireKey(); err != nil {
		return View{}, err
	}
	n, err := s.Store.CountIntegrationsByOwner(ctx, owner.ID)
	if err != nil {
		return View{}, dbErr("count", err)
	}
	if n >= MaxPerOwner {
		return View{}, invalid("You already have %d integrations, the most anyone can have.", MaxPerOwner)
	}
	uid := newUID()
	p, err := s.validate(ctx, req, nil, Secrets{}, uid)
	if err != nil {
		return View{}, err
	}
	blob, err := s.seal(uid, p.secrets)
	if err != nil {
		return View{}, err
	}
	cfg, _ := json.Marshal(p.config)
	filter, _ := json.Marshal(p.filter)
	var row db.Integration
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		var err error
		row, err = q.CreateIntegration(ctx, db.CreateIntegrationParams{Uid: uid, OwnerID: owner.ID, Name: p.name, Kind: string(p.kind),
			Content: p.content, Config: cfg, Secrets: blob, Filter: filter})
		if err != nil {
			return dbErr("create integration", err)
		}
		return s.writeSelection(ctx, q, row.ID, p)
	})
	if err != nil {
		return View{}, err
	}
	s.Log.Info("integration created", "integration", row.ID, "owner", owner.ID, "kind", row.Kind)
	l, err := decode(row)
	if err != nil {
		return View{}, err
	}
	v, err := s.view(ctx, l)
	if err == nil && l.kind == MQTT && l.config.HADiscovery {
		o := s.publishDiscovery(ctx, l, p.secrets, false)
		v.Discovery = &o
	}
	return v, err
}

// Update changes an integration. Secret fields left empty keep their
// saved values.
func (s *Service) Update(ctx context.Context, owner db.User, id int64, req Request) (View, error) {
	if err := s.requireKey(); err != nil {
		return View{}, err
	}
	prev, err := s.load(ctx, owner, id)
	if err != nil {
		return View{}, err
	}
	prevSec, err := s.secretsOf(prev.row)
	if err != nil {
		prevSec = Secrets{} // saved with another key: everything must be entered again
	}
	p, err := s.validate(ctx, req, &prev, prevSec, prev.row.Uid)
	if err != nil {
		return View{}, err
	}
	blob, err := s.seal(prev.row.Uid, p.secrets)
	if err != nil {
		return View{}, err
	}
	cfg, _ := json.Marshal(p.config)
	filter, _ := json.Marshal(p.filter)
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		if err := q.UpdateIntegration(ctx, db.UpdateIntegrationParams{ID: id, Name: p.name, Content: p.content, Config: cfg, Secrets: blob, Filter: filter}); err != nil {
			return dbErr("update integration", err)
		}
		return s.writeSelection(ctx, q, id, p)
	})
	if err != nil {
		return View{}, err
	}
	s.Log.Info("integration changed", "integration", id, "owner", owner.ID)
	cur, err := s.load(ctx, owner, id)
	if err != nil {
		return View{}, err
	}
	v, err := s.view(ctx, cur)
	if err != nil {
		return v, err
	}
	if cur.kind == MQTT {
		var notes []string
		ok := true
		if discoveryChanged(prev.config, cur.config) {
			o := s.publishDiscovery(ctx, prev, prevSec, true)
			ok = ok && o.OK
			notes = append(notes, o.Message)
		}
		if cur.config.HADiscovery {
			o := s.publishDiscovery(ctx, cur, p.secrets, false)
			ok = ok && o.OK
			notes = append(notes, o.Message)
		}
		if len(notes) > 0 {
			v.Discovery = &Outcome{OK: ok, Message: strings.Join(notes, " ")}
		}
	}
	return v, nil
}

// Delete deletes an integration, its deliveries and its Home Assistant
// entity.
func (s *Service) Delete(ctx context.Context, owner db.User, id int64) error {
	l, err := s.load(ctx, owner, id)
	if err != nil {
		return err
	}
	var sec Secrets
	if l.kind == MQTT && l.config.HADiscovery && s.Enabled() {
		sec, err = s.secretsOf(l.row)
	}
	if err := s.Store.DeleteIntegration(ctx, id); err != nil {
		return dbErr("delete integration", err)
	}
	s.Log.Info("integration deleted", "integration", id, "owner", owner.ID)
	if l.kind == MQTT && l.config.HADiscovery && s.Enabled() && err == nil {
		s.publishDiscovery(ctx, l, sec, true)
	}
	return nil
}

// Pause pauses an integration: by its owner, or (admin true) by an admin.
// Pending deliveries are cancelled.
func (s *Service) Pause(ctx context.Context, actor db.User, id int64, admin bool) error {
	by := "owner"
	if admin {
		by = adminRole
		if _, err := s.Store.IntegrationByID(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
	} else if _, err := s.load(ctx, actor, id); err != nil {
		return err
	}
	return s.pause(ctx, id, by)
}

func (s *Service) pause(ctx context.Context, id int64, by string) error {
	return s.Store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.PauseIntegration(ctx, db.PauseIntegrationParams{ID: id, PausedBy: by, At: s.now()})
		if err != nil {
			return dbErr("pause", err)
		}
		if n == 0 {
			return nil // already paused
		}
		if _, err := q.CancelPendingDeliveries(ctx, []int64{id}); err != nil {
			return dbErr("cancel deliveries", err)
		}
		s.Log.Info("integration paused", "integration", id, "by", by)
		return nil
	})
}

// Resume resumes a paused integration. Only an admin can resume one an
// admin paused (owner, Q3).
func (s *Service) Resume(ctx context.Context, actor db.User, id int64, admin bool) error {
	if admin {
		if _, err := s.Store.IntegrationByID(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return dbErr("integration", err)
		}
	} else {
		l, err := s.load(ctx, actor, id)
		if err != nil {
			return err
		}
		if l.row.PausedBy != nil && *l.row.PausedBy == adminRole {
			return &Error{Status: http.StatusForbidden, Msg: "An admin paused this integration. Only an admin can resume it."}
		}
	}
	if _, err := s.Store.ResumeIntegration(ctx, id); err != nil {
		return dbErr("resume", err)
	}
	s.Log.Info("integration resumed", "integration", id, "by_admin", admin)
	return nil
}

// Deliveries is an integration's last 100 deliveries.
func (s *Service) Deliveries(ctx context.Context, owner db.User, id int64) ([]Delivery, error) {
	if _, err := s.load(ctx, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.Store.RecentDeliveries(ctx, id)
	if err != nil {
		return nil, dbErr("deliveries", err)
	}
	out := []Delivery{}
	for _, r := range rows {
		out = append(out, Delivery{MessageID: r.MessageID, Capcode: r.Capcode, SentAt: r.SentAt.UTC(), Status: r.Status, Attempts: r.Attempts,
			LastResult: r.LastResult, LastError: r.LastError, DeliveredAt: r.DeliveredAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// Test sends a test delivery and returns what the endpoint answered (R51c).
func (s *Service) Test(ctx context.Context, owner db.User, id int64) (TestResult, error) {
	if err := s.requireKey(); err != nil {
		return TestResult{}, err
	}
	l, err := s.load(ctx, owner, id)
	if err != nil {
		return TestResult{}, err
	}
	s.mu.Lock()
	if s.tests == nil {
		s.tests = map[int64]time.Time{}
	}
	if last, ok := s.tests[id]; ok && time.Since(last) < testInterval {
		s.mu.Unlock()
		return TestResult{}, &Error{Status: http.StatusTooManyRequests, Msg: "Wait a few seconds between tests."}
	}
	s.tests[id] = time.Now()
	s.mu.Unlock()
	sec, err := s.secretsOf(l.row)
	if err != nil {
		return TestResult{}, invalid("%s", err.Error())
	}
	capcode := "0000000"
	var alias *string
	if codes, err := s.Store.IntegrationCapcodes(ctx, id); err == nil && len(codes) > 0 {
		capcode = codes[0]
	} else if as, err := s.Store.IntegrationAliases(ctx, id); err == nil && len(as) > 0 {
		capcode, alias = as[0].Pattern, &as[0].Name
	}
	var link *string
	if s.AppURL != nil {
		u := s.AppURL.JoinPath("integrations").String()
		link = &u
	}
	p := testPage(s.now(), capcode, alias, link)
	actx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	defer cancel()
	start := time.Now()
	r := s.send(actx, l, sec, p, true)
	out := TestResult{OK: r.outcome == delivered, Result: r.status, Error: r.err, Body: bodyText(r.body), TookMS: time.Since(start).Milliseconds()}
	if out.Result == "" {
		out.Result = "not delivered"
	}
	s.Log.Info("integration test", "integration", id, "ok", out.OK, "result", out.Result, "error", out.Error)
	return out, nil
}

// AdminList is one user's integrations, as admins may see them on that
// user's page (R44b, R47). There is no list of everyone's.
func (s *Service) AdminList(ctx context.Context, ownerID int64) ([]AdminView, error) {
	rows, err := s.Store.AdminIntegrationsOfUser(ctx, ownerID)
	if err != nil {
		return nil, dbErr("integrations", err)
	}
	out := []AdminView{}
	for _, r := range rows {
		out = append(out, AdminView{ID: r.ID, Name: r.Name, Kind: Kind(r.Kind), Status: r.Status,
			PausedBy: r.PausedBy, LastDeliveredAt: r.LastDeliveredAt, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// AllowList is the admins' allow list (R51).
func (s *Service) AllowList(ctx context.Context) (AllowList, error) {
	v, err := s.Store.GetSetting(ctx, allowListKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return AllowList{}, nil
	}
	if err != nil {
		return AllowList{}, dbErr("allow list", err)
	}
	var entries []string
	if err := json.Unmarshal([]byte(v), &entries); err != nil {
		return AllowList{}, nil
	}
	return ParseAllowList(entries)
}

// SetAllowList replaces the allow list.
func (s *Service) SetAllowList(ctx context.Context, actor db.User, entries []string) (AllowList, error) {
	if len(entries) > 100 {
		return AllowList{}, invalid("The allow list can have at most 100 entries.")
	}
	a, err := ParseAllowList(entries)
	if err != nil {
		return AllowList{}, invalid("%s", err.Error())
	}
	b, _ := json.Marshal(a.Entries())
	if err := s.Store.SetSetting(ctx, db.SetSettingParams{Key: allowListKey, Value: string(b)}); err != nil {
		return AllowList{}, dbErr("allow list", err)
	}
	s.Log.Info("integration allow list changed", "by", actor.ID, "entries", len(a.Entries()))
	return a, nil
}

// RotateKeys seals every blob saved with the old key again with the
// current one (ADR 0006 §4).
func (s *Service) RotateKeys(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	rows, err := s.Store.AllIntegrationSecrets(ctx)
	if err != nil {
		return 0, dbErr("secrets", err)
	}
	n := 0
	for _, r := range rows {
		if !s.Box.Stale(r.Secrets) {
			continue
		}
		plain, err := s.Box.Open(r.Uid, r.Secrets)
		if err != nil {
			continue
		}
		blob, err := s.Box.Seal(r.Uid, plain)
		if err != nil {
			return n, err
		}
		if err := s.Store.SetIntegrationSecrets(ctx, db.SetIntegrationSecretsParams{ID: r.ID, Secrets: blob}); err != nil {
			return n, dbErr("secrets", err)
		}
		n++
	}
	return n, nil
}
