# Costroid-Next — M2 detailed implementation plan

> **Provenance:** synthesized at the M2 `/goal` kickoff (2026-06-20) from the four M2 seeds
> in [`../PROGRESS.md`](../PROGRESS.md) + the canon (`docs/COSTROID-NEXT.md` §3.3 M2 / §6.4 /
> the ⚑ Readiness gate D) + a repo-fit code map of the **merged** M1 cloud-lane machinery
> (`CloudUsageEvent`, `cloud_usage_to_focus`, `focus_records_from_v12_import`, `apply_pricing`,
> the `PricingCatalog`, the `reconcile`/`vendor_report` engine, the lane guard, the `import`
> CLI). Line numbers/symbols were verified against the tree at synthesis time; **re-verify
> before editing — the code wins.** Tracked from [`../PROGRESS.md`](../PROGRESS.md).
>
> **Status: EXECUTED — T0–T14 COMPLETE on branch `costroid-next` (2026-06-20).** The §1.5
> decisions (D1–D6) were signed off (all recommended); every T-task landed on the per-task
> dev-loop (build → independent adversarial review → fold-in → commit), each green. Both M1
> deferrals closed (the T14 per-token-rate TODO → M2 T4; the v1.2-input leg → M2 T9). The M2
> milestone-boundary **clean-build re-verify is done** (`cargo clean` → rebuild: fmt · clippy ·
> `test --workspace` (25) · store CLI (5) · deny default + all-features · offline-acceptance
> (7) · pricing-snapshot integrity · focus-validator conformance (8 OK legs) · MSRV 1.88 — all
> GREEN). **⛔ Awaiting the human's full fresh-eyes review before any merge to `main`**
> (milestone-boundary cadence; the agent does not merge). See the handoff in
> [`../PROGRESS.md`](../PROGRESS.md).

# M2 detailed plan — the cloud/API cost lane

## 0. Scope in one sentence

M2 turns the M1 cloud-lane *scaffold* (which already mints `cloud_api` `FocusRecord`s and
reprices usage-only imports from the **small curated** catalog) into a real cloud lane:
(a) a **bundled, dated, hashed LiteLLM pricing snapshot + user override**, layered under the
curated catalog and stamped with its provenance (R8); (b) **carrying the foreign
authoritative pricing detail** (SkuPriceId / unit-prices / PricingQuantity / multi-currency)
through the import so a source-priced row is *fully* priced — closing the M1 T14 deferral;
(c) **truing the synthetic AWS Data Exports FOCUS fixtures** to real column shapes + a
**Bedrock Application Inference Profile** attribution path, and **wiring the deferred
v1.2-INPUT validation leg**; (d) a **merged dev-tool + cloud_api ledger** where lanes stay
separate in every total and only `grand_total_usd` crosses, with the authoritative-invoice
(AWS FOCUS) vs estimate (LiteLLM-priced API logs) distinction stamped and reconcilable. All
ingest is a **user-provided exported file, parsed pure-local in providers/core**; the live
AWS/Bedrock API path is `connect`-gated + C4-gated + needs a **separate** sign-off and is
**not built** in M2. Zero new CLI-reachable crate deps (the offline gate stays byte-for-byte).

---

## 1. M2 "done" criteria (close against this mechanically — never self-judged prose)

