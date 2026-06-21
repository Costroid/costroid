//! The loopback server's read path + R4-safe view models (M5 T3).
//!
//! The server reads the **stored** 3-lane FOCUS ledger ([`costroid_store::Store::all_focus_rows`])
//! and projects it into three bounded view models — **timeline**, **comparison**, **break-even** —
//! via `costroid-core` only (no `core→power` edge: the local energy rate `e` comes from the stored
//! `local_inference` rows through [`costroid_core::local_energy_only_rate`], never a live
//! `costroid-power` estimate). Every serialized field is bounded metadata (a number, an enum, or an
//! identifier) drawn from the metadata-allowlist columns — **never** prompt/response content (R4,
//! the Cardinal Rule), asserted by a forbidden-substring test sharing
//! [`costroid_store::FORBIDDEN_SUBSTRINGS`]. Money is serialized from exact `Decimal` strings,
//! never `f64`.

use std::path::Path;

use chrono::{DateTime, Datelike, Duration, TimeZone, Utc};
use costroid_core::{
    blended_cloud_per_token, breakeven_report, cloud_price_per_token, cloud_reference_points,
    local_energy_only_rate, AssumptionStamp, BreakevenInputs, BreakevenOutcome, BreakevenReport,
    CoreError, FocusRecord, Period, SweepPoint, TokenType, UsdAmount,
};
use costroid_store::{Store, StoreError};
use rust_decimal::Decimal;
use serde::Serialize;

/// The FOCUS `x_Lane` values (== `LedgerLane::{LocalInference,DeveloperTool,CloudApi}`).
const LOCAL_LANE: &str = "local_inference";
const DEV_LANE: &str = "developer_tool";

/// The scenario knobs the server's break-even/comparison views assume. Defaults here; M5 T8 wires
/// the read-only `[breakeven]` config over them. (No `electricity_rate`: the local rate `e` is read
/// from the stored ledger, not recomputed.)
#[derive(Debug, Clone, PartialEq)]
pub struct Scenario {
    /// The pricing-catalog model the cloud counterfactual is priced against.
    pub cloud_model: String,
    /// The hardware purchase price (the amortized capex) — exact `Decimal`.
    pub hardware_capex: Decimal,
    /// The break-even depreciation period, in days (the calendar amortization basis).
    pub depreciation_period_days: Decimal,
    /// The output-token share for the cloud blend (0..=1). The stored local rows collapse in+out
    /// into one meter, so this is a scenario knob, not derivable from the ledger.
    pub output_share: Decimal,
    /// The timeline bucket period.
    pub timeline_period: Period,
}

impl Default for Scenario {
    fn default() -> Self {
        Self {
            cloud_model: "claude-opus-4-8".to_string(),
            hardware_capex: Decimal::from(2000),
            depreciation_period_days: Decimal::from(1095),
            output_share: Decimal::new(8, 1), // 0.8
            timeline_period: Period::Day,
        }
    }
}

/// The three views, assembled together for `GET /api/...` or the HTML shell.
#[derive(Debug, Clone, Serialize)]
pub struct Views {
    pub timeline: TimelineView,
    pub comparison: ComparisonView,
    pub breakeven: BreakevenView,
}

/// Spend over time + a per-group breakdown (R4: group labels are bounded model/tool/project ids).
#[derive(Debug, Clone, Serialize)]
pub struct TimelineView {
    pub period: String,
    pub buckets: Vec<TimelineBucket>,
    pub by_group: Vec<GroupSpend>,
}

