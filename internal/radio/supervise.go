package radio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Why the pipeline was started again (R13b): the restarts metric's reasons.
const (
	// LostDevice: rtl_fm said the dongle was lost, missing or couldn't be
	// opened, and no audio followed.
	LostDevice = "lost_device"
	// Stalled: no audio for Stall, with nothing said.
	Stalled = "stalled"
	// Exited: rtl_fm or multimon-ng ended by itself, having said nothing
	// about the dongle.
	Exited = "exited"
)

// Reasons lists them.
var Reasons = []string{LostDevice, Stalled, Exited}

// R13b's numbers.
const (
	DefaultStall      = 30 * time.Second // no audio for this long is a fault
	DefaultLostGrace  = 2 * time.Second  // a lost-device message, then no audio for this long, is one
	DefaultKillWait   = 5 * time.Second  // SIGTERM, then SIGKILL after this
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = time.Minute
	DefaultExitAfter  = 5 // failed starts in a row before giving up (see Supervisor.ExitAfter)
)

// ErrGaveUp is Run giving up on the radio, so that the client exits and
// is started afresh (R13b).
var ErrGaveUp = errors.New("the radio pipeline keeps failing to start")

// A Supervisor runs rtl_fm and multimon-ng, passing the audio from one to
// the other through Health (R74a), and starts them again when they fail
// (R13b): a lost-device message from rtl_fm with no audio after it, no
// audio for Stall, or either program ending. A program that doesn't stop
// when asked is killed, and each is waited for.
type Supervisor struct {
	Settings Settings
	Health   *Health
	Messages io.Writer    // rtl_fm's messages go here as they come (never page text)
	Log      *slog.Logger // restarts and giving up
	// ExitAfter is how many failed starts in a row Run allows, once the
	// dongle has given audio in this run or been missing, before it gives
	// up with ErrGaveUp; 0 never gives up. A failed start is one that
	// ends without audio, other than by a stall.
	ExitAfter int
	// Running is told when the pipeline starts and stops (R74); nil is fine.
	Running func(running bool)

	// R13b's timings; zero is the default.
	Stall, LostGrace, KillWait, MinBackoff, MaxBackoff time.Duration

	// The programs, for tests: nil runs rtl_fm and multimon-ng (under
	// stdbuf) with the settings' arguments.
	RTLFM, Decoder func() *exec.Cmd

	poll time.Duration // how often the audio is looked at
}

func (s *Supervisor) defaults() {
	set := func(d *time.Duration, def time.Duration) {
		if *d <= 0 {
			*d = def
		}
	}
	set(&s.Stall, DefaultStall)
	set(&s.LostGrace, DefaultLostGrace)
	set(&s.KillWait, DefaultKillWait)
	set(&s.MinBackoff, DefaultMinBackoff)
	set(&s.MaxBackoff, DefaultMaxBackoff)
	set(&s.poll, 250*time.Millisecond)
	if s.RTLFM == nil {
		s.RTLFM = func() *exec.Cmd { return exec.Command("rtl_fm", RTLFMArgs(s.Settings)...) }
	}
	if s.Decoder == nil {
		s.Decoder = func() *exec.Cmd { return exec.Command("stdbuf", MultimonArgs(s.Settings)...) }
	}
	if s.Messages == nil {
		s.Messages = io.Discard
	}
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
}

func (s *Supervisor) running(r bool) {
	if s.Running != nil {
		s.Running(r)
	}
}

