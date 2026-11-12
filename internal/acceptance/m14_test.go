package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M14 at API level (ADR 0025): albums (R82-R82b, R82f, A72) and the
// scrubber's counts and jumps (R81b-R81d, A71). The browser's side is in
// web/tests/e2e.spec.ts.

func (m *member) newAlbum(name string, description *string) server.Album {
	m.e.t.Helper()
	var a server.Album
	if code, p := m.call(http.MethodPost, "/api/v1/albums", server.AlbumInput{Name: &name, Description: description}, &a); code != http.StatusCreated {
		m.e.t.Fatalf("%s creates album %q: %d %s", m.email, name, code, p.Error)
	}
	return a
}

func (m *member) addTo(album, message int64) {
	m.e.t.Helper()
	if code, p := m.call(http.MethodPut, fmt.Sprintf("/api/v1/albums/%d/messages/%d", album, message), nil, nil); code != http.StatusNoContent {
		m.e.t.Fatalf("%s adds %d to album %d: %d %s", m.email, message, album, code, p.Error)
	}
}

func (m *member) note(album, message int64, note string) int {
	m.e.t.Helper()
	code, _ := m.call(http.MethodPut, fmt.Sprintf("/api/v1/albums/%d/messages/%d/note", album, message), server.NoteInput{Note: &note}, nil)
	return code
}

func (m *member) album(id int64) (int, server.Album) {
	m.e.t.Helper()
	var a server.Album
	code, _ := m.call(http.MethodGet, fmt.Sprintf("/api/v1/albums/%d", id), nil, &a)
	return code, a
}

func (m *member) albumList(id int64, query string) []server.Message {
	m.e.t.Helper()
	return m.list(fmt.Sprintf("album=%d&ignored=true&%s", id, query))
}

func (m *member) albums() []server.Album {
	m.e.t.Helper()
	var out []server.Album
	if code, p := m.call(http.MethodGet, "/api/v1/albums", nil, &out); code != http.StatusOK {
		m.e.t.Fatalf("%s lists albums: %d %s", m.email, code, p.Error)
	}
	return out
}


func texts(ms []server.Message) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.DisplayText)
	}
	return out
}

