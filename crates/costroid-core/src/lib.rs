//! Costroid data pipeline interfaces.
//!
//! Milestone 2 provides a non-aggregating path from local provider logs to
//! FOCUS-shaped export rows. Trend aggregation and UI-facing summaries are
//! intentionally deferred.

use chrono::{DateTime, Utc};
use costroid_focus::{
    to_csv_string, to_json_string, FocusAccessPath, FocusError, FocusRecord, TokenType,
    UnpricedUsage, DEFAULT_BILLING_CURRENCY,
};
use costroid_providers::{
    default_providers, AccessPath, HostEnv, LimitWindow, ProviderId, UsageEvent,
};
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub fn bundled_pricing_json() -> &'static str {
    include_str!("../../../pricing/pricing.v1.json")
}

pub fn bundled_pricing_value() -> Result<serde_json::Value, CoreError> {
    serde_json::from_str(bundled_pricing_json()).map_err(CoreError::from)
}

pub fn local_snapshot(env: &HostEnv) -> Snapshot {
    let mut usage_events = Vec::new();
    let mut limit_windows = Vec::new();

    for provider in default_providers() {
        let location = match provider.discover(env) {
            Ok(Some(location)) => location,
            Ok(None) | Err(_) => continue,
        };
        if let Ok(mut usage) = provider.parse_usage(&location) {
            usage_events.append(&mut usage);
        }
        if let Ok(mut limits) = provider.parse_limits(&location) {
            limit_windows.append(&mut limits);
        }
    }

    Snapshot {
        generated_at: Utc::now(),
        usage_events,
        limit_windows,
    }
}

pub fn focus_records_from_usage(events: &[UsageEvent]) -> Result<Vec<FocusRecord>, CoreError> {
    let mut records = Vec::new();
    for event in events {
        push_meter_records(event, &mut records)?;
    }
    Ok(records)
}

pub fn focus_records_from_local_logs(env: &HostEnv) -> Result<Vec<FocusRecord>, CoreError> {
    let snapshot = local_snapshot(env);
    focus_records_from_usage(&snapshot.usage_events)
}

pub fn export_focus_json(rows: Vec<FocusRecord>) -> Result<String, CoreError> {
    to_json_string(rows).map_err(CoreError::from)
}

pub fn export_focus_csv(rows: &[FocusRecord]) -> Result<String, CoreError> {
    to_csv_string(rows).map_err(CoreError::from)
}

fn push_meter_records(event: &UsageEvent, records: &mut Vec<FocusRecord>) -> Result<(), CoreError> {
    let meters = [
        (TokenType::Input, event.input_tokens),
        (TokenType::Output, event.output_tokens),
        (TokenType::CacheRead, event.cache_read_tokens),
        (TokenType::CacheWrite, event.cache_write_tokens),
    ];

    for (token_type, token_count) in meters {
        if token_count == 0 {
            continue;
        }
        records.push(FocusRecord::unpriced_usage(UnpricedUsage {
            timestamp: event.timestamp,
            tool: event.tool.to_string(),
            model: event.model.clone(),
            token_type,
            token_count,
            project: event.project.clone(),
            access_path: focus_access_path(event.access_path),
            service_name: service_name(event.tool).to_string(),
            service_provider_name: vendor_name(event.tool).to_string(),
            host_provider_name: vendor_name(event.tool).to_string(),
            invoice_issuer_name: vendor_name(event.tool).to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        })?);
    }

    Ok(())
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

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Period {
    Day,
    Week,
    Month,
    Year,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum GroupBy {
    Model,
    App,
    Total,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Snapshot {
    pub generated_at: DateTime<Utc>,
    pub usage_events: Vec<UsageEvent>,
    pub limit_windows: Vec<LimitWindow>,
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

#[derive(Debug, Error)]
pub enum CoreError {
    #[error("bundled pricing JSON is invalid: {0}")]
    PricingJson(#[from] serde_json::Error),

    #[error("FOCUS export failed: {0}")]
    Focus(#[from] FocusError),
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{LocalResult, TimeZone};
    use costroid_focus::{PRICING_CATEGORY_STANDARD, PRICING_STATUS_MISSING_PRICE};

    fn timestamp() -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(2026, 1, 1, 10, 0, 0) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        }
    }

    #[test]
    fn bundled_pricing_placeholder_is_valid_json() {
        assert!(bundled_pricing_value().is_ok());
    }

    #[test]
    fn default_options_match_now_screen_defaults() {
        let options = EngineOptions::default();

        assert_eq!(options.period, Period::Week);
        assert_eq!(options.group_by, GroupBy::Model);
    }

    #[test]
    fn usage_events_convert_to_one_record_per_nonzero_meter() {
        let event = UsageEvent {
            tool: ProviderId::Codex,
            model: "example-model".to_string(),
            timestamp: timestamp(),
            input_tokens: 10,
            output_tokens: 20,
            cache_read_tokens: 30,
            cache_write_tokens: 0,
            project: Some("/work/project".to_string()),
            access_path: AccessPath::Subscription,
        };
        let rows = match focus_records_from_usage(&[event]) {
            Ok(value) => value,
            Err(err) => panic!("conversion should succeed: {err}"),
        };

        assert_eq!(rows.len(), 3);
        assert!(rows.iter().all(|row| row.x_estimated));
        assert!(rows
            .iter()
            .all(|row| row.pricing_category == PRICING_CATEGORY_STANDARD));
        assert!(rows
            .iter()
            .all(|row| row.x_pricing_status == PRICING_STATUS_MISSING_PRICE));
    }

    #[test]
    fn export_helpers_emit_json_and_csv() {
        let rows = match focus_records_from_usage(&[UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "example-model".to_string(),
            timestamp: timestamp(),
            input_tokens: 1,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Unknown,
        }]) {
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
}
