//! `costroid breakeven` — the local-vs-cloud break-even + scenario engine (M4, behind the
//! off-by-default `power` feature).
//!
//! Pure-compute, no hardware, no network: it computes the **local** energy-only marginal rate `e`
//! via `costroid-power`'s estimated harness (`estimate_run` — no subprocess), resolves the
//! **cloud** per-token price + blends it via `costroid-core`'s pricing catalog, and hands both to
//! the pure `costroid-core::breakeven` crossover math (no `core→power` edge — the local scalar
//! enters core as a plain `Decimal`). The result is a **range + methodology + assumption stamp**,
//! never a single hero number (R6), with a `--plain` ASCII path.
//!
//! `e` is **energy-only** (`energy_cost / total_tokens`) — never `local_cost_per_1m`, which folds
//! in the amortized hardware that is already the calendar-fixed term `hw_fixed_per_day` (folding it
//! in would double-count the capex and corrupt the crossover). It is carried at full precision
//! (`Decimal::from_f64_retain`, never `round_dp`).

use anyhow::{anyhow, bail, Result};
use costroid_core::{
    blended_cloud_per_token, breakeven_report, cloud_price_per_token, cloud_reference_points,
    AssumptionStamp, BreakevenInputs, BreakevenOutcome, BreakevenReport, SweepPoint, TokenType,
    UsdAmount,
};
use costroid_power::{
    bundled_models, bundled_power_profiles, estimate_run, LocalRunReport, ProfileOverrides,
    DEFAULT_BENCHMARK_SUITE,
};
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;

use crate::render::{RenderOptions, SemanticStyle, StyledDocument, StyledLine};
use crate::BreakevenArgs;

const SECONDS_PER_DAY: i64 = 86_400;
/// The default break-even depreciation calendar when neither flag nor config sets one (≈ 3 years).
const DEFAULT_DEPRECIATION_DAYS: i64 = 1095;
/// The default cloud model to compare against when unset.
const DEFAULT_CLOUD_MODEL: &str = "claude-opus-4-8";
/// The runtime label for the estimated harness (no subprocess is launched).
const ESTIMATED_RUNTIME: &str = "ollama";

