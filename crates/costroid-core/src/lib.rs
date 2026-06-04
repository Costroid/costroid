//! Costroid data pipeline and aggregation interfaces.

use std::collections::BTreeMap;

use chrono::{DateTime, Datelike, Duration, Local, LocalResult, NaiveDate, TimeZone, Utc};
use costroid_focus::{
    to_csv_string, to_json_string, FocusAccessPath, FocusError, FocusRecord, TokenType,
    UnpricedUsage, DEFAULT_BILLING_CURRENCY, PRICING_CATEGORY_STANDARD,
    PRICING_STATUS_MISSING_PRICE, PRICING_UNIT_TOKENS,
};
use costroid_providers::{
    default_providers, read_cursor_config, AccessPath, CursorConfig, HostEnv, LimitKind,
    LimitWindow, Provider, ProviderId, UsageEvent,
};
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};
use thiserror::Error;

const PRICING_STATUS_PRICED: &str = "priced";
const PRICING_STATUS_UNKNOWN_MODEL: &str = "unknown_model";
const PRICING_SCHEMA_VERSION: &str = "1";
const PRICING_UNIT_1M_TOKENS: &str = "1M_tokens";
const UNKNOWN_GROUP_VALUE: &str = "unknown";
const TOTAL_GROUP_VALUE: &str = "total";

pub fn bundled_pricing_json() -> &'static str {
    // Bundled inside this crate (not the workspace root) so `cargo package`
    // includes it and the crate publishes standalone to crates.io.
    include_str!("../pricing/pricing.v1.json")
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
    let pricing = PricingCatalog::bundled()?;
    let mut records = Vec::new();
    for event in events {
        push_meter_records(event, &pricing, &mut records)?;
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
        .map(|limit| limit_summary(limit, snapshot.generated_at))
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

pub fn trends_summary(snapshot: &EngineSnapshot, options: TrendsOptions) -> TrendsSummary {
    let mut buckets = BTreeMap::<(PeriodRange, CostLane, GroupKey), AggregateTotals>::new();

    for row in &snapshot.focus_rows {
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
        // Cursor is detect-and-defer: it keeps no token usage or quota on disk
        // (both are live server RPCs; ARCHITECTURE.md §4), so it produces zero
        // events/limits and is reported as `Detected` with the selected model,
        // logged-in flag, and the Phase-2 deferral carried in `message`. Every other
        // provider keeps the generic usage/limits-derived status.
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
/// the explicit "usage/quota live (Phase 2)" deferral. Honest about what is locally
/// knowable (presence + model) versus what is not (cost + quota). Never includes the
/// account email/userId — only whether a session exists.
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
        "BETA — {model}, {login}; usage unavailable — live (Phase 2); \
         quota unavailable — live (Phase 2)"
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
            Some(rate) => apply_pricing(&mut row, rate, pricing),
            None if model.is_none() => {
                row.x_pricing_status = PRICING_STATUS_UNKNOWN_MODEL.to_string();
            }
            None => {}
        }

        records.push(row);
    }

    Ok(())
}