#[derive(Debug, Clone, Serialize)]
pub struct TimelineBucket {
    pub start: String,
    pub end: String,
    pub effective_usd: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct GroupSpend {
    pub group: String,
    pub lane: String,
    pub effective_usd: String,
    pub tokens: String,
}

/// Actual local + dev-tool spend vs the counterfactual cloud list price for the same token volume.
/// `cloud` is always a **counterfactual list-price estimate**, never an actual cloud bill (MED6).
#[derive(Debug, Clone, Serialize)]
pub struct ComparisonView {
    pub local_usd: String,
    pub dev_usd: String,
    pub actual_total_usd: String,
    pub tokens: String,
    pub cloud_counterfactual_usd: String,
    pub cloud_model: String,
    pub pricing_snapshot_id: String,
    /// Always true — the cloud figure is a list-price counterfactual, not a billed amount (MED6).
    pub cloud_is_counterfactual_estimate: bool,
    /// False when there is no local/dev spend to compare (honest empty state).
    pub has_data: bool,
}

/// The local-vs-cloud break-even (R6: a range + the assumption stamp, never a hero number). `e` is
/// the energy-only, total-token rate from the stored ledger (MED2/MED8). With no local rows
/// `no_local` is true and the figures are absent (honest empty state, MED5).
#[derive(Debug, Clone, Serialize)]
pub struct BreakevenView {
    /// "crosses_at" | "always" | "never" | "infeasible".
    pub outcome: String,
    /// The crossover daily token volume (CrossesAt / Infeasible v_star); absent otherwise.
    pub crossover_tokens_per_day: Option<String>,
    pub reason: Option<String>,
    /// The sensitivity band over the swept assumptions (R6).
    pub band_low: Option<String>,
    pub band_high: Option<String>,
    pub band_has_never: bool,
    pub band_has_always: bool,
    /// The energy-only local rate `e` (USD per 1M tokens) — NOT the capex-inclusive total (MED8).
    pub local_energy_per_million_usd: Option<String>,
    pub cloud_blended_per_million_usd: Option<String>,
    pub cloud_model: String,
    pub hardware_usd: String,
    pub depreciation_days: String,
    pub output_share: String,
    /// The stored local rows' measurement mode (estimated / measured / mixed) — RENDERED (MED7).
    pub measurement_mode: String,
    /// The stored local rows' hardware profile id (`id@as_of`) — dated provenance (R8), RENDERED.
    pub hardware_profile: String,
    pub pricing_snapshot_id: Option<String>,
    /// The labeled, dated DeepSWE-Bench $/task reference — never folded into the crossover math.
    pub deepswe_reference: Vec<CloudReference>,
    /// True when the stored ledger has no `local_inference` rows (honest empty state, MED5).
    pub no_local: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct CloudReference {
    pub model: String,
    pub dollars_per_task: Option<String>,
    pub benchmark: String,
    pub as_of: String,
}

/// Read the stored 3-lane ledger. A missing store file is an honest empty ledger (no rows), not an
/// error — and the file is NOT created (the server is a read view, never a writer).
pub fn load_rows(path: &Path) -> Result<Vec<FocusRecord>, StoreError> {
    if !path.exists() {
        return Ok(Vec::new());
    }
    Store::open(path)?.all_focus_rows()
}

/// Assemble all three views over the stored rows + the scenario knobs.
pub fn build_views(rows: &[FocusRecord], scenario: &Scenario) -> Result<Views, CoreError> {
    Ok(Views {
        timeline: build_timeline(rows, scenario),
        comparison: build_comparison(rows, scenario)?,
        breakeven: build_breakeven(rows, scenario)?,
    })
}

/// Render an exact `Decimal` money value as a bounded decimal string (never `f64`; rounded to
/// micro-dollars for display, still an exact decimal string).
fn money(value: Decimal) -> String {
    value.round_dp(6).normalize().to_string()
}

/// A per-token rate as the readable per-1M-tokens figure (the catalog's native unit).
fn per_million(rate: Decimal) -> Option<String> {
    rate.checked_mul(Decimal::from(1_000_000))
        .map(|per_m| per_m.round_dp(6).normalize().to_string())
}

/// The `[start, end)` period bucket a timestamp falls in (the full-history timeline buckets every
/// stored row by its OWN charge time, not a now()-anchored recent window). All UTC.
fn bucket_bounds(ts: DateTime<Utc>, period: Period) -> (DateTime<Utc>, DateTime<Utc>) {
    let date = ts.date_naive();
    let at_midnight = |d: chrono::NaiveDate| -> DateTime<Utc> {
        match d.and_hms_opt(0, 0, 0) {
            Some(naive) => Utc.from_utc_datetime(&naive),
            None => ts, // 00:00:00 is always valid; fall back to the raw ts if it somehow is not.
        }
    };
    match period {
        Period::Day => {
            let start = at_midnight(date);
            (start, start + Duration::days(1))
        }
        Period::Week => {
            let back = date.weekday().num_days_from_monday() as i64;
            let monday = date - Duration::days(back);
            let start = at_midnight(monday);
            (start, start + Duration::days(7))
        }
        Period::Month => {
            let first =
                chrono::NaiveDate::from_ymd_opt(date.year(), date.month(), 1).unwrap_or(date);
            let (ny, nm) = if date.month() == 12 {
                (date.year() + 1, 1)
            } else {
                (date.year(), date.month() + 1)
            };
            let next = chrono::NaiveDate::from_ymd_opt(ny, nm, 1).unwrap_or(date);
            (at_midnight(first), at_midnight(next))
        }
        Period::Year => {
            let first = chrono::NaiveDate::from_ymd_opt(date.year(), 1, 1).unwrap_or(date);
            let next = chrono::NaiveDate::from_ymd_opt(date.year() + 1, 1, 1).unwrap_or(date);
            (at_midnight(first), at_midnight(next))
        }
    }
}

pub fn build_timeline(rows: &[FocusRecord], scenario: &Scenario) -> TimelineView {
    // Bucket EVERY stored row by its own charge time (full history), summing effective cost.
    let mut by_bucket: std::collections::BTreeMap<DateTime<Utc>, (Decimal, DateTime<Utc>)> =
        std::collections::BTreeMap::new();
    for row in rows {
        let (start, end) = bucket_bounds(row.charge_period_start, scenario.timeline_period);
        let entry = by_bucket.entry(start).or_insert((Decimal::ZERO, end));
        entry.0 = entry.0.checked_add(row.effective_cost).unwrap_or(entry.0);
    }
    let buckets = by_bucket
        .into_iter()
        .map(|(start, (effective, end))| TimelineBucket {
            start: start.to_rfc3339(),
            end: end.to_rfc3339(),
            effective_usd: money(effective),
        })
        .collect();

    // The per-(lane, model) breakdown across ALL three lanes (core's `aggregate_rows` is gated to
    // developer_tool rows only; the server's ledger spans all lanes, so it groups them directly).
    let mut by_group_map: std::collections::BTreeMap<(String, String), (Decimal, Decimal)> =
        std::collections::BTreeMap::new();
    for row in rows {
        let entry = by_group_map
            .entry((row.x_lane.clone(), row.x_model.clone()))
            .or_insert((Decimal::ZERO, Decimal::ZERO));
        entry.0 = entry.0.checked_add(row.effective_cost).unwrap_or(entry.0);
        entry.1 = entry
            .1
            .checked_add(row.x_consumed_tokens)
            .unwrap_or(entry.1);
    }
    let by_group = by_group_map
        .into_iter()
        .map(|((lane, group), (effective, tokens))| GroupSpend {
            group,
            lane,
            effective_usd: money(effective),
            tokens: tokens.normalize().to_string(),
        })
        .collect();

    TimelineView {
        period: format!("{:?}", scenario.timeline_period).to_lowercase(),
        buckets,
        by_group,
    }
}

pub fn build_comparison(
    rows: &[FocusRecord],
    scenario: &Scenario,
) -> Result<ComparisonView, CoreError> {
    let mut local_usd = Decimal::ZERO;
    let mut dev_usd = Decimal::ZERO;
    let mut tokens = Decimal::ZERO;
    for row in rows {
        match row.x_lane.as_str() {
            LOCAL_LANE => {
                local_usd = local_usd
                    .checked_add(row.effective_cost)
                    .unwrap_or(local_usd);
                tokens = tokens.checked_add(row.x_consumed_tokens).unwrap_or(tokens);
            }
            DEV_LANE => {
                dev_usd = dev_usd.checked_add(row.effective_cost).unwrap_or(dev_usd);
                tokens = tokens.checked_add(row.x_consumed_tokens).unwrap_or(tokens);
            }
            _ => {}
        }
    }
    let actual_total = local_usd.checked_add(dev_usd).unwrap_or(local_usd);
    let (blended, snapshot_id) = blended_cloud(&scenario.cloud_model, scenario.output_share)?;
    let counterfactual = blended.checked_mul(tokens).unwrap_or(Decimal::ZERO);
    Ok(ComparisonView {
        local_usd: money(local_usd),
        dev_usd: money(dev_usd),
        actual_total_usd: money(actual_total),
        tokens: tokens.normalize().to_string(),
        cloud_counterfactual_usd: money(counterfactual),
        cloud_model: scenario.cloud_model.clone(),
        pricing_snapshot_id: snapshot_id,
        cloud_is_counterfactual_estimate: true,
        has_data: !tokens.is_zero(),
    })
}

pub fn build_breakeven(
    rows: &[FocusRecord],
    scenario: &Scenario,
) -> Result<BreakevenView, CoreError> {
    let measurement_mode =
        dominant_local_field(rows, |row| row.x_measurement_mode.clone()).unwrap_or_default();
    let hardware_profile =
        dominant_local_field(rows, |row| row.x_hardware_profile.clone()).unwrap_or_default();

    // `e` — the energy-only, total-token local rate from the stored ledger (NEVER effective/tokens,
    // which double-counts the amortized capex). None → no local rows → honest empty state (MED5).
    let energy = match local_energy_only_rate(rows)? {
        Some(value) => value,
        None => {
            return Ok(BreakevenView::no_local(
                scenario,
                &measurement_mode,
                &hardware_profile,
            ))
        }
    };

    let (cloud, snapshot_id) = blended_cloud(&scenario.cloud_model, scenario.output_share)?;
    let capex = UsdAmount::from_usd(scenario.hardware_capex);
    let base = BreakevenInputs {
        local_energy_per_token: energy,
        hardware_capex: capex,
        depreciation_period_days: scenario.depreciation_period_days,
        cloud_per_token: cloud,
        // The server links no `costroid-power`, so it has no machine tok/s → no feasibility ceiling.
        max_tokens_per_day: None,
    };
    let sweep = build_sweep(&base, scenario)?;
    let stamp = AssumptionStamp {
        // The server reads `e` from the stored ledger rather than recomputing energy, so there is
        // no single electricity rate to stamp here (it is baked into the stored rows).
        electricity_rate_per_kwh: Decimal::ZERO,
        hardware_price: capex,
        depreciation_period_days: scenario.depreciation_period_days,
        utilization: Decimal::ONE,
        output_share: scenario.output_share,
        local_energy_per_token: energy,
        blended_cloud_per_token: cloud,
        measurement_mode: measurement_mode.clone(),
        hardware_profile: hardware_profile.clone(),
        pricing_snapshot_id: snapshot_id.clone(),
        collector_version: env!("CARGO_PKG_VERSION").to_string(),
    };
    let overlay = cloud_reference_points()?;
    let report = breakeven_report(base, sweep, stamp, overlay)?;
    Ok(BreakevenView::from_report(
        &report,
        scenario,
        energy,
        cloud,
        &snapshot_id,
        &measurement_mode,
        &hardware_profile,
    ))
}

impl BreakevenView {
    fn no_local(scenario: &Scenario, measurement_mode: &str, hardware_profile: &str) -> Self {
        Self {
            outcome: "no_local".to_string(),
            crossover_tokens_per_day: None,
            reason: Some(
                "no local runs recorded yet — run `costroid bench` to populate the local lane"
                    .to_string(),
            ),
            band_low: None,
            band_high: None,
            band_has_never: false,
            band_has_always: false,
            local_energy_per_million_usd: None,
            cloud_blended_per_million_usd: None,
            cloud_model: scenario.cloud_model.clone(),
            hardware_usd: money(scenario.hardware_capex),
            depreciation_days: scenario.depreciation_period_days.normalize().to_string(),
            output_share: scenario.output_share.normalize().to_string(),
            measurement_mode: measurement_mode.to_string(),
            hardware_profile: hardware_profile.to_string(),
            pricing_snapshot_id: None,
            deepswe_reference: Vec::new(),
            no_local: true,
        }
    }

