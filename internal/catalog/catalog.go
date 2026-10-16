// Package catalog changes aliases and templates (R27, R28). Every change
// queues a re-parse of the affected messages in the same transaction
// (R23a), and every alias change writes its history (R57), whichever tool
// made it: the command line, the import, an admin, or an approved request.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// Alias is an alias's settings, with its template by name.
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
	if !IsRange(p) {
		p = decoder.PadCapcode(p)
	}
	return p, nil
}

// IsRange reports whether a pattern has wildcards (a range alias).
func IsRange(p string) bool { return strings.ContainsAny(p, "%_") }

// ErrNotFound means the named alias or template doesn't exist.
var ErrNotFound = errors.New("not found")

// Refusal is a change refused for a reason the person can act on. Msg is
// shown to them; it may name aliases, so it's never logged.
type Refusal struct {
	Status int
	Code   string
	Msg    string
}

func (e *Refusal) Error() string { return e.Msg }

func refuse(status int, code, format string, args ...any) error {
	return &Refusal{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Sources of a change, as history records them (R57).
const (
	SourceAdmin   = "admin"
	SourceRequest = "request"
	SourceCLI     = "cli"
	SourceImport  = "import"
)

// By says who made a change: the source, the user who made or approved it,
// and for an approved request, who asked and which request.
type By struct {
	Source      string
	UserID      *int64
	RequestedBy *int64
	RequestID   *int64
}

// CLI is a change made with the command line.
var CLI = By{Source: SourceCLI}

func reparse(ctx context.Context, q *db.Queries, pattern, reason string) error {
	return q.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: pattern, Reason: reason})
}

// reparses collects the capcodes and patterns a transaction's alias
// changes need re-parsed (R23a), so a caller queues each once.
type reparses map[string]string // pattern -> reason

func (r reparses) add(pattern, reason string) {
	if _, ok := r[pattern]; !ok {
		r[pattern] = reason
	}
}

func (r reparses) queue(ctx context.Context, q *db.Queries) error {
	for p, reason := range r {
		if err := reparse(ctx, q, p, reason); err != nil {
			return dbErr("request re-parse", err)
		}
	}
	return nil
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
		patterns, err := q.AliasPatternsForTemplate(ctx, &cur.ID)
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

// DeleteTemplate deletes a template that no alias and no waiting request
// uses (section 14, ADR 0007 §1).
func (c *Catalog) DeleteTemplate(ctx context.Context, name string) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.TemplateByName(ctx, name)
		if err != nil {
			return dbErr("template "+name, err)
		}
		patterns, err := q.AliasPatternsForTemplate(ctx, &cur.ID)
		if err != nil {
			return dbErr("aliases of template", err)
		}
		if len(patterns) > 0 {
			return fmt.Errorf("template %s is used by %d aliases", name, len(patterns))
		}
		waiting, err := q.CountWaitingRequestsForTemplate(ctx, &cur.ID)
		if err != nil {
			return dbErr("requests for template", err)
		}
		if waiting > 0 {
			return fmt.Errorf("template %s is named by %d waiting alias requests; review them first", name, waiting)
		}
		if _, err := q.DeleteTemplate(ctx, cur.ID); err != nil {
			return dbErr("delete template", err)
		}
		return nil
	})
}

// templateID looks up a template by name; "" means no template (R30:
// an alias without an agency still has a name).
func templateID(ctx context.Context, q *db.Queries, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	tpl, err := q.TemplateByName(ctx, name)
	if err != nil {
		return nil, dbErr("template "+name, err)
	}
	return &tpl.ID, nil
}

// templateName looks up a template by id for the browser's requests and
// edits; nil means no template.
func templateName(ctx context.Context, q *db.Queries, id *int64) (*string, error) {
	if id == nil {
		return nil, nil
	}
	t, err := q.TemplateByID(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, refuse(http.StatusUnprocessableEntity, "no_template", "That template doesn't exist any more. Choose another.")
	}
	if err != nil {
		return nil, dbErr("template", err)
	}
	return &t.Name, nil
}

// settings are an alias's stored settings, with its template's name for
// history.
type settings struct {
	pattern string
	name    string
	tplID   *int64
	tplName *string
	ignored bool
}

// change is a set of fields to change; nil (or setTemplate false) leaves a
// field as it is.
type change struct {
	pattern     *string
	name        *string
	setTemplate bool
	tplID       *int64
	tplName     *string
	ignored     *bool
}

