//! FOCUS-shaped export primitives for Costroid.
//!
//! Milestone 2 intentionally implements Costroid's AI-usage subset as an
//! append-friendly prefix of the FOCUS 1.3 Cost and Usage schema. The output is
//! not yet claimed to be validator-conformant; remaining mandatory columns are
//! added in a later Phase 1 milestone.

use chrono::{DateTime, Datelike, Duration, LocalResult, TimeZone, Utc};
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub const FOCUS_VERSION: &str = "1.3";
pub const DEFAULT_BILLING_CURRENCY: &str = "USD";
pub const CHARGE_CATEGORY_USAGE: &str = "Usage";
pub const CHARGE_FREQUENCY_USAGE_BASED: &str = "Usage-Based";
pub const PRICING_CATEGORY_STANDARD: &str = "Standard";
pub const SERVICE_CATEGORY_AI: &str = "AI and Machine Learning";
pub const PRICING_STATUS_MISSING_PRICE: &str = "missing_price";

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

/// Costroid's current AI-usage subset of the FOCUS 1.3 Cost and Usage schema.
///
/// Field order is intentional: FOCUS columns first in an append-friendly order,
/// custom `x_` columns last. Future full-conformance columns should be added
/// before the `x_` block.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct FocusRecord {
    pub billed_cost: Decimal,
    pub effective_cost: Decimal,
    pub list_cost: Decimal,
    pub contracted_cost: Decimal,
    pub billing_currency: String,

    pub billing_period_start: DateTime<Utc>,
    pub billing_period_end: DateTime<Utc>,
    pub charge_period_start: DateTime<Utc>,
    pub charge_period_end: DateTime<Utc>,

    pub charge_category: String,
    pub charge_class: Option<String>,
    pub charge_description: String,
    pub charge_frequency: String,

    pub service_name: String,
    pub service_category: String,
    pub service_provider_name: String,
    pub host_provider_name: String,
    pub invoice_issuer_name: String,

    pub sku_id: Option<String>,
    pub sku_price_id: Option<String>,
    pub pricing_category: String,
    pub pricing_quantity: Decimal,
    pub pricing_unit: String,
    pub list_unit_price: Option<Decimal>,
    pub contracted_unit_price: Option<Decimal>,

    pub consumed_quantity: Decimal,
    pub consumed_unit: String,

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
}

impl FocusRecord {
    pub fn unpriced_usage(input: UnpricedUsage) -> Result<Self, FocusError> {
        let (billing_period_start, billing_period_end) = billing_period(input.timestamp)?;
        let charge_period_end = input
            .timestamp
            .checked_add_signed(Duration::seconds(1))
            .ok_or(FocusError::InvalidTimestamp)?;
        let token_type = input.token_type.as_str();
        let cost = Decimal::from(0);
        let consumed_quantity = Decimal::from(input.token_count);
        let pricing_quantity = consumed_quantity / Decimal::from(1_000_000_u64);

        Ok(Self {
            billed_cost: cost,
            effective_cost: cost,
            list_cost: cost,
            contracted_cost: cost,
            billing_currency: input.billing_currency,
            billing_period_start,
            billing_period_end,
            charge_period_start: input.timestamp,
            charge_period_end,
            charge_category: CHARGE_CATEGORY_USAGE.to_string(),
            charge_class: None,
            charge_description: format!("{} {} tokens", input.model, token_type),
            charge_frequency: CHARGE_FREQUENCY_USAGE_BASED.to_string(),
            service_name: input.service_name,
            service_category: SERVICE_CATEGORY_AI.to_string(),
            service_provider_name: input.service_provider_name,
            host_provider_name: input.host_provider_name,
            invoice_issuer_name: input.invoice_issuer_name,
            sku_id: Some(format!("{}:{token_type}", input.model)),
            sku_price_id: None,
            pricing_category: PRICING_CATEGORY_STANDARD.to_string(),
            pricing_quantity,
            pricing_unit: "1M tokens".to_string(),
            list_unit_price: None,
            contracted_unit_price: None,
            consumed_quantity,
            consumed_unit: "tokens".to_string(),
            x_model: input.model,
            x_token_type: token_type.to_string(),
            x_access_path: input.access_path.as_str().to_string(),
            x_estimated: true,
            x_tool: input.tool,
            x_project: input.project,
            x_pricing_status: PRICING_STATUS_MISSING_PRICE.to_string(),
        })
    }
}

pub fn to_json_string(rows: Vec<FocusRecord>) -> Result<String, FocusError> {
    let envelope = FocusExportEnvelope::new(rows);
    serde_json::to_string_pretty(&envelope).map_err(FocusError::from)
}

pub fn to_csv_string(rows: &[FocusRecord]) -> Result<String, FocusError> {
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
        assert_eq!(record.pricing_category, PRICING_CATEGORY_STANDARD);
        assert_eq!(record.list_unit_price, None);
        assert_eq!(record.contracted_unit_price, None);
        assert_eq!(record.sku_price_id, None);
        assert_eq!(record.x_pricing_status, PRICING_STATUS_MISSING_PRICE);
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
    fn json_export_uses_wrapper_not_bare_array() {
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
    }

    #[test]
    fn csv_export_has_stable_append_friendly_header() {
        let csv = match to_csv_string(&[record()]) {
            Ok(value) => value,
            Err(err) => panic!("csv should serialize: {err}"),
        };
        let header = match csv.lines().next() {
            Some(value) => value,
            None => panic!("csv should have a header"),
        };

        assert!(header.starts_with("BilledCost,EffectiveCost,ListCost,ContractedCost"));
        assert!(header.contains("ServiceProviderName,HostProviderName,InvoiceIssuerName"));
        assert!(header.ends_with(
            "x_Model,x_TokenType,x_AccessPath,x_Estimated,x_Tool,x_Project,x_PricingStatus"
        ));
        let fields: Vec<&str> = header.split(',').collect();
        assert!(!fields.contains(&"ProviderName"));
        assert!(!fields.contains(&"PublisherName"));
    }
}