// A72. Albums: made, shown with their messages as they are now, private,
// thinned and removed by retention, kept for a description, and gone with
// the account.
func TestA72Albums(t *testing.T) {
	e := setup(t)
	b := e.inviteAndEnrol(e.user, "b@example.com")
	c := e.inviteAndEnrol(e.user, "c@example.com")
	mel := e.job.Location
	run := time.Date(2026, time.October, 13, 3, 0, 0, 0, mel)
	at := func(days int, text string) int64 {
		return e.mustPost("a", "0100001", text, run.AddDate(0, 0, -days)).MessageID
	}
	m1 := at(50, "@@EX0000701 HSF1 SMOKE FROM SHED @ 12 FICTION ST, MADEUPTON #STN1")
	m2 := at(45, "HbRE EX0000701 CREWS RETURNING #STN1")
	m3 := at(35, "QDCHECK THE TRAILER LIGHTS")

	// B creates "Big storm" and adds 3 messages: it's under Albums with 3.
	big := b.newAlbum("Big storm", nil)
	for _, id := range []int64{m1, m2, m3} {
		b.addTo(big.ID, id)
	}
	if got := b.albums(); len(got) != 1 || got[0].Name != "Big storm" || got[0].Messages != 3 {
		t.Fatalf("B's albums: %+v", got)
	}
	if got := texts(b.albumList(big.ID, "")); !slices.Equal(got, []string{"CHECK THE TRAILER LIGHTS", "RE EX0000701 CREWS RETURNING #STN1", "EX0000701 HSF1 SMOKE FROM SHED @ 12 FICTION ST, MADEUPTON #STN1"}) {
		t.Errorf("album, newest first: %q", got)
	}
	// User C and an admin are refused.
	for _, who := range []*member{c, e.admin} {
		if code, _ := who.album(big.ID); code != http.StatusNotFound {
			t.Errorf("%s reads B's album: %d, want 404", who.email, code)
		}
		if code, _ := who.call(http.MethodGet, fmt.Sprintf("/api/v1/messages?album=%d", big.ID), nil, nil); code != http.StatusNotFound {
			t.Errorf("%s lists B's album's messages: %d, want 404", who.email, code)
		}
	}

	// Aliases as they are now (R23a): Station 1 renamed.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE aliases SET name = 'Station One' WHERE pattern = '0100001'`); err != nil {
		t.Fatal(err)
	}
	for _, m := range b.albumList(big.ID, "") {
		if m.Alias == nil || *m.Alias != "Station One" {
			t.Errorf("album shows alias %v, want the current name", m.Alias)
		}
	}
	// A redaction rule matching one of its messages: as if m3 was stored
	// before a rule matching this number existed.
	phone := "0412 345 678"
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET text = $2 WHERE id = $1`, m3, "QDCHECK THE TRAILER LIGHTS, CALL "+phone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job.RunOnce(e.ctx, run); err != nil {
		t.Fatal(err)
	}
	if got := texts(b.albumList(big.ID, "")); len(got) != 3 || got[0] != "CHECK THE TRAILER LIGHTS, CALL REDACTED" {
		t.Errorf("after the nightly run: %q, want the message redacted", got)
	}

	// Descriptions and notes: "Storm notes", with a description and 2
	// messages, one with a note.
	desc := "Pages from the made-up storm"
	notes := b.newAlbum("Storm notes", &desc)
	b.addTo(notes.ID, m1)
	b.addTo(notes.ID, m2)
	if code := b.note(notes.ID, m1, "First page of the storm"); code != http.StatusOK {
		t.Fatalf("B writes a note: %d", code)
	}
	if code, a := b.album(notes.ID); code != http.StatusOK || a.Description == nil || *a.Description != desc || a.Messages != 2 {
		t.Errorf("Storm notes: %d %+v", code, a)
	}
	byID := map[int64]server.Message{}
	for _, m := range b.albumList(notes.ID, "") {
		byID[m.ID] = m
	}
	if n := byID[m1].Note; n == nil || *n != "First page of the storm" {
		t.Errorf("m1's note: %v", n)
	}
	if n := byID[m2].Note; n != nil {
		t.Errorf("m2's note: %q, want none", *n)
	}
	// C can read neither.
	if code, _ := c.album(notes.ID); code != http.StatusNotFound {
		t.Errorf("C reads Storm notes: %d", code)
	}
	if code, _ := c.call(http.MethodGet, fmt.Sprintf("/api/v1/messages?album=%d", notes.ID), nil, nil); code != http.StatusNotFound {
		t.Errorf("C reads Storm notes' notes: %d", code)
	}
	// The same message in a second album has no note there until B writes one.
	for _, m := range b.albumList(big.ID, "") {
		if m.Note != nil {
			t.Errorf("message %d has note %q in Big storm", m.ID, *m.Note)
		}
	}
	// The search box's storm finds nothing from a description or a note.
	if got := b.list("q=storm"); len(got) != 0 {
		t.Errorf("q=storm lists %q, want nothing", texts(got))
	}
	if got := b.list("q=" + url.QueryEscape(`"First page"`)); len(got) != 0 {
		t.Errorf(`q="First page" lists %q, want nothing`, texts(got))
	}

	// Retention deletes 2 of Big storm's messages: it holds 1.
	if err := e.job.SetDays(e.ctx, 40); err != nil {
		t.Fatal(err)
	}
	if res, err := e.job.RunOnce(e.ctx, run); err != nil || res.Deleted != 2 {
		t.Fatalf("nightly: %+v %v", res, err)
	}
	if code, a := b.album(big.ID); code != http.StatusOK || a.Messages != 1 {
		t.Errorf("Big storm after retention: %d %+v, want 1 message", code, a)
	}
	// Both of Storm notes' messages went: it's kept for its description, empty,
	// says so, and m1's note went with it.
	code, a := b.album(notes.ID)
	if code != http.StatusOK || a.Messages != 0 || a.EmptiedAt == nil || a.Description == nil || *a.Description != desc {
		t.Errorf("Storm notes after retention: %d %+v, want kept, empty, with its description and emptied_at", code, a)
	}
	if rows := e.rowsContaining("First page of the storm"); len(rows) != 0 {
		t.Errorf("the note is still stored: %v", rows)
	}
	// The last of Big storm goes: so does the album.
	if err := e.job.SetDays(e.ctx, 30); err != nil {
		t.Fatal(err)
	}
	if res, err := e.job.RunOnce(e.ctx, run); err != nil || res.Deleted != 1 {
		t.Fatalf("nightly: %+v %v", res, err)
	}
	if code, _ := b.album(big.ID); code != http.StatusNotFound {
		t.Errorf("Big storm after its last message: %d, want gone", code)
	}
	if got := b.albums(); len(got) != 1 || got[0].Name != "Storm notes" {
		t.Errorf("B's albums: %+v", got)
	}

	// B deletes their account: B's albums are gone.
	b.newAlbum("Another", ptr("kept until the account goes"))
	if code, p := b.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("B deletes their account: %d %s", code, p.Error)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM albums WHERE owner_id = $1`, b.id).Scan(&n); err != nil || n != 0 {
		t.Errorf("B's albums after deleting the account: %d (%v)", n, err)
	}
	if rows := e.rowsContaining("Pages from the made-up storm"); len(rows) != 0 {
		t.Errorf("B's description is still stored: %v", rows)
	}
	t.Log("A72: albums list their messages newest first as they are now, refuse C and the admin, lose messages to retention, are kept for a description and go with the account")
}

