# Integrations

**Integrations are a convenience, not a replacement for your pager: delivery is best-effort, and a page may arrive late or not at all.**

Each user can forward new pages to their own systems from **Integrations** in the app (SPEC.md R47–R51c; design in [ADR 0006](adr/0006-m5-integrations-and-metrics.md)). An integration is:

- a **selection**: one or more capcodes and/or aliases, chosen in one field. Type an alias name or a capcode and choose from the suggestions. A capcode with its own alias is chosen as that alias ("Station 1 (0100001)"). A capcode that only a range alias covers stays a capcode, with the range named beside it ("0100009 in Stations block"), so typing one capcode never selects a whole range; type the range's name to choose it. The selection is required, so nobody forwards the whole network by accident. An alias selects the pages that resolved to it, as its alias view shows; or, instead, **large events**, each when it opens (see [Large events](#large-events));
- an optional **filter**: event categories or raw event-type codes, priorities, and text, group, agency or job number (admins also have the flags). Empty means every page from the selection;
- a **destination**: a webhook, MQTT, Discord, Telegram, Pushover or ntfy;
- its **content**: **Full** (the text and every field) or **Summary** (alias, capcode, event type and category, priority, sent time and job number; no text, address, location or extra fields). Webhooks and MQTT default to Full. Discord, Telegram, Pushover and ntfy are third parties, so they default to Summary.

Nobody else can see your integrations. Admins can see that they exist, and pause them, but never their secrets or where they send.

## When pages are sent

- **Only new pages**, after redaction. Creating an integration doesn't send older pages.
- **Within 5 seconds** of the first receiver hearing the page, normally well under one.
- **On time or not at all.** A failed delivery is retried after 2, 4, 8, 16 and 32 seconds, then every minute, until 5 minutes after the page was first received. After that it's **skipped: too old**. A page that reaches the server already 5 minutes old (from a receiver that was offline) is skipped too.
- **Never twice.** Each page is delivered to an integration at most once, however many receivers heard it. Every delivery carries the page's `id`.
- **A request that was sent but never answered** (no reply within 10 seconds) isn't retried, because the endpoint may have acted on it. It's recorded as failed.
- **What's retried:** the request couldn't be sent (connection refused, DNS, TLS), or the endpoint answered `408`, `425`, `429` or `5xx` (except `501`). Any other answer (for example `400`, `401`, `404`, or a redirect, which isn't followed) is a refusal, and that page isn't retried.
- **Paused after 3 pages in a row** weren't delivered. You see "Paused: kept failing", the last error and when it happened. Fix the endpoint, press **Send test**, then **Resume**. An integration paused by an admin can only be resumed by an admin.

Each integration lists its last 100 deliveries for 30 days: **Delivered**, **Retrying**, **Failed**, **Skipped: too old** or **Cancelled** (paused, or its owner was deactivated). Errors never contain page text.

**Send test** sends a made-up delivery marked `"test": true` and shows what the endpoint answered: its status, how long it took, and the start of its reply.

## Private addresses

Integrations can't reach private, loopback, link-local or other reserved addresses (`10.x`, `192.168.x`, `127.x`, `169.254.x`, `100.64.x`, `198.18.x`, `240.x` and the like), so they can't be used to probe your network. IPv6 addresses that carry an IPv4 address (NAT64 `64:ff9b::`, 6to4 `2002::` and IPv4-compatible `::a.b.c.d`) are judged by that IPv4 address. Local-use NAT64 (`64:ff9b:1::/48`) and Teredo (`2001::/32`) addresses are refused whole. If yours is on your LAN (Home Assistant, Node-RED, a self-hosted ntfy or broker), ask an admin to allow its address or range in **Admin → Integration addresses**.

## Webhook

`POST` to your URL with `Content-Type: application/json` and the payload below. Any `2xx` answer counts as delivered. You can add one custom header, for example `Authorization: Bearer …` or `X-Api-Key: …`.

The URL is kept secret once saved (only its scheme and host are shown), because URLs such as Home Assistant webhooks carry their secret in the URL. Use `http://` only on your own network.

## MQTT

Publishes the payload below to your topic with QoS 1, not retained, then disconnects. Settings: the broker (`mqtt://host` on port 1883, or `mqtts://host` on 8883), the topic (default `pagerarchive/<id>`), an optional user name and password, and for `mqtts://` an optional CA certificate if your broker uses its own CA.

### Home Assistant discovery

Turn on **Home Assistant discovery** and Home Assistant shows a **PagerArchive** device with one **event entity** per integration. When you save, PagerArchive publishes a retained config to `homeassistant/event/pagerarchive/<id>/config` (the prefix can be changed). Each matching page fires the entity with event type `page`, and Send test fires it with `test`. Every other key of the payload becomes an attribute of the event (`priority`, `alias`, `text`, …). Turning discovery off, or deleting the integration, removes the entity.

Example automation (Home Assistant 2024.10 or later):

```yaml
automation:
  - alias: "Station 1 emergency page: lights on"
    triggers:
      - trigger: state
        entity_id: event.station_1_pager
        not_from: ["unavailable", "unknown"]   # not when Home Assistant restarts
    conditions:
      - condition: template
        value_template: >
          {{ trigger.to_state.attributes.event_type == 'page'
             and trigger.to_state.attributes.priority == 'Emergency' }}
    actions:
      - action: light.turn_on
        target:
          entity_id: light.station_bay
```

Home Assistant records event attributes in its history. With Full content that includes the page text.

### Home Assistant without MQTT: a webhook

In Home Assistant, a webhook trigger gives you a URL `http://homeassistant.lan:8123/api/webhook/<webhook_id>`. Use it as a PagerArchive webhook (an admin allows its address), and choose a long random `webhook_id`: it's the only secret.

```yaml
automation:
  - alias: "PagerArchive page to my phone"
    triggers:
      - trigger: webhook
        webhook_id: "pagerarchive-k3x9q2m7vw4d-long-random-part"
        allowed_methods: [POST]
        local_only: true
    conditions:
      - condition: template
        value_template: "{{ not trigger.json.test and trigger.json.priority == 'Emergency' }}"
    actions:
      - action: notify.mobile_app_my_phone
        data:
          title: "{{ trigger.json.alias or trigger.json.capcode }}"
          message: "{{ trigger.json.text or trigger.json.event_label }}"
```

## Presets

Each preset sends the page in that service's own format, with a title such as `Emergency · Station 1` and a line with the event type, job number and time (in the deployment's time zone). Full content puts the page text first. Every preset links to the page in PagerArchive, which only opens after signing in. Text longer than a service allows is cut, ending with `…`.

