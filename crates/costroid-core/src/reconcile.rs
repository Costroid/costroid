//! Estimate-vs-invoice reconciliation engine (T9c).
//!
//! Pure `costroid-core` logic that compares Costroid's **local estimate** (Σ tokens ×
//! bundled prices — ALWAYS an estimate, carried on `FocusRecord.billed_cost`) against the
//! **vendor-billed** cost report T9b's adapters produce (the invoice side — the SOURCE OF
//! TRUTH), per UTC day and per model, surfacing the signed variance with typed, honest
//! labels.
//!
//! The engine NEVER fetches — it consumes the [`vendor_report`](crate::vendor_report)
//! types `costroid-core` itself defines, so it stays pure-core with **no
//! `costroid-connect` dependency** (the dependency direction is `connect → core`, never
//! `core → connect`). A future caller (T10) fetches the report and hands it in.
//!
//! ## Honesty rules (getting any one wrong is a bug)
//!
//! - The local figure is **never** presented as the bill; the vendor invoice is the
//!   source of truth. The engine never silently "corrects" the estimate.
//! - Vendor-side absence is **typed absence**, never a fabricated `$0` ([`BilledAbsence`]).
//!   A `$0` on the *local* side, by contrast, is a genuine estimate ("no local usage
//!   observed") — only the vendor side is guarded against fabricated zeroes.
//! - The vendor report's typed caveats **survive** onto [`CostReconciliation::caveats`],
//!   and OpenAI's per-model best-effort label additionally onto each
//!   [`ModelReconciliation::confidence`]. Flattening either away is a bug.
//! - Money is exact [`UsdAmount`]/[`Decimal`] end to end — never `f64`.
//! - Only the **API lane** is reconciled (the only lane with a vendor invoice);
//!   subscription lanes are out of scope (limits are not summable dollars).
//! - The local estimate is bucketed by **UTC day**, matching the vendor's UTC-midnight
//!   daily buckets (live-confirmed in T9b — §11.5 ✅ T9b) so the two sides line up.
//!
//! ## What the engine does NOT consume
//!
//! It reconciles the **cost** report only. The token-usage report's
//! `responses_api_coverage_unconfirmed` caveat bounds a *token-side* comparison, which
//! this engine does not perform: OpenAI's `costs` bills all traffic (including the
//! Responses API that Codex rides), so the dollar **day totals here are complete** and
//! that caveat does not apply to them. The OpenAI per-model **dollar** figure is still
//! best-effort, which is carried as [`AmountConfidence::DerivedBestEffort`].

use std::collections::{BTreeMap, BTreeSet};

use chrono::NaiveDate;
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};

use costroid_focus::FocusRecord;

use crate::vendor_report::{
    AmountConfidence, CostReportCaveats, CostReportOutcome, MoneyParseError, UsdAmount,
    VendorCostDay, VendorCostReport, VendorReportUnavailable,
};
use crate::CostLane;

// ---------------------------------------------------------------------------
// The local-estimate side
// ---------------------------------------------------------------------------

/// The local-estimate side of a reconciliation: estimated dollars per **(UTC day,
/// model)**, **API lane only** (the only lane with a vendor invoice — subscription lanes
/// are out of scope, §12.13).
///
/// Build it from the FOCUS rows `costroid-core` already computes via
/// [`from_focus_records`](Self::from_focus_records) (which filters to the API lane and
/// buckets by UTC day), or assemble it directly with [`add`](Self::add). The caller
/// (T10) should scope the rows to the **one vendor** under reconciliation before building
/// it — day totals mix models across whatever rows are handed in, so feeding two vendors'
/// rows would compare a cross-vendor estimate against one vendor's invoice.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct LocalCostEstimate {
    // date -> (model -> summed estimate). BTreeMaps keep day/model order deterministic.
    days: BTreeMap<NaiveDate, BTreeMap<String, UsdAmount>>,
}

impl LocalCostEstimate {
    /// An empty estimate.
    pub fn new() -> Self {
        LocalCostEstimate::default()
    }

    /// Add an estimated dollar amount for a model on a UTC day, accumulating into any
    /// existing entry. `Err` only on money overflow (never a panic).
    pub fn add(
        &mut self,
        date: NaiveDate,
        model: &str,
        amount: UsdAmount,
    ) -> Result<(), MoneyParseError> {
        let models = self.days.entry(date).or_default();
        let slot = models.entry(model.to_string()).or_insert(UsdAmount::ZERO);
        *slot = slot
            .checked_add(amount)
            .ok_or_else(|| MoneyParseError::OutOfRange {
                raw: format!("<local estimate {date} {model}>"),
            })?;
        Ok(())
    }