// R82: every album endpoint refuses everyone but the album's owner, admins
// included, and leaves the album as it was.
func TestAlbumsArePrivate(t *testing.T) {
	e := setup(t)
	b, c := e.user, e.inviteAndEnrol(e.user, "c@example.com")
	id := e.mustPost("a", "0100001", "QDTRAINING TONIGHT", e.now).MessageID
	other := e.mustPost("a", "0100002", "QDTRAINING TOMORROW", e.now.Add(-time.Minute)).MessageID
	a := b.newAlbum("Mine", ptr("Only mine"))
	b.addTo(a.ID, id)
	if b.note(a.ID, id, "My note") != http.StatusOK {
		t.Fatal("note")
	}
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/albums/%d"},
		{http.MethodPatch, "/api/v1/albums/%d"},
		{http.MethodDelete, "/api/v1/albums/%d"},
		{http.MethodPut, "/api/v1/albums/%d/messages/" + fmt.Sprint(other)},
		{http.MethodDelete, "/api/v1/albums/%d/messages/" + fmt.Sprint(id)},
		{http.MethodPut, "/api/v1/albums/%d/messages/" + fmt.Sprint(id) + "/note"},
		{http.MethodGet, "/api/v1/messages?album=%d"},
		{http.MethodGet, "/api/v1/messages/counts?album=%d"},
		{http.MethodGet, "/api/v1/stream?album=%d"},
	}
	for _, who := range []*member{c, e.admin} {
		for _, ep := range endpoints {
			var body any
			switch ep.method {
			case http.MethodPatch:
				body = server.AlbumInput{Name: ptr("Taken"), Description: ptr("")}
			case http.MethodPut:
				body = server.NoteInput{Note: ptr("Not yours")}
			}
			path := fmt.Sprintf(ep.path, a.ID)
			resp, err := who.do(ep.method, e.url+path, body)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s: %s %s: %d, want 404", who.email, ep.method, path, resp.StatusCode)
			}
		}
		if got := who.albums(); len(got) != 0 {
			t.Errorf("%s's albums list %+v", who.email, got)
		}
		var holding map[string][]int64
		if code, _ := who.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/albums", id), nil, &holding); code != http.StatusOK || len(holding["albums"]) != 0 {
			t.Errorf("%s: the message's albums: %d %v", who.email, code, holding)
		}
	}
	// Unchanged for B.
	code, got := b.album(a.ID)
	ms := b.albumList(a.ID, "")
	if code != http.StatusOK || got.Name != "Mine" || got.Description == nil || *got.Description != "Only mine" || len(ms) != 1 || ms[0].ID != id ||
		ms[0].Note == nil || *ms[0].Note != "My note" {
		t.Errorf("B's album after the others tried: %d %+v %+v", code, got, ms)
	}
	// Notes are only in the album's own list, for its owner.
	for _, m := range e.list("") {
		if m.Note != nil {
			t.Errorf("the message list shows a note: %+v", m)
		}
	}
	var holding map[string][]int64
	if code, _ := b.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/albums", id), nil, &holding); code != http.StatusOK || !slices.Equal(holding["albums"], []int64{a.ID}) {
		t.Errorf("B: the message's albums: %d %v", code, holding)
	}
	// Names, descriptions and notes never reach the log.
	for _, s := range []string{"Only mine", "My note", "Not yours"} {
		if strings.Contains(e.logs.String(), s) {
			t.Errorf("the log holds %q", s)
		}
	}
}