pub fn run_breakeven(args: &BreakevenArgs, render_options: RenderOptions) -> Result<()> {
    // 1. Config defaults (MED3-validated), overridden by flags.
    let config = costroid_config::load().map_err(|err| anyhow!("config: {err}"))?;
    let scenario = config
        .breakeven_scenario()
        .map_err(|err| anyhow!("{err}"))?;

    let utilization = resolve_decimal(args.utilization, scenario.utilization, Decimal::ONE)?;
    // Validate at the CLI boundary: an out-of-range utilization (flag OR config) would silently
    // inflate the feasibility ceiling (tok/s · utilization · 86400). Reject it with a clear error.
    if utilization <= Decimal::ZERO || utilization > Decimal::ONE {
        bail!("utilization must be in (0, 1], got {utilization}");
    }
    let depreciation_period_days = resolve_decimal(
        args.depreciation_period_days,
        scenario.depreciation_period_days,
        Decimal::from(DEFAULT_DEPRECIATION_DAYS),
    )?;
    let cloud_model = args
        .compare_to
        .clone()
        .or(scenario.cloud_model)
        .unwrap_or_else(|| DEFAULT_CLOUD_MODEL.to_string());
    let tokens_per_day = match args.tokens_per_day {
        Some(value) => Some(finite_decimal(value, "tokens-per-day")?),
        None => scenario.tokens_per_day,
    };

    // 2. Local energy-only e + the feasibility ceiling, via costroid-power's estimated harness.
    let manifest = bundled_models()?;
    let (spec, quant) = manifest.resolve(&args.model, args.quant.as_deref())?;
    let profiles = bundled_power_profiles()?;
    let electricity_rate = first_f64(args.electricity_rate, scenario.electricity_rate_per_kwh);
    let hardware_price = first_f64(args.hardware_price, scenario.hardware_price);
    let overrides = ProfileOverrides {
        hardware_profile_id: args.hardware_profile.clone(),
        // The estimated sampler's load is the profile default (no CLI override — bench's rule).
        load_watts: None,
        electricity_rate_per_kwh: electricity_rate,
        hardware_price,
        // NOT the break-even basis (MED3): the break-even calendar is `depreciation_period_days`.
        // The per-run lifetime only affects the report's (unused-here) x_AmortizedHwCost.
        hardware_lifetime_seconds: None,
    };
    let resolved = profiles.resolve(&overrides)?;
    let report = estimate_run(
        spec,
        &quant,
        ESTIMATED_RUNTIME,
        args.tokens_in,
        args.tokens_out,
        &resolved,
        DEFAULT_BENCHMARK_SUITE,
    )?;
    let energy_per_token = energy_only_rate(&report)?;
    let max_tokens_per_day = feasibility_ceiling(spec.estimated_tok_s, utilization)?;

    // 3. Cloud per-meter prices → the scenario-mix blend `c`.
    let output_share = resolve_output_share(args, scenario.output_share, &report)?;
    let override_json = read_pricing_override(args)?;
    let input_rate = require_rate(&cloud_model, TokenType::Input, override_json.as_deref())?;
    let output_rate = require_rate(&cloud_model, TokenType::Output, override_json.as_deref())?;
    let cloud_per_token = blended_cloud_per_token(
        input_rate.price_per_token,
        output_rate.price_per_token,
        output_share,
    )?;

    // 4. The base scenario + the capex.
    let capex_decimal = Decimal::from_f64_retain(resolved.hardware_price)
        .ok_or_else(|| anyhow!("hardware price is not a finite number"))?;
    let capex = UsdAmount::from_usd(capex_decimal);
    let base = BreakevenInputs {
        local_energy_per_token: energy_per_token,
        hardware_capex: capex,
        depreciation_period_days,
        cloud_per_token,
        max_tokens_per_day: Some(max_tokens_per_day),
    };

    // 5. The sensitivity sweep (R6 — a range, not a hero number).
    let sweep = build_sweep(
        &base,
        input_rate.price_per_token,
        output_rate.price_per_token,
        output_share,
    );

    // 6. The assumption stamp (R6/R8) — what this comparison assumed.
    let stamp = AssumptionStamp {
        electricity_rate_per_kwh: Decimal::from_f64_retain(resolved.electricity_rate_per_kwh)
            .ok_or_else(|| anyhow!("electricity rate is not a finite number"))?,
        hardware_price: capex,
        depreciation_period_days,
        utilization,
        output_share,
        local_energy_per_token: energy_per_token,
        blended_cloud_per_token: cloud_per_token,
        measurement_mode: report.measurement_mode.as_focus_str().to_string(),
        hardware_profile: report.hardware_profile_stamp.clone(),
        pricing_snapshot_id: input_rate.snapshot_id.clone(),
        collector_version: env!("CARGO_PKG_VERSION").to_string(),
    };

    // 7. Assemble the report (with the labeled, dated DeepSWE overlay) and render.
    let overlay = cloud_reference_points()?;
    let report = breakeven_report(base, sweep, stamp, overlay)?;
    let document = render_breakeven(&report, &cloud_model, tokens_per_day);
    print!("{}", document.render(render_options));
    Ok(())
}

/// `e` — the local **energy-only** marginal rate, `energy_cost / total_tokens`, as an exact
/// `Decimal` at full f64 precision (`from_f64_retain`, never `round_dp`). It selects `energy_cost`
/// (NOT `local_run_cost`/`local_cost_per_1m`, which include the amortized hardware already counted
/// as the calendar-fixed term) — using the total here would double-count the capex.
fn energy_only_rate(report: &LocalRunReport) -> Result<Decimal> {
    let total_tokens = report
        .tokens_in
        .checked_add(report.tokens_out)
        .ok_or_else(|| anyhow!("token total overflowed"))?;
    if total_tokens == 0 {
        bail!("a break-even estimate needs a non-zero token count");
    }
    let energy_cost = Decimal::from_f64_retain(report.energy_cost)
        .ok_or_else(|| anyhow!("energy cost is not a finite number"))?;
    energy_cost
        .checked_div(Decimal::from(total_tokens))
        .ok_or_else(|| anyhow!("energy-only rate overflowed"))
}

/// The machine's feasibility ceiling: the maximum tokens it can produce per day,
/// `estimated_tok_s · utilization · 86_400` (a plain number passed to core — no `core→power` edge).
fn feasibility_ceiling(estimated_tok_s: f64, utilization: Decimal) -> Result<Decimal> {
    let tok_s = Decimal::from_f64_retain(estimated_tok_s)
        .ok_or_else(|| anyhow!("estimated tok/s is not a finite number"))?;
    tok_s
        .checked_mul(Decimal::from(SECONDS_PER_DAY))
        .and_then(|per_day| per_day.checked_mul(utilization))
        .ok_or_else(|| anyhow!("feasibility ceiling overflowed"))
}

