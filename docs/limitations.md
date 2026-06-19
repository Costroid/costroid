# Costroid — known limitations & methodology caveats

Costroid is honest about what it can and cannot know. Cost is always an **estimate**
(your tokens × current prices), never the authoritative bill — design for reconciliation
against the provider invoice. This page records the methodology caveats that ride the
data; it grows as milestones land (M6 consolidates the full set).

## Sidechain (sub-agent) attribution

Claude Code transcripts mark sub-agent turns with a top-level `isSidechain: true`.
Costroid **keeps counting** every sidechain turn's tokens (they are real, billable usage)
but **annotates** them rather than trusting their attribution:

- `x_Sidechain = true` on every meter row from a sidechain turn.
- `x_AttributionConfidence = "uncertain"` (vs `"confident"` for a mainline turn).

Why uncertain: a sub-agent turn's `model` / `project` (`cwd`) as logged may reflect the
orchestrating session rather than the sub-agent's own context, so per-model / per-project
*attribution* of sidechain rows can be slightly off. The **totals are not affected** —
the tokens are counted exactly once (the `(message.id, requestId)` de-dup is unchanged).

Reconciliation note (verified vs ccusage on real logs, 2026-06): mainline usage matches
ccusage to the cent for every model; the only residual is `claude-opus-4-8` landing
~0.08% under ccusage, located entirely in how much sub-agent (sidechain) cache-read each
tool retains after de-dup — a known, benign methodology difference, not an under-count of
distinct billable turns. The provider invoice is the ground truth (reconciliation lane).

## Collector version

Every FOCUS row is stamped with `x_CollectorVersion` (the Costroid version that minted
it). Token-attribution methodology can shift between versions; the stamp lets a
replayed/exported ledger record which normalization logic produced each row.

## FOCUS import currency

The M1 FOCUS v1.2 importer carries a single authoritative cost into a USD ledger and
**refuses** a non-USD source rather than silently relabeling it. Multi-currency import is
an M2 cloud-lane feature.
