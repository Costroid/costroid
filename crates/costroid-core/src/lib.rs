//! Costroid data pipeline and aggregation interfaces.

use std::collections::{BTreeMap, BTreeSet};

use chrono::{DateTime, Datelike, Duration, Local, LocalResult, NaiveDate, TimeZone, Utc};
use costroid_focus::{
    to_csv_string, to_json_string, FocusAccessPath, FocusError, LedgerLane, TokenType,
    UnpricedUsage, ATTRIBUTION_UNCERTAIN, DEFAULT_BILLING_CURRENCY, PRICING_CATEGORY_STANDARD,
    PRICING_STATUS_MISSING_PRICE, PRICING_UNIT_TOKENS,
};
// Re-export FOCUS's record type from the engine crate: the apps depend on `core`, not on
// `costroid-focus` directly (the dependency arc is `apps → core → {providers, focus}`), so
// a caller that needs to name a normalized row — e.g. the T10c `reconcile` command wiring,
// which scopes `FocusRecord`s by vendor before `LocalCostEstimate::from_focus_records` —
// reaches it here rather than taking a direct `focus` edge.
pub use costroid_focus::FocusRecord;
use costroid_providers::{
    default_providers, read_cursor_config, AccessPath, CanonicalEvent, CloudUsageEvent,
    CursorConfig, LimitStatus, LimitWindow, LocalRunEvent, Provider, UsageEvent,
};
// Re-export the provider-layer types that appear in this crate's PUBLIC API — the
// parameter type of `collect_local_snapshot`/`now_summary` callers need (`HostEnv`),
// the field types of the public `LimitSummary`/`LimitAvailability`
// (`ProviderId`/`LimitKind`/`LimitMeasure`), and the `DataSource`/`AuthMethod`/
// `Capability` lane descriptors carried by the public `ProviderCapabilityView` (so the
// Providers tab can match on each lane's source). Without this a "core-only" consumer
// (the Step 6 taskbar `apps/bar`, which depends only on `costroid-core` —
// ARCHITECTURE) could not name the engine's own public surface and would be forced
// into a direct `costroid-providers` edge. Same rationale as the `FocusRecord`
// re-export above; the rest of `costroid-providers` stays an internal dependency.
pub use costroid_providers::{
    AuthMethod, Capability, DataSource, HostEnv, LimitKind, LimitMeasure, ProviderId,
};
// The FOCUS-import version discriminator appears in this crate's public API
// (`focus_records_from_v12_import`); re-export it so a core-only consumer can name it.
pub use costroid_providers::focus_import::FocusInputVersion;
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};
use thiserror::Error;

mod bench;
pub use bench::{
    bench_view, BenchDisclaimer, BenchFrontier, BenchView, FrontierPoint, FrontierStanding,
    OverlayAppearance, OverlayModel, RepricingDelta, RepricingStatus,
};

pub mod reconcile;
pub use reconcile::{
    reconcile_cost, BilledAbsence, CostReconciliation, DayReconciliation, LocalCostEstimate,
    ModelReconciliation, ReconciledReportStatus, VendorBilled,
};

pub mod vendor_report;
pub use vendor_report::{
    utc_date_from_rfc3339, utc_date_from_unix_seconds, AccessForbiddenHint, AmountConfidence,
    CostLineItem, CostReportCaveats, CostReportOutcome, DateRange, ModelCostAmount,
    ModelTokenUsage, MoneyParseError, UsageReportCaveats, UsageReportOutcome, UsdAmount,
    VendorCostDay, VendorCostReport, VendorReportUnavailable, VendorUsageDay, VendorUsageReport,
    GEMINI_UNAVAILABLE_MESSAGE,
};

const PRICING_STATUS_PRICED: &str = "priced";
const PRICING_STATUS_UNKNOWN_MODEL: &str = "unknown_model";
const PRICING_SCHEMA_VERSION: &str = "1";
const PRICING_UNIT_1M_TOKENS: &str = "1M_tokens";
const UNKNOWN_GROUP_VALUE: &str = "unknown";
const TOTAL_GROUP_VALUE: &str = "total";

/// The cross-check fraction above which a Claude `rate_limits` reading is treated as
/// "high" (mirrors the render layer's `WARN_FRACTION`; ARCHITECTURE).
const HIGH_USAGE_FRACTION: f64 = 0.80;
/// The absolute summed-token floor below which a *high* Claude reading is implausible
/// and demoted to `Unverified` (ARCHITECTURE, the one genuinely-open
/// number — biased low so it only flags "near-max on almost no usage", never a real
/// heavy prompt, and only ever demotes; an under-conservative floor would let a
/// false-100% through, which is the failure being guarded). Tunable.
const UNVERIFIED_TOKEN_FLOOR: u64 = 5_000;

pub fn bundled_pricing_json() -> &'static str {
    // Bundled inside this crate (not the workspace root) so `cargo package`
    // includes it and the crate publishes standalone to crates.io.
    include_str!("../pricing/pricing.v1.json")
}

/// The bundled LiteLLM-derived cloud-API pricing snapshot (M2) — the long-tail tier
/// layered UNDER the curated catalog. A vendored, dated, hashed MIT data artifact (see
/// `pricing/README.md`); never fetched at build or runtime (R8).
pub fn bundled_litellm_pricing_json() -> &'static str {
    include_str!("../pricing/litellm-prices.v1.json")
}

/// The pinned **full** upstream content sha256 the bundled LiteLLM snapshot was derived from
/// (R8). Asserted against the artifact's embedded `content_hash` by the loader test, so CI
/// catches a snapshot swapped to a *different* upstream revision — not only the offline
/// `scripts/check_pricing_snapshots.sh` sidecar check. Bump deliberately when re-pinning
/// (in lockstep with `scripts/refresh_litellm_pricing.py`'s `RAW_SHA256` + the artifact).
pub const LITELLM_SNAPSHOT_CONTENT_HASH: &str =
    "36c8994e4d65edcfe396c64737d90aa0f7f303784067a26dfc2090994c6fde4d";

/// The default user pricing-override path: `$XDG_CONFIG_HOME/costroid/pricing-override.json`,
/// falling back to `~/.config/costroid/pricing-override.json`. `None` when neither
/// `XDG_CONFIG_HOME` nor `HOME` is set (no override location → bundled only). Pure path
/// construction — does not touch the filesystem.
pub fn default_pricing_override_path() -> Option<std::path::PathBuf> {
    let file = std::path::Path::new("costroid").join("pricing-override.json");
    if let Some(xdg) = std::env::var_os("XDG_CONFIG_HOME") {
        if !xdg.is_empty() {
            return Some(std::path::Path::new(&xdg).join(file));
        }
    }
    let home = std::env::var_os("HOME").filter(|h| !h.is_empty())?;
    Some(std::path::Path::new(&home).join(".config").join(file))
}

/// Read a user pricing-override file's content for [`PricingCatalog::layered`].
///
/// - `explicit = Some(path)`: the user pointed at a file — an unreadable path is a typed
///   error (they asked for it).
/// - `explicit = None`: use the [`default_pricing_override_path`]; a missing file (or no
///   path at all) returns `Ok(None)` (zero-config → bundled only). A present-but-unreadable
///   default file is a typed error rather than a silent skip.
///
/// A *malformed* override (bad JSON) surfaces later from [`PricingCatalog::layered`] as a
/// typed [`CoreError`]; the CLI decides whether to treat the default-path case as a warning
/// (fall back to bundled) vs the explicit-path case as fatal.
pub fn read_pricing_override(
    explicit: Option<&std::path::Path>,
) -> Result<Option<String>, CoreError> {
    let path = match explicit {
        Some(path) => path.to_path_buf(),
        None => match default_pricing_override_path() {
            Some(path) if path.exists() => path,
            _ => return Ok(None),
        },
    };
    std::fs::read_to_string(&path).map(Some).map_err(|err| {
        CoreError::PricingValidation(format!(
            "reading pricing override {}: {err}",
            path.display()
        ))
    })
}

pub fn bundled_pricing_value() -> Result<serde_json::Value, CoreError> {
    serde_json::from_str(bundled_pricing_json()).map_err(CoreError::from)
}

pub fn collect_local_snapshot(env: &HostEnv) -> Result<EngineSnapshot, CoreError> {
    collect_snapshot_from_providers(env, default_providers(), Utc::now())
}

/// Compatibility wrapper for the Milestone 2 API.
pub fn local_snapshot(env: &HostEnv) -> Snapshot {
    match collect_local_snapshot(env) {
        Ok(snapshot) => snapshot,
        Err(_) => EngineSnapshot::empty(Utc::now()),
    }
}

pub fn focus_records_from_usage(events: &[UsageEvent]) -> Result<Vec<FocusRecord>, CoreError> {
    let pricing = PricingCatalog::layered_default()?;
    let mut records = Vec::new();
    for event in events {
        push_meter_records(event, &pricing, &mut records)?;
    }
    Ok(records)
}

/// Lane-tagged dispatch: normalize a slice of [`CanonicalEvent`]s into FOCUS rows,
/// routing each variant to its per-lane normalizer so every emitted row carries the
/// correct [`LedgerLane`] (`x_Lane`).
///
/// - [`CanonicalEvent::Tool`] reuses the existing developer-tool path
///   ([`push_meter_records`], exactly as [`focus_records_from_usage`]); its rows are
///   estimates (your tokens × current prices). The lane is `developer_tool`, set at
///   the meter site.
/// - [`CanonicalEvent::Cloud`] becomes one `cloud_api`-lane row via
///   [`cloud_usage_to_focus`]; the cloud bill is authoritative, so a present
///   `billed_cost` is preserved verbatim (not recomputed).
/// - [`CanonicalEvent::Local`] becomes one `local_inference`-lane row via
///   [`local_run_to_focus`]; only the lane + token count are carried (the energy
///   columns are deferred to M3).
///
/// This does not sum across lanes — mixing estimated dev-tool rows with authoritative
/// cloud-bill rows is guarded separately (T6). T5 only produces correctly-laned rows.
pub fn focus_records_from_canonical(
    events: &[CanonicalEvent],
) -> Result<Vec<FocusRecord>, CoreError> {
    let pricing = PricingCatalog::layered_default()?;
    let mut records = Vec::new();
    for event in events {
        match event {
            CanonicalEvent::Tool(usage) => {
                // Reuse the existing developer-tool meter path verbatim — no
                // duplicated meter logic. Lane is `developer_tool` (set by the
                // UnpricedUsage site in `push_meter_records`).
                push_meter_records(usage, &pricing, &mut records)?;
            }
            CanonicalEvent::Cloud(cloud) => {
                records.push(cloud_usage_to_focus(cloud)?);
            }
            CanonicalEvent::Local(local) => {
                records.push(local_run_to_focus(local)?);
            }
        }
    }
    Ok(records)
}

/// Build a single `cloud_api`-lane FOCUS row from a [`CloudUsageEvent`].
///
/// This is the reusable home the M1 v1.2 import bridge (T14) will call. Cloud rows
/// are **source-priced**: the cloud invoice is authoritative, so when `billed_cost`
/// is present it is parsed verbatim into the money type and stamped onto the cost
/// columns with `x_Estimated = false`. When it is absent the row stays on the
/// estimate path (`x_Estimated` remains `true`); no estimate is recomputed here.
/// Parse an optional foreign decimal column (cost / unit-price / quantity) carried as a
/// string into `Decimal`, or a typed `CoreError::Import` — never `f64`, never a panic. A
/// blank/absent cell is `None`.
fn parse_cloud_decimal(field: &str, raw: Option<&str>) -> Result<Option<Decimal>, CoreError> {
    match raw.map(str::trim).filter(|value| !value.is_empty()) {
        Some(value) => Decimal::from_str_exact(value)
            .map(Some)
            .map_err(|err| CoreError::Import(format!("invalid cloud {field} {value:?}: {err}"))),
        None => Ok(None),
    }
}

fn cloud_usage_to_focus(cloud: &CloudUsageEvent) -> Result<FocusRecord, CoreError> {
    // Aggregate cloud row: a single output-token meter via the API access path. NOTE: a
    // FOCUS cloud line is already cost-aggregated per SKU, so Costroid does not split it
    // back into input/output meters — the per-token *rate* it carries (below) is the
    // billable fact; the token-type label stays the aggregate `output`.
    let model = cloud.model.clone().unwrap_or_default();
    let mut row = FocusRecord::unpriced_usage(UnpricedUsage {
        lane: LedgerLane::CloudApi,
        timestamp: cloud.timestamp,
        tool: cloud.service_name.clone(),
        model,
        token_type: TokenType::Output,
        token_count: cloud.token_count.unwrap_or(0),
        project: None,
        access_path: FocusAccessPath::Api,
        service_name: cloud.service_name.clone(),
        service_provider_name: cloud.service_provider_name.clone(),
        host_provider_name: cloud.service_provider_name.clone(),
        invoice_issuer_name: cloud.service_provider_name.clone(),
        // Multi-currency (D3): carry the bill's NATIVE currency, never relabel to USD. A
        // blank/absent BillingCurrency falls back to the ledger default.
        billing_currency: cloud
            .billing_currency
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .unwrap_or(DEFAULT_BILLING_CURRENCY)
            .to_string(),
    })?;

    // Bedrock workload attribution (D4): carry the bounded inference-profile id (the system
    // id only — never a name/tag) when the source provides it. Independent of pricing, so it
    // applies to both source-priced and usage-only Bedrock rows.
    row.x_inference_profile_id = cloud
        .inference_profile_id
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_string);

    if let Some(raw) = cloud.billed_cost.as_deref() {
        let cost = Decimal::from_str_exact(raw.trim()).map_err(|err| {
            CoreError::Import(format!("invalid cloud billed_cost {raw:?}: {err}"))
        })?;
        // Source-authoritative bill. The separate FOCUS cost columns are carried verbatim
        // when present (cost fidelity), else they fall back to BilledCost.
        let effective =
            parse_cloud_decimal("EffectiveCost", cloud.effective_cost.as_deref())?.unwrap_or(cost);
        let list = parse_cloud_decimal("ListCost", cloud.list_cost.as_deref())?.unwrap_or(cost);
        let contracted = parse_cloud_decimal("ContractedCost", cloud.contracted_cost.as_deref())?
            .unwrap_or(cost);
        row.billed_cost = cost;
        row.effective_cost = effective;
        row.list_cost = list;
        row.contracted_cost = contracted;
        row.pricing_currency_effective_cost = effective;
        row.x_estimated = false;
        // The row IS priced (by the source bill, not our catalog), so it must read
        // "priced" — not the unpriced helper's default "missing_price" — or pricing
        // coverage under-reports and a now/window estimate would wrongly skip it. Reuse
        // the existing "priced" constant (a new status would break `window_estimated_usd`
        // + `PricingCoverage::add`, which key on it exactly).
        row.x_pricing_status = PRICING_STATUS_PRICED.to_string();

        // Carry the foreign export's per-token pricing detail (M2 T4 — closes the M1
        // per-token-rate deferral). FOCUS requires the pricing-detail columns be null when
        // SkuPriceId is null, so only populate them when the export carries a SkuPriceId.
        if let Some(sku_price_id) = cloud
            .sku_price_id
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
        {
            let consumed = Decimal::from(cloud.token_count.unwrap_or(0));
            let priced_qty =
                parse_cloud_decimal("PricingQuantity", cloud.pricing_quantity.as_deref())?
                    .unwrap_or(consumed);
            let list_unit = parse_cloud_decimal("ListUnitPrice", cloud.list_unit_price.as_deref())?;
            let contracted_unit = parse_cloud_decimal(
                "ContractedUnitPrice",
                cloud.contracted_unit_price.as_deref(),
            )?;
            row.sku_price_id = Some(sku_price_id.to_string());
            row.pricing_category = Some(
                cloud
                    .pricing_category
                    .clone()
                    .unwrap_or_else(|| PRICING_CATEGORY_STANDARD.to_string()),
            );
            // Normalize the unit label to Costroid's canonical "tokens" (the foreign export
            // may spell it "Tokens"); the numeric rate is what's authoritative.
            row.pricing_unit = Some(PRICING_UNIT_TOKENS.to_string());
            row.pricing_quantity = Some(priced_qty);
            row.consumed_quantity = Some(consumed);
            row.list_unit_price = list_unit;
            row.contracted_unit_price = contracted_unit;
            row.pricing_currency_list_unit_price = list_unit;
            row.pricing_currency_contracted_unit_price = contracted_unit;
        }
    }

    Ok(row)
}

/// Build a single `local_inference`-lane FOCUS row from a [`LocalRunEvent`].
///
/// T5 only tags the lane + carries the consumed-token count (`tokens_out`); the
/// energy/cost custom columns are intentionally left unset.
fn local_run_to_focus(local: &LocalRunEvent) -> Result<FocusRecord, CoreError> {
    // M3 will populate the local-inference energy columns once they land (lean
    // sign-off deferred them); T5 only tags the lane + tokens.
    let row = FocusRecord::unpriced_usage(UnpricedUsage {
        lane: LedgerLane::LocalInference,
        timestamp: local.timestamp,
        tool: local.runtime_kind.clone(),
        model: local.model.clone(),
        token_type: TokenType::Output,
        token_count: local.tokens_out,
        project: None,
        access_path: FocusAccessPath::Unknown,
        service_name: local.runtime_kind.clone(),
        service_provider_name: local.runtime_kind.clone(),
        host_provider_name: local.runtime_kind.clone(),
        invoice_issuer_name: local.runtime_kind.clone(),
        billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
    })?;
    Ok(row)
}

/// Normalize a slice of FOCUS-1.2-imported [`CloudUsageEvent`]s into `cloud_api`-lane
/// FOCUS rows — the M1 v1.2-in / v1.3-out import bridge (T14).
///
/// Each event becomes one row via [`cloud_usage_to_focus`] (T5), reused verbatim — so a
/// **source-priced** row (a present `billed_cost`) keeps its authoritative cost across the
/// FOCUS cost columns with `x_Estimated = false`. A **usage-only** row (no `billed_cost`)
/// takes the catalog estimate path like a local tool log: when its model resolves in the
/// bundled catalog [`apply_pricing`] reprices it (`x_Estimated` stays `true`,
/// `x_PricingStatus = "priced"`); otherwise it stays unpriced. Every emitted row is
/// stamped with the source FOCUS version on `x_FocusInputVersion`.
///
/// Lane separation (T6) holds: these are `cloud_api`-lane rows; the typed guard keeps them
/// out of the developer-tool dollar total.
pub fn focus_records_from_v12_import(
    events: &[CloudUsageEvent],
    source_version: &FocusInputVersion,
) -> Result<Vec<FocusRecord>, CoreError> {
    focus_records_from_v12_import_with_override(events, source_version, None)
}

/// As [`focus_records_from_v12_import`], but layers a user pricing-override file's content
/// (M2 / D5) over the curated + LiteLLM tiers when repricing usage-only rows. `override_json`
/// is the override file's content (the CLI reads it via [`read_pricing_override`]); `None`
/// uses the bundled tiers only. A malformed override is a typed [`CoreError`].
pub fn focus_records_from_v12_import_with_override(
    events: &[CloudUsageEvent],
    source_version: &FocusInputVersion,
    override_json: Option<&str>,
) -> Result<Vec<FocusRecord>, CoreError> {
    let pricing = PricingCatalog::layered(override_json)?;
    let version = source_version.as_str().to_string();
    let mut records = Vec::with_capacity(events.len());
    for event in events {
        let mut row = cloud_usage_to_focus(event)?;
        // Usage-only foreign row (no authoritative bill): estimate it from the bundled
        // catalog like a local log, when the model is known. `cloud_usage_to_focus` already
        // preserved a present `billed_cost` (source-priced), so only the None case reprices.
        if event.billed_cost.is_none() {
            if let Some(rate) = pricing
                .resolve_key(event.model.as_deref().unwrap_or_default())
                .and_then(|key| pricing.rate(key, TokenType::Output))
            {
                apply_pricing(&mut row, rate);
            }
        }
        row.x_focus_input_version = Some(version.clone());
        records.push(row);
    }
    Ok(records)
}

pub fn focus_records_from_local_logs(env: &HostEnv) -> Result<Vec<FocusRecord>, CoreError> {
    Ok(collect_local_snapshot(env)?.focus_rows)
}

pub fn export_focus_json(rows: Vec<FocusRecord>) -> Result<String, CoreError> {
    to_json_string(rows).map_err(CoreError::from)
}

pub fn export_focus_csv(rows: &[FocusRecord]) -> Result<String, CoreError> {
    to_csv_string(rows).map_err(CoreError::from)
}

pub fn now_summary(snapshot: &EngineSnapshot, options: NowOptions) -> NowSummary {
    let cost_period = period_range_for(options.cost_period, snapshot.generated_at);
    let current_costs = summarize_rows(
        snapshot
            .focus_rows
            .iter()
            .filter(|row| cost_period.contains(row.charge_period_start)),
        options.group_by,
    );
    let limits = snapshot
        .limit_windows
        .iter()
        .map(|limit| limit_summary(limit, &snapshot.focus_rows, snapshot.generated_at))
        .collect();

    NowSummary {
        generated_at: snapshot.generated_at,
        cost_period,
        group_by: options.group_by,
        limits,
        current_costs,
        providers: snapshot.providers.clone(),
    }
}

/// The API-lane spend of a now-summary, formatted as the `~`-hedged USD estimate the
/// now-header leads with (e.g. `"~$42.18"`).
///
/// Money stays [`Decimal`] inside the engine; a consumer that must not depend on
/// `rust_decimal` (the Step 6 taskbar `apps/bar`, a core-only consumer — ARCHITECTURE)
/// receives the finished display string rather than the raw `Decimal`. This mirrors the
/// CLI now-header exactly (`apps/cli/src/render.rs`): the sum of the [`CostLane::Api`]
/// rows' `billed_cost`, formatted by `format_money(.., "USD", estimated = true)`. The
/// period spend is always an estimate (your tokens × current prices), never the
/// authoritative bill — so it is always `~`-hedged.
pub fn now_api_spend_display(summary: &NowSummary) -> String {
    let total = summary
        .current_costs
        .iter()
        .filter(|row| row.lane == CostLane::Api)
        .fold(Decimal::ZERO, |sum, row| sum + row.totals.billed_cost);
    format_money_usd(&total, true)
}

/// One model's API-lane spend for the taskbar's per-model breakdown: the model id, its
/// `~`-hedged + estimate-labeled spend string, and its `0.0..=1.0` share of the largest model's
/// spend (for a share bar). Money stays [`Decimal`] in the engine; a `rust_decimal`-free consumer
/// (the Step 6 taskbar `apps/bar`) receives the finished string + the normalized fraction.
#[derive(Debug, Clone, PartialEq)]
pub struct ModelSpend {
    pub model: String,
    /// The `~`-hedged + estimate-labeled spend (e.g. `"~$24.10"`).
    pub spend_display: String,
    /// This model's spend as a fraction of the largest model's spend (`0.0..=1.0`) — the share-bar
    /// length. The top model is always `1.0`; an all-zero set yields all-zero (an honest flat row).
    pub fraction: f64,
}

/// The per-model API-lane spend breakdown of a now-summary, highest spend first (ties broken by
/// model name) — the input a `rust_decimal`-free consumer (the taskbar) needs to paint a colored
/// per-model share list without naming `Decimal`. Mirrors the CLI now-screen's `sorted_lane_rows`
/// ordering + `format_money(.., estimated = true)`; the `fraction` is each row's `billed_cost`
/// normalized to the series max (the same display-scaling pattern as [`forecast_daily_fractions`],
/// no pricing math). Subscription/unknown lanes are excluded (never a summable `$`).
pub fn now_model_spend_breakdown(summary: &NowSummary) -> Vec<ModelSpend> {
    let mut rows: Vec<&CostLaneSummary> = summary
        .current_costs
        .iter()
        .filter(|row| row.lane == CostLane::Api)
        .collect();
    rows.sort_by(|left, right| {
        right
            .totals
            .billed_cost
            .cmp(&left.totals.billed_cost)
            .then_with(|| left.group.value.cmp(&right.group.value))
    });
    let max = rows
        .iter()
        .map(|row| row.totals.billed_cost)
        .max()
        .unwrap_or(Decimal::ZERO);
    rows.into_iter()
        .map(|row| ModelSpend {
            model: row.group.value.clone(),
            spend_display: format_money_usd(&row.totals.billed_cost, true),
            fraction: if max <= Decimal::ZERO {
                0.0
            } else {
                (row.totals.billed_cost / max)
                    .to_f64()
                    .unwrap_or(0.0)
                    .clamp(0.0, 1.0)
            },
        })
        .collect()
}

/// Normalize a Forecast view's daily actual spends to `0.0..=1.0` fractions of the series max —
/// the input a `rust_decimal`-free consumer (the Step 6 taskbar `apps/bar`) needs to paint the
/// daily-spend sparkline without naming `Decimal`. Output is parallel to `days` (one fraction per
/// day, in order). An empty series, or one whose max is `0` (no spend yet), yields all-zero
/// fractions — an honest flat baseline, never a `0/0`. Pure display scaling: no pricing/forecast
/// math (the projection itself is [`forecast_view`]); each `Decimal` stays in the engine.
pub fn forecast_daily_fractions(days: &[ForecastDay]) -> Vec<f64> {
    let max = days
        .iter()
        .map(|day| day.spent_usd)
        .max()
        .unwrap_or(Decimal::ZERO);
    if max <= Decimal::ZERO {
        return vec![0.0; days.len()];
    }
    days.iter()
        .map(|day| {
            (day.spent_usd / max)
                .to_f64()
                .unwrap_or(0.0)
                .clamp(0.0, 1.0)
        })
        .collect()
}

/// Format a USD [`Decimal`] for display as `"$X.XX"` — or `"~$X.XX"` when `estimated` —
/// rounded to cents, with thousands separators.
///
/// The single `Decimal`→display path kept in the engine for `rust_decimal`-free consumers
/// (`apps/bar`, which displays money but never computes it): money stays `Decimal` in core
/// and the bar only renders the string. Mirrors the CLI's private `format_money` USD path
/// (`apps/cli/src/render.rs`).
pub fn format_money_usd(amount: &Decimal, estimated: bool) -> String {
    let prefix = if estimated { "~" } else { "" };
    let rounded = amount.round_dp(2).to_string();
    let (whole, fraction) = match rounded.split_once('.') {
        Some((whole, fraction)) => (whole, fraction),
        None => (rounded.as_str(), ""),
    };
    let mut cents = fraction.chars().take(2).collect::<String>();
    while cents.len() < 2 {
        cents.push('0');
    }
    format!("{prefix}${}.{cents}", group_thousands(whole))
}

/// Format a strictly-positive budget/forecast overshoot as `"~$Z"`, guarding the sub-cent case so
/// a real overshoot never renders as the self-contradictory `~$0.00`: an overshoot that rounds
/// below a cent renders `"<$0.01"` instead (an honest "less than a cent over"). A `Decimal`→string
/// display route for the `rust_decimal`-free taskbar (the over-by value stays `Decimal` in core).
/// Mirrors the CLI's private `format_over_by` (`apps/cli/src/render.rs`).
pub fn format_over_by_usd(over: &Decimal) -> String {
    if over.round_dp(2) == Decimal::ZERO {
        "<$0.01".to_string()
    } else {
        format_money_usd(over, true)
    }
}

/// Format a `0.0..=1.0` token-share [`Decimal`] as a whole-percent string (`"NN%"`), for the
/// `rust_decimal`-free taskbar's Anomalies model-mix callouts (the share stays `Decimal` in core).
/// Mirrors the CLI's `percent(share.to_f64())` exactly (round-half, 0 dp).
pub fn decimal_share_percent(share: &Decimal) -> String {
    format!("{:.0}%", (share.to_f64().unwrap_or(0.0) * 100.0).round())
}

/// The "~N.Nx your norm" multiple to show for an anomaly/spend-spike callout, or `None` to fall
/// back to the descriptive (no-multiple) phrasing. Suppressed when there is no magnitude (the
/// median was `0`), when the DISPLAYED baseline rounds to zero (a multiple over a shown
/// `$0.00`/`0%` would be self-contradictory — the caller passes whether its OWN displayed baseline
/// is zero), or when the multiple rounds to `1.0x` (a flagged-but-tiny move, honest to describe but
/// misleading to quantify). All math stays exact [`Decimal`]; only the final label is a string — a
/// `Decimal`→string route for the `rust_decimal`-free taskbar. Mirrors the CLI's private
/// `anomaly_multiple_phrase`.
pub fn anomaly_multiple_phrase(
    magnitude: Option<&Decimal>,
    baseline_displays_zero: bool,
) -> Option<String> {
    let multiple = magnitude?;
    if baseline_displays_zero {
        return None;
    }
    let rounded = multiple.round_dp(1);
    if rounded <= Decimal::ONE {
        return None;
    }
    Some(rounded.to_string())
}

/// Insert `,` thousands separators into a (possibly signed) integer string. Mirrors the
/// CLI's `with_thousands`.
fn group_thousands(value: &str) -> String {
    let (sign, digits) = value
        .strip_prefix('-')
        .map(|digits| ("-", digits))
        .unwrap_or(("", value));
    let mut reversed = String::new();
    for (index, ch) in digits.chars().rev().enumerate() {
        if index > 0 && index % 3 == 0 {
            reversed.push(',');
        }
        reversed.push(ch);
    }
    let grouped = reversed.chars().rev().collect::<String>();
    format!("{sign}{grouped}")
}

pub fn trends_summary(snapshot: &EngineSnapshot, options: TrendsOptions) -> TrendsSummary {
    let mut buckets = BTreeMap::<(PeriodRange, CostLane, GroupKey), AggregateTotals>::new();

    for row in &snapshot.focus_rows {
        // §170 dev-tool gate: only developer_tool rows feed the trends $ buckets (and thus the
        // CLI's `plain_api_bucket_values` sparkline, which re-sums these buckets by CostLane::Api).
        if !is_developer_tool_lane(row) {
            continue;
        }
        let range = period_range_for(options.period, row.charge_period_start);
        let lane = CostLane::from_access_path(&row.x_access_path);
        let group = group_key(row, options.group_by);
        buckets
            .entry((range, lane, group))
            .or_default()
            .add_row(row);
    }

    let buckets = buckets
        .into_iter()
        .map(|((period, lane, group), totals)| TrendBucket {
            period,
            group,
            lane,
            totals,
        })
        .collect();

    TrendsSummary {
        generated_at: snapshot.generated_at,
        period: options.period,
        group_by: options.group_by,
        buckets,
        totals: summarize_rows(snapshot.focus_rows.iter(), options.group_by),
        providers: snapshot.providers.clone(),
    }
}

/// The Models tab (T12): per API-billed model, fuse the spend + token mix with the
/// bench/frontier overlay (cost-vs-quality standing + equal-volume re-pricing). A pure
/// projection over the existing snapshot — it reuses [`bench_view`] + the same per-row
/// [`AggregateTotals::add_row`] aggregation and resolved-key grouping `bench_view` performs,
/// and introduces NO new pricing/bench math. API-cost rows ONLY (the frontier is API-only,
/// ARCHITECTURE): a subscription-only model never appears. Spend is **lifetime-scoped**
/// (all API usage in the snapshot, not period-scoped) — it reconciles with `trends` (lifetime
/// totals) and the `frontier` overlay, NOT with the period-scoped `now` tab.
///
/// Rows are keyed by the **resolved catalog key** (via [`PricingCatalog::resolve_key`]), the
/// SAME merge `bench_view` uses, so a model's dated snapshots collapse to one row that joins
/// 1:1 to the overlay by `model_id` — Models and Frontier always agree. A model with no
/// bundled-benchmark appearance is surfaced as a GAP (`overlay == None`, or matched with empty
/// `appearances`), never a guessed standing; a benchmarked model can never render as a gap.
pub fn models_view(snapshot: &EngineSnapshot) -> Result<ModelsView, CoreError> {
    let bench = bench_view(snapshot)?;
    let pricing = PricingCatalog::layered_default()?;

    // Aggregate API-lane spend + tokens by RESOLVED catalog key — the SAME grouping +
    // per-row aggregation `bench_view` performs (bench.rs). Keying by the raw `x_model`
    // instead would split a model's dated fragments (e.g. claude-opus-4-7-20251101 +
    // -20251201), leaving the second fragment unmatched against the overlay (which merges them
    // under one key) and rendering a benchmarked model as a gap — the §6 honesty invariant
    // inverted. `resolve_key` falls back to the raw id for a genuinely unknown model, exactly
    // as bench_view does, so the two views stay row-for-row identical.
    let mut by_key: BTreeMap<String, AggregateTotals> = BTreeMap::new();
    for row in &snapshot.focus_rows {
        // §170 dev-tool gate: the Models tab's per-model spend is developer-tool-only.
        if !is_developer_tool_lane(row) {
            continue;
        }
        if CostLane::from_access_path(&row.x_access_path) != CostLane::Api {
            continue;
        }
        let key = pricing
            .resolve_key(&row.x_model)
            .map(str::to_string)
            .unwrap_or_else(|| row.x_model.clone());
        by_key.entry(key).or_default().add_row(row);
    }

    let mut models: Vec<ModelRow> = by_key
        .into_iter()
        .map(|(model, totals)| {
            // 1:1 join by resolved key: the overlay is keyed by the same resolved key, so a
            // benchmarked model always matches its appearances (never a fabricated gap).
            let overlay = bench
                .overlay
                .iter()
                .find(|model_overlay| model_overlay.model_id == model)
                .cloned();
            ModelRow {
                model,
                totals,
                overlay,
            }
        })
        .collect();
    // Highest spend first, then name — mirror `sorted_lane_rows` so the tab orders like trends.
    models.sort_by(|left, right| {
        right
            .totals
            .billed_cost
            .cmp(&left.totals.billed_cost)
            .then_with(|| left.model.cmp(&right.model))
    });

    Ok(ModelsView {
        generated_at: snapshot.generated_at,
        no_api_usage: models.is_empty(),
        models,
        disclaimer: bench.disclaimer,
        providers: bench.providers,
    })
}

/// The Budget tab (T14): compare the user's monthly $ target(s) against ACTUAL API-lane
/// spend in the current calendar month — the local estimate (always `~`-hedged), NOT an
/// invoice (`costroid reconcile` is the invoice-true surface). Pure + **config-neutral**:
/// the targets are an INPUT ([`BudgetTargets`]); core never reads a file.
///
/// **API-lane ONLY** (§170, lanes-never-summed): subscription rows never contribute a dollar
/// (limits are not a summable bill), and a budgeted tool that has local usage but NO API lane is
/// surfaced in [`BudgetView::excluded_tools`] — never as a fabricated `$0 / target` row. That
/// covers both a flat-fee *subscription* tool (subscription-lane rows) and a tool whose rows are
/// merely *unclassified* (the `UnknownAccess` lane — e.g. a Codex/Claude install with no
/// rate-limit/credential signal): neither is API-billed, so a $ budget can't apply. A tool with
/// NO local usage at all is left as a legitimate `$0 / target` row (the user may be planning
/// ahead). Spend is scoped to the current month so it can be compared against a *monthly* cap;
/// the not-API-billed classification looks at the tool's whole local history (its billing
/// *nature*, not just this month). Every figure is an estimate.
pub fn budget_view(snapshot: &EngineSnapshot, targets: &BudgetTargets) -> BudgetView {
    let month = period_range_for(Period::Month, snapshot.generated_at);

    let mut month_api_by_tool: BTreeMap<String, Decimal> = BTreeMap::new();
    let mut month_api_total = Decimal::ZERO;
    // Lifetime classification (the tool's billing *nature*): does it ever bill via API, and what
    // non-API usage does it have (a flat-fee subscription, or merely unclassified rows)? Drives
    // the §170 not-API-billed guard below.
    let mut tools_with_api: BTreeSet<String> = BTreeSet::new();
    let mut tools_with_subscription: BTreeSet<String> = BTreeSet::new();
    let mut tools_with_unknown: BTreeSet<String> = BTreeSet::new();

    for row in &snapshot.focus_rows {
        // §170 dev-tool gate: the Budget tab compares a $ target against developer-tool API spend
        // only — a cloud_api/local_inference row is on its own lane, never this monthly $ figure.
        if !is_developer_tool_lane(row) {
            continue;
        }
        match CostLane::from_access_path(&row.x_access_path) {
            CostLane::Api => {
                tools_with_api.insert(row.x_tool.clone());
                if month.contains(row.charge_period_start) {
                    *month_api_by_tool
                        .entry(row.x_tool.clone())
                        .or_insert(Decimal::ZERO) += row.billed_cost;
                    month_api_total += row.billed_cost;
                }
            }
            CostLane::SubscriptionEstimate => {
                tools_with_subscription.insert(row.x_tool.clone());
            }
            CostLane::UnknownAccess => {
                tools_with_unknown.insert(row.x_tool.clone());
            }
        }
    }

    let month_elapsed_fraction = period_elapsed_fraction(&month, snapshot.generated_at);

    let mut rows: Vec<BudgetRow> = Vec::new();
    let mut excluded_tools: Vec<BudgetExcludedTool> = Vec::new();

    for (tool, target) in &targets.per_tool {
        // A non-positive cap is meaningless (it would divide by zero) — skip it rather than
        // fabricate a row.
        if *target <= Decimal::ZERO {
            continue;
        }
        // §170 guard: a tool with local usage but NO API lane has no API bill to budget —
        // withhold the $ comparison. Distinguish a flat-fee subscription (assertable) from a
        // merely-unclassified install (honest "not API-billed", no subscription claim). A tool
        // with NO local usage at all is NOT excluded: it stays a legitimate $0/target row.
        if !tools_with_api.contains(tool) {
            let has_subscription = tools_with_subscription.contains(tool);
            if has_subscription || tools_with_unknown.contains(tool) {
                let reason = if has_subscription {
                    BudgetExclusion::FlatFeeSubscription
                } else {
                    BudgetExclusion::NotApiBilled
                };
                excluded_tools.push(BudgetExcludedTool {
                    tool: tool.clone(),
                    reason,
                });
                continue;
            }
        }
        let spent = month_api_by_tool
            .get(tool)
            .copied()
            .unwrap_or(Decimal::ZERO);
        rows.push(budget_row(
            BudgetScope::Tool(tool.clone()),
            *target,
            spent,
            month_elapsed_fraction,
        ));
    }

    if let Some(total) = targets.total_monthly_usd {
        if total > Decimal::ZERO {
            rows.push(budget_row(
                BudgetScope::Total,
                total,
                month_api_total,
                month_elapsed_fraction,
            ));
        }
    }

    // Most-utilized budget first (the most-pressing on top); ties broken by scope key for a
    // stable order. `fraction` is finite (target > 0, spent >= 0), so the compare sees no NaN.
    rows.sort_by(|left, right| {
        right
            .fraction
            .partial_cmp(&left.fraction)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| budget_scope_key(&left.scope).cmp(&budget_scope_key(&right.scope)))
    });
    excluded_tools.sort_by(|left, right| left.tool.cmp(&right.tool));

    BudgetView {
        generated_at: snapshot.generated_at,
        no_budget_set: targets.is_empty(),
        rows,
        excluded_tools,
        spent_total_usd: month_api_total,
        month_elapsed_fraction,
    }
}

