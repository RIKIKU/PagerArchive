#!/bin/sh
# Creates Authentik's role and database in the shared PostgreSQL container
# (SPEC.md R40, ADR 0001 section 5). Part of the release's archive; runs once,
# when the data volume is new. PagerArchive's own database setup, the metrics
# role included, is the API's (R84).
set -eu
psql -v ON_ERROR_STOP=1 -v pw="$AUTHENTIK_DB_PASSWORD" --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
CREATE ROLE authentik LOGIN PASSWORD :'pw';
CREATE DATABASE authentik OWNER authentik;
SQL