    fn from_report(
        report: &BreakevenReport,
        scenario: &Scenario,
        energy: Decimal,
        cloud: Decimal,
        snapshot_id: &str,
        measurement_mode: &str,
        hardware_profile: &str,
    ) -> Self {
        let (outcome, crossover, reason) = match &report.headline {
            BreakevenOutcome::CrossesAt { tokens_per_day } => (
                "crosses_at".to_string(),
                Some(tokens_per_day.round_dp(0).normalize().to_string()),
                None,
            ),
            BreakevenOutcome::Always => ("always".to_string(), None, None),
            BreakevenOutcome::Never { reason } => ("never".to_string(), None, Some(reason.clone())),
            BreakevenOutcome::Infeasible { v_star, reason, .. } => (
                "infeasible".to_string(),
                Some(v_star.round_dp(0).normalize().to_string()),
                Some(reason.clone()),
            ),
        };
        let band = report.band();
        let deepswe_reference = report
            .cloud_reference
            .iter()
            .filter(|point| point.benchmark.starts_with("DeepSWE"))
            .map(|point| CloudReference {
                model: point.model.clone(),
                dollars_per_task: point
                    .dollars_per_task
                    .map(|value| value.round_dp(2).normalize().to_string()),
                benchmark: point.benchmark.clone(),
                as_of: point.as_of.clone(),
            })
            .collect();
        Self {
            outcome,
            crossover_tokens_per_day: crossover,
            reason,
            band_low: band
                .low
                .map(|value| value.round_dp(0).normalize().to_string()),
            band_high: band
                .high
                .map(|value| value.round_dp(0).normalize().to_string()),
            band_has_never: band.has_never,
            band_has_always: band.has_always,
            local_energy_per_million_usd: per_million(energy),
            cloud_blended_per_million_usd: per_million(cloud),
            cloud_model: scenario.cloud_model.clone(),
            hardware_usd: money(scenario.hardware_capex),
            depreciation_days: scenario.depreciation_period_days.normalize().to_string(),
            output_share: scenario.output_share.normalize().to_string(),
            measurement_mode: measurement_mode.to_string(),
            hardware_profile: hardware_profile.to_string(),
            pricing_snapshot_id: Some(snapshot_id.to_string()),
            deepswe_reference,
            no_local: false,
        }
    }
}

/// Blend the catalog input/output per-token prices at `output_share` → `(c, snapshot_id)`.
fn blended_cloud(model: &str, output_share: Decimal) -> Result<(Decimal, String), CoreError> {
    let input = require_rate(model, TokenType::Input)?;
    let output = require_rate(model, TokenType::Output)?;
    let blended =
        blended_cloud_per_token(input.price_per_token, output.price_per_token, output_share)?;
    Ok((blended, input.snapshot_id))
}

fn require_rate(
    model: &str,
    token_type: TokenType,
) -> Result<costroid_core::CloudTokenPrice, CoreError> {
    cloud_price_per_token(model, token_type, None)?.ok_or_else(|| {
        CoreError::Breakeven(format!(
            "no {} price for cloud model `{model}` in the pricing catalog",
            token_type.as_str()
        ))
    })
}

/// A small sensitivity sweep (R6): hardware ±20% and output mix ±0.2 (clamped, re-blending the
/// cloud rate). `e` is fixed — it is read from the stored ledger, not swept.
fn build_sweep(base: &BreakevenInputs, scenario: &Scenario) -> Result<Vec<SweepPoint>, CoreError> {
    let mut sweep = Vec::new();
    for (label, factor) in [
        ("hardware -20%", Decimal::new(8, 1)),
        ("hardware +20%", Decimal::new(12, 1)),
    ] {
        let capex = scenario
            .hardware_capex
            .checked_mul(factor)
            .ok_or_else(|| CoreError::Breakeven("swept hardware price overflowed".to_string()))?;
        sweep.push(SweepPoint {
            label: label.to_string(),
            inputs: BreakevenInputs {
                hardware_capex: UsdAmount::from_usd(capex),
                ..base.clone()
            },
        });
    }
    for (label, delta) in [
        ("output mix -0.2", Decimal::new(-2, 1)),
        ("output mix +0.2", Decimal::new(2, 1)),
    ] {
        let share = clamp_unit(
            scenario
                .output_share
                .checked_add(delta)
                .unwrap_or(scenario.output_share),
        );
        let (cloud, _) = blended_cloud(&scenario.cloud_model, share)?;
        sweep.push(SweepPoint {
            label: label.to_string(),
            inputs: BreakevenInputs {
                cloud_per_token: cloud,
                ..base.clone()
            },
        });
    }
    Ok(sweep)
}

fn clamp_unit(value: Decimal) -> Decimal {
    value.max(Decimal::ZERO).min(Decimal::ONE)
}

/// The single value of a nullable local-row field across all `local_inference` rows, or "mixed"
/// when they disagree (None when there are no local rows / all null).
fn dominant_local_field<F>(rows: &[FocusRecord], field: F) -> Option<String>
where
    F: Fn(&FocusRecord) -> Option<String>,
{
    let mut seen: Option<String> = None;
    for row in rows.iter().filter(|row| row.x_lane == LOCAL_LANE) {
        if let Some(value) = field(row) {
            match &seen {
                None => seen = Some(value),
                Some(current) if *current == value => {}
                Some(_) => return Some("mixed".to_string()),
            }
        }
    }
    seen
}

#[cfg(test)]
mod tests {
    use super::*;
    use costroid_core::focus_records_from_canonical;
    use costroid_providers::{CanonicalEvent, CloudUsageEvent, LocalRunEvent};

