//! Cost-vs-quality benchmark frontier.
//!
//! Loads a bundled, dated, cited benchmark snapshot (`bench/benchmarks.v1.json`),
//! computes the Pareto-efficient frontier *per benchmark*, and overlays the user's
//! actual API-billed model mix and Costroid's own cache-correct spend. It **informs,
//! never prescribes** (ARCHITECTURE.md §2, §9.6): it sees spend + benchmark scores but
//! not task difficulty, so every figure is advisory and carries its sources + date.
//!
//! The benchmark `cost_per_task_usd` is a task-average used only for the reference
//! frontier's cost axis — never for the user's bill. The dollar delta and the user's
//! actual spend always use the pricing catalog / Costroid's cache-correct cost.

use std::collections::{BTreeMap, BTreeSet};

use chrono::{DateTime, NaiveDate, Utc};
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};

use crate::{
    decimal_to_u64, CoreError, CostLane, EngineSnapshot, PricingCatalog, ProviderStatus,
    TokenTotals, PRICING_STATUS_PRICED,
};

const BENCH_SCHEMA_VERSION: &str = "1";

/// The hedge that travels with every re-pricing delta. Cost only, never quality.
const DISCLAIMER_NOTE: &str = "~ cost-only comparison at equal token volume; not a quality claim.";

/// The four token meters Costroid prices, in a stable order.
const METERS: [&str; 4] = ["input", "output", "cache_read", "cache_write"];

fn bundled_benchmarks_json() -> &'static str {
    // Bundled inside this crate (sibling of pricing/) so `cargo package` includes it
    // and the crate publishes standalone — exactly like pricing.v1.json.
    include_str!("../bench/benchmarks.v1.json")
}

// ---------------------------------------------------------------------------
// Bundled-JSON parse structs (private; Deserialize only — mirror PricingTable).
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize)]
struct BenchmarkTable {
    schema_version: String,
    #[serde(default)]
    benchmarks: Vec<Benchmark>,
}

#[derive(Debug, Deserialize)]
struct Benchmark {
    name: String,
    role: String,
    source: String,
    as_of: String,
    #[serde(default)]
    harness: Option<String>,
    cost_note: String,
    #[serde(default)]
    points: Vec<BenchmarkPoint>,
}

#[derive(Debug, Deserialize)]
struct BenchmarkPoint {
    model_id: String,
    label: String,
    score_pct: Decimal,
    #[serde(default)]
    cost_per_task_usd: Option<Decimal>,
    #[serde(default)]
    note: Option<String>,
}

impl BenchmarkTable {
    fn bundled() -> Result<Self, CoreError> {
        Self::from_json(bundled_benchmarks_json())
    }

    fn from_json(value: &str) -> Result<Self, CoreError> {
        // CoreError already owns `From<serde_json::Error>` for the pricing path, so we
        // cannot add a second `#[from]`; map parse errors to BenchValidation by hand.
        let table: BenchmarkTable = serde_json::from_str(value).map_err(|err| {
            CoreError::BenchValidation(format!("benchmark JSON parse error: {err}"))
        })?;
        table.validate()?;
        Ok(table)
    }