// R82f: the limits, in characters, on names, descriptions and notes; plain
// text only; one name per person.
func TestAlbumLimits(t *testing.T) {
	e := setup(t)
	b := e.user
	id := e.mustPost("a", "0100001", "QDTRAINING TONIGHT", e.now).MessageID
	create := func(name string, desc *string) int {
		code, _ := b.call(http.MethodPost, "/api/v1/albums", server.AlbumInput{Name: &name, Description: desc}, nil)
		return code
	}
	for _, tt := range []struct {
		what string
		name string
		desc *string
		want int
	}{
		{"a 100-character name", strings.Repeat("n", 100), nil, http.StatusCreated},
		{"a 101-character name", strings.Repeat("m", 101), nil, http.StatusUnprocessableEntity},
		{"an empty name", "   ", nil, http.StatusUnprocessableEntity},
		{"a name with a line break", "Two\nlines", nil, http.StatusUnprocessableEntity},
		{"a 2,000-character description", "Long", ptr(strings.Repeat("é", 2000)), http.StatusCreated},
		{"a 2,001-character description", "Longer", ptr(strings.Repeat("d", 2001)), http.StatusUnprocessableEntity},
		{"a description with lines", "Lines", ptr("One\nTwo\n\tThree"), http.StatusCreated},
		{"a description with a control character", "Bell", ptr("Ring \a"), http.StatusUnprocessableEntity},
		{"the same name again, case aside", "LINES", nil, http.StatusConflict},
	} {
		if got := create(tt.name, tt.desc); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.what, got, tt.want)
		}
	}
	// Trimmed; an empty description is none.
	var a server.Album
	if code, _ := b.call(http.MethodPost, "/api/v1/albums", server.AlbumInput{Name: ptr("  Spaced  "), Description: ptr("  ")}, &a); code != http.StatusCreated || a.Name != "Spaced" || a.Description != nil {
		t.Errorf("trimmed: %d %+v", code, a)
	}
	b.addTo(a.ID, id)
	for _, tt := range []struct {
		note string
		want int
	}{
		{strings.Repeat("é", 500), http.StatusOK},
		{strings.Repeat("x", 501), http.StatusUnprocessableEntity},
		{"Line one\nline two", http.StatusOK},
	} {
		if got := b.note(a.ID, id, tt.note); got != tt.want {
			t.Errorf("a %d-character note: %d, want %d", len([]rune(tt.note)), got, tt.want)
		}
	}
	// Renaming to a name in use is refused; a description can be removed.
	if code, _ := b.call(http.MethodPatch, fmt.Sprintf("/api/v1/albums/%d", a.ID), server.AlbumInput{Name: ptr("lines")}, nil); code != http.StatusConflict {
		t.Errorf("rename to a name in use: %d", code)
	}
	var renamed server.Album
	if code, _ := b.call(http.MethodPatch, fmt.Sprintf("/api/v1/albums/%d", a.ID), server.AlbumInput{Name: ptr("Renamed"), Description: ptr("For training")}, &renamed); code != http.StatusOK ||
		renamed.Name != "Renamed" || renamed.Description == nil || *renamed.Description != "For training" {
		t.Errorf("rename: %d %+v", code, renamed)
	}
	if code, _ := b.call(http.MethodPatch, fmt.Sprintf("/api/v1/albums/%d", a.ID), server.AlbumInput{Description: ptr("")}, &renamed); code != http.StatusOK || renamed.Name != "Renamed" || renamed.Description != nil {
		t.Errorf("remove the description: %d %+v", code, renamed)
	}
	// A message that doesn't exist can't be added.
	if code, _ := b.call(http.MethodPut, fmt.Sprintf("/api/v1/albums/%d/messages/999999", a.ID), nil, nil); code != http.StatusNotFound {
		t.Errorf("a missing message: %d", code)
	}
	// Deleting it.
	if code, _ := b.call(http.MethodDelete, fmt.Sprintf("/api/v1/albums/%d", a.ID), nil, nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _ := b.album(a.ID); code != http.StatusNotFound {
		t.Errorf("after delete: %d", code)
	}
}

