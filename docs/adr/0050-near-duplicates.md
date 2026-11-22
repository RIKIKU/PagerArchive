# ADR 0050: Near duplicates from a second receiver

## Status

Status: Proposed (2026-11-20). The owner's decisions of 2026-11-20, before a second receiver beside the first goes live: copies of one page to one capcode within R19's 10 seconds whose text is nearly the same are one message; the message keeps the copy that parses, else the longer of a truncated pair ("B + the message with the most bytes wins"), else the first; the other copy is kept as its client's reception, with its own text. R19, R19a, R19b, R83a, A131. Migration 00035 (two nullable columns, no backfill: R91a). `VERSION` and the compose image stay as they are (ADR 0024); the pull request is labelled `feature`.

## Context

In a test with two receivers in one rack, their text for the same page was byte-identical for only 57 to 81 % of pages. R19 merges copies by a hash of capcode and text, so the rest, 20 to 40 % of the pages both hear, would be stored twice: a clean copy and a garbled one. Each would alert, count in the reports and open or join large events on its own.

How POCSAG copies differ decides how to compare them:

- **Substitution.** A codeword carries 20 data bits, protected by BCH(31,21), which puts right 2 bit errors (the decoder may correct fewer). A codeword with more decodes wrong: up to 3 characters (20 bits is under three 7-bit characters) in a run. The page keeps its length. A character garbled into a control character is removed with the control tokens (R17), so a character can also go missing.
- **Truncation.** The receiver loses the signal and the page ends early.
- **A junk tail.** The decoder reads past the end and adds a few characters, often control or non-ASCII ones.

## Decisions

### 1. Nearly the same: the shorter copy against the start of the longer

Each copy is compared as stored (control tokens removed, redacted: P1), without its trailing junk: whitespace, control characters and anything outside printable ASCII. The **distance** is the edit distance (Levenshtein: a substitution, insertion or deletion costs one) between the shorter copy and the start of the longer that matches it best: the longer copy's end is free. So a garbled codeword costs up to 3, a lost character 1, and a truncated copy 0.

Two copies are the same transmission when the distance is at most **floor(ratio × the shorter copy's length)**. The ratio is **0.1** by default, `DEDUP_NEAR_RATIO` in `.env` (0 to 0.5; 0 turns near duplicates off). Why 0.1:

- a page of 30 characters may have one bad codeword (3 characters), one of 100 characters three;
- a page under 10 characters may differ by nothing but its tail: "Test 1" and "Test 2" stay two pages;
- a real correction, such as another address, grid and appliances on the same job, differs by more than 1 in 10 characters, and stays a page of its own (A131's made-up correction is 12 edits from its page, of 85 characters, where 8 are allowed).

A change of a character or two can't be told from a garble by the text: two pages to one station a second apart that differ only in an alarm level, a call sign or a counter. Those are kept apart by who heard them (decision 5: a receiver's second copy is another transmission) and by job numbers (below). What's left, and accepted: such an update that one receiver heard and the other missed the page before it, and a page whose correction only adds text at the end (it looks like the whole of a truncated copy). Neither loses the text: it's kept as its client's copy and shown on the message. `dedup-report` (decision 7) measures how often pairs come close to the threshold, so an operator can tune it on their own archive.

Only messages to the same capcode sent within R19's window (10 s either side of the copy's receive time) are compared, and the nearest text wins (a tie: the nearest in time). An exact copy is still R19's and behaves as before.

**Different job numbers are different pages**, however near the texts (R23's job number, from the format or the network's pattern, on both). Two incidents paged to one station within seconds, in one format, can differ in little but the job and the address; merging them would make the second a copy of the first, and its alert would never be sent. The failures aren't equal: a garbled job number that's still a valid job number (each changed character still a digit) only stores a copy twice, as before this ADR, while a merged incident loses an alert.

### 2. Which copy the message keeps

