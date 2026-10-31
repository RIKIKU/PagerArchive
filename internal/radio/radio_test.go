package radio

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// rtl_fm runs without squelch, so a working dongle always produces audio
// and silence means it's stuck (R74a).
func TestNoSquelch(t *testing.T) {
	for _, s := range []Settings{{Frequency: "148.8125M", PPM: "0"}, {Frequency: "148.8125M", PPM: "-3", Gain: "40"}} {
		args := RTLFMArgs(s)
		if slices.Contains(args, "-l") {
			t.Errorf("rtl_fm %s: squelch (-l) is on, so a quiet channel would look like a stuck dongle", strings.Join(args, " "))
		}
		if !slices.Equal(args[:6], []string{"-f", "148.8125M", "-M", "fm", "-s", "22050"}) || args[len(args)-1] != "-" {
			t.Errorf("rtl_fm %s", strings.Join(args, " "))
		}
	}
	if !t.Failed() {
		t.Logf("R74a: rtl_fm %s: no squelch", strings.Join(RTLFMArgs(Settings{Frequency: "148.8125M", PPM: "0"}), " "))
	}
}

// Each message rtl-sdr 2.0.2 can print about the four kinds of trouble is
// that kind; a line explaining the one before isn't counted again; the
// normal start-up lines and anything unknown are "other" (R74a, ADR 0013).
func TestKinds(t *testing.T) {
	for line, want := range map[string]string{
		"No supported devices found.":      NoDevice,
		"No matching devices found.":       NoDevice,
		"Failed to open rtlsdr device #0.": OpenFailed,
		"usb_open error -3":                OpenFailed,
		"usb_claim_interface error -6":     OpenFailed,
		"Kernel driver is active, or device is claimed by second instance of librtlsdr.":          OpenFailed,
		"Detaching kernel driver failed!":                                                         OpenFailed,
		"Detaching kernel driver failed!Failed to open rtlsdr device #0.":                         OpenFailed,
		"Please fix the device permissions, e.g. by installing the udev rules file rtl-sdr.rules": "",
		"In the first case, please either detach or blacklist the kernel module":                  "",
		"(dvb_usb_rtl28xxu), or enable automatic detaching at compile time.":                      "",
		"cb transfer status: 1, canceling...":                                                     USBTransferFailed,
		"cb transfer status: -1, canceling...":                                                    USBTransferFailed,
		"Failed to submit transfer 3":                                                             USBTransferFailed,
		"Please increase your allowed usbfs buffer size with the following command:":              "",
		"echo 0 > /sys/module/usbcore/parameters/usbfs_memory_mb":                                 "",
		"rtlsdr_read_reg failed with -7":                                                          USBTransferFailed,
		"rtlsdr_write_reg failed with -9":                                                         USBTransferFailed,
		"rtlsdr_demod_read_reg failed with -1":                                                    USBTransferFailed,
		"rtlsdr_demod_write_reg failed with -4":                                                   USBTransferFailed,
		"r82xx_write: i2c wr failed=-4 reg=1a len=1":                                              USBTransferFailed,
		"r82xx_read: i2c rd failed=-1 reg=00 len=4":                                               USBTransferFailed,
		"[R82XX] PLL not locked!":                                                                 TunerNotLocked,
		"[R82XX] No valid PLL values for 148812500 Hz!":                                           TunerNotLocked,
		"[E4K] PLL not locked for 148812500 Hz!":                                                  TunerNotLocked,
		"[FC0012] no valid PLL combination found for 148812500 Hz!":                               TunerNotLocked,
		"[FC0013] no valid PLL combination found for 148812500 Hz!":                               TunerNotLocked,
		"Found 1 device(s):":                                                                      Other,
		"  0:  Realtek, RTL2838UHIDIR, SN: 00000001":                                              Other,
		"Using device 0: Generic RTL2832U OEM":                                                    Other,
		"Found Rafael Micro R820T tuner":                                                          Other,
		"Tuned to 149079312 Hz.":                                                                  Other,
		"Signal caught, exiting!":                                                                 Other,
		"something rtl_fm has never said":                                                         Other,
		"":                                                                                        "",
		"   ":                                                                                     "",
	} {
		if got := Kind(line); got != want {
			t.Errorf("Kind(%q) = %q, want %q", line, got, want)
		}
	}
}

