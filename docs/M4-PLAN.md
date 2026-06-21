# Costroid-Next — M4 detailed implementation plan

> **Provenance:** synthesized from `docs/COSTROID-NEXT.md` §3.1.C/D (break-even + scenario
> modeling), §3.2 (the cost model, lines 124-136), §3.3 (the M4 milestone, line 144), §5.5
> (DeepSWE-Bench cloud anchor + Gemma 4), §6.12 (DoD, the M4 deciding test at line 311); the
> `docs/M3-PLAN.md` house-style; `PROGRESS.md` M4 section (lines 280-292) + checklist (line 354);
> and a **verified** read of the real symbols this milestone builds on — every symbol below was
> confirmed against the working tree (file:line) by a two-pass research + adversarial-verify sweep
> (0 refuted / 0 corrected). Standing rule: **re-verify before editing — the code wins.**
>
> **Status: 📝 PLANNED — NO CODE WRITTEN.** **D1–D4 ✅ SIGNED OFF 2026-06-21 (all recommended ★)**
> via the question panel. **Rev 2 (2026-06-21): coordinator review folded in — "sound, 0 blockers"
> + 1 high / 3 med / 4 low deltas (see the Revision log below).** Now awaiting the coordinator's
> confirmation of these deltas BEFORE any code (T0–T9). Branch `costroid-next` @ `3b79fcd` (M0, M1,
> M2, M3a merged to `main`; M3b is a separate human handoff that does **not** block M4 —
> `PROGRESS.md:403`). Per-task dev-loop (build → independent adversarial review → fix → present).
> Golden rules hold.
>
> **Revision log — Rev 2 (coordinator deltas folded in, each with its deciding test):**
> - **HIGH — the blended cloud $/tok is now its own tested step (new T3).** `c = in·(1−out_share) +
>   out·out_share` was owned by no task; it becomes a **pure core helper** with its own deciding
>   test (out_share 0 → input-only, 1 → output-only, intermediate to the cent). T2 stays per-meter;
>   the crossover (T1) consumes `c` from this helper.
> - **MED1 — feasibility ceiling (T1 outcome + T7 input + T8 render).** `BreakevenOutcome` gains
>   `Infeasible { v_star, max_tokens_per_day, reason }`; the CLI computes the ceiling
>   `estimated_tok_s · utilization · 86400` and passes it in (no `core→power` edge); `V* > ceiling
>   → Infeasible`.
> - **MED2 — full-precision, energy-only local rate (T1 test + T7 conversion).** The marginal rate
>   `e` is **energy-only**: `e = energy_cost / total_tokens` (`LocalRunReport.energy_cost`,
>   harness.rs:42) — **NEVER** `local_cost_per_1m ÷ 1e6`, which includes `amortized_hw_cost` and
>   would double-count the capex that is already the fixed term `hw_fixed_per_day` (corrupting the
>   crossover). Carry it at full precision via `Decimal::from_f64_retain` on `energy_cost` (never
>   `round_dp`); a ~1e-9 `e` must survive and select the correct Never-vs-CrossesAt branch.
> - **MED3 — one break-even lifetime + unit (T1 + T6).** `depreciation_period` is the break-even
>   basis; `hardware_lifetime_seconds` is the §3.2 / `x_AmortizedHwCost` per-run attribution only;
>   T1's worked example pins the period in **days**; setting both as the break-even basis is a typed
>   error.
> - **LOW — T1** adds scaling-invariance (`capex×k → V*×k`; `(c−e)×k → V*×(1/k)`) + a 2nd crossover
>   point/mix; **T4** asserts the mixed sweep marks `[N_low … never]` with **exact** endpoints (not
>   "sanity"); **T7** pins one hand-checked end-to-end numeric `V*`; **T0** now **edits** canon
>   §3.2's "cumulative `local_run_cost` ≤ cumulative `cloud_cost`" sentence to the calendar-fixed
>   model (keeping the per-run block as `x_AmortizedHwCost`).

---

# M4 detailed plan — the local-vs-cloud break-even + scenario engine

## 0. Scope in one sentence

Given a **workload profile** (tokens/day, input/output mix, utilization, electricity rate,
hardware price + depreciation period, pricing-snapshot date), compute the **local-vs-cloud
crossover** versus named cloud prices — *"local breaks even at N tokens/day, or **never** + the
reason"* — as **pure compute** in `costroid-core` (no hardware, no network, no `core→power` edge),
where (a) the LOCAL cost scalar enters core as a plain numeric **input** computed by the CLI via
`costroid-power` behind the off-by-default `power` feature; (b) the CLOUD side is resolved from the
M2 layered per-token pricing **catalog**; (c) the DeepSWE-Bench `$/task` figure rides along as a
clearly-**labeled, dated empirical overlay**, never the crossover math; (d) the output is a
**range + methodology + full assumption stamp**, never a single hero number (R6); and (e) every
visual has a `--plain` ASCII equivalent and never relies on color alone.

