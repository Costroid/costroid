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

## Source-priced cloud rows carry the foreign per-token rate (M2)

A FOCUS-imported cloud row that carries an authoritative cost is **source-priced**: that
cost is preserved exactly (`x_Estimated = false`). As of the M2 cloud lane, when the
foreign export also carries its own pricing detail (`SkuPriceId` + `ListUnitPrice` /
`ContractedUnitPrice` + `PricingQuantity`), Costroid **carries it through verbatim**, so a
source-priced row is *fully* priced — not just costed. (When the export has **no**
`SkuPriceId`, FOCUS requires the pricing-detail columns be null, so they stay null; the
cost is still exact.) A *usage-only* imported row — no source cost — is instead
re-estimated from the layered catalog like a local log, and gets a catalog `SkuPriceId` +
rate + the `x_PricingSnapshotId` provenance stamp. (Source-authoritative rows carry **no**
`x_PricingSnapshotId` — they are the bill, not an estimate against a snapshot.)

## FOCUS v1.2 import fixtures are a metadata subset

The committed `fixtures/focus/v1.2/` are a deliberate metadata subset (only the columns
the importer reads), **not complete FOCUS 1.2 documents** — so the conformance gate
validates the 1.3 *output*, not the v1.2 *input*. A full-fixture 1.2 input-validation
leg (against the vendored `scripts/focus-ruleset-1.2/`) is a documented fast-follow.
