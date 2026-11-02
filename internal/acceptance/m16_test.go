package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/importer"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M16 at API level (ADR 0018): what the import does with each kind of line,
// and what it leaves alone. A76 itself runs through the command in
// internal/stacktest.

// importLine is one page in an import file, as JSON.
func importLine(at time.Time, capcode, text, sourceID string) string {
	rec := map[string]string{"received_at": at.Format(time.RFC3339Nano), "capcode": capcode, "text": text}
	if sourceID != "" {
		rec["source_id"] = sourceID
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

type importRun struct {
	importer.Report
	out, progress string
	err           error
}

// importer builds an import as the command does, with the live path's service,
// whose OnStored queues deliveries: the importer has to turn that off.
func (e *env) importer(dry bool, out, progress *bytes.Buffer) *importer.Importer {
	return &importer.Importer{
		Store: e.st, Ingest: e.srv.Ingest, Network: e.srv.Ingest.Network,
		Cutoff: e.job.Cutoff(time.Now(), 3650), Now: time.Now,
		DryRun: dry, Report: out, Progress: progress,
	}
}

func (e *env) runImport(file string, change func(*importer.Importer)) importRun {
	e.t.Helper()
	var out, progress bytes.Buffer
	im := e.importer(false, &out, &progress)
	if change != nil {
		change(im)
	}
	rep, err := im.Run(context.Background(), strings.NewReader(file))
	rep.Write(&out)
	return importRun{Report: rep, out: out.String(), progress: progress.String(), err: err}
}

func (e *env) mustImport(lines ...string) importRun {
	e.t.Helper()
	r := e.runImport(strings.Join(lines, "\n")+"\n", nil)
	if r.err != nil {
		e.t.Fatalf("import: %v", r.err)
	}
	return r
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// Every line a file can get wrong is refused by its number, for a reason that
// holds nothing from the line, and the others are stored (R83e, P1). Bad JSON
// is the trap: Go's decoder quotes what it choked on.
func TestM16RefusedLines(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	mark := "SECRETWORD"
	lines := []struct{ line, want string }{
		{`SECRETWORD not json at all`, "not valid JSON"},
		{`{"received_at": "` + at.Format(time.RFC3339) + `", "capcode": "0100001", "text": "SECRETWORD`, "not valid JSON"},
		{`{"received_at": "` + at.Format(time.RFC3339) + `", "capcode": "0100001", "text": "SECRETWORD"} trailing SECRETWORD`, "not valid JSON"},
		{`["SECRETWORD"]`, "not a JSON object"},
		{`{"received_at": "` + at.Format(time.RFC3339) + `", "capcode": "0100001", "text": 5}`, "not a JSON object"},
		{`{"SECRETWORD": 1}`, "not a JSON object"},
		{`{"received_at": "SECRETWORD", "capcode": "0100001", "text": "x"}`, "received_at must be RFC 3339"},
		{`{"received_at": "2024-03-01T10:00:00", "capcode": "0100001", "text": "x"}`, "received_at must be RFC 3339"},
		{`{"capcode": "0100001", "text": "x"}`, "received_at must be RFC 3339"},
		{importLine(time.Now().Add(time.Hour), "0100001", "x", ""), "ahead of the server"},
		{importLine(at, "SECRETWORD", "x", ""), "capcode must be"},
		{importLine(at, "01000011", "x", ""), "capcode must be"},
		{importLine(at, "0100001", "", ""), "text must be"},
		{importLine(at, "0100001", strings.Repeat("x", 4097), ""), "text must be"},
		{importLine(at, "0100001", "SECRET\x00WORD", ""), "NUL"},
		{`{"received_at": "` + at.Format(time.RFC3339) + `", "capcode": "0100001", "text": "x", "source_id": ""}`, "source_id must be"},
		{importLine(at, "0100001", "x", strings.Repeat("x", 201)), "source_id must be"},
		{`{"received_at": "` + at.Format(time.RFC3339) + `", "capcode": "0100001", "text": "SECRETWORD ` + "\xff" + `"}`, "valid UTF-8"},
		{`{"text": "` + mark + strings.Repeat("x", 70_000) + `"}`, "longer than 64 KiB"},
		{``, "not valid JSON"},
	}
	var file strings.Builder
	var want []string
	for i, l := range lines {
		file.WriteString(l.line + "\n")
		want = append(want, fmt.Sprintf("line %d: ", i+1))
	}
	good := importLine(at, "0100001", "QDGOOD PAGE", "good")
	file.WriteString(good) // and no newline at the end
	r := e.runImport(file.String(), nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.Lines != int64(len(lines)+1) || r.Refused != int64(len(lines)) || r.Stored != 1 {
		t.Errorf("read %d, refused %d, stored %d, want %d, %d, 1", r.Lines, r.Refused, r.Stored, len(lines)+1, len(lines))
	}
	outLines := strings.Split(r.out, "\n")
	for i, l := range lines {
		if i >= len(outLines) || !strings.HasPrefix(outLines[i], want[i]) || !strings.Contains(outLines[i], l.want) {
			t.Errorf("line %d: reported %q, want %q and %q", i+1, outLines[i], want[i], l.want)
		}
	}
	for name, s := range map[string]string{"report": r.out, "progress": r.progress, "logs": e.logs.String()} {
		if strings.Contains(s, mark) {
			t.Errorf("the %s holds a refused line's text:\n%s", name, s)
		}
	}
	if got := e.list("q=GOOD"); len(got) != 1 {
		t.Errorf("the good line: %d found", len(got))
	}
}

// R83b: quiet. No delivery is queued, no live update is sent, also to a watch
// catching up after a dropped connection, and no client's last-seen time moves.
// "import" isn't a client. Then a client hears a page: all of that works.
func TestM16Quiet(t *testing.T) {
	e := setup(t)
	hook := standin.Webhook()
	defer hook.Close()
	e.allowLoopback()
	e.createIntegration(e.user, webhookTo("Everything", hook, "0100001", "0100002"))
	e.runWorker()
	// A page the watch has, and a client that has been seen.
	first := e.mustPost("a", "0100001", "QDBEFORE THE IMPORT", e.now.Add(-time.Minute))
	var seen time.Time
	read := func(id string) (t0 time.Time) {
		if err := e.st.Pool.QueryRow(e.ctx, `SELECT last_seen_at FROM clients WHERE id = $1`, id).Scan(&t0); err != nil {
			t.Fatal(err)
		}
		return t0
	}
	seen = read("a")
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 1 }) {
		t.Fatalf("the integration got %d deliveries of the live page", len(hook.Requests()))
	}
	open := e.stream("")
	// Recent pages, which R51a would send if a client had just heard them.
	e.mustImport(
		importLine(time.Now().Add(-time.Second), "0100001", "QDIMPORTED NOW", "i1"),
		importLine(e.now.Add(-time.Hour), "0100002", "QDIMPORTED EARLIER", "i2"),
		importLine(e.now.Add(-2*time.Hour), "0100001", "QDIMPORTED BEFORE THAT", ""),
	)
	if n := e.count(`SELECT count(*) FROM messages`); n != 4 {
		t.Fatalf("%d messages stored, want 4", n)
	}
	if n := e.count(`SELECT count(*) FROM deliveries`); n != 1 {
		t.Errorf("%d deliveries queued, want the live page's 1", n)
	}
	// The worker has had every chance to send them.
	time.Sleep(2 * time.Second)
	if n := len(hook.Requests()); n != 1 {
		t.Errorf("the integration got %d deliveries, want the live page's 1", n)
	}
	if got := read("a"); !got.Equal(seen) {
		t.Errorf("client a last seen %v, was %v", got, seen)
	}
	if n := e.count(`SELECT count(*) FROM clients WHERE last_seen_at IS NOT NULL`); n != 1 {
		t.Errorf("%d clients seen, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM clients WHERE id = 'import' OR name = 'import'`); n != 0 {
		t.Errorf("import is a client")
	}
	if n := e.count(`SELECT count(*) FROM receptions`); n != 1 {
		t.Errorf("%d receptions, want the live page's 1", n)
	}
	// A watch that reconnects after the import hears of no imported page.
	catchUp := e.sse(fmt.Sprintf("after_id=%d", first.MessageID), e.user.cookie, "")
	for ev := range catchUp {
		if ev.Type == "catch-up" && !strings.Contains(ev.Data, `"count":0`) {
			t.Errorf("the catch-up after the import: %s", ev.Data)
		}
		if ev.Type == "message" {
			t.Errorf("the catch-up sent a message: %s", ev.Data)
		}
		if ev.Type == "ready" {
			break
		}
	}
	// A live page after it: the open list and the integration hear of it, and
	// only of it.
	live := e.mustPost("a", "0100001", "QDAFTER THE IMPORT", e.now)
	select {
	case m := <-open:
		if m.ID != live.MessageID {
			t.Errorf("the open list got message %d (%q), want the live page %d", m.ID, m.Text, live.MessageID)
		}
	case <-time.After(5 * time.Second):
		t.Error("the open list didn't get the live page")
	}
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 2 }) {
		t.Errorf("the integration got %d deliveries, want 2", len(hook.Requests()))
	}
	// Metrics don't name it.
	req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+metricsToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metrics, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(metrics), `client="a"`) || strings.Contains(string(metrics), `client="import"`) || strings.Contains(string(metrics), `client=""`) {
		t.Errorf("metrics: %d, and they should name client a and not import: %s", resp.StatusCode, metrics)
	}
}

