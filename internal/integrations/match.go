package integrations

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Matches reports whether a stored message passes the filter (R49). The
// selection is checked by the database before this. Categories come from
// the same function the display uses, so an integration and the message
// list agree. admin is the owner's role: flag criteria apply to admins only.
func (f Filter) Matches(m db.SearchMessagesRow, n *profile.Network, admin bool) bool {
	if len(f.Categories) > 0 || len(f.Codes) > 0 {
		ok := false
		if m.EventType != nil {
			ok = slices.Contains(f.Codes, *m.EventType) || slices.Contains(f.Categories, pipeline.Category(n, *m.EventType))
		} else {
			// No event type counts as Other, as in reports (R38).
			ok = slices.Contains(f.Categories, "Other")
		}
		if !ok {
			return false
		}
	}
	if len(f.Priorities) > 0 {
		p := "none"
		if m.Priority != nil {
			p = *m.Priority
		}
		if !slices.Contains(f.Priorities, p) {
			return false
		}
	}
	if f.Text != "" && !textContains(m, f.Text) {
		return false
	}
	if f.Group != "" && (m.PagingGroup == nil || !strings.EqualFold(*m.PagingGroup, f.Group)) {
		return false
	}
	if f.Agency != "" && (m.Agency == nil || !containsFold(*m.Agency, f.Agency)) {
		return false
	}
	if f.Job != "" && (m.JobNumber == nil || *m.JobNumber != f.Job) {
		return false
	}
	if admin {
		if (f.ParseFailed && !m.ParseFailed) || (f.AddressUnsplit && !m.AddressUnsplit) || (f.LocationMismatch && !m.LocationMismatch) {
			return false
		}
	}
	return true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// textContains is search's text criterion (section 14): a case-insensitive
// substring of the text or of an extra field.
func textContains(m db.SearchMessagesRow, q string) bool {
	if containsFold(m.Text, q) {
		return true
	}
	var extra map[string]any
	_ = json.Unmarshal(m.Extra, &extra)
	for _, v := range extra {
		switch x := v.(type) {
		case string:
			if containsFold(x, q) {
				return true
			}
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok && containsFold(s, q) {
					return true
				}
			}
		}
	}
	return false
}
