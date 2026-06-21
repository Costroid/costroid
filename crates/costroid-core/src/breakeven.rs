//! M4 — the pure local-vs-cloud break-even crossover math (calendar-fixed amortization).
//!
//! Given the local **energy-only** marginal rate `e` (USD/token — `energy_cost / total_tokens`,
//! NEVER the `local_cost_per_1M`-derived total, which already folds in the amortized hardware and
//! would double-count the capex), the hardware purchase price amortized as a **calendar-fixed**
//! daily cost `hw_fixed_per_day = capex / depreciation_period_days` (utilization-independent — the
//! box depreciates whether or not it runs), and the blended cloud per-token price `c` (USD/token,
//! at the workload's input/output mix), the daily costs are
//!
//! ```text
//! local_per_day(V) = hw_fixed_per_day + e * V
//! cloud_per_day(V) = c * V
//! ```
//!
//! so local ≤ cloud at the daily token volume
//!
//! ```text
//! V* = hw_fixed_per_day / (c - e)     when c > e
//!    = NEVER                          when c <= e   (capex never recovered at any volume)
//! ```
//!
//! This is **pure `Decimal` arithmetic** — no power, no network, no pricing catalog — so it is
//! unit-testable on synthetic inputs with neither `costroid-power` nor the pricing catalog linked
//! (the local scalar enters as an input; `costroid-core` keeps no `costroid-power` edge). The
//! per-run `amortized_hw_cost` of §3.2 is a *different* attribution (the per-row `x_AmortizedHwCost`
//! FOCUS charge, linear in run-seconds → no crossover) and is **not** used here. See
//! `docs/M4-PLAN.md` (D1) and `docs/COSTROID-NEXT.md` §3.2.

use rust_decimal::Decimal;

use crate::vendor_report::UsdAmount;
use crate::CoreError;

/// The number of days in a token-rate's denominator is per-day; `86_400` seconds per day is the
/// conversion used when a lifetime is supplied in seconds rather than days.
const SECONDS_PER_DAY: i64 = 86_400;

/// The resolved, validated inputs to one break-even computation. All money is exact `Decimal` /
/// [`UsdAmount`] — never `f64`. The local rate is **energy-only** and **full-precision** (the
/// caller converts it via `Decimal::from_f64_retain` on `energy_cost`, never `round_dp`).
#[derive(Debug, Clone, PartialEq)]
pub struct BreakevenInputs {
    /// `e` — the local **energy-only** marginal cost, USD per token. MUST exclude amortized
    /// hardware (that is the fixed term `hw_fixed_per_day`); folding it in double-counts the capex.
    pub local_energy_per_token: Decimal,
    /// The hardware purchase price (capex) amortized over the depreciation calendar.
    pub hardware_capex: UsdAmount,
    /// The break-even depreciation period in **days** (the calendar amortization basis). Resolve
    /// it from the scenario knobs via [`resolve_depreciation_days`] so the one-lifetime rule holds.
    pub depreciation_period_days: Decimal,
    /// `c` — the blended cloud price, USD per token, at the workload's input/output mix (the value
    /// produced by the T3 blend helper). The deterministic crossover compares against this.
    pub cloud_per_token: Decimal,
    /// The machine's feasibility ceiling: the maximum tokens it can produce per day
    /// (`estimated_tok_s · utilization · 86_400`, computed by the caller — no `core→power` edge).
    /// `None` means "no ceiling check" (the crossover is reported even if unreachable).
    pub max_tokens_per_day: Option<Decimal>,
}

/// The outcome of a break-even computation. The crossover is a **daily token volume**, never a
/// single hero dollar figure (the sensitivity range + the assumption stamp ride on the
/// `BreakevenReport` that wraps this — T4).
#[derive(Debug, Clone, PartialEq)]
pub enum BreakevenOutcome {
    /// Local is cheaper for daily volumes at or above `tokens_per_day` (the crossover `V*`).
    CrossesAt { tokens_per_day: Decimal },
    /// Local is cheaper at every positive volume — zero fixed hardware cost and `c > e`.
    Always,
    /// Local can **never** beat cloud at any volume: the cloud per-token price is at or below the
    /// local marginal energy rate (`c ≤ e`), so the hardware capex is never recovered.
    Never { reason: String },
    /// A real crossover exists at `v_star` but it exceeds what the machine can produce per day
    /// (`max_tokens_per_day`), so the break-even is unreachable on this hardware.
    Infeasible {
        v_star: Decimal,
        max_tokens_per_day: Decimal,
        reason: String,
    },
}