// fieldChanges is history's "what changed": field -> [before, after].
type fieldChanges map[string][2]any

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func sameID(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func record(ctx context.Context, q *db.Queries, aliasID int64, key, name, action string, ch fieldChanges, by By) error {
	js, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	if by.Source == "" {
		return errors.New("history: no source")
	}
	_, err = q.InsertAliasChange(ctx, db.InsertAliasChangeParams{AliasID: aliasID, PatternKey: key, AliasName: name, Action: action,
		Changes: js, Source: by.Source, MadeBy: by.UserID, RequestedBy: by.RequestedBy, RequestID: by.RequestID})
	if err != nil {
		return dbErr("record history", err)
	}
	return nil
}

// addAlias creates an alias inside a transaction, records it and queues
// a re-parse. The pattern must already be normalised.
func addAlias(ctx context.Context, q *db.Queries, s settings, by By, rp reparses) (int64, error) {
	if cur, err := q.AliasByKey(ctx, s.pattern); err == nil {
		return 0, &DuplicateAliasError{Pattern: s.pattern, Existing: cur.Name, ExistingID: cur.ID}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, dbErr("alias "+s.pattern, err)
	}
	id, err := q.CreateAlias(ctx, db.CreateAliasParams{Pattern: s.pattern, Name: s.name, TemplateID: s.tplID, Ignored: s.ignored})
	if err != nil {
		if isUniqueViolation(err) {
			return 0, errRaced
		}
		return 0, dbErr("add alias "+s.pattern, err)
	}
	ch := fieldChanges{"pattern": {nil, s.pattern}, "name": {nil, s.name}, "template": {nil, strOrNil(s.tplName)}, "ignored": {nil, s.ignored}}
	if err := record(ctx, q, id, s.pattern, s.name, "added", ch, by); err != nil {
		return 0, err
	}
	rp.add(s.pattern, "alias added")
	return id, nil
}

// lockAlias reads an alias for a change, locking its row.
func lockAlias(ctx context.Context, q *db.Queries, id int64) (settings, error) {
	cur, err := q.AliasForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings{}, refuse(http.StatusNotFound, "no_alias", "That alias doesn't exist any more.")
	}
	if err != nil {
		return settings{}, dbErr("alias", err)
	}
	return settings{pattern: cur.Pattern, name: cur.Name, tplID: cur.TemplateID, tplName: cur.TemplateName, ignored: cur.Ignored}, nil
}

// changeAlias applies a change to an alias inside a transaction: only the
// fields set, exactly as given. It records what actually changed (nothing,
// if nothing did) and queues re-parsing when the template or capcode
// changed (R23a).
func changeAlias(ctx context.Context, q *db.Queries, id int64, c change, by By, rp reparses) error {
	cur, err := lockAlias(ctx, q, id)
	if err != nil {
		return err
	}
	next := cur
	ch := fieldChanges{}
	if c.pattern != nil && *c.pattern != cur.pattern {
		if other, err := q.AliasByKey(ctx, *c.pattern); err == nil && other.ID != id {
			return &DuplicateAliasError{Pattern: *c.pattern, Existing: other.Name, ExistingID: other.ID}
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return dbErr("alias", err)
		}
		next.pattern = *c.pattern
		ch["pattern"] = [2]any{cur.pattern, next.pattern}
	}
	if c.name != nil && *c.name != cur.name {
		next.name = *c.name
		ch["name"] = [2]any{cur.name, next.name}
	}
	if c.setTemplate && !sameID(c.tplID, cur.tplID) {
		next.tplID, next.tplName = c.tplID, c.tplName
		ch["template"] = [2]any{strOrNil(cur.tplName), strOrNil(next.tplName)}
	}
	if c.ignored != nil && *c.ignored != cur.ignored {
		next.ignored = *c.ignored
		ch["ignored"] = [2]any{cur.ignored, next.ignored}
	}
	if len(ch) == 0 {
		return nil
	}
	if err := q.SetAlias(ctx, db.SetAliasParams{ID: id, Pattern: next.pattern, Name: next.name, TemplateID: next.tplID, Ignored: next.ignored}); err != nil {
		if isUniqueViolation(err) {
			return errRaced
		}
		return dbErr("update alias", err)
	}
	if err := record(ctx, q, id, next.pattern, next.name, "changed", ch, by); err != nil {
		return err
	}
	// Names and the ignored flag are read through the join; the template
	// and capcode decide parsing.
	if _, ok := ch["pattern"]; ok {
		rp.add(cur.pattern, "alias capcode changed")
		rp.add(next.pattern, "alias capcode changed")
	} else if _, ok := ch["template"]; ok {
		rp.add(next.pattern, "alias template changed")
	}
	return nil
}

// Deleted says what deleting an alias touched.
type Deleted struct {
	Integrations int64 `json:"integrations"` // integrations that selected it
	Requests     int64 `json:"requests"`     // waiting requests closed as "alias deleted"
}