/// The minimum number of days of the current (UTC) month that must have elapsed before a linear
/// run-rate projection is trustworthy. Below it, the Forecast tab shows an honest insufficient-data
/// state rather than extrapolate a wild month-end off one or two days (§11.5 T15 — the 3-day floor).
const MIN_FORECAST_DAYS: u32 = 3;

/// The Forecast tab (T15): project this month's API-lane $ spend and per-quota-window exhaustion
/// ETAs, all labeled estimates — "~$X projected API spend this month" + "hit your weekly Claude
/// limit ~Friday". A pure projection over the snapshot; **config-neutral**, **pure-local**, no
/// network.
///
/// **$ projection = a linear run-rate over the elapsed month**
/// (`spend_to_date / days_elapsed × days_in_month`) off the shared per-**UTC-day** API-lane $
/// series ([`reconcile::api_lane_daily_usd_series`]). The numerator (spend-to-date) AND the
/// denominator (days-elapsed/in-month) are taken from the SAME **UTC** calendar as that series —
/// never mixing a UTC-day sum with the local-month boundaries `budget_view` uses (§11.5 T15
/// consistency rule). Suppressed below the [`MIN_FORECAST_DAYS`]-day floor → an honest
/// insufficient-data state; every figure is an estimate (the `~` hedge is the render layer's).
///
/// **Quota ETA = a linear burn** from the current [`LimitMeasure::TokenFraction`] to `resets_at`,
/// projected ONLY off a fresh, cross-checked [`LimitAvailability::Available`] token-fraction
/// reading (it rides [`now_summary`], which already runs the sanitize/cross-check/stale-age-out
/// ladder). Every other arm — `Unverified`/`Estimated`/`Partial`/`Unavailable`, or a dollar
/// `Spend` measure — degrades to "ETA unavailable", never a confident wrong ETA (ARCHITECTURE).
pub fn forecast_view(snapshot: &EngineSnapshot) -> ForecastView {
    let now = snapshot.generated_at;
    let today = now.date_naive();
    let (year, month) = (today.year(), today.month());
    let days_in_month = days_in_month_utc(year, month);
    let days_elapsed = today.day();

    // The shared per-UTC-day API-lane $ series (T16 Anomalies reuses the same helper). Because it
    // buckets by UTC day, every month figure below stays UTC too (the consistency rule).
    let series = reconcile::api_lane_daily_usd_series(&snapshot.focus_rows);
    let no_api_usage = series.is_empty();

    // This month's actual per-day spend (ascending) + the spend-to-date numerator — same UTC
    // calendar as days_elapsed/days_in_month. A future-dated row (clock skew) past `today` is
    // excluded from spend-to-date.
    let mut daily_actuals: Vec<ForecastDay> = Vec::new();
    let mut spend_to_date = Decimal::ZERO;
    for (&date, &amount) in &series {
        if date.year() == year && date.month() == month && date <= today {
            daily_actuals.push(ForecastDay {
                date,
                spent_usd: amount,
            });
            spend_to_date += amount;
        }
    }

    let spend = if days_elapsed >= MIN_FORECAST_DAYS {
        // spend_to_date × days_in_month ÷ days_elapsed (days_elapsed ≥ 3, so never ÷0). Exact
        // Decimal with checked ops; an implausible overflow falls back to the spend-to-date.
        let projected_month_usd = spend_to_date
            .checked_mul(Decimal::from(days_in_month))
            .and_then(|scaled| scaled.checked_div(Decimal::from(days_elapsed)))
            .unwrap_or(spend_to_date);
        SpendForecast::Projected {
            projected_month_usd,
            spend_to_date_usd: spend_to_date,
            days_elapsed,
            days_in_month,
        }
    } else {
        SpendForecast::InsufficientData {
            spend_to_date_usd: spend_to_date,
            days_elapsed,
            days_in_month,
            min_days: MIN_FORECAST_DAYS,
        }
    };

    // Ride the same now-view limits the Now tab renders — the sanitize/cross-check/stale-age-out
    // ladder already ran, so a stale/unverified/estimated reading is degraded for free.
    let quota_etas = now_summary(snapshot, NowOptions::default())
        .limits
        .iter()
        .map(|limit| quota_eta(limit, now))
        .collect();

    ForecastView {
        generated_at: now,
        no_api_usage,
        spend,
        daily_actuals,
        quota_etas,
    }
}

/// The number of days in the given **UTC** calendar month = (first of next month) − (first of
/// this month). Falls back to 30 (never panics) on an impossible date.
fn days_in_month_utc(year: i32, month: u32) -> u32 {
    let first_this = NaiveDate::from_ymd_opt(year, month, 1);
    let first_next = if month == 12 {
        NaiveDate::from_ymd_opt(year + 1, 1, 1)
    } else {
        NaiveDate::from_ymd_opt(year, month + 1, 1)
    };
    match (first_this, first_next) {
        (Some(this), Some(next)) => (next - this).num_days().clamp(28, 31) as u32,
        _ => 30,
    }
}

/// Project one window's exhaustion ETA, riding the finalized [`LimitSummary`]. ONLY a fresh,
/// cross-checked [`LimitAvailability::Available`] token-fraction reading is projectable; every
/// other arm (incl. a dollar `Spend` measure) degrades to a typed "unavailable" — never a
/// confident wrong ETA (§11.5 T15 / ARCHITECTURE).
fn quota_eta(limit: &LimitSummary, now: DateTime<Utc>) -> QuotaEta {
    let outcome = match &limit.availability {
        LimitAvailability::Available {
            measure: LimitMeasure::TokenFraction(fraction),
            resets_at,
            reset_in_seconds,
        } => project_quota_eta(limit.kind, *fraction, *resets_at, *reset_in_seconds, now),
        _ => QuotaEtaOutcome::Unavailable {
            reason: QuotaEtaUnavailable::ReadingNotProjectable,
        },
    };
    QuotaEta {
        tool: limit.tool,
        kind: limit.kind,
        outcome,
    }
}

/// The linear-burn projection for a fresh token-fraction reading: the `fraction` was consumed
/// over the window's elapsed time (`window_duration − reset_in_seconds`); at that rate, project
/// when it reaches `1.0` and compare against the reset. All in seconds-space so no wild
/// `DateTime` is ever built (the constructed instant is bounded by `reset_in_seconds`).
fn project_quota_eta(
    kind: LimitKind,
    fraction: f64,
    resets_at: DateTime<Utc>,
    reset_in_seconds: i64,
    now: DateTime<Utc>,
) -> QuotaEtaOutcome {
    let window_secs = window_duration(kind).num_seconds() as f64;
    let reset_in = reset_in_seconds.max(0) as f64;
    let elapsed = window_secs - reset_in;
    if elapsed <= 0.0 {
        // The reset covers the whole window (just started) or a clock skew — too little elapsed
        // time to estimate a burn rate.
        return QuotaEtaOutcome::Unavailable {
            reason: QuotaEtaUnavailable::WindowJustStarted,
        };
    }
    if fraction <= 0.0 {
        // No usage yet — nothing to extrapolate; at zero burn the window resets first.
        return QuotaEtaOutcome::ResetsFirst {
            resets_at,
            fraction,
        };
    }
    if fraction >= 1.0 {
        // Already at/over the limit — effectively hit now.
        return QuotaEtaOutcome::ProjectedHit { at: now, fraction };
    }
    let secs_to_full = (1.0 - fraction) * elapsed / fraction;
    if secs_to_full < reset_in {
        let at = now + Duration::seconds(secs_to_full.round() as i64);
        QuotaEtaOutcome::ProjectedHit { at, fraction }
    } else {
        QuotaEtaOutcome::ResetsFirst {
            resets_at,
            fraction,
        }
    }
}

// ---------------------------------------------------------------------------
// The Anomalies engine (T16)
// ---------------------------------------------------------------------------

/// The trailing window (UTC days, ending today) the anomaly baselines are computed over — the
/// user's OWN recent history. 14 days balances responsiveness against a stable median for spiky,
/// right-skewed AI spend.
const ANOMALY_BASELINE_DAYS: i64 = 14;
/// The minimum number of distinct active UTC days (within the window) before ANY anomaly is
/// surfaced. Below it the baseline is too thin to call anything anomalous, so the tab shows an
/// honest "not enough history yet" state rather than fabricate a verdict (§11.5 T16).
const ANOMALY_MIN_HISTORY_DAYS: usize = 7;

/// The conservative MAD multiplier `k`: flag only when the deviation exceeds `k · MAD`. 3.5 is
/// deliberately high so the tab stays proactive, never alarmist (MAD beats mean±σ on spiky spend).
/// Built as an exact [`Decimal`] (3.5) so the whole detector is exact math, never `f64` (`new` is
/// not `const`, so this is a fn, mirroring the bundled-const style).
fn anomaly_k() -> Decimal {
    Decimal::new(35, 1)
}
/// The absolute daily-$ deviation floor. When the trailing spend is near-flat its MAD is `0`, so
/// `k · MAD` is `0` and ANY change would flag — taking `max(k·MAD, floor)` keeps a trivial change
/// from flagging while still surfacing a real jump on a flat history (the §12.22 MAD=0 guard).
/// Tunable.
fn anomaly_spend_floor_usd() -> Decimal {
    Decimal::ONE
}
/// The absolute share-of-tokens deviation floor (0.15 = 15 percentage points) — the MAD=0 guard
/// for the model-mix signal, same role as [`anomaly_spend_floor_usd`]. Tunable.
fn anomaly_share_floor() -> Decimal {
    Decimal::new(15, 2)
}

/// The median of a slice of [`Decimal`]s (a sorted copy; the mean of the two middle elements for
/// an even count). `None` for an empty slice — never panics, never `unwrap`s.
fn decimal_median(values: &[Decimal]) -> Option<Decimal> {
    if values.is_empty() {
        return None;
    }
    let mut sorted = values.to_vec();
    sorted.sort_unstable();
    let mid = sorted.len() / 2;
    if sorted.len() % 2 == 1 {
        sorted.get(mid).copied()
    } else {
        match (sorted.get(mid.wrapping_sub(1)), sorted.get(mid)) {
            (Some(low), Some(high)) => low
                .checked_add(*high)
                .and_then(|sum| sum.checked_div(Decimal::from(2)))
                .or(Some(*high)),
            _ => None,
        }
    }
}

/// The median absolute deviation (MAD): `median(|xᵢ − median|)`. `None` for an empty slice. A
/// near-flat series yields `0`, so a caller MUST guard the `0` with an absolute floor before
/// using it as a threshold (the §12.22 MAD=0 pitfall) — done in [`detect_anomaly`].
fn decimal_mad(values: &[Decimal], median: Decimal) -> Option<Decimal> {
    if values.is_empty() {
        return None;
    }
    let deviations: Vec<Decimal> = values
        .iter()
        .map(|value| value.checked_sub(median).unwrap_or(Decimal::ZERO).abs())
        .collect();
    decimal_median(&deviations)
}

/// The shared fields a flagged anomaly carries, returned by [`detect_anomaly`].
struct AnomalyMatch {
    baseline_median: Decimal,
    deviation: Decimal,
    magnitude: Option<Decimal>,
}

/// Decide whether `value` is anomalous vs the `baseline` series, using a robust baseline (median +
/// MAD), the conservative `k = 3.5` multiplier, and an absolute `floor` (the MAD=0 guard). When
/// `high_side_only`, only `value > median` flags (a spend spike — so a partial current day can
/// only spike UP, never read as an unusual drop); otherwise either direction flags (a model-mix
/// shift). Returns the matched fields, or `None` when within the norm / the baseline is empty.
///
/// The threshold is `max(k·MAD, floor)` and the test is `deviation > threshold` — a comparison,
/// never a division by MAD, so there is no divide-by-zero.
fn detect_anomaly(
    value: Decimal,
    baseline: &[Decimal],
    floor: Decimal,
    high_side_only: bool,
) -> Option<AnomalyMatch> {
    let median = decimal_median(baseline)?;
    let mad = decimal_mad(baseline, median)?;
    let signed = value.checked_sub(median)?;
    let deviation = signed.abs();
    let scaled = mad.checked_mul(anomaly_k()).unwrap_or(mad);
    let threshold = scaled.max(floor);
    let direction_ok = !high_side_only || signed > Decimal::ZERO;
    if deviation <= threshold || !direction_ok {
        return None;
    }
    let magnitude = if median > Decimal::ZERO {
        value.checked_div(median)
    } else {
        None
    };
    Some(AnomalyMatch {
        baseline_median: median,
        deviation,
        magnitude,
    })
}

/// The model id a model-mix anomaly names (for a deterministic sort tiebreak); `""` for any other
/// signal.
fn anomaly_model_name(anomaly: &Anomaly) -> &str {
    match &anomaly.signal {
        AnomalySignal::ModelMixShift { model } => model.as_str(),
        AnomalySignal::SpendSpike { .. } => "",
    }
}

/// The Anomalies tab (T16, widened to all-lane model-mix in T16b): proactive, non-alarmist
/// callouts vs the user's OWN recent history — TWO signals, both off `snapshot.focus_rows`, with
/// **asymmetric lane scopes**: a daily **spend spike** (**API-lane $** — subscription lanes are not
/// a summable dollar, §12.13) and a **model-mix shift** (**all-lane** share-of-tokens — a token
/// share is lane-agnostic, so a subscription-only user is served, T16b). Each is compared to a
/// robust baseline (the median + MAD over the trailing [`ANOMALY_BASELINE_DAYS`] UTC days), flagged
/// only past a conservative `3.5·MAD` (with an absolute floor so a near-flat history never flags
/// trivia). The view-level history basis ([`AnomaliesView::history_days`]) is the **all-lane
/// token-day** count (the universal basis driving `enough_history` / `no_usage`); the spend spike
/// keeps its OWN API-lane-$ day gate (≥ [`ANOMALY_MIN_HISTORY_DAYS`]), so the two signals may cite
/// different realized baselines. Pure-local, config-neutral, computed-never-persisted (mirrors
/// [`forecast_view`]); UTC days throughout (the same calendar the shared series buckets on — never
/// mixing UTC + local).
///
/// The quota burn-rate signal the original plan named is DEFERRED: the Claude/Codex `rate_limits`
/// caches persist a single point-in-time reading, so local data has no multi-day quota series to
/// difference (§11.5 T16). This view consults NO quota reading; the deferral is surfaced honestly
/// in the render footnote, never as a fabricated signal.
pub fn anomalies_view(snapshot: &EngineSnapshot) -> AnomaliesView {
    let today = snapshot.generated_at.date_naive();
    let window_start = today
        .checked_sub_signed(Duration::days(ANOMALY_BASELINE_DAYS - 1))
        .unwrap_or(today);

    // The spend-spike rides the **API-lane** $ series (subscription lanes are not a summable dollar,
    // §12.13); the model-mix rides the **all-lane** token series (a token share is lane-agnostic, so
    // a subscription-only user is served — T16b). Both are scoped to the trailing window
    // [window_start, today] — a future-dated row (clock skew) past today is dropped.
    let spend_series: BTreeMap<NaiveDate, Decimal> =
        reconcile::api_lane_daily_usd_series(&snapshot.focus_rows)
            .into_iter()
            .filter(|(date, _)| *date >= window_start && *date <= today)
            .collect();
    let token_series: BTreeMap<NaiveDate, BTreeMap<String, Decimal>> =
        reconcile::all_lane_daily_token_series(&snapshot.focus_rows)
            .into_iter()
            .filter(|(date, _)| *date >= window_start && *date <= today)
            .collect();

    // The view-level history basis = the **all-lane token-day** count (the universal basis): it
    // drives `enough_history`, `no_usage`, and the headline "your N-day norm". The spend spike keeps
    // its OWN API-lane-$ day count as a separate gate below, so the two signals may cite different
    // realized baselines.
    let history_days = token_series.len();
    let mut view = AnomaliesView {
        generated_at: snapshot.generated_at,
        history_days,
        min_history_days: ANOMALY_MIN_HISTORY_DAYS,
        baseline_days: ANOMALY_BASELINE_DAYS as usize,
        enough_history: history_days >= ANOMALY_MIN_HISTORY_DAYS,
        // No all-lane token usage at all (`history_days == 0`). After T16b a subscription-only user
        // IS covered once they accrue enough token-days, so this is a TRANSIENT zero-state that
        // fills in as usage accrues — never the old permanent "no API-lane usage" no-coverage state.
        no_usage: token_series.is_empty(),
        anomalies: Vec::new(),
    };
    if !view.enough_history {
        return view;
    }

    // --- Signal 1: spend spike (API-lane $, high-side), gated on its OWN realized day count. ---
    // It fires only when the API-lane-$ series itself has >= ANOMALY_MIN_HISTORY_DAYS days (a
    // subscription-only user has an empty $ series, so this is skipped) and cites THAT count as its
    // baseline — which may differ from the all-lane `history_days`. Its "today" is the most recent
    // active UTC day in the $ series (the user's last billed day), not the calendar `today`.
    let spend_days = spend_series.len();
    if spend_days >= ANOMALY_MIN_HISTORY_DAYS {
        if let Some(latest_spend_day) = spend_series.keys().next_back().copied() {
            let spend_values: Vec<Decimal> = spend_series.values().copied().collect();
            let latest_spend = spend_series
                .get(&latest_spend_day)
                .copied()
                .unwrap_or(Decimal::ZERO);
            if let Some(found) =
                detect_anomaly(latest_spend, &spend_values, anomaly_spend_floor_usd(), true)
            {
                view.anomalies.push(Anomaly {
                    signal: AnomalySignal::SpendSpike {
                        date: latest_spend_day,
                    },
                    value: latest_spend,
                    baseline_median: found.baseline_median,
                    deviation: found.deviation,
                    magnitude: found.magnitude,
                    baseline_days: spend_days,
                });
            }
        }
    }

    // --- Signal 2: model-mix shift (all-lane tokens, two-sided — a model's share-of-tokens on the
    // latest active token day vs its own trailing-window median share; catches both a surge and a
    // collapse). It cites the all-lane `history_days` as its baseline. Its "today" is the most recent
    // active UTC day in the token series (which, for a subscription-only user, the $ series lacks). ---
    let latest_day = match token_series.keys().next_back().copied() {
        Some(day) => day,
        None => return view,
    };
    let day_totals: BTreeMap<NaiveDate, Decimal> = token_series
        .iter()
        .map(|(date, models)| {
            let total = models.values().copied().fold(Decimal::ZERO, |acc, tokens| {
                acc.checked_add(tokens).unwrap_or(acc)
            });
            (*date, total)
        })
        .collect();
    // Every model that appears anywhere in the window (BTreeSet → deterministic order).
    let mut models: BTreeSet<&str> = BTreeSet::new();
    for day in token_series.values() {
        for model in day.keys() {
            models.insert(model.as_str());
        }
    }
    let latest_total = day_totals
        .get(&latest_day)
        .copied()
        .unwrap_or(Decimal::ZERO);
    let mut mix_anomalies: Vec<Anomaly> = Vec::new();
    if latest_total > Decimal::ZERO {
        for model in models {
            // The model's share-of-tokens on each active day (0 on a day with usage where the
            // model was absent — a genuine 0 share, so a surge from never-used still flags).
            let shares: Vec<Decimal> = day_totals
                .iter()
                .filter(|(_, total)| **total > Decimal::ZERO)
                .map(|(date, total)| {
                    let model_tokens = token_series
                        .get(date)
                        .and_then(|models| models.get(model))
                        .copied()
                        .unwrap_or(Decimal::ZERO);
                    model_tokens.checked_div(*total).unwrap_or(Decimal::ZERO)
                })
                .collect();
            let latest_model_tokens = token_series
                .get(&latest_day)
                .and_then(|models| models.get(model))
                .copied()
                .unwrap_or(Decimal::ZERO);
            let latest_share = latest_model_tokens
                .checked_div(latest_total)
                .unwrap_or(Decimal::ZERO);
            if let Some(found) = detect_anomaly(latest_share, &shares, anomaly_share_floor(), false)
            {
                mix_anomalies.push(Anomaly {
                    signal: AnomalySignal::ModelMixShift {
                        model: model.to_string(),
                    },
                    value: latest_share,
                    baseline_median: found.baseline_median,
                    deviation: found.deviation,
                    magnitude: found.magnitude,
                    baseline_days: history_days,
                });
            }
        }
    }
    // Most-deviant model-mix shift first (deterministic: deviation desc, then model name).
    mix_anomalies.sort_by(|a, b| {
        b.deviation
            .cmp(&a.deviation)
            .then_with(|| anomaly_model_name(a).cmp(anomaly_model_name(b)))
    });
    view.anomalies.extend(mix_anomalies);
    view
}

/// A stable sort key for a budget scope: per-tool rows first (ordered by id), the overall
/// total last on a tie.
fn budget_scope_key(scope: &BudgetScope) -> (u8, &str) {
    match scope {
        BudgetScope::Tool(tool) => (0, tool.as_str()),
        BudgetScope::Total => (1, "total"),
    }
}

/// How much over its time-proportional share a budget may be used before its pace reads as
/// "ahead of pace" — a small slack so an exactly-linear spend isn't flagged.
const BUDGET_PACE_EPSILON: f64 = 0.01;

fn budget_row(
    scope: BudgetScope,
    target: Decimal,
    spent: Decimal,
    month_elapsed_fraction: f64,
) -> BudgetRow {
    let fraction = (spent / target).to_f64().unwrap_or(0.0);
    let over_by_usd = if spent > target {
        Some(spent - target)
    } else {
        None
    };
    let pace = if fraction > 1.0 {
        BudgetPace::OverBudget
    } else if fraction > month_elapsed_fraction + BUDGET_PACE_EPSILON {
        BudgetPace::AheadOfPace
    } else {
        BudgetPace::OnTrack
    };
    BudgetRow {
        scope,
        target_usd: target,
        spent_usd: spent,
        fraction,
        over_by_usd,
        pace,
    }
}

/// The fraction (0..=1) of a period elapsed at `at` — the budget pace reference line.
fn period_elapsed_fraction(range: &PeriodRange, at: DateTime<Utc>) -> f64 {
    let total = (range.end - range.start).num_seconds();
    if total <= 0 {
        return 0.0;
    }
    let elapsed = (at - range.start).num_seconds().clamp(0, total);
    elapsed as f64 / total as f64
}

// ---------------------------------------------------------------------------
// The alerts crossing-detector (T17)
// ---------------------------------------------------------------------------

/// The quota fraction at/above which a WARN alert fires — the canonical near-limit *warning*
/// threshold (`0.80`). The apps/cli limit-meter + budget render alias this exact value (its
/// `WARN_FRACTION`), and it is the default of [`AlertThresholds::quota_warn_fraction`], so the
/// meter the user sees and the alert that fires share ONE number — never forked into a third set.
pub const ALERT_WARN_FRACTION: f64 = 0.80;
/// The quota fraction at/above which a CRITICAL alert fires — the canonical near-limit *critical*
/// threshold (`0.95`). Aliased by the apps/cli render `CRITICAL_FRACTION`; the default of
/// [`AlertThresholds::quota_critical_fraction`].
pub const ALERT_CRITICAL_FRACTION: f64 = 0.95;

/// Detect the active threshold crossings over a point-in-time snapshot — PURE and config-neutral:
/// the [`AlertThresholds`] and the opt-in [`AdvisoryAlerts`] sources are INPUTS, never read from a
/// file (mirrors [`budget_view`]). The alert sources are gathered independently and NEVER mixed:
///
/// * **Quota (%)** (T17) — one alert per quota window at/above a fraction threshold, fired ONLY off
///   a fresh, cross-checked [`LimitAvailability::Available`] reading (a token-fraction, or a dollar
///   `Spend` pool with a known allowance). An `Unverified`/`Estimated`/`Partial`/`Unavailable`
///   reading is NEVER alerted (the T15 discipline / ARCHITECTURE — degrade, never a confident
///   wrong alarm; staleness is already aged-out to `Estimated` upstream by `now_summary`).
/// * **Budget ($)** (T17) — one alert per [`BudgetRow`] STRICTLY over its monthly target (riding
///   `over_by_usd`), API-lane only.
/// * **Forecast projection ($, advisory)** (T17b) — fires ONLY when `advisory.forecast` is `Some`
///   (its opt-in sub-flag is on): a real `SpendForecast::Projected` month-end total that STRICTLY
///   exceeds an existing, not-already-over `BudgetScope::Total` target. `InsufficientData` (the
///   noisy early-month state) never fires, and an already-over total is owned by the hard Budget
///   alert above — never double-alerted. Total-scope only (the forecast projects the total).
/// * **Spend spike ($, advisory)** (T17b) — fires ONLY when `advisory.anomalies` is `Some`: one
///   alert per [`AnomalySignal::SpendSpike`] `anomalies_view` produced (already gated upstream on
///   enough history + the conservative `3.5·MAD` — never fabricated here).
///
/// Both advisory variants are `is_critical() == false` (a heads-up, not a hard crossing). The
/// result is ordered critical-tier first (so a `--check` / banner headline is the most pressing
/// crossing), a STABLE sort that preserves the insertion order within a tier: CRITICAL quota +
/// over-budget, then WARN quota, then the forecast projection, then the spend spike. With both
/// advisory sources off (`AdvisoryAlerts::default()`), the output is byte-identical to T17.
pub fn active_alerts(
    now: &NowSummary,
    budget: &BudgetView,
    thresholds: &AlertThresholds,
    advisory: AdvisoryAlerts<'_>,
) -> Vec<Alert> {
    let mut alerts: Vec<Alert> = Vec::new();

    // Quota class — % crossings off FRESH, cross-checked readings ONLY.
    for limit in &now.limits {
        if let Some(alert) = quota_alert(limit, thresholds) {
            alerts.push(alert);
        }
    }

    // Budget class — $ crossings: STRICTLY over the monthly target (BudgetView's own over-state).
    for row in &budget.rows {
        if let Some(over_by_usd) = row.over_by_usd {
            alerts.push(Alert::Budget {
                scope: row.scope.clone(),
                spent_usd: row.spent_usd,
                target_usd: row.target_usd,
                over_by_usd,
            });
        }
    }

    // Advisory: forecast TOTAL-budget projection (T17b) — opt-in via `advisory.forecast`. Pushed
    // AFTER the hard classes so the stable sort lands it after quota WARN (it is non-critical).
    if let Some(forecast) = advisory.forecast {
        if let Some(alert) = forecast_budget_alert(forecast, budget) {
            alerts.push(alert);
        }
    }

    // Advisory: spend-spike anomaly (T17b) — opt-in via `advisory.anomalies`. `anomalies_view`
    // already gated the spike on enough history + the 3.5·MAD threshold; we never fabricate one.
    // Model-mix shifts are intentionally NOT alerted (informational, not a cost/quota crossing).
    if let Some(anomalies) = advisory.anomalies {
        for anomaly in &anomalies.anomalies {
            if let AnomalySignal::SpendSpike { date } = &anomaly.signal {
                alerts.push(Alert::SpendSpike {
                    date: *date,
                    value_usd: anomaly.value,
                    baseline_median_usd: anomaly.baseline_median,
                    magnitude: anomaly.magnitude,
                });
            }
        }
    }

    // Most-pressing first: critical-tier (CRITICAL quota + any over-budget) before everything
    // non-critical (WARN quota, then the advisory sources). A STABLE sort, so the within-tier
    // insertion order above is preserved.
    alerts.sort_by_key(|alert| if alert.is_critical() { 0 } else { 1 });
    alerts
}

/// The forecast TOTAL-budget projection advisory alert (T17b), or `None` when it must not fire.
/// Fires only when ALL hold: the projection is a real [`SpendForecast::Projected`] (never the
/// early-month [`SpendForecast::InsufficientData`] — no noisy projection); a [`BudgetScope::Total`]
/// row exists (the user set a total budget) and is NOT already over (`over_by_usd.is_none()` — when
/// already over, T17's hard [`Alert::Budget`] owns it, so we never double-alert); and the
/// projection STRICTLY exceeds the total target (a projection exactly at target is not "expected to
/// exceed"). Total-scope only — `forecast_view` projects the total, not per-tool.
fn forecast_budget_alert(forecast: &ForecastView, budget: &BudgetView) -> Option<Alert> {
    let SpendForecast::Projected {
        projected_month_usd,
        ..
    } = &forecast.spend
    else {
        return None;
    };
    let projected_month_usd = *projected_month_usd;
    let total = budget
        .rows
        .iter()
        .find(|row| row.scope == BudgetScope::Total)?;
    if total.over_by_usd.is_some() {
        return None;
    }
    if projected_month_usd <= total.target_usd {
        return None;
    }
    Some(Alert::Forecast {
        projected_month_usd,
        target_usd: total.target_usd,
        projected_over_by_usd: projected_month_usd - total.target_usd,
    })
}

/// One quota window's alert, or `None` when it must not fire. Fires ONLY off a fresh,
/// cross-checked [`LimitAvailability::Available`] reading whose consumed fraction clears a
/// threshold — every other availability arm (and a dollar pool with no allowance) yields `None`.
fn quota_alert(limit: &LimitSummary, thresholds: &AlertThresholds) -> Option<Alert> {
    let (fraction, reset_in_seconds) = match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => (alert_fraction(measure)?, *reset_in_seconds),
        // Unverified / Estimated / Partial / Unavailable: never a confident alarm.
        _ => return None,
    };
    let level = alert_level(fraction, thresholds)?;
    Some(Alert::Quota {
        tool: limit.tool,
        kind: limit.kind,
        level,
        fraction,
        reset_in_seconds,
    })
}

/// The consumed fraction a fresh quota reading represents, for thresholding. A token-fraction is
/// the fraction directly; a dollar `Spend` pool is `used / included` when the allowance is known
/// (and positive) — the share of the included credit consumed; a pure-overage pool (no allowance)
/// has no fraction to threshold, so it never alerts. A non-finite result is rejected.
fn alert_fraction(measure: &LimitMeasure) -> Option<f64> {
    let fraction = match measure {
        LimitMeasure::TokenFraction(fraction) => *fraction,
        LimitMeasure::Spend {
            used_usd,
            included_usd,
        } => {
            let included = (*included_usd)?;
            if included <= Decimal::ZERO {
                return None;
            }
            (*used_usd / included).to_f64()?
        }
    };
    fraction.is_finite().then_some(fraction)
}

/// WARN vs CRITICAL vs no-alert for a consumed fraction, given the thresholds. CRITICAL is checked
/// first so it wins at a boundary; below the WARN threshold there is no alert. A non-finite or
/// out-of-range threshold (a hostile config) simply never matches — `f64` comparison is total
/// against the finite `fraction`, so there is no panic.
fn alert_level(fraction: f64, thresholds: &AlertThresholds) -> Option<AlertLevel> {
    if fraction >= thresholds.quota_critical_fraction {
        Some(AlertLevel::Critical)
    } else if fraction >= thresholds.quota_warn_fraction {
        Some(AlertLevel::Warn)
    } else {
        None
    }
}

pub fn period_range_for(period: Period, anchor: DateTime<Utc>) -> PeriodRange {
    let local_anchor = anchor.with_timezone(&Local);
    let local_start = start_of_period_local(period, local_anchor);
    let local_end = add_period_local(period, local_start);

    PeriodRange {
        start: local_start.with_timezone(&Utc),
        end: local_end.with_timezone(&Utc),
    }
}

fn collect_snapshot_from_providers(
    env: &HostEnv,
    providers: Vec<Box<dyn Provider>>,
    generated_at: DateTime<Utc>,
) -> Result<EngineSnapshot, CoreError> {
    let mut snapshot = EngineSnapshot::empty(generated_at);

    for provider in providers {
        let provider_id = provider.id();
        // Capture the declared capability for EVERY provider up front — before discovery
        // can `continue` past a missing/errored one — so the Providers tab always has a
        // descriptor to render, joined to `providers` by id.
        snapshot
            .capabilities
            .push(ProviderCapabilityView::from_capability(
                provider_id,
                provider.capability(),
            ));
        let location = match provider.discover(env) {
            Ok(Some(location)) => location,
            Ok(None) => {
                snapshot.providers.push(ProviderStatus {
                    provider: provider_id,
                    status: ProviderStatusKind::Missing,
                    files: 0,
                    usage_events: 0,
                    focus_rows: 0,
                    limit_windows: 0,
                    message: Some("no local data found".to_string()),
                });
                continue;
            }
            Err(err) => {
                snapshot.providers.push(ProviderStatus {
                    provider: provider_id,
                    status: ProviderStatusKind::Error,
                    files: 0,
                    usage_events: 0,
                    focus_rows: 0,
                    limit_windows: 0,
                    message: Some(err.to_string()),
                });
                continue;
            }
        };

        let files = location.files.len();
        let mut messages = Vec::new();
        let mut usage_events = Vec::new();
        let mut limit_windows = Vec::new();
        let mut usage_ok = true;
        let mut limits_ok = true;

        match provider.parse_usage(&location) {
            Ok(events) => usage_events = events,
            Err(err) => {
                usage_ok = false;
                messages.push(err.to_string());
            }
        }

        match provider.parse_limits(&location) {
            Ok(limits) => limit_windows = limits,
            Err(err) => {
                limits_ok = false;
                messages.push(err.to_string());
            }
        }

        let focus_rows = focus_records_from_usage(&usage_events)?;
        // Cursor is detect-only: it keeps no token usage or quota on disk (both are
        // served live server-side; ARCHITECTURE.md §4), so it produces zero
        // events/limits and is reported as `Detected` with the selected model,
        // logged-in flag, and the discovery-gated "no sanctioned source" deferral
        // carried in `message` (live quota is discovery-gated, §8 — never session
        // reuse). Every other provider keeps the generic usage/limits-derived status.
        let (status, message) = if provider_id == ProviderId::Cursor {
            (
                ProviderStatusKind::Detected,
                Some(cursor_detected_message(&read_cursor_config(&location))),
            )
        } else {
            let status = provider_status_kind(usage_ok, limits_ok);
            let message = if messages.is_empty() {
                None
            } else {
                Some(messages.join("; "))
            };
            (status, message)
        };

        snapshot.providers.push(ProviderStatus {
            provider: provider_id,
            status,
            files,
            usage_events: usage_events.len(),
            focus_rows: focus_rows.len(),
            limit_windows: limit_windows.len(),
            message,
        });
        snapshot.usage_events.append(&mut usage_events);
        snapshot.limit_windows.append(&mut limit_windows);
        snapshot.focus_rows.extend(focus_rows);
    }

    Ok(snapshot)
}

fn provider_status_kind(usage_ok: bool, limits_ok: bool) -> ProviderStatusKind {
    match (usage_ok, limits_ok) {
        (true, true) => ProviderStatusKind::Available,
        (false, false) => ProviderStatusKind::Error,
        (true, false) | (false, true) => ProviderStatusKind::Partial,
    }
}

/// The detected-status line for Cursor: the selected model, the logged-in flag, and
/// the explicit "usage/quota unavailable - no sanctioned source" note (live quota is
/// discovery-gated; ARCHITECTURE.md §8). Honest about what is locally knowable
/// (presence + model) versus what is not (cost + quota). Never includes the account
/// email/userId — only whether a session exists.
fn cursor_detected_message(config: &CursorConfig) -> String {
    let model = match (&config.display_name, &config.model_id) {
        (Some(display), Some(id)) => format!("model {display} ({id})"),
        (Some(display), None) => format!("model {display}"),
        (None, Some(id)) => format!("model {id}"),
        (None, None) => "model unknown".to_string(),
    };
    let login = if config.logged_in {
        "logged in"
    } else {
        "login unknown"
    };
    format!(
        "BETA - {model}, {login}; usage unavailable - no sanctioned source; \
         quota unavailable - no sanctioned source"
    )
}

fn push_meter_records(
    event: &UsageEvent,
    pricing: &PricingCatalog,
    records: &mut Vec<FocusRecord>,
) -> Result<(), CoreError> {
    let meters = [
        (TokenType::Input, event.input_tokens),
        (TokenType::Output, event.output_tokens),
        (TokenType::CacheRead, event.cache_read_tokens),
        (TokenType::CacheWrite, event.cache_write_tokens),
    ];
    // Resolve the raw log model id to a catalog key ONCE: exact match wins
    // (preserving exactness; an explicit dated entry would override the fallback),
    // else a base id with a strict date-snapshot suffix stripped, iff that base is
    // in the table. Both the model-info and rate lookups use the same resolved key,
    // so model-presence and rate-presence can never disagree.
    let resolved = pricing.resolve_key(&event.model);
    let model = resolved.and_then(|key| pricing.model(key));

    for (token_type, token_count) in meters {
        if token_count == 0 {
            continue;
        }
        let mut row = FocusRecord::unpriced_usage(UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: event.timestamp,
            tool: event.tool.to_string(),
            model: event.model.clone(),
            token_type,
            token_count,
            project: event.project.clone(),
            access_path: focus_access_path(event.access_path),
            service_name: model
                .map(|model| model.service_name.clone())
                .unwrap_or_else(|| service_name(event.tool).to_string()),
            service_provider_name: vendor_name(event.tool).to_string(),
            host_provider_name: vendor_name(event.tool).to_string(),
            invoice_issuer_name: vendor_name(event.tool).to_string(),
            billing_currency: model
                .map(|_| pricing.currency.clone())
                .unwrap_or_else(|| DEFAULT_BILLING_CURRENCY.to_string()),
        })?;

        match resolved.and_then(|key| pricing.rate(key, token_type)) {
            Some(rate) => apply_pricing(&mut row, rate),
            None if model.is_none() => {
                row.x_pricing_status = PRICING_STATUS_UNKNOWN_MODEL.to_string();
            }
            None => {}
        }

        // Sidechain attribution (T15): keep counting the row, annotate its confidence. A
        // sub-agent turn's tool/model/project may be the orchestrator's, so its
        // attribution is "uncertain" — never dropped (see docs/limitations.md).
        if event.is_sidechain {
            row.x_sidechain = true;
            row.x_attribution_confidence = ATTRIBUTION_UNCERTAIN.to_string();
        }

        records.push(row);
    }

    Ok(())
}

