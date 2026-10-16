// Package integrations forwards new pages to users' own systems (R47-R51c,
// ADR 0006): webhooks, MQTT (with Home Assistant discovery) and presets for
// Discord, Telegram, Pushover and ntfy.
//
// Page text is never logged, never stored outside the messages table and
// never part of an error (P1). Payloads are built from the message when
// they're sent.
package integrations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Kind is a destination type (R50).
type Kind string

const (
	Webhook  Kind = "webhook"
	MQTT     Kind = "mqtt"
	Discord  Kind = "discord"
	Telegram Kind = "telegram"
	Pushover Kind = "pushover"
	Ntfy     Kind = "ntfy"
)

// Kinds lists every destination type.
var Kinds = []Kind{Webhook, MQTT, Discord, Telegram, Pushover, Ntfy}

// Preset is a service with its own format. Presets default to Summary,
// because those services are third parties (R50a).
func (k Kind) Preset() bool { return k == Discord || k == Telegram || k == Pushover || k == Ntfy }

func (k Kind) valid() bool {
	for _, x := range Kinds {
		if k == x {
			return true
		}
	}
	return false
}

// Content is what an integration sends (R50a).
const (
	Full    = "full"
	Summary = "summary"
)

// Config holds an integration's settings that aren't secret.
type Config struct {
	// Target is the scheme and host of a secret URL (webhook, Discord),
	// shown in its place. Set by the server.
	Target     string `json:"target,omitempty"`
	AuthHeader string `json:"auth_header,omitempty"` // webhook: the custom auth header's name

	Broker      string `json:"broker,omitempty"` // mqtt:// or mqtts://
	Username    string `json:"username,omitempty"`
	Topic       string `json:"topic,omitempty"`
	CACert      string `json:"ca_cert,omitempty"` // PEM, for a broker with its own CA
	HADiscovery bool   `json:"ha_discovery,omitempty"`
	HAPrefix    string `json:"ha_prefix,omitempty"`

	ChatID string `json:"chat_id,omitempty"` // Telegram
	Server string `json:"server,omitempty"`  // ntfy

	// Priority: Pushover -2..2, ntfy 1..5. nil follows the page
	// (Emergency high, others normal).
	Priority *int `json:"priority,omitempty"`
}

// Secrets are encrypted when stored and never returned (R51).
type Secrets struct {
	URL         string `json:"url,omitempty"`        // webhook, Discord
	AuthValue   string `json:"auth_value,omitempty"` // webhook
	Password    string `json:"password,omitempty"`   // MQTT
	BotToken    string `json:"bot_token,omitempty"`  // Telegram
	AppToken    string `json:"app_token,omitempty"`  // Pushover
	UserKey     string `json:"user_key,omitempty"`   // Pushover
	Topic       string `json:"topic,omitempty"`      // ntfy
	AccessToken string `json:"access_token,omitempty"`
}

// Which says which secrets are set, never their values.
func (s Secrets) Which() map[string]bool {
	out := map[string]bool{}
	for name, v := range map[string]string{"url": s.URL, "auth_value": s.AuthValue, "password": s.Password, "bot_token": s.BotToken,
		"app_token": s.AppToken, "user_key": s.UserKey, "topic": s.Topic, "access_token": s.AccessToken} {
		if v != "" {
			out[name] = true
		}
	}
	return out
}

// merge keeps saved values for fields left empty.
func (s Secrets) merge(saved Secrets) Secrets {
	keep := func(n, o string) string {
		if n == "" {
			return o
		}
		return n
	}
	return Secrets{
		URL: keep(s.URL, saved.URL), AuthValue: keep(s.AuthValue, saved.AuthValue), Password: keep(s.Password, saved.Password),
		BotToken: keep(s.BotToken, saved.BotToken), AppToken: keep(s.AppToken, saved.AppToken), UserKey: keep(s.UserKey, saved.UserKey),
		Topic: keep(s.Topic, saved.Topic), AccessToken: keep(s.AccessToken, saved.AccessToken),
	}
}

// Filter narrows the selection (R49). Every criterion is optional; they
// combine with AND, and the values within one list with OR.
type Filter struct {
	Categories []string `json:"categories,omitempty"` // report categories, "Other" included
	Codes      []string `json:"codes,omitempty"`      // raw event-type codes
	Priorities []string `json:"priorities,omitempty"` // Emergency, Non-emergency, Admin, none
	Text       string   `json:"text,omitempty"`
	Group      string   `json:"group,omitempty"`
	Agency     string   `json:"agency,omitempty"`
	Job        string   `json:"job,omitempty"`
	// Admin only (R33, R49): ignored while the owner isn't an admin.
	ParseFailed      bool `json:"parse_failed,omitempty"`
	AddressUnsplit   bool `json:"address_unsplit,omitempty"`
	LocationMismatch bool `json:"location_mismatch,omitempty"`
}

