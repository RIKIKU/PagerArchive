// Package decoder turns decoder program output into page records (R12).
// v1 ships one adapter, for multimon-ng.
package decoder

import (
	"regexp"
	"strconv"
	"strings"
)

// Record is one decoded page, independent of the decoder that produced it.
type Record struct {
	Capcode  string // zero-padded to 7 digits
	Function int
	Baud     int
	Numeric  bool
	Text     string
}

// Adapter turns one line of decoder output into a Record. ok is false for
// lines that aren't pages (status lines, tone-only pages, other modes).
type Adapter interface {
	Decode(line string) (rec Record, ok bool)
}

// PadCapcode zero-pads a capcode to 7 digits.
func PadCapcode(s string) string {
	if len(s) >= 7 {
		return s
	}
	return strings.Repeat("0", 7-len(s)) + s
}

// multimon-ng prints "POCSAG512: Address: %7lu  Function: %1hhi  " and then
// "Alpha:   %s" or "Numeric: %s"; with -v3 a "Certainty: %5i  " comes first.
var multimonLine = regexp.MustCompile(`^POCSAG(512|1200|2400): Address: *([0-9]{1,7}) +Function: ([0-3]) +(?:Certainty: *-?[0-9]+ +)?(Alpha|Numeric):(.*)$`)

// Multimon decodes multimon-ng POCSAG output.
type Multimon struct{}

func (Multimon) Decode(line string) (Record, bool) {
	line = strings.TrimRight(line, "\r\n")
	m := multimonLine.FindStringSubmatch(line)
	if m == nil {
		return Record{}, false
	}
	baud, _ := strconv.Atoi(m[1])
	fn, _ := strconv.Atoi(m[3])
	numeric := m[4] == "Numeric"
	// Trim only multimon's separator (section 14).
	sep := "   "
	if numeric {
		sep = " "
	}
	text := strings.TrimPrefix(m[5], sep)
	if text == "" {
		return Record{}, false // tone-only
	}
	return Record{
		Capcode:  PadCapcode(m[2]),
		Function: fn,
		Baud:     baud,
		Numeric:  numeric,
		Text:     text,
	}, true
}
