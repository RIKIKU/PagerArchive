# The print sheet

The print sheet collects pages to print for teaching recruits to read them (SPEC.md R67, R68; design in [ADR 0012](adr/0012-m11-print-sheet.md)).

- **Adding:** open **Print sheet** in the header. Its page has its own search, the same as the search on the message list, and you tick **Print** on a message in its results to add it to the sheet below them; untick it to take it off. Ticking happens only there: the message list, a job and an alias have no tick boxes. **Print sheet** in the header shows how many messages the sheet holds, at most 100.
- **The sheet** lists them in the order you added them. **Remove** takes one off; **Clear** empties it, after asking. A message that has since been deleted (by retention, say) leaves the sheet, with a note saying how many.
- **Printing:** **Print** opens the browser's print dialog. Each message prints as a pager shows it, its text in full and nothing else, in a heavy black border, black on white, and never split across two paper pages. Choose "Save as PDF" in the dialog for a PDF.
- **The browser's own headers and footers:** in the print dialog, untick "Headers and footers" to leave out the browser's own date, title and address.
- **Where it's kept:** in this browser only, for your account only, until you clear it. It holds message IDs, never the text, so the text always comes from PagerArchive as you're allowed to see it.

Printed pages are outside PagerArchive's control: retention and deactivation don't reach paper.
