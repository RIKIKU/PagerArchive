// Package server is the HTTP API. Handlers never log or echo page text (P1).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// MaxFuture is how far ahead of the server's clock a receive time may be.
const MaxFuture = 60 * time.Second

// MaxTextBytes caps a page's text. POCSAG pages are far shorter.
const MaxTextBytes = 4096

// Server serves the API.
type Server struct {
	Store  *store.Store
	Ingest *ingest.Service
	Log    *slog.Logger
	Now    func() time.Time
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pages", s.postPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
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
	outcome, id, err := s.Ingest.Ingest(ctx, clientID, ingest.Page{
		Capcode:    decoder.PadCapcode(req.Capcode),
		Text:       req.Text,
		ReceivedAt: req.ReceivedAt.UTC(),
		Function:   req.Function,
		Baud:       req.Baud,
	})
	if err != nil {
		s.Log.Error("ingest failed", "client", clientID, "error", err.Error())
		problem(w, http.StatusInternalServerError, "couldn't store the page")
		return
	}
	s.Log.Info("page", "client", clientID, "outcome", string(outcome), "message_id", id)
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
