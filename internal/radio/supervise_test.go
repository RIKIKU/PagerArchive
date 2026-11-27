//go:build unix

package radio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var standinBin struct {
	once sync.Once
	path string
	err  error
}

// standin builds the stand-in for rtl_fm (./standin) once per run.
func standin(t *testing.T) string {
	t.Helper()
	standinBin.once.Do(func() {
		dir, err := os.MkdirTemp("", "pa-radio-standin-")
		if err != nil {
			standinBin.err = err
			return
		}
		standinBin.path = filepath.Join(dir, "rtl_fm")
		if out, err := exec.Command("go", "build", "-o", standinBin.path, "./standin").CombinedOutput(); err != nil {
			standinBin.err = errors.New("build the stand-in: " + err.Error() + "\n" + string(out))
		}
	})
	if standinBin.err != nil {
		t.Fatal(standinBin.err)
	}
	return standinBin.path
}

func TestMain(m *testing.M) {
	code := m.Run()
	if standinBin.path != "" {
		os.RemoveAll(filepath.Dir(standinBin.path))
	}
	os.Exit(code)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// rig is a Supervisor running the stand-in as rtl_fm and cat as the
// decoder, at short timings.
type rig struct {
	t      *testing.T
	dir    string
	sup    *Supervisor
	log    *syncBuffer
	cancel context.CancelFunc
	done   chan error

	mu   sync.Mutex
	cmds []*exec.Cmd // each rtl_fm started
	read int64       // bytes the decoder passed on
}

func newRig(t *testing.T, s *Supervisor) *rig {
	t.Helper()
	bin := standin(t)
	r := &rig{t: t, dir: t.TempDir(), log: &syncBuffer{}, done: make(chan error, 1)}
	s.Health = NewHealth(time.Now)
	s.Log = slog.New(slog.NewTextHandler(r.log, nil))
	s.Messages = r.log
	s.RTLFM = func() *exec.Cmd {
		c := exec.Command(bin, RTLFMArgs(Settings{Frequency: "148.8125M", PPM: "0"})...)
		c.Env = append(os.Environ(), "STANDIN_DIR="+r.dir)
		r.mu.Lock()
		r.cmds = append(r.cmds, c)
		r.mu.Unlock()
		return c
	}
	if s.Decoder == nil {
		s.Decoder = func() *exec.Cmd { return exec.Command("cat") }
	}
	s.poll = 50 * time.Millisecond
	r.sup = s
	return r
}

func (r *rig) control(name string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) remove(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		r.done <- r.sup.Run(ctx, func(out io.Reader) {
			n, _ := io.Copy(io.Discard, out)
			r.mu.Lock()
			r.read += n
			r.mu.Unlock()
		})
	}()
	r.t.Cleanup(func() { r.stop() })
}

// stop ends Run, which must return at once, leaving no stand-in running.
func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Errorf("Run: %v", err)
		}
	case <-time.After(r.sup.KillWait + 5*time.Second):
		r.t.Fatal("Run didn't return once stopped")
	}
	r.noneLeft()
}

// noneLeft: every rtl_fm started has ended and been reaped.
func (r *rig) noneLeft() {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cmds {
		if c.Process == nil {
			continue
		}
		if err := syscall.Kill(c.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
			r.t.Errorf("rtl_fm %d is still there (running or defunct): %v", c.Process.Pid, err)
		}
	}
}

func (r *rig) starts() int {
	b, _ := os.ReadFile(filepath.Join(r.dir, "starts"))
	return strings.Count(string(b), "\n")
}

func (r *rig) audio() int64 { return r.sup.Health.Bytes() }

func eventually(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return f()
}

// audioFlows waits for audio to grow by a second's worth.
func (r *rig) audioFlows(within time.Duration) bool {
	from := r.audio()
	return eventually(within, func() bool { return r.audio() > from+2*SampleRate })
}

func short() *Supervisor {
	return &Supervisor{Stall: 2 * time.Second, LostGrace: 500 * time.Millisecond, KillWait: time.Second,
		MinBackoff: 100 * time.Millisecond, MaxBackoff: 400 * time.Millisecond, ExitAfter: DefaultExitAfter}
}