    /// Fail-closed validation. A missing/sentinel/unparseable `as_of` is rejected so a
    /// stale or uncited date can never ship (the permanent guard).
    fn validate(&self) -> Result<(), CoreError> {
        if self.schema_version != BENCH_SCHEMA_VERSION {
            return Err(CoreError::BenchValidation(format!(
                "unsupported schema_version {}; expected {}",
                self.schema_version, BENCH_SCHEMA_VERSION
            )));
        }
        if self.benchmarks.is_empty() {
            return Err(CoreError::BenchValidation(
                "bundled benchmark table has no benchmarks".to_string(),
            ));
        }
        for benchmark in &self.benchmarks {
            if benchmark.source.trim().is_empty() {
                return Err(CoreError::BenchValidation(format!(
                    "benchmark {} has an empty source",
                    benchmark.name
                )));
            }
            if NaiveDate::parse_from_str(benchmark.as_of.trim(), "%Y-%m-%d").is_err() {
                return Err(CoreError::BenchValidation(format!(
                    "benchmark {} has an invalid as_of {:?}; expected YYYY-MM-DD",
                    benchmark.name, benchmark.as_of
                )));
            }
            if benchmark.points.is_empty() {
                return Err(CoreError::BenchValidation(format!(
                    "benchmark {} has no points",
                    benchmark.name
                )));
            }
            let mut seen = BTreeSet::new();
            for point in &benchmark.points {
                if !seen.insert(point.model_id.as_str()) {
                    return Err(CoreError::BenchValidation(format!(
                        "benchmark {} has duplicate model_id {}",
                        benchmark.name, point.model_id
                    )));
                }
                if point.score_pct < Decimal::ZERO || point.score_pct > Decimal::from(100) {
                    return Err(CoreError::BenchValidation(format!(
                        "benchmark {} model {} score_pct {} is outside 0..=100",
                        benchmark.name, point.model_id, point.score_pct
                    )));
                }
                if matches!(point.cost_per_task_usd, Some(cost) if cost < Decimal::ZERO) {
                    return Err(CoreError::BenchValidation(format!(
                        "benchmark {} model {} has a negative cost",
                        benchmark.name, point.model_id
                    )));
                }
            }
        }
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Public output types (Serialize; the CLI renders them). Decimal is not Eq, so
// no type carrying a Decimal derives Eq (consistent with AggregateTotals).
// ---------------------------------------------------------------------------

/// One benchmark after dominance is computed.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct BenchFrontier {
    pub name: String,
    pub role: String,
    pub source: String,
    pub as_of: String,
    pub harness: Option<String>,
    pub cost_note: String,
    pub points: Vec<FrontierPoint>,
}

/// A single benchmark point with its frontier standing.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct FrontierPoint {
    pub model_id: String,
    pub label: String,
    pub score_pct: Decimal,
    /// `None` => no published cost (plotted by score only, cost "n/a").
    pub cost_per_task_usd: Option<Decimal>,
    pub standing: FrontierStanding,
    /// `false` for a benchmark model with no pricing-catalog entry (e.g. composer-2.5)
    /// — never a re-pricing target.
    pub priced_in_catalog: bool,
    /// Optional availability caveat, e.g. "Cursor subscription only - no API access".
    pub note: Option<String>,
}

/// Where a point sits relative to its benchmark's cost-quality frontier.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum FrontierStanding {
    OnFrontier,
    /// Dominated by another point (cheaper-or-equal AND higher-or-equal, strictly
    /// better on one axis); carries that point's `model_id`.
    Dominated {
        by: String,
    },
    /// No published cost — cannot be placed on the cost axis; plotted by score only.
    CostUnknown,
}

/// An API-billed model the user actually used, overlaid on the frontiers.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct OverlayModel {
    /// Resolved pricing-catalog key.
    pub model_id: String,
    /// The raw `x_model` string from the logs (for display).
    pub raw_model: String,
    /// Costroid's cache-correct actual spend across this model's API rows.
    pub billed_cost: Decimal,
    pub tokens: TokenTotals,
    /// Per benchmark this model appears on. Empty => on no bundled benchmark (a gap).
    pub appearances: Vec<OverlayAppearance>,
    /// Equal-volume, cost-only re-pricing comparisons vs the frontier targets.
    pub repricing: Vec<RepricingDelta>,
    /// Whether EVERY accumulated row was priced (`x_PricingStatus == "priced"`). When
    /// false the `billed_cost` baseline understates real spend (unpriced rows carry $0),
    /// so no `Computed` re-pricing delta is ever emitted against it — the comparison is
    /// surfaced as a labeled gap, never an invented number.
    pub fully_priced: bool,
}

#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct OverlayAppearance {
    pub benchmark_name: String,
    pub score_pct: Decimal,
    pub standing: FrontierStanding,
}

/// "~$X cheaper/more at equal token volume" — cost only, never a quality claim.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct RepricingDelta {
    pub target_model_id: String,
    pub target_label: String,
    /// `repriced(target) - actual_billed(this model)`, USD. Negative => target cheaper
    /// at the same token volume. Zero (and ignorable) when `status != Computed`.
    pub delta_usd: Decimal,
    pub status: RepricingStatus,
    /// Benchmarks where the target is on-frontier.
    pub on_frontier_in: Vec<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RepricingStatus {
    Computed,
    /// Target lacks a catalog rate for a meter this model used → no number (never invented).
    TargetRateGap,
    /// Target is this same model → not a comparison.
    SameModel,
    /// This model's own spend baseline includes unpriced rows (its $ is a placeholder
    /// zero/undercount) → no delta against it (never compared, never invented).
    BaselineUnpriced,
}