    /// Build the local estimate from FOCUS rows: keep only **API-lane** rows, bucket by
    /// **UTC day** (`charge_period_start.date_naive()`) and `x_Model`, summing the
    /// estimated `billed_cost` (which equals `effective_cost` for a local estimate — there
    /// is no local discount data). `Err` only on money overflow.
    ///
    /// UTC-day bucketing is deliberate: the vendor reports bill in UTC-midnight daily
    /// buckets (live-confirmed in T9b), so the local side must bucket the same way to
    /// compare like with like — this is NOT the local-timezone bucketing the trends view
    /// uses for human-facing day grouping.
    pub fn from_focus_records(rows: &[FocusRecord]) -> Result<Self, MoneyParseError> {
        let mut estimate = LocalCostEstimate::new();
        for (date, model, cost) in api_lane_day_rows(rows) {
            estimate.add(date, model, UsdAmount::from_usd(cost))?;
        }
        Ok(estimate)
    }

    /// The UTC days that carry a local estimate, ascending.
    pub fn dates(&self) -> impl Iterator<Item = NaiveDate> + '_ {
        self.days.keys().copied()
    }

    fn models_on(&self, date: NaiveDate) -> Option<&BTreeMap<String, UsdAmount>> {
        self.days.get(&date)
    }

    /// The summed estimate for a day across all its models (`ZERO` if the day is absent).
    fn day_total(&self, date: NaiveDate) -> UsdAmount {
        match self.days.get(&date) {
            Some(models) => models
                .values()
                .copied()
                .fold(UsdAmount::ZERO, |acc, amount| {
                    // Day total was already proven addable when each model was inserted;
                    // saturate defensively rather than panic if it somehow overflows.
                    acc.checked_add(amount).unwrap_or(acc)
                }),
            None => UsdAmount::ZERO,
        }
    }
}

/// The shared **API-lane + UTC-day** row classification: yield each API-lane row as a
/// `(UTC day, model, estimated $)` tuple, keyed by `charge_period_start.date_naive()`. Both
/// the per-`(day, model)` estimate ([`LocalCostEstimate::from_focus_records`]) and the per-day
/// series ([`api_lane_daily_usd_series`]) build on this so the lane filter + UTC-day bucketing
/// live in ONE place. API-lane only — the only lane with a $ bill (subscription lanes are not a
/// summable dollar, §12.13); the `$` is the estimated `billed_cost`, exact [`Decimal`].
fn api_lane_day_rows(rows: &[FocusRecord]) -> impl Iterator<Item = (NaiveDate, &str, Decimal)> {
    rows.iter().filter_map(|row| {
        if CostLane::from_access_path(&row.x_access_path) != CostLane::Api {
            return None;
        }
        Some((
            row.charge_period_start.date_naive(),
            row.x_model.as_str(),
            row.billed_cost,
        ))
    })
}

/// The per-**UTC-day** API-lane estimated-$ series — the shared daily series the Forecast (T15)
/// and Anomalies (T16) tabs build on, the generalization of
/// [`LocalCostEstimate::from_focus_records`]'s lane+date bucketing (it collapses the per-model
/// detail to a per-day total). Keyed by `charge_period_start.date_naive()` (the same UTC-midnight
/// buckets the vendor reports use), summing the estimated `billed_cost` (exact [`Decimal`], never
/// `f64`); ascending by day (`BTreeMap`).
///
/// A caller projecting a month-end run-rate off this series **must** keep its month-boundary +
/// elapsed-days math on the SAME UTC calendar — never mix a UTC-day sum with a local-month
/// elapsed fraction (§11.5 T15 consistency rule).
pub(crate) fn api_lane_daily_usd_series(rows: &[FocusRecord]) -> BTreeMap<NaiveDate, Decimal> {
    let mut series: BTreeMap<NaiveDate, Decimal> = BTreeMap::new();
    for (date, _model, cost) in api_lane_day_rows(rows) {
        *series.entry(date).or_insert(Decimal::ZERO) += cost;
    }
    series
}

// ---------------------------------------------------------------------------
// The reconciled output
// ---------------------------------------------------------------------------

/// A vendor billed figure for a day or model: the vendor reported an amount, or it is
/// **typed-absent**. The engine NEVER fabricates a `$0` for a vendor gap.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VendorBilled {
    /// The vendor reported this billed amount for the day/model.
    Billed(UsdAmount),
    /// No vendor figure — a typed reason, never `$0`.
    Unavailable(BilledAbsence),
}

