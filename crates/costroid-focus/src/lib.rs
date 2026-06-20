//! FOCUS 1.3 Cost and Usage export primitives for Costroid.
//!
//! As of Milestone 6a, `FocusRecord` carries the full FOCUS 1.3 Cost and Usage
//! column set so the official validator's conditional dependency checks resolve.
//! Columns Costroid cannot derive from local data are emitted null where the spec
//! permits; the few that a not-null cascade forces are populated with the
//! spec-correct categorical value or, for billing-source identifiers with no local
//! value, a clearly-non-billing placeholder (documented as a deviation).
//!
//! Numeric columns serialize as genuine numbers in JSON and as decimal-pointed
//! values in CSV (so the validator's DECIMAL/DOUBLE/FLOAT type checks pass even
//! when every value in a column is whole).
//!
//! As of Milestone 6b, pricing is represented per token: `PricingUnit = "tokens"`,
//! `PricingQuantity` is the token count, and the unit-price columns are per-token
//! rates (the per-1M catalog rate ÷ 1_000_000). Cost is unchanged — `cost = tokens
//! × rate` is invariant — only the representation changed. On rows with no priced
//! SKU (`SkuPriceId` null), FOCUS 1.3 requires `ConsumedQuantity` / `PricingQuantity`
//! / `PricingUnit` / `PricingCategory` to be null, so they are; the raw token count
//! still travels on the always-populated `x_ConsumedTokens` custom column for the
//! aggregation engine. One genuine validator-ruleset defect remains documented (the
//! `ListCost`/`ContractedCost` = unit-price × quantity check, which the validator
//! evaluates in zero-tolerance float64 even though Costroid's decimal arithmetic is
//! exact); see `scripts/focus_known_failures.txt`.

use std::cell::Cell;

use chrono::{DateTime, Datelike, Duration, LocalResult, TimeZone, Timelike, Utc};
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize, Serializer};
use serde_json::value::RawValue;
use thiserror::Error;

pub const FOCUS_VERSION: &str = "1.3";
pub const DEFAULT_BILLING_CURRENCY: &str = "USD";
pub const CHARGE_CATEGORY_USAGE: &str = "Usage";
pub const CHARGE_FREQUENCY_USAGE_BASED: &str = "Usage-Based";
pub const PRICING_CATEGORY_STANDARD: &str = "Standard";
/// FOCUS `PricingUnit` / `ConsumedUnit` for per-token AI usage. Singular count
/// unit (no numeric multiplier) so it conforms to the FOCUS UnitFormat.
pub const PRICING_UNIT_TOKENS: &str = "tokens";
pub const SERVICE_CATEGORY_AI: &str = "AI and Machine Learning";
/// Valid FOCUS `ServiceSubcategory` paired with `ServiceCategory = "AI and Machine
/// Learning"`. Costroid's three providers are LLM coding tools, so this is the
/// correct classification — not a deviation.
pub const SERVICE_SUBCATEGORY_GENERATIVE_AI: &str = "Generative AI";
pub const PRICING_STATUS_MISSING_PRICE: &str = "missing_price";

/// `x_AttributionConfidence` on a row whose tool/model/project attribution is trusted —
/// the default for a mainline (non-sidechain) turn.
pub const ATTRIBUTION_CONFIDENT: &str = "confident";
/// `x_AttributionConfidence` on a row Costroid keeps counting but cannot fully trust the
/// attribution of — today, a sub-agent (sidechain) turn (its model/project may be the
/// orchestrator's, not the sub-agent's). Annotated honestly, never dropped.
pub const ATTRIBUTION_UNCERTAIN: &str = "uncertain";
/// `x_CollectorVersion` — the Costroid version that produced a row, stamped on every
/// FOCUS record so a replayed/exported ledger records which normalization logic minted
/// it (token-attribution methodology can shift between versions; see `docs/limitations.md`).
pub const COLLECTOR_VERSION: &str = env!("CARGO_PKG_VERSION");

/// Placeholder `BillingAccountId`. FOCUS requires `BillingAccountId` to be
/// non-null, but Costroid is a local estimator with no billing-account identity.
/// This obviously-non-billing sentinel is a documented deviation; Costroid never
/// fabricates realistic-looking account identifiers.
pub const BILLING_ACCOUNT_ID_LOCAL: &str = "costroid-local-estimate";
/// Placeholder `BillingAccountName`, paired with [`BILLING_ACCOUNT_ID_LOCAL`].
pub const BILLING_ACCOUNT_NAME_LOCAL: &str = "Costroid local estimate";
/// Placeholder `BillingAccountType`. FOCUS forces this non-null whenever
/// `BillingAccountId` is non-null; since our account id is itself a placeholder,
/// the type is too (documented deviation).
pub const BILLING_ACCOUNT_TYPE_LOCAL: &str = "Local estimate";

pub type FocusTimestamp = DateTime<Utc>;

#[derive(Debug, Error)]
pub enum FocusError {
    #[error("invalid timestamp for FOCUS period calculation")]
    InvalidTimestamp,