| Preset | You need | Sent as |
|---|---|---|
| **Discord** | A channel's webhook URL: Server settings → Integrations → Webhooks → Copy webhook URL | An embed. Mentions are turned off, so a page can't ping `@everyone` |
| **Telegram** | A bot token from @BotFather, and the chat ID (a number; groups start with `-`) or a channel's `@name`. Add the bot to the chat first | Plain text, without link previews |
| **Pushover** | An application token and your user key (30 letters and digits each) | Title, message and a link. Priority follows the page (high for Emergency, normal otherwise) or is fixed, up to high |
| **ntfy** | The server (default `https://ntfy.sh`; a self-hosted one is fine), a topic, and an access token if the topic is protected | Title, message, a click link, and a siren tag for Emergency. Priority as for Pushover, up to high (4) |

There's no "repeat until acknowledged" priority: PagerArchive isn't for operational alerting (SPEC.md, Out of scope).

On a public ntfy server, anyone who knows the topic can read it: pick a long random topic, or keep Summary content. PagerArchive keeps the topic secret once saved.

## Payload

The same JSON goes to webhooks and MQTT. This is version 1.

```json
{
  "version": 1,
  "event_type": "page",
  "test": false,
  "id": 1234,
  "url": "https://pager.example.org/messages/1234",
  "integration": { "id": "k3x9q2m7vw4d", "name": "Station 1 fires" },
  "content": "full",
  "sent_at": "2026-10-16T03:04:05Z",
  "capcode": "0100001",
  "alias": "Station 1",
  "agency": "Example Fire Service",
  "priority": "Emergency",
  "heartbeat": false,
  "category": "Structure fire",
  "event_label": "House fire",
  "job_number": "EX0000001",
  "text": "EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1",
  "fields": {
    "event_type": "HSF1",
    "description": "SMOKE FROM ROOF",
    "address": "12 FICTION ST",
    "locality": "MADEUPTON",
    "grid_ref": "123456",
    "units": ["T1", "T2"],
    "group": "STN1"
  },
  "extra": {},
  "labels": { "event_type": "House fire" },
  "location": { "lon": -140.864937, "lat": -34.831359, "approximate": false, "source": "address" }
}
```

