package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/importer"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

const importUsage = "usage: import-messages [--dry-run] <file, or - for standard input>"

// importMessages is R83: pages from an existing archive, from a file or
// standard input (docs/import.md). The report goes to stdout, progress to
// stderr. Neither holds page text.
func importMessages(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	dry, name := false, ""
	for _, a := range args {
		switch {
		case a == "--dry-run" || a == "-dry-run":
			dry = true
		case name == "" && (a == "-" || !strings.HasPrefix(a, "-")):
			name = a
		default:
			return errors.New(importUsage)
		}
	}
	if name == "" {
		return errors.New(importUsage)
	}
	r := stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}

	set, err := profile.LoadDir(getenv("PROFILES_DIR", "/profiles"))
	if err != nil {
		return fmt.Errorf("invalid profile:\n%w", err)
	}
	net, err := pickNetwork(set, os.Getenv("NETWORK_PROFILE"))
	if err != nil {
		return err
	}
	if net == nil {
		return errors.New("no network profile in PROFILES_DIR")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	loc, err := time.LoadLocation(getenv("TZ", "UTC"))
	if err != nil {
		return fmt.Errorf("TZ: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	// Pages are placed against the dataset in the database, which the API
	// loads when it starts.
	dsHash, err := geocode.DatasetHash(net.DatasetPath, net.StreetTypes)
	if err != nil {
		return fmt.Errorf("address dataset: %w", err)
	}
	loaded, err := geocode.Loaded(ctx, st, dsHash)
	if err != nil {
		return fmt.Errorf("address dataset: %s", store.SafeError(err))
	}
	if !loaded {
		return errors.New("the address dataset isn't loaded: start the API with this profile first")
	}
	ds, err := streets.LoadFile(net.DatasetPath)
	if err != nil {
		return fmt.Errorf("address dataset: %w", err)
	}
	ix := streets.NewIndex(ds.Streets, net.StreetTypes)
	ds = nil

	job := &nightly.Job{Store: st, Location: loc}
	days, err := job.Days(ctx)
	if err != nil {
		return fmt.Errorf("retention: %s", store.SafeError(err))
	}
	im := &importer.Importer{
		Store: st,
		// No OnStored: nothing is queued for an integration (R83b).
		Ingest:   &ingest.Service{Store: st, Network: net, Agencies: set.Agencies, Streets: ix, Geo: &geocode.Geocoder{Index: ix, GridSystem: net.GridSystem}},
		Network:  net,
		Cutoff:   job.Cutoff(time.Now(), days),
		Now:      time.Now,
		DryRun:   dry,
		Report:   stdout,
		Progress: stderr,
		LockWait: 30 * time.Second,
	}
	rep, err := im.Run(ctx, r)
	if errors.Is(err, importer.ErrRunning) {
		return err
	}
	rep.Write(stdout)
	if err != nil {
		return fmt.Errorf("%w; run the same file again to finish", err)
	}
	return nil
}
