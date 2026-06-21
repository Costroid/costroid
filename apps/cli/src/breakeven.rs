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
    AssumptionStamp, BreakevenInputs, BreakevenOutcome, BreakevenReport, EngineSnapshot,
    SweepPoint, TokenType, UsdAmount,
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
    let (report, cloud_model, tokens_per_day) = breakeven_report_for(args)?;
    let document = render_breakeven(&report, &cloud_model, tokens_per_day);
    print!("{}", document.render(render_options));
    Ok(())
}

/// Build the break-even report for a scenario — shared by the `costroid breakeven` subcommand and
/// the power-gated TUI overlay ([`breakeven_overlay_document`]). Pure-compute: config defaults +
/// the bundled estimated harness + the catalog pricing → the M4 engine. Returns the report, the
/// resolved cloud model, and the "where you are" daily volume (both for [`render_breakeven`]).
fn breakeven_report_for(
    args: &BreakevenArgs,
) -> Result<(BreakevenReport, String, Option<Decimal>)> {
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
    // Reject a non-positive daily volume (flag OR config) at the CLI boundary, mirroring the
    // utilization guard — a zero/negative "where you are" volume is meaningless.
    if let Some(volume) = tokens_per_day {
        if volume <= Decimal::ZERO {
            bail!("tokens-per-day must be positive, got {volume}");
        }
    }

    // 2. Local energy-only e + the feasibility ceiling, via costroid-power's estimated harness.
    let manifest = bundled_models()?;
    let (spec, quant) = manifest.resolve(&args.model, args.quant.as_deref())?;
    let profiles = bundled_power_profiles()?;
    let electricity_rate = first_f64(args.electricity_rate, scenario.electricity_rate_per_kwh);
    let overrides = ProfileOverrides {
        hardware_profile_id: args.hardware_profile.clone(),
        // The estimated sampler's load is the profile default (no CLI override — bench's rule).
        load_watts: None,
        // electricity_rate stays on the f64 power-profile path (it feeds the energy estimate).
        electricity_rate_per_kwh: electricity_rate,
        // Break-even computes the capex separately (exact for a config Decimal — see `resolve_capex`);
        // the profile's hardware_price would only feed the per-run amortized cost, which break-even
        // does not use, so leave it at the profile default here.
        hardware_price: None,
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

    // 4. The base scenario + the capex (the config's exact Decimal is threaded straight through;
    //    from_f64_retain is used only for the flag (f64) path or the f64 profile default).
    let capex = UsdAmount::from_usd(resolve_capex(
        args.hardware_price,
        scenario.hardware_price,
        resolved.hardware_price,
    )?);
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
    Ok((report, cloud_model, tokens_per_day))
}

/// The power-gated TUI break-even/comparison overlay document (the `b` overlay; M5 T1). Reuses the
/// shared seam + [`render_breakeven`] for the break-even facet, and adds a comparison facet over the
/// live snapshot's `local_inference` rows (actual local spend vs counterfactual cloud at the
/// report's blended list-price rate). On a compute error it renders one honest error line; with no
/// local rows it renders an honest empty state (never a fabricated free break-even, MED5).
pub(crate) fn breakeven_overlay_document(snapshot: Option<&EngineSnapshot>) -> StyledDocument {
    let mut doc = StyledDocument::new();
    doc.push(StyledLine {
        spans: vec![crate::render::StyledSpan {
            content: "Break-even & comparison".to_string(),
            style: SemanticStyle::Strong,
        }],
    });
    doc.push(StyledLine::plain(""));

    match breakeven_report_for(&BreakevenArgs::tui_overlay()) {
        Ok((report, cloud_model, tokens_per_day)) => {
            // The comparison facet: actual stored local spend vs counterfactual cloud list price.
            for line in comparison_lines(snapshot, &report) {
                doc.push(line);
            }
            doc.push(StyledLine::plain(""));
            // The break-even facet — the shared renderer (carries measurement_mode + the snapshot
            // stamp + the "counterfactual list-price estimate" label).
            for line in render_breakeven(&report, &cloud_model, tokens_per_day).lines {
                doc.push(line);
            }
        }
        Err(err) => {
            let mut line = StyledLine::new();
            line.push_styled("break-even unavailable", SemanticStyle::Warn);
            line.push_plain(format!(": {err}"));
            doc.push(line);
        }
    }
    doc
}

/// The actual-local-spend vs counterfactual-cloud comparison over the snapshot's `local_inference`
/// rows, priced at the report's blended cloud rate. With no local rows it returns an honest empty
/// state (MED5 — never a fabricated `e = 0` / free break-even). Carries the pricing-snapshot id.
fn comparison_lines(
    snapshot: Option<&EngineSnapshot>,
    report: &BreakevenReport,
) -> Vec<StyledLine> {
    // The FOCUS `x_Lane` value for the local-inference lane (== `LedgerLane::LocalInference`).
    let local = "local_inference";
    let mut actual_spend = Decimal::ZERO;
    let mut tokens = Decimal::ZERO;
    if let Some(snapshot) = snapshot {
        for row in snapshot.focus_rows.iter().filter(|row| row.x_lane == local) {
            actual_spend = actual_spend
                .checked_add(row.effective_cost)
                .unwrap_or(actual_spend);
            tokens = tokens.checked_add(row.x_consumed_tokens).unwrap_or(tokens);
        }
    }
    if tokens.is_zero() {
        return vec![StyledLine::plain(
            "No local runs recorded yet — run `costroid bench` to populate the local lane.",
        )];
    }

    let counterfactual = report
        .stamp
        .blended_cloud_per_token
        .checked_mul(tokens)
        .unwrap_or(Decimal::ZERO);
    let mut actual_line = StyledLine::new();
    actual_line.push_plain("Actual local spend: ");
    actual_line.push_styled(
        format!("${}", actual_spend.round_dp(4).normalize()),
        SemanticStyle::Data,
    );
    actual_line.push_plain(format!(" over {} tokens", fmt_tokens(tokens)));

    let mut cloud_line = StyledLine::new();
    cloud_line.push_plain("Counterfactual cloud: ");
    cloud_line.push_styled(
        format!("${}", counterfactual.round_dp(4).normalize()),
        SemanticStyle::Data,
    );
    cloud_line.push_plain(format!(
        " (same {} tokens at list price)",
        fmt_tokens(tokens)
    ));

    vec![
        actual_line,
        cloud_line,
        StyledLine {
            spans: vec![crate::render::StyledSpan {
                content: format!(
                    "Pricing snapshot {} (cloud is a list-price counterfactual).",
                    report.stamp.pricing_snapshot_id
                ),
                style: SemanticStyle::Muted,
            }],
        },
    ]
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

/// Resolve the amortized capex: flag → config → profile default. The config value is an EXACT
/// `Decimal` threaded straight through (no f64 round-trip); only the flag (f64) path and the f64
/// profile default go through `from_f64_retain` (L1/L3 — preserve config exactness).
fn resolve_capex(
    flag: Option<f64>,
    config: Option<Decimal>,
    profile_default: f64,
) -> Result<Decimal> {
    match (flag, config) {
        (Some(value), _) => finite_decimal(value, "hardware-price"),
        (None, Some(decimal)) => Ok(decimal),
        (None, None) => Decimal::from_f64_retain(profile_default)
            .ok_or_else(|| anyhow!("hardware price is not a finite number")),
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

    // R6/R8 honesty: the cloud side is a COUNTERFACTUAL list-price estimate (your tokens × the
    // dated catalog list prices), never your actual negotiated cloud bill. The pricing-snapshot id
    // it was computed against is in the assumption stamp below.
    doc.push(StyledLine {
        spans: vec![crate::render::StyledSpan {
            content: "Cloud = counterfactual list-price estimate (your tokens × current list \
                      prices — not your actual cloud bill)."
                .to_string(),
            style: SemanticStyle::Muted,
        }],
    });

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

    /// BLOCKER (M5 T2) — the CROSS-INTERFACE basis lock. The CLI's `energy_only_rate` (over a
    /// `costroid-power` estimate) and the server's `core::local_energy_only_rate` (over a STORED
    /// `local_inference` row, built through the real `local_run_to_focus`) must agree for the same
    /// run. They agree ONLY because both divide energy by the **total** (in+out) token basis: the
    /// row's `x_consumed_tokens` is `tokens_in + tokens_out` (the T2 fix). With `amortized > 0` the
    /// `effective − amortized` subtraction is non-vacuous, and the discriminator below proves the
    /// equality would BREAK on an output-only basis — so a regression of `local_run_to_focus` to
    /// `tokens_out` fails this test.
    #[test]
    fn server_e_from_a_stored_row_equals_the_cli_energy_only_rate() {
        use costroid_core::{focus_records_from_canonical, local_energy_only_rate};
        use costroid_providers::{CanonicalEvent, LocalRunEvent};

        // The same run, expressed for the CLI side: energy_cost 2.0, local_run_cost 5.0 →
        // amortized 3.0 (> 0, non-vacuous), tokens_in 400_000 + tokens_out 600_000 = 1_000_000.
        let report = report_with(2.0, 5.0, 400_000, 600_000);
        let Ok(cli_e) = energy_only_rate(&report) else {
            panic!("the CLI energy-only rate must compute");
        };

        // The same run, expressed for the server side: a stored `local_inference` row built through
        // the REAL `local_run_to_focus` (via `focus_records_from_canonical`). Cost strings are taken
        // from the report so `effective_cost − x_amortized_hw_cost == energy_cost` exactly.
        let (Some(local_run_cost), Some(amortized)) = (
            Decimal::from_f64_retain(report.local_run_cost),
            Decimal::from_f64_retain(report.amortized_hw_cost),
        ) else {
            panic!("the report costs are finite");
        };
        assert!(
            amortized > Decimal::ZERO,
            "amortized must be > 0 (non-vacuous): {amortized}"
        );
        let Some(ts) = chrono::DateTime::from_timestamp(0, 0) else {
            panic!("the unix epoch is a valid timestamp");
        };
        let event = LocalRunEvent {
            timestamp: ts,
            model: report.model.clone(),
            quant: report.quant.clone(),
            runtime_kind: report.runtime_kind.clone(),
            tokens_in: report.tokens_in,
            tokens_out: report.tokens_out,
            run_seconds: report.run_seconds,
            avg_power_watts: report.avg_power_watts,
            measurement_mode: "estimated".to_string(),
            energy_wh: report.energy_wh,
            amortized_hw_cost: amortized.to_string(),
            local_run_cost: local_run_cost.to_string(),
            electricity_rate_per_kwh: report.electricity_rate_per_kwh,
            hardware_price: report.hardware_price,
            hardware_lifetime_seconds: report.hardware_lifetime_seconds,
            hardware_profile_id: report.hardware_profile_stamp.clone(),
            benchmark_id: report.benchmark_id.clone(),
            billing_currency: report.currency.clone(),
        };
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Local(event)]) else {
            panic!("the local run normalizes to a FOCUS row");
        };
        // The stored row carries the TOTAL token basis (the T2 fix) and a positive amortized cost.
        assert_eq!(rows.len(), 1, "a local run is one row");
        assert_eq!(
            rows[0].x_consumed_tokens,
            Decimal::from(1_000_000_u64),
            "x_consumed_tokens must be the TOTAL (in+out) basis"
        );
        assert!(matches!(rows[0].x_amortized_hw_cost, Some(a) if a > Decimal::ZERO));

        let Ok(Some(server_e)) = local_energy_only_rate(&rows) else {
            panic!("the server energy-only rate must compute from the stored row");
        };

        // The basis lock: the two interfaces agree.
        assert!(
            same(server_e, cli_e),
            "server e ({server_e}) must equal the CLI energy_only_rate ({cli_e})"
        );

        // Discriminator: the equality holds because the basis is TOTAL (1_000_000). An output-only
        // basis (600_000) would give a different e — so this test fails if the basis ever regresses.
        let Some(output_only_e) = Decimal::from_f64_retain(report.energy_cost)
            .and_then(|energy| energy.checked_div(Decimal::from(report.tokens_out)))
        else {
            panic!("the output-only rate divides");
        };
        assert!(
            !same(server_e, output_only_e),
            "the total-basis e ({server_e}) must differ from the output-only e ({output_only_e})"
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

    /// Remove every ANSI CSI sequence (`\x1b[...m`) so a styled render can be compared to plain.
    fn strip_ansi(text: &str) -> String {
        let mut out = String::new();
        let mut chars = text.chars();
        while let Some(c) = chars.next() {
            if c == '\u{1b}' {
                for next in chars.by_ref() {
                    if next == 'm' {
                        break;
                    }
                }
            } else {
                out.push(c);
            }
        }
        out
    }

    fn snapshot_stamp() -> AssumptionStamp {
        AssumptionStamp {
            electricity_rate_per_kwh: dec(16, 2),
            hardware_price: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            utilization: Decimal::ONE,
            output_share: dec(5, 1),
            local_energy_per_token: dec(5, 7),
            blended_cloud_per_token: dec(25, 7),
            measurement_mode: "estimated".to_string(),
            hardware_profile: "strix-halo-128gb@2026-06-20".to_string(),
            pricing_snapshot_id: "curated@2026-06-02".to_string(),
            collector_version: "0.6.0".to_string(),
        }
    }

    fn crossover_snapshot_report() -> BreakevenReport {
        // e=0.0000005, capex $2000 / 1000 days = $2/day, c=0.0000025 → V* = 1,000,000.
        let inputs = BreakevenInputs {
            local_energy_per_token: dec(5, 7),
            hardware_capex: UsdAmount::from_usd(Decimal::from(2000)),
            depreciation_period_days: Decimal::from(1000),
            cloud_per_token: dec(25, 7),
            max_tokens_per_day: None,
        };
        let Ok(headline) = breakeven(&inputs) else {
            panic!("the snapshot scenario must be a crossover");
        };
        BreakevenReport {
            headline,
            sensitivity: Vec::new(),
            stamp: snapshot_stamp(),
            cloud_reference: Vec::new(),
        }
    }

    #[test]
    fn styled_and_plain_render_are_byte_identical_minus_ansi() {
        // T8: --plain is the styled render with the ANSI stripped — byte-for-byte. A below-crossover
        // scenario makes the styled render actually emit color (Accent + Warn), so it is non-vacuous.
        let report = crossover_snapshot_report();
        let doc = render_breakeven(&report, "claude-opus-4-8", Some(Decimal::from(500_000)));
        let styled = doc.render(crate::render::RenderOptions {
            mode: crate::render::RenderMode::Ascii,
            ansi: true,
            width: 80,
        });
        let plain = doc.render(RenderOptions::plain());
        assert!(
            styled.contains('\u{1b}'),
            "the styled render must emit ANSI (else this test is vacuous)"
        );
        assert_eq!(
            strip_ansi(&styled),
            plain,
            "stripping ANSI from the styled render must equal the plain render"
        );
    }

    #[test]
    fn plain_crossover_snapshot_is_pinned() {
        // A representative CrossesAt report's --plain output, pinned line-for-line (T8) — catches
        // any format / label / ordering / unit regression.
        let plain = render_breakeven(&crossover_snapshot_report(), "claude-opus-4-8", None)
            .render(RenderOptions::plain());
        let expected_lines = [
            "Local-vs-cloud break-even  (vs claude-opus-4-8)",
            "",
            "Local breaks even at 1000000 tokens/day.",
            "Cloud = counterfactual list-price estimate (your tokens × current list prices — not your actual cloud bill).",
            "",
            "Sensitivity range: 1000000 … 1000000 tokens/day",
            "",
            "Assumptions (estimate — your tokens × current prices):",
            "  electricity: $0.16/kWh",
            "  hardware: $2000",
            "  depreciation: 1000 days",
            "  utilization: 1",
            "  output share: 0.5",
            "  local energy: $0.5/1M tok",
            "  cloud blended: $2.5/1M tok",
            "  measurement: estimated",
            "  hardware profile: strix-halo-128gb@2026-06-20",
            "  pricing snapshot: curated@2026-06-02",
            "  collector: 0.6.0",
            "",
            "Estimate — ranges + methodology, never a single hero number; reconcile against your provider invoice.",
        ];
        assert_eq!(plain, format!("{}\n", expected_lines.join("\n")));
    }

    // ---- M5 T1: the power-gated TUI break-even/comparison overlay ----

    /// A snapshot carrying one `local_inference` row (built through the real `local_run_to_focus`),
    /// so the overlay's comparison facet has actual local spend to report.
    fn snapshot_with_one_local_row() -> EngineSnapshot {
        use costroid_core::focus_records_from_canonical;
        use costroid_providers::{CanonicalEvent, LocalRunEvent};
        let Some(ts) = chrono::DateTime::from_timestamp(0, 0) else {
            panic!("the unix epoch is a valid timestamp");
        };
        let event = LocalRunEvent {
            timestamp: ts,
            model: "gemma-4-26b-a4b".to_string(),
            quant: "Q4_K_M".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in: 1_000,
            tokens_out: 9_000,
            run_seconds: 10.0,
            avg_power_watts: 100.0,
            measurement_mode: "estimated".to_string(),
            energy_wh: 0.5,
            amortized_hw_cost: "0.001".to_string(),
            local_run_cost: "0.003".to_string(),
            electricity_rate_per_kwh: 0.16,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            hardware_profile_id: "strix-halo-128gb@2026-06-20".to_string(),
            benchmark_id: "test".to_string(),
            billing_currency: "USD".to_string(),
        };
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Local(event)]) else {
            panic!("the local run normalizes to a FOCUS row");
        };
        EngineSnapshot {
            generated_at: ts,
            usage_events: Vec::new(),
            focus_rows: rows,
            limit_windows: Vec::new(),
            providers: Vec::new(),
            capabilities: Vec::new(),
        }
    }

    #[test]
    fn overlay_renders_comparison_and_honesty_labels() {
        let snapshot = snapshot_with_one_local_row();
        let plain = breakeven_overlay_document(Some(&snapshot)).render(RenderOptions::plain());
        // The comparison facet over the snapshot's local rows.
        assert!(
            plain.contains("Actual local spend"),
            "comparison spend:\n{plain}"
        );
        assert!(
            plain.contains("Counterfactual cloud"),
            "comparison cloud:\n{plain}"
        );
        // MED6: the counterfactual list-price label; MED7: the estimated measurement mode; plus the
        // pricing-snapshot stamp.
        assert!(
            plain.contains("counterfactual list-price estimate"),
            "MED6 label:\n{plain}"
        );
        assert!(
            plain.contains("measurement: estimated"),
            "MED7 mode:\n{plain}"
        );
        assert!(
            plain.contains("pricing snapshot"),
            "snapshot stamp:\n{plain}"
        );
        // Never color-alone: --plain carries no ANSI escape.
        assert!(
            !plain.contains('\u{1b}'),
            "plain overlay must carry no ANSI"
        );
    }

    #[test]
    fn overlay_renders_the_exact_engine_outcome() {
        // MED8: the overlay shows the engine's EXACT outcome value (recomputed via the same seam),
        // not a fudged or merely "well-formed" verdict.
        let Ok((report, _, _)) = breakeven_report_for(&BreakevenArgs::tui_overlay()) else {
            panic!("the overlay scenario computes");
        };
        let snapshot = snapshot_with_one_local_row();
        let plain = breakeven_overlay_document(Some(&snapshot)).render(RenderOptions::plain());
        match &report.headline {
            BreakevenOutcome::CrossesAt { tokens_per_day } => assert!(
                plain.contains(&format!(
                    "breaks even at {} tokens/day",
                    fmt_tokens(*tokens_per_day)
                )),
                "overlay must show the exact crossover {}:\n{plain}",
                fmt_tokens(*tokens_per_day)
            ),
            BreakevenOutcome::Infeasible {
                v_star,
                max_tokens_per_day,
                ..
            } => assert!(
                plain.contains(&format!("break even at {} tokens/day", fmt_tokens(*v_star)))
                    && plain.contains(&format!("{} tokens/day", fmt_tokens(*max_tokens_per_day))),
                "overlay must show the exact infeasible v_star + ceiling:\n{plain}"
            ),
            BreakevenOutcome::Always => {
                assert!(plain.contains("cheaper at every volume"), "{plain}")
            }
            BreakevenOutcome::Never { .. } => assert!(plain.contains("NEVER"), "{plain}"),
        }
    }

    #[test]
    fn overlay_with_no_local_rows_is_an_honest_empty_state() {
        // MED5: zero local rows → an honest empty state, never a fabricated comparison/free break-even.
        let plain = breakeven_overlay_document(None).render(RenderOptions::plain());
        assert!(
            plain.contains("No local runs recorded yet"),
            "honest empty state:\n{plain}"
        );
        assert!(
            !plain.contains("Actual local spend"),
            "no fabricated comparison without local rows:\n{plain}"
        );
    }

    #[test]
    fn overlay_styled_and_plain_are_byte_identical_minus_ansi() {
        let snapshot = snapshot_with_one_local_row();
        let doc = breakeven_overlay_document(Some(&snapshot));
        let styled = doc.render(crate::render::RenderOptions {
            mode: crate::render::RenderMode::Ascii,
            ansi: true,
            width: 80,
        });
        let plain = doc.render(RenderOptions::plain());
        assert!(
            styled.contains('\u{1b}'),
            "the styled overlay must emit ANSI (else this test is vacuous)"
        );
        assert_eq!(
            strip_ansi(&styled),
            plain,
            "stripping ANSI from the styled overlay must equal the plain render"
        );
    }
}
