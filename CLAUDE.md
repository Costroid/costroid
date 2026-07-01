# CLAUDE.md

**`AGENTS.md` is the single source of truth — read it fully before doing anything.** This file adds Claude Code-specific operating notes on top of it.

## Operating rules

- Read `AGENTS.md` first every session and follow **all** its invariants and Do-NOTs; the security invariants (Cardinal Rule, least-privilege credentials) are absolute.
- **Verification-first (hard rule):** don't say a task is done until you've built, run, and tested it in the environment and can show the real output — never simulate or guess. If you can't verify, say so and stop.
- **Stay in scope:** small, single-purpose changes. Don't refactor unrelated code, restructure the repo, or create new files/docs unprompted — propose larger changes first.
- **Don't re-litigate settled choices:** before proposing to change an architectural decision, check `docs/decisions.md` (the append-only decision log) and don't silently reverse it.

## Per task

1. Identify which concern it belongs to (see `AGENTS.md` → *Stack & shape*).
2. If it touches ingestion/FOCUS, add or update a sample export in `testdata/`.
3. Make the smallest correct change.
4. Run the checks (see `AGENTS.md` → *Working here*) and paste the results.
5. Conventional Commit + short PR description with verification output.

Environment: WSL2 Ubuntu; repo `~/costroid`; Go latest stable, Node LTS, pnpm, DuckDB.