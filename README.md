# PagerArchive

A self-hosted archive and live browser for POCSAG paging networks, received with inexpensive RTL-SDR dongles and decoded with `multimon-ng`.

- Many receivers, one server, with deduplication across receivers
- Live updates, multi-criteria search, job grouping and a map
- Network knowledge (page formats, redaction rules, ignored capcodes) in JSON **profiles**, so it works for any POCSAG network
- Sensitive details are removed **before** anything is stored

**Status:** milestone M1 (client, ingest, redaction, deduplication, profiles, `validate-profile`). No browser UI yet. See [`SPEC.md`](SPEC.md).

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

PagerArchive is for people interested in what's happening on their network. It is not a tool for harassment.

## Licence

[MIT](LICENSE)
