package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// groupStandIn is Authentik's group API for one group, as SwitcherGroup uses it.
type groupStandIn struct {
	mu      sync.Mutex
	members map[string]bool
	missing bool
	readErr error
	setErr  error
	reads   int
	// hold, when set, makes a read wait until it's closed, after taking the
	// members it will answer with.
	hold    chan struct{}
	reading chan struct{}
}

func (d *groupStandIn) GroupMembers(ctx context.Context, group string) (string, map[string]bool, error) {
	d.mu.Lock()
	d.reads++
	m, missing, err, hold := maps.Clone(d.members), d.missing, d.readErr, d.hold
	d.mu.Unlock()
	if hold != nil {
		d.reading <- struct{}{}
		<-hold
	}
	switch {
	case err != nil:
		return "", nil, err
	case missing:
		return "", nil, fmt.Errorf("find group: %w", ErrNoGroup)
	}
	return "g-1", m, nil
}

func (d *groupStandIn) SetGroupMember(ctx context.Context, groupID, subject string, member bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.setErr != nil {
		return d.setErr
	}
	if member {
		d.members[subject] = true
	} else {
		delete(d.members, subject)
	}
	return nil
}

func newGroup(d *groupStandIn) (*SwitcherGroup, *bytes.Buffer) {
	var logs bytes.Buffer
	return &SwitcherGroup{Name: "radioarchive-preview", Dir: d, Log: slog.New(slog.NewJSONHandler(&syncWriter{w: &logs}, nil))}, &logs
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// A159 (R77f): the members are read at start and then every interval.
func TestSwitcherGroupReadsAtStartAndEveryInterval(t *testing.T) {
	d := &groupStandIn{members: map[string]bool{"7": true}}
	g, _ := newGroup(d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.run(ctx, 50*time.Millisecond); close(done) }()
	eventually(t, func() bool { return g.Member("7") }, "the first read")
	d.mu.Lock()
	d.members["12"] = true // changed in Authentik
	d.mu.Unlock()
	eventually(t, func() bool { return g.Member("12") }, "a read after the interval")
	cancel()
	<-done
	if SwitcherReadEvery != 30*time.Second {
		t.Errorf("SwitcherReadEvery is %v, want R77f's 30 s", SwitcherReadEvery)
	}
}

// A159: a failed read keeps the members last read, and is logged once; the
// read that works again is logged once too. A missing group has nobody.
func TestSwitcherGroupKeepsMembersThroughFailures(t *testing.T) {
	d := &groupStandIn{members: map[string]bool{"7": true}}
	g, logs := newGroup(d)
	ctx := context.Background()
	g.Read(ctx)
	d.readErr = errors.New("find group: HTTP 502")
	g.Read(ctx)
	g.Read(ctx)
	if !g.Member("7") || g.Problem() != GroupUnreadable {
		t.Errorf("after failed reads: member %v, problem %q", g.Member("7"), g.Problem())
	}
	d.readErr = nil
	g.Read(ctx)
	g.Read(ctx)
	if g.Problem() != "" {
		t.Errorf("read again: problem %q", g.Problem())
	}
	if n := strings.Count(logs.String(), "couldn't read the switcher's group"); n != 1 {
		t.Errorf("the failure logged %d times, want once", n)
	}
	if n := strings.Count(logs.String(), "the switcher's group is read again"); n != 1 {
		t.Errorf("the recovery logged %d times, want once", n)
	}
	d.missing = true
	g.Read(ctx)
	if g.Member("7") || g.Problem() != GroupMissing {
		t.Errorf("a missing group: member %v, problem %q", g.Member("7"), g.Problem())
	}
	var refusal *Error
	if err := g.set(ctx, "7", true); !errors.As(err, &refusal) || refusal.Status != http.StatusConflict || refusal.Code != "no_group" {
		t.Errorf("adding to a missing group: %v", err)
	}
	d.missing, d.setErr = false, errors.New("add to group: HTTP 403")
	if err := g.set(ctx, "7", true); !errors.As(err, &refusal) || refusal.Status != http.StatusServiceUnavailable {
		t.Errorf("Authentik refusing: %v", err)
	}
}

// A159: a change made here while a read is under way isn't undone when the
// read, which may not have it, finishes; and nothing waits for that read.
func TestSwitcherGroupWriteDuringReadWins(t *testing.T) {
	d := &groupStandIn{members: map[string]bool{"7": true}}
	g, _ := newGroup(d)
	ctx := context.Background()
	g.Read(ctx)
	d.hold, d.reading = make(chan struct{}), make(chan struct{})
	read := make(chan struct{})
	go func() { g.Read(ctx); close(read) }()
	<-d.reading // the read has taken the members, without 12

	answered := make(chan bool)
	go func() { answered <- g.Member("7") }()
	select {
	case in := <-answered:
		if !in {
			t.Error("during a read: the members last read are gone")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("asking for a member waited for the read")
	}
	if err := g.set(ctx, "12", true); err != nil {
		t.Fatal(err)
	}
	close(d.hold)
	<-read
	if !g.Member("12") {
		t.Error("the read that started before the change undid it")
	}
	d.hold = nil
	g.Read(ctx)
	if !g.Member("12") || !g.Member("7") {
		t.Error("the next read lost a member")
	}
	if err := g.set(ctx, "7", false); err != nil || g.Member("7") {
		t.Errorf("removing: %v, still a member %v", err, g.Member("7"))
	}
}

func eventually(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s didn't happen in 5 s", what)
		}
	}
}
