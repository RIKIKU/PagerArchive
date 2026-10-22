package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M9 at API level (ADR 0010): what the live stream does for the PowerShell
// module's watch, and what a role change does to open streams. A59 itself
// runs against real Authentik in internal/stacktest.

// event is one Server-Sent Event.
type event struct {
	Type string // "message" when the stream names none
	ID   string
	Data string
	At   time.Time
}

// sse opens /api/v1/stream as cookie or token and returns its events; the
// channel closes when the server ends the stream.
func (e *env) sse(query string, cookie *http.Cookie, token string) <-chan event {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	e.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/api/v1/stream?"+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	before := e.srv.Hub.Len()
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("stream: %v %v", err, resp)
	}
	out := make(chan event, 1000)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		ev := event{}
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Data != "" || ev.Type != "" {
					if ev.Type == "" {
						ev.Type = "message"
					}
					ev.At = time.Now()
					out <- ev
				}
				ev = event{}
			case strings.HasPrefix(line, "event: "):
				ev.Type = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	// Subscribed once the hub has one more connection (or the stream ended
	// straight away, which the caller sees as a closed channel).
	for deadline := time.Now().Add(5 * time.Second); e.srv.Hub.Len() == before && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	return out
}

// until reads events until one of type typ, failing after d.
func until(t *testing.T, ch <-chan event, typ string, d time.Duration) (event, []event) {
	t.Helper()
	var seen []event
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended before a %q event; saw %d events", typ, len(seen))
			}
			if ev.Type == typ {
				return ev, seen
			}
			seen = append(seen, ev)
		case <-timeout:
			t.Fatalf("no %q event within %v; saw %d events", typ, d, len(seen))
		}
	}
}

// ended waits for the server to end the stream.
func ended(t *testing.T, ch <-chan event, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatalf("stream still open after %v", d)
		}
	}
}

func reason(t *testing.T, ev event) string {
	t.Helper()
	var r struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &r); err != nil {
		t.Fatalf("reconnect event: %v", err)
	}
	return r.Reason
}

func (e *env) setRole(m *member, role string) {
	e.t.Helper()
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", m.id), map[string]string{"role": role}, nil); code >= 300 {
		e.t.Fatalf("set %s's role to %s: %d %s", m.email, role, code, p.Error)
	}
}

// A role change closes the user's live connections, browser and bearer,
// which then reconnect with the new role's view (ADR 0010 §4).
func TestM9RoleChangeClosesStreams(t *testing.T) {
	e := setup(t)
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)
	second := e.newMember("second@example.com", accounts.RoleAdmin, e.admin)
	e.mustPost("a", "0100001", "@@GARBLED 901", time.Now())

	browser := e.sse("", second.cookie, "")
	token := iss.accessToken(t, second.claims.Subject, nil)
	bearer := e.sse("", nil, token)
	other := e.sse("", e.user.cookie, "") // someone else's stays open

	start := time.Now()
	e.setRole(second, accounts.RoleUser)
	for name, ch := range map[string]<-chan event{"browser": browser, "bearer": bearer} {
		ev, _ := until(t, ch, "reconnect", 5*time.Second)
		if r := reason(t, ev); r != "account" {
			t.Errorf("%s: reconnect reason %q", name, r)
		}
		ended(t, ch, time.Second)
	}
	closed := time.Since(start)

	// Reconnected: the browser has the user's view; the module is refused.
	again := e.sse("", second.cookie, "")
	e.mustPost("a", "0100001", "@@GARBLED 902", time.Now())
	ev, _ := until(t, again, "message", 2*time.Second)
	var m server.Message
	if err := json.Unmarshal([]byte(ev.Data), &m); err != nil || m.Flags != nil {
		t.Errorf("demoted admin's live message still has flags: %+v %v", m.Flags, err)
	}
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/stream", token, nil); code != http.StatusForbidden || p.Code != server.CodeAdminsOnly {
		t.Errorf("demoted admin's module reconnects: %d %+v", code, p)
	}
	select {
	case ev, ok := <-other:
		if !ok || ev.Type == "reconnect" {
			t.Error("another user's stream was closed")
		}
	default:
	}

	// Promotion too: the stream reconnects and gets flags.
	e.setRole(second, accounts.RoleAdmin)
	if ev, _ := until(t, again, "reconnect", 5*time.Second); reason(t, ev) != "account" {
		t.Errorf("promotion: %q", ev.Data)
	}
	promoted := e.sse("", second.cookie, "")
	e.mustPost("a", "0100001", "@@GARBLED 903", time.Now())
	ev, _ = until(t, promoted, "message", 2*time.Second)
	if err := json.Unmarshal([]byte(ev.Data), &m); err != nil || m.Flags == nil || !m.Flags.ParseFailed {
		t.Errorf("promoted user's live message has no flags: %+v %v", m.Flags, err)
	}

	// Setting the role it already has closes nothing.
	e.setRole(second, accounts.RoleAdmin)
	select {
	case ev, ok := <-promoted:
		if !ok || ev.Type == "reconnect" {
			t.Error("an unchanged role closed the stream")
		}
	case <-time.After(300 * time.Millisecond):
	}
	t.Logf("ADR 0010 §4: demotion closed the browser's and the module's streams %v after the change; reconnected without flags; the module refused (powershell_admins_only)", closed.Round(time.Millisecond))
}