---

## 1. M4 "done" criteria (close against this mechanically — never self-judged prose)

1. **The M4 deciding test is green** (§6.12 line 311): a break-even **unit test that includes a
   "never" case** — a synthetic scenario where `blended_cloud_$/tok ≤ local_marginal_energy_$/tok`
   returns `Never { reason }` with the documented reason — *alongside* a hand-computed
   real-crossover case (`CrossesAt { tokens_per_day = N }`), an **`Infeasible`** case (V* above the
   machine's tokens/day ceiling), a **full-precision** case (a ~1e-9 `e` selects the correct
   branch), and the **scaling-invariance** identities. Pure, in `costroid-core`, no power/network.
2. **The amortization model is the calendar-fixed one (D1).** Hardware is a fixed periodic capex
   (`hardware_price / lifetime → $/period`, utilization-independent); the per-run §3.2
   `amortized_hw_cost` stays the FOCUS-row attribution (`x_AmortizedHwCost`) and is **not** the
   break-even basis. A test pins both the crossover formula and the "never" pin
   (`Never ⟺ blended_cloud_$/tok ≤ local_marginal_energy_$/tok`).
3. **No `core→power` edge** (`costroid-core/Cargo.toml` internal deps stay exactly
   `costroid-focus` + `costroid-providers`); the local marginal-energy-$/token + hardware capex
   enter core as `Decimal`/`UsdAmount` inputs. The break-even math is unit-testable on synthetic
   numbers with neither `costroid-power` nor the pricing catalog linked.
4. **Cloud side from the catalog (D3), blended as its own tested step.** The deterministic
   crossover uses the M2 layered per-token catalog (OpenAI/Anthropic/Bedrock) via a new **public**
   core accessor (model → per-input / per-output `Decimal` + `source@as_of#hash8` snapshot id); a
   **separate, independently-tested pure helper** blends those to the scenario's `c = in·(1 −
   out_share) + out·out_share`. The DeepSWE-Bench `$/task` appears only as a labeled, dated overlay
   (`FrontierPoint.cost_per_task_usd`), never multiplied into the crossover.
5. **Output is a range + stamp, not a hero number (D4, R6).** The result carries a
   `[N_low … N_high]` sensitivity band over the adjustable inputs + the full assumption stamp
   (electricity rate, hardware price, lifetime, utilization, in/out mix, **measurement mode**,
   hardware-profile id, **pricing-snapshot date/hash**). Emitted as a **dedicated result struct**,
   **not** a new FOCUS row / `x_` column.
6. **The `costroid breakeven` CLI subcommand exists** behind `#[cfg(feature = "power")]` (it
   computes the local scalar via `costroid-power`), mirrors `bench`'s economics flags + the new
   scenario flags, and renders a styled verdict with a `--plain` ASCII equivalent (byte-identical
   content; warn/critical always paired with a text cue — never color-alone).
7. **Money is `Decimal`/`UsdAmount` end-to-end** — never `f64` for a dollar amount. (Note:
   `UsdAmount` has no `Display`/`ops`; render via `as_usd() → Decimal`; per-token *rates* are bare
   `Decimal`.)
8. **Offline guarantee intact.** Default CLI byte-for-byte no-network; the `--features power`
   build's offline-acceptance leg stays green (break-even is pure-compute — strictly *less* I/O
   than the already-proven estimated `bench`); `POWER_ALLOWED` stays exactly `["costroid-power"]`
   (no new crate).
9. **A scenario config section** (`[breakeven]`/`[scenario]`) loads via the read-only `serde(default)`
   pattern (absent section = zero-config default), with a projection method into a core-side
   neutral input type; no writer, no new dependency.
10. **Pre-PR gate green** (`cargo fmt --all -- --check && cargo clippy --workspace --all-targets --
    -D warnings && cargo test --workspace`), plus the `--features power` clippy+test legs, MSRV,
    license/advisories, and the focus-conformance + offline-acceptance CI legs.
11. **Docs reconciled in lockstep** — `README.md` (the new subcommand), `docs/ARCHITECTURE.md`
    (the break-even module + the public catalog accessor), `PROGRESS.md` (M4 box), and the §6.12
    M4 checkbox.
