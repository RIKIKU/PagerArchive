package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
)

// When the server says the receive time is ahead of its clock, the client
// warns without page text, drops the page and carries on (ADR 0002).
func TestSenderWarnsWhenClockAhead(t *testing.T) {
	var mu sync.Mutex
	var accepted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Page
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.Text == "QDsecret future page" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"received_at is more than 60 s ahead of the server's clock","code":"clock_ahead"}`))
			return
		}
		mu.Lock()
		accepted = append(accepted, p.Text)
		mu.Unlock()
	}))
	defer srv.Close()

	var logs bytes.Buffer
	b := NewBuffer(10)
	b.Push(Page{Capcode: "0000001", Text: "QDsecret future page"})
	b.Push(Page{Capcode: "0000001", Text: "QDnext"})
	s := &Sender{URL: srv.URL, Key: "k", HTTP: srv.Client(), Buffer: b, Log: slog.New(slog.NewTextHandler(&logs, nil)), MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for b.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	out := logs.String()
	t.Logf("client log: %s", out)
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "clock is more than 60 s ahead") {
		t.Error("no clock-ahead warning")
	}
	if strings.Contains(out, "secret") {
		t.Error("warning contains page text")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(accepted) != 1 || accepted[0] != "QDnext" {
		t.Errorf("accepted %v", accepted)
	}
}

func TestBufferDropsOldest(t *testing.T) {
	b := NewBuffer(2)
	b.Push(Page{Text: "1"})
	b.Push(Page{Text: "2"})
	if !b.Push(Page{Text: "3"}) {
		t.Error("expected a drop")
	}
	if p, _ := b.Peek(); p.Text != "2" || b.Len() != 2 {
		t.Errorf("peek %q len %d", p.Text, b.Len())
	}
}

func TestReaderDecodesAndKeepsTextOffStdout(t *testing.T) {
	var out bytes.Buffer
	b := NewBuffer(10)
	at := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	rd := &Reader{ClientID: "a", Adapter: decoder.Multimon{}, Buffer: b, Now: func() time.Time { return at }, Stdout: &out, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	in := strings.Join([]string{
		"POCSAG512: Address:  100001  Function: 0  Alpha:   QDhello",
		"POCSAG512: Address:  100001  Function: 0 ", // tone-only
		"garbage",
		"POCSAG1200: Address:    4242  Function: 1  Numeric: 123",
	}, "\n")
	if err := rd.Run(strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 2 {
		t.Fatalf("buffered %d, want 2", b.Len())
	}
	p, _ := b.Peek()
	want := Page{ClientID: "a", Capcode: "0100001", Text: "QDhello", ReceivedAt: at, Baud: 512}
	p.trace = nil // its trace, which a no-op tracer doesn't record
	if p != want {
		t.Errorf("got %+v, want %+v", p, want)
	}
	if out.Len() != 0 {
		t.Errorf("PRINT_PAGES is off but stdout got %q", out.String())
	}
}

// The server is down at first: pages wait in memory, keep their receive
// times and order, and go out when it's back. A page the server rejects is
// dropped and doesn't block the next one.
func TestSenderRetriesInOrder(t *testing.T) {
	var mu sync.Mutex
	var got []Page
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/api/v1/pages" {
			t.Errorf("bad request: %s %s", r.Header.Get("Authorization"), r.URL.Path)
		}
		if calls <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var p Page
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.Text == "bad" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		got = append(got, p)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	b := NewBuffer(10)
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	b.Push(Page{Capcode: "0000001", Text: "one", ReceivedAt: t0})
	b.Push(Page{Capcode: "0000001", Text: "bad", ReceivedAt: t0.Add(time.Second)})
	b.Push(Page{Capcode: "0000001", Text: "two", ReceivedAt: t0.Add(2 * time.Second)})
	s := &Sender{URL: srv.URL + "/", Key: "k", HTTP: srv.Client(), Buffer: b, Log: slog.New(slog.NewTextHandler(&logs, nil)), MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for b.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Text != "one" || !got[0].ReceivedAt.Equal(t0) || got[1].Text != "two" || !got[1].ReceivedAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("server got %+v", got)
	}
	for _, text := range []string{"one", "bad", "two"} {
		if strings.Contains(logs.String(), "text="+text) || strings.Contains(logs.String(), `"`+text+`"`) {
			t.Errorf("client log contains page text %q: %s", text, logs.String())
		}
	}
}
