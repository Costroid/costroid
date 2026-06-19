# Costroid-Next — M1 detailed implementation plan

> **Provenance:** synthesized by the M0→M1 design workflow (2026-06-19): 4 sub-area designers
> (event-model · store · FOCUS-mapping · collectors) → adversarial repo-fit verification against the
> repo's hard gates (MSRV/offline/deny/Cardinal-rule — the same lens that rejected DuckDB) → synthesis.
> Line numbers/symbols were verified against the tree at synthesis time; **re-verify before editing —
> the code wins.** Tracked from [`../PROGRESS.md`](../PROGRESS.md). **Status: PLAN — awaiting human
> sign-off on the export-schema additions (T2/T3) + the `import` CLI subcommand (T19) before execution.**

# M1 detailed plan — FOCUS three-lane ledger foundation

Synthesized from four repo-fit-verified sub-area designs (event-model, store,
focus-mapping, collectors) + their adversarial verdicts. All line numbers /
symbols below were re-verified against the actual tree (the code wins).

## 0. Scope in one sentence

M1 EXTENDS the v0.6.0 FOCUS-1.3 emitter into a three-lane ledger
(developer-tool / cloud-API / local-inference) by adding: (a) a top-level
`x_Lane` discriminator + the canonical event model, (b) a feature-gated SQLite
store, (c) a v1.2-IN → v1.3-OUT FOCUS importer with isolated version mapping,
(d) collector hardening (sidechain attribution), all proven by the existing
`scripts/focus_conformance.sh` + `offline.rs` machinery extended, never
replaced. Zero new deps reach the CLI graph except the feature-gated store.

---

## 1. M1 "done" criteria (close against this mechanically — never self-judged prose)

1. `scripts/focus_conformance.sh` exits 0 on **both** a CSV leg and a JSON leg,
   validating `export --format {csv,json}` against the **vendored 1.3.0.1
   ruleset offline** (`--block-download`), matching the exact-match
   `scripts/focus_known_failures.txt` contract (`report-fail-count` re-pinned
   deliberately in-lockstep with any fixture-row change).
2. The **synthetic-v1.2 round-trip leg** is green (v1.2-in → normalize → v1.3-out
   stays 1.3-conformant). The **real AWS v1.2 leg** is present-but-SKIPPED with a
   LOUD notice naming C1 (exit-90 SKIP idiom, like `offline_acceptance.sh`)
   until the C1 fixture lands. **[partially C1-dependent]**
3. ≥1 deterministic **collector golden test** (Claude + Codex) is green: token
   totals per type + `(message.id, requestId)` de-dup collapse + sidechain
   attribution pinned EXACT; asserts the normalized **FocusRecord** row (not just
   `UsageEvent`), to add value at the M1 boundary.
4. `cargo test -p costroid --test offline` passes with a new `STORE_ALLOWED`
   subset-allowlist: the `--features store` graph delta ⊆ `STORE_ALLOWED`, and
   the **default (store-off) CLI graph is rusqlite-free** (the existing
   `cli_default_build_links_no_network_tls_or_telemetry_crate` stays green
   byte-for-byte).
5. `cargo deny check licenses bans` (incl. the `--all-features` CI variant) is
   green with the store's transitive set on the allowlist (MIT/MIT-or-Apache/Zlib
   — **no CDLA-Permissive**). No Parquet tree in the M1 deciding test.
6. The **R4 no-content structural test** is green: a field-exhaustive
   destructuring (no `..` rest-pattern) of `FocusRecord` + the store row type +
   the SQLite `CREATE TABLE` DDL string proves no free-text content column;
   `charge_description` asserted to stay the derived `"{model} {token_type}
   tokens"` form.
7. The store builds on **MSRV 1.88** and on **macOS + Windows** (bundled
   `libsqlite3-sys` C compile in the cross-platform CI job).
8. **No `unwrap`/`expect`/`panic!`** in any lib crate (incl. tests); all new
   constructors/normalizers/readers return `Result` via `thiserror`.
9. Every new extension column is **x_PascalCase**; `focus_known_failures.txt`
   re-pinned in the same change as any column/fixture change.