/// Opaque, stable per-rate SKU price identifier. The unit component reflects the
/// FOCUS-facing per-token basis (consistent with PricingUnit / ListUnitPrice), and the
/// `as_of` is the WINNING tier's snapshot date — so a layered catalog's SKU id reflects
/// the tier that actually priced the row, never a global/curated date.
fn sku_price_id(rate: &CatalogRate) -> String {
    format!(
        "{}:{}:{}:{}:{}",
        rate.provider, rate.model, rate.meter, PRICING_UNIT_TOKENS, rate.as_of
    )
}

fn apply_pricing(row: &mut FocusRecord, rate: &CatalogRate) {
    // Per-token representation (FOCUS UnitFormat): PricingQuantity is the token
    // count, the unit-price columns are per-token (the per-1M catalog rate ÷
    // 1_000_000). Cost is invariant: per_token × tokens == tokens × rate ÷ 1e6,
    // identical to the previous (tokens / 1e6) × rate and exact in Decimal for
    // every catalog rate (each price has ≤2 dp, so ÷1e6 terminates at ≤8 dp).
    let per_token = rate.price / Decimal::from(1_000_000_u64);
    let quantity = row.x_consumed_tokens;
    let cost = per_token * quantity;
    row.billed_cost = cost;
    row.effective_cost = cost;
    row.list_cost = cost;
    row.contracted_cost = cost;
    // A priced SKU exists: populate the columns nulled on unpriced rows.
    row.consumed_quantity = Some(quantity);
    row.pricing_quantity = Some(quantity);
    row.pricing_category = Some(PRICING_CATEGORY_STANDARD.to_string());
    row.pricing_unit = Some(PRICING_UNIT_TOKENS.to_string());
    row.sku_price_id = Some(sku_price_id(rate));
    row.list_unit_price = Some(per_token);
    row.contracted_unit_price = Some(per_token);
    // PricingCurrency == BillingCurrency for Costroid, so the pricing-currency
    // columns mirror their billing-currency counterparts.
    row.pricing_currency_effective_cost = cost;
    row.pricing_currency_list_unit_price = Some(per_token);
    row.pricing_currency_contracted_unit_price = Some(per_token);
    row.x_pricing_status = PRICING_STATUS_PRICED.to_string();
    // R8: stamp the winning tier's provenance (source + date + content hash) so an
    // estimated row records exactly which snapshot priced it.
    row.x_pricing_snapshot_id = Some(rate.snapshot_id.clone());
}

/// If `model` ends in a strict dated-snapshot suffix, return the base id with the
/// suffix removed; otherwise `None`.
///
/// Recognizes ONLY the two shapes providers actually mint: `-YYYYMMDD` (Anthropic
/// snapshots, e.g. `claude-haiku-4-5-20251001`) and `-YYYY-MM-DD` (OpenAI
/// snapshots, e.g. `gpt-5.5-2025-10-01`). A version component like `-8` (one digit)
/// matches neither, so a genuinely new version (`claude-opus-4-8`) is never
/// mistaken for a dated snapshot. A suffix that would leave an empty base is
/// rejected. Pure, ASCII, never panics.
fn strip_date_suffix(model: &str) -> Option<&str> {
    strip_dashed_date(model).or_else(|| strip_compact_date(model))
}

/// `<base>-YYYY-MM-DD` → `<base>` (OpenAI dated-snapshot form).
fn strip_dashed_date(model: &str) -> Option<&str> {
    let head = model.get(..model.len().checked_sub(11)?)?;
    let tail = model.get(head.len()..)?.as_bytes();
    let ok = tail[0] == b'-'
        && tail[1..5].iter().all(u8::is_ascii_digit)
        && tail[5] == b'-'
        && tail[6..8].iter().all(u8::is_ascii_digit)
        && tail[8] == b'-'
        && tail[9..11].iter().all(u8::is_ascii_digit);
    (ok && !head.is_empty()).then_some(head)
}

/// `<base>-YYYYMMDD` → `<base>` (Anthropic dated-snapshot form, exactly 8 digits).
fn strip_compact_date(model: &str) -> Option<&str> {
    let head = model.get(..model.len().checked_sub(9)?)?;
    let tail = model.get(head.len()..)?.as_bytes();
    let ok = tail[0] == b'-' && tail[1..].iter().all(u8::is_ascii_digit);
    (ok && !head.is_empty()).then_some(head)
}

#[derive(Debug, Deserialize)]
struct PricingTable {
    schema_version: String,
    /// The pricing source label for the R8 stamp (`"curated"` / `"litellm"` /
    /// `"override"`); defaults to `"bundled"` when a (e.g. older) snapshot omits it.
    #[serde(default = "default_pricing_source")]
    source: String,
    as_of: String,
    /// The content hash of the source data (e.g. the pinned upstream sha256), recorded
    /// for the R8 stamp. Optional — a hand-curated tier identifies by source+date.
    #[serde(default)]
    content_hash: Option<String>,
    currency: String,
    #[serde(default)]
    models: Vec<PricingModel>,
}

fn default_pricing_source() -> String {
    "bundled".to_string()
}

/// The R8 provenance stamp for a pricing tier: `"{source}@{as_of}"`, plus the first 8 hex
/// chars of the content hash when present — e.g. `"litellm@2026-06-18#36c8994e"` or
/// `"curated@2026-06-02"`. Stamped on every estimated row via `x_PricingSnapshotId`.
fn pricing_snapshot_id(source: &str, as_of: &str, content_hash: Option<&str>) -> String {
    match content_hash {
        Some(hash) if !hash.is_empty() => {
            // First ≤8 chars, taken CHAR-safely: `content_hash` is a free-form JSON string
            // from a user override, so a byte slice (`&hash[..8]`) could split a multibyte
            // char and panic — and a library crate must never panic.
            let short: String = hash.chars().take(8).collect();
            format!("{source}@{as_of}#{short}")
        }
        _ => format!("{source}@{as_of}"),
    }
}

#[derive(Debug, Deserialize)]
struct PricingModel {
    provider: String,
    model: String,
    service_name: String,
    #[serde(default)]
    rates: Vec<PricingRate>,
}

