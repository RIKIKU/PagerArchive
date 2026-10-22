// Package search turns R33 search criteria into a database query. The same
// Filter drives the message list and each live-update connection, so a
// viewer's list and their live updates always agree (R32, A4).
package search

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Filter is a set of search criteria. Empty fields mean "any". A field with
// several values matches any of them; different fields must all match
// (R63a, ADR 0009 §8).
type Filter struct {
	Text       string   // case-insensitive substring of the text or an extra field
	Capcode    []string // zero-padded capcodes
	Alias      []string // substrings of the alias name
	AliasLike  []string // case-insensitive LIKE patterns for the alias name
	AliasID    []int64  // messages that resolved to these aliases (R35e)
	Agency     []string // substrings of the template's agency
	AgencyLike []string
	Group      []string // exact, case-insensitive
	GroupLike  []string
	Priority   []string // Emergency, Non-emergency, Admin, or none
	Category   []string // network event categories, or Other
	Job        []string
	From, To   *time.Time // sent time, From inclusive, To exclusive
	Unaliased  bool       // "capcodes with no alias"
	Ignored    bool       // include pages to ignored aliases (R27)
	Located    bool       // only messages placed on the map (R35)

	// Admin only (R33): filter by flag. Each true field must be set; Flags
	// matches messages with any of the flags it names.
	ParseFailed, AddressUnsplit, LocationMismatch bool
	Flags                                         []string
}

// Flags are the flags admins can filter by (R33).
var Flags = []string{"parse_failed", "address_unsplit", "location_mismatch"}

// MaxValues caps how many values one parameter may repeat.
const MaxValues = 500

// listParams are the parameters that may repeat, and the field each fills.
func (f *Filter) listParams() map[string]*[]string {
	return map[string]*[]string{
		"capcode": &f.Capcode, "alias": &f.Alias, "alias_like": &f.AliasLike, "agency": &f.Agency, "agency_like": &f.AgencyLike,
		"group": &f.Group, "group_like": &f.GroupLike, "priority": &f.Priority, "category": &f.Category, "job": &f.Job, "flag": &f.Flags,
	}
}

// values reads a repeated parameter, trimmed, without empty values.
func values(v url.Values, key string) ([]string, error) {
	var out []string
	for _, s := range v[key] {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > MaxValues {
		return nil, fmt.Errorf("%s: at most %d values", key, MaxValues)
	}
	return out, nil
}

// Parse reads a Filter from query parameters. Admin-only flags are ignored
// unless admin is true. loc is the deployment time zone for date-only
// values (R35a).
func Parse(v url.Values, admin bool, loc *time.Location) (Filter, error) {
	f := Filter{Text: strings.TrimSpace(v.Get("q"))}
	for key, dst := range f.listParams() {
		vs, err := values(v, key)
		if err != nil {
			return f, err
		}
		*dst = vs
	}
	ids, err := values(v, "alias_id")
	if err != nil {
		return f, err
	}
	for _, s := range ids {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return f, errors.New("alias_id must be a number")
		}
		f.AliasID = append(f.AliasID, id)
	}
	for i, c := range f.Capcode {
		p, err := catalog.NormalisePattern(c)
		if err != nil || strings.ContainsAny(p, "%_") {
			return f, errors.New("capcode must be 1 to 7 digits")
		}
		f.Capcode[i] = p
	}
	for _, p := range f.Priority {
		switch p {
		case "Emergency", "Non-emergency", "Admin", "none":
		default:
			return f, fmt.Errorf("unknown priority %q", p)
		}
	}
	for _, fl := range f.Flags {
		if !slices.Contains(Flags, fl) {
			return f, fmt.Errorf("unknown flag %q", fl)
		}
	}
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
	} else {
		f.Flags = nil
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
	if f.Text != "" {
		v.Set("q", f.Text)
	}
	for k, vs := range f.listParams() {
		for _, s := range *vs {
			v.Add(k, s)
		}
	}
	for _, id := range f.AliasID {
		v.Add("alias_id", strconv.FormatInt(id, 10))
	}
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

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// likeContains escapes each value for ILIKE and wraps it in %…%.
func likeContains(vs []string) []string {
	out := make([]string, 0, len(vs))
	for _, s := range vs {
		out = append(out, "%"+likeEscaper.Replace(s)+"%")
	}
	return out
}

func upper(vs []string) []string {
	out := make([]string, 0, len(vs))
	for _, s := range vs {
		out = append(out, strings.ToUpper(s))
	}
	return out
}

// orEmpty passes an empty array, not NULL, for "any".
func orEmpty[T any](vs []T) []T {
	if vs == nil {
		return []T{}
	}
	return vs
}

// Cursor is a keyset position: messages strictly older than it.
type Cursor struct {
	SentAt time.Time
	ID     int64
}

// categoryCodes adds a category's event codes to codes, and its prefixes
// (codes ending in *) to prefixes as LIKE patterns.
func categoryCodes(c profile.Category, codes, prefixes *[]string) {
	for _, code := range c.Codes {
		if prefix, ok := strings.CutSuffix(code, "*"); ok {
			*prefixes = append(*prefixes, likeEscaper.Replace(prefix)+"%")
		} else {
			*codes = append(*codes, code)
		}
	}
}

// Params builds the database query. n is the page size.
func (f Filter) Params(n *profile.Network, before *Cursor, limit int) (db.SearchMessagesParams, error) {
	p := db.SearchMessagesParams{
		Capcodes:         orEmpty(f.Capcode),
		Aliases:          likeContains(f.Alias),
		AliasLikes:       orEmpty(f.AliasLike),
		AliasIds:         orEmpty(f.AliasID),
		Agencies:         likeContains(f.Agency),
		AgencyLikes:      orEmpty(f.AgencyLike),
		PagingGroups:     upper(f.Group),
		GroupLikes:       orEmpty(f.GroupLike),
		Priorities:       orEmpty(f.Priority),
		JobNumbers:       orEmpty(f.Job),
		SentFrom:         f.From,
		SentTo:           f.To,
		Unaliased:        f.Unaliased,
		IncludeIgnored:   f.Ignored,
		ParseFailed:      f.ParseFailed,
		AddressUnsplit:   f.AddressUnsplit,
		LocationMismatch: f.LocationMismatch,
		Flags:            orEmpty(f.Flags),
		Located:          f.Located,
		CatCodes:         []string{},
		CatPrefixes:      []string{},
		AllCatCodes:      []string{},
		AllCatPrefixes:   []string{},
		MaxRows:          int32(limit),
	}
	if f.Text != "" {
		q := "%" + likeEscaper.Replace(f.Text) + "%"
		p.Q = &q
	}
	for _, name := range f.Category {
		if name == "Other" {
			p.CatOther = true
			continue
		}
		i := slices.IndexFunc(n.Categories, func(c profile.Category) bool { return c.Name == name })
		if i < 0 {
			return p, fmt.Errorf("unknown category %q", name)
		}
		categoryCodes(n.Categories[i], &p.CatCodes, &p.CatPrefixes)
	}
	if len(f.Category) > 0 {
		p.CatFilter = true
		for _, c := range n.Categories {
			categoryCodes(c, &p.AllCatCodes, &p.AllCatPrefixes)
		}
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
