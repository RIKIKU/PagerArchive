# Receiving client

The client receives POCSAG pages and posts them to the server. Either it runs `rtl_fm` and `multimon-ng` itself (set `RADIO_FREQUENCY`), or it reads `multimon-ng` output on stdin.

## Getting an API key

Each client has its own key. On the server, in the API container (which has `DATABASE_URL` set):

```
docker exec <api-container> pagerarchive client add shed-receiver "Shed receiver"
```

The key is printed once. `pagerarchive client revoke <id>` revokes it immediately. `pagerarchive client list` shows each client's status and when it last heard a page.

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

```
docker run -d --name pager-client --restart unless-stopped \
  --device /dev/bus/usb \
  -e SERVER_URL=https://pager.example.test -e CLIENT_ID=shed-receiver \
  -e CLIENT_KEY_FILE=/run/secrets/key -v ./key:/run/secrets/key:ro \
  -e RADIO_FREQUENCY=148.8125M \
  --log-driver local --log-opt max-size=1m \
  ghcr.io/rikiku/pagerarchive-client
```

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