/// Resolve the **single** break-even depreciation period, in days (MED3).
///
/// The break-even basis is the `depreciation_period` (here, days); the per-run
/// `hardware_lifetime_seconds` belongs to the §3.2 / `x_AmortizedHwCost` attribution **only** and
/// must never double as the break-even basis. Supplying both is a typed error (ambiguous); so is
/// supplying neither (break-even needs a calendar). A non-positive period is a typed error.
pub fn resolve_depreciation_days(
    depreciation_period_days: Option<Decimal>,
    hardware_lifetime_seconds_as_basis: Option<Decimal>,
) -> Result<Decimal, CoreError> {
    let days = match (depreciation_period_days, hardware_lifetime_seconds_as_basis) {
        (Some(_), Some(_)) => {
            return Err(CoreError::Breakeven(
                "two lifetimes given for the break-even basis: `depreciation_period` is the basis; \
                 `hardware_lifetime_seconds` is the per-run x_AmortizedHwCost attribution only — \
                 set exactly one"
                    .to_string(),
            ));
        }
        (Some(days), None) => days,
        (None, Some(seconds)) => {
            return Err(CoreError::Breakeven(format!(
                "`hardware_lifetime_seconds` ({seconds}) is the per-run x_AmortizedHwCost \
                 attribution, not the break-even basis — set `depreciation_period` instead"
            )));
        }
        (None, None) => {
            return Err(CoreError::Breakeven(
                "break-even needs a `depreciation_period` (the calendar amortization basis)"
                    .to_string(),
            ));
        }
    };
    if days <= Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "depreciation_period must be positive, got {days} days"
        )));
    }
    Ok(days)
}

/// Convert a lifetime expressed in **seconds** to days (exact `Decimal` division by `86_400`).
/// A convenience for callers that hold a `hardware_lifetime_seconds`-shaped period and want to use
/// it as the *break-even* basis explicitly (e.g. a config that only carries seconds).
pub fn days_from_seconds(seconds: Decimal) -> Result<Decimal, CoreError> {
    seconds
        .checked_div(Decimal::from(SECONDS_PER_DAY))
        .ok_or_else(|| CoreError::Breakeven(format!("lifetime {seconds}s is out of range")))
}

/// Compute the calendar-fixed break-even crossover. Pure: typed errors, never a panic.
///
/// `inputs.depreciation_period_days` is expected to come from [`resolve_depreciation_days`] (the
/// one-lifetime rule), but every input is re-validated here, so a caller may also pass a raw
/// period safely. Note `CrossesAt.tokens_per_day` may be a *truncated* (non-exact) `Decimal` when
/// the division does not terminate — downstream display rounds it for presentation rather than
/// treating it as exact.
pub fn breakeven(inputs: &BreakevenInputs) -> Result<BreakevenOutcome, CoreError> {
    let e = inputs.local_energy_per_token;
    let c = inputs.cloud_per_token;
    let capex = inputs.hardware_capex.as_usd();
    let days = inputs.depreciation_period_days;

    // Validate (R6 honesty): a non-physical input is a typed error, never a silent wrong answer.
    // (`Decimal` is always finite — no NaN/inf guard needed, unlike the `f64` profile path.)
    if days <= Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "depreciation_period must be positive, got {days} days"
        )));
    }
    if e < Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "local energy rate must be non-negative, got {e}/token"
        )));
    }
    if c < Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "cloud rate must be non-negative, got {c}/token"
        )));
    }
    if capex < Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "hardware capex must be non-negative, got {capex}"
        )));
    }
    if let Some(max) = inputs.max_tokens_per_day {
        if max <= Decimal::ZERO {
            return Err(CoreError::Breakeven(format!(
                "max_tokens_per_day must be positive when set, got {max}"
            )));
        }
    }

    // The cloud's per-token advantage. `Never` exactly when it is non-positive. Uses
    // `checked_sub` (not `-`) so it can never panic even if a future refactor reorders or drops
    // the non-negativity guards above (`rust_decimal`'s `Sub` panics on overflow).
    let margin = c
        .checked_sub(e)
        .ok_or_else(|| CoreError::Breakeven("cloud-vs-local margin overflowed".to_string()))?;
    if margin <= Decimal::ZERO {
        return Ok(BreakevenOutcome::Never {
            reason: format!(
                "cloud {c}/token <= local energy {e}/token — local never pays back its hardware at \
                 any volume"
            ),
        });
    }

    // c > e from here. Amortize the hardware as a calendar-fixed daily cost (D1).
    let hw_fixed_per_day = capex
        .checked_div(days)
        .ok_or_else(|| CoreError::Breakeven("amortized hardware cost overflowed".to_string()))?;

    if hw_fixed_per_day.is_zero() {
        // Zero fixed cost and a positive margin → local wins at every positive volume.
        return Ok(BreakevenOutcome::Always);
    }

    let v_star = hw_fixed_per_day
        .checked_div(margin)
        .ok_or_else(|| CoreError::Breakeven("break-even volume overflowed".to_string()))?;

    if let Some(max) = inputs.max_tokens_per_day {
        if v_star > max {
            return Ok(BreakevenOutcome::Infeasible {
                v_star,
                max_tokens_per_day: max,
                reason: format!(
                    "break-even needs {v_star} tokens/day but the machine tops out at {max} \
                     tokens/day — unreachable on this hardware"
                ),
            });
        }
    }

    Ok(BreakevenOutcome::CrossesAt {
        tokens_per_day: v_star,
    })
}

