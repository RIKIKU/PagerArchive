package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/RIKIKU/PagerArchive/internal/golden"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

const validateUsage = `usage: pagerarchive validate-profile [flags] <dir>

Validates every profile under <dir> against the JSON Schemas, compiles its
patterns, and runs every golden-*.json table found under <dir>. Agency tables
run with the named network profile. Needs no database (SPEC.md R10).

Exit status: 0 if every confirmed row passes, 1 otherwise. Rows with
"status": "proposed" are reported but don't fail the run.

flags:
`

// errFailed means validation ran and found failures (already reported).
var errFailed = errors.New("validation failed")

func validateProfile(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("validate-profile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	network := fs.String("network", "", "network profile to pair agency tables with (default: the only one in <dir>)")
	streetsFile := fs.String("streets", "", "street list for address_split, replacing the network's address dataset (e.g. a golden street fixture)")
	verbose := fs.Bool("v", false, "print every row, not just failures")
	fs.Usage = func() {
		fmt.Fprint(stderr, validateUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("validate-profile needs exactly one directory")
	}
	dir := fs.Arg(0)

	set, err := profile.LoadDir(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "profiles: %d network, %d agency: schemas and patterns OK\n", len(set.Networks), len(set.Agencies))

	net, err := pickNetwork(set, *network)
	if err != nil {
		return err
	}
	var ix *streets.Index
	if net != nil {
		path := net.DatasetPath
		if *streetsFile != "" {
			path = *streetsFile
		}
		ds, err := streets.LoadFile(path)
		if err != nil {
			return fmt.Errorf("street list: %w", err)
		}
		ix = streets.NewIndex(ds.Streets, net.StreetTypes)
		fmt.Fprintf(stdout, "network: %s; streets: %s (%d)\n", net.Name, path, len(ds.Streets))
	}

	tables, err := golden.LoadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].File < tables[j].File })
	runner := golden.Runner{Set: set, Network: net, Streets: ix}
	failed := false
	for _, t := range tables {
		res, err := runner.Run(t)
		if err != nil {
			return err
		}
		pass, fail, propFail := res.Counts()
		for _, row := range res.Rows {
			status := "PASS"
			switch {
			case len(row.Diffs) > 0 && row.Case.Proposed():
				status = "FAIL (proposed, not counted)"
			case len(row.Diffs) > 0:
				status = "FAIL"
			}
			if *verbose || len(row.Diffs) > 0 {
				fmt.Fprintf(stdout, "  %s %s: %s\n", status, t.Profile, row.Case.Name)
			}
			for _, d := range row.Diffs {
				fmt.Fprintf(stdout, "      %s\n", d)
			}
		}
		line := fmt.Sprintf("%s (%s, %s): %d passed, %d failed", t.Profile, res.Kind, t.File, pass, fail)
		if propFail > 0 {
			line += fmt.Sprintf(", %d proposed rows failing", propFail)
		}
		fmt.Fprintln(stdout, line)
		if fail > 0 {
			failed = true
		}
	}
	if len(tables) == 0 {
		fmt.Fprintln(stdout, "no golden tables found")
	}
	if failed {
		return errFailed
	}
	return nil
}

func pickNetwork(set *profile.Set, name string) (*profile.Network, error) {
	if name != "" {
		n, ok := set.Networks[name]
		if !ok {
			return nil, fmt.Errorf("no network profile named %q", name)
		}
		return n, nil
	}
	switch len(set.Networks) {
	case 0:
		return nil, nil
	case 1:
		for _, n := range set.Networks {
			return n, nil
		}
	}
	return nil, errors.New("several network profiles found: choose one with --network")
}