12. **No prompt/response content** anywhere in the break-even path (the Cardinal Rule); scenario
    inputs are numeric knobs only.
13. **The outcome enum is complete + feasibility-aware (MED1).** `BreakevenOutcome =
    CrossesAt | Never { reason } | Always | Infeasible { v_star, max_tokens_per_day, reason }`; the
    CLI supplies `max_tokens_per_day = estimated_tok_s · utilization · 86400` as a plain input (no
    `core→power` edge), and `V* > ceiling → Infeasible`, rendered + stamped.
14. **One break-even lifetime unit; full-precision local rate (MED3 + MED2).** `depreciation_period`
    (the break-even basis, pinned in **days** in T1) is distinct from `hardware_lifetime_seconds`
    (the §3.2 / `x_AmortizedHwCost` per-run attribution); setting both as the break-even basis is a
    typed error. The local rate `e` is carried at full precision (`from_f64_retain`, never
    `round_dp`).

---

## 1.5 ⚑ DECISIONS TO SIGN OFF BEFORE CODING (CLAUDE.md "ask first")

> These four decisions touch the **public CLI surface** (a new subcommand) and the cost
> methodology, so they are surfaced for the human's sign-off **before any code** (CLAUDE.md
> "Decide vs. ask"). Recommended defaults marked **★**. The plan below is written assuming the ★
> options; a different choice revises the affected tasks.
>
> **✅ SIGNED OFF 2026-06-21 (all recommended ★):** D1 = (a) calendar-fixed capex; D2 = (a) pure
> in core + `costroid breakeven` behind `power`; D3 = (a) catalog crossover + DeepSWE labeled
> overlay; D4 = (a) range + methodology + stamp, dedicated struct. The plan below stands as
> written. Still gated on the coordinator's review before any code.

- **D1 — Amortization model for the crossover (methodology).**
  - **(a) ★ Calendar-fixed capex.** Treat hardware as a fixed periodic cost over the depreciation
    calendar (`hardware_price / lifetime_days → $/day`, **utilization-independent** — the box
    depreciates whether or not it runs). Then `local_per_day(V) = hw_fixed_per_day + e·V` and
    `cloud_per_day(V) = c·V`, giving a genuine crossover `V* = hw_fixed_per_day / (c − e)` when
    `c > e`, and **`Never`** when `c ≤ e` (the hardware capex can never be recovered at any volume).
    Pin: **`Never ⟺ blended_cloud_$/tok (c) ≤ local_marginal_energy_$/tok (e)`** — exactly the
    canon's "never, with the reason." The §3.2 per-run `amortized_hw_cost` remains the **FOCUS-row
    attribution** (`x_AmortizedHwCost`), a separate use of the same capex.
  - **(b) rejected: per-run run-seconds attribution as the break-even basis.** Summing the §3.2
    per-run `amortized_hw_cost` over a month is **linear in tokens**, so local $/token is constant
    → the comparison degenerates to "always cheaper" or "never cheaper" with **no volume
    crossover** and no honest "breaks even at N tokens/day." This is the FOCUS-row attribution, not
    the forward-looking break-even.

- **D2 — Placement, gating, and the CLI surface (architecture + public CLI surface).**
  - **(a) ★** Break-even **math is pure in `costroid-core`** (`breakeven.rs`), unit-testable on
    synthetic `Decimal` inputs; core computes the **cloud** cost from its own catalog (no
    `core→power` edge — forbidden). The **local** marginal-energy-$/token + hardware capex are an
    **input**, computed by the CLI via `costroid-power` behind the off-by-default `power` feature
    (consistent with `costroid bench`). CLI surface: a new **`costroid breakeven`** subcommand
    (four-spot `#[cfg(feature = "power")]` registration in `main.rs`, mirroring `bench`) + a
    `[breakeven]`/`[scenario]` read-only config section.
  - **(b) rejected:** putting the break-even math in `costroid-power`, or adding a `core→power`
    edge — both violate the dependency direction and make the math un-unit-testable without the
    power feature.