1. **The merged-ledger deciding test is green** (the canon's M2 deciding test): a fixture
   test ingesting **dev-tool logs (developer_tool lane) + a synthetic AWS Data Exports FOCUS
   sample (cloud_api lane)** produces **one** `FocusRecord` ledger where: every dev-tool
   `$`-summer counts *only* dev-tool rows; per-lane totals (`lane_total_usd`) are independent;
   `grand_total_usd` is the only figure crossing lanes; and the merged CSV/JSON export
   validates clean through `scripts/focus_conformance.sh` (subset contract, no new failing
   rule).
2. **Source-priced rows are fully priced (T14 closed):** a synthetic priced v1.2/AWS row
   imports with its authoritative cost preserved exactly *and* `SkuPriceId` /
   `PricingQuantity` / `ListUnitPrice` / `ContractedUnitPrice` populated from the foreign
   export (no longer null); `docs/limitations.md`'s "source-priced rows have no per-token
   rate" caveat is **removed** and the in-code `TODO(deferred…)` in `cloud_usage_to_focus`
   is **gone**.
3. **LiteLLM snapshot integrity (R8):** the cloud lane reprices usage-only rows from a
   **bundled dated** LiteLLM-derived snapshot; **no runtime/build fetch** (proven: the
   offline-acceptance + forbidden-crates gates stay green byte-for-byte, and a test asserts
   the snapshot file's recorded `as_of` + `sha256` match its content); every estimated row is
   stamped `x_Estimated = true` + the chosen pricing-provenance stamp (§1.5 D1); an
   authoritative AWS-FOCUS row is stamped `x_Estimated = false`.
4. **Multi-currency (T-seed 2):** a non-USD source no longer errors; its native
   `BillingCurrency` is carried faithfully and **never auto-converted**; a cross-currency sum
   is refused/separated exactly as a cross-lane sum is (per-currency subtotals), proven by a
   mixed-currency test.
5. **v1.2-INPUT validation leg is wired** (closes the M1 fold-in deferral): a full-mandatory-
   column synthetic fixture validates against the vendored `scripts/focus-ruleset-1.2/` with
   `--validate-version 1.2`, with its own pinned 1.2 known-failure list; the two M1 READMEs +
   `docs/limitations.md` that described it as a "documented fast-follow" are updated to
   "wired".
6. **R4 holds on the cloud import:** the M1 no-`..` field-exhaustive forcing function still
   compiles over the (now wider) `RawFocusRow` / `CloudUsageEvent`; **no** new field is a
   `ChargeDescription` / `ResourceName` / `Tags` / free-text column; the Bedrock attribution
   carries **only a bounded id** (§1.5 D4), never a user-chosen name/tag.
7. **Offline guarantee intact:** `cargo test -p costroid --test offline` is green and the
   default CLI graph is byte-for-byte unchanged (M2 adds **zero** new CLI-reachable crate
   deps — data files + existing serde, no new Cargo edge); `scripts/offline_acceptance.sh`
   green.
8. The store persists any new x_ columns (schema-version bump + the metadata-allowlist subset
   assertion still fail-closed); the merged ledger round-trips ingest→replay→export
   byte-identical.
9. **No `unwrap`/`expect`/`panic!`** in any lib crate (incl. tests); all new
   readers/repricers/loaders return `Result` via `thiserror`; the user-override loader treats
   a missing/garbled file as a typed, non-fatal "use bundled" (zero-config default).
10. Every new extension column is **x_PascalCase**; `focus_known_failures.txt` (and the new
    1.2 list) re-pinned in the same change as any column/fixture change; the header-pin
    `ends_with` assertion in `costroid-focus` updated in lockstep.
11. Cross-OS compile green (the `cross-platform` CI job) + MSRV 1.88 (`-p costroid --features
    store`) + `cargo deny` (default + `--all-features`) green; the LiteLLM data file's license
    is verified permissive-redistributable and recorded with attribution (T1).

---

## 1.5 ⚑ DECISIONS TO SIGN OFF BEFORE CODING (CLAUDE.md "ask first")

> These are the **export/output-schema, public-CLI, and network/secrets** decisions M2
> cannot make unilaterally. **No T-task is coded until these are signed off.** Recommended
> defaults are marked ★; the four highest-leverage ones are also asked interactively.
>
> **✅ SIGNED OFF 2026-06-20 (all recommended ★):**
> - **D1 → (a)** one combined `x_PricingSnapshotId` = `"{source}@{as_of}#{short_sha}"`.
> - **D2 → (a)** layered `user-override > curated pricing.v1.json > LiteLLM long-tail`; LiteLLM
>   = vendored MIT data file compiled into core (date+sha256+attribution), dev-script refresh,
>   no build/runtime fetch.
> - **D3 → (a)** carry native `BillingCurrency`, never auto-convert; cross-currency refused/
>   separated into per-currency subtotals.
> - **D4 → (a)** carry only the bounded inference-profile id as `x_InferenceProfileId`; never
>   the profile name/tags.
> - **D5 → proceed as described** (no objection): `import` accepts AWS-shaped FOCUS + non-USD;
>   add `--pricing-override <path>` to `import`/`export`; no other new subcommand.
> - **D6 → proceed as described** (no objection): the live AWS/Bedrock **API** path is NOT
>   built in M2 (connect-gated + C4-gated + separately sign-off-gated); file-import only.

- **D1 — R8 pricing-provenance stamp (output schema).** R8 requires recording *source + date
  + content hash for every comparison*. Options: **(a) ★ one combined column
  `x_PricingSnapshotId`** = `"{source}@{as_of}#{short_sha}"` (leanest — one column, the M1
  "lean schema" lesson); (b) a **typed trio** `x_PricingSnapshotDate` + `x_PricingSnapshotHash`
  + `x_PricingSource` (queryable, M1-style); (c) **no new column**, reuse `x_Estimated` /
  `x_PricingStatus` only (does **not** record date/hash → weakest R8, not recommended).
  Populated on **estimated** rows; `None`/empty on **source-authoritative** invoice rows.
- **D2 — LiteLLM snapshot layering + vendoring posture (schema-adjacent + licensing).**
  **(a) ★ Layered precedence** `user-override > curated pricing.v1.json > LiteLLM long-tail`
  — LiteLLM fills the model long tail (Bedrock/OpenAI/Gemini/etc.) while the **curated**
  catalog stays authoritative for the dev-tool models (must not regress the
  *verified-to-the-cent-vs-ccusage* figures); the LiteLLM snapshot is a **vendored MIT data
  artifact** compiled into `costroid-core` like the existing `pricing.v1.json` (date + sha256
  + attribution in a sibling README), **refreshed only by a dev script** — never a build- or
  run-time fetch. ("Out of the crate dependency graph" = not a Cargo *crate* dep and not a
  fetch; it is a vendored data file, MIT being permissive ships fine inside the Apache-2.0
  binary with attribution.) (b) **Replace** `pricing.v1.json` wholesale with the LiteLLM
  snapshot (single source — risks regressing the to-the-cent dev-tool numbers). (c) Keep
  LiteLLM **dev/CI-only**, not compiled in (then cloud rows can't be repriced offline at
  runtime — breaks seed 1; not viable).
- **D3 — Multi-currency (semantics).** **(a) ★** Carry native `BillingCurrency` faithfully,
  **never auto-convert**; cross-currency totals refused/separated into per-currency subtotals
  (exactly like cross-lane). (b) Convert all to USD via a **bundled dated FX snapshot** (adds
  an FX data artifact + drift + a second R8 surface). (c) Keep refusing non-USD (defers the
  seed). No FX = no second pricing-integrity surface to defend.
- **D4 — Bedrock Application Inference Profile attribution (output schema + R4).** **(a) ★**
  Carry **only the bounded inference-profile id** (the system id / last ARN segment) as a
  whitelisted `x_InferenceProfileId`, enabling per-workload attribution while staying R4-safe;
  **never** the user-chosen profile *name* or cost-allocation *tags* (free text → R4
  violation). (b) **Defer** workload attribution — import Bedrock rows as plain `cloud_api`
  rows priced from the catalog (no per-workload split) in M2.
- **D5 — Public CLI delta (proceeding as described unless you object).** `import` is
  generalized to accept the real **AWS Data Exports FOCUS** column shape + **non-USD**
  (behavior change: it no longer errors on a non-USD bill — D3). Add **`--pricing-override
  <path>`** to `import` (and `export`) to point at the user override file (else the XDG
  default `~/.config/costroid/pricing-override.json`, missing = bundled). No other new
  subcommand. Re-verify the offline gate after the (dep-edge-free) wiring.
- **D6 — Network/secrets boundary (confirming the goal's own line).** The **live** AWS Data
  Exports / Cost Explorer / Bedrock API path (credential read, fetch) is **NOT built in M2**:
  it is `connect`-gated (keychain-only) **and** C4-gated, present-but-SKIP (env-gated loud
  SKIP, like M1's `COSTROID_REAL_AWS_FOCUS` leg), and building it later needs its **own**
  human sign-off. M2 ships **file-import-only**. The `costroid` CLI stays byte-for-byte
  no-network.

---

## 2. Ordered task list (dependency-correct; spike first)

Each task notes its deciding test, **C4** status (synthetic now vs the real-AWS leg), and the
top risk + mitigation. Order: prose → license/vendor spike → schema columns → catalog →
import detail → currency → repricing → fixtures → Bedrock → v1.2-input leg → merged ledger →
reconciliation → CLI → live-path-SKIP → deciding test.

### T0 — Correct stale prose + drop the M2 plan (honest closure)
- **Do:** Land this plan under a heading from `PROGRESS.md`. Pre-stage the doc edits the later
  tasks complete: `docs/limitations.md` ("Multi-currency import is an M2 cloud-lane feature",
  "Source-priced cloud rows have no per-token rate", "FOCUS v1.2 import fixtures are a
  metadata subset") and the two READMEs (`scripts/focus-ruleset-1.2/README.md`,
  `fixtures/focus/v1.2/README.md`) that call the v1.2-input leg a "documented fast-follow" —
  flip each to its M2-closed wording **in the task that actually closes it** (T5/T3, T9), not
  here, so prose never leads the code.
- **Deciding test:** none (docs); reviewed in the same PRs.
- **C4:** no. **Risk:** prose drifts ahead of code → reader thinks M2 closed early.
  **Mitigation:** each prose flip rides the commit that makes it true.

### T1 — LiteLLM data license + vendoring SPIKE (gate, like M1 T1)
- **Do:** Confirm the redistribution license of LiteLLM's
  `model_prices_and_context_window.json` (the LiteLLM repo is **MIT**; verify the data file
  carries no separate/stricter terms). Author `scripts/refresh_litellm_pricing.<sh|py>` (a
  **dev/CI-only** tool, *not* wired into build or the CLI) that fetches a pinned upstream
  revision and **transforms** it into Costroid's existing `PricingCatalog` JSON schema
  (provider/model/service_name/rates[meter,unit,price]) — pruned to the relevant providers
  (anthropic/openai/google/bedrock/…), keeping the artifact lean. Emit
  `crates/costroid-core/pricing/litellm-prices.<date>.json` + a sibling `README.md` recording
  **source URL, upstream pinned revision/date, fetched-on date, sha256, license + attribution**
  (the `scripts/focus-ruleset` posture).
- **Deciding test:** the spike's own pin + license check + `cargo deny check licenses bans`
  green with the data file present; a unit test asserts the committed file's recorded
  `as_of`/`sha256` match its bytes. **STOP/ASK the human** if the data license is not
  permissive-redistributable (the documented FOCUS-sample / DuckDB-license failure mode).
- **C4:** no. **Risk (license, any-one-fatal):** the data file is non-redistributable.
  **Mitigation:** spike the license first; fallback = ship only the curated `pricing.v1.json`
  (the cloud lane then prices only curated models, long tail = `unknown_model`, honestly).

### T2 — R8 pricing-provenance column(s) on `FocusRecord` (costroid-focus) **[D1 — schema]**
- **Do:** Per the signed-off D1, add the provenance column(s) (x_PascalCase) trailing
  `x_CollectorVersion`; default `None`/empty in `unpriced_usage` (so dev-tool + source-priced
  rows are unaffected). Update the header-pin `ends_with` assertion + the CSV-header golden in
  lockstep. Keep `FocusRecord` `PartialEq`-only (Option columns derive-safe).
- **Deciding test:** an estimated row serializes the provenance stamp non-empty; a
  source-authoritative row serializes it empty/null; the no-`..` R4 destructure (T16-class)
  still compiles with the new field classified as bounded metadata.
- **C4:** no. **Risk:** breaking the header pin / a downstream `==`-on-FocusRecord test.
  **Mitigation:** single-source the header string; run the full focus + core test set.

### T3 — Layered `PricingCatalog` + user override + provenance plumbing (costroid-core) **[D2]**
- **Do:** Generalize `PricingCatalog` to load **layered** sources with the D2 precedence
  (user-override > curated `pricing.v1.json` > `litellm-prices.<date>.json`), each
  `CatalogRate` carrying its **source label + as_of + snapshot sha256**. Add a typed
  user-override loader (XDG `~/.config/costroid/pricing-override.json`; missing/garbled =
  typed non-fatal → bundled only, zero-config). `apply_pricing` (lib.rs ~1644) stamps the D1
  provenance column(s) from the winning rate's source/as_of/hash. Keep `resolve_key`'s
  exact-match-then-strip-date-suffix logic; precedence resolves per-model, never blends two
  sources for one rate.
- **Deciding test:** an override entry beats the curated rate beats the LiteLLM rate for the
  same model; the stamped provenance reflects the **winning** source; a model only in the
  LiteLLM tier prices from it and stamps `litellm@<date>#<hash>`; the curated dev-tool models
  reprice **byte-identically** to today (no ccusage regression).
- **C4:** no. **Risk (HIGH):** layering silently regresses the to-the-cent dev-tool numbers.
  **Mitigation:** a regression test pinning the curated-model rates to their current values;
  curated tier always wins over LiteLLM.

### T4 — Carry foreign authoritative pricing detail through the import (providers/core) **[seed 2 — closes T14]**
- **Do:** Widen `RawFocusRow` (focus_import.rs) + `CloudUsageEvent` (providers/lib.rs:190)
  with the **bounded** FOCUS pricing-detail columns: `SkuPriceId`, `PricingQuantity`,
  `PricingUnit`, `ListUnitPrice`, `ContractedUnitPrice`, `PricingCurrency`, `EffectiveCost`,
  `ListCost`, `ContractedCost`, `PricingCategory`, `ConsumedUnit`, `ChargePeriodEnd`,
  `ChargeCategory` — **all `Option` + serde-renamed + `#[serde(default)]`**, all
  numbers/ids/enums (R4: no free text). In `cloud_usage_to_focus` (lib.rs:159) the
  source-priced branch now **populates** `sku_price_id` / `pricing_quantity` /
  `pricing_unit` / `list_unit_price` / `contracted_unit_price` / `pricing_category` from the
  foreign export (instead of leaving them null), keeping the authoritative cost verbatim +
  `x_Estimated=false`. **Delete** the `TODO(deferred…)` block (lib.rs ~186–191) and the
  `docs/limitations.md` "no per-token rate" caveat.
- **Deciding test:** a synthetic priced row imports with cost preserved exactly **and** the
  five pricing-detail columns populated from the source (asserted non-null + value-correct);
  the no-`..` R4 destructure still compiles (every new field bounded-metadata-classified); a
  usage-only row is unaffected (still repriced from the catalog).
- **C4:** no (synthetic). **Risk:** a source unit-price disagrees with its lump cost (rate ×
  qty ≠ cost). **Mitigation:** carry the source values **verbatim** (don't recompute);
  document that source self-consistency is the export's, not Costroid's, to guarantee.

### T5 — Multi-currency carry-through (providers/core) **[seed 2 — D3]**
- **Do:** Remove the M1 non-USD refusal in `FocusV12Mapping::map_row` (focus_import.rs:156);
  carry `BillingCurrency` + `PricingCurrency` faithfully onto the row's currency columns;
  **never auto-convert** (no FX). In core, the per-lane/grand totals refuse a cross-currency
  sum the way they refuse cross-lane — surface per-currency subtotals + a typed
  `CurrencyMismatch` rather than a silently-wrong USD figure. Flip the `docs/limitations.md`
  multi-currency caveat.
- **Deciding test:** a EUR row imports (no error), carries `BillingCurrency=EUR`, and is
  **excluded** from the USD `grand_total_usd`; a mixed USD+EUR fixture yields two per-currency
  subtotals, never one blended number.
- **C4:** no. **Risk:** a hidden assumption that the ledger is single-currency inflates a
  cross-currency total. **Mitigation:** the mixed-currency test on each total; mirror the
  cross-lane guard discipline.

### T6 — Generalize the cloud repricing path to the layered catalog (costroid-core) **[seed 1]**
- **Do:** Point `focus_records_from_v12_import` (lib.rs:247) at the layered catalog (T3) so
  **usage-only cloud rows** reprice from the LiteLLM long tail (Bedrock/OpenAI/Gemini models
  the curated catalog lacks) and stamp the D1 provenance; **source-priced rows untouched**
  (authoritative, via T4). This is the seed-1 "generalize the M1 bridge that already reprices
  from the bundled catalog".
- **Deciding test:** a usage-only OpenAI/Bedrock row (absent from the curated catalog) prices
  from the LiteLLM tier with `x_Estimated=true` + the LiteLLM provenance stamp; an unknown
  model stays `unknown_model` (no fabricated price).
- **C4:** no. **Risk:** the LiteLLM model-id spelling differs from the import's SkuId.
  **Mitigation:** reuse `resolve_key`'s suffix-tolerant match; unmatched → honest
  `unknown_model`, never a wrong rate.

### T7 — True the synthetic AWS Data Exports FOCUS fixtures (synthetic) **[seed 3]**
- **Do:** Expand `fixtures/focus/v1.2/` toward the **real** AWS Data Exports "FOCUS 1.2 with
  AWS columns" shape: the full FOCUS 1.2 **mandatory** column set + AWS `x_` extras
  (`x_ServiceCode` / `x_UsageType` / Bedrock rows) — **synthetic, no real billing data (R4)**.
  Add a **full-mandatory-column** fixture specifically for the T9 v1.2-input validation leg
  (the M1 fixtures are deliberate subsets). Keep the AWS `x_` extras present so the R4 drop is
  still exercised.
- **Deciding test:** the AWS-shaped fixture imports to `cloud_api` rows with the pricing
  detail (T4) + currency (T5) carried; the AWS `x_ServiceCode`/`x_UsageType` are dropped
  (R4); the full-mandatory fixture is structurally complete enough for T9.
- **C4:** **synthetic now; the real AWS sample is C4** (T13). **Risk:** synthetic columns
  diverge from a real export. **Mitigation:** build to AWS's *published* FOCUS-1.2 column
  dictionary; the one-file `FocusV12Mapping` seam localizes the C4 correction.

### T8 — Bedrock Application Inference Profile attribution (costroid-providers) **[D4 — schema/R4]**
- **Do:** Per the signed-off D4: recognize Bedrock service rows (ServiceName/x_ServiceCode);
  if D4(a), carry the **bounded inference-profile id** onto a whitelisted `x_InferenceProfileId`
  column (focus) + map Bedrock model ids to the catalog (T6). The id is the **system
  identifier only** — never the user-chosen profile name or tags.
- **Deciding test:** a synthetic Bedrock FOCUS row imports as `cloud_api`, prices from the
  catalog, and (D4a) carries the bounded profile id; an R4 test asserts no profile *name* /
  tag / free-text reached any column; (D4b) the row imports cleanly without an attribution
  column.
- **C4:** synthetic now; real Bedrock AIP export is C4 (T13). **Risk (R4):** the
  inference-profile *name* (user free text) leaks via attribution. **Mitigation:** whitelist
  the id field only; the no-`..` destructure forces classification of the new column.

### T9 — Wire the FOCUS v1.2-INPUT validation leg into `focus_conformance.sh` **[seed 3 — closes M1 deferral]**
- **Do:** Add a leg that validates the T7 full-mandatory fixture against the **vendored
  `scripts/focus-ruleset-1.2/`** with `--validate-version 1.2 --block-download` (the leg the
  M1 READMEs described but did not wire). Add a **pinned 1.2 known-failure list** (its own
  exact-match contract) + a `--rule-set-path scripts/focus-ruleset-1.2` invocation in
  `validate_csv` (or a sibling helper). Flip the two READMEs +
  `docs/limitations.md`-subset-note from "documented fast-follow" → "wired".
- **Deciding test:** `focus_conformance.sh` runs the new 1.2-input leg and it passes against
  the real `focus-validator` (locally via `uv`, as at the M1 boundary) + in the CI
  `focus-conformance` job (no new job).
- **C4:** no (synthetic full-mandatory fixture). **Risk:** the full-1.2 mandatory set surfaces
  many new validator findings. **Mitigation:** pin the 1.2 known-failure list deliberately in
  the same commit; the synthetic fixture is *complete*, so failures are validator defects, not
  Costroid gaps.

### T10 — Merged-ledger assembly + lane-integrity re-verification (costroid-core) **[seed 4 — the M2 deciding test]**
- **Do:** A path that produces **one** `FocusRecord` ledger from dev-tool rows + imported
  cloud rows (the natural concatenation that `aggregate_rows`/the store already supports —
  verify no new merge primitive is needed). **Re-audit every `is_developer_tool_lane` gate**
  (the 11 core + 2 CLI call sites mapped at synthesis) against a **3-lane** fixture
  (developer_tool + cloud_api + local_inference placeholder); add a per-lane cloud_api
  `$`-summer (`lane_total_usd(CloudApi)`) and confirm `grand_total_usd` is the only crosser.
- **Deciding test (the M2 gate):** the §1.1 merged-ledger fixture — dev-tool + synthetic AWS
  FOCUS → one ledger; dev-tool `$`-summers count only dev-tool rows; cloud total independent;
  grand total crosses; merged export validates clean through `focus_conformance.sh`.
- **C4:** no (synthetic). **Risk (HIGHEST BLAST RADIUS):** a missed `$`-summer lets a cloud
  row inflate a dev-tool/now/budget/forecast figure. **Mitigation:** the mixed-3-lane test
  hits **each** summer; this is the typed enforcement of "lanes never summed across".

### T11 — Authoritative-vs-estimate reconciliation wiring (costroid-core)
- **Do:** Ensure the **AWS FOCUS invoice** (source-priced, `x_Estimated=false`) vs the
  **LiteLLM-estimated API logs** (`x_Estimated=true`) distinction flows to the existing
  `reconcile` engine: the estimated cloud rows form the estimate side; the authoritative
  imported rows are usable as the invoice side (the `vendor_report` shape, or a thin adapter
  from authoritative FOCUS rows → `VendorCostReport`). **Reuse** `reconcile_cost`; no new CLI
  command required (the existing `reconcile` is `connect`-gated — keep the new path
  pure-core/offline so it works on imported files without `connect`).
- **Deciding test:** a fixture of estimated cloud rows + an authoritative AWS-FOCUS "invoice"
  reconciles to a signed per-day/per-model variance via `reconcile_cost`, with the estimate
  honestly labeled the estimate and the authoritative figure the bill.
- **C4:** no (synthetic). **Risk:** scope creep into a new reconcile surface.
  **Mitigation:** reuse `reconcile_cost` + `vendor_report`; if the adapter proves non-trivial,
  this task is **trimmable** — the M2 *deciding* test is T10, and "design for reconciliation"
  is satisfied by the stamped distinction + a unit test, not a new command.

### T12 — Public CLI surface: import generalization + `--pricing-override` (apps/cli) **[D5 — CLI]**
- **Do:** Per D5: `import` now accepts the AWS-shaped FOCUS + non-USD (behavior follows
  T4/T5/T7); add `--pricing-override <path>` to `import` (and `export`), defaulting to the XDG
  path (missing = bundled). No new subcommand. Update `--help` + README.
- **Deciding test:** `cargo test -p costroid --test offline` stays green (no new dep edge); a
  CLI integration test imports a synthetic AWS FOCUS fixture and emits a schema-valid merged
  v1.3 ledger **byte-identical to the library path**; `--pricing-override` changes a priced
  row's rate + provenance stamp.
- **C4:** no (human-gated by D5 sign-off). **Risk:** a new CLI-reachable dep slips in.
  **Mitigation:** the override loader uses only `std::fs` + existing `serde_json`; re-run the
  offline gate after wiring.

### T13 — Live AWS/Bedrock path: present-but-SKIP + sign-off gate (NOT built) **[D6 — network]**
- **Do:** **Do not build** the live AWS Data Exports / Cost Explorer / Bedrock API path. Keep
  the real-data leg present-but-SKIP: the existing `COSTROID_REAL_AWS_FOCUS` file leg covers a
  real **exported file**; document (in `docs/COSTROID-NEXT.md` / `PROGRESS.md`) that the live
  **API** path is `connect`-gated + C4-gated and needs its **own** human sign-off before any
  code (network only ever in `costroid-connect`, keychain-only, off by default).
- **Deciding test:** the SKIP leg prints a loud C4 notice when unset; the offline + forbidden-
  crates gates prove no live AWS code exists in providers/core/CLI.
- **C4:** **YES — the live path is C4-gated** (and sign-off-gated). **Risk:** building a live
  call without sign-off violates the golden rule. **Mitigation:** explicit non-goal; the gate
  is the proof.

### T14 — Deciding test + CI wiring (the M2 gate)
- **Do:** EXTEND `scripts/focus_conformance.sh` (don't replace): add the **merged-ledger leg**
  (T10) + the **v1.2-input leg** (T9); re-pin `focus_known_failures.txt` + the new 1.2 list
  deliberately if validated rows change. Extend the existing CI jobs (no new job): the
  merged-ledger + currency + reconcile tests ride `pre-pr`; the store-column bump rides
  `cross-platform` + `msrv` + `license` + `offline-acceptance`.
- **Deciding test:** all CI jobs green — pre-pr (fmt/clippy/test incl. merged-ledger +
  mixed-currency + R4 + provenance), cross-platform, msrv (store on 1.88), focus-conformance
  (CSV + JSON + synthetic-v1.2 round-trip + **v1.2-input** + **merged-ledger** legs), license
  (LiteLLM data allowlisted, no copyleft), advisories, offline-acceptance (byte-for-byte
  default CLI + no-egress).
- **C4:** the real-AWS legs stay present-but-SKIP. **Risk:** a new validated row hides inside a
  known-defective rule. **Mitigation:** the exact-match contract + deliberate re-pin in the
  same commit (the M1 discipline).

---

## 3. C4 dependency map

**Buildable NOW on synthetic fixtures (NOT C4-blocked):** T0–T12, T14.
- The LiteLLM snapshot (T1), the layered catalog + provenance (T2/T3/T6), the authoritative
  pricing-detail + multi-currency carry-through (T4/T5), the synthetic AWS-shaped + Bedrock
  fixtures (T7/T8), the v1.2-input leg (T9), the merged ledger + reconciliation (T10/T11), and
  the CLI surface (T12) are all built to AWS's **published** FOCUS-1.2-with-AWS-columns
  dictionary, so the diff to a real export is localized to `FocusV12Mapping` + the fixtures.

**BLOCKED on C4 (a real AWS account: Data Exports FOCUS + a Bedrock Application Inference
Profile):**
- **The live AWS/Bedrock *API* path (T13)** — and **separately sign-off-gated** (network code
  only in `costroid-connect`). M2 does **not** build it.
- **Truing the synthetic fixtures against a real export** — the `COSTROID_REAL_AWS_FOCUS`
  file leg flips from SKIP to asserting once a real (locally-held, never-committed) export
  exists; M2 **closes without it** (synthetic legs green, real leg present-but-SKIP).

---

## 4. Cross-cutting risks to resolve EARLY (the M1 hazards, re-armed for M2)

1. **Lane-summer fan-out (highest blast radius — T10).** M1 mapped 11 core + 2 CLI
   `is_developer_tool_lane` gates; M2 is the first milestone to actually put **non-dev-tool $
   rows** in the ledger, so a single missed gate now silently inflates a real figure. Cover
   each summer with the mixed-3-lane test before any cloud producer is wired into a view.
2. **Pricing-source layering must not regress the to-the-cent dev-tool numbers (T3).** The
   curated `pricing.v1.json` is verified-to-the-cent vs ccusage — the LiteLLM tier must be
   *strictly below* it in precedence, pinned by a regression test.
3. **R8 = source + date + hash, on every estimated row (T2/T3).** Not just "bundled" — the
   provenance must be *stamped* so a comparison is auditable; a snapshot file whose recorded
   hash ≠ its bytes is a test failure.
4. **R4 on the widened import surface (T4/T8).** `RawFocusRow`/`CloudUsageEvent` grow by ~13
   fields + a Bedrock id — every one must be bounded metadata; the no-`..` forcing function +
   the whitelist subset assertion are the guard. The Bedrock profile **name** is the trap.
5. **Multi-currency = a second "never sum across" invariant (T5).** Treat currency like lane:
   refuse the cross-currency sum, never blend. No runtime FX (would be a network/drift
   surface).
6. **Zero new CLI-reachable crate deps (T12).** M2 is data-files + existing serde + `std::fs`;
   if any task reaches for a new crate (an FX client, a richer CSV lib, a hashing crate not
   already present), STOP — re-run the offline gate and review against `ALWAYS_FORBIDDEN`.
7. **The live AWS path is sign-off-gated AND C4-gated (T13).** Two independent gates; M2 trips
   neither. Network only ever in `costroid-connect`.
8. **`focus_known_failures.txt` (+ the new 1.2 list) are exact-match contracts (T9/T14).** Any
   new validated fixture row re-pins counts in the **same** commit.

---

## 5. What M2 deliberately does NOT do (defended scope)

- **No live AWS/Bedrock API calls or credential reads** (T13) — `connect`-gated + C4-gated +
  separately sign-off-gated.
- **No FX conversion** (D3) — native currency carried, never converted.
- **No local-inference lane population** — that is M3 (the 8 local x_ columns stay deferred);
  M2 only re-verifies the lane *guard* against a local placeholder row.
- **No new web/TUI surface** — M5. M2 is the ledger + import + pricing; views consume it later.
- **No Parquet** — deferred since M1 (CSV + JSON remain the exports).
