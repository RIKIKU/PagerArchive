package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Albums (R82-R82b, R82f; ADR 0025). An album is its creator's alone: every
// handler reads it with the signed-in user as its owner, so anyone else,
// admins included, gets "no such album", the same as for an album that
// doesn't exist. Names, descriptions and notes are the user's own words:
// never logged, never searched, and the redaction rules don't apply to them.

// Album limits, in characters (R82f).
const (
	MaxAlbumName        = 100
	MaxAlbumDescription = 2000
	MaxAlbumNote        = 500
	maxAlbumBody        = 32 << 10
)

// Album is an album as its owner sees it.
type Album struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Messages    int64     `json:"messages"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// EmptiedAt is set when retention took its last message and it was kept
	// for its description (R82a); null otherwise.
	EmptiedAt *time.Time `json:"emptied_at"`
}

func albumOf(r db.MyAlbumRow) Album {
	return Album{ID: r.ID, Name: r.Name, Description: r.Description, Messages: r.Messages, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), EmptiedAt: utc(r.EmptiedAt)}
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// AlbumInput is the body of POST /api/v1/albums and PATCH /api/v1/albums/{id}.
// In a PATCH, a field left out is unchanged; a description of "" removes it.
type AlbumInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// NoteInput is the body of PUT /api/v1/albums/{id}/messages/{message}/note:
// "" or null removes the note.
type NoteInput struct {
	Note *string `json:"note"`
}

func decodeAlbumBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlbumBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// plainText checks the user's own text: valid UTF-8, at most max characters,
// and no control characters but line breaks and tabs (and none of those in a
// name). It's trimmed; empty is nil.
func plainText(s *string, field string, max int, lines bool) (*string, string) {
	if s == nil {
		return nil, ""
	}
	v := strings.TrimSpace(*s)
	if !utf8.ValidString(v) {
		return nil, field + " must be text"
	}
	for _, c := range v {
		if unicode.IsControl(c) && !(lines && (c == '\n' || c == '\r' || c == '\t')) {
			return nil, field + " can't hold control characters"
		}
	}
	if n := utf8.RuneCountInString(v); n > max {
		return nil, field + " can be at most " + strconv.Itoa(max) + " characters (this has " + strconv.Itoa(n) + ")"
	}
	if v == "" {
		return nil, ""
	}
	return &v, ""
}

func albumID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		problem(w, http.StatusNotFound, "no such album")
		return 0, false
	}
	return id, true
}

// myAlbum reads one of the signed-in user's albums: not found (404) for an
// album that isn't theirs, whoever asks.
func (s *Server) myAlbum(w http.ResponseWriter, r *http.Request, id int64) (db.MyAlbumRow, bool) {
	a, err := s.Store.MyAlbum(r.Context(), db.MyAlbumParams{ID: id, OwnerID: currentUser(r).ID})
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "no such album")
		return a, false
	}
	if err != nil {
		s.Log.Error("album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return a, false
	}
	return a, true
}

// albumParam reads the album a message list, its counts or its live updates
// are kept to (?album=), checking it's the signed-in user's (R82). ok is
// false when it answered the request.
func (s *Server) albumParam(w http.ResponseWriter, r *http.Request) (album *int64, ok bool) {
	v := r.URL.Query().Get("album")
	if v == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < 1 {
		problem(w, http.StatusNotFound, "no such album")
		return nil, false
	}
	if _, ok := s.myAlbum(w, r, id); !ok {
		return nil, false
	}
	return &id, true
}

// duplicateName says whether err is the one-name-per-person constraint.
func duplicateName(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.MyAlbums(r.Context(), currentUser(r).ID)
	if err != nil {
		s.Log.Error("albums", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := []Album{}
	for _, a := range rows {
		out = append(out, albumOf(db.MyAlbumRow(a)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAlbum(w http.ResponseWriter, r *http.Request) {
	var in AlbumInput
	if !decodeAlbumBody(w, r, &in) {
		return
	}
	name, msg := plainText(in.Name, "The name", MaxAlbumName, false)
	if msg == "" && name == nil {
		msg = "Give the album a name"
	}
	desc, dmsg := plainText(in.Description, "The description", MaxAlbumDescription, true)
	if msg == "" {
		msg = dmsg
	}
	if msg != "" {
		problem(w, http.StatusUnprocessableEntity, msg)
		return
	}
	u := currentUser(r)
	id, err := s.Store.CreateAlbum(r.Context(), db.CreateAlbumParams{OwnerID: u.ID, Name: *name, Description: desc})
	if duplicateName(err) {
		problem(w, http.StatusConflict, "You already have an album with that name")
		return
	}
	if err != nil {
		s.Log.Error("create album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't create the album")
		return
	}
	a, ok := s.myAlbum(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, albumOf(a))
}

func (s *Server) getAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	if a, ok := s.myAlbum(w, r, id); ok {
		writeJSON(w, http.StatusOK, albumOf(a))
	}
}

// updateAlbum renames an album or changes its description (R82, R82f).
func (s *Server) updateAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	var in AlbumInput
	if !decodeAlbumBody(w, r, &in) {
		return
	}
	a, ok := s.myAlbum(w, r, id)
	if !ok {
		return
	}
	name, desc := a.Name, a.Description
	if in.Name != nil {
		n, msg := plainText(in.Name, "The name", MaxAlbumName, false)
		if msg == "" && n == nil {
			msg = "Give the album a name"
		}
		if msg != "" {
			problem(w, http.StatusUnprocessableEntity, msg)
			return
		}
		name = *n
	}
	if in.Description != nil {
		d, msg := plainText(in.Description, "The description", MaxAlbumDescription, true)
		if msg != "" {
			problem(w, http.StatusUnprocessableEntity, msg)
			return
		}
		desc = d
	}
	n, err := s.Store.UpdateAlbum(r.Context(), db.UpdateAlbumParams{ID: id, OwnerID: currentUser(r).ID, Name: name, Description: desc})
	if duplicateName(err) {
		problem(w, http.StatusConflict, "You already have an album with that name")
		return
	}
	if err != nil {
		s.Log.Error("update album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't change the album")
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such album")
		return
	}
	if a, ok = s.myAlbum(w, r, id); ok {
		writeJSON(w, http.StatusOK, albumOf(a))
	}
}

func (s *Server) deleteAlbum(w http.ResponseWriter, r *http.Request) {
	id, ok := albumID(w, r)
	if !ok {
		return
	}
	n, err := s.Store.DeleteAlbum(r.Context(), db.DeleteAlbumParams{ID: id, OwnerID: currentUser(r).ID})
	if err != nil {
		s.Log.Error("delete album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't delete the album")
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such album")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// albumMessage reads the album and message of /api/v1/albums/{id}/messages/{message}.
func (s *Server) albumMessage(w http.ResponseWriter, r *http.Request) (album, message int64, ok bool) {
	if album, ok = albumID(w, r); !ok {
		return
	}
	message, err := strconv.ParseInt(r.PathValue("message"), 10, 64)
	if err != nil || message < 1 {
		problem(w, http.StatusNotFound, "no such message")
		return 0, 0, false
	}
	if _, ok = s.myAlbum(w, r, album); !ok {
		return
	}
	return album, message, true
}

// addToAlbum adds a message to an album (R82). Adding it again changes
// nothing, its note included.
func (s *Server) addToAlbum(w http.ResponseWriter, r *http.Request) {
	album, message, ok := s.albumMessage(w, r)
	if !ok {
		return
	}
	found, err := s.Store.MessagesByIDs(r.Context(), []int64{message})
	if err == nil && len(found) == 0 {
		problem(w, http.StatusNotFound, "no such message")
		return
	}
	if err == nil {
		_, err = s.Store.AddToAlbum(r.Context(), db.AddToAlbumParams{AlbumID: album, OwnerID: currentUser(r).ID, MessageID: message})
	}
	if err != nil {
		s.Log.Error("add to album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't add it")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeFromAlbum(w http.ResponseWriter, r *http.Request) {
	album, message, ok := s.albumMessage(w, r)
	if !ok {
		return
	}
	n, err := s.Store.RemoveFromAlbum(r.Context(), db.RemoveFromAlbumParams{AlbumID: album, OwnerID: currentUser(r).ID, MessageID: message})
	if err != nil {
		s.Log.Error("remove from album", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't remove it")
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "that message isn't in this album")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setNote sets or removes a message's note in one album (R82f).
func (s *Server) setNote(w http.ResponseWriter, r *http.Request) {
	album, message, ok := s.albumMessage(w, r)
	if !ok {
		return
	}
	var in NoteInput
	if !decodeAlbumBody(w, r, &in) {
		return
	}
	note, msg := plainText(in.Note, "The note", MaxAlbumNote, true)
	if msg != "" {
		problem(w, http.StatusUnprocessableEntity, msg)
		return
	}
	n, err := s.Store.SetAlbumNote(r.Context(), db.SetAlbumNoteParams{AlbumID: album, OwnerID: currentUser(r).ID, MessageID: message, Note: note})
	if err != nil {
		s.Log.Error("album note", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't save the note")
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "that message isn't in this album")
		return
	}
	writeJSON(w, http.StatusOK, NoteInput{Note: note})
}

// messageAlbums lists which of the signed-in user's albums hold a message,
// for the detail view's "Add to album" (R82).
func (s *Server) messageAlbums(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusNotFound, "no such message")
		return
	}
	ids, err := s.Store.AlbumsHolding(r.Context(), db.AlbumsHoldingParams{OwnerID: currentUser(r).ID, MessageID: id})
	if err != nil {
		s.Log.Error("message albums", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	writeJSON(w, http.StatusOK, map[string][]int64{"albums": ids})
}

// addNotes gives each message its note in the album (R82f).
func (s *Server) addNotes(r *http.Request, album int64, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	notes, err := s.Store.AlbumNotes(r.Context(), db.AlbumNotesParams{AlbumID: album, Ids: ids})
	if err != nil {
		return errors.New(store.SafeError(err))
	}
	byID := map[int64]string{}
	for _, n := range notes {
		byID[n.MessageID] = n.Note
	}
	for i := range msgs {
		if n, ok := byID[msgs[i].ID]; ok {
			msgs[i].Note = &n
		}
	}
	return nil
}
