# ADR 0026: M14, the scrubber and albums

## Status

Status: Proposed (2026-11-12). The owner's brief for M14: the scrubber (R81b-R81d, A71) and albums (R82-R82b, with R82f's descriptions and notes, A72), on the branch `m14`, which carries the R82f spec commit of PR #25 and has main merged in. The PowerShell module doesn't change, so its version stays.

## Context

R81b-R81d add a strip down the right of the message list and the alias, job and album views for large jumps, with how busy each mark is, following the search and live updates, and a zoom into 5-minute blocks for an hour of today or yesterday. R82-R82f add albums: one user's named lists of messages, private to them, thinned by retention, with an optional description and a note on each message. Both sit on what M13 built (ADR 0015): the list (`MessageList.svelte`), the search's one query (`SearchMessages`, ADR 0017), the side menu's tree (`nav.ts`) and the tokens.

## Decisions

### 1. The data model (`00017_albums.sql`)

- `albums`: `owner_id` (the user, `ON DELETE CASCADE`), `name` (1-100 characters), `description` (1-2,000 characters, or NULL for none), `created_at`, `updated_at`, `emptied_at` and `retention_touched` (below). A unique index on `(owner_id, lower(name))`: one name per person, case aside, so the menu's entries can be told apart.
- `album_messages`: `(album_id, message_id)` as the key, both `ON DELETE CASCADE`, with `note` (1-500 characters, or NULL) and `added_at`. The note is on the pair, so the same message in two albums has a note in each (R82f), and a message deleted by retention takes its places in albums and their notes with it (R82a). An index on `message_id`, for retention's batches.
- The limits are in characters (code points), checked by the API and by the database. Text is trimmed; an empty description or note is none. A name may not hold control characters; a description or note may hold line breaks and tabs, nothing else of that kind. Plain text everywhere: the browser shows it as text, never as markup.
- An upgrade adds the two tables and nothing else (A77). Account deletion keeps a user row as a placeholder (R43c), so it deletes the albums itself (`DeleteAlbumsOf`), in the same transaction; a deactivated user's albums stay (R82b).

### 2. Privacy (R82)

Every query on albums names the owner (`... AND owner_id = $user`), so there's no path to someone else's album, an admin's included. Anyone else gets 404 "no such album", the same as for an album that doesn't exist, so the API doesn't say which albums exist. Names, descriptions and notes are never logged (e2e.sh's log check looks for its own) and never in telemetry (spans carry route patterns, R70). They aren't searched: they're in their own tables, which `SearchMessages` doesn't read (A72's `storm`). Being the user's own words, the redaction rules don't apply to them (R82f).

### 3. Retention (R82a)

The nightly run's batches (`DeleteMessagesBefore`) now also mark each album a deleted message leaves (`retention_touched`), in the same statement: a statement sees the rows as they were before it, so it finds the albums though the cascade removes the rows. After the batches, `DeleteEmptiedAlbums` deletes the marked albums left with no messages and no description, and `FinishRetentionAlbums` sets `emptied_at` on the ones kept for their description and clears the marks. The marks are in the table, so a run that stops part-way is finished by the next. Only retention deletes an album: one a user made and hasn't filled yet, or emptied by hand, stays. Adding a message clears `emptied_at`. The album view says "Its messages have passed the retention period" for an empty album with `emptied_at`, and every album screen has the one line R82a asks for.

### 4. The API