10. The "lanes never summed across" invariant is a **typed guard**: every $-summer
    in core gates on `x_Lane == developer_tool` before its existing
    `CostLane::Api` filter; a mixed-lane test proves a cloud/local row can never
    inflate the dev-tool total; **all v0.6.0 (developer_tool-only) tests stay
    green**.
11. The stale milestone prose in `docs/COSTROID-NEXT.md` and `PROGRESS.md`
    ("DuckDB + Parquet store") is corrected to "SQLite (rusqlite, bundled);
    Parquet deferred to a separate spike" so the milestone the deciding test
    closes against matches the resolved A5/R11 decision.

---

## 2. Ordered task list (dependency-correct; spikes first)

Order: doc-fix → Parquet spike (gate) → event model → store → FOCUS
mapping/collectors → deciding test/CI. Each task notes its deciding test, C1
status, and top risk + mitigation.

### T0 — Correct stale milestone prose (unblocks honest closure)
- **Do:** Edit `docs/COSTROID-NEXT.md:141` and `PROGRESS.md:142/254` from
  "DuckDB/Parquet store" → "SQLite (rusqlite, bundled); Parquet deferred to a
  separate spike". Drop the M1 detailed plan under a new heading.
- **Deciding test:** none (docs); reviewed in the same PR.
- **C1:** no.
- **Risk:** plan diverges from milestone text → a future reader wrongly calls M1
  incomplete. **Mitigation:** land this first, in the same change set.

### T1 — Parquet writer SPIKE (gate, NOT adopted into the deciding test)
- **Do:** Spike `parquet`/`arrow` as a throwaway branch: pin exact versions, run
  `cargo deny check licenses bans`, run the `offline.rs` BFS forbidden-scan over
  all 6 targets, check MSRV 1.88. **STOP/ASK the human** if the tree introduces
  ANY CDLA-Permissive / non-allowlisted license OR any `ALWAYS_FORBIDDEN` crate
  (reqwest/hyper/tokio/webpki-roots) — this is the documented DuckDB failure
  mode. Default outcome: **defer Parquet**; CSV+JSON are the always-available
  exports.
- **Deciding test:** the spike's own pin+deny+forbidden-scan+MSRV pass/fail; on
  fail → no Parquet leg in T13.
- **C1:** no.
- **Risk (HIGH, DuckDB-class):** parquet/arrow pull a large tree.
  **Mitigation:** spike in isolation; the M1 deciding test must NOT depend on
  Parquet regardless of outcome.

### T2 — Event model: `LedgerLane` + mandatory `x_Lane` column (costroid-focus)
- **Do:** Add `pub enum LedgerLane { DeveloperTool, CloudApi, LocalInference }`
  (serde snake_case + `as_str()`, mirroring `FocusAccessPath`). Add
  `pub x_lane: String` (`rename = "x_Lane"`) to `FocusRecord`, placed FIRST in
  the x_ block. Add `lane: LedgerLane` to `UnpricedUsage`. **Compile fan-out (9
  sites) — set `lane = LedgerLane::DeveloperTool` at each:** focus/lib.rs
  470/526/623, core/lib.rs 1423/2975/3168/4428, core/reconcile.rs 557, cli
  reconcile.rs 265, cli tui.rs 1261/1827, cli render.rs 6006. Update the
  `ends_with` header assertion at **focus/lib.rs:787** to the new x_-tail order.
- **Deciding test:** extend `csv_header_carries_full_focus_column_set...` to
  assert `x_Lane` is in the x_ block and dev-tool rows carry
  `x_Lane=developer_tool`; JSON round-trip asserts `x_Lane` always present.
- **C1:** no.
- **Risk:** the 9-site fan-out + the `ends_with` pin (focus/lib.rs:787) are
  under-stated by the source design. **Mitigation:** enumerate all 9 sites in the
  PR; `FocusRecord` is built ONLY via `unpriced_usage()` (no direct literals, no
  `Default`), so the FocusRecord field additions themselves don't fan out — only
  the `UnpricedUsage` signature does. Confirm `FocusRecord` stays `PartialEq`-only
  (it is NOT `Eq`) so `Option<Decimal>` columns are derive-safe.