// R83b: a page's receive times say "import", to admins and users alike, and
// users still see receivers numbered from 1.
func TestM16ReceivedByImport(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	e.mustImport(importLine(at, "0100001", "QDHEARD", "r1"), importLine(at.Add(3*time.Second), "0100001", "QDHEARD", "r2"))
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT id FROM messages`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// A client hears the same page, within R19's 10 seconds.
	if got := e.mustPost("a", "0100001", "QDHEARD", at.Add(5*time.Second)); got.Outcome != "duplicate" || got.MessageID != id {
		t.Fatalf("the live copy: %+v", got)
	}
	want := map[*member][]string{e.admin: {"import", "import", "Client A"}, e.user: {"import", "import", "Receiver 1"}}
	for m, names := range want {
		var msg server.Message
		if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), nil, &msg); code != http.StatusOK {
			t.Fatalf("detail: %d %s", code, p.Error)
		}
		var got []string
		for _, r := range msg.Receptions {
			got = append(got, r.Client)
		}
		if !slices.Equal(got, names) {
			t.Errorf("receptions %v, want %v", got, names)
		}
		var page server.MessagePage
		if code, p := m.call(http.MethodGet, "/api/v1/messages?receptions=true", nil, &page); code != http.StatusOK || len(page.Messages) != 1 {
			t.Fatalf("list: %d %s", code, p.Error)
		}
		got = got[:0]
		for _, r := range page.Messages[0].Receptions {
			got = append(got, r.Client)
		}
		if !slices.Equal(got, names) {
			t.Errorf("list's receptions %v, want %v", got, names)
		}
	}
}

// R83a: the same path as a live page: redaction, cleaning, parsing, location,
// flags, with nothing redacted anywhere. A page that fails to parse is stored
// in full (P2). The same text through the API stores the same message.
func TestM16SamePathAsALivePage(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	texts := []struct{ capcode, text string }{
		{"0100001", "@@EX0000051 HSF1 SMOKE CALL 0412 345 678 AGE: 45 SEX: M @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS P1 P2 #STN1"},
		{"100002", "Hb<NUL>RE EX0000051 UPDATE<LF>MORE #STN2"},
		{"0100003", "QDANYTHING FREE"},
		{"0100004", "!!NO KNOWN MARKER"}, // parse_failed: no format for no priority
		{"0999999", "@@EX0000099 HSF1 NO ALIAS"},
		{"0000050", "QDHEARTBEAT"},
		{"0000041", "QDIGNORED CAPCODE"},
	}
	var lines []string
	for i, p := range texts {
		lines = append(lines, importLine(at.Add(time.Duration(i)*time.Minute), p.capcode, p.text, fmt.Sprintf("s%d", i)))
	}
	r := e.mustImport(lines...)
	if r.Stored != 6 || r.Ignored != 1 || r.ParseFailed != 1 {
		t.Errorf("stored %d, ignored %d, parse failures %d: want 6, 1, 1", r.Stored, r.Ignored, r.ParseFailed)
	}
	const columns = `capcode, text, priority, heartbeat, format, job_number, event_type, description, address, locality, grid_ref,
		paging_group, units::text, extra::text, parse_failed, address_unsplit, alias_id, ST_AsText(location::geometry), location_source, location_mismatch, sent_at`
	imported := e.rows(`SELECT ` + columns + ` FROM messages ORDER BY sent_at`)
	if strings.Contains(fmt.Sprint(imported), "0412") || strings.Contains(fmt.Sprint(imported), "AGE:") || strings.Contains(fmt.Sprint(imported), "SEX:") {
		t.Errorf("a redacted detail was stored: %v", imported)
	}
	if !strings.Contains(fmt.Sprint(imported), "CALL REDACTED REDACTED REDACTED") {
		t.Errorf("the redacted text isn't there: %v", imported)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE function IS NOT NULL OR baud IS NOT NULL`); n != 0 {
		t.Errorf("%d imported pages have a function code or baud rate, which a file doesn't say", n)
	}
	// The same pages from a client, in place of the import.
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	for i, p := range texts {
		e.mustPost("a", p.capcode, p.text, at.Add(time.Duration(i)*time.Minute))
	}
	live := e.rows(`SELECT ` + columns + ` FROM messages ORDER BY sent_at`)
	if !slices.Equal(imported, live) {
		t.Errorf("imported and live differ:\nimported %v\nlive     %v", imported, live)
	}
}