// rtl_fm hangs as it does when the dongle drops off USB: a burst of "cb
// transfer status", then silence, ignoring SIGTERM. The client kills it
// and starts it again, and the audio comes back (R13b).
func TestRadioRestartsAfterHang(t *testing.T) {
	r := newRig(t, short())
	r.control("quiet")
	r.run()
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio at the start: %s", r.log)
	}
	hungAt := time.Now()
	r.control("hang")
	if !eventually(10*time.Second, func() bool { return r.sup.Health.Restarts()[LostDevice] == 1 }) {
		t.Fatalf("no restart for the lost dongle: %v\n%s", r.sup.Health.Restarts(), r.log)
	}
	took := time.Since(hungAt)
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio after the restart: %s", r.log)
	}
	log := r.log.String()
	if !strings.Contains(log, "radio pipeline didn't stop when asked: killed") || !strings.Contains(log, "reason=lost_device") ||
		!strings.Contains(log, "radio audio flowing again") {
		t.Errorf("the log doesn't say it was killed and started again:\n%s", log)
	}
	if strings.Count(log, "cb transfer status: 5, canceling...") != 15 {
		t.Errorf("rtl_fm's messages weren't all passed to the log:\n%s", log)
	}
	if n := r.starts(); n != 2 {
		t.Errorf("rtl_fm started %d times, want 2", n)
	}
	r.mu.Lock()
	first := r.cmds[0].Process.Pid
	r.mu.Unlock()
	if err := syscall.Kill(first, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the hung rtl_fm (%d) is still there: %v", first, err)
	}
	r.stop()
	if !t.Failed() {
		t.Logf("R13b: rtl_fm hung after 15 \"cb transfer status\" lines, ignoring SIGTERM: killed and started again %v later, audio flowing", took.Round(10*time.Millisecond))
	}
}

// Audio stopping with nothing said is a stall: the pipeline is started
// again after Stall (R13b).
func TestRadioRestartsAfterStall(t *testing.T) {
	r := newRig(t, short())
	r.control("quiet")
	r.run()
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio at the start: %s", r.log)
	}
	at := time.Now()
	r.control("stall")
	if !eventually(8*time.Second, func() bool { return r.sup.Health.Restarts()[Stalled] == 1 }) {
		t.Fatalf("no restart for the stall: %v\n%s", r.sup.Health.Restarts(), r.log)
	}
	took := time.Since(at)
	if took < r.sup.Stall {
		t.Errorf("restarted %v after the audio stopped, before Stall (%v)", took, r.sup.Stall)
	}
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio after the restart: %s", r.log)
	}
	if strings.Contains(r.log.String(), "killed") {
		t.Errorf("a stand-in that stops when asked was killed:\n%s", r.log)
	}
	if got := r.sup.Health.Restarts(); got[LostDevice] != 0 || got[Exited] != 0 {
		t.Errorf("restarts %v, want one stalled only", got)
	}
	// The silence is counted across the restart: from when audio stopped.
	r.stop()
	if !t.Failed() {
		t.Logf("R13b: audio stopped with nothing said: started again %v later (Stall %v), audio flowing", took.Round(10*time.Millisecond), r.sup.Stall)
	}
}

// A quiet channel still gives audio (squelch is off), so it never
// restarts; nor does a lost-device message the audio carries on through.
func TestRadioQuietChannelNoRestart(t *testing.T) {
	r := newRig(t, short())
	r.control("quiet")
	r.run()
	time.Sleep(time.Second)
	r.control("usb-error") // "cb transfer status: 1, canceling...", and the audio carries on
	time.Sleep(3 * r.sup.Stall)
	if got := r.sup.Health.Restarts(); got[LostDevice]+got[Stalled]+got[Exited] != 0 || r.starts() != 1 {
		t.Errorf("a healthy quiet channel restarted: %v, %d starts\n%s", got, r.starts(), r.log)
	}
	if r.sup.Health.Messages()[USBTransferFailed] != 1 {
		t.Errorf("the message wasn't counted: %v", r.sup.Health.Messages())
	}
	if s := r.sup.Health.Silent(); s > time.Second {
		t.Errorf("silent for %v on a working radio", s)
	}
	r.stop()
	if !t.Failed() {
		t.Logf("R13b: %v of a quiet channel, with a passing USB message: 1 start, no restarts", 3*r.sup.Stall+time.Second)
	}
}

// The dongle goes missing for a moment and comes back: the client retries
// until it's there, without giving up (R13b).
func TestRadioMissingThenBack(t *testing.T) {
	r := newRig(t, short())
	r.control("quiet")
	r.run()
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio at the start: %s", r.log)
	}
	r.control("gone")
	r.control("hang")
	if !eventually(10*time.Second, func() bool { return r.sup.Health.Messages()[NoDevice] >= 2 }) {
		t.Fatalf("no failed starts: %v\n%s", r.sup.Health.Messages(), r.log)
	}
	r.remove("gone")
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio once the dongle was back: %s", r.log)
	}
	select {
	case err := <-r.done:
		t.Fatalf("Run ended: %v", err)
	default:
	}
	got := r.sup.Health.Restarts()
	r.stop()
	if got[LostDevice] < 3 {
		t.Errorf("restarts %v, want the hang and 2 or more failed starts as lost_device", got)
	}
	if !t.Failed() {
		t.Logf("R13b: missing for %d starts, then back: restarts %v, audio flowing, still running", r.starts()-2, got)
	}
}

