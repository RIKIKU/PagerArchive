# PagerArchive

A self-hosted archive and live browser for POCSAG paging networks, received with inexpensive RTL-SDR dongles and decoded with `multimon-ng`.

- Many receivers, one server, with deduplication across receivers
- Live updates, multi-criteria search, job grouping and a map
- Network knowledge (page formats, redaction rules, ignored capcodes) in JSON **profiles**, so it works for any POCSAG network
- Sensitive details are removed **before** anything is stored
- Per-user integrations forward new pages to your own systems (a convenience, not a replacement for your pager)

**Status:** milestone M7 (invite trees in Administration, a preview before deactivating a tree with people spared by hand or by branch, trusted users, deactivation records) on top of M6 (aliases managed in the browser, with change requests, review and history), M5 (per-user integrations: webhook, MQTT with Home Assistant discovery, Discord, Telegram, Pushover and ntfy; Prometheus metrics; an Administration area) on top of M4 (map with local geocoding and self-hosted tiles, capcode reports, receiver liveness, retention and the nightly job, resource budget, images on GHCR), M3 (invite-only accounts through Authentik: invitations, emailed codes, roles, deactivation), M2 (browser UI, live updates, search, job and detail views) and M1 (client, ingest, redaction, deduplication, profiles, `validate-profile`). See [`SPEC.md`](SPEC.md).

- [Deploying the server](docs/deploy.md)
- [Integrations](docs/integrations.md)
- [Profiles and `validate-profile`](docs/profiles.md)
- [Receiving client](docs/client.md)
- [Architecture decisions](docs/adr/)

## Development

Needs Go (see `go.mod`) and, for the acceptance tests, PostgreSQL with PostGIS:

```
docker run -d --name pa-test-db -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgis/postgis:18-3.6
export PAGERARCHIVE_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable'
go test ./...
go run ./cmd/pagerarchive validate-profile -v profiles/example
```

The acceptance tests wipe that database. Database code is generated with [sqlc](https://sqlc.dev) (`sqlc generate`) from `internal/store/queries.sql`.

The browser app is in `web/` (SvelteKit, Node 24):

```
cd web && npm ci && npm run check && npm run build   # build is embedded by the next go build
npx playwright install --with-deps chromium firefox webkit
cd .. && sh scripts/e2e.sh                           # browser tests against a real API and PostGIS
```

The account checks run against the full compose stack, with Authentik and a [Mailpit](https://mailpit.axllent.org) mail catcher (never a real mail server). They need Docker with compose and ports 8080, 9000 and 8025 free:

```
sh scripts/stack-checks.sh                 # A23-A28 and A33 through Authentik's flow executor API
sh scripts/stack-e2e.sh --project=chromium # section 11 steps 2 and 3 in a browser
sh scripts/a15.sh                          # A15 resource budget: one hour plus 10 idle minutes (CI: -m 5 -w 1 -i 2 -r 30000)
```

The example dataset and its tiny test map are generated: `go run scripts/example-map/main.go`, then `sh scripts/example-map/build-tiles.sh` (needs tippecanoe).

`npm run dev` serves the app with live reload and sends `/api` and `/auth` to an API on `127.0.0.1:8080`.

PagerArchive is for people interested in what's happening on their network. It is not a tool for harassment.

## Licence

[MIT](LICENSE)
