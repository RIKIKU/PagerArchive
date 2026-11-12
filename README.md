# PagerArchive

A self-hosted archive and live browser for POCSAG paging networks, received with inexpensive RTL-SDR dongles and decoded with `multimon-ng`.

- Many receivers, one server, with deduplication across receivers
- Live updates, multi-criteria search, job grouping and a map
- Network knowledge (page formats, redaction rules, ignored capcodes) in JSON **profiles**, so it works for any POCSAG network
- Sensitive details are removed **before** anything is stored
- Per-user integrations forward new pages to your own systems (a convenience, not a replacement for your pager)
- A year scrubber for big jumps through the archive, private albums with notes, a print sheet, capcode reports and an unknown-capcodes list
- Admin tools in the browser and a PowerShell module; nightly, self-checked backups; an example Grafana monitoring stack

**Status:** in production use since v1.0.0 (November 2026), receiving a live network around the clock. Releases are cut deliberately, not on every merge: every Saturday when something changed, on demand, and straight away for a security update ([ADR 0024](docs/adr/0024-deliberate-releases.md)); each release lists what changed. What's built and what's next is in [`SPEC.md`](SPEC.md)'s delivery plan; the [releases](https://github.com/RIKIKU/PagerArchive/releases) say what each version brought.

- [Deploying, upgrading and backups](docs/deploy.md)
- [Importing messages from an existing archive](docs/import.md)
- [Integrations](docs/integrations.md)
- [Monitoring: telemetry and the example Grafana stack](docs/monitoring.md)
- [The print sheet](docs/print-sheet.md)
- [Profiles and `validate-profile`](docs/profiles.md)
- [Receiving client](docs/client.md)
- [PowerShell module](docs/powershell.md)
- [Architecture decisions](docs/adr/)

## Development

Needs Go (see `go.mod`) and, for the acceptance tests, PostgreSQL with PostGIS:

```
docker run -d --name pa-test-db -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgis/postgis:18-3.7
export PAGERARCHIVE_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable'
go test ./...
go run ./cmd/pagerarchive validate-profile -v profiles/example
```

The acceptance tests wipe that database. CI runs only the checks a change affects ([ADR 0025](docs/adr/0025-ci-speed.md)); a branch never sets the version, as the Release workflow does that. Database code is generated with [sqlc](https://sqlc.dev) (`sqlc generate`) from `internal/store/queries.sql`.

The browser app is in `web/` (SvelteKit, Node 24):

```
cd web && npm ci && npm run check && npm run build   # build is embedded by the next go build
npx playwright install --with-deps chromium firefox webkit
cd .. && sh scripts/e2e.sh                           # browser tests against a real API and PostGIS
```

The account checks run against the full compose stack, with Authentik and a [Mailpit](https://mailpit.axllent.org) mail catcher (never a real mail server). They need Docker with compose and ports 8080, 9000 and 8025 free:

```
sh scripts/stack-checks.sh                 # A23-A28, A33 and the PowerShell module's A20, A21, A51-A55, A59 (needs pwsh)
sh scripts/stack-e2e.sh --project=chromium # section 11 steps 2 and 3 in a browser
sh scripts/a77.sh                          # A77 upgrade from the previous release
sh scripts/a78.sh --project=chromium       # A78 install with no checkout, then behind a proxy on an external network
sh scripts/a15.sh                          # A15 resource budget: one hour plus 10 idle minutes (CI: -m 5 -w 1 -i 2 -r 30000)
```

The PowerShell module is in `powershell/PagerArchive`, with its tests in `powershell/tests` (Pester 5.9.1 and PSScriptAnalyzer 1.25.0 on `PSModulePath`; `scripts/ci/Save-TestModules.ps1` saves them):

```
pwsh -NoProfile -File powershell/tests/Invoke-Tests.ps1   # analyzer, meta-tests, behaviour and sign-in tests
```

The example dataset and its tiny test map are generated: `go run scripts/example-map/main.go`, then `sh scripts/example-map/build-tiles.sh` (needs tippecanoe).

`npm run dev` serves the app with live reload and sends `/api` and `/auth` to an API on `127.0.0.1:8080`.

PagerArchive is for people interested in what's happening on their network. It is not a tool for harassment.

## Licence

[MIT](LICENSE)