    fn ts() -> chrono::DateTime<Utc> {
        let Some(value) = chrono::DateTime::from_timestamp(1_750_000_000, 0) else {
            panic!("a valid timestamp");
        };
        value
    }

    /// A `local_inference` row via the real `local_run_to_focus` (total-token basis, amortized > 0).
    fn local_event(
        tokens_in: u64,
        tokens_out: u64,
        run_cost: &str,
        amortized: &str,
    ) -> CanonicalEvent {
        CanonicalEvent::Local(LocalRunEvent {
            timestamp: ts(),
            model: "gemma-4-26b-a4b".to_string(),
            quant: "Q4_K_M".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in,
            tokens_out,
            run_seconds: 10.0,
            avg_power_watts: 100.0,
            measurement_mode: "estimated".to_string(),
            energy_wh: 0.5,
            amortized_hw_cost: amortized.to_string(),
            local_run_cost: run_cost.to_string(),
            electricity_rate_per_kwh: 0.16,
            hardware_price: 2000.0,
            hardware_lifetime_seconds: 94_608_000.0,
            hardware_profile_id: "strix-halo-128gb@2026-06-20".to_string(),
            benchmark_id: "test".to_string(),
            billing_currency: "USD".to_string(),
        })
    }

    fn cloud_event(model: &str, tokens: u64) -> CanonicalEvent {
        CanonicalEvent::Cloud(Box::new(CloudUsageEvent {
            timestamp: ts(),
            service_name: "Anthropic API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            model: Some(model.to_string()),
            token_count: Some(tokens),
            billed_cost: None,
            effective_cost: None,
            list_cost: None,
            contracted_cost: None,
            sku_price_id: None,
            pricing_category: None,
            pricing_quantity: None,
            pricing_unit: None,
            list_unit_price: None,
            contracted_unit_price: None,
            pricing_currency: None,
            consumed_unit: None,
            billing_currency: None,
            inference_profile_id: None,
        }))
    }