/// Blend the per-meter cloud prices into the scenario's single cloud per-token rate `c` (T3/D3):
///
/// ```text
/// c = input_per_token * (1 - output_share) + output_per_token * output_share
/// ```
///
/// `output_share` is the fraction of tokens that are **output**, in `[0, 1]` (outside → typed
/// error). This is the value the T1 [`breakeven`] crossover consumes as `cloud_per_token`. Pure
/// `Decimal` arithmetic; splitting it out keeps [`crate::cloud_price_per_token`] strictly per-meter
/// and gives the mix weighting its own test surface. Per-token prices must be non-negative.
pub fn blended_cloud_per_token(
    input_per_token: Decimal,
    output_per_token: Decimal,
    output_share: Decimal,
) -> Result<Decimal, CoreError> {
    if output_share < Decimal::ZERO || output_share > Decimal::ONE {
        return Err(CoreError::Breakeven(format!(
            "output_share must be in [0, 1], got {output_share}"
        )));
    }
    if input_per_token < Decimal::ZERO || output_per_token < Decimal::ZERO {
        return Err(CoreError::Breakeven(format!(
            "per-token cloud prices must be non-negative, got input {input_per_token} / output {output_per_token}"
        )));
    }
    // `1 - output_share` ∈ [0, 1]; checked arithmetic so a pathological pair never panics.
    let input_share = Decimal::ONE
        .checked_sub(output_share)
        .ok_or_else(|| CoreError::Breakeven("input share underflowed".to_string()))?;
    let input_term = input_per_token
        .checked_mul(input_share)
        .ok_or_else(|| CoreError::Breakeven("blended input term overflowed".to_string()))?;
    let output_term = output_per_token
        .checked_mul(output_share)
        .ok_or_else(|| CoreError::Breakeven("blended output term overflowed".to_string()))?;
    input_term
        .checked_add(output_term)
        .ok_or_else(|| CoreError::Breakeven("blended cloud rate overflowed".to_string()))
}

/// The full assumption stamp (R6/R8): exactly what a break-even comparison assumed, so the result
/// is a methodology + a range, never an unexplained hero number. Carries the dated provenance the
/// schema already records elsewhere (`x_HardwareProfile`, `x_PricingSnapshotId`, the measurement
/// mode), reused here rather than reinvented.
#[derive(Debug, Clone, PartialEq)]
pub struct AssumptionStamp {
    pub electricity_rate_per_kwh: Decimal,
    pub hardware_price: UsdAmount,
    pub depreciation_period_days: Decimal,
    /// The utilization fraction assumed (0..=1).
    pub utilization: Decimal,
    /// The output-token share assumed for the cloud blend (0..=1).
    pub output_share: Decimal,
    /// `e` — the energy-only local marginal rate, USD/token (full precision).
    pub local_energy_per_token: Decimal,
    /// `c` — the blended cloud rate, USD/token, at `output_share`.
    pub blended_cloud_per_token: Decimal,
    /// The power measurement mode (`estimated` / `measured_*`).
    pub measurement_mode: String,
    /// The dated hardware-profile id, `"{id}@{as_of}"`.
    pub hardware_profile: String,
    /// The winning pricing tier's R8 stamp, `"{source}@{as_of}#{hash8}"`.
    pub pricing_snapshot_id: String,
    /// The Costroid version that produced the comparison.
    pub collector_version: String,
}

/// A clearly-labeled, dated DeepSWE-Bench `$/task` reference point (T5/D3). This is an empirical
/// cloud-side OVERLAY — informational context shown beside the verdict — and is **never** part of
/// the deterministic per-token crossover math.
#[derive(Debug, Clone, PartialEq)]
pub struct CloudReferencePoint {
    pub benchmark: String,
    pub model: String,
    /// The benchmark's own reported avg `$/task`; `None` → "n/a" (never zero, never a guess).
    pub dollars_per_task: Option<Decimal>,
    pub as_of: String,
    pub source: String,
}

/// One labeled alternative input set for the sensitivity sweep (e.g. the low/high extreme of a
/// swept assumption). The caller builds these — it knows which knobs it varied and by how much.
#[derive(Debug, Clone, PartialEq)]
pub struct SweepPoint {
    pub label: String,
    pub inputs: BreakevenInputs,
}

/// The break-even outcome at one swept scenario point.
#[derive(Debug, Clone, PartialEq)]
pub struct SensitivityPoint {
    pub label: String,
    pub outcome: BreakevenOutcome,
}

/// The summarized sensitivity band over the headline + all swept points (R6 — a range, not a hero
/// number). `low`/`high` are the smallest/largest *feasible* crossover volumes seen; the flags
/// record when some point can never break even, is unreachable on the hardware, or is always
/// cheaper (a degenerate crossover at 0).
#[derive(Debug, Clone, PartialEq)]
pub struct BreakevenBand {
    pub low: Option<Decimal>,
    pub high: Option<Decimal>,
    pub has_always: bool,
    pub has_never: bool,
    pub has_infeasible: bool,
}

