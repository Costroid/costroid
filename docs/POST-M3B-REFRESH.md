# POST-M3B-REFRESH — the closed runbook for the real wall-meter numbers

Costroid ships **before** the M3b on-hardware measurement run, so every local-inference cost,
energy, and throughput figure is **estimated — pending M3b measurement** (R8/R10): derived from a
dated power profile + a community-analog throughput estimate, never a real captured joules/token.

This is the **closed, file-by-file** runbook the human follows **after** the wall-meter run lands —
exactly which files/figures to replace, how to regenerate each `.sha256`, what `as_of` to bump, the
`measurement_mode` flip (`estimated` → `measured_wallmeter`), and the integrity re-pass. A
**drift-guard test** (`apps/cli/tests/post_m3b_refresh.rs`) asserts this checklist enumerates
**exactly** the set of committed `benchmarks/**` artifacts — so a benchmark artifact added later
without updating this list **fails the build** (the deferral can never silently grow a loose end).

> **Scope.** M3b is the *measurement* run only. It does **not** change the FOCUS schema, the
> break-even math, or any CLI surface — it replaces estimated *values* with measured ones and flips
> the honesty stamps. Cost remains an estimate against the marginal energy floor; the provider
> invoice stays the ground truth.

---

## Step 0 — capture the real numbers (the wall-meter run)

Run the benchmark **measured** (the recommended source is a wall power meter — true total-system
draw, M3a `WallMeterPowerSampler`), recording the measured average watts and the real wall-clock
time per run:

```bash
EPOCH=1781913600   # keep the dated as_of pin so the row timestamp stays deterministic
SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
  bench --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 \
  --measure --wall-meter-watts <MEASURED_WATTS> --out json
```

A measured run stamps `x_MeasurementMode = "measured_wallmeter"` and clears `x_Estimated`. Record
the measured tok/s (so the per-run `wall_seconds` reflects the real machine, not the estimate).

---

## Step 1 — the bundled power data (the source of the estimates)

These are the dated assumptions every figure derives from. Bump their `as_of` to the measurement
date and replace the estimated values with measured ones, then regenerate each sidecar.

- **`crates/costroid-power/models/gemma4.v1.json`**
  - Replace each model's `estimated_tok_s` with the **measured** tok/s; set `tok_s_estimated: false`.
  - Bump the top-level `as_of` (and each `quality.as_of` if re-cited).
  - Regen the sidecar: `( cd crates/costroid-power/models && sha256sum gemma4.v1.json > gemma4.v1.json.sha256 )`
  - Update the Rust pin `GEMMA4_MANIFEST_AS_OF` (loader test).
- **`crates/costroid-power/profiles/hardware.v1.json`**
  - Replace `load_watts` (and `load_watts_range`) with the **measured** wall draw; set `estimated: false`.
  - Bump `as_of`; regen `( cd crates/costroid-power/profiles && sha256sum hardware.v1.json > hardware.v1.json.sha256 )`
  - Update the Rust pin `POWER_PROFILE_AS_OF`.
- **Integrity re-pass:** `bash scripts/check_power_profiles.sh`

---

## Step 2 — the versioned benchmark dataset (`benchmarks/**`)

For **each** committed benchmark artifact below: replace the listed figures/fields with the real
captured numbers, flip `measurement_mode`, bump `as_of`, regenerate the `.sha256`, and re-run the
integrity gate. (Prefer a new dated directory, e.g. `benchmarks/gemma4-vs-cloud-<YYYY-MM>/`, if you
want to preserve this estimated snapshot alongside the measured one — the drift-guard then also
requires the new artifacts to be added here.)

### `benchmarks/gemma4-vs-cloud-2026-06/manifest.v1.json`
- **Flip** every run's `measurement_mode`: `"estimated"` → `"measured_wallmeter"`.
- **Replace** per run: `estimated_tok_s` (→ measured, set `tok_s_estimated: false`), `energy_wh`,
  `avg_power_watts`, `local_run_cost`, `amortized_hw_cost`, `energy_only_cost`,
  `energy_only_e_per_token`, `energy_only_e_per_million`, and each `cloud_comparison`
  break-even volume + sensitivity band.
- **Bump** the top-level `as_of` (and `local.load_watts` if the profile changed).
- **Update** the header `note` to drop the "estimated — pending M3b" framing for the measured run.
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06 && sha256sum manifest.v1.json > manifest.v1.json.sha256 )`

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-31b-dense.bench.json`
- **Regenerate** from the measured run (Step 0) for `gemma-4-31b-dense`; the row will carry
  `x_MeasurementMode = "measured_wallmeter"`, `x_Estimated = false`, and the measured
  `x_MeasuredWh` / `x_AvgPowerWatts` / cost columns.
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06/raw && sha256sum gemma-4-31b-dense.bench.json > gemma-4-31b-dense.bench.json.sha256 )`

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-26b-a4b.bench.json`
- **Regenerate** from the measured run for `gemma-4-26b-a4b` (mode → `measured_wallmeter`).
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06/raw && sha256sum gemma-4-26b-a4b.bench.json > gemma-4-26b-a4b.bench.json.sha256 )`

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-12b-unified.bench.json`
- **Regenerate** from the measured run for `gemma-4-12b-unified` (mode → `measured_wallmeter`).
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06/raw && sha256sum gemma-4-12b-unified.bench.json > gemma-4-12b-unified.bench.json.sha256 )`

- **Integrity re-pass:** `bash scripts/check_benchmarks.sh`

> ⚠️ The inverse honesty guard (`apps/cli/tests/post_m3b_refresh.rs` + `samples_datasets.rs`)
> currently asserts every manifest run + every committed sample row is `measurement_mode ==
> "estimated"`. When you flip the benchmark runs to `"measured_wallmeter"`, **update those guards**
> to accept the measured mode (or scope them to the still-estimated samples) — the guards exist to
> stop a *premature* measured claim, not to block the real refresh.

---

## Step 3 — the writeup figures (`docs/benchmark-gemma4-vs-cloud.md`)

- **Replace** every hero figure (the per-model energy/cost/throughput tables, the energy-only `e`
  table, the break-even volume + sensitivity tables) with the measured numbers from Step 2.
- **Drop** the `estimated — pending M3b measurement` stamp wording for the now-measured figures
  (keep it on anything still estimated). Note: `scripts/check_doc_stamps.sh` requires the stamp
  only while a hero figure is present *and* unmeasured — once measured, update the writeup framing.
- The cloud-side dollars (catalog list prices) do **not** change from M3b; only the local side does.

---

## Step 4 — the demo / sample pack (only if you re-true it)

The T1 demo pack stays **estimated** by design (it ships with the tool, hardware-free). Refresh it
**only** if you decide the demo should show measured numbers:

- `samples/benchmark/gemma-4-31b-dense.bench.json` (+ `.sha256`)
- `samples/benchmark/gemma-4-26b-a4b.bench.json` (+ `.sha256`)
- `samples/benchmark/README.md` + `samples/README.md` (the stamp wording)
- `docs/methodology.md` §4 worked example (the pinned `e` value — also update the cross-check test
  `apps/cli/tests/methodology_crosscheck.rs`).

If you leave the demo estimated (recommended), make **no** change here — the drift-guard does not
require it.

---

## Step 5 — the full integrity re-pass (the deciding gate)

```bash
bash scripts/check_power_profiles.sh
bash scripts/check_benchmarks.sh
bash scripts/check_doc_stamps.sh
cargo test --workspace        # incl. the drift-guard + the (updated) measurement-mode guards
```

All green = the refresh is complete and the honesty stamps tell the truth again.