fn apply_pricing(row: &mut FocusRecord, rate: &CatalogRate, pricing: &PricingCatalog) {
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
    row.sku_price_id = Some(pricing.sku_price_id(rate));
    row.list_unit_price = Some(per_token);
    row.contracted_unit_price = Some(per_token);
    // PricingCurrency == BillingCurrency for Costroid, so the pricing-currency
    // columns mirror their billing-currency counterparts.
    row.pricing_currency_effective_cost = cost;
    row.pricing_currency_list_unit_price = Some(per_token);
    row.pricing_currency_contracted_unit_price = Some(per_token);
    row.x_pricing_status = PRICING_STATUS_PRICED.to_string();
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
    as_of: String,
    currency: String,
    #[serde(default)]
    models: Vec<PricingModel>,
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
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct CatalogRate {
    provider: String,
    model: String,
    meter: String,
    unit: String,
    price: Decimal,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct PricingCatalog {
    as_of: String,
    currency: String,
    models: BTreeMap<String, PricingModelInfo>,
    rates: BTreeMap<(String, String), CatalogRate>,
}

impl PricingCatalog {
    fn bundled() -> Result<Self, CoreError> {
        Self::from_json(bundled_pricing_json())
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

    /// Resolve a raw log model id to the catalog key whose info/rates apply.
    ///
    /// 1. Exact match wins — preserves prior behavior and lets a curated explicit
    ///    dated entry override the base-alias fallback (the escape hatch when a
    ///    snapshot is ever repriced away from its base).
    /// 2. Else strip a strict date-snapshot suffix and use the bare base **iff that
    ///    base already exists in the catalog** — so we never invent a mapping, and a
    ///    version bump (`claude-opus-4-8`) is never folded onto a different version.
    /// 3. Else `None` (genuinely unknown model → `unknown_model`).
    fn resolve_key<'a>(&'a self, model: &'a str) -> Option<&'a str> {
        if self.models.contains_key(model) {
            return Some(model);
        }
        let base = strip_date_suffix(model)?;
        if self.models.contains_key(base) {
            return Some(base);
        }
        None
    }

    fn sku_price_id(&self, rate: &CatalogRate) -> String {
        // Opaque, stable per-rate identifier. The unit component reflects the
        // FOCUS-facing per-token basis (consistent with PricingUnit / ListUnitPrice),
        // not the catalog's per-1M rate basis — no stale "1M_tokens" in the id.
        format!(
            "{}:{}:{}:{}:{}",
            rate.provider, rate.model, rate.meter, PRICING_UNIT_TOKENS, self.as_of
        )
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

        let mut catalog = Self {
            as_of: table.as_of,
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

fn summarize_rows<'a, I>(rows: I, group_by: GroupBy) -> Vec<CostLaneSummary>
where
    I: IntoIterator<Item = &'a FocusRecord>,
{
    let mut summaries = BTreeMap::<(CostLane, GroupKey), AggregateTotals>::new();
    for row in rows {
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

fn limit_summary(limit: &LimitWindow, generated_at: DateTime<Utc>) -> LimitSummary {
    LimitSummary {
        tool: limit.tool,
        plan: limit.plan.clone(),
        kind: limit.kind,
        label: limit.label.clone(),
        availability: limit_availability(limit, generated_at),
    }
}

fn limit_availability(limit: &LimitWindow, generated_at: DateTime<Utc>) -> LimitAvailability {
    let reset_in_seconds = limit
        .resets_at
        .map(|resets_at| clamp_reset_seconds(resets_at, generated_at));
    let is_stale = limit
        .resets_at
        .map(|resets_at| resets_at < generated_at)
        .unwrap_or(false);

    match (limit.used_fraction, limit.resets_at, is_stale) {
        (Some(used_fraction), Some(resets_at), false) => LimitAvailability::Available {
            used_fraction,
            resets_at,
            reset_in_seconds: reset_in_seconds.unwrap_or(0),
        },
        (None, None, _) => LimitAvailability::Unavailable {
            reason: limit
                .label
                .clone()
                .unwrap_or_else(|| "limit data unavailable from local logs".to_string()),
        },
        _ => LimitAvailability::Partial {
            used_fraction: limit.used_fraction,
            resets_at: limit.resets_at,
            reset_in_seconds,
            reason: if is_stale {
                "data may be stale".to_string()
            } else {
                "limit data incomplete".to_string()
            },
        },
    }
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
}

impl EngineSnapshot {
    fn empty(generated_at: DateTime<Utc>) -> Self {
        Self {
            generated_at,
            usage_events: Vec::new(),
            focus_rows: Vec::new(),
            limit_windows: Vec::new(),
            providers: Vec::new(),
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
    pub availability: LimitAvailability,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LimitAvailability {
    Available {
        used_fraction: f64,
        resets_at: DateTime<Utc>,
        reset_in_seconds: i64,
    },
    Partial {
        used_fraction: Option<f64>,
        resets_at: Option<DateTime<Utc>>,
        reset_in_seconds: Option<i64>,
        reason: String,
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

#[derive(Debug, Error)]
pub enum CoreError {
    #[error("bundled pricing JSON is invalid: {0}")]
    PricingJson(#[from] serde_json::Error),

    #[error("bundled pricing table is invalid: {0}")]
    PricingValidation(String),

    #[error("FOCUS export failed: {0}")]
    Focus(#[from] FocusError),
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{LocalResult, TimeZone, Timelike, Weekday};
    use costroid_focus::{PRICING_CATEGORY_STANDARD, PRICING_STATUS_MISSING_PRICE};
    use costroid_providers::{CursorProvider, DataLocation, ProviderError};
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
        assert_eq!(
            catalog.sku_price_id(rate),
            "openai:gpt-5.5:input:tokens:2026-06-02"
        );
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
                used_fraction: None,
                resets_at: None,
                label: Some("unavailable".to_string()),
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::FiveHour,
                used_fraction: Some(0.5),
                resets_at: Some(now + Duration::minutes(30)),
                label: None,
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                used_fraction: Some(0.6),
                resets_at: None,
                label: None,
            },
            LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                used_fraction: Some(0.7),
                resets_at: Some(now - Duration::minutes(5)),
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
        match &summary.limits[3].availability {
            LimitAvailability::Partial {
                reset_in_seconds,
                reason,
                ..
            } => {
                assert_eq!(*reset_in_seconds, Some(0));
                assert!(reason.contains("stale"));
            }
            _ => panic!("expired reset should be partial stale data"),
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
                    used_fraction: Some(0.4),
                    resets_at: Some(now + Duration::hours(1)),
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
    }

    #[test]
    fn cursor_detected_status_defers_usage_and_quota() {
        // The real CursorProvider against the fake `.cursor` fixture tree: the install
        // is detected with its selected model + logged-in flag, but usage and quota are
        // deferred to Phase 2 (zero events, zero limits, zero FOCUS rows).
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
            "live (Phase 2)",
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
}