// rows returns each row of a query as one string.
func (e *env) rows(sql string, args ...any) []string {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, sql, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, fmt.Sprint(vals...))
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// The stored result of an import, without IDs.
func (e *env) importedResult() []string {
	e.t.Helper()
	return e.rows(`SELECT m.capcode, m.text, m.sent_at, m.job_number, m.parse_failed, m.dedup_key,
		(SELECT string_agg(i.received_at::text || ' ' || COALESCE(i.source_id, '-'), ',' ORDER BY i.received_at, i.source_id)
		 FROM import_receptions i WHERE i.message_id = m.id)
		FROM messages m ORDER BY m.sent_at, m.capcode, m.text`)
}

// The result doesn't depend on the order of the file (ADR 0018): R19's rule is
// measured from the first copy in time, whichever line says so first, and a
// third copy 12 seconds after the first is a new message.
func TestM16OrderOfTheFile(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-24 * time.Hour)
	var lines []string
	for i := range 300 {
		lines = append(lines, importLine(at.Add(time.Duration(i)*time.Minute), fmt.Sprintf("010000%d", 1+i%4), fmt.Sprintf("QDPAGE %d", i), fmt.Sprintf("p%d", i)))
	}
	// A chain: 0 and 8 seconds are one message, 12 is after the first copy's 10.
	day := at.Add(-24 * time.Hour)
	lines = append(lines,
		importLine(day, "0100001", "QDCHAIN", "c1"),
		importLine(day.Add(8*time.Second), "0100001", "QDCHAIN", "c2"),
		importLine(day.Add(12*time.Second), "0100001", "QDCHAIN", "c3"),
		// Same time, same page, no source ID: one copy.
		importLine(day.Add(time.Hour), "0100002", "QDNO ID", ""),
		importLine(day.Add(time.Hour), "0100002", "QDNO ID", ""),
		// Same time, different pages: the same order every time.
		importLine(day.Add(2*time.Hour), "0100003", "QDTIE B", "t2"),
		importLine(day.Add(2*time.Hour), "0100003", "QDTIE A", "t1"),
	)
	r := e.mustImport(lines...)
	if r.Stored != 300+2+1+2 || r.Duplicates != 1 || r.AlreadyImported != 1 {
		t.Errorf("stored %d, merged %d, already imported %d: want 305, 1, 1", r.Stored, r.Duplicates, r.AlreadyImported)
	}
	chain := e.rows(`SELECT sent_at FROM messages WHERE text = 'QDCHAIN' ORDER BY sent_at`)
	if len(chain) != 2 {
		t.Errorf("the chain is %d messages, want 2", len(chain))
	}
	want := e.importedResult()
	wantIDs := e.rows(`SELECT text FROM messages WHERE sent_at = $1 ORDER BY id`, day.Add(2*time.Hour))
	if !slices.Equal(wantIDs, []string{"QDTIE A", "QDTIE B"}) {
		t.Errorf("same-time pages stored in the order %v", wantIDs)
	}
	for seed := range uint64(3) {
		if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages`); err != nil {
			t.Fatal(err)
		}
		shuffled := slices.Clone(lines)
		rand.New(rand.NewPCG(seed, 76)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		e.mustImport(shuffled...)
		if got := e.importedResult(); !slices.Equal(got, want) {
			t.Fatalf("shuffled %d: a different result\n got %v\nwant %v", seed, got, want)
		}
		if got := e.rows(`SELECT text FROM messages WHERE sent_at = $1 ORDER BY id`, day.Add(2*time.Hour)); !slices.Equal(got, wantIDs) {
			t.Errorf("shuffled %d: same-time pages stored in the order %v", seed, got)
		}
	}
}

// R83a, R19: merged with pages already stored, within 10 seconds either side,
// by the earliest receive time (R21); later ones are new.
func TestM16DeduplicatesAgainstStoredPages(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	live := e.mustPost("a", "0100001", "QDLIVE PAGE", at)
	r := e.mustImport(
		importLine(at.Add(-3*time.Second), "0100001", "QDLIVE PAGE", "before"),
		importLine(at.Add(5*time.Second), "0100001", "QDLIVE PAGE", "after"),
		importLine(at.Add(30*time.Second), "0100001", "QDLIVE PAGE", "later"),
		importLine(at, "0100002", "QDLIVE PAGE", "other-capcode"),
	)
	if r.Stored != 2 || r.Duplicates != 2 {
		t.Errorf("stored %d, merged %d: want 2, 2", r.Stored, r.Duplicates)
	}
	var sent time.Time
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT sent_at FROM messages WHERE id = $1`, live.MessageID).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Equal(at.Add(-3 * time.Second)) {
		t.Errorf("sent at %v, want the earliest copy, %v", sent, at.Add(-3*time.Second))
	}
	if n := e.count(`SELECT count(*) FROM import_receptions WHERE message_id = $1`, live.MessageID); n != 2 {
		t.Errorf("%d import copies on the live page, want 2", n)
	}
}

