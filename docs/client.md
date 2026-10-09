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

Keep the client's clock in sync with NTP. The server rejects receive times more than 60 seconds ahead of its own clock.

## Docker

Images aren't published yet (ADR 0002). Until then, build the image with `docker build -f Dockerfile.client -t ghcr.io/rikiku/pagerarchive-client .`

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

Install `rtl-sdr` and `multimon-ng` from the distribution, copy the `pagerarchive-client` binary (linux/arm64) to `/usr/local/bin`, and run it as a systemd service with the settings above in an `EnvironmentFile`. The client logs to the journal; leave `PRINT_PAGES` off so page text never reaches the journal.
