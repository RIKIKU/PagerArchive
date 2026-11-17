# ADR 0039: Told when a radio stops decoding

## Status

Proposed (2026-11-17). The owner: "add that alert ... let's start doing that now". SPEC.md R74b, with A118. On the branch `decode-alert` from main; the pull request is labelled `feature`. It adds to the telemetry decisions of ADR 0013 (R74a's "Radio stalled").

## Context

Twice on 2026-11-17 a production receiver kept giving audio (about 44 kB a second, its level normal) while its decoding collapsed, and nothing fired:

- **00:23 to 01:04 UTC**: from about 5 to 20 pages decoded in 5 minutes to 0 to 3, a few of them garbled. "Client not receiving" (R39: 30 minutes with no page of any kind) didn't fire, because a garbled page every so often reset it; "Radio stalled" (R74a) didn't, because audio flowed.
- **01:15 to 03:53 UTC**: a fixed `RADIO_GAIN` lowered the audio level by about 3.5 dB and the owner missed many pages.

The client already counts what it decodes (`pagerarchive_client_pages_decoded_total`, R74: each line multimon-ng gives that is a page) and the audio it gets (`pagerarchive_client_radio_audio_bytes_total`, R74a). What was missing is a rule that compares the first with what's normal for that client, with the audio still there.

A fixed number of pages a minute can't go in the rule: the repo is public and generic, and a network's traffic differs from another's by orders of magnitude, and within one network between 3 am and 3 pm.

## Decisions

### 1. "Radio not decoding": the client's last 30 minutes against its own usual for that half hour

`rules.yaml`, uid `pa-radio-not-decoding`, one alert per client (the `client` label, from `instance`, as "Radio stalled"):

- **`decoded`**: `increase(pagerarchive_client_pages_decoded_total[30m])` by client, only for clients whose `rate(pagerarchive_client_radio_audio_bytes_total[5m])` is over **40,000** bytes a second. rtl_fm runs at 22,050 samples a second, so a working dongle gives 44,100; 40,000 is that less 10 %. That isn't a number about the network: it's the client's own. A client with no audio is "Radio stalled"'s, and a client reading stdin has no audio series, so neither is this rule's.
- **`usual`**: the average of the same `increase(...[30m])` at `offset 1d`, `2d` ... `7d`, each day labelled and averaged by client (`or` of seven `label_replace`s), so a day the client has no numbers for (it didn't exist, or Prometheus has lost it) isn't counted as zero. With no day at all there is no usual, and no alert: a new client raises nothing for its first day.
- **The floor**: `usual >= 20`. With fewer than 20 usual pages in the half hour, a quarter of it is 5 or fewer, which chance gives too often to call a fault (for a Poisson count averaging 20, 4 or fewer is about 1 in 60,000; for one averaging 8, 1 or fewer is about 1 in 330, and real traffic is burstier than Poisson). So a quiet network, or a quiet hour of a busy one, is left to "Client not receiving".
- **`share`** = `decoded / usual`; the condition is a threshold on it: **under 0.25** to fire, and a recovery threshold (`unloadEvaluator`) of **over 0.5** to clear, so a client hovering near one line doesn't fire and clear over and over; **`for: 5m`**, so a 5-minute lull doesn't fire it.
- `noDataState: OK`: no share means no client with audio and a usual worth comparing (new, quiet, stalled, switched off), each another rule's job or nobody's. `execErrState: Error`, as the other client rules.

The window, the floor and the lines are settings, each marked THE THRESHOLD SETTING in the file (docs/monitoring.md, "Alerts, and where they go").

Rejected:

- **The client's average over the last day, whatever the hour.** Simpler, but traffic follows the day: on the history below, a night is often under a quarter of the day's average. In a simulation over the history below (1-minute steps, the same window, lines, floor and `for`), it fires 27 times besides 00:23 against 15 for the same half hour's average, for 1,442 minutes against 686, the extra mostly at night.
- **A 20-minute window.** In the same simulation it fires 2 minutes sooner on 00:23, and 21 other times against 15.
- **A floor in pages a minute**, or a fixed fall in pages: numbers about one network.
- **Requiring both a ratio and a statistical bound** (fewer than usual less 3 or 4 square roots of it): about the same firings as the floor of 20 on this history, and harder to read and to set.

### 2. Garbled pages count

The rule counts what the client decoded, garbled pages too. The server can tell a page that matched one of the profile's formats from one that matched none (`pagerarchive_messages_flagged{flag="parse_failed"}`), but:

- it's the number of stored messages flagged now, over all clients, not per client, so it can't be set against one client's decodes without new code in the API;
- on that receiver it went up by 0 to 3 an hour throughout, the two windows included: the garbled pages the owner saw mostly still matched a format, so the API couldn't tell them either;
- in the 00:23 window the decode count itself fell to a few pages in 30 minutes, which a few garbled pages don't hide: the rule is a share, not "any page", as R39 is.

So using the server's flag wouldn't be small, and on this history wouldn't have changed a firing. Not done.

### 3. On the dashboards

- **Clients**: "Pages decoded: the last 30 minutes against the usual", each client's 30-minute count, its usual and a dashed line at a quarter of it (the alert line), beside the audio panels: audio normal and the count at the dashed line is this alert at a glance.
- **Failures**: a square for **Radio not decoding** (clients under the line now, the rule's own query without its `for` and recovery line) and one for **Radio stalled**, which had none; both are in A115's list of panels that must run.

## The history check

Every number below is from the production Prometheus (read-only queries of the rule's own two expressions, at 1-minute steps, the rule's state then worked out with its thresholds, recovery line and `for`); numbers and times only, no page, capcode or address.

- Prometheus holds the receiver's metrics from **2026-11-09 00:11 UTC**; the rule has a usual from **2026-11-10 00:13**, so it could have run for **7.2 days**, to 2026-11-17 05:07.
- **00:23 to 01:04 UTC on 2026-11-17: it fires**, at **00:54** (31 minutes after decoding fell: the 30 minutes drop under a quarter at 00:49, then 5 minutes), until 01:06, when the client was restarted and decoding came back. The share at five-minute points: 0.97 at 00:35, 0.40 at 00:45, 0.25 at 00:50, 0.17 at 00:55, 0.14 at 01:00, against a usual of 33 to 40 pages in the half hour.
- **01:15 to 03:53 UTC (the gain): it doesn't fire, and can't.** The client decoded as many lines as usual or more (0.7 to 2.8 times its usual through the window): the low gain garbled pages and cost parts of them, but didn't lower the count, and the API's parse-failed flag didn't move either (decision 2). What did change is the audio level: about −11 dBFS for 8 days, −13 to −16 dBFS in that window. An alert on the level moving away from its own usual would see it; that's a separate decision for the owner, not made here.
- **Other firings over the 7.2 days: 14** (UTC, start and how long it fired, and the lowest share):
  2026-11-10 07:06 (19 min, 0.12), 20:30 (155 min, 0.00); 11-11 00:02 (10 min, 0.15), 00:22 (48 min, 0.21), 08:17 (13 min, 0.19), 09:03 (28 min, 0.10), 12:16 (3 min, 0.00), 19:08 (23 min, 0.00); 11-12 02:52 (15 min, 0.06), 03:20 (225 min, 0.00), 09:10 (83 min, 0.05), 21:24 (31 min, 0.00); 11-13 19:18 (11 min, 0.09); 11-14 09:58 (13 min, 0.19).
  Prometheus alone can't say which were faults: there's no second receiver in it. But most look like 00:23, not like quiet: audio normal throughout, and whole half hours with no page at all at times of day when this client usually decodes 20 to 50, for hours at a time (2026-11-10 20:30 to 23:06, 2026-11-12 03:20 to 07:06, with two client restarts at 03:17 on 11-12). The short ones at 0.15 to 0.21 are the likeliest to be quiet spells. Checking those times against another receiver (or Pagermon) would settle it.
- With the rule's own queries: without `for: 5m` it would have fired 25 times in all; with a floor of 16 instead of 20, 21.

## Consequences

- A client that is new, or on a network with fewer than about 40 pages an hour, isn't watched by this rule (docs/monitoring.md says how to lengthen the window for a quiet network). "Client not receiving" still catches silence.
- It fires about 30 minutes after decoding collapses, the same order as "Client not receiving" for silence; a shorter window fires sooner and more often.
- A change of daylight-saving time shifts the usual by an hour for a week; a network's traffic changing for good (a new pager group) moves the usual with it within a week.
- The test stack's Prometheus takes samples up to 9 days old (`deploy/monitoring/test/prometheus-test.yml`, `out_of_order_time_window`), and publishes its port on 127.0.0.1, both for A118 only: a deployment's doesn't. A118 writes its made-up clients' numbers straight into Prometheus with remote write (protobuf and snappy, built in the test with `protowire` and snappy's literal-only form, so no new module), because Alloy's OTLP path takes only current samples: a test client can then have days of history, and have stopped decoding before the test starts. The rule reads only Prometheus, so it's the rule that's tested.
- `scripts/monitoring-checks.sh` takes `PA_MON_WEBHOOK_PORT` for a host where 18099 is taken.

## Evidence

cld01, from the branch's files (`PA_MON_WEBHOOK_PORT=18199 PA_MON_RUN="TestA78|TestA118" sh scripts/monitoring-checks.sh`; 18099 is taken there):

- A78, installed from the release's files: `alert rules: ["Client not receiving" "Backup failing" "Upgrade failed" "Radio stalled" "Radio not decoding" "API unreachable" "Authentik down" "Failed sign-ins" "Authentik tasks failing"]`.
- A118 (`--- PASS: TestA118ToldWhenARadioStopsDecoding (594.86s)`):
  - `wrote two days' history at this time of day for dec-a-163544, dec-b-163544, dec-c-163544 and dec-e-163544, none for dec-d-163544 (new); dec-a-163544 and dec-d-163544 stopped decoding at 16:15:30, dec-c-163544's radio stalled then`
  - `"Radio not decoding" fired for dec-a-163544 7m37s after the test started (27m37s after it stopped decoding)`; the alert's summary in Grafana then: `Client dec-a-163544 decoded 40 pages in the last 30 minutes; it usually decodes 180 then` (while pending)
  - `"Pages decoded: the last 30 minutes against the usual" for dec-a-163544: last 30 minutes 12, usual 180, alert line 45; for dec-b-163544 180 against 180`
  - `with a third of the usual decoded, still firing 90 s later`; `it resolved 28s after decoding came back to two thirds of the usual`
  - `fired for dec-a-163544 only; not for dec-b-163544 (as usual), dec-c-163544 (radio stalled: "Radio stalled" did), dec-d-163544 (no history) or dec-e-163544 (too quiet to tell); no "Client not receiving"`
