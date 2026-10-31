package radio

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Health is the radio's numbers (R74a): audio received, its level, how
// long since audio last arrived, and rtl_fm's messages by kind. The audio
// path only adds to atomics, so measuring can't block or slow it whatever
// the exporter is doing: the metrics side reads them when it collects.
type Health struct {
	now        func() time.Time
	start      time.Time
	bytes      atomic.Int64  // audio passed to multimon-ng
	samples    atomic.Int64  // since the last Level
	sumSquares atomic.Uint64 // of those samples
	lastAudio  atomic.Int64  // Unix nanoseconds; 0 until audio arrives
	odd        bool          // a sample split between two reads: its first byte (Pass only)
	oddByte    byte

	mu       sync.Mutex
	messages map[string]int64 // rtl_fm's messages by kind
}

// NewHealth starts counting. now is the clock (time.Now).
func NewHealth(now func() time.Time) *Health {
	h := &Health{now: now, start: now(), messages: map[string]int64{}}
	for _, k := range Kinds {
		h.messages[k] = 0 // every kind is reported from the start, so a first one shows as an increase
	}
	return h
}

// Pass copies rtl_fm's audio to multimon-ng unchanged, measuring each
// chunk after it's written: measuring never delays the audio it measures,
// and takes no lock.
func (h *Health) Pass(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			h.measure(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// measure takes one chunk of audio: signed 16-bit little-endian samples,
// as rtl_fm -M fm writes them.
func (h *Health) measure(b []byte) {
	h.bytes.Add(int64(len(b)))
	h.lastAudio.Store(h.now().UnixNano())
	var sum uint64
	var n int64
	if h.odd && len(b) > 0 {
		s := int64(int16(uint16(h.oddByte) | uint16(b[0])<<8))
		sum, n, b, h.odd = uint64(s*s), 1, b[1:], false
	}
	for i := 0; i+1 < len(b); i += 2 {
		s := int64(int16(uint16(b[i]) | uint16(b[i+1])<<8))
		sum += uint64(s * s)
		n++
	}
	if len(b)%2 == 1 {
		h.odd, h.oddByte = true, b[len(b)-1]
	}
	h.samples.Add(n)
	h.sumSquares.Add(sum)
}

// Bytes is the audio received so far.
func (h *Health) Bytes() int64 { return h.bytes.Load() }

// Level is the audio's RMS level in dBFS since the last call (0 is full
// scale; -120 is the floor), and false if no audio came in between.
func (h *Health) Level() (float64, bool) {
	n := h.samples.Swap(0)
	sum := h.sumSquares.Swap(0)
	if n == 0 {
		return 0, false
	}
	rms := math.Sqrt(float64(sum) / float64(n))
	if rms < 32768e-6 {
		return -120, true
	}
	return 20 * math.Log10(rms/32768), true
}

// Silent is how long since audio last arrived, or since the start if none
// has.
func (h *Health) Silent() time.Duration {
	last := h.start
	if t := h.lastAudio.Load(); t != 0 {
		last = time.Unix(0, t)
	}
	return h.now().Sub(last)
}

// Messages is rtl_fm's messages so far, by kind.
func (h *Health) Messages() map[string]int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int64, len(h.messages))
	for k, v := range h.messages {
		out[k] = v
	}
	return out
}

func (h *Health) count(kind string) {
	h.mu.Lock()
	h.messages[kind]++
	h.mu.Unlock()
}

// Watch writes each of rtl_fm's messages to log, as rtl_fm wrote it, and
// counts it by kind. Only the kind is kept: a message can name the device
// and its serial number, and its text is never exported (R70).
func (h *Health) Watch(r io.Reader, log io.Writer) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		fmt.Fprintln(log, line)
		if k := Kind(line); k != "" {
			h.count(k)
		}
	}
	_, _ = io.Copy(log, r) // a line too long to scan: pass the rest on as it is
}

// Kinds of rtl_fm message (R74a). Anything that isn't one of the first
// four is Other, rtl_fm's normal start-up lines included.
const (
	NoDevice          = "no_device"
	OpenFailed        = "open_failed"
	USBTransferFailed = "usb_transfer_failed"
	TunerNotLocked    = "tuner_not_locked"
	Other             = "other"
)

// Kinds lists them.
var Kinds = []string{NoDevice, OpenFailed, USBTransferFailed, TunerNotLocked, Other}

// The messages each kind matches, from rtl_fm, its convenience functions,
// librtlsdr and the tuner drivers in rtl-sdr 2.0.2 (Debian 2.0.2-2, as the
// client image installs it; ADR 0013 lists each with its source). A kind
// of "" is a line that only explains the line before it, and isn't counted
// again.
var kindRes = []struct {
	kind string
	re   *regexp.Regexp
}{
	{NoDevice, regexp.MustCompile(`^No (supported|matching) devices found\.$`)},
	{OpenFailed, regexp.MustCompile(`^(Failed to open rtlsdr device #\d+\.|usb_open error -?\d+|usb_claim_interface error -?\d+|Kernel driver is active, or device is claimed by second instance of librtlsdr\.)$`)},
	{OpenFailed, regexp.MustCompile(`^Detaching kernel driver failed!`)}, // printed without a newline
	{"", regexp.MustCompile(`^(Please fix the device permissions, e\.g\. by installing the udev rules file rtl-sdr\.rules|In the first case, please either detach or blacklist the kernel module|\(dvb_usb_rtl28xxu\), or enable automatic detaching at compile time\.)$`)},
	{USBTransferFailed, regexp.MustCompile(`^(cb transfer status: -?\d+, canceling\.\.\.|Failed to submit transfer \d+|rtlsdr_(demod_)?(read|write)_reg failed with -?\d+|r82xx_(write|read): i2c (wr|rd) failed=-?\d+ reg=[0-9a-f]{2} len=\d+)$`)},
	{"", regexp.MustCompile(`^(Please increase your allowed usbfs buffer size with the following command:|echo 0 > /sys/module/usbcore/parameters/usbfs_memory_mb)$`)},
	{TunerNotLocked, regexp.MustCompile(`^(\[R82XX\] PLL not locked!|\[R82XX\] No valid PLL values for \d+ Hz!|\[E4K\] PLL not locked for \d+ Hz!|\[FC001[23]\] no valid PLL combination found for \d+ Hz!)$`)},
}

// Kind is the kind of one line from rtl_fm's stderr, or "" for a blank
// line or one that only explains the line before it.
func Kind(line string) string {
	line = strings.TrimRight(line, " \t\r")
	if line == "" {
		return ""
	}
	for _, k := range kindRes {
		if k.re.MatchString(line) {
			return k.kind
		}
	}
	return Other
}
