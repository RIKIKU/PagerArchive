# Receiving client

The client receives POCSAG pages and posts them to the server. Either it runs `rtl_fm` and `multimon-ng` itself (set `RADIO_FREQUENCY`), or it reads `multimon-ng` output on stdin.

## Getting an API key

Each client has its own key. On the server, in the API container (which has `DATABASE_URL` set):

```
docker exec <api-container> pagerarchive client add shed-receiver "Shed receiver"
```

The key is printed once. `pagerarchive client revoke <id>` revokes it immediately. `pagerarchive client list` shows each client's status, when it last heard a page, and the version it last said it runs (with since when it's been behind the server's, R14c).

## Versions

A client is **never newer than its server** (R14b): run the release your server runs, or an earlier one in the same major version. A server accepts pages from any earlier client of its major version, as what a client sends only gains fields within one. Each client tells the server its version with every page (its User-Agent, `pagerarchive-client/<version>`), so the server knows which clients are behind; the example monitoring stack shows it on the Clients dashboard and alerts with **Client behind the server** when one has run an older release than the server's for more than a day ([Monitoring](monitoring.md)). Upgrade the server first, then its clients: by hand, or with [automatic upgrades](#automatic-upgrades).

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `SERVER_URL` | (required) | Server base URL, e.g. `https://pager.example.test` |
| `CLIENT_ID` | (required) | The ID the key was issued for |
| `CLIENT_KEY` / `CLIENT_KEY_FILE` | (required) | The API key, or a file that holds it |
| `ALLOW_PLAIN_HTTP` | `false` | `true` stops the start-up warning about a plain `http://` `SERVER_URL` (below) |
| `RADIO_FREQUENCY` | empty: read stdin | e.g. `148.8125M` |
| `RADIO_GAIN` | automatic | `rtl_fm` gain in dB |
| `RADIO_PPM` | `0` | Frequency correction |
| `POCSAG_BAUDS` | `512,1200,2400` | Baud rates to decode |
| `MULTIMON_MODE` | `alpha` | `multimon-ng -f` mode |
| `RADIO_EXIT_AFTER` | `5` | Failed starts of the radio in a row before the client exits so that Docker (or systemd) starts it afresh, once the dongle has worked or been missing ([When the dongle drops out](#when-the-dongle-drops-out)); `0` never exits |
| `BUFFER_SIZE` | `1000` | Pages held **in memory** while the server is unreachable. The oldest are dropped when full; all are lost on restart |
| `PRINT_PAGES` | `false` | `true` prints decoded pages to the console for troubleshooting |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty: off | Where to send the client's telemetry (its traces, log lines and metrics: lines read, pages decoded and sent, failed sends, buffered and dropped pages, R74), e.g. the example monitoring stack's Alloy. The other standard `OTEL_*` settings apply; see [Monitoring](monitoring.md). Console page text (`PRINT_PAGES`) is never sent |

## Reaching the server

The client only posts to `SERVER_URL`. A receiver on the server's own network can use its LAN address. A **remote receiver** (at another site, on a mobile connection) needs to reach the server's **public URL**, through your reverse proxy with HTTPS. If the server stays LAN-only, give the remote receiver a VPN into the server's network (WireGuard, Tailscale or similar) and use the LAN address over it. The client only needs to reach the app (`/api/v1/pages`), not Authentik.

Over plain `http://` the client's key crosses the network unencrypted, and anyone on the way can read it and post pages as that receiver. So a client whose `SERVER_URL` is `http://` and not this machine (`localhost`, `127.0.0.1`, `[::1]`) logs a warning at every start. Use `https://` where you can. If the way to the server is your own network or a VPN, set `ALLOW_PLAIN_HTTP=true` to say so, and the warning stops.

## The dongle: blacklist the DVB-T driver

Linux loads its DVB-T TV driver for RTL-SDR dongles as soon as one is plugged in, and then `rtl_fm` can't open it (`usb_claim_interface error -6`). On the machine the dongle is plugged into (the Docker host, for Docker), stop the driver from loading:

```sh
echo 'blacklist dvb_usb_rtl28xxu' | sudo tee /etc/modprobe.d/blacklist-rtl.conf
sudo modprobe -r dvb_usb_rtl28xxu   # or reboot
```

`rtl_test` should then find the dongle without errors.

The client deliberately runs `rtl_fm` **with squelch (`-l`) off**: a working dongle then always produces audio (hiss, between pages), so audio stopping means the dongle has stopped. With squelch on, a quiet channel would be silent too, and squelch can clip the start of a page. The client passes that audio to `multimon-ng` itself and measures it on the way, and with telemetry on it reports its **radio health**: audio per second, its level, how long since audio last came, and `rtl_fm`'s errors by kind (docs/monitoring.md, "Radio health"). `rtl_fm`'s messages still go to the client's log as before.

## When the dongle drops out

A dongle can drop off USB for a moment (a loose cable, a hub losing power, interference) and come back a couple of seconds later. `rtl_fm` doesn't recover from that: it prints a burst of `cb transfer status: 5, canceling...` and then hangs, writing nothing and never exiting. So the client watches the pipeline and starts `rtl_fm` and `multimon-ng` again by itself (R13b) when:

- `rtl_fm` says the dongle is lost, missing or can't be opened (`cb transfer status`, `No supported devices found.`, `usb_claim_interface error`, `Failed to open rtlsdr device`, ...) and no audio comes for 2 seconds after it;
- no audio comes for **30 seconds**, whatever `rtl_fm` says. Squelch is off, so a working dongle sends audio all the time, even on a quiet channel: a pause of that length is always a fault. 30 seconds is far past any pause a working pipeline makes (audio comes every few milliseconds, and `rtl_fm` takes about a second to start), and a restart then still comes well before the 2-minute **Radio stalled** alert, so a hang that a restart fixes is over before anyone is told;
- `rtl_fm` or `multimon-ng` ends by itself.

It asks each program to stop, kills one that hasn't after 5 seconds (a hung `rtl_fm` can ignore the request), and waits for both, so nothing is left behind. While the dongle is missing it keeps trying, waiting 1 second, then 2, 4 and so on up to a minute between tries, and starting again at 1 second when the dongle comes back or has worked for 30 seconds. Its log says each time (`radio pipeline restarting`, with the reason, and `radio audio flowing again`), the restarts are counted by reason in its metrics, and **Radio silent for** keeps counting through them, so a dongle that doesn't come back still fires **Radio stalled** after 2 minutes ([Monitoring](monitoring.md#radio-health)).

**Why it sometimes exits.** Docker gives a container the USB devices that are there when it starts (`--device /dev/bus/usb`). A dongle that comes back gets a new device number, which a container that's still running can't open (`usb_open error -4`); a new start of the container can. So when a dongle that worked in this run can't be started again `RADIO_EXIT_AFTER` (5) times in a row (about 20 seconds), or one that was missing comes back but can't be opened 5 times in a row, the client logs `radio pipeline keeps failing to start` and exits with an error, and Docker's restart policy (`--restart unless-stopped`, as above) starts it afresh. It doesn't exit otherwise: a dongle that's missing from the start, or held by Linux's DVB-T driver, isn't helped by starting again, and exiting over and over would reset **Radio silent for**, and the alert would never fire. Run it with a restart policy (Docker) or `Restart=always` (systemd, below); without one, set `RADIO_EXIT_AFTER=0` and restart the client by hand after the dongle comes back.

A container can instead see a returning dongle without being started again if `/dev/bus/usb` is mounted rather than passed as devices, with access to USB devices allowed: `-v /dev/bus/usb:/dev/bus/usb --device-cgroup-rule 'c 189:* rmw'` in place of `--device /dev/bus/usb`. The client then gets it back on its next try. That gives the container every USB device on the host, so the plain `--device` above stays the default.

## Clock

Keep the client's clock in sync with NTP. The server rejects receive times more than 60 seconds ahead of its own clock. The client then logs a warning (without page text) and drops the page, and `pagerarchive client list` shows the client as `clock ahead` until it next sends a page the server accepts.

## Docker

The image is published on GitHub's container registry for linux/amd64 and linux/arm64, and pulls without signing in: `ghcr.io/rikiku/pagerarchive-client` (`latest` is the latest release, and each release is tagged with its version; `main` is the newest build of main, which reports a version such as `1.7.6-dev+3f2c1ab`). To build it yourself: `docker build -f Dockerfile.client -t ghcr.io/rikiku/pagerarchive-client .`

### Installing with Compose

Each [release](https://github.com/RIKIKU/PagerArchive/releases) attaches a client's files: `client-compose.yaml` (it names the client's image by that release's exact version), `client-env.example` and `client-auto-upgrade.sh`. On the client's host, in a folder of its own, get the files of **the release your server runs** (`pagerarchive version` on the server, or the Server version on the Clients dashboard), so the client isn't newer than its server:

```sh
mkdir ~/pagerarchive-client && cd ~/pagerarchive-client
v=<the server's version, e.g. 1.24.0>
for f in client-compose.yaml client-env.example client-auto-upgrade.sh; do
  curl -fLO "https://github.com/RIKIKU/PagerArchive/releases/download/v$v/$f"
done
# optional, with the GitHub CLI: each was built by this repository's release run, from that release's commit
for f in client-compose.yaml client-env.example client-auto-upgrade.sh; do
  gh attestation verify "$f" --repo RIKIKU/PagerArchive --signer-workflow RIKIKU/PagerArchive/.github/workflows/ci.yml \
    --source-ref refs/heads/main --deny-self-hosted-runners --source-digest "$(gh api repos/RIKIKU/PagerArchive/commits/v$v --jq .sha)"
done
mv client-compose.yaml compose.yaml && mv client-env.example .env && chmod 755 client-auto-upgrade.sh
nano .env                              # SERVER_URL, CLIENT_ID, RADIO_FREQUENCY and the rest (Settings, above)
(umask 077 && nano key)                # the client's API key, alone on its line
docker compose up -d
```

The key stays in the file `key`, readable by you only, which Compose mounts read-only into the client; it's never in `.env` or a command line. Compose maps the dongle (`/dev/bus/usb`) and keeps the logs small (`local` driver, 1 MB). `docker compose logs -f` shows them. To change a setting, edit `.env` and run `docker compose up -d` again. To upgrade by hand, get the files of the server's new release the same way (keeping your `.env` and `key`) and run `docker compose up -d`; or switch on automatic upgrades.

### Automatic upgrades

The client's host can upgrade the client itself, the way the server's automatic upgrades work ([Deploying](deploy.md#automatic-upgrades)), to the release its server runs (R84n). It's **off** until you switch it on, in the client's folder, as the user who runs `docker compose` there:

```sh
./client-auto-upgrade.sh enable              # hourly, at a fixed minute, from your crontab
./client-auto-upgrade.sh enable --systemd    # or a systemd user timer (needs lingering: loginctl enable-linger)
./client-auto-upgrade.sh run                 # check now
./client-auto-upgrade.sh status              # what it last did
./client-auto-upgrade.sh disable             # off again
```

Every hour it asks the server its version (`POST /api/v1/client/auto-upgrade`, with the client's own key) and finds the newest **full** release on GitHub (never a pre-release, a draft or a build of main) that **isn't newer than the server's**. It upgrades when that's newer than the client, in the same major version, not marked failed, and its release notes don't start with "Upgrade steps". So the client follows its server: when the server upgrades, the client does within the hour, and never before it.

1. **Verified first** (R84o): the release's `client-compose.yaml` and `client-auto-upgrade.sh`, and the client's image, by their attestations, signed by this repository's `ci.yml` on main, on a GitHub-hosted runner, from the commit the release's tag names, with a pinned GitHub CLI (and jq) it downloads into `auto-upgrade/bin/` for the host's architecture (amd64 or arm64) and checks against its published checksum on every run. Nothing is installed if verification can't run or fails; it tries again the next hour.
2. **Backed up**: copies of `compose.yaml`, `.env`, `compose.override.yaml` and the script in `auto-upgrade/files/` (mode 700; the last 3 are kept).
3. **Upgraded**: the verified files installed, `docker compose pull` and `up -d`; the image Compose runs must be the one verified.
4. **Checked**: the client runs the new version, from the verified image, and is still running a minute later (`AUTO_UPGRADE_HEALTH_WAIT`, 60 s), not restarted. A client that can't start, for its settings or its radio, restarts over and over, and that shows.
5. **Rolled back** if the upgrade or the check fails: the previous files and the image that was running go back, with nothing pulled, and the release is **marked failed**, so it isn't tried again until you run `./client-auto-upgrade.sh retry X.Y.Z`. A run that stopped part way (the host went down) leaves its state, and the next run does nothing until you check the client and run `./client-auto-upgrade.sh clear`.

Only one upgrade runs on a host at a time: every run holds the host-wide lock `/run/lock/archive-upgrade.lock`, the same one the server's automatic upgrades and a companion app's take (R84m), so a client on the server's own host never upgrades while the server does.

**Seeing what it does.** Each step writes one JSON line to `auto-upgrade/status/auto-upgrade.log`, and the status is in `auto-upgrade/status/status.json` (`./client-auto-upgrade.sh status`); the last cron run's output is in `auto-upgrade/last-run.txt`. Neither ever holds page text. At each step the script also tells the server, which keeps the last report (versions, known words, times and counts: not the notes) and shows it in its metrics and on the Clients dashboard: whether it's on, the last check and its result, a run going now with its step and target, and whether the last upgrade failed (R84p). A client whose host reports nothing for 3 hours shows its last check getting old there. The server's alert **Client behind the server** fires when a client stays behind for more than a day, upgraded automatically or not.

The client needs the server's version to upgrade, so a server from before client upgrades (it answers 404) is upgraded first; until then each check says it couldn't ask. The [Raspberry Pi install without Docker](#raspberry-pi-no-docker) isn't upgraded automatically: upgrade its binary by hand to the server's release.

### `docker run` instead

```
docker run -d --name pager-client --restart unless-stopped \
  --device /dev/bus/usb \
  -e SERVER_URL=https://pager.example.test -e CLIENT_ID=shed-receiver \
  -e CLIENT_KEY_FILE=/run/secrets/key -v ./key:/run/secrets/key:ro \
  -e RADIO_FREQUENCY=148.8125M \
  --log-driver local --log-opt max-size=1m \
  ghcr.io/rikiku/pagerarchive-client:<the server's version>
```

A client started this way isn't upgraded automatically: the script needs the Compose install above.

To replay captured decoder output instead of using a radio, leave `RADIO_FREQUENCY` unset and pipe the lines in with `docker run -i`.

> **Warning: page text and Docker logs.** The client never writes page text to a file. But with `PRINT_PAGES=true`, page text goes to the container's output, and Docker's default `json-file` logging driver **stores container output on disk** in clear text. Leave `PRINT_PAGES` off. If you turn it on, use `--log-driver none`, or at least limit logs with `--log-driver local --log-opt max-size=1m`, and turn it off again when you're done.

## Raspberry Pi (no Docker)

Each [release](https://github.com/RIKIKU/PagerArchive/releases) has the client as a single binary for linux/arm64 (Raspberry Pi 3, 4 and 5 with a 64-bit OS) and linux/amd64, with a `SHA256SUMS` file. Install `rtl-sdr` and `multimon-ng` from the distribution, blacklist the DVB-T driver (above), and install the binary:

```sh
sudo apt install rtl-sdr multimon-ng
curl -fLO https://github.com/RIKIKU/PagerArchive/releases/download/<version>/pagerarchive-client-<version>-linux-arm64
curl -fLO https://github.com/RIKIKU/PagerArchive/releases/download/<version>/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
# optional, with the GitHub CLI: it was built by this repository's release run, from that release's commit
gh attestation verify pagerarchive-client-<version>-linux-arm64 --repo RIKIKU/PagerArchive \
  --signer-workflow RIKIKU/PagerArchive/.github/workflows/ci.yml --source-ref refs/heads/main --deny-self-hosted-runners \
  --source-digest "$(gh api repos/RIKIKU/PagerArchive/commits/<version> --jq .sha)"
sudo install -m 755 pagerarchive-client-<version>-linux-arm64 /usr/local/bin/pagerarchive-client
```

Then run it as a systemd service with the settings above in an `EnvironmentFile` (readable by root only, since it holds the key), e.g. `/etc/systemd/system/pagerarchive-client.service`:

```ini
[Unit]
Description=PagerArchive receiving client
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/pagerarchive-client.env
ExecStart=/usr/local/bin/pagerarchive-client
Restart=always

[Install]
WantedBy=multi-user.target
```

The client logs to the journal; leave `PRINT_PAGES` off so page text never reaches the journal.
