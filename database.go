package pagerarchive

import _ "embed"

// The database set-up the configuration archive holds (deploy/postgres), for
// the API and its tests: one copy of each, the one a deployment runs.

// LockedAccountsSQL makes pagerarchive_locked_accounts() in Authentik's
// database (R52, ADR 0006): run by the database's first-start script, by
// api-role.sh, and by the API while it connects as the superuser.
//
//go:embed deploy/postgres/locked-accounts.sql
var LockedAccountsSQL string

// APIRoleSQL makes the API's own role, pagerarchive, and hands it
// PagerArchive's objects (R84i, ADR 0033). Plain SQL, run as the superuser in
// PagerArchive's database; it sets no password.
//
//go:embed deploy/postgres/api-role.sql
var APIRoleSQL string