/// Resolve a `[0,1]`-ish numeric knob: flag → config → default.
fn resolve_decimal(
    flag: Option<f64>,
    config: Option<Decimal>,
    default: Decimal,
) -> Result<Decimal> {
    match flag {
        Some(value) => finite_decimal(value, "scenario value"),
        None => Ok(config.unwrap_or(default)),
    }
}

/// The output-token share for the cloud blend: flag → config → derived from the run's in/out mix.
fn resolve_output_share(
    args: &BreakevenArgs,
    config: Option<Decimal>,
    report: &LocalRunReport,
) -> Result<Decimal> {
    if let Some(value) = args.output_share {
        return finite_decimal(value, "output-share");
    }
    if let Some(value) = config {
        return Ok(value);
    }
    // Default: the workload's own output fraction, tokens_out / (tokens_in + tokens_out).
    let total = report
        .tokens_in
        .checked_add(report.tokens_out)
        .ok_or_else(|| anyhow!("token total overflowed"))?;
    if total == 0 {
        return Ok(Decimal::ONE);
    }
    Decimal::from(report.tokens_out)
        .checked_div(Decimal::from(total))
        .ok_or_else(|| anyhow!("output share overflowed"))
}

fn finite_decimal(value: f64, label: &str) -> Result<Decimal> {
    Decimal::from_f64_retain(value).ok_or_else(|| anyhow!("{label} must be a finite number"))
}

/// Prefer the flag's f64, else the config's `Decimal` (lossily → f64 for the f64-based power API).
fn first_f64(flag: Option<f64>, config: Option<Decimal>) -> Option<f64> {
    flag.or_else(|| config.and_then(|value| value.to_f64()))
}

fn read_pricing_override(args: &BreakevenArgs) -> Result<Option<String>> {
    match &args.pricing_override {
        Some(path) => costroid_core::read_pricing_override(Some(path.as_path()))
            .map_err(|err| anyhow!("pricing override: {err}")),
        None => Ok(None),
    }
}

fn require_rate(
    model: &str,
    token_type: TokenType,
    override_json: Option<&str>,
) -> Result<costroid_core::CloudTokenPrice> {
    cloud_price_per_token(model, token_type, override_json)
        .map_err(|err| anyhow!("cloud pricing: {err}"))?
        .ok_or_else(|| {
            anyhow!(
                "no {} price for cloud model `{model}` in the pricing catalog (try --compare-to \
                 with a known model, e.g. claude-opus-4-8 / gpt-5.5)",
                token_type.as_str()
            )
        })
}

/// The sensitivity sweep: vary the most uncertain break-even inputs over a documented span so the
/// report is a RANGE, not a hero number (R6). Electricity ±50% scales the energy-only `e`; hardware
/// price ±20% scales the capex; the output mix ±0.2 re-blends the cloud rate (clamped to [0,1]).
fn build_sweep(
    base: &BreakevenInputs,
    input_per_token: Decimal,
    output_per_token: Decimal,
    output_share: Decimal,
) -> Vec<SweepPoint> {
    let mut sweep = Vec::new();

    for (label, factor) in [
        ("electricity -50%", Decimal::new(5, 1)),
        ("electricity +50%", Decimal::new(15, 1)),
    ] {
        if let Some(scaled) = base.local_energy_per_token.checked_mul(factor) {
            let mut inputs = base.clone();
            inputs.local_energy_per_token = scaled;
            sweep.push(SweepPoint {
                label: label.to_string(),
                inputs,
            });
        }
    }

    for (label, factor) in [
        ("hardware -20%", Decimal::new(8, 1)),
        ("hardware +20%", Decimal::new(12, 1)),
    ] {
        if let Some(scaled) = base.hardware_capex.as_usd().checked_mul(factor) {
            let mut inputs = base.clone();
            inputs.hardware_capex = UsdAmount::from_usd(scaled);
            sweep.push(SweepPoint {
                label: label.to_string(),
                inputs,
            });
        }
    }

    for (label, delta) in [
        ("output mix -0.2", Decimal::new(-2, 1)),
        ("output mix +0.2", Decimal::new(2, 1)),
    ] {
        // `output_share` is pre-validated to [0,1]; `checked_add` keeps the discipline regardless.
        let Some(raw) = output_share.checked_add(delta) else {
            continue;
        };
        let share = clamp_unit(raw);
        if let Ok(blended) = blended_cloud_per_token(input_per_token, output_per_token, share) {
            let mut inputs = base.clone();
            inputs.cloud_per_token = blended;
            sweep.push(SweepPoint {
                label: label.to_string(),
                inputs,
            });
        }
    }

    sweep
}