// R83c: safe to repeat, by source_id when it's given: a corrected copy of a
// page isn't stored again, even with another time and text. Otherwise by dedup
// key and receive time.
func TestM16SafeToRepeat(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-24 * time.Hour)
	file := []string{
		importLine(at, "0100001", "QDWITH ID", "a"),
		importLine(at.Add(3*time.Second), "0100001", "QDWITH ID", "b"),
		importLine(at.Add(time.Minute), "0100002", "QDNO ID", ""),
		importLine(at.Add(time.Minute+3*time.Second), "0100002", "QDNO ID", ""),
		// A copy after the third's window, tied with nothing: its own message.
		importLine(at.Add(2*time.Minute), "0100002", "QDNO ID", ""),
	}
	first := e.mustImport(file...)
	if first.Stored != 3 || first.Duplicates != 2 {
		t.Errorf("first run: stored %d, merged %d: want 3, 2", first.Stored, first.Duplicates)
	}
	want := e.importedResult()
	second := e.mustImport(file...)
	if second.Stored != 0 || second.Duplicates != 0 || second.AlreadyImported != 5 {
		t.Errorf("second run: stored %d, merged %d, already %d: want 0, 0, 5", second.Stored, second.Duplicates, second.AlreadyImported)
	}
	if got := e.importedResult(); !slices.Equal(got, want) {
		t.Errorf("the second run changed the result:\n got %v\nwant %v", got, want)
	}
	// source_id decides: another time and another text, the same ID.
	third := e.mustImport(importLine(at.Add(5*time.Hour), "0100003", "QDCORRECTED", "a"))
	if third.Stored != 0 || third.AlreadyImported != 1 {
		t.Errorf("a known source_id with new content: stored %d, already %d: want 0, 1", third.Stored, third.AlreadyImported)
	}
	// Repeated in one file, in time order or not.
	fourth := e.mustImport(importLine(at.Add(6*time.Hour), "0100003", "QDTWICE ONE", "twice"), importLine(at.Add(7*time.Hour), "0100003", "QDTWICE TWO", "twice"))
	if fourth.Stored != 1 || fourth.AlreadyImported != 1 {
		t.Errorf("a source_id twice in a file: stored %d, already %d: want 1, 1", fourth.Stored, fourth.AlreadyImported)
	}
}