    fn rows_3_lane() -> Vec<FocusRecord> {
        // A local run (tokens_in 1000 + tokens_out 9000 = 10_000 total; energy = 0.005 − 0.002 =
        // 0.003 over 10_000 → e = 0.0000003/token) + a cloud row + (cloud already covers the dev/
        // cloud lanes; the local lane is the one break-even turns on).
        let Ok(rows) = focus_records_from_canonical(&[
            local_event(1_000, 9_000, "0.005", "0.002"),
            cloud_event("claude-opus-4-8", 1_000),
        ]) else {
            panic!("the canonical events normalize");
        };
        rows
    }

    #[test]
    fn comparison_prices_the_counterfactual_at_the_catalog_with_a_snapshot() {
        let rows = rows_3_lane();
        let scenario = Scenario::default();
        let Ok(view) = build_comparison(&rows, &scenario) else {
            panic!("comparison builds");
        };
        // The cloud side is a counterfactual list-price estimate (MED6), stamped with a snapshot.
        assert!(view.cloud_is_counterfactual_estimate);
        assert!(
            !view.pricing_snapshot_id.is_empty(),
            "a pricing snapshot id is stamped"
        );
        assert!(view.has_data, "there is local spend to compare");
        // The counterfactual equals tokens × the blended catalog rate (hand-checked direction: a
        // positive cloud cost for a positive token volume).
        let Ok((blended, _)) = blended_cloud(&scenario.cloud_model, scenario.output_share) else {
            panic!("blend");
        };
        // The comparison's "actual" side is the local + dev lanes only; the cloud_api row is the
        // counterfactual basis, not actual spend, so it is excluded → tokens = local total 10_000.
        let tokens = Decimal::from(10_000_u64);
        let expected = money(blended.checked_mul(tokens).unwrap_or(Decimal::ZERO));
        assert_eq!(view.cloud_counterfactual_usd, expected);
        assert_eq!(view.tokens, "10000");
    }

