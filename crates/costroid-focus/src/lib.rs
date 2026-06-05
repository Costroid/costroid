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
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UnpricedUsage {
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
            x_model: input.model,
            x_token_type: token_type.to_string(),
            x_access_path: input.access_path.as_str().to_string(),
            x_estimated: true,
            x_tool: input.tool,
            x_project: input.project,
            x_pricing_status: PRICING_STATUS_MISSING_PRICE.to_string(),
            x_consumed_tokens: consumed_tokens,
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
    String::from_utf8(bytes).map_err(FocusError::from)
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
            "x_Model,x_TokenType,x_AccessPath,x_Estimated,x_Tool,x_Project,x_PricingStatus,x_ConsumedTokens"
        ));
    }
}