- **D3 — Cloud comparison basis (methodology).**
  - **(a) ★** The deterministic crossover uses the **per-token pricing catalog**
    (OpenAI/Anthropic/Bedrock; per-1M ÷ 1e6 → per-token `Decimal`, USD); the **DeepSWE-Bench
    `$/task`** is a clearly-**labeled, dated empirical overlay** (`bench_view` /
    `FrontierPoint.cost_per_task_usd`), shown beside the verdict but **never** folded into the
    crossover arithmetic. The crossover number is identical with or without the overlay present.
  - **(b) rejected:** using DeepSWE `$/task` *as* the cloud cost in the crossover — it is a
    task-average on an undisclosed token-pricing scaffold (its own `cost_note` says "indicative,
    not cache-correct"); using it as the bill would violate R6/R8.

- **D4 — Output shape (R6 honesty + accessibility).**
  - **(a) ★** A **break-even point + a sensitivity range** over the adjustable inputs + the **full
    assumption stamp** (electricity rate, hardware price, lifetime, utilization, in/out mix,
    measurement mode, hardware-profile id, pricing-snapshot date/hash). A **dedicated result
    struct** (not a FOCUS row / `x_` column — break-even is a comparison, not a charge). A
    `--plain` ASCII path is required; never color-alone.
  - **(b) rejected:** a single hero break-even number; or shoehorning the result onto `FocusRecord`
    (its 22-col `x_` tail has no crossover/per-1M columns and is a per-meter charge ledger).

---

## 2. Ordered task list (dependency-correct; pure math first, CLI last)

> Each task notes its **Do**, its **Deciding test**, and its top **Risk + Mitigation**. Ordering:
> the pure core math + its deciding test land first (T1 — the milestone gate), then the cloud
> per-meter accessor (T2) and the **blend helper** (T3) it feeds, the range/stamp (T4) + overlay
> (T5), then the config (T6) and the CLI/render (T7/T8) that consume them, then wire-up (T9). All
> `§`/`R#` refs point into `docs/COSTROID-NEXT.md`.

### T0 — Reconcile the break-even canon + edit §3.2 to the calendar-fixed model (docs) **[LOW (Rev 2)]**
- **Do:** Confirm §3.2 (lines 124-136), §3.1.C/D, §3.3 line 144, §6.12 line 311 against this plan.
  **Edit** canon §3.2's break-even sentence (line 136) — currently "Break-even = the monthly token
  volume / utilization at which **cumulative `local_run_cost` ≤ cumulative `cloud_cost`**" — to the
  **calendar-fixed** model (D1): break-even is the daily volume `V*` at which
  `hw_fixed_per_day + e·V ≤ c·V`, i.e. `V* = hw_fixed_per_day / (c − e)`, and **never** when
  `c ≤ e`. **Keep** the §3.2 per-run formula block (`amortized_hw_cost = (hardware_price /
  hardware_lifetime_seconds)·run_seconds`) verbatim, re-labeled as the **`x_AmortizedHwCost`
  per-run FOCUS attribution** (a separate use of the capex), so the two models are visibly distinct.
  No source-code behavior change.
- **Deciding test:** none (docs); a reviewer diff shows exactly two changes — the §3.2 break-even
  sentence now reads the calendar-fixed model, and the per-run block is retained + re-labeled as
  `x_AmortizedHwCost`.
- **Risk (R8):** the canon §3.2 line currently reads as if the per-run sum *is* the break-even (it
  is linear → no crossover). **Mitigation:** this edit fixes the canon itself; D1 + T1's tests
  encode the calendar-fixed model.

### T1 — The pure break-even core math (`costroid-core::breakeven`) **[D1] [the M4 deciding test]**
- **Do:** A new `breakeven.rs` module. A pure function over **explicit** inputs (no power, no
  network, no catalog): `local_marginal_energy_per_token: Decimal` (= `e`, full precision —
  MED2), `hardware_capex: UsdAmount`, `depreciation_period` (the **break-even** lifetime basis —
  MED3), blended `cloud_per_token: Decimal` (= `c`, supplied by the T3 blend helper on the real
  path / explicit in tests), `max_tokens_per_day: Option<Decimal>` (the feasibility ceiling
  supplied by the caller — MED1), and a `Scenario { tokens_per_day, output_share, utilization,
  depreciation_period, electricity_rate (echo for stamp), … }`. Compute
  `hw_fixed_per_day = hardware_capex / depreciation_period_days` (calendar-fixed, D1) and
  `V* = hw_fixed_per_day / (c − e)`. Return `enum BreakevenOutcome { CrossesAt { tokens_per_day },
  Never { reason }, Always, Infeasible { v_star, max_tokens_per_day, reason } }`:
  - `c ≤ e` → **`Never { reason }`** (capex never recovered at any volume);
  - `c > e`, `hw_fixed == 0` → **`Always`**;
  - `c > e`, `V* > max_tokens_per_day` → **`Infeasible { v_star, max_tokens_per_day, reason }`**
    (MED1 — the crossover is real but exceeds what the machine can produce per day);
  - else → **`CrossesAt { V* }`**.
  Money/rates = `Decimal`/`UsdAmount`; `e` is consumed at **full precision** — the function never
  `round_dp`s it (MED2). **MED3:** `depreciation_period` is the only break-even lifetime; a caller
  that also supplies `hardware_lifetime_seconds` *as the break-even basis* is a **typed error**
  (`hardware_lifetime_seconds` belongs to the §3.2 per-run attribution only). Typed errors
  (reuse/extend `CoreError`) for non-positive period / NaN / negative inputs — **never a panic**
  (libs deny `unwrap`/`expect`/`panic!`).
- **Deciding test (the M4 gate):** worked-example unit tests with **exact hand-computed** values:
  (i) the required **"never" case** — `c ≤ e` → `Never { reason }` with the documented reason;
  (ii) a **real crossover** — `c > e`, hand-computed `V* = N tokens/day`, the **period pinned in
  days** (MED3);
  (iii) a **2nd crossover at a different output mix** (LOW) — a different `c` → a different
  hand-computed `V*`;
  (iv) an **`Infeasible` case** (MED1) — `V* > max_tokens_per_day` → `Infeasible` with the ceiling
  echoed;
  (v) a **full-precision case** (MED2) — a `~1e-9` difference in `e` survives (no rounding) and
  flips the Never-vs-CrossesAt branch at the boundary;
  (vi) **scaling-invariance** (LOW) — `capex×k → V*×k`; `(c−e)×k → V*×(1/k)`;
  (vii) degenerate guards (non-positive period, NaN rate, conflicting lifetimes (MED3), zero
  hardware → `Always`) are typed errors / the documented branch, not panics.
- **M4.** **Risk (HIGH — the milestone hinges here):** per-run-by-reflex (no crossover); a unit
  slip (per-day vs per-month, per-token vs per-1M); silently rounding `e`. **Mitigation:** D1 fixed
  up front; the period/unit pinned in **days** in the worked examples; the never + crossover +
  infeasible cases hand-computed and asserted to the cent; the ~1e-9 test guards precision.

### T2 — Public cloud per-token accessor on the catalog (`costroid-core`) **[D3]**
- **Do:** The layered catalog (`PricingCatalog`, `resolve_key` lib.rs:2086, `rate` lib.rs:2061) is
  **private** today and `bundled_pricing_value()` exposes only the curated tier as untyped JSON.
  Add a small **public** accessor — e.g. `cloud_price_per_token(model, token_type, override) ->
  Result<Option<PricedRate>, CoreError>` returning the **per-meter** per-token `Decimal` (catalog
  per-1M ÷ `1_000_000`, lib.rs:1854), the `source@as_of#hash8` snapshot id (`pricing_snapshot_id`,
  lib.rs:1938), and the resolved currency (USD). Reuse `PricingCatalog::layered(read_pricing_override(…))`.
  This stays **strictly per-meter** (input vs output separately) — the mix blend is the separate
  T3 helper. *(New public library surface → covered by D2 sign-off.)*
- **Deciding test:** a known model (e.g. an Anthropic + an OpenAI + a Bedrock row) resolves to the
  expected per-token `Decimal` + snapshot id; an unknown model → `Ok(None)`; a known model with a
  missing meter → `Ok(None)`/documented; date-suffixed model names resolve via `strip_date_suffix`.
- **M4.** **Risk:** leaking the private catalog internals / unit confusion (per-1M vs per-token).
  **Mitigation:** the accessor returns a narrow typed `PricedRate`, not the catalog; divide-by-1e6
  pinned in the test against a known JSON rate.

### T3 — Blended cloud $/token helper (`costroid-core`) **[D3 — HIGH (Rev 2)]**
- **Do:** A **pure** helper that combines the T2 per-meter prices into the scenario's blended cloud
  per-token: `c = input_per_token·(1 − output_share) + output_per_token·output_share`, with
  `output_share ∈ [0, 1]` (a typed error otherwise). Bare `Decimal` in / out — no power, no
  network. This is the value the T1 crossover consumes as `cloud_per_token`. Splitting it out keeps
  T2 strictly per-meter and gives the mix arithmetic its own test surface (it was owned by no task
  before Rev 2).
- **Deciding test:** `output_share = 0` → `c == input_per_token`; `output_share = 1` → `c ==
  output_per_token`; an **intermediate** mix (e.g. 0.25) → the hand-computed `c` **to the cent**;
  `output_share` outside `[0,1]` → typed error; the produced `c` feeds T1 and reproduces a
  hand-computed `V*` (closes the HIGH gap end-to-end).
- **M4.** **Risk:** the mix is computed ad-hoc at the call site (untested) or weighted backwards
  (input/output swapped). **Mitigation:** one named helper, the only blend path; the
  0 / 1 / intermediate test pins direction + value.

### T4 — Sensitivity range + assumption stamp (`costroid-core`) **[D4]**
- **Do:** Wrap T1 in a sweep over the adjustable inputs (electricity rate, hardware price,
  depreciation period, utilization, output mix) across a documented low/high span → a
  `[N_low … N_high]` break-even **band**, propagating `Never`/`Infeasible` endpoints honestly (a
  swept endpoint that crosses into `c ≤ e` is marked **never**, not dropped). Attach the full
  **assumption stamp** struct (electricity rate, hardware price, depreciation period, utilization,
  output mix, measurement mode, hardware-profile id `id@as_of`, pricing-snapshot
  `source@as_of#hash8`, collector version). The public `BreakevenReport` = point + band + stamp +
  (T5) overlay.
- **Deciding test:** a scenario yields a **band, not a scalar**; the stamp carries every required
  field; a **mixed sweep that runs from feasible into `c ≤ e` marks the band `[N_low … never]` with
  EXACT endpoints** (the hand-computed `N_low` and the precise `never` cut-over — **not** a
  "monotone/sanity" check — LOW); a fully-"never" scenario propagates "never" across the whole band.
- **M4.** **Risk (R6):** presenting a hero number; an incomplete stamp; a sweep that silently drops
  "never"/"infeasible" endpoints. **Mitigation:** `BreakevenReport` has no single-number
  constructor that omits the band; the exact-endpoint test; a stamp-completeness assertion.

### T5 — DeepSWE-Bench `$/task` empirical overlay (labeled, NOT crossover) (`costroid-core`) **[D3]**
- **Do:** Surface the dated DeepSWE v1.1 `$/task` points (via `bench_view` /
  `FrontierPoint.cost_per_task_usd`, bench.rs:170 — already shipped, fail-closed `as_of`) on the
  `BreakevenReport` as a clearly-labeled, dated reference list (model · `$/task` · `as_of` ·
  source), kept structurally separate from the crossover fields. Never multiply it into `V*`.
- **Deciding test:** the overlay is present, dated, and labeled; **the crossover `V*` is
  bit-identical whether or not the overlay is attached** (proves it is reference-only); a `None`
  cost stays "n/a", never zero.
- **M4.** **Risk:** the overlay bleeds into the crossover math. **Mitigation:** the overlay is a
  separate field consumed by no arithmetic; the bit-identical assertion guards it.

### T6 — The scenario config section (`costroid-config`) **[D2]**
- **Do:** Add a `[breakeven]` (or `[scenario]`) section: a `#[derive(Default, Deserialize)]
  #[serde(default)]` struct, a **private** field on `Config`, money as the `Money(Decimal)` newtype,
  physical knobs as plain `f64`/`bool` (mirroring `AlertsConfig`). **MED3:** the break-even basis is
  `depreciation_period` (with an explicit unit — days); `hardware_lifetime_seconds` is accepted only
  for the §3.2 per-run attribution, and a config that supplies **both** as the break-even basis is
  rejected as a typed error by the projection. Add a projection `Config::breakeven_scenario() ->
  costroid_core::ScenarioInput` (the neutral input type defined in core, like
  `BudgetTargets`/`AlertThresholds`). Read-only; absent section = zero-config default; unknown keys
  ignored. No writer, no new dependency.
- **Deciding test:** a TOML `[breakeven]` loads into the scenario; an absent section =
  `Config::default()` scenario; an unknown key is ignored (forward-compat); a config that sets
  **both** a break-even `depreciation_period` and a conflicting break-even lifetime is a typed error
  (MED3).
- **M4.** **Risk:** money parsed as `f64`; two competing lifetime knobs. **Mitigation:** reuse the
  `Money` newtype + its `deserialize_any` visitor; one break-even lifetime field (MED3), the other
  reserved for the per-run attribution.

### T7 — The `costroid breakeven` CLI subcommand **[D2 — public CLI surface]**
- **Do:** Four-spot registration in `apps/cli/src/main.rs`, all `#[cfg(feature = "power")]` (it
  computes the local scalar via `costroid-power`): the `mod breakeven;` line, the
  `Command::Breakeven(BreakevenArgs)` variant, the `BreakevenArgs` struct, the dispatch arm. Flags
  mirror `bench`'s economics (`--model`, `--quant`, `--electricity-rate`, `--hardware-price`,
  `--hardware-lifetime-seconds`, `--hardware-profile`) **plus** scenario flags (`--tokens-per-day`,
  `--output-share`, `--utilization`, `--depreciation-period`, `--compare-to`/`--cloud-model`,
  `--pricing-override`, reuse `--out` `ExportFormat` if a machine emit is wanted). Flow:
  (1) compute the **energy-only** marginal rate `e = energy_cost / total_tokens` from
  `LocalRunReport.energy_cost` (harness.rs:42) via `estimate_run` (pure, no subprocess) — **NOT**
  `local_cost_per_1m ÷ 1e6` (that includes `amortized_hw_cost` → double-counts the capex already in
  `hw_fixed_per_day`) — converting the f64 `energy_cost` to `Decimal` via **`from_f64_retain` (full
  precision, never `round_dp`)** (MED2); (2) compute the
  **feasibility ceiling** `max_tokens_per_day = estimated_tok_s · utilization · 86400` (from
  `ModelSpec.estimated_tok_s`) and pass it to core as a plain number (MED1 — **no `core→power`
  edge**); (3) resolve per-meter cloud prices via T2 and blend via T3 → `c`; (4) call T4's
  `breakeven_report(…)`; (5) hand to T8's renderer. Config defaults from T6; flags override.