/// The hedge label + the pricing date the re-pricing math used. Per-benchmark sources
/// and dates live on each [`BenchFrontier`].
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct BenchDisclaimer {
    pub note: &'static str,
    pub pricing_as_of: String,
}

/// The full frontier view the CLI renders.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct BenchView {
    pub generated_at: DateTime<Utc>,
    pub frontiers: Vec<BenchFrontier>,
    /// API-billed used models, BTreeMap-ordered. Empty when `no_api_usage`.
    pub overlay: Vec<OverlayModel>,
    /// True when the user has zero API-billed rows: frontiers still render as a
    /// reference, the overlay is empty, and no delta is fabricated.
    pub no_api_usage: bool,
    pub disclaimer: BenchDisclaimer,
    /// Provider detection status (mirrors the snapshot) so the standalone surface can
    /// surface the same provider notes / no-providers guidance as now/trends.
    pub providers: Vec<ProviderStatus>,
}

// ---------------------------------------------------------------------------
// Computation.
// ---------------------------------------------------------------------------

/// Build the frontier view: bundled benchmarks (dominance computed) + an honest
/// API-billed overlay drawn from the existing snapshot's `focus_rows`.
pub fn bench_view(snapshot: &EngineSnapshot) -> Result<BenchView, CoreError> {
    let table = BenchmarkTable::bundled()?;
    let pricing = PricingCatalog::bundled()?;
    let frontiers = build_frontiers(&table, &pricing);
    let disclaimer = BenchDisclaimer {
        note: DISCLAIMER_NOTE,
        pricing_as_of: pricing.as_of.clone(),
    };

    // Group API-lane rows by resolved catalog key. Use the same lane classifier the
    // now/trends summaries use so "your spend" reconciles exactly and can't drift.
    let mut accum: BTreeMap<String, OverlayAccum> = BTreeMap::new();
    for row in &snapshot.focus_rows {
        // §170 dev-tool gate: the frontier "your spend" overlay is developer-tool-only, so a
        // cloud_api/local_inference row never moves `entry.billed_cost` (it stays row-for-row
        // identical to models_view, which gates the same way). No-op at v0.6.0.
        if !crate::is_developer_tool_lane(row) {
            continue;
        }
        if CostLane::from_access_path(&row.x_access_path) != CostLane::Api {
            continue;
        }
        let key = pricing
            .resolve_key(&row.x_model)
            .map(str::to_string)
            .unwrap_or_else(|| row.x_model.clone());
        let entry = accum.entry(key).or_insert_with(|| OverlayAccum {
            raw_model: row.x_model.clone(),
            billed_cost: Decimal::ZERO,
            tokens: TokenTotals::default(),
            fully_priced: true,
        });
        entry.billed_cost += row.billed_cost;
        entry.fully_priced &= row.x_pricing_status == PRICING_STATUS_PRICED;
        entry
            .tokens
            .add(&row.x_token_type, decimal_to_u64(row.x_consumed_tokens));
    }

    if accum.is_empty() {
        return Ok(BenchView {
            generated_at: snapshot.generated_at,
            frontiers,
            overlay: Vec::new(),
            no_api_usage: true,
            disclaimer,
            providers: snapshot.providers.clone(),
        });
    }

    let targets = repricing_targets(&frontiers);
    let overlay = accum
        .into_iter()
        .map(|(model_id, acc)| {
            let appearances = frontier_appearances(&frontiers, &model_id);
            let repricing = repricing_for(
                &model_id,
                &acc.tokens,
                acc.billed_cost,
                acc.fully_priced,
                &targets,
                &pricing,
            );
            OverlayModel {
                model_id,
                raw_model: acc.raw_model,
                billed_cost: acc.billed_cost,
                tokens: acc.tokens,
                appearances,
                repricing,
                fully_priced: acc.fully_priced,
            }
        })
        .collect();

    Ok(BenchView {
        generated_at: snapshot.generated_at,
        frontiers,
        overlay,
        no_api_usage: false,
        disclaimer,
        providers: snapshot.providers.clone(),
    })
}