/// The full M4 break-even result (T4/D4): the headline outcome at the expected inputs, the
/// sensitivity sweep, the assumption stamp, and the labeled DeepSWE-Bench overlay (T5). Emitted as
/// its own structure — a break-even is a comparison, not a FOCUS charge row.
#[derive(Debug, Clone, PartialEq)]
pub struct BreakevenReport {
    pub headline: BreakevenOutcome,
    pub sensitivity: Vec<SensitivityPoint>,
    pub stamp: AssumptionStamp,
    pub cloud_reference: Vec<CloudReferencePoint>,
}

impl BreakevenReport {
    /// Summarize the headline + every swept point into a [`BreakevenBand`] (R6). `Always` counts
    /// as a feasible crossover at volume 0 (cheaper from the first token).
    pub fn band(&self) -> BreakevenBand {
        let mut band = BreakevenBand {
            low: None,
            high: None,
            has_always: false,
            has_never: false,
            has_infeasible: false,
        };
        let outcomes =
            std::iter::once(&self.headline).chain(self.sensitivity.iter().map(|p| &p.outcome));
        for outcome in outcomes {
            match outcome {
                BreakevenOutcome::CrossesAt { tokens_per_day } => {
                    fold_band(&mut band, *tokens_per_day);
                }
                BreakevenOutcome::Always => {
                    band.has_always = true;
                    fold_band(&mut band, Decimal::ZERO);
                }
                BreakevenOutcome::Never { .. } => band.has_never = true,
                BreakevenOutcome::Infeasible { .. } => band.has_infeasible = true,
            }
        }
        band
    }
}

/// Fold a feasible crossover volume into the band's low/high extremes.
fn fold_band(band: &mut BreakevenBand, v: Decimal) {
    band.low = Some(band.low.map_or(v, |low| low.min(v)));
    band.high = Some(band.high.map_or(v, |high| high.max(v)));
}