// A bearer stream ends with a reconnect event when its token expires, so it
// never runs on a token the API would refuse (ADR 0010 §4).
func TestM9BearerStreamEndsAtTokenExpiry(t *testing.T) {
	e := setup(t)
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)
	exp := time.Now().Add(3 * time.Second).Truncate(time.Second)
	newest := e.mustPost("a", "0100001", "QDBefore the watch", time.Now())
	token := iss.accessToken(t, e.admin.claims.Subject, func(c map[string]any) { c["exp"] = exp.Unix() })
	ch := e.sse("", nil, token)
	// Without after_id, the stream starts after the newest message.
	ready, _ := until(t, ch, "ready", time.Second)
	if want := fmt.Sprintf(`{"caught_up":0,"last_id":%d}`, newest.MessageID); ready.Data != want {
		t.Errorf("ready: %s, want %s", ready.Data, want)
	}
	ev, _ := until(t, ch, "reconnect", 6*time.Second)
	if r := reason(t, ev); r != "expired" {
		t.Errorf("reason %q, want expired", r)
	}
	if late := ev.At.Sub(exp); late < -100*time.Millisecond || late > time.Second {
		t.Errorf("ended %v from the token's expiry", late)
	}
	ended(t, ch, time.Second)
	// A browser session's stream isn't timed.
	browser := e.sse("", e.admin.cookie, "")
	select {
	case ev, ok := <-browser:
		if !ok || ev.Type == "reconnect" {
			t.Error("a browser stream ended")
		}
	case <-time.After(4 * time.Second):
	}
	t.Logf("ADR 0010 §4: the bearer stream sent reconnect (expired) %v after its token's expiry", ev.At.Sub(exp).Round(time.Millisecond))
}

// after_id: a catch-up event with how many follow, the matching messages
// stored since, oldest first, then ready with the count and the next
// after_id, then live; each exactly once while pages keep
// arriving during the catch-up, and IDs only increase (ADR 0010 §3).
func TestM9CatchUpExactlyOnce(t *testing.T) {
	e := setup(t)
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)
	token := iss.accessToken(t, e.admin.claims.Subject, nil)
	first := e.mustPost("a", "0100001", "QDCatch-up start", time.Now())

	// More than two catch-up batches, every other one matching the filter.
	var texts []string
	for i := range 450 {
		texts = append(texts, fmt.Sprintf("QDCatch-up stored %03d", i))
	}
	e.posts = append(e.posts, texts...)
	post := func(i int, text string) error {
		capcode := []string{"0100001", "0100002"}[i%2]
		body, _ := json.Marshal(server.PageRequest{ClientID: "a", Capcode: capcode, Text: text, ReceivedAt: time.Now(), Baud: 512})
		if code, pr := e.postRaw(e.keys["a"], body); code != http.StatusOK || pr.Outcome != "stored" {
			return fmt.Errorf("post %d: %d %s", i, code, pr.Outcome)
		}
		return nil
	}
	for i, text := range texts {
		if err := post(i, text); err != nil {
			t.Fatal(err)
		}
	}

	// Pages keep arriving from 8 clients at once, starting before the stream
	// opens and going on while it catches up, so some are stored around the
	// moment it subscribes (fewer than the 64 a connection may fall behind by
	// arrive after that).
	var during []string
	for i := range 60 {
		during = append(during, fmt.Sprintf("QDCatch-up during %03d", i))
	}
	e.posts = append(e.posts, during...)
	var wg sync.WaitGroup
	var posted atomic.Int32
	errs := make(chan error, len(during))
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < len(during); i += 8 {
				if err := post(i, during[i]); err != nil {
					errs <- err
				}
				posted.Add(1)
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	for posted.Load() < 10 {
		time.Sleep(time.Millisecond)
	}
	ch := e.sse(fmt.Sprintf("capcode=0100001&receptions=true&after_id=%d", first.MessageID), nil, token)
	// First how many follow (R63b: the module's "Reconnected" warning says
	// so before they come out), then exactly that many.
	catchUp, before := until(t, ch, "catch-up", 30*time.Second)
	if len(before) != 0 {
		t.Errorf("%d events before catch-up", len(before))
	}
	var announced struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal([]byte(catchUp.Data), &announced)
	ready, replayed := until(t, ch, "ready", 30*time.Second)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var count struct {
		Count  int   `json:"caught_up"`
		LastID int64 `json:"last_id"`
	}
	_ = json.Unmarshal([]byte(ready.Data), &count)
	if count.Count != len(replayed) || count.Count < 225 || announced.Count != count.Count {
		t.Errorf("catch-up said %d follow, ready says %d caught up, %d messages between", announced.Count, count.Count, len(replayed))
	}
	if last := replayed[len(replayed)-1].ID; count.LastID < mustAtoi(t, last) {
		t.Errorf("ready says last_id %d, but %s was sent", count.LastID, last)
	}

	// Every matching page comes out once, whether it was stored before the
	// stream or during the catch-up. A page's display text drops "QD".
	want := map[string]bool{}
	for _, list := range [][]string{texts, during} {
		for i, text := range list {
			want[strings.TrimPrefix(text, "QD")] = i%2 == 0 // 0100001
		}
	}
	wanted := 0
	for _, ok := range want {
		if ok {
			wanted++
		}
	}
	msgs := replayed
	for deadline := time.After(10 * time.Second); len(msgs) < wanted; {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended")
			}
			if ev.Type == "message" {
				msgs = append(msgs, ev)
			}
		case <-deadline:
			t.Fatalf("only %d of %d pages came out", len(msgs), wanted)
		}
	}
	got := map[string]int{}
	var last int64
	for _, ev := range msgs {
		var m server.Message
		if err := json.Unmarshal([]byte(ev.Data), &m); err != nil {
			t.Fatal(err)
		}
		if m.ID <= last {
			t.Errorf("message %d after %d: out of order or repeated", m.ID, last)
		}
		last = m.ID
		if m.Capcode != "0100001" {
			t.Errorf("message %d to %s doesn't match the filter", m.ID, m.Capcode)
		}
		if len(m.Receptions) != 1 || m.Receptions[0].Client != "Client A" {
			t.Errorf("message %d receptions: %+v", m.ID, m.Receptions)
		}
		got[m.DisplayText]++
	}
	for text, ok := range want {
		if ok && got[text] != 1 {
			t.Errorf("%q came out %d times", text, got[text])
		}
		if !ok && got[text] != 0 {
			t.Errorf("%q (another capcode) came out", text)
		}
	}
	// Nothing more is waiting.
	select {
	case ev, ok := <-ch:
		if ok && ev.Type == "message" {
			t.Errorf("an extra message: %s", ev.ID)
		}
	case <-time.After(300 * time.Millisecond):
	}
	duringIn := 0
	for _, ev := range replayed {
		if strings.Contains(ev.Data, "Catch-up during") {
			duringIn++
		}
	}
	t.Logf("ADR 0010 §3: the catch-up event said %d follow; caught up %d messages (ready: %d), then %d live; of the matching pages posted from 8 clients around the subscription, %d came in the catch-up and %d live; each matching page once, IDs increasing", announced.Count, len(replayed), count.Count, len(msgs)-len(replayed), duringIn, len(msgs)-len(replayed))

	// A bad after_id is refused.
	if code, _, _ := e.bearer(http.MethodGet, "/api/v1/stream?after_id=x", token, nil); code != http.StatusBadRequest {
		t.Errorf("after_id=x: %d", code)
	}
}