- **Deciding test (`apps/cli/tests`):** `breakeven` with synthetic flags **prints one hand-checked
  end-to-end numeric `V*`** computed with **energy-only `e`** (`energy_cost / total_tokens`) — the
  fixed model/price/scenario is chosen so the asserted `V*` is **wrong if anyone used the total
  `local_cost_per_1m` rate instead**, so this test is the one place that catches the energy-only
  slip (T1 takes `e` as an explicit input and cannot catch it); a "never"-inducing flag set prints
  the reason; a low-`--utilization` set drives `Infeasible` with the ceiling shown (MED1); the
  default build does **not** know the subcommand (power off);
  `cli_power_feature_admits_only_power_allowed` + `cli_default_build_is_power_free` still pass.
- **M4.** **Risk:** pulling a new crate (trips the offline gate); rounding `e`; mis-splitting
  energy-only vs total cost. **Mitigation:** reuse only the already-linked power symbols; derive
  marginal energy $/token = `energy_cost / total_tokens` from `LocalRunReport` (harness.rs:31-56)
  at full precision; keep `POWER_ALLOWED` unchanged.

### T8 — Human-readable break-even render + `--plain` (`apps/cli`) **[D4]**
- **Do:** A styled `StyledDocument` (the `run_trends` pattern, `render.rs`): the crossover / never /
  **infeasible** sentence (MED1 — render `Infeasible { v_star, max_tokens_per_day, reason }` as
  "would break even at V* tokens/day, but the machine tops out at N tokens/day — infeasible"), the
  `[N_low … N_high]` band, the assumption stamp, and the labeled DeepSWE overlay. Color via
  `SemanticStyle` only; every `Warn`/`Critical` (e.g. never / infeasible) paired with a textual cue
  (never color-alone); `print!("{}", doc.render(render_options))`. Take `render_options:
  render::RenderOptions` (the global `--plain` threads through `main.rs:293`).