// R82f: a note belongs to a message in one album.
func TestAlbumNotesPerAlbum(t *testing.T) {
	e := setup(t)
	b := e.user
	id := e.mustPost("a", "0100001", "QDTRAINING TONIGHT", e.now).MessageID
	one, two := b.newAlbum("One", nil), b.newAlbum("Two", nil)
	b.addTo(one.ID, id)
	b.addTo(two.ID, id)
	noteIn := func(album int64) string {
		ms := b.albumList(album, "")
		if len(ms) != 1 {
			t.Fatalf("album %d holds %d", album, len(ms))
		}
		if ms[0].Note == nil {
			return ""
		}
		return *ms[0].Note
	}
	b.note(one.ID, id, "Why in one")
	if noteIn(one.ID) != "Why in one" || noteIn(two.ID) != "" {
		t.Errorf("notes: %q, %q", noteIn(one.ID), noteIn(two.ID))
	}
	b.note(two.ID, id, "Why in two")
	b.note(one.ID, id, "Changed in one")
	if noteIn(one.ID) != "Changed in one" || noteIn(two.ID) != "Why in two" {
		t.Errorf("notes: %q, %q", noteIn(one.ID), noteIn(two.ID))
	}
	// Adding it again keeps its note.
	b.addTo(one.ID, id)
	if noteIn(one.ID) != "Changed in one" {
		t.Errorf("after adding again: %q", noteIn(one.ID))
	}
	// "" removes it.
	b.note(one.ID, id, "")
	if noteIn(one.ID) != "" || noteIn(two.ID) != "Why in two" {
		t.Errorf("after removing: %q, %q", noteIn(one.ID), noteIn(two.ID))
	}
	// Taken out and put back, it has no note.
	if code, _ := b.call(http.MethodDelete, fmt.Sprintf("/api/v1/albums/%d/messages/%d", two.ID, id), nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	b.addTo(two.ID, id)
	if noteIn(two.ID) != "" {
		t.Errorf("put back: %q", noteIn(two.ID))
	}
	// A note for a message not in the album is refused.
	other := e.mustPost("a", "0100002", "QDNOT IN THE ALBUM", e.now.Add(-time.Minute)).MessageID
	if code := b.note(one.ID, other, "No"); code != http.StatusNotFound {
		t.Errorf("note on a message not in the album: %d", code)
	}
}

// R82a, R82b: retention keeps an album with no messages that it never
// emptied, deletes one it emptied that has no description, keeps one with a
// description (until a message is added again); a deactivated user's albums
// are kept.
func TestAlbumRetention(t *testing.T) {
	e := setup(t)
	b := e.user
	mel := e.job.Location
	run := time.Date(2026, time.October, 13, 3, 0, 0, 0, mel)
	old := e.mustPost("a", "0100001", "QDOLD PAGE", run.AddDate(0, 0, -100)).MessageID
	newer := e.mustPost("a", "0100001", "QDNEWER PAGE", run.AddDate(0, 0, -1)).MessageID
	empty := b.newAlbum("Never had any", nil)
	plain := b.newAlbum("Plain", nil)
	described := b.newAlbum("Described", ptr("Why I kept these"))
	mixed := b.newAlbum("Mixed", nil)
	b.addTo(plain.ID, old)
	b.addTo(described.ID, old)
	b.addTo(mixed.ID, old)
	b.addTo(mixed.ID, newer)
	if err := e.job.SetDays(e.ctx, 30); err != nil {
		t.Fatal(err)
	}
	if res, err := e.job.RunOnce(e.ctx, run); err != nil || res.Deleted != 1 {
		t.Fatalf("nightly: %+v %v", res, err)
	}
	if code, a := b.album(empty.ID); code != http.StatusOK || a.EmptiedAt != nil {
		t.Errorf("an album that never had messages: %d %+v, want kept", code, a)
	}
	if code, _ := b.album(plain.ID); code != http.StatusNotFound {
		t.Errorf("emptied, no description: %d, want deleted", code)
	}
	code, a := b.album(described.ID)
	if code != http.StatusOK || a.Messages != 0 || a.EmptiedAt == nil {
		t.Errorf("emptied, described: %d %+v, want kept and marked", code, a)
	}
	if code, a := b.album(mixed.ID); code != http.StatusOK || a.Messages != 1 || a.EmptiedAt != nil {
		t.Errorf("one left: %d %+v", code, a)
	}
	// Another run changes nothing; adding a message clears the mark.
	if _, err := e.job.RunOnce(e.ctx, run.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if code, a := b.album(described.ID); code != http.StatusOK || a.EmptiedAt == nil {
		t.Errorf("after another run: %d %+v", code, a)
	}
	b.addTo(described.ID, newer)
	if code, a := b.album(described.ID); code != http.StatusOK || a.Messages != 1 || a.EmptiedAt != nil {
		t.Errorf("a message added again: %d %+v, want the mark cleared", code, a)
	}
	// R82b: deactivated, B's albums are kept, and B has them back when reactivated.
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", b.id), server.DeactivateRequest{}, nil); code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", code, p.Error)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM albums WHERE owner_id = $1`, b.id).Scan(&n); err != nil || n != 3 {
		t.Errorf("a deactivated user's albums: %d (%v), want 3", n, err)
	}
}

// A71 at API level: the scrubber's marks for 40 days of pages, one day ten
// times as busy; counts that follow the search; zoom; and the jump.
func TestA71ScrubberCounts(t *testing.T) {
	e := setup(t)
	now := time.Now().In(time.UTC)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	busy := today.AddDate(0, 0, -10)
	// 40 days: 3 pages a day at 10:00, 12:00 and 14:00 to Station 1 and one at
	// 11:00 to Station 2; the busy day 30 to Station 1 and 10 to Station 2.
	for d := 39; d >= 2; d-- {
		day := today.AddDate(0, 0, -d)
		n1, n2 := 3, 1
		if day.Equal(busy) {
			n1, n2 = 30, 10
		}
		for i := range n1 {
			e.mustPost("a", "0100001", fmt.Sprintf("QDSTATION ONE DAY %d PAGE %d", d, i), day.Add(10*time.Hour+time.Duration(i)*time.Minute))
		}
		for i := range n2 {
			e.mustPost("a", "0100002", fmt.Sprintf("QDSTATION TWO DAY %d PAGE %d", d, i), day.Add(11*time.Hour+time.Duration(i)*time.Minute))
		}
	}
	// Yesterday: five pages in its 14:00 hour, at 14:02, 14:07, 14:12 (two) and 14:58.
	yesterday := today.AddDate(0, 0, -1)
	for i, min := range []int{2, 7, 12, 12, 58} {
		e.mustPost("a", "0100001", fmt.Sprintf("QDYESTERDAY PAGE %d", i), yesterday.Add(14*time.Hour+time.Duration(min)*time.Minute))
	}
	counts := func(query string) server.Counts {
		t.Helper()
		var c server.Counts
		if code := e.getJSON("/api/v1/messages/counts?"+query, &c); code != http.StatusOK {
			t.Fatalf("counts?%s: %d", query, code)
		}
		if !c.Counted {
			t.Fatalf("counts?%s: not counted", query)
		}
		return c
	}
	day := func(c server.Counts, d time.Time) int64 {
		t.Helper()
		for _, m := range c.Marks {
			if m.Start.Equal(d) && m.Kind == "day" {
				return m.Count
			}
		}
		t.Fatalf("no mark for %s", d.Format(time.DateOnly))
		return 0
	}
	all := counts("")
	if oldest := all.Marks[len(all.Marks)-1]; !oldest.Start.Equal(today.AddDate(0, 0, -29)) {
		t.Errorf("oldest mark %s, want 29 days ago: days before the last 30 aren't on the scrubber", oldest.Start)
	}
	var total int64
	for _, m := range all.Marks {
		total += m.Count
	}
	// 30 days back, less today and yesterday: 27 ordinary days of 4 and the busy day's 40, and yesterday's 5.
	if want := int64(27*4 + 40 + 5); total != want {
		t.Errorf("counted %d over the last 30 days, want %d", total, want)
	}
	if b, n := day(all, busy), day(all, busy.AddDate(0, 0, 1)); b != 40 || n != 4 {
		t.Errorf("busy day %d, its neighbour %d", b, n)
	}
	// alias:"Station 1" (the box sends a quoted alias as alias_like): only Station 1's.
	one := counts("alias_like=" + url.QueryEscape("Station 1"))
	if b, n := day(one, busy), day(one, busy.AddDate(0, 0, -1)); b != 30 || n != 3 {
		t.Errorf("Station 1: busy day %d, its neighbour %d", b, n)
	}
	// A 6-month range is in weeks, 2 years in months.
	kindsOf := func(c server.Counts) map[string]int {
		k := map[string]int{}
		for _, m := range c.Marks {
			k[m.Kind]++
		}
		return k
	}
	six := counts("from=" + today.AddDate(0, -6, 0).Format(time.DateOnly) + "&to=" + today.Format(time.DateOnly))
	if k := kindsOf(six); k["week"] < 26 || k["day"] != 0 || k["month"] != 0 {
		t.Errorf("6 months: %v, want weeks", k)
	}
	two := counts("from=" + today.AddDate(-2, 0, 0).Format(time.DateOnly) + "&to=" + today.Format(time.DateOnly))
	if k := kindsOf(two); k["month"] < 24 || k["week"] != 0 || k["day"] != 0 {
		t.Errorf("2 years: %v, want months", k)
	}
	var sum int64
	for _, m := range two.Marks {
		sum += m.Count
	}
	if want := int64(38*4 + 36 + 5); sum != want {
		t.Errorf("2 years: counted %d, want every page, %d", sum, want)
	}
	// Zoom: yesterday's 14:00 hour in 5-minute blocks.
	hour := yesterday.Add(14 * time.Hour)
	z := counts("hour=" + url.QueryEscape(hour.Format(time.RFC3339)))
	if len(z.Marks) != 12 {
		t.Fatalf("zoom: %d blocks", len(z.Marks))
	}
	want := []int64{1, 0, 0, 0, 0, 0, 0, 0, 0, 2, 1, 1} // newest first
	for i, m := range z.Marks {
		if m.Count != want[i] || m.Kind != "block" {
			t.Errorf("block %d (%s): %d %s, want %d", i, m.Start.Format("15:04"), m.Count, m.Kind, want[i])
		}
	}
	if code := e.getJSON("/api/v1/messages/counts?hour="+url.QueryEscape(today.AddDate(0, 0, -5).Add(14*time.Hour).Format(time.RFC3339)), nil); code != http.StatusBadRequest {
		t.Errorf("zooming into an older day: %d, want 400", code)
	}
	// The jump: the list at the end of 20 days ago starts with that day's last page.
	twenty := today.AddDate(0, 0, -20)
	var page server.MessagePage
	if code := e.getJSON("/api/v1/messages?at="+url.QueryEscape(twenty.AddDate(0, 0, 1).Format(time.RFC3339)), &page); code != http.StatusOK || len(page.Messages) == 0 {
		t.Fatalf("jump: %d %d", code, len(page.Messages))
	}
	// Day 20's last page is Station 2's, at 11:00.
	if first := page.Messages[0]; first.DisplayText != "STATION TWO DAY 20 PAGE 0" || page.Newer == "" || page.Next == "" {
		t.Errorf("jump: first %q, newer %q, next %q", first.DisplayText, page.Newer, page.Next)
	}
	// The newer messages: the five nearest, newest first.
	var newer server.MessagePage
	if code := e.getJSON("/api/v1/messages?limit=5&after="+url.QueryEscape(page.Newer), &newer); code != http.StatusOK {
		t.Fatalf("after: %d", code)
	}
	if got := texts(newer.Messages); !slices.Equal(got, []string{"STATION ONE DAY 18 PAGE 0", "STATION TWO DAY 19 PAGE 0", "STATION ONE DAY 19 PAGE 2", "STATION ONE DAY 19 PAGE 1", "STATION ONE DAY 19 PAGE 0"}) {
		t.Errorf("after the jump, newer: %q", got)
	}
	if newer.Newer == "" {
		t.Error("after: no cursor for newer still")
	}
	// Up to the newest: the last page of newer ones has no cursor.
	newer = server.MessagePage{}
	if code := e.getJSON("/api/v1/messages?limit=200&after="+url.QueryEscape(page.Newer), &newer); code != http.StatusOK || newer.Newer != "" || len(newer.Messages) != 17*4+40+5 {
		t.Errorf("all newer: %d, %d messages, newer %q", code, len(newer.Messages), newer.Newer)
	}
	if code := e.getJSON("/api/v1/messages?at=2026-01-01T00:00:00Z&before=x", nil); code != http.StatusBadRequest {
		t.Errorf("at with before: %d", code)
	}
}