// Run runs the pipeline until ctx ends, handing each run's decoder output
// to consume, which reads it until it ends. It returns nil when ctx ends,
// ErrGaveUp when it gives up (ExitAfter), or the error of the first start
// when rtl_fm or multimon-ng can't be run at all.
func (s *Supervisor) Run(ctx context.Context, consume func(io.Reader)) error {
	s.defaults()
	g := giveUp{limit: s.ExitAfter}
	backoff := s.MinBackoff
	lastSaid := ""
	for n := 0; ; n++ {
		begun := time.Now()
		var res result
		p, err := s.start(consume)
		switch {
		case err != nil && n == 0:
			return err
		case err != nil:
			s.Log.Warn("radio pipeline didn't start", "error", err.Error())
			res = result{reason: Exited}
		default:
			s.running(true)
			res = s.watch(ctx, p, n)
			s.running(false)
		}
		if ctx.Err() != nil {
			return nil
		}
		if g.after(res) {
			s.Log.Error("radio pipeline keeps failing to start: exiting so the client is started afresh", "failed_starts", g.failed)
			return ErrGaveUp
		}
		said := fmt.Sprint(res.kinds[NoDevice], res.kinds[OpenFailed])
		switch {
		case res.audio && time.Since(begun) >= s.Stall:
			backoff = s.MinBackoff // it had been working: start again at once
		case n > 0 && said != lastSaid:
			backoff = s.MinBackoff // the dongle went or came back: try again soon
		}
		lastSaid = said
		wait := backoff
		backoff = min(2*backoff, s.MaxBackoff)
		s.Health.restarted(res.reason)
		s.Log.Warn("radio pipeline restarting", "reason", res.reason, "in", wait.String(), "restart", n+1)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// result is how one run of the pipeline ended.
type result struct {
	reason string
	audio  bool            // any audio came
	kinds  map[string]bool // the lost-device kinds rtl_fm said
}

// giveUp decides when Run gives up (R13b). A container is given the
// devices there when it starts, so a dongle back on the bus under a new
// device number can be opened only by a new start of the container. That's
// worth exiting for when the dongle worked in this run and now won't
// start, or was missing and has come back but can't be opened. Exiting
// for anything else would only reset the silence "Radio stalled" watches,
// over and over.
type giveUp struct {
	limit       int
	worked      bool // the dongle has given audio in this run
	sawNoDevice bool // rtl_fm has found no dongle in this run
	failed      int  // failed starts in a row that count
}

// after takes one run's result and says whether to give up.
func (g *giveUp) after(r result) bool {
	if r.audio {
		g.worked, g.failed = true, 0
		return false
	}
	if r.reason == Stalled {
		return false // running but silent: not a start that failed
	}
	if r.kinds[NoDevice] {
		g.sawNoDevice = true
	}
	if g.worked || (g.sawNoDevice && r.kinds[OpenFailed]) {
		g.failed++
	} else {
		g.failed = 0
	}
	return g.limit > 0 && g.failed >= g.limit
}

// lostKinds are the messages that say rtl_fm has lost the dongle, or
// can't find or open it (R74a's kinds; ADR 0013). "cb transfer status"
// in particular means librtlsdr has cancelled its USB reads for good, and
// rtl_fm then waits forever without exiting.
var lostKinds = map[string]bool{NoDevice: true, OpenFailed: true, USBTransferFailed: true}

// pipeline is one run of rtl_fm and multimon-ng.
type pipeline struct {
	rtl, dec         *exec.Cmd
	rtlDone, decDone chan struct{} // closed once each has ended and been reaped
	ended            chan struct{} // the decoder's output has ended
	lost             chan struct{} // a lost-device message came (buffered 1)
	reads            []*os.File    // the client's ends of the pipes
	wg               sync.WaitGroup

	mu    sync.Mutex
	kinds map[string]bool
}

func (s *Supervisor) start(consume func(io.Reader)) (*pipeline, error) {
	p := &pipeline{rtl: s.RTLFM(), dec: s.Decoder(), rtlDone: make(chan struct{}), decDone: make(chan struct{}),
		ended: make(chan struct{}), lost: make(chan struct{}, 1), kinds: map[string]bool{}}
	// Pipes of the client's own, so that waiting for a program never closes
	// what's still being read from it.
	var files []*os.File
	pipe := func() (r, w *os.File) {
		r, w, err := os.Pipe()
		if err == nil {
			files = append(files, r, w)
		}
		return r, w
	}
	audioR, audioW := pipe()
	msgR, msgW := pipe()
	inR, inW := pipe()
	outR, outW := pipe()
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	if len(files) != 8 {
		closeAll()
		return nil, errors.New("radio pipeline: no pipes")
	}
	p.rtl.Stdout, p.rtl.Stderr = audioW, msgW
	p.dec.Stdin, p.dec.Stdout = inR, outW
	ownGroup(p.rtl)
	ownGroup(p.dec)
	if err := p.rtl.Start(); err != nil {
		closeAll()
		return nil, fmt.Errorf("start rtl_fm: %w", err)
	}
	if err := p.dec.Start(); err != nil {
		signal(p.rtl, true)
		_ = p.rtl.Wait()
		closeAll()
		return nil, fmt.Errorf("start multimon-ng: %w", err)
	}
	// The programs have their ends; the client keeps only its own.
	for _, f := range []*os.File{audioW, msgW, inR, outW} {
		f.Close()
	}
	p.reads = []*os.File{audioR, msgR, outR, inW}
	go func() { _ = p.rtl.Wait(); close(p.rtlDone) }()
	go func() { _ = p.dec.Wait(); close(p.decDone) }()
	p.wg.Add(3)
	go func() {
		defer p.wg.Done()
		s.Health.watch(msgR, s.Messages, func(kind string) {
			if lostKinds[kind] {
				p.mu.Lock()
				p.kinds[kind] = true
				p.mu.Unlock()
				select {
				case p.lost <- struct{}{}:
				default:
				}
			}
		})
	}()
	go func() {
		defer p.wg.Done()
		// When multimon-ng stops taking audio, closing rtl_fm's output stops
		// rtl_fm too; when rtl_fm stops, closing multimon-ng's input lets it
		// finish.
		_ = s.Health.Pass(inW, audioR)
		inW.Close()
		audioR.Close()
	}()
	go func() {
		defer p.wg.Done()
		consume(outR)
		close(p.ended)
	}()
	return p, nil
}

// watch watches one run until it fails or ctx ends, then stops it.
func (s *Supervisor) watch(ctx context.Context, p *pipeline, n int) result {
	t := time.NewTicker(s.poll)
	defer t.Stop()
	res := result{}
	last, progress := s.Health.Bytes(), time.Now() // progress: when audio last came (or the start)
	var lostAt time.Time
	for res.reason == "" {
		select {
		case <-ctx.Done():
			res.reason = "stopped"
		case <-p.ended:
			res.reason = Exited
		case <-p.lost:
			if lostAt.IsZero() {
				lostAt = time.Now()
			}
		case now := <-t.C:
			if b := s.Health.Bytes(); b != last {
				last, progress = b, now
				if !res.audio && n > 0 {
					s.Log.Info("radio audio flowing again", "restarts", n)
				}
				res.audio = true
			}
			switch {
			case !lostAt.IsZero() && progress.Sub(lostAt) > s.LostGrace:
				lostAt = time.Time{} // the audio carried on through it: no fault
			case !lostAt.IsZero() && now.Sub(later(progress, lostAt)) >= s.LostGrace:
				res.reason = LostDevice
			case now.Sub(progress) >= s.Stall:
				res.reason = Stalled
			}
		}
	}
	if s.stop(p) {
		s.Log.Warn("radio pipeline didn't stop when asked: killed")
	}
	if b := s.Health.Bytes(); b != last {
		res.audio = true
	}
	p.mu.Lock()
	res.kinds = p.kinds
	p.mu.Unlock()
	if res.reason == Exited && len(res.kinds) > 0 {
		res.reason = LostDevice
	}
	return res
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// stop asks both programs to end (SIGTERM), kills what hasn't after
// KillWait (SIGKILL), waits for both, and then for everything reading
// from them. It says whether anything had to be killed.
func (s *Supervisor) stop(p *pipeline) (killed bool) {
	both := func() <-chan struct{} {
		c := make(chan struct{})
		go func() { <-p.rtlDone; <-p.decDone; close(c) }()
		return c
	}()
	for _, c := range []struct {
		cmd  *exec.Cmd
		done chan struct{}
	}{{p.rtl, p.rtlDone}, {p.dec, p.decDone}} {
		select {
		case <-c.done:
		default:
			signal(c.cmd, false)
		}
	}
	select {
	case <-both:
	case <-time.After(s.KillWait):
		killed = true
		for _, c := range []struct {
			cmd  *exec.Cmd
			done chan struct{}
		}{{p.rtl, p.rtlDone}, {p.dec, p.decDone}} {
			select {
			case <-c.done:
			default:
				signal(c.cmd, true)
			}
		}
		<-both
	}
	// Their ends of the pipes closed with them, so the reads end; if one
	// doesn't, closing the client's ends ends it.
	read := make(chan struct{})
	go func() { p.wg.Wait(); close(read) }()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		for _, f := range p.reads {
			f.Close()
		}
		<-read
	}
	for _, f := range p.reads {
		f.Close()
	}
	return killed
}
