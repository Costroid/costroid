# Costroid-Next — M3 detailed implementation plan

> **Provenance:** synthesized at the M3 `/goal` kickoff (2026-06-20) from the canon
> (`docs/COSTROID-NEXT.md` §3.3 M3 / the just-revised §5.3/§5.4 measured ladder / §6.3 /
> §6.4 / §4 rules R1·R4·R6·R7·R8·R10 + A2) + the merged M0 `costroid-power` scaffold + a
> repo-fit code map of where the local-inference lane already touches the tree
> (`PowerSampler` + the 4-source selector seam, `MeasurementMode`, the §3.2 `cost` model,
> `CanonicalEvent::Local`/`LocalRunEvent`, the M0-stub `local_run_to_focus`, the store
> fan-out, the offline allowlists, the bundled-data + sha256 pattern). Line numbers/symbols
> were verified against the tree at synthesis time; **re-verify before editing — the code
> wins.** Tracked from [`../PROGRESS.md`](../PROGRESS.md).
>
> **Status: ✅ EXECUTED — T0–T13 COMPLETE on branch `costroid-next` (2026-06-20).** D1–D5 signed
> off (all recommended); every task landed on the per-task dev-loop (build → independent
> adversarial review → fold-in → commit), each green. The **milestone-boundary clean-build
> re-verify is done** (`cargo clean` → rebuild: fmt · clippy `--workspace` · `test --workspace`
> (26) · power feature · store · deny default+all-features · power+pricing integrity · MSRV 1.88
> (workspace + power) · offline-acceptance · focus-validator conformance (8 OK legs incl. the
> 3-lane merged ledger) — all GREEN), and a final independent boundary review APPROVED. **⛔
> Awaiting the human's full fresh-eyes review before any merge to `main`** (milestone-boundary
> cadence; the agent does not merge). **M3b** (a real captured joules/token — wall-meter-primary)
> is a SEPARATE human handoff and does NOT block M4. See the handoff in
> [`../PROGRESS.md`](../PROGRESS.md) note (n).

# M3 detailed plan — the dual-mode local-inference cost engine

## 0. Scope in one sentence

M3 turns the M0 `costroid-power` *scaffold* (the `PowerSampler` trait + 3 stub impls + a
runtime selector + the verified §3.2 cost model) into a real local-inference economics engine:
(a) a **four-source, wall-meter-led** `PowerSampler` (estimated / wall-meter / on-chip sysfs
on Linux / on-chip LibreHardwareMonitor on Windows) with runtime probing and the reordered
selector; (b) a **subprocess** inference runner (llama.cpp / Ollama via CLI + stdout/stderr
stats — **not** FFI, **not** the localhost HTTP API) and a **benchmark harness** that
integrates power over wall-clock time into **Wh / J·token⁻¹ / $·(1M tok)⁻¹**; (c) **dated,
stamped, overridable** assumptions (a bundled hardware/electricity profile + a Gemma 4 model
manifest — reproduce, never bake a magic number, R8); (d) the **7 local `x_` columns** (§6.4)
populated on every `local_inference` row, with **measured-vs-estimated stamped on each record
(R6/R10)**; (e) a minimal `costroid bench` CLI behind an off-by-default `power` feature that
emits a FOCUS-conformant local row. Everything in **M3a is agent-ownable + CI-tested on
SYNTHETIC power fixtures and a STUB runner** — it needs **no hardware**; the real captured
joules/token is the **M3b** human handoff (wall-meter-primary). The default `costroid` CLI
stays **byte-for-byte no-network** (the LHM live read is deferred to M3b; M3a ships only its
JSON parser + a fixture).

---

## 1. M3a "done" criteria (close against this mechanically — never self-judged prose)