// deleteAlias deletes an alias inside a transaction. Its waiting requests
// close as "alias deleted" (R55), integrations that selected a one-capcode
// alias select the capcode instead (owner, 2026-10-16), the deletion is
// recorded, and its messages are re-resolved to whatever alias now covers
// them.
func deleteAlias(ctx context.Context, q *db.Queries, id int64, by By, rp reparses) (Deleted, error) {
	var d Deleted
	cur, err := lockAlias(ctx, q, id)
	if err != nil {
		return d, err
	}
	if d.Integrations, err = q.CountIntegrationsSelectingAlias(ctx, id); err != nil {
		return d, dbErr("integrations of alias", err)
	}
	if !IsRange(cur.pattern) && d.Integrations > 0 {
		if err := q.SelectCapcodeInsteadOfAlias(ctx, db.SelectCapcodeInsteadOfAliasParams{Capcode: cur.pattern, AliasID: id}); err != nil {
			return d, dbErr("keep capcode in integrations", err)
		}
	}
	if d.Requests, err = q.CloseRequestsOfAlias(ctx, &id); err != nil {
		return d, dbErr("close requests", err)
	}
	ch := fieldChanges{"pattern": {cur.pattern, nil}, "name": {cur.name, nil}, "template": {strOrNil(cur.tplName), nil}, "ignored": {cur.ignored, nil}}
	if err := record(ctx, q, id, cur.pattern, cur.name, "deleted", ch, by); err != nil {
		return d, err
	}
	if _, err := q.DeleteAlias(ctx, id); err != nil {
		return d, dbErr("delete alias", err)
	}
	rp.add(cur.pattern, "alias deleted")
	return d, nil
}

// AddAlias creates an alias from the command line (template by name).
func (c *Catalog) AddAlias(ctx context.Context, a Alias) error {
	pattern, err := NormalisePattern(a.Pattern)
	if err != nil {
		return err
	}
	err = c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		tid, err := templateID(ctx, q, a.Template)
		if err != nil {
			return err
		}
		_, err = addAlias(ctx, q, settings{pattern: pattern, name: a.Name, tplID: tid, tplName: optional(a.Template), ignored: a.Ignored}, CLI, rp)
		return err
	})
	return c.raced(ctx, pattern, err)
}

// raced turns a unique violation that another writer caused meanwhile into
// the usual duplicate error (R27a).
func (c *Catalog) raced(ctx context.Context, pattern string, err error) error {
	if !errors.Is(err, errRaced) {
		return err
	}
	if cur, lookupErr := c.Store.AliasByKey(ctx, pattern); lookupErr == nil {
		return &DuplicateAliasError{Pattern: pattern, Existing: cur.Name, ExistingID: cur.ID}
	}
	return fmt.Errorf("%s already has an alias (R27a)", pattern)
}

var errRaced = errors.New("unique violation")

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// DuplicateAliasError rejects a second alias for a capcode or pattern (R27a).
type DuplicateAliasError struct {
	Pattern, Existing string
	ExistingID        int64
}

func (e *DuplicateAliasError) Error() string {
	return fmt.Sprintf("%s already has the alias %q, and each capcode or pattern has one alias (R27a); edit it instead: pagerarchive alias set %s <name> [--template T]",
		e.Pattern, e.Existing, e.Pattern)
}

// ForAdmin is the refusal an admin sees in the browser (R53, A42).
func (e *DuplicateAliasError) ForAdmin() *Refusal {
	return &Refusal{Status: http.StatusConflict, Code: "duplicate_alias",
		Msg: fmt.Sprintf("%s already has the alias %q. Each capcode or pattern has one alias: edit %q instead.", e.Pattern, e.Existing, e.Existing)}
}

// GetAlias returns an alias's current settings.
func (c *Catalog) GetAlias(ctx context.Context, pattern string) (Alias, error) {
	pattern, err := NormalisePattern(pattern)
	if err != nil {
		return Alias{}, err
	}
	cur, err := c.Store.AliasDetailByKey(ctx, pattern)
	if err != nil {
		return Alias{}, dbErr("alias "+pattern, err)
	}
	a := Alias{Pattern: pattern, Name: cur.Name, Ignored: cur.Ignored}
	if cur.TemplateName != nil {
		a.Template = *cur.TemplateName
	}
	return a, nil
}

