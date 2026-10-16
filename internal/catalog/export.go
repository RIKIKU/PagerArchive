package catalog

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

// ExportHeader is the export's header row: the import format (R58, R30).
var ExportHeader = []string{"id", "address", "alias", "agency", "icon", "color", "ignore"}

// formulaStart is what makes a spreadsheet read a cell as a formula.
const formulaStart = "=+-@\t\r"

// guard writes a value a spreadsheet would run as a formula with a leading
// apostrophe, so opening an export never runs one (ADR 0007 §6).
func guard(s string) string {
	if s != "" && strings.ContainsRune(formulaStart, rune(s[0])) {
		return "'" + s
	}
	return s
}

// unguard reverses guard on import, so an export imports unchanged.
func unguard(s string) string {
	if len(s) > 1 && s[0] == '\'' && strings.ContainsRune(formulaStart, rune(s[1])) {
		return s[1:]
	}
	return s
}

// ExportCSV writes every alias in the import format (R58): the template's
// name in the agency column (the import looks templates up by it), and the
// template's icon and colour. Importing it into the same database reports
// no differences (A46). It returns the number of aliases written.
func (c *Catalog) ExportCSV(ctx context.Context, w io.Writer) (int, error) {
	rows, err := c.Store.ExportAliases(ctx)
	if err != nil {
		return 0, dbErr("export", err)
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	if err := cw.Write(ExportHeader); err != nil {
		return 0, err
	}
	for _, a := range rows {
		rec := []string{strconv.FormatInt(a.ID, 10), a.Pattern, guard(a.Name), guard(deref(a.TemplateName)),
			guard(deref(a.Icon)), guard(deref(a.Colour)), strconv.FormatBool(a.Ignored)}
		if err := cw.Write(rec); err != nil {
			return 0, err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
