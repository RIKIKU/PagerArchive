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
- **A profile directory** for your network (see [profiles](profiles.md)). Without one, the made-up example profile is used.

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
4. **Add receiving clients** with the CLI (see [the client](client.md)):

   ```sh
   docker compose exec api pagerarchive client add shed "Shed receiver"
   ```

Admins manage users on **Users**: they can deactivate someone or their whole invite tree, reactivate, and promote or demote admins. Deactivation signs people out at once. Aliases and templates are managed with `pagerarchive alias …` and `pagerarchive template …` (`docker compose exec api pagerarchive help`).

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
- Authentik is set up not to contact third parties: avatars show initials only, and analytics, error reporting and update checks are off.

## Backups

Back up the `db` volume, which holds both databases. Messages that PagerArchive deletes (retention) can survive in your own backups.

## Upgrading

```sh
git pull && docker compose pull && docker compose up -d --build
```

Database migrations run when the API starts. Before moving Authentik to a new version, run `scripts/stack-checks.sh` from the repo against that version.
