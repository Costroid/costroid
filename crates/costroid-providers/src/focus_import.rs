//! The FOCUS-import seam: read a foreign FOCUS export (CSV or JSON) into Costroid's
//! canonical cloud lane.
//!
//! # Cardinal Rule (R4): metadata only
//!
//! [`RawFocusRow`] — the shape a foreign FOCUS file is parsed into — carries ONLY
//! bounded metadata: costs, a timestamp, provider identity, a SKU/model id, a token
//! count, the billing currency, and the version marker. It deliberately has **no**
//! free-text-capable field: no `ChargeDescription`, `ResourceId`/`ResourceName`,
//! `Tags`/`AllocatedTags`, `SkuPriceDetails` — and obviously no
//! prompt/completion/message/content/text. Any other column present in the source
//! (e.g. AWS Data Exports' `x_ServiceCode` / `x_UsageType`) is **not a field here**, so
//! serde drops it at parse — the importer is *structurally incapable* of carrying
//! provider-specific free text downstream. The R4 structural test (T16) enforces this.
//!
//! # Version isolation
//!
//! All FOCUS-1.2 column semantics live in exactly one place — [`FocusV12Mapping`]. A
//! future v1.4 reader is a sibling [`FocusInputMapping`] impl; the reader/bridge above
//! it never names a version-specific column. [`detect_version`] reads the optional
//! `x_FocusVersion` marker (a real FOCUS export carries its version in the export
//! manifest, not per-row — so an unmarked file defaults to [`FocusInputVersion::V1_2`]
//! with the caveat flagged, never silently).

use chrono::{DateTime, Utc};
use serde::Deserialize;
use thiserror::Error;

use crate::CloudUsageEvent;

/// The FOCUS spec version an imported file declares (or is assumed to be).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FocusInputVersion {
    /// FOCUS 1.2 — the version the M1 bridge imports.
    V1_2,
    /// FOCUS 1.3 — detected, but not an import target (Costroid's *output* is 1.3;
    /// re-importing 1.3 is deferred). Surfaced distinctly so a caller can report it.
    V1_3,
    /// An unrecognized version marker — never imported.
    Unknown(String),
}

impl FocusInputVersion {
    /// The canonical version string (`"1.2"` / `"1.3"` / the raw unknown marker) — what
    /// gets stamped on `x_FocusInputVersion`.
    pub fn as_str(&self) -> &str {
        match self {
            Self::V1_2 => "1.2",
            Self::V1_3 => "1.3",
            Self::Unknown(value) => value.as_str(),
        }
    }
}

/// The outcome of [`detect_version`]: the version plus whether it was **assumed** (no
/// explicit marker found). The caller surfaces the caveat when `assumed_default` is set.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VersionDetection {
    pub version: FocusInputVersion,
    /// `true` when no `x_FocusVersion` marker was present and [`FocusInputVersion::V1_2`]
    /// was assumed by default (a real FOCUS export declares its version in the manifest,
    /// not per-row).
    pub assumed_default: bool,
}

/// A successful import: the canonical events + the detected source version (so the CLI
/// can stamp `x_FocusInputVersion` and surface the assumed-default caveat).
#[derive(Debug, Clone, PartialEq)]
pub struct FocusImport {
    pub events: Vec<CloudUsageEvent>,
    pub detection: VersionDetection,
}