/// Why a vendor billed figure is absent for a day/model — typed absence, never `$0`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BilledAbsence {
    /// The whole vendor report could not be produced; carries the vendor's typed reason
    /// (e.g. `NotConnected`, `AuthenticationFailed`, or Gemini's `NoSanctionedStaticKeyApi`).
    ReportUnavailable(VendorReportUnavailable),
    /// The report was produced but does not cover this UTC day (outside the fetched/served
    /// range — history depth or data latency). The vendor billing for the day is unknown,
    /// which is NOT the same as `$0`.
    DayNotCovered,
    /// The report covers this day but attributes no billed dollars to this model (the
    /// vendor folded it into another line or genuinely reported no per-model figure for
    /// it). Distinct from a model the vendor explicitly billed `$0`.
    ModelNotInReport,
}

/// One model's estimate-vs-billed comparison within a UTC day.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelReconciliation {
    /// The model id (as it appears locally and/or in the vendor report — matched by exact
    /// string).
    pub model: String,
    /// Costroid's local estimate for this model that day. A genuine `$0` means "no local
    /// usage observed for this model", which IS a valid estimate.
    pub local_estimate: UsdAmount,
    /// The vendor's billed figure for this model, or typed absence.
    pub vendor_billed: VendorBilled,
    /// Confidence in the vendor's per-model attribution — `Some` only when billed:
    /// [`AmountConfidence::DerivedBestEffort`] for OpenAI's `line_item`-parsed per-model
    /// dollars, [`AmountConfidence::Exact`] for Anthropic's vendor-attributed model.
    pub confidence: Option<AmountConfidence>,
    /// Signed variance `= local_estimate − vendor_billed` (positive ⇒ the estimate exceeds
    /// the invoice; negative ⇒ the invoice exceeds the estimate). `None` when the vendor
    /// side is absent (no fabricated delta).
    pub variance: Option<UsdAmount>,
    /// Variance as a percentage **of the vendor-billed figure** (the source of truth):
    /// `100 × variance / vendor_billed`. `None` when the vendor side is absent OR the
    /// billed figure is `$0` (the percentage is undefined). Full `Decimal` precision —
    /// any rounding for display is T10's.
    pub variance_pct: Option<Decimal>,
}

/// One UTC day's estimate-vs-invoice comparison.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DayReconciliation {
    /// The UTC day.
    pub date: NaiveDate,
    /// The day's total local estimate (API lane, summed across models).
    pub local_estimate: UsdAmount,
    /// The day's total vendor-billed figure, or typed absence.
    pub vendor_billed: VendorBilled,
    /// Signed variance `= local_estimate − vendor_billed` (see [`ModelReconciliation::variance`]).
    pub variance: Option<UsdAmount>,
    /// Variance percentage of the billed figure (see [`ModelReconciliation::variance_pct`]).
    pub variance_pct: Option<Decimal>,
    /// Per-model breakdown — the union of locally-estimated and vendor-billed models for
    /// the day, in model-id order.
    pub by_model: Vec<ModelReconciliation>,
}

/// Whether the vendor side of a reconciliation had a report to compare against.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ReconciledReportStatus {
    /// A vendor report was available and compared per day/model.
    Available,
    /// No vendor report — carries the typed reason. The local estimate still surfaces (so
    /// the user sees their estimate beside a typed "why there is no invoice").
    Unavailable(VendorReportUnavailable),
}

/// The result of reconciling **one vendor's** billed cost report against the local
/// estimate.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CostReconciliation {
    /// Per-UTC-day comparisons, ascending by date — the union of local-estimate days and
    /// vendor-report days.
    pub days: Vec<DayReconciliation>,
    /// The vendor report's typed honesty caveats, carried through **unchanged**:
    /// `priority_tier_absent` (Anthropic totals understate the bill for priority-tier
    /// users) and `per_model_derived_best_effort` (OpenAI per-model dollars are
    /// best-effort). When the report is unavailable these are `default` (all `false`).
    pub caveats: CostReportCaveats,
    /// Whether a vendor report was available at all. When `Unavailable`, every day's
    /// `vendor_billed` is `Unavailable(ReportUnavailable(..))`.
    pub report: ReconciledReportStatus,
}

// ---------------------------------------------------------------------------
// The engine
// ---------------------------------------------------------------------------

/// Reconcile the local estimate against one vendor's billed-cost report outcome.
///
/// - On [`CostReportOutcome::Available`], each UTC day in the union of (local days,
///   vendor days) is compared: day totals and per-model figures, with signed variance and
///   variance percentage. A vendor gap is typed absence (never `$0`).
/// - On [`CostReportOutcome::Unavailable`], the local estimate is still surfaced day by
///   day, but every vendor figure is `Unavailable(ReportUnavailable(reason))` — so e.g.
///   Gemini reconciles to "unavailable", never to a fabricated `$0` delta.
///
/// The vendor report's caveats are carried onto [`CostReconciliation::caveats`] unchanged.
pub fn reconcile_cost(
    local: &LocalCostEstimate,
    outcome: &CostReportOutcome,
) -> CostReconciliation {
    match outcome {
        CostReportOutcome::Available(report) => reconcile_available(local, report),
        CostReportOutcome::Unavailable(reason) => reconcile_unavailable(local, reason),
    }
}