// SPEC.md ADR 0008 §5, M9 small item 3: an invitee whose join waits on a
// tree deactivation's locks, while the deactivation cancels the invitation,
// is told it was cancelled, and their new Authentik user is removed.
func TestM9JoinRacingTreeDeactivation(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	if code, p := a.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "j@example.com"}, nil); code != http.StatusCreated {
		t.Fatalf("A invites j: %d %s", code, p.Error)
	}
	id, _ := e.dir.invitationFor("j@example.com")
	claims, err := e.dir.enrol(id, "") // Authentik has enrolled j
	if err != nil {
		t.Fatal(err)
	}

	// What DeactivateTree does, held open: lock the tree's rows, cancel
	// their unused invitations.
	conn, err := pgx.Connect(e.ctx, os.Getenv("PAGERARCHIVE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(e.ctx)
	tx, err := conn.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, "SELECT id FROM users WHERE id = $1 FOR UPDATE", a.id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, "UPDATE invitations SET status = 'cancelled', cancelled_at = now() WHERE invited_by = $1 AND status = 'pending'", a.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.signIn(claims)
		done <- err
	}()
	// The join reads the invitation as still pending, then waits on A's row.
	for deadline := time.Now().Add(10 * time.Second); ; {
		var waiting int
		if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid()").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the join never waited on the tree's locks")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var refusal *accounts.Error
	if !errors.As(err, &refusal) || refusal.Code != "invitation_cancelled" || refusal.Msg != "Your invitation was cancelled before you finished enrolling." {
		t.Fatalf("join: %v", err)
	}
	e.dir.mu.Lock()
	_, left := e.dir.users[claims.Subject]
	e.dir.mu.Unlock()
	if left {
		t.Error("the Authentik user from the cancelled invitation is still there")
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM users WHERE email_key = 'j@example.com'").Scan(&n); err != nil || n != 0 {
		t.Errorf("an account was created for j: %d %v", n, err)
	}
	t.Logf("M9 item 3: the racing join is refused with %q and its Authentik user removed", refusal.Msg)
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("%q isn't a number", s)
	}
	return n
}
