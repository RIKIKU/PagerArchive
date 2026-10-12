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

The first start takes a minute or two while Authentik sets up its database and applies `deploy/authentik/blueprints/pagerarchive.yaml`. That file configures sign-in, enrolment, password reset and the app's access to Authentik's API. `docker compose ps` shows the containers as healthy when they're ready.

Then:

1. **Create the first admin.** Open the app and choose **Set up the first admin**. Only the address in `INITIAL_ADMIN_EMAIL` can finish, by entering the code emailed to it. It doesn't have to be in `ALLOWED_EMAIL_DOMAINS`. Once an admin exists, this option disappears and the setting is ignored.
2. **Invite people** from **Account**. Invitations go to addresses in `ALLOWED_EMAIL_DOMAINS` only, work once, and expire after 7 days. Each person sets a password and confirms their address with an emailed code.
3. **Add receiving clients** with the CLI (see [the client](client.md)):

   ```sh
   docker compose exec api pagerarchive client add shed "Shed receiver"
   ```

Admins manage users on **Users**: they can deactivate someone or their whole invite tree, reactivate, and promote or demote admins. Deactivation signs people out at once. Aliases and templates are managed with `pagerarchive alias …` and `pagerarchive template …` (`docker compose exec api pagerarchive help`).

Authentik's own administrator (`akadmin`, with the password from `.env`) is for you as the operator, at `AUTHENTIK_URL`. It isn't a PagerArchive account. Change PagerArchive's sign-in through the blueprint, not Authentik's admin pages, because the blueprint is re-applied on start.

## Reverse proxy

Forward each hostname to its container: the app to `api:8080` and Authentik to `authentik-server:9000`, or to `API_BIND` and `AUTHENTIK_BIND` on the host. Two things matter:

- **The app** streams live updates with Server-Sent Events, so don't buffer them, and allow long-lived responses.
- **Authentik** uses WebSockets.

Both must receive the original `Host` header and `X-Forwarded-Proto`.

An example for nginx (for instance linuxserver/swag's `proxy-confs`):

```nginx
# pager.example.org
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_buffering off;          # live updates (the API also sends X-Accel-Buffering: no)
    proxy_read_timeout 1h;
}

# auth.example.org
location / {
    proxy_pass http://127.0.0.1:9000;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

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
