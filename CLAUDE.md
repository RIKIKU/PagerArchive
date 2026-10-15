# PagerArchive

Self-hosted archive and live browser for any POCSAG paging network. **`SPEC.md` is the source of truth. Read it before any work.**

## Rules
- Spec items marked `OPEN` are undecided: stop and ask the owner. Don't guess.
- IMPORTANT: Principles P1–P3 in `SPEC.md` override everything. Redacted details are never stored, logged or shown.
- No implementation code until `docs/adr/0001-technology-stack.md` is approved by the owner.
- Golden tables (`profiles/**/golden-*.json`) are owned by the owner. You may propose new rows, but never change an expected value to make a test pass. Raw lines are never edited.
- This repo is generic and public: never add the name of a real agency, network or other paging project, real captures, or real addresses. Network-specific knowledge belongs in a private profile repo, mounted at runtime.
- Never commit anything named `*PRIVATE*`, `.env`, or secrets.
- Show evidence when claiming something works: the command run and its output.
- Styles use the tokens in web/src/app.css; no hard-coded colours or font sizes in components.