- **Deciding test:** `--plain` / `NO_COLOR` / non-TTY all produce **byte-identical content** to the
  styled run minus escapes; the "never" **and** "infeasible" reasons render with their numbers; a
  snapshot of the plain output is asserted; no raw `\x1b[` / `ratatui::Color` introduced.
- **M4.** **Risk (R-accessibility):** color-alone or a `--plain` divergence. **Mitigation:** compose
  only via the shared `StyledSpan` helpers (the single place ANSI is applied, render.rs:254); the
  byte-identical test.

### T9 — Wire-up, CI, docs (all crates)
- **Do:** Run the full pre-PR gate + the `--features power` clippy/test legs; confirm the
  offline-acceptance power leg stays green (break-even adds no I/O); update `README.md`
  (the subcommand + an example), `docs/ARCHITECTURE.md` (the `breakeven` + blend modules + the
  public catalog accessor + "no core→power edge"), `PROGRESS.md` (M4 → built, T-list), and check the
  §6.12 M4 box + the COSTROID-NEXT/PROGRESS M4 checklist lines.
- **Deciding test:** the full gate green; `cargo test -p costroid --test offline` +
  `scripts/offline_acceptance.sh` green; the §6.12 M4 deciding test (T1) referenced as the gate.
- **M4.** **Risk:** docs drift from behavior. **Mitigation:** docs edited in the same commits as
  the behavior they describe (DoD).

