// Package search turns R33 search criteria into a database query. The same
// Filter drives the message list and each live-update connection, so a
// viewer's list and their live updates always agree (R32, A4).
package search

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Filter is a set of search criteria. Empty fields mean "any".
type Filter struct {
	Text      string // case-insensitive substring of the text or an extra field
	Capcode   string
	Alias     string // substring of the alias name
	Agency    string // substring of the template's agency
	Group     string // exact, case-insensitive
	Priority  string // Emergency, Non-emergency, Admin, or none
	Category  string // a network event category, or Other
	Job       string
	From, To  *time.Time // sent time, From inclusive, To exclusive
	Unaliased bool       // "capcodes with no alias"
	Ignored   bool       // include pages to ignored aliases (R27)

	// Admin only (R33): filter by flag.
	ParseFailed, AddressUnsplit, LocationMismatch bool
}

// Parse reads a Filter from query parameters. Admin-only flags are ignored
// unless admin is true. loc is the deployment time zone for date-only
// values (R35a).
func Parse(v url.Values, admin bool, loc *time.Location) (Filter, error) {
	f := Filter{
		Text:     strings.TrimSpace(v.Get("q")),
		Alias:    strings.TrimSpace(v.Get("alias")),
		Agency:   strings.TrimSpace(v.Get("agency")),
		Group:    strings.TrimSpace(v.Get("group")),
		Priority: v.Get("priority"),
		Category: v.Get("category"),
		Job:      strings.TrimSpace(v.Get("job")),
	}
	if c := strings.TrimSpace(v.Get("capcode")); c != "" {
		p, err := catalog.NormalisePattern(c)
		if err != nil || strings.ContainsAny(p, "%_") {
			return f, errors.New("capcode must be 1 to 7 digits")
		}
		f.Capcode = p
	}
	switch f.Priority {
	case "", "Emergency", "Non-emergency", "Admin", "none":
	default:
		return f, fmt.Errorf("unknown priority %q", f.Priority)
	}
	var err error
	if f.From, err = parseTime(v.Get("from"), loc, false); err != nil {
		return f, err
	}
	if f.To, err = parseTime(v.Get("to"), loc, true); err != nil {
		return f, err
	}
	f.Unaliased = v.Get("unaliased") == "true"
	f.Ignored = v.Get("ignored") == "true"
	if admin {
		f.ParseFailed = v.Get("parse_failed") == "true"
		f.AddressUnsplit = v.Get("address_unsplit") == "true"
		f.LocationMismatch = v.Get("location_mismatch") == "true"
	}
	return f, nil
}

// parseTime accepts RFC 3339 or a date (YYYY-MM-DD) in loc. A date used as
// the end of a range includes that whole day (section 14: inclusive ranges).
func parseTime(s string, loc *time.Location, end bool) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q", s)
	}
	if end {
		d = d.AddDate(0, 0, 1)
	}
	return &d, nil
}

// Values encodes the filter as query parameters (inverse of Parse).
func (f Filter) Values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("q", f.Text)
	set("capcode", f.Capcode)
	set("alias", f.Alias)
	set("agency", f.Agency)
	set("group", f.Group)
	set("priority", f.Priority)
	set("category", f.Category)
	set("job", f.Job)
	if f.From != nil {
		v.Set("from", f.From.Format(time.RFC3339))
	}
	if f.To != nil {
		v.Set("to", f.To.Format(time.RFC3339))
	}
	for k, b := range map[string]bool{"unaliased": f.Unaliased, "ignored": f.Ignored, "parse_failed": f.ParseFailed, "address_unsplit": f.AddressUnsplit, "location_mismatch": f.LocationMismatch} {
		if b {
			v.Set(k, "true")
		}
	}
	return v
}

// likeContains escapes s for ILIKE and wraps it in %…%.
func likeContains(s string) *string {
	if s == "" {
		return nil
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	r = "%" + r + "%"
	return &r
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Cursor is a keyset position: messages strictly older than it.
type Cursor struct {
	SentAt time.Time
	ID     int64
}

// Params builds the database query. n is the page size.
func (f Filter) Params(n *profile.Network, before *Cursor, limit int) (db.SearchMessagesParams, error) {
	p := db.SearchMessagesParams{
		Q:                likeContains(f.Text),
		Capcode:          ptr(f.Capcode),
		Alias:            likeContains(f.Alias),
		Agency:           likeContains(f.Agency),
		PagingGroup:      ptr(f.Group),
		Priority:         ptr(f.Priority),
		JobNumber:        ptr(f.Job),
		SentFrom:         f.From,
		SentTo:           f.To,
		Unaliased:        f.Unaliased,
		IncludeIgnored:   f.Ignored,
		ParseFailed:      f.ParseFailed,
		AddressUnsplit:   f.AddressUnsplit,
		LocationMismatch: f.LocationMismatch,
		CatCodes:         []string{},
		CatPrefixes:      []string{},
		MaxRows:          int32(limit),
	}
	if f.Category != "" {
		want := f.Category != "Other"
		found := !want
		for _, c := range n.Categories {
			if want && c.Name != f.Category {
				continue
			}
			found = true
			for _, code := range c.Codes {
				if prefix, ok := strings.CutSuffix(code, "*"); ok {
					p.CatPrefixes = append(p.CatPrefixes, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)+"%")
				} else {
					p.CatCodes = append(p.CatCodes, code)
				}
			}
		}
		if !found {
			return p, fmt.Errorf("unknown category %q", f.Category)
		}
		p.CatWant = &want
	}
	if before != nil {
		p.BeforeSent, p.BeforeID = &before.SentAt, &before.ID
	}
	return p, nil
}

// EncodeCursor and DecodeCursor make an opaque page token.
func EncodeCursor(c Cursor) string {
	return strconv.FormatInt(c.SentAt.UnixMicro(), 36) + "." + strconv.FormatInt(c.ID, 36)
}

func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	a, b, ok := strings.Cut(s, ".")
	us, err1 := strconv.ParseInt(a, 36, 64)
	id, err2 := strconv.ParseInt(b, 36, 64)
	if !ok || err1 != nil || err2 != nil {
		return nil, errors.New("invalid cursor")
	}
	return &Cursor{SentAt: time.UnixMicro(us).UTC(), ID: id}, nil
}
