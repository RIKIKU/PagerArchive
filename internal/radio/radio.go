// Package radio runs the client's radio pipeline (R13): rtl_fm's audio
// passes through the client on its way to multimon-ng and is measured on
// the way (R74a): how much arrives and how loud it is. None of it is kept
// or exported. rtl_fm's messages go to the client's log as before and are
// counted by kind.
package radio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Settings are the pipeline's settings (cmd/pagerarchive-client).
type Settings struct {
	Frequency string   // RADIO_FREQUENCY, e.g. 148.8125M
	Gain      string   // RADIO_GAIN in dB; empty for automatic
	PPM       string   // RADIO_PPM
	Mode      string   // MULTIMON_MODE: alpha, numeric or auto
	Bauds     []string // POCSAG_BAUDS
}

// SampleRate is rtl_fm's output rate: signed 16-bit mono samples, so
// 44,100 bytes of audio a second.
const SampleRate = 22050

// RTLFMArgs are rtl_fm's arguments. There's no squelch (-l), and there
// mustn't be: with squelch on, rtl_fm writes nothing while the channel is
// quiet, and that silence would look like a stuck dongle. Without it a
// working dongle always produces audio, noise included, so audio stopping
// while rtl_fm runs means the dongle has stopped (R74a).
func RTLFMArgs(s Settings) []string {
	args := []string{"-f", s.Frequency, "-M", "fm", "-s", fmt.Sprint(SampleRate), "-p", s.PPM}
	if s.Gain != "" {
		args = append(args, "-g", s.Gain)
	}
	return append(args, "-")
}

// MultimonArgs runs multimon-ng under stdbuf, so its output is line
// buffered, reading raw audio from stdin.
func MultimonArgs(s Settings) []string {
	args := []string{"-oL", "multimon-ng", "-t", "raw", "-f", s.Mode}
	for _, b := range s.Bauds {
		args = append(args, "-a", "POCSAG"+strings.TrimSpace(b))
	}
	return append(args, "/dev/stdin")
}

// Start runs rtl_fm and multimon-ng, passing the audio from one to the
// other through h, and returns multimon-ng's output and a function that
// waits for both to end. rtl_fm's messages are written to log as they come
// (never page text) and counted in h.
func Start(ctx context.Context, s Settings, h *Health, log io.Writer) (io.Reader, func() error, error) {
	rtl := exec.CommandContext(ctx, "rtl_fm", RTLFMArgs(s)...)
	mm := exec.CommandContext(ctx, "stdbuf", MultimonArgs(s)...)
	audio, err := rtl.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	messages, err := rtl.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	in, err := mm.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	out, err := mm.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := rtl.Start(); err != nil {
		return nil, nil, fmt.Errorf("start rtl_fm: %w", err)
	}
	if err := mm.Start(); err != nil {
		_ = rtl.Process.Kill()
		_ = rtl.Wait()
		return nil, nil, fmt.Errorf("start multimon-ng: %w", err)
	}
	watched := make(chan struct{})
	go func() { h.Watch(messages, log); close(watched) }()
	passed := make(chan struct{})
	go func() {
		// When multimon-ng stops taking audio, closing rtl_fm's output
		// stops rtl_fm too; when rtl_fm stops, closing multimon-ng's input
		// lets it finish.
		if err := h.Pass(in, audio); err != nil {
			audio.Close()
		}
		in.Close()
		close(passed)
	}()
	wait := func() error {
		<-passed
		<-watched // all reads are done before Wait closes the pipes
		return errors.Join(mm.Wait(), rtl.Wait())
	}
	return out, wait, nil
}