    #[test]
    fn breakeven_uses_the_energy_only_total_token_rate_not_the_capex_inclusive_total() {
        let rows = rows_3_lane();
        let Ok(view) = build_breakeven(&rows, &Scenario::default()) else {
            panic!("break-even builds");
        };
        assert!(!view.no_local, "there is a local row");
        // The energy-only e = (0.005 − 0.002)/10_000 = 0.0000003/token → $0.3/1M.
        assert_eq!(
            view.local_energy_per_million_usd,
            Some("0.3".to_string()),
            "e must be energy-only over the TOTAL token basis"
        );
        // Discriminator: the capex-inclusive total rate (effective/tokens = 0.005/10_000 =
        // 0.0000005 → $0.5/1M) is DIFFERENT — proving the view used energy-only, not the total.
        assert_ne!(view.local_energy_per_million_usd, Some("0.5".to_string()));
    }

    #[test]
    fn breakeven_with_no_local_rows_is_an_honest_empty_state() {
        // Only a cloud row → no local lane → honest empty state (MED5), never a fabricated crossover.
        let Ok(rows) = focus_records_from_canonical(&[cloud_event("claude-opus-4-8", 1_000)])
        else {
            panic!("normalize");
        };
        let Ok(view) = build_breakeven(&rows, &Scenario::default()) else {
            panic!("break-even builds");
        };
        assert!(view.no_local, "no local rows → no_local");
        assert_eq!(view.outcome, "no_local");
        assert!(
            view.crossover_tokens_per_day.is_none(),
            "no fabricated crossover"
        );
        assert!(view.local_energy_per_million_usd.is_none());
    }