/// A raw FOCUS input row — **metadata only** (R4); see the module docs. Every field is
/// `Option` + `#[serde(default)]` so a missing column (e.g. the absent `x_FocusVersion`
/// on an unmarked file) deserializes cleanly rather than erroring, and unknown source
/// columns (no matching field) are dropped by serde.
#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct RawFocusRow {
    #[serde(rename = "BilledCost", default)]
    pub billed_cost: Option<String>,
    // The separate authoritative cost columns + the per-token pricing detail (M2 T4). All
    // bounded metadata (decimals-as-strings / ids / enum-ish labels), never content — carried
    // so a source-priced import is fully priced. Absent columns deserialize to None.
    #[serde(rename = "EffectiveCost", default)]
    pub effective_cost: Option<String>,
    #[serde(rename = "ListCost", default)]
    pub list_cost: Option<String>,
    #[serde(rename = "ContractedCost", default)]
    pub contracted_cost: Option<String>,
    #[serde(rename = "SkuPriceId", default)]
    pub sku_price_id: Option<String>,
    #[serde(rename = "PricingCategory", default)]
    pub pricing_category: Option<String>,
    #[serde(rename = "PricingQuantity", default)]
    pub pricing_quantity: Option<String>,
    #[serde(rename = "PricingUnit", default)]
    pub pricing_unit: Option<String>,
    #[serde(rename = "ListUnitPrice", default)]
    pub list_unit_price: Option<String>,
    #[serde(rename = "ContractedUnitPrice", default)]
    pub contracted_unit_price: Option<String>,
    #[serde(rename = "PricingCurrency", default)]
    pub pricing_currency: Option<String>,
    #[serde(rename = "ConsumedUnit", default)]
    pub consumed_unit: Option<String>,
    #[serde(rename = "ChargePeriodStart", default)]
    pub charge_period_start: Option<String>,
    #[serde(rename = "BillingCurrency", default)]
    pub billing_currency: Option<String>,
    #[serde(rename = "ServiceName", default)]
    pub service_name: Option<String>,
    #[serde(rename = "ProviderName", default)]
    pub provider_name: Option<String>,
    #[serde(rename = "PublisherName", default)]
    pub publisher_name: Option<String>,
    /// FOCUS 1.2 has no standard model column for AI usage; the model id rides `SkuId`
    /// (a synthetic-fixture convention localized to [`FocusV12Mapping`]).
    #[serde(rename = "SkuId", default)]
    pub sku_id: Option<String>,
    #[serde(rename = "ConsumedQuantity", default)]
    pub consumed_quantity: Option<String>,
    /// Costroid's own detection marker (an `x_` extension), NOT a standard FOCUS column.
    #[serde(rename = "x_FocusVersion", default)]
    pub x_focus_version: Option<String>,
}

