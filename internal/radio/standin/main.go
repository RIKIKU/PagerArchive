// Command standin stands in for rtl_fm in A67 and in the demo client
// (docs/monitoring.md): installed as rtl_fm in the client image, it takes
// rtl_fm's arguments, prints rtl_fm's start-up messages and writes what
// rtl_fm -M fm -s 22050 writes, in real time: signed 16-bit audio, 22,050
// samples a second. The audio is the hiss an FM receiver without squelch
// hears, with a made-up POCSAG page (the example network's: no real page)
// at 1200 baud every STANDIN_EVERY (default 20s).
//
// Files in STANDIN_DIR (default /run/rtl_fm-standin) change what it does
// while it runs, so a test or a person can make it misbehave:
//
//	pause      while it exists, no audio is written and rtl_fm keeps
//	           running, as with a stuck dongle
//	usb-error  writes rtl_fm's message for a failed USB transfer once, then
//	           is removed
//	message    writes this file's first line to stderr once, then is
//	           removed
//
// and it keeps there:
//
//	args       the arguments it was started with
//	pages      each page it has written, "capcode text", one a line
//	written    the bytes of audio written so far and the seconds spent
//	           writing (not paused)
//	marker     a string of bytes it writes into the audio as samples, so a
//	           test can show no audio reached the collector
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	rate = 22050
	tick = 100 * time.Millisecond
)

// The pages it sends: the example profile's made-up network, numbered.
var pages = []struct {
	capcode uint32
	text    string
}{
	{100001, "@@EX067%04d HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1"},
	{100002, "HbRE EX067%04d CREWS RETURNING #STN2"},
	{100004, "QDTraining %04d Tuesday 7pm at the station"},
}

func main() {
	dir := os.Getenv("STANDIN_DIR")
	if dir == "" {
		dir = "/run/rtl_fm-standin"
	}
	every := 20 * time.Second
	if v := os.Getenv("STANDIN_EVERY"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second {
			fmt.Fprintln(os.Stderr, "STANDIN_EVERY must be a duration of 5s or more")
			os.Exit(1)
		}
		every = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	put := func(name, s string) {
		tmp := filepath.Join(dir, "."+name)
		if os.WriteFile(tmp, []byte(s), 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(dir, name))
		}
	}
	put("args", strings.Join(os.Args[1:], " ")+"\n")
	_ = os.Remove(filepath.Join(dir, "pages"))
	marker := fmt.Sprintf("STANDIN-AUDIO-%08x", rand.Uint32())
	put("marker", marker+"\n")

	// rtl_fm 2.0.2's start-up messages for a dongle with an R820T tuner.
	for _, l := range []string{"Found 1 device(s):", "  0:  Realtek, RTL2838UHIDIR, SN: 00000001", "", "Using device 0: Generic RTL2832U OEM",
		"Found Rafael Micro R820T tuner", "Tuner gain set to automatic.", "Tuner error set to 0 ppm.", "Tuned to 149066075 Hz.",
		"Oversampling input by: 46x.", "Oversampling output by: 1x.", "Buffer size: 8.04ms", "Exact sample rate is: 1014300.020041 Hz",
		"Sampling at 1014300 S/s.", "Output at 22050 Hz."} {
		fmt.Fprintln(os.Stderr, l)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "Signal caught, exiting!")
		os.Exit(0)
	}()

	rng := rand.New(rand.NewPCG(1, 2))
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	var queue []int16 // a page's audio still to write
	var queued string // and its page, recorded once it's all written
	var written int64
	var spent time.Duration
	n, ticks := 0, 0
	next := time.Now().Add(3 * time.Second)
	last := time.Now()
	t := time.NewTicker(tick)
	for range t.C {
		now := time.Now()
		if _, err := os.Stat(filepath.Join(dir, "usb-error")); err == nil {
			fmt.Fprintln(os.Stderr, "cb transfer status: 1, canceling...")
			_ = os.Remove(filepath.Join(dir, "usb-error"))
		}
		if b, err := os.ReadFile(filepath.Join(dir, "message")); err == nil {
			fmt.Fprintln(os.Stderr, strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0])
			_ = os.Remove(filepath.Join(dir, "message"))
		}
		if _, err := os.Stat(filepath.Join(dir, "pause")); err == nil {
			last, next = now, now.Add(3*time.Second) // nothing is written, and nothing is caught up afterwards
			continue
		}
		want := int(now.Sub(last).Seconds() * rate)
		spent += now.Sub(last)
		last = now
		if len(queue) == 0 && !now.Before(next) {
			p := pages[n%len(pages)]
			text := fmt.Sprintf(p.text, n)
			queue = audio(encode(p.capcode, text), 1200, rate, rng)
			queued = fmt.Sprintf("%07d %s", p.capcode, text)
			n++
			next = now.Add(every)
		}
		samples := make([]int16, 0, want)
		hiss := len(queue) == 0 // this chunk is hiss only, so the marker can't touch a page
		for len(samples) < want {
			if len(queue) > 0 {
				k := min(len(queue), want-len(samples))
				samples = append(samples, queue[:k]...)
				if queue = queue[k:]; len(queue) == 0 {
					f, err := os.OpenFile(filepath.Join(dir, "pages"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
					if err == nil {
						fmt.Fprintln(f, queued)
						f.Close()
					}
				}
				continue
			}
			samples = append(samples, noise(want-len(samples), rng)...)
		}
		buf := make([]byte, 2*len(samples))
		for i, s := range samples {
			binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
		}
		if ticks++; hiss && ticks%100 == 0 && len(buf) > len(marker) { // the marker, every 10 s or so, in the hiss
			copy(buf[len(buf)-len(marker):], marker)
		}
		if _, err := out.Write(buf); err != nil {
			os.Exit(1) // the client has gone
		}
		if err := out.Flush(); err != nil {
			os.Exit(1)
		}
		written += int64(len(buf))
		put("written", fmt.Sprintf("%d %.1f\n", written, spent.Seconds()))
	}
}