fn clamp_unit(value: Decimal) -> Decimal {
    value.max(Decimal::ZERO).min(Decimal::ONE)
}

// ---------------------------------------------------------------------------
// Render (T8) — a styled document with a `--plain` ASCII equivalent; never color-alone (every
// Warn/Critical also carries a textual cue: NEVER / INFEASIBLE).
// ---------------------------------------------------------------------------

fn render_breakeven(
    report: &BreakevenReport,
    cloud_model: &str,
    tokens_per_day: Option<Decimal>,
) -> StyledDocument {
    let mut doc = StyledDocument::new();

    doc.push(StyledLine {
        spans: vec![crate::render::StyledSpan {
            content: format!("Local-vs-cloud break-even  (vs {cloud_model})"),
            style: SemanticStyle::Strong,
        }],
    });
    doc.push(StyledLine::plain(""));

    // The headline verdict. Warn/Critical are always paired with a text cue (never color-alone).
    let mut verdict = StyledLine::new();
    match &report.headline {
        BreakevenOutcome::CrossesAt { tokens_per_day } => {
            verdict.push_plain("Local breaks even at ");
            verdict.push_styled(fmt_tokens(*tokens_per_day), SemanticStyle::Accent);
            verdict.push_plain(" tokens/day.");
        }
        BreakevenOutcome::Always => {
            verdict.push_styled("Local is cheaper at every volume", SemanticStyle::Accent);
            verdict.push_plain(" (no hardware cost to recover).");
        }
        BreakevenOutcome::Never { reason } => {
            verdict.push_styled("NEVER", SemanticStyle::Warn);
            verdict.push_plain(format!(" breaks even — {reason}."));
        }
        BreakevenOutcome::Infeasible {
            v_star,
            max_tokens_per_day,
            ..
        } => {
            verdict.push_styled("INFEASIBLE", SemanticStyle::Critical);
            verdict.push_plain(format!(
                ": would break even at {} tokens/day, but the machine tops out at {} tokens/day.",
                fmt_tokens(*v_star),
                fmt_tokens(*max_tokens_per_day)
            ));
        }
    }
    doc.push(verdict);

    // "Where you are" context, if a daily volume was given.
    if let Some(volume) = tokens_per_day {
        doc.push(where_you_are_line(&report.headline, volume));
    }

    // The sensitivity band (R6 — a range, not a hero number).
    doc.push(StyledLine::plain(""));
    doc.push(sensitivity_line(report));

    // The assumption stamp (R6/R8).
    doc.push(StyledLine::plain(""));
    doc.push(StyledLine::plain(
        "Assumptions (estimate — your tokens × current prices):",
    ));
    for (key, value) in stamp_rows(&report.stamp) {
        let mut line = StyledLine::new();
        line.push_plain(format!("  {key}: "));
        line.push_styled(value, SemanticStyle::Muted);
        doc.push(line);
    }

    // The labeled, dated DeepSWE-Bench overlay — reference only, NOT the crossover math.
    let references = primary_reference(&report.cloud_reference);
    if !references.is_empty() {
        doc.push(StyledLine::plain(""));
        doc.push(StyledLine::plain(
            "Cloud $/task reference (DeepSWE-Bench — labeled, dated; not the crossover):",
        ));
        for point in references {
            let cost = point
                .dollars_per_task
                .map(|value| format!("${}", value.round_dp(2).normalize()))
                .unwrap_or_else(|| "n/a".to_string());
            let mut line = StyledLine::new();
            line.push_plain(format!("  {} · ", point.model));
            line.push_styled(cost, SemanticStyle::Data);
            line.push_plain(format!("  ({} {})", point.benchmark, point.as_of));
            doc.push(line);
        }
    }

    doc.push(StyledLine::plain(""));
    doc.push(StyledLine {
        spans: vec![crate::render::StyledSpan {
            content: "Estimate — ranges + methodology, never a single hero number; reconcile \
                      against your provider invoice."
                .to_string(),
            style: SemanticStyle::Muted,
        }],
    });

    doc
}