/// Errors the FOCUS importer can return. Typed (no `unwrap`/`expect`/`panic!`).
#[derive(Debug, Error)]
pub enum FocusImportError {
    /// CSV parse failure.
    #[error("FOCUS CSV parse error: {0}")]
    Csv(#[from] csv::Error),
    /// JSON parse failure.
    #[error("FOCUS JSON parse error: {0}")]
    Json(#[from] serde_json::Error),
    /// A detected version Costroid does not import (1.3, or an unrecognized marker).
    #[error("unsupported FOCUS input version `{0}` (Costroid imports FOCUS 1.2)")]
    UnsupportedVersion(String),
    /// A row carried a value that could not be mapped (bad timestamp / cost / currency /
    /// quantity). Names the row index + the reason; never panics.
    #[error("FOCUS row {row}: {message}")]
    Row { row: usize, message: String },
}

/// A version-specific column→field mapping. v1.2 is the only impl today; a future v1.4
/// reader slots in here without touching [`import_focus_csv`] / [`import_focus_json`].
pub trait FocusInputMapping {
    /// The FOCUS version this mapping reads.
    fn version(&self) -> FocusInputVersion;
    /// Map one raw row into a canonical [`CloudUsageEvent`] (metadata only). `index` is
    /// the 0-based row position, used only for error messages.
    fn map_row(&self, row: &RawFocusRow, index: usize)
        -> Result<CloudUsageEvent, FocusImportError>;
}

/// The FOCUS **1.2** → canonical mapping. The ONE place v1.2 column semantics live, so
/// truing the synthetic fixtures to a real AWS export's column shape is a one-file change.
pub struct FocusV12Mapping;

impl FocusInputMapping for FocusV12Mapping {
    fn version(&self) -> FocusInputVersion {
        FocusInputVersion::V1_2
    }

    fn map_row(
        &self,
        row: &RawFocusRow,
        index: usize,
    ) -> Result<CloudUsageEvent, FocusImportError> {
        let timestamp = parse_timestamp(row.charge_period_start.as_deref(), index)?;

        // Multi-currency (M2 / D3): carry the bill's native BillingCurrency faithfully —
        // never relabel or auto-convert it. Normalize the CASE to upper (ISO 4217 codes are
        // uppercase) so a `usd`/`Usd` bill is not mistaken for a foreign currency and dropped
        // from the USD totals. The core bridge keeps the row in its native currency and
        // excludes a genuinely-non-USD row from the USD totals (no FX).
        let billing_currency =
            non_empty(row.billing_currency.as_deref()).map(str::to_ascii_uppercase);

        let service_name = non_empty(row.service_name.as_deref())
            .unwrap_or_default()
            .to_string();
        // Provider identity: ProviderName, falling back to PublisherName.
        let service_provider_name = non_empty(row.provider_name.as_deref())
            .or_else(|| non_empty(row.publisher_name.as_deref()))
            .unwrap_or_default()
            .to_string();
        // Model id rides SkuId (see RawFocusRow::sku_id). None when absent.
        let model = non_empty(row.sku_id.as_deref()).map(str::to_string);

        let token_count = match non_empty(row.consumed_quantity.as_deref()) {
            Some(raw) => Some(parse_u64(raw, index)?),
            None => None,
        };

        // The source-authoritative cost, carried verbatim as a decimal string (the core
        // bridge parses it; never as f64 here). Absent/blank → None (a usage-only row the
        // core bridge can re-estimate from the catalog, like a local log).
        let billed_cost = non_empty(row.billed_cost.as_deref()).map(str::to_string);

        // The foreign export's own per-token pricing detail + separate cost columns (T4),
        // carried verbatim as bounded metadata strings; the core bridge parses the decimals.
        let owned = |value: Option<&str>| non_empty(value).map(str::to_string);

        Ok(CloudUsageEvent {
            timestamp,
            service_name,
            service_provider_name,
            model,
            token_count,
            billed_cost,
            effective_cost: owned(row.effective_cost.as_deref()),
            list_cost: owned(row.list_cost.as_deref()),
            contracted_cost: owned(row.contracted_cost.as_deref()),
            sku_price_id: owned(row.sku_price_id.as_deref()),
            pricing_category: owned(row.pricing_category.as_deref()),
            pricing_quantity: owned(row.pricing_quantity.as_deref()),
            pricing_unit: owned(row.pricing_unit.as_deref()),
            list_unit_price: owned(row.list_unit_price.as_deref()),
            contracted_unit_price: owned(row.contracted_unit_price.as_deref()),
            // Currency codes normalized to upper-case (ISO 4217), like billing_currency.
            pricing_currency: non_empty(row.pricing_currency.as_deref())
                .map(str::to_ascii_uppercase),
            consumed_unit: owned(row.consumed_unit.as_deref()),
            billing_currency,
        })
    }
}

/// Detect the FOCUS version of a set of raw rows from the optional `x_FocusVersion`
/// marker (the first non-empty one wins). No marker → assume [`FocusInputVersion::V1_2`]
/// with `assumed_default = true` so the caller records the caveat.
pub fn detect_version(rows: &[RawFocusRow]) -> VersionDetection {
    for row in rows {
        if let Some(marker) = non_empty(row.x_focus_version.as_deref()) {
            let version = match marker {
                "1.2" | "1.2.0" => FocusInputVersion::V1_2,
                "1.3" | "1.3.0" => FocusInputVersion::V1_3,
                other => FocusInputVersion::Unknown(other.to_string()),
            };
            return VersionDetection {
                version,
                assumed_default: false,
            };
        }
    }
    VersionDetection {
        version: FocusInputVersion::V1_2,
        assumed_default: true,
    }
}

/// Import a FOCUS **CSV** export into canonical cloud events. Detects the version, then
/// maps every row through the version's [`FocusInputMapping`]. Unknown source columns
/// are dropped (R4). Returns [`FocusImportError::UnsupportedVersion`] for a non-1.2 file.
pub fn import_focus_csv(data: &str) -> Result<FocusImport, FocusImportError> {
    let mut reader = csv::Reader::from_reader(data.as_bytes());
    let mut rows = Vec::new();
    for result in reader.deserialize::<RawFocusRow>() {
        rows.push(result?);
    }
    map_detected(&rows)
}

/// Import a FOCUS **JSON** export (a bare array of row objects) into canonical cloud
/// events. Same detection + mapping as [`import_focus_csv`].
pub fn import_focus_json(data: &str) -> Result<FocusImport, FocusImportError> {
    let rows: Vec<RawFocusRow> = serde_json::from_str(data)?;
    map_detected(&rows)
}

/// Shared detect-then-map core for both [`import_focus_csv`] and [`import_focus_json`].
fn map_detected(rows: &[RawFocusRow]) -> Result<FocusImport, FocusImportError> {
    let detection = detect_version(rows);
    let mapping: &dyn FocusInputMapping = match detection.version {
        FocusInputVersion::V1_2 => &FocusV12Mapping,
        FocusInputVersion::V1_3 | FocusInputVersion::Unknown(_) => {
            return Err(FocusImportError::UnsupportedVersion(
                detection.version.as_str().to_string(),
            ));
        }
    };
    let mut events = Vec::with_capacity(rows.len());
    for (index, row) in rows.iter().enumerate() {
        events.push(mapping.map_row(row, index)?);
    }
    Ok(FocusImport { events, detection })
}

/// `Some(trimmed)` when the input is present and non-blank, else `None`. Trims so a
/// whitespace-only cell reads as absent.
fn non_empty(value: Option<&str>) -> Option<&str> {
    value.map(str::trim).filter(|trimmed| !trimmed.is_empty())
}

/// Parse a required RFC 3339 `ChargePeriodStart` into UTC, or a typed row error.
fn parse_timestamp(value: Option<&str>, index: usize) -> Result<DateTime<Utc>, FocusImportError> {
    let raw = non_empty(value).ok_or_else(|| FocusImportError::Row {
        row: index,
        message: "missing ChargePeriodStart".to_string(),
    })?;
    DateTime::parse_from_rfc3339(raw)
        .map(|dt| dt.with_timezone(&Utc))
        .map_err(|err| FocusImportError::Row {
            row: index,
            message: format!("unparseable ChargePeriodStart `{raw}`: {err}"),
        })
}

/// Parse a whole-number token count, or a typed row error.
fn parse_u64(value: &str, index: usize) -> Result<u64, FocusImportError> {
    value.parse::<u64>().map_err(|err| FocusImportError::Row {
        row: index,
        message: format!("unparseable ConsumedQuantity `{value}`: {err}"),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const MARKED_CSV: &str = include_str!("../../../fixtures/focus/v1.2/synthetic-v12-marked.csv");
    const UNMARKED_CSV: &str =
        include_str!("../../../fixtures/focus/v1.2/synthetic-v12-unmarked.csv");
    const JSON: &str = include_str!("../../../fixtures/focus/v1.2/synthetic-v12.json");
    const AWS_CSV: &str = include_str!("../../../fixtures/focus/v1.2/synthetic-aws-v12.csv");

    #[test]
    fn detects_v12_from_an_explicit_marker() {
        let Ok(import) = import_focus_csv(MARKED_CSV) else {
            panic!("marked v1.2 CSV should import");
        };
        assert_eq!(import.detection.version, FocusInputVersion::V1_2);
        assert!(
            !import.detection.assumed_default,
            "an explicit marker is not an assumed default"
        );
        assert_eq!(import.events.len(), 2);
    }

    #[test]
    fn defaults_to_v12_with_a_caveat_when_unmarked() {
        let Ok(import) = import_focus_csv(UNMARKED_CSV) else {
            panic!("unmarked CSV should import");
        };
        assert_eq!(import.detection.version, FocusInputVersion::V1_2);
        assert!(
            import.detection.assumed_default,
            "an unmarked file defaults to V1_2 WITH the caveat flagged"
        );
        assert_eq!(import.events.len(), 2);
    }

    #[test]
    fn maps_v12_columns_to_canonical_cloud_events() {
        let Ok(import) = import_focus_csv(MARKED_CSV) else {
            panic!("marked CSV should import");
        };
        let first = &import.events[0];
        assert_eq!(first.service_name, "Claude API");
        assert_eq!(first.service_provider_name, "Anthropic");
        assert_eq!(first.model.as_deref(), Some("claude-sonnet-4-6"));
        assert_eq!(first.token_count, Some(8_200));
        assert_eq!(first.billed_cost.as_deref(), Some("0.0123"));
    }

    #[test]
    fn json_import_matches_the_csv_import() {
        let Ok(csv_import) = import_focus_csv(MARKED_CSV) else {
            panic!("CSV should import");
        };
        let Ok(json_import) = import_focus_json(JSON) else {
            panic!("JSON should import");
        };
        // The JSON fixture mirrors the marked CSV row-for-row → identical canonical events.
        assert_eq!(json_import.events, csv_import.events);
        assert_eq!(json_import.detection.version, FocusInputVersion::V1_2);
    }

    #[test]
    fn drops_provider_specific_columns_r4() {
        // The AWS sample carries x_ServiceCode / x_UsageType (and is unmarked). They are
        // not RawFocusRow fields, so serde drops them at parse — no trace can reach the
        // canonical events.
        let Ok(import) = import_focus_csv(AWS_CSV) else {
            panic!("AWS-shaped CSV should import");
        };
        assert!(import.detection.assumed_default, "AWS sample has no marker");
        assert_eq!(import.events.len(), 2);
        // Structural proof: the canonical events carry no field capable of holding the
        // dropped provider-specific values. Serialize and assert the dropped tokens are
        // absent (a belt-and-braces check on top of the type-level guarantee).
        let Ok(serialized) = serde_json::to_string(&import.events) else {
            panic!("events should serialize");
        };
        assert!(!serialized.contains("ServiceCode"));
        assert!(!serialized.contains("UsageType"));
        assert!(!serialized.contains("BedrockModelUnits"));
        // The model still rode SkuId through.
        assert_eq!(
            import.events[0].model.as_deref(),
            Some("anthropic.claude-sonnet-4-6")
        );
    }

    #[test]
    fn an_unknown_version_marker_is_a_typed_error_not_a_panic() {
        let csv = "BilledCost,ChargePeriodStart,x_FocusVersion\n\
                   0.01,2026-06-15T10:00:00Z,9.9\n";
        match import_focus_csv(csv) {
            Err(FocusImportError::UnsupportedVersion(version)) => assert_eq!(version, "9.9"),
            other => panic!("expected UnsupportedVersion(9.9), got {other:?}"),
        }
    }

    #[test]
    fn a_v13_marker_is_unsupported_for_import() {
        let csv = "BilledCost,ChargePeriodStart,x_FocusVersion\n\
                   0.01,2026-06-15T10:00:00Z,1.3\n";
        // detect_version reports V1_3 distinctly...
        let mut reader = csv::Reader::from_reader(csv.as_bytes());
        let rows: Vec<RawFocusRow> = reader
            .deserialize()
            .collect::<Result<_, _>>()
            .unwrap_or_default();
        assert_eq!(detect_version(&rows).version, FocusInputVersion::V1_3);
        // ...but importing it is refused (Costroid imports 1.2; output IS 1.3).
        match import_focus_csv(csv) {
            Err(FocusImportError::UnsupportedVersion(version)) => assert_eq!(version, "1.3"),
            other => panic!("expected UnsupportedVersion(1.3), got {other:?}"),
        }
    }

    #[test]
    fn a_non_usd_source_is_carried_not_refused() {
        // M2 / D3: a non-USD bill imports (no error) and carries its native BillingCurrency
        // verbatim — never relabeled or auto-converted. The core bridge keeps it in EUR and
        // excludes it from the USD totals.
        let csv = "BilledCost,ChargePeriodStart,BillingCurrency,ConsumedQuantity\n\
                   1.00,2026-06-15T10:00:00Z,EUR,1000\n";
        let Ok(import) = import_focus_csv(csv) else {
            panic!("a non-USD source should now import (D3 multi-currency)");
        };
        assert_eq!(import.events.len(), 1);
        assert_eq!(
            import.events[0].billing_currency.as_deref(),
            Some("EUR"),
            "the native currency is carried verbatim, never relabeled to USD"
        );
    }

    #[test]
    fn a_lowercase_usd_currency_is_normalized_so_it_counts_as_usd() {
        // A casing typo (`usd`) must NOT be mistaken for a foreign currency and dropped from
        // the USD total — the import boundary upper-cases the ISO 4217 code.
        let csv = "BilledCost,ChargePeriodStart,BillingCurrency,PricingCurrency,ConsumedQuantity\n\
                   1.00,2026-06-15T10:00:00Z,usd,Usd,1000\n";
        let Ok(import) = import_focus_csv(csv) else {
            panic!("a lowercase-usd source should import");
        };
        assert_eq!(import.events[0].billing_currency.as_deref(), Some("USD"));
        assert_eq!(import.events[0].pricing_currency.as_deref(), Some("USD"));
    }

    #[test]
    fn a_malformed_timestamp_is_a_typed_row_error() {
        let csv = "BilledCost,ChargePeriodStart\n0.01,not-a-timestamp\n";
        match import_focus_csv(csv) {
            Err(FocusImportError::Row { row, message }) => {
                assert_eq!(row, 0);
                assert!(message.contains("ChargePeriodStart"));
            }
            other => panic!("expected a Row error, got {other:?}"),
        }
    }
}
