package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M11 at API level (ADR 0012): GET /api/v1/messages/by-id, which the print
// sheet (R67) shows its messages with. The sheet itself, its storage and the
// printout are checked in web/tests/e2e.spec.ts (A60).

// byID asks for ids as m and returns the status, the decoded reply and the
// reply's raw bytes.
func (m *member) byID(ids ...string) (int, server.Messages, []byte) {
	m.e.t.Helper()
	q := ""
	for i, id := range ids {
		if i > 0 {
			q += "&"
		}
		q += "id=" + id
	}
	resp, err := m.do(http.MethodGet, m.e.url+"/api/v1/messages/by-id?"+q, nil)
	if err != nil {
		m.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out server.Messages
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			m.e.t.Fatal(err)
		}
	}
	return resp.StatusCode, out, raw
}

func (m *member) list(query string) []server.Message {
	m.e.t.Helper()
	var page server.MessagePage
	if code, p := m.call(http.MethodGet, "/api/v1/messages?"+query, nil, &page); code != http.StatusOK {
		m.e.t.Fatalf("GET messages?%s: %d %s", query, code, p.Error)
	}
	return page.Messages
}

func idStrings(ns ...int64) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = fmt.Sprint(n)
	}
	return out
}

// The print sheet's messages: in the order asked, exactly as the message
// list gives them to each person, redaction included (P1), flags for admins
// only (R35c), and gone messages reported as missing.
func TestM11MessagesByID(t *testing.T) {
	e := setup(t)
	// A7's page: the phone number is redacted before it's stored.
	redacted := e.mustPost("a", "0100001", "QDCheck the shed Saturday. Text 0412 345 678 if available. EX0000001", e.now.Add(-3*time.Minute)).MessageID
	garbled := e.mustPost("a", "0100002", "@@GARBLED 123", e.now.Add(-2*time.Minute)).MessageID
	fire := e.mustPost("a", "0100003", "@@EX0000005 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN3", e.now.Add(-time.Minute)).MessageID
	if redacted == 0 || garbled == 0 || fire == 0 {
		t.Fatalf("posted messages %d, %d, %d", redacted, garbled, fire)
	}

	for _, who := range []*member{e.user, e.admin} {
		// Asked for out of time order: the reply keeps the order asked.
		code, got, raw := who.byID(idStrings(garbled, fire, redacted)...)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", who.email, code, raw)
		}
		var order []int64
		for _, m := range got.Messages {
			order = append(order, m.ID)
		}
		if !reflect.DeepEqual(order, []int64{garbled, fire, redacted}) || len(got.Missing) != 0 {
			t.Fatalf("%s: order %v, missing %v; want [%d %d %d], none", who.email, order, got.Missing, garbled, fire, redacted)
		}
		// Each is what the message list gives the same person.
		listed := map[int64]server.Message{}
		for _, m := range who.list("") {
			listed[m.ID] = m
		}
		for _, m := range got.Messages {
			if !reflect.DeepEqual(m, listed[m.ID]) {
				t.Errorf("%s: message %d differs from the list's:\n by-id %+v\n list  %+v", who.email, m.ID, m, listed[m.ID])
			}
		}
		if text := got.Messages[2].DisplayText; text != "Check the shed Saturday. Text REDACTED if available. EX0000001" {
			t.Errorf("%s: redacted message's text %q", who.email, text)
		}
		for _, number := range []string{"0412 345 678", "0412345678"} {
			if strings.Contains(string(raw), number) {
				t.Errorf("%s: the reply holds %q", who.email, number)
			}
		}
		// Flags are admin-only details (R35c).
		admin := who == e.admin
		if hasFlags := strings.Contains(string(raw), `"flags"`); hasFlags != admin {
			t.Errorf("%s: flags in the reply %v, want %v: %s", who.email, hasFlags, admin, raw)
		}
		if admin && (got.Messages[0].Flags == nil || !got.Messages[0].Flags.ParseFailed) {
			t.Errorf("admin: the garbled page's flags %+v, want parse_failed", got.Messages[0].Flags)
		}
		if strings.Contains(string(raw), `"receptions"`) {
			t.Errorf("%s: receive times in the reply", who.email)
		}
		t.Logf("M11: %s got %v in the order asked, as the list shows them, flags %v", who.email, order, admin)
	}

	// A page to an ignored alias is hidden from the main list but shown in
	// the alias view (R35e), so it can be on a sheet.
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE aliases SET ignored = true WHERE name = 'Station 4'"); err != nil {
		t.Fatal(err)
	}
	ignored := e.mustPost("a", "0100004", "QDA page to an ignored alias", e.now).MessageID
	if code, got, raw := e.user.byID(idStrings(ignored)...); code != http.StatusOK || len(got.Messages) != 1 || !got.Messages[0].Ignored {
		t.Errorf("ignored alias's page: %d %s", code, raw)
	}

	// Gone (retention, R45, or anything else): reported as missing, the rest
	// still in order. Duplicates count once.
	if _, err := e.st.Pool.Exec(e.ctx, "DELETE FROM messages WHERE id = $1", fire); err != nil {
		t.Fatal(err)
	}
	code, got, raw := e.user.byID(idStrings(redacted, fire, garbled, redacted, 999999999)...)
	if code != http.StatusOK || len(got.Messages) != 2 || got.Messages[0].ID != redacted || got.Messages[1].ID != garbled ||
		!reflect.DeepEqual(got.Missing, []int64{fire, 999999999}) {
		t.Fatalf("after deleting %d: %d %s", fire, code, raw)
	}
	t.Logf("M11: deleted message %d and unknown 999999999 reported missing: %v", fire, got.Missing)

	// The limit, and bad IDs.
	many := make([]int64, server.MaxSheet+1)
	for i := range many {
		many[i] = int64(i + 1)
	}
	if code, _, _ := e.user.byID(idStrings(many[:server.MaxSheet]...)...); code != http.StatusOK {
		t.Errorf("%d IDs: %d, want 200", server.MaxSheet, code)
	}
	for name, q := range map[string][]string{
		fmt.Sprintf("%d IDs", server.MaxSheet+1): idStrings(many...),
		"none":                                   nil,
		"not a number":                           {"abc"},
		"zero":                                   {"0"},
		"negative":                               {"-5"},
	} {
		if code, _, raw := e.user.byID(q...); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, raw)
		}
	}

	// Signed out: refused like every other API path (A29).
	var nobody *member
	resp, err := nobody.do(http.MethodGet, e.url+"/api/v1/messages/by-id?id=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", resp.StatusCode)
	}
}
