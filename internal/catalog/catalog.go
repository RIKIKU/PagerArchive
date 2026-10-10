// Package catalog changes aliases and templates (R27, R28). Every change
// queues a re-parse of the affected messages in the same transaction
// (R23a), whichever tool made it: CLI, import or admin UI.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Template is a template's settings.
type Template struct {
	Name, Agency  string
	Icon, Colour  *string
	AgencyProfile *string // nil: text only
}

// Alias is an alias's settings.
type Alias struct {
	Pattern, Name, Template string
	Ignored                 bool
}

// Catalog changes aliases and templates.
type Catalog struct {
	Store *store.Store
}

var patternRe = regexp.MustCompile(`^[0-9%_]{1,7}$`)

// NormalisePattern validates a capcode pattern and zero-pads exact capcodes.
func NormalisePattern(p string) (string, error) {
	if !patternRe.MatchString(p) {
		return "", fmt.Errorf("pattern %q: use 1 to 7 digits, %% and _", p)
	}
	if !strings.ContainsAny(p, "%_") {
		p = decoder.PadCapcode(p)
	}
	return p, nil
}

// ErrNotFound means the named alias or template doesn't exist.
var ErrNotFound = errors.New("not found")

func reparse(ctx context.Context, q *db.Queries, pattern, reason string) error {
	return q.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: pattern, Reason: reason})
}

func dbErr(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return fmt.Errorf("%s: %s", op, store.SafeError(err))
}

// AddTemplate creates a template.
func (c *Catalog) AddTemplate(ctx context.Context, t Template) error {
	if t.Name == "" || t.Agency == "" {
		return errors.New("a template needs a name and an agency")
	}
	_, err := c.Store.CreateTemplate(ctx, db.CreateTemplateParams{Name: t.Name, Agency: t.Agency, Icon: t.Icon, Colour: t.Colour, AgencyProfile: t.AgencyProfile})
	if err != nil {
		return dbErr("add template "+t.Name, err)
	}
	return nil
}

// UpdateTemplate changes a template and re-parses every alias that uses it.
func (c *Catalog) UpdateTemplate(ctx context.Context, t Template) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.TemplateByName(ctx, t.Name)
		if err != nil {
			return dbErr("template "+t.Name, err)
		}
		if err := q.UpdateTemplate(ctx, db.UpdateTemplateParams{ID: cur.ID, Agency: t.Agency, Icon: t.Icon, Colour: t.Colour, AgencyProfile: t.AgencyProfile}); err != nil {
			return dbErr("update template", err)
		}
		patterns, err := q.AliasPatternsForTemplate(ctx, cur.ID)
		if err != nil {
			return dbErr("aliases of template", err)
		}
		for _, p := range patterns {
			if err := reparse(ctx, q, p, "template changed"); err != nil {
				return dbErr("request re-parse", err)
			}
		}
		return nil
	})
}

// DeleteTemplate deletes a template that no alias uses (section 14).
func (c *Catalog) DeleteTemplate(ctx context.Context, name string) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.TemplateByName(ctx, name)
		if err != nil {
			return dbErr("template "+name, err)
		}
		patterns, err := q.AliasPatternsForTemplate(ctx, cur.ID)
		if err != nil {
			return dbErr("aliases of template", err)
		}
		if len(patterns) > 0 {
			return fmt.Errorf("template %s is used by %d aliases", name, len(patterns))
		}
		if _, err := q.DeleteTemplate(ctx, cur.ID); err != nil {
			return dbErr("delete template", err)
		}
		return nil
	})
}

// AddAlias creates an alias and re-parses the messages it matches.
func (c *Catalog) AddAlias(ctx context.Context, a Alias) error {
	pattern, err := NormalisePattern(a.Pattern)
	if err != nil {
		return err
	}
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		tpl, err := q.TemplateByName(ctx, a.Template)
		if err != nil {
			return dbErr("template "+a.Template, err)
		}
		if _, err := q.CreateAlias(ctx, db.CreateAliasParams{Pattern: pattern, Name: a.Name, TemplateID: tpl.ID, Ignored: a.Ignored}); err != nil {
			return dbErr("add alias "+pattern, err)
		}
		return reparse(ctx, q, pattern, "alias added")
	})
}

// UpdateAlias changes an alias's name, template or ignored flag.
func (c *Catalog) UpdateAlias(ctx context.Context, a Alias) error {
	pattern, err := NormalisePattern(a.Pattern)
	if err != nil {
		return err
	}
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.AliasByPattern(ctx, pattern)
		if err != nil {
			return dbErr("alias "+pattern, err)
		}
		tpl, err := q.TemplateByName(ctx, a.Template)
		if err != nil {
			return dbErr("template "+a.Template, err)
		}
		if err := q.UpdateAlias(ctx, db.UpdateAliasParams{ID: cur.ID, Name: a.Name, TemplateID: tpl.ID, Ignored: a.Ignored}); err != nil {
			return dbErr("update alias", err)
		}
		if tpl.ID == cur.TemplateID {
			return nil // name and ignored flag are read through the join
		}
		return reparse(ctx, q, pattern, "alias template changed")
	})
}

// DeleteAlias deletes an alias; its messages are re-resolved to whatever
// alias now matches them, if any.
func (c *Catalog) DeleteAlias(ctx context.Context, pattern string) error {
	pattern, err := NormalisePattern(pattern)
	if err != nil {
		return err
	}
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.AliasByPattern(ctx, pattern)
		if err != nil {
			return dbErr("alias "+pattern, err)
		}
		if _, err := q.DeleteAlias(ctx, cur.ID); err != nil {
			return dbErr("delete alias", err)
		}
		return reparse(ctx, q, pattern, "alias deleted")
	})
}