// Request creates or changes an integration.
type Request struct {
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	Content  string   `json:"content"` // "" is the kind's default
	Capcodes []string `json:"capcodes"`
	AliasIDs []int64  `json:"alias_ids"`
	Filter   Filter   `json:"filter"`
	Config   Config   `json:"config"`
	Secrets  Secrets  `json:"secrets"` // on a change, empty fields keep the saved values
}

// AliasRef is a selected alias.
type AliasRef struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Name    string `json:"name"`
}

// View is an integration as its owner sees it: never its secrets.
type View struct {
	ID       int64    `json:"id"`
	UID      string   `json:"uid"`
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	Content  string   `json:"content"`
	Capcodes []string `json:"capcodes"`
	// CoveredBy names the range alias of each capcode that one covers (R48).
	CoveredBy       map[string]string `json:"covered_by"`
	Aliases         []AliasRef        `json:"aliases"`
	Filter          Filter            `json:"filter"`
	Config          Config            `json:"config"`
	SecretsSet      map[string]bool   `json:"secrets_set"`
	SecretsError    string            `json:"secrets_error,omitempty"` // e.g. saved with another key
	Status          string            `json:"status"`                  // active or paused
	PausedBy        *string           `json:"paused_by"`               // failing, owner or admin
	PausedAt        *time.Time        `json:"paused_at"`
	FailedPages     int32             `json:"failed_pages"`
	LastError       *string           `json:"last_error"`
	LastErrorAt     *time.Time        `json:"last_error_at"`
	LastDeliveredAt *time.Time        `json:"last_delivered_at"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	// Discovery is what happened to Home Assistant discovery on this save.
	Discovery *Outcome `json:"discovery,omitempty"`
}

// Outcome is the result of a side effect the owner should know about.
type Outcome struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Delivery is one page's delivery as its owner sees it.
type Delivery struct {
	MessageID   int64      `json:"message_id"`
	Capcode     string     `json:"capcode"`
	SentAt      time.Time  `json:"sent_at"`
	Status      string     `json:"status"` // pending, delivered, failed, too_old, cancelled
	Attempts    int32      `json:"attempts"`
	LastResult  *string    `json:"last_result"`
	LastError   *string    `json:"last_error"`
	DeliveredAt *time.Time `json:"delivered_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// AdminView is what admins see of anyone's integration: that it exists,
// never what it sends or where (R47).
type AdminView struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Kind            Kind       `json:"kind"`
	Status          string     `json:"status"`
	PausedBy        *string    `json:"paused_by"`
	LastDeliveredAt *time.Time `json:"last_delivered_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Suggestion is one thing the selection field offers (R48): an alias, or a
// capcode with the name of the range alias covering it, if any.
type Suggestion struct {
	Kind      string `json:"kind"` // alias or capcode
	AliasID   int64  `json:"alias_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Range     bool   `json:"range,omitempty"` // the alias is a pattern
	Capcode   string `json:"capcode,omitempty"`
	CoveredBy string `json:"covered_by,omitempty"`
}

// TestResult is what the endpoint answered a Send test (R51c).
type TestResult struct {
	OK     bool   `json:"ok"`
	Result string `json:"result"`          // e.g. "HTTP 204 No Content"
	Error  string `json:"error,omitempty"` // never page text
	Body   string `json:"body,omitempty"`  // the first 1 KB of the answer
	TookMS int64  `json:"took_ms"`
}

// Error is a refusal the user can act on. Msg is shown to them.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &Error{Status: http.StatusUnprocessableEntity, Msg: fmt.Sprintf(format, args...)}
}

var errNotFound = &Error{Status: http.StatusNotFound, Msg: "No such integration."}

// loaded is an integration read from the database, decoded.
type loaded struct {
	row         db.Integration
	ownerRole   string
	ownerStatus string
	kind        Kind
	config      Config
	filter      Filter
}

func decode(row db.Integration) (loaded, error) {
	l := loaded{row: row, kind: Kind(row.Kind)}
	if err := json.Unmarshal(row.Config, &l.config); err != nil {
		return l, fmt.Errorf("integration %d: config", row.ID)
	}
	if err := json.Unmarshal(row.Filter, &l.filter); err != nil {
		return l, fmt.Errorf("integration %d: filter", row.ID)
	}
	return l, nil
}

func fromByID(r db.IntegrationByIDRow) (loaded, error) {
	l, err := decode(db.Integration{
		ID: r.ID, Uid: r.Uid, OwnerID: r.OwnerID, Name: r.Name, Kind: r.Kind, Content: r.Content, Config: r.Config,
		Secrets: r.Secrets, Filter: r.Filter, Status: r.Status, PausedBy: r.PausedBy, PausedAt: r.PausedAt,
		FailedPages: r.FailedPages, LastError: r.LastError, LastErrorAt: r.LastErrorAt, LastDeliveredAt: r.LastDeliveredAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	})
	l.ownerRole, l.ownerStatus = r.OwnerRole, r.OwnerStatus
	return l, err
}