// R83c: a run that stops part-way, then the same file: the same result as one
// full run, across batches.
func TestM16StoppedPartWay(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-48 * time.Hour)
	var lines []string
	// Four batches, with duplicates and pages with no ID across the boundaries.
	for i := range importer.BatchSize*4 + 7 {
		id := fmt.Sprintf("k%d", i)
		if i%10 == 0 {
			id = ""
		}
		lines = append(lines, importLine(at.Add(time.Duration(i)*time.Second), "0100001", fmt.Sprintf("QDPAGE %d", i/2), id))
	}
	file := strings.Join(lines, "\n") + "\n"
	full := e.runImport(file, nil)
	if full.err != nil {
		t.Fatal(full.err)
	}
	want := e.importedResult()
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("killed")
	r := e.runImport(file, func(im *importer.Importer) {
		im.AfterBatch = func(done int64) error {
			if done >= importer.BatchSize*2 {
				return stop
			}
			return nil
		}
	})
	if !errors.Is(r.err, stop) {
		t.Fatalf("the stopped run: %v", r.err)
	}
	if n := e.count(`SELECT count(*) FROM import_receptions`); n != importer.BatchSize*2 {
		t.Errorf("%d copies stored by the stopped run, want %d", n, importer.BatchSize*2)
	}
	again := e.runImport(file, nil)
	if again.err != nil {
		t.Fatal(again.err)
	}
	if got := e.importedResult(); !slices.Equal(got, want) {
		t.Error("stopped and run again gives a different result from one full run")
	}
	if again.Stored+r.Stored != full.Stored || again.Duplicates+r.Duplicates != full.Duplicates {
		t.Errorf("stored %d+%d, merged %d+%d; one full run: %d, %d", r.Stored, again.Stored, r.Duplicates, again.Duplicates, full.Stored, full.Duplicates)
	}
}

