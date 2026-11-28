package acceptance

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// channels is a list of areas as "zones:A=501 regions:North", each with its
// dispatch channel when it has one.
func channels(as []server.AreaRef) string {
	var out []string
	for _, a := range as {
		s := a.Layer + ":" + a.Name
		if a.DispatchChannel != nil {
			s += fmt.Sprintf("=%d", *a.DispatchChannel)
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// A160. Dispatch channels from areas (owner, 2026-11-28; R88, R92f, R9), at
// the API level: the example's zones have made-up dispatch channels (Zone A
// 501, Zone B 502) and its regions none; each area of a message, a job and
// the map's list carries its own, no payload names one; and an agency's
// field_names name its extra fields on a message. (The template, the
// numbers' reading and validate-profile's checks are unit checks in
// internal/profile; the chips are A161, in web/tests/e2e.spec.ts.)
func TestA160DispatchChannels(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()

	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, webhookTo("Station 1, Full", hook, "0100001"))
	west := e.mustPost("a", "0100001", "@@EX0000160 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON ALARM 2 #STN1", e.now.Add(-2*time.Minute)).MessageID // Zone A, Region North
	east := e.mustPost("a", "0100002", "@@EX0000160 HSF1 SMOKE FROM ROOF @ 5 EXAMPLE ST, MADEUPTON #STN2", e.now.Add(-time.Minute)).MessageID           // Zone B, Region North
	outside := e.mustPost("a", "0100001", "@@EX0000161 GRS3 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN1", e.now).MessageID                               // in no area
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) >= 2 }) {
		t.Fatal("no webhook requests within 5 s")
	}

	msg := func(id int64) server.Message {
		t.Helper()
		var m server.Message
		if code, _ := e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), nil, &m); code != http.StatusOK {
			t.Fatalf("message %d: %d", id, code)
		}
		return m
	}
	for id, want := range map[int64]string{west: "zones:A=501 regions:North", east: "zones:B=502 regions:North", outside: ""} {
		if got := channels(msg(id).Areas); got != want {
			t.Errorf("message %d's areas: %q, want %q", id, got, want)
		}
	}
	// The raw JSON: a number on an area with one, and no key on one without.
	if code, body := e.rawGet(fmt.Sprintf("/api/v1/messages/%d", west)); code != http.StatusOK || !strings.Contains(body, `{"layer":"zones","name":"A","dispatch_channel":501}`) || !strings.Contains(body, `{"layer":"regions","name":"North"}`) {
		t.Errorf("the message's JSON (%d): %s", code, body)
	}
	// The list, a job's areas and the map's list.
	var list server.MessagePage
	e.user.call(http.MethodGet, "/api/v1/messages?job=EX0000160", nil, &list)
	if len(list.Messages) != 2 || channels(list.Messages[0].Areas) != "zones:B=502 regions:North" || channels(list.Messages[1].Areas) != "zones:A=501 regions:North" {
		t.Errorf("EX0000160's list: %+v", list.Messages)
	}
	for job, want := range map[string]string{"EX0000160": "zones:A=501 zones:B=502 regions:North", "EX0000161": ""} {
		var ja server.JobAreas
		if code, _ := e.user.call(http.MethodGet, "/api/v1/jobs/"+job+"/areas", nil, &ja); code != http.StatusOK || channels(ja.Areas) != want {
			t.Errorf("%s's areas: %d %q, want %q", job, code, channels(ja.Areas), want)
		}
	}
	var mp server.MapPage
	e.user.call(http.MethodGet, "/api/v1/map", nil, &mp)
	for _, m := range mp.Messages {
		if m.ID == west && channels(m.Areas) != "zones:A=501 regions:North" {
			t.Errorf("the map's list: %q", channels(m.Areas))
		}
	}
	// No payload names one.
	for _, req := range hook.Requests() {
		if strings.Contains(string(req.Body), "dispatch") {
			t.Errorf("a payload names a dispatch channel: %s", req.Body)
		}
	}

	// R9: field names, from the agency profile as it is now.
	if m := msg(west); m.FieldNames != nil || m.Extra["alarm_level"] != "2" {
		t.Errorf("no field_names in the profile: %v, extra %v", m.FieldNames, m.Extra)
	}
	fire := e.srv.Ingest.Agencies["example-fire"]
	fire.FieldNames = map[string]string{"alarm_level": "Alarm", "not_there": "Nothing"}
	defer func() { fire.FieldNames = nil }()
	if m := msg(west); len(m.FieldNames) != 1 || m.FieldNames["alarm_level"] != "Alarm" {
		t.Errorf("field_names: %v, want alarm_level Alarm only", m.FieldNames)
	}
	if code, body := e.rawGet(fmt.Sprintf("/api/v1/messages/%d", east)); code != http.StatusOK || strings.Contains(body, "field_names") {
		t.Errorf("a message with no named extra field (%d): %s", code, body)
	}
	t.Logf("A160: areas %q, %q and none; job EX0000160 %q; field_names %v", channels(msg(west).Areas), channels(msg(east).Areas), "zones:A=501 zones:B=502 regions:North", msg(west).FieldNames)
}