### T3 — Event model: the 8 local/cloud optional x_ columns (costroid-focus)
- **Do:** Add `x_MeasuredWh`/`x_AmortizedHwCost`/`x_AvgPowerWatts`/
  `x_CloudEquivCost` as `Option<Decimal>` (**`serialize_with =
  serialize_decimal_opt`**, NOT `serialize_decimal`) and
  `x_HardwareProfile`/`x_RuntimeKind`/`x_BenchmarkId`/`x_MeasurementMode` as
  `Option<String>`, all x_PascalCase, trailing the existing 8 + `x_Lane`. Names
  verbatim from COSTROID-NEXT §6.4. Dev-tool path leaves all 8 `None`.
- **Deciding test:** a dev-tool record serializes all 8 as null/empty; a local
  record serializes `x_MeasuredWh` with a decimal point + `x_MeasurementMode` as
  string.
- **C1:** no.
- **Risk:** using `serialize_decimal` would emit `0.0` on non-local rows, falsely
  implying a real measurement (violates R6 honesty). **Mitigation:** mandate
  `serialize_decimal_opt`; metadata-only bounded numbers/IDs (no text).

### T4 — Event model: `CanonicalEvent` + `CloudUsageEvent` + `LocalRunEvent` (costroid-providers)
- **Do:** Add `pub enum CanonicalEvent { Tool(UsageEvent), Cloud(CloudUsageEvent),
  Local(LocalRunEvent) }`; leave `UsageEvent` (lib.rs:136-147) **byte-identical**.
  `CloudUsageEvent` = FOCUS-1.2-import metadata; `LocalRunEvent` = token counts +
  energy/power (`f64`) + measurement_mode (`String`). Derive
  Debug/Clone/PartialEq/Serialize/Deserialize. **Carry lane + measurement-mode as
  plain `String`/own-enum at the providers boundary — NO `costroid-focus` or
  `costroid-power` dep** (providers must stay internal-dep-free).
- **Deciding test:** serde round-trip per variant; an R4 structural test that
  FAILS if a `String` field named prompt/completion/content/text/message is ever
  added to the two new structs.
- **C1:** no.
- **Risk:** scope creep (these are M2/M3-populated) + accidentally adding a
  `costroid-*` dep to providers. **Mitigation:** define minimally from doc
  3.1.A/B + 6.4; add a doc-comment/test forbidding any `costroid-*` dep in
  providers.

### T5 — Event model: core normalizers + lane-tagged dispatch (costroid-core)
- **Do:** Next to `push_meter_records` (core/lib.rs:1400) add
  `local_run_to_focus()`, `cloud_usage_to_focus()`, and
  `focus_records_from_canonical(&[CanonicalEvent]) -> Result<Vec<FocusRecord>,
  CoreError>` dispatching by variant (Tool→developer_tool, Cloud→cloud_api,
  Local→local_inference). Local rows set the 8 local x_ columns; convert `f64`→
  `Decimal` only here. `focus_records_from_usage` (lib.rs:99) unchanged. Add
  `CoreError::Import`.
- **Deciding test:** synthetic `LocalRunEvent` → FocusRecord with
  `x_Lane=local_inference`, `x_MeasuredWh` populated, never `developer_tool`;
  `CloudUsageEvent` → `x_Lane=cloud_api`.
- **C1:** no.
- **Risk:** core gaining `costroid-power`'s `power` feature → breaches R1 +
  offline surface. **Mitigation:** core consumes ONLY plain `f64`/`Decimal` from
  `LocalRunEvent` (which lives in providers, not power); add a CI grep / assertion
  that `cargo build -p costroid` (default) links no power path.

### T6 — Event model: typed lane-separation guard in every $-summer (costroid-core)
- **Do:** Gate every dollar-summer on `x_Lane == developer_tool` BEFORE its
  existing `CostLane::Api` filter: `api_lane_*` (lib.rs:159/188), budget
  (458-474), forecast (581-583), anomalies (844-851),
  `reconcile::api_lane_daily_usd_series`. Add separate per-lane summers
  (cloud_api $, local_inference $), never folded into the dev-tool total. Keep the
  `CostLane` match exhaustive + dev-tool-scoped.
- **Deciding test:** a snapshot mixing one developer_tool/Api + one cloud_api +
  one local_inference row → dev-tool API total = ONLY the dev-tool row; cloud/
  local totals on their own summers. All v0.6.0 tests stay green.
