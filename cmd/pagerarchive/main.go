// Command pagerarchive is the PagerArchive server. Subcommands:
//
//	serve             run the API (default)
//	validate-profile  check profiles and run golden tables, without a database
//	client            add, revoke or list receiving clients
//	import-messages   import pages from an existing archive
//	dedup-report      count how near duplicates differ, to tune R19a's threshold
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
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata" // the server image has no zoneinfo (R35a)

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pagerarchive "github.com/RIKIKU/PagerArchive"
	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/areas"
	"github.com/RIKIKU/PagerArchive/internal/authentik"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/redaction"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/reports"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/streets"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
	"github.com/RIKIKU/PagerArchive/web"
)

// version is the build's, from the VERSION file (ADR 0016, ADR 0024).
var version = pagerarchive.Version

const usage = `usage: pagerarchive <command> [arguments]

commands:
  serve                       run the API server (default)
  validate-profile [flags] D  validate profiles and run golden tables under D
  client add <id> [name]      add a receiving client and print its API key
  client revoke <id>          revoke a client's API key
  client list                 list clients
  service-account create <name> --owner <admin>
                              create a read-only service account owned by an admin
                              (user ID or email) and print its credential once
  service-account list        list service accounts
  service-account revoke <name>
                              end a service account at once
  service-account rotate <name>
                              give it a new credential, ending the old one at once
  import-messages [--dry-run] F
                              import pages from an existing archive, from a JSON
                              Lines file or - for standard input (docs/import.md)
  dedup-report [--since 7d]   count how near-duplicate copies of pages differed,
                              and how far apart close pages were: counts only,
                              never text (R19b)
  version                     print the version

Templates and aliases are managed in the browser or with the PagerArchive
PowerShell module (docs/powershell.md), which uses the same API and rules.

serve reads its settings from the environment:
  DATABASE_URL     PostgreSQL connection string (required): the API's own role,
                   pagerarchive, not a superuser (docs/deploy.md)
  PROFILES_DIR     directory of profile files (default /profiles; /dev/null mounted
                   there means the example built into the image, with a warning)
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
                          (must differ from Authentik.s administrator.s: R44c)
  NIGHTLY_AT              local time of the nightly retention and redaction run (default 03:00)
  DEDUP_NEAR_RATIO        edits a copy of a page may differ by, per character, and still be
                          the same transmission (R19a; default 0.1, at most 0.5; 0 turns
                          near duplicates off); import-messages and dedup-report read it too
  BACKUP_STATUS_FILE      the backup service's status file (default: no backups to report)
  BACKUP_REQUEST_FILE     where Back up now asks the backup service for a backup (default: it can't)
  AUTO_UPGRADE_STATUS_FILE  what automatic upgrades last did (default: they are off)
  MAP_TILES               path to a PMTiles map extract (default: no background map;
                          /dev/null mounted there means the example built into the image)
  MAP_ATTRIBUTION         the extract's credit line (default: the one in the extract's metadata)
  RADIOARCHIVE_URL        RadioArchive's public URL: fireground links (R92a) and the app switcher
                          (R77e; default: none, PagerArchive stands alone)
  RADIOARCHIVE_NAME       the name the switcher shows (default Radio)
  LOOK_COOKIE_DOMAIN      the parent domain whose look cookie the apps share, such as
                          example.org for pager.example.org (R92b; default: none, the
                          look is the account's alone)
  INTEGRATION_SECRETS_KEY      32 random bytes in base64 that encrypt integrations' secrets
                               (openssl rand -base64 32); without it integrations are off
  INTEGRATION_SECRETS_KEY_OLD  the previous key, only while rotating
  METRICS_TOKEN                bearer token for GET /metrics (default: metrics off)
  AUTHENTIK_METRICS_DB_PASSWORD  the pagerarchive_metrics role's password, for the
                               locked-accounts metric; the role is created on start
                               while DATABASE_URL is a superuser (otherwise by
                               deploy/postgres/api-role.sh)
                               (or AUTHENTIK_METRICS_DATABASE_URL, for one set up elsewhere)
  OTEL_EXPORTER_OTLP_ENDPOINT  where to send traces and logs over OTLP/HTTP, e.g.
                               http://alloy:4318 (default: telemetry off); the other
                               standard OTEL_* settings apply (docs/monitoring.md)
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
	case "service-account":
		err = serviceAccountCmd(args, stdout)
	case "import-messages":
		err = importMessages(args, os.Stdin, stdout, stderr)
	case "dedup-report":
		err = dedupReport(args, stdout, stderr)
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

// The made-up example built into the server image (Dockerfile), for a
// deployment that mounts no profile or map (R84).
const (
	builtinProfiles = "/usr/share/pagerarchive/example-profile"
	builtinMap      = "/usr/share/pagerarchive/example-map.pmtiles"
)

// mountedOr returns path, or def if what's at path is a character device:
// compose.yaml mounts /dev/null there when .env names no file of the
// deployment's own, since Compose can't leave a mount out. A path that's
// missing or empty stays an error for the caller, so a mistyped setting never
// falls back quietly. The bool says def was chosen.
func mountedOr(path, def string) (string, bool) {
	if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return def, true
	}
	return path, false
}

// profilesDir is where the profiles are, and whether that's the built-in example.
func profilesDir() (string, bool) {
	return mountedOr(getenv("PROFILES_DIR", "/profiles"), builtinProfiles)
}

func serve(stderr io.Writer) error {
	plain := slog.New(slog.NewJSONHandler(stderr, nil))
	// Telemetry (R69-R75, docs/monitoring.md): off unless OTEL_* says where
	// to send it. Log lines go to stderr as before and, when it's on, over
	// OTLP too (R73). Metrics stay on /metrics (R75).
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "pagerarchive", Version: version, Log: plain})
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tel.Shutdown(flush)
	}()
	log := slog.New(tel.LogHandler(plain.Handler(), "pagerarchive"))
	tracer := tel.Tracer("pagerarchive")

	// Profiles first: an invalid profile stops the server before anything
	// else happens (R1, A14).
	profiles, example := profilesDir()
	if example {
		log.Warn("no network profile is mounted (PROFILES_DIR in .env): using the made-up example profile, whose redaction rules aren't your network's")
	}
	set, err := profile.LoadDir(profiles)
	if err != nil {
		return fmt.Errorf("invalid profile:\n%w", err)
	}
	for _, w := range set.Warnings() {
		log.Warn("a profile still names the call sign by its old name: rename it (ADR 0040)", "warning", w)
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
	var dbTracer []pgx.QueryTracer
	if tel.On() {
		dbTracer = append(dbTracer, store.Tracer{T: tracer})
	}
	st, err := store.Open(ctx, dbURL, dbTracer...)
	if err != nil {
		return err
	}
	defer st.Close()
	// After the migrations: keep PostGIS's extensions at the image's version
	// (R84c). A failure is a warning, and the API starts anyway.
	store.UpdatePostGIS(ctx, st.Pool, log)
	// R84i: the API's own role, not the superuser. An install from before it
	// still connects as the superuser, and works, with this warning on every
	// start until it's switched over (deploy/postgres/api-role.sh).
	superuser, err := st.Superuser(ctx)
	if err != nil {
		log.Warn("couldn't check the API's database role", "error", store.SafeError(err))
	} else if superuser {
		log.Warn("the API connects to PostgreSQL as a superuser, which can reach Authentik's database and the database server itself: switch it to its own role with deploy/postgres/api-role.sh (docs/deploy.md, \"The API's database role\")")
	}

	loc, err := time.LoadLocation(getenv("TZ", "UTC"))
	if err != nil {
		return fmt.Errorf("TZ: %w", err)
	}
	auth, err := authConfig()
	if err != nil {
		return err
	}
	hash, legacyHash, err := reparse.ProfileHashes(set, net)
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
	// The profile's layers of areas (R88, ADR 0035): loaded when they change,
	// before any page is placed, layers no longer named removed, and the area
	// of every page stored before worked out in the background. A layer whose
	// file is missing or broken is logged and left out: it doesn't stop the API
	// (an automatic upgrade must come up), and Administration says so.
	areaLayers := areas.Start(ctx, st, net.Areas, log)
	if len(areaLayers) > 0 {
		go func() {
			for ctx.Err() == nil {
				err := areas.Backfill(ctx, st, log, 30*time.Second)
				if err == nil || ctx.Err() != nil {
					return
				}
				log.Error("stored pages' areas", "error", store.SafeError(err))
				select {
				case <-ctx.Done():
				case <-time.After(time.Minute):
				}
			}
		}()
	}
	// Pages stored before their derived localities were kept get theirs, in
	// the background, once, and the report counts are built again with them
	// (R24, ADR 0034).
	rep := &reports.Service{Pool: st.Pool, Location: loc, Log: log}
	go func() {
		for ctx.Err() == nil {
			err := geocoder.BackfillLocalities(ctx, st, log, 30*time.Second, rep.Rebuild)
			if err == nil || ctx.Err() != nil {
				return
			}
			log.Error("stored pages' localities", "error", store.SafeError(err))
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
		}
	}()
	// Live updates (R32), and the side menu's new-large-event dot (R77c),
	// which a rebuild that opens an event tells too.
	hub := live.NewHub()
	worker := &reparse.Worker{
		Store: st, Network: net, Agencies: set.Agencies, Streets: ix, Geo: geocoder,
		ProfileHash: hash, LegacyProfileHash: legacyHash, Log: log, Poll: time.Minute, Tracer: tracer,
		Regroup: &events.Regrouper{Network: net, Opened: hub.EventOpened},
	}
	if err := worker.CheckProfiles(ctx); err != nil {
		return fmt.Errorf("profile change check: %s", store.SafeError(err))
	}
	go worker.Run(ctx)
	// Retroactive redaction (P1, R45a, ADR 0044): the nightly run starts and
	// does it; this carries a run cut short by a restart or upgrade on from
	// its last batch, and one an admin resumed or started again.
	redactor := &redaction.Worker{Store: st, Network: net, Reparse: worker, Log: log, Poll: time.Minute, Tracer: tracer}
	if err := redactor.CheckRules(ctx); err != nil {
		return fmt.Errorf("redaction rules check: %s", store.SafeError(err))
	}
	go redactor.Run(ctx)

	dir := &authentik.Client{URL: auth.authentikURL, Token: auth.apiToken, HTTP: auth.http}
	acc := &accounts.Service{
		Store: st, Dir: dir, AllowedDomains: auth.domains, InitialAdmin: auth.initialAdmin,
		Now: time.Now, Log: log, CloseLive: hub.CloseUsers,
	}
	if v := os.Getenv("INVITATION_SENDER_LIMITS"); v != "" {
		if acc.SenderLimits, err = accounts.ParseSenderLimits(v); err != nil {
			return err
		}
	}
	go expireSessions(ctx, st, log)
	go acc.CheckFirstAdminAddress(ctx, adminAddressCheckEvery, adminAddressCheckTries)
	go acc.WatchInvitations(ctx, adminAddressCheckEvery, adminAddressCheckTries)
	// R92e: everyone in the group the companion app admits, once Authentik answers.
	go acc.JoinUsersGroup(ctx, dir, adminAddressCheckEvery, adminAddressCheckTries)

	at, err := nightly.ParseAt(getenv("NIGHTLY_AT", "03:00"))
	if err != nil {
		return err
	}
	// M24's reports (R38a-R38i, ADR 0031): built in the background on the
	// first start, then kept in step as pages change, and by the nightly run.
	go rep.Run(ctx)
	job := &nightly.Job{Store: st, Redaction: redactor, Location: loc, At: at, Now: time.Now, Log: log, Tracer: tracer, Reports: rep}
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
		AppURL: auth.appURL, Location: loc, Version: version, Log: log, Now: time.Now, Tracer: tracer,
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
		// With its own URL, the role is someone else's to set up, and with the
		// API's own database role (R84i) it's the database's first-start
		// script's or api-role.sh's: only a superuser can make it. A failure
		// costs this one metric, so it doesn't stop the API.
		if pw := os.Getenv("AUTHENTIK_METRICS_DB_PASSWORD"); pw != "" && os.Getenv("AUTHENTIK_METRICS_DATABASE_URL") == "" && superuser {
			if err := authentik.EnsureLocksRole(ctx, dbURL, pw); err != nil {
				log.Warn("the locked-accounts metric's role couldn't be set up", "error", store.SafeError(err))
			}
		}
	}

	mapTiles := os.Getenv("MAP_TILES")
	if mapTiles != "" {
		mapTiles, _ = mountedOr(mapTiles, builtinMap)
		if _, err := os.Stat(mapTiles); err != nil {
			return fmt.Errorf("MAP_TILES: %w", err)
		}
	}

	nearRatio, err := nearRatioSetting()
	if err != nil {
		return err
	}
	radio, err := radioArchiveURL(os.Getenv("RADIOARCHIVE_URL"))
	if err != nil {
		return err
	}
	lookDomain, err := lookCookieDomain(os.Getenv("LOOK_COOKIE_DOMAIN"), auth.appURL)
	if err != nil {
		return err
	}
	srv := &server.Server{
		RadioArchiveName: getenv("RADIOARCHIVE_NAME", "Radio"),
		Store:            st,
		Ingest: &ingest.Service{
			Store:     st,
			Network:   net,
			Agencies:  set.Agencies,
			Streets:   ix,
			Geo:       geocoder,
			OnStored:  integ.Enqueue,
			Events:    &events.Detector{Network: net, Fire: integ.EnqueueEvent},
			Regroup:   &events.Regrouper{Network: net, Opened: hub.EventOpened},
			NearRatio: nearRatio,
			Log:       log,
			Tracer:    tracer,
		},
		Tracer:   tracer,
		Log:      log,
		Now:      time.Now,
		Hub:      hub,
		Location: loc,
		Accounts: acc,
		Catalog:  &catalog.Catalog{Store: st},
		Reports:  rep,
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
		ServiceAccounts: &server.PowerShell{
			Issuer:   auth.authentikURL.JoinPath("/application/o/pagerarchive-service/").String(),
			ClientID: "pagerarchive-service",
			HTTP:     auth.http,
		},
		AppURL:                auth.appURL,
		AccountURL:            auth.authentikURL.JoinPath("/if/user/").String() + "#/settings",
		Nightly:               job,
		Reparse:               &worker.Progress,
		Redaction:             redactor,
		BackupStatusFile:      os.Getenv("BACKUP_STATUS_FILE"),
		BackupRequestFile:     os.Getenv("BACKUP_REQUEST_FILE"),
		AutoUpgradeStatusFile: os.Getenv("AUTO_UPGRADE_STATUS_FILE"),
		Started:               time.Now(),
		MapTiles:              mapTiles,
		MapAttribution:        os.Getenv("MAP_ATTRIBUTION"),
		RadioArchiveURL:       radio,
		LookCookieDomain:      lookDomain,
		Areas:                 areaLayers,
		Integrations:          integ,
		MetricsToken:          os.Getenv("METRICS_TOKEN"),
		Locks:                 locks,
		Version:               version,
	}
	// R52: the flagged counts are counted in the background, once a minute,
	// and only while the metrics are on: a scrape never counts them.
	if srv.MetricsToken != "" {
		srv.Flagged = &server.FlagCounts{Store: st, Log: log}
		go srv.Flagged.Run(ctx)
		// R84c: an extension update the API couldn't run (a switched API isn't
		// the superuser, ADR 0033), for the example stack to alert on: checked
		// now, after UpdatePostGIS, and every hour, as a db container can be
		// recreated with a newer image while the API runs.
		srv.Extensions = &server.ExtensionChecks{Store: st, Log: log}
		go srv.Extensions.Run(ctx)
	}
	// R64: template names from before they had to differ by more than letter
	// case (migration 00022), on every start until an admin sorts them out.
	if dups, err := srv.Catalog.CaseDuplicates(ctx); err != nil {
		log.Warn("templates whose names differ only in case couldn't be listed", "error", err.Error())
	} else if len(dups) > 0 {
		names := make([]string, len(dups))
		for i, d := range dups {
			names[i] = fmt.Sprintf("%q (id %d)", d.Name, d.ID)
		}
		log.Warn("template names differ only in letter case, from before names had to differ by more: new names can't match them. Rename or delete all but one of each, in Administration, Templates",
			"templates", strings.Join(names, ", "))
	}
	// R27b: aliases sharing a name, ignoring case, from before names were
	// unique (migration 00024), on every start until an admin sorts them
	// out. By capcode and id: alias names are never logged.
	if groups, err := srv.Catalog.NameDuplicates(ctx); err != nil {
		log.Warn("aliases that share a name couldn't be listed", "error", err.Error())
	} else if len(groups) > 0 {
		list := make([]string, len(groups))
		for i, g := range groups {
			each := make([]string, len(g))
			for j, a := range g {
				each[j] = fmt.Sprintf("%s (id %d)", a.Pattern, a.ID)
			}
			list[i] = strings.Join(each, " = ")
		}
		log.Warn("aliases share names, ignoring letter case, from before names had to be unique: new names can't match them. Rename all but one of each, in Administration, Aliases",
			"groups", len(groups), "aliases", strings.Join(list, "; "))
	}
	// The app's page is sent in the signed-in user's look (R82d).
	srv.UI = web.Handler(srv.PageLook)
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

// Authentik can take minutes to answer on a first start, and the API doesn't
// wait for it: the address check (R44c) and the invitations check (ADR 0021)
// keep asking for 10 minutes.
const (
	adminAddressCheckEvery = 5 * time.Second
	adminAddressCheckTries = 120
)

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

// lookCookieDomain reads LOOK_COOKIE_DOMAIN (R92b): the parent domain the
// apps share their look cookie on, in lower case and without a leading dot.
// The app's own host must be in it, or the browser would refuse the cookie.
// Empty: there's no shared look.
func lookCookieDomain(v string, app *url.URL) (string, error) {
	v = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), ".")
	if v == "" {
		return "", nil
	}
	if !cookieDomainRe.MatchString(v) {
		return "", errors.New("LOOK_COOKIE_DOMAIN must be a domain name, such as example.org")
	}
	if host := strings.ToLower(app.Hostname()); host != v && !strings.HasSuffix(host, "."+v) {
		return "", fmt.Errorf("LOOK_COOKIE_DOMAIN (%s) must be APP_URL's host or a parent domain of it (%s)", v, host)
	}
	return v, nil
}

var cookieDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// radioArchiveURL reads RADIOARCHIVE_URL (R92): the radio beside this
// PagerArchive, an absolute http(s) URL with a host, without its trailing
// slash. Empty: it stands alone.
func radioArchiveURL(v string) (string, error) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("RADIOARCHIVE_URL must be an absolute http(s) URL, such as https://radio.example.com")
	}
	return v, nil
}
