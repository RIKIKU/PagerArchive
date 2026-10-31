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
| `RADIO_FREQUENCY` | empty: read stdin | e.g. `148.8125M` |
| `RADIO_GAIN` | automatic | `rtl_fm` gain in dB |
| `RADIO_PPM` | `0` | Frequency correction |
| `POCSAG_BAUDS` | `512,1200,2400` | Baud rates to decode |
| `MULTIMON_MODE` | `alpha` | `multimon-ng -f` mode |
| `BUFFER_SIZE` | `1000` | Pages held **in memory** while the server is unreachable. The oldest are dropped when full; all are lost on restart |
| `PRINT_PAGES` | `false` | `true` prints decoded pages to the console for troubleshooting |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty: off | Where to send the client's telemetry (its traces, log lines and metrics: lines read, pages decoded and sent, failed sends, buffered and dropped pages, R74), e.g. the example monitoring stack's Alloy. The other standard `OTEL_*` settings apply; see [Monitoring](monitoring.md). Console page text (`PRINT_PAGES`) is never sent |

## Reaching the server

The client only posts to `SERVER_URL`. A receiver on the server's own network can use its LAN address. A **remote receiver** (at another site, on a mobile connection) needs to reach the server's **public URL**, through your reverse proxy with HTTPS. If the server stays LAN-only, give the remote receiver a VPN into the server's network (WireGuard, Tailscale or similar) and use the LAN address over it. The client only needs to reach the app (`/api/v1/pages`), not Authentik.

## The dongle: blacklist the DVB-T driver

Linux loads its DVB-T TV driver for RTL-SDR dongles as soon as one is plugged in, and then `rtl_fm` can't open it (`usb_claim_interface error -6`). On the machine the dongle is plugged into (the Docker host, for Docker), stop the driver from loading:

```sh
echo 'blacklist dvb_usb_rtl28xxu' | sudo tee /etc/modprobe.d/blacklist-rtl.conf
sudo modprobe -r dvb_usb_rtl28xxu   # or reboot
```

`rtl_test` should then find the dongle without errors.

The client runs `rtl_fm` **without squelch**, on purpose: a working dongle then always produces audio (hiss, between pages), so audio stopping means the dongle has stopped. The client passes that audio to `multimon-ng` itself and measures it on the way, and with telemetry on it reports its **radio health**: audio per second, its level, how long since audio last came, and `rtl_fm`'s errors by kind (docs/monitoring.md, "Radio health"). `rtl_fm`'s messages still go to the client's log as before.

## Clock

Keep the client's clock in sync with NTP. The server rejects receive times more than 60 seconds ahead of its own clock. The client then logs a warning (without page text) and drops the page, and `pagerarchive client list` shows the client as `clock ahead` until it next sends a page the server accepts.

## Docker

The image is published on GitHub's container registry for linux/amd64 and linux/arm64, and pulls without signing in: `ghcr.io/rikiku/pagerarchive-client` (`latest` follows `main`; releases are tagged with their version). To build it yourself: `docker build -f Dockerfile.client -t ghcr.io/rikiku/pagerarchive-client .`

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