// UpdateAlias changes an alias's name, template or ignored flag from the
// command line.
func (c *Catalog) UpdateAlias(ctx context.Context, a Alias) error {
	pattern, err := NormalisePattern(a.Pattern)
	if err != nil {
		return err
	}
	return c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		cur, err := q.AliasByPattern(ctx, pattern)
		if err != nil {
			return dbErr("alias "+pattern, err)
		}
		tid, err := templateID(ctx, q, a.Template)
		if err != nil {
			return err
		}
		return changeAlias(ctx, q, cur.ID, change{name: &a.Name, setTemplate: true, tplID: tid, tplName: optional(a.Template), ignored: &a.Ignored}, CLI, rp)
	})
}

// DeleteAlias deletes an alias from the command line.
func (c *Catalog) DeleteAlias(ctx context.Context, pattern string) error {
	pattern, err := NormalisePattern(pattern)
	if err != nil {
		return err
	}
	return c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		cur, err := q.AliasByPattern(ctx, pattern)
		if err != nil {
			return dbErr("alias "+pattern, err)
		}
		_, err = deleteAlias(ctx, q, cur.ID, CLI, rp)
		return err
	})
}

// AliasInput is an alias as an admin enters it in the browser (R53).
type AliasInput struct {
	Pattern    string `json:"pattern"`
	Name       string `json:"name"`
	TemplateID *int64 `json:"template_id"` // null: no template (text only)
	Ignored    bool   `json:"ignored"`
}

// CheckName validates an alias name entered in the browser: 1 to 100
// characters, no control characters, and not starting with a character a
// spreadsheet would take as a formula (ADR 0007 §2, §6).
func CheckName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return "", refuse(http.StatusUnprocessableEntity, "bad_name", "Give the alias a name.")
	case n > 100:
		return "", refuse(http.StatusUnprocessableEntity, "bad_name", "An alias name is at most 100 characters.")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", refuse(http.StatusUnprocessableEntity, "bad_name", "An alias name can't contain tabs, line breaks or other control characters.")
	case strings.ContainsRune("=+-@", rune(name[0])):
		return "", refuse(http.StatusUnprocessableEntity, "bad_name", "An alias name can't start with =, +, - or @.")
	}
	return name, nil
}

func checkPattern(p string) (string, error) {
	pattern, err := NormalisePattern(strings.TrimSpace(p))
	if err != nil {
		return "", refuse(http.StatusUnprocessableEntity, "bad_pattern",
			"A capcode is 1 to 7 digits; a pattern uses digits with %% (any run of characters) and _ (one character), 7 characters at most.")
	}
	return pattern, nil
}

// CreateAlias adds an alias for an admin (R53). It takes effect at once.
func (c *Catalog) CreateAlias(ctx context.Context, in AliasInput, by By) (int64, error) {
	pattern, err := checkPattern(in.Pattern)
	if err != nil {
		return 0, err
	}
	name, err := CheckName(in.Name)
	if err != nil {
		return 0, err
	}
	var id int64
	err = c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		tplName, err := templateName(ctx, q, in.TemplateID)
		if err != nil {
			return err
		}
		id, err = addAlias(ctx, q, settings{pattern: pattern, name: name, tplID: in.TemplateID, tplName: tplName, ignored: in.Ignored}, by, rp)
		return err
	})
	return id, forAdmin(c.raced(ctx, pattern, err))
}

// EditAlias sets an alias's capcode or pattern, name, template and ignored
// flag for an admin (R53).
func (c *Catalog) EditAlias(ctx context.Context, id int64, in AliasInput, by By) error {
	pattern, err := checkPattern(in.Pattern)
	if err != nil {
		return err
	}
	name, err := CheckName(in.Name)
	if err != nil {
		return err
	}
	err = c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		tplName, err := templateName(ctx, q, in.TemplateID)
		if err != nil {
			return err
		}
		return changeAlias(ctx, q, id, change{pattern: &pattern, name: &name, setTemplate: true, tplID: in.TemplateID, tplName: tplName, ignored: &in.Ignored}, by, rp)
	})
	return forAdmin(c.raced(ctx, pattern, err))
}

// RemoveAlias deletes an alias for an admin (R53, R55).
func (c *Catalog) RemoveAlias(ctx context.Context, id int64, by By) (Deleted, error) {
	var d Deleted
	err := c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		var err error
		d, err = deleteAlias(ctx, q, id, by, rp)
		return err
	})
	return d, err
}

func forAdmin(err error) error {
	var dup *DuplicateAliasError
	if errors.As(err, &dup) {
		return dup.ForAdmin()
	}
	return err
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// inTx runs fn in a transaction and then queues the re-parses its alias
// changes need, in the same transaction (R23a).
func (c *Catalog) inTx(ctx context.Context, fn func(*db.Queries, reparses) error) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		rp := reparses{}
		if err := fn(q, rp); err != nil {
			return err
		}
		return rp.queue(ctx, q)
	})
}
