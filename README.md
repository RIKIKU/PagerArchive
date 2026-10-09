# PagerArchive

A self-hosted archive and live browser for POCSAG paging networks, received with inexpensive RTL-SDR dongles and decoded with `multimon-ng`.

- Many receivers, one server, with deduplication across receivers
- Live updates, multi-criteria search, job grouping and a map
- Network knowledge (page formats, redaction rules, ignored capcodes) in JSON **profiles**, so it works for any POCSAG network
- Sensitive details are removed **before** anything is stored

**Status:** specification stage. See [`SPEC.md`](SPEC.md). No code yet.

PagerArchive is for people interested in what's happening on their network. It is not a tool for harassment.

## Licence

[MIT](LICENSE)
