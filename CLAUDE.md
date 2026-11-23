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
- Docs-only changes (SPEC.md, docs/**, other Markdown) run no checks: their run skips every check and "All checks passed" reports at once (ADR 0014, ADR 0025).
- A pull request runs only the checks its files affect (`scripts/ci/changes.sh`, ADR 0025). A new file or folder runs every check until it has an area there (`scripts/ci/changes-checks.sh` warns until then); when a job starts reading a new file, add it to that job's area. Flaky passes are warnings on the run ("flaky: …"): look into them, don't ignore them.
- Merge freely, release deliberately (SPEC.md 12.1, ADR 0024): a merge to main isn't a release, and a branch never sets the version. Leave `VERSION` (`0.0.0-dev`) and the API's image in `compose.yaml` (`:main`) as they are: the Release workflow works out each release's version and writes it into the build. Raise `ModuleVersion` in `powershell/PagerArchive/PagerArchive.psd1` when the module changes, once per release: CI says if it's needed. Label a pull request `feature` when it adds a feature (`.github/release.yml` says when): its merge makes the next release a minor version.
- Every style value is a token (R82c), from ArchiveUx (the shared design system, pinned in web/package.json; ADR 0049) or, for the map's only, web/src/app.css: colours, type sizes, fonts and weights, spacing, corner radii, borders, target and other sizes, and how each priority is shown. Components use `var(--…)` only, in style blocks, inline styles and class strings; `web/scripts/check-tokens.mjs` fails CI on a hard-coded value (A73).
- A pull request that changes the UI carries screenshots of the change (owner, 2026-11-23: "if you're making UI changes, include a screenshot in the PR"): desktop (1,280 px) and phone (375 px), light and dark where it matters, made-up data only. Push the PNGs to the `pr-screenshots` branch under `pr<number>/` (never force-push it) and embed them in a PR comment from their raw.githubusercontent.com URLs.
- The PowerShell module follows `docs/powershell-guidelines.md`.
- Long jobs (SPEC.md R91, ADR 0042): design any process that could take more than 30 minutes on a production-sized archive to be observable in Grafana, restartable, resumable, pauseable and stoppable. Never put a long data rewrite or backfill in a migration (R91a): run it in the background as a long job.
