# M3b Phase 2 — flip `gemma-4-31b-dense` estimated → MEASURED (FLIP PLAN)

> **Cadence gate.** This document is written **first** for plan-review. **No shipped
> data / code / allowlist is edited until this plan is approved.** After approval: execute the
> per-file checklist (§7), regen the two `.sha256` sidecars, run the full gate (§9), present for
> boundary review. Golden rules hold (no `unwrap`/`expect`/`panic!` in lib **or** tests;
> permissive-only; the default build makes zero network calls).
>
> **Status: figures are FINAL** (from the completed measured run, §1) and **independently verified**
> (§0.1). Awaiting plan-review sign-off on the §10 decisions before any edit.

## 0. Scope (what this is, and is not)

A **per-model PARTIAL flip** of the published benchmark dataset
`benchmarks/gemma4-vs-cloud-2026-06/`: replace the **estimated** `gemma-4-31b-dense` figures with
the **real wall-meter measurement**, flip its honesty stamps, and add it to the authoritative
`MEASURED_MODELS` allowlist. `gemma-4-26b-a4b` and `gemma-4-12b-unified` stay **estimated — pending
M3b measurement**.

Follows [`docs/POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md) **Steps 1b–5**; **Step 1 is SKIPPED**
(D3 partial flip — the bundled `crates/costroid-power/models/gemma4.v1.json` +
`profiles/hardware.v1.json` stay estimated, so the demo/samples stay byte-stable and `models.rs` /
`profile.rs` are **untouched**). The demo/sample pack (`samples/**`) stays estimated (Step 4).

**Measurement provenance.** ASUS ProArt PX‑13 / Radeon 8060S (Strix Halo, gfx1151), **llama.cpp
Vulkan backend** (NOT ROCm), Windows GPU driven from WSL2 via interop (wrapper
`/home/eren/llama-gpu.sh` → `/mnt/c/llama/llama-completion.exe -ngl 999 -fa 1 --ignore-eos
-no-cnv`). PM 231 E wall meter: **idle 15 W, steady decode 96 W** (total‑system AC, idle‑inclusive).
Three validation runs (`--wall-meter-watts 96 --tokens-out 4000`) gave `x_MeasuredWh ≈ 10.29`
(±0.13 %); the committed full run (§1) is `49.494614 Wh` at **~9.70 tok/s** (the long 18,000-token
generation averages slower than the 4,000-token validations as the KV cache grows).

### 0.1 Independent verification (done — pre-review)

A 3-agent verification workflow (read/compute only, no edits) ran before this finalization:
- **Recompute** — re-derived every figure from the committed raw row with exact `Decimal`, blind to
  this plan, using the documented `costroid-core::breakeven` formula. **Matched §4/§5 to the digit.**
- **Coupling audit** — `breaks_on_flip` came back **EMPTY**: every committed surface that fails when
  the benchmarks/ 31b row flips is one of the five planned files (§7.A/B). `methodology_crosscheck.rs`,
  `samples_datasets.rs`, `docs_presence.rs`, `focus_conformance.sh`, `bench_cli.rs` confirmed **safe**
  (samples/docs/bundled-tied, not benchmarks/). The provenance guard provably forces
  allowlist+manifest+raw to flip together.
- **Method audit** — **0 blockers.** Confirmed D1 (`--measure` ignores `--tokens-in`), D2 (the CLI
  cannot emit the measured break-even; the hand-computation faithfully replicates the engine; option
  B is *worse*), D3 (no test couples the header `runtime_kind` to per-row `x_RuntimeKind`). Its
  refinements are folded in below (D1 wording; the run-object-has-no-`runtime_kind` note; the e-basis
  documentation requirement; the `energy_only_cost = Wh/1000 × rate` cross-check; the
  `matches!`/`grep -F` reminders).

---

## 1. Step 1 — the regenerated committed raw row (the source figures) — DONE

The committed raw row was regenerated **once** by the measured `bench` (a *captured measurement*,
not a byte-reproducible estimate — see §6):

```bash
cd ~/costroid
SOURCE_DATE_EPOCH=1781913600 cargo run -q -p costroid --features power -- bench \
  --model gemma-4-31b-dense --runtime llama.cpp --binary /home/eren/llama-gpu.sh \
  --runtime-model "C:\Users\ereno\.lmstudio\models\lmstudio-community\gemma-4-31B-it-GGUF\gemma-4-31B-it-Q4_K_M.gguf" \
  --tokens-out 18000 --tokens-in 2000 --measure --wall-meter-watts 96 --out json
```

`SOURCE_DATE_EPOCH=1781913600` (2026-06-20T00:00:00Z) keeps the row timestamp byte-deterministic
(D5) — the row's `ChargePeriodStart`/`End` stay 2026-06-20, *not* the measurement date.

> **D1 token basis (verified against `apps/cli/src/bench.rs` + the runner).** In `--measure` mode the
> prompt is the fixed `BENCH_PROMPT` and `--tokens-in 2000` is **never read** (only the estimated
> branch uses it). `tokens_in` is the **runtime-reported** prompt-token count — **~18 for this
> benchmark prompt** (not a hardcoded constant; a re-tokenization to 17/19 would give 18017/18019).
> The committed run tokenized to **18** → `tokens_out 18000`, **total 18018** → `x_ConsumedTokens =
> 18018.0` (the M5 total-token basis). The `--tokens-out 18000` flag *is* honored (the decode budget).

### 1.1 The regenerated row's load-bearing fields (FINAL)

| Field (raw FOCUS row) | Estimated (current, committed) | **Measured (regenerated)** |
|---|---|---|
| `ServiceName` / `ProviderName` / `x_Tool` | `ollama` | **`llama.cpp`** |
| `x_RuntimeKind` | `ollama` | **`llama.cpp`** |
| `x_BenchmarkId` | `…/Q4_K_M/ollama` | **`…/Q4_K_M/llama.cpp`** |
| `x_MeasurementMode` | `estimated` | **`measured_wallmeter`** |
| `x_Estimated` | `true` | **`false`** |
| `x_AvgPowerWatts` | `155.0` | **`96.0`** |
| `x_ConsumedTokens` | `20000.0` | **`18018.0`** |
| `x_MeasuredWh` | `64.583333` | **`49.494614`** |
| `BilledCost` = `local_run_cost` | `0.0420431253` | **`0.0471557357`** |
| `x_AmortizedHwCost` | `0.031709792` | **`0.0392365975`** |
| `x_HardwareProfile` | `strix-halo-128gb@2026-06-20` | **unchanged** (profile stays estimated, D3) |

Derived for the manifest (§4), all reproducible **from the committed raw row** by exact arithmetic:

| Quantity | Formula | **Measured** |
|---|---|---|
| measured `tok_s` | `tokens_out × avg_W ÷ (Wh × 3600)` | **9.698 tok/s** |
| `run_seconds` | `Wh × 3600 ÷ avg_W` | **≈ 1856.05 s** (~30.9 min) |
| `energy_only_cost` | `BilledCost − x_AmortizedHwCost` | **`0.0079191382`** |
| cross-check | `Wh ÷ 1000 × 0.16` | `0.00791913824` ✓ (matches to 4e-11) |
| `energy_only_e_per_token` (run, ÷18018) | `energy_only_cost ÷ 18018` | **`0.000000439512610`** † |
| `energy_only_e_per_million` | `e_per_token × 1e6` | **`0.439512610`** † |

> † **Precision note.** `0.0079191382 ÷ 18018` is **non-terminating** (18018 = 2·3²·7·11·13). It is
> rendered at the estimated-entry precision (15 dp / 9 sig figs); `e_per_million = e_per_token ×
> 1e6` holds in the strings. (The estimated entry terminated because its basis was 20000.) Flagged
> in §10-Q5 in case the reviewer prefers fewer digits.

---

## 2. Decisions (with recommendations) — please confirm at review

### D1 — token basis = the measured reality (18,018), per-model
**Recommendation: adopt as stated.** The manifest's 31b `tokens_in / tokens_out / tokens_total` and
its per-token figures move to the measured run's reality: **18 in / 18,000 out / 18,018 total** (the
`tokens_in` is the runtime-reported prompt count, ~18 for this prompt — see §1). `26b`/`12b` keep
`2000/18000/20000`. The writeup must note **31b = measured (real short benchmark prompt) vs 26b/12b
estimated**.

### D2 — recompute break-even with measured 96 W + measured tok/s
**Recommendation: keep the cloud comparison scenario fixed; swap only the local power profile.**

Per **POST-M3B-REFRESH Step 3** ("the cloud-side dollars do not change from M3b; only the local
side does"), the break-even keeps the **2,000-in / 18,000-out (20,000-token, 0.9 output-share)
workload and the cloud side** (`cloud_cost_same_tokens = 0.46`, `blended_cloud_per_million = 23`,
`output_share = 0.9`) **unchanged**; only the **local** energy rate moves from estimated
(155 W / 12.0 tok/s) to **measured (96 W / 9.698 tok/s)**.

**The energy is identical either way** — the measured `energy_only_cost` is the pure 18,000-token
**decode** energy at 96 W, and the engine models prefill as free (`wall_seconds = tokens_out ÷
tok_s`), so it is the same energy for the 18,018-total run and for the 20,000-token workload. Only
the per-token **denominator** differs:

- **run** (manifest run fields, D1): `e_run = energy_only_cost ÷ 18018 = 0.000000439512610`.
- **break-even** (D2, the modelled 20,000-token workload, comparable to 26b/12b):
  `e_be = energy_only_cost ÷ 20000 = 0.00000039595691` (terminates). Note `e_run = e_be × 20000/18018`.

> ⚠ **Decision to confirm (the one judgement call).** This introduces a *basis split* the estimated
> manifest did not have. The verification confirmed the alternative (**"option B"** — model the
> break-even on the measured run's own mix, 18,018 total, `output_share ≈ 0.999`) is **worse**: it
> forces `c ≈ 24.98 $/M`, `cloud_cost ≈ 0.45009` (non-round), `V* ≈ 74,427`, and breaks comparability
> with 26b/12b — contradicting Step 3. **I recommend the Step-3-faithful split** and **the writeup
> must state both bases explicitly** (run `e` ÷18018 vs break-even `e` ÷20000; the cloud side is the
> unchanged 20,000/0.9 counterfactual) so a reader does not read the 0.4395/M run figure and the
> 0.3960/M break-even figure as a contradiction.

**Mechanism note (verified).** `costroid breakeven` derives `e` from the **bundled estimated**
profile (155 W) + manifest (12.0 tok/s), which D3 forbids touching, and there is **no
`--load-watts` / `--tok-s` flag**. So `costroid breakeven` **cannot emit the measured break-even**
(it still prints `81237`). The measured `V*` + sensitivity are therefore **computed from the
documented engine formula** (`crates/costroid-core/src/breakeven.rs`), shown in full in §5 — verified
to reproduce the committed *estimated* bands (opus 64990/81237/98818; sonnet 110002/137503/167824)
to the token, then re-derived independently from the measured row.

### D3 — manifest header + 31b run stamps + as_of + note/source
**Recommendation: adopt, with the sub-points below.**
- **Header** `local.runtime_kind`: `ollama → llama.cpp`. ⚠ The 26b/12b raw rows keep
  `x_RuntimeKind = "ollama"` (estimated, unchanged), so the header field then describes the measured
  31b run; the rewritten header `note` makes the mix explicit. (No test couples the header
  `runtime_kind` to the per-row value — verified.)
- **Header** `local.load_watts`: **keep `"155"`** (the estimated profile default 26b/12b use). The
  measured 96 W lives in the 31b run's `avg_power_watts` + raw row. D3 does not ask to change it.
- **The 31b *run object* has no `runtime_kind` field** (it's a header-only key — the runbook's
  Step-2 phrase "change *its* runtime_kind" is imprecise; there is nothing at the run level to flip).
  The run's runtime change is carried by the regenerated raw row's `x_RuntimeKind = "llama.cpp"`.
- 31b run: `measurement_mode → measured_wallmeter`, `tok_s_estimated → false`, `estimated_tok_s →
  "9.698"`.
- `as_of`: `2026-06-20 → 2026-06-25` (the measurement date; the *row timestamp* stays 2026-06-20 via
  `SOURCE_DATE_EPOCH`, D5).
- `note` / `source`: rewrite for the **mixed** dataset (31b measured via wall meter on llama.cpp
  Vulkan; 26b/12b estimated — pending M3b).

### D4 — fix POST-M3B-REFRESH.md Step 0 (WSL/Vulkan, not ROCm)
**Recommendation: adopt.** Step 0's "HARD-abort unless `rocm-smi` shows the iGPU busy" is wrong for
WSL2 (`rocm-smi` is unsupported there). Replace the GPU-residency check with the method actually
used — `rocminfo` / `llama --list-devices` + the llama.cpp **Vulkan** startup logs (`ggml_vulkan:
Found N Vulkan devices`, layers *offloaded to GPU*, a *Vulkan* KV/compute buffer) + Windows Task
Manager GPU load — and relabel the backend **Vulkan via the WSL-interop wrapper** (drop "ROCm/HIP"
and the "gfx1151 ROCm env"). Keep the CPU-fallback abort (≈2–4 tok/s, no GPU residency → abort).

### D5 (discovered) — README
**Recommendation: NO required change; confirm.** The README's only 31b references are the
**hardware-free estimated demo** (`bench`/`breakeven`, no `--measure` — stays byte-stable, Step 4)
and a break-even **example output that uses the default 26b model** (`87214 tokens/day`), neither of
which the 31b-only measured flip affects. The README presents **no** measured/benchmark-dataset hero
figure. *(Optional:* a one-line note in the local-inference section that a measured 31B wall-meter
benchmark now ships in `benchmarks/…`. Recommend skipping unless the reviewer wants it; the
pre-existing M6-4 "label the illustrative figure" item is out of scope here.)*

---

## 3. Files NOT changed (and why) — keeps the checklist honest (verified `breaks_on_flip` = ∅)

| Surface | Why it stays |
|---|---|
| `crates/costroid-power/models/gemma4.v1.json` (+ `.sha256`, `models.rs`, `models/README.md`) | D3 partial flip — bundled estimate stays; demo byte-stable; `GEMMA4_MANIFEST_AS_OF`/`@2026-06-20` pins stay green |
| `crates/costroid-power/profiles/hardware.v1.json` (+ `.sha256`, `profile.rs`) | same — 155 W estimate stays the shared default; `POWER_PROFILE_AS_OF` pin stays green |
| `samples/benchmark/*.bench.json` (+ `.sha256`), `samples/README.md`, `samples/benchmark/README.md` | Step 4 — the demo pack stays estimated; `samples_datasets.rs` + `focus_conformance.sh` byte-match it |
| `docs/methodology.md` **§4 worked example** + `apps/cli/tests/methodology_crosscheck.rs` + `apps/cli/tests/docs_presence.rs` | pinned to the **sample** 31b row (a *distinct* file from benchmarks/, estimated, unchanged); the literal `0.000000516666665` must stay |
| `apps/cli/tests/samples_datasets.rs`, `apps/cli/tests/bench_cli.rs`, `scripts/focus_conformance.sh`, `scripts/check_benchmarks.sh` (script body) | verified safe — sample/bundled-tied or sha256-only (the script needs no edit; only the 2 sidecars regen) |
| `benchmarks/…/raw/gemma-4-26b-a4b.bench.json` + `gemma-4-12b-unified.bench.json` (+ `.sha256`) | estimated, unchanged (listed in the runbook only to keep the checklist closed) |
| Root `README.md` | D5 — no measured hero figure to relabel (confirm) |

---

## 4. Recomputed manifest fields — `gemma-4-31b-dense` run (§7 file 1) — FINAL

| Manifest field | Estimated (current) | **Measured (new)** |
|---|---|---|
| `measurement_mode` | `"estimated"` | **`"measured_wallmeter"`** |
| `tok_s_estimated` | `true` | **`false`** |
| `estimated_tok_s` | `"12.0"` | **`"9.698"`** (measured) |
| `tokens_in` / `tokens_out` / `tokens_total` | `2000 / 18000 / 20000` | **`18 / 18000 / 18018`** |
| `energy_wh` | `"64.583333"` | **`"49.494614"`** |
| `avg_power_watts` | `"155.0"` | **`"96.0"`** |
| `local_run_cost` | `"0.0420431253"` | **`"0.0471557357"`** |
| `amortized_hw_cost` | `"0.031709792"` | **`"0.0392365975"`** |
| `energy_only_cost` | `"0.0103333333"` | **`"0.0079191382"`** |
| `energy_only_e_per_token` (run, ÷18018) | `"0.000000516666665"` | **`"0.000000439512610"`** † |
| `energy_only_e_per_million` | `"0.516666665"` | **`"0.439512610"`** † |
| `cloud_comparison.compared_model` | `claude-opus-4-8` | unchanged |
| `cloud_comparison.input_price_per_million` / `output_…` | `"5"` / `"25"` | unchanged |
| `cloud_comparison.cloud_cost_same_tokens` | `"0.460000"` | **unchanged** (Step 3) |
| `cloud_comparison.blended_cloud_per_million` | `"23"` | **unchanged** (Step 3) |
| `cloud_comparison.output_share` | `"0.9"` | **unchanged** (Step 3) |
| `cloud_comparison.breakeven_tokens_per_day` | `"81237"` | **`"80803"`** (§5) |
| `cloud_comparison.breakeven_sensitivity_tokens_per_day` | `["64990","98818"]` | **`["64643","98177"]`** (§5) |
| `cloud_comparison.outcome` | `"crosses_at"` | unchanged |

Manifest header (D3): `as_of "2026-06-20"→"2026-06-25"`; `local.runtime_kind "ollama"→"llama.cpp"`;
`note`/`source` rewritten for the mixed dataset.

---

## 5. The break-even recomputation (D2) — engine formula, FINAL

```
hw_fixed_per_day = hardware_price ÷ depreciation_period_days = 2000 ÷ 1095 = 1.8264840…
V*               = hw_fixed_per_day ÷ (c − e)                      (c > e here; else NEVER)
e_be             = energy_only_cost ÷ 20000 = 0.00000039595691     (D2: 20,000-token workload)
sweep (== CLI build_sweep): electricity ±50% scales e; hardware ±20% scales capex;
                   output-mix ±0.2 re-blends c (share 0.9 → 0.7 and 1.1→clamp 1.0);
                   band = min/max of the round_dp(0) V* over headline + 6 points
```

**vs `claude-opus-4-8`** (`c = 23 $/M = 2.3e-5`; margin `2.260404e-5`):

| Sweep point | raw V* | rounded |
|---|---|---|
| headline | 80803.42 | **80803** |
| electricity −50% | 80101.85 | 80102 |
| electricity +50% | 81517.39 | 81517 |
| hardware −20% | 64642.74 | **64643** (band low) |
| hardware +20% | 96964.11 | 96964 |
| output mix −0.2 (share 0.7 → c 19 $/M) | 98176.72 | **98177** (band high) |
| output mix +0.2 (share 1.0 → c 25 $/M) | 74235.12 | 74235 |

→ manifest 31b `breakeven_tokens_per_day = "80803"`, `…sensitivity = ["64643", "98177"]`.

**vs `claude-sonnet-4-6`** (writeup row only; `c = 13.8 $/M = 1.38e-5`; margin `1.340404e-5`):
headline **136264** (raw 136263.66), band **[109011, 165983]** (hw −20% low 109010.93; output −0.2
high 165982.99). (elec −50% 134280, elec +50% 138306, hw +20% 163516, output +0.2 125067.)

All `c ≫ e` → every point is `CrossesAt` (no Never/Infeasible/Always; no machine ceiling supplied).
Independently re-derived from the committed raw row (§0.1).

---

## 6. Reproducibility / honesty consequences (the README + dataset-README fix)

1. **The 31b raw row is a captured measurement, not byte-reproducible.** `--measure` depends on the
   wall meter + GPU + the user's GGUF and varies run-to-run; the committed bytes are **one** run,
   pinned by `gemma-4-31b-dense.bench.json.sha256`. The dataset `README.md` regen loop (which
   currently regenerates all three estimated rows) must split: **26b/12b** stay the deterministic
   estimated loop; **31b** documents the §1 measured command + states it is a captured run (the
   sha256 sidecar is the integrity anchor, not byte-regeneration).
2. **The 31b break-even is not reproduced by `costroid breakeven`.** That CLI reads the unchanged
   bundled estimate (→ still prints `81237`). The dataset `README.md` + the writeup must note 31b's
   manifest break-even is computed from the **measured** `e` (96 W / 9.698 tok/s) via the engine
   formula (§5), and that the estimated CLI command reproduces the *estimated* number, not the
   committed measured one.
3. **State the cross-check identity** in the writeup/methodology note: `energy_only_cost = x_MeasuredWh
   ÷ 1000 × electricity_rate` (= `49.494614 ÷ 1000 × 0.16 = 0.0079191382`), so a reviewer can
   re-derive the measured marginal `e` from the raw row.

---

## 7. Per-file flip checklist (CLOSED — verified `breaks_on_flip` = ∅ over the committed tree)

**A. Allowlist flip (code) — lands together with the data (Step 1b).**
1. `crates/costroid-power/src/measured.rs` — add `("gemma-4-31b-dense",
   MeasurementMode::MeasuredWallmeter)` to `MEASURED_MODELS`; update the const/module docs ("empty
   until M3b" → "Phase 2: 31b measured"); **rewrite the `measured_models_is_empty_pre_m3b` test**
   (it asserts `is_empty()` + `measured_mode_for("gemma-4-31b-dense").is_none()` — both now false)
   to assert the new state (31b present as `MeasuredWallmeter`; a non-listed model is `None`). Use
   `matches!`/`==`/`assert!` — **no `unwrap`/`expect`/`panic!`** (golden rule, tests included).
2. `apps/cli/tests/post_m3b_refresh.rs` — add `("gemma-4-31b-dense", "measured_wallmeter")` to
   `MEASURED_MODELS_MIRROR`. (The `#[cfg(feature="power")]` tie + the provenance guard then *require*
   31b measured end-to-end and 26b/12b estimated — green only once the data flip lands.)

**B. Data (`benchmarks/gemma4-vs-cloud-2026-06/`).**
3. `manifest.v1.json` — apply §4 (31b run flip + header D3). Leave 26b/12b runs untouched.
4. `manifest.v1.json.sha256` — `(cd benchmarks/gemma4-vs-cloud-2026-06 && sha256sum manifest.v1.json > manifest.v1.json.sha256)`
5. `raw/gemma-4-31b-dense.bench.json` — replace with the §1 regenerated measured row (verbatim from
   the captured run).
6. `raw/gemma-4-31b-dense.bench.json.sha256` — `(cd …/raw && sha256sum gemma-4-31b-dense.bench.json > gemma-4-31b-dense.bench.json.sha256)`
7. `raw/gemma-4-26b-a4b.bench.json` (+ `.sha256`) — **UNCHANGED** (closed-checklist entry).
8. `raw/gemma-4-12b-unified.bench.json` (+ `.sha256`) — **UNCHANGED** (closed-checklist entry).
9. `README.md` — mixed-reality line (31b measured wall-meter/llama.cpp/Vulkan; 26b/12b estimated)
   + the §6 regen/break-even honesty split.

**C. Writeup + methodology.**
10. `docs/benchmark-gemma4-vs-cloud.md` — per-row labels: 31b rows → **measured** (energy table:
    `49.494614 Wh`, `$0.0079191382` energy-only, `$0.0392365975` HW, `$0.0471557357` total, 9.698
    tok/s, 96 W; energy-only `e` table ≈ `$0.44/M`; both break-even rows → opus 80803 [64643,98177],
    sonnet 136264 [109011,165983]; the headline ≈81k line → ≈80.8k [≈65k–98k]); 26b/12b rows stay
    *estimated — pending M3b measurement*; the canonical **stamp must remain present** (gate); note
    the 96 W / 9.698 tok/s / llama.cpp-Vulkan basis, the run-vs-breakeven token-basis note (D2/§6.3),
    and the energy-formula caption made per-row (96 W for 31b, 155 W for 26b/12b).
11. `docs/methodology.md` — **§1 only**: the inverse-guard sentence (line ~33) → "every committed
    **sample** row, and every **benchmark** row not on `MEASURED_MODELS`, is `estimated`". Keep all
    `docs_presence.rs` literals (`measured_wallmeter`, `0.000000516666665`, the stamp). §4 + the
    ladder table unchanged.

**D. Runbook fix.**
12. `docs/POST-M3B-REFRESH.md` — Step 0 D4 fix (rocm-smi → Vulkan/WSL method + backend relabel).
    Do **not** touch its `benchmarks/**` path enumerations (the drift-guard requires them verbatim).

> **After the doc edits, before the gate:** `grep -F "estimated — pending M3b measurement"` in both
> `docs/benchmark-gemma4-vs-cloud.md` and `docs/methodology.md` to confirm the canonical stamp still
> appears (so `check_doc_stamps.sh` stays green).

---

## 8. Provenance-guard sanity (why the order in §7 is safe)

`apps/cli/tests/post_m3b_refresh.rs::benchmark_dataset_provenance_matches_the_measured_models_allowlist`
checks: a model **on** the allowlist must be its declared measured mode in **both** the manifest run
and the raw row with `x_Estimated=false` and a real backing row; a model **off** it must be
`estimated` both places. So steps A (allowlist) and B3/B5 (manifest + raw) **must be committed
together** — doing either alone fails the guard. 26b/12b stay estimated → still pass. (Verified: an
empty mirror + measured data fails Rule 6; a measured mirror + estimated data fails run-provenance /
the `#[cfg(feature=power)]` const tie; a manifest/raw half-flip fails the JOIN.)

---

## 9. The deciding gate (run all; all must pass before boundary review)

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace                           # incl. provenance guard, methodology_crosscheck, docs_presence, samples_datasets
cargo test -p costroid --features power           # ties the mirror ↔ MEASURED_MODELS + the power tests
bash scripts/check_benchmarks.sh                 # sha256 of the two regenerated sidecars + the rest
bash scripts/check_doc_stamps.sh                 # stamp still present in the mixed docs
bash scripts/check_power_profiles.sh             # bundled profile/manifest unchanged (D3 skip)
```

---

## 10. Open questions for the reviewer

1. **D2 basis (the one real judgement call):** confirm the **Step-3-faithful split** (cloud
   comparison fixed at the 20,000/0.9 workload; `e_be = energy_only_cost ÷ 20000`; run fields over
   18,018), with the writeup documenting both bases — verified superior to option B?
2. **D3 header `runtime_kind`:** OK to set the dataset header `local.runtime_kind = "llama.cpp"`
   even though 26b/12b raw rows remain `ollama` (clarified by the rewritten `note`)? Keep
   `load_watts = "155"`?
3. **D5 README:** confirm **no README change** (or request the optional one-line measured-benchmark
   note)?
4. **`as_of`** = `2026-06-25` (today / the measurement date) acceptable for the manifest header?
5. **Precision (†):** OK to render the non-terminating `energy_only_e_per_token` /
   `energy_only_e_per_million` at the estimated-entry precision (15 dp / 9 sig figs), or prefer
   fewer significant figures for a measured quantity?