- **C1:** no.
- **Risk (HIGHEST BLAST RADIUS):** miss one summer → a cloud/local row silently
  inflates the now-header/budget/forecast. **Mitigation:** cover EACH summer with
  the mixed-lane test; this is the typed enforcement of "never summed across
  lanes."

### T7 — Close R14: serde round-trips (test-only; no enum widening)
- **Do:** `LimitKind` already has Daily/Monthly/BillingCycle and `LimitMeasure`
  already has `Spend` (providers/lib.rs:149-176); all consumers match the arms
  (bar banner.rs:129-133, cli render.rs:2494-2496). Add the missing serde
  round-trip tests for the three newer kinds + `LimitMeasure::Spend`. **Do NOT
  re-fork `LimitWindow`.**
- **Deciding test:** round-trip `LimitWindow` for kind ∈ {Daily,Monthly,
  BillingCycle}, measure ∈ {TokenFraction, Spend{used_usd,included_usd}},
  asserting wire forms (`five-hour`, `billing-cycle`, `token_fraction`, `spend`).
- **C1:** no.
- **Risk:** over-engineering by re-forking. **Mitigation:** test-only; the model
  is already present + consumed.

### T8 — Expose a public aggregation entry in core (unblocks store replay)
- **Do:** `summarize_rows` is **private** (core/lib.rs:1729) and there is no
  public `aggregate_by`. Add a stable public entry (e.g.
  `pub fn aggregate_rows(rows, group_by) -> Vec<CostLaneSummary>`) that the store
  replay path calls. Edge stays store → core (core must never depend on store).
- **Deciding test:** unit test that `aggregate_rows` over a fixture matches the
  existing internal summary.
- **C1:** no.
- **Risk:** the store sub-area assumed `summarize_rows` was reusable; it is not.
  **Mitigation:** expose the API explicitly as its own task BEFORE the store.

### T9 — Store: scaffold a feature-gated `costroid-store` crate (MSRV proof FIRST)
- **Do:** New `costroid-store` leaf crate depending only on `costroid-focus`
  (+ core via the bridge), reachable from the CLI ONLY behind a `store` feature,
  using `rusqlite` (bundled `libsqlite3-sys`). **MSRV is a GATE, not a comment:**
  run `cargo +1.88.0 build -p costroid --features store` in CI before adopting the
  pinned version. If it fails 1.88, isolate as a standalone crate/binary with its
  OWN `rust-version` (the `apps/server` / `costroid-power` precedent) NOT reachable
  from the 1.88 CLI graph.
- **Deciding test:** default `cargo build` links no `libsqlite3-sys`;
  `cargo +1.88.0 build -p costroid --features store` green.
- **C1:** no.
- **Risk (HIGH):** `rusqlite`/`libsqlite3-sys` 0.37 MSRV vs the 1.88 ceiling is
  UNVERIFIED. **Mitigation:** prove 1.88 before pinning; the M0 spike recorded
  rusqlite=0.37.0/libsqlite3-sys=0.35.0 bundled clean on 1.88 — re-prove after
  actual wiring (the spike was pre-integration).

### T10 — Store: metadata-only WHITELIST schema (NOT a 1:1 column mirror)
- **Do:** Schema is an explicit **metadata-column allowlist** (tokens, costs as
  `Decimal`→TEXT not `f64`, timestamps, model/tool/session/sku IDs, x_* metadata,
  `limit_window`), NOT a mechanical col-per-FocusRecord-field. **DROP or
  hash-only** the free-text-capable FOCUS columns
  (`charge_description`/`resource_name`/`resource_id`/`tags`/`allocated_tags`/
  `sku_price_details`). Persist `schema_version` + `focus_version` (R14-adjacent),
  additive `ADD COLUMN` migrations only.
- **Deciding test:** round-trip; **"schema columns ⊆ approved metadata allowlist"**
  (fail-closed, mirroring `CONNECT_ALLOWED`); the same assertion on the ingest
  mapper so a C1 AWS row cannot smuggle text into a retained column.
- **C1:** no (schema buildable now; the AWS-ingest assertion proven on synthetic).
- **Risk (HIGH, R4):** a 1:1 mirror is structurally capable of holding text, and
  the C1 AWS ingest path is exactly where external free text could enter — a
  "no column named content/prompt" test does NOT prevent text in
  `charge_description`. **Mitigation:** whitelist + fail-closed subset assertion
  on both schema and mapper.