// R83e: a dry run stores nothing and says what a real run does. That holds
// within the file, against stored pages, and across batches.
func TestM16DryRun(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-48 * time.Hour)
	e.mustPost("a", "0100001", "QDSTORED ALREADY", at)
	e.mustImport(importLine(at.Add(time.Minute), "0100002", "QDIMPORTED ALREADY", "old"))
	lines := []string{
		importLine(at.Add(3*time.Second), "0100001", "QDSTORED ALREADY", "x1"),
		importLine(at.Add(time.Minute), "0100002", "QDIMPORTED ALREADY", "old"),
		importLine(at.Add(time.Minute), "0100002", "QDANOTHER", ""),
		importLine(at.Add(time.Minute+time.Second), "0100002", "QDANOTHER", ""),
		importLine(at.Add(2*time.Minute), "0100003", "QDSAME ID", "same"),
		importLine(at.Add(3*time.Minute), "0100003", "QDSAME ID LATER", "same"),
		importLine(e.now.AddDate(-11, 0, 0), "0100003", "QDTOO OLD", "tooold"),
		importLine(at, "0000041", "QDIGNORED", "ignored"),
		importLine(at, "0100004", "!!NO MARKER", "noparse"),
		importLine(at.Add(5*time.Minute), "0100002", "QDIDENTICAL", ""),
		importLine(at.Add(5*time.Minute), "0100002", "QDIDENTICAL", ""),
		"not json",
	}
	// Over two batches, with a copy on each side of the boundary.
	for i := range importer.BatchSize + 3 {
		lines = append(lines, importLine(at.Add(time.Hour+time.Duration(i)*2*time.Second), "0100004", fmt.Sprintf("QDMANY %d", i/2), fmt.Sprintf("m%d", i)))
	}
	file := strings.Join(lines, "\n") + "\n"
	before := e.rows(`SELECT id, text FROM messages ORDER BY id`)
	var out, progress bytes.Buffer
	im := e.importer(true, &out, &progress)
	dry, err := im.Run(context.Background(), strings.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.rows(`SELECT id, text FROM messages ORDER BY id`); !slices.Equal(got, before) {
		t.Errorf("the dry run stored something: %v", got)
	}
	if n := e.count(`SELECT count(*) FROM import_receptions`); n != 1 {
		t.Errorf("%d import copies after the dry run, want 1", n)
	}
	var report bytes.Buffer
	dry.Write(&report)
	if !strings.Contains(report.String(), "dry run: nothing was stored") {
		t.Errorf("the report doesn't say it's a dry run:\n%s", report.String())
	}
	real := e.mustImport(strings.TrimSuffix(file, "\n"))
	dry.DryRun, real.Report.DryRun = false, false
	if dry != real.Report {
		t.Errorf("the dry run reported\n%+v\nthe real run\n%+v", dry, real.Report)
	}
	if real.Lines != int64(len(lines)) || real.Refused != 1 || real.TooOld != 1 || real.Ignored != 1 || real.ParseFailed != 1 {
		t.Errorf("the real run: %+v", real.Report)
	}
}