struct OverlayAccum {
    raw_model: String,
    billed_cost: Decimal,
    tokens: TokenTotals,
    fully_priced: bool,
}

fn build_frontiers(table: &BenchmarkTable, pricing: &PricingCatalog) -> Vec<BenchFrontier> {
    table
        .benchmarks
        .iter()
        .map(|benchmark| {
            let points = benchmark
                .points
                .iter()
                .enumerate()
                .map(|(idx, point)| FrontierPoint {
                    model_id: point.model_id.clone(),
                    label: point.label.clone(),
                    score_pct: point.score_pct,
                    cost_per_task_usd: point.cost_per_task_usd,
                    standing: standing_for(point, &benchmark.points, idx),
                    priced_in_catalog: pricing.model(&point.model_id).is_some(),
                    note: point.note.clone(),
                })
                .collect();
            BenchFrontier {
                name: benchmark.name.clone(),
                role: benchmark.role.clone(),
                source: benchmark.source.clone(),
                as_of: benchmark.as_of.clone(),
                harness: benchmark.harness.clone(),
                cost_note: benchmark.cost_note.clone(),
                points,
            }
        })
        .collect()
}

/// Pareto dominance within one benchmark, over points with a *known* cost.
/// `P` is dominated iff some other `Q` is cheaper-or-equal AND higher-or-equal AND
/// strictly better on at least one axis. A point with no cost is `CostUnknown` and is
/// excluded from the scan (it can't dominate or be dominated without a cost coordinate).
fn standing_for(point: &BenchmarkPoint, points: &[BenchmarkPoint], idx: usize) -> FrontierStanding {
    let Some(cost) = point.cost_per_task_usd else {
        return FrontierStanding::CostUnknown;
    };
    for (other_idx, other) in points.iter().enumerate() {
        if other_idx == idx {
            continue;
        }
        let Some(other_cost) = other.cost_per_task_usd else {
            continue;
        };
        let cheaper_or_equal = other_cost <= cost;
        let higher_or_equal = other.score_pct >= point.score_pct;
        let strictly_better = other_cost < cost || other.score_pct > point.score_pct;
        if cheaper_or_equal && higher_or_equal && strictly_better {
            return FrontierStanding::Dominated {
                by: other.model_id.clone(),
            };
        }
    }
    FrontierStanding::OnFrontier
}

struct RepricingTarget {
    model_id: String,
    label: String,
    on_frontier_in: Vec<String>,
}

/// The re-pricing targets: models that are on-frontier on at least one benchmark AND
/// have a pricing-catalog entry (so composer-2.5, on CursorBench's frontier but absent
/// from the catalog, is never a target — it would have no rates to re-price against).
fn repricing_targets(frontiers: &[BenchFrontier]) -> Vec<RepricingTarget> {
    let mut by_model: BTreeMap<String, RepricingTarget> = BTreeMap::new();
    for frontier in frontiers {
        for point in &frontier.points {
            if point.priced_in_catalog && point.standing == FrontierStanding::OnFrontier {
                by_model
                    .entry(point.model_id.clone())
                    .or_insert_with(|| RepricingTarget {
                        model_id: point.model_id.clone(),
                        label: point.label.clone(),
                        on_frontier_in: Vec::new(),
                    })
                    .on_frontier_in
                    .push(frontier.name.clone());
            }
        }
    }
    by_model.into_values().collect()
}

fn frontier_appearances(frontiers: &[BenchFrontier], model_id: &str) -> Vec<OverlayAppearance> {
    frontiers
        .iter()
        .flat_map(|frontier| {
            frontier
                .points
                .iter()
                .filter(move |point| point.model_id == model_id)
                .map(move |point| OverlayAppearance {
                    benchmark_name: frontier.name.clone(),
                    score_pct: point.score_pct,
                    standing: point.standing.clone(),
                })
        })
        .collect()
}