### T11 — Store: ingest + replay reusing the public aggregation + export
- **Do:** Ingest `FocusRecord` rows (one canonical persisted shape; keep
  `CanonicalEvent` in-memory-only). Replay reuses `aggregate_rows` (from T8);
  `export --out` reuses the existing `export_focus_{json,csv}`.
- **Deciding test:** ingest→replay→export round-trips through
  `focus_conformance.sh` clean.
- **C1:** no.
- **Risk:** double-counting on replay. **Mitigation:** store FocusRecord rows
  (not events); reuse the single existing exporter path.

### T12 — Store: `STORE_ALLOWED` offline guard (static + runtime)
- **Do:** Two artifacts mirroring `CONNECT_ALLOWED`/`SERVER_ALLOWED`:
  **(a) static** — a `cli_store_feature_admits_only_STORE_ALLOWED` subset test in
  `offline.rs` (delta of `--features store` over the default CLI ⊆
  `STORE_ALLOWED = {rusqlite, libsqlite3-sys, cc, pkg-config, vcpkg}` + reviewed
  transitives) + a `#[ignore] print_store_delta` regenerator; assert the default
  graph is store-free. **(b) runtime** — extend
  `scripts/offline_acceptance.sh` to run the store path under network isolation
  and assert no `AF_INET`. (`cc`/`pkg-config` already in `CONNECT_ALLOWED`;
  `vcpkg`/`libsqlite3-sys` are new — review them.) `STORE_ALLOWED` must be a
  fail-closed SUBSET assertion, not a name list.
- **Deciding test:** `cargo test -p costroid --test offline` green with the
  store-delta assertion; default-build empty-allowlist forbidden test stays
  byte-for-byte clean with `store` off.
- **C1:** no.
- **Risk:** if the store roots at an **island crate not reachable from the CLI**,
  the delta test is vacuous and CI wiring claims break. **Mitigation:** land the
  store as a `store` FEATURE on a CLI-reachable crate so the existing
  `offline-acceptance` step covers it with no new job; otherwise add
  `costroid-store` as a 7th BFS root in `offline.rs` (a real code change).

### T13 — FOCUS mapping: version-detection seam + v1.2 reader (costroid-providers)
- **Do:** New `crates/costroid-providers/src/focus_import.rs`. Add
  `csv.workspace = true` to **providers/Cargo.toml** (it is NOT there today —
  only focus has `csv`; benign, no new BFS node). Add `FocusInputVersion {V1_2,
  V1_3, Unknown(String)}`, `detect_version()` (default V1_2 + recorded caveat when
  no marker), the `FocusInputMapping` trait, and `FocusV12Mapping` (the ONLY place
  v1.2 column names appear — v1.4 is a sibling impl). Add `RawFocusRow`
  (metadata-only; **explicitly NO prompt/completion fields, and NO
  ChargeDescription/ResourceName/Tags/SkuPriceDetails** — synthesized downstream)
  + `MappedUsage` (mirrors `UnpricedUsage`). `import_focus_v12_csv/json` returning
  `Result<_, FocusImportError>` (thiserror, `#[from]` csv/serde_json). Stamp the
  source spec version on `x_FocusInputVersion` (x_PascalCase).
- **Deciding test:** `detect_version()` → V1_2 for marked + unmarked (default+
  caveat) fixtures, `Unknown` → `FocusImportError::UnsupportedVersion` (no panic);
  R4 test asserts no free-text input column is retained on `MappedUsage`.
- **C1:** **synthetic now; real AWS column shape is C1** (see T15).
- **Risk:** over-fitting the trait to v1.2; v1.2 token-column spellings differ
  between AWS and LiteLLM. **Mitigation:** all column names ONLY inside
  `FocusV12Mapping`; synthetic fixtures built to the published 1.2 spec so the C1
  correction is one-file localized.

