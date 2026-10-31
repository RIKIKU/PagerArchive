// Package server is the HTTP API. Handlers never log or echo page text (P1).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// MaxFuture is how far ahead of the server's clock a receive time may be.
const MaxFuture = 60 * time.Second

// MaxTextBytes caps a page's text. POCSAG pages are far shorter.
const MaxTextBytes = 4096

// Server serves the API.
type Server struct {
	Store    *store.Store
	Ingest   *ingest.Service
	Log      *slog.Logger
	Now      func() time.Time
	Hub      *live.Hub
	Location *time.Location // deployment time zone (R35a)
	// Accounts and OIDC sign users in (R40-R44). AppURL is the public URL.
	Accounts *accounts.Service
	// Catalog changes aliases (R53-R58).
	Catalog *catalog.Catalog
	OIDC    *OIDC
	// PowerShell checks the PowerShell module's tokens (R60); nil refuses them.
	PowerShell *PowerShell
	AppURL     *url.URL
	// AccountURL is where users change their name and password (Authentik).
	AccountURL string
	// UI serves the browser app; nil serves none.
	UI http.Handler
	// Nightly is the retention and redaction job (R45).
	Nightly *nightly.Job
	// MapTiles is the PMTiles extract's path ("" for none) and MapAttribution
	// its credit line (R35).
	MapTiles, MapAttribution string
	// Integrations forwards new pages to users' systems (R47-R51c); nil
	// leaves them out.
	Integrations *integrations.Service
	// MetricsToken guards /metrics ("" turns it off); Locks counts locked
	// accounts for it (nil leaves that metric out) (R52).
	MetricsToken string
	Locks        LockCounter
	Version      string
	// Tracer traces requests, ingest and live updates (R71, R72); nil
	// traces nothing. Requests counts them for /metrics.
	Tracer   trace.Tracer
	Requests *telemetry.RequestMetrics

	pages pageCounts
	http  *telemetry.HTTP
	// ingest stores one page at a time and announces it before the next, so
	// message IDs reach live connections in increasing order (ADR 0010 §3).
	ingest sync.Mutex
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Spans are named by these patterns, and nothing else (R70).
	var patterns []string
	handle := func(pattern string, h http.Handler) {
		mux.Handle(pattern, h)
		patterns = append(patterns, pattern)
	}
	handle("POST /api/v1/pages", http.HandlerFunc(s.postPage))
	// Everything else under /api/v1 needs a signed-in user (A29).
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/meta":                         s.getMeta,
		"GET /api/v1/messages":                     s.listMessages,
		"GET /api/v1/messages/{id}":                s.getMessage,
		"GET /api/v1/messages/by-id":               s.messagesByID,
		"GET /api/v1/jobs/{job}":                   s.getJob,
		"GET /api/v1/capcodes/unaliased":           s.unaliased,
		"GET /api/v1/capcodes/{capcode}":           s.getCapcode,
		"GET /api/v1/aliases/{id}":                 s.getAlias,
		"GET /api/v1/map":                          s.mapMessages,
		"GET /api/v1/map/tiles.pmtiles":            s.mapTiles,
		"GET /api/v1/reports/capcode":              s.capcodeReport,
		"GET /api/v1/admin/retention":              s.adminOnly(s.getRetention),
		"PUT /api/v1/admin/retention":              s.adminOnly(s.putRetention),
		"GET /api/v1/stream":                       s.stream,
		"GET /api/v1/me":                           s.getMe,
		"DELETE /api/v1/me":                        s.deleteMe,
		"GET /api/v1/invitations":                  s.myInvitations,
		"POST /api/v1/invitations":                 s.invite,
		"DELETE /api/v1/invitations/{id}":          s.cancelInvitation,
		"GET /api/v1/admin/clients":                s.adminOnly(s.listClients),
		"GET /api/v1/admin/users":                  s.adminOnly(s.listUsers),
		"GET /api/v1/admin/invitations":            s.adminOnly(s.allInvitations),
		"POST /api/v1/admin/users/{id}/deactivate": s.adminOnly(s.deactivate),
		"POST /api/v1/admin/users/{id}/reactivate": s.adminOnly(s.reactivate),
		"PUT /api/v1/admin/users/{id}/role":        s.adminOnly(s.setRole),
		// Invite trees (R42a-R42d, ADR 0008).
		"GET /api/v1/admin/users/{id}/deactivation-preview": s.adminOnly(s.deactivationPreview),
		"POST /api/v1/admin/users/{id}/deactivate-tree":     s.adminOnly(s.deactivateTree),
		"PUT /api/v1/admin/users/{id}/trusted":              s.adminOnly(s.setTrusted),
		"GET /api/v1/integrations/selection":                s.suggestSelection,
		"GET /api/v1/integrations":                          s.listIntegrations,
		"POST /api/v1/integrations":                         s.createIntegration,
		"GET /api/v1/integrations/{id}":                     s.getIntegration,
		"PUT /api/v1/integrations/{id}":                     s.updateIntegration,
		"DELETE /api/v1/integrations/{id}":                  s.deleteIntegration,
		"POST /api/v1/integrations/{id}/test":               s.testIntegration,
		"POST /api/v1/integrations/{id}/pause":              s.pauseIntegration(false),
		"POST /api/v1/integrations/{id}/resume":             s.resumeIntegration(false),
		"GET /api/v1/integrations/{id}/deliveries":          s.integrationDeliveries,
		"GET /api/v1/admin/users/{id}":                      s.adminOnly(s.adminUser),
		"POST /api/v1/admin/integrations/{id}/pause":        s.adminOnly(s.pauseIntegration(true)),
		"POST /api/v1/admin/integrations/{id}/resume":       s.adminOnly(s.resumeIntegration(true)),
		"GET /api/v1/admin/integration-hosts":               s.adminOnly(s.getAllowList),
		"PUT /api/v1/admin/integration-hosts":               s.adminOnly(s.putAllowList),
		// Aliases in the browser (R53-R58, ADR 0007).
		"GET /api/v1/templates":                          s.listTemplates,
		"GET /api/v1/alias-requests":                     s.myRequests,
		"POST /api/v1/alias-requests":                    s.submitRequest,
		"PUT /api/v1/alias-requests/{id}":                s.editRequest,
		"POST /api/v1/alias-requests/{id}/withdraw":      s.withdrawRequest,
		"GET /api/v1/admin/alias-requests":               s.adminOnly(s.adminRequests),
		"POST /api/v1/admin/alias-requests/{id}/approve": s.adminOnly(s.approveRequest),
		"POST /api/v1/admin/alias-requests/{id}/reject":  s.adminOnly(s.rejectRequest),
		"GET /api/v1/admin/aliases":                      s.adminOnly(s.adminAliases),
		"POST /api/v1/admin/aliases":                     s.adminOnly(s.createAlias),
		"GET /api/v1/admin/aliases/export":               s.adminOnly(s.exportAliases),
		"GET /api/v1/admin/aliases/{id}":                 s.adminOnly(s.adminAlias),
		"PUT /api/v1/admin/aliases/{id}":                 s.adminOnly(s.editAlias),
		"DELETE /api/v1/admin/aliases/{id}":              s.adminOnly(s.deleteAlias),
		// For the PowerShell module (R63, ADR 0009 §8).
		"POST /api/v1/admin/aliases/import":   s.adminOnly(s.importAliases),
		"GET /api/v1/admin/templates":         s.adminOnly(s.adminTemplates),
		"POST /api/v1/admin/templates":        s.adminOnly(s.createTemplate),
		"PATCH /api/v1/admin/templates/{id}":  s.adminOnly(s.editTemplate),
		"DELETE /api/v1/admin/templates/{id}": s.adminOnly(s.deleteTemplate),
		// Template icons (R64-R66, ADR 0011).
		"PUT /api/v1/admin/templates/{id}/icon":    s.adminOnly(s.putTemplateIcon),
		"DELETE /api/v1/admin/templates/{id}/icon": s.adminOnly(s.deleteTemplateIcon),
		"GET /api/v1/admin/agency-profiles":        s.adminOnly(s.agencyProfiles),
		"GET /api/v1/icons/{file}":                 s.getIcon,
	} {
		handle(pattern, s.signedIn(h))
	}
	// Any other /api/v1 path: 401 without a session, 404 with one.
	handle("/api/", s.signedIn(func(w http.ResponseWriter, r *http.Request) { problem(w, http.StatusNotFound, "not found") }))
	handle("GET /auth/login", http.HandlerFunc(s.login))
	handle("GET /auth/callback", http.HandlerFunc(s.callback))
	handle("POST /auth/logout", http.HandlerFunc(s.logout))
	handle("GET /auth/status", http.HandlerFunc(s.authStatus))
	handle("GET /auth/powershell", http.HandlerFunc(s.powershellSettings))
	handle("POST /auth/first-admin", http.HandlerFunc(s.firstAdmin))
	handle("GET /metrics", http.HandlerFunc(s.metrics))
	handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	if s.UI != nil {
		handle("/", s.UI)
	}
	telemetry.AllowSpanNames(telemetry.RouteNames(patterns)...)
	if s.Requests == nil {
		s.Requests = &telemetry.RequestMetrics{}
	}
	// A client's page post starts its span once the key is checked, so only
	// a client's trace context is continued (R70).
	s.http = &telemetry.HTTP{Tracer: s.Tracer, Metrics: s.Requests, Deferred: func(r *http.Request) bool {
		return r.Method == http.MethodPost && r.URL.Path == "/api/v1/pages"
	}}
	// Browsers may only change things from this site; API clients (no
	// Origin or Sec-Fetch-Site header) are unaffected.
	return s.http.Handler(securityHeaders(http.NewCrossOriginProtection().Handler(mux)))
}