    #[error("failed to serialize FOCUS JSON: {0}")]
    Json(#[from] serde_json::Error),

    #[error("failed to serialize FOCUS CSV: {0}")]
    Csv(#[from] csv::Error),

    #[error("failed to flush FOCUS CSV: {0}")]
    Io(#[from] std::io::Error),

    #[error("failed to convert FOCUS CSV to UTF-8: {0}")]
    Utf8(#[from] std::string::FromUtf8Error),
}

// --- Numeric serialization mode ---------------------------------------------
//
// FOCUS numeric columns must be real numbers. JSON emits them as unquoted number
// tokens (via `RawValue`); CSV emits them as decimal-pointed strings so the
// validator's column type inference reads DOUBLE rather than INTEGER even when a
// whole column is integer-valued (e.g. all-zero unpriced costs, token counts).
// The two encodings diverge, so a thread-local selects which one the shared
// `serialize_with` hooks produce. `to_json_string` / `to_csv_string` set it.

#[derive(Clone, Copy, PartialEq, Eq)]
enum SerMode {
    Json,
    Csv,
}

thread_local! {
    static SER_MODE: Cell<SerMode> = const { Cell::new(SerMode::Json) };
}

struct SerModeGuard(SerMode);

impl SerModeGuard {
    fn new(mode: SerMode) -> Self {
        SerModeGuard(SER_MODE.with(|m| m.replace(mode)))
    }
}

impl Drop for SerModeGuard {
    fn drop(&mut self) {
        SER_MODE.with(|m| m.set(self.0));
    }
}

/// Render a decimal so it is unambiguously a decimal value: always carries a
/// `.`-separated fractional part. `rust_decimal` never uses scientific notation,
/// so this is safe for both JSON numbers and CSV.
fn decimal_with_point(value: &Decimal) -> String {
    let rendered = value.to_string();
    if rendered.contains('.') {
        rendered
    } else {
        format!("{rendered}.0")
    }
}

fn serialize_decimal<S: Serializer>(value: &Decimal, serializer: S) -> Result<S::Ok, S::Error> {
    match SER_MODE.with(Cell::get) {
        SerMode::Csv => serializer.serialize_str(&decimal_with_point(value)),
        SerMode::Json => RawValue::from_string(decimal_with_point(value))
            .map_err(serde::ser::Error::custom)?
            .serialize(serializer),
    }
}

fn serialize_decimal_opt<S: Serializer>(
    value: &Option<Decimal>,
    serializer: S,
) -> Result<S::Ok, S::Error> {
    match value {
        Some(value) => serialize_decimal(value, serializer),
        None => serializer.serialize_none(),
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FocusExportEnvelope<T> {
    #[serde(rename = "focusVersion")]
    pub focus_version: String,
    pub rows: Vec<T>,
}

impl<T> FocusExportEnvelope<T> {
    pub fn new(rows: Vec<T>) -> Self {
        Self {
            focus_version: FOCUS_VERSION.to_string(),
            rows,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FocusAccessPath {
    Api,
    Subscription,
    Unknown,
}

impl FocusAccessPath {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Api => "api",
            Self::Subscription => "subscription",
            Self::Unknown => "unknown",
        }
    }

    /// The exact inverse of [`as_str`](Self::as_str): parse the canonical string back to
    /// the variant, or `None` for an unrecognized value. The single source of truth for
    /// the string↔enum mapping (the round-trip is asserted in tests), so a replay path
    /// (e.g. `costroid-store`) reconstructs the enum without duplicating the table.
    pub fn from_focus_str(value: &str) -> Option<Self> {
        match value {
            "api" => Some(Self::Api),
            "subscription" => Some(Self::Subscription),
            "unknown" => Some(Self::Unknown),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LedgerLane {
    DeveloperTool,
    CloudApi,
    LocalInference,
}

impl LedgerLane {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::DeveloperTool => "developer_tool",
            Self::CloudApi => "cloud_api",
            Self::LocalInference => "local_inference",
        }
    }

    /// The exact inverse of [`as_str`](Self::as_str): parse the canonical string back to
    /// the variant, or `None` for an unrecognized value. The single source of truth for
    /// the string↔enum mapping (the round-trip is asserted in tests), so a replay path
    /// (e.g. `costroid-store`) reconstructs the lane without duplicating the table.
    pub fn from_focus_str(value: &str) -> Option<Self> {
        match value {
            "developer_tool" => Some(Self::DeveloperTool),
            "cloud_api" => Some(Self::CloudApi),
            "local_inference" => Some(Self::LocalInference),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TokenType {
    Input,
    Output,
    CacheRead,
    CacheWrite,
}

impl TokenType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Input => "input",
            Self::Output => "output",
            Self::CacheRead => "cache_read",
            Self::CacheWrite => "cache_write",
        }
    }

    /// The exact inverse of [`as_str`](Self::as_str): parse the canonical string back to
    /// the variant, or `None` for an unrecognized value. The single source of truth for
    /// the string↔enum mapping (the round-trip is asserted in tests), so a replay path
    /// (e.g. `costroid-store`) reconstructs the token type without duplicating the table.
    pub fn from_focus_str(value: &str) -> Option<Self> {
        match value {
            "input" => Some(Self::Input),
            "output" => Some(Self::Output),
            "cache_read" => Some(Self::CacheRead),
            "cache_write" => Some(Self::CacheWrite),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UnpricedUsage {
    pub lane: LedgerLane,
    pub timestamp: DateTime<Utc>,
    pub tool: String,
    pub model: String,
    pub token_type: TokenType,
    pub token_count: u64,
    pub project: Option<String>,
    pub access_path: FocusAccessPath,
    pub service_name: String,
    pub service_provider_name: String,
    pub host_provider_name: String,
    pub invoice_issuer_name: String,
    pub billing_currency: String,
}

/// A FOCUS 1.3 Cost and Usage charge row.
///
/// Field order is the serialized column order. The full FOCUS 1.3 column set
/// comes first (PascalCase via serde), Costroid's custom `x_` columns last.
/// Numeric columns use [`serialize_decimal`] / [`serialize_decimal_opt`].
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct FocusRecord {
    // Costs (BillingCurrency).
    #[serde(serialize_with = "serialize_decimal")]
    pub billed_cost: Decimal,
    #[serde(serialize_with = "serialize_decimal")]
    pub effective_cost: Decimal,
    #[serde(serialize_with = "serialize_decimal")]
    pub list_cost: Decimal,
    #[serde(serialize_with = "serialize_decimal")]
    pub contracted_cost: Decimal,

    // Billing account (no local billing identity — documented placeholders).
    pub billing_account_id: String,
    pub billing_account_name: String,
    pub billing_account_type: Option<String>,
    pub billing_currency: String,

    // Time.
    pub billing_period_start: DateTime<Utc>,
    pub billing_period_end: DateTime<Utc>,
    pub charge_period_start: DateTime<Utc>,
    pub charge_period_end: DateTime<Utc>,

    // Charge classification.
    pub charge_category: String,
    pub charge_class: Option<String>,
    pub charge_description: String,
    pub charge_frequency: String,

    // Service & provider. ProviderName/PublisherName are deprecated in 1.3 but
    // the validator still requires them present; they mirror the active
    // participating-entity columns.
    pub service_name: String,
    pub service_category: String,
    pub service_subcategory: Option<String>,
    pub service_provider_name: String,
    pub host_provider_name: String,
    pub invoice_issuer_name: String,
    pub provider_name: String,
    pub publisher_name: String,
    pub invoice_id: Option<String>,

    // SKU / pricing.
    pub sku_id: Option<String>,
    pub sku_price_id: Option<String>,
    pub sku_meter: Option<String>,
    pub sku_price_details: Option<String>,
    // PricingCategory / PricingQuantity / PricingUnit are null on rows with no
    // priced SKU (SkuPriceId null) per FOCUS 1.3; populated on priced rows.
    pub pricing_category: Option<String>,
    pub pricing_currency: String,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub pricing_quantity: Option<Decimal>,
    pub pricing_unit: Option<String>,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub list_unit_price: Option<Decimal>,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub contracted_unit_price: Option<Decimal>,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub pricing_currency_list_unit_price: Option<Decimal>,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub pricing_currency_contracted_unit_price: Option<Decimal>,
    #[serde(serialize_with = "serialize_decimal")]
    pub pricing_currency_effective_cost: Decimal,

    // Consumption. ConsumedQuantity is null on rows with no priced SKU
    // (SkuPriceId null) per FOCUS 1.3; the raw count lives on x_ConsumedTokens.
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub consumed_quantity: Option<Decimal>,
    pub consumed_unit: String,

    // FOCUS columns Costroid cannot derive from local logs (emitted null).
    pub commitment_discount_category: Option<String>,
    pub commitment_discount_id: Option<String>,
    pub commitment_discount_name: Option<String>,
    #[serde(serialize_with = "serialize_decimal_opt")]
    pub commitment_discount_quantity: Option<Decimal>,
    pub commitment_discount_status: Option<String>,
    pub commitment_discount_type: Option<String>,
    pub commitment_discount_unit: Option<String>,
    pub capacity_reservation_id: Option<String>,
    pub capacity_reservation_status: Option<String>,
    pub region_id: Option<String>,
    pub region_name: Option<String>,
    pub availability_zone: Option<String>,
    pub resource_id: Option<String>,
    pub resource_name: Option<String>,
    pub resource_type: Option<String>,
    pub sub_account_id: Option<String>,
    pub sub_account_name: Option<String>,
    pub sub_account_type: Option<String>,
    pub tags: Option<String>,
    pub contract_applied: Option<String>,
    pub allocated_method_id: Option<String>,
    pub allocated_method_details: Option<String>,
    pub allocated_resource_id: Option<String>,
    pub allocated_resource_name: Option<String>,
    pub allocated_tags: Option<String>,

    // Custom (x_ prefix per FOCUS).
    #[serde(rename = "x_Lane")]
    pub x_lane: String,
    #[serde(rename = "x_Model")]
    pub x_model: String,
    #[serde(rename = "x_TokenType")]
    pub x_token_type: String,
    #[serde(rename = "x_AccessPath")]
    pub x_access_path: String,
    #[serde(rename = "x_Estimated")]
    pub x_estimated: bool,
    #[serde(rename = "x_Tool")]
    pub x_tool: String,
    #[serde(rename = "x_Project")]
    pub x_project: Option<String>,
    #[serde(rename = "x_PricingStatus")]
    pub x_pricing_status: String,
    /// Raw token count for this meter row, always populated (even on unpriced
    /// rows where `ConsumedQuantity` must be null). The aggregation engine reads
    /// this for token totals so nulling `ConsumedQuantity` never drops usage.
    #[serde(rename = "x_ConsumedTokens", serialize_with = "serialize_decimal")]
    pub x_consumed_tokens: Decimal,
    /// The FOCUS spec version this row was **imported from** (the v1.2-in / v1.3-out
    /// bridge stamps `"1.2"`); `None` on rows Costroid produced directly from local
    /// tool logs / local-inference runs (not imported). Bounded version string — never
    /// content.
    #[serde(rename = "x_FocusInputVersion")]
    pub x_focus_input_version: Option<String>,
    /// `true` when this row came from a sub-agent (sidechain) turn. Costroid keeps
    /// counting sidechain usage; this flags it (its attribution is less certain). A bool
    /// like `x_Estimated` — never content.
    #[serde(rename = "x_Sidechain")]
    pub x_sidechain: bool,
    /// How much to trust this row's tool/model/project attribution —
    /// [`ATTRIBUTION_CONFIDENT`] / [`ATTRIBUTION_UNCERTAIN`]. A flat scalar string like
    /// `x_AccessPath` (never a tagged enum on the wire).
    #[serde(rename = "x_AttributionConfidence")]
    pub x_attribution_confidence: String,
    /// The Costroid version that minted this row ([`COLLECTOR_VERSION`]). Bounded version
    /// string — never content.
    #[serde(rename = "x_CollectorVersion")]
    pub x_collector_version: String,
    /// The pricing snapshot that priced this row, as `"{source}@{as_of}#{hash8}"`
    /// (e.g. `"litellm@2026-06-18#36c8994e"` / `"curated@2026-06-02"`) — the source +
    /// date + content hash R8 requires recording for every comparison. `Some` on a row
    /// Costroid **estimated** from a bundled/override pricing snapshot; `None` on a
    /// source-authoritative (foreign-invoice) row and on as-yet-unpriced rows. A bounded
    /// provenance id — never content.
    #[serde(rename = "x_PricingSnapshotId")]
    pub x_pricing_snapshot_id: Option<String>,
    /// The Amazon Bedrock **Application Inference Profile** identifier a cloud row is
    /// attributed to (M2 / D4) — the bounded SYSTEM id (the inference-profile id / last ARN
    /// segment), enabling per-workload attribution. **Never** the user-chosen profile *name*
    /// or cost-allocation *tags* (those are free text → R4-forbidden). `None` on every
    /// non-Bedrock row. A bounded id — never content.
    #[serde(rename = "x_InferenceProfileId")]
    pub x_inference_profile_id: Option<String>,

    // ---- Local-inference economics (M3 / §6.4) — populated on `local_inference`-lane rows;
    // `None` on developer_tool + cloud_api rows. All bounded numbers / ids / enum-ish labels
    // (R4: never content). The energy figure rides `x_MeasuredWh` in BOTH measured and
    // estimated mode; `x_MeasurementMode` + `x_Estimated` disclose which (R6 honesty).
    /// Energy consumed by the local run, in watt-hours (`avg_power_watts * run_seconds / 3600`).
    /// Bounded number. `None` off the local-inference lane.
    #[serde(rename = "x_MeasuredWh", serialize_with = "serialize_decimal_opt")]
    pub x_measured_wh: Option<Decimal>,
    /// Average power draw over the run, in watts (from the selected `PowerSampler`). Bounded
    /// number. `None` off the local-inference lane.
    #[serde(rename = "x_AvgPowerWatts", serialize_with = "serialize_decimal_opt")]
    pub x_avg_power_watts: Option<Decimal>,
    /// The dated hardware/power profile id the run's assumptions came from (e.g.
    /// `"strix-halo-128gb@2026-06-20"`) — the R8 "stamp the assumption" id. A bounded id —
    /// never content. `None` off the local-inference lane.
    #[serde(rename = "x_HardwareProfile")]
    pub x_hardware_profile: Option<String>,
    /// The amortized hardware cost attributed to the run
    /// (`hardware_price / hardware_lifetime_seconds * run_seconds`), in `BillingCurrency`.
    /// Bounded number. `None` off the local-inference lane.
    #[serde(rename = "x_AmortizedHwCost", serialize_with = "serialize_decimal_opt")]
    pub x_amortized_hw_cost: Option<Decimal>,
    /// The local inference runtime that produced the run — a bounded enum-ish label
    /// (`"ollama"` / `"llama.cpp"`), never content. `None` off the local-inference lane.
    #[serde(rename = "x_RuntimeKind")]
    pub x_runtime_kind: Option<String>,
    /// The benchmark id identifying the fixed prompt-suite + model + quant + runtime flags that
    /// produced the run (reproducibility, R10) — a bounded id, never content. `None` off the
    /// local-inference lane.
    #[serde(rename = "x_BenchmarkId")]
    pub x_benchmark_id: Option<String>,
    /// How the run's power was obtained — the bounded `MeasurementMode` wire value
    /// (`measured_wallmeter` / `measured_sysfs` / `measured_lhm` / `estimated`, §6.4). The
    /// measured-vs-estimated stamp R6/R10 require on every record. `None` off the
    /// local-inference lane.
    #[serde(rename = "x_MeasurementMode")]
    pub x_measurement_mode: Option<String>,
}

impl FocusRecord {
    pub fn unpriced_usage(input: UnpricedUsage) -> Result<Self, FocusError> {
        // Instantaneous transcript turns are point-in-time. FOCUS uses an
        // inclusive start / exclusive end, so end = start + 1s. Truncate to whole
        // seconds (FOCUS DateTimeFormat is second-granular).
        let charge_period_start = input
            .timestamp
            .with_nanosecond(0)
            .unwrap_or(input.timestamp);
        let (billing_period_start, billing_period_end) = billing_period(charge_period_start)?;
        let charge_period_end = charge_period_start
            .checked_add_signed(Duration::seconds(1))
            .ok_or(FocusError::InvalidTimestamp)?;
        let token_type = input.token_type.as_str();
        let cost = Decimal::from(0);
        let consumed_tokens = Decimal::from(input.token_count);

        Ok(Self {
            billed_cost: cost,
            effective_cost: cost,
            list_cost: cost,
            contracted_cost: cost,
            billing_account_id: BILLING_ACCOUNT_ID_LOCAL.to_string(),
            billing_account_name: BILLING_ACCOUNT_NAME_LOCAL.to_string(),
            billing_account_type: Some(BILLING_ACCOUNT_TYPE_LOCAL.to_string()),
            billing_currency: input.billing_currency.clone(),
            billing_period_start,
            billing_period_end,
            charge_period_start,
            charge_period_end,
            charge_category: CHARGE_CATEGORY_USAGE.to_string(),
            charge_class: None,
            charge_description: format!("{} {} tokens", input.model, token_type),
            charge_frequency: CHARGE_FREQUENCY_USAGE_BASED.to_string(),
            service_name: input.service_name,
            service_category: SERVICE_CATEGORY_AI.to_string(),
            service_subcategory: Some(SERVICE_SUBCATEGORY_GENERATIVE_AI.to_string()),
            service_provider_name: input.service_provider_name.clone(),
            host_provider_name: input.host_provider_name,
            invoice_issuer_name: input.invoice_issuer_name.clone(),
            provider_name: input.service_provider_name,
            publisher_name: input.invoice_issuer_name,
            invoice_id: None,
            sku_id: Some(format!("{}:{token_type}", input.model)),
            sku_price_id: None,
            sku_meter: Some(token_type.to_string()),
            sku_price_details: None,
            // No priced SKU yet: FOCUS 1.3 requires PricingCategory / PricingQuantity
            // / PricingUnit / ConsumedQuantity to be null when SkuPriceId is null.
            // `apply_pricing` (costroid-core) populates them when a rate is found.
            //
            // NOTE: the "MUST NOT be null when Usage" sibling rules don't conflict
            // only because Costroid leaves ChargeClass and CommitmentDiscountStatus
            // null — populating either on an unpriced row would reintroduce the
            // conflict. See the unpriced-row convention.
            pricing_category: None,
            pricing_currency: input.billing_currency,
            pricing_quantity: None,
            pricing_unit: None,
            list_unit_price: None,
            contracted_unit_price: None,
            pricing_currency_list_unit_price: None,
            pricing_currency_contracted_unit_price: None,
            pricing_currency_effective_cost: cost,
            consumed_quantity: None,
            consumed_unit: PRICING_UNIT_TOKENS.to_string(),
            commitment_discount_category: None,
            commitment_discount_id: None,
            commitment_discount_name: None,
            commitment_discount_quantity: None,
            commitment_discount_status: None,
            commitment_discount_type: None,
            commitment_discount_unit: None,
            capacity_reservation_id: None,
            capacity_reservation_status: None,
            region_id: None,
            region_name: None,
            availability_zone: None,
            resource_id: None,
            resource_name: None,
            resource_type: None,
            sub_account_id: None,
            sub_account_name: None,
            sub_account_type: None,
            tags: None,
            contract_applied: None,
            allocated_method_id: None,
            allocated_method_details: None,
            allocated_resource_id: None,
            allocated_resource_name: None,
            allocated_tags: None,
            x_lane: input.lane.as_str().to_string(),
            x_model: input.model,
            x_token_type: token_type.to_string(),
            x_access_path: input.access_path.as_str().to_string(),
            x_estimated: true,
            x_tool: input.tool,
            x_project: input.project,
            x_pricing_status: PRICING_STATUS_MISSING_PRICE.to_string(),
            x_consumed_tokens: consumed_tokens,
            // Not an imported row by default — set by the core FOCUS-import bridge only.
            x_focus_input_version: None,
            // Mainline (non-sidechain), confident attribution by default — the dev-tool
            // collector (`push_meter_records`) overrides these for a sidechain turn.
            x_sidechain: false,
            x_attribution_confidence: ATTRIBUTION_CONFIDENT.to_string(),
            x_collector_version: COLLECTOR_VERSION.to_string(),
            // Set by the catalog repricer (apply_pricing / the cloud-import bridge) when
            // this row is estimated from a bundled/override snapshot; stays None on a
            // source-authoritative row and on as-yet-unpriced rows (R8 honesty).
            x_pricing_snapshot_id: None,
            // Set only by the cloud-import bridge for an Amazon Bedrock row carrying an
            // application-inference-profile id; None on every other row.
            x_inference_profile_id: None,
            // Local-inference economics (M3): populated only by the local-run mapping
            // (`local_run_to_focus`, costroid-core) for a `local_inference`-lane row; None on
            // every developer_tool / cloud_api row.
            x_measured_wh: None,
            x_avg_power_watts: None,
            x_hardware_profile: None,
            x_amortized_hw_cost: None,
            x_runtime_kind: None,
            x_benchmark_id: None,
            x_measurement_mode: None,
        })
    }
}

pub fn to_json_string(rows: Vec<FocusRecord>) -> Result<String, FocusError> {
    let _guard = SerModeGuard::new(SerMode::Json);
    let envelope = FocusExportEnvelope::new(rows);
    serde_json::to_string_pretty(&envelope).map_err(FocusError::from)
}

pub fn to_csv_string(rows: &[FocusRecord]) -> Result<String, FocusError> {
    let _guard = SerModeGuard::new(SerMode::Csv);
    let mut writer = csv::Writer::from_writer(Vec::new());
    for row in rows {
        writer.serialize(row)?;
    }
    writer.flush()?;
    let bytes = writer.get_ref().clone();
    let csv = String::from_utf8(bytes).map_err(FocusError::from)?;
    if !rows.is_empty() {
        return Ok(csv);
    }
    // Zero rows: the csv crate emits its header only with the first record, but the
    // export contract (ARCHITECTURE "Export shapes") is "the first row is the exact
    // FOCUS column-name header" — for an empty export too, so consumers always see
    // the schema. Derive the header by serializing a throwaway placeholder row and
    // keeping only its header line — the serde field order stays the single source
    // of truth (no hand-maintained column list to drift).
    let placeholder = FocusRecord::unpriced_usage(UnpricedUsage {
        lane: LedgerLane::DeveloperTool,
        timestamp: DateTime::UNIX_EPOCH,
        tool: String::new(),
        model: String::new(),
        token_type: TokenType::Input,
        token_count: 0,
        project: None,
        access_path: FocusAccessPath::Unknown,
        service_name: String::new(),
        service_provider_name: String::new(),
        host_provider_name: String::new(),
        invoice_issuer_name: String::new(),
        billing_currency: String::new(),
    })?;
    let mut writer = csv::Writer::from_writer(Vec::new());
    writer.serialize(&placeholder)?;
    writer.flush()?;
    let bytes = writer.get_ref().clone();
    let with_row = String::from_utf8(bytes).map_err(FocusError::from)?;
    let header = with_row.lines().next().unwrap_or_default();
    Ok(format!("{header}\n"))
}

fn billing_period(timestamp: DateTime<Utc>) -> Result<(DateTime<Utc>, DateTime<Utc>), FocusError> {
    let start = utc_datetime(timestamp.year(), timestamp.month(), 1)?;
    let (next_year, next_month) = if timestamp.month() == 12 {
        (timestamp.year() + 1, 1)
    } else {
        (timestamp.year(), timestamp.month() + 1)
    };
    let end = utc_datetime(next_year, next_month, 1)?;
    Ok((start, end))
}

fn utc_datetime(year: i32, month: u32, day: u32) -> Result<DateTime<Utc>, FocusError> {
    match Utc.with_ymd_and_hms(year, month, day, 0, 0, 0) {
        LocalResult::Single(value) => Ok(value),
        LocalResult::Ambiguous(_, _) | LocalResult::None => Err(FocusError::InvalidTimestamp),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::LocalResult;

    fn timestamp() -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(2026, 1, 15, 12, 34, 56) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        }
    }

    fn record() -> FocusRecord {
        let input = UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: timestamp(),
            tool: "codex".to_string(),
            model: "example-model".to_string(),
            token_type: TokenType::Input,
            token_count: 1_500,
            project: Some("/work/project".to_string()),
            access_path: FocusAccessPath::Subscription,
            service_name: "Codex".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };
        match FocusRecord::unpriced_usage(input) {
            Ok(value) => value,
            Err(err) => panic!("record should build: {err}"),
        }
    }

    #[test]
    fn focus_enum_str_round_trips_for_every_variant() {
        // The single source of truth for the string↔enum mapping: `from_focus_str` is the
        // exact inverse of `as_str` for EVERY variant, so a replay path (costroid-store)
        // reconstructs the enum faithfully. An unknown string yields None (never a default).
        for lane in [
            LedgerLane::DeveloperTool,
            LedgerLane::CloudApi,
            LedgerLane::LocalInference,
        ] {
            assert_eq!(LedgerLane::from_focus_str(lane.as_str()), Some(lane));
        }
        assert_eq!(LedgerLane::from_focus_str("not_a_lane"), None);

        for token in [
            TokenType::Input,
            TokenType::Output,
            TokenType::CacheRead,
            TokenType::CacheWrite,
        ] {
            assert_eq!(TokenType::from_focus_str(token.as_str()), Some(token));
        }
        assert_eq!(TokenType::from_focus_str("not_a_token"), None);

        for path in [
            FocusAccessPath::Api,
            FocusAccessPath::Subscription,
            FocusAccessPath::Unknown,
        ] {
            assert_eq!(FocusAccessPath::from_focus_str(path.as_str()), Some(path));
        }
        assert_eq!(FocusAccessPath::from_focus_str("not_a_path"), None);
    }

    #[test]
    fn export_envelope_uses_canonical_focus_version() {
        let envelope = FocusExportEnvelope::<()>::new(Vec::new());

        assert_eq!(envelope.focus_version, FOCUS_VERSION);
        assert!(envelope.rows.is_empty());
    }

    #[test]
    fn unpriced_usage_has_required_cost_and_pricing_markers() {
        let record = record();

        assert_eq!(record.billed_cost, Decimal::from(0));
        assert_eq!(record.effective_cost, Decimal::from(0));
        assert_eq!(record.list_cost, Decimal::from(0));
        assert_eq!(record.contracted_cost, Decimal::from(0));
        // No priced SKU: FOCUS 1.3 requires these null when SkuPriceId is null.
        assert_eq!(record.sku_price_id, None);
        assert_eq!(record.pricing_category, None);
        assert_eq!(record.pricing_quantity, None);
        assert_eq!(record.pricing_unit, None);
        assert_eq!(record.consumed_quantity, None);
        assert_eq!(record.list_unit_price, None);
        assert_eq!(record.contracted_unit_price, None);
        assert_eq!(record.pricing_currency_list_unit_price, None);
        // The raw token count still travels for the aggregation engine.
        assert_eq!(record.x_consumed_tokens, Decimal::from(1_500));
        assert_eq!(record.x_pricing_status, PRICING_STATUS_MISSING_PRICE);
    }

    #[test]
    fn unpriced_usage_populates_mandatory_focus_columns() {
        let record = record();

        // Billing identity has no honest local value: documented placeholders.
        assert_eq!(record.billing_account_id, BILLING_ACCOUNT_ID_LOCAL);
        assert_eq!(record.billing_account_name, BILLING_ACCOUNT_NAME_LOCAL);
        assert_eq!(
            record.billing_account_type.as_deref(),
            Some(BILLING_ACCOUNT_TYPE_LOCAL)
        );
        // Deprecated participating-entity columns mirror the active ones.
        assert_eq!(record.provider_name, "OpenAI");
        assert_eq!(record.publisher_name, "OpenAI");
        // Correct categorical classification (not a deviation).
        assert_eq!(
            record.service_subcategory.as_deref(),
            Some(SERVICE_SUBCATEGORY_GENERATIVE_AI)
        );
        // SkuMeter accompanies the SkuId; pricing currency mirrors billing currency.
        assert_eq!(record.sku_meter.as_deref(), Some("input"));
        assert_eq!(record.pricing_currency, DEFAULT_BILLING_CURRENCY);
        assert_eq!(record.pricing_currency_effective_cost, Decimal::from(0));
        // Columns with no local value stay null.
        assert_eq!(record.region_id, None);
        assert_eq!(record.commitment_discount_id, None);
        assert_eq!(record.tags, None);
    }

    #[test]
    fn unpriced_usage_maps_time_columns() {
        let record = record();

        assert_eq!(record.charge_period_start, timestamp());
        assert_eq!(record.charge_period_end, timestamp() + Duration::seconds(1));
        assert_eq!(
            record.billing_period_start.to_rfc3339(),
            "2026-01-01T00:00:00+00:00"
        );
        assert_eq!(
            record.billing_period_end.to_rfc3339(),
            "2026-02-01T00:00:00+00:00"
        );
    }

    #[test]
    fn charge_period_start_is_truncated_to_whole_seconds() {
        let mut input = UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: timestamp(),
            tool: "codex".to_string(),
            model: "m".to_string(),
            token_type: TokenType::Input,
            token_count: 10,
            project: None,
            access_path: FocusAccessPath::Api,
            service_name: "s".to_string(),
            service_provider_name: "p".to_string(),
            host_provider_name: "p".to_string(),
            invoice_issuer_name: "p".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };
        input.timestamp = match timestamp().with_nanosecond(123_456_789) {
            Some(value) => value,
            None => panic!("nanosecond should be valid"),
        };

        let record = match FocusRecord::unpriced_usage(input) {
            Ok(value) => value,
            Err(err) => panic!("record should build: {err}"),
        };

        assert_eq!(record.charge_period_start.nanosecond(), 0);
        assert_eq!(
            record.charge_period_end,
            record.charge_period_start + Duration::seconds(1)
        );
    }

    #[test]
    fn json_export_emits_numbers_not_quoted_decimals() {
        let json = match to_json_string(vec![record()]) {
            Ok(value) => value,
            Err(err) => panic!("json should serialize: {err}"),
        };
        let value: serde_json::Value = match serde_json::from_str(&json) {
            Ok(value) => value,
            Err(err) => panic!("json should parse: {err}"),
        };

        assert_eq!(value["focusVersion"], FOCUS_VERSION);
        assert!(value["rows"].is_array());
        let row = &value["rows"][0];
        // Cost columns are JSON numbers, not quoted strings.
        assert!(row["BilledCost"].is_number(), "BilledCost must be a number");
        // Unpriced row: pricing/consumed quantity columns and unit price are null.
        assert!(row["ListUnitPrice"].is_null());
        assert!(row["PricingQuantity"].is_null());
        assert!(row["ConsumedQuantity"].is_null());
        assert!(row["PricingUnit"].is_null());
        assert!(row["PricingCategory"].is_null());
        // The raw token count is carried as a number on x_ConsumedTokens (1500).
        assert!(
            row["x_ConsumedTokens"].is_number(),
            "x_ConsumedTokens must be a number"
        );
        assert_eq!(row["x_ConsumedTokens"].as_f64(), Some(1500.0));
    }

    #[test]
    fn csv_export_renders_numerics_with_decimal_point() {
        let csv = match to_csv_string(&[record()]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        let header = match csv.lines().next() {
            Some(value) => value,
            None => panic!("csv should have a header"),
        };
        let data = match csv.lines().nth(1) {
            Some(value) => value,
            None => panic!("csv should have a data row"),
        };
        let columns: Vec<&str> = header.split(',').collect();
        let values: Vec<&str> = data.split(',').collect();
        let field = |name: &str| -> &str {
            match columns.iter().position(|c| *c == name) {
                Some(index) => values[index],
                None => panic!("column {name} should exist"),
            }
        };

        // Whole-valued numeric columns still carry a decimal point so the
        // validator infers a decimal/float type, not integer.
        assert_eq!(field("BilledCost"), "0.0");
        assert_eq!(field("x_ConsumedTokens"), "1500.0");
        // Null option columns are empty fields (unpriced row).
        assert_eq!(field("ListUnitPrice"), "");
        assert_eq!(field("ConsumedQuantity"), "");
        assert_eq!(field("PricingQuantity"), "");
        assert_eq!(field("PricingUnit"), "");
        assert_eq!(field("PricingCategory"), "");
    }

    #[test]
    fn priced_shape_serializes_token_unit_and_per_token_price() {
        // Simulate the shape `apply_pricing` (costroid-core) produces: token-count
        // PricingQuantity, "tokens" unit, and a tiny per-token unit price.
        let mut record = record();
        record.pricing_unit = Some(PRICING_UNIT_TOKENS.to_string());
        record.pricing_category = Some(PRICING_CATEGORY_STANDARD.to_string());
        record.pricing_quantity = Some(Decimal::from(1_500));
        record.consumed_quantity = Some(Decimal::from(1_500));
        // 0.30 per 1M tokens -> 0.0000003 per token (a tiny decimal).
        record.list_unit_price = Some(Decimal::new(3, 7));

        let csv = match to_csv_string(&[record]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        let header = match csv.lines().next() {
            Some(value) => value,
            None => panic!("csv should have a header"),
        };
        let data = match csv.lines().nth(1) {
            Some(value) => value,
            None => panic!("csv should have a data row"),
        };
        let columns: Vec<&str> = header.split(',').collect();
        let values: Vec<&str> = data.split(',').collect();
        let field = |name: &str| -> &str {
            match columns.iter().position(|c| *c == name) {
                Some(index) => values[index],
                None => panic!("column {name} should exist"),
            }
        };

        assert_eq!(field("PricingUnit"), "tokens");
        assert_eq!(field("PricingCategory"), "Standard");
        // Token-count quantities serialize with a decimal point (validator type check).
        assert_eq!(field("PricingQuantity"), "1500.0");
        assert_eq!(field("ConsumedQuantity"), "1500.0");
        // Tiny per-token price renders plainly (no scientific notation).
        assert_eq!(field("ListUnitPrice"), "0.0000003");
    }

    #[test]
    fn csv_header_carries_full_focus_column_set_then_custom_columns() {
        let csv = match to_csv_string(&[record()]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        let header = match csv.lines().next() {
            Some(value) => value,
            None => panic!("csv should have a header"),
        };
        let fields: Vec<&str> = header.split(',').collect();

        assert!(header.starts_with("BilledCost,EffectiveCost,ListCost,ContractedCost"));
        for required in [
            "BillingAccountId",
            "BillingAccountName",
            "BillingAccountType",
            "ProviderName",
            "PublisherName",
            "ServiceSubcategory",
            "SkuMeter",
            "PricingCurrency",
        ] {
            assert!(fields.contains(&required), "missing column {required}");
        }
        assert!(header.ends_with(
            "x_Lane,x_Model,x_TokenType,x_AccessPath,x_Estimated,x_Tool,x_Project,x_PricingStatus,x_ConsumedTokens,x_FocusInputVersion,x_Sidechain,x_AttributionConfidence,x_CollectorVersion,x_PricingSnapshotId,x_InferenceProfileId,x_MeasuredWh,x_AvgPowerWatts,x_HardwareProfile,x_AmortizedHwCost,x_RuntimeKind,x_BenchmarkId,x_MeasurementMode"
        ));
    }

    /// D1 / R8: the combined pricing-provenance stamp serializes the value verbatim when
    /// present (an estimated row) and an empty cell when absent (a source-authoritative /
    /// unpriced row) — so a comparison's source+date+hash is always recorded or visibly
    /// absent, never silently wrong.
    #[test]
    fn x_pricing_snapshot_id_serializes_some_and_none() {
        let mut rec = record();
        assert!(
            rec.x_pricing_snapshot_id.is_none(),
            "a Costroid-produced (unpriced) row carries no snapshot stamp by default"
        );
        let Ok(csv) = to_csv_string(std::slice::from_ref(&rec)) else {
            panic!("csv export should succeed");
        };
        let mut lines = csv.lines();
        let (Some(header_line), Some(row_line)) = (lines.next(), lines.next()) else {
            panic!("expected a header + one data row");
        };
        let header: Vec<&str> = header_line.split(',').collect();
        let row: Vec<&str> = row_line.split(',').collect();
        let Some(idx) = header.iter().position(|c| *c == "x_PricingSnapshotId") else {
            panic!("x_PricingSnapshotId column should be present");
        };
        assert_eq!(
            header.len(),
            row.len(),
            "one cell per column (fixture has no embedded comma)"
        );
        assert_eq!(row[idx], "", "None snapshot id is an empty cell");

        rec.x_pricing_snapshot_id = Some("litellm@2026-06-18#36c8994e".to_string());
        let Ok(csv) = to_csv_string(std::slice::from_ref(&rec)) else {
            panic!("csv export should succeed");
        };
        let Some(row_line) = csv.lines().nth(1) else {
            panic!("expected a data row");
        };
        let row: Vec<&str> = row_line.split(',').collect();
        assert_eq!(
            row[idx], "litellm@2026-06-18#36c8994e",
            "estimated row carries the stamp verbatim"
        );
    }

    /// M3 T2 / §6.4: the 7 local-inference economics columns are empty on a non-local row and
    /// carry their bounded values verbatim on a `local_inference` row. Proves the schema is
    /// present, defaults null (so developer_tool + cloud_api rows are unaffected), and round-trips.
    #[test]
    fn local_inference_columns_are_null_off_lane_and_populated_on_a_local_row() {
        let cols = [
            "x_MeasuredWh",
            "x_AvgPowerWatts",
            "x_HardwareProfile",
            "x_AmortizedHwCost",
            "x_RuntimeKind",
            "x_BenchmarkId",
            "x_MeasurementMode",
        ];
        // A default (developer_tool) row leaves every local column an empty cell.
        let rec = record();
        let Ok(csv) = to_csv_string(std::slice::from_ref(&rec)) else {
            panic!("csv export should succeed");
        };
        let mut lines = csv.lines();
        let (Some(header_line), Some(row_line)) = (lines.next(), lines.next()) else {
            panic!("expected a header + one data row");
        };
        let header: Vec<&str> = header_line.split(',').collect();
        let row: Vec<&str> = row_line.split(',').collect();
        assert_eq!(header.len(), row.len(), "one cell per column");
        for col in cols {
            let Some(idx) = header.iter().position(|c| *c == col) else {
                panic!("local column {col} should be present");
            };
            assert_eq!(row[idx], "", "{col} is empty on a non-local row");
        }

        // A populated local row carries each value verbatim.
        let mut local = record();
        local.x_lane = "local_inference".to_string();
        local.x_measured_wh = Some(Decimal::from_str_exact("0.0044").unwrap_or(Decimal::ZERO));
        local.x_avg_power_watts = Some(Decimal::from_str_exact("160.0").unwrap_or(Decimal::ZERO));
        local.x_hardware_profile = Some("strix-halo-128gb@2026-06-20".to_string());
        local.x_amortized_hw_cost =
            Some(Decimal::from_str_exact("0.0021").unwrap_or(Decimal::ZERO));
        local.x_runtime_kind = Some("ollama".to_string());
        local.x_benchmark_id = Some("gemma4-coding-v1".to_string());
        local.x_measurement_mode = Some("measured_wallmeter".to_string());
        let Ok(csv) = to_csv_string(std::slice::from_ref(&local)) else {
            panic!("csv export should succeed");
        };
        let Some(row_line) = csv.lines().nth(1) else {
            panic!("expected a data row");
        };
        let row: Vec<&str> = row_line.split(',').collect();
        let cell = |name: &str| {
            let Some(idx) = header.iter().position(|c| *c == name) else {
                panic!("column {name} should be present");
            };
            row[idx]
        };
        assert_eq!(cell("x_MeasuredWh"), "0.0044");
        assert_eq!(cell("x_AvgPowerWatts"), "160.0");
        assert_eq!(cell("x_HardwareProfile"), "strix-halo-128gb@2026-06-20");
        assert_eq!(cell("x_AmortizedHwCost"), "0.0021");
        assert_eq!(cell("x_RuntimeKind"), "ollama");
        assert_eq!(cell("x_BenchmarkId"), "gemma4-coding-v1");
        assert_eq!(cell("x_MeasurementMode"), "measured_wallmeter");
    }

    /// R4 — the Cardinal Rule, as a COMPILE-TIME forcing function (T16).
    ///
    /// Destructures EVERY `FocusRecord` field with NO `..` rest-pattern. Adding any field
    /// to `FocusRecord` makes this test fail to COMPILE (`E0027` — pattern does not mention
    /// field) until the new field is named here AND consciously classified — turning "did
    /// someone add a content column?" from a silent risk into a forced compile-time review.
    ///
    /// The type-safe-by-construction fields (decimals, timestamps, bools, bounded
    /// enum/identifier strings) are discarded with `: _`. The free-text-CAPABLE FOCUS
    /// columns — the ones a *foreign* import could carry prose in — are bound and asserted
    /// to be either the **derived** form (`charge_description`) or **None** on a
    /// Costroid-produced row: Costroid never populates them with content, and `FocusRecord`
    /// is built ONLY via `unpriced_usage` (no `Default`, no public literal), so this single
    /// representative row proves the property for every produced row.
    #[test]
    fn r4_focus_record_is_field_exhaustive_and_holds_no_content() {
        let rec = record();
        let FocusRecord {
            // Costs / quantities / timestamps / bool — structurally text-incapable.
            billed_cost: _,
            effective_cost: _,
            list_cost: _,
            contracted_cost: _,
            // Billing account — documented bounded placeholders.
            billing_account_id: _,
            billing_account_name: _,
            billing_account_type: _,
            billing_currency: _,
            billing_period_start: _,
            billing_period_end: _,
            charge_period_start: _,
            charge_period_end: _,
            // Charge classification — bounded consts, EXCEPT charge_description (asserted
            // to be the derived "{model} {token_type} tokens" form below).
            charge_category: _,
            charge_class: _,
            charge_description,
            charge_frequency: _,
            // Service & provider — bounded provider/service identifiers.
            service_name: _,
            service_category: _,
            service_subcategory: _,
            service_provider_name: _,
            host_provider_name: _,
            invoice_issuer_name: _,
            provider_name: _,
            publisher_name: _,
            invoice_id: _,
            // SKU / pricing — bounded ids + decimals, EXCEPT sku_price_details (free-text-
            // capable in foreign FOCUS; asserted None — Costroid never sets it).
            sku_id: _,
            sku_price_id: _,
            sku_meter: _,
            sku_price_details,
            pricing_category: _,
            pricing_currency: _,
            pricing_quantity: _,
            pricing_unit: _,
            list_unit_price: _,
            contracted_unit_price: _,
            pricing_currency_list_unit_price: _,
            pricing_currency_contracted_unit_price: _,
            pricing_currency_effective_cost: _,
            consumed_quantity: _,
            consumed_unit: _,
            // Commitment-discount / reservation / region / sub-account — null on Costroid
            // rows; bounded id/enum even when a foreign import populates them.
            commitment_discount_category: _,
            commitment_discount_id: _,
            commitment_discount_name: _,
            commitment_discount_quantity: _,
            commitment_discount_status: _,
            commitment_discount_type: _,
            commitment_discount_unit: _,
            capacity_reservation_id: _,
            capacity_reservation_status: _,
            region_id: _,
            region_name: _,
            availability_zone: _,
            // Resource / tag / allocation columns — the free-text-CAPABLE set. Asserted
            // None: Costroid never carries a resource description, tag map, or allocation
            // detail (where foreign prose could hide).
            resource_id,
            resource_name,
            resource_type,
            sub_account_id: _,
            sub_account_name: _,
            sub_account_type: _,
            tags,
            contract_applied: _,
            allocated_method_id: _,
            allocated_method_details,
            allocated_resource_id: _,
            allocated_resource_name,
            allocated_tags,
            // Custom x_ taxonomy — bounded enum/flag/id/version/count, never content.
            x_lane: _,
            x_model: _,
            x_token_type: _,
            x_access_path: _,
            x_estimated: _,
            x_tool: _,
            x_project: _,
            x_pricing_status: _,
            x_consumed_tokens: _,
            x_focus_input_version: _,
            x_sidechain: _,
            x_attribution_confidence: _,
            x_collector_version: _,
            x_pricing_snapshot_id: _,
            x_inference_profile_id: _,
            // Local-inference economics (M3) — bounded numbers / ids / enum-ish labels, never
            // content. A new local column lands here and is consciously classified (R4).
            x_measured_wh: _,
            x_avg_power_watts: _,
            x_hardware_profile: _,
            x_amortized_hw_cost: _,
            x_runtime_kind: _,
            x_benchmark_id: _,
            x_measurement_mode: _,
        } = rec;

        // The one description-named column is always the DERIVED form — never user content.
        assert_eq!(charge_description, "example-model input tokens");
        // Every free-text-capable FOCUS column is unpopulated on a Costroid-produced row.
        assert!(
            sku_price_details.is_none(),
            "sku_price_details must stay null"
        );
        assert!(resource_id.is_none(), "resource_id must stay null");
        assert!(resource_name.is_none(), "resource_name must stay null");
        assert!(resource_type.is_none(), "resource_type must stay null");
        assert!(tags.is_none(), "tags must stay null");
        assert!(allocated_tags.is_none(), "allocated_tags must stay null");
        assert!(
            allocated_method_details.is_none(),
            "allocated_method_details must stay null"
        );
        assert!(
            allocated_resource_name.is_none(),
            "allocated_resource_name must stay null"
        );
    }

    /// R4 export-surface guard: no EXPORTED column name carries a content-bearing token.
    /// (`ChargeDescription` is the FOCUS-required column whose value the test above pins to
    /// the derived form, so the name token "description" is intentionally not scanned here.)
    #[test]
    fn r4_no_exported_column_name_is_content_bearing() {
        let csv = match to_csv_string(&[record()]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        let header = match csv.lines().next() {
            Some(value) => value,
            None => panic!("csv should have a header"),
        };
        for column in header.split(',') {
            let lower = column.to_lowercase();
            for forbidden in ["prompt", "completion", "message", "content", "text"] {
                assert!(
                    !lower.contains(forbidden),
                    "R4: exported column `{column}` carries content-bearing token `{forbidden}`"
                );
            }
        }
    }

    #[test]
    fn zero_row_csv_export_still_emits_the_header() {
        // The documented contract (ARCHITECTURE "Export shapes"): the first row is the
        // exact FOCUS column-name header — even for an empty export, so consumers
        // always see the schema rather than an empty file.
        let empty = match to_csv_string(&[]) {
            Ok(value) => value,
            Err(err) => panic!("empty csv should serialize: {err}"),
        };
        assert_eq!(empty.lines().count(), 1, "header line only: {empty}");
        assert!(empty.ends_with('\n'));
        // Byte-identical to the header of a populated export (single source of truth).
        let populated = match to_csv_string(&[record()]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        assert_eq!(empty.lines().next(), populated.lines().next());
    }

    #[test]
    fn lane_is_serialized_on_x_lane_column() {
        let base = UnpricedUsage {
            lane: LedgerLane::DeveloperTool,
            timestamp: timestamp(),
            tool: "codex".to_string(),
            model: "example-model".to_string(),
            token_type: TokenType::Input,
            token_count: 1_500,
            project: Some("/work/project".to_string()),
            access_path: FocusAccessPath::Api,
            service_name: "Codex".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };

        let dev_input = base.clone();
        let Ok(dev) = FocusRecord::unpriced_usage(dev_input) else {
            panic!("developer-tool record should build");
        };
        assert_eq!(dev.x_lane, "developer_tool");

        let cloud_input = UnpricedUsage {
            lane: LedgerLane::CloudApi,
            ..base
        };
        let Ok(cloud) = FocusRecord::unpriced_usage(cloud_input) else {
            panic!("cloud-api record should build");
        };
        assert_eq!(cloud.x_lane, "cloud_api");
    }
}