- `GET /api/v1/albums` (the user's, by name, with each one's count), `POST /api/v1/albums` (`name`, `description`), `GET`, `PATCH` (rename, change or remove the description) and `DELETE /api/v1/albums/{id}`.
- `PUT` and `DELETE /api/v1/albums/{id}/messages/{message}` add a message (again: no change, its note kept) and take it out; `PUT /api/v1/albums/{id}/messages/{message}/note` sets or removes its note there.
- `GET /api/v1/messages/{id}/albums`: which of the user's albums hold a message, for "Add to album".
- An album's messages are the message list's, with `album=<id>`: `GET /api/v1/messages?album=…` (with the search's other parameters, a page at a time, newest first, current aliases and redaction, each with its `note`), and the scrubber's counts and live updates take it too. `album` isn't one of the search's own parameters (`search.Parse`); the server reads it and checks it's the user's (`albumParam`) before it becomes `Filter.Album`. `SearchMessages` keeps a message when an `album_messages` row joins it to that album.
- `GET /api/v1/messages/counts` (with the list's parameters, and `hour=` to zoom): the scrubber's marks, newest first, each with its start, end, kind (hour, day, week, month, or block) and count, and whether they were counted (section 5).
- The list jumps with `GET /api/v1/messages?at=<time>`: the messages sent before then, newest first, and `newer`, a cursor for the newer ones; `?after=<cursor>` gives the page just newer than one, nearest first in the query (`Store.SearchNewer`: the search's own query and tries, its order reversed) and newest first in the reply, with `newer` while there are more. These are A29's signed-in endpoints too.

### 5. How the scrubber gets its counts cheaply (R81c)

- **One query, the search's own.** `Store.CountMessages` builds the search's query for the list's filters exactly as `SearchMessages` would (it captures the SQL and arguments sqlc's code sends), then swaps its columns for `width_bucket(sent_at, <the marks' edges>)` and `count(*)`, and its order for a `GROUP BY`. So the counts follow every filter the list has (text terms and `or:`, aliases, agencies, groups, priorities, categories, flags, the view's alias, job or album, ignored aliases) with nothing written twice, and the alias: names are looked up first, as for the search (ADR 0017). One statement counts every mark, reading only the range's messages: by sent time (`messages_sent_idx`), by alias and sent time for an alias's pages (`messages_alias_sent_idx`, often index-only), and through the search indexes for text.
- **The marks are worked out in Go** (`search.Marks`), in the deployment's time zone (R35a): the last 30 days, or the search's range, never past the current hour; a day each, a week from Monday over 90 days, a month over 365; today and yesterday by the hour (23 or 25 when the clocks change). Zoom (`search.Zoom`) is twelve 5-minute blocks of an hour of today or yesterday, and refused for older days (R81d).
- **Live updates cost nothing.** The list already receives each new message that matches its search (R32), so the browser adds one to that message's mark, and asks again only when one is newer than every mark (a new hour) or the stream reconnects.
- **Within the budget, always.** Counting runs with `jit = off` and a 2-second statement timeout (`store.CountsTimeout`). Counts that would take longer come back as `counted: false`: the marks work, without their dots, and the browser asks once more 3 seconds later, when what the first try read is in the caches. The counts never hold up the list: the list and its counts are separate requests.
- **Not done: a table of counts per day.** It would make long unfiltered ranges cheap on a cold disk, but only for filters it could hold (aliases, agencies), at the cost of a trigger on every message written and a rebuild when the time zone changes. The measurements below say whether it's wanted: see "Question for the owner".

### 6. The scrubber in the browser (`Scrubber.svelte`)

- Down the right of every message list that has one (the list, the alias and capcode views, the job view, an album; not the print page), sticky under the top bar, beside the rows only. On a phone it's narrower (the target's width) and has no labels, above the bottom bar.
- Each mark has up to four dots: none for an empty mark, then one to four by its count against the busiest mark of the same kind (an hour against hours, a day against days), so the hours of today aren't all one dot beside the days. A few labels: "Today" and "Yesterday" where their hours start, a date on Mondays, the month for weeks, the year for months.
- Where the list is: the mark holding the first row in view is marked (an edge and a tint). Clicking or dragging (pointer capture, so a drag can leave the strip) names the mark under the pointer in a bubble while dragging and jumps on release. A jump is to the messages sent before the mark's end, with the newest of them at the top of the view; the list then shows "Load newer messages" and "Newest" above them, and loads the newer ones as it's scrolled up, keeping the rows in view where they were. A live message newer than everything loaded waits for that (as one older than everything loaded waits for paging).
- The keyboard (R81b): the strip is one vertical slider (`role="slider"`, its value text the mark's name and count): Up and Down move a mark (an hour within today and yesterday), Page Up and Page Down a week, Home and End to the ends; it jumps a moment after the last key, so holding a key doesn't ask the API for every mark.
- Zoom (R81d): at an hour of today or yesterday, a button at the top ("Zoom into Yesterday 14:00, by 5 minutes") shows that hour's twelve blocks with their dots; "The hour after" (top) and "The hour before" (bottom) move an hour, within today and yesterday; "Back to the days" returns.
- New icons (R77a), from Tabler as before: albums `album`, add `plus`, edit `pencil`, remove `trash`, note `note`, zoom in and out `zoom-in`, `zoom-out`, the hours after and before `caret-up`, `caret-down` (carets, as the calendar's months; chevrons are menu groups), and newest `chevrons-up`. New tokens (R82c): `--scrubber-width`, `--scrubber-width-phone`, `--scrubber-edge`, `--scrubber-dot`, `--scrubber-dot-colour`.

### 7. Albums in the browser

- The side menu's **Albums** group (R77), after Messages: its heading links to `/albums`, its start page (the albums, and a form for a new one), and it holds one entry per album, with its count ("Big storm, 3 messages"), loaded with the frame and refreshed on every change. The phone's bottom bar has Messages, Map, Albums and More (R81); More leaves Albums out, as it has its own tab.
- `/albums/{id}`: the name, the count, the description at the top, "Rename or describe", "Delete album" (confirmed), R82a's line, then the list with the scrubber and the search box (the box searches the album there), each row with its note, "Add note" or "Edit note", and "Take out". No live updates: new pages never join an album. Breadcrumbs: `Albums / Big storm` (R79).
- "Add to album" (with the Albums icon) in a message's detail view opens a dialog: a tick box per album (ticking adds, unticking takes out, at once), and a new album's name with "Create and add". The list keeps no tick boxes (R67).

## Tests

- Go, stack-free: `TestMarksLast30Days`, `TestMarksRange` (days, weeks from Monday, months, cut to the range), `TestMarksDaylightSaving`, `TestZoom`, `TestBounds`.
- Go, through the API and the database (`internal/acceptance/m14_test.go`): `TestA72Albums` (A72 as written: Big storm's 3 messages, C and the admin refused, current aliases, the nightly redaction, retention's 2 then the last, Storm notes' description and note, the second album without the note, `storm` finding nothing, Storm notes kept and marked with its note gone, the account deleted); `TestAlbumsArePrivate` (another user and an admin get 404 from every album endpoint, the album's messages, counts and live updates included, and the album is unchanged); `TestAlbumLimits` (100, 2,000 and 500 characters, accented ones counted as one, control characters, empty names, one name per person, renaming); `TestAlbumNotesPerAlbum`; `TestAlbumRetention` (kept with no messages ever, deleted when emptied without a description, kept and marked with one, the mark cleared by a new message, a deactivated user's albums kept); `TestA71ScrubberCounts` (A71's 40 days at API level: the marks, the busy day, `alias:"Station 1"`, weeks and months, zoom's blocks, the jump and the newer pages). A29 lists the new endpoints.
- A71's speed on A75's 4,562,500 pages: `a71CountsSpeed`, run by `TestA75SearchSpeed` after its searches (CI's search-speed job), cold after a restart and three times warm.
- Browser (`web/tests/e2e.spec.ts`, Chromium, Firefox and WebKit): "A71 scrubber" (40 days of made-up pages, the drag to 20 days ago, the busy day's dots, days before the last 30 absent, weeks and months, yesterday's hour, zoom and a block, the keyboard, Station 1's counts, a live page's count, axe), "A72 albums" (create, add from the detail view, the menu's count, C and the admin refused, a description and a note, the second album, `storm`, the emptied album's line, renaming, a phone's Albums tab and album view with no horizontal scroll, deleting, axe at 1,280 and 375 px), and the axe check of every view with `/albums` and an album added. A68 and A70 check the Albums group and tab.

## Evidence

See the pull request.

## Question for the owner

Long ranges on a cold disk: see the pull request's speed check. The last 30 days, with every search A71 and A75 name, are counted within a second, cold and warm. A range of years with no filter reads up to a year of messages a second on a cold disk, so the first look at it can be uncounted (no dots) until the retry; warm, it's counted. A table of counts per day would fix that for unfiltered and alias-only ranges.
