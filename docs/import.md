# Importing messages from an existing archive

`pagerarchive import-messages` moves pages from an older archive into PagerArchive (SPEC.md section 7.21, ADR 0018). It's for a move, not for a feed: users aren't imported, and aliases come in through the alias import (`Import-PagerArchiveAlias`, docs/powershell.md).

## The file

JSON Lines: one page per line, as a JSON object. Lines don't have to be in time order.

```json
{"received_at":"2024-03-01T10:15:30+11:00","capcode":"0100001","text":"@@EX0000051 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1","source_id":"123456"}
{"received_at":"2024-03-01T10:15:33+11:00","capcode":"0100001","text":"@@EX0000051 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1","source_id":"123457"}
{"received_at":"2024-03-01T10:20:00Z","capcode":"579064","text":"QDCHECK HOSES TODAY"}
```

| Field | | |
|---|---|---|
| `received_at` | required | When the page was received: RFC 3339, with its offset. If the other archive kept local times, convert them first. |
| `capcode` | required | 1 to 7 digits, as a string. It's padded to 7. |
| `text` | required | The page as the decoder gave it, priority marker included, 1 to 4096 bytes. Don't redact it yourself: redaction, cleaning and parsing happen here, as for a live page. |
| `source_id` | optional | The page's ID in the archive it came from, as a string, up to 200 bytes. It must be unique across everything you import. |

Any other field is refused, so a misspelt `source_id` can't go unnoticed.

## Running it

The API must have started once with the same network profile: it loads the address dataset that pages are placed against. The file isn't mounted anywhere. Send it through standard input:

```sh
# Check the whole file first. Nothing is stored.
docker compose exec -T api pagerarchive import-messages --dry-run - < pages.jsonl

# Then import it.
docker compose exec -T api pagerarchive import-messages - < pages.jsonl
```

`-T` matters: without it, Compose gives the command a terminal in place of your file. Progress goes to standard error and the report to standard output, so `> report.txt` keeps the report.

```
line 17: not valid JSON
line 3920: received_at must be RFC 3339 with its offset
lines read                                           3100000
pages stored                                         3071482
duplicates merged                                    28401
already imported                                     0
parse failures                                       1204
skipped by retention                                 113
ignored capcodes                                     2
refused lines                                        2
source IDs not kept (a redaction rule matches them)  0
```

A dry run's report says what a real run would do, and is exact unless pages arrive in between.

Refused lines don't stop the import, and the command still exits 0. Fix them in your conversion, if they matter, and run the file again. Each is named by its line number and a reason, never its text.

## What it does

- **The same path as a live page.** Each page goes through cleaning, redaction, parsing, placing on the map, deduplication and job grouping, as if a client had sent it at its `received_at`. A page that fails to parse is still stored and shown in full. Redaction happens before anything leaves the command: an unredacted page never reaches the database.
- **The order of the file doesn't matter.** The command reads the whole file first, then stores the pages in time order. Copies of a page no more than 10 seconds after its first copy are merged, within the file and with pages already stored.
- **Quiet.** Integrations aren't set off, open lists and watches hear nothing, no alert fires, and no client's last-seen time changes. A page's detail view shows `import` as what received it. `import` isn't a client: it isn't in the clients list, the metrics or the dashboards.
- **Retention.** Pages older than the retention period are skipped and counted.

## Stopping and running it again

The same file can be imported any number of times. A page is known by its `source_id`, or without one by its text and receive time, and is stored once. An import that stops part-way, for any reason, finishes when you run the file again, and the result is the same as one full run: it re-reads the file, then skips what's there, and pays for the first stage again.

Stop one with `docker compose restart api`, which stops the API for a moment too. Ctrl+C isn't enough: it ends `docker compose exec`, and the import goes on inside the container, with as much of the file as it has read if it's still reading. While one is running, another waits 30 seconds, then says `another import is running`.

## While it runs

The app keeps serving users and live pages, and the import stores at least 500 pages a second (R83f). So 3 million pages take under 2 hours, and 4.6 million under 3 hours. Reading and sorting the file comes first and takes a few minutes of that.

The first stage holds the file's pages, redacted, in a temporary table in the database, which goes when the command ends, however it ends. Have free disk of about **three times the file's size** for that, and about **1 KB per page** for what's stored.

Reports, progress and logs never hold page text. The file itself does, unredacted. Delete it when you're done, and remember your shell's `> report.txt` holds only the report.

Backups taken during an import hold what's been imported so far. As always (P1), write-ahead logs and backups are outside what the app controls, though nothing redacted reaches them through an import.
