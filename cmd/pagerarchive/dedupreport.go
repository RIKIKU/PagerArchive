package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/neardup"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

const dedupReportUsage = "usage: dedup-report [--since <days>d or <hours>h, at most 31d; default 7d]"

// maxReportSpan bounds what the report reads, so it stays a short read-only
// query (R19b) and never becomes a long job (R91).
const maxReportSpan = 31 * 24 * time.Hour

// nearRatioSetting is DEDUP_NEAR_RATIO (R19a): how many edits a copy may
// differ by, per character of the shorter copy. Unset is the default; 0
// turns near duplicates off (ingest.Service.NearRatio below 0).
func nearRatioSetting() (float64, error) {
	v := strings.TrimSpace(os.Getenv("DEDUP_NEAR_RATIO"))
	if v == "" {
		return 0, nil
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r < 0 || r > neardup.MaxRatio {
		return 0, fmt.Errorf("DEDUP_NEAR_RATIO must be a number from 0 to %.1f", neardup.MaxRatio)
	}
	if r == 0 {
		return -1, nil
	}
	return r, nil
}

// parseSpan reads 7d or 12h (or a Go duration).
func parseSpan(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return 0, errors.New(dedupReportUsage)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errors.New(dedupReportUsage)
	}
	return d, nil
}

// dedupReport is R19b: how the near duplicates kept since a time differ,
// and how far apart the pages to one capcode within 10 s that were kept
// apart are, so an operator can tune DEDUP_NEAR_RATIO on a real archive;
// and how often a vote among three or more receivers' copies would change
// a message's text (ADR 0050, measuring voting). It reads only, in one
// read-only transaction, and prints counts and histograms: never a page's text, a
// capcode or anything else from a page (P1).
func dedupReport(args []string, stdout, stderr io.Writer) error {
	span := 7 * 24 * time.Hour
	for i := 0; i < len(args); i++ {
		a := args[i]
		var v string
		switch {
		case a == "--since" || a == "-since":
			if i+1 >= len(args) {
				return errors.New(dedupReportUsage)
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "--since="):
			v = strings.TrimPrefix(a, "--since=")
		default:
			return errors.New(dedupReportUsage)
		}
		var err error
		if span, err = parseSpan(v); err != nil {
			return err
		}
	}
	if span > maxReportSpan {
		return errors.New("--since is at most 31d")
	}
	ratio, err := nearRatioSetting()
	if err != nil {
		return err
	}
	svc := &ingest.Service{NearRatio: ratio}

	profiles, example := profilesDir()
	if example {
		fmt.Fprintln(stderr, "warning: no network profile is mounted (PROFILES_DIR in .env): using the made-up example profile")
	}
	set, err := profile.LoadDir(profiles)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	since := time.Now().Add(-span)
	rep, err := ingest.DedupReport(ctx, st, net, set.Agencies, svc.Ratio(), since)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "since %s\n\n", since.UTC().Format(time.RFC3339))
	rep.Write(stdout)
	return nil
}
