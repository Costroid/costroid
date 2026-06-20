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

## Source-priced cloud rows have no per-token rate

A FOCUS-imported cloud row that carries an authoritative cost is **source-priced**: that
cost is preserved exactly (`x_Estimated = false`), but Costroid does not reconstruct a
per-token rate from a lump source cost, so `SkuPriceId` / `PricingQuantity` /
`ListUnitPrice` stay null on those rows (the foreign export's own pricing detail is not
yet carried through). (A *usage-only* imported row — no source cost — is instead
re-estimated from the bundled catalog like a local log, and does get a catalog
`SkuPriceId`/rate.) The cost
is exact; only the per-token *rate* breakdown is absent. The cloud lane (M2) will carry
the foreign pricing detail. This is why the v1.2 round-trip's 1.3 output validates as a
**subset** of the documented validator defects (the SkuPriceId-null defect rule applies),
never with a new failure.

## FOCUS v1.2 import fixtures are a metadata subset

The committed `fixtures/focus/v1.2/` are a deliberate metadata subset (only the columns
the importer reads), **not complete FOCUS 1.2 documents** — so the conformance gate
validates the 1.3 *output*, not the v1.2 *input*. A full-fixture 1.2 input-validation
leg (against the vendored `scripts/focus-ruleset-1.2/`) is a documented fast-follow.
