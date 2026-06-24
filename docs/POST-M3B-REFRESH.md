# POST-M3B-REFRESH — the closed runbook for the real wall-meter numbers

Costroid ships **before** the M3b on-hardware measurement run, so every local-inference cost,
energy, and throughput figure is **estimated — pending M3b measurement** (R8/R10): derived from a
dated power profile + a community-analog throughput estimate, never a real captured joules/token.

The M3b refresh is a **per-model PARTIAL flip.** The human-decided run measures **one** model —
`gemma-4-31b-dense`, in WSL2 on the Strix Halo (gfx1151) via GPU-accelerated **llama.cpp** + a
display-only wall meter — and leaves the others estimated until they too are measured. The flip is
**provenance-anchored**: a benchmark row may claim a measured number **only** for a model on the
authoritative allowlist `costroid_power::MEASURED_MODELS` (and its test mirror). The drift-guard
[`apps/cli/tests/post_m3b_refresh.rs`](../apps/cli/tests/post_m3b_refresh.rs) enforces
**estimated-unless-allowlisted** in both directions (manifest run **and** the joined raw FOCUS row),
so a half-flipped or fabricated measured claim **fails the build**.

This is the **closed, file-by-file** runbook the human follows **after** the wall-meter run lands —
exactly which files/figures to replace, how to regenerate each `.sha256`, what `as_of` to bump, the
`measurement_mode` flip (`estimated` → `measured_wallmeter`), the **allowlist flip**, and the
integrity re-pass. A **drift-guard test** asserts this checklist enumerates **exactly** the set of
committed `benchmarks/**` artifacts — so a benchmark artifact added later without an entry here
**fails the build** (the deferral can never silently grow a loose end).

> **Scope.** M3b is the *measurement* run only. It does **not** change the FOCUS schema, the
> break-even math, or any CLI surface — it replaces estimated *values* with measured ones for the
> allowlisted model(s) and flips the honesty stamps. Cost remains an estimate against the marginal
> energy floor; the provider invoice stays the ground truth.

---

## Step 0 — capture the real numbers (the wall-meter run) — PROTOCOL

Run the benchmark **measured** for `gemma-4-31b-dense` (the recommended source is a wall power meter
— true total-system draw, M3a `WallMeterPowerSampler`). Follow this protocol so the captured figure
is honest and reproducible:

- **Warm-then-time, DECODE-ONLY.** Warm the model first (load weights + prefill), then time **only**
  the decode/generation window. The engine's `wall_seconds = tokens_out / tok_s` is decode-dominated
  (see [`methodology.md`](methodology.md) §4 + `crates/costroid-power/src/harness.rs`), so the
  measured tok/s **and** the avg watts must come from the decode phase, not the load/prefill.
- **Avg watts from the meter kWh-delta over that SAME window.** Read the wall meter's cumulative kWh
  immediately before and after the decode window; `avg_watts = Δkwh / window_hours`. Take **≥3 runs**
  and report the average **and** the spread (the band feeds the sensitivity, not a single hero
  number).
- **The run is `llama.cpp`, not the committed `ollama`.** The committed dataset records
  `runtime_kind: "ollama"`; the measured GPU run is **llama.cpp** (ROCm/HIP) — so **pass
  `--runtime llama.cpp` to the `bench` command below; the default is `ollama`**, and omitting it
  stamps the row `x_RuntimeKind = "ollama"`, contradicting the measured run. Update the manifest
  run's `runtime_kind` (→ `llama.cpp`) and `quant` if it differs; the regenerated raw row then
  carries `x_RuntimeKind` / `x_BenchmarkId` (`…/llama.cpp`) from the runner.
- **Size `.wslconfig` so 31b-Q4 weights + KV fit the ROCm GTT pool.** The 31B-dense `Q4_K_M` weights
  **plus** the KV cache for the 20,000-token window must fit the WSL2 GPU GTT allocation. Set the
  `.wslconfig` memory high enough (record the value you used) and the gfx1151 ROCm env so the iGPU,
  not the CPU, runs the decode.
- **HARD-abort unless `rocm-smi` shows the iGPU busy + weights resident and tok/s ≈ 10–13.** Before
  trusting a run, confirm `rocm-smi` reports GPU utilization + the weights resident in VRAM/GTT and a
  decode rate in the **~10–13 tok/s** GPU band. A silent **CPU fallback** (≈2–4 tok/s, no GPU
  residency) must **abort the run** — never record a CPU number as if it were the GPU.
- **Wall = total-system draw, idle-inclusive.** The wall meter reads the **whole machine** (PSU, RAM,
  fans, storage, conversion losses). **Record the idle draw** separately so the captured `load_watts`
  is understood as total-system-under-load, idle-inclusive (consistent with the profile's
  `load_watts` semantics + the package-vs-wall caveat, [`methodology.md`](methodology.md) §2).

