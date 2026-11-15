-- The API's own database role, pagerarchive (SPEC.md R84i, ADR 0033). Run as
-- the superuser, in PagerArchive's database (postgres), by the database's
-- first-start script (20-pagerarchive.sh) on a new install and by
-- api-role.sh, which switches an existing install over. Safe to run again:
-- each run ends with the same rights. Plain SQL, so the API's tests run it as
-- it is.
--
-- The role owns PagerArchive's tables, functions and types, and may create
-- new ones in the public schema, which its migrations need. It isn't a
-- superuser, can't create roles or databases, is in no other role (so not in
-- the ones that read or write the server's files or run programs), and can't
-- connect to any other database, Authentik's included. The password is set
-- by whoever runs this, never here.
--
-- The extensions PagerArchive's first migration names are created here, as
-- the superuser: PostGIS can only be created by one, and pg_trgm by a role
-- with CREATE on the database, which the API's isn't given.

DO $role$
DECLARE
	r record;
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive') THEN
		CREATE ROLE pagerarchive;
	END IF;
	ALTER ROLE pagerarchive LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT;
	FOR r IN
		SELECT g.rolname FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid
		WHERE m.member = 'pagerarchive'::regrole
	LOOP
		EXECUTE format('REVOKE %I FROM pagerarchive', r.rolname);
	END LOOP;

	-- PagerArchive's database: connect, and (below) create in its public schema.
	EXECUTE format('GRANT CONNECT, TEMPORARY ON DATABASE %I TO pagerarchive', current_database());

	-- No other database. CONNECT is PUBLIC's by default, so it's taken from
	-- PUBLIC and given back to the roles that need it: Authentik's own, and the
	-- locked-accounts metric's (R52) if it exists. Superusers need no grant.
	FOR r IN SELECT datname FROM pg_database WHERE datname <> current_database() LOOP
		EXECUTE format('REVOKE ALL ON DATABASE %I FROM PUBLIC, pagerarchive', r.datname);
	END LOOP;
	IF EXISTS (SELECT 1 FROM pg_database WHERE datname = 'authentik') THEN
		IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authentik') THEN
			GRANT CONNECT, TEMPORARY ON DATABASE authentik TO authentik;
		END IF;
		IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics') THEN
			GRANT CONNECT ON DATABASE authentik TO pagerarchive_metrics;
		END IF;
	END IF;
END
$role$;

CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
GRANT USAGE, CREATE ON SCHEMA public TO pagerarchive;

-- What earlier releases made as the superuser becomes the role's: every table,
-- view, sequence, function and type in the public schema, except an
-- extension's (PostGIS's spatial_ref_sys and functions stay the superuser's).
-- A table's indexes, and the sequences its identity and serial columns own,
-- follow the table.
DO $own$
DECLARE
	o record;
BEGIN
	FOR o IN
		SELECT c.oid, c.relkind FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		  AND c.relowner <> 'pagerarchive'::regrole
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		  AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('a', 'i')))
	LOOP
		EXECUTE format('ALTER %s %s OWNER TO pagerarchive',
			CASE o.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'f' THEN 'FOREIGN TABLE' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,
			o.oid::regclass);
	END LOOP;
	FOR o IN
		SELECT p.oid, p.prokind FROM pg_proc p
		WHERE p.pronamespace = 'public'::regnamespace
		  AND p.proowner <> 'pagerarchive'::regrole
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
	LOOP
		EXECUTE format('ALTER %s %s OWNER TO pagerarchive',
			CASE o.prokind WHEN 'p' THEN 'PROCEDURE' WHEN 'a' THEN 'AGGREGATE' ELSE 'FUNCTION' END,
			o.oid::regprocedure);
	END LOOP;
	-- Enums, domains, ranges (their multiranges follow) and composite types made
	-- on their own; a table's row type and an array type follow what they
	-- belong to.
	FOR o IN
		SELECT t.oid FROM pg_type t
		WHERE t.typnamespace = 'public'::regnamespace
		  AND t.typowner <> 'pagerarchive'::regrole
		  AND (t.typtype IN ('e', 'd', 'r') OR (t.typtype = 'c' AND (SELECT relkind FROM pg_class WHERE oid = t.typrelid) = 'c'))
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
	LOOP
		EXECUTE format('ALTER TYPE %s OWNER TO pagerarchive', o.oid::regtype);
	END LOOP;
END
$own$;
