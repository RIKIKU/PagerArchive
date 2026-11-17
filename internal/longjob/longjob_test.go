package longjob

import (
	"testing"
	"time"
)

// R23b: the rate and time left are from the run's start; done can pass the
// total (pages stored while it runs), and the finished run's total is then
// what it did.
func TestProgress(t *testing.T) {
	var p Progress
	if s := p.Snapshot(); s.Running || s.Total != 0 || !s.Started.IsZero() {
		t.Fatalf("zero value: %+v", s)
	}
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	p.Begin(1, 1000, 0, at, at)
	for range 250 {
		p.Add()
	}
	s := p.Snapshot()
	perMin, eta := s.Rate(at.Add(time.Minute))
	if !s.Running || s.Done != 250 || s.Percent() != 25 || perMin != 250 || eta != 180 {
		t.Errorf("a quarter done in a minute: %+v, %v a minute, %d s left, %d%%", s, perMin, eta, s.Percent())
	}
	if _, eta := (Snapshot{Total: 10, Started: at}).Rate(at.Add(time.Minute)); eta != -1 {
		t.Errorf("nothing done: %d s left, want -1 (unknown)", eta)
	}
	for range 760 {
		p.Add()
	}
	if s := p.Snapshot(); s.Percent() != 100 {
		t.Errorf("past the total: %d%%", s.Percent())
	}
	p.Finish()
	if s := p.Snapshot(); s.Running || s.Total != 1010 || s.Done != 1010 || s.Count != 1010 {
		t.Errorf("finished: %+v", s)
	}
	p.Begin(2, 5, 0, at, at)
	p.Add()
	if s := p.Snapshot(); s.Done != 1 || s.Total != 5 || s.Count != 1011 {
		t.Errorf("a second run: %+v", s)
	}
}

// R23c: a run carried on after a restart or a pause has its rate from then,
// and the state follows an admin's change at once.
func TestProgressCarriedOn(t *testing.T) {
	var p Progress
	if s := p.Snapshot(); s.State != Idle || s.State.Code() != 0 {
		t.Fatalf("zero value: %+v", s)
	}
	started := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	at := started.Add(time.Hour)
	p.Begin(7, 1000, 400, started, at)
	for range 100 {
		p.Add()
	}
	s := p.Snapshot()
	perMin, eta := s.Rate(at.Add(time.Minute))
	if s.State != Running || s.State.Code() != 1 || s.Done != 500 || perMin != 100 || eta != 300 {
		t.Errorf("carried on from 400: %+v, %v a minute, %d s left", s, perMin, eta)
	}
	p.Undo(20)
	if s := p.Snapshot(); s.Done != 480 || s.Count != 100 {
		t.Errorf("a batch rolled back: %+v", s)
	}
	p.Interrupt(Paused)
	if s := p.Snapshot(); s.State != Paused || s.Running || s.State.Code() != 2 || !p.Interrupted() {
		t.Errorf("paused: %+v", s)
	}
	p.Hold(Stopped, 7, 1000, 480, started)
	if s := p.Snapshot(); s.State != Stopped || s.State.Code() != 3 || s.Done != 480 {
		t.Errorf("stopped: %+v", s)
	}
	p.Begin(7, 1000, 0, at, at)
	if p.Interrupted() {
		t.Error("starting again keeps the interrupt")
	}
}