```bash
EPOCH=1781913600   # KEEP the dated as_of pin — the row timestamp must stay byte-deterministic (D5)
SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
  bench --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 \
  --runtime llama.cpp \
  --measure --wall-meter-watts <MEASURED_WATTS> --out json
```

A measured run stamps `x_MeasurementMode = "measured_wallmeter"` and clears `x_Estimated`. Record the
measured tok/s (so the per-run `wall_seconds` reflects the real machine, not the estimate).

---

## Step 1 — the bundled power data — SKIP for a partial flip (D3)

For a **31b-only** partial flip: **SKIP this step.** The bundled
`crates/costroid-power/models/gemma4.v1.json` + `profiles/hardware.v1.json` stay **estimated** — the
demo/samples stay byte-stable, and the shared profile stays the default estimate. Re-truing the
shared profile is **out of scope** for a partial flip.

> **Optional — full re-true (only if you later measure *all* models AND decide to re-base the shared
> profile).** Then, and only then: in `gemma4.v1.json` replace each model's `estimated_tok_s` with the
> measured tok/s + set `tok_s_estimated: false`, bump `as_of`, regen
> `gemma4.v1.json.sha256`, and update the Rust pin `GEMMA4_MANIFEST_AS_OF`; in `hardware.v1.json`
> replace `load_watts` (+ `load_watts_range`) with the measured wall draw + set `estimated: false`,
> bump `as_of`, regen `hardware.v1.json.sha256`, and update `POWER_PROFILE_AS_OF`. Integrity re-pass:
> `bash scripts/check_power_profiles.sh`. **Do not do this for a single-model flip** — it would
> re-base the demo and every estimate off one model's number.

---

## Step 1b — the allowlist flip (the provenance gate)

This is what authorizes a measured benchmark row to ship — do it **together with** Step 2:

- Add the model to the authoritative const in
  [`crates/costroid-power/src/measured.rs`](../crates/costroid-power/src/measured.rs):
  ```rust
  pub const MEASURED_MODELS: &[(&str, MeasurementMode)] = &[
      ("gemma-4-31b-dense", MeasurementMode::MeasuredWallmeter),
  ];
  ```
  Then **update the `measured_models_is_empty_pre_m3b` test in that same file** — it asserts
  `MEASURED_MODELS.is_empty()` (and `measured_mode_for("gemma-4-31b-dense").is_none()`), both of
  which become false the moment you add the entry, so amend or remove it or `cargo test` goes red.
- Add the **matching** mirror entry in
  [`apps/cli/tests/post_m3b_refresh.rs`](../apps/cli/tests/post_m3b_refresh.rs):
  ```rust
  const MEASURED_MODELS_MIRROR: &[(&str, &str)] = &[
      ("gemma-4-31b-dense", "measured_wallmeter"),
  ];
  ```

The `#[cfg(feature = "power")]` tie in that test asserts the mirror equals the const, and CI runs
`cargo test -p costroid --features power`, so the two cannot drift. (This **replaces** the old
"update the inverse-estimated guards" note — the guard is now allowlist-anchored, so you *declare*
the measured model here instead of editing assertions. A measured row whose model is **not** added
here fails the build.)

---

## Step 2 — the versioned benchmark dataset (`benchmarks/**`) — flip only 31b

Flip **only** `gemma-4-31b-dense`; leave `gemma-4-26b-a4b` and `gemma-4-12b-unified` **estimated**
(they stay listed here so the checklist stays CLOSED). Prefer flipping in place; if you want to
preserve this estimated snapshot alongside the measured one, copy it into a new dated directory
(e.g. `benchmarks/gemma4-vs-cloud-<YYYY-MM>/`) first — the drift-guard then also requires the new
artifacts to be added to this checklist.

### `benchmarks/gemma4-vs-cloud-2026-06/manifest.v1.json`
- **Flip** the `gemma-4-31b-dense` run's `measurement_mode`: `"estimated"` → `"measured_wallmeter"`,
  set its `tok_s_estimated: false`, and change its `runtime_kind` `"ollama"` → `"llama.cpp"` (Step 0).
- **Replace** that run's `estimated_tok_s` (→ measured), `energy_wh`, `avg_power_watts`,
  `local_run_cost`, `amortized_hw_cost`, `energy_only_cost`, `energy_only_e_per_token`,
  `energy_only_e_per_million`, and its `cloud_comparison` break-even volume + sensitivity band.
- **Leave** the `gemma-4-26b-a4b` + `gemma-4-12b-unified` runs **unchanged** (`measurement_mode ==
  "estimated"`).
