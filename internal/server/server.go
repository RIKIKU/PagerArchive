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

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/store"
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

	pages pageCounts
	// ingest stores one page at a time and announces it before the next, so
	// message IDs reach live connections in increasing order (ADR 0010 §3).
	ingest sync.Mutex
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pages", s.postPage)
	// Everything else under /api/v1 needs a signed-in user (A29).
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/meta":                         s.getMeta,
		"GET /api/v1/messages":                     s.listMessages,
		"GET /api/v1/messages/{id}":                s.getMessage,
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
		mux.HandleFunc(pattern, s.signedIn(h))
	}
	// Any other /api/v1 path: 401 without a session, 404 with one.
	mux.HandleFunc("/api/", s.signedIn(func(w http.ResponseWriter, r *http.Request) { problem(w, http.StatusNotFound, "not found") }))
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("GET /auth/status", s.authStatus)
	mux.HandleFunc("GET /auth/powershell", s.powershellSettings)
	mux.HandleFunc("POST /auth/first-admin", s.firstAdmin)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	if s.UI != nil {
		mux.Handle("/", s.UI)
	}
	// Browsers may only change things from this site; API clients (no
	// Origin or Sec-Fetch-Site header) are unaffected.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
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
	ctx := r.Context()
	clientID, ok := s.authenticate(ctx, w, r)
	if !ok {
		return
	}
	var req PageRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// Decoder errors can quote the body, so they're never echoed or logged.
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch {
	case req.ClientID != "" && req.ClientID != clientID:
		problem(w, http.StatusForbidden, "client_id doesn't match the API key")
		return
	case !capcodeRe.MatchString(req.Capcode):
		problem(w, http.StatusUnprocessableEntity, "capcode must be 1 to 7 digits")
		return
	case req.Text == "" || len(req.Text) > MaxTextBytes || !utf8.ValidString(req.Text):
		problem(w, http.StatusUnprocessableEntity, "text must be 1 to 4096 bytes of UTF-8")
		return
	case req.ReceivedAt.IsZero():
		problem(w, http.StatusUnprocessableEntity, "received_at is required")
		return
	case req.ReceivedAt.After(s.Now().Add(MaxFuture)):
		s.pages.add("clock_ahead")
		s.Log.Warn("page rejected: client clock ahead", "client", clientID)
		if err := s.Store.MarkClockAhead(ctx, clientID); err != nil {
			s.Log.Error("mark clock ahead", "client", clientID, "error", store.SafeError(err))
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
		s.Hub.Publish(id)
	}
	s.ingest.Unlock()
	if err != nil {
		s.Log.Error("ingest failed", "client", clientID, "error", err.Error())
		problem(w, http.StatusInternalServerError, "couldn't store the page")
		return
	}
	s.Log.Info("page", "client", clientID, "outcome", string(outcome), "message_id", id)
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
		s.Log.Error("authenticate", "error", store.SafeError(err))
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