#[derive(Debug, Deserialize)]
struct PricingRate {
    meter: String,
    unit: String,
    price: Decimal,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct PricingModelInfo {
    service_name: String,
    /// The tier this model entry came from (`"curated"`/`"litellm"`/`"override"`/…) — used
    /// by [`resolve_key`](PricingCatalog::resolve_key) to prefer the higher-precedence tier
    /// when both an exact dated entry (e.g. litellm) and a base entry (e.g. curated) exist.
    source: String,
}

/// Precedence rank for a pricing tier (lower = higher precedence): override > curated >
/// litellm > anything else. Mirrors the layering order in [`PricingCatalog::layered`].
fn tier_rank(source: &str) -> u8 {
    match source {
        "override" => 0,
        "curated" => 1,
        "litellm" => 2,
        _ => 3,
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct CatalogRate {
    provider: String,
    model: String,
    meter: String,
    unit: String,
    price: Decimal,
    /// The snapshot date of the tier this rate came from (feeds `sku_price_id`), so a
    /// layered catalog's per-rate SKU id reflects the winning tier, not a global date.
    as_of: String,
    /// The R8 provenance stamp of the winning tier ([`pricing_snapshot_id`]); copied onto
    /// `x_PricingSnapshotId` when this rate prices a row.
    snapshot_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct PricingCatalog {
    /// The primary (top-tier) snapshot date — curated's, or the override's when present.
    /// For catalog-level display (e.g. the Frontier `pricing_as_of`); a row's accurate
    /// per-tier date lives on its [`CatalogRate::as_of`].
    as_of: String,
    currency: String,
    models: BTreeMap<String, PricingModelInfo>,
    rates: BTreeMap<(String, String), CatalogRate>,
}

impl PricingCatalog {
    /// The curated tier alone (no long tail). Kept for tests + as the single-tier parse
    /// primitive; production paths use [`layered_default`](Self::layered_default).
    fn bundled() -> Result<Self, CoreError> {
        Self::from_json(bundled_pricing_json())
    }

    /// The production catalog: the LiteLLM long-tail (lowest precedence) under the curated
    /// catalog, with no user override. Every internal record-builder uses this so a row is
    /// priced from the best tier and stamped with its provenance (R8).
    fn layered_default() -> Result<Self, CoreError> {
        Self::layered(None)
    }

    /// The layered catalog (D2): `user-override > curated > LiteLLM long-tail`. `override_json`
    /// is the override file's content (the caller reads it; see [`read_pricing_override`]); a
    /// malformed override is a typed [`CoreError`] the caller may downgrade to a warning.
    fn layered(override_json: Option<&str>) -> Result<Self, CoreError> {
        // Lowest precedence first; each higher tier overlays (owns its models entirely).
        let mut catalog = Self::from_json(bundled_litellm_pricing_json())?;
        catalog.overlay(Self::from_json(bundled_pricing_json())?);
        if let Some(json) = override_json {
            catalog.overlay(Self::from_json(json)?);
        }
        Ok(catalog)
    }

    /// Overlay a higher-precedence tier. Per-model precedence (D2): a model present in
    /// `higher` is owned ENTIRELY by it (all four meters), so any lower-tier rate for that
    /// model is dropped first — never blending two sources within one model.
    fn overlay(&mut self, higher: PricingCatalog) {
        self.rates
            .retain(|(model, _meter), _| !higher.models.contains_key(model));
        self.models.extend(higher.models);
        self.rates.extend(higher.rates);
        self.as_of = higher.as_of;
        self.currency = higher.currency;
    }

    fn from_json(value: &str) -> Result<Self, CoreError> {
        let table = serde_json::from_str::<PricingTable>(value)?;
        Self::from_table(table)
    }

    fn model(&self, model: &str) -> Option<&PricingModelInfo> {
        self.models.get(model)
    }

    fn rate(&self, model: &str, token_type: TokenType) -> Option<&CatalogRate> {
        self.rates
            .get(&(model.to_string(), token_type.as_str().to_string()))
    }

    /// Per-1M-token list price for a `(model, meter)` pair, by the meter's string id
    /// (`"input"`/`"output"`/`"cache_read"`/`"cache_write"`). Lets `bench` re-price by
    /// the `x_token_type` string without round-tripping through `TokenType`.
    fn meter_price(&self, model: &str, meter: &str) -> Option<Decimal> {
        self.rates
            .get(&(model.to_string(), meter.to_string()))
            .map(|rate| rate.price)
    }

    /// Resolve a raw log model id to the catalog key whose info/rates apply.
    ///
    /// 1. Both an exact match AND a date-stripped base may exist (e.g. litellm carries the
    ///    exact dated `claude-haiku-4-5-20251001` while curated carries the base
    ///    `claude-haiku-4-5`). Prefer the **higher-precedence tier** ([`tier_rank`]) so a
    ///    curated base wins over a litellm dated entry — keeping curated authoritative and
    ///    preserving M1's "dated snapshot prices at its base" honesty. A tie (same tier)
    ///    keeps the exact match (an explicit dated entry in the SAME tier wins its base).
    /// 2. Only one present → that one. The base is used **iff it exists** — never invent a
    ///    mapping, and a version bump (`claude-opus-4-8`) is never folded onto another version.
    /// 3. Neither → `None` (genuinely unknown model → `unknown_model`).
    fn resolve_key<'a>(&'a self, model: &'a str) -> Option<&'a str> {
        let exact = self.models.get_key_value(model);
        let base = strip_date_suffix(model).and_then(|b| self.models.get_key_value(b));
        match (exact, base) {
            (Some((exact_key, exact_info)), Some((base_key, base_info))) => {
                if tier_rank(&base_info.source) < tier_rank(&exact_info.source) {
                    Some(base_key.as_str())
                } else {
                    Some(exact_key.as_str())
                }
            }
            (Some((exact_key, _)), None) => Some(exact_key.as_str()),
            (None, Some((base_key, _))) => Some(base_key.as_str()),
            (None, None) => None,
        }
    }

    fn from_table(table: PricingTable) -> Result<Self, CoreError> {
        if table.schema_version != PRICING_SCHEMA_VERSION {
            return Err(CoreError::PricingValidation(format!(
                "unsupported schema_version {}; expected {}",
                table.schema_version, PRICING_SCHEMA_VERSION
            )));
        }
        if table.currency != DEFAULT_BILLING_CURRENCY {
            return Err(CoreError::PricingValidation(format!(
                "unsupported currency {}; expected {}",
                table.currency, DEFAULT_BILLING_CURRENCY
            )));
        }

        let as_of = table.as_of;
        let source = table.source;
        let snapshot_id = pricing_snapshot_id(&source, &as_of, table.content_hash.as_deref());
        let mut catalog = Self {
            as_of: as_of.clone(),
            currency: table.currency,
            models: BTreeMap::new(),
            rates: BTreeMap::new(),
        };

        for model in table.models {
            if catalog
                .models
                .insert(
                    model.model.clone(),
                    PricingModelInfo {
                        service_name: model.service_name.clone(),
                        source: source.clone(),
                    },
                )
                .is_some()
            {
                return Err(CoreError::PricingValidation(format!(
                    "duplicate pricing model {}",
                    model.model
                )));
            }

            for rate in model.rates {
                if rate.unit != PRICING_UNIT_1M_TOKENS {
                    return Err(CoreError::PricingValidation(format!(
                        "unsupported pricing unit {} for {}:{}",
                        rate.unit, model.model, rate.meter
                    )));
                }
                if !is_supported_meter(&rate.meter) {
                    return Err(CoreError::PricingValidation(format!(
                        "unsupported pricing meter {} for {}",
                        rate.meter, model.model
                    )));
                }

                let key = (model.model.clone(), rate.meter.clone());
                if catalog.rates.contains_key(&key) {
                    return Err(CoreError::PricingValidation(format!(
                        "duplicate pricing rate {}:{}",
                        model.model, rate.meter
                    )));
                }

                catalog.rates.insert(
                    key,
                    CatalogRate {
                        provider: model.provider.clone(),
                        model: model.model.clone(),
                        meter: rate.meter,
                        unit: rate.unit,
                        price: rate.price,
                        as_of: as_of.clone(),
                        snapshot_id: snapshot_id.clone(),
                    },
                );
            }
        }

        Ok(catalog)
    }
}

fn is_supported_meter(value: &str) -> bool {
    matches!(value, "input" | "output" | "cache_read" | "cache_write")
}

fn focus_access_path(access_path: AccessPath) -> FocusAccessPath {
    match access_path {
        AccessPath::Api => FocusAccessPath::Api,
        AccessPath::Subscription => FocusAccessPath::Subscription,
        AccessPath::Unknown => FocusAccessPath::Unknown,
    }
}

fn service_name(provider: ProviderId) -> &'static str {
    match provider {
        ProviderId::ClaudeCode => "Claude Code",
        ProviderId::Codex => "Codex",
        ProviderId::Cursor => "Cursor",
    }
}

fn vendor_name(provider: ProviderId) -> &'static str {
    match provider {
        ProviderId::ClaudeCode => "Anthropic",
        ProviderId::Codex => "OpenAI",
        ProviderId::Cursor => "Anysphere",
    }
}

/// True iff this row belongs to the developer-tool ledger lane — the ONLY lane that feeds the
/// developer-tool dollar totals. cloud_api / local_inference rows are summed on their own lane
/// totals, never into the dev-tool total (the "lanes never summed across" invariant).
///
/// This is the `x_Lane` ledger axis ([`LedgerLane`]), which is DISTINCT from the `x_AccessPath`-derived
/// [`CostLane`] axis the dev-tool summers group by: a `cloud_api`-lane row can still carry
/// `access_path = Api` → `CostLane::Api`, so without this guard it would be folded into the
/// developer-tool API total. Every dev-tool $-summer gates on this BEFORE its `CostLane` filter.
/// At v0.6.0 every row is `developer_tool`, so the gate is a no-op (all rows pass); it is the typed
/// enforcement that prevents future cross-lane pollution once import (T14) / local inference (M3)
/// rows arrive.
///
/// Public so app-side dev-tool $-summers (e.g. the CLI History header, which `costroid-focus`
/// does not reach as a runtime dep) gate on the SAME predicate — one source of truth, no
/// duplicated `"developer_tool"` literal.
pub fn is_developer_tool_lane(row: &FocusRecord) -> bool {
    row.x_lane == LedgerLane::DeveloperTool.as_str()
}

/// Total `billed_cost` (estimate) for one ledger lane across the given rows — each lane summed
/// independently so a cloud/local row never moves the developer-tool total. The companion of the
/// dev-tool gate: lanes that are deliberately excluded from the dev-tool total each get their OWN
/// summable total here, never folded into another lane (the "lanes never summed across" invariant).
///
/// Multi-currency (D3): only **USD** rows are summed — a non-USD cloud row is kept in its native
/// currency and excluded from this USD figure rather than blended in (no FX). Use
/// [`total_by_currency`] to see every currency's subtotal.
pub fn lane_total_usd(rows: &[FocusRecord], lane: LedgerLane) -> Decimal {
    let lane = lane.as_str();
    rows.iter()
        .filter(|row| row.x_lane == lane && row.billing_currency == DEFAULT_BILLING_CURRENCY)
        .fold(Decimal::ZERO, |sum, row| sum + row.billed_cost)
}

/// Grand total `billed_cost` across ALL lanes (the sum of every lane's [`lane_total_usd`]). Because
/// each row belongs to exactly one lane, this equals `lane_total_usd(DeveloperTool) +
/// lane_total_usd(CloudApi) + lane_total_usd(LocalInference)` — the lanes partition the rows.
///
/// Multi-currency (D3): **USD only**, like [`lane_total_usd`] — never blends currencies.
pub fn grand_total_usd(rows: &[FocusRecord]) -> Decimal {
    rows.iter()
        .filter(|row| row.billing_currency == DEFAULT_BILLING_CURRENCY)
        .fold(Decimal::ZERO, |sum, row| sum + row.billed_cost)
}

/// Per-currency `billed_cost` subtotals across all rows, keyed by `BillingCurrency` (D3). The
/// honest companion to the USD-only [`grand_total_usd`]: a non-USD cloud row is carried in its
/// native currency and surfaced here, never silently converted or dropped. Cross-currency sums
/// are refused exactly as cross-lane sums are — there is no single blended number.
pub fn total_by_currency(rows: &[FocusRecord]) -> BTreeMap<String, Decimal> {
    let mut totals: BTreeMap<String, Decimal> = BTreeMap::new();
    for row in rows {
        *totals
            .entry(row.billing_currency.clone())
            .or_insert(Decimal::ZERO) += row.billed_cost;
    }
    totals
}

/// Public wrapper over the internal [`summarize_rows`] aggregator for the
/// store-replay path (M1): the edge reads rows from the store and hands them here,
/// keeping the dependency direction store → core (core never depends on the store).
///
/// Inherits the M1 T6 developer-tool gate of [`summarize_rows`] — these are
/// **developer-tool** summaries: cloud_api / local_inference rows are excluded
/// here and summed on their own lane totals via [`lane_total_usd`]. Behavior is
/// identical to the internal aggregator; this only widens its visibility.
pub fn aggregate_rows(rows: &[FocusRecord], group_by: GroupBy) -> Vec<CostLaneSummary> {
    summarize_rows(rows.iter(), group_by)
}

fn summarize_rows<'a, I>(rows: I, group_by: GroupBy) -> Vec<CostLaneSummary>
where
    I: IntoIterator<Item = &'a FocusRecord>,
{
    let mut summaries = BTreeMap::<(CostLane, GroupKey), AggregateTotals>::new();
    for row in rows {
        // §170 dev-tool gate: only developer_tool rows feed the dev-tool dollar totals
        // (current_costs/totals → now/trends $, now_api_spend_display, now_model_spend_breakdown).
        // cloud_api/local_inference rows are summed on their own lane totals, never here.
        if !is_developer_tool_lane(row) {
            continue;
        }
        let lane = CostLane::from_access_path(&row.x_access_path);
        let group = group_key(row, group_by);
        summaries.entry((lane, group)).or_default().add_row(row);
    }

    summaries
        .into_iter()
        .map(|((lane, group), totals)| CostLaneSummary {
            group,
            lane,
            totals,
        })
        .collect()
}

fn group_key(row: &FocusRecord, group_by: GroupBy) -> GroupKey {
    let value = match group_by {
        GroupBy::Model => non_empty_value(&row.x_model),
        GroupBy::App => row
            .x_project
            .as_deref()
            .map(non_empty_value)
            .unwrap_or_else(|| UNKNOWN_GROUP_VALUE.to_string()),
        GroupBy::Total => TOTAL_GROUP_VALUE.to_string(),
    };

    GroupKey {
        kind: group_by,
        value,
    }
}

fn non_empty_value(value: &str) -> String {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        UNKNOWN_GROUP_VALUE.to_string()
    } else {
        trimmed.to_string()
    }
}

fn limit_summary(
    limit: &LimitWindow,
    rows: &[FocusRecord],
    generated_at: DateTime<Utc>,
) -> LimitSummary {
    // The finalize pass (ARCHITECTURE): the cross-check needs per-window
    // usage volume, which exists only here at the core layer (the provider could not see
    // it). Compute the trailing-window volume + its priced value, then demote a
    // high-but-trivial Claude reading to Unverified before mapping to the render verdict.
    let volume = window_token_volume(rows, limit.tool, limit.kind, generated_at);
    let estimated_usd = window_estimated_usd(rows, limit.tool, limit.kind, generated_at);
    let finalized = LimitWindow {
        status: finalize_limit_status(limit, &volume),
        ..limit.clone()
    };
    LimitSummary {
        tool: limit.tool,
        plan: limit.plan.clone(),
        kind: limit.kind,
        label: limit.label.clone(),
        captured_at: finalized.captured_at,
        availability: limit_availability(&finalized, generated_at, &volume, estimated_usd),
    }
}

/// The cross-check (ARCHITECTURE — the #31820 guard: flag, never
/// suppress, never rewrite the number). A Claude `rate_limits` reading that is *high*
/// (`fraction ≥ HIGH_USAGE_FRACTION`) but whose trailing-window token volume is *trivial*
/// (`< UNVERIFIED_TOKEN_FLOOR`) is implausible — demote `Verified → Unverified` so it
/// renders flagged, never as a confident near-max meter. One-directional and
/// conservative: it can only *lower* confidence (a genuinely high reading — shared
/// claude.ai chat, or one heavy prompt — may be real), so it never asserts a reading is
/// correct. Applies ONLY to Claude's buggy push; Codex windows come from sanctioned
/// rollout logs (§7) and are trusted on arrival.
fn finalize_limit_status(limit: &LimitWindow, volume: &TokenTotals) -> LimitStatus {
    if limit.tool == ProviderId::ClaudeCode && limit.status == LimitStatus::Verified {
        if let Some(LimitMeasure::TokenFraction(fraction)) = limit.measure {
            if fraction >= HIGH_USAGE_FRACTION && volume.total() < UNVERIFIED_TOKEN_FLOOR {
                return LimitStatus::Unverified;
            }
        }
    }
    limit.status
}

/// Per-window local token volume, summed from the snapshot's FOCUS rows in the trailing
/// window for `kind` (filter by `x_tool` + `charge_period_start` inside the window
/// ending at `now`). Returns the per-meter [`TokenTotals`] so the `Estimated` render can
/// show the breakdown; the cross-check uses its scalar `.total()`. Lives in
/// `costroid-core` — the provider has no access to usage rows (ARCHITECTURE
/// §5).
fn window_token_volume(
    rows: &[FocusRecord],
    tool: ProviderId,
    kind: LimitKind,
    now: DateTime<Utc>,
) -> TokenTotals {
    let tool = tool.to_string();
    let mut totals = TokenTotals::default();
    for row in rows {
        // §170 dev-tool gate (symmetry with `window_estimated_usd`): this is a
        // developer-tool quota-window volume that feeds the Verified→Unverified cross-check
        // (`finalize_limit_status`). Sum developer_tool rows only — a cloud_api/local_inference
        // import row must never inflate the window volume (which could mask a demotion). The
        // `x_tool == ProviderId` filter already blocks most foreign rows, but the lane gate is
        // the load-bearing guard. No-op at v0.6.0 (every row is developer_tool).
        if !is_developer_tool_lane(row) {
            continue;
        }
        if row_in_trailing_window(row, &tool, kind, now) {
            totals.add(&row.x_token_type, decimal_to_u64(row.x_consumed_tokens));
        }
    }
    totals
}

/// The priced dollar value of a window's local volume — the existing cost calculator's
/// per-row `effective_cost`, summed over the same trailing-window rows. `None` when any
/// contributing row is unpriced (`x_pricing_status != "priced"`): the volume is shown
/// alone, never a guessed price (ARCHITECTURE). `None` too when the
/// window has no rows (the caller only reaches `Estimated` with nonzero volume).
fn window_estimated_usd(
    rows: &[FocusRecord],
    tool: ProviderId,
    kind: LimitKind,
    now: DateTime<Utc>,
) -> Option<Decimal> {
    let tool = tool.to_string();
    let mut sum = Decimal::ZERO;
    let mut any = false;
    for row in rows {
        // §170 dev-tool gate: the per-window estimated $ (a developer-tool quota-window figure)
        // sums developer_tool rows only — a cloud_api/local_inference row never feeds it.
        if !is_developer_tool_lane(row) {
            continue;
        }
        if row_in_trailing_window(row, &tool, kind, now) {
            if row.x_pricing_status != PRICING_STATUS_PRICED {
                return None;
            }
            sum += row.effective_cost;
            any = true;
        }
    }
    any.then_some(sum)
}

/// Whether a FOCUS row belongs to `tool`'s trailing window for `kind` ending at `now`.
fn row_in_trailing_window(
    row: &FocusRecord,
    tool: &str,
    kind: LimitKind,
    now: DateTime<Utc>,
) -> bool {
    row.x_tool == tool
        && row.charge_period_start <= now
        && row.charge_period_start >= now - window_duration(kind)
}

/// The trailing duration each quota window covers.
fn window_duration(kind: LimitKind) -> Duration {
    match kind {
        LimitKind::FiveHour => Duration::hours(5),
        LimitKind::Daily => Duration::days(1),
        LimitKind::Weekly => Duration::days(7),
        LimitKind::Monthly | LimitKind::BillingCycle => Duration::days(30),
    }
}

/// Map a finalized [`LimitWindow`] to its render-layer [`LimitAvailability`]
/// (ARCHITECTURE — a pure map; no I/O, no clock beyond
/// `generated_at`). Staleness is evaluated *here* against the live `generated_at` (so
/// `--live` re-checks it each tick), never frozen by the provider:
/// * `status == Unavailable` / no measure → `Estimated` (volume > 0) else `Unavailable`;
/// * a *stale* reading (`resets_at < generated_at`), any status → same age-out;
/// * `status == Unverified`, not stale → `Unverified` (carry the measure, flagged);
/// * `status == Verified`, not stale, live reset → `Available`;
/// * `status == Verified`, not stale, reset unknown → `Partial`.
fn limit_availability(
    limit: &LimitWindow,
    generated_at: DateTime<Utc>,
    volume: &TokenTotals,
    estimated_usd: Option<Decimal>,
) -> LimitAvailability {
    let reset_in_seconds = limit
        .resets_at
        .map(|resets_at| clamp_reset_seconds(resets_at, generated_at));
    let is_stale = limit
        .resets_at
        .map(|resets_at| resets_at < generated_at)
        .unwrap_or(false);

    // No usable reading — an explicit Unavailable status or a missing measure — falls
    // back to the volume-based estimate, never a fabricated %.
    let measure = match (limit.status, limit.measure.clone()) {
        (LimitStatus::Unavailable, _) | (_, None) => {
            return estimate_or_unavailable(limit, volume, estimated_usd);
        }
        (_, Some(measure)) => measure,
    };

    // A reading whose window has already reset is stale; age it out to the estimate
    // regardless of status (covers Verified and Unverified, Claude and Codex alike).
    if is_stale {
        return estimate_or_unavailable(limit, volume, estimated_usd);
    }

    match limit.status {
        LimitStatus::Unverified => LimitAvailability::Unverified {
            measure,
            resets_at: limit.resets_at,
            reset_in_seconds,
        },
        LimitStatus::Verified => match limit.resets_at {
            Some(resets_at) => LimitAvailability::Available {
                measure,
                resets_at,
                reset_in_seconds: reset_in_seconds.unwrap_or(0),
            },
            None => LimitAvailability::Partial {
                measure: Some(measure),
                resets_at: None,
                reset_in_seconds: None,
                reason: "reset time unknown".to_string(),
            },
        },
        // Unreachable: the Unavailable status returns above. Kept as a non-panicking
        // arm so the match stays exhaustive (no unwrap/panic in library code).
        LimitStatus::Unavailable => estimate_or_unavailable(limit, volume, estimated_usd),
    }
}

/// The absent→estimate fallback (ARCHITECTURE): show the window's local
/// token volume + priced value when there is no trustworthy %, else `Unavailable`. Never
/// blank when there is something to show, never a fabricated number.
fn estimate_or_unavailable(
    limit: &LimitWindow,
    volume: &TokenTotals,
    estimated_usd: Option<Decimal>,
) -> LimitAvailability {
    let volume_tokens = volume.total();
    if volume_tokens > 0 {
        LimitAvailability::Estimated {
            volume_tokens,
            estimated_usd,
        }
    } else {
        LimitAvailability::Unavailable {
            reason: unavailable_reason(limit),
        }
    }
}

fn unavailable_reason(limit: &LimitWindow) -> String {
    limit
        .label
        .clone()
        .unwrap_or_else(|| "limit data unavailable from local logs".to_string())
}

fn clamp_reset_seconds(resets_at: DateTime<Utc>, generated_at: DateTime<Utc>) -> i64 {
    resets_at
        .signed_duration_since(generated_at)
        .num_seconds()
        .max(0)
}

fn start_of_period_local(period: Period, anchor: DateTime<Local>) -> DateTime<Local> {
    match period {
        Period::Day => local_start_of_day(anchor.date_naive(), anchor),
        Period::Week => {
            let days_from_monday = i64::from(anchor.weekday().num_days_from_monday());
            let date = match anchor
                .date_naive()
                .checked_sub_signed(Duration::days(days_from_monday))
            {
                Some(value) => value,
                None => anchor.date_naive(),
            };
            local_start_of_day(date, anchor)
        }
        Period::Month => local_start_for_ymd(anchor.year(), anchor.month(), 1, anchor),
        Period::Year => local_start_for_ymd(anchor.year(), 1, 1, anchor),
    }
}

fn add_period_local(period: Period, start: DateTime<Local>) -> DateTime<Local> {
    match period {
        Period::Day => add_days_local(start, 1),
        Period::Week => add_days_local(start, 7),
        Period::Month => {
            let (year, month) = if start.month() == 12 {
                match start.year().checked_add(1) {
                    Some(year) => (year, 1),
                    None => return add_days_local(start, 31),
                }
            } else {
                (start.year(), start.month() + 1)
            };
            local_start_for_ymd(year, month, 1, add_days_local(start, 31))
        }
        Period::Year => match start.year().checked_add(1) {
            Some(year) => local_start_for_ymd(year, 1, 1, add_days_local(start, 366)),
            None => add_days_local(start, 366),
        },
    }
}

fn add_days_local(start: DateTime<Local>, days: i64) -> DateTime<Local> {
    let fallback = start + Duration::days(days);
    match start.date_naive().checked_add_signed(Duration::days(days)) {
        Some(date) => local_start_of_day(date, fallback),
        None => fallback,
    }
}

fn local_start_for_ymd(
    year: i32,
    month: u32,
    day: u32,
    fallback: DateTime<Local>,
) -> DateTime<Local> {
    match NaiveDate::from_ymd_opt(year, month, day) {
        Some(date) => local_start_of_day(date, fallback),
        None => fallback,
    }
}

fn local_start_of_day(date: NaiveDate, fallback: DateTime<Local>) -> DateTime<Local> {
    let midnight = match date.and_hms_opt(0, 0, 0) {
        Some(value) => value,
        None => return fallback,
    };

    match Local.from_local_datetime(&midnight) {
        LocalResult::Single(value) => value,
        LocalResult::Ambiguous(first, _) => first,
        LocalResult::None => first_valid_local_after(midnight, fallback),
    }
}

fn first_valid_local_after(
    start: chrono::NaiveDateTime,
    fallback: DateTime<Local>,
) -> DateTime<Local> {
    for minutes_after in 1_i64..=180 {
        let candidate = match start.checked_add_signed(Duration::minutes(minutes_after)) {
            Some(value) => value,
            None => return fallback,
        };
        match Local.from_local_datetime(&candidate) {
            LocalResult::Single(value) => return value,
            LocalResult::Ambiguous(first, _) => return first,
            LocalResult::None => {}
        }
    }
    fallback
}

pub type Snapshot = EngineSnapshot;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct EngineSnapshot {
    pub generated_at: DateTime<Utc>,
    pub usage_events: Vec<UsageEvent>,
    pub focus_rows: Vec<FocusRecord>,
    pub limit_windows: Vec<LimitWindow>,
    pub providers: Vec<ProviderStatus>,
    /// The declared `Capability` of every provider considered this collection, captured
    /// before the `Box<dyn Provider>` set was consumed — present even for providers that
    /// were missing or errored, so the Providers tab (T11) can render each lane's source
    /// and what is unavailable regardless of detection outcome. Parallel to `providers`,
    /// joined by `ProviderId`.
    pub capabilities: Vec<ProviderCapabilityView>,
}

impl EngineSnapshot {
    fn empty(generated_at: DateTime<Utc>) -> Self {
        Self {
            generated_at,
            usage_events: Vec::new(),
            focus_rows: Vec::new(),
            limit_windows: Vec::new(),
            providers: Vec::new(),
            capabilities: Vec::new(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProviderStatus {
    pub provider: ProviderId,
    pub status: ProviderStatusKind,
    pub files: usize,
    pub usage_events: usize,
    pub focus_rows: usize,
    pub limit_windows: usize,
    pub message: Option<String>,
}

/// An owned projection of a provider's [`Capability`] descriptor (§2b), captured per
/// provider during collection so a consumer (the Providers tab, T11) can render *what is
/// available and why* without re-instantiating the `Box<dyn Provider>` set.
///
/// `Capability` is `Copy` but its `quota_kinds: &'static [LimitKind]` borrows a static
/// slice, which blocks `Deserialize`; this view owns a `Vec<LimitKind>` instead so the
/// whole [`EngineSnapshot`] stays `Serialize`/`Deserialize`. Honest by construction: a
/// lane with no clean source carries [`DataSource::Unavailable`], never a fabricated one.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProviderCapabilityView {
    pub provider: ProviderId,
    pub api_cost: DataSource,
    pub subscription_quota: DataSource,
    pub model_mix: DataSource,
    pub auth: AuthMethod,
    pub quota_kinds: Vec<LimitKind>,
}

impl ProviderCapabilityView {
    /// Project a provider's `Capability` into the owned, serializable view. Infallible —
    /// `capability()` is infallible and the only owning step is copying the static
    /// `quota_kinds` slice into a `Vec`.
    fn from_capability(provider: ProviderId, capability: Capability) -> Self {
        Self {
            provider,
            api_cost: capability.api_cost,
            subscription_quota: capability.subscription_quota,
            model_mix: capability.model_mix,
            auth: capability.auth,
            quota_kinds: capability.quota_kinds.to_vec(),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ProviderStatusKind {
    Available,
    /// Installed and healthy, but with no local cost/quota data by design — the
    /// detect-and-defer case (Cursor, whose usage and quota are live server RPCs,
    /// not local files). Distinct from `Missing` ("not installed") and `Available`
    /// ("local usage parsed"); the deferral detail rides `ProviderStatus.message`.
    Detected,
    Partial,
    Missing,
    Error,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Period {
    Day,
    Week,
    Month,
    Year,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum GroupBy {
    Model,
    App,
    Total,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CostLane {
    Api,
    SubscriptionEstimate,
    UnknownAccess,
}

impl CostLane {
    fn from_access_path(value: &str) -> Self {
        match value {
            "api" => Self::Api,
            "subscription" => Self::SubscriptionEstimate,
            _ => Self::UnknownAccess,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
pub struct PeriodRange {
    pub start: DateTime<Utc>,
    pub end: DateTime<Utc>,
}

impl PeriodRange {
    pub fn contains(&self, timestamp: DateTime<Utc>) -> bool {
        timestamp >= self.start && timestamp < self.end
    }
}

#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
pub struct GroupKey {
    pub kind: GroupBy,
    pub value: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct TokenTotals {
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_write: u64,
}

impl TokenTotals {
    pub fn total(&self) -> u64 {
        self.input + self.output + self.cache_read + self.cache_write
    }

    fn add(&mut self, token_type: &str, tokens: u64) {
        match token_type {
            "input" => self.input += tokens,
            "output" => self.output += tokens,
            "cache_read" => self.cache_read += tokens,
            "cache_write" => self.cache_write += tokens,
            _ => {}
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PricingCoverage {
    pub priced_rows: usize,
    pub missing_price_rows: usize,
    pub unknown_model_rows: usize,
}

impl PricingCoverage {
    fn add(&mut self, status: &str) {
        match status {
            PRICING_STATUS_PRICED => self.priced_rows += 1,
            PRICING_STATUS_UNKNOWN_MODEL => self.unknown_model_rows += 1,
            PRICING_STATUS_MISSING_PRICE => self.missing_price_rows += 1,
            _ => self.missing_price_rows += 1,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AggregateTotals {
    pub row_count: usize,
    pub billed_cost: Decimal,
    pub effective_cost: Decimal,
    pub currency: Option<String>,
    pub multiple_currencies: bool,
    pub tokens: TokenTotals,
    pub pricing_coverage: PricingCoverage,
    pub estimated_rows: usize,
}

impl AggregateTotals {
    fn add_row(&mut self, row: &FocusRecord) {
        self.row_count += 1;
        self.billed_cost += row.billed_cost;
        self.effective_cost += row.effective_cost;
        self.add_currency(&row.billing_currency);
        // Token totals come from x_ConsumedTokens (always populated), not
        // ConsumedQuantity, which is null on unpriced rows per FOCUS 1.3.
        self.tokens
            .add(&row.x_token_type, decimal_to_u64(row.x_consumed_tokens));
        self.pricing_coverage.add(&row.x_pricing_status);
        if row.x_estimated {
            self.estimated_rows += 1;
        }
    }

    fn add_currency(&mut self, currency: &str) {
        match &self.currency {
            None => self.currency = Some(currency.to_string()),
            Some(current) if current == currency => {}
            Some(_) => self.multiple_currencies = true,
        }
    }
}

impl Default for AggregateTotals {
    fn default() -> Self {
        Self {
            row_count: 0,
            billed_cost: Decimal::from(0),
            effective_cost: Decimal::from(0),
            currency: None,
            multiple_currencies: false,
            tokens: TokenTotals::default(),
            pricing_coverage: PricingCoverage::default(),
            estimated_rows: 0,
        }
    }
}

fn decimal_to_u64(value: Decimal) -> u64 {
    value.to_u64().unwrap_or_default()
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CostLaneSummary {
    pub group: GroupKey,
    pub lane: CostLane,
    pub totals: AggregateTotals,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LimitSummary {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub label: Option<String>,
    /// When the underlying reading was observed — threaded from the finalized
    /// [`LimitWindow::captured_at`] so the render layer (which only ever sees
    /// `LimitSummary`, never `LimitWindow`) can draw the always-on "as of HH:MM"
    /// freshness stamp (ARCHITECTURE). For `Unavailable` windows this is
    /// the UNIX-epoch sentinel, but those arms render no stamp so it never surfaces.
    pub captured_at: DateTime<Utc>,
    pub availability: LimitAvailability,
}

/// How usable a single quota reading is at the availability/render layer. Carries
/// the [`LimitMeasure`] (token-fraction or dollar `Spend`) so the render layer — which
/// only ever sees `LimitSummary.availability`, never the provider `LimitWindow` — can
/// format dollars without touching a type (ARCHITECTURE D1). `Estimated` lives
/// only here, never on `LimitWindow`; it is wired by T4/T6.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LimitAvailability {
    Available {
        measure: LimitMeasure,
        resets_at: DateTime<Utc>,
        reset_in_seconds: i64,
    },
    Partial {
        measure: Option<LimitMeasure>,
        resets_at: Option<DateTime<Utc>>,
        reset_in_seconds: Option<i64>,
        reason: String,
    },
    /// A present reading that failed cross-check (provider `LimitStatus::Unverified`).
    /// Surfaced honestly rather than as a confident number; wired by T4/T6.
    Unverified {
        measure: LimitMeasure,
        resets_at: Option<DateTime<Utc>>,
        reset_in_seconds: Option<i64>,
    },
    /// No provider reading — Costroid's own volume-based estimate stands in. Its
    /// producer (the volume-based fallback, T4: `estimate_or_unavailable` via
    /// `limit_availability`) and its render (T6) are built.
    Estimated {
        volume_tokens: u64,
        estimated_usd: Option<Decimal>,
    },
    Unavailable {
        reason: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct NowOptions {
    pub cost_period: Period,
    pub group_by: GroupBy,
}

impl Default for NowOptions {
    fn default() -> Self {
        Self {
            cost_period: Period::Week,
            group_by: GroupBy::Model,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TrendsOptions {
    pub period: Period,
    pub group_by: GroupBy,
}

impl Default for TrendsOptions {
    fn default() -> Self {
        Self {
            period: Period::Week,
            group_by: GroupBy::Model,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct EngineOptions {
    pub period: Period,
    pub group_by: GroupBy,
}

impl Default for EngineOptions {
    fn default() -> Self {
        Self {
            period: Period::Week,
            group_by: GroupBy::Model,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct NowSummary {
    pub generated_at: DateTime<Utc>,
    pub cost_period: PeriodRange,
    pub group_by: GroupBy,
    pub limits: Vec<LimitSummary>,
    pub current_costs: Vec<CostLaneSummary>,
    pub providers: Vec<ProviderStatus>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TrendsSummary {
    pub generated_at: DateTime<Utc>,
    pub period: Period,
    pub group_by: GroupBy,
    pub buckets: Vec<TrendBucket>,
    pub totals: Vec<CostLaneSummary>,
    pub providers: Vec<ProviderStatus>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TrendBucket {
    pub period: PeriodRange,
    pub group: GroupKey,
    pub lane: CostLane,
    pub totals: AggregateTotals,
}

/// The Models tab view (T12): per-model API spend + token mix fused with the frontier
/// overlay. A pure projection over the existing snapshot (no new pricing/bench math). Not
/// `Deserialize`/`Eq` — it carries the `Serialize`-only [`OverlayModel`] (Decimal/standing),
/// matching [`BenchView`]; it is a computed view, never persisted.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ModelsView {
    pub generated_at: DateTime<Utc>,
    /// Per-model rows, highest API spend first. Empty when there is no API usage.
    pub models: Vec<ModelRow>,
    /// True when the user has zero API-billed rows: the tab renders an explicit empty state,
    /// never a fabricated row (mirrors [`BenchView::no_api_usage`]).
    pub no_api_usage: bool,
    /// The bench hedge note + pricing date, so the tab can footnote its estimates.
    pub disclaimer: BenchDisclaimer,
    pub providers: Vec<ProviderStatus>,
}

/// One per-model row: the API-lane spend + token mix (now/trends-consistent
/// [`AggregateTotals`]) joined to the matching bench [`OverlayModel`]. `overlay == None` (or a
/// matched overlay with empty `appearances`) means the model is un-benchmarked — a GAP, never
/// a guessed standing.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ModelRow {
    /// The resolved catalog key (the frontier overlay's `model_id`) — a model's dated
    /// snapshots collapse to one key here, so it can differ from the raw `x_model` that
    /// `now`/`trends` group by; falls back to the raw id for an unknown model.
    pub model: String,
    /// API-lane spend + token mix + pricing coverage for this model.
    pub totals: AggregateTotals,
    /// The matched frontier overlay (standing + re-pricing), if any.
    pub overlay: Option<OverlayModel>,
}

/// The config-neutral INPUT to [`budget_view`] (T14): the user's monthly $ targets, parsed by
/// the apps/cli config layer from its TOML. Core never reads a file — it takes these as data.
/// Money is [`Decimal`] (never f64); per-tool keys are the `x_Tool` ids
/// (`claude-code`/`codex`/`cursor`). API-lane only — a flat-fee subscription is never given a
/// $ target (§170). `Deserialize` so the config layer can map into it directly.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct BudgetTargets {
    /// An optional overall monthly cap across all API-lane spend.
    pub total_monthly_usd: Option<Decimal>,
    /// Optional per-tool monthly caps, keyed by the `x_Tool` id.
    pub per_tool: BTreeMap<String, Decimal>,
}

impl BudgetTargets {
    /// True when the user set no target at all — the tab renders the honest "no budget set"
    /// empty state rather than a fabricated comparison.
    pub fn is_empty(&self) -> bool {
        self.total_monthly_usd.is_none() && self.per_tool.is_empty()
    }
}

/// Which spend a [`BudgetRow`] caps: one specific tool (by `x_Tool` id) or the overall total.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum BudgetScope {
    /// The overall monthly cap across every tool's API-lane spend.
    Total,
    /// A single tool's monthly cap, by `x_Tool` id.
    Tool(String),
}

/// The pace of a budget vs. how far through the month we are — a lightweight reference cue
/// (NOT the full month-end projection, which is the Forecast tab, T15).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum BudgetPace {
    /// Spending no faster than the month is elapsing (used share <= elapsed share).
    OnTrack,
    /// Spending faster than the month elapses (used share > elapsed share) — trending to exceed.
    AheadOfPace,
    /// Already over the monthly target.
    OverBudget,
}

/// One budget comparison: used vs. target for the current month, API-lane only. Every figure
/// is a local estimate (the `~` hedge is applied at the render layer).
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct BudgetRow {
    pub scope: BudgetScope,
    /// The monthly $ cap the user set.
    pub target_usd: Decimal,
    /// Actual API-lane spend so far this month (an estimate).
    pub spent_usd: Decimal,
    /// `spent / target` as a fraction; can exceed 1.0 when over budget.
    pub fraction: f64,
    /// `spent - target` when *strictly* over budget; `None` at or under (so an exactly-at-budget
    /// row is "at budget", never "over by $0.00").
    pub over_by_usd: Option<Decimal>,
    pub pace: BudgetPace,
}

/// A budgeted tool withheld from a $ comparison (§170): it has local usage but no API lane, so a
/// $ budget — which tracks API spend — cannot honestly apply. The `reason` keeps the tab from
/// asserting "subscription" when the billing path is merely unclassified.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct BudgetExcludedTool {
    /// The `x_Tool` id the user budgeted.
    pub tool: String,
    pub reason: BudgetExclusion,
}

/// Why a budgeted tool is withheld from the $ comparison.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum BudgetExclusion {
    /// Flat-fee subscription usage (subscription-lane rows present, no API lane).
    FlatFeeSubscription,
    /// Local usage exists but none is API-billed and its billing path is unclassified (e.g. a
    /// Codex/Claude install with no rate-limit/credential signal — its rows land in the
    /// `UnknownAccess` lane). Honest: not asserted to be a subscription, only that it is not
    /// API-billed, so a $ budget can't apply.
    NotApiBilled,
}

/// The Budget tab view (T14): the active budget comparisons + the tools withheld from a $
/// comparison + the honest empty state. A pure projection over the snapshot; carries `f64`
/// (not `Eq`) and is `Serialize`-only — a computed view, never persisted (mirrors [`ModelsView`]).
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct BudgetView {
    pub generated_at: DateTime<Utc>,
    /// The active budgets (per-tool + total), most-utilized first. Empty when none apply.
    pub rows: Vec<BudgetRow>,
    /// Tools the user budgeted that have local usage but no API lane, so a $ comparison is
    /// withheld (§170). Sorted by tool id; the tab notes each with its honest reason.
    pub excluded_tools: Vec<BudgetExcludedTool>,
    /// True when the user set no targets at all — render the "no budget set" empty state.
    pub no_budget_set: bool,
    /// Total API-lane spend this month across all tools (an estimate) — the header figure.
    pub spent_total_usd: Decimal,
    /// The fraction (0..=1) of the current month elapsed — the pace reference line.
    pub month_elapsed_fraction: f64,
}

/// The Forecast tab view (T15): a month-end $ spend projection + per-quota-window exhaustion
/// ETAs, every figure a labeled estimate. A pure projection over the snapshot; carries `f64`/
/// `Decimal` and is `Serialize`-only (not `Eq`/`Deserialize`) — a computed view, never persisted
/// (mirrors [`BudgetView`]/[`ModelsView`]).
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ForecastView {
    pub generated_at: DateTime<Utc>,
    /// True when the snapshot has zero API-lane rows at all: render the honest "nothing to
    /// forecast yet" empty state rather than a fabricated $0 projection. Quota ETAs (a separate,
    /// subscription-window axis) may still be present.
    pub no_api_usage: bool,
    /// The month-end $ spend projection, or the honest insufficient-data state below the floor.
    pub spend: SpendForecast,
    /// Per-**UTC-day** actual API-lane spend for the current month so far (ascending) — the
    /// sparkline series. Empty when there is no spend this month.
    pub daily_actuals: Vec<ForecastDay>,
    /// One projected exhaustion ETA per quota window (parallel to the Now view's limits); each is
    /// a projected instant, a "resets before you hit it", or a typed "unavailable" (degrade,
    /// never a confident wrong ETA).
    pub quota_etas: Vec<QuotaEta>,
}

/// One UTC day's actual API-lane estimated spend — a sparkline datum.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ForecastDay {
    pub date: NaiveDate,
    /// The day's summed API-lane estimate (exact [`Decimal`]; always an estimate).
    pub spent_usd: Decimal,
}

/// The month-end $ projection (always an estimate), or the honest below-floor state.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SpendForecast {
    /// Enough of the month elapsed (`days_elapsed >= MIN_FORECAST_DAYS`) to extrapolate the
    /// run-rate: `projected_month_usd = spend_to_date_usd / days_elapsed × days_in_month`, all on
    /// the UTC calendar (numerator + denominator share one calendar).
    Projected {
        projected_month_usd: Decimal,
        spend_to_date_usd: Decimal,
        days_elapsed: u32,
        days_in_month: u32,
    },
    /// Too little of the month has elapsed (`days_elapsed < min_days`) for a stable run-rate —
    /// the projection is suppressed in favor of this honest state.
    InsufficientData {
        spend_to_date_usd: Decimal,
        days_elapsed: u32,
        days_in_month: u32,
        min_days: u32,
    },
}

/// One quota window's projected exhaustion ETA (T15). Projected ONLY off a fresh, cross-checked
/// [`LimitAvailability::Available`] token-fraction; every other availability arm degrades to
/// [`QuotaEtaOutcome::Unavailable`] (ARCHITECTURE — degrade, never a confident wrong ETA).
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct QuotaEta {
    pub tool: ProviderId,
    pub kind: LimitKind,
    pub outcome: QuotaEtaOutcome,
}

/// A quota window's ETA outcome: a projected exhaustion instant, a "resets first", or a typed
/// "unavailable". Carries `f64` so it is `PartialEq` but not `Eq`.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum QuotaEtaOutcome {
    /// At the current burn rate the window is projected to exhaust at `at` — before it resets.
    ProjectedHit { at: DateTime<Utc>, fraction: f64 },
    /// At the current burn rate the window resets (`resets_at`) before it would exhaust.
    ResetsFirst {
        resets_at: DateTime<Utc>,
        fraction: f64,
    },
    /// No trustworthy projection — a typed reason (never a confident guess).
    Unavailable { reason: QuotaEtaUnavailable },
}

/// Why a quota ETA could not be projected — typed, so the render names the honest reason.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum QuotaEtaUnavailable {
    /// The reading is not a fresh, cross-checked `Available` token-fraction — it is
    /// `Unverified`/`Estimated`/`Partial`/`Unavailable`, or a dollar `Spend` measure. Degraded
    /// per ARCHITECTURE (stale is already aged-out to `Estimated` upstream).
    ReadingNotProjectable,
    /// Too little of the window has elapsed (or a clock skew) to estimate a burn rate.
    WindowJustStarted,
}

/// The Anomalies tab view (T16, widened to all-lane model-mix in T16b): proactive callouts vs the
/// user's OWN recent history — two signals with **asymmetric lane scopes** (a daily spend spike,
/// **API-lane $**, and a model-mix shift, **all-lane** token share), each with a magnitude + the
/// compared window. A pure projection over the snapshot; carries `Decimal` and is `Serialize`-only
/// (not `Eq`/`Deserialize`) — a computed view, never persisted (mirrors [`ForecastView`]). The
/// quota-burn signal the original plan named is DEFERRED (local data has no multi-day quota
/// history, §11.5) — surfaced honestly in the render footnote, never faked.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct AnomaliesView {
    pub generated_at: DateTime<Utc>,
    /// Distinct active UTC days of **all-lane token** history within the trailing window (the
    /// universal basis) — both the suppression input and the compared-window size shown to the
    /// user. The spend spike additionally has its own API-lane-$ day gate (it may differ); each
    /// [`Anomaly::baseline_days`] reports the realized window for THAT signal.
    pub history_days: usize,
    /// The minimum-history floor (days); below it `enough_history` is false and `anomalies` is
    /// empty (render the honest "not enough history yet - N of M days" state).
    pub min_history_days: usize,
    /// The trailing-baseline window length (UTC days) — the "your N-day norm" the callouts cite.
    pub baseline_days: usize,
    /// True when `history_days >= min_history_days` — only then are anomalies computed. Lets the
    /// render distinguish "not enough history yet" from the honest "no anomalies" clean state.
    pub enough_history: bool,
    /// True when there is **no usage at all** (`history_days == 0` — the all-lane token series is
    /// empty). After T16b the model-mix signal counts every lane, so a subscription-only user (e.g.
    /// Claude Code Max with no API key) IS covered once they accrue enough token-days — this is now
    /// a **TRANSIENT** zero-state that fills in as usage accrues, NOT the old permanent
    /// no-coverage state. The render branches on it FIRST with transient "no usage recorded yet"
    /// copy, distinct from the thin-history "N of M days" line (mirrors the zero-state idea of
    /// [`ForecastView::no_api_usage`] / [`ModelsView::no_api_usage`], but all-lane and transient).
    pub no_usage: bool,
    /// The detected anomalies — the spend spike (if any) first, then model-mix shifts most-deviant
    /// first. Empty when there is not enough history OR usage is in line with the norm.
    pub anomalies: Vec<Anomaly>,
}

/// One detected anomaly: a value in the user's recent history that deviates from their OWN
/// trailing-window median by more than the conservative `3.5·MAD` threshold (with an absolute
/// floor so a near-flat history's `MAD=0` never flags trivia). Every figure is an estimate.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Anomaly {
    /// Which signal fired (and its subject — the day for a spike, the model for a mix shift).
    pub signal: AnomalySignal,
    /// The anomalous value: the day's API-lane spend `$` for a [`AnomalySignal::SpendSpike`], or
    /// the model's **all-lane** share-of-tokens (`0..=1`) for a [`AnomalySignal::ModelMixShift`] —
    /// exact [`Decimal`].
    pub value: Decimal,
    /// The baseline: the median of the trailing-window values this is compared against (a `$`
    /// median for a spike, a share median for a mix shift).
    pub baseline_median: Decimal,
    /// `|value − baseline_median|` — the absolute deviation that cleared the threshold.
    pub deviation: Decimal,
    /// `value ÷ baseline_median` — the "~N× your norm" multiple, `None` when the median is `0`
    /// (the multiple is undefined; the render falls back to the absolute figures).
    pub magnitude: Option<Decimal>,
    /// The number of trailing-window days the baseline was computed over (the compared window) — the
    /// signal's OWN realized basis: the **API-lane-$** day count for a [`AnomalySignal::SpendSpike`]
    /// and the **all-lane token-day** count ([`AnomaliesView::history_days`]) for a
    /// [`AnomalySignal::ModelMixShift`]; the two MAY differ (T16b).
    pub baseline_days: usize,
}

/// Which anomaly signal fired (T16 ships two; the quota-burn signal is deferred — local data has
/// no multi-day quota history, §11.5).
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AnomalySignal {
    /// A UTC day whose total **API-lane** spend deviates (high-side) from the trailing-window
    /// median ($ is API-lane only — subscription lanes are not a summable dollar).
    SpendSpike { date: NaiveDate },
    /// A model whose **all-lane** share-of-tokens on the latest active day deviates from its own
    /// trailing-window median share (a token share is lane-agnostic, so this serves
    /// subscription-only users too — T16b).
    ModelMixShift { model: String },
}

/// The config-neutral INPUT to [`active_alerts`] (T17): the quota fraction thresholds at which a
/// WARN / CRITICAL alert fires. Core never reads a file — the apps/cli config layer maps its
/// `[alerts]` TOML section into this (defaults when unset). Budget alerts need no fraction here —
/// they ride [`BudgetView`]'s own STRICT over-target state, so the two alert classes never share a
/// threshold. Carries `f64`, so `PartialEq` not `Eq`.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct AlertThresholds {
    /// Fire a WARN quota alert at/above this consumed fraction (default [`ALERT_WARN_FRACTION`]).
    pub quota_warn_fraction: f64,
    /// Fire a CRITICAL quota alert at/above this consumed fraction (default
    /// [`ALERT_CRITICAL_FRACTION`]).
    pub quota_critical_fraction: f64,
}

impl Default for AlertThresholds {
    fn default() -> Self {
        Self {
            quota_warn_fraction: ALERT_WARN_FRACTION,
            quota_critical_fraction: ALERT_CRITICAL_FRACTION,
        }
    }
}

/// The opt-in **advisory** alert sources (T17b), passed to [`active_alerts`] as borrowed,
/// already-computed views. Each is `Some(view)` only when its OWN default-off sub-flag is on, and
/// `None` when off — so the master `enabled` switch (enforced upstream by the apps/cli config
/// layer) still gates the whole feature, these are sub-flags within it. Config-neutral like
/// [`AlertThresholds`]: core reads no file and computes no view here — the caller decides each flag
/// and hands in the view. With both `None` (the [`Default`]) `active_alerts` is byte-identical to
/// its T17 quota+budget output. The borrows let the detector read without cloning; the struct is
/// `Copy` (two `Option<&_>`), so it is cheap to pass.
#[derive(Debug, Clone, Copy, Default)]
pub struct AdvisoryAlerts<'a> {
    /// `Some(forecast)` enables the TOTAL-budget projection source ([`Alert::Forecast`]); `None` =
    /// off. Total-scope only — [`forecast_view`] projects the total, not per-tool.
    pub forecast: Option<&'a ForecastView>,
    /// `Some(anomalies)` enables the spend-spike source ([`Alert::SpendSpike`]); `None` = off. Only
    /// [`AnomalySignal::SpendSpike`] anomalies alert — model-mix shifts never do.
    pub anomalies: Option<&'a AnomaliesView>,
}

/// The severity of a quota alert — WARN (near the limit) vs CRITICAL (very near / at it). Budget
/// alerts carry no level: a budget alert fires only when STRICTLY over target, an inherently
/// critical-tier state (see [`Alert::is_critical`]).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AlertLevel {
    Warn,
    Critical,
}

/// One active alert the user opted in to see. The sources are SEPARATE variants and are NEVER
/// mixed: the two hard crossings — a quota window over a % threshold ([`Alert::Quota`]) and a
/// budget over its monthly $ target ([`Alert::Budget`]) — plus the two opt-in advisory heads-ups
/// (T17b) — a month-end projection over the total budget ([`Alert::Forecast`]) and a daily spend
/// spike ([`Alert::SpendSpike`]). A computed signal carrying `f64`/`Decimal` — `Serialize`-only,
/// `PartialEq` (not `Eq`), never persisted (mirrors the Step-5 view types). Its producer
/// [`active_alerts`] keeps it honest: a quota alert fires ONLY off a fresh, cross-checked
/// [`LimitAvailability::Available`] reading, the advisory sources only when their sub-flag is on.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "snake_case", tag = "class")]
pub enum Alert {
    /// A quota window at/above a fraction threshold (the % class). `fraction` is the consumed
    /// share (a token-fraction directly, or `used/included` for a dollar credit pool); `level` is
    /// WARN vs CRITICAL per the [`AlertThresholds`]. The render frames this as quota-extension,
    /// never money.
    Quota {
        tool: ProviderId,
        kind: LimitKind,
        level: AlertLevel,
        fraction: f64,
        /// Seconds until the window resets — so the copy can name the reset wait.
        reset_in_seconds: i64,
    },
    /// A budget scope STRICTLY over its monthly $ target (the $ class) — rides [`BudgetRow`]'s
    /// `over_by_usd` (API-lane only). Every figure is an estimate (the `~` hedge is the render
    /// layer's).
    Budget {
        scope: BudgetScope,
        spent_usd: Decimal,
        target_usd: Decimal,
        over_by_usd: Decimal,
    },
    /// A month-end PROJECTION expected to exceed the user's TOTAL budget target — the advisory
    /// forecast class (T17b), opt-in via [`AdvisoryAlerts::forecast`]. Total-scope only (the
    /// forecast projects the total, not per-tool); fires only off a real
    /// [`SpendForecast::Projected`] strictly over a not-already-over total target (see
    /// [`forecast_budget_alert`]). Advisory-tier: `is_critical()` is false. Every figure is an
    /// estimate (the `~` hedge is the render layer's).
    Forecast {
        /// The projected month-end API-lane spend (an estimate).
        projected_month_usd: Decimal,
        /// The TOTAL monthly $ target the projection is expected to exceed.
        target_usd: Decimal,
        /// `projected_month_usd − target_usd` — the projected overage (always strictly positive).
        projected_over_by_usd: Decimal,
    },
    /// A daily API-lane spend SPIKE vs the user's own trailing-window norm — the advisory anomaly
    /// class (T17b), opt-in via [`AdvisoryAlerts::anomalies`]. Mirrors one
    /// [`AnomalySignal::SpendSpike`] from `anomalies_view` (already gated on enough history + the
    /// conservative `3.5·MAD`). Advisory-tier: `is_critical()` is false. Every figure is an
    /// estimate (the `~` hedge is the render layer's).
    SpendSpike {
        /// The UTC day that spiked.
        date: NaiveDate,
        /// The day's API-lane spend (an estimate).
        value_usd: Decimal,
        /// The trailing-window $ median the spike is compared against.
        baseline_median_usd: Decimal,
        /// `value ÷ baseline_median` — the "~N× your norm" multiple, `None` when the median is `0`
        /// (the multiple is undefined; the render falls back to a descriptive phrase).
        magnitude: Option<Decimal>,
    },
}

impl Alert {
    /// True for a critical-tier crossing (a CRITICAL quota reading or any over-budget state) — the
    /// render uses this to pick the `!!` cue / red style; a WARN quota and both advisory sources
    /// (forecast projection, spend spike) are the lighter `!` / amber heads-up tier.
    pub fn is_critical(&self) -> bool {
        match self {
            Alert::Quota { level, .. } => *level == AlertLevel::Critical,
            Alert::Budget { .. } => true,
            // Advisory heads-ups, never a hard crossing.
            Alert::Forecast { .. } | Alert::SpendSpike { .. } => false,
        }
    }
}

#[derive(Debug, Error)]
pub enum CoreError {
    #[error("bundled pricing JSON is invalid: {0}")]
    PricingJson(#[from] serde_json::Error),

    #[error("bundled pricing table is invalid: {0}")]
    PricingValidation(String),

    #[error("bundled benchmark data is invalid: {0}")]
    BenchValidation(String),

    #[error("FOCUS export failed: {0}")]
    Focus(#[from] FocusError),

    #[error("import failed: {0}")]
    Import(String),
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{LocalResult, TimeZone, Timelike, Weekday};
    use costroid_focus::{PRICING_CATEGORY_STANDARD, PRICING_STATUS_MISSING_PRICE};
    use costroid_providers::{
        AuthMethod, Capability, CursorProvider, DataLocation, DataSource, ProviderError,
    };
    use std::path::PathBuf;

    fn fixture_path(parts: &[&str]) -> PathBuf {
        let mut path = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        path.push("..");
        path.push("..");
        path.push("fixtures");
        for part in parts {
            path.push(part);
        }
        path
    }

    fn timestamp() -> DateTime<Utc> {
        utc_datetime(2026, 1, 1, 10, 0, 0)
    }

    fn utc_datetime(
        year: i32,
        month: u32,
        day: u32,
        hour: u32,
        minute: u32,
        second: u32,
    ) -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(year, month, day, hour, minute, second) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        }
    }

    fn naive_date(year: i32, month: u32, day: u32) -> NaiveDate {
        match NaiveDate::from_ymd_opt(year, month, day) {
            Some(date) => date,
            None => panic!("test date should be valid"),
        }
    }

    fn usage_event(
        tool: ProviderId,
        access_path: AccessPath,
        timestamp: DateTime<Utc>,
    ) -> UsageEvent {
        UsageEvent {
            tool,
            model: "gpt-5.5".to_string(),
            timestamp,
            input_tokens: 10,
            output_tokens: 20,
            cache_read_tokens: 30,
            cache_write_tokens: 0,
            project: Some("/work/project".to_string()),
            access_path,
            is_sidechain: false,
        }
    }

    fn record(
        access_path: FocusAccessPath,
        timestamp: DateTime<Utc>,
        model: &str,
        project: Option<&str>,
        token_type: TokenType,
        token_count: u64,
    ) -> FocusRecord {
        match FocusRecord::unpriced_usage(UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp,
            tool: "codex".to_string(),
            model: model.to_string(),
            token_type,
            token_count,
            project: project.map(ToString::to_string),
            access_path,
            service_name: "Codex".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        }) {
            Ok(value) => value,
            Err(err) => panic!("record should build: {err}"),
        }
    }

    fn snapshot_with_rows(
        generated_at: DateTime<Utc>,
        focus_rows: Vec<FocusRecord>,
        limit_windows: Vec<LimitWindow>,
    ) -> EngineSnapshot {
        EngineSnapshot {
            generated_at,
            usage_events: Vec::new(),
            focus_rows,
            limit_windows,
            providers: Vec::new(),
            capabilities: Vec::new(),
        }
    }

    fn api_cost_row(group: &str, billed: Decimal) -> CostLaneSummary {
        CostLaneSummary {
            group: GroupKey {
                kind: GroupBy::Model,
                value: group.to_string(),
            },
            lane: CostLane::Api,
            totals: AggregateTotals {
                billed_cost: billed,
                ..AggregateTotals::default()
            },
        }
    }

    fn now_summary_with_costs(current_costs: Vec<CostLaneSummary>) -> NowSummary {
        let at = timestamp();
        NowSummary {
            generated_at: at,
            cost_period: PeriodRange { start: at, end: at },
            group_by: GroupBy::Model,
            limits: Vec::new(),
            current_costs,
            providers: Vec::new(),
        }
    }

    #[test]
    fn format_money_usd_groups_rounds_and_hedges() {
        assert_eq!(format_money_usd(&Decimal::new(4218, 2), true), "~$42.18");
        assert_eq!(format_money_usd(&Decimal::new(4218, 2), false), "$42.18");
        // Thousands separators, no hedge.
        assert_eq!(
            format_money_usd(&Decimal::new(123456, 2), false),
            "$1,234.56"
        );
        // Zero pads to two decimals and still hedges.
        assert_eq!(format_money_usd(&Decimal::ZERO, true), "~$0.00");
        // A whole-dollar value pads cents.
        assert_eq!(format_money_usd(&Decimal::from(7), true), "~$7.00");
    }

    #[test]
    fn now_api_spend_display_sums_api_lane_only() {
        // Two API-lane rows sum; a subscription-lane row is excluded from the dollar total.
        let mut subscription = api_cost_row("claude-opus", Decimal::new(9999, 2));
        subscription.lane = CostLane::SubscriptionEstimate;
        let summary = now_summary_with_costs(vec![
            api_cost_row("gpt-5.5", Decimal::new(1250, 2)),
            api_cost_row("claude-sonnet", Decimal::new(3000, 2)),
            subscription,
        ]);
        assert_eq!(now_api_spend_display(&summary), "~$42.50");
    }

    #[test]
    fn now_api_spend_display_is_zero_when_no_api_usage() {
        // No fabricated absence: zero API spend is the honest sum, hedged.
        assert_eq!(
            now_api_spend_display(&now_summary_with_costs(Vec::new())),
            "~$0.00"
        );
    }

    // ----- T6: the LedgerLane dev-tool gate + per-lane totals (the "lanes never summed
    // across" invariant). These prove that a cloud_api / local_inference row never moves the
    // developer-tool dollar total, and that each lane has its OWN summable total. At v0.6.0 every
    // shipped row is developer_tool, so every gate is a no-op on real data; the synthetic
    // mixed-lane fixtures below are what exercise the guard. -----

    /// One FOCUS row on an arbitrary `lane` + `access_path`, with `billed_cost`/`effective_cost`
    /// overwritten to `whole_dollars` (the summers read these). `charge_period_start` defaults to
    /// `when` so the row falls inside whatever period the test scopes.
    fn lane_record(
        lane: LedgerLane,
        access_path: FocusAccessPath,
        when: DateTime<Utc>,
        whole_dollars: i64,
    ) -> FocusRecord {
        let mut record = match FocusRecord::unpriced_usage(UnpricedUsage {
            lane,
            timestamp: when,
            tool: "codex".to_string(),
            model: "gpt-5.5".to_string(),
            token_type: TokenType::Output,
            token_count: 1_000,
            project: Some("/work/project".to_string()),
            access_path,
            service_name: "Codex".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        }) {
            Ok(record) => record,
            Err(err) => panic!("lane record should build: {err}"),
        };
        let cost = Decimal::from(whole_dollars);
        record.billed_cost = cost;
        record.effective_cost = cost;
        // A priced status so the per-window estimated-$ summer (window_estimated_usd) does not
        // short-circuit to None on these synthetic rows.
        record.x_pricing_status = PRICING_STATUS_PRICED.to_string();
        record
    }

    /// The canonical T6 mixed-lane fixture: exactly one developer_tool + Api row ($5), one
    /// cloud_api row ($7), and one local_inference row ($11). All three share an access_path of
    /// `Api`, so the ONLY thing separating the cloud/local rows from the dev-tool total is the
    /// `x_Lane` gate under test (an `access_path`-only filter would wrongly fold in all $23).
    fn mixed_lane_rows(when: DateTime<Utc>) -> Vec<FocusRecord> {
        vec![
            lane_record(LedgerLane::DeveloperTool, FocusAccessPath::Api, when, 5),
            lane_record(LedgerLane::CloudApi, FocusAccessPath::Api, when, 7),
            lane_record(LedgerLane::LocalInference, FocusAccessPath::Api, when, 11),
        ]
    }

    #[test]
    fn dev_tool_total_excludes_cloud_and_local_lanes() {
        // The mixed-lane guard: the developer-tool API total — via BOTH the now-summary path
        // (now_api_spend_display over a real snapshot) AND lane_total_usd(DeveloperTool) — equals
        // ONLY the $5 developer_tool row. The $7 cloud + $11 local rows do NOT move it, even though
        // all three carry access_path = Api (so a CostLane-only summer would have summed $23).
        let when = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(when);
        let snapshot = snapshot_with_rows(when, rows.clone(), Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());
        // The developer-tool API total surfaced to the CLI/taskbar is $5 only — not $12, not $23.
        assert_eq!(now_api_spend_display(&summary), "~$5.00");
        // current_costs holds ONE Api-lane row (the cloud/local rows were gated out before the
        // CostLane grouping), and its billed_cost is the developer_tool $5.
        let api_rows: Vec<&CostLaneSummary> = summary
            .current_costs
            .iter()
            .filter(|row| row.lane == CostLane::Api)
            .collect();
        assert_eq!(
            api_rows.len(),
            1,
            "only the dev-tool Api row survives the gate"
        );
        assert_eq!(api_rows[0].totals.billed_cost, Decimal::from(5));

        // The typed per-lane helper agrees: the dev-tool lane total is exactly $5.
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::DeveloperTool),
            Decimal::from(5)
        );
    }

    #[test]
    fn per_lane_totals_are_independent() {
        // Per-lane independence: each lane sums only its own rows. Adding cloud/local dollars never
        // changes another lane's total.
        let when = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(when);
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::DeveloperTool),
            Decimal::from(5)
        );
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::CloudApi),
            Decimal::from(7)
        );
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::LocalInference),
            Decimal::from(11)
        );
    }

    #[test]
    fn grand_total_is_the_sum_of_disjoint_lanes() {
        // Grand-total invariant: the grand total is $23, equals the sum of the three lane totals,
        // and (because other lanes are non-zero) is strictly greater than the dev-tool lane alone —
        // i.e. the lanes are DISJOINT and partition the rows, never each equal to the grand total.
        let when = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(when);

        assert_eq!(grand_total_usd(&rows), Decimal::from(23));
        assert_eq!(
            grand_total_usd(&rows),
            lane_total_usd(&rows, LedgerLane::DeveloperTool)
                + lane_total_usd(&rows, LedgerLane::CloudApi)
                + lane_total_usd(&rows, LedgerLane::LocalInference),
            "lanes sum to the grand total"
        );
        assert_ne!(
            lane_total_usd(&rows, LedgerLane::DeveloperTool),
            grand_total_usd(&rows),
            "a single lane is NOT the grand total when other lanes are non-zero"
        );
    }

    #[test]
    fn dev_tool_only_data_is_unchanged_by_the_gate() {
        // v0.6.0 no-regression: on all-developer_tool data the gate is a no-op — the dev-tool API
        // total equals the grand total (no other lane exists to subtract), so existing behavior is
        // byte-for-byte preserved.
        let when = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = vec![
            lane_record(LedgerLane::DeveloperTool, FocusAccessPath::Api, when, 5),
            lane_record(LedgerLane::DeveloperTool, FocusAccessPath::Api, when, 7),
        ];
        let snapshot = snapshot_with_rows(when, rows.clone(), Vec::new());
        let summary = now_summary(&snapshot, NowOptions::default());
        assert_eq!(now_api_spend_display(&summary), "~$12.00");
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::DeveloperTool),
            Decimal::from(12)
        );
        assert_eq!(
            grand_total_usd(&rows),
            lane_total_usd(&rows, LedgerLane::DeveloperTool)
        );
    }

    #[test]
    fn aggregate_rows_matches_the_internal_now_summary_aggregation() {
        // T8: the public store-replay wrapper produces exactly what the internal aggregator
        // does — equal to now_summary().current_costs over a snapshot of the same rows (the
        // canonical internal consumer of summarize_rows). The mixed-lane fixture also proves
        // aggregate_rows INHERITS the T6 developer-tool gate: only the $5 dev-tool Api row
        // survives; the $7 cloud + $11 local rows (all access_path = Api) are excluded.
        let when = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(when);

        let aggregated = aggregate_rows(&rows, GroupBy::Model);

        // (a) Matches the internal aggregation that feeds now_summary.current_costs.
        let snapshot = snapshot_with_rows(when, rows.clone(), Vec::new());
        let summary = now_summary(&snapshot, NowOptions::default());
        assert_eq!(aggregated, summary.current_costs);

        // (b) Non-vacuous: exactly one Api lane summary survives the dev-tool gate, and its
        // billed_cost is the dev-tool $5 only (NOT $12, NOT the $23 grand total).
        assert_eq!(
            aggregated.len(),
            1,
            "only the dev-tool Api row survives the gate"
        );
        assert_eq!(aggregated[0].lane, CostLane::Api);
        assert_eq!(aggregated[0].totals.billed_cost, Decimal::from(5));
        assert_eq!(aggregated[0].totals.row_count, 1);
    }

    #[test]
    fn budget_view_dev_tool_spend_excludes_cloud_and_local_lanes() {
        // The Budget tab's monthly API spend is developer-tool-only: against the mixed-lane fixture
        // (codex tool, all access_path = Api), the figure for `codex` is $5 — the cloud $7 + local
        // $11 are on their own lanes and never inflate the budget figure.
        let when = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = mixed_lane_rows(when);
        let snapshot = snapshot_with_rows(when, rows, Vec::new());
        let view = budget_view(
            &snapshot,
            &budget_targets(Some(10_000), &[("codex", 10_000)]),
        );

        let codex = view
            .rows
            .iter()
            .find(|row| matches!(&row.scope, BudgetScope::Tool(t) if t == "codex"));
        let codex = match codex {
            Some(row) => row,
            None => panic!("codex budget row should exist: {:?}", view.rows),
        };
        assert_eq!(codex.spent_usd, Decimal::from(5), "dev-tool spend only");
        // The grand $/total row also sees only the dev-tool $5, not $23.
        let total = view
            .rows
            .iter()
            .find(|row| matches!(row.scope, BudgetScope::Total));
        if let Some(total) = total {
            assert_eq!(total.spent_usd, Decimal::from(5), "total is dev-tool only");
        }
    }

    #[test]
    fn window_estimated_usd_excludes_cloud_and_local_lanes() {
        // The per-window estimated-$ (a developer-tool quota-window figure) sums developer_tool rows
        // only. All three mixed-lane rows fall inside Codex's 5-hour window and are priced, so an
        // ungated summer would report $23; the gated summer reports $5.
        let now = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(now);
        let estimated = window_estimated_usd(&rows, ProviderId::Codex, LimitKind::FiveHour, now);
        assert_eq!(estimated, Some(Decimal::from(5)), "dev-tool window $ only");
    }

    #[test]
    fn window_token_volume_excludes_cloud_and_local_lanes() {
        // Symmetry with the $-side guard above: the per-window TOKEN volume — which feeds the
        // Verified→Unverified cross-check (`finalize_limit_status`, compared to
        // UNVERIFIED_TOKEN_FLOOR) — must sum developer_tool rows only. All three mixed-lane rows
        // carry x_tool="codex", 1_000 output tokens, inside Codex's 5-hour window, so an ungated
        // summer would report 3_000 (and could mask a demotion); the gated summer reports 1_000.
        let now = utc_datetime(2026, 1, 1, 12, 0, 0);
        let rows = mixed_lane_rows(now);
        let volume = window_token_volume(&rows, ProviderId::Codex, LimitKind::FiveHour, now);
        assert_eq!(
            volume.total(),
            1_000,
            "only the developer_tool row's tokens count toward the window volume"
        );
    }

    #[test]
    fn now_model_spend_breakdown_sorts_hedges_and_normalizes() {
        // API-lane only, highest spend first; a subscription-lane row is excluded.
        let mut subscription = api_cost_row("claude-opus", Decimal::new(9999, 2));
        subscription.lane = CostLane::SubscriptionEstimate;
        let summary = now_summary_with_costs(vec![
            api_cost_row("gpt-5.5", Decimal::new(1250, 2)),
            api_cost_row("claude-sonnet", Decimal::new(5000, 2)),
            subscription,
        ]);
        let breakdown = now_model_spend_breakdown(&summary);
        assert_eq!(breakdown.len(), 2, "subscription lane excluded");
        // Highest spend leads and is the full-length share bar.
        assert_eq!(breakdown[0].model, "claude-sonnet");
        assert_eq!(breakdown[0].spend_display, "~$50.00");
        assert!((breakdown[0].fraction - 1.0).abs() < 1e-9);
        // The smaller model is a 0.25 share of the largest (12.50 / 50.00).
        assert_eq!(breakdown[1].model, "gpt-5.5");
        assert!((breakdown[1].fraction - 0.25).abs() < 1e-9);
    }

    #[test]
    fn now_model_spend_breakdown_handles_empty_and_zero() {
        // No usage → no rows (never a fabricated $0 model).
        assert!(now_model_spend_breakdown(&now_summary_with_costs(Vec::new())).is_empty());
        // All-zero spend → present rows, but an honest flat 0.0 share (never a 0/0).
        let summary = now_summary_with_costs(vec![
            api_cost_row("gpt-5.5", Decimal::ZERO),
            api_cost_row("claude-sonnet", Decimal::ZERO),
        ]);
        let breakdown = now_model_spend_breakdown(&summary);
        assert_eq!(breakdown.len(), 2);
        assert!(breakdown.iter().all(|row| row.fraction == 0.0));
    }

    #[test]
    fn models_view_includes_api_lane_excludes_subscription() {
        // The Models tab is API-cost only (the frontier is API-only) — a subscription-lane
        // use of the same model must not add a second row.
        let events = [
            usage_event(ProviderId::Codex, AccessPath::Api, timestamp()),
            usage_event(
                ProviderId::ClaudeCode,
                AccessPath::Subscription,
                timestamp(),
            ),
        ];
        let focus_rows = match focus_records_from_usage(&events) {
            Ok(rows) => rows,
            Err(err) => panic!("events should price: {err}"),
        };
        let snapshot = snapshot_with_rows(timestamp(), focus_rows, Vec::new());

        let view = match models_view(&snapshot) {
            Ok(view) => view,
            Err(err) => panic!("models view should build: {err}"),
        };

        assert!(!view.no_api_usage);
        assert_eq!(view.models.len(), 1, "models: {:?}", view.models);
        let row = &view.models[0];
        assert_eq!(row.model, "gpt-5.5");
        // The token mix carries through from the API-lane usage (10/20/30 in usage_event).
        assert_eq!(row.totals.tokens.input, 10);
        assert_eq!(row.totals.tokens.output, 20);
        assert_eq!(row.totals.tokens.cache_read, 30);
        // gpt-5.5 is a bundled-benchmark model, so the overlay is matched (a real standing,
        // never a guessed gap).
        let overlay = match &row.overlay {
            Some(overlay) => overlay,
            None => panic!("gpt-5.5 should match a bench overlay"),
        };
        assert_eq!(overlay.model_id, "gpt-5.5");
    }

    #[test]
    fn models_view_empty_snapshot_has_no_api_usage() {
        let view = match models_view(&EngineSnapshot::empty(timestamp())) {
            Ok(view) => view,
            Err(err) => panic!("models view should build: {err}"),
        };
        assert!(view.no_api_usage);
        assert!(view.models.is_empty());
    }

    // ----- budget_view (T14) -----

    /// One FOCUS meter row for the budget tests: a known tool, access path, charge timestamp,
    /// and (overwritten) billed cost. Mid-month timestamps keep the local-anchored month range
    /// off any boundary regardless of the test host's timezone.
    fn budget_record(
        tool: &str,
        access_path: FocusAccessPath,
        when: DateTime<Utc>,
        cents: i64,
    ) -> FocusRecord {
        let mut record = match FocusRecord::unpriced_usage(UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: when,
            tool: tool.to_string(),
            model: "gpt-5.5".to_string(),
            token_type: TokenType::Output,
            token_count: 1_000,
            project: None,
            access_path,
            service_name: "svc".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        }) {
            Ok(record) => record,
            Err(err) => panic!("budget record should build: {err}"),
        };
        let cost = Decimal::new(cents, 2);
        record.billed_cost = cost;
        record.effective_cost = cost;
        record
    }

    fn budget_targets(total: Option<i64>, per_tool: &[(&str, i64)]) -> BudgetTargets {
        BudgetTargets {
            total_monthly_usd: total.map(|cents| Decimal::new(cents, 2)),
            per_tool: per_tool
                .iter()
                .map(|(tool, cents)| (tool.to_string(), Decimal::new(*cents, 2)))
                .collect(),
        }
    }

    #[test]
    fn budget_view_empty_targets_is_no_budget_set() {
        let snapshot = snapshot_with_rows(timestamp(), Vec::new(), Vec::new());
        let view = budget_view(&snapshot, &BudgetTargets::default());
        assert!(view.no_budget_set);
        assert!(view.rows.is_empty());
        assert!(view.excluded_tools.is_empty());
    }

    #[test]
    fn budget_view_compares_current_month_api_lane_spend() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![
            budget_record(
                "codex",
                FocusAccessPath::Api,
                utc_datetime(2026, 6, 10, 12, 0, 0),
                1_500,
            ),
            // A LAST-month API row must NOT count toward the monthly budget.
            budget_record(
                "codex",
                FocusAccessPath::Api,
                utc_datetime(2026, 5, 10, 12, 0, 0),
                9_900,
            ),
        ];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("codex", 5_000)]));

        assert!(!view.no_budget_set);
        assert_eq!(view.rows.len(), 1);
        let row = &view.rows[0];
        assert_eq!(row.scope, BudgetScope::Tool("codex".to_string()));
        // Only the in-month $15.00 counts, not last month's $99.00.
        assert_eq!(row.spent_usd, Decimal::new(1_500, 2));
        assert_eq!(row.target_usd, Decimal::new(5_000, 2));
        assert!(row.over_by_usd.is_none());
        assert_eq!(view.spent_total_usd, Decimal::new(1_500, 2));
    }

    #[test]
    fn budget_view_total_sums_api_only_never_subscription() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let when = utc_datetime(2026, 6, 10, 12, 0, 0);
        let rows = vec![
            budget_record("codex", FocusAccessPath::Api, when, 2_000),
            budget_record("claude-code", FocusAccessPath::Api, when, 1_000),
            // A subscription row is a flat-fee plan, NOT a summable dollar — excluded.
            budget_record("claude-code", FocusAccessPath::Subscription, when, 9_900),
        ];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(Some(10_000), &[]));

