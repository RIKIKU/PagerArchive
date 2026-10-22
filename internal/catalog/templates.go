package catalog

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Templates for admins (R28, R64; ADR 0009 §8). The PowerShell module uses
// these now, and M10's template screens later. Templates have no history.

// TemplateInput is a template as an admin creates it.
type TemplateInput struct {
	Name          string  `json:"name"`
	Agency        string  `json:"agency"`
	Colour        *string `json:"colour"`         // null or "": none
	AgencyProfile *string `json:"agency_profile"` // null or "": text only
}

// TemplateChange changes a template's settings; a field left null stays as
// it is, and "" clears the colour or the profile.
type TemplateChange struct {
	Agency        *string `json:"agency"`
	Colour        *string `json:"colour"`
	AgencyProfile *string `json:"agency_profile"`
}

// checkText validates a template's name or agency: 1 to 100 characters, no
// control characters, not starting with a character a spreadsheet would take
// as a formula (as for alias names, ADR 0007 §6).
func checkText(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case n == 0:
		return "", refuse(http.StatusUnprocessableEntity, "bad_"+what, "Give the template %s.", map[string]string{"name": "a name", "agency": "an agency"}[what])
	case n > 100:
		return "", refuse(http.StatusUnprocessableEntity, "bad_"+what, "A template's %s is at most 100 characters.", what)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "", refuse(http.StatusUnprocessableEntity, "bad_"+what, "A template's %s can't contain tabs, line breaks or other control characters.", what)
	case strings.ContainsRune("=+-@", rune(s[0])):
		return "", refuse(http.StatusUnprocessableEntity, "bad_"+what, "A template's %s can't start with =, +, - or @.", what)
	}
	return s, nil
}

// checkColour accepts a CSS colour name or hex value, as imports carry.
func checkColour(c *string) (*string, error) {
	if c == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*c)
	if s == "" {
		return nil, nil
	}
	ok := len(s) <= 32
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '#' && i == 0) {
			ok = false
		}
	}
	if !ok {
		return nil, refuse(http.StatusUnprocessableEntity, "bad_colour", "A colour is a name like red or a hex value like #c00.")
	}
	return &s, nil
}

// checkProfile accepts a loaded agency profile, or none.
func checkProfile(p *string, profiles []string) (*string, error) {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil, nil
	}
	s := strings.TrimSpace(*p)
	if !slices.Contains(profiles, s) {
		return nil, refuse(http.StatusUnprocessableEntity, "unknown_profile", "No agency profile is called %q.", s)
	}
	return &s, nil
}

// CreateTemplate adds a template (R64). profiles are the agency profiles
// loaded at start-up.
func (c *Catalog) CreateTemplate(ctx context.Context, in TemplateInput, profiles []string) (int64, error) {
	name, err := checkText("name", in.Name)
	if err != nil {
		return 0, err
	}
	agency, err := checkText("agency", in.Agency)
	if err != nil {
		return 0, err
	}
	colour, err := checkColour(in.Colour)
	if err != nil {
		return 0, err
	}
	prof, err := checkProfile(in.AgencyProfile, profiles)
	if err != nil {
		return 0, err
	}
	var id int64
	err = c.Store.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.TemplateByName(ctx, name); err == nil {
			return refuse(http.StatusConflict, "template_exists", "There's already a template called %q.", name)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return dbErr("template", err)
		}
		id, err = q.CreateTemplate(ctx, db.CreateTemplateParams{Name: name, Agency: agency, Colour: colour, AgencyProfile: prof})
		if isUniqueViolation(err) {
			return refuse(http.StatusConflict, "template_exists", "There's already a template called %q.", name)
		}
		if err != nil {
			return dbErr("create template", err)
		}
		return nil
	})
	return id, err
}

// EditTemplate changes a template. Every alias using it changes at once
// (R28); a new agency profile re-parses their messages (R23a).
func (c *Catalog) EditTemplate(ctx context.Context, id int64, in TemplateChange, profiles []string) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.TemplateByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(http.StatusNotFound, "no_template", "That template doesn't exist any more.")
		}
		if err != nil {
			return dbErr("template", err)
		}
		next := db.UpdateTemplateParams{ID: id, Agency: cur.Agency, Icon: cur.Icon, Colour: cur.Colour, AgencyProfile: cur.AgencyProfile}
		if in.Agency != nil {
			if next.Agency, err = checkText("agency", *in.Agency); err != nil {
				return err
			}
		}
		if in.Colour != nil {
			if next.Colour, err = checkColour(in.Colour); err != nil {
				return err
			}
		}
		if in.AgencyProfile != nil {
			if next.AgencyProfile, err = checkProfile(in.AgencyProfile, profiles); err != nil {
				return err
			}
		}
		if err := q.UpdateTemplate(ctx, next); err != nil {
			return dbErr("update template", err)
		}
		if deref(next.AgencyProfile) == deref(cur.AgencyProfile) {
			return nil
		}
		patterns, err := q.AliasPatternsForTemplate(ctx, &id)
		if err != nil {
			return dbErr("aliases of template", err)
		}
		for _, p := range patterns {
			if err := reparse(ctx, q, p, "template profile changed"); err != nil {
				return dbErr("request re-parse", err)
			}
		}
		return nil
	})
}

// RemoveTemplate deletes a template that no alias and no waiting request
// uses (R64, ADR 0007 §1). The refusal says how many use it.
func (c *Catalog) RemoveTemplate(ctx context.Context, id int64) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.TemplateByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(http.StatusNotFound, "no_template", "That template doesn't exist any more.")
		}
		if err != nil {
			return dbErr("template", err)
		}
		patterns, err := q.AliasPatternsForTemplate(ctx, &id)
		if err != nil {
			return dbErr("aliases of template", err)
		}
		if n := len(patterns); n > 0 {
			return refuse(http.StatusConflict, "template_in_use", "%q can't be deleted: %d %s it. Move them to another template first.", cur.Name, n, plural(n, "alias uses", "aliases use"))
		}
		waiting, err := q.CountWaitingRequestsForTemplate(ctx, &id)
		if err != nil {
			return dbErr("requests for template", err)
		}
		if waiting > 0 {
			return refuse(http.StatusConflict, "template_in_use", "%q can't be deleted: %d waiting alias %s it. Review them first.", cur.Name, waiting, plural(int(waiting), "request names", "requests name"))
		}
		if _, err := q.DeleteTemplate(ctx, id); err != nil {
			return dbErr("delete template", err)
		}
		return nil
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