### T14 — FOCUS mapping: core bridge with source-priced branch (costroid-core)
- **Do:** `focus_records_from_v12_import(&[MappedUsage]) -> Result<Vec<FocusRecord>,
  CoreError>`. **Source-priced rows are a NEW assembly branch, NOT
  `apply_pricing`** — `apply_pricing` (core/lib.rs:1456) recomputes `cost =
  per_token × tokens` and OVERWRITES the cost columns; for a foreign-authoritative
  cost, set the four cost columns + PricingQuantity/SkuPriceId directly after
  `unpriced_usage` and stamp `x_Estimated=false`. Unpriced foreign rows take the
  existing estimate path. **PricingStatus decision (resolved):** reuse the
  existing `"priced"` status, carrying the source-authoritative distinction on
  `x_Estimated=false` alone — adding a new `priced_by_source` constant would break
  `window_estimated_usd` (lib.rs:1859, returns None for status ≠ exactly
  `"priced"`) and `PricingCoverage::add` (lib.rs:2257-2260). If a new constant is
  ever required, BOTH those sites must be updated + regression-tested.
- **Deciding test:** synthetic priced v1.2 row → FocusRecord with that exact cost
  preserved, never overwritten by the bundled catalog, and contributing to the
  estimated-USD window; unpriced row → repriced like local logs. No x_snake_case
  appears.
- **C1:** no (synthetic).
- **Risk:** dropping/double-pricing a source cost; the new-status downstream
  breakage. **Mitigation:** explicit source-priced vs needs-pricing branch; reuse
  `"priced"`; reuse the cost-invariant test.

### T15 — Collectors: sidechain attribution + golden tests (costroid-providers/core)
- **Do:** Add `is_sidechain: bool` (parse top-level `isSidechain` via
  `get(..).and_then(Value::as_bool).unwrap_or(false)` — no unwrap/expect) +
  `AttributionConfidence` enum (default `Confident`) to `UsageEvent`. Sidechain
  rows → `Uncertain`, **keep counting** (annotate, do NOT change the
  `(message.id, requestId)` de-dup). Add `x_Sidechain` +
  `x_AttributionConfidence` columns AFTER `x_ConsumedTokens` (flat scalar
  String via `as_str()`, like `x_AccessPath` — NOT tagged-enum serde) AND update
  the `ends_with` header assertion (focus/lib.rs:787). Thread both through
  `UnpricedUsage` → all 4 meter rows in `push_meter_records` (1423). Add a
  deterministic Claude+Codex golden asserting the normalized **FocusRecord**
  (token totals per type, dedup collapse, sidechain tag). Add `x_CollectorVersion`
  const + a `docs/limitations.md` undercount note. **Codex `input_tokens` is
  cache-INCLUSIVE** (saturating_sub of cached, providers/lib.rs:1023); **Claude
  input is cache-EXCLUSIVE** (no subtraction) — do NOT introduce a subtraction on
  the Claude side.
- **Deciding test:** golden snapshot fails on any token-total/dedup/sidechain
  drift; R4 test asserts neither `UsageEvent` nor `FocusRecord` gains a String
  field sourced from message/content.
- **C1:** no (committed synthetic fixtures only).
- **Risk:** breaking the `ends_with` pin + the per-meter fan-out; mis-stating
  Codex cache semantics. **Mitigation:** update the header string (single source
  of truth) in the same change; assert the FOCUS row, not just the event.

### T16 — R4 no-content structural test (costroid-focus + costroid-store)
- **Do:** Field-exhaustive destructuring of `FocusRecord` + the store row type
  **without `..`** so adding any field is a COMPILE error until consciously
  classified; assert the SQLite `CREATE TABLE` DDL has no unconstrained TEXT
  content column; assert `charge_description` stays the derived
  `"{model} {token_type} tokens"` (focus/lib.rs:375).
- **Deciding test:** the destructuring test compiles only when every field is
  enumerated + classified metadata; DDL scan passes.
- **C1:** no.
- **Risk:** a name-allowlist drifts silently. **Mitigation:** the
  no-`..`-rest-pattern destructuring converts drift into a forced compile-time
  decision.

### T17 — Deciding test + CI wiring (the M1 gate)
- **Do:** EXTEND `scripts/focus_conformance.sh` (do not replace): add a **JSON
  leg** (export `--format json`, extract the `rows` array from the
  `FocusExportEnvelope` — the validator reads tabular CSV, not the
  `{focusVersion, rows}` envelope — serialize to CSV and re-validate against the
  SAME 1.3 ruleset + allowlist, OR assert JSON-rows == CSV-rows both
  validator-clean) and a **synthetic-v1.2 round-trip leg** (v1.2-in → v1.3-out,
  validate the re-emitted 1.3 output). Re-pin `focus_known_failures.txt` (counts +
  `report-fail-count = 9`) deliberately if validated rows change. Extend the CI
  `focus-conformance` job to run both legs (no new job). Store proof rides
  existing jobs: `offline-acceptance` (static store-delta), `cross-platform`
  (store compiles macOS+Windows), `license` (cargo-deny `--all-features`).
