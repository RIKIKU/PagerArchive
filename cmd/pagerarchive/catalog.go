package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

func openCatalog(ctx context.Context) (*catalog.Catalog, func(), error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, nil, errors.New("DATABASE_URL is not set")
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	return &catalog.Catalog{Store: st}, st.Close, nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// parseInterspersed parses flags that may come after positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// importCmd imports aliases from a CSV export (R30). The report names alias
// data only where the owner needs to check it: duplicated capcodes (R27a)
// and repeated names.
func importCmd(ctx context.Context, cat *catalog.Catalog, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("alias import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mappingFile := fs.String("mapping", "", `JSON file: {"agency_profiles": {"<agency>": "<profile>"}}`)
	overridesFile := fs.String("overrides", "", `JSON file resolving duplicated capcodes: {"duplicates": {"<capcode>": {"keep_id": "…"} | {"keep_row": N} | {"skip": true}}}`)
	dryRun := fs.Bool("dry-run", false, "check the file and report, without changing anything")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: alias import <file.csv> [--mapping M.json] [--overrides O.json] [--dry-run]")
	}
	var opts catalog.ImportOptions
	opts.DryRun = *dryRun
	if *mappingFile != "" {
		if opts.Mapping, err = catalog.LoadMapping(*mappingFile); err != nil {
			return err
		}
	}
	if *overridesFile != "" {
		if opts.Overrides, err = catalog.LoadOverrides(*overridesFile); err != nil {
			return err
		}
	}
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	res, err := cat.ImportCSV(ctx, f, opts)
	if errors.Is(err, catalog.ErrUnresolved) {
		fmt.Fprintln(stdout, "import stopped: nothing was changed")
		if len(res.Unresolved) > 0 {
			fmt.Fprintf(stdout, "%d capcodes have more than one row; add each to the overrides file:\n", len(res.Unresolved))
			for _, d := range res.Unresolved {
				fmt.Fprintf(stdout, "  %s\n", d.Pattern)
				for _, r := range d.Rows {
					fmt.Fprintf(stdout, "    %s\n", r)
				}
			}
		}
		for _, s := range res.StaleOverrides {
			fmt.Fprintf(stdout, "override problem: %s\n", s)
		}
		return err
	}
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprintln(stdout, "dry run: nothing was changed")
	}
	fmt.Fprintf(stdout, "rows read: %d\naliases added: %d\naliases updated: %d\nmarked ignored: %d\nwithout a template: %d\nduplicated capcodes resolved by overrides: %d\nrows skipped: %d\n",
		res.Rows, res.AliasesAdded, res.AliasesUpdated, res.Ignored, res.WithoutTemplate, res.Resolved, res.Skipped)
	fmt.Fprintf(stdout, "templates created: %d %v\ntemplates already present (unchanged): %d %v\n",
		len(res.TemplatesCreated), res.TemplatesCreated, len(res.TemplatesKept), res.TemplatesKept)
	for _, p := range res.Problems {
		fmt.Fprintln(stdout, "  "+p)
	}
	if len(res.RepeatedNames) > 0 {
		fmt.Fprintf(stdout, "%d alias names are used for more than one capcode (allowed; please check):\n", len(res.RepeatedNames))
		for _, n := range res.RepeatedNames {
			fmt.Fprintf(stdout, "  %q: %s\n", n.Name, strings.Join(n.Patterns, ", "))
		}
	}
	if !*dryRun {
		fmt.Fprintln(stdout, "every message will be re-parsed by the running API")
	}
	return nil
}

func templateCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: template add|set|delete|list")
	}
	ctx := context.Background()
	cat, done, err := openCatalog(ctx)
	if err != nil {
		return err
	}
	defer done()
	switch args[0] {
	case "add", "set":
		fs := flag.NewFlagSet("template "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		agency := fs.String("agency", "", "agency name (required)")
		prof := fs.String("profile", "", "agency profile name (empty: text only)")
		icon := fs.String("icon", "", "icon name")
		colour := fs.String("colour", "", "colour, e.g. #c62828")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("usage: template %s <name> --agency A [--profile P] [--icon I] [--colour C]", args[0])
		}
		t := catalog.Template{Name: pos[0], Agency: *agency, AgencyProfile: optional(*prof), Icon: optional(*icon), Colour: optional(*colour)}
		if args[0] == "add" {
			err = cat.AddTemplate(ctx, t)
		} else {
			err = cat.UpdateTemplate(ctx, t)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "template %s saved\n", t.Name)
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: template delete <name>")
		}
		if err := cat.DeleteTemplate(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "template %s deleted\n", args[1])
	case "list":
		rows, err := cat.Store.ListTemplates(ctx)
		if err != nil {
			return fmt.Errorf("list: %s", store.SafeError(err))
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tAGENCY\tPROFILE\tICON\tCOLOUR\tALIASES")
		for _, t := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", t.Name, t.Agency, deref(t.AgencyProfile), deref(t.Icon), deref(t.Colour), t.AliasCount)
		}
		return tw.Flush()
	default:
		return errors.New("usage: template add|set|delete|list")
	}
	return nil
}

func aliasCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: alias add|set|delete|list")
	}
	ctx := context.Background()
	cat, done, err := openCatalog(ctx)
	if err != nil {
		return err
	}
	defer done()
	switch args[0] {
	case "add", "set":
		fs := flag.NewFlagSet("alias "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		tpl := fs.String("template", "", "template name (empty: no template, text only)")
		ignored := fs.Bool("ignored", false, "hide this alias's pages from the default list (R27)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			return fmt.Errorf("usage: alias %s <pattern> <name> [--template T] [--ignored]", args[0])
		}
		a := catalog.Alias{Pattern: pos[0], Name: pos[1], Template: *tpl, Ignored: *ignored}
		if args[0] == "add" {
			err = cat.AddAlias(ctx, a)
		} else {
			err = cat.UpdateAlias(ctx, a)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "alias %s saved; affected messages will be re-parsed\n", a.Pattern)
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: alias delete <pattern>")
		}
		if err := cat.DeleteAlias(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "alias %s deleted; affected messages will be re-parsed\n", args[1])
	case "import":
		return importCmd(ctx, cat, args[1:], stdout, stderr)
	case "list":
		rows, err := cat.Store.ListAliases(ctx)
		if err != nil {
			return fmt.Errorf("list: %s", store.SafeError(err))
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PATTERN\tNAME\tTEMPLATE\tIGNORED")
		for _, a := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", a.Pattern, a.Name, deref(a.TemplateName), a.Ignored)
		}
		return tw.Flush()
	default:
		return errors.New("usage: alias add|set|delete|list")
	}
	return nil
}