// R83d: pages older than the retention period, by the calendar date the nightly
// job uses, are skipped and counted.
func TestM16Retention(t *testing.T) {
	e := setup(t)
	if err := e.job.SetDays(e.ctx, 30); err != nil {
		t.Fatal(err)
	}
	cutoff := e.job.Cutoff(time.Now(), 30)
	r := e.runImport(strings.Join([]string{
		importLine(cutoff.Add(-time.Second), "0100001", "QDTOO OLD", "o"),
		importLine(cutoff, "0100001", "QDJUST KEPT", "k"),
	}, "\n")+"\n", func(im *importer.Importer) { im.Cutoff = cutoff })
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.TooOld != 1 || r.Stored != 1 {
		t.Errorf("too old %d, stored %d: want 1, 1", r.TooOld, r.Stored)
	}
	if got := e.list("q=JUST"); len(got) != 1 {
		t.Errorf("the page on the cut-off date: %d found", len(got))
	}
	if _, err := e.job.RunOnce(e.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := e.list("q=JUST"); len(got) != 1 {
		t.Errorf("the nightly run deleted a page the import kept: %d found", len(got))
	}
	// The nightly run's retention takes the import's copies with the message.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET sent_at = $1`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job.RunOnce(e.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM import_receptions`); n != 0 {
		t.Errorf("%d import copies left after retention deleted the message", n)
	}
}

// P1: a source ID a redaction rule matches isn't kept. The page is, known by
// its dedup key and time, and a second run still stores nothing.
func TestM16SourceIDThatARuleMatches(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	file := importLine(at, "0100001", "QDPAGE", "0412 345 678")
	first := e.mustImport(file)
	if first.Stored != 1 || first.SourceIDsGone != 1 {
		t.Errorf("stored %d, source IDs not kept %d: want 1, 1", first.Stored, first.SourceIDsGone)
	}
	if n := e.count(`SELECT count(*) FROM import_receptions WHERE source_id IS NOT NULL`); n != 0 {
		t.Errorf("%d source IDs kept", n)
	}
	if second := e.mustImport(file); second.Stored != 0 || second.AlreadyImported != 1 {
		t.Errorf("second run: stored %d, already %d: want 0, 1", second.Stored, second.AlreadyImported)
	}
}

