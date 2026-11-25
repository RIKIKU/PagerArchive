// Package pipeline applies network and agency profile rules to a page.
// It is pure: no I/O, no logging. Callers must never log page text (P1).
package pipeline

import (
	"crypto/sha256"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// Redacted replaces each redaction match (section 14).
const Redacted = "REDACTED"

// Prepared is a page after the network rules, ready to store (R16–R18, R22).
type Prepared struct {
	Dropped   bool // ignored capcode (R16): never stored
	Capcode   string
	Text      string           // control tokens removed, redacted, marker kept
	Priority  profile.Priority // "" means none
	Heartbeat bool
}

// controlToken matches the ASCII control names multimon-ng prints.
var controlToken = regexp.MustCompile(`<(?:NUL|SOH|STX|ETX|EOT|ENQ|ACK|BEL|BS|HT|LF|VT|FF|CR|SO|SI|DLE|DC[1-4]|NAK|SYN|ETB|CAN|EM|SUB|ESC|FS|GS|RS|US|DEL)>`)

// CleanControlTokens removes control tokens; <LF> and <CR> become a space (R17).
func CleanControlTokens(s string) string {
	return controlToken.ReplaceAllStringFunc(s, func(tok string) string {
		if tok == "<LF>" || tok == "<CR>" {
			return " "
		}
		return ""
	})
}

// Redact applies the network's redaction rules in order (R18).
func Redact(n *profile.Network, s string) string {
	for _, r := range n.Redactions {
		s = r.Apply(s, Redacted)
	}
	return s
}

// Prepare applies the network rules to a page with a 7-digit capcode.
func Prepare(n *profile.Network, capcode, raw string) Prepared {
	c, _ := strconv.Atoi(capcode)
	for _, r := range n.Ignored {
		if r.Contains(c) {
			return Prepared{Dropped: true, Capcode: capcode}
		}
	}
	p := Prepared{Capcode: capcode}
	p.Text = Redact(n, CleanControlTokens(raw))
	if rs := []rune(p.Text); len(rs) >= 2 {
		p.Priority = n.Markers[string(rs[:2])]
	}
	for _, r := range n.Heartbeat {
		if r.Contains(c) {
			p.Heartbeat = true
		}
	}
	return p
}

// Body is the text a pager displays and formats run on: the stored text
// without its priority marker. With no known marker it's the whole text.
func Body(p Prepared) string {
	if p.Priority == "" {
		return p.Text
	}
	return string([]rune(p.Text)[2:])
}

// DedupKey is the deduplication key: a hash of capcode and stored text (R19).
func DedupKey(p Prepared) []byte {
	sum := sha256.Sum256([]byte(p.Capcode + "\n" + p.Text))
	return sum[:]
}

// Parsed is the result of running an agency profile's formats on a page.
type Parsed struct {
	Format         string         // matching format, "" if none
	Fields         map[string]any // string or []string; absent means null
	ParseFailed    bool
	AddressUnsplit bool
	// LargeEventField: a field holds a value the agency's large_event_fields
	// lists, so the page opens a large event if it counts (R87: Emergency,
	// to an aliased capcode).
	LargeEventField bool
	// Fireground: the fireground channel numbers the page names (R92a), from
	// the network profile, whatever its agency profile.
	Fireground []int32
}

// String returns a text field, or "" if it's null.
func (p Parsed) String(name string) string {
	s, _ := p.Fields[name].(string)
	return s
}

// Parse runs the agency's formats in order (R3). agency nil means text only
// (R4). Network job number patterns apply to every page (R23). ix may be nil.
func Parse(n *profile.Network, agency *profile.Agency, ix *streets.Index, p Prepared) Parsed {
	body := Body(p)
	res := Parsed{Fields: map[string]any{}}
	if agency != nil {
		res.ParseFailed = true
		for _, f := range agency.Formats {
			if f.Priorities != nil && !f.Priorities[p.Priority] {
				continue
			}
			if f.Re == nil {
				res.Format, res.ParseFailed = f.Name, false
				break
			}
			m := f.Re.FindStringSubmatchIndex(body)
			if m == nil {
				continue
			}
			res.Format, res.ParseFailed = f.Name, false
			extract(&res, f, ix, body, m)
			break
		}
	}
	if _, ok := res.Fields["job_number"]; !ok {
		for _, re := range n.JobPatterns {
			if s := re.FindString(body); s != "" {
				res.Fields["job_number"] = s
				break
			}
		}
	}
	res.LargeEventField = agency.OpensLargeEvent(res.Fields)
	res.Fireground = FiregroundChannels(n, p)
	return res
}

func extract(res *Parsed, f profile.Format, ix *streets.Index, body string, m []int) {
	set := func(name, v string) {
		if _, done := res.Fields[name]; !done && v != "" {
			res.Fields[name] = v
		}
	}
	for i, name := range f.Re.SubexpNames() {
		if name == "" || m[2*i] < 0 {
			continue
		}
		v := strings.TrimSpace(body[m[2*i]:m[2*i+1]])
		if as, ok := f.AddrSplit[name]; ok {
			var desc, addr string
			found := false
			if ix != nil {
				desc, addr, found = ix.Split(v)
			}
			if !found {
				desc, addr = v, ""
				res.AddressUnsplit = true
			}
			set(as.Description, desc)
			set(as.Address, addr)
			continue
		}
		if strings.HasPrefix(name, "_") {
			continue
		}
		if re, ok := f.Split[name]; ok {
			if _, done := res.Fields[name]; done {
				continue
			}
			var list, moved []string
			in := f.Into[name]
			for _, part := range re.Split(v, -1) {
				switch part = strings.TrimSpace(part); {
				case part == "":
				case in != nil && in.Moves(part):
					moved = append(moved, part)
				default:
					list = append(list, part)
				}
			}
			if len(list) > 0 {
				res.Fields[name] = list
			}
			// R7, ADR 0047: the tokens into names go to its list, in order,
			// after any that another split field of this format moved there.
			if len(moved) > 0 {
				prev, _ := res.Fields[in.Field].([]string)
				res.Fields[in.Field] = append(prev, moved...)
			}
			continue
		}
		set(name, v)
	}
}

// Category maps an event-type code to its report category; "Other" when
// unmapped. A code ending in * matches by prefix.
func Category(n *profile.Network, code string) string {
	for _, c := range n.Categories {
		for _, pat := range c.Codes {
			if pat == code || (strings.HasSuffix(pat, "*") && strings.HasPrefix(code, strings.TrimSuffix(pat, "*"))) {
				return c.Name
			}
		}
	}
	return "Other"
}

// DeploymentCodes are the network's deployment codes in a page (R5, R87), in
// the order they first appear, each once: the literal text (P3), found in
// the text a pager displays. A network with no pattern has none.
func DeploymentCodes(n *profile.Network, p Prepared) []string {
	if n == nil || n.DeploymentCodes == nil {
		return nil
	}
	var out []string
	for _, m := range n.DeploymentCodes.FindAllStringSubmatch(Body(p), -1) {
		code := strings.TrimSpace(m[n.DeploymentCodeGroup])
		if code != "" && !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	return out
}

// maxFiregroundDigits is the most digits a fireground channel number has
// once its leading zeros are dropped (R92a): a longer one gives no link.
const maxFiregroundDigits = 9

// FiregroundChannels are the fireground channel numbers a page names (R92a),
// found by the network's pattern in the text a pager displays: each match's
// group "number" (else the whole match), when it's all ASCII digits, with
// its leading zeros dropped; each distinct number once, in the order it
// first appears. nil when there are none or the network has no pattern.
func FiregroundChannels(n *profile.Network, p Prepared) []int32 {
	if n == nil || n.Fireground == nil {
		return nil
	}
	var out []int32
	for _, m := range n.Fireground.FindAllStringSubmatch(Body(p), -1) {
		num, ok := channelNumber(m[n.FiregroundGroup])
		if ok && !slices.Contains(out, num) {
			out = append(out, num)
		}
	}
	return out
}

// channelNumber reads a fireground channel number: all ASCII digits, at
// least one, leading zeros ignored ("041" is 41, "000" is 0).
func channelNumber(s string) (int32, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return 0, true
	}
	if len(s) > maxFiregroundDigits {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 32)
	return int32(v), err == nil
}