// securityHeaders sets headers every response gets.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

// PageRequest is the body of POST /api/v1/pages.
type PageRequest struct {
	ClientID   string    `json:"client_id"`
	Capcode    string    `json:"capcode"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
	Function   int       `json:"function"`
	Baud       int       `json:"baud"`
}

// PageResponse is the reply to a stored, duplicate or ignored page.
type PageResponse struct {
	Outcome   ingest.Outcome `json:"outcome"`
	MessageID int64          `json:"message_id,omitempty"`
}

var capcodeRe = regexp.MustCompile(`^[0-9]{1,7}$`)

func (s *Server) postPage(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authenticate(r.Context(), w, r)
	// The trace a request carries is continued only once its key says it's
	// a receiving client's; anything else starts its own (R70, R71).
	r = telemetry.ContinueRequest(s.Tracer, r, ok)
	ctx := r.Context()
	if !ok {
		return
	}
	// The ingest span: its outcome, and the stored message's ID (R71).
	ctx, span := telemetry.Or(s.Tracer).Start(ctx, "ingest", trace.WithAttributes(attribute.String("pagerarchive.client.id", clientID)))
	defer span.End()
	refused := func(kind string) {
		span.SetAttributes(attribute.String("pagerarchive.ingest.outcome", "refused"), attribute.String("error.type", kind))
	}
	var req PageRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// Decoder errors can quote the body, so they're never echoed or logged.
		refused("invalid_json")
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch {
	case req.ClientID != "" && req.ClientID != clientID:
		refused("wrong_client")
		problem(w, http.StatusForbidden, "client_id doesn't match the API key")
		return
	case !capcodeRe.MatchString(req.Capcode):
		refused("invalid_capcode")
		problem(w, http.StatusUnprocessableEntity, "capcode must be 1 to 7 digits")
		return
	case req.Text == "" || len(req.Text) > MaxTextBytes || !utf8.ValidString(req.Text):
		refused("invalid_text")
		problem(w, http.StatusUnprocessableEntity, "text must be 1 to 4096 bytes of UTF-8")
		return
	case req.ReceivedAt.IsZero():
		refused("no_receive_time")
		problem(w, http.StatusUnprocessableEntity, "received_at is required")
		return
	case req.ReceivedAt.After(s.Now().Add(MaxFuture)):
		refused(CodeClockAhead)
		s.pages.add("clock_ahead")
		s.Log.WarnContext(ctx, "page rejected: client clock ahead", "client", clientID)
		if err := s.Store.MarkClockAhead(ctx, clientID); err != nil {
			s.Log.ErrorContext(ctx, "mark clock ahead", "client", clientID, "error", store.SafeError(err))
		}
		writeJSON(w, http.StatusUnprocessableEntity, Problem{
			Error: "received_at is more than 60 s ahead of the server's clock",
			Code:  CodeClockAhead,
		})
		return
	}
	s.ingest.Lock()
	outcome, id, err := s.Ingest.Ingest(ctx, clientID, ingest.Page{
		Capcode:    decoder.PadCapcode(req.Capcode),
		Text:       req.Text,
		ReceivedAt: req.ReceivedAt.UTC(),
		Function:   req.Function,
		Baud:       req.Baud,
	})
	if err == nil && outcome == ingest.Stored && s.Hub != nil {
		// The live update: the open lists and watches it reached (R71). No
		// span per viewer, so no trace ties a user to a message (R70).
		_, span := telemetry.Or(s.Tracer).Start(ctx, "live.publish")
		span.SetAttributes(attribute.Int("pagerarchive.live.connections", s.Hub.Publish(id)))
		span.End()
	}
	s.ingest.Unlock()
	if err != nil {
		span.SetAttributes(attribute.String("error.type", "store_failed"))
		span.SetStatus(codes.Error, "")
		s.Log.ErrorContext(ctx, "ingest failed", "client", clientID, "error", err.Error())
		problem(w, http.StatusInternalServerError, "couldn't store the page")
		return
	}
	span.SetAttributes(attribute.String("pagerarchive.ingest.outcome", string(outcome)))
	if id != 0 {
		span.SetAttributes(attribute.Int64("pagerarchive.message.id", id))
	}
	s.Log.InfoContext(ctx, "page", "client", clientID, "outcome", string(outcome), "message_id", id)
	s.pages.add(string(outcome))
	if outcome == ingest.Stored {
		// Its deliveries were queued with it; send them now (R51a: within 5 s).
		s.Integrations.Wake()
	}
	writeJSON(w, http.StatusOK, PageResponse{Outcome: outcome, MessageID: id})
}

func (s *Server) authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, bool) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || key == "" {
		problem(w, http.StatusUnauthorized, "missing API key")
		return "", false
	}
	id, err := s.Store.ActiveClientByKeyHash(ctx, store.HashKey(key))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusUnauthorized, "unknown or revoked API key")
		return "", false
	}
	if err != nil {
		s.Log.ErrorContext(ctx, "authenticate", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't check the API key")
		return "", false
	}
	return id, true
}

// Problem is the body of an error response. Code, when set, is stable for
// clients to act on.
type Problem struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// CodeClockAhead: the receive time is more than MaxFuture ahead of the
// server's clock (ADR 0002).
const CodeClockAhead = "clock_ahead"

func problem(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Problem{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