// samples is s16le audio of the given samples.
func samples(s ...int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// The level is the RMS in dBFS, the same however the audio is split into
// reads, even mid-sample; a quiet second has no level.
func TestLevel(t *testing.T) {
	now := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	h := NewHealth(func() time.Time { return now })
	sine := make([]int16, SampleRate)
	for i := range sine {
		sine[i] = int16(16384 * math.Sin(2*math.Pi*1000*float64(i)/SampleRate))
	}
	b := samples(sine...)
	for len(b) > 0 { // reads of 7 bytes: every other one splits a sample
		n := min(7, len(b))
		h.measure(b[:n])
		b = b[n:]
	}
	db, ok := h.Level()
	if want := 20 * math.Log10(0.5/math.Sqrt2); !ok || math.Abs(db-want) > 0.05 {
		t.Errorf("a half-scale sine: %.2f dBFS (%v), want %.2f", db, ok, want)
	}
	if h.Bytes() != 2*SampleRate {
		t.Errorf("bytes %d, want %d", h.Bytes(), 2*SampleRate)
	}
	if _, ok := h.Level(); ok {
		t.Error("a level with no audio since the last one")
	}
	h.measure(samples(0, 0, 0, 0))
	if db, _ := h.Level(); db != -120 {
		t.Errorf("digital silence: %.1f dBFS, want the -120 floor", db)
	}
	now = now.Add(90 * time.Second)
	if s := h.Silent(); s != 90*time.Second {
		t.Errorf("silent for %v, want 90s", s)
	}
}

// slowWriter takes a little at a time, as a pipe to a busy multimon-ng does.
type slowWriter struct{ out bytes.Buffer }

func (w *slowWriter) Write(p []byte) (int, error) {
	if len(p) > 1000 {
		p = p[:1000]
		w.out.Write(p)
		return len(p), io.ErrShortWrite
	}
	return w.out.Write(p)
}

// Pass hands multimon-ng exactly what rtl_fm wrote, and counts it.
func TestPassUnchanged(t *testing.T) {
	in := make([]byte, 5<<20+1) // an odd length: the last sample is cut
	_, _ = rand.Read(in)
	var out bytes.Buffer
	h := NewHealth(time.Now)
	if err := h.Pass(&out, bytes.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Fatal("multimon-ng got different audio from what rtl_fm wrote")
	}
	if h.Bytes() != int64(len(in)) {
		t.Errorf("counted %d bytes, want %d", h.Bytes(), len(in))
	}
	// A write error stops the copy, so rtl_fm can be stopped.
	if err := h.Pass(&slowWriter{}, bytes.NewReader(in)); err != io.ErrShortWrite {
		t.Errorf("a failed write: %v", err)
	}
	t.Logf("R74a: %d bytes passed through unchanged and counted", len(in))
}

// Watch writes every line to the log as it came and counts kinds only.
func TestWatch(t *testing.T) {
	h := NewHealth(time.Now)
	var log bytes.Buffer
	in := "Found 1 device(s):\n  0:  Realtek, RTL2838UHIDIR, SN: 00000001\n\nusb_open error -3\nPlease fix the device permissions, e.g. by installing the udev rules file rtl-sdr.rules\ncb transfer status: 1, canceling...\nno newline at the end"
	h.Watch(strings.NewReader(in), &log)
	if log.String() != in+"\n" {
		t.Errorf("logged %q, want rtl_fm's lines as they came", log.String())
	}
	want := map[string]int64{NoDevice: 0, OpenFailed: 1, USBTransferFailed: 1, TunerNotLocked: 0, Other: 3}
	if got := h.Messages(); !maps.Equal(got, want) {
		t.Errorf("counted %v, want %v", got, want)
	}
}