1. **The M3a deciding test is green** (the canon's M3a gate): deterministic **cost-math on
   synthetic power fixtures** — a constant-watt fixture *and* a varying-watt sample sequence
   integrate to the correct `avg_power_watts` → `energy_wh` → `J/token` → `$/1M`, exactly per
   §3.2, with worked examples; plus an **estimated-mode what-if** (no subprocess) that computes
   the same economics from the manifest tok/s + the profile watts. No real power number is
   asserted (R10).
2. **Measured-vs-estimated stamped on every record (R6/R10):** every `local_inference`
   `FocusRecord` carries `x_MeasurementMode ∈ {measured_wallmeter, measured_sysfs,
   measured_lhm, estimated}` and `x_Estimated`; a row is `estimated` unless it carries a real
   measured-energy reading; **no figure is ever labeled measured unless it came from a real
   sampler** (synthetic fixtures in CI are labeled by the mode of the synthetic sampler, never
   asserted as real hardware values).
3. **The four-source, wall-meter-led selector works** (D1): with a wall meter configured the
   selector picks it even when a sysfs node is present; with no wall meter it picks the on-chip
   source (sysfs on Linux / LHM on Windows) if available; else estimated; the active mode is
   stamped. Cross-platform green: non-Linux / `power`-off compiles clean "unavailable" stubs.
4. **Subprocess runner, not FFI (A2):** the runner spawns a user-installed `llama.cpp`/`ollama`
   binary and parses **only** token counts + timings from its stats output; `unsafe_code =
   "forbid"` holds (no FFI); no async runtime is reachable; a `StubRunner` drives the harness
   deterministically in CI (no binary needed). **R4:** the committed stats-parse golden
   fixtures contain **only** the stats/timing lines — never a prompt or a completion; the
   runner discards the model's generated text and retains only counts.
5. **Assumptions are dated/stamped/overridable (R8):** the estimated power profile (hardware
   watts/price/lifetime + electricity rate) and the Gemma 4 manifest are **bundled dated data
   artifacts** with a recorded `as_of` + `sha256` (a test asserts each file's recorded hash
   matches its bytes), refreshed only by a dev script, **never fetched** at build or runtime;
   each is **overridable** (CLI flag / config); the winning profile id is stamped on
   `x_HardwareProfile`. Community/published numbers are clearly stamped **estimated** (R10).
6. **R4 holds on the widened local surface:** the no-`..` field-exhaustive forcing functions
   (in `costroid-focus` over `FocusRecord`, in `costroid-providers` over `LocalRunEvent`, in
   `costroid-store` over the persisted columns) still compile; **no** new field is free text —
   every local `x_` column is a bounded number / id / enum / bool.
7. **Offline guarantee intact, byte-for-byte:** the default `costroid` build adds **zero**
   network/async crates; `cargo test -p costroid --test offline` is byte-for-byte unchanged
   (the new `power` CLI feature is off by default; the LHM loopback read is **not built** in
   M3a); `scripts/offline_acceptance.sh` green. A `power`-on build links `costroid-power` and
   is asserted to add **no** forbidden crate (a `POWER_ALLOWED` subset-allowlist, empty or
   bounded, mirroring `STORE_ALLOWED`).
8. **The store persists the 7 new columns** (schema-version bump + the metadata-allowlist
   subset assertion still fail-closed); a `local_inference` row round-trips
   ingest→replay→export **byte-identical**.
9. **No `unwrap`/`expect`/`panic!`** in any lib crate (incl. tests — `panic!` allowed in
   tests, `unwrap`/`expect` are not); the runner (unreadable binary / unparseable stats /
   non-zero exit), the samplers, the profile/manifest loaders, and the override loaders all
   return typed `Result`s; a missing override / absent sensor / absent binary is a typed,
   non-fatal degrade (estimated fallback), never a crash.
10. Every new extension column is **x_PascalCase** (§6.4 names verbatim); the header-pin
    `ends_with` assertion + the CSV-header golden in `costroid-focus` are updated in lockstep;
    `focus_known_failures.txt` re-pinned in the same change as any conformance-row change.
11. Cross-OS compile green (the `cross-platform` CI job) in **both** feature states (`power`
    on and off) + MSRV 1.88 (`costroid-power` + `-p costroid --features power`) + `cargo deny`
    (default + `--all-features`) green; the profile/manifest data files' licenses are verified
    permissive and recorded.
12. **The merged 3-lane ledger validates:** `scripts/focus_conformance.sh`'s merged-ledger leg
    is extended with a `local_inference` row (developer_tool + cloud_api + local_inference in
    one ledger) and validates clean (subset contract, no new failing rule); the lane-separation
    invariant holds (the M2 `lane_total_usd` / `grand_total_usd` guards already cover three
    lanes — re-verified against a populated local row).

---

## 1.5 ⚑ DECISIONS TO SIGN OFF BEFORE CODING (CLAUDE.md "ask first")

> These are the **export/output-schema, public-CLI, and network-boundary** decisions M3
> cannot make unilaterally. **No T-task is coded until these are signed off.** Recommended
> defaults are marked ★; the highest-leverage ones are asked interactively.
>
> **✅ SIGNED OFF 2026-06-20 (all recommended ★):**
> - **D1 → (a)** wall-meter-led selector (wall meter → on-chip → estimated) + a 4th
>   `MeasuredLhm` (`measured_lhm`) mode.
> - **D2 → (a)** all 7 §6.4 local `x_` columns land in M3a; M3b is data-only (no new column).
> - **D3 → (a)** bundled dated/stamped/overridable hardware+electricity profile; the default
>   electricity rate is **`0.16 USD/kWh`, `as_of 2026-06-20`,
>   `label "global-household-average-template"`, `estimated`**, overridable via
>   `--electricity-rate` / `[power]`; the Turkey EPDK tariff is the documented override
>   template (not the baked default).
> - **D4 → (a)** Gemma 4 family manifest (31B dense, 26B-A4B, 12B unified, E2B/E4B), default
>   quant **`Q4_K_M`** (`Q8_0` for the quality-sensitive variant); quality from published
>   scores (source + date, never re-derived); tok/s `estimated` until M3b.
> - **D5 → (a)** add `costroid bench` behind an off-by-default `power` CLI feature (estimated/
>   what-if default + `--measure`); the LHM live loopback read is **deferred to M3b** (M3a ships
>   the parser-only seam) → the default CLI stays byte-for-byte no-network.

- **D1 — The selector order (runtime behavior).** **(a) ★ Wall-meter-led:** the runtime
  selector picks **wall meter if configured → else on-chip if available (sysfs on Linux / LHM
  on Windows) → else estimated**, stamping the active mode. This *reverses* the M0 scaffold's
  current order (`sysfs if present → else wall meter → else estimated`) to match the revised
  §5.3/§5.4: the wall meter is the truest cross-OS figure, and most users are on Windows where
  there is no native sysfs at all. (b) Keep the M0 sysfs-first order (on-chip beats a
  configured wall meter) — rejected: it prefers the *less* honest package-power reading over a
  user's deliberately-configured true-draw meter. **A 4th `MeasurementMode` variant
  (`MeasuredLhm` → `measured_lhm`) is added** for the Windows on-chip source either way.
- **D2 — The 7 local `x_` columns + the M3a/M3b split (export schema).** Add all **seven**
  §6.4 names verbatim, trailing `x_InferenceProfileId` on `FocusRecord`: **`x_MeasuredWh`,
  `x_AvgPowerWatts`, `x_HardwareProfile`, `x_AmortizedHwCost`, `x_RuntimeKind`,
  `x_BenchmarkId`, `x_MeasurementMode`**. **(a) ★ All 7 land in M3a; M3b adds NO new column —
  it only supplies real measured *values* that flow through the same columns.** Rationale: a
  `LocalRunEvent` is produced in *either* estimated or measured mode, and M3a already produces
  both (estimated mode + synthetic-sampler "measured" rows), so the schema + population + store
  fan-out + tests all belong in M3a; M3b is data-only. (b) Split the columns (energy columns
  M3b-only) — rejected: it would leave estimated-mode economics unable to populate the energy
  columns in M3a, defeating the universal fallback. **Honesty note (R6, applies to (a)):**
  `x_MeasuredWh` carries the integrated energy in **both** modes; `x_MeasurementMode` +
  `x_Estimated` disambiguate measured vs estimated provenance (the column name follows §6.4
  verbatim per the goal; the mode stamp is what makes it honest). All seven are bounded
  (decimal / id / enum / string) — `None`/empty in `unpriced_usage`, so dev-tool + cloud rows
  are unaffected.
- **D3 — The estimated power profile + electricity rate (dated assumptions + override
  location).** **(a) ★** Ship a **bundled, dated, sha256-stamped** profile artifact
  `crates/costroid-power/profiles/hardware.v1.json` (+ `.sha256` + a sibling `README.md` with
  source/as_of/license — the `pricing.v1.json` posture) carrying the hardware profile(s)
  (`strix-halo-128gb`: load-watts range, idle watts, hardware price, amortization lifetime,
  memory bandwidth) **and** a dated default **electricity rate**, all clearly stamped
  **estimated** (R6/R10 — community-measured §5.2 ranges, never presented as measured). It is
  **overridable** via (i) `costroid bench` flags (`--electricity-rate`, `--hardware-price`,
  `--hardware-lifetime`, `--wall-meter-watts`, `--hardware-profile`) and (ii) a `[power]`
  section in the existing `config.toml` (extending `costroid-config`, mirroring
  `[budget]`/`[alerts]`). The **default electricity rate** is proposed below for sign-off
  (it is the one genuinely judgemental number). (b) Hard-code the profile in Rust constants —
  rejected (R8: a hidden, undated number). (c) Require the user to supply everything with no
  default — rejected (estimated mode must work out-of-the-box).
  - **⚑ Proposed dated default electricity rate (sign-off needed — the only "magic number"):**
    a clearly-stamped **dated template**, overridable. Recommendation: **`0.16 USD/kWh`,
    `as_of 2026-06-20`, label `"global-household-average-template"`** (a round, honestly-
    approximate global household figure so local rows land in the USD lane by default), with
    the **Turkey EPDK residential tariff documented as the founder's override template** (§5.5)
    — *not* baked as the default, because an undated TRY figure would (i) drift and (ii) push
    local rows out of the USD `grand_total_usd` (D3/M2 multi-currency excludes non-USD). The
    human picks the value + currency; the code only ever ships it **dated + stamped +
    overridable**.
- **D4 — The Gemma 4 model/quant set + quality source (data artifact).** **(a) ★** Ship a
  **bundled, dated, sha256-stamped** manifest `crates/costroid-power/models/gemma4.v1.json`
  (+ `.sha256` + README) standardizing on the **Gemma 4 family (Apache-2.0)** per §3.1.E /
  §5.5: **`gemma-4-31b-dense`** (30.7B, dense flagship/coding counterexample),
  **`gemma-4-26b-a4b`** (25.2B/3.8B-active MoE, the fast point), **`gemma-4-12b-unified`**, and
  **`gemma-4-e2b`/`gemma-4-e4b`** (edge). Each entry: id, **quant set** (proposed default
  **`Q4_K_M`** as the measured quant; **`Q8_0`** noted for the quality-sensitive variant),
  params/active-params/ctx, the shipped **draft model** (speculative-decoding lever to
  *measure*), the **published quality score with source URL + date** (model card / public
  arena — **never re-derived**, R10; marked *as published / n/a* where no coding score
  exists), and a **tok/s estimate stamped `estimated` until M3b**. License verified Apache-2.0
  (the model card; Costroid ships **no weights**). (b) Hard-code the model list — rejected
  (R8/R10). **Pin to Gemma 4 only** (Gemma 1–3 are non-OSI). The quality axis feeds the
  Frontier later (M4/M6); M3a only needs the manifest for the harness + estimated profile.
- **D5 — Public CLI surface + the LHM network-boundary (CLI + network).** **(a) ★** Add a
  single new subcommand **`costroid bench`** behind a **new off-by-default `power` CLI
  feature** (`power = ["dep:costroid-power"]`, mirroring `connect`/`store`). Two modes:
  **estimated/what-if** (default; no subprocess — computes from the manifest tok/s + the
  profile; the CI-testable path, needs no binary/hardware) and **`--measure`** (`power`-on;
  spawns the real llama.cpp/Ollama subprocess + samples power via the selected sampler). It
  emits a `local_inference` FOCUS row (CSV/JSON), reuses `export`'s plumbing, and is
  **offline-safe** (subprocess = `std::process`; sysfs = `std::fs`; no network crate). The
  default CLI graph is **unchanged** (power off) → `offline.rs` byte-for-byte; a new
  `POWER_ALLOWED` subset-allowlist + a power-build test assert the `power`-on delta adds no
  forbidden crate. **The LHM live loopback read is NOT built in M3a** (D5/network boundary):
  M3a ships only the LHM **JSON parser** + a committed `data.json` fixture + a stub
  `WindowsLhmPowerSampler` (probe → false); the live `TcpStream` read to `localhost:8085`
  (gated `#[cfg(all(target_os="windows", feature="power"))]`, loopback-only, with its own
  offline-gate carve-out) is the **M3b** field-verification handoff. So M3a contains **zero**
  loopback/AF_INET code. (b) Defer the CLI entirely to M5 (run M3b via a cargo example /
  ignored test) — viable but the human then has no first-class way to run the on-hardware
  benchmark; not recommended.

> **Architecture decisions taken WITHOUT sign-off (internal structure — CLAUDE.md "decide on
> your own"), recorded for transparency:**
> - **No `costroid-core → costroid-power` dependency edge.** The §3.2 cost math lives only in
>   `costroid-power`; the **CLI `bench` command** (which depends on both, under `power`) runs
>   the harness, gets a computed `LocalRunReport`, translates it into a
>   `costroid-providers::LocalRunEvent` enriched with the bounded economics fields, and calls
>   the existing `core::focus_records_from_canonical`. `core::local_run_to_focus` becomes a
>   pure **mapping** (event fields → FOCUS columns), **no math, no power dep** — so `core`,
>   `providers`, and the default CLI graph are dependency-unchanged, and `costroid-power` stays
>   a leaf. The deterministic cost-math tests live in `costroid-power`; the FOCUS-mapping tests
>   live in `core`.
> - **The runner + harness + samplers + manifests all live in `costroid-power`** (§6.2: "local
>   inference runner, the `PowerSampler` abstraction, benchmark harness, energy/cost model");
>   the runner is a trait so a `StubRunner` makes the harness CI-testable without a binary.

---

## 2. Ordered task list (dependency-correct; data/scaffold first)

Each task notes its deciding test, its M3a/M3b status, and the top risk + mitigation. Order:
selector/mode → schema columns → store → event model → data artifacts → harness → runner →
core mapping → LHM seam → CLI → conformance/CI → docs.

### T0 — Reconcile the measured-ladder canon ✅ DONE
- **Done (this kickoff):** committed the uncommitted §5.3/§5.4 edits (`a15bd8f`), then
  reconciled the 5 dependent spots to the four-source, wall-meter-led ladder (`3f5b218`): §3.3
  M3, R1, §6.3 impls (+`WindowsLhmPowerSampler`), §6.3 selector (the new order — surfaced as
  **D1**), §6.12 DoD, plus §6.4's `x_MeasurementMode` enum (+`measured_lhm`). Pre-stage note:
  the `costroid-power` code comments/Cargo description still say "three implementations" — those
  flip in **T1** (the task that makes them true), not as doc-only prose.
- **Deciding test:** none (docs); `grep` confirms no `three-source`/`sysfs if present` remains
  in the canon.

### T1 — 4th `MeasurementMode` + the wall-meter-led selector (costroid-power) **[D1]**
- **Do:** Add `MeasurementMode::MeasuredLhm` (`measured_lhm`, `is_measured() = true`). Add the
  `WindowsLhmPowerSampler` type (full impl is T10) so the selector has all four. **Reorder
  `select_sampler`** to the D1 order: **wall meter if configured → else on-chip (sysfs on
  Linux / LHM on Windows) if probed → else estimated**. Update the scaffold's
  "three implementations"/"three-source" comments + the Cargo `description` to four.
- **Deciding test:** with a configured wall meter **and** a present sysfs node, the selector
  returns the wall-meter mode (the D1 reversal — this test would *fail* under the M0 order);
  with no wall meter, on-chip wins if probed, else estimated; `measured_lhm` round-trips
  through `as_focus_str`/serde; cross-platform compile (the existing
  `sysfs_self_disables_when_unavailable_and_selector_falls_through` test is updated for the new
  order).
- **M3a.** **Risk:** the reordered selector changes M0 test expectations. **Mitigation:** the
  M0 selector tests are updated in this commit; D1 is signed off first.

### T2 — The 7 local `x_` columns on `FocusRecord` (costroid-focus) **[D2 — schema]**
- **Do:** Add the seven §6.4 columns (x_PascalCase) trailing `x_InferenceProfileId`:
  `x_MeasuredWh` (`Option<Decimal>`), `x_AvgPowerWatts` (`Option<Decimal>`),
  `x_HardwareProfile` (`Option<String>` — bounded id), `x_AmortizedHwCost` (`Option<Decimal>`),
  `x_RuntimeKind` (`Option<String>` — bounded enum-ish: `ollama`/`llama.cpp`), `x_BenchmarkId`
  (`Option<String>` — bounded id), `x_MeasurementMode` (`Option<String>` — the four-value
  enum). Default `None` in `unpriced_usage` (dev-tool + cloud rows unaffected). Update the
  header-pin `ends_with` assertion + the CSV-header golden in lockstep. Add all seven to the
  no-`..` R4 destructure (classified as bounded metadata). Keep `FocusRecord` `PartialEq`.
- **Deciding test:** a hand-built local row serializes all 7 non-empty; a dev-tool/cloud row
  serializes them null/empty; the header golden matches the new `ends_with`; the no-`..`
  destructure still compiles (a new field would be a compile error); `r4_no_exported_column_
  name_is_content_bearing` still passes.
- **M3a.** **Risk (HIGH blast radius):** breaking the header pin / a `==`-on-`FocusRecord`
  test elsewhere. **Mitigation:** single-source the header string; run the full focus + core +
  store + CLI test set.

### T3 — Store fan-out for the 7 new columns (costroid-store) **[schema]**
- **Do:** Thread the 7 columns through **every** SQL fan-out site (the M2 lesson — there are
  ~8): bump `SCHEMA_VERSION` 6→7; add to `USAGE_ROWS_COLUMNS`; the `CREATE TABLE` DDL; the
  `INSERT` column list; the `params!` row; the `SELECT` list; the `row.get(N)` indices; the
  `reconstruct_row` assembly; and the persist-or-drop **forcing-function** destructure. All
  `TEXT`/decimal-as-string (no free-text column → the fail-closed allowlist subset assertion
  still holds).
- **Deciding test:** a `local_inference` row with all 7 columns populated round-trips
  ingest→replay→export **byte-identical**; the DDL-vs-allowlist subset assertion passes; a new
  unclassified field is a compile error (the forcing function).
- **M3a.** **Risk:** a missed fan-out site silently drops a column (the M2 "store dropped
  priced-SKU columns" bug). **Mitigation:** the byte-identical round-trip test + the
  forcing-function destructure catch any omission.

### T4 — Enrich `LocalRunEvent` with bounded economics fields (costroid-providers) **[D2]**
- **Do:** Extend `LocalRunEvent` (providers/lib.rs:242) — currently raw observations only
  (timestamp, model, runtime_kind, tokens_in/out, run_seconds, avg_power_watts,
  measurement_mode) — with the **bounded** computed/assumption fields the CLI carries from the
  harness to core: `quant: String`, `energy_wh: f64`, `amortized_hw_cost: String`
  (decimal-string, never f64 money), `local_run_cost: String`, `electricity_rate_per_kwh: f64`,
  `hardware_price: f64`, `hardware_lifetime_seconds: f64`, `hardware_profile_id: String`,
  `benchmark_id: String`, `billing_currency: String`. All bounded metadata (R4: numbers / ids
  / enum-ish labels — never content). Update the no-`..` R4 forcing function + the serde
  round-trip + `sample_local_run_event`.
- **Deciding test:** `LocalRunEvent` JSON round-trips with the new fields; the
  `lane_events_stay_metadata_only_r4_guard` no-`..` destructure compiles (every new field
  consciously classified); no field is content-bearing.
- **M3a.** **Risk:** mixing raw + derived on one struct invites a future "just add the prompt"
  field. **Mitigation:** the forcing function + an R4 field-name scan; decimal-as-string keeps
  money out of f64.

### T5 — Bundled dated hardware/electricity profile + loader + override (costroid-power) **[D3 — R8]**
- **Do:** Add `crates/costroid-power/profiles/hardware.v1.json` (+ `.sha256` + `README.md`
  recording source/as_of/sha256/license) carrying the dated, **estimated**-stamped hardware
  profile(s) + the dated default electricity rate (D3 values, sign-off-gated). `include_str!`
  it; a typed loader parses it; a typed override loader reads `[power]` from config + the CLI
  flags (missing → bundled default, zero-config). A dev-only
  `scripts/refresh_power_profiles.*` regenerates it (no build/runtime fetch). The winning
  profile id is the `x_HardwareProfile` stamp.
- **Deciding test:** the recorded `sha256` matches the file bytes (a unit test, like
  `bundled_litellm_snapshot_loads_with_pinned_provenance`); a missing override → the bundled
  default; an override changes the effective rate; an absent/garbled profile is a typed
  non-fatal error; the profile is stamped `estimated`.
- **M3a.** **Risk (R8):** an undated/hidden number, or a profile hash that drifts from its
  bytes. **Mitigation:** the sha256-matches-bytes test + the dated README; community numbers
  carry an `estimated` label (R10).

### T6 — Bundled dated Gemma 4 model manifest + loader (costroid-power) **[D4]**
- **Do:** Add `crates/costroid-power/models/gemma4.v1.json` (+ `.sha256` + README) with the
  D4 family (id, quant set, params/active/ctx, draft model, **published** quality score +
  source URL + date, tok/s **estimate** stamped `estimated`). `include_str!` + a typed loader.
  Verify Apache-2.0 (model card); ship **no weights**. Dev-only refresh script.
- **Deciding test:** the recorded `sha256` matches bytes; the family loads (5 entries); each
  quality score carries a non-empty source + date; each tok/s is flagged `estimated`; an
  unknown model id resolves to a typed "not in manifest" (no fabricated spec).
- **M3a.** **Risk (R10):** a tok/s or quality number presented as measured. **Mitigation:**
  every tok/s is `estimated`-stamped; quality is `as published` with a citation; the harness
  (T7) is the only producer of real throughput, at M3b.

### T7 — The benchmark harness: sampler integration + the §3.2 cost model → `LocalRunReport` (costroid-power) **[the M3a deciding test]**
- **Do:** A harness that, given a `&dyn PowerSampler` + a `&dyn Runner` (T8) + a profile (T5)
  + a model spec (T6), (i) **measured mode:** samples power on an interval over the run
  (blocking `std::thread::sleep` loop), averages to `avg_power_watts`, runs the model via the
  runner, and integrates power×time → `energy_wh`/`J·token⁻¹`; (ii) **estimated/what-if mode:**
  computes the same economics from manifest tok/s + profile watts with no subprocess. Feeds the
  existing §3.2 `cost::local_run_cost` to get `local_run_cost` / `local_cost_per_1m`. Returns a
  pure `LocalRunReport` (tokens, run_seconds, avg_watts, energy_wh, amortized_hw_cost,
  local_run_cost, local_cost_per_1m, J/token, measurement_mode, hardware_profile_id,
  benchmark_id, electricity_rate, quant). `BenchmarkId` = a stable hash of (suite, model,
  quant, runtime flags, prompt-set version).
- **Deciding test (the M3a gate):** a **constant-watt synthetic sampler** + a **varying-watt
  sample sequence** each integrate to the documented `avg_power_watts` → `energy_wh` →
  `J/token` → `$/1M` (worked examples, exact per §3.2); the estimated what-if computes from
  manifest tok/s + profile with **no** runner call; zero tokens / zero duration are typed
  errors (reuse `PowerError`); **no real power number asserted** (R10).
- **M3a.** **Risk:** the power-integration averaging is subtly wrong (e.g. trapezoidal vs
  rectangular over uneven intervals). **Mitigation:** pin the integration method in a worked
  example with hand-computed energy; the synthetic sequence has a known closed-form average.

### T8 — The subprocess inference runner + stats parser + StubRunner (costroid-power) **[A2 / R4]**
- **Do:** A `Runner` trait (`run(spec) -> Result<RunOutput, PowerError>`) with a
  `LlamaCppRunner` + an `OllamaRunner` that **spawn the user-installed binary** (`std::process`,
  configurable path; CLI/stdout — **not** the localhost HTTP API, **not** FFI) and **parse only
  token counts + timings** from the stats output (llama.cpp's `llama_print_timings` stderr
  block: prompt eval tokens/time, eval tokens/time, tok/s; `ollama run --verbose`'s eval
  stats). The runner **discards the generated text** (R4) — it reads stats from stderr / the
  verbose-stats stream and never persists stdout content. A `StubRunner` returns a fixed
  `RunOutput` for deterministic harness tests.
- **Deciding test:** golden parse of **committed stats fixtures** (`fixtures/local/llama-cpp-
  timings.txt`, `fixtures/local/ollama-verbose.txt` — **stats/timing lines only, no
  prompt/completion text**, R4) → the exact `RunOutput` token counts + timings; a missing
  binary / non-zero exit / unparseable stats → a typed `PowerError` (never a panic); the
  `StubRunner` drives T7's harness with no subprocess; an R4 test asserts the fixtures contain
  no content-bearing lines.
- **M3a** (the runner code + golden parse; a *real* subprocess run is M3b). **Risk (R4):** a
  fixture (or a future real capture) includes the model's output. **Mitigation:** the fixtures
  are stats-only by construction + the R4 scan; the runner reads the stats stream, not stdout
  content.

### T9 — `local_run_to_focus` populates all 7 columns (costroid-core) **[mapping]**
- **Do:** Replace the M0-stub `local_run_to_focus` (lib.rs:338, lane+tokens only) with the
  full **mapping** from the enriched `LocalRunEvent` (T4): set `BilledCost`/`EffectiveCost`/
  `ListCost`/`ContractedCost` = `local_run_cost` (in the event's `billing_currency`), and the
  7 `x_` columns (`x_MeasuredWh`=energy_wh, `x_AvgPowerWatts`, `x_AmortizedHwCost`,
  `x_HardwareProfile`, `x_RuntimeKind`, `x_BenchmarkId`, `x_MeasurementMode`); set `x_Estimated`
  = `!mode.is_measured()`. **No math, no power dep** (the economics arrive pre-computed). Parse
  decimal-strings via the existing `parse_cloud_decimal`-style helper (typed errors, never
  f64/panic).
- **Deciding test:** extend `canonical_local_event_yields_local_inference_lane_with_tokens` —
  a **measured** event maps to a local row with `x_MeasurementMode=measured_wallmeter`,
  `x_Estimated=false`, and energy/cost columns populated; an **estimated** event maps with
  `x_MeasurementMode=estimated`, `x_Estimated=true`; the cost lands in `lane_total_usd(Local
  Inference)` (USD) and never in the dev-tool total (re-verify the lane guard).
- **M3a.** **Risk:** a non-USD local row (TRY electricity rate) silently dropped from USD
  totals surprises the user. **Mitigation:** default the rate to USD (D3); document the
  multi-currency interaction in T13; the lane/currency guards are the M2 ones (already tested).

### T10 — The LibreHardwareMonitor seam: parser + fixture (costroid-power) **[D5 — network boundary]**
- **Do:** Implement `WindowsLhmPowerSampler` as a **parser-only seam** for M3a: a pure
  `parse_package_watts(json: &str) -> Result<f64, PowerError>` that walks the LHM
  `data.json` tree to the SMU **Package** power sensor (whole-APU package power — label it
  such, R6); `probe()` returns **false** (the live read is M3b); `sample_watts()` returns
  `SensorUnavailable` until the live read lands. Commit a real-shaped
  `fixtures/local/lhm-data.json` (no user content — it is a hardware-sensor tree). **No
  `TcpStream`/loopback/AF_INET code in M3a** — so the default and `power`-on CLI graphs both
  stay byte-for-byte no-network.
- **Deciding test:** `parse_package_watts` extracts the package watts from the committed
  fixture; a malformed/absent sensor → a typed error; `probe()` is false; cross-platform
  compile (the parser is pure; the type exists on every target).
- **M3a** (parser + fixture + stub). **The live loopback read is M3b** (gated
  `#[cfg(all(target_os="windows", feature="power"))]`, loopback-only, with its own offline-gate
  carve-out, field-verified on the 8060S). **Risk:** scope-creeping the live read into M3a and
  tripping the offline gate. **Mitigation:** M3a is parser-only by construction; the offline
  test proves no AF_INET path exists.

### T11 — The `costroid bench` CLI + the `power` feature + offline guard (apps/cli) **[D5 — CLI]**
- **Do:** Add `power = ["dep:costroid-power"]` (off by default) + an optional `costroid-power`
  dep, mirroring `connect`/`store`. Add `costroid bench`: **estimated/what-if** by default
  (manifest tok/s + profile; no subprocess — the CI-testable, hardware-free path) and
  `--measure` (`power`-on; real subprocess + the selected sampler). Flags: `--model`,
  `--quant`, `--runtime {ollama,llama.cpp}`, `--electricity-rate`, `--hardware-price`,
  `--hardware-lifetime`, `--wall-meter-watts`, `--hardware-profile`, `--format {csv,json}`,
  `--out`. It builds a `LocalRunReport` (costroid-power) → enriched `LocalRunEvent` → emits a
  `local_inference` FOCUS row via the existing core path. Reads `[power]` config defaults. Add
  a `POWER_ALLOWED` subset-allowlist to `offline.rs` + a `power_build_admits_no_new_network_
  crate` test (mirroring the store test) + assert `costroid-power` is actually linked under the
  feature.
- **Deciding test:** `cargo test -p costroid --test offline` is **byte-for-byte unchanged**
  (default build); the new power-build test shows the `power` delta is bounded by `POWER_ALLOWED`
  (no forbidden crate) and links `costroid-power`; a `--features power` integration test runs
  `bench` in **estimated mode** (no binary) → a schema-valid `local_inference` row
  **byte-identical** to the library path; `--electricity-rate` changes the row's cost +
  assumptions.
- **M3a.** **Risk:** a new CLI-reachable network/async crate slips in via `costroid-power`.
  **Mitigation:** `costroid-power`'s deps are serde/thiserror (+ chrono/rust_decimal at M3, all
  already in the CLI graph) — the delta is empty; re-run the offline + acceptance gates after
  wiring; subprocess = `std::process`, not a crate.

### T12 — Conformance + CI: the 3-lane merged ledger + data-integrity check **[the conformance gate]**
- **Do:** Extend `scripts/focus_conformance.sh`'s **merged-ledger leg** to append a
  `local_inference` row (emitted by `bench` in estimated mode, or a committed fixture) so the
  ledger spans **all three lanes**; re-pin `focus_known_failures.txt` deliberately if validated
  rows change. Add a **data-integrity check** for the new bundled artifacts (extend
  `scripts/check_pricing_snapshots.sh` or a sibling: `sha256sum -c` the profile + manifest
  sidecars, fail-closed). Wire the `power`-build offline test + the cross-OS `power`-on build
  into the existing CI jobs (no new job).
- **Deciding test:** all CI legs green — pre-pr (fmt/clippy/test incl. the cost-math + mapping
  + R4 + offline tests), cross-platform (`power` on and off, macOS + Windows),
  msrv (`costroid-power` + `-p costroid --features power` on 1.88), focus-conformance (CSV +
  JSON + v1.2 round-trip + v1.2-input + **3-lane merged-ledger**), license (profile/manifest
  data permissive), advisories, offline-acceptance (byte-for-byte default + no-egress).
- **M3a.** **Risk:** a new validated local row hides inside a known-defective rule.
  **Mitigation:** the exact-match contract + a deliberate re-pin in the same commit (the
  M1/M2 discipline).

### T13 — Docs: methodology + limitations + ARCHITECTURE/PROGRESS **[honesty, R6]**
- **Do:** Update `docs/limitations.md` with the M3 honesty caveats (R6): **no source isolates
  GPU-only watts** on this APU (sysfs + LHM = whole-APU **package** power, overlaps CPU; the
  wall meter is **true total draw, ~20–40% higher**); **measured vs estimated** is stamped per
  row; **at low volume local usually LOSES** on pure cost (wins on privacy / unlimited /
  experimentation); the electricity rate + hardware price/lifetime are **dated, stamped,
  overridable** assumptions (R8); **ranges + methodology, never a hero number**; tok/s +
  quality are **estimated / as-published until M3b**. Reconcile `docs/ARCHITECTURE.md` (the
  four-source sampler, the runner, the harness, the `power` CLI feature) + `PROGRESS.md`.
- **Deciding test:** none (docs); reviewed in the same PRs; the methodology is reproducible
  (R10).
- **M3a.** **Risk:** the docs overclaim measurement. **Mitigation:** every claim ties to a
  stamped column + the M3b "not yet measured" status.

---

## 3. The M3a / M3b split (what is agent-ownable vs the human handoff)

**M3a — agent-ownable, CI-tested, the merge target (needs NO hardware):** T0–T13 above. The
`PowerSampler` trait + the four sources (Estimated + WallMeter real; Sysfs real read gated
Linux+`power`; **WindowsLhm = parser-only seam**) + the wall-meter-led selector + runtime
probing; the **subprocess runner** (code + stats-parse goldens; the `StubRunner` drives CI) +
the **benchmark harness**; the dated profile + Gemma 4 manifest; the 7 `x_` columns through
focus + store + the core mapping; the `costroid bench` CLI (estimated/what-if is the
hardware-free CI path); deterministic **cost-math on synthetic power fixtures**; cross-platform
green in both feature states. **CI never asserts a real power number (R10).**

**M3b — human-gated measured confirmation (a SEPARATE handoff; does NOT block M4):** a real
captured **joules/token** from a real run, flowing through the **same** columns (D2 — no new
schema). In order of convenience (none gates M3a or M4):
1. **PRIMARY — the wall meter on the Strix Halo** (Windows/WSL, ~$20 smart plug → true
   total-system draw, no dual-boot): run `costroid bench --measure --wall-meter-watts …` (or a
   CSV/smart-plug feed) on a Gemma 4 model and capture the figure.
2. **Optional bonus — the WindowsLhm live read:** implement the gated loopback `TcpStream` read
   to `localhost:8085/data.json` (+ its offline-gate carve-out) and field-verify the package
   sensor on the actual 8060S.
3. **Optional bonus — the native-Linux sysfs path:** confirm `power1_average` reads on gfx1151
   (dual-boot, now OPTIONAL — a free on-chip reader + a clean writeup datapoint).
4. **Optional bonus — the 5800H native-Linux box:** validate the full measured pipeline
   end-to-end with a small model (E2B/E4B) if it exposes `power1_average`.

**NEVER fabricate or hardcode a power/tok-s number; CI NEVER asserts a real one (R10).**

---

## 4. Cross-cutting risks to resolve EARLY (the M1/M2 hazards, re-armed for M3)

1. **The offline boundary on the LHM read (highest-care).** A loopback `TcpStream` to
   `localhost:8085` is an AF_INET socket — the CLI's strict no-network gate would trip. M3a
   sidesteps it entirely (parser-only seam, no socket code); the live read is M3b behind
   `power`+windows with its own loopback-only carve-out. **Never** add a network crate.
2. **R10 — no fabricated measurements.** Community/published numbers (§5.2 watts, §5.5 quality,
   tok/s) are **estimated/as-published**-stamped data, never asserted as measured; CI tests
   only synthetic fixtures + worked cost-math; the real number is M3b.
3. **R8 — dated/stamped/overridable assumptions.** The profile + manifest are
   bundled-dated-hashed (sha256-matches-bytes test) + overridable; the electricity rate is the
   one judgemental default (D3, sign-off-gated); no hidden magic number.
4. **R4 on the widened local surface.** `FocusRecord` (+7), `LocalRunEvent` (+10), the store
   (+7) all grow — every field bounded; the three no-`..` forcing functions + the store
   allowlist subset assertion are the guard; the runner discards model output text; the stats
   fixtures are content-free.
5. **Lane/currency integrity (the M2 invariants, now with a populated local row).** A populated
   `local_inference` `$` row must never inflate a dev-tool/cloud total; default the rate to USD
   so local rows land in `grand_total_usd`; re-verify every `is_developer_tool_lane` /
   `lane_total_usd` guard against a 3-lane fixture.
6. **Cross-platform, both feature states.** `power` off + non-Linux compiles clean
   "unavailable" stubs; `power` on links `costroid-power` with no new network crate; the store
   fan-out + header pin stay in lockstep across focus/store/core/CLI.
7. **The store fan-out (the M2 "dropped columns" bug).** 7 columns × ~8 SQL sites + the forcing
   function; the byte-identical round-trip test is the proof.
8. **Warm-cache hazard (the M2 `include_str!` lesson).** After touching the bundled profile /
   manifest JSON, a local re-verify needs `cargo clean -p costroid-power` (CARGO_INCREMENTAL=0
   is insufficient); CI is unaffected.

---

## 5. What M3 deliberately does NOT do (defended scope)

- **No fabricated/hardcoded power or tok/s numbers** — the real captured joules/token is M3b
  (R10); M3a ships estimated mode + synthetic fixtures.
- **No LHM live loopback read in M3a** (D5) — parser-only seam; the gated loopback read is M3b.
- **No FFI / no async runtime / no local HTTP-API client** (A2) — the runner is a `std::process`
  subprocess reading stdout/stderr; `unsafe_code = "forbid"` holds.
- **No live AWS/cloud calls** — unchanged from M2 (that boundary is `connect`-only).
- **No break-even / scenario engine** — that is **M4** (it consumes M3's local cost + the
  DeepSWE-Bench cloud snapshot).
- **No web/TUI surface for the local lane** — that is **M5**; M3 adds only the `bench` CLI +
  the FOCUS rows the later views consume.
- **No Parquet** — deferred since M1 (CSV + JSON remain the exports).
- **No new copyleft dep** — the profile/manifest are permissive data files; the runner adds no
  crate; LHM's MPL stays out-of-process (no link/FFI).
