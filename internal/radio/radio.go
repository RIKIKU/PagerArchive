// Package radio runs the client's radio pipeline (R13): rtl_fm's audio
// passes through the client on its way to multimon-ng and is measured on
// the way (R74a): how much arrives and how loud it is. None of it is kept
// or exported. rtl_fm's messages go to the client's log as before and are
// counted by kind. When the pipeline fails, the client starts it again
// (R13b, Supervisor).
package radio

import (
	"fmt"
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
