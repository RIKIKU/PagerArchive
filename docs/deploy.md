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
- **A profile directory** for your network (see [profiles](profiles.md)), with its **address dataset** and any **areas** files it names (a missing one is logged and left off the map; `validate-profile` catches it). Without one, the API uses the made-up example profile built into its image and warns on every start: its redaction rules aren't your network's, so details your network's rules would remove are stored (SPEC.md P1). Use it only to try PagerArchive out.
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

### Checking the files

Each release's files and images are attested by the release's own run (GitHub artifact attestations), so you can check they were built by this repository's Release workflow, from the commit the release's tag names, and not changed since. With the [GitHub CLI](https://cli.github.com/), download the archive instead of piping it into `tar`, then, with the release's version:

```sh
version=1.12.0
commit=$(gh api repos/RIKIKU/PagerArchive/commits/v$version --jq .sha)
check() {
  gh attestation verify "$1" --repo RIKIKU/PagerArchive \
    --signer-workflow RIKIKU/PagerArchive/.github/workflows/ci.yml \
    --source-ref refs/heads/main --source-digest "$commit" --deny-self-hosted-runners
}
check compose.yaml
check pagerarchive-config.tar.gz
check oci://ghcr.io/rikiku/pagerarchive:$version
```

Each succeeds (in a terminal it says `Verification succeeded!`), or fails with exit status 1 if the file was changed, or wasn't built by this repository's `ci.yml` (the workflow the Release workflow runs) on main, on GitHub's own runners, from that release's commit. The client's image is `oci://ghcr.io/rikiku/pagerarchive-client:$version`. Releases from before attestations were added (November 2026) have none.

The first start takes a minute or two while Authentik sets up its database and applies `deploy/authentik/blueprints/pagerarchive.yaml`. That file configures sign-in, enrolment, password reset, guessing protection and the app's access to Authentik's API. `docker compose ps` shows `db` as healthy, and the others as running. The app's sign-in works once Authentik has finished.

**Set up the first admin before the app is reachable from the internet.** Add both hostnames to your reverse proxy (below) with the LAN-only lines (`allow` your LAN, `deny all`) left in, so only machines on your network can reach them. If another proxy or a tunnel forwards internet traffic to yours, read [Behind another proxy or a tunnel](#behind-another-proxy-or-a-tunnel) first: without it, those lines let the internet in. Then:

1. **Create the first admin** from a machine on your LAN. Open the app and choose **Set up the first admin**. Only the address in `INITIAL_ADMIN_EMAIL` can finish, by entering the code emailed to it. It doesn't have to be in `ALLOWED_EMAIL_DOMAINS`. It must differ from `AUTHENTIK_ADMIN_EMAIL`, or this is refused ([why](#two-accounts-with-one-address)). Once an admin exists, this option disappears and the setting is ignored.
2. **Open the app to the internet**: remove the LAN-only lines from both hostnames' `location /` blocks. Leave them on Authentik's admin interface (`/if/admin/`), and check from outside (see [Checking it](#checking-it)).
3. **Invite people** from **Account**. Invitations go to addresses in `ALLOWED_EMAIL_DOMAINS` only, and work until the person joins, for 7 days. Opening the link uses nothing up, so a mail provider's link scanner can't spoil it. Each person sets a password and confirms their address with an emailed code. One invitation sends at most 5 code emails an hour, however often its link is opened, so a leaked link can't use up the deployment's mail quota. An invitation that expired, or whose email went astray, can be **resent** by its inviter or an admin: a new link for 7 days, and the old one stops working.
4. **Add receiving clients** with the CLI (see [the client](client.md)). `pagerarchive` on the server keeps only the jobs that need the server: `serve`, `validate-profile`, `client`, `service-account` and `version`. A client key can post pages, and pages can fire integrations, so creating one needs a shell on the server. Everything else an admin does from a command line goes through the [PowerShell module](powershell.md).

   ```sh
   docker compose exec api pagerarchive client add shed "Shed receiver"
   ```
5. **Service accounts, for a tool** (optional; R62a, ADR 0048). A tool that reads the archive (a script, a monitoring job or an agent) gets a read-only **service account**, owned by an admin and made on the server, never in the browser:

   ```sh
   docker compose exec api pagerarchive service-account create report-bot --owner you@example.org
   ```

   It prints the account's credential **once**: give it to the tool (with the PowerShell module, `Connect-PagerArchive -Uri https://pager.example.org -ServiceAccount (Get-Credential -UserName report-bot)`, pasting the credential as the password), and don't keep a copy. The credential expires after **90 days**: `pagerarchive service-account rotate report-bot` gives a new one and ends the old one at once, `pagerarchive service-account list` shows each account's owner, status and expiry, and `pagerarchive service-account revoke report-bot` ends it at once. A service account reads pages and what admins read, except people's details (users, emails, invite trees, invitations, alias requests), and changes nothing. It's in its owner's invite tree, so deactivating the tree deactivates it; it shows on **Users** marked "Service account", where admins can deactivate it, and deleting the owner's account revokes it. It needs no setting: the `pagerarchive-service` client comes with the Authentik blueprint.

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
- **Keep Authentik's admin interface LAN-only** in your proxy (`/if/admin/` in the example below; behind another proxy or a tunnel, see [below](#behind-another-proxy-or-a-tunnel)). Sign in at `AUTHENTIK_URL/if/admin/` from your LAN.
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

The proxy's HTTPS is also what lets people [install the app on their phones](phone.md). Don't cache the app's responses in the proxy: `/service-worker.js` must reach browsers fresh (the app sends it `no-cache`), so that an upgrade reaches installed apps.

Both must receive the original `Host` header and `X-Forwarded-Proto`. Authentik also gets `X-Forwarded-For` set to the address the request came from, never the client's own header passed on: Authentik trusts that header from private addresses, so a header passed on would let anyone choose the address Authentik records.

Authentik's **Base URL** (the address it's reached at) is `AUTHENTIK_URL`, unless `.env` sets `AUTHENTIK_WEB__BASE_URL`. Authentik copies it into its settings on a start when the setting is empty, and never overwrites it: if you later move Authentik to another address, change it under **System**, **Settings** in Authentik's admin interface too.

An example for nginx (for instance linuxserver/swag's `proxy-confs`), with the proxy reaching the host's ports. Behind a proxy on a Docker network, use `http://pagerarchive:8080` and `http://pagerarchive-authentik:9000` instead. Replace `192.168.0.0/16` with your LAN. Each block is complete, so don't also include SWAG's `proxy.conf` in these locations: it would send some headers twice. In `pager.example.org`'s server block:

<!-- proxy:app -->
```nginx
location / {
    allow 192.168.0.0/16;         # LAN only until the first admin is set up;
    deny all;                     # then remove these two lines
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_http_version 1.1;
    proxy_buffering off;          # live updates (the API also sends X-Accel-Buffering: no)
    proxy_read_timeout 1h;
}

# Metrics are scraped on the LAN at API_BIND, never through the proxy.
location /metrics {
    deny all;
}
```

In `auth.example.org`'s server block:

<!-- proxy:auth -->
```nginx
location / {
    allow 192.168.0.0/16;         # LAN only until the first admin is set up;
    deny all;                     # then remove these two lines
    proxy_pass http://127.0.0.1:9000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $remote_addr;
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
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

Blocking `/if/admin/` hides the admin pages from the internet. It isn't the only safeguard, because Authentik's API is shared with the pages users need. That's why akadmin also needs a second factor.

The `api` container reaches Authentik directly on the compose network (`AUTHENTIK_INTERNAL_URL`), so it doesn't depend on the proxy for that.

### Behind another proxy or a tunnel

The LAN-only lines work only when the internet reaches this proxy directly (for example, a port forwarded on your router to it). If something else on your network forwards internet traffic to it, such as a proxy in a DMZ or a tunnel's connector (Cloudflare Tunnel and the like), every request seems to come from that forwarder's address. When that address is in your LAN range, **every internet request passes the LAN-only lines**, and Authentik's admin interface and the first-admin set-up are open to the internet. Do one of these two:

**Block the paths where the internet comes in** (simplest). On the proxy the internet reaches first, refuse Authentik's admin interface and the metrics, before forwarding anything else as usual. In nginx:

<!-- proxy:front -->
```nginx
# In auth.example.org's server block on the front proxy:
location /if/admin/ {
    deny all;
}

# In pager.example.org's server block on the front proxy:
location /metrics {
    deny all;
}
```

Don't forward either hostname from the front proxy until the first admin is set up (step 2 above). A tunnel's connector does no blocking of its own: use its service's access rules for `/if/admin/` and `/metrics` instead, or the second way.

**Or tell this proxy who the forwarder is**, so it uses the client's real address. The forwarder must set a header with the client's address: an nginx front proxy with `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`, and Cloudflare sets `CF-Connecting-IP` itself. Then, in both hostnames' server blocks on this proxy, with the forwarder's own address in place of `192.168.1.10` (its address only, never a whole range):

<!-- proxy:real-ip -->
```nginx
set_real_ip_from 192.168.1.10;    # the forwarder, and nothing else
real_ip_header X-Forwarded-For;   # or CF-Connecting-IP behind Cloudflare
real_ip_recursive on;
```

and add `deny 192.168.1.10;` above each `allow` line, so a request the forwarder sends without the header is refused, not let in as the forwarder's. `$remote_addr` is then the client's address, so the LAN-only lines and Authentik's `X-Forwarded-For` above work as on a direct set-up.

### Checking it

From a machine outside your network (a phone off Wi-Fi will do), each of these must say `403`, and must still say it with `-H 'X-Forwarded-For: 192.168.1.20'` added:

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://auth.example.org/if/admin/
curl -s -o /dev/null -w '%{http_code}\n' https://pager.example.org/metrics
```

Before the first admin is set up, `https://pager.example.org/` must say `403` from outside too.

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
3. counts again the reports' jobs that those changed (R38a), so the reports follow;
4. vacuums the message and report tables.

It also deletes integrations' delivery records older than 30 days.

Shortening the period asks for the cut-off date to be confirmed. A run missed while the server was down is made up when it starts. The Retention page shows the last run's result.

Pages printed from [the print sheet](print-sheet.md) are outside PagerArchive's control: retention and deactivation don't reach paper.

## Two receivers and near duplicates

A second receiver hears the same pages with its own errors: a garbled character or three, a page cut short, a few junk characters at the end. Copies of a page to one capcode within 10 seconds, from different receivers, whose text is nearly the same are one message (R19a, ADR 0050); pages of different job numbers never are, nor two that one receiver heard (its second is another page, such as an alarm raised a level). The message keeps the copy that parses, or the longer of a truncated pair, or the first; the other is kept as that receiver's copy, with its own text, and shown under it on the message's page. A better copy that comes after the first replaces the message's text at once: nothing waits for it, and an integration already sent the page isn't sent it again.

`DEDUP_NEAR_RATIO` in `.env` is how many edits a copy may differ by, per character of the shorter copy (default `0.1`, at most `0.5`; `0` turns near duplicates off). To see how your receivers' copies differ, on your own archive, with counts only:

```sh
docker compose exec api pagerarchive dedup-report --since 7d
```

It counts the near duplicates kept (by how they differed, and which copy parses), the pages to one capcode within 10 seconds that were kept apart, with histograms of how far apart each pair was, and what other ratios would have done. It never prints a page's text or a capcode.

## Integrations

Users forward new pages to their own systems from **Integrations**: a webhook, MQTT (with Home Assistant discovery), Discord, Telegram, Pushover or ntfy. See [integrations](integrations.md) for what each needs and what is sent.

- **Set `INTEGRATION_SECRETS_KEY`** in `.env`: 32 random bytes in base64 (`openssl rand -base64 32`). It encrypts integrations' secrets (URLs, tokens, passwords) in the database, so a database dump or backup alone doesn't reveal them. Keep it with your other secrets, outside the database's backups. Without it, integrations are off and the API logs a warning. If it's lost, every owner enters their secrets again.
- **Changing the key:** set the new one as `INTEGRATION_SECRETS_KEY` and the old one as `INTEGRATION_SECRETS_KEY_OLD`, restart `api` (it seals every secret again with the new key and logs how many), then remove `INTEGRATION_SECRETS_KEY_OLD`.
- **Private addresses:** integrations can't reach private, loopback or link-local addresses (protection against server-side request forgery). Reserved ranges and IPv6 forms of these addresses (NAT64, 6to4, IPv4-compatible) are refused too, as are all local-use NAT64 (`64:ff9b:1::/48`) and Teredo (`2001::/32`) addresses. An admin allows specific hosts or ranges, such as your Home Assistant, in **Admin → Integration addresses**.
- Admins see each user's integrations on that user's page in **Admin → Users**, and can pause them, but never their secrets or where they send. The users list shows how many each user has and marks any paused for failing. Nothing lists every user's integrations. Deactivating a user stops their integrations; deleting an account deletes them.

## Metrics

`GET /metrics` on the `api` port gives Prometheus-format metrics (SPEC.md R52), for your own monitoring to alert on: each receiver's last-seen time and whether it's receiving (R39) or its clock is ahead, pages received by outcome, messages with each admin flag (counted in the background every minute, with when they were last counted in `pagerarchive_messages_flagged_counted_timestamp_seconds`: R52), integration deliveries by result (`delivered`, `failed`, `too_old`, `cancelled`), integrations by status, accounts locked by guessing protection, Authentik's sign-ins, failed sign-ins (by reason) and errors in the last 5 minutes, the last nightly run, and how far a re-parse has got (`pagerarchive_reparse_running`, `pagerarchive_reparse_state` (0 idle, 1 running, 2 paused, 3 stopped: R23c), `pagerarchive_reparse_run_messages` and `pagerarchive_reparse_run_messages_done` for the run going, or the last one, and `pagerarchive_reparse_messages_total`, whose rate is messages a minute: R23b), and the same for retroactive redaction's run (`pagerarchive_redaction_running`, `pagerarchive_redaction_state`, `pagerarchive_redaction_run_messages`, `pagerarchive_redaction_run_messages_done` and `pagerarchive_redaction_messages_total`: R45a). There is no page text in it, and receivers appear by client ID only.

- **Set `METRICS_TOKEN`** in `.env` (`openssl rand -base64 36 | tr -d '/+='`). Without it, `/metrics` is off. Scrapes need it as a bearer token.
- **Locked accounts** and **Authentik's sign-ins and errors** are counted in Authentik's database, through a role, `pagerarchive_metrics`, that can call two functions and read nothing else: only counts come out, never a username or an address. Set `AUTHENTIK_METRICS_DB_PASSWORD` in `.env`. On a new install the database makes the role on its first start. On an install whose API still connects as the superuser, the API makes it when it starts, or changes its password. Once the API has [its own role](#the-apis-database-role), run `deploy/postgres/api-role.sh` after setting or changing the password, and once after upgrading to the release that added the sign-ins function ([Monitoring](monitoring.md#an-install-that-already-runs-the-stack)). Without it, those metrics are left out.
- Scrape it on your LAN, at `API_BIND`. The [proxy example](#reverse-proxy) refuses `/metrics`, so it isn't reachable from the internet.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: pagerarchive
    authorization:
      credentials: <METRICS_TOKEN>
    static_configs:
      - targets: ["pager-host.lan:8080"]   # API_BIND
```

Alerts for a quiet receiver, for backups and for a database extension update waiting to be run by hand (R84c), for example: `pagerarchive_client_receiving == 0`, `pagerarchive_backup_failing == 1` and `pagerarchive_db_extension_update_pending == 1`.

The backups' state is there too: when the last good one was, whether they're failing, and the last good night's size and how long its dumps and check took (`pagerarchive_backup_last_good_bytes`, `_dump_seconds`, `_check_seconds`; see [Backups](#backups)).

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
- **TCP keepalives** on Authentik's database connections (the `sysctls` of Authentik's containers: Authentik deprecated its own `AUTHENTIK_POSTGRESQL__CONN_OPTIONS`), so a connection that died without being closed fails within about a minute, and Authentik reconnects.

To see whether Authentik is stuck, count tasks that have waited more than 5 minutes:

```sh
docker compose exec db psql -U postgres -d authentik -tAc \
  "SELECT count(*) FROM authentik_tasks_task WHERE state = 'queued' AND mtime < now() - interval '5 minutes'"
```

Anything above 0 means its worker isn't picking up work. Restart Authentik as above.

## The API's database role

The API connects to PostgreSQL as its own role, `pagerarchive`, not as the superuser (SPEC.md R84i, ADR 0033). The role owns PagerArchive's tables and may create new ones for its migrations, and nothing more: it isn't a superuser, can't make roles or databases, can't read or write the server's files or run programs, and can't connect to Authentik's database. So a flaw in the API can't reach Authentik's accounts or the database server itself. The backup service still connects as the superuser, as it dumps and restores both databases.

**A new install** gets the role from `.env`: set `PAGERARCHIVE_DB_PASSWORD` like the other secrets, letters and digits only, and leave `PAGERARCHIVE_DB_USER=pagerarchive` as it is. The database makes the role on its first start.

**An install from before the role** has neither setting, so its API connects as the superuser, as it always did, and logs a warning on every start: "the API connects to PostgreSQL as a superuser". It works as before, and upgrades (by hand or automatic) and rollbacks need nothing new. To switch it over, with the stack running and no upgrade under way, from the folder with `compose.yaml`:

```sh
deploy/postgres/api-role.sh
```

It counts every table's rows; then, in one transaction, makes the role, hands it PagerArchive's tables, functions and types, takes away what it mustn't have, and sets a new random password; signs in as the role and checks it can't do what it mustn't; writes `PAGERARCHIVE_DB_USER` and `PAGERARCHIVE_DB_PASSWORD` into `.env` (the password is never printed); recreates the API, and only the API; and checks that the API connects as the role and every table still has its rows. If it stops before `.env` is written, nothing has changed for the API; if the API doesn't come back as the role, it puts `.env` back and the API connects as the superuser again. With `AUTHENTIK_METRICS_DB_PASSWORD` set, it also makes the locked-accounts metric's role and function (see [Metrics](#metrics)), which the API can no longer make itself. It's safe to run again: on a switched install it checks everything and repairs what's missing, and changes nothing else.

To go back to the superuser: `deploy/postgres/api-role.sh undo`. That takes the two lines out of `.env` and recreates the API; the role stays, and a superuser needs nothing from it. An automatic upgrade's rollback to a release from before the role needs no undo: that release's `compose.yaml` ignores the two lines.

Once switched, the API can't update PostGIS's extensions after a newer `postgis/postgis` image (R84c): it logs a WARN naming each one and the fix, as in [Upgrading](#upgrading), and keeps working on the older version until you run it. That stays a hand step, run as the superuser: nothing runs it for you. So you aren't relying on spotting the WARN, `/metrics` has `pagerarchive_db_extension_update_pending{extension}`, 1 while an update is waiting, checked at start and every hour, and the [example monitoring stack](monitoring.md#alerts-and-where-they-go) alerts on it with **Database extension update pending**.

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

**If something goes wrong,** admins see it in **Administration**, and on **Administration → Backups**: the last backup failed, or the last good one is more than 36 hours old. `/metrics` has `pagerarchive_backup_last_good_timestamp_seconds` and `pagerarchive_backup_failing`, and the [example monitoring stack](monitoring.md#alerts-and-where-they-go) alerts on it. Its [backups dashboard](monitoring.md#the-dashboards) also graphs each night's size, dump time and check time, so you can see the check grow with the archive. `docker compose logs backup backup-check` says when each ran and how long it took.

**Back up now.** To back up before a change you might want to undo, such as an upgrade, or to try again after fixing something, an admin presses **Back up now** on **Administration → Backups**, or runs `Start-PagerArchiveBackup -Wait` in [PowerShell](powershell.md#backups). It's the same backup and check as the nightly one, and the page shows it asked for, running, then its result. The API doesn't run it: it writes a request file to the `backup-request` volume, which only it and the `backup` service mount, and the `backup` service, which looks every minute, does the backup and check and then deletes the request. So the API still never sees the dumps or Docker, and can't write the status the alerts read. One at a time: while one is asked for or running (the nightly one too), another is refused. The extra backup is that day's in the folder, under the same retention: it replaces one made earlier the same day, and after a good one the nightly backup doesn't run again that day (after a failed one, it does). On the server, `docker compose exec backup pagerarchive-backup now` does the same at once.

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

Everything is as it was that night, and people sign in with the passwords they had then. The restore gives the API's role and the locked-accounts metric's role their rights again, as the dumps hold none. Restoring an install whose API had [its own role](#the-apis-database-role) needs the old `.env`, with its `PAGERARCHIVE_DB_PASSWORD`; without it, run `deploy/postgres/api-role.sh` once the stack is up. It won't overwrite anything: if the install has tables in it, it stops and says so. It also refuses a night that failed its check, unless you add `--unchecked` after the date. Database migrations for a newer version run when the API starts, as for any upgrade, so a dump can go into a newer install, but not an older one.

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
| `X.Y.Z`, `X.Y`, `latest` | a release (`latest`: the latest one) |
| `main` | the newest build of `main`, which isn't a release: it reports a version such as `1.7.6-dev+3f2c1ab`, the next release's with `-dev+<commit>` |
| `sha-<commit>` | one exact build |

`compose.yaml` names PagerArchive's image, and upstream's PostGIS and Authentik images, by their major.minor tags (`pagerarchive:0.16`), so the minor version you run is the `compose.yaml` you have, and `docker compose pull` gets every patch release. Keeping up with patches is your job: nothing pulls them for you. Nothing about versions goes in `.env`.

## Upgrading

Releases come out weekly, at about 18:20 UTC on Fridays, when anything but the documentation changed that week, straight away for a security fix, and now and then in between. A build of `main` isn't a release.

**A patch release** (0.16.0 to 0.16.1) needs only these two commands, from the folder with `compose.yaml`. Run them regularly, as patches often carry security fixes:

```sh
docker compose pull && docker compose up -d
```

**A new minor or major version** (0.16 to 0.17) won't reach you through `docker compose pull`: your `compose.yaml` names the minor version you have (`pagerarchive:0.16`), so pulling keeps you on its patches. A minor release comes with new features, a database migration or changed configuration files, and you must download that release's `compose.yaml` and archive first, then pull and up. To see whether one is out, compare the [latest release](https://github.com/RIKIKU/PagerArchive/releases/latest) with the version `docker compose exec api pagerarchive version` reports. Upgrades can skip releases:

```sh
base=https://github.com/RIKIKU/PagerArchive/releases/latest/download
curl -fLO $base/compose.yaml
curl -fL $base/pagerarchive-config.tar.gz | tar -xz
docker compose pull && docker compose up -d
```

To check the new files and image first, see [Checking the files](#checking-the-files).

Or have the server do this every hour, with a backup first and a rollback if the upgrade fails: see [Automatic upgrades](#automatic-upgrades).

That's all: `.env` and `compose.override.yaml` stay as they are, and database migrations run when the API starts. A release can change a third-party service's files: `docker compose up -d` recreates that service, and leaves the others running. If the upgrade recreates `db`, Compose restarts Authentik too (see [When the database restarts](#when-the-database-restarts)).

A newer `postgis/postgis` image brings a newer PostGIS, but a database keeps the PostGIS version it was set up with until it's updated. The API does that on every start, after the migrations: each installed PostGIS extension that's behind the image is updated, and the log says so at INFO, with the old and new versions. If the log has a WARN saying it couldn't update one, usually because the API's database login doesn't own that extension, the API starts anyway on the older version: run `ALTER EXTENSION <name> UPDATE;` (the extension the WARN names) in the database as the extension's owner, for example `docker compose exec db psql -U postgres -c 'ALTER EXTENSION postgis UPDATE;'`. This is a hand step: after a newer database image, check the log or the [example monitoring stack](monitoring.md#alerts-and-where-they-go)'s **Database extension update pending** alert, which fires while one is waiting (`pagerarchive_db_extension_update_pending` is 1) and clears within the hour after you've run it, or at once if you restart the API. PostGIS 3.7 no longer ships `postgis_tiger_geocoder`, which older images installed in every new database: if a WARN says the image no longer provides it, it's left as it was. PagerArchive doesn't use it, so you can remove it the same way with `DROP EXTENSION postgis_tiger_geocoder;`.

Before version 1.0.0, a release can break the upgrade. If so, its release notes start with "Upgrade steps". Read the notes before you upgrade.

## Automatic upgrades

Instead of upgrading by hand, you can have the server do it. It's off unless you switch it on, and it's for a deployment installed as above, from the release's files (R84). It runs on the host, as the user who runs `docker compose` for PagerArchive, from that user's crontab, so it needs no root: the user only needs to be able to run Docker. PagerArchive itself never runs Docker or upgrades anything.

**What it does, every hour:** it asks GitHub's releases API (no account or token needed) for the newest full release, ignoring drafts and pre-releases. If that's newer than the version running, it upgrades, unless:

- it's a new major version, or its release notes start with "Upgrade steps": those you do by hand, as above;
- it failed here before: a failed release is never tried again by itself;
- a backup is running or asked for: it tries again the next hour.

The upgrade is five steps, each logged:

1. **Verify.** The release's `compose.yaml` and archive, and PagerArchive's images (the server's, and the client's if your `compose.override.yaml` names it by a major.minor tag), must carry this repository's attestations, as in [Checking the files](#checking-the-files). They're checked with a pinned release of the GitHub CLI, downloaded into `auto-upgrade/bin/` once and checked against that release's published checksum on every run, so the host needs nothing installed. The CLI needs no sign-in: the attestations come from GitHub's API and are checked against Sigstore's public keys. If anything can't be verified, or the CLI can't be fetched or doesn't match its checksum, nothing is installed, and it tries again the next hour.
2. **Back up.** The same backup and check as the nightly one (`pagerarchive-backup now`), kept aside in your backup folder under `pre-upgrade/` (hard links, so it costs no space while that night's files are still there), and copies of `compose.yaml`, `.env`, `compose.override.yaml` and `deploy/` in `auto-upgrade/files/`. The last 3 of each are kept.
3. **Upgrade.** The verified files are installed, a client tag like `pagerarchive-client:1.9` in `compose.override.yaml` moves to the new minor version, then `docker compose pull` (the images pulled must be the ones verified) and `docker compose up -d`. If you set `AUTO_UPGRADE_MONITORING_DIR`, the example monitoring stack is upgraded the same way, as in [Upgrading the stack](monitoring.md#upgrading-the-stack).
4. **Check.** Every container is running; the API answers and runs the new version, from the verified image (it answers only once its database migrations have run); your own addresses in `AUTO_UPGRADE_CHECK_URLS` answer; and a page is stored after the restart, within 30 minutes (`AUTO_UPGRADE_PAGE_WINDOW`). That last check counts only when pages were arriving: when the 2 hours before the upgrade (4 windows) held at least 20 pages, 5 a window on average, so a whole window with none would be very unusual. On a quieter network it's skipped, and the status says so: a quiet network is never a reason to roll back. Pages your receivers held while the API restarted count, as they arrive.
5. **If any of that fails, it rolls back.** The previous release's files go back, and so do the images that were running, without pulling anything. If the new version changed either database (PagerArchive's migrations, Authentik's, or a PostGIS extension's version), the database is first restored from step 2's backup, the documented way ([Restoring](#restoring)): the stack is stopped, the database's volume replaced by a new one, the backup restored into it, then the old version started. Pages stored between the upgrade and the rollback are lost in that case. The release is marked failed and isn't tried again.

**Switching it on.** From the folder with `compose.yaml`:

```sh
deploy/auto-upgrade/auto-upgrade.sh enable
docker compose up -d
deploy/auto-upgrade/auto-upgrade.sh run
```

`enable` adds `AUTO_UPGRADE_STATUS_DIR=./auto-upgrade/status` to `.env` (the folder whose status the API shows) and an hourly line to your crontab, at a random minute. `docker compose up -d` lets the API see the status. `run` does the first check at once, so you can see it work. With `enable --systemd` it's a systemd user timer instead of cron; that runs while you're logged out only with lingering (`loginctl enable-linger`, which may need an administrator). With `--no-schedule` it's neither, for your own scheduler to run `deploy/auto-upgrade/auto-upgrade.sh run` hourly.

The script comes in the release's archive, so a release from before automatic upgrades doesn't have it: upgrade by hand to one that does first.

| Setting in `.env` | Default | What it is |
|---|---|---|
| `AUTO_UPGRADE_STATUS_DIR` | none: off | Set by `enable`. Where the status and log are, which the API reads (and the example monitoring stack, if you set the same folder in its `.env`) |
| `AUTO_UPGRADE_PAGE_WINDOW` | `30` | Minutes after the restart in which a page must be stored, when pages were arriving before |
| `AUTO_UPGRADE_START_TIMEOUT` | `600` | Seconds the API may take to answer after the restart |
| `AUTO_UPGRADE_CHECK_URLS` | none | Your own addresses that must answer after an upgrade, space-separated, such as `https://pager.example.org/healthz https://auth.example.org/-/health/live/` |
| `AUTO_UPGRADE_MONITORING_DIR` | none | The example monitoring stack's folder, to upgrade it too. If it fails, both are rolled back |
| `AUTO_UPGRADE_KEEP` | `3` | How many pre-upgrade backups and copies of the files are kept |

**Seeing what it does.** **Administration → Upgrades** shows whether it's on, the last check and what it found, the last upgrade, the history, and the releases that failed. Administration's start page has a notice when the last upgrade failed (saying whether it was rolled back and the database restored), when one stopped part way, or when there's been no check for 3 hours. `/metrics` has `pagerarchive_auto_upgrade_failing`, `pagerarchive_auto_upgrade_last_check_timestamp_seconds` (when a run last checked for a release: its start, so during a run it's that run's start; the run's end doesn't move it), `pagerarchive_auto_upgrade_run{step}` (1 for what a run is doing now: `idle`, `checking`, `verifying`, `backing-up`, `upgrading`, `health-check` or `rolling-back`), while a run is going `pagerarchive_auto_upgrade_run_start_timestamp_seconds` (when it started) and, once it knows, `pagerarchive_auto_upgrade_target_info{to}` (the release it's upgrading to), `pagerarchive_auto_upgrade_last_success_timestamp_seconds` (with the versions), `pagerarchive_auto_upgrade_failures_total` and `pagerarchive_auto_upgrade_rollbacks_total`. The example monitoring stack shows them on its backups and upgrades dashboard, has each step's log line in Loki, and alerts on a failed upgrade with **Upgrade failed** ([Monitoring](monitoring.md#the-dashboards)). There's no other alerting. On the server, `deploy/auto-upgrade/auto-upgrade.sh status` prints the status, `auto-upgrade/status/auto-upgrade.log` has one JSON line per step, and `auto-upgrade/last-run.txt` has what the last run printed. None of them ever holds page text.

**When it needs you.** If a rollback fails too, the notice says where, and the stack is left as it is (stopped, if the restore failed, so the old version doesn't start on an empty database). The pre-upgrade backup is in the backup folder's `pre-upgrade/`, and the files are in `auto-upgrade/files/`, newest last: put the files back, restore the night as in [Restoring](#restoring), and start the stack. If the host stopped part way through an upgrade, the next run refuses to do anything until you've checked the install and run `deploy/auto-upgrade/auto-upgrade.sh clear`. Once you've looked, `clear` also removes the notice and the alert for a failed upgrade. To try a failed release again: `deploy/auto-upgrade/auto-upgrade.sh retry 1.10.0`.

**Checking a release by hand.** `deploy/auto-upgrade/auto-upgrade.sh verify` (or `verify v1.10.0`) checks the latest release's files and images as step 1 does, and changes nothing.

**Switching it off.** `deploy/auto-upgrade/auto-upgrade.sh disable` removes the crontab line (or the timer). Administration then says it's off. You can also remove `AUTO_UPGRADE_STATUS_DIR` from `.env`, then run `docker compose up -d`.

**Upgrading by hand still works**, whether it's on or off, exactly as in [Upgrading](#upgrading): the script only looks at the version that's running, so it carries on from whatever you installed. Switch it off first, or upgrade between its hourly runs: it never runs twice at once, and holds `auto-upgrade/lock` while it runs.

## Reports' counts

The reports under Reports (R38a-R38i) count jobs from tables of counts the API keeps beside the pages (ADR 0031). The first start of a release with them counts every stored job once, in the background: a few minutes for a large archive, during which the API serves as usual and the reports say their numbers are incomplete. Changing `TZ` counts them again, as a report's days and hours are the deployment's. After that, every new, re-parsed or deleted page is counted within seconds.