fn where_you_are_line(headline: &BreakevenOutcome, volume: Decimal) -> StyledLine {
    let mut line = StyledLine::new();
    line.push_plain(format!("At your {} tokens/day: ", fmt_tokens(volume)));
    match headline {
        BreakevenOutcome::CrossesAt { tokens_per_day } => {
            if volume >= *tokens_per_day {
                line.push_styled("local is already cheaper", SemanticStyle::Accent);
            } else {
                line.push_styled("cloud is still cheaper", SemanticStyle::Warn);
                line.push_plain(" (below the crossover)");
            }
        }
        BreakevenOutcome::Always => {
            line.push_styled("local is cheaper", SemanticStyle::Accent);
        }
        BreakevenOutcome::Never { .. } => {
            line.push_styled("cloud is cheaper (and always will be)", SemanticStyle::Warn);
        }
        BreakevenOutcome::Infeasible { .. } => {
            line.push_styled(
                "the crossover is unreachable on this hardware",
                SemanticStyle::Warn,
            );
        }
    }
    line
}

fn sensitivity_line(report: &BreakevenReport) -> StyledLine {
    let band = report.band();
    let mut line = StyledLine::new();
    line.push_plain("Sensitivity range: ");
    match (band.low, band.high) {
        (Some(low), Some(high)) => {
            line.push_styled(
                format!("{} … {}", fmt_tokens(low), fmt_tokens(high)),
                SemanticStyle::Data,
            );
            line.push_plain(" tokens/day");
            if band.has_never {
                line.push_plain(" — some scenarios ");
                line.push_styled("NEVER", SemanticStyle::Warn);
                line.push_plain(" break even");
            }
            if band.has_infeasible {
                line.push_plain(" — some are ");
                line.push_styled("INFEASIBLE", SemanticStyle::Warn);
                line.push_plain(" on this hardware");
            }
        }
        _ => {
            line.push_styled("NEVER", SemanticStyle::Warn);
            line.push_plain(" within the swept assumptions");
        }
    }
    line
}

fn stamp_rows(stamp: &AssumptionStamp) -> Vec<(String, String)> {
    vec![
        (
            "electricity".to_string(),
            format!(
                "${}/kWh",
                stamp.electricity_rate_per_kwh.round_dp(4).normalize()
            ),
        ),
        (
            "hardware".to_string(),
            format!("${}", stamp.hardware_price.as_usd().round_dp(2).normalize()),
        ),
        (
            "depreciation".to_string(),
            format!("{} days", stamp.depreciation_period_days.normalize()),
        ),
        (
            "utilization".to_string(),
            stamp.utilization.normalize().to_string(),
        ),
        (
            "output share".to_string(),
            stamp.output_share.normalize().to_string(),
        ),
        (
            "local energy".to_string(),
            fmt_per_million(stamp.local_energy_per_token),
        ),
        (
            "cloud blended".to_string(),
            fmt_per_million(stamp.blended_cloud_per_token),
        ),
        ("measurement".to_string(), stamp.measurement_mode.clone()),
        (
            "hardware profile".to_string(),
            stamp.hardware_profile.clone(),
        ),
        (
            "pricing snapshot".to_string(),
            stamp.pricing_snapshot_id.clone(),
        ),
        ("collector".to_string(), stamp.collector_version.clone()),
    ]
}

/// The primary (DeepSWE) reference points only — the labeled cloud-agent $/task overlay.
fn primary_reference(
    points: &[costroid_core::CloudReferencePoint],
) -> Vec<&costroid_core::CloudReferencePoint> {
    points
        .iter()
        .filter(|point| point.benchmark.starts_with("DeepSWE"))
        .collect()
}

/// Round a token volume to a whole number for display (the underlying `Decimal` may be truncated).
fn fmt_tokens(value: Decimal) -> String {
    value.round_dp(0).normalize().to_string()
}