fn reconcile_available(local: &LocalCostEstimate, report: &VendorCostReport) -> CostReconciliation {
    // Index the vendor days by date for lookup.
    let vendor_days: BTreeMap<NaiveDate, &VendorCostDay> =
        report.days.iter().map(|day| (day.date, day)).collect();

    // The union of dates present on either side, ascending (BTreeSet keeps order).
    let mut dates: BTreeSet<NaiveDate> = BTreeSet::new();
    dates.extend(local.dates());
    dates.extend(vendor_days.keys().copied());

    let days = dates
        .into_iter()
        .map(|date| reconcile_day(local, vendor_days.get(&date).copied(), date))
        .collect();

    CostReconciliation {
        days,
        caveats: report.caveats,
        report: ReconciledReportStatus::Available,
    }
}

fn reconcile_unavailable(
    local: &LocalCostEstimate,
    reason: &VendorReportUnavailable,
) -> CostReconciliation {
    let absent = || VendorBilled::Unavailable(BilledAbsence::ReportUnavailable(reason.clone()));

    let days = local
        .dates()
        .map(|date| {
            let by_model = local
                .models_on(date)
                .into_iter()
                .flat_map(|models| models.iter())
                .map(|(model, amount)| ModelReconciliation {
                    model: model.clone(),
                    local_estimate: *amount,
                    vendor_billed: absent(),
                    confidence: None,
                    variance: None,
                    variance_pct: None,
                })
                .collect();
            DayReconciliation {
                date,
                local_estimate: local.day_total(date),
                vendor_billed: absent(),
                variance: None,
                variance_pct: None,
                by_model,
            }
        })
        .collect();

    CostReconciliation {
        days,
        caveats: CostReportCaveats::default(),
        report: ReconciledReportStatus::Unavailable(reason.clone()),
    }
}

fn reconcile_day(
    local: &LocalCostEstimate,
    vendor_day: Option<&VendorCostDay>,
    date: NaiveDate,
) -> DayReconciliation {
    let local_day_total = local.day_total(date);
    let day_billed = match vendor_day {
        Some(day) => VendorBilled::Billed(day.total),
        None => VendorBilled::Unavailable(BilledAbsence::DayNotCovered),
    };
    let (day_variance, day_variance_pct) = variance_of(local_day_total, &day_billed);

    DayReconciliation {
        date,
        local_estimate: local_day_total,
        vendor_billed: day_billed,
        variance: day_variance,
        variance_pct: day_variance_pct,
        by_model: reconcile_models(local.models_on(date), vendor_day),
    }
}

fn reconcile_models(
    local_models: Option<&BTreeMap<String, UsdAmount>>,
    vendor_day: Option<&VendorCostDay>,
) -> Vec<ModelReconciliation> {
    // Vendor per-model figures for the day, indexed by model id.
    let vendor_models: BTreeMap<&str, (UsdAmount, AmountConfidence)> = match vendor_day {
        Some(day) => day
            .by_model
            .iter()
            .map(|m| (m.model.as_str(), (m.amount, m.confidence)))
            .collect(),
        None => BTreeMap::new(),
    };

    // The union of model ids on either side (BTreeSet keeps deterministic order).
    let mut models: BTreeSet<&str> = BTreeSet::new();
    if let Some(local_models) = local_models {
        models.extend(local_models.keys().map(String::as_str));
    }
    models.extend(vendor_models.keys().copied());

    models
        .into_iter()
        .map(|model| {
            let local_estimate = local_models
                .and_then(|m| m.get(model))
                .copied()
                .unwrap_or(UsdAmount::ZERO);

            let (vendor_billed, confidence) = match vendor_day {
                // The report covers the day: a model is either billed, or explicitly not
                // attributed by the vendor (typed absence, not a fabricated $0).
                Some(_) => match vendor_models.get(model) {
                    Some((amount, confidence)) => {
                        (VendorBilled::Billed(*amount), Some(*confidence))
                    }
                    None => (
                        VendorBilled::Unavailable(BilledAbsence::ModelNotInReport),
                        None,
                    ),
                },
                // The report does not cover this day at all.
                None => (
                    VendorBilled::Unavailable(BilledAbsence::DayNotCovered),
                    None,
                ),
            };

            let (variance, variance_pct) = variance_of(local_estimate, &vendor_billed);

            ModelReconciliation {
                model: model.to_string(),
                local_estimate,
                vendor_billed,
                confidence,
                variance,
                variance_pct,
            }
        })
        .collect()
}

