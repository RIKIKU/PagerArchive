// Command pagerarchive is the PagerArchive server. Subcommands:
//
//	serve             run the API (default)
//	validate-profile  check profiles and run golden tables, without a database
//	client            add, revoke or list receiving clients
//	version           print the version
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // the server image has no zoneinfo (R35a)

	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/streets"
	"github.com/RIKIKU/PagerArchive/web"
)

// profileHash identifies the profiles and address dataset in use, so a
// change triggers a re-parse (section 14, R23a).
func profileHash(set *profile.Set, net *profile.Network) (string, error) {
	data, err := os.ReadFile(net.DatasetPath)
	if err != nil {
		return "", fmt.Errorf("address dataset: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "network %s %s\n", net.Name, net.Hash)
	names := make([]string, 0, len(set.Agencies))
	for n := range set.Agencies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "agency %s %s\n", n, set.Agencies[n].Hash)
	}
	fmt.Fprintf(h, "dataset %x\n", sha256.Sum256(data))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: pagerarchive <command> [arguments]

commands:
  serve                       run the API server (default)
  validate-profile [flags] D  validate profiles and run golden tables under D
  client add <id> [name]      add a receiving client and print its API key
  client revoke <id>          revoke a client's API key
  client list                 list clients
  template add|set <name> --agency A [--profile P] [--icon I] [--colour C]
  template delete <name>      delete a template no alias uses
  template list               list templates
  alias add|set <pattern> <name> [--template T] [--ignored]
  alias import <file.csv> [--mapping M.json] [--overrides O.json] [--dry-run]
                              add or update aliases from a CSV export (R30)
  alias delete <pattern>      delete an alias
  alias list                  list aliases
  version                     print the version

Alias and template changes re-parse the affected messages (R23a).

serve reads its settings from the environment:
  DATABASE_URL     PostgreSQL connection string (required)
  PROFILES_DIR     directory of profile files (default /profiles)
  NETWORK_PROFILE  network profile name (default: the only one in PROFILES_DIR)
  LISTEN_ADDR      address to listen on (default :8080)
  TZ               deployment time zone, e.g. Australia/Melbourne (default UTC)
  DEV_VIEWER_ROLE  user or admin: every viewer's role until sign-in (M3) (default user)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(stderr)
	case "validate-profile":
		err = validateProfile(args, stdout, stderr)
	case "client":
		err = clientCmd(args, stdout)
	case "template":
		err = templateCmd(args, stdout, stderr)
	case "alias":
		err = aliasCmd(args, stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, "pagerarchive", version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprint(stderr, usage)
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		if !errors.Is(err, errFailed) && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "error:", err)
		}
		return 1
	}
	return 0
}

func getenv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func serve(stderr io.Writer) error {
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	// Profiles first: an invalid profile stops the server before anything
	// else happens (R1, A14).
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
	ds, err := streets.LoadFile(net.DatasetPath)
	if err != nil {
		return fmt.Errorf("address dataset: %w", err)
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

	loc, err := time.LoadLocation(getenv("TZ", "UTC"))
	if err != nil {
		return fmt.Errorf("TZ: %w", err)
	}
	role := getenv("DEV_VIEWER_ROLE", "user")
	if role != "user" && role != "admin" {
		return errors.New(`DEV_VIEWER_ROLE must be "user" or "admin"`)
	}
	hash, err := profileHash(set, net)
	if err != nil {
		return err
	}

	ix := streets.NewIndex(ds.Streets, net.StreetTypes)
	worker := &reparse.Worker{
		Store: st, Network: net, Agencies: set.Agencies, Streets: ix,
		ProfileHash: hash, Log: log, Poll: time.Minute,
	}
	if err := worker.CheckProfiles(ctx); err != nil {
		return fmt.Errorf("profile change check: %s", store.SafeError(err))
	}
	go worker.Run(ctx)

	srv := &server.Server{
		Store: st,
		Ingest: &ingest.Service{
			Store:    st,
			Network:  net,
			Agencies: set.Agencies,
			Streets:  ix,
		},
		Log:        log,
		Now:        time.Now,
		Hub:        live.NewHub(),
		Location:   loc,
		ViewerRole: role,
		UI:         web.Handler(),
	}
	addr := getenv("LISTEN_ADDR", ":8080")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", addr, "network", net.Name, "agencies", len(set.Agencies), "version", version, "viewer_role", role)
	if role == "admin" {
		log.Warn("DEV_VIEWER_ROLE=admin: every viewer is an admin until sign-in arrives (M3)")
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

func clientCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("client needs a subcommand: add, revoke or list")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	switch {
	case args[0] == "add" && (len(args) == 2 || len(args) == 3):
		name := ""
		if len(args) == 3 {
			name = args[2]
		}
		key, err := st.AddClient(ctx, args[1], name)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "client %s added. Its API key is shown only once:\n%s\n", args[1], key)
	case args[0] == "revoke" && len(args) == 2:
		n, err := st.RevokeClient(ctx, args[1])
		if err != nil {
			return fmt.Errorf("revoke: %s", store.SafeError(err))
		}
		if n == 0 {
			return fmt.Errorf("no active client %q", args[1])
		}
		fmt.Fprintf(stdout, "client %s revoked\n", args[1])
	case args[0] == "list" && len(args) == 1:
		rows, err := st.ListClients(ctx)
		if err != nil {
			return fmt.Errorf("list: %s", store.SafeError(err))
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLAST SEEN")
		for _, c := range rows {
			status, seen := "active", "never"
			switch {
			case c.RevokedAt != nil:
				status = "revoked"
			case c.ClockAheadSince != nil:
				status = "clock ahead"
			}
			if c.LastSeenAt != nil {
				seen = c.LastSeenAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.ID, c.Name, status, seen)
		}
		return tw.Flush()
	default:
		return errors.New("usage: client add <id> [name] | client revoke <id> | client list")
	}
	return nil
}