fn repricing_for(
    model_id: &str,
    tokens: &TokenTotals,
    billed_cost: Decimal,
    fully_priced: bool,
    targets: &[RepricingTarget],
    pricing: &PricingCatalog,
) -> Vec<RepricingDelta> {
    targets
        .iter()
        .map(|target| {
            if target.model_id == model_id {
                return RepricingDelta {
                    target_model_id: target.model_id.clone(),
                    target_label: target.label.clone(),
                    delta_usd: Decimal::ZERO,
                    status: RepricingStatus::SameModel,
                    on_frontier_in: target.on_frontier_in.clone(),
                };
            }
            // An unpriced baseline ($0/undercounted billed_cost) must never anchor a
            // "costs about $X less/more" claim — surface a gap, never an invented delta.
            if !fully_priced {
                return RepricingDelta {
                    target_model_id: target.model_id.clone(),
                    target_label: target.label.clone(),
                    delta_usd: Decimal::ZERO,
                    status: RepricingStatus::BaselineUnpriced,
                    on_frontier_in: target.on_frontier_in.clone(),
                };
            }
            match repriced_total(tokens, &target.model_id, pricing) {
                Some(repriced) => RepricingDelta {
                    target_model_id: target.model_id.clone(),
                    target_label: target.label.clone(),
                    delta_usd: repriced - billed_cost,
                    status: RepricingStatus::Computed,
                    on_frontier_in: target.on_frontier_in.clone(),
                },
                None => RepricingDelta {
                    target_model_id: target.model_id.clone(),
                    target_label: target.label.clone(),
                    delta_usd: Decimal::ZERO,
                    status: RepricingStatus::TargetRateGap,
                    on_frontier_in: target.on_frontier_in.clone(),
                },
            }
        })
        .collect()
}

/// Re-price the user's token volume at the target's catalog rates (same per-token
/// formula as the pricing engine). `None` if the target lacks a rate for a meter the
/// user actually used — surfaced as a gap, never invented.
fn repriced_total(tokens: &TokenTotals, target: &str, pricing: &PricingCatalog) -> Option<Decimal> {
    let million = Decimal::from(1_000_000_u64);
    let mut total = Decimal::ZERO;
    for meter in METERS {
        let volume = meter_volume(tokens, meter);
        if volume == 0 {
            continue;
        }
        let price = pricing.meter_price(target, meter)?;
        total += Decimal::from(volume) * price / million;
    }
    Some(total)
}

