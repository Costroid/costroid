//! Provider-facing interfaces for local AI-tool usage data.
//!
//! Milestone 1 defines shared data shapes only. Provider discovery and parsing
//! implementations land in later milestones.

use std::fmt;
use std::path::PathBuf;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum ProviderId {
    ClaudeCode,
    Codex,
    Cursor,
}

impl fmt::Display for ProviderId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let value = match self {
            Self::ClaudeCode => "claude-code",
            Self::Codex => "codex",
            Self::Cursor => "cursor",
        };
        f.write_str(value)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HostEnv {
    pub home_dir: PathBuf,
    pub windows_home_dir: Option<PathBuf>,
    pub is_wsl: bool,
}

impl HostEnv {
    pub fn new(home_dir: PathBuf, windows_home_dir: Option<PathBuf>, is_wsl: bool) -> Self {
        Self {
            home_dir,
            windows_home_dir,
            is_wsl,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DataLocation {
    pub provider: ProviderId,
    pub root: PathBuf,
    pub files: Vec<PathBuf>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AccessPath {
    Api,
    Subscription,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct UsageEvent {
    pub tool: ProviderId,
    pub model: String,
    pub timestamp: DateTime<Utc>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub project: Option<String>,
    pub access_path: AccessPath,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum LimitKind {
    FiveHour,
    Weekly,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LimitWindow {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub used_fraction: Option<f64>,
    pub resets_at: Option<DateTime<Utc>>,
    pub label: Option<String>,
}

#[derive(Debug, Error)]
pub enum ProviderError {
    #[error("{provider}: {message}")]
    DataUnavailable {
        provider: ProviderId,
        message: String,
    },
}

pub trait Provider: Send + Sync {
    fn id(&self) -> ProviderId;

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError>;

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError>;

    fn parse_limits(&self, loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError>;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn provider_ids_match_documented_values() {
        assert_eq!(ProviderId::ClaudeCode.to_string(), "claude-code");
        assert_eq!(ProviderId::Codex.to_string(), "codex");
        assert_eq!(ProviderId::Cursor.to_string(), "cursor");
    }
}