- **Update** the top-level header `note` + `source`: drop the "every run estimated — pending M3b"
  framing for the now-**mixed** dataset (31b measured via wall meter on llama.cpp; 26b/12b still
  estimated — pending M3b). Bump `as_of` to the measurement date.
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06 && sha256sum manifest.v1.json > manifest.v1.json.sha256 )`

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-31b-dense.bench.json`
- **Regenerate** from the measured run (Step 0, `SOURCE_DATE_EPOCH=1781913600` kept); the row will
  carry `x_MeasurementMode = "measured_wallmeter"`, `x_Estimated = false`, `x_RuntimeKind =
  "llama.cpp"`, and the measured `x_MeasuredWh` / `x_AvgPowerWatts` / cost columns.
- **Regen sidecar:** `( cd benchmarks/gemma4-vs-cloud-2026-06/raw && sha256sum gemma-4-31b-dense.bench.json > gemma-4-31b-dense.bench.json.sha256 )`

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-26b-a4b.bench.json`
- **UNCHANGED** for a 31b-only flip (stays `measurement_mode == "estimated"`). Listed to keep the
  checklist closed. *(Regenerate + flip only if you also measure this model and add it to the
  allowlist.)*

### `benchmarks/gemma4-vs-cloud-2026-06/raw/gemma-4-12b-unified.bench.json`
- **UNCHANGED** for a 31b-only flip (stays `measurement_mode == "estimated"`). Listed to keep the
  checklist closed. *(Regenerate + flip only if you also measure this model and add it to the
  allowlist.)*

### `benchmarks/gemma4-vs-cloud-2026-06/README.md`
- Rewrite the "**every run carries `measurement_mode == "estimated"`**" line to the **mixed** reality
  (31b measured via wall meter; 26b/12b estimated — pending M3b). *(Not `.sha256`-pinned, so a free
  edit — but still a surface you must update so the dataset README doesn't lie.)*

- **Integrity re-pass:** `bash scripts/check_benchmarks.sh`

---

## Step 3 — the writeup figures (`docs/benchmark-gemma4-vs-cloud.md`) — PER-ROW labels

The hero tables are now **mixed**, and `scripts/check_doc_stamps.sh` is **presence-only** — it asserts
the canonical stamp appears *somewhere* in a doc that shows a hero figure; it **cannot** tell which
row is measured vs estimated. So the **per-row** labelling is your responsibility:

- **Label the `gemma-4-31b-dense` rows measured** (wall meter, dated) — replace its energy/cost/tok-s
  hero figures, its energy-only `e`, and its break-even volume + band with the measured numbers from
  Step 2.
- **Keep the `gemma-4-12b-unified` + `gemma-4-26b-a4b` rows `estimated — pending M3b measurement`**
  (unchanged), so the canonical stamp still legitimately appears at least once and the doc-stamp gate
  still passes.
- The cloud-side dollars (catalog list prices) do **not** change from M3b; only the local side does.
- **Also update `docs/methodology.md` §1** — the inverse-guard sentence currently reads "every
  committed sample/benchmark row is `x_MeasurementMode == "estimated"`". After a partial flip make it
  honest about the mixed state, e.g. "every committed **sample** row, and every **benchmark** row not
  on `MEASURED_MODELS`, is `estimated`" (methodology.md is scanned by the doc-stamp gate, so keep the
  canonical stamp present there too).

---

## Step 4 — the demo / sample pack — UNCHANGED (D4)

The T1 demo pack stays **estimated** by design (it ships with the tool, hardware-free) and the
drift-guard does **not** require it to change. Make **no** change here for a partial flip:
`samples/benchmark/*.bench.json` (+ `.sha256`), `samples/README.md`,
`samples/benchmark/README.md`, and the `docs/methodology.md` §4 worked example / its cross-check test
`apps/cli/tests/methodology_crosscheck.rs` all stay estimated. (Only re-true the demo if you
deliberately decide it should show measured numbers — a separate, optional choice.)

---

## Step 5 — the full integrity re-pass (the deciding gate)

```bash
bash scripts/check_power_profiles.sh
bash scripts/check_benchmarks.sh
bash scripts/check_doc_stamps.sh
cargo test --workspace                       # incl. the drift-guard + the allowlist provenance guard
cargo test -p costroid --features power       # ties MEASURED_MODELS_MIRROR ↔ costroid_power::MEASURED_MODELS
```

With `gemma-4-31b-dense` added to `MEASURED_MODELS` (+ the mirror), the provenance guard now
**requires** 31b to be `measured_wallmeter` end-to-end (manifest run + raw row + `x_Estimated =
false`, backed by a real raw row) and **still requires** 26b/12b to be `estimated`. All green = the
partial refresh is complete and the honesty stamps tell the truth again.