/// Signed variance (`local − billed`) and its percentage of the billed figure, computed
/// only when the vendor side is `Billed`. The percentage is `None` when billed is `$0`
/// (undefined) or on the (practically impossible) `Decimal` overflow.
fn variance_of(
    local_estimate: UsdAmount,
    vendor_billed: &VendorBilled,
) -> (Option<UsdAmount>, Option<Decimal>) {
    let billed = match vendor_billed {
        VendorBilled::Billed(amount) => *amount,
        VendorBilled::Unavailable(_) => return (None, None),
    };
    let variance = match local_estimate.checked_sub(billed) {
        Some(variance) => variance,
        None => return (None, None),
    };
    let pct = variance
        .as_usd()
        .checked_div(billed.as_usd())
        .and_then(|ratio| ratio.checked_mul(Decimal::ONE_HUNDRED));
    (Some(variance), pct)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vendor_report::CostLineItem;
    use chrono::{DateTime, TimeZone, Utc};
    use costroid_focus::{FocusAccessPath, TokenType, UnpricedUsage};

    // ---- helpers -------------------------------------------------------------

    #[track_caller]
    fn ok<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }

    fn date(y: i32, m: u32, d: u32) -> NaiveDate {
        match NaiveDate::from_ymd_opt(y, m, d) {
            Some(date) => date,
            None => panic!("invalid test date"),
        }
    }

    fn usd(literal: &str) -> UsdAmount {
        UsdAmount::from_usd(ok(Decimal::from_str_exact(literal)))
    }

    /// A vendor cost day with a single per-model `Exact` line item (the Anthropic shape).
    fn vendor_day(date: NaiveDate, model: &str, dollars: &str) -> VendorCostDay {
        ok(VendorCostDay::from_line_items(
            date,
            vec![CostLineItem {
                label: "usage".to_string(),
                amount: usd(dollars),
                model: Some(model.to_string()),
                cost_type: Some("tokens".to_string()),
                service_tier: Some("standard".to_string()),
                confidence: AmountConfidence::Exact,
            }],
        ))
    }

    fn available(days: Vec<VendorCostDay>, caveats: CostReportCaveats) -> CostReportOutcome {
        CostReportOutcome::Available(VendorCostReport { days, caveats })
    }

    /// A priced API-lane FOCUS row for the local-estimate side, at a given UTC instant.
    fn api_row(at: DateTime<Utc>, model: &str, billed: &str) -> FocusRecord {
        let mut row = ok(FocusRecord::unpriced_usage(UnpricedUsage {
            timestamp: at,
            tool: "claude-code".to_string(),
            model: model.to_string(),
            token_type: TokenType::Input,
            token_count: 1_000,
            project: None,
            access_path: FocusAccessPath::Api,
            service_name: "Claude".to_string(),
            service_provider_name: "Anthropic".to_string(),
            host_provider_name: "Anthropic".to_string(),
            invoice_issuer_name: "Anthropic".to_string(),
            billing_currency: "USD".to_string(),
        }));
        let cost = ok(Decimal::from_str_exact(billed));
        row.billed_cost = cost;
        row.effective_cost = cost;
        row
    }

    fn utc(y: i32, m: u32, d: u32, h: u32, min: u32) -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(y, m, d, h, min, 0) {
            chrono::LocalResult::Single(value) => value,
            _ => panic!("invalid test instant"),
        }
    }

    fn local_with(entries: &[(NaiveDate, &str, &str)]) -> LocalCostEstimate {
        let mut estimate = LocalCostEstimate::new();
        for (date, model, dollars) in entries {
            ok(estimate.add(*date, model, usd(dollars)));
        }
        estimate
    }

    fn day(recon: &CostReconciliation, want: NaiveDate) -> &DayReconciliation {
        match recon.days.iter().find(|d| d.date == want) {
            Some(day) => day,
            None => panic!("no reconciled day for {want}"),
        }
    }

    fn model<'a>(day: &'a DayReconciliation, want: &str) -> &'a ModelReconciliation {
        match day.by_model.iter().find(|m| m.model == want) {
            Some(model) => model,
            None => panic!("no reconciled model {want}"),
        }
    }

    // ---- exact match (zero delta) -------------------------------------------

    #[test]
    fn exact_match_day_has_zero_variance() {
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "claude-sonnet-4-6", "1.50")]);
        let outcome = available(
            vec![vendor_day(d, "claude-sonnet-4-6", "1.50")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        assert_eq!(recon.report, ReconciledReportStatus::Available);

        let day = day(&recon, d);
        assert_eq!(day.local_estimate, usd("1.50"));
        assert_eq!(day.vendor_billed, VendorBilled::Billed(usd("1.50")));
        assert_eq!(day.variance, Some(usd("0")));
        assert_eq!(day.variance_pct, Some(ok(Decimal::from_str_exact("0"))));

        let m = model(day, "claude-sonnet-4-6");
        assert_eq!(m.variance, Some(usd("0")));
        assert_eq!(m.confidence, Some(AmountConfidence::Exact));
    }

    // ---- under / over estimate (signed variance, both directions) -----------

    #[test]
    fn over_estimate_day_has_positive_signed_variance() {
        // Estimate $2.00, billed $1.50 → +$0.50 over, +33.33…% of the bill.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "2.00")]);
        let outcome = available(
            vec![vendor_day(d, "m", "1.50")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let day = day(&recon, d);
        assert_eq!(day.variance, Some(usd("0.50")));
        // 0.50 / 1.50 * 100 = 33.33… — exact Decimal, no float drift, sign positive.
        let pct = match day.variance_pct {
            Some(pct) => pct,
            None => panic!("expected a percentage"),
        };
        assert!(pct > ok(Decimal::from_str_exact("33.3")));
        assert!(pct < ok(Decimal::from_str_exact("33.4")));
    }

    #[test]
    fn under_estimate_day_has_negative_signed_variance() {
        // Estimate $1.00, billed $1.50 → −$0.50 under, −33.33…%.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "1.00")]);
        let outcome = available(
            vec![vendor_day(d, "m", "1.50")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let day = day(&recon, d);
        assert_eq!(day.variance, Some(usd("-0.50")));
        let pct = match day.variance_pct {
            Some(pct) => pct,
            None => panic!("expected a percentage"),
        };
        assert!(pct < ok(Decimal::from_str_exact("-33.3")));
    }

    // ---- typed vendor-side absence (never a fabricated $0) -------------------

    #[test]
    fn local_day_outside_the_vendor_range_is_typed_absence_not_zero() {
        // The vendor report covers June 2 only; the local estimate also has June 1.
        let d1 = date(2026, 6, 1);
        let d2 = date(2026, 6, 2);
        let local = local_with(&[(d1, "m", "1.00"), (d2, "m", "2.00")]);
        let outcome = available(
            vec![vendor_day(d2, "m", "2.00")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);

        let uncovered = day(&recon, d1);
        assert_eq!(uncovered.local_estimate, usd("1.00"));
        assert_eq!(
            uncovered.vendor_billed,
            VendorBilled::Unavailable(BilledAbsence::DayNotCovered)
        );
        assert_eq!(uncovered.variance, None, "no fabricated $0 delta");
        assert_eq!(uncovered.variance_pct, None);
        // Its model also reports DayNotCovered, never a $0 billed.
        let m = model(uncovered, "m");
        assert_eq!(
            m.vendor_billed,
            VendorBilled::Unavailable(BilledAbsence::DayNotCovered)
        );
        assert_eq!(m.variance, None);

        // The covered day still reconciles normally.
        assert_eq!(day(&recon, d2).variance, Some(usd("0")));
    }

    #[test]
    fn model_billed_but_not_locally_estimated_is_a_real_local_zero() {
        // The vendor bills a model the local estimate never saw → local $0 is a genuine
        // estimate (no observed usage), and the variance flags the gap honestly.
        let d = date(2026, 6, 1);
        let local = LocalCostEstimate::new();
        let outcome = available(
            vec![vendor_day(d, "ghost-model", "0.75")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let m = model(day(&recon, d), "ghost-model");
        assert_eq!(m.local_estimate, UsdAmount::ZERO);
        assert_eq!(m.vendor_billed, VendorBilled::Billed(usd("0.75")));
        assert_eq!(m.variance, Some(usd("-0.75")));
    }

    #[test]
    fn model_estimated_locally_but_not_in_vendor_breakdown_is_typed_absence() {
        // The day is covered, but the vendor attributes nothing to this model.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "covered", "1.00"), (d, "uncovered", "0.40")]);
        let outcome = available(
            vec![vendor_day(d, "covered", "1.00")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let m = model(day(&recon, d), "uncovered");
        assert_eq!(m.local_estimate, usd("0.40"));
        assert_eq!(
            m.vendor_billed,
            VendorBilled::Unavailable(BilledAbsence::ModelNotInReport)
        );
        assert_eq!(
            m.variance, None,
            "no fabricated $0 for an un-attributed model"
        );
    }

    #[test]
    fn whole_report_unavailable_surfaces_local_estimate_with_typed_reason() {
        // Gemini / not-connected: every vendor figure is typed-unavailable, never $0,
        // but the local estimate still surfaces.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "1.25")]);
        let outcome =
            CostReportOutcome::Unavailable(VendorReportUnavailable::NoSanctionedStaticKeyApi);

        let recon = reconcile_cost(&local, &outcome);
        assert_eq!(
            recon.report,
            ReconciledReportStatus::Unavailable(VendorReportUnavailable::NoSanctionedStaticKeyApi)
        );
        let day = day(&recon, d);
        assert_eq!(day.local_estimate, usd("1.25"));
        assert_eq!(
            day.vendor_billed,
            VendorBilled::Unavailable(BilledAbsence::ReportUnavailable(
                VendorReportUnavailable::NoSanctionedStaticKeyApi
            ))
        );
        assert_eq!(day.variance, None);
        let m = model(day, "m");
        assert_eq!(m.local_estimate, usd("1.25"));
        assert!(matches!(
            m.vendor_billed,
            VendorBilled::Unavailable(BilledAbsence::ReportUnavailable(_))
        ));
    }

    // ---- caveat survival ----------------------------------------------------

    #[test]
    fn priority_tier_caveat_survives_onto_the_result() {
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "1.00")]);
        let caveats = CostReportCaveats {
            priority_tier_absent: true,
            per_model_derived_best_effort: false,
        };
        let outcome = available(vec![vendor_day(d, "m", "1.00")], caveats);

        let recon = reconcile_cost(&local, &outcome);
        assert!(recon.caveats.priority_tier_absent);
        assert!(!recon.caveats.per_model_derived_best_effort);
    }

    #[test]
    fn openai_per_model_best_effort_caveat_survives_on_result_and_per_model() {
        // The OpenAI shape: per-model dollars derived from `line_item`, best-effort.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "gpt-5.5", "3.00")]);
        let day_billed = ok(VendorCostDay::from_line_items(
            d,
            vec![CostLineItem {
                label: "gpt-5.5, input".to_string(),
                amount: UsdAmount::from_usd(ok(Decimal::from_str_exact("2.40"))),
                model: Some("gpt-5.5".to_string()),
                cost_type: None,
                service_tier: None,
                confidence: AmountConfidence::DerivedBestEffort,
            }],
        ));
        let caveats = CostReportCaveats {
            priority_tier_absent: false,
            per_model_derived_best_effort: true,
        };
        let outcome = available(vec![day_billed], caveats);

        let recon = reconcile_cost(&local, &outcome);
        assert!(recon.caveats.per_model_derived_best_effort);
        let m = model(day(&recon, d), "gpt-5.5");
        assert_eq!(m.confidence, Some(AmountConfidence::DerivedBestEffort));
        assert_eq!(m.variance, Some(usd("0.60"))); // 3.00 − 2.40
    }

    // ---- Decimal precision (no float drift) ---------------------------------

    #[test]
    fn variance_keeps_full_decimal_precision() {
        // Values f64 cannot represent exactly; the variance must be exact.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "0.30")]);
        let outcome = available(
            vec![vendor_day(d, "m", "0.10")],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let day = day(&recon, d);
        // 0.30 − 0.10 = 0.20 exactly (f64 would give 0.19999999999999998).
        assert_eq!(day.variance, Some(usd("0.20")));
    }

    #[test]
    fn variance_pct_is_none_when_billed_is_zero() {
        // Estimate > $0 but the vendor billed $0 (e.g. free credits): percentage is
        // undefined, the signed dollar variance is still reported.
        let d = date(2026, 6, 1);
        let local = local_with(&[(d, "m", "1.00")]);
        let outcome = available(vec![vendor_day(d, "m", "0")], CostReportCaveats::default());

        let recon = reconcile_cost(&local, &outcome);
        let day = day(&recon, d);
        assert_eq!(day.variance, Some(usd("1.00")));
        assert_eq!(day.variance_pct, None);
    }

    // ---- from_focus_records: API-lane filter + UTC-day bucketing ------------

    #[test]
    fn from_focus_records_keeps_api_lane_and_buckets_by_utc_day() {
        let rows = vec![
            // June 1 (UTC), two API rows for the same model → summed.
            api_row(utc(2026, 6, 1, 9, 0), "m", "0.10"),
            api_row(utc(2026, 6, 1, 18, 0), "m", "0.20"),
            // Late June 1 UTC and early June 2 UTC bucket to their UTC days.
            api_row(utc(2026, 6, 1, 23, 30), "m", "0.05"),
            api_row(utc(2026, 6, 2, 0, 30), "m", "0.40"),
        ];
        let estimate = ok(LocalCostEstimate::from_focus_records(&rows));

        // June 1: 0.10 + 0.20 + 0.05 = 0.35; June 2: 0.40.
        assert_eq!(estimate.day_total(date(2026, 6, 1)), usd("0.35"));
        assert_eq!(estimate.day_total(date(2026, 6, 2)), usd("0.40"));
    }

    #[test]
    fn from_focus_records_excludes_subscription_and_unknown_lanes() {
        let mut sub = api_row(utc(2026, 6, 1, 9, 0), "m", "9.99");
        sub.x_access_path = FocusAccessPath::Subscription.as_str().to_string();
        let mut unknown = api_row(utc(2026, 6, 1, 9, 0), "m", "5.55");
        unknown.x_access_path = FocusAccessPath::Unknown.as_str().to_string();
        let api = api_row(utc(2026, 6, 1, 9, 0), "m", "1.00");

        let estimate = ok(LocalCostEstimate::from_focus_records(&[sub, unknown, api]));
        // Only the API-lane row counts.
        assert_eq!(estimate.day_total(date(2026, 6, 1)), usd("1.00"));
    }

    #[test]
    fn from_focus_records_then_reconcile_end_to_end() {
        let rows = vec![
            api_row(utc(2026, 6, 1, 9, 0), "claude-sonnet-4-6", "1.20"),
            api_row(utc(2026, 6, 1, 10, 0), "claude-opus-4-8", "3.00"),
        ];
        let local = ok(LocalCostEstimate::from_focus_records(&rows));
        let d = date(2026, 6, 1);
        let outcome = available(
            vec![ok(VendorCostDay::from_line_items(
                d,
                vec![
                    CostLineItem {
                        label: "sonnet".to_string(),
                        amount: usd("1.00"),
                        model: Some("claude-sonnet-4-6".to_string()),
                        cost_type: Some("tokens".to_string()),
                        service_tier: Some("standard".to_string()),
                        confidence: AmountConfidence::Exact,
                    },
                    CostLineItem {
                        label: "opus".to_string(),
                        amount: usd("3.00"),
                        model: Some("claude-opus-4-8".to_string()),
                        cost_type: Some("tokens".to_string()),
                        service_tier: Some("standard".to_string()),
                        confidence: AmountConfidence::Exact,
                    },
                ],
            ))],
            CostReportCaveats::default(),
        );

        let recon = reconcile_cost(&local, &outcome);
        let day = day(&recon, d);
        // Day total: estimate 4.20 vs billed 4.00 → +0.20.
        assert_eq!(day.local_estimate, usd("4.20"));
        assert_eq!(day.vendor_billed, VendorBilled::Billed(usd("4.00")));
        assert_eq!(day.variance, Some(usd("0.20")));
        // Per model: sonnet over by 0.20, opus exact.
        assert_eq!(model(day, "claude-sonnet-4-6").variance, Some(usd("0.20")));
        assert_eq!(model(day, "claude-opus-4-8").variance, Some(usd("0")));
    }

    #[test]
    fn add_accumulates_same_day_same_model() {
        let d = date(2026, 6, 1);
        let mut estimate = LocalCostEstimate::new();
        ok(estimate.add(d, "m", usd("0.10")));
        ok(estimate.add(d, "m", usd("0.20")));
        assert_eq!(estimate.day_total(d), usd("0.30"));
    }

    // ---- api_lane_daily_usd_series (shared by Forecast/Anomalies) ------------

    #[test]
    fn daily_series_sums_api_lane_per_utc_day_across_models() {
        let rows = vec![
            // June 1 (UTC): two models on the same day → summed into one day total.
            api_row(utc(2026, 6, 1, 9, 0), "sonnet", "0.10"),
            api_row(utc(2026, 6, 1, 18, 0), "opus", "0.20"),
            // Late June 1 UTC + early June 2 UTC bucket to their own UTC days.
            api_row(utc(2026, 6, 1, 23, 30), "sonnet", "0.05"),
            api_row(utc(2026, 6, 2, 0, 30), "opus", "0.40"),
        ];
        let series = api_lane_daily_usd_series(&rows);
        assert_eq!(
            series.get(&date(2026, 6, 1)).copied(),
            Some(usd("0.35").as_usd())
        );
        assert_eq!(
            series.get(&date(2026, 6, 2)).copied(),
            Some(usd("0.40").as_usd())
        );
    }

    #[test]
    fn daily_series_excludes_subscription_and_unknown_lanes() {
        let mut sub = api_row(utc(2026, 6, 1, 9, 0), "m", "9.99");
        sub.x_access_path = FocusAccessPath::Subscription.as_str().to_string();
        let mut unknown = api_row(utc(2026, 6, 1, 9, 0), "m", "5.55");
        unknown.x_access_path = FocusAccessPath::Unknown.as_str().to_string();
        let api = api_row(utc(2026, 6, 1, 9, 0), "m", "1.00");

        let series = api_lane_daily_usd_series(&[sub, unknown, api]);
        // Only the API-lane row contributes, and only its UTC day is present.
        assert_eq!(series.len(), 1);
        assert_eq!(
            series.get(&date(2026, 6, 1)).copied(),
            Some(usd("1.00").as_usd())
        );
    }

    #[test]
    fn daily_series_is_empty_with_no_api_rows() {
        assert!(api_lane_daily_usd_series(&[]).is_empty());
    }
}