fn meter_volume(tokens: &TokenTotals, meter: &str) -> u64 {
    match meter {
        "input" => tokens.input,
        "output" => tokens.output,
        "cache_read" => tokens.cache_read,
        "cache_write" => tokens.cache_write,
        _ => 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::focus_records_from_usage;
    use chrono::TimeZone;
    use costroid_providers::{AccessPath, ProviderId, UsageEvent};

    fn ts() -> DateTime<Utc> {
        // A Wednesday noon — safely inside one ISO week in any timezone, so the
        // now-summary week filter and the (unfiltered) overlay see the same rows.
        match Utc.with_ymd_and_hms(2026, 1, 7, 12, 0, 0) {
            chrono::LocalResult::Single(value) => value,
            _ => panic!("fixed test timestamp should be valid"),
        }
    }

    fn event(model: &str, access: AccessPath, input: u64, output: u64) -> UsageEvent {
        UsageEvent {
            tool: ProviderId::Codex,
            model: model.to_string(),
            timestamp: ts(),
            input_tokens: input,
            output_tokens: output,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: Some("/work/proj".to_string()),
            access_path: access,
        }
    }

    fn snapshot(events: &[UsageEvent]) -> EngineSnapshot {
        let focus_rows = match focus_records_from_usage(events) {
            Ok(rows) => rows,
            Err(err) => panic!("events should price: {err}"),
        };
        EngineSnapshot {
            generated_at: ts(),
            usage_events: Vec::new(),
            focus_rows,
            limit_windows: Vec::new(),
            providers: Vec::new(),
            capabilities: Vec::new(),
        }
    }

    fn frontier<'a>(view: &'a BenchView, name: &str) -> &'a BenchFrontier {
        match view.frontiers.iter().find(|f| f.name == name) {
            Some(f) => f,
            None => panic!("benchmark {name} should be present"),
        }
    }

    fn point<'a>(frontier: &'a BenchFrontier, model_id: &str) -> &'a FrontierPoint {
        match frontier.points.iter().find(|p| p.model_id == model_id) {
            Some(p) => p,
            None => panic!("point {model_id} should be present on {}", frontier.name),
        }
    }

    fn benchmark_point(model_id: &str, score_pct: i64, cost: Option<i64>) -> BenchmarkPoint {
        BenchmarkPoint {
            model_id: model_id.to_string(),
            label: model_id.to_string(),
            score_pct: Decimal::from(score_pct),
            cost_per_task_usd: cost.map(Decimal::from),
            note: None,
        }
    }

    // 1 — bundled data parses + validates, with both benchmarks and real dates.
    #[test]
    fn bundled_benchmarks_parse_and_validate() {
        let table = match BenchmarkTable::bundled() {
            Ok(table) => table,
            Err(err) => panic!("bundled benchmarks should validate: {err}"),
        };
        assert_eq!(table.benchmarks.len(), 2);
        assert_eq!(table.benchmarks[0].name, "DeepSWE v1.1");
        assert_eq!(table.benchmarks[0].as_of, "2026-06-14");
        assert_eq!(table.benchmarks[1].name, "CursorBench v3.1");
        assert_eq!(table.benchmarks[1].as_of, "2026-05-18");
    }

    // 2 — the as_of guard is fail-closed: sentinel / empty / impossible date all reject.
    #[test]
    fn as_of_guard_is_fail_closed() {
        let body = |as_of: &str| {
            format!(
                r#"{{"schema_version":"1","benchmarks":[{{"name":"X","role":"primary","source":"https://x","as_of":"{as_of}","cost_note":"n","points":[{{"model_id":"gpt-5.5","label":"g","score_pct":"70.0","cost_per_task_usd":"1.0"}}]}}]}}"#
            )
        };
        for bad in ["FILL_ME", "", "2026-13-99", "May 30 2026"] {
            match BenchmarkTable::from_json(&body(bad)) {
                Err(CoreError::BenchValidation(_)) => {}
                other => panic!("as_of {bad:?} should be rejected, got {other:?}"),
            }
        }
        // A real date is accepted.
        assert!(BenchmarkTable::from_json(&body("2026-05-30")).is_ok());
    }

    // 3 — frontier correctness on the seeded DeepSWE v1.1 data (the DoD assertion).
    #[test]
    fn deepswe_opus48_is_dominated() {
        let view = match bench_view(&snapshot(&[])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let deepswe = frontier(&view, "DeepSWE v1.1");
        // opus-4.8 (59% / $13.22) is dominated by gpt-5.5 (67% / $7.23) — cheaper AND higher.
        assert_eq!(
            point(deepswe, "claude-opus-4-8").standing,
            FrontierStanding::Dominated {
                by: "gpt-5.5".to_string()
            }
        );
        assert_eq!(
            point(deepswe, "gpt-5.5").standing,
            FrontierStanding::OnFrontier
        );
        // fable-5 tops the board on score → on-frontier even with no pricing-catalog entry.
        assert_eq!(
            point(deepswe, "claude-fable-5").standing,
            FrontierStanding::OnFrontier
        );
        // sonnet-4.6 is the cheapest point at $5.52 → on-frontier (not CostUnknown).
        assert_eq!(
            point(deepswe, "claude-sonnet-4-6").standing,
            FrontierStanding::OnFrontier
        );
        assert!(point(deepswe, "claude-sonnet-4-6")
            .cost_per_task_usd
            .is_some());
    }

    // 3b — a synthetic null-cost point exercises the (seed-unused) CostUnknown path.
    #[test]
    fn cost_unknown_point_is_score_only() {
        let points = vec![
            benchmark_point("gpt-5.5", 70, Some(6)),
            benchmark_point("mystery", 40, None),
        ];
        assert_eq!(
            standing_for(&points[1], &points, 1),
            FrontierStanding::CostUnknown
        );
        // The priced point is unaffected by the cost-unknown one.
        assert_eq!(
            standing_for(&points[0], &points, 0),
            FrontierStanding::OnFrontier
        );
    }

    // 4 — tie handling: equal cost+score keep both on-frontier; strict beats on one axis.
    #[test]
    fn dominance_tie_handling() {
        let tied = vec![
            benchmark_point("a", 50, Some(5)),
            benchmark_point("b", 50, Some(5)),
        ];
        assert_eq!(
            standing_for(&tied[0], &tied, 0),
            FrontierStanding::OnFrontier
        );
        assert_eq!(
            standing_for(&tied[1], &tied, 1),
            FrontierStanding::OnFrontier
        );

        // equal cost, higher score dominates the lower.
        let same_cost = vec![
            benchmark_point("hi", 60, Some(5)),
            benchmark_point("lo", 50, Some(5)),
        ];
        assert_eq!(
            standing_for(&same_cost[1], &same_cost, 1),
            FrontierStanding::Dominated {
                by: "hi".to_string()
            }
        );

        // equal score, cheaper dominates the pricier.
        let same_score = vec![
            benchmark_point("cheap", 50, Some(3)),
            benchmark_point("dear", 50, Some(8)),
        ];
        assert_eq!(
            standing_for(&same_score[1], &same_score, 1),
            FrontierStanding::Dominated {
                by: "cheap".to_string()
            }
        );
    }

    // 5 — API rows only: a subscription row for the same model is excluded from spend.
    #[test]
    fn api_rows_only_excludes_subscription() {
        let view = match bench_view(&snapshot(&[
            event("gpt-5.5", AccessPath::Api, 1_000_000, 0),
            event("gpt-5.5", AccessPath::Subscription, 1_000_000, 0),
        ])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        assert!(!view.no_api_usage);
        assert_eq!(view.overlay.len(), 1);
        // Only the API row's input tokens count; gpt-5.5 input is $5.00 / 1M.
        assert_eq!(view.overlay[0].tokens.input, 1_000_000);
        assert_eq!(view.overlay[0].billed_cost, Decimal::new(500, 2));
    }

    // 5b — note 2: the overlay's API total reconciles with the now-summary API total.
    #[test]
    fn overlay_api_total_reconciles_with_now_summary() {
        let snap = snapshot(&[
            event("gpt-5.5", AccessPath::Api, 1_000_000, 500_000),
            event("claude-opus-4-7", AccessPath::Api, 200_000, 0),
            event("gpt-5.5", AccessPath::Subscription, 999_999, 0),
        ]);
        let view = match bench_view(&snap) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let overlay_total: Decimal = view.overlay.iter().map(|m| m.billed_cost).sum();

        let now = crate::now_summary(&snap, crate::NowOptions::default());
        let now_api_total: Decimal = now
            .current_costs
            .iter()
            .filter(|c| c.lane == CostLane::Api)
            .map(|c| c.totals.billed_cost)
            .sum();

        assert_eq!(overlay_total, now_api_total);
    }

    // 6 — no API usage: reference frontier, empty overlay, zero delta, no fabrication.
    #[test]
    fn no_api_usage_zero_delta_reference() {
        let view = match bench_view(&snapshot(&[event(
            "gpt-5.5",
            AccessPath::Subscription,
            1_000_000,
            0,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        assert!(view.no_api_usage);
        assert!(view.overlay.is_empty());
        assert_eq!(view.frontiers.len(), 2);
    }

    // 7 — re-pricing math: opus volume re-priced at gpt-5.5 catalog rates, exact.
    #[test]
    fn repricing_delta_on_known_volume() {
        let view = match bench_view(&snapshot(&[event(
            "claude-opus-4-7",
            AccessPath::Api,
            1_000_000,
            500_000,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let opus = &view.overlay[0];
        let gpt = match opus
            .repricing
            .iter()
            .find(|d| d.target_model_id == "gpt-5.5")
        {
            Some(delta) => delta,
            None => panic!("gpt-5.5 should be a re-pricing target"),
        };
        assert_eq!(gpt.status, RepricingStatus::Computed);
        // gpt-5.5: input $5.00/1M, output $30.00/1M → 5.00 + 15.00 = $20.00 at this volume.
        assert_eq!(gpt.delta_usd + opus.billed_cost, Decimal::new(2000, 2));
        // opus-4-7 is itself an on-frontier target → SameModel (not a comparison).
        let self_delta = opus
            .repricing
            .iter()
            .find(|d| d.target_model_id == "claude-opus-4-7");
        assert_eq!(
            self_delta.map(|d| d.status),
            Some(RepricingStatus::SameModel)
        );
    }

    // 8 — note 3: composer-2.5 is a gap (no catalog price), never a re-pricing target,
    // and carries its Cursor-only note.
    #[test]
    fn composer_is_a_gap_not_a_target() {
        let view = match bench_view(&snapshot(&[event(
            "claude-opus-4-7",
            AccessPath::Api,
            10,
            0,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let cursorbench = frontier(&view, "CursorBench v3.1");
        let composer = point(cursorbench, "composer-2.5");
        assert!(!composer.priced_in_catalog);
        assert_eq!(
            composer.note.as_deref(),
            Some("Cursor subscription only - no API access")
        );
        for overlay in &view.overlay {
            assert!(
                overlay
                    .repricing
                    .iter()
                    .all(|d| d.target_model_id != "composer-2.5"),
                "composer-2.5 must never be a re-pricing target"
            );
        }
    }

    // 9 — a used model on no benchmark surfaces as a gap (empty appearances), not an error.
    #[test]
    fn missing_model_is_a_gap() {
        let view = match bench_view(&snapshot(&[event(
            "claude-haiku-4-5",
            AccessPath::Api,
            10,
            0,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let haiku = match view
            .overlay
            .iter()
            .find(|m| m.model_id == "claude-haiku-4-5")
        {
            Some(model) => model,
            None => panic!("haiku should be in the overlay"),
        };
        assert!(haiku.appearances.is_empty());
    }

    // 10 — a target missing a rate for a meter the user used is a gap, no number.
    #[test]
    fn repricing_skips_target_rate_gap() {
        // gpt-5.5 (OpenAI) has no cache_write rate; a model that used cache_write
        // cannot be re-priced against it → TargetRateGap, never a fabricated number.
        let mut cache_write_event = event("claude-opus-4-7", AccessPath::Api, 0, 0);
        cache_write_event.cache_write_tokens = 1_000_000;
        let view = match bench_view(&snapshot(&[cache_write_event])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let opus = &view.overlay[0];
        let gpt = match opus
            .repricing
            .iter()
            .find(|d| d.target_model_id == "gpt-5.5")
        {
            Some(delta) => delta,
            None => panic!("gpt-5.5 should appear as a target"),
        };
        assert_eq!(gpt.status, RepricingStatus::TargetRateGap);
    }

    // 11 — the disclaimer carries the cost-only hedge and the pricing date.
    #[test]
    fn disclaimer_carries_hedge_and_pricing_date() {
        let view = match bench_view(&snapshot(&[])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        assert!(view.disclaimer.note.starts_with('~'));
        assert!(view.disclaimer.note.contains("not a quality claim"));
        assert!(!view.disclaimer.pricing_as_of.is_empty());
    }

    // 12 — an unpriced baseline never anchors a Computed delta (never invent a number).
    #[test]
    fn unpriced_baseline_gets_gap_status_never_a_computed_delta() {
        // "mystery-model" is absent from the pricing catalog, so its API rows carry $0
        // placeholder costs (x_PricingStatus = unknown_model). A delta computed against
        // that baseline would fabricate a "costs about $X more/less" claim.
        let view = match bench_view(&snapshot(&[event(
            "mystery-model",
            AccessPath::Api,
            1_000_000,
            200_000,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let mystery = match view
            .overlay
            .iter()
            .find(|model| model.raw_model == "mystery-model")
        {
            Some(model) => model,
            None => panic!("mystery-model should appear in the overlay"),
        };
        assert!(!mystery.fully_priced);
        assert!(mystery
            .repricing
            .iter()
            .all(|delta| delta.status != RepricingStatus::Computed));
        assert!(mystery
            .repricing
            .iter()
            .any(|delta| delta.status == RepricingStatus::BaselineUnpriced));

        // A fully-priced model keeps its Computed comparisons.
        let view = match bench_view(&snapshot(&[event(
            "gpt-5.5",
            AccessPath::Api,
            1_000_000,
            200_000,
        )])) {
            Ok(view) => view,
            Err(err) => panic!("bench_view should build: {err}"),
        };
        let priced = &view.overlay[0];
        assert!(priced.fully_priced);
        assert!(priced
            .repricing
            .iter()
            .any(|delta| delta.status == RepricingStatus::Computed));
    }
}