// A dongle that worked and then won't start ExitAfter times in a row:
// Run gives up, so the client is started afresh (R13b).
func TestRadioGivesUpAfterLosingAWorkingDongle(t *testing.T) {
	r := newRig(t, short())
	r.control("quiet")
	r.run()
	if !r.audioFlows(5 * time.Second) {
		t.Fatalf("no audio at the start: %s", r.log)
	}
	r.control("gone")
	r.control("hang")
	var err error
	select {
	case err = <-r.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("Run didn't give up: %v\n%s", r.sup.Health.Restarts(), r.log)
	}
	r.cancel()
	r.cancel = nil
	if !errors.Is(err, ErrGaveUp) {
		t.Fatalf("Run: %v, want ErrGaveUp", err)
	}
	if n := r.starts(); n != 1+DefaultExitAfter {
		t.Errorf("%d starts, want the first and %d that failed", n, DefaultExitAfter)
	}
	if !strings.Contains(r.log.String(), "radio pipeline keeps failing to start") {
		t.Errorf("giving up isn't logged:\n%s", r.log)
	}
	r.noneLeft()
	if !t.Failed() {
		t.Logf("R13b: a dongle that had worked, then %d failed starts: Run gave up (%v)", DefaultExitAfter, err)
	}
}

// Failed starts back off from MinBackoff, doubling to MaxBackoff.
func TestRadioBackoff(t *testing.T) {
	s := short()
	s.ExitAfter = 0
	r := newRig(t, s)
	var mu sync.Mutex
	var at []time.Time
	bin := standin(t)
	s.RTLFM = func() *exec.Cmd {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		c := exec.Command(bin)
		c.Env = append(os.Environ(), "STANDIN_DIR="+r.dir)
		return c
	}
	r.control("gone")
	r.run()
	if !eventually(5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(at) >= 7 }) {
		t.Fatalf("%d starts", len(at))
	}
	r.stop()
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{100, 200, 400, 400, 400, 400}
	var gaps []time.Duration
	for i, w := range want {
		gap := at[i+1].Sub(at[i])
		gaps = append(gaps, gap.Round(10*time.Millisecond))
		if gap < w*time.Millisecond || gap > w*time.Millisecond+500*time.Millisecond {
			t.Errorf("start %d came %v after the one before, want about %v", i+2, gap, w*time.Millisecond)
		}
	}
	if !t.Failed() {
		t.Logf("R13b: failed starts %v apart, from MinBackoff doubling to MaxBackoff", gaps)
	}
}

// When Run gives up (R13b's cases).
func TestRadioWhenToGiveUp(t *testing.T) {
	noDevice := result{reason: LostDevice, kinds: map[string]bool{NoDevice: true}}
	openFailed := result{reason: LostDevice, kinds: map[string]bool{OpenFailed: true}}
	worked := result{reason: LostDevice, audio: true, kinds: map[string]bool{USBTransferFailed: true}}
	stalled := result{reason: Stalled}
	exited := result{reason: Exited}
	rep := func(r result, n int) []result {
		var out []result
		for range n {
			out = append(out, r)
		}
		return out
	}
	cat := func(rs ...[]result) []result {
		var out []result
		for _, r := range rs {
			out = append(out, r...)
		}
		return out
	}
	for _, c := range []struct {
		name string
		runs []result
		want int // the run (1-based) it gives up after, 0 for never
	}{
		{"a working dongle lost, never back", cat([]result{worked}, rep(noDevice, 10)), 6},
		{"a working dongle lost, back but unopenable", cat([]result{worked}, rep(noDevice, 2), rep(openFailed, 10)), 6},
		{"a working dongle lost, back within 4 starts", cat([]result{worked}, rep(noDevice, 4), []result{worked}, rep(noDevice, 4)), 0},
		{"no dongle from the start", rep(noDevice, 50), 0},
		{"held by another driver from the start", rep(openFailed, 50), 0},
		{"missing, then back but unopenable", cat(rep(noDevice, 10), rep(openFailed, 10)), 15},
		{"a dongle that keeps stalling", cat([]result{worked}, rep(stalled, 50)), 0},
		{"rtl_fm that keeps ending, having worked", cat([]result{worked}, rep(exited, 10)), 6},
	} {
		g := giveUp{limit: DefaultExitAfter}
		got := 0
		for i, r := range c.runs {
			if g.after(r) {
				got = i + 1
				break
			}
		}
		if got != c.want {
			t.Errorf("%s: gave up after run %d, want %d", c.name, got, c.want)
		}
		g = giveUp{limit: 0}
		for _, r := range c.runs {
			if g.after(r) {
				t.Errorf("%s: gave up with RADIO_EXIT_AFTER=0", c.name)
				break
			}
		}
	}
}
