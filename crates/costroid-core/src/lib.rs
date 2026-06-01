//! Costroid engine interfaces.
//!
//! Milestone 1 wires the crate boundaries and bundled pricing asset only. The
//! provider orchestration, cost calculation, and aggregation behavior are
//! intentionally deferred.

use chrono::{DateTime, Utc};
use costroid_providers::{LimitWindow, UsageEvent};
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub fn bundled_pricing_json() -> &'static str {
    include_str!("../../../pricing/pricing.v1.json")
}

pub fn bundled_pricing_value() -> Result<serde_json::Value, CoreError> {
    serde_json::from_str(bundled_pricing_json()).map_err(CoreError::from)
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
}

#[cfg(test)]
mod tests {
    use super::*;

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
}