---

## 3. What lands in M4 vs what defers to M5/M6

- **In M4 (agent-ownable, CI-tested, the merge target — needs NO hardware, NO network):** the
  pure core break-even math + the milestone deciding test (T1), the public cloud per-meter accessor
  (T2) + the blend helper (T3), the sensitivity range + assumption stamp (T4), the labeled DeepSWE
  overlay (T5), the scenario config section (T6), the `costroid breakeven` CLI + styled/`--plain`
  render (T7/T8), wire-up + docs (T9). The estimated local engine (M3a) supplies the local scalar;
  M3b is **not** required (`PROGRESS.md:403` — "M4 can proceed on the estimated engine in parallel").
- **Defers to M5 — Interfaces:** surfacing break-even in the TUI, the Axum/`tiny_http` local API,
  and the minimal three-view web UI. M4 ships the **engine + the one CLI surface**; the broader
  UI is M5.
- **Defers to M6:** the methodology/limitations doc page, demo assets, and any benchmark-dataset
  writeup beyond the already-shipped dated DeepSWE v1.1 snapshot.

---

## 4. Cross-cutting risks to resolve EARLY

1. **The amortization subtlety (D1) is the whole milestone.** Resolve it up front; encode both the
   crossover and the "never" pin in T1's hand-computed tests. Per-run vs calendar-fixed is the
   difference between "no crossover ever" and an honest break-even.