        assert_eq!(view.rows.len(), 1);
        assert_eq!(view.rows[0].scope, BudgetScope::Total);
        // $20.00 + $10.00 API only; the $99.00 subscription row never contributes.
        assert_eq!(view.rows[0].spent_usd, Decimal::new(3_000, 2));
        assert_eq!(view.spent_total_usd, Decimal::new(3_000, 2));
    }

    #[test]
    fn budget_view_flat_fee_subscription_tool_is_guarded_not_compared() {
        // A tool billed ONLY via a flat-fee subscription (no API rows, ever) must not get a $
        // comparison (§170) — it is surfaced in excluded_tools, never as a $0/target row.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![budget_record(
            "cursor",
            FocusAccessPath::Subscription,
            utc_datetime(2026, 6, 10, 12, 0, 0),
            0,
        )];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("cursor", 5_000)]));

        assert!(
            view.rows.is_empty(),
            "a flat-fee tool gets no $ row: {:?}",
            view.rows
        );
        assert_eq!(
            view.excluded_tools,
            vec![BudgetExcludedTool {
                tool: "cursor".to_string(),
                reason: BudgetExclusion::FlatFeeSubscription,
            }]
        );
        assert!(!view.no_budget_set);
    }

    #[test]
    fn budget_view_unknown_access_only_tool_is_excluded_as_not_api_billed() {
        // A tool whose local rows are all UnknownAccess (no API, no subscription — e.g. a Codex
        // install with no rate-limit signal) is NOT API-billed, so it gets the honest
        // "not API-billed" exclusion, never a fabricated $0/target row (and never a fabricated
        // "subscription" claim).
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![budget_record(
            "codex",
            FocusAccessPath::Unknown,
            utc_datetime(2026, 6, 10, 12, 0, 0),
            0,
        )];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("codex", 4_000)]));

        assert!(
            view.rows.is_empty(),
            "an unknown-access tool gets no $ row: {:?}",
            view.rows
        );
        assert_eq!(
            view.excluded_tools,
            vec![BudgetExcludedTool {
                tool: "codex".to_string(),
                reason: BudgetExclusion::NotApiBilled,
            }]
        );
    }

    #[test]
    fn budget_view_tool_with_no_usage_at_all_is_a_legitimate_zero_row() {
        // A budgeted tool with NO local usage of any kind is NOT excluded — it stays a real
        // $0/target row (the user may be planning ahead before any API spend).
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let snapshot = snapshot_with_rows(now, Vec::new(), Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("codex", 4_000)]));

        assert!(view.excluded_tools.is_empty());
        assert_eq!(view.rows.len(), 1);
        assert_eq!(view.rows[0].spent_usd, Decimal::ZERO);
    }

    #[test]
    fn budget_view_tool_with_api_usage_is_compared_even_if_also_subscription() {
        // A tool used BOTH via API and subscription IS budgetable — the API lane is compared,
        // the subscription lane ignored (lanes never summed). Not flat-fee.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let when = utc_datetime(2026, 6, 10, 12, 0, 0);
        let rows = vec![
            budget_record("claude-code", FocusAccessPath::Api, when, 1_200),
            budget_record("claude-code", FocusAccessPath::Subscription, when, 9_900),
        ];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("claude-code", 5_000)]));

        assert!(view.excluded_tools.is_empty());
        assert_eq!(view.rows.len(), 1);
        assert_eq!(view.rows[0].spent_usd, Decimal::new(1_200, 2));
    }

    #[test]
    fn budget_view_over_budget_sets_over_by_and_pace() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![budget_record(
            "codex",
            FocusAccessPath::Api,
            utc_datetime(2026, 6, 10, 12, 0, 0),
            6_000,
        )];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(&snapshot, &budget_targets(None, &[("codex", 5_000)]));

        let row = &view.rows[0];
        assert_eq!(row.over_by_usd, Some(Decimal::new(1_000, 2)));
        assert!(row.fraction > 1.0);
        assert_eq!(row.pace, BudgetPace::OverBudget);
    }

    #[test]
    fn budget_view_skips_nonpositive_targets() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let snapshot = snapshot_with_rows(now, Vec::new(), Vec::new());
        // A 0 (or negative) cap is meaningless — skipped, never a divide-by-zero.
        let view = budget_view(&snapshot, &budget_targets(Some(0), &[("codex", -100)]));
        assert!(view.rows.is_empty());
        assert!(view.excluded_tools.is_empty());
        // The user DID set targets, so it is not the "no budget set" empty state.
        assert!(!view.no_budget_set);
    }

    #[test]
    fn budget_view_orders_most_utilized_first() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let when = utc_datetime(2026, 6, 10, 12, 0, 0);
        let rows = vec![
            budget_record("claude-code", FocusAccessPath::Api, when, 1_000),
            budget_record("codex", FocusAccessPath::Api, when, 4_800),
        ];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = budget_view(
            &snapshot,
            &budget_targets(None, &[("codex", 5_000), ("claude-code", 5_000)]),
        );
        // codex (96%) outranks claude-code (20%).
        assert_eq!(view.rows[0].scope, BudgetScope::Tool("codex".to_string()));
        assert_eq!(
            view.rows[1].scope,
            BudgetScope::Tool("claude-code".to_string())
        );
    }

    // ----- active_alerts (T17) -----

    /// A finalized quota [`LimitSummary`] with an explicit availability — the detector reads the
    /// finalized summary (not a raw window), so the tests build it directly.
    fn alert_limit(
        tool: ProviderId,
        kind: LimitKind,
        availability: LimitAvailability,
    ) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: utc_datetime(2026, 6, 16, 12, 0, 0),
            availability,
        }
    }

    fn available_fraction(fraction: f64) -> LimitAvailability {
        LimitAvailability::Available {
            measure: LimitMeasure::TokenFraction(fraction),
            resets_at: utc_datetime(2026, 6, 16, 17, 0, 0),
            reset_in_seconds: 3_600,
        }
    }

    fn now_with_limits(limits: Vec<LimitSummary>) -> NowSummary {
        let at = utc_datetime(2026, 6, 16, 12, 0, 0);
        NowSummary {
            generated_at: at,
            cost_period: period_range_for(Period::Week, at),
            group_by: GroupBy::Model,
            limits,
            current_costs: Vec::new(),
            providers: Vec::new(),
        }
    }

    /// A [`BudgetRow`] mirroring [`budget_row`]'s over/fraction derivation (target > 0).
    fn alert_budget_row(scope: BudgetScope, target_cents: i64, spent_cents: i64) -> BudgetRow {
        let target = Decimal::new(target_cents, 2);
        let spent = Decimal::new(spent_cents, 2);
        BudgetRow {
            scope,
            target_usd: target,
            spent_usd: spent,
            fraction: (spent / target).to_f64().unwrap_or(0.0),
            over_by_usd: (spent > target).then_some(spent - target),
            pace: if spent > target {
                BudgetPace::OverBudget
            } else {
                BudgetPace::OnTrack
            },
        }
    }

    fn budget_with_rows(rows: Vec<BudgetRow>) -> BudgetView {
        BudgetView {
            generated_at: utc_datetime(2026, 6, 16, 12, 0, 0),
            rows,
            excluded_tools: Vec::new(),
            no_budget_set: false,
            spent_total_usd: Decimal::ZERO,
            month_elapsed_fraction: 0.5,
        }
    }

    fn no_budget() -> BudgetView {
        budget_with_rows(Vec::new())
    }

    #[test]
    fn alert_thresholds_default_matches_canonical_near_limit_fractions() {
        // The detector's defaults ARE the render meter's WARN/CRITICAL (never a forked third set).
        let defaults = AlertThresholds::default();
        assert_eq!(defaults.quota_warn_fraction, ALERT_WARN_FRACTION);
        assert_eq!(defaults.quota_critical_fraction, ALERT_CRITICAL_FRACTION);
        assert_eq!(ALERT_WARN_FRACTION, 0.80);
        assert_eq!(ALERT_CRITICAL_FRACTION, 0.95);
    }

    #[test]
    fn active_alerts_fires_quota_warn_and_critical_off_available_only() {
        let now = now_with_limits(vec![
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::Weekly,
                available_fraction(0.82),
            ),
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::FiveHour,
                available_fraction(0.97),
            ),
            alert_limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                available_fraction(0.5),
            ),
        ]);
        let alerts = active_alerts(
            &now,
            &no_budget(),
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        assert_eq!(alerts.len(), 2, "the 50% window must not fire: {alerts:?}");
        // Critical-tier first.
        match &alerts[0] {
            Alert::Quota {
                kind,
                level,
                fraction,
                ..
            } => {
                assert_eq!(*kind, LimitKind::FiveHour);
                assert_eq!(*level, AlertLevel::Critical);
                assert!((fraction - 0.97).abs() < 1e-9);
            }
            other => panic!("expected a critical quota alert first, got {other:?}"),
        }
        match &alerts[1] {
            Alert::Quota { kind, level, .. } => {
                assert_eq!(*kind, LimitKind::Weekly);
                assert_eq!(*level, AlertLevel::Warn);
            }
            other => panic!("expected a warn quota alert second, got {other:?}"),
        }
    }

    #[test]
    fn active_alerts_never_fires_off_unverified_estimated_partial_or_unavailable() {
        // Each reading is at/above CRITICAL, yet NONE is a fresh cross-checked `Available` — so the
        // detector stays silent (the T15 discipline / ARCHITECTURE).
        let now = now_with_limits(vec![
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::Weekly,
                LimitAvailability::Unverified {
                    measure: LimitMeasure::TokenFraction(0.99),
                    resets_at: None,
                    reset_in_seconds: None,
                },
            ),
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::FiveHour,
                LimitAvailability::Estimated {
                    volume_tokens: 9_000_000,
                    estimated_usd: Some(Decimal::new(1_000, 2)),
                },
            ),
            alert_limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                LimitAvailability::Partial {
                    measure: Some(LimitMeasure::TokenFraction(0.99)),
                    resets_at: None,
                    reset_in_seconds: None,
                    reason: "no reset".to_string(),
                },
            ),
            alert_limit(
                ProviderId::Codex,
                LimitKind::Weekly,
                LimitAvailability::Unavailable {
                    reason: "no data".to_string(),
                },
            ),
        ]);
        let alerts = active_alerts(
            &now,
            &no_budget(),
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        assert!(
            alerts.is_empty(),
            "no alert may fire off a non-fresh reading: {alerts:?}"
        );
    }

    #[test]
    fn active_alerts_fires_budget_over_only_when_strictly_over_target() {
        let budget = budget_with_rows(vec![
            // Over by $10.00.
            alert_budget_row(BudgetScope::Tool("codex".to_string()), 5_000, 6_000),
            // Exactly at budget — NOT over (strict `spent > target`), so no alert.
            alert_budget_row(BudgetScope::Total, 10_000, 10_000),
            // Comfortably under.
            alert_budget_row(BudgetScope::Tool("claude-code".to_string()), 6_000, 1_500),
        ]);
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        assert_eq!(
            alerts.len(),
            1,
            "only the strictly-over row fires: {alerts:?}"
        );
        match &alerts[0] {
            Alert::Budget {
                scope,
                over_by_usd,
                spent_usd,
                target_usd,
            } => {
                assert_eq!(*scope, BudgetScope::Tool("codex".to_string()));
                assert_eq!(*over_by_usd, Decimal::new(1_000, 2));
                assert_eq!(*spent_usd, Decimal::new(6_000, 2));
                assert_eq!(*target_usd, Decimal::new(5_000, 2));
            }
            other => panic!("expected a budget alert, got {other:?}"),
        }
    }

    #[test]
    fn active_alerts_spend_measure_uses_used_over_included_and_skips_no_allowance() {
        let now = now_with_limits(vec![
            // 9.60 / 10.00 = 0.96 → critical.
            alert_limit(
                ProviderId::Cursor,
                LimitKind::Monthly,
                LimitAvailability::Available {
                    measure: LimitMeasure::Spend {
                        used_usd: Decimal::new(960, 2),
                        included_usd: Some(Decimal::new(1_000, 2)),
                    },
                    resets_at: utc_datetime(2026, 6, 30, 0, 0, 0),
                    reset_in_seconds: 1_209_600,
                },
            ),
            // No allowance (pure overage) → no fraction → never alerts, even at large usage.
            alert_limit(
                ProviderId::Cursor,
                LimitKind::BillingCycle,
                LimitAvailability::Available {
                    measure: LimitMeasure::Spend {
                        used_usd: Decimal::new(9_999, 2),
                        included_usd: None,
                    },
                    resets_at: utc_datetime(2026, 6, 30, 0, 0, 0),
                    reset_in_seconds: 1_209_600,
                },
            ),
        ]);
        let alerts = active_alerts(
            &now,
            &no_budget(),
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        assert_eq!(
            alerts.len(),
            1,
            "only the with-allowance pool fires: {alerts:?}"
        );
        match &alerts[0] {
            Alert::Quota {
                kind,
                level,
                fraction,
                ..
            } => {
                assert_eq!(*kind, LimitKind::Monthly);
                assert_eq!(*level, AlertLevel::Critical);
                assert!((fraction - 0.96).abs() < 1e-9);
            }
            other => panic!("expected a critical spend-pool quota alert, got {other:?}"),
        }
    }

    #[test]
    fn active_alerts_orders_critical_tier_first_across_classes() {
        // A WARN quota + an over-budget: the budget (critical-tier) sorts ahead of the warn quota.
        let now = now_with_limits(vec![alert_limit(
            ProviderId::ClaudeCode,
            LimitKind::Weekly,
            available_fraction(0.82),
        )]);
        let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 11_000)]);
        let alerts = active_alerts(
            &now,
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        assert_eq!(alerts.len(), 2);
        assert!(matches!(alerts[0], Alert::Budget { .. }), "{alerts:?}");
        assert!(
            matches!(
                alerts[1],
                Alert::Quota {
                    level: AlertLevel::Warn,
                    ..
                }
            ),
            "{alerts:?}"
        );
        // is_critical agrees with the ordering.
        assert!(alerts[0].is_critical());
        assert!(!alerts[1].is_critical());
    }

    #[test]
    fn active_alerts_honors_custom_thresholds() {
        let thresholds = AlertThresholds {
            quota_warn_fraction: 0.50,
            quota_critical_fraction: 0.90,
        };
        let now = now_with_limits(vec![
            alert_limit(
                ProviderId::Codex,
                LimitKind::Weekly,
                available_fraction(0.60),
            ),
            alert_limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                available_fraction(0.95),
            ),
        ]);
        let alerts = active_alerts(&now, &no_budget(), &thresholds, AdvisoryAlerts::default());
        assert_eq!(alerts.len(), 2);
        // 0.95 ≥ 0.90 → critical (sorts first); 0.60 ≥ 0.50 → warn.
        assert!(matches!(
            alerts[0],
            Alert::Quota {
                level: AlertLevel::Critical,
                ..
            }
        ));
        assert!(matches!(
            alerts[1],
            Alert::Quota {
                level: AlertLevel::Warn,
                ..
            }
        ));
    }

    // ----- active_alerts advisory sources (T17b) -----

    /// A minimal [`ForecastView`] carrying just the `spend` arm the advisory detector reads.
    fn forecast_with_spend(spend: SpendForecast) -> ForecastView {
        ForecastView {
            generated_at: utc_datetime(2026, 6, 16, 12, 0, 0),
            no_api_usage: false,
            spend,
            daily_actuals: Vec::new(),
            quota_etas: Vec::new(),
        }
    }

    /// A `Projected` month-end spend of `projected_cents` (the rest of the projection is unread by
    /// the detector, so the basis figures are illustrative).
    fn projected(projected_cents: i64) -> SpendForecast {
        SpendForecast::Projected {
            projected_month_usd: Decimal::new(projected_cents, 2),
            spend_to_date_usd: Decimal::new(projected_cents / 2, 2),
            days_elapsed: 16,
            days_in_month: 30,
        }
    }

    /// An [`AnomaliesView`] carrying exactly the supplied anomalies (history gates already passed
    /// upstream — the detector trusts the view and never re-derives them).
    fn anomalies_with(anomalies: Vec<Anomaly>) -> AnomaliesView {
        AnomaliesView {
            generated_at: utc_datetime(2026, 6, 16, 12, 0, 0),
            history_days: 12,
            min_history_days: 7,
            baseline_days: 14,
            enough_history: true,
            no_usage: false,
            anomalies,
        }
    }

    fn spend_spike_anomaly(date: NaiveDate, value_cents: i64, median_cents: i64) -> Anomaly {
        let value = Decimal::new(value_cents, 2);
        let median = Decimal::new(median_cents, 2);
        Anomaly {
            signal: AnomalySignal::SpendSpike { date },
            value,
            baseline_median: median,
            deviation: value - median,
            magnitude: (median > Decimal::ZERO).then(|| value / median),
            baseline_days: 12,
        }
    }

    #[test]
    fn active_alerts_advisory_off_is_byte_identical_to_t17() {
        // Inputs where BOTH advisory sources WOULD fire if their sub-flag were on: a quota WARN, an
        // over-budget per-tool row, a not-over Total the projection exceeds, and a spend spike. With
        // the advisory sources OFF (the Default), the output must be exactly the T17 quota+budget
        // set — proving the sub-flag IS the opt-in (no advisory variant leaks in).
        let now = now_with_limits(vec![alert_limit(
            ProviderId::ClaudeCode,
            LimitKind::Weekly,
            available_fraction(0.82),
        )]);
        // A per-tool budget OVER (the hard Budget alert) + a Total NOT over (so the forecast source
        // has an under-target total to project past — an already-over total would suppress it).
        let budget = budget_with_rows(vec![
            alert_budget_row(BudgetScope::Tool("codex".to_string()), 5_000, 6_000),
            alert_budget_row(BudgetScope::Total, 10_000, 4_000),
        ]);
        let forecast = forecast_with_spend(projected(50_000));
        let anomalies = anomalies_with(vec![spend_spike_anomaly(
            naive_date(2026, 6, 16),
            2_000,
            250,
        )]);

        let baseline = active_alerts(
            &now,
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts::default(),
        );
        let with_views = active_alerts(
            &now,
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: Some(&anomalies),
            },
        );

        // Default-off is exactly the two hard classes (over-budget critical first, then WARN quota).
        assert_eq!(baseline.len(), 2, "{baseline:?}");
        assert!(matches!(baseline[0], Alert::Budget { .. }));
        assert!(matches!(
            baseline[1],
            Alert::Quota {
                level: AlertLevel::Warn,
                ..
            }
        ));
        assert!(
            !baseline
                .iter()
                .any(|a| matches!(a, Alert::Forecast { .. } | Alert::SpendSpike { .. })),
            "advisory sources must NOT fire when off: {baseline:?}"
        );
        // Turning the sub-flags on is strictly additive: the first two stay identical, two more append.
        assert_eq!(with_views[..2], baseline[..]);
        assert_eq!(with_views.len(), 4, "{with_views:?}");
    }

    #[test]
    fn active_alerts_forecast_fires_when_projected_over_an_under_total() {
        // Projected $500 over a $100 total that is NOT yet over → one advisory Forecast alert.
        let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 4_000)]);
        let forecast = forecast_with_spend(projected(50_000));
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: None,
            },
        );
        assert_eq!(alerts.len(), 1, "{alerts:?}");
        match &alerts[0] {
            Alert::Forecast {
                projected_month_usd,
                target_usd,
                projected_over_by_usd,
            } => {
                assert_eq!(*projected_month_usd, Decimal::new(50_000, 2));
                assert_eq!(*target_usd, Decimal::new(10_000, 2));
                assert_eq!(*projected_over_by_usd, Decimal::new(40_000, 2));
            }
            other => panic!("expected a Forecast alert, got {other:?}"),
        }
        // Advisory-tier: never critical.
        assert!(!alerts[0].is_critical());
    }

    #[test]
    fn active_alerts_forecast_suppressed_when_insufficient_data() {
        // The noisy early-month state must never project an alarm.
        let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 0)]);
        let forecast = forecast_with_spend(SpendForecast::InsufficientData {
            spend_to_date_usd: Decimal::new(9_000, 2),
            days_elapsed: 2,
            days_in_month: 30,
            min_days: 3,
        });
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: None,
            },
        );
        assert!(
            alerts.is_empty(),
            "InsufficientData must not fire a forecast alert: {alerts:?}"
        );
    }

    #[test]
    fn active_alerts_forecast_suppressed_when_total_already_over() {
        // Already over → the hard Budget alert owns it; no double-alert from the forecast source.
        let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 12_000)]);
        let forecast = forecast_with_spend(projected(50_000));
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: None,
            },
        );
        assert_eq!(alerts.len(), 1, "exactly the hard budget alert: {alerts:?}");
        assert!(
            matches!(alerts[0], Alert::Budget { .. }),
            "an already-over total is the hard Budget alert, never a forecast: {alerts:?}"
        );
    }

    #[test]
    fn active_alerts_forecast_suppressed_at_or_under_target() {
        // A projection exactly at, or under, target is not "expected to exceed" → no alert.
        for projected_cents in [10_000, 9_999] {
            let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 0)]);
            let forecast = forecast_with_spend(projected(projected_cents));
            let alerts = active_alerts(
                &now_with_limits(Vec::new()),
                &budget,
                &AlertThresholds::default(),
                AdvisoryAlerts {
                    forecast: Some(&forecast),
                    anomalies: None,
                },
            );
            assert!(
                alerts.is_empty(),
                "projected {projected_cents}c vs $100 target must not fire: {alerts:?}"
            );
        }
    }

    #[test]
    fn active_alerts_forecast_requires_a_total_budget_row() {
        // Only a per-tool budget (no Total row) → the forecast source has nothing to compare against.
        let budget = budget_with_rows(vec![alert_budget_row(
            BudgetScope::Tool("codex".to_string()),
            10_000,
            1_000,
        )]);
        let forecast = forecast_with_spend(projected(50_000));
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: None,
            },
        );
        assert!(
            alerts.is_empty(),
            "no Total budget row → no forecast alert (per-tool forecast is out of scope): {alerts:?}"
        );
    }

    #[test]
    fn active_alerts_spend_spike_fires_when_anomalies_on() {
        let anomalies = anomalies_with(vec![spend_spike_anomaly(
            naive_date(2026, 6, 16),
            2_000,
            250,
        )]);
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &no_budget(),
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: None,
                anomalies: Some(&anomalies),
            },
        );
        assert_eq!(alerts.len(), 1, "{alerts:?}");
        match &alerts[0] {
            Alert::SpendSpike {
                date,
                value_usd,
                baseline_median_usd,
                magnitude,
            } => {
                assert_eq!(*date, naive_date(2026, 6, 16));
                assert_eq!(*value_usd, Decimal::new(2_000, 2));
                assert_eq!(*baseline_median_usd, Decimal::new(250, 2));
                assert_eq!(
                    *magnitude,
                    Some(Decimal::new(2_000, 2) / Decimal::new(250, 2))
                );
            }
            other => panic!("expected a SpendSpike alert, got {other:?}"),
        }
        assert!(!alerts[0].is_critical());
    }

    #[test]
    fn active_alerts_spend_spike_ignores_model_mix_shift() {
        // Only SpendSpike anomalies alert — a model-mix shift is informational, never a crossing.
        let anomalies = anomalies_with(vec![Anomaly {
            signal: AnomalySignal::ModelMixShift {
                model: "claude-opus-4".to_string(),
            },
            value: Decimal::new(60, 2),
            baseline_median: Decimal::new(20, 2),
            deviation: Decimal::new(40, 2),
            magnitude: Some(Decimal::new(3, 0)),
            baseline_days: 12,
        }]);
        let alerts = active_alerts(
            &now_with_limits(Vec::new()),
            &no_budget(),
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: None,
                anomalies: Some(&anomalies),
            },
        );
        assert!(
            alerts.is_empty(),
            "a model-mix shift must NOT alert: {alerts:?}"
        );
    }

    #[test]
    fn active_alerts_advisory_sources_sort_after_quota_and_budget() {
        // A full mix: critical quota + over-budget (critical tier) then WARN quota, forecast,
        // spend spike (advisory tier). Order: criticals first, then WARN, then forecast, then spike.
        let now = now_with_limits(vec![
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::FiveHour,
                available_fraction(0.97),
            ),
            alert_limit(
                ProviderId::ClaudeCode,
                LimitKind::Weekly,
                available_fraction(0.82),
            ),
        ]);
        let budget = budget_with_rows(vec![alert_budget_row(BudgetScope::Total, 10_000, 4_000)]);
        let forecast = forecast_with_spend(projected(50_000));
        let anomalies = anomalies_with(vec![spend_spike_anomaly(
            naive_date(2026, 6, 16),
            2_000,
            250,
        )]);
        let alerts = active_alerts(
            &now,
            &budget,
            &AlertThresholds::default(),
            AdvisoryAlerts {
                forecast: Some(&forecast),
                anomalies: Some(&anomalies),
            },
        );
        assert_eq!(alerts.len(), 4, "{alerts:?}");
        // [0] CRITICAL quota, [1] WARN quota, [2] Forecast, [3] SpendSpike — note: with no
        // over-budget here, the only critical-tier item is the CRITICAL quota.
        assert!(matches!(
            alerts[0],
            Alert::Quota {
                level: AlertLevel::Critical,
                ..
            }
        ));
        assert!(matches!(
            alerts[1],
            Alert::Quota {
                level: AlertLevel::Warn,
                ..
            }
        ));
        assert!(matches!(alerts[2], Alert::Forecast { .. }), "{alerts:?}");
        assert!(matches!(alerts[3], Alert::SpendSpike { .. }), "{alerts:?}");
        assert!(alerts[0].is_critical());
        assert!(!alerts[1].is_critical() && !alerts[2].is_critical() && !alerts[3].is_critical());
    }

    // ----- forecast_view (T15) -----

    /// A Verified token-fraction quota window for the forecast quota-ETA tests.
    fn token_window(
        tool: ProviderId,
        kind: LimitKind,
        fraction: f64,
        resets_at: Option<DateTime<Utc>>,
        captured_at: DateTime<Utc>,
        status: LimitStatus,
    ) -> LimitWindow {
        LimitWindow {
            tool,
            plan: None,
            kind,
            measure: Some(LimitMeasure::TokenFraction(fraction)),
            resets_at,
            captured_at,
            status,
            label: None,
        }
    }

    #[test]
    fn forecast_view_projects_month_run_rate_on_the_utc_calendar() {
        // June 16 (UTC) → 16 of 30 days elapsed. spend-to-date is the in-month API-lane sum, and
        // the projection scales it to the full month: 20 × 30 / 16 = $37.50. The numerator and
        // denominator are BOTH UTC (the consistency rule) — last-month and future rows excluded.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![
            budget_record(
                "codex",
                FocusAccessPath::Api,
                utc_datetime(2026, 6, 5, 9, 0, 0),
                500,
            ),
            budget_record(
                "claude-code",
                FocusAccessPath::Api,
                utc_datetime(2026, 6, 10, 9, 0, 0),
                1_500,
            ),
            // Last month — different UTC month, excluded.
            budget_record(
                "codex",
                FocusAccessPath::Api,
                utc_datetime(2026, 5, 30, 9, 0, 0),
                9_900,
            ),
            // After today — a future-dated row is excluded from spend-to-date.
            budget_record(
                "codex",
                FocusAccessPath::Api,
                utc_datetime(2026, 6, 20, 9, 0, 0),
                5_000,
            ),
            // Subscription lane never contributes a dollar.
            budget_record(
                "claude-code",
                FocusAccessPath::Subscription,
                utc_datetime(2026, 6, 8, 9, 0, 0),
                2_000,
            ),
        ];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = forecast_view(&snapshot);

        assert!(!view.no_api_usage);
        match view.spend {
            SpendForecast::Projected {
                projected_month_usd,
                spend_to_date_usd,
                days_elapsed,
                days_in_month,
            } => {
                assert_eq!(spend_to_date_usd, Decimal::new(2_000, 2)); // $20.00
                assert_eq!(days_elapsed, 16);
                assert_eq!(days_in_month, 30);
                assert_eq!(projected_month_usd, Decimal::new(3_750, 2)); // $37.50
            }
            other => panic!("expected a projection, got {other:?}"),
        }
        // The per-day actuals are this month's spend days, ascending (the future row excluded).
        assert_eq!(view.daily_actuals.len(), 2);
        assert_eq!(view.daily_actuals[0].date.day(), 5);
        assert_eq!(view.daily_actuals[1].date.day(), 10);
        assert_eq!(view.daily_actuals[0].spent_usd, Decimal::new(500, 2));
        assert_eq!(view.daily_actuals[1].spent_usd, Decimal::new(1_500, 2));
    }

    #[test]
    fn forecast_view_below_three_day_floor_is_insufficient_data() {
        // Only 2 of the month's days have elapsed (June 2 UTC) — below the 3-day floor, so the $
        // projection is suppressed in favor of the honest insufficient-data state.
        let now = utc_datetime(2026, 6, 2, 12, 0, 0);
        let rows = vec![budget_record(
            "codex",
            FocusAccessPath::Api,
            utc_datetime(2026, 6, 1, 9, 0, 0),
            500,
        )];
        let snapshot = snapshot_with_rows(now, rows, Vec::new());
        let view = forecast_view(&snapshot);

        match view.spend {
            SpendForecast::InsufficientData {
                spend_to_date_usd,
                days_elapsed,
                days_in_month,
                min_days,
            } => {
                assert_eq!(spend_to_date_usd, Decimal::new(500, 2));
                assert_eq!(days_elapsed, 2);
                assert_eq!(days_in_month, 30);
                assert_eq!(min_days, 3);
            }
            other => panic!("expected insufficient data, got {other:?}"),
        }
    }

    #[test]
    fn forecast_view_no_api_usage_is_flagged() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let view = forecast_view(&snapshot_with_rows(now, Vec::new(), Vec::new()));
        assert!(view.no_api_usage);
        assert!(view.daily_actuals.is_empty());
    }

    #[test]
    fn forecast_daily_fractions_normalizes_to_the_series_max() {
        let day = |d: u32, cents: i64| ForecastDay {
            date: match NaiveDate::from_ymd_opt(2026, 6, d) {
                Some(date) => date,
                None => panic!("valid date"),
            },
            spent_usd: Decimal::new(cents, 2),
        };
        // Max is the $4.00 day → it normalizes to 1.0; the others scale linearly.
        let days = vec![day(1, 100), day(2, 400), day(3, 200), day(4, 0)];
        let fractions = forecast_daily_fractions(&days);
        assert_eq!(fractions.len(), 4);
        assert!((fractions[0] - 0.25).abs() < 1e-9, "{:?}", fractions);
        assert!((fractions[1] - 1.0).abs() < 1e-9);
        assert!((fractions[2] - 0.5).abs() < 1e-9);
        assert_eq!(fractions[3], 0.0);
        // Every fraction is bounded to [0, 1].
        assert!(fractions.iter().all(|f| (0.0..=1.0).contains(f)));
    }

    #[test]
    fn forecast_daily_fractions_handles_empty_and_all_zero() {
        // Empty → empty; an all-zero series → all-zero fractions (an honest flat baseline, never a 0/0).
        assert!(forecast_daily_fractions(&[]).is_empty());
        let zeros = vec![
            ForecastDay {
                date: match NaiveDate::from_ymd_opt(2026, 6, 1) {
                    Some(date) => date,
                    None => panic!("valid date"),
                },
                spent_usd: Decimal::ZERO,
            };
            3
        ];
        assert_eq!(forecast_daily_fractions(&zeros), vec![0.0, 0.0, 0.0]);
    }

    #[test]
    fn format_over_by_usd_guards_the_sub_cent_case() {
        // A real overshoot below a cent renders "<$0.01", never the contradictory "~$0.00".
        assert_eq!(format_over_by_usd(&Decimal::new(1, 3)), "<$0.01"); // $0.001
        assert_eq!(format_over_by_usd(&Decimal::ZERO), "<$0.01");
        // A genuine overshoot renders the `~`-hedged estimate.
        assert_eq!(format_over_by_usd(&Decimal::new(1050, 2)), "~$10.50");
    }

    #[test]
    fn decimal_share_percent_rounds_like_the_cli() {
        assert_eq!(decimal_share_percent(&Decimal::ZERO), "0%");
        assert_eq!(decimal_share_percent(&Decimal::new(925, 3)), "93%"); // 0.925 -> 93%
        assert_eq!(decimal_share_percent(&Decimal::ONE), "100%");
    }

    #[test]
    fn anomaly_multiple_phrase_suppresses_unhelpful_multiples() {
        // No magnitude (median was 0) -> None.
        assert_eq!(anomaly_multiple_phrase(None, false), None);
        // Displayed baseline is zero -> None (a multiple over "$0.00"/"0%" is self-contradictory).
        assert_eq!(
            anomaly_multiple_phrase(Some(&Decimal::new(30, 1)), true),
            None
        );
        // Rounds to <= 1.0x -> None (a flagged-but-tiny move).
        assert_eq!(
            anomaly_multiple_phrase(Some(&Decimal::new(104, 2)), false),
            None
        );
        // A genuine multiple is shown to 1 dp.
        assert_eq!(
            anomaly_multiple_phrase(Some(&Decimal::new(35, 1)), false),
            Some("3.5".to_string())
        );
    }

    #[test]
    fn forecast_view_quota_eta_projects_only_off_a_fresh_available_token_fraction() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let half_week = Duration::seconds(302_400); // 3.5 days left in a 7-day window
        let limits = vec![
            // Codex weekly, 90% with half the week left → projected to hit BEFORE the reset.
            token_window(
                ProviderId::Codex,
                LimitKind::Weekly,
                0.9,
                Some(now + half_week),
                now,
                LimitStatus::Verified,
            ),
            // Codex 5h, 10% with most of the window left → resets before you hit it.
            token_window(
                ProviderId::Codex,
                LimitKind::FiveHour,
                0.1,
                Some(now + Duration::minutes(240)),
                now,
                LimitStatus::Verified,
            ),
            // An Unverified reading is NOT projectable — degrade.
            token_window(
                ProviderId::Codex,
                LimitKind::Weekly,
                0.5,
                Some(now + half_week),
                now,
                LimitStatus::Unverified,
            ),
            // A stale reading (reset already passed) ages out upstream → not projectable.
            token_window(
                ProviderId::Codex,
                LimitKind::Weekly,
                0.5,
                Some(now - Duration::minutes(5)),
                now,
                LimitStatus::Verified,
            ),
        ];
        let snapshot = snapshot_with_rows(now, Vec::new(), limits);
        let etas = forecast_view(&snapshot).quota_etas;
        assert_eq!(etas.len(), 4);

        assert!(
            matches!(etas[0].outcome, QuotaEtaOutcome::ProjectedHit { fraction, .. } if fraction == 0.9),
            "weekly 90% should project a hit: {:?}",
            etas[0].outcome
        );
        assert!(
            matches!(etas[1].outcome, QuotaEtaOutcome::ResetsFirst { .. }),
            "5h 10% should reset first: {:?}",
            etas[1].outcome
        );
        assert!(
            matches!(
                etas[2].outcome,
                QuotaEtaOutcome::Unavailable {
                    reason: QuotaEtaUnavailable::ReadingNotProjectable
                }
            ),
            "an unverified reading is not projectable: {:?}",
            etas[2].outcome
        );
        assert!(
            matches!(
                etas[3].outcome,
                QuotaEtaOutcome::Unavailable {
                    reason: QuotaEtaUnavailable::ReadingNotProjectable
                }
            ),
            "a stale reading is aged out, not projectable: {:?}",
            etas[3].outcome
        );
    }

    #[test]
    fn forecast_view_quota_eta_just_started_window_is_unavailable() {
        // A window whose reset is a full window-length away has zero elapsed time — no burn rate.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let limits = vec![token_window(
            ProviderId::Codex,
            LimitKind::FiveHour,
            0.4,
            Some(now + Duration::hours(5)),
            now,
            LimitStatus::Verified,
        )];
        let snapshot = snapshot_with_rows(now, Vec::new(), limits);
        let etas = forecast_view(&snapshot).quota_etas;
        assert!(
            matches!(
                etas[0].outcome,
                QuotaEtaOutcome::Unavailable {
                    reason: QuotaEtaUnavailable::WindowJustStarted
                }
            ),
            "a just-started window has no burn rate: {:?}",
            etas[0].outcome
        );
    }

    // ----- anomalies_view (T16) -----

    /// One API-lane FOCUS meter row for the anomaly tests: a model, a charge time, a raw token
    /// count (→ `x_ConsumedTokens`), and an (overwritten) billed cost.
    fn anomaly_record(model: &str, when: DateTime<Utc>, tokens: u64, cents: i64) -> FocusRecord {
        anomaly_record_lane(model, when, tokens, cents, FocusAccessPath::Api)
    }

    /// Like [`anomaly_record`] but on a chosen lane — for the T16b all-lane model-mix tests (a
    /// subscription-only or mixed user). The $ (spend-spike) series counts only the API lane; the
    /// token (model-mix) series counts every lane, so a subscription row drives the mix but not the $.
    fn anomaly_record_lane(
        model: &str,
        when: DateTime<Utc>,
        tokens: u64,
        cents: i64,
        access_path: FocusAccessPath,
    ) -> FocusRecord {
        let mut record = match FocusRecord::unpriced_usage(UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: when,
            tool: "codex".to_string(),
            model: model.to_string(),
            token_type: TokenType::Output,
            token_count: tokens,
            project: None,
            access_path,
            service_name: "svc".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        }) {
            Ok(record) => record,
            Err(err) => panic!("anomaly record should build: {err}"),
        };
        let cost = Decimal::new(cents, 2);
        record.billed_cost = cost;
        record.effective_cost = cost;
        record
    }

    #[test]
    fn decimal_median_handles_empty_odd_and_even() {
        assert_eq!(decimal_median(&[]), None);
        assert_eq!(
            decimal_median(&[Decimal::new(3, 0), Decimal::new(1, 0), Decimal::new(2, 0)]),
            Some(Decimal::new(2, 0))
        );
        // Even count → mean of the two middle elements: median([1,2,3,4]) = 2.5.
        assert_eq!(
            decimal_median(&[
                Decimal::new(1, 0),
                Decimal::new(2, 0),
                Decimal::new(3, 0),
                Decimal::new(4, 0)
            ]),
            Some(Decimal::new(25, 1))
        );
    }

    #[test]
    fn decimal_mad_is_zero_for_a_flat_series_and_never_panics() {
        let flat = vec![Decimal::new(5, 0); 6];
        let median = match decimal_median(&flat) {
            Some(value) => value,
            None => panic!("non-empty series has a median"),
        };
        assert_eq!(decimal_mad(&flat, median), Some(Decimal::ZERO));
        assert_eq!(decimal_mad(&[], Decimal::ZERO), None);
    }

    #[test]
    fn anomalies_view_below_seven_days_history_is_suppressed() {
        // Only 3 distinct days of all-lane token history — below the 7-day floor, so NO anomaly is
        // surfaced and the honest insufficient-history state is reported instead.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let rows = vec![
            anomaly_record("gpt-5.5", utc_datetime(2026, 6, 14, 9, 0, 0), 1_000, 100),
            anomaly_record("gpt-5.5", utc_datetime(2026, 6, 15, 9, 0, 0), 1_000, 100),
            anomaly_record("gpt-5.5", utc_datetime(2026, 6, 16, 9, 0, 0), 1_000, 5_000),
        ];
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(!view.enough_history);
        assert_eq!(view.history_days, 3);
        assert_eq!(view.min_history_days, 7);
        assert!(view.anomalies.is_empty());
        // Thin history is NOT the zero-state — the user has 3 token-days, just below the floor.
        assert!(!view.no_usage);
    }

    #[test]
    fn anomalies_view_empty_snapshot_is_suppressed() {
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let view = anomalies_view(&snapshot_with_rows(now, Vec::new(), Vec::new()));
        assert!(!view.enough_history);
        assert_eq!(view.history_days, 0);
        assert!(view.anomalies.is_empty());
        // Zero rows ⇒ the TRANSIENT zero-state (no usage recorded yet) — it fills in as usage
        // accrues; distinct from thin history (`history_days` 1..6). (T16b test #5.)
        assert!(view.no_usage);
    }

    #[test]
    fn anomalies_view_flags_a_spend_spike_vs_the_users_own_norm() {
        // Seven flat ~$1 days then a $20 latest day (today). The robust median stays $1, so the
        // spike clears the conservative 3.5*MAD threshold and is flagged ~20x the norm.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = (9..=15)
            .map(|day| anomaly_record("gpt-5.5", utc_datetime(2026, 6, day, 9, 0, 0), 1_000, 100))
            .collect();
        rows.push(anomaly_record(
            "gpt-5.5",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            2_000,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(view.enough_history);
        let spike = view
            .anomalies
            .iter()
            .find(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. }))
            .unwrap_or_else(|| panic!("expected a spend spike: {:?}", view.anomalies));
        match spike.signal {
            AnomalySignal::SpendSpike { date } => {
                assert_eq!(
                    date,
                    match NaiveDate::from_ymd_opt(2026, 6, 16) {
                        Some(date) => date,
                        None => panic!("valid date"),
                    }
                );
            }
            ref other => panic!("expected a spend spike, got {other:?}"),
        }
        assert_eq!(spike.value, Decimal::new(2_000, 2)); // $20.00 latest
        assert_eq!(spike.baseline_median, Decimal::ONE); // $1.00 median
        assert_eq!(spike.magnitude, Some(Decimal::from(20))); // ~20x the norm
    }

    #[test]
    fn anomalies_view_flat_history_does_not_flag_a_trivial_change_but_does_flag_a_real_jump() {
        // MAD=0 guard: a near-flat history (MAD 0) must NOT flag a sub-floor change, but MUST
        // still flag a jump that clears the absolute floor.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let flat: Vec<FocusRecord> = (9..=15)
            .map(|day| anomaly_record("gpt-5.5", utc_datetime(2026, 6, day, 9, 0, 0), 1_000, 500))
            .collect();

        // Latest day $5.50 — a $0.50 deviation, below the $1 floor → NOT flagged.
        let mut trivial = flat.clone();
        trivial.push(anomaly_record(
            "gpt-5.5",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            550,
        ));
        let trivial_view = anomalies_view(&snapshot_with_rows(now, trivial, Vec::new()));
        assert!(trivial_view.enough_history);
        assert!(
            !trivial_view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. })),
            "a sub-floor change on a flat history must not flag: {:?}",
            trivial_view.anomalies
        );

        // Latest day $50 — a $45 deviation, well over the floor → flagged even with MAD=0.
        let mut real = flat;
        real.push(anomaly_record(
            "gpt-5.5",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            5_000,
        ));
        let real_view = anomalies_view(&snapshot_with_rows(now, real, Vec::new()));
        assert!(
            real_view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. })),
            "a real jump on a flat history must flag even when MAD=0: {:?}",
            real_view.anomalies
        );
    }

    #[test]
    fn anomalies_view_model_mix_mad_zero_share_floor_guards_both_ways() {
        // A near-flat 50/50 mix for eight days (each model's share MAD is 0). A sub-15-point
        // wobble on the latest day must NOT flag (the share floor), but a >15-point shift MUST —
        // the model-mix counterpart of the spend MAD=0 guard test. Spend is held flat ($1/record)
        // so only the mix signal is in play.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut base: Vec<FocusRecord> = Vec::new();
        for day in 9..=15 {
            base.push(anomaly_record(
                "model-a",
                utc_datetime(2026, 6, day, 9, 0, 0),
                1_000,
                100,
            ));
            base.push(anomaly_record(
                "model-b",
                utc_datetime(2026, 6, day, 9, 0, 0),
                1_000,
                100,
            ));
        }

        // Latest day 55/45 — an 11-point wobble, below the 0.15 share floor → NOT flagged.
        let mut wobble = base.clone();
        wobble.push(anomaly_record(
            "model-a",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_100,
            100,
        ));
        wobble.push(anomaly_record(
            "model-b",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            900,
            100,
        ));
        let wobble_view = anomalies_view(&snapshot_with_rows(now, wobble, Vec::new()));
        assert!(wobble_view.enough_history);
        assert!(
            !wobble_view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::ModelMixShift { .. })),
            "a sub-floor mix wobble must not flag: {:?}",
            wobble_view.anomalies
        );

        // Latest day 85/15 — a 35-point shift, over the floor → flagged on both models.
        let mut shift = base;
        shift.push(anomaly_record(
            "model-a",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_700,
            100,
        ));
        shift.push(anomaly_record(
            "model-b",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            300,
            100,
        ));
        let shift_view = anomalies_view(&snapshot_with_rows(now, shift, Vec::new()));
        let shifted: Vec<&str> = shift_view
            .anomalies
            .iter()
            .filter_map(|anomaly| match &anomaly.signal {
                AnomalySignal::ModelMixShift { model } => Some(model.as_str()),
                _ => None,
            })
            .collect();
        assert!(
            shifted.contains(&"model-a"),
            "the surged model: {shifted:?}"
        );
        assert!(
            shifted.contains(&"model-b"),
            "the dropped model: {shifted:?}"
        );
    }

    #[test]
    fn anomalies_view_flags_a_model_mix_shift_both_ways() {
        // gpt-5.5 carried 100% of tokens for seven days, then claude-opus-4.7 takes 100% on the
        // latest day — a mix shift flagged on BOTH the collapsing and the surging model. Spend is
        // held flat ($1/day) so the spend signal stays quiet and this isolates the mix signal.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = (9..=15)
            .map(|day| anomaly_record("gpt-5.5", utc_datetime(2026, 6, day, 9, 0, 0), 1_000, 100))
            .collect();
        rows.push(anomaly_record(
            "claude-opus-4.7",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            100,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(view.enough_history);
        // No spend spike (flat $1/day).
        assert!(
            !view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. })),
            "flat spend must not flag a spend spike: {:?}",
            view.anomalies
        );
        let shifted: Vec<&str> = view
            .anomalies
            .iter()
            .filter_map(|anomaly| match &anomaly.signal {
                AnomalySignal::ModelMixShift { model } => Some(model.as_str()),
                _ => None,
            })
            .collect();
        assert!(
            shifted.contains(&"gpt-5.5"),
            "the collapsed model: {shifted:?}"
        );
        assert!(
            shifted.contains(&"claude-opus-4.7"),
            "the surged model: {shifted:?}"
        );
    }

    #[test]
    fn anomalies_view_in_line_usage_reports_no_anomalies() {
        // Eight days of steady ~$5 spend, single model — enough history, nothing anomalous.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let costs = [500, 520, 480, 510, 490, 505, 495, 500];
        let rows: Vec<FocusRecord> = costs
            .iter()
            .enumerate()
            .map(|(index, cents)| {
                anomaly_record(
                    "gpt-5.5",
                    utc_datetime(2026, 6, 9 + index as u32, 9, 0, 0),
                    1_000,
                    *cents,
                )
            })
            .collect();
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(view.enough_history);
        assert!(
            view.anomalies.is_empty(),
            "steady usage should surface no anomalies: {:?}",
            view.anomalies
        );
    }

    #[test]
    fn anomalies_view_subscription_only_user_gets_a_model_mix_callout() {
        // THE T16b HEADLINE WIN (tests #1 + #2): a subscription-only user (Claude Code Max, NO API
        // key → zero API-lane rows) STILL gets a model-mix callout, because the model-mix signal
        // counts ALL lanes. model-a held 100% of tokens for seven subscription days, then
        // claude-opus surges to 100% on the latest day — a mix shift flagged on both. There is no
        // API-lane $ at all, so `no_usage` is false (all-lane token history is non-empty),
        // `enough_history` holds, and there is NO spend spike (the $ gate is skipped).
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = (9..=15)
            .map(|day| {
                anomaly_record_lane(
                    "model-a",
                    utc_datetime(2026, 6, day, 9, 0, 0),
                    1_000,
                    100,
                    FocusAccessPath::Subscription,
                )
            })
            .collect();
        rows.push(anomaly_record_lane(
            "claude-opus-4.7",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            100,
            FocusAccessPath::Subscription,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(!view.no_usage, "all-lane token history is non-empty");
        assert!(view.enough_history);
        assert_eq!(view.history_days, 8);
        let shifted: Vec<&str> = view
            .anomalies
            .iter()
            .filter_map(|anomaly| match &anomaly.signal {
                AnomalySignal::ModelMixShift { model } => Some(model.as_str()),
                _ => None,
            })
            .collect();
        assert!(
            shifted.contains(&"model-a"),
            "the collapsed model: {shifted:?}"
        );
        assert!(
            shifted.contains(&"claude-opus-4.7"),
            "the surged model: {shifted:?}"
        );
        // #2: a subscription-only user has no API-lane $, so the spend gate is skipped entirely.
        assert!(
            !view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. })),
            "no API-lane $ ⇒ no spend spike: {:?}",
            view.anomalies
        );
        // Each model-mix anomaly cites the all-lane history_days baseline.
        for anomaly in &view.anomalies {
            assert_eq!(anomaly.baseline_days, 8);
        }
    }

    #[test]
    fn anomalies_view_model_mix_pools_api_and_subscription_lanes() {
        // (Test #3) A MIXED user whose mix shift is visible ONLY when the two lanes are POOLED. Each
        // lane alone is stable (API = 100% model-a every day; subscription = 100% model-b every
        // day), so the OLD API-lane-only signal would see no shift at all. Pooled, model-b's
        // share jumps 50% → 90% on the latest day (the subscription lane's volume surges), which the
        // all-lane model-mix flags on both models. API spend is held flat ($1/day) so no spike.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = Vec::new();
        for day in 9..=16 {
            rows.push(anomaly_record_lane(
                "model-a",
                utc_datetime(2026, 6, day, 9, 0, 0),
                1_000,
                100,
                FocusAccessPath::Api,
            ));
        }
        for day in 9..=15 {
            rows.push(anomaly_record_lane(
                "model-b",
                utc_datetime(2026, 6, day, 10, 0, 0),
                1_000,
                0,
                FocusAccessPath::Subscription,
            ));
        }
        // The latest day: subscription model-b volume surges (9_000 tokens) → pooled model-b 90%.
        rows.push(anomaly_record_lane(
            "model-b",
            utc_datetime(2026, 6, 16, 10, 0, 0),
            9_000,
            0,
            FocusAccessPath::Subscription,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(view.enough_history);
        assert_eq!(view.history_days, 8);
        let shifted: Vec<&str> = view
            .anomalies
            .iter()
            .filter_map(|anomaly| match &anomaly.signal {
                AnomalySignal::ModelMixShift { model } => Some(model.as_str()),
                _ => None,
            })
            .collect();
        assert!(
            shifted.contains(&"model-b"),
            "pooled model-b surged: {shifted:?}"
        );
        assert!(
            shifted.contains(&"model-a"),
            "pooled model-a collapsed: {shifted:?}"
        );
        assert!(
            !view
                .anomalies
                .iter()
                .any(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. })),
            "flat API $ ⇒ no spend spike: {:?}",
            view.anomalies
        );
    }

    #[test]
    fn anomalies_view_signals_cite_their_own_realized_baseline_days() {
        // (Test #4) The two signals cite DIFFERENT realized baselines: the spend spike cites its OWN
        // API-lane-$ day count, the model-mix cites the all-lane token-day count. Subscription
        // model-a runs the full 14-day window (14 all-lane token-days) while API usage exists on
        // only the last 7 days (7 API-lane-$ days). Both fire: a $ spike on the latest day and a
        // model-mix shift (model-b surges).
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = Vec::new();
        // 14 subscription days (Jun 3..16) of model-a → 14 all-lane token-days, $0.
        for day in 3..=16 {
            rows.push(anomaly_record_lane(
                "model-a",
                utc_datetime(2026, 6, day, 8, 0, 0),
                1_000,
                0,
                FocusAccessPath::Subscription,
            ));
        }
        // 7 API days (Jun 10..16) of model-a: flat $1, then a $20 spike on the latest day.
        for day in 10..=15 {
            rows.push(anomaly_record_lane(
                "model-a",
                utc_datetime(2026, 6, day, 9, 0, 0),
                1_000,
                100,
                FocusAccessPath::Api,
            ));
        }
        rows.push(anomaly_record_lane(
            "model-a",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            1_000,
            2_000,
            FocusAccessPath::Api,
        ));
        // A model-b API surge on the latest day → a pooled model-mix shift.
        rows.push(anomaly_record_lane(
            "model-b",
            utc_datetime(2026, 6, 16, 9, 30, 0),
            9_000,
            100,
            FocusAccessPath::Api,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert_eq!(
            view.history_days, 14,
            "all-lane token-days span the full window"
        );
        let spike = view
            .anomalies
            .iter()
            .find(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. }))
            .unwrap_or_else(|| panic!("expected a spend spike: {:?}", view.anomalies));
        assert_eq!(
            spike.baseline_days, 7,
            "the spend spike cites its OWN API-lane-$ day count"
        );
        let mix = view
            .anomalies
            .iter()
            .find(|anomaly| matches!(anomaly.signal, AnomalySignal::ModelMixShift { .. }))
            .unwrap_or_else(|| panic!("expected a model-mix shift: {:?}", view.anomalies));
        assert_eq!(
            mix.baseline_days, 14,
            "the model-mix cites the all-lane token-day count"
        );
        assert_ne!(
            spike.baseline_days, mix.baseline_days,
            "the two signals' realized baselines differ here"
        );
    }

    #[test]
    fn anomalies_view_api_only_user_both_signals_share_one_baseline() {
        // (Test #6) No-regression for the T16 API-only persona: with usage on the API lane ONLY, the
        // API-lane-$ day count == the all-lane token-day count, so both signals fire AND cite the
        // SAME baseline (= history_days). Seven flat $1 gpt-5.5 days, then a $20 claude-opus surge.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = (9..=15)
            .map(|day| anomaly_record("gpt-5.5", utc_datetime(2026, 6, day, 9, 0, 0), 1_000, 100))
            .collect();
        rows.push(anomaly_record(
            "claude-opus-4.7",
            utc_datetime(2026, 6, 16, 9, 0, 0),
            5_000,
            2_000,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(!view.no_usage);
        assert_eq!(view.history_days, 8);
        let spike = view
            .anomalies
            .iter()
            .find(|anomaly| matches!(anomaly.signal, AnomalySignal::SpendSpike { .. }))
            .unwrap_or_else(|| panic!("expected a spend spike: {:?}", view.anomalies));
        let mix = view
            .anomalies
            .iter()
            .find(|anomaly| matches!(anomaly.signal, AnomalySignal::ModelMixShift { .. }))
            .unwrap_or_else(|| panic!("expected a model-mix shift: {:?}", view.anomalies));
        assert_eq!(spike.baseline_days, 8);
        assert_eq!(mix.baseline_days, 8);
        assert_eq!(spike.baseline_days, view.history_days);
    }

    #[test]
    fn anomalies_view_spend_spike_excludes_subscription_and_windows_out_future_rows() {
        // The spend spike is API-lane-$ only, so a huge SUBSCRIPTION-lane charge must never enter
        // the $ series (no phantom spike); a future-dated row (clock skew) is windowed out of BOTH
        // series. Post-T16b the subscription row DOES enter the all-lane token series, but as the
        // same model (gpt-5.5) it does not shift the mix — so nothing flags either way.
        let now = utc_datetime(2026, 6, 16, 12, 0, 0);
        let mut rows: Vec<FocusRecord> = (9..=16)
            .map(|day| anomaly_record("gpt-5.5", utc_datetime(2026, 6, day, 9, 0, 0), 1_000, 100))
            .collect();
        // A huge SUBSCRIPTION-lane charge on the latest day — must never enter the $ (spend) series
        // (if it did, Jun 16 would read ~$100 and a spike would fire).
        let sub = anomaly_record_lane(
            "gpt-5.5",
            utc_datetime(2026, 6, 16, 10, 0, 0),
            1_000,
            9_900,
            FocusAccessPath::Subscription,
        );
        rows.push(sub);
        // A future-dated API spike past `today` — excluded from the window (both series).
        rows.push(anomaly_record(
            "gpt-5.5",
            utc_datetime(2026, 6, 20, 9, 0, 0),
            1_000,
            9_900,
        ));
        let view = anomalies_view(&snapshot_with_rows(now, rows, Vec::new()));
        assert!(view.enough_history);
        assert_eq!(
            view.history_days, 8,
            "Jun 9..16 token-days (the subscription row overlaps Jun 16; the future row is windowed out)"
        );
        assert!(
            view.anomalies.is_empty(),
            "the subscription $ + future rows must not drive an anomaly (same model → no mix shift): {:?}",
            view.anomalies
        );
    }

    fn api_usage_event(model: &str, output_tokens: u64) -> UsageEvent {
        UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: model.to_string(),
            timestamp: timestamp(),
            input_tokens: 0,
            output_tokens,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: Some("/work/project".to_string()),
            access_path: AccessPath::Api,
            is_sidechain: false,
        }
    }

    #[test]
    fn models_view_merges_dated_fragments_so_a_benchmarked_model_is_never_a_gap() {
        // Two dated snapshots of ONE benchmarked model — `claude-opus-4-7` is in both the
        // pricing catalog and the bundled benchmarks, and `resolve_key` folds each dated form
        // onto that base. Keying by the raw `x_model` (the pre-fix bug) would split them and
        // leave the second fragment unmatched against the resolved-key overlay, rendering a
        // benchmarked model as "not benchmarked" — the §6 honesty invariant INVERTED. They
        // must collapse to ONE resolved-key row that joins 1:1 to the overlay.
        let events = [
            api_usage_event("claude-opus-4-7-20251101", 1_000_000),
            api_usage_event("claude-opus-4-7-20251201", 2_000_000),
        ];
        let focus_rows = match focus_records_from_usage(&events) {
            Ok(rows) => rows,
            Err(err) => panic!("events should price: {err}"),
        };
        let api_spend = focus_rows
            .iter()
            .filter(|row| CostLane::from_access_path(&row.x_access_path) == CostLane::Api)
            .fold(Decimal::ZERO, |acc, row| acc + row.billed_cost);
        let snapshot = snapshot_with_rows(timestamp(), focus_rows, Vec::new());

        let view = match models_view(&snapshot) {
            Ok(view) => view,
            Err(err) => panic!("models view should build: {err}"),
        };

        // ONE row at the resolved catalog key, not two raw fragments.
        assert_eq!(view.models.len(), 1, "models: {:?}", view.models);
        let row = &view.models[0];
        assert_eq!(row.model, "claude-opus-4-7");
        // Spend is summed across both fragments (so Models reconciles with trends + the
        // frontier overlay — the repricing-volume skew is gone with the join-key fix).
        assert!(api_spend > Decimal::ZERO, "fragments should price > 0");
        assert_eq!(row.totals.billed_cost, api_spend);
        // The benchmarked model is matched 1:1 to its overlay, with real appearances.
        let overlay = match &row.overlay {
            Some(overlay) => overlay,
            None => panic!("benchmarked claude-opus-4-7 must match an overlay, not a gap"),
        };
        assert_eq!(overlay.model_id, "claude-opus-4-7");
        assert!(
            !overlay.appearances.is_empty(),
            "claude-opus-4-7 is on a bundled benchmark"
        );

        // General invariant (§6): NO model with a bench appearance ever renders as a gap.
        // Every benchmarked overlay model resolves to exactly one row carrying its appearances.
        let bench = match bench_view(&snapshot) {
            Ok(bench) => bench,
            Err(err) => panic!("bench view should build: {err}"),
        };
        for benchmarked in bench
            .overlay
            .iter()
            .filter(|overlay| !overlay.appearances.is_empty())
        {
            let matched = match view
                .models
                .iter()
                .find(|row| row.model == benchmarked.model_id)
            {
                Some(row) => row,
                None => panic!("benchmarked {} has no models row", benchmarked.model_id),
            };
            match &matched.overlay {
                Some(joined) => assert!(
                    !joined.appearances.is_empty(),
                    "benchmarked {} rendered as a gap (empty appearances)",
                    benchmarked.model_id
                ),
                None => panic!(
                    "benchmarked {} rendered as a gap (no overlay)",
                    benchmarked.model_id
                ),
            }
        }
    }

    fn cost_for(rows: &[FocusRecord], token_type: &str) -> Decimal {
        match rows.iter().find(|row| row.x_token_type == token_type) {
            Some(row) => row.billed_cost,
            None => panic!("{token_type} row should exist"),
        }
    }

    #[test]
    fn bundled_pricing_is_valid_json() {
        assert!(bundled_pricing_value().is_ok());
    }

    #[test]
    fn bundled_pricing_deserializes_decimal_string_rates() {
        let catalog = match PricingCatalog::bundled() {
            Ok(value) => value,
            Err(err) => panic!("bundled pricing should parse: {err}"),
        };

        let rate = match catalog.rate("gpt-5.5", TokenType::Input) {
            Some(value) => value,
            None => panic!("gpt-5.5 input rate should exist"),
        };

        assert_eq!(rate.price, Decimal::new(500, 2));
        assert_eq!(catalog.currency, "USD");
        assert_eq!(sku_price_id(rate), "openai:gpt-5.5:input:tokens:2026-06-02");
    }

    #[test]
    fn bundled_litellm_snapshot_loads_with_pinned_provenance() {
        // T1-deferred loader check: the vendored LiteLLM artifact parses, and its embedded
        // source/as_of/content_hash surface on the per-rate provenance stamp (R8).
        let catalog = match PricingCatalog::from_json(bundled_litellm_pricing_json()) {
            Ok(value) => value,
            Err(err) => panic!("litellm snapshot should parse: {err}"),
        };
        let rate = match catalog.rate("mistral-large-latest", TokenType::Input) {
            Some(value) => value,
            None => panic!("a litellm long-tail model should be present"),
        };
        assert_eq!(rate.as_of, "2026-06-18");
        assert_eq!(rate.snapshot_id, "litellm@2026-06-18#36c8994e");
        assert_eq!(catalog.currency, "USD");

        // Pin the FULL upstream sha256 (not just the 8-char stamp): the artifact's embedded
        // `content_hash` MUST equal LITELLM_SNAPSHOT_CONTENT_HASH, so CI catches a snapshot
        // swapped to a different upstream revision (fold-in: the offline sidecar check alone
        // would not). Read it from the raw JSON the loader does not retain in full.
        let raw: serde_json::Value = match serde_json::from_str(bundled_litellm_pricing_json()) {
            Ok(value) => value,
            Err(err) => panic!("litellm snapshot should be valid JSON: {err}"),
        };
        assert_eq!(
            raw.get("content_hash").and_then(serde_json::Value::as_str),
            Some(LITELLM_SNAPSHOT_CONTENT_HASH),
            "the artifact's embedded content_hash must match the pinned full upstream sha256"
        );
        // And the stamp suffix is the first 8 chars of that full hash (not an independent value).
        assert!(
            rate.snapshot_id
                .ends_with(&format!("#{}", &LITELLM_SNAPSHOT_CONTENT_HASH[..8])),
            "the stamp suffix derives from the pinned full hash"
        );
    }

    #[test]
    fn layered_catalog_precedence_and_provenance_stamp() {
        // An override re-prices a curated model (claude-sonnet-4-6) at an absurd rate.
        let override_json = r#"{"schema_version":"1","source":"override","as_of":"2026-06-20",
            "content_hash":"deadbeefcafe","currency":"USD","models":[
            {"provider":"anthropic","model":"claude-sonnet-4-6","service_name":"Anthropic API",
             "rates":[{"meter":"input","unit":"1M_tokens","price":"99.00"}]}]}"#;
        let catalog = match PricingCatalog::layered(Some(override_json)) {
            Ok(value) => value,
            Err(err) => panic!("layered catalog should build: {err}"),
        };
        // override > curated: the override rate + stamp win for claude-sonnet-4-6.
        let r = match catalog.rate("claude-sonnet-4-6", TokenType::Input) {
            Some(value) => value,
            None => panic!("override rate"),
        };
        assert_eq!(r.price, Decimal::new(99, 0));
        assert_eq!(r.snapshot_id, "override@2026-06-20#deadbeef");
        // curated > litellm: a curated model not in the override keeps its curated rate+stamp.
        let r = match catalog.rate("gpt-5.5", TokenType::Input) {
            Some(value) => value,
            None => panic!("curated rate"),
        };
        assert_eq!(r.price, Decimal::new(500, 2));
        assert_eq!(r.snapshot_id, "curated@2026-06-02");
        // litellm long tail: a model absent from curated + override keeps the litellm stamp.
        let r = match catalog.rate("mistral-large-latest", TokenType::Input) {
            Some(value) => value,
            None => panic!("mistral-large-latest should come from the litellm tier"),
        };
        assert_eq!(r.snapshot_id, "litellm@2026-06-18#36c8994e");
    }

    #[test]
    fn d2_higher_tier_shadows_lower_on_price_not_just_provenance() {
        // Non-vacuous D2 shadowing guard: two tiers carry the SAME model at DIFFERENT prices,
        // so a per-model precedence bug (the lower tier shadowing the higher) would change the
        // PRICE — not only the snapshot_id. (The bundled curated/litellm tiers agree on price
        // for shared models, so a same-price test could only catch a stamp regression; this
        // synthetic pair catches a real mispricing.)
        let litellm = r#"{"schema_version":"1","source":"litellm","as_of":"2026-06-18",
            "content_hash":"aabbccddeeff","currency":"USD","models":[
            {"provider":"x","model":"shadowed-model","service_name":"X",
             "rates":[{"meter":"output","unit":"1M_tokens","price":"99.00"}]}]}"#;
        let curated_json = r#"{"schema_version":"1","source":"curated","as_of":"2026-06-02",
            "currency":"USD","models":[
            {"provider":"x","model":"shadowed-model","service_name":"X",
             "rates":[{"meter":"output","unit":"1M_tokens","price":"1.00"}]}]}"#;
        let mut catalog = match PricingCatalog::from_json(litellm) {
            Ok(value) => value,
            Err(err) => panic!("litellm tier should parse: {err}"),
        };
        let curated = match PricingCatalog::from_json(curated_json) {
            Ok(value) => value,
            Err(err) => panic!("curated tier should parse: {err}"),
        };
        catalog.overlay(curated); // curated (higher precedence) overlays litellm
        let rate = match catalog.rate("shadowed-model", TokenType::Output) {
            Some(value) => value,
            None => panic!("the shadowed model should resolve"),
        };
        // THE deciding assertion: the curated PRICE wins (1.00), not litellm's 99.00. If the
        // overlay let the lower tier shadow the higher, this would be 99.00 — the test fails.
        assert_eq!(
            rate.price,
            Decimal::new(1, 0),
            "the higher (curated) tier's price shadows the lower (litellm) tier's"
        );
        assert_eq!(rate.snapshot_id, "curated@2026-06-02");
    }

    #[test]
    fn dev_tool_models_reprice_unchanged_and_stamp_curated() {
        // R8 + no-regression: a dev-tool claude-sonnet-4-6 row prices from the CURATED tier
        // (authoritative; curated > litellm), unchanged from M1 (3.00/15.00 per 1M), and
        // carries the curated provenance stamp — the long-tail layer never alters it.
        let event = UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-sonnet-4-6".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 1_000_000,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Api,
            is_sidechain: false,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let input = match rows.iter().find(|r| r.x_token_type == "input") {
            Some(value) => value,
            None => panic!("input meter"),
        };
        assert_eq!(input.billed_cost, Decimal::new(3, 0));
        assert_eq!(
            input.x_pricing_snapshot_id.as_deref(),
            Some("curated@2026-06-02")
        );
        let output = match rows.iter().find(|r| r.x_token_type == "output") {
            Some(value) => value,
            None => panic!("output meter"),
        };
        assert_eq!(output.billed_cost, Decimal::new(15, 0));
        assert_eq!(
            output.x_pricing_snapshot_id.as_deref(),
            Some("curated@2026-06-02")
        );
    }

    #[test]
    fn pricing_snapshot_id_is_char_safe_and_never_panics() {
        // Real (ASCII-hex) cases: first 8 chars.
        assert_eq!(
            pricing_snapshot_id("litellm", "2026-06-18", Some("36c8994e4d65")),
            "litellm@2026-06-18#36c8994e"
        );
        // Short / empty / absent hashes.
        assert_eq!(
            pricing_snapshot_id("override", "2026-06-20", Some("abcd")),
            "override@2026-06-20#abcd"
        );
        assert_eq!(
            pricing_snapshot_id("curated", "2026-06-02", None),
            "curated@2026-06-02"
        );
        assert_eq!(
            pricing_snapshot_id("curated", "2026-06-02", Some("")),
            "curated@2026-06-02"
        );
        // Multibyte content_hash must NOT panic on a non-char-boundary byte slice (a user
        // override could supply anything). First 8 CHARS, char-safe.
        assert_eq!(
            pricing_snapshot_id("override", "2026-06-20", Some("aéééééééz")),
            "override@2026-06-20#aééééééé"
        );
    }

    #[test]
    fn read_pricing_override_reads_explicit_and_errors_on_missing_explicit() {
        // An explicit, readable path returns its content; an explicit missing path is a
        // typed error (the user asked for it). Default-path resolution is env-dependent and
        // covered by the CLI; here we exercise the deterministic explicit-path branches.
        let path =
            std::env::temp_dir().join(format!("costroid-override-{}.json", std::process::id()));
        if std::fs::write(&path, "{\"models\":[]}").is_err() {
            panic!("temp override write should succeed");
        }
        match read_pricing_override(Some(&path)) {
            Ok(Some(content)) => assert_eq!(content, "{\"models\":[]}"),
            other => panic!("explicit readable path should return its content: {other:?}"),
        }
        let _ = std::fs::remove_file(&path);
        let missing =
            std::env::temp_dir().join(format!("costroid-missing-{}.json", std::process::id()));
        match read_pricing_override(Some(&missing)) {
            Err(_) => {}
            other => panic!("explicit missing path should be a typed error: {other:?}"),
        }
    }

    #[test]
    fn default_options_match_now_screen_defaults() {
        let options = EngineOptions::default();
        let now_options = NowOptions::default();
        let trends_options = TrendsOptions::default();

        assert_eq!(options.period, Period::Week);
        assert_eq!(options.group_by, GroupBy::Model);
        assert_eq!(now_options.cost_period, Period::Week);
        assert_eq!(now_options.group_by, GroupBy::Model);
        assert_eq!(trends_options.period, Period::Week);
        assert_eq!(trends_options.group_by, GroupBy::Model);
    }

    #[test]
    fn usage_events_convert_to_one_record_per_nonzero_meter() {
        let event = usage_event(ProviderId::Codex, AccessPath::Subscription, timestamp());
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };

        assert_eq!(rows.len(), 3);
        assert!(rows.iter().all(|row| row.x_estimated));
        assert!(rows
            .iter()
            .all(|row| row.pricing_category.as_deref() == Some(PRICING_CATEGORY_STANDARD)));
        assert!(rows
            .iter()
            .all(|row| row.x_pricing_status == PRICING_STATUS_PRICED));
    }

    #[test]
    fn priced_usage_applies_costs_per_model_meter() {
        let event = UsageEvent {
            tool: ProviderId::Codex,
            model: "gpt-5.5".to_string(),
            timestamp: timestamp(),
            input_tokens: 10,
            output_tokens: 20,
            cache_read_tokens: 30,
            cache_write_tokens: 0,
            project: Some("/work/project".to_string()),
            access_path: AccessPath::Api,
            is_sidechain: false,
        };

        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };

        let input = match rows.iter().find(|row| row.x_token_type == "input") {
            Some(value) => value,
            None => panic!("input row should exist"),
        };
        let output = match rows.iter().find(|row| row.x_token_type == "output") {
            Some(value) => value,
            None => panic!("output row should exist"),
        };
        let cache_read = match rows.iter().find(|row| row.x_token_type == "cache_read") {
            Some(value) => value,
            None => panic!("cache_read row should exist"),
        };

        // Per-token representation: PricingQuantity is the token count, PricingUnit
        // is "tokens", and ListUnitPrice is per-token (5.00 / 1M = 0.000005).
        assert_eq!(input.pricing_quantity, Some(Decimal::from(10)));
        assert_eq!(input.consumed_quantity, Some(Decimal::from(10)));
        assert_eq!(input.pricing_unit.as_deref(), Some("tokens"));
        assert_eq!(
            input.pricing_category.as_deref(),
            Some(PRICING_CATEGORY_STANDARD)
        );
        assert_eq!(input.list_unit_price, Some(Decimal::new(5, 6)));
        assert_eq!(input.contracted_unit_price, Some(Decimal::new(5, 6)));
        // Cost is unchanged from M4.5: 10 tokens x $5.00/1M = $0.00005.
        assert_eq!(input.billed_cost, Decimal::new(5, 5));
        assert_eq!(input.effective_cost, input.billed_cost);
        assert_eq!(input.list_cost, input.billed_cost);
        assert_eq!(input.contracted_cost, input.billed_cost);
        assert_eq!(
            input.sku_price_id.as_deref(),
            Some("openai:gpt-5.5:input:tokens:2026-06-02")
        );
        assert_eq!(input.service_name, "OpenAI API");
        assert_eq!(input.billing_currency, "USD");
        assert_eq!(input.x_pricing_status, PRICING_STATUS_PRICED);

        assert_eq!(output.billed_cost, Decimal::new(6, 4));
        assert_eq!(cache_read.billed_cost, Decimal::new(15, 6));
    }

    #[test]
    fn cost_equals_per_token_price_times_quantity_and_matches_legacy_formula() {
        let event = UsageEvent {
            tool: ProviderId::Codex,
            model: "gpt-5.5".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_234_567,
            output_tokens: 20,
            cache_read_tokens: 30,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Api,
            is_sidechain: false,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let million = Decimal::from(1_000_000_u64);
        let legacy =
            |tokens: u64, per_million: Decimal| (Decimal::from(tokens) / million) * per_million;
        for row in &rows {
            let unit = match row.list_unit_price {
                Some(value) => value,
                None => panic!("priced row should have a unit price"),
            };
            let quantity = match row.pricing_quantity {
                Some(value) => value,
                None => panic!("priced row should have a pricing quantity"),
            };
            // FOCUS invariant, exact in Decimal: ListCost == ListUnitPrice x PricingQuantity.
            assert_eq!(row.list_cost, unit * quantity);
            assert_eq!(row.billed_cost, row.list_cost);
        }
        // Bit-for-bit identical to the pre-M6b (tokens / 1e6) x rate formula.
        assert_eq!(
            cost_for(&rows, "input"),
            legacy(1_234_567, Decimal::new(500, 2))
        );
        assert_eq!(cost_for(&rows, "output"), legacy(20, Decimal::new(3000, 2)));
        assert_eq!(
            cost_for(&rows, "cache_read"),
            legacy(30, Decimal::new(50, 2))
        );
    }

    #[test]
    fn claude_sonnet_prices_all_token_meters() {
        let event = UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-sonnet-4-6".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 1_000_000,
            cache_read_tokens: 1_000_000,
            cache_write_tokens: 1_000_000,
            project: None,
            access_path: AccessPath::Subscription,
            is_sidechain: false,
        };

        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };

        assert_eq!(rows.len(), 4);
        assert!(rows
            .iter()
            .all(|row| row.x_pricing_status == PRICING_STATUS_PRICED));
        assert!(rows.iter().all(|row| row.x_estimated));
        assert!(rows.iter().all(|row| row.x_access_path == "subscription"));
        assert_eq!(cost_for(&rows, "input"), Decimal::new(3, 0));
        assert_eq!(cost_for(&rows, "output"), Decimal::new(15, 0));
        assert_eq!(cost_for(&rows, "cache_read"), Decimal::new(30, 2));
        assert_eq!(cost_for(&rows, "cache_write"), Decimal::new(375, 2));
    }

    #[test]
    fn known_model_missing_meter_keeps_unpriced_convention() {
        let event = UsageEvent {
            tool: ProviderId::Codex,
            model: "gpt-5.5".to_string(),
            timestamp: timestamp(),
            input_tokens: 0,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 1_000_000,
            project: None,
            access_path: AccessPath::Api,
            is_sidechain: false,
        };

        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let row = &rows[0];

        assert_eq!(row.x_pricing_status, PRICING_STATUS_MISSING_PRICE);
        assert_eq!(row.billed_cost, Decimal::from(0));
        assert_eq!(row.list_unit_price, None);
        assert_eq!(row.contracted_unit_price, None);
        assert_eq!(row.sku_price_id, None);
        // FOCUS 1.3: null when SkuPriceId is null. Token count survives on x_.
        assert_eq!(row.pricing_category, None);
        assert_eq!(row.pricing_quantity, None);
        assert_eq!(row.pricing_unit, None);
        assert_eq!(row.consumed_quantity, None);
        assert_eq!(row.x_consumed_tokens, Decimal::from(1_000_000));
        assert_eq!(row.service_name, "OpenAI API");
        assert_eq!(row.billing_currency, "USD");
    }

    #[test]
    fn unknown_model_keeps_unpriced_convention_with_unknown_status() {
        let event = UsageEvent {
            tool: ProviderId::Cursor,
            model: "mystery-model".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Unknown,
            is_sidechain: false,
        };

        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let row = &rows[0];

        assert_eq!(row.x_pricing_status, PRICING_STATUS_UNKNOWN_MODEL);
        assert_eq!(row.billed_cost, Decimal::from(0));
        assert_eq!(row.list_unit_price, None);
        assert_eq!(row.contracted_unit_price, None);
        assert_eq!(row.sku_price_id, None);
        // FOCUS 1.3: null when SkuPriceId is null. Token count survives on x_.
        assert_eq!(row.pricing_category, None);
        assert_eq!(row.pricing_quantity, None);
        assert_eq!(row.pricing_unit, None);
        assert_eq!(row.consumed_quantity, None);
        assert_eq!(row.x_consumed_tokens, Decimal::from(1_000_000));
        assert_eq!(row.service_name, "Cursor");
        assert_eq!(row.billing_currency, DEFAULT_BILLING_CURRENCY);
    }

    #[test]
    fn exact_match_priced_costs_are_invariant_under_resolution() {
        // Cardinal rule: adding suffix-tolerant routing must not move any
        // already-priced model's cost. Exact matches take the same path as before.
        let events = vec![
            UsageEvent {
                tool: ProviderId::Codex,
                model: "gpt-5.5".to_string(),
                timestamp: timestamp(),
                input_tokens: 1_000_000,
                output_tokens: 1_000_000,
                cache_read_tokens: 1_000_000,
                cache_write_tokens: 0,
                project: None,
                access_path: AccessPath::Api,
                is_sidechain: false,
            },
            UsageEvent {
                tool: ProviderId::ClaudeCode,
                model: "claude-sonnet-4-6".to_string(),
                timestamp: timestamp(),
                input_tokens: 1_000_000,
                output_tokens: 1_000_000,
                cache_read_tokens: 1_000_000,
                cache_write_tokens: 1_000_000,
                project: None,
                access_path: AccessPath::Subscription,
                is_sidechain: false,
            },
        ];
        let rows = match focus_records_from_usage(&events) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let cost_of = |model: &str, meter: &str| -> Decimal {
            match rows
                .iter()
                .find(|r| r.x_model == model && r.x_token_type == meter)
            {
                Some(r) => r.billed_cost,
                None => panic!("missing row for {model}/{meter}"),
            }
        };
        // gpt-5.5 per-1M: input 5.00, output 30.00, cache_read 0.50 (no cache_write).
        assert_eq!(cost_of("gpt-5.5", "input"), Decimal::new(5, 0));
        assert_eq!(cost_of("gpt-5.5", "output"), Decimal::new(30, 0));
        assert_eq!(cost_of("gpt-5.5", "cache_read"), Decimal::new(50, 2));
        // claude-sonnet-4-6 per-1M: 3.00 / 15.00 / 0.30 / 3.75.
        assert_eq!(cost_of("claude-sonnet-4-6", "input"), Decimal::new(3, 0));
        assert_eq!(cost_of("claude-sonnet-4-6", "output"), Decimal::new(15, 0));
        assert_eq!(
            cost_of("claude-sonnet-4-6", "cache_read"),
            Decimal::new(30, 2)
        );
        assert_eq!(
            cost_of("claude-sonnet-4-6", "cache_write"),
            Decimal::new(375, 2)
        );
        // Exact matches route to their own rate: SkuPriceId embeds the model id.
        for row in rows
            .iter()
            .filter(|r| r.x_pricing_status == PRICING_STATUS_PRICED)
        {
            let sku_price_id = match &row.sku_price_id {
                Some(value) => value,
                None => panic!("priced row should carry a SkuPriceId"),
            };
            assert!(
                sku_price_id.contains(&row.x_model),
                "exact-match SkuPriceId should embed the model id: {sku_price_id}"
            );
        }
    }

    #[test]
    fn dated_haiku_snapshot_resolves_to_base_rate_with_honest_ids() {
        // The real heavy-usage case: claude-haiku-4-5-20251001 is the dated snapshot
        // of the in-table base claude-haiku-4-5. It must price at the base rate,
        // while x_Model + SkuId keep the ACTUAL dated id and SkuPriceId points at the
        // base rate that priced it.
        let event = UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-haiku-4-5-20251001".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 1_000_000,
            cache_read_tokens: 1_000_000,
            cache_write_tokens: 1_000_000,
            project: None,
            access_path: AccessPath::Subscription,
            is_sidechain: false,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        assert_eq!(rows.len(), 4);
        assert!(rows
            .iter()
            .all(|r| r.x_pricing_status == PRICING_STATUS_PRICED));
        let cost_of = |meter: &str| -> Decimal {
            match rows.iter().find(|r| r.x_token_type == meter) {
                Some(r) => r.billed_cost,
                None => panic!("missing meter {meter}"),
            }
        };
        // base claude-haiku-4-5 per-1M: 1.00 / 5.00 / 0.10 / 1.25.
        assert_eq!(cost_of("input"), Decimal::new(1, 0));
        assert_eq!(cost_of("output"), Decimal::new(5, 0));
        assert_eq!(cost_of("cache_read"), Decimal::new(10, 2));
        assert_eq!(cost_of("cache_write"), Decimal::new(125, 2));
        for row in &rows {
            // Honesty: the report shows what actually ran...
            assert_eq!(row.x_model, "claude-haiku-4-5-20251001");
            let expected_sku_id = format!("claude-haiku-4-5-20251001:{}", row.x_token_type);
            assert_eq!(row.sku_id.as_deref(), Some(expected_sku_id.as_str()));
            // ...while the price id references the BASE rate that priced it.
            let sku_price_id = match &row.sku_price_id {
                Some(value) => value,
                None => panic!("priced row should carry a SkuPriceId"),
            };
            assert!(
                sku_price_id.starts_with("anthropic:claude-haiku-4-5:"),
                "SkuPriceId should reference the base rate: {sku_price_id}"
            );
            assert!(
                !sku_price_id.contains("20251001"),
                "SkuPriceId must not embed the dated id: {sku_price_id}"
            );
        }
    }

    #[test]
    fn openai_dashed_date_snapshot_resolves_to_base() {
        // OpenAI snapshots use -YYYY-MM-DD; gpt-5.5-2025-10-01 must price at gpt-5.5.
        let event = UsageEvent {
            tool: ProviderId::Codex,
            model: "gpt-5.5-2025-10-01".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Api,
            is_sidechain: false,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        assert_eq!(rows.len(), 1);
        let row = &rows[0];
        assert_eq!(row.x_pricing_status, PRICING_STATUS_PRICED);
        assert_eq!(row.billed_cost, Decimal::new(5, 0));
        assert_eq!(row.x_model, "gpt-5.5-2025-10-01");
        let sku_price_id = match &row.sku_price_id {
            Some(value) => value,
            None => panic!("priced row should carry a SkuPriceId"),
        };
        assert!(
            sku_price_id.starts_with("openai:gpt-5.5:"),
            "{sku_price_id}"
        );
    }

    #[test]
    fn genuinely_new_or_fake_models_stay_unknown_not_missing_price() {
        // The "not too loose" guard: a version bump absent from the table
        // (claude-opus-4-9), a date-shaped id whose base is absent
        // (made-up-model-20251001), and a plain fake must all flag unknown_model
        // (NOT missing_price), with no cost and a null SkuPriceId.
        for model in [
            "claude-opus-4-9",
            "made-up-model-20251001",
            "totally-fake-xyz",
        ] {
            let event = UsageEvent {
                tool: ProviderId::ClaudeCode,
                model: model.to_string(),
                timestamp: timestamp(),
                input_tokens: 1_000_000,
                output_tokens: 0,
                cache_read_tokens: 0,
                cache_write_tokens: 0,
                project: None,
                access_path: AccessPath::Subscription,
                is_sidechain: false,
            };
            let rows = match focus_records_from_usage(&[event]) {
                Ok(value) => value,
                Err(err) => panic!("conversion should succeed: {err}"),
            };
            assert_eq!(rows.len(), 1);
            let row = &rows[0];
            assert_eq!(
                row.x_pricing_status, PRICING_STATUS_UNKNOWN_MODEL,
                "{model} must be unknown_model, not missing_price"
            );
            assert_eq!(row.billed_cost, Decimal::from(0));
            assert_eq!(row.sku_price_id, None);
            assert_eq!(row.pricing_quantity, None);
        }
    }

    #[test]
    fn opus_4_8_prices_at_published_rates() {
        // Step 2 (table refresh): once the curated claude-opus-4-8 entry exists, the
        // genuinely-new model flips unknown_model -> priced at its own exact rate.
        let event = UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-opus-4-8".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 1_000_000,
            cache_read_tokens: 1_000_000,
            cache_write_tokens: 1_000_000,
            project: None,
            access_path: AccessPath::Subscription,
            is_sidechain: false,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        assert_eq!(rows.len(), 4);
        assert!(rows
            .iter()
            .all(|r| r.x_pricing_status == PRICING_STATUS_PRICED));
        let cost_of = |meter: &str| -> Decimal {
            match rows.iter().find(|r| r.x_token_type == meter) {
                Some(r) => r.billed_cost,
                None => panic!("missing meter {meter}"),
            }
        };
        // published claude-opus-4-8 per-1M: 5.00 / 25.00 / 0.50 / 6.25.
        assert_eq!(cost_of("input"), Decimal::new(5, 0));
        assert_eq!(cost_of("output"), Decimal::new(25, 0));
        assert_eq!(cost_of("cache_read"), Decimal::new(50, 2));
        assert_eq!(cost_of("cache_write"), Decimal::new(625, 2));
        for row in &rows {
            // Exact match: SkuPriceId references opus-4-8 itself, not an aliased base.
            let sku_price_id = match &row.sku_price_id {
                Some(value) => value,
                None => panic!("priced row should carry a SkuPriceId"),
            };
            assert!(
                sku_price_id.starts_with("anthropic:claude-opus-4-8:"),
                "{sku_price_id}"
            );
        }
    }

    #[test]
    fn strip_date_suffix_only_strips_real_snapshots() {
        // Version components / base ids are never treated as dates.
        assert_eq!(strip_date_suffix("claude-opus-4-8"), None);
        assert_eq!(strip_date_suffix("gpt-5.5"), None);
        assert_eq!(strip_date_suffix("gpt-5.4"), None);
        assert_eq!(strip_date_suffix("claude-haiku-4-5"), None);
        assert_eq!(strip_date_suffix("mystery-model"), None);
        // Anthropic compact 8-digit date.
        assert_eq!(
            strip_date_suffix("claude-haiku-4-5-20251001"),
            Some("claude-haiku-4-5")
        );
        assert_eq!(
            strip_date_suffix("claude-3-5-sonnet-20241022"),
            Some("claude-3-5-sonnet")
        );
        // OpenAI dashed date.
        assert_eq!(strip_date_suffix("gpt-5.5-2025-10-01"), Some("gpt-5.5"));
        assert_eq!(strip_date_suffix("gpt-4o-2024-08-06"), Some("gpt-4o"));
        // Wrong digit counts / malformed are left unmatched (conservative).
        assert_eq!(strip_date_suffix("claude-haiku-4-5-2025100"), None); // 7 digits
        assert_eq!(strip_date_suffix("claude-haiku-4-5-202510011"), None); // 9 digits
        assert_eq!(strip_date_suffix("claude-haiku-4-5-2025"), None); // 4 digits
                                                                      // Empty base rejected; no panic on degenerate inputs.
        assert_eq!(strip_date_suffix("-20251001"), None);
        assert_eq!(strip_date_suffix("20251001"), None);
        assert_eq!(strip_date_suffix(""), None);
    }

    #[test]
    fn resolve_key_treats_absent_version_as_unknown_until_an_entry_exists() {
        // Demonstrates the step-1 -> step-2 transition independently of the bundled
        // table: a version bump is unknown (resolve_key None, not a missing meter)
        // while absent, and resolves exactly once an entry is added.
        let without = r#"{"schema_version":"1","as_of":"2026-06-02","currency":"USD","models":[{"provider":"anthropic","model":"claude-opus-4-7","service_name":"Anthropic API","rates":[{"meter":"input","unit":"1M_tokens","price":"5.00"}]}]}"#;
        let with = r#"{"schema_version":"1","as_of":"2026-06-02","currency":"USD","models":[{"provider":"anthropic","model":"claude-opus-4-7","service_name":"Anthropic API","rates":[{"meter":"input","unit":"1M_tokens","price":"5.00"}]},{"provider":"anthropic","model":"claude-opus-4-8","service_name":"Anthropic API","rates":[{"meter":"input","unit":"1M_tokens","price":"5.00"}]}]}"#;
        let absent = match PricingCatalog::from_json(without) {
            Ok(value) => value,
            Err(err) => panic!("parse should succeed: {err}"),
        };
        let present = match PricingCatalog::from_json(with) {
            Ok(value) => value,
            Err(err) => panic!("parse should succeed: {err}"),
        };
        // A version bump is not a date suffix, so it never folds onto opus-4-7.
        assert_eq!(absent.resolve_key("claude-opus-4-8"), None);
        assert_eq!(
            present.resolve_key("claude-opus-4-8"),
            Some("claude-opus-4-8")
        );
    }

    #[test]
    fn explicit_dated_entry_overrides_base_alias_fallback() {
        // Escape hatch: an exact dated entry wins over the date-stripped base, so a
        // repriced snapshot can be pinned without code changes.
        let json = r#"{"schema_version":"1","as_of":"2026-06-02","currency":"USD","models":[{"provider":"anthropic","model":"claude-haiku-4-5","service_name":"Anthropic API","rates":[{"meter":"input","unit":"1M_tokens","price":"1.00"}]},{"provider":"anthropic","model":"claude-haiku-4-5-20251001","service_name":"Anthropic API","rates":[{"meter":"input","unit":"1M_tokens","price":"9.99"}]}]}"#;
        let catalog = match PricingCatalog::from_json(json) {
            Ok(value) => value,
            Err(err) => panic!("parse should succeed: {err}"),
        };
        assert_eq!(
            catalog.resolve_key("claude-haiku-4-5-20251001"),
            Some("claude-haiku-4-5-20251001")
        );
        let rate = match catalog.rate("claude-haiku-4-5-20251001", TokenType::Input) {
            Some(value) => value,
            None => panic!("explicit dated entry should have a rate"),
        };
        assert_eq!(rate.price, Decimal::new(999, 2));
    }

    #[test]
    fn api_and_subscription_priced_rows_stay_in_separate_lanes() {
        let rows = match focus_records_from_usage(&[
            UsageEvent {
                tool: ProviderId::Codex,
                model: "gpt-5.5".to_string(),
                timestamp: timestamp(),
                input_tokens: 1_000_000,
                output_tokens: 0,
                cache_read_tokens: 0,
                cache_write_tokens: 0,
                project: None,
                access_path: AccessPath::Api,
                is_sidechain: false,
            },
            UsageEvent {
                tool: ProviderId::Codex,
                model: "gpt-5.5".to_string(),
                timestamp: timestamp(),
                input_tokens: 0,
                output_tokens: 1_000_000,
                cache_read_tokens: 0,
                cache_write_tokens: 0,
                project: None,
                access_path: AccessPath::Subscription,
                is_sidechain: false,
            },
        ]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let snapshot = snapshot_with_rows(timestamp(), rows, Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());
        let api = match summary
            .current_costs
            .iter()
            .find(|summary| summary.lane == CostLane::Api)
        {
            Some(value) => value,
            None => panic!("api lane should exist"),
        };
        let subscription = match summary
            .current_costs
            .iter()
            .find(|summary| summary.lane == CostLane::SubscriptionEstimate)
        {
            Some(value) => value,
            None => panic!("subscription lane should exist"),
        };

        assert_eq!(api.totals.billed_cost, Decimal::new(5, 0));
        assert_eq!(subscription.totals.billed_cost, Decimal::new(30, 0));
        assert_eq!(api.totals.pricing_coverage.priced_rows, 1);
        assert_eq!(subscription.totals.pricing_coverage.priced_rows, 1);
        assert_eq!(api.totals.estimated_rows, 1);
        assert_eq!(subscription.totals.estimated_rows, 1);
    }

    #[test]
    fn export_helpers_emit_json_and_csv() {
        let rows = match focus_records_from_usage(&[usage_event(
            ProviderId::ClaudeCode,
            AccessPath::Unknown,
            timestamp(),
        )]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };
        let json = match export_focus_json(rows.clone()) {
            Ok(value) => value,
            Err(err) => panic!("json export should succeed: {err}"),
        };
        let csv = match export_focus_csv(&rows) {
            Ok(value) => value,
            Err(err) => panic!("csv export should succeed: {err}"),
        };

        assert!(json.contains("\"focusVersion\": \"1.3\""));
        assert!(csv.starts_with("BilledCost,EffectiveCost,ListCost,ContractedCost"));
    }

    #[test]
    fn now_summary_keeps_access_path_lanes_separate() {
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                generated_at,
                "shared-model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Subscription,
                generated_at,
                "shared-model",
                Some("/work/a"),
                TokenType::Output,
                20,
            ),
            record(
                FocusAccessPath::Unknown,
                generated_at,
                "shared-model",
                Some("/work/a"),
                TokenType::CacheRead,
                30,
            ),
        ];
        let snapshot = snapshot_with_rows(generated_at, rows, Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());

        assert_eq!(summary.current_costs.len(), 3);
        assert!(summary
            .current_costs
            .iter()
            .any(|summary| summary.lane == CostLane::Api && summary.totals.tokens.input == 10));
        assert!(summary.current_costs.iter().any(|summary| {
            summary.lane == CostLane::SubscriptionEstimate && summary.totals.tokens.output == 20
        }));
        assert!(summary.current_costs.iter().any(|summary| {
            summary.lane == CostLane::UnknownAccess && summary.totals.tokens.cache_read == 30
        }));
    }

    #[test]
    fn engine_totals_tokens_from_unpriced_rows_via_x_consumed_tokens() {
        // Unpriced rows null ConsumedQuantity (FOCUS 1.3), so the engine must read
        // token totals from x_ConsumedTokens — else unpriced usage would vanish.
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let row = record(
            FocusAccessPath::Api,
            generated_at,
            "unpriced-model",
            Some("/work/a"),
            TokenType::Input,
            4_242,
        );
        assert_eq!(
            row.consumed_quantity, None,
            "unpriced row nulls ConsumedQuantity"
        );
        assert_eq!(row.x_consumed_tokens, Decimal::from(4_242));
        let snapshot = snapshot_with_rows(generated_at, vec![row], Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());
        let total: u64 = summary
            .current_costs
            .iter()
            .map(|summary| summary.totals.tokens.input)
            .sum();
        assert_eq!(total, 4_242);
    }

    #[test]
    fn now_summary_uses_configurable_cost_period() {
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let previous_week = utc_datetime(2026, 1, 2, 12, 0, 0);
        let previous_month = utc_datetime(2025, 12, 31, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                previous_week,
                "old-model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                generated_at,
                "new-model",
                Some("/work/a"),
                TokenType::Input,
                20,
            ),
            record(
                FocusAccessPath::Api,
                previous_month,
                "last-month-model",
                Some("/work/a"),
                TokenType::Input,
                30,
            ),
        ];
        let snapshot = snapshot_with_rows(generated_at, rows, Vec::new());

        let week = now_summary(&snapshot, NowOptions::default());
        let month = now_summary(
            &snapshot,
            NowOptions {
                cost_period: Period::Month,
                group_by: GroupBy::Model,
            },
        );

        assert_eq!(week.current_costs.len(), 1);
        assert_eq!(week.current_costs[0].totals.tokens.input, 20);
        assert_eq!(month.current_costs.len(), 2);
        assert_eq!(
            month
                .current_costs
                .iter()
                .map(|summary| summary.totals.tokens.input)
                .sum::<u64>(),
            30
        );
        assert!(!month
            .current_costs
            .iter()
            .any(|summary| summary.group.value == "last-month-model"));
    }

    #[test]
    fn trends_group_by_model_app_and_total() {
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                generated_at,
                "model-a",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                generated_at,
                "model-b",
                Some("/work/b"),
                TokenType::Input,
                20,
            ),
            record(
                FocusAccessPath::Api,
                generated_at,
                "model-b",
                None,
                TokenType::Input,
                30,
            ),
        ];
        let snapshot = snapshot_with_rows(generated_at, rows, Vec::new());

        let by_model = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Week,
                group_by: GroupBy::Model,
            },
        );
        let by_app = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Week,
                group_by: GroupBy::App,
            },
        );
        let total = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Week,
                group_by: GroupBy::Total,
            },
        );

        assert_eq!(by_model.totals.len(), 2);
        assert!(by_model
            .totals
            .iter()
            .any(|summary| summary.group.value == "model-a"));
        assert_eq!(by_app.totals.len(), 3);
        assert!(by_app
            .totals
            .iter()
            .any(|summary| summary.group.value == UNKNOWN_GROUP_VALUE));
        assert_eq!(total.totals.len(), 1);
        assert_eq!(total.totals[0].group.value, TOTAL_GROUP_VALUE);
        assert_eq!(total.totals[0].totals.tokens.input, 60);
    }

    #[test]
    fn trends_buckets_by_selected_local_periods() {
        let monday_local = local_datetime(2026, 1, 5, 12, 0, 0);
        let sunday_local = local_datetime(2026, 1, 11, 12, 0, 0);
        let next_monday_local = local_datetime(2026, 1, 12, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                monday_local.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                sunday_local.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                20,
            ),
            record(
                FocusAccessPath::Api,
                next_monday_local.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                30,
            ),
        ];
        let snapshot = snapshot_with_rows(monday_local.with_timezone(&Utc), rows, Vec::new());

        let week = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Week,
                group_by: GroupBy::Total,
            },
        );

        assert_eq!(week.buckets.len(), 2);
        assert_eq!(
            week.buckets[0].period.start.with_timezone(&Local).weekday(),
            Weekday::Mon
        );
        assert_eq!(
            week.buckets[1].period.start.with_timezone(&Local).weekday(),
            Weekday::Mon
        );
        assert_ne!(week.buckets[0].period.start, week.buckets[1].period.start);
        assert_eq!(week.buckets[0].totals.tokens.input, 30);
        assert_eq!(week.buckets[1].totals.tokens.input, 30);
    }

    #[test]
    fn trends_day_buckets_split_at_local_midnight() {
        let before_midnight = local_datetime(2026, 1, 5, 23, 59, 59);
        let after_midnight = local_datetime(2026, 1, 6, 0, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                before_midnight.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                after_midnight.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                20,
            ),
        ];
        let snapshot = snapshot_with_rows(after_midnight.with_timezone(&Utc), rows, Vec::new());

        let day = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Day,
                group_by: GroupBy::Total,
            },
        );

        assert_eq!(day.buckets.len(), 2);
        assert_ne!(day.buckets[0].period.start, day.buckets[1].period.start);
        assert_eq!(day.buckets[0].totals.row_count, 1);
        assert_eq!(day.buckets[0].totals.tokens.input, 10);
        assert_eq!(day.buckets[1].totals.row_count, 1);
        assert_eq!(day.buckets[1].totals.tokens.input, 20);
    }

    #[test]
    fn trends_month_buckets_split_by_local_month() {
        let january = local_datetime(2025, 1, 31, 12, 0, 0);
        let february = local_datetime(2025, 2, 1, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                january.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                february.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                20,
            ),
        ];
        let snapshot = snapshot_with_rows(february.with_timezone(&Utc), rows, Vec::new());

        let month = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Month,
                group_by: GroupBy::Total,
            },
        );

        assert_eq!(month.buckets.len(), 2);
        assert_eq!(
            month.buckets[0].period.start.with_timezone(&Local).month(),
            1
        );
        assert_eq!(month.buckets[0].totals.row_count, 1);
        assert_eq!(month.buckets[0].totals.tokens.input, 10);
        assert_eq!(
            month.buckets[1].period.start.with_timezone(&Local).month(),
            2
        );
        assert_eq!(month.buckets[1].totals.row_count, 1);
        assert_eq!(month.buckets[1].totals.tokens.input, 20);
    }

    #[test]
    fn trends_year_buckets_split_by_local_year() {
        let current_year = local_datetime(2025, 12, 31, 12, 0, 0);
        let next_year = local_datetime(2026, 1, 1, 12, 0, 0);
        let rows = vec![
            record(
                FocusAccessPath::Api,
                current_year.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                10,
            ),
            record(
                FocusAccessPath::Api,
                next_year.with_timezone(&Utc),
                "model",
                Some("/work/a"),
                TokenType::Input,
                30,
            ),
        ];
        let snapshot = snapshot_with_rows(next_year.with_timezone(&Utc), rows, Vec::new());

        let year = trends_summary(
            &snapshot,
            TrendsOptions {
                period: Period::Year,
                group_by: GroupBy::Total,
            },
        );

        assert_eq!(year.buckets.len(), 2);
        assert_eq!(
            year.buckets[0].period.start.with_timezone(&Local).year(),
            2025
        );
        assert_eq!(year.buckets[0].totals.row_count, 1);
        assert_eq!(year.buckets[0].totals.tokens.input, 10);
        assert_eq!(
            year.buckets[1].period.start.with_timezone(&Local).year(),
            2026
        );
        assert_eq!(year.buckets[1].totals.row_count, 1);
        assert_eq!(year.buckets[1].totals.tokens.input, 30);
    }

    #[test]
    fn local_period_boundaries_start_at_local_midnight() {
        let local = local_datetime(2026, 1, 15, 0, 30, 0);

        let day = period_range_for(Period::Day, local.with_timezone(&Utc));
        let month = period_range_for(Period::Month, local.with_timezone(&Utc));
        let year = period_range_for(Period::Year, local.with_timezone(&Utc));

        assert_eq!(day.start.with_timezone(&Local).hour(), 0);
        assert_eq!(day.start.with_timezone(&Local).minute(), 0);
        assert_eq!(month.start.with_timezone(&Local).day(), 1);
        assert_eq!(year.start.with_timezone(&Local).month(), 1);
        assert_eq!(year.start.with_timezone(&Local).day(), 1);
    }

    #[test]
    fn placeholder_pricing_reports_missing_price_and_tokens() {
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let rows = vec![record(
            FocusAccessPath::Api,
            generated_at,
            "model",
            Some("/work/a"),
            TokenType::Input,
            10,
        )];
        let snapshot = snapshot_with_rows(generated_at, rows, Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());
        let totals = &summary.current_costs[0].totals;

        assert_eq!(totals.billed_cost, Decimal::from(0));
        assert_eq!(totals.effective_cost, Decimal::from(0));
        assert_eq!(totals.tokens.input, 10);
        assert_eq!(totals.pricing_coverage.missing_price_rows, 1);
        assert_eq!(totals.estimated_rows, 1);
    }

    #[test]
    fn pricing_coverage_tracks_unknown_models_separately() {
        let generated_at = utc_datetime(2026, 1, 7, 12, 0, 0);
        let mut row = record(
            FocusAccessPath::Api,
            generated_at,
            "model",
            Some("/work/a"),
            TokenType::Input,
            10,
        );
        row.x_pricing_status = PRICING_STATUS_UNKNOWN_MODEL.to_string();
        let snapshot = snapshot_with_rows(generated_at, vec![row], Vec::new());

        let summary = now_summary(&snapshot, NowOptions::default());

        assert_eq!(
            summary.current_costs[0]
                .totals
                .pricing_coverage
                .unknown_model_rows,
            1
        );
    }

    #[test]
    fn limit_availability_distinguishes_unavailable_available_partial_and_stale() {
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limits = vec![
            LimitWindow {
                tool: ProviderId::ClaudeCode,
                plan: None,
                kind: LimitKind::FiveHour,
                measure: None,
                resets_at: None,
                captured_at: now,
                status: LimitStatus::Unavailable,
                label: Some("unavailable".to_string()),
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::FiveHour,
                measure: Some(LimitMeasure::TokenFraction(0.5)),
                resets_at: Some(now + Duration::minutes(30)),
                captured_at: now,
                status: LimitStatus::Verified,
                label: None,
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                measure: Some(LimitMeasure::TokenFraction(0.6)),
                resets_at: None,
                captured_at: now,
                status: LimitStatus::Verified,
                label: None,
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                measure: Some(LimitMeasure::TokenFraction(0.7)),
                resets_at: Some(now - Duration::minutes(5)),
                captured_at: now,
                status: LimitStatus::Verified,
                label: None,
            },
        ];
        let snapshot = snapshot_with_rows(now, Vec::new(), limits);

        let summary = now_summary(&snapshot, NowOptions::default());

        assert!(matches!(
            summary.limits[0].availability,
            LimitAvailability::Unavailable { .. }
        ));
        assert!(matches!(
            summary.limits[1].availability,
            LimitAvailability::Available {
                reset_in_seconds: 1800,
                ..
            }
        ));
        assert!(matches!(
            summary.limits[2].availability,
            LimitAvailability::Partial { .. }
        ));
        // A reading whose window has already reset (resets_at in the past) ages out
        // against generated_at (ARCHITECTURE) — with no local volume
        // to estimate from, that is Unavailable, never a confident stale meter.
        assert!(matches!(
            summary.limits[3].availability,
            LimitAvailability::Unavailable { .. }
        ));
    }

    #[test]
    fn limit_availability_maps_status_and_measure() {
        // The pure status+measure map (T2): the reshaped arms carry the LimitMeasure,
        // Unverified surfaces as its own arm, and a measure-less reading — even one not
        // flagged Unavailable — degrades to Unavailable rather than a fake number.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let window = |measure, status, resets_at| LimitWindow {
            tool: ProviderId::ClaudeCode,
            plan: None,
            kind: LimitKind::FiveHour,
            measure,
            resets_at,
            captured_at: now,
            status,
            label: None,
        };

        // Verified + measure + live reset → Available, carrying the measure.
        let available = limit_availability(
            &window(
                Some(LimitMeasure::TokenFraction(0.42)),
                LimitStatus::Verified,
                Some(now + Duration::minutes(30)),
            ),
            now,
            &TokenTotals::default(),
            None,
        );
        assert!(matches!(
            available,
            LimitAvailability::Available {
                measure: LimitMeasure::TokenFraction(f),
                reset_in_seconds: 1800,
                ..
            } if (f - 0.42).abs() < 1e-9
        ));

        // Unverified status → Unverified arm (never Available), measure preserved.
        let unverified = limit_availability(
            &window(
                Some(LimitMeasure::TokenFraction(0.9)),
                LimitStatus::Unverified,
                Some(now + Duration::minutes(30)),
            ),
            now,
            &TokenTotals::default(),
            None,
        );
        assert!(matches!(
            unverified,
            LimitAvailability::Unverified {
                measure: LimitMeasure::TokenFraction(_),
                ..
            }
        ));

        // Verified but measure-less → Unavailable, not a fabricated zero (no local
        // volume to estimate from).
        let no_measure = limit_availability(
            &window(None, LimitStatus::Verified, Some(now)),
            now,
            &TokenTotals::default(),
            None,
        );
        assert!(matches!(no_measure, LimitAvailability::Unavailable { .. }));

        // A Spend measure rides the same arms untouched (renders in T6).
        let spend = limit_availability(
            &window(
                Some(LimitMeasure::Spend {
                    used_usd: Decimal::new(1250, 2),
                    included_usd: Some(Decimal::new(2000, 2)),
                }),
                LimitStatus::Verified,
                Some(now + Duration::hours(1)),
            ),
            now,
            &TokenTotals::default(),
            None,
        );
        assert!(matches!(
            spend,
            LimitAvailability::Available {
                measure: LimitMeasure::Spend { .. },
                ..
            }
        ));
    }

    /// A Claude FOCUS row at `timestamp` carrying `tokens` input tokens. When `usd` is
    /// `Some`, the row is marked priced with that `effective_cost`; otherwise it stays
    /// unpriced (cost 0) — so tests can drive both estimated-value branches.
    fn claude_row(timestamp: DateTime<Utc>, tokens: u64, usd: Option<Decimal>) -> FocusRecord {
        let mut row = record(
            FocusAccessPath::Subscription,
            timestamp,
            "claude-sonnet",
            None,
            TokenType::Input,
            tokens,
        );
        row.x_tool = ProviderId::ClaudeCode.to_string();
        if let Some(usd) = usd {
            row.x_pricing_status = PRICING_STATUS_PRICED.to_string();
            row.effective_cost = usd;
        }
        row
    }

    /// A Claude 5h window with the given measure/status/reset, captured at `now`.
    fn claude_five_hour(
        measure: Option<LimitMeasure>,
        status: LimitStatus,
        resets_at: Option<DateTime<Utc>>,
        now: DateTime<Utc>,
    ) -> LimitWindow {
        LimitWindow {
            tool: ProviderId::ClaudeCode,
            plan: None,
            kind: LimitKind::FiveHour,
            measure,
            resets_at,
            captured_at: now,
            status,
            label: None,
        }
    }

    #[test]
    fn window_token_volume_sums_only_in_window_same_tool_rows() {
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let mut codex = record(
            FocusAccessPath::Subscription,
            now - Duration::hours(1),
            "gpt-5.5",
            None,
            TokenType::Input,
            7_777,
        );
        // A Codex row inside Claude's 5h window must NOT count toward Claude's volume.
        codex.x_tool = ProviderId::Codex.to_string();
        let rows = vec![
            claude_row(now - Duration::hours(1), 1_000, None), // inside the 5h window
            claude_row(now - Duration::hours(10), 9_000, None), // older than 5h, inside 7d
            codex,
        ];

        let five = window_token_volume(&rows, ProviderId::ClaudeCode, LimitKind::FiveHour, now);
        assert_eq!(five.total(), 1_000); // only the in-window Claude row

        let weekly = window_token_volume(&rows, ProviderId::ClaudeCode, LimitKind::Weekly, now);
        assert_eq!(weekly.total(), 10_000); // both Claude rows fall inside 7 days
    }

    #[test]
    fn cross_check_demotes_high_but_trivial_claude_reading_to_unverified() {
        // The #31820 guard: a flat 100% with only trivial logged volume is implausible,
        // so it renders Unverified (flagged), never a confident near-max meter.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(
            Some(LimitMeasure::TokenFraction(1.0)),
            LimitStatus::Verified,
            Some(now + Duration::hours(1)),
            now,
        );
        let rows = vec![claude_row(now - Duration::minutes(30), 200, None)]; // below the floor
        let snapshot = snapshot_with_rows(now, rows, vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        assert!(matches!(
            summary.limits[0].availability,
            LimitAvailability::Unverified { .. }
        ));
    }

    #[test]
    fn cross_check_keeps_high_claude_reading_with_real_volume_available() {
        // A genuinely high reading backed by real logged volume is NOT demoted — the
        // guard only flags the implausible "near-max on almost no usage" case.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(
            Some(LimitMeasure::TokenFraction(0.96)),
            LimitStatus::Verified,
            Some(now + Duration::hours(1)),
            now,
        );
        let rows = vec![claude_row(now - Duration::minutes(30), 50_000, None)]; // above the floor
        let snapshot = snapshot_with_rows(now, rows, vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        assert!(matches!(
            summary.limits[0].availability,
            LimitAvailability::Available { .. }
        ));
    }

    #[test]
    fn cross_check_never_applies_to_codex_windows() {
        // Codex windows come from sanctioned rollout logs (§7), so a high-but-trivial
        // Codex reading is trusted on arrival — the cross-check is Claude-only.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let codex = LimitWindow {
            tool: ProviderId::Codex,
            plan: Some("plus".to_string()),
            kind: LimitKind::FiveHour,
            measure: Some(LimitMeasure::TokenFraction(1.0)),
            resets_at: Some(now + Duration::hours(1)),
            captured_at: now,
            status: LimitStatus::Verified,
            label: None,
        };
        // No Codex volume at all — would trip the floor if the guard applied.
        let snapshot = snapshot_with_rows(now, Vec::new(), vec![codex]);

        let summary = now_summary(&snapshot, NowOptions::default());
        assert!(matches!(
            summary.limits[0].availability,
            LimitAvailability::Available { .. }
        ));
    }

    #[test]
    fn unavailable_reading_with_volume_estimates_value() {
        // No trustworthy % (Unavailable) but real local volume → labeled estimate with
        // a priced value, never blank, never a fabricated %.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(None, LimitStatus::Unavailable, None, now);
        let rows = vec![
            claude_row(
                now - Duration::minutes(10),
                1_000,
                Some(Decimal::new(150, 2)),
            ),
            claude_row(
                now - Duration::minutes(20),
                2_000,
                Some(Decimal::new(250, 2)),
            ),
        ];
        let snapshot = snapshot_with_rows(now, rows, vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        match &summary.limits[0].availability {
            LimitAvailability::Estimated {
                volume_tokens,
                estimated_usd,
            } => {
                assert_eq!(*volume_tokens, 3_000);
                assert_eq!(*estimated_usd, Some(Decimal::new(400, 2)));
            }
            other => panic!("expected Estimated, got {other:?}"),
        }
    }

    #[test]
    fn unavailable_reading_with_unpriced_volume_shows_volume_without_price() {
        // Same fallback, but with unpriced rows the $ value is None — show the volume
        // alone, never a guessed price (§6).
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(None, LimitStatus::Unavailable, None, now);
        let rows = vec![claude_row(now - Duration::minutes(10), 4_096, None)];
        let snapshot = snapshot_with_rows(now, rows, vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        match &summary.limits[0].availability {
            LimitAvailability::Estimated {
                volume_tokens,
                estimated_usd,
            } => {
                assert_eq!(*volume_tokens, 4_096);
                assert_eq!(*estimated_usd, None);
            }
            other => panic!("expected Estimated, got {other:?}"),
        }
    }

    #[test]
    fn unavailable_reading_without_volume_is_unavailable() {
        // Nothing to show: no % and no volume → Unavailable, not a zero estimate.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(None, LimitStatus::Unavailable, None, now);
        let snapshot = snapshot_with_rows(now, Vec::new(), vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        assert!(matches!(
            summary.limits[0].availability,
            LimitAvailability::Unavailable { .. }
        ));
    }

    #[test]
    fn stale_verified_reading_ages_out_to_estimate() {
        // A Verified reading whose window already reset ages out against generated_at:
        // with local volume present it becomes a labeled Estimate, never a stale meter.
        let now = utc_datetime(2026, 1, 7, 12, 0, 0);
        let limit = claude_five_hour(
            Some(LimitMeasure::TokenFraction(0.5)),
            LimitStatus::Verified,
            Some(now - Duration::hours(1)), // already reset
            now,
        );
        let rows = vec![claude_row(
            now - Duration::hours(2),
            6_000,
            Some(Decimal::new(900, 2)),
        )];
        let snapshot = snapshot_with_rows(now, rows, vec![limit]);

        let summary = now_summary(&snapshot, NowOptions::default());
        match &summary.limits[0].availability {
            LimitAvailability::Estimated {
                volume_tokens,
                estimated_usd,
            } => {
                assert_eq!(*volume_tokens, 6_000);
                assert_eq!(*estimated_usd, Some(Decimal::new(900, 2)));
            }
            other => panic!("stale Verified should age out to Estimated, got {other:?}"),
        }
    }

    #[test]
    fn snapshot_collection_degrades_provider_errors() {
        let env = HostEnv::new(PathBuf::from("/home/example"), Vec::new(), false);
        let now = timestamp();
        // Cursor plays the "missing" role here: it now has dedicated detect-and-defer
        // semantics (any discovered Cursor → `Detected`), so the generic error path is
        // exercised with Claude instead. Cursor's discovered path is covered by
        // `cursor_detected_status_defers_usage_and_quota`.
        let providers: Vec<Box<dyn Provider>> = vec![
            Box::new(FakeProvider::failing_usage(ProviderId::ClaudeCode)),
            Box::new(FakeProvider::missing(ProviderId::Cursor)),
            Box::new(FakeProvider::available(
                ProviderId::Codex,
                vec![usage_event(
                    ProviderId::Codex,
                    AccessPath::Subscription,
                    now,
                )],
                vec![LimitWindow {
                    tool: ProviderId::Codex,
                    plan: Some("plus".to_string()),
                    kind: LimitKind::FiveHour,
                    measure: Some(LimitMeasure::TokenFraction(0.4)),
                    resets_at: Some(now + Duration::hours(1)),
                    captured_at: now,
                    status: LimitStatus::Verified,
                    label: None,
                }],
            )),
        ];

        let snapshot = match collect_snapshot_from_providers(&env, providers, now) {
            Ok(value) => value,
            Err(err) => panic!("snapshot should collect with non-fatal provider errors: {err}"),
        };

        assert_eq!(snapshot.providers.len(), 3);
        assert!(snapshot.providers.iter().any(|status| {
            status.provider == ProviderId::ClaudeCode
                && status.status == ProviderStatusKind::Partial
        }));
        assert!(snapshot.providers.iter().any(|status| {
            status.provider == ProviderId::Cursor && status.status == ProviderStatusKind::Missing
        }));
        assert!(snapshot.providers.iter().any(|status| {
            status.provider == ProviderId::Codex
                && status.status == ProviderStatusKind::Available
                && status.focus_rows == 3
        }));
        assert_eq!(snapshot.focus_rows.len(), 3);
        assert_eq!(snapshot.limit_windows.len(), 1);

        // The capability is captured for EVERY provider considered — including the
        // missing one (Cursor) and the errored one (Claude) — parallel to `providers`,
        // so the Providers tab always has a descriptor to render. (The `FakeProvider`
        // double declares the conservative all-`Unavailable` descriptor.)
        assert_eq!(snapshot.capabilities.len(), 3);
        for id in [
            ProviderId::ClaudeCode,
            ProviderId::Codex,
            ProviderId::Cursor,
        ] {
            let view = match snapshot
                .capabilities
                .iter()
                .find(|view| view.provider == id)
            {
                Some(view) => view,
                None => panic!("capability for {id} should be captured"),
            };
            assert_eq!(view.api_cost, DataSource::Unavailable);
            assert_eq!(view.auth, AuthMethod::None);
            assert!(view.quota_kinds.is_empty());
        }
    }

    #[test]
    fn cursor_detected_status_defers_usage_and_quota() {
        // The real CursorProvider against the fake `.cursor` fixture tree: the install
        // is detected with its selected model + logged-in flag, but usage and quota are
        // unavailable — no sanctioned source (discovery-gated): zero events, zero limits,
        // zero FOCUS rows.
        let env = HostEnv::new(fixture_path(&["cursor", "home"]), Vec::new(), false);
        let providers: Vec<Box<dyn Provider>> = vec![Box::new(CursorProvider)];

        let snapshot = match collect_snapshot_from_providers(&env, providers, timestamp()) {
            Ok(value) => value,
            Err(err) => panic!("cursor detect-and-defer should collect: {err}"),
        };

        assert_eq!(snapshot.providers.len(), 1);
        let status = &snapshot.providers[0];
        assert_eq!(status.provider, ProviderId::Cursor);
        assert_eq!(status.status, ProviderStatusKind::Detected);
        assert_eq!(status.usage_events, 0);
        assert_eq!(status.focus_rows, 0);
        assert_eq!(status.limit_windows, 0);

        let message = status.message.as_deref().unwrap_or_default();
        for needle in [
            "BETA",
            "composer-2.5",
            "Composer 2.5 Fast",
            "logged in",
            "no sanctioned source",
        ] {
            assert!(
                message.contains(needle),
                "detected message {message:?} should contain {needle:?}"
            );
        }

        assert!(snapshot.focus_rows.is_empty());
        assert!(snapshot.limit_windows.is_empty());
        // Cursor contributes no dollars: there is nothing for cost math to touch.
        assert!(snapshot.usage_events.is_empty());
    }

    fn local_datetime(
        year: i32,
        month: u32,
        day: u32,
        hour: u32,
        minute: u32,
        second: u32,
    ) -> DateTime<Local> {
        match Local.with_ymd_and_hms(year, month, day, hour, minute, second) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(first, _) => first,
            LocalResult::None => {
                panic!("test local timestamp should be valid in the host timezone")
            }
        }
    }

    struct FakeProvider {
        provider: ProviderId,
        discoverable: bool,
        usage_error: bool,
        usage: Vec<UsageEvent>,
        limits: Vec<LimitWindow>,
    }

    impl FakeProvider {
        fn missing(provider: ProviderId) -> Self {
            Self {
                provider,
                discoverable: false,
                usage_error: false,
                usage: Vec::new(),
                limits: Vec::new(),
            }
        }

        fn failing_usage(provider: ProviderId) -> Self {
            Self {
                provider,
                discoverable: true,
                usage_error: true,
                usage: Vec::new(),
                limits: Vec::new(),
            }
        }

        fn available(
            provider: ProviderId,
            usage: Vec<UsageEvent>,
            limits: Vec<LimitWindow>,
        ) -> Self {
            Self {
                provider,
                discoverable: true,
                usage_error: false,
                usage,
                limits,
            }
        }
    }

    impl Provider for FakeProvider {
        fn id(&self) -> ProviderId {
            self.provider
        }

        // A test double declares nothing as available — the honest conservative
        // descriptor (all `Unavailable`, no login, no quota window). Required only
        // to satisfy the new `capability()` trait method (T3); no test reads it.
        fn capability(&self) -> Capability {
            Capability {
                api_cost: DataSource::Unavailable,
                subscription_quota: DataSource::Unavailable,
                model_mix: DataSource::Unavailable,
                auth: AuthMethod::None,
                quota_kinds: &[],
            }
        }

        fn discover(&self, _env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
            if self.discoverable {
                Ok(Some(DataLocation {
                    provider: self.provider,
                    root: PathBuf::from("/fake"),
                    files: vec![PathBuf::from("/fake/data.jsonl")],
                }))
            } else {
                Ok(None)
            }
        }

        fn parse_usage(&self, _loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
            if self.usage_error {
                Err(ProviderError::DataUnavailable {
                    provider: self.provider,
                    message: "synthetic usage failure".to_string(),
                })
            } else {
                Ok(self.usage.clone())
            }
        }

        fn parse_limits(&self, _loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
            Ok(self.limits.clone())
        }
    }

    fn cloud_event(billed_cost: Option<&str>) -> CloudUsageEvent {
        CloudUsageEvent {
            timestamp: timestamp(),
            service_name: "Claude 3.5 Sonnet".to_string(),
            service_provider_name: "Anthropic".to_string(),
            model: Some("claude-3-5-sonnet".to_string()),
            token_count: Some(1_000),
            billed_cost: billed_cost.map(str::to_string),
            ..cloud_event_no_detail()
        }
    }

    /// A `CloudUsageEvent` with no foreign pricing detail (every T4 detail field `None`) —
    /// the base for tests that don't exercise the per-token-rate carry-through.
    fn cloud_event_no_detail() -> CloudUsageEvent {
        CloudUsageEvent {
            timestamp: timestamp(),
            service_name: String::new(),
            service_provider_name: String::new(),
            model: None,
            token_count: None,
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
        }
    }

    #[test]
    fn canonical_tool_event_yields_developer_tool_lane() {
        let event = usage_event(ProviderId::Codex, AccessPath::Subscription, timestamp());
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Tool(event.clone())]) else {
            panic!("tool canonical event should normalize");
        };
        // Same shape the existing dev-tool path produces.
        let Ok(direct) = focus_records_from_usage(&[event]) else {
            panic!("dev-tool path should normalize");
        };
        assert_eq!(rows, direct);
        assert!(!rows.is_empty(), "tool event should emit meter rows");
        assert!(
            rows.iter().all(|row| row.x_lane == "developer_tool"),
            "all tool rows carry the developer_tool lane"
        );
    }

    #[test]
    fn canonical_cloud_event_with_billed_cost_is_source_priced() {
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Cloud(Box::new(
            cloud_event(Some("12.34")),
        ))]) else {
            panic!("cloud canonical event should normalize");
        };
        assert_eq!(rows.len(), 1, "a cloud event is one aggregate row");
        let row = &rows[0];
        assert_eq!(row.x_lane, "cloud_api");
        // The source bill is preserved verbatim, not recomputed.
        let Ok(expected) = Decimal::from_str_exact("12.34") else {
            panic!("decimal literal should parse");
        };
        assert_eq!(row.billed_cost, expected);
        assert_eq!(row.effective_cost, expected);
        assert_eq!(row.list_cost, expected);
        assert_eq!(row.contracted_cost, expected);
        assert_eq!(row.pricing_currency_effective_cost, expected);
        assert!(
            !row.x_estimated,
            "a source-priced cloud row is not estimated"
        );
        assert_eq!(
            row.x_pricing_status, "priced",
            "a source-priced row reads priced (not missing_price), so pricing coverage counts it"
        );
    }

    #[test]
    fn canonical_cloud_event_without_billed_cost_stays_estimated() {
        let Ok(rows) =
            focus_records_from_canonical(&[CanonicalEvent::Cloud(Box::new(cloud_event(None)))])
        else {
            panic!("cloud canonical event should normalize");
        };
        assert_eq!(rows.len(), 1);
        let row = &rows[0];
        assert_eq!(row.x_lane, "cloud_api");
        assert!(
            row.x_estimated,
            "no source bill: the row stays on the estimate path"
        );
    }

    #[test]
    fn canonical_cloud_event_with_bad_cost_errors_without_panicking() {
        let result = focus_records_from_canonical(&[CanonicalEvent::Cloud(Box::new(cloud_event(
            Some("not-a-number"),
        )))]);
        assert!(
            matches!(result, Err(CoreError::Import(_))),
            "a malformed source cost is a CoreError::Import, not a panic: {result:?}"
        );
    }

    #[test]
    fn v12_import_source_priced_row_preserves_cost_and_stamps_version() {
        // A source-priced foreign row (a present billed_cost) keeps its authoritative cost
        // verbatim and is stamped with the input FOCUS version. Use a CATALOG-KNOWN model
        // (claude-sonnet-4-6) with a source bill that does NOT match what the catalog would
        // compute, so a regression that wrongly repriced a source-priced row would change
        // billed_cost away from 0.0123 and fail the assertion below (the reprice guard is
        // real, not vacuous on a catalog-unknown model).
        let event = CloudUsageEvent {
            model: Some("claude-sonnet-4-6".to_string()),
            token_count: Some(1_000_000),
            billed_cost: Some("0.0123".to_string()),
            service_name: "Claude API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("v1.2 import should normalize");
        };
        assert_eq!(rows.len(), 1);
        let row = &rows[0];
        assert_eq!(row.x_lane, "cloud_api");
        let Ok(expected) = Decimal::from_str_exact("0.0123") else {
            panic!("decimal literal should parse");
        };
        assert_eq!(
            row.billed_cost, expected,
            "the source bill is preserved verbatim"
        );
        assert!(!row.x_estimated, "a source-priced row is not an estimate");
        assert_eq!(row.x_pricing_status, "priced");
        assert_eq!(
            row.x_focus_input_version.as_deref(),
            Some("1.2"),
            "every imported row is stamped with its source FOCUS version"
        );
    }

    #[test]
    fn v12_import_source_priced_row_carries_foreign_pricing_detail() {
        // T4 (closes the M1 per-token-rate deferral): a source-priced row whose foreign
        // export carries a SkuPriceId + unit-prices is now FULLY priced — the detail columns
        // are populated from the source verbatim, not left null.
        let event = CloudUsageEvent {
            model: Some("anthropic.claude-sonnet-4-6".to_string()),
            token_count: Some(8_200),
            billed_cost: Some("0.0123".to_string()),
            effective_cost: Some("0.0123".to_string()),
            list_cost: Some("0.0123".to_string()),
            contracted_cost: Some("0.0123".to_string()),
            sku_price_id: Some("aws-sku-claude-sonnet-output".to_string()),
            pricing_category: Some("Standard".to_string()),
            pricing_quantity: Some("8200".to_string()),
            pricing_unit: Some("Tokens".to_string()),
            list_unit_price: Some("0.0000015".to_string()),
            contracted_unit_price: Some("0.0000015".to_string()),
            pricing_currency: Some("USD".to_string()),
            consumed_unit: Some("Tokens".to_string()),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("source-priced import should normalize");
        };
        let row = &rows[0];
        let Ok(cost) = Decimal::from_str_exact("0.0123") else {
            panic!("decimal literal should parse");
        };
        assert_eq!(row.billed_cost, cost, "cost preserved exactly");
        assert!(
            !row.x_estimated,
            "source-priced row is authoritative, not an estimate"
        );
        // The deciding assertions: the foreign per-token rate detail is now carried.
        assert_eq!(
            row.sku_price_id.as_deref(),
            Some("aws-sku-claude-sonnet-output"),
            "the foreign SkuPriceId is carried (was null before T4)"
        );
        assert_eq!(row.pricing_quantity, Some(Decimal::from(8_200)));
        assert_eq!(row.consumed_quantity, Some(Decimal::from(8_200)));
        let Ok(rate) = Decimal::from_str_exact("0.0000015") else {
            panic!("decimal literal should parse");
        };
        assert_eq!(row.list_unit_price, Some(rate));
        assert_eq!(row.contracted_unit_price, Some(rate));
        assert_eq!(row.pricing_currency_list_unit_price, Some(rate));
        assert_eq!(row.pricing_category.as_deref(), Some("Standard"));
        assert_eq!(
            row.pricing_unit.as_deref(),
            Some("tokens"),
            "unit normalized to canonical"
        );
        // A source-authoritative row carries NO catalog provenance stamp (R8 / D1).
        assert!(
            row.x_pricing_snapshot_id.is_none(),
            "an authoritative row is not catalog-estimated, so it carries no snapshot stamp"
        );
    }

    #[test]
    fn v12_import_source_priced_row_without_sku_price_id_keeps_detail_null() {
        // FOCUS conformance: with NO source SkuPriceId, the pricing-detail columns stay null
        // (the rule "detail must be null when SkuPriceId is null"), even though the cost is
        // carried — the M1 behavior, preserved for detail-less exports.
        let event = CloudUsageEvent {
            model: Some("some-model".to_string()),
            token_count: Some(1_000),
            billed_cost: Some("0.50".to_string()),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("import should normalize");
        };
        let row = &rows[0];
        assert!(!row.x_estimated);
        assert!(
            row.sku_price_id.is_none(),
            "no source SkuPriceId → pricing detail stays null (FOCUS rule)"
        );
        assert!(row.pricing_quantity.is_none());
        assert!(row.list_unit_price.is_none());
    }

    #[test]
    fn multi_currency_row_carries_native_currency_and_is_excluded_from_usd_total() {
        // D3: a non-USD cloud bill is carried in its native currency (never relabeled), and
        // is EXCLUDED from the USD totals (no FX) — while total_by_currency surfaces it.
        let usd = CloudUsageEvent {
            model: Some("claude-sonnet-4-6".to_string()),
            token_count: Some(1_000),
            billed_cost: Some("10.00".to_string()),
            billing_currency: Some("USD".to_string()),
            service_name: "Claude API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            ..cloud_event_no_detail()
        };
        let eur = CloudUsageEvent {
            model: Some("mistral-large-latest".to_string()),
            token_count: Some(1_000),
            billed_cost: Some("7.00".to_string()),
            billing_currency: Some("EUR".to_string()),
            service_name: "Mistral API".to_string(),
            service_provider_name: "Mistral".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[usd, eur], &FocusInputVersion::V1_2) else {
            panic!("a mixed-currency import should normalize (no refusal)");
        };
        assert_eq!(rows.len(), 2);
        assert_eq!(rows[0].billing_currency, "USD");
        assert_eq!(
            rows[1].billing_currency, "EUR",
            "EUR carried verbatim, not relabeled"
        );

        // USD-only totals: only the USD row counts; the EUR row is excluded (no FX blend).
        let Ok(ten) = Decimal::from_str_exact("10.00") else {
            panic!("decimal");
        };
        assert_eq!(lane_total_usd(&rows, LedgerLane::CloudApi), ten);
        assert_eq!(grand_total_usd(&rows), ten);

        // ...but the EUR subtotal is surfaced, never silently dropped.
        let by_currency = total_by_currency(&rows);
        let Ok(seven) = Decimal::from_str_exact("7.00") else {
            panic!("decimal");
        };
        assert_eq!(by_currency.get("USD"), Some(&ten));
        assert_eq!(by_currency.get("EUR"), Some(&seven));
        assert_eq!(
            by_currency.len(),
            2,
            "two currencies, two subtotals — never blended"
        );
    }

    #[test]
    fn v12_import_usage_only_litellm_only_model_prices_from_the_long_tail() {
        // T6 (seed 1): a usage-only cloud row whose model is ABSENT from the curated catalog
        // but present in the LiteLLM long tail (e.g. gpt-4o) is repriced from LiteLLM — an
        // estimate, stamped with the litellm snapshot provenance.
        let event = CloudUsageEvent {
            model: Some("gpt-4o".to_string()),
            token_count: Some(1_000_000),
            billed_cost: None,
            service_name: "OpenAI API".to_string(),
            service_provider_name: "OpenAI".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("long-tail import should normalize");
        };
        let row = &rows[0];
        assert!(row.x_estimated, "a catalog-repriced row is an estimate");
        assert_eq!(row.x_pricing_status, "priced");
        // gpt-4o output rate is 10.00 / 1M (litellm) → 1M tokens → $10.00.
        let Ok(ten) = Decimal::from_str_exact("10").map(|d| d.normalize()) else {
            panic!("decimal");
        };
        assert_eq!(
            row.billed_cost.normalize(),
            ten,
            "priced from the litellm output rate"
        );
        assert_eq!(
            row.x_pricing_snapshot_id.as_deref(),
            Some("litellm@2026-06-18#36c8994e"),
            "the row records WHICH snapshot priced it (litellm long tail), not curated"
        );
    }

    #[test]
    fn v12_import_usage_only_bedrock_model_id_prices_from_the_long_tail() {
        // A Bedrock SkuId (carries the `anthropic.` prefix + a date + `-v1:0`) resolves in
        // the litellm tier and reprices — proving the long tail covers AWS Bedrock model ids.
        let event = CloudUsageEvent {
            model: Some("anthropic.claude-3-5-sonnet-20240620-v1:0".to_string()),
            token_count: Some(1_000),
            billed_cost: None,
            service_name: "Amazon Bedrock".to_string(),
            service_provider_name: "AWS".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("bedrock long-tail import should normalize");
        };
        let row = &rows[0];
        assert_eq!(
            row.x_pricing_status, "priced",
            "the Bedrock id resolved in the litellm tier"
        );
        assert!(
            row.billed_cost > Decimal::ZERO,
            "a non-zero estimate was produced"
        );
        assert_eq!(
            row.x_pricing_snapshot_id.as_deref(),
            Some("litellm@2026-06-18#36c8994e")
        );
    }

    #[test]
    fn v12_import_usage_only_unknown_model_stays_unpriced_no_fabrication() {
        // An unknown model (in neither tier) is NOT fabricated a price: cost stays 0, no
        // catalog stamp — honesty over coverage.
        let event = CloudUsageEvent {
            model: Some("totally-made-up-model-xyz".to_string()),
            token_count: Some(1_000),
            billed_cost: None,
            service_name: "Mystery API".to_string(),
            service_provider_name: "Mystery".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("unknown-model import should still normalize");
        };
        let row = &rows[0];
        assert_eq!(row.billed_cost, Decimal::ZERO, "no fabricated price");
        assert_ne!(
            row.x_pricing_status, "priced",
            "an unknown model is not priced"
        );
        assert!(
            row.x_pricing_snapshot_id.is_none(),
            "an unpriced row carries no snapshot stamp"
        );
    }

    #[test]
    fn v12_import_bedrock_row_carries_bounded_inference_profile_id_d4() {
        // D4: a Bedrock cloud row carries the bounded inference-profile id for per-workload
        // attribution (independent of pricing); a non-Bedrock row carries none.
        // A USAGE-ONLY Bedrock row (no billed_cost) — proves the profile id is carried
        // independent of pricing (the id is set before the billed_cost branch).
        let bedrock = CloudUsageEvent {
            model: Some("anthropic.claude-3-5-sonnet-20240620-v1:0".to_string()),
            token_count: Some(1_000),
            billed_cost: None,
            inference_profile_id: Some("aip-0a1b2c3d4e5f6789".to_string()),
            service_name: "Amazon Bedrock".to_string(),
            service_provider_name: "AWS".to_string(),
            ..cloud_event_no_detail()
        };
        let other = CloudUsageEvent {
            model: Some("claude-sonnet-4-6".to_string()),
            token_count: Some(1_000),
            billed_cost: None,
            service_name: "Claude API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[bedrock, other], &FocusInputVersion::V1_2)
        else {
            panic!("import should normalize");
        };
        assert_eq!(
            rows[0].x_inference_profile_id.as_deref(),
            Some("aip-0a1b2c3d4e5f6789"),
            "the bounded Bedrock inference-profile id is carried even on a usage-only row"
        );
        assert!(
            rows[0].x_estimated,
            "the usage-only Bedrock row took the estimate path (id carried independent of pricing)"
        );
        assert!(
            rows[1].x_inference_profile_id.is_none(),
            "a non-Bedrock row carries no inference profile id"
        );
    }

    #[test]
    fn merged_dev_tool_and_cloud_ledger_keeps_lanes_separate() {
        // THE M2 DECIDING TEST (seed 4): one FOCUS ledger built from BOTH real producers —
        // developer-tool logs (focus_records_from_usage) AND an imported synthetic AWS Bedrock
        // FOCUS export (import_focus_csv → focus_records_from_v12_import) — keeps the lanes
        // separate in every total; only grand_total_usd crosses lanes.
        const AWS_CSV: &str = include_str!("../../../fixtures/focus/v1.2/synthetic-aws-v12.csv");

        // Developer-tool lane: a real catalog-priced Claude usage event (USD).
        let dev_event = UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-sonnet-4-6".to_string(),
            timestamp: timestamp(),
            input_tokens: 1_000_000,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Api,
            is_sidechain: false,
        };
        let Ok(dev_rows) = focus_records_from_usage(&[dev_event]) else {
            panic!("dev-tool normalization");
        };
        // Cloud lane: import the synthetic AWS Bedrock FOCUS export (source-priced).
        let Ok(import) = costroid_providers::focus_import::import_focus_csv(AWS_CSV) else {
            panic!("aws import");
        };
        let Ok(cloud_rows) =
            focus_records_from_v12_import(&import.events, &import.detection.version)
        else {
            panic!("cloud bridge");
        };

        // The merged ledger is the natural concatenation — no special merge primitive.
        let mut ledger = dev_rows.clone();
        ledger.extend(cloud_rows.clone());

        assert!(
            dev_rows.iter().all(|r| r.x_lane == "developer_tool"),
            "every dev-tool row is the developer_tool lane"
        );
        assert!(
            !cloud_rows.is_empty() && cloud_rows.iter().all(|r| r.x_lane == "cloud_api"),
            "every imported row is the cloud_api lane"
        );

        // Lane totals independent; the dev-tool total counts ONLY dev-tool rows.
        let dev_total = lane_total_usd(&ledger, LedgerLane::DeveloperTool);
        let cloud_total = lane_total_usd(&ledger, LedgerLane::CloudApi);
        assert!(dev_total > Decimal::ZERO, "dev-tool lane is priced");
        assert!(cloud_total > Decimal::ZERO, "cloud lane is priced");
        assert_eq!(
            dev_total,
            lane_total_usd(&dev_rows, LedgerLane::DeveloperTool),
            "merging cloud rows did NOT move the dev-tool total"
        );
        // Only grand_total_usd crosses the lanes.
        assert_eq!(grand_total_usd(&ledger), dev_total + cloud_total);

        // A dev-tool $-summer (aggregate_rows) over the MERGED ledger equals the same summer
        // over the dev-tool rows alone — the cloud rows never leak into a dev-tool aggregate.
        assert_eq!(
            aggregate_rows(&ledger, GroupBy::Model),
            aggregate_rows(&dev_rows, GroupBy::Model),
            "the dev-tool aggregation ignores cloud_api rows in the merged ledger"
        );

        // The cloud rows carry the M2 attribution + provenance (Bedrock id; source-priced).
        assert!(
            cloud_rows
                .iter()
                .any(|r| r.x_inference_profile_id.as_deref() == Some("aip-0a1b2c3d4e5f6789")),
            "an imported Bedrock row carries its inference-profile id"
        );

        // The merged export is well-formed: exactly one header + one row per ledger entry.
        let Ok(csv) = export_focus_csv(&ledger) else {
            panic!("merged export");
        };
        assert_eq!(
            csv.lines().count(),
            1 + ledger.len(),
            "one header + one row per merged-ledger entry"
        );
    }

    #[test]
    fn v12_import_usage_only_row_is_repriced_from_the_catalog_like_a_local_log() {
        // A usage-only foreign row (no billed_cost) whose model is in the bundled catalog
        // is repriced from the catalog — an ESTIMATE (x_estimated stays true), status
        // "priced", a non-zero cost — exactly like a local tool log.
        let event = CloudUsageEvent {
            model: Some("claude-sonnet-4-6".to_string()),
            token_count: Some(1_000_000),
            billed_cost: None,
            service_name: "Claude API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            ..cloud_event_no_detail()
        };
        let Ok(rows) = focus_records_from_v12_import(&[event], &FocusInputVersion::V1_2) else {
            panic!("usage-only import should normalize");
        };
        let row = &rows[0];
        assert_eq!(row.x_lane, "cloud_api");
        assert!(
            row.x_estimated,
            "a catalog-repriced row is an estimate (your tokens x current prices)"
        );
        assert_eq!(row.x_pricing_status, "priced", "catalog reprice → priced");
        assert!(
            row.billed_cost > Decimal::ZERO,
            "the catalog reprice produced a non-zero estimated cost: {}",
            row.billed_cost
        );
        assert_eq!(row.x_focus_input_version.as_deref(), Some("1.2"));
    }

    #[test]
    fn v12_import_rows_never_inflate_the_developer_tool_total() {
        // Lane separation (T6): imported cloud rows are cloud_api-lane and stay out of the
        // developer-tool dollar total even though they carry a real cost.
        let events = [cloud_event(Some("99.99"))];
        let Ok(rows) = focus_records_from_v12_import(&events, &FocusInputVersion::V1_2) else {
            panic!("import should normalize");
        };
        assert!(rows.iter().all(|row| row.x_lane == "cloud_api"));
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::DeveloperTool),
            Decimal::ZERO,
            "a cloud_api row must not appear in the developer-tool total"
        );
        assert_eq!(
            lane_total_usd(&rows, LedgerLane::CloudApi),
            Decimal::from_str_exact("99.99").unwrap_or(Decimal::ZERO),
            "it appears in the cloud_api total instead"
        );
    }

    #[test]
    fn canonical_local_event_yields_local_inference_lane_with_tokens() {
        let local = LocalRunEvent {
            timestamp: timestamp(),
            model: "llama-3.1-8b".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in: 500,
            tokens_out: 1_200,
            run_seconds: 4.2,
            avg_power_watts: 95.0,
            measurement_mode: "estimated".to_string(),
        };
        let Ok(rows) = focus_records_from_canonical(&[CanonicalEvent::Local(local)]) else {
            panic!("local canonical event should normalize");
        };
        assert_eq!(rows.len(), 1, "a local run is one row");
        let row = &rows[0];
        assert_eq!(row.x_lane, "local_inference");
        // Consumed tokens carried (tokens_out); no energy column exists yet (M3).
        assert_eq!(row.x_consumed_tokens, Decimal::from(1_200_u64));
        assert_eq!(row.x_token_type, "output");
        assert_eq!(row.x_model, "llama-3.1-8b");
    }
}