/// Display a per-token rate as the more readable per-1M-tokens figure (the catalog's native unit).
/// On a pathological overflow (unreachable for any real catalog price), fall back to the per-token
/// value with the CORRECT `/token` unit rather than mislabeling a 1e6-off number as `/1M tok`.
fn fmt_per_million(rate: Decimal) -> String {
    match rate.checked_mul(Decimal::from(1_000_000_i64)) {
        Some(per_million) => format!("${}/1M tok", per_million.round_dp(4).normalize()),
        None => format!("${}/token", rate.normalize()),
    }
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;
    use costroid_core::breakeven;
    use costroid_power::MeasurementMode;

    fn dec(mantissa: i64, scale: u32) -> Decimal {
        Decimal::new(mantissa, scale)
    }

    fn same(a: Decimal, b: Decimal) -> bool {
        a.normalize() == b.normalize()
    }

    /// A synthetic report whose `energy_cost` and `local_run_cost` DIFFER, so a test can prove the
    /// rate is taken from `energy_cost` (energy-only), not the capex-inclusive total.
    fn report_with(
        energy_cost: f64,
        local_run_cost: f64,
        tokens_in: u64,
        tokens_out: u64,
    ) -> LocalRunReport {
        LocalRunReport {
            model: "gemma-4-26b-a4b".to_string(),
            quant: "Q4_K_M".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in,
            tokens_out,
            run_seconds: 10.0,
            avg_power_watts: 100.0,
            energy_wh: 0.0,
            joules_per_token: 0.0,
            energy_cost,
            amortized_hw_cost: local_run_cost - energy_cost,
            local_run_cost,
            local_cost_per_1m: 0.0,
            measurement_mode: MeasurementMode::Estimated,
            hardware_profile_stamp: "strix-halo-128gb@2026-06-20".to_string(),
            benchmark_id: "test".to_string(),
            electricity_rate_per_kwh: 0.16,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            currency: "USD".to_string(),
        }
    }

    #[test]
    fn energy_only_rate_uses_energy_cost_not_the_capex_inclusive_total() {
        // energy_cost 2.0 over 1,000,000 tokens → e = 0.000002 (NOT local_run_cost 5.0 → 0.000005).
        let report = report_with(2.0, 5.0, 400_000, 600_000);
        let Ok(e) = energy_only_rate(&report) else {
            panic!("the energy-only rate must compute");
        };
        assert!(
            same(e, dec(2, 6)),
            "e must be energy_cost/total = 0.000002, got {e}"
        );
        assert!(
            !same(e, dec(5, 6)),
            "e must NOT be the capex-inclusive total rate 0.000005"
        );
    }

    #[test]
    fn end_to_end_crossover_uses_energy_only_e_and_would_differ_on_the_total() {
        // This is the ONE place the energy-only slip is caught (T1 takes e as an explicit input).
        // capex $2000 / 1000 days = $2/day; c = 0.000006.
        // energy-only e = 0.000002 → margin 0.000004 → V* = 2 / 0.000004 = 500,000.
        // the WRONG total e = 0.000005 → margin 0.000001 → V* = 2,000,000 (a different answer).
        let report = report_with(2.0, 5.0, 400_000, 600_000);
        let Ok(e) = energy_only_rate(&report) else {
            panic!("rate");
        };
        let inputs = BreakevenInputs {
            local_energy_per_token: e,
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(6, 6),
            max_tokens_per_day: None,
        };
        let Ok(BreakevenOutcome::CrossesAt { tokens_per_day }) = breakeven(&inputs) else {
            panic!("energy-only crossover");
        };
        assert!(
            same(tokens_per_day, Decimal::from(500_000)),
            "energy-only V* must be 500,000, got {tokens_per_day}"
        );

        // The capex-inclusive total would give a materially different V* (2,000,000) — proving the
        // energy-only choice changes the answer, so this test fails if T7 ever uses the total.
        let wrong_total = Decimal::from_f64_retain(report.local_run_cost)
            .and_then(|c| c.checked_div(Decimal::from(1_000_000_u64)));
        let Some(wrong_e) = wrong_total else {
            panic!("total rate");
        };
        let wrong_inputs = BreakevenInputs {
            local_energy_per_token: wrong_e,
            ..inputs.clone()
        };
        let Ok(BreakevenOutcome::CrossesAt {
            tokens_per_day: wrong_v,
        }) = breakeven(&wrong_inputs)
        else {
            panic!("total crossover");
        };
        assert!(
            !same(wrong_v, tokens_per_day),
            "the total-rate V* ({wrong_v}) must differ from the energy-only V* ({tokens_per_day})"
        );
        assert!(
            same(wrong_v, Decimal::from(2_000_000)),
            "total V* = 2,000,000"
        );
    }

    #[test]
    fn feasibility_ceiling_is_tok_s_times_utilization_times_a_day() {
        // 50 tok/s · 0.5 utilization · 86,400 s/day = 2,160,000 tokens/day.
        let Ok(ceiling) = feasibility_ceiling(50.0, dec(5, 1)) else {
            panic!("ceiling");
        };
        assert!(same(ceiling, Decimal::from(2_160_000)), "got {ceiling}");
    }

    #[test]
    fn the_plain_render_carries_text_cues_and_no_escapes() {
        // A "never" report renders with the NEVER text cue and is byte-identical under --plain
        // (no ANSI escapes) — never color-alone.
        let inputs = BreakevenInputs {
            local_energy_per_token: dec(5, 6), // e > c → Never
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(1, 6),
            max_tokens_per_day: None,
        };
        let Ok(headline) = breakeven(&inputs) else {
            panic!("never outcome");
        };
        let report = BreakevenReport {
            headline,
            sensitivity: Vec::new(),
            stamp: test_stamp(),
            cloud_reference: Vec::new(),
        };
        let plain =
            render_breakeven(&report, "claude-opus-4-8", None).render(RenderOptions::plain());
        assert!(plain.contains("NEVER"), "the never cue must be textual");
        assert!(
            no_escape(&plain),
            "--plain output must contain no ANSI escape"
        );
        assert!(plain.contains("claude-opus-4-8"), "names the cloud model");
    }

    fn test_stamp() -> AssumptionStamp {
        AssumptionStamp {
            electricity_rate_per_kwh: dec(16, 2),
            hardware_price: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            utilization: Decimal::ONE,
            output_share: dec(5, 1),
            local_energy_per_token: dec(5, 6),
            blended_cloud_per_token: dec(1, 6),
            measurement_mode: "estimated".to_string(),
            hardware_profile: "strix-halo-128gb@2026-06-20".to_string(),
            pricing_snapshot_id: "curated@2026-06-02".to_string(),
            collector_version: "0.6.0".to_string(),
        }
    }

    fn no_escape(text: &str) -> bool {
        !text.contains('\u{1b}')
    }

    #[test]
    fn the_infeasible_headline_renders_its_text_cue_under_plain() {
        // A real crossover above the machine ceiling → Infeasible; the INFEASIBLE cue must be
        // textual (never color-alone) and survive --plain with no escapes.
        let inputs = BreakevenInputs {
            local_energy_per_token: dec(1, 6),
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(3, 6), // margin 0.000002 → V* 1,000,000
            max_tokens_per_day: Some(Decimal::from(500_000)), // below V* → Infeasible
        };
        let Ok(headline) = breakeven(&inputs) else {
            panic!("infeasible outcome");
        };
        assert!(matches!(headline, BreakevenOutcome::Infeasible { .. }));
        let report = BreakevenReport {
            headline,
            sensitivity: Vec::new(),
            stamp: test_stamp(),
            cloud_reference: Vec::new(),
        };
        let plain =
            render_breakeven(&report, "claude-opus-4-8", None).render(RenderOptions::plain());
        assert!(
            plain.contains("INFEASIBLE"),
            "the infeasible cue must be textual"
        );
        assert!(no_escape(&plain), "no ANSI under --plain");
    }

    #[test]
    fn below_the_crossover_says_cloud_is_still_cheaper_under_plain() {
        // CrossesAt 1,000,000, but the user is at 500,000/day (below) → the cue is textual.
        let inputs = BreakevenInputs {
            local_energy_per_token: dec(5, 7),
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(25, 7), // V* 1,000,000
            max_tokens_per_day: None,
        };
        let Ok(headline) = breakeven(&inputs) else {
            panic!("crossover outcome");
        };
        let report = BreakevenReport {
            headline,
            sensitivity: Vec::new(),
            stamp: test_stamp(),
            cloud_reference: Vec::new(),
        };
        let plain = render_breakeven(&report, "claude-opus-4-8", Some(Decimal::from(500_000)))
            .render(RenderOptions::plain());
        assert!(
            plain.contains("cloud is still cheaper"),
            "below-crossover cue must be textual"
        );
        assert!(no_escape(&plain), "no ANSI under --plain");
    }
}