2. **Unit discipline.** Catalog price is per-1M tokens → ÷1e6 for per-token (lib.rs:1854); the
   crossover is per-day; energy is per-token. Pin every unit in worked examples.
3. **Money types.** `UsdAmount(Decimal)` has **no `Display`/`Add`/`Sub`/`Sum`** — render via
   `as_usd() → Decimal`, do exact math via `checked_add`/`checked_sub`; per-token *rates* are bare
   `Decimal`. Never an `f64` dollar amount (clippy + R discipline).
4. **The catalog is private (T2).** The new public accessor is the only new library surface — keep
   it narrow (a typed `PricedRate`, never the catalog) and covered by D2 sign-off.
5. **Blended cloud per-token at the scenario mix (now T3).** Cloud input vs output prices differ;
   the crossover must use the **mix-weighted** cloud per-token (`c = in·(1−out_share) +
   out·out_share`), computed by the dedicated T3 helper, not ad-hoc at the call site
   (`output_share ∈ [0,1]`).
6. **Utilization semantics — define honestly.** State exactly what utilization scales (the energy
   duty-cycle / whether the daily volume fits the machine-hours) so the crossover is not silently
   optimistic; it is calendar-independent for the *hardware* term (D1) but bounds the *energy* and
   the **feasibility ceiling** (`estimated_tok_s · utilization · 86400`, MED1).
7. **R6 "ranges not a hero number"** is a done-criterion (item 5), enforced structurally by T4.
8. **Feasibility, precision, and lifetime ambiguity (Rev 2).** A real `V*` can exceed the machine's
   tokens/day ceiling → the **`Infeasible`** outcome (MED1), with the ceiling supplied by the CLI as
   a plain number (no `core→power` edge). The local rate `e` must stay full-precision
   (`from_f64_retain`, never `round_dp`) or a near-tie flips the wrong branch (MED2). Exactly one
   break-even lifetime unit (`depreciation_period`, days) — `hardware_lifetime_seconds` is the
   per-run §3.2 attribution only; setting both as the break-even basis is a typed error (MED3).

---

## 5. What M4 deliberately does NOT do (defended scope)

- **No web UI / API / TUI panel** — that is M5. M4 is the engine + the single `costroid breakeven`
  CLI surface.
- **No live DeepSWE-Bench fetch** — it consumes the already-shipped dated v1.1 snapshot
  (`bench/benchmarks.v1.json`, R8); never hardcodes or recomputes a `$/task` value.
- **No new FOCUS `x_` column / no FOCUS-row emission for the break-even result** — break-even is a
  comparison, not a charge; it is its own struct (D4). The per-run `x_AmortizedHwCost` (M3) is
  untouched.
- **No `core→power` edge, no network, no credential code.** Pure compute; the local scalar enters
  as a number (D2). If any task appears to need network/credentials → **stop and ask the human.**
- **No change to the M3 measured/estimated engine or its samplers** — M4 reads its output.
- **No telemetry; the default build stays byte-for-byte no-network.**