1. **The one that parses better**: a format matched (not `parse_failed`), its address was split, then the more fields it found. "The expected fields" is read as that ranking, not as every group of the format: two copies of a page that has no grid reference both lack it, so a strict "all fields" rule would call both failures and fall through to length.
2. If they parse as well as each other, **the longer** after cleaning, **only when the shorter looks like a truncated copy**: it's near the longer's start, and the longer runs on past it by more than 3 characters (one codeword's worth). That guard keeps a longer, different page from winning by length; and a longer copy that differs only by a junk tail of a codeword or less is the same length for this rule.
3. Otherwise **the first stored** (the copy already in the message).

The other copy is kept as that client's **reception, with its own text** (decision 4). The metric `pagerarchive_dedup_near_total{kept}` labels which rule decided: `parsed`, `longer` or `first`.

### 3. A better copy after the first was stored: swap at once, no hold

The default is no added latency (alerting speed matters), so the first copy is stored and announced as soon as it comes. When a better copy comes within the window, in the same transaction:

- the message takes its text, dedup hash, priority and parsed fields, and its sent time stays the earliest (R21); it's **re-parsed and placed as a live page is**: job number (so job grouping, R23), location, area (R88a) and derived locality (R24). The report counts follow by their triggers (R38a), and search reads the new text;
- the copies that had the old text now keep it as their own; a copy whose own text is the new one keeps none;
- **integrations**: `OnStored` runs again. It queues at most one delivery per integration and page (R51b, the deliveries' primary key), so an integration already told **isn't told again**; one that the better copy now matches (say, a filter on structure fires that the cut copy didn't name) is told now. A delivery still waiting is built from the message when it's sent (ADR 0006), so it carries the better text;
- **large events**: a message in no event is given to the detector as a new page would be, so a better copy that now carries a code opens or joins an event, and an event it opens is told to large-event integrations (no one was told of it before). A message already in an event has the events around it rebuilt after the commit, as after a re-parse (R87f), which tells no one (R87d);
- **live views**: the stream sends an `update` event with the message as it is now, and the list shows it in place (or adds it, if it now matches the search); a message that no longer matches gets a `remove` event.

A short hold (wait a second for a second copy before storing) was considered and refused: it delays every page's alert for the 20 to 40 % that differ, and the swap does the same work after the fact.

### 4. Storage: the copy's own text, in its reception

`receptions` and `import_receptions` each gain a nullable `text`: NULL when the copy's text is its message's, as every copy's was before; otherwise that copy's text, stored after control-token removal and redaction like a message's (P1). Migration 00035 only adds the columns (no default: no row is rewritten), and nothing is backfilled (R91a). Copies merged before it have no text of their own, which is right: they were exact.

**P1.** A copy's text comes from the same `pipeline.Prepare` as the message's, so it's redacted before anything stores it. Retroactive redaction (R45a) applies changed rules to the copies of each batch's messages in the same transaction; a copy whose text is then its message's keeps none. A131 proves both, and that no log line holds either copy's text: the ingest log line of a near duplicate has its kind, reason and shape only.

### 5. The same client, nearly the same, within 10 seconds: two pages

Networks repeat pages deliberately, and R19 treats a same-client **exact** repeat as the same transmission, which it is. A **near** one is kept apart: a copy is a near duplicate only of a message its client has no copy of yet.

Why: a receiver hears each transmission once, so its second copy is a second transmission: a deliberate repeat it decoded differently, or a different page. Within seconds, the different pages are the ones that matter: the same job's alarm raised a level (`ALARM 2`, then `ALARM 3`, a second apart, as A126 posts), the same page for another station (`#STN1`, then `#STN2`, as A121 posts), a short test page with a counter. They differ by a character, as a garbled copy does, and nothing in the text tells them apart; merging one would make it a copy of the first, and its alert would never be sent. The failures aren't equal: a garbled repeat kept apart is a copy stored twice, as before this ADR; a merged update is a lost alert. A second receiver's copy of the repeat still merges into the repeat's message, which that receiver has no copy of.

An import has no clients (R83b), so every copy in a file is from one source: near duplicates in a file are merged by the threshold and job numbers alone. An imported page sets off nothing (R83b), so a merged update loses no alert there; its text is kept as the import's copy.

### 6. Metrics

Counters since the API started, on R52's endpoint, labelled only by kinds, reasons and client IDs (few: an admin adds each client):

- `pagerarchive_dedup_total{kind="exact|near"}`: copies merged into a message;
- `pagerarchive_dedup_near_total{kept="first|parsed|longer"}`: near duplicates, by the rule that chose the copy;
- `pagerarchive_dedup_near_shape_total{shape="substitution|truncation|junk_tail|other"}`: how they differed;
- `pagerarchive_dedup_near_swapped_total`: better copies that replaced a message's text;
- `pagerarchive_client_receptions_total{client}`: each client's copies recorded, new messages and duplicates;
- `pagerarchive_client_variants_total{client}`: each client's copies kept with their own text.

### 7. Tuning data: `pagerarchive dedup-report --since 7d`

A server-only command, like the import, reading only. For the near duplicates kept since then: how many, by shape (compared again at the ratio in use, and how many are no longer near at it), which copy parses when their lengths differ (the longer, the shorter, both, neither), which parses better, and histograms of the edits between them and of edits per character. For pairs of messages to one capcode sent within 10 s of each other that were kept apart: how many, how many of them name different job numbers (never merged, and left out of the rest), and the same histograms for the others. And at other ratios (0.05 to 0.3): how many kept pairs would be kept apart and how many separate pairs would merge. **Counts and histograms only**: never a page's text, a capcode or anything else from a page (P1). It reads at most 31 days, so it stays a short read-only query, not a long job (R91).

### 8. The import

R83a's path is the live one, so an import merges near duplicates in the file the same way (`import_receptions.text`), and its report adds "of which near duplicates". A second run stores nothing new (R83c): an imported copy kept with its own text is known by its capcode, receive time and that text. A dry run counts near duplicates against the database and the file, without working out which copy would be kept. Copies of one capcode now wait for each other (an advisory lock on the capcode, not the hash), so a live page to a capcode in an import's batch waits for that batch, as one with the same text did before (a batch is 500 pages).

### 9. The UI

The message's detail view lists each reception as before ("Receiver 1" to users, the client's name to admins: R39), and under a copy kept with its own text, "Heard as" and that text, with the characters not in the message's marked. Everyone who sees the message sees its copies, as with the receptions already.

## Consequences

- A second receiver stores 20 to 40 % fewer messages than R19 alone would, with nothing heard lost.
- A message's text can change once within seconds of being stored. What was already sent stays sent; the live list shows the change.
- The threshold is a judgement. It's configurable, measured by `dedup-report`, and its known blind spots (decision 1) keep the other text visible.
