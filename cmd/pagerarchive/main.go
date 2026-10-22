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
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // the server image has no zoneinfo (R35a)

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/authentik"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
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
	dataset, err := geocode.DatasetHash(net.DatasetPath, net.StreetTypes)
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
	fmt.Fprintf(h, "dataset %s\n", dataset)
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
  version                     print the version

Templates and aliases are managed in the browser or with the PagerArchive
PowerShell module (docs/powershell.md), which uses the same API and rules.

serve reads its settings from the environment:
  DATABASE_URL     PostgreSQL connection string (required)
  PROFILES_DIR     directory of profile files (default /profiles)
  NETWORK_PROFILE  network profile name (default: the only one in PROFILES_DIR)
  LISTEN_ADDR      address to listen on (default :8080)
  TZ               deployment time zone, e.g. Australia/Melbourne (default UTC)
  APP_URL                 the app.s public URL, e.g. https://pager.example.org (required)
  AUTHENTIK_URL           Authentik.s public URL, its own subdomain (required)
  AUTHENTIK_INTERNAL_URL  how the API reaches Authentik, e.g. http://authentik-server:9000
                          (default: AUTHENTIK_URL)
  AUTHENTIK_API_TOKEN     the app.s Authentik API token (required)
  OIDC_CLIENT_ID          (default pagerarchive)
  OIDC_CLIENT_SECRET      (required)
  POWERSHELL_CLIENT_ID    the PowerShell module's Authentik client (default pagerarchive-powershell)
  ALLOWED_EMAIL_DOMAINS   domains invitations may go to, comma-separated (required)
  INITIAL_ADMIN_EMAIL     may enrol as the first admin while there is no admin
  NIGHTLY_AT              local time of the nightly retention and redaction run (default 03:00)
  MAP_TILES               path to a PMTiles map extract (default: no background map)
  MAP_ATTRIBUTION         the extract's credit line (default: the one in the extract's metadata)
  INTEGRATION_SECRETS_KEY      32 random bytes in base64 that encrypt integrations' secrets
                               (openssl rand -base64 32); without it integrations are off
  INTEGRATION_SECRETS_KEY_OLD  the previous key, only while rotating
  METRICS_TOKEN                bearer token for GET /metrics (default: metrics off)
  AUTHENTIK_METRICS_DB_PASSWORD  the pagerarchive_metrics role's password, for the
                               locked-accounts metric (or AUTHENTIK_METRICS_DATABASE_URL)
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

	loc, err := time.LoadLocation(getenv("TZ", "UTC"))
	if err != nil {
		return fmt.Errorf("TZ: %w", err)
	}
	auth, err := authConfig()
	if err != nil {
		return err
	}
	hash, err := profileHash(set, net)
	if err != nil {
		return err
	}

	ix := streets.NewIndex(ds.Streets, net.StreetTypes)
	nStreets := len(ds.Streets)
	ds = nil
	// ADR 0001: the street index moves into PostgreSQL if it outgrows 50 MB.
	var mem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mem)
	log.Info("street index built", "streets", nStreets, "heap_mib", mem.HeapInuse>>20)
	// The dataset is loaded before anything is placed or re-parsed. A large
	// one takes a while, but only when it changes (ADR 0005).
	dsHash, err := geocode.DatasetHash(net.DatasetPath, net.StreetTypes)
	if err != nil {
		return fmt.Errorf("address dataset: %w", err)
	}
	started := time.Now()
	stats, err := geocode.Load(ctx, st, net.DatasetPath, ix, dsHash)
	if err != nil {
		return err
	}
	if !stats.Skipped {
		log.Info("address dataset loaded", "addresses", stats.Addresses, "streets", stats.Streets, "localities", stats.Localities, "seconds", int(time.Since(started).Seconds()))
	}
	geocoder := &geocode.Geocoder{Index: ix, GridSystem: net.GridSystem}
	worker := &reparse.Worker{
		Store: st, Network: net, Agencies: set.Agencies, Streets: ix, Geo: geocoder,
		ProfileHash: hash, Log: log, Poll: time.Minute,
	}
	if err := worker.CheckProfiles(ctx); err != nil {
		return fmt.Errorf("profile change check: %s", store.SafeError(err))
	}
	go worker.Run(ctx)

	hub := live.NewHub()
	dir := &authentik.Client{URL: auth.authentikURL, Token: auth.apiToken, HTTP: auth.http}
	acc := &accounts.Service{
		Store: st, Dir: dir, AllowedDomains: auth.domains, InitialAdmin: auth.initialAdmin,
		Now: time.Now, Log: log, SignedOut: hub.CloseUsers,
	}
	go expireSessions(ctx, st, log)

	at, err := nightly.ParseAt(getenv("NIGHTLY_AT", "03:00"))
	if err != nil {
		return err
	}
	job := &nightly.Job{Store: st, Network: net, Reparse: worker, Location: loc, At: at, Now: time.Now, Log: log}
	go job.Run(ctx)

	// Integrations (R47-R51c, ADR 0006). Without a key they're off, and the
	// API still starts, so an upgrade doesn't stop it.
	var box *integrations.Box
	if key := os.Getenv("INTEGRATION_SECRETS_KEY"); key != "" {
		if box, err = integrations.NewBox(key, os.Getenv("INTEGRATION_SECRETS_KEY_OLD")); err != nil {
			return err
		}
	} else {
		log.Warn("integrations are off: INTEGRATION_SECRETS_KEY is not set (docs/deploy.md)")
	}
	integ := &integrations.Service{
		Store: st, Network: net, Agencies: set.Agencies, Box: box, Presets: integrations.DefaultPresets(),
		AppURL: auth.appURL, Location: loc, Version: version, Log: log, Now: time.Now,
	}
	integ.Guard = &integrations.Guard{Allow: integ.AllowList}
	if n, err := integ.RotateKeys(ctx); err != nil {
		return fmt.Errorf("integration secrets: %w", err)
	} else if n > 0 {
		log.Info("integration secrets sealed again with the new key", "integrations", n)
	}
	go integ.Run(ctx)

	// R52: the locked-accounts count comes from Authentik's database, through
	// a role that can only call one function (ADR 0006, owner decision Q1).
	var locks server.LockCounter
	if lockURL, err := authentikMetricsURL(dbURL); err != nil {
		return err
	} else if lockURL != "" {
		cfg, err := pgxpool.ParseConfig(lockURL)
		if err != nil {
			return errors.New("AUTHENTIK_METRICS_DATABASE_URL isn't a valid connection string")
		}
		cfg.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return errors.New("AUTHENTIK_METRICS_DATABASE_URL isn't a valid connection string")
		}
		defer pool.Close()
		locks = &authentik.Locks{Pool: pool}
	}

	mapTiles := os.Getenv("MAP_TILES")
	if mapTiles != "" {
		if _, err := os.Stat(mapTiles); err != nil {
			return fmt.Errorf("MAP_TILES: %w", err)
		}
	}

	srv := &server.Server{
		Store: st,
		Ingest: &ingest.Service{
			Store:    st,
			Network:  net,
			Agencies: set.Agencies,
			Streets:  ix,
			Geo:      geocoder,
			OnStored: integ.Enqueue,
		},
		Log:      log,
		Now:      time.Now,
		Hub:      hub,
		Location: loc,
		Accounts: acc,
		Catalog:  &catalog.Catalog{Store: st},
		OIDC: &server.OIDC{
			Issuer:       auth.authentikURL.JoinPath("/application/o/pagerarchive/").String(),
			ClientID:     auth.clientID,
			ClientSecret: auth.clientSecret,
			RedirectURL:  auth.appURL.JoinPath("/auth/callback").String(),
			HTTP:         auth.http,
		},
		PowerShell: &server.PowerShell{
			Issuer:   auth.authentikURL.JoinPath("/application/o/pagerarchive-powershell/").String(),
			ClientID: getenv("POWERSHELL_CLIENT_ID", "pagerarchive-powershell"),
			HTTP:     auth.http,
		},
		AppURL:         auth.appURL,
		AccountURL:     auth.authentikURL.JoinPath("/if/user/").String() + "#/settings",
		UI:             web.Handler(),
		Nightly:        job,
		MapTiles:       mapTiles,
		MapAttribution: os.Getenv("MAP_ATTRIBUTION"),
		Integrations:   integ,
		MetricsToken:   os.Getenv("METRICS_TOKEN"),
		Locks:          locks,
		Version:        version,
	}
	addr := getenv("LISTEN_ADDR", ":8080")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", addr, "network", net.Name, "agencies", len(set.Agencies), "version", version, "app_url", auth.appURL.String(), "authentik_url", auth.authentikURL.String())
	if ok, _ := acc.FirstAdminAvailable(ctx); ok {
		log.Info("no admin yet: INITIAL_ADMIN_EMAIL can set up the first admin from the sign-in page")
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

// authSettings are the sign-in settings (R40-R44, ADR 0004).
type authSettings struct {
	appURL, authentikURL   *url.URL
	clientID, clientSecret string
	apiToken               string
	domains                []string
	initialAdmin           string
	http                   *http.Client
}

func authConfig() (authSettings, error) {
	var a authSettings
	parse := func(name string, required bool) (*url.URL, error) {
		v := strings.TrimRight(os.Getenv(name), "/")
		if v == "" {
			if required {
				return nil, fmt.Errorf("%s is not set", name)
			}
			return nil, nil
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s must be an http(s) URL", name)
		}
		return u, nil
	}
	var err error
	if a.appURL, err = parse("APP_URL", true); err != nil {
		return a, err
	}
	if a.authentikURL, err = parse("AUTHENTIK_URL", true); err != nil {
		return a, err
	}
	internal, err := parse("AUTHENTIK_INTERNAL_URL", false)
	if err != nil {
		return a, err
	}
	a.http = &http.Client{Transport: authentik.Transport(a.authentikURL, internal, nil), Timeout: 30 * time.Second}
	a.clientID = getenv("OIDC_CLIENT_ID", "pagerarchive")
	for _, v := range []struct {
		name string
		dst  *string
	}{{"OIDC_CLIENT_SECRET", &a.clientSecret}, {"AUTHENTIK_API_TOKEN", &a.apiToken}} {
		if *v.dst = os.Getenv(v.name); *v.dst == "" {
			return a, fmt.Errorf("%s is not set", v.name)
		}
	}
	if a.domains = accounts.ParseDomains(os.Getenv("ALLOWED_EMAIL_DOMAINS")); len(a.domains) == 0 {
		return a, errors.New("ALLOWED_EMAIL_DOMAINS is not set")
	}
	if a.initialAdmin = strings.TrimSpace(os.Getenv("INITIAL_ADMIN_EMAIL")); a.initialAdmin != "" {
		if _, ok := accounts.ParseEmail(a.initialAdmin); !ok {
			return a, errors.New("INITIAL_ADMIN_EMAIL is not an email address")
		}
	}
	return a, nil
}

// authentikMetricsURL is the connection for the locked-accounts metric:
// AUTHENTIK_METRICS_DATABASE_URL, or else DATABASE_URL's server with the
// pagerarchive_metrics role, AUTHENTIK_METRICS_DB_PASSWORD and the
// authentik database. "" leaves the metric out.
func authentikMetricsURL(dbURL string) (string, error) {
	if v := os.Getenv("AUTHENTIK_METRICS_DATABASE_URL"); v != "" {
		return v, nil
	}
	pw := os.Getenv("AUTHENTIK_METRICS_DB_PASSWORD")
	if pw == "" {
		return "", nil
	}
	u, err := url.Parse(dbURL)
	if err != nil || u.Host == "" {
		return "", errors.New("AUTHENTIK_METRICS_DB_PASSWORD needs DATABASE_URL in URL form (postgres://…)")
	}
	u.User = url.UserPassword("pagerarchive_metrics", pw)
	u.Path = "/authentik"
	return u.String(), nil
}

// expireSessions deletes expired sessions hourly.
func expireSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := st.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			log.Error("expire sessions", "error", store.SafeError(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
