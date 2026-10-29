# Deploying the server

The server stack runs with Docker Compose on a Linux host (amd64):

| Container | What it is |
|---|---|
| `api` | PagerArchive: the API, the browser app and the CLI |
| `db` | PostgreSQL with PostGIS. Holds PagerArchive's database and Authentik's |
| `authentik-server`, `authentik-worker` | [Authentik](https://goauthentik.io), which handles sign-in, invitations and emailed codes |

PagerArchive never sees passwords: people sign in on Authentik's pages, and Authentik sends the invitation and code emails. PagerArchive keeps the accounts, roles and who invited whom.

## Before you start

- **Two hostnames** behind your reverse proxy, which handles TLS: one for the app (e.g. `pager.example.org`) and one for Authentik (e.g. `auth.example.org`).
- **An SMTP account** Authentik can send mail through.
- **A profile directory** for your network (see [profiles](profiles.md)), with its **address dataset**. Without one, the made-up example profile is used.
- **A map extract** of your area (see [Map](#map)). Without one, the made-up example map is used.

## Configure and start

```sh
cp .env.example .env
# fill in .env: URLs, allowed domains, the first admin's address, secrets, SMTP
docker compose up -d
```

The first start takes a minute or two while Authentik sets up its database and applies `deploy/authentik/blueprints/pagerarchive.yaml`. That file configures sign-in, enrolment, password reset, guessing protection and the app's access to Authentik's API. `docker compose ps` shows the containers as healthy when they're ready.

**Set up the first admin before the app is reachable from the internet.** Add both hostnames to your reverse proxy (below) with the LAN-only lines (`allow` your LAN, `deny all`) left in, so only machines on your network can reach them. Then:

1. **Create the first admin** from a machine on your LAN. Open the app and choose **Set up the first admin**. Only the address in `INITIAL_ADMIN_EMAIL` can finish, by entering the code emailed to it. It doesn't have to be in `ALLOWED_EMAIL_DOMAINS`. Once an admin exists, this option disappears and the setting is ignored.
2. **Open the app to the internet**: remove the LAN-only lines from both hostnames' `location /` blocks. Leave them on Authentik's admin interface (`/if/admin/`).
3. **Invite people** from **Account**. Invitations go to addresses in `ALLOWED_EMAIL_DOMAINS` only, work once, and expire after 7 days. Each person sets a password and confirms their address with an emailed code.
4. **Add receiving clients** with the CLI (see [the client](client.md)). `pagerarchive` on the server keeps only the jobs that need the server: `serve`, `validate-profile`, `client` and `version`. A client key can post pages, and pages can fire integrations, so creating one needs a shell on the server. Everything else an admin does from a command line goes through the [PowerShell module](powershell.md).

   ```sh
   docker compose exec api pagerarchive client add shed "Shed receiver"
   ```

Admin screens are under the header's **Admin** link (admins only). Admins manage users on **Users**: they can deactivate someone, reactivate, and promote or demote admins. Each user's page shows their invite tree. Deactivating a whole tree first shows a preview, where admins can spare anyone, or a whole branch, and give a reason. People marked **trusted** start spared. Each tree deactivation is recorded on the pages of the people in it. Deactivation signs people out at once.

### Templates and icons

Admins manage templates on **Admin → Templates** (or with the [PowerShell module](powershell.md)): name, agency, icon, colour and agency profile. Changes take effect at once for every alias using the template. Choosing another agency profile re-parses the template's pages in the background. A template that aliases still use can't be deleted. Users choose from the templates when they ask for an alias, and can't change them.

A template's **icon** is an image stored in PagerArchive's database, never a link to another site, so browsers fetch icons only from PagerArchive. Upload a PNG, ICO, GIF, JPEG or WebP file of at most 100 KB on the template's page; SVG files aren't accepted. PagerArchive checks the file by its contents and stores it as a 64×64 PNG, keeping nothing else from the file. The icon shows beside the alias name on every message, on a light tile so dark artwork shows in dark mode too, with the agency's name as its text alternative. With the module, `Set-PagerArchiveTemplate -Uri` downloads an image on your computer and uploads it; the server never fetches anything itself.

### Aliases

From M6 the database is the source of truth for aliases (ADR 0007).

- **Admins** add, edit and delete aliases on **Admin → Aliases**. Changes take effect at once. Each alias's page shows its history: what changed, who asked for it, and who made or approved it. Only admins see history, because it names people.
- **Everyone else** asks for a new alias, a change or a deletion: from an alias's page (**Request a change**, **Request deletion**), from a capcode with no alias, or on **Requests**. A request can carry a note. People follow their requests on **Requests**, and can edit or withdraw them while they wait.
- **Admin → Alias requests** lists the waiting requests, with a count on the Admin link. Each shows the current value beside the proposed one, and is marked when the alias has changed since it was requested. **Approve** applies the request exactly as it was made. **Reject** needs feedback for the person who asked.
- Deleting an alias closes its waiting requests ("alias deleted"). Integrations that selected an alias for one capcode keep that capcode. Deactivating or deleting someone withdraws their waiting requests.
- **Import** with the [PowerShell module](powershell.md): `Import-Csv aliases.csv | Import-PagerArchiveAlias`. It only adds aliases for capcodes and patterns that have none, and lists how existing ones differ from the file. `-Update` applies those differences, recorded in each alias's history as made by the admin who ran it. Rows for one capcode that differ stop the whole import; leave rows out in the pipeline and run it again. `-WhatIf` is the dry run. Unknown agency profile names in `-AgencyProfile` refuse the import.
- **Export** with **Admin → Aliases → Export CSV**, or `Get-PagerArchiveAlias | Export-Csv aliases.csv`. Both are in the import format, with the template's name in the `agency` column (or `Template`), so importing it back reports no differences. In the browser's export, values that a spreadsheet would run as a formula are written with a leading `'`, which the import removes, and the `icon` column is empty: icons are images, added to templates (R30).

### Authentik's administrator

Authentik's own administrator (`akadmin`, signing in with `AUTHENTIK_ADMIN_EMAIL` and the password from `.env`) is for you as the operator. It isn't a PagerArchive account, and it can change everything in Authentik, so it is protected three ways:

- **A second factor is required.** The first time akadmin signs in, Authentik asks you to set up an authenticator app (any TOTP app). Every later sign-in asks for its code. In Authentik's user settings you can add a passkey or recovery codes as well. PagerArchive users are never asked for this.
- **Keep Authentik's admin interface LAN-only** in your proxy (`/if/admin/` in the example below). Sign in at `AUTHENTIK_URL/if/admin/` from your LAN.
- **Guessing protection** applies to akadmin as to everyone (below).

Change PagerArchive's sign-in through the blueprint, not Authentik's admin pages, because the blueprint is re-applied on start.

### Guessing protection

After 5 wrong passwords or emailed codes for one account, Authentik refuses further attempts for that account for 15 minutes (SPEC.md R44a), even if the sign-in is restarted or a new code is requested. People see "Too many wrong attempts for this account". A successful sign-in starts the count again. Because the lock is per account, someone who knows an address can lock that account for 15 minutes at a time. That's the accepted cost of stopping guessing.

## Reverse proxy

Forward each hostname to its container: the app to `api:8080` and Authentik to `authentik-server:9000`, or to `API_BIND` and `AUTHENTIK_BIND` on the host. Two things matter:

- **The app** streams live updates with Server-Sent Events, so don't buffer them, and allow long-lived responses.
- **Authentik** uses WebSockets.

Both must receive the original `Host` header and `X-Forwarded-Proto`.

An example for nginx (for instance linuxserver/swag's `proxy-confs`). Replace `192.168.0.0/16` with your LAN:

```nginx
# pager.example.org
location / {
    allow 192.168.0.0/16;         # LAN only until the first admin is set up;
    deny all;                     # then remove these two lines
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_buffering off;          # live updates (the API also sends X-Accel-Buffering: no)
    proxy_read_timeout 1h;
}

# auth.example.org
location / {
    allow 192.168.0.0/16;         # LAN only until the first admin is set up;
    deny all;                     # then remove these two lines
    proxy_pass http://127.0.0.1:9000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}

# Authentik's admin interface: always LAN only.
location /if/admin/ {
    allow 192.168.0.0/16;
    deny all;
    proxy_pass http://127.0.0.1:9000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

Blocking `/if/admin/` hides the admin pages from the internet. It isn't the only safeguard, because Authentik's API is shared with the pages users need. That's why akadmin also needs a second factor.

The `api` container reaches Authentik directly on the compose network (`AUTHENTIK_INTERNAL_URL`), so it doesn't depend on the proxy for that.

## Privacy

- No page text is ever written to the logs (SPEC.md P1).
- **Authentik logs rejected refresh tokens.** When the PowerShell module, or anyone, presents a refresh token that has expired, been revoked or been replaced, Authentik writes a warning (`Refresh token does not exist`, `is expired` or `is revoked`) that includes the token's value. Such a token no longer works, but treat Authentik's log as sensitive: keep it to the server, and don't paste it into issues. PagerArchive's own log never contains a token.
- Integrations send pages where their owners choose (below). With **Full** content that includes the page text, so Discord, Telegram, Pushover and ntfy default to **Summary**, which has no text or address.
- Authentik is set up not to contact third parties: avatars show initials only, and analytics, error reporting and update checks are off.

## Map

The map (R35) is drawn from a **PMTiles** vector extract that the API serves itself, using HTTP range requests. There's no tile server, and nobody else sees what users look at. Fonts and icons are built into the image. Make an extract of your area once, with the [`pmtiles` command](https://github.com/protomaps/go-pmtiles/releases) and a daily OpenStreetMap build from Protomaps:

```sh
# --bbox is min lon, min lat, max lon, max lat (here, around London). A region
# the size of a state at zoom 15 is a few hundred MB to a few GB.
pmtiles extract https://build.protomaps.com/20261001.pmtiles map.pmtiles --bbox=-0.6,51.2,0.4,51.8 --maxzoom=15
```

Then set `MAP_TILES_FILE=/path/to/map.pmtiles` in `.env`. The credit line comes from the extract (`© OpenStreetMap`), and `MAP_ATTRIBUTION` overrides it. Refresh the extract whenever you like and restart `api`. The extract's maximum zoom sets how closely users can zoom in with detail; 15 is plenty for street level.

Browsers without WebGL can't draw the map. The map page tells them so, and still lists the located messages.

## Retention and the nightly run

Admins set how long messages are kept, in days, on the Retention page (default 3,650, minimum 30). A nightly run at `NIGHTLY_AT` (default `03:00`, in `TZ`):

1. deletes messages sent before the cut-off date (the run's date minus the retention period), with everything linked to them;
2. applies changed redaction rules to stored messages and re-parses the ones that changed (P1);
3. vacuums the message tables.

It also deletes integrations' delivery records older than 30 days.

Shortening the period asks for the cut-off date to be confirmed. A run missed while the server was down is made up when it starts. The Retention page shows the last run's result.

Pages printed from [the print sheet](print-sheet.md) are outside PagerArchive's control: retention and deactivation don't reach paper.

## Integrations

Users forward new pages to their own systems from **Integrations**: a webhook, MQTT (with Home Assistant discovery), Discord, Telegram, Pushover or ntfy. See [integrations](integrations.md) for what each needs and what is sent.

- **Set `INTEGRATION_SECRETS_KEY`** in `.env`: 32 random bytes in base64 (`openssl rand -base64 32`). It encrypts integrations' secrets (URLs, tokens, passwords) in the database, so a database dump or backup alone doesn't reveal them. Keep it with your other secrets, outside the database's backups. Without it, integrations are off and the API logs a warning. If it's lost, every owner enters their secrets again.
- **Changing the key:** set the new one as `INTEGRATION_SECRETS_KEY` and the old one as `INTEGRATION_SECRETS_KEY_OLD`, restart `api` (it seals every secret again with the new key and logs how many), then remove `INTEGRATION_SECRETS_KEY_OLD`.
- **Private addresses:** integrations can't reach private, loopback or link-local addresses (protection against server-side request forgery). Reserved ranges and IPv6 forms of these addresses (NAT64, 6to4, IPv4-compatible) are refused too, as are all local-use NAT64 (`64:ff9b:1::/48`) and Teredo (`2001::/32`) addresses. An admin allows specific hosts or ranges, such as your Home Assistant, in **Admin → Integration addresses**.
- Admins see each user's integrations on that user's page in **Admin → Users**, and can pause them, but never their secrets or where they send. The users list shows how many each user has and marks any paused for failing. Nothing lists every user's integrations. Deactivating a user stops their integrations; deleting an account deletes them.

## Metrics

`GET /metrics` on the `api` port gives Prometheus-format metrics (SPEC.md R52), for your own monitoring to alert on: each receiver's last-seen time and whether it's receiving (R39) or its clock is ahead, pages received by outcome, messages with each admin flag, integration deliveries by result (`delivered`, `failed`, `too_old`, `cancelled`), integrations by status, accounts locked by guessing protection, and the last nightly run. There is no page text in it, and receivers appear by client ID only.

- **Set `METRICS_TOKEN`** in `.env` (`openssl rand -base64 36 | tr -d '/+='`). Without it, `/metrics` is off. Scrapes need it as a bearer token.
- **Locked accounts** are counted in Authentik's database, through a role, `pagerarchive_metrics`, that can call one function and read nothing else. Set `AUTHENTIK_METRICS_DB_PASSWORD` in `.env` before the first start. Without it, that one metric is left out.
- Scrape it on your LAN, and keep `/metrics` out of your reverse proxy's public locations.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: pagerarchive
    authorization:
      credentials: <METRICS_TOKEN>
    static_configs:
      - targets: ["pager-host.lan:8080"]   # API_BIND
```

An alert for a quiet receiver, for example: `pagerarchive_client_receiving == 0`.

## When the database restarts

Authentik's workers can hang on database connections that went stale when the `db` container restarted. Sign-in pages may still load, but invitation and code emails stop going out. So whenever `db` restarts (an upgrade, a crash, or the host rebooting and starting containers in a different order), restart Authentik afterwards, from the directory with `compose.yaml`:

```sh
docker compose restart authentik-server authentik-worker
```

`compose.yaml` does two things to prevent the problem:

- **Compose restarts Authentik for you** whenever Compose itself restarts or recreates `db` (`docker compose up -d` after a new PostGIS image, or `docker compose restart db`). That's `restart: true` under `depends_on`, which needs Docker Compose 2.17 or later. It can't help when Docker restarts `db` on its own (after a crash) or when the host reboots. Run the command above then.
- **TCP keepalives** on Authentik's database connections (`AUTHENTIK_POSTGRESQL__CONN_OPTIONS`), so a connection that died without being closed fails within about a minute, and Authentik reconnects.

To see whether Authentik is stuck, count tasks that have waited more than 5 minutes:

```sh
docker compose exec db psql -U postgres -d authentik -tAc \
  "SELECT count(*) FROM authentik_tasks_task WHERE state = 'queued' AND mtime < now() - interval '5 minutes'"
```

Anything above 0 means its worker isn't picking up work. Restart Authentik as above.

## Backups

Back up the `db` volume, which holds both databases. Messages that PagerArchive deletes (retention) can survive in your own backups.

## Images

Images are published to GitHub's container registry by CI, but only from a build whose checks all passed:

| Image | Architectures |
|---|---|
| `ghcr.io/rikiku/pagerarchive` | amd64 (the server) |
| `ghcr.io/rikiku/pagerarchive-client` | amd64, arm64 (receivers, e.g. a Raspberry Pi) |

| Tag | Is |
|---|---|
| `latest`, `main` | the newest build of `main` |
| `X.Y.Z`, `X.Y` | a release |
| `sha-<commit>` | one exact build |

`compose.yaml` uses `latest` unless you set `PAGERARCHIVE_VERSION` (e.g. `PAGERARCHIVE_VERSION=sha-1a2b3c4` to pin one build).

## Upgrading

```sh
git pull && docker compose pull && docker compose up -d
```

Database migrations run when the API starts. If the upgrade recreates `db`, Compose restarts Authentik too (see [When the database restarts](#when-the-database-restarts)). Before moving Authentik to a new version, run `scripts/stack-checks.sh` from the repo against that version. That includes the locked-accounts metric, which reads Authentik's tables.

**From a version before integrations (M5)**, add to `.env`: `INTEGRATION_SECRETS_KEY`, `METRICS_TOKEN` and `AUTHENTIK_METRICS_DB_PASSWORD` (see [Integrations](#integrations) and [Metrics](#metrics)). Then create the metrics role once, after `docker compose up -d`:

```sh
docker compose exec db sh /docker-entrypoint-initdb.d/20-pagerarchive-metrics.sh
```

It's safe to run again, for example after changing the role's password.

**From a version before template icons (M10)**: the migration drops the old `icon` text column of templates, which held icon names from CSV imports that nothing used. Back up the database first if you want to keep them (`docker compose exec db pg_dump -U postgres -Fc postgres > before-m10.dump`). Add each template's icon afterwards on **Admin → Templates**.
