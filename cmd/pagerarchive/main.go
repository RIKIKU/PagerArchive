// Command pagerarchive is the PagerArchive server. Subcommands:
//
//	serve             run the API (default)
//	validate-profile  check profiles and run golden tables, without a database
//	client            add, revoke or list receiving clients
//	version           print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: pagerarchive <command> [arguments]

commands:
  serve                       run the API server (default)
  validate-profile [flags] D  validate profiles and run golden tables under D
  client add <id> [name]      add a receiving client and print its API key
  client revoke <id>          revoke a client's API key
  client list                 list clients
  version                     print the version

serve reads its settings from the environment:
  DATABASE_URL     PostgreSQL connection string (required)
  PROFILES_DIR     directory of profile files (default /profiles)
  NETWORK_PROFILE  network profile name (default: the only one in PROFILES_DIR)
  LISTEN_ADDR      address to listen on (default :8080)
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

	srv := &server.Server{
		Store: st,
		Ingest: &ingest.Service{
			Store:    st,
			Network:  net,
			Agencies: set.Agencies,
			Streets:  streets.NewIndex(ds.Streets, net.StreetTypes),
		},
		Log: log,
		Now: time.Now,
	}
	addr := getenv("LISTEN_ADDR", ":8080")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", addr, "network", net.Name, "agencies", len(set.Agencies), "version", version)
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
			if c.RevokedAt != nil {
				status = "revoked"
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