// P1: when the rules change, the nightly run reaches the stored source IDs too.
func TestM16NightlyRedactionReachesSourceIDs(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	phone := "0412 345 678"
	e.mustImport(
		importLine(at, "0100001", "QDFIRST", "keep"),
		importLine(at.Add(time.Minute), "0100001", "QDSECOND", "was-fine"),
		importLine(at.Add(time.Minute), "0100001", "QDSECOND", "also-fine"),
	)
	// As if stored before a rule matching this number existed. Two of them, on
	// the same copy, so that the two become one.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE import_receptions SET source_id = 'CALL ' || $1::text || ' ' || source_id WHERE source_id <> 'keep'`, phone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job.RunOnce(e.ctx, e.now); err != nil {
		t.Fatal(err)
	}
	if got := e.rows(`SELECT COALESCE(source_id, '-') FROM import_receptions ORDER BY 1`); !slices.Equal(got, []string{"-", "keep"}) {
		t.Errorf("source IDs after the nightly run: %v, want no ID and keep", got)
	}
	// Known by its dedup key and time: the same line stores nothing new.
	if r := e.mustImport(importLine(at.Add(time.Minute), "0100001", "QDSECOND", phone)); r.Stored != 0 || r.Duplicates != 0 || r.AlreadyImported != 1 {
		t.Errorf("the line again: stored %d, merged %d, already %d: want 0, 0, 1", r.Stored, r.Duplicates, r.AlreadyImported)
	}
}

// An empty file is an empty report.
func TestM16EmptyFile(t *testing.T) {
	e := setup(t)
	r := e.runImport("", nil)
	if r.err != nil || r.Lines != 0 || r.Stored != 0 {
		t.Errorf("lines %d, stored %d, err %v", r.Lines, r.Stored, r.err)
	}
}

// The first line can start with a byte order mark, and lines can end with CRLF,
// as a file made on Windows does. Capcodes are padded to 7 digits (R12).
func TestM16FileFormat(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour)
	bom := string(rune(0xFEFF))
	r := e.runImport(bom+importLine(at, "100001", "QDONE", "1")+"\r\n"+importLine(at.Add(time.Minute), "0100001", "QDTWO", "2")+"\r\n", nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.Stored != 2 || r.Refused != 0 {
		t.Fatalf("stored %d, refused %d: want 2, 0\n%s", r.Stored, r.Refused, r.out)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE capcode = '0100001'`); n != 2 {
		t.Errorf("%d pages on 0100001, want 2", n)
	}
	// A time with its offset is the same instant as in UTC, to the microsecond.
	offset := at.In(time.FixedZone("", 10*3600)).Format(time.RFC3339)
	if second := e.mustImport(`{"received_at":"` + offset + `","capcode":"0100002","text":"QDOFFSET"}`); second.Stored != 1 {
		t.Errorf("a time with an offset: stored %d", second.Stored)
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE sent_at = $1 AND text = 'QDOFFSET'`, at); n != 1 {
		t.Errorf("the time with an offset wasn't kept as the same instant")
	}
}

// One import at a time, and it leaves no lock behind.
func TestM16OneImportAtATime(t *testing.T) {
	e := setup(t)
	line := importLine(e.now.Add(-time.Hour), "0100001", "QDPAGE", "1")
	var inner importRun
	outer := e.runImport(line+"\n", func(im *importer.Importer) {
		im.AfterBatch = func(int64) error {
			inner = e.runImport(line+"\n", nil)
			return nil
		}
	})
	if outer.err != nil {
		t.Fatal(outer.err)
	}
	if inner.err == nil || !strings.Contains(inner.err.Error(), "another import is running") {
		t.Errorf("a second import while one runs: %v", inner.err)
	}
	if again := e.mustImport(line); again.AlreadyImported != 1 {
		t.Errorf("the next import: already %d, want 1", again.AlreadyImported)
	}
}
