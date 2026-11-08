# Deploying the server

The server stack runs with Docker Compose on a Linux host (amd64):

| Container | What it is |
|---|---|
| `api` | PagerArchive: the API, the browser app and the CLI |
| `db` | PostgreSQL with PostGIS. Holds PagerArchive's database and Authentik's |
| `authentik-server`, `authentik-worker` | [Authentik](https://goauthentik.io), which handles sign-in, invitations and emailed codes |
| `backup`, `backup-check` | Nightly dumps of both databases, and their check (see [Backups](#backups)) |

PagerArchive never sees passwords: people sign in on Authentik's pages, and Authentik sends the invitation and code emails. PagerArchive keeps the accounts, roles and who invited whom.

You don't need a copy of the source repository. A deployment is one folder holding the files attached to a [release](https://github.com/RIKIKU/PagerArchive/releases), your `.env`, and your own data.

## Before you start

- **Two hostnames** behind your reverse proxy, which handles TLS: one for the app (e.g. `pager.example.org`) and one for Authentik (e.g. `auth.example.org`).
- **An SMTP account** Authentik can send mail through.
- **Docker with Compose 2.24 or later.**
- **A profile directory** for your network (see [profiles](profiles.md)), with its **address dataset**. Without one, the API uses the made-up example profile built into its image and warns on every start: its redaction rules aren't your network's, so details your network's rules would remove are stored (SPEC.md P1). Use it only to try PagerArchive out.
- **A map extract** of your area (see [Map](#map)). Without one, the made-up example map built into the image is used.

## Install

Download three files from the latest release into an empty folder, unpack the archive, and fill in `.env`:

```sh
mkdir pagerarchive && cd pagerarchive
base=https://github.com/RIKIKU/PagerArchive/releases/latest/download
curl -fLO $base/compose.yaml
curl -fL -o .env $base/env.example
curl -fL $base/pagerarchive-config.tar.gz | tar -xz
chmod 600 .env
# fill in .env: URLs, allowed domains, the first admin's address, secrets, SMTP,
# and PROFILES_DIR and MAP_TILES_FILE if your profile and map are in this folder
docker compose up -d
```

| File | What it is |
|---|---|
| `compose.yaml` | The stack, naming PagerArchive, PostGIS and Authentik by their major.minor tags, so `docker compose pull` gets patch releases |
| `.env` | Your settings, from `env.example` |
| `deploy/` | From the archive: configuration for the third-party services, which run their upstream images unchanged. Don't edit these files, because an upgrade replaces them |

The first start takes a minute or two while Authentik sets up its database and applies `deploy/authentik/blueprints/pagerarchive.yaml`. That file configures sign-in, enrolment, password reset, guessing protection and the app's access to Authentik's API. `docker compose ps` shows `db` as healthy, and the others as running. The app's sign-in works once Authentik has finished.

**Set up the first admin before the app is reachable from the internet.** Add both hostnames to your reverse proxy (below) with the LAN-only lines (`allow` your LAN, `deny all`) left in, so only machines on your network can reach them. Then:

1. **Create the first admin** from a machine on your LAN. Open the app and choose **Set up the first admin**. Only the address in `INITIAL_ADMIN_EMAIL` can finish, by entering the code emailed to it. It doesn't have to be in `ALLOWED_EMAIL_DOMAINS`. It must differ from `AUTHENTIK_ADMIN_EMAIL`, or this is refused ([why](#two-accounts-with-one-address)). Once an admin exists, this option disappears and the setting is ignored.
2. **Open the app to the internet**: remove the LAN-only lines from both hostnames' `location /` blocks. Leave them on Authentik's admin interface (`/if/admin/`).
3. **Invite people** from **Account**. Invitations go to addresses in `ALLOWED_EMAIL_DOMAINS` only, and work until the person joins, for 7 days. Opening the link uses nothing up, so a mail provider's link scanner can't spoil it. Each person sets a password and confirms their address with an emailed code. One invitation sends at most 5 code emails an hour, however often its link is opened, so a leaked link can't use up the deployment's mail quota. An invitation that expired, or whose email went astray, can be **resent** by its inviter or an admin: a new link for 7 days, and the old one stops working.
4. **Add receiving clients** with the CLI (see [the client](client.md)). `pagerarchive` on the server keeps only the jobs that need the server: `serve`, `validate-profile`, `client` and `version`. A client key can post pages, and pages can fire integrations, so creating one needs a shell on the server. Everything else an admin does from a command line goes through the [PowerShell module](powershell.md).

   ```sh
   docker compose exec api pagerarchive client add shed "Shed receiver"
   ```

Admin screens are under the header's **Admin** link (admins only). Admins manage users on **Users**: they can deactivate someone, reactivate, and promote or demote admins. Each user's page shows their invite tree and the invitations they sent, which admins can resend or cancel. Deactivating a whole tree first shows a preview, where admins can spare anyone, or a whole branch, and give a reason. People marked **trusted** start spared. Each tree deactivation is recorded on the pages of the people in it. Deactivation signs people out at once.

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

Don't change PagerArchive's sign-in on Authentik's admin pages, because the blueprint is re-applied on start.

#### Two accounts with one address

`AUTHENTIK_ADMIN_EMAIL` and `INITIAL_ADMIN_EMAIL` must differ (SPEC.md R44c), as in `.env.example`: for example `authentik-admin@example.org` and `you@example.org`. Authentik's administrator's address needn't receive mail, so one that exists nowhere is fine.

Authentik's sign-in and password-reset forms take an address and find one account, the older one if two hold it. That is `akadmin`, whose account Authentik makes on its first start. If the first admin has the same address:

- the sign-in form says username `akadmin`, and the first admin's password always fails, even after they change it from inside a session;
- forgot-password says "no (allowed) MFA authenticator configured", because `akadmin` has no email device.

So the API asks Authentik on every start, and warns if another account holds `INITIAL_ADMIN_EMAIL`: "first admin's address is also held by another account in Authentik". While there's no admin yet, **Set up the first admin** is refused for the same reason.

**The fix: give `akadmin` a different address, in Authentik.** Changing `AUTHENTIK_ADMIN_EMAIL` in `.env` isn't enough, because Authentik only reads it when it first makes the account. Either run this on the server, with the new address in place of the example:

<!-- r44c:fix -->
```sh
docker compose exec authentik-worker ak shell -c "from authentik.core.models import User; u = User.objects.get(username='akadmin'); u.email = 'authentik-admin@example.org'; u.save()"
```

or sign in at `AUTHENTIK_URL/if/admin/` from your LAN, with the shared address and `AUTHENTIK_ADMIN_PASSWORD` (and your second factor, which the first sign-in asks you to set up), and change the address under **Directory**, **Users**, `akadmin`, **Edit**.

Then:

1. Put the same new address in `AUTHENTIK_ADMIN_EMAIL` in `.env`, so the file says what's true.
2. Run `docker compose restart api`, and check that `docker compose logs --since 1m api` doesn't show the warning.
3. Sign in from another browser with the first admin's address and password. If you've forgotten the password, forgot-password now works. From now on, `akadmin` signs in with its new address. The failed attempts counted against `akadmin`, so it may be locked for up to 15 minutes (below).

### Guessing protection

After 5 wrong passwords or emailed codes for one account, Authentik refuses further attempts for that account for 15 minutes (SPEC.md R44a), even if the sign-in is restarted or a new code is requested. People see "Too many wrong attempts for this account". A successful sign-in starts the count again. Because the lock is per account, someone who knows an address can lock that account for 15 minutes at a time. That's the accepted cost of stopping guessing.

## Reverse proxy

Forward each hostname to its container, or to `API_BIND` and `AUTHENTIK_BIND` on the host. A proxy in Docker can reach the containers directly over its own network (see [Your own changes](#your-own-changes)). Two things matter:

- **The app** streams live updates with Server-Sent Events, so don't buffer them, and allow long-lived responses.
- **Authentik** uses WebSockets.

Both must receive the original `Host` header and `X-Forwarded-Proto`.

An example for nginx (for instance linuxserver/swag's `proxy-confs`), with the proxy reaching the host's ports. Behind a proxy on a Docker network, use `http://pagerarchive:8080` and `http://pagerarchive-authentik:9000` instead. Replace `192.168.0.0/16` with your LAN:

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

## Your own changes

Don't edit `compose.yaml`, because an upgrade replaces it. Put what's particular to your site in a `compose.override.yaml` next to it. Docker Compose reads that file by itself, and an upgrade leaves it alone.

For example, if your reverse proxy runs in Docker on an existing network named `proxy`, this joins the app and Authentik to that network. The database stays off it. It also stops publishing their ports on the host, as the proxy no longer needs them:

<!-- a78:override -->
```yaml
services:
  api:
    ports: !reset []
    networks:
      default:
      proxy:
        aliases: [pagerarchive]
  authentik-server:
    ports: !reset []
    networks:
      default:
      proxy:
        aliases: [pagerarchive-authentik]

networks:
  proxy:
    external: true
```

Your proxy then forwards to `http://pagerarchive:8080` and `http://pagerarchive-authentik:9000`. Use those aliases rather than `api`: other stacks on a shared network can have a service with that name too. Keep `default` in each list, or the containers lose the database. `docker compose up -d` applies a changed override.

The [example monitoring stack](monitoring.md#starting-the-stack) joins the same way, with its own `monitoring` network. With both, `api` has all three networks under one `networks:`, and both networks are under the last.

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
- **Locked accounts** are counted in Authentik's database, through a role, `pagerarchive_metrics`, that can call one function and read nothing else. Set `AUTHENTIK_METRICS_DB_PASSWORD` in `.env`, and the API creates the role when it starts, or changes its password. Without it, that one metric is left out.
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

Alerts for a quiet receiver and for backups, for example: `pagerarchive_client_receiving == 0` and `pagerarchive_backup_failing == 1`.

The backups' state is there too: when the last good one was, and whether they're failing (see [Backups](#backups)).

Since M12, `/metrics` also has requests by route and status with their durations (and, for a scraper that asks for OpenMetrics, exemplars linking them to traces), the database pool, open live-update connections, and the Go runtime's memory and CPU. The names above are unchanged.

## Monitoring and telemetry

The API and the receiving clients can also send traces and logs (and the clients metrics) over OTLP, and the release includes a ready-made Grafana stack (Alloy, Tempo, Loki, Prometheus, Grafana) with dashboards and alerts for a client that stops, an API that's unreachable and a failing backup. It's off until you set `OTEL_EXPORTER_OTLP_ENDPOINT`. See [Monitoring](monitoring.md).

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

## Moving from an existing archive

[Importing messages](import.md) stores pages from an older archive while the app keeps running: `docker compose exec -T api pagerarchive import-messages - < pages.jsonl`. Add your aliases first, so the pages parse.

## Backups

Every night at `BACKUP_AT` (default `04:00`, in `TZ`, after the nightly run) the stack dumps PagerArchive's database and Authentik's into a folder, and keeps 14 days of them. There's nothing to install or schedule. Nothing leaves the host unless you copy it: see [Copying the folder off the host](#copying-the-folder-off-the-host).

| Setting in `.env` | Default | What it is |
|---|---|---|
| `BACKUP_DIR` | `./backups`, next to `compose.yaml` | The folder |
| `BACKUP_AT` | `04:00` | When each night's backup starts |
| `BACKUP_KEEP_DAYS` | `14` | How many days are kept. Older nights are deleted, but only after a good backup, so failures never use up the good ones |
| `BACKUP_UID`, `BACKUP_GID` | `0` | Who owns the files. They hold everything in both databases, so only that user and group can read them. Set `BACKUP_GID` to a group your file backup tool is in, or leave them if it runs as root |

A night is four files:

```text
2026-11-03.pagerarchive.dump
2026-11-03.authentik.dump
2026-11-03.counts.tsv       each table's rows when the dump was taken
2026-11-03.check            "ok", or what went wrong
```

**Safe to copy at any time.** Each file is written as `<name>.partial` and renamed when it's complete, so a file with one of those names is always whole. Tell your backup tool to skip `*.partial`, if it can. One it copies anyway is worth nothing and does no harm.

**Each night's dumps are checked.** The `backup-check` container has no network and no passwords. It restores the dumps into a throwaway database of its own and compares every table's row count with the counts taken when the dump was. It needs free disk space for that about the size of your database, for the length of the check, on Docker's disk (the `backup-check` volume).

**If something goes wrong,** admins see it in **Administration**, and on **Administration → Backups**: the last backup failed, or the last good one is more than 36 hours old. `/metrics` has `pagerarchive_backup_last_good_timestamp_seconds` and `pagerarchive_backup_failing`, and the [example monitoring stack](monitoring.md#alerts-and-where-they-go) alerts on it. `docker compose logs backup backup-check` says when each ran and how long it took. To try it now, or after fixing something: `docker compose exec backup pagerarchive-backup now`.

**What it costs,** at 10 years of pages (4,562,500, a 2.4 GB database), measured on a fast test disk: about 30 s for the dumps and 4 minutes for the check, at about 290 MB in the folder per night, so about 4 GB for 14 nights. A restore takes about 4 minutes. Memory that can't be given back stays under 250 MB during the check, and the file cache on top is given back when anything needs it. A spinning disk will take longer, by an amount not yet measured.

### What's in a backup

Everything in both databases: pages, aliases, templates and icons, integrations, users and their sign-ins. Redacted details are never stored, so they aren't in a backup either (SPEC.md P1). A dump does hold everything else, Authentik's password hashes included, so treat the folder as you would the database.

Not in a backup, so keep them yourself:

- **`.env`.** You need its secrets to restore. `INTEGRATION_SECRETS_KEY` and `AUTHENTIK_SECRET_KEY` above all: without the first, every integration's secrets have to be entered again. Keep it apart from the dumps, or the dumps' integration secrets are no longer protected by it.
- **Your profile and your map extract**, which you made and can make again.

Messages that retention has deleted can survive in backups, for as long as you keep them, here and in any copies.

### Restoring

This restores a chosen night into an **empty install**: a new folder made as in [Install](#install), with your old `.env`, or the same folder after `docker compose down -v`, which deletes the database and not the backup folder. Put the backups where `BACKUP_DIR` says, and don't start the stack first:

<!-- a80:restore -->
```sh
docker compose run --rm backup restore 2026-11-03
```

Without a date, it lists the nights. It starts the database, restores both dumps, and checks the row counts again. Then start everything:

```sh
docker compose up -d
```

Everything is as it was that night, and people sign in with the passwords they had then. It won't overwrite anything: if the install has tables in it, it stops and says so. It also refuses a night that failed its check, unless you add `--unchecked` after the date. Database migrations for a newer version run when the API starts, as for any upgrade, so a dump can go into a newer install, but not an older one.

### Copying the folder off the host

That's your job, and any tool that copies files will do. For example, to a NAS mounted at `/mnt/nas`, from cron on the host at 07:00, after the night's check:

```sh
0 7 * * * rsync -a --delete --exclude '*.partial' /opt/pagerarchive/backups/ /mnt/nas/pagerarchive-backups/
```

`--delete` makes the copy keep 14 days too. Leave it out to keep more there, and remember that deleted messages live on in whatever you keep.

## Images

PagerArchive's images are published to GitHub's container registry by CI, but only from a build whose checks all passed:

| Image | Architectures |
|---|---|
| `ghcr.io/rikiku/pagerarchive` | amd64 (the server) |
| `ghcr.io/rikiku/pagerarchive-client` | amd64, arm64 (receivers, e.g. a Raspberry Pi) |

| Tag | Is |
|---|---|
| `X.Y.Z`, `X.Y` | a release |
| `latest`, `main` | the newest build of `main` |
| `sha-<commit>` | one exact build |

`compose.yaml` names PagerArchive's image, and upstream's PostGIS and Authentik images, by their major.minor tags (`pagerarchive:0.16`), so the minor version you run is the `compose.yaml` you have, and `docker compose pull` gets every patch release. Keeping up with patches is your job: nothing pulls them for you. Nothing about versions goes in `.env`.

## Upgrading

**A patch release** (0.16.0 to 0.16.1) needs only these two commands, from the folder with `compose.yaml`. Run them regularly, as patches often carry security fixes:

```sh
docker compose pull && docker compose up -d
```

**A new minor or major version** (0.16 to 0.17) also needs that release's `compose.yaml` and archive first. Upgrades can skip releases:

```sh
base=https://github.com/RIKIKU/PagerArchive/releases/latest/download
curl -fLO $base/compose.yaml
curl -fL $base/pagerarchive-config.tar.gz | tar -xz
docker compose pull && docker compose up -d
```

That's all: `.env` and `compose.override.yaml` stay as they are, and database migrations run when the API starts. A release can change a third-party service's files: `docker compose up -d` recreates that service, and leaves the others running. If the upgrade recreates `db`, Compose restarts Authentik too (see [When the database restarts](#when-the-database-restarts)).

Before version 1.0.0, a release can break the upgrade. If so, its release notes start with "Upgrade steps". Read the notes before you upgrade.