| Key | Is |
|---|---|
| `event_type` | `page`, or `test` for Send test. (The page's own event-type code is `fields.event_type`.) This is Home Assistant's event format |
| `test` | `true` only for Send test, whose `id` is `null` and whose values are made up |
| `id` | The page's ID in PagerArchive: the same for every copy of the page, never reused. Use it to drop repeats if you ever need to |
| `url` | The page in PagerArchive (needs signing in) |
| `sent_at` | When the first receiver heard it, UTC |
| `text` | The page as a pager shows it: without its priority marker |
| `priority` | `Emergency`, `Non-emergency`, `Admin` or `null` |
| `category`, `event_label` | The event type's report category and display label, from the profiles |
| `fields`, `extra`, `labels` | The parsed fields, the profile's extra fields and their display labels. Missing values are `null`. `fields.units` is the appliances the page names, shown in the app as Appliances; its key stays `units` |
| `location` | Where the page is placed (`address`, `street`, which is approximate, or `grid`), or `null` |
| `flags` | Only for admins' integrations: `parse_failed`, `address_unsplit`, `location_mismatch` |

A **Summary** payload has only `version`, `event_type`, `test`, `id`, `url`, `integration`, `content`, `sent_at`, `capcode`, `alias`, `priority`, `category`, `event_label`, `job_number` and `fields.event_type`.

Requests come with `User-Agent: PagerArchive/<version>`.

## Large events

An integration can select **large events** instead of capcodes and aliases (SPEC.md R87–R87d; design in [ADR 0027](adr/0027-m22-large-events.md)). A large event tells you something big is happening on the network, so you know where to look; for more, listen to the radio or check other systems. PagerArchive groups Emergency pages to aliased capcodes (not ignored ones: owner, 2026-11-14) into a large event when a page carries a code that opens one (the network profile's `deployment_code_pattern`), or when one job has enough Emergency pages to aliased capcodes (20 by default, set by an admin in **Administration → Large event settings**; a page heard by several receivers counts once). Pages within 12 hours of each other share an event, so a busy day's incidents may share one. Every signed-in user sees them under **Tools → Large events**. An event is simply a large event: there are no levels to choose (owner, 2026-11-13).

It sends **once per event, when the event opens**, and never again for that event: not as more pages, codes or jobs join it, and not when a page joins two events into one (each was sent when it opened). Events are rebuilt when pages are re-parsed (an alias added, changed, deleted or marked ignored, a template's profile changed, or the network profile changed; owner, 2026-11-14), and a rebuild **never sends anything**, even for an event it forms: only an event that live pages open is sent. A delivery already made stays in the integration's list, about whichever event now holds the page that opened it (about none, if no event holds it). A rebuild can change an event's ID: the event that keeps most of an old event's pages keeps its ID, and a link to an ID no event keeps finds nothing (404), as a link to an event merged into another does. An event that opened before the integration was made never fires it. It sends a **summary of the event, never page text**, whatever its content is set to; its content is always `summary`. Timing, retries, the 5-minute cut-off, "never twice", pausing and Send test are the same as for pages, counted from the page that opened the event. A large-event integration has no filter.

The webhook and MQTT JSON:

```json
{
  "version": 1,
  "event_type": "large_event",
  "test": false,
  "id": 1240,
  "url": "https://pager.example.org/large-events/7",
  "integration": { "id": "k3x9q2m7vw4d", "name": "Big days" },
  "content": "summary",
  "sent_at": "2026-11-10T00:00:00Z",
  "large_event": {
    "id": 7,
    "started_at": "2026-11-10T00:00:00Z",
    "opened_by": { "id": 1240, "sent_at": "2026-11-10T00:00:00Z" },
    "jobs": ["EX0000101"],
    "emergency_pages": 1,
    "url": "https://pager.example.org/large-events/7"
  }
}
```

| Key | Is |
|---|---|
| `event_type` | `large_event`, or `test` for Send test (whose `id` and `opened_by.id` are `null` and whose values are made up) |
| `id`, `sent_at` | The page that opened the event, and when it was first heard. Never sent twice |
| `large_event.id` | The event |
| `large_event.started_at` | The event's first page's sent time. It can be earlier than the page that opened it: a job's earlier pages join the event with it |
| `large_event.opened_by` | The page that opened the event: its `id` and `sent_at` |
| `large_event.jobs` | The event's job numbers so far, by their first page |
| `large_event.emergency_pages` | How many Emergency pages the event holds so far (every page of an event is one) |
| `url`, `large_event.url` | The event's view in PagerArchive (needs signing in) |

There is no page text, address, appliance count, code or level in it (owner, 2026-11-13).

Discord, Telegram, Pushover and ntfy get the same summary in their own format: "Large event", then when it started and opened, the jobs, the number of Emergency pages and the link. Home Assistant's event entity for such an integration fires `large_event` events.
