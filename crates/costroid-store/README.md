# costroid-store

Costroid's local SQLite usage store: a persistent ledger of FOCUS usage rows kept
entirely on the machine.

Storage is SQLite (`rusqlite` with the `bundled` C amalgamation — no system SQLite
required). It is a leaf crate (no `costroid-core` dependency yet; replay/aggregate is a
later milestone) and is **not** reachable from the default `costroid` CLI build: it is
linked only behind the CLI's off-by-default `store` feature.

## R4 — the Cardinal Rule: metadata only

The single `usage_rows` table is an **explicit metadata allowlist**. It stores only the
bounded, non-free-text metadata needed to reconstruct a `FocusRecord` on replay (token
counts, model, lane, costs as decimal strings, timestamps, provider identity, SKU
identifiers). It deliberately **drops every free-text-capable or non-derivable FOCUS
column** — `ChargeDescription` (re-derived on replay), `ResourceId`/`ResourceName`,
`Tags`, `SkuPriceDetails`, region/sub-account columns, and so on. The store is
*structurally incapable* of holding prompt or response content. A fail-closed schema
test asserts the `CREATE TABLE` DDL contains no forbidden substring and that every
column belongs to the documented allowlist.

## Money is a decimal string, never a float

All cost columns are persisted as `TEXT` decimal strings (mirroring the FOCUS export),
never `f64` — exact decimal arithmetic, no binary-float drift.