- **Deciding test:** all seven CI jobs green: pre-pr (fmt/clippy/test incl. golden
  + R4 + mixed-lane), cross-platform, msrv (store on 1.88), focus-conformance
  (CSV+JSON+synthetic-v1.2), license (store deps allowlisted, no CDLA),
  advisories, offline-acceptance (static store-delta + dynamic no-egress).
- **C1:** **partially** — the real-AWS-v1.2 leg is C1-blocked; M1 closes on the
  synthetic round-trip with the real leg present-but-SKIP-honest.
- **Risk:** the validator may be CSV-only AND cannot read the JSON envelope.
  **Mitigation:** settle empirically first (`python -m focus_validator.main
  --data-file <json> ...`); if CSV-only, extract `rows` → CSV before validation.

### T18 — C1-GATED: real AWS v1.2 sample + true the mapping (BLOCKED on C1)
- **Do:** When C1 lands (official FOCUS v1.2 schema + redistribution-license-
  confirmed AWS Data Exports FOCUS sample): vendor `fixtures/focus/v1.2/` (+ README
  provenance/license note), reconcile `FocusV12Mapping` column names/nullability
  to the REAL schema, add a v1.2-input conformance fixture, flip the real-v1.2 leg
  from SKIP to asserting clean round-trip, re-pin known-failure counts in the same
  commit.
- **Deciding test:** `focus_conformance.sh` green on the REAL AWS v1.2 sample →
  v1.3 export; real v1.2 input validates against the validator's bundled 1.2.0.1
  model; sample license confirmed redistributable in its README.
- **C1:** **YES — blocked.**
- **Risk:** real AWS columns diverge from the published spec; the sample may be
  non-redistributable. **Mitigation:** the isolation seam (T13) localizes the fix
  to `FocusV12Mapping` + the fixture; fallback is a permanent spec-built synthetic
  + a documented manual-validation step. **Never block M1 closure on this.**

### T19 — ASK-HUMAN-GATED: public `costroid import` CLI subcommand
- **Do:** Wiring `focus_records_from_v12_import` to `costroid import --format
  focus-csv|focus-json --version auto|1.2 <path>` CHANGES the public CLI surface →
  **requires human sign-off per CLAUDE.md "Decide vs ask"** (also covers the
  `x_FocusInputVersion`/`x_Sidechain`/`x_AttributionConfidence` output-schema
  additions, and whether M1 may close on the synthetic v1.2 leg without C1). The
  library path (T13/T14) ships regardless; only the user-facing command needs
  approval.
- **Deciding test:** after sign-off, `cargo test -p costroid --test offline` stays
  green (no new CLI dep edge); a CLI integration test imports a synthetic v1.2
  fixture and emits schema-valid v1.3 byte-identical to the library path.
- **C1:** no (but human-gated).
- **Risk:** building before sign-off violates the operating manual.
  **Mitigation:** land the library API first; gate the subcommand on approval.

---

## 3. C1 dependency map

**Buildable NOW on synthetic fixtures (NOT C1-blocked):** T0–T17, T19.
- The entire event model (T2–T7), store (T8–T12), the v1.2 reader + mapping seam +
  core bridge + collectors (T13–T15), R4 test (T16), and the deciding-test
  machinery with the **synthetic** v1.2 round-trip leg + JSON leg (T17). The
  `FocusV12Mapping` + `RawFocusRow` are built to the **published FOCUS 1.2 spec**
  so the diff to real AWS is localized to one file when C1 lands.

**BLOCKED on C1 (the real FOCUS v1.2 schema + a redistribution-license-confirmed
AWS Data Exports FOCUS sample, CI is offline):**
- **T18 only** — vendoring the real AWS sample as an offline CI fixture, truing
  `FocusV12Mapping` to the real column shape/nullability, the v1.2-input
  conformance fixture, and flipping the real-v1.2 conformance leg from SKIP to
  asserting.