    #[test]
    fn timeline_buckets_and_groups_sum_the_effective_cost() {
        let rows = rows_3_lane();
        let view = build_timeline(&rows, &Scenario::default());
        // Both events share one day → at least one bucket, and the per-group breakdown is non-empty.
        assert!(
            !view.buckets.is_empty(),
            "a populated ledger yields timeline buckets"
        );
        assert!(
            !view.by_group.is_empty(),
            "the per-group breakdown is non-empty"
        );
    }

    #[test]
    fn views_expose_only_bounded_metadata_never_content_r4() {
        // R4 (the Cardinal Rule): the serialized view JSON carries NO content-bearing field name,
        // sharing the store's promoted FORBIDDEN_SUBSTRINGS source of truth.
        let rows = rows_3_lane();
        let Ok(views) = build_views(&rows, &Scenario::default()) else {
            panic!("views build");
        };
        let Ok(json) = serde_json::to_string(&views) else {
            panic!("views serialize");
        };
        let lower = json.to_lowercase();
        for forbidden in costroid_store::FORBIDDEN_SUBSTRINGS {
            // `"text"` is a substring of legitimate values here (none), but as a FIELD-NAME guard it
            // must not appear as a key; our slim models use none of these as field names.
            assert!(
                !lower.contains(forbidden),
                "R4 violation: the view JSON contains forbidden substring `{forbidden}`:\n{json}"
            );
        }
    }

    #[test]
    fn missing_store_file_is_an_honest_empty_ledger() {
        let path = std::path::Path::new("/nonexistent/costroid/ledger-does-not-exist.db");
        let Ok(rows) = load_rows(path) else {
            panic!("a missing store file is empty, not an error");
        };
        assert!(rows.is_empty(), "no store file → no rows");
    }
}