/// Assemble the full break-even report (T4): compute the headline outcome and the outcome at every
/// swept point, attaching the assumption stamp + the labeled cloud reference. A swept point with a
/// non-physical input is a typed error (the caller built the sweep); `Never`/`Infeasible` are
/// honest outcomes, never dropped.
pub fn breakeven_report(
    base: BreakevenInputs,
    sweep: Vec<SweepPoint>,
    stamp: AssumptionStamp,
    cloud_reference: Vec<CloudReferencePoint>,
) -> Result<BreakevenReport, CoreError> {
    let headline = breakeven(&base)?;
    let mut sensitivity = Vec::with_capacity(sweep.len());
    for point in sweep {
        let outcome = breakeven(&point.inputs)?;
        sensitivity.push(SensitivityPoint {
            label: point.label,
            outcome,
        });
    }
    Ok(BreakevenReport {
        headline,
        sensitivity,
        stamp,
        cloud_reference,
    })
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    /// `n × 10^-scale` as an exact `Decimal` (e.g. `dec(5, 7)` = 0.0000005).
    fn dec(mantissa: i64, scale: u32) -> Decimal {
        Decimal::new(mantissa, scale)
    }

    /// Numeric equality independent of trailing-zero scale.
    fn same(a: Decimal, b: Decimal) -> bool {
        a.normalize() == b.normalize()
    }

    #[test]
    fn blend_endpoints_are_input_only_and_output_only() {
        let input = dec(3, 6); // $0.000003/token
        let output = dec(15, 6); // $0.000015/token
        let Ok(at_zero) = blended_cloud_per_token(input, output, Decimal::ZERO) else {
            panic!("share 0 must blend");
        };
        assert!(same(at_zero, input), "output_share 0 → input-only");
        let Ok(at_one) = blended_cloud_per_token(input, output, Decimal::ONE) else {
            panic!("share 1 must blend");
        };
        assert!(same(at_one, output), "output_share 1 → output-only");
    }

    #[test]
    fn blend_intermediate_mix_is_exact_to_the_cent() {
        // input 0.000003, output 0.000015, share 0.25 →
        // 0.000003·0.75 + 0.000015·0.25 = 0.00000225 + 0.00000375 = 0.000006.
        let Ok(c) = blended_cloud_per_token(dec(3, 6), dec(15, 6), dec(25, 2)) else {
            panic!("intermediate mix must blend");
        };
        assert!(
            same(c, dec(6, 6)),
            "blended c must be exactly 0.000006, got {c}"
        );
    }

    #[test]
    fn blend_rejects_out_of_range_share_and_negative_prices() {
        for bad_share in [dec(-1, 1), dec(11, 1)] {
            // -0.1 and 1.1
            assert!(matches!(
                blended_cloud_per_token(dec(3, 6), dec(15, 6), bad_share),
                Err(CoreError::Breakeven(_))
            ));
        }
        // Both halves of the OR guard: a negative INPUT price and a negative OUTPUT price.
        assert!(matches!(
            blended_cloud_per_token(dec(-1, 6), dec(15, 6), dec(25, 2)),
            Err(CoreError::Breakeven(_))
        ));
        assert!(matches!(
            blended_cloud_per_token(dec(3, 6), dec(-1, 6), dec(25, 2)),
            Err(CoreError::Breakeven(_))
        ));
    }

    #[test]
    fn blended_c_feeds_the_crossover_end_to_end() {
        // HIGH (Rev 2): T2 per-meter prices → T3 blend → T1 crossover, reproducing a hand V*.
        // input 0.000003, output 0.000015, share 0.25 → c = 0.000006.
        let Ok(c) = blended_cloud_per_token(dec(3, 6), dec(15, 6), dec(25, 2)) else {
            panic!("blend must succeed");
        };
        // capex $2000 / 1000 days = $2/day; e (energy-only) = 0.000001 → margin 0.000005 →
        // V* = 2 / 0.000005 = 400,000 tokens/day.
        let inputs = BreakevenInputs {
            local_energy_per_token: dec(1, 6),
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: c,
            max_tokens_per_day: None,
        };
        let Ok(BreakevenOutcome::CrossesAt { tokens_per_day }) = breakeven(&inputs) else {
            panic!("blended c must drive a crossover");
        };
        assert!(
            same(tokens_per_day, Decimal::from(400_000)),
            "blended-c crossover must be 400,000 tokens/day, got {tokens_per_day}"
        );
    }

    /// The shared worked example (pinned in DAYS — MED3):
    /// capex $2000 over 1000 days → hw_fixed = $2.00/day; e = $0.0000005/token (energy-only);
    /// c = $0.0000025/token → margin $0.0000020 → V* = 2.00 / 0.0000020 = 1,000,000 tokens/day.
    fn base_inputs() -> BreakevenInputs {
        BreakevenInputs {
            local_energy_per_token: dec(5, 7), // 0.0000005
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(25, 7), // 0.0000025
            max_tokens_per_day: None,
        }
    }

    fn stamp() -> AssumptionStamp {
        AssumptionStamp {
            electricity_rate_per_kwh: dec(16, 2),
            hardware_price: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            utilization: dec(5, 1),
            output_share: dec(25, 2),
            local_energy_per_token: dec(5, 7),
            blended_cloud_per_token: dec(25, 7),
            measurement_mode: "estimated".to_string(),
            hardware_profile: "strix-halo-128gb@2026-06-20".to_string(),
            // The R8 "{source}@{as_of}#{hash8}" form (the litellm tier carries a content hash;
            // the stamp stores whatever provenance id the caller supplies, opaquely).
            pricing_snapshot_id: "litellm@2026-06-18#36c8994e".to_string(),
            collector_version: "0.6.0".to_string(),
        }
    }

    /// A `BreakevenInputs` clone of the base with a different cloud rate (to sweep the mix/price).
    fn with_cloud(c: Decimal) -> BreakevenInputs {
        BreakevenInputs {
            cloud_per_token: c,
            ..base_inputs()
        }
    }

    #[test]
    fn the_report_is_a_band_not_a_scalar_with_a_full_stamp() {
        // Headline base V* = 1,000,000; two cheaper-cloud sweep points give larger V*.
        // c=0.0000015 → margin 0.0000010 → V* = 2/0.0000010 = 2,000,000.
        let sweep = vec![
            SweepPoint {
                label: "cloud=low".to_string(),
                inputs: with_cloud(dec(15, 7)), // 0.0000015 → V* 2,000,000
            },
            SweepPoint {
                label: "cloud=high".to_string(),
                inputs: with_cloud(dec(45, 7)), // 0.0000045 → V* 500,000
            },
        ];
        let Ok(report) = breakeven_report(base_inputs(), sweep, stamp(), Vec::new()) else {
            panic!("the report must assemble");
        };
        // It is a RANGE: more than one outcome, summarized into a low..high band.
        assert_eq!(report.sensitivity.len(), 2);
        let band = report.band();
        assert!(
            same(band.low.unwrap_or(Decimal::MAX), Decimal::from(500_000)),
            "band low = the cheapest-volume crossover (500,000), got {:?}",
            band.low
        );
        assert!(
            same(band.high.unwrap_or(Decimal::ZERO), Decimal::from(2_000_000)),
            "band high = the largest crossover (2,000,000), got {:?}",
            band.high
        );
        assert!(!band.has_never && !band.has_infeasible);
        // The full stamp is present (R6/R8) — a representative spot-check across its fields.
        assert_eq!(report.stamp.measurement_mode, "estimated");
        assert_eq!(report.stamp.hardware_profile, "strix-halo-128gb@2026-06-20");
        assert_eq!(
            report.stamp.pricing_snapshot_id,
            "litellm@2026-06-18#36c8994e"
        );
        assert!(same(report.stamp.local_energy_per_token, dec(5, 7)));
    }

    #[test]
    fn a_mixed_sweep_marks_the_band_low_to_never_with_exact_endpoints() {
        // The sweep runs from a feasible crossover into c <= e (never). The band must report the
        // EXACT feasible low endpoint AND flag the "never" endpoint — not drop it, not "sanity".
        let sweep = vec![
            SweepPoint {
                label: "cloud=high".to_string(),
                inputs: with_cloud(dec(45, 7)), // 0.0000045 → V* 500,000 (the feasible end)
            },
            SweepPoint {
                label: "cloud=at_energy".to_string(),
                inputs: with_cloud(dec(5, 7)), // c == e → Never
            },
        ];
        // Headline base = V* 1,000,000; feasible sweep point = 500,000; never point flags has_never.
        let Ok(report) = breakeven_report(base_inputs(), sweep, stamp(), Vec::new()) else {
            panic!("the report must assemble");
        };
        let band = report.band();
        assert!(
            same(band.low.unwrap_or(Decimal::MAX), Decimal::from(500_000)),
            "exact band low = 500,000, got {:?}",
            band.low
        );
        assert!(
            same(band.high.unwrap_or(Decimal::ZERO), Decimal::from(1_000_000)),
            "exact band high = 1,000,000 (the headline), got {:?}",
            band.high
        );
        assert!(
            band.has_never,
            "the c<=e endpoint must be marked never, not dropped"
        );
        assert!(!band.has_infeasible);
    }

    #[test]
    fn an_all_never_sweep_propagates_never_with_no_feasible_band() {
        // Every point has c <= e → the whole band is "never" (no feasible low/high).
        let never_inputs = with_cloud(dec(3, 7)); // 0.0000003 < e 0.0000005
        let sweep = vec![SweepPoint {
            label: "cloud=lower".to_string(),
            inputs: with_cloud(dec(1, 7)), // 0.0000001 < e
        }];
        let Ok(report) = breakeven_report(never_inputs, sweep, stamp(), Vec::new()) else {
            panic!("the report must assemble");
        };
        assert!(matches!(report.headline, BreakevenOutcome::Never { .. }));
        let band = report.band();
        assert!(band.has_never);
        assert!(
            band.low.is_none() && band.high.is_none(),
            "no feasible crossover anywhere"
        );
    }

    #[test]
    fn the_cloud_overlay_never_changes_the_crossover_and_carries_none_as_na() {
        // (T5/D3) The DeepSWE-Bench $/task overlay is reference-only: the headline + band must be
        // BIT-IDENTICAL whether or not it is attached. A None $/task is carried as None (→ "n/a"),
        // never coerced to zero.
        let overlay = vec![
            CloudReferencePoint {
                benchmark: "DeepSWE v1.1".to_string(),
                model: "claude-opus-4-8".to_string(),
                dollars_per_task: Some(dec(1322, 2)), // $13.22
                as_of: "2026-06-14".to_string(),
                source: "https://deepswe.datacurve.ai".to_string(),
            },
            CloudReferencePoint {
                benchmark: "DeepSWE v1.1".to_string(),
                model: "no-published-cost".to_string(),
                dollars_per_task: None, // stays "n/a", never zero
                as_of: "2026-06-14".to_string(),
                source: "https://deepswe.datacurve.ai".to_string(),
            },
        ];
        let Ok(without) = breakeven_report(base_inputs(), Vec::new(), stamp(), Vec::new()) else {
            panic!("report without overlay");
        };
        let Ok(with) = breakeven_report(base_inputs(), Vec::new(), stamp(), overlay) else {
            panic!("report with overlay");
        };
        // The crossover math is untouched by the overlay (bit-identical headline + band).
        assert_eq!(with.headline, without.headline);
        assert_eq!(with.band(), without.band());
        // The overlay is carried verbatim, and a missing cost stays None (not 0).
        assert_eq!(with.cloud_reference.len(), 2);
        assert_eq!(with.cloud_reference[1].dollars_per_task, None);
    }

    #[test]
    fn crossover_is_the_hand_computed_volume_in_days() {
        // (ii) real crossover, period pinned in days.
        let Ok(outcome) = breakeven(&base_inputs()) else {
            panic!("the base scenario must compute");
        };
        let BreakevenOutcome::CrossesAt { tokens_per_day } = outcome else {
            panic!("expected a crossover, got {outcome:?}");
        };
        assert!(
            same(tokens_per_day, Decimal::from(1_000_000)),
            "V* must be exactly 1,000,000 tokens/day, got {tokens_per_day}"
        );
    }

    #[test]
    fn the_required_never_case_when_cloud_le_energy() {
        // (i) the canon-required "never" case: c <= e → Never, with the documented reason.
        let mut inputs = base_inputs();
        inputs.cloud_per_token = dec(4, 7); // 0.0000004 < e 0.0000005
        let Ok(outcome) = breakeven(&inputs) else {
            panic!("must compute");
        };
        let BreakevenOutcome::Never { reason } = outcome else {
            panic!("c < e must be Never, got {outcome:?}");
        };
        assert!(reason.contains("never"), "reason states never: {reason}");

        // The boundary c == e is also Never (margin exactly zero — capex never recovered).
        let mut tie = base_inputs();
        tie.cloud_per_token = dec(5, 7); // == e
        assert!(
            matches!(breakeven(&tie), Ok(BreakevenOutcome::Never { .. })),
            "c == e must be Never (margin zero), not a division"
        );
    }

    #[test]
    fn a_second_crossover_at_a_different_output_mix() {
        // (iii) LOW: a different blended c (a heavier output mix) → a different hand-computed V*.
        // c = 0.0000045 → margin 0.0000040 → V* = 2.00 / 0.0000040 = 500,000 tokens/day.
        let mut inputs = base_inputs();
        inputs.cloud_per_token = dec(45, 7); // 0.0000045
        let Ok(BreakevenOutcome::CrossesAt { tokens_per_day }) = breakeven(&inputs) else {
            panic!("expected a crossover at the heavier mix");
        };
        assert!(
            same(tokens_per_day, Decimal::from(500_000)),
            "V* at the heavier mix must be 500,000, got {tokens_per_day}"
        );
    }

    #[test]
    fn a_real_crossover_above_the_machine_ceiling_is_infeasible() {
        // (iv) MED1: V* = 1,000,000 but the machine tops out at 500,000 → Infeasible, ceiling echoed.
        let mut inputs = base_inputs();
        inputs.max_tokens_per_day = Some(Decimal::from(500_000));
        let Ok(outcome) = breakeven(&inputs) else {
            panic!("must compute");
        };
        let BreakevenOutcome::Infeasible {
            v_star,
            max_tokens_per_day,
            reason,
        } = outcome
        else {
            panic!("V* > ceiling must be Infeasible, got {outcome:?}");
        };
        assert!(same(v_star, Decimal::from(1_000_000)), "v_star echoed");
        assert!(
            same(max_tokens_per_day, Decimal::from(500_000)),
            "ceiling echoed"
        );
        assert!(
            reason.contains("tops out"),
            "reason names the ceiling: {reason}"
        );

        // A ceiling at/above V* keeps it a feasible crossover.
        let mut feasible = base_inputs();
        feasible.max_tokens_per_day = Some(Decimal::from(1_000_000));
        assert!(
            matches!(breakeven(&feasible), Ok(BreakevenOutcome::CrossesAt { .. })),
            "ceiling == V* is still feasible"
        );
    }

    #[test]
    fn a_sub_rounding_margin_is_honored_at_full_precision() {
        // (v) MED2 — the crossover must use the FULL-precision energy-only rate, never a rounded
        // one. `e` and `c` here AGREE to 9 dp but differ in the 15th place. The perturbation is on
        // the margin `c - e`; its sub-9-dp magnitude must survive into the branch selection.
        //
        // The guard is NON-vacuous: because the two rates are indistinguishable at 9 dp, a stray
        // `round_dp(_, 9)` on either rate (e.g. a careless f64→Decimal conversion in the CLI,
        // T7/MED2) would collapse the margin to zero and pick the WRONG branch — proven by the
        // `round_dp` equality assertion below, which would make the full-precision crossover
        // disappear if the code ever rounded.
        let e = dec(500_000_003, 15); // 0.000000500000003
        let c = dec(500_000_009, 15); // 0.000000500000009  (c > e by 6e-15, below 9 dp)
        assert_eq!(
            e.round_dp(9),
            c.round_dp(9),
            "non-vacuous only if e and c collapse to the same value when rounded to 9 dp"
        );

        // Full precision sees the real +6e-15 margin → a (very large) crossover exists; a
        // round_dp(9) regression would instead see margin 0 → Never. The code must NOT round.
        let mut crosses = base_inputs();
        crosses.local_energy_per_token = e;
        crosses.cloud_per_token = c;
        assert!(
            matches!(breakeven(&crosses), Ok(BreakevenOutcome::CrossesAt { .. })),
            "a sub-rounding positive margin must still yield a crossover (full precision preserved)"
        );

        // Symmetric: swap so e > c by the same sub-9-dp amount → the negative sign is honored as
        // Never, not flattened to a rounded tie.
        let mut never = base_inputs();
        never.local_energy_per_token = c;
        never.cloud_per_token = e;
        assert!(
            matches!(breakeven(&never), Ok(BreakevenOutcome::Never { .. })),
            "a sub-rounding negative margin must be Never (full precision preserved)"
        );
    }

    #[test]
    fn scaling_invariance_of_the_crossover() {
        // (vi) LOW: capex × k → V* × k;  (c − e) × k → V* × (1/k).
        let Ok(BreakevenOutcome::CrossesAt { tokens_per_day: v0 }) = breakeven(&base_inputs())
        else {
            panic!("base crossover");
        };

        // capex × 2 → V* × 2.
        let mut capex2 = base_inputs();
        capex2.hardware_capex = UsdAmount::from_usd(Decimal::from(4000));
        let Ok(BreakevenOutcome::CrossesAt {
            tokens_per_day: v_cap,
        }) = breakeven(&capex2)
        else {
            panic!("scaled-capex crossover");
        };
        assert!(same(v_cap, v0 * Decimal::from(2)), "capex×2 → V*×2");

        // margin × 2 (keep e, set c = e + 2·margin) → V* × 1/2.
        let e = base_inputs().local_energy_per_token;
        let margin = base_inputs().cloud_per_token - e;
        let mut margin2 = base_inputs();
        margin2.cloud_per_token = e + margin * Decimal::from(2);
        let Ok(BreakevenOutcome::CrossesAt {
            tokens_per_day: v_mar,
        }) = breakeven(&margin2)
        else {
            panic!("scaled-margin crossover");
        };
        assert!(
            same(v_mar * Decimal::from(2), v0),
            "margin×2 → V*×(1/2): {v_mar} × 2 should equal {v0}"
        );
    }

    #[test]
    fn zero_capex_with_positive_margin_is_always() {
        let mut inputs = base_inputs();
        inputs.hardware_capex = UsdAmount::ZERO;
        assert!(
            matches!(breakeven(&inputs), Ok(BreakevenOutcome::Always)),
            "free hardware + c > e → Always cheaper"
        );
        // Always is not demoted by a feasibility ceiling: with no fixed cost to amortize, local is
        // cheaper at every positive volume, so even a tiny ceiling cannot make it infeasible.
        inputs.max_tokens_per_day = Some(Decimal::from(1));
        assert!(
            matches!(breakeven(&inputs), Ok(BreakevenOutcome::Always)),
            "Always holds with a ceiling set (no capex to recover)"
        );
    }

    #[test]
    fn degenerate_inputs_are_typed_errors_not_panics() {
        // (vii) non-positive period, negative rates, non-positive ceiling → typed errors.
        let mut bad_period = base_inputs();
        bad_period.depreciation_period_days = Decimal::ZERO;
        assert!(matches!(
            breakeven(&bad_period),
            Err(CoreError::Breakeven(_))
        ));

        let mut neg_energy = base_inputs();
        neg_energy.local_energy_per_token = dec(-1, 7);
        assert!(matches!(
            breakeven(&neg_energy),
            Err(CoreError::Breakeven(_))
        ));

        let mut neg_cloud = base_inputs();
        neg_cloud.cloud_per_token = dec(-1, 7);
        assert!(matches!(
            breakeven(&neg_cloud),
            Err(CoreError::Breakeven(_))
        ));

        let mut neg_capex = base_inputs();
        neg_capex.hardware_capex = UsdAmount::from_usd(Decimal::from(-1));
        assert!(matches!(
            breakeven(&neg_capex),
            Err(CoreError::Breakeven(_))
        ));

        let mut zero_ceiling = base_inputs();
        zero_ceiling.max_tokens_per_day = Some(Decimal::ZERO);
        assert!(matches!(
            breakeven(&zero_ceiling),
            Err(CoreError::Breakeven(_))
        ));
    }

    #[test]
    fn the_one_lifetime_rule_rejects_conflicts_and_absence() {
        // MED3: depreciation_period is the basis; hardware_lifetime_seconds is §3.2 only.
        // Exactly one → ok; both → error; neither → error; non-positive → error.
        let Ok(days) = resolve_depreciation_days(Some(Decimal::from(1000)), None) else {
            panic!("a lone depreciation_period must resolve");
        };
        assert!(same(days, Decimal::from(1000)));

        assert!(
            matches!(
                resolve_depreciation_days(Some(Decimal::from(1000)), Some(Decimal::from(86_400))),
                Err(CoreError::Breakeven(_))
            ),
            "both set → conflict error"
        );
        assert!(
            matches!(
                resolve_depreciation_days(None, Some(Decimal::from(86_400))),
                Err(CoreError::Breakeven(_))
            ),
            "lifetime-as-basis without a depreciation_period → error"
        );
        assert!(
            matches!(
                resolve_depreciation_days(None, None),
                Err(CoreError::Breakeven(_))
            ),
            "neither → error (break-even needs a calendar)"
        );
        assert!(matches!(
            resolve_depreciation_days(Some(Decimal::ZERO), None),
            Err(CoreError::Breakeven(_))
        ));

        // The seconds→days helper is exact: 86_400 s == 1 day.
        let Ok(one_day) = days_from_seconds(Decimal::from(86_400)) else {
            panic!("86_400s must convert");
        };
        assert!(same(one_day, Decimal::from(1)));
    }
}