- **M1 closes WITHOUT C1** (criterion #2): the synthetic round-trip is green, the
  real leg is present-but-SKIPPED with a loud C1 notice. Confirm this posture with
  the human at the M0→M1 checkpoint (T19).

---

## 4. Cross-cutting risks to resolve EARLY (raised by the verifiers)

1. **Lane-summer fan-out (highest blast radius — T6).** Every $-summer
   (api_lane_* lib.rs:159/188, budget 458-474, forecast 581-583, anomalies
   844-851, reconcile) must gate on `x_Lane==developer_tool` or a cloud/local row
   silently inflates the dev-tool total. Cover each with the mixed-lane test
   before any cloud/local producer exists.
2. **`UnpricedUsage.lane` 9-site compile fan-out + the `ends_with` header pin
   (T2).** Under-stated by the event-model design — enumerate all 9 sites + update
   focus/lib.rs:787 in the same change.
3. **Store placement decides everything downstream (T9/T12).** `store` must be a
   FEATURE on a CLI-reachable crate (not an island crate), or the offline
   store-delta test is vacuous, MSRV/CI-wiring claims break, and the default CLI
   stays store-free only by accident. Resolve before T9.
4. **Store schema must be a metadata WHITELIST, not a 1:1 mirror (T10).** A
   col-per-field mirror materializes free-text FOCUS columns
   (charge_description/resource_name/tags/sku_price_details) — structurally
   capable of holding text, especially on the C1 AWS ingest path. Fail-closed
   subset assertion on schema AND mapper.
5. **`summarize_rows`/`aggregate_by` are PRIVATE in core (T8).** The store replay
   plan has no public API to call — expose `aggregate_rows` first.
6. **Source-priced import is a NEW branch, not `apply_pricing` (T14)**, and a new
   `priced_by_source` status would break `window_estimated_usd` (lib.rs:1859) +
   `PricingCoverage::add` (lib.rs:2257) — reuse `"priced"` + `x_Estimated=false`.
7. **MSRV 1.88 of rusqlite 0.37 is UNVERIFIED post-integration (T9).** Prove
   `cargo +1.88.0 build -p costroid --features store` as a GATE before pinning.
8. **Parquet is a SPIKE, never a deciding-test dependency (T1).** STOP/ASK the
   human on any CDLA/forbidden crate; CSV+JSON are the always-available exports.
9. **JSON-validator input format is unverified (T17).** The validator reads
   tabular CSV, not the `{focusVersion, rows}` envelope — settle empirically and
   extract `rows` first.
10. **`focus_known_failures.txt` is an exact-match contract (`report-fail-count =
    9`).** Adding x_ columns alone is spec-safe; ANY new validated fixture row
    changes per-row counts — re-pin deliberately in the same commit, never let "a
    new violating row hide inside a known-defective rule."
11. **providers must stay internal-dep-free (T4/T13).** Carry lane +
    measurement-mode as plain String at the providers boundary; `csv` must be
    ADDED to providers/Cargo.toml (it is not there today) — benign, no new BFS
    node, but state it explicitly.

---

## 5. T1 Parquet spike — RESULT (run 2026-06-19)

Ran per T1 (throwaway, like the M0 spikes): `parquet = "=59.0.0"`, repo `deny.toml`.

| Check | Result |
|---|---|
| Packages | 90 (vs rusqlite's 15) |
| ALWAYS_FORBIDDEN crates | **none** (no reqwest/hyper/tokio/webpki-roots — the bare `parquet` crate, no `arrow` feature, avoids the DuckDB tree) |
| `cargo deny check licenses bans` | **ok** |
| Non-permissive licenses | **NONE** (all permissive) |
| MSRV 1.88 | **builds** |

**Outcome:** Parquet is **viable + gate-clean** — but its 90-package surface + multiple C-compression
libs (zstd-sys/lz4/brotli) add real cross-OS C-build weight for a **non-essential** export.
**Decision: DEFER Parquet from the M1 critical path** (CSV + JSON are the M1 deciding-test exports,
already shipped). Parquet becomes an optional output (behind the `store` feature or a fast-follow) —
the M1 deciding test never depends on it (criterion #5). Revisit when columnar export earns its weight.
