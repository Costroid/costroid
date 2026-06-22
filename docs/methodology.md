# Costroid — methodology (how energy, tokens, and break-even are derived)

This page is the **technical methodology** behind Costroid's local-inference economics: exactly how a
cost-per-token and a local-vs-cloud break-even are computed, what is **measured** vs **estimated**, and
why. It is the companion to [`limitations.md`](limitations.md) (what Costroid *cannot* know) and
[`ARCHITECTURE.md`](ARCHITECTURE.md) (where the code lives). When this doc disagrees with the code,
**the code wins** — every formula below is the one the engine runs.

> **Honesty stamp (R8/R10).** Costroid ships **before** the M3b on-hardware measurement run, so every
> local-inference cost / energy / throughput figure in this document is **estimated — pending M3b
> measurement** — derived from a dated, stamped, overridable power profile and a community-analog
> throughput estimate, never a real captured number. The
> [`docs/POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md) checklist flips those figures to "measured" once the
> wall-meter run lands. Cost is **always an estimate** — design for reconciliation against the provider
> invoice, which is the ground truth.

## 1. Measured vs estimated — the measurement-mode ladder

Every `local_inference` FOCUS row carries an `x_MeasurementMode` stamp recording **how the energy was
obtained**. The mode ladder (`crates/costroid-power/src/mode.rs` + `sampler.rs`) is, from most to least
honest:

| `x_MeasurementMode` | Source | Honesty |
|---|---|---|
| `measured_wallmeter` | A wall power meter on the whole machine — **true total-system draw** | **Most honest** — the measured ladder *leads* with it |
| `measured_sysfs` | Linux `power1_average` sysfs (on-chip, whole-APU **package** power) | Package-grade convenience (needs native Linux) |
| `measured_lhm` | Windows LibreHardwareMonitor "Package" sensor (on-chip, whole-APU **package** power) | Package-grade convenience |
| `estimated` | A dated, stamped, overridable power **profile** (no hardware) — **the default** | An assumption, never a reading |

`x_Estimated` is cleared (`false`) **only** for the three `measured_*` modes; an `estimated` row always
carries `x_Estimated = true`. By default `costroid bench` runs in **`estimated`** mode (no hardware
required), so everything ships and demos with zero hardware — and the **inverse honesty guard** asserts
that every committed sample/benchmark row is `x_MeasurementMode == "estimated"` (no shipped artifact may
claim a measured number before M3b).

## 2. Package power vs wall power (the ~20–40% caveat)

The target hardware (AMD Strix Halo, Radeon 8060S iGPU / gfx1151) is an **APU**: the iGPU shares a power
rail with the CPU, so **no on-chip source can isolate GPU-only watts**. Every on-chip reading — Linux
`power1_average` sysfs **and** the Windows LibreHardwareMonitor "Package" sensor alike — is **whole-APU
package power** (it overlaps the CPU and is time-averaged), **not** GPU-only.

A **wall meter** measures **true total-system draw** (the PSU, RAM, fans, storage, and conversion losses
on top of the package), which is typically **~20–40% higher** than the on-chip package figure. Because
the wall meter is the most honest number, the measured ladder **leads with it** (`measured_wallmeter`),
and the on-chip readers are the optional package-grade convenience tiers below it. The package-vs-wall
gap is why an estimated run uses a profile `load_watts` calibrated to **system** draw, not a package
sensor reading.

## 3. The per-run energy/cost model (§3.2)

For one local run, the engine (`costroid-power`) computes:

- **Energy (Wh):** `x_MeasuredWh = avg_power_watts × wall_seconds / 3600`, where in `estimated` mode
  `avg_power_watts` is the profile `load_watts` (default **155 W** for `strix-halo-128gb`) and
  `wall_seconds` is derived from the **generated (output) token count ÷ the model's estimated
  throughput** (decode time dominates — `tokens_out / estimated_tok_s`, matching the engine). In a
  `measured_*` mode `avg_power_watts` is the sampled draw.
- **Energy cost (USD):** `energy_wh / 1000 × electricity_rate`, the dated electricity rate (default
  **0.16 USD/kWh**, a `global-household-average-template`).
- **Amortized hardware cost (USD):** `x_AmortizedHwCost = hardware_price × wall_seconds /
  hardware_lifetime_seconds` — the per-run share of the calendar-fixed capex.
- **Effective cost (USD):** `EffectiveCost = energy_cost + amortized_hw_cost`, stamped on the FOCUS row.

The dated, stamped, **overridable** assumptions (R8) live in
`crates/costroid-power/profiles/hardware.v1.json`: `electricity_rate.value = 0.16 USD/kWh`,
`hardware_price = 2000 USD`, `load_watts = 155 W`, `hardware_lifetime_seconds = 94608000` (3 years). Set
your own via `--electricity-rate` / the `[power]` config; the winning profile id rides `x_HardwareProfile`
as `"{id}@{as_of}"` (e.g. `strix-halo-128gb@2026-06-20`).

## 4. The energy-only marginal rate `e` over **total (in+out) tokens** (the M5 lock)

The break-even **marginal** local rate `e` (USD/token) is the energy-only cost — it **excludes the
amortized capex**, because the capex is the break-even **fixed** term (`hw_fixed_per_day`); folding it
into `e` as well would double-count it. The public helper is
`costroid_core::local_energy_only_rate(rows) -> Result<Option<Decimal>, CoreError>`
(`crates/costroid-core/src/breakeven.rs`):

```
e = Σ (EffectiveCost − x_AmortizedHwCost)  ÷  Σ x_ConsumedTokens
```

over the `local_inference` rows, where `x_ConsumedTokens = tokens_in + tokens_out` — the **TOTAL-token
basis** (the M5 lock: `local_run_to_focus` stamps `in + out`, so the CLI's live `e` and the server's
stored `e` share one basis). It is **never** `EffectiveCost / tokens` (that double-counts the capex).
Fail-closed: a local row with a null `x_AmortizedHwCost` is a typed error (never silently treated as 0);
no local rows at all is an honest `Ok(None)` (never a fabricated `e = 0`); the rate is returned at full
precision (never rounded).

### Worked example (pinned to the committed `gemma-4-31b-dense` benchmark row)

The committed `samples/benchmark/gemma-4-31b-dense.bench.json` is one estimated `local_inference` row for
a 2,000-in + 18,000-out = **20,000-token** run:

| Field | Value |
|---|---|
| `EffectiveCost` | `0.0420431253` USD |
| `x_AmortizedHwCost` | `0.031709792` USD |
| `x_ConsumedTokens` | `20000` |

Energy-only cost = `0.0420431253 − 0.031709792` = **`0.0103333333`** USD.

```
e = 0.0103333333 ÷ 20000 = 0.000000516666665 USD/token   (estimated — pending M3b measurement)
```

So the marginal energy cost of this estimated 31B-dense run is **≈ $0.00000052 per token**, or about
**$0.52 per million tokens** — the **energy floor**, with the hardware capex tracked separately as the
break-even fixed term. This exact value (`0.000000516666665`) is pinned by the `e`-formula cross-check
test (`apps/cli/tests/methodology_crosscheck.rs`), which runs `local_energy_only_rate` on a fixture built
from these very numbers and asserts the result equals the figure printed here — so this worked example
can never silently drift from the engine.

## 5. Break-even math (calendar-fixed amortization, a band, and the "never"/infeasible case)

The local-vs-cloud crossover is **pure compute in `costroid-core::breakeven`** (no `core → power` edge).
The hardware is amortized as a **calendar-fixed** capex (utilization-independent):

```
hw_fixed_per_day = hardware_price ÷ depreciation_period_days
V*               = hw_fixed_per_day ÷ (c − e)      tokens/day to break even
```

where `e` is the energy-only marginal rate from §4 and `c` is the blended cloud per-token rate
(`blended_cloud_per_token` = input-rate × input-share + output-rate × output-share, from the M2 layered
pricing catalog with its `source@as_of#hash8` stamp). The outcome is one of:

- a **finite `V*`** — "local breaks even at N tokens/day";
- **`Never`** — `c ≤ e` (the cloud per-token rate is at or below the local energy floor, so the capex is
  never recovered) — reported honestly with the reason, never a fabricated number;
- **`Always`** — zero capex;
- **`Infeasible`** — `V*` exceeds the machine's tokens/day ceiling (`estimated_tok_s × utilization ×
  86400`), i.e. the hardware physically cannot push enough tokens to break even.

Because the inputs are estimates, the result is **never a single hero number**: `breakeven_report`
returns the headline **plus a sensitivity band** (the crossover recomputed across a range of the
uncertain inputs) **plus a full `AssumptionStamp`** (R6/R8: the electricity rate, hardware price,
depreciation period, utilization, output share, `e`, `c`, the measurement mode, the dated hardware
profile, and the pricing-snapshot id). The dated **DeepSWE-Bench `$/task`** overlay is shown as a
**labeled reference only** — never part of the crossover math. The **one-break-even-lifetime rule** is
enforced: `depreciation_period` is the amortization basis; `hardware_lifetime_seconds` is per-run only
(mixing them is a typed error). Every `NEVER` / `INFEASIBLE` verdict carries a textual cue (never
color-alone).

## 6. Cross-references

- What Costroid **cannot** know (sub-agent attribution, package-vs-wall, the uncertain-row annotation,
  the M4/M5 interface caveats): [`limitations.md`](limitations.md).
- Where the code lives (the crate graph, the `costroid-power` engine, the loopback server data path, the
  offline model): [`ARCHITECTURE.md`](ARCHITECTURE.md).
- The post-M3b figure refresh: [`docs/POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md) *(landed with the
  benchmark dataset, T8)*.
