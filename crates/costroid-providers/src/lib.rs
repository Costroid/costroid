//! Provider-facing interfaces and local parsers for AI-tool usage data.

use std::collections::BTreeSet;
use std::env;
use std::fmt;
use std::fs::{self, File};
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};

use chrono::{DateTime, LocalResult, TimeZone, Utc};
use serde::{Deserialize, Serialize};
use serde_json::Value;
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

    pub fn detect() -> Self {
        let home_dir = env::var_os("HOME")
            .map(PathBuf::from)
            .or_else(|| env::var_os("USERPROFILE").map(PathBuf::from))
            .unwrap_or_else(|| PathBuf::from("."));
        let is_wsl = fs::read_to_string("/proc/sys/kernel/osrelease")
            .map(|value| {
                let value = value.to_ascii_lowercase();
                value.contains("microsoft") || value.contains("wsl")
            })
            .unwrap_or(false);
        let windows_home_dir = detect_windows_home(is_wsl);

        Self::new(home_dir, windows_home_dir, is_wsl)
    }

    pub fn claude_roots(&self) -> Vec<PathBuf> {
        let mut roots = Vec::new();
        roots.extend(claude_config_dir_roots());
        roots.push(self.home_dir.join(".config").join("claude"));
        roots.push(self.home_dir.join(".claude"));
        if let Some(windows_home) = &self.windows_home_dir {
            roots.push(windows_home.join(".config").join("claude"));
            roots.push(windows_home.join(".claude"));
        }
        dedupe_paths(roots)
    }

    pub fn codex_roots(&self) -> Vec<PathBuf> {
        let mut roots = vec![self.home_dir.join(".codex")];
        if let Some(windows_home) = &self.windows_home_dir {
            roots.push(windows_home.join(".codex"));
        }
        dedupe_paths(roots)
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
    Unknown,
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

    #[error("{provider}: failed to read {path}: {source}")]
    Io {
        provider: ProviderId,
        path: PathBuf,
        source: std::io::Error,
    },
}

pub trait Provider: Send + Sync {
    fn id(&self) -> ProviderId;

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError>;

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError>;

    fn parse_limits(&self, loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError>;
}

#[derive(Debug, Default)]
pub struct ClaudeCodeProvider;

impl Provider for ClaudeCodeProvider {
    fn id(&self) -> ProviderId {
        ProviderId::ClaudeCode
    }

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        for root in env.claude_roots() {
            let files = collect_jsonl_files(ProviderId::ClaudeCode, &root.join("projects"))?;
            if !files.is_empty() {
                return Ok(Some(DataLocation {
                    provider: ProviderId::ClaudeCode,
                    root,
                    files,
                }));
            }
        }
        Ok(None)
    }

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
        let access_path = claude_access_path(&loc.root);
        let mut events = Vec::new();
        for file in &loc.files {
            for value in read_jsonl_values(ProviderId::ClaudeCode, file)? {
                if let Some(event) = parse_claude_usage(&value, access_path) {
                    events.push(event);
                }
            }
        }
        Ok(events)
    }

    fn parse_limits(&self, _loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
        Ok(vec![
            unavailable_limit(ProviderId::ClaudeCode, LimitKind::FiveHour),
            unavailable_limit(ProviderId::ClaudeCode, LimitKind::Weekly),
        ])
    }
}

#[derive(Debug, Default)]
pub struct CodexProvider;

impl Provider for CodexProvider {
    fn id(&self) -> ProviderId {
        ProviderId::Codex
    }

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        for root in env.codex_roots() {
            let files = collect_jsonl_files(ProviderId::Codex, &root.join("sessions"))?;
            if !files.is_empty() {
                return Ok(Some(DataLocation {
                    provider: ProviderId::Codex,
                    root,
                    files,
                }));
            }
        }
        Ok(None)
    }

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
        let has_subscription_limits = codex_has_rate_limits(loc)?;
        let access_path = if has_subscription_limits {
            AccessPath::Subscription
        } else {
            AccessPath::Unknown
        };
        let mut events = Vec::new();
        for file in &loc.files {
            events.extend(parse_codex_file(file, access_path)?.usage_events);
        }
        Ok(events)
    }

    fn parse_limits(&self, loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
        let mut primary = None;
        let mut secondary = None;
        for file in &loc.files {
            let parsed = parse_codex_file(file, AccessPath::Unknown)?;
            primary = choose_limit(primary, parsed.primary_limit);
            secondary = choose_limit(secondary, parsed.secondary_limit);
        }
        let limits = vec![
            primary.unwrap_or_else(|| unavailable_limit(ProviderId::Codex, LimitKind::FiveHour)),
            secondary.unwrap_or_else(|| unavailable_limit(ProviderId::Codex, LimitKind::Weekly)),
        ];
        Ok(limits)
    }
}

#[derive(Debug, Default)]
pub struct CursorProvider;

impl Provider for CursorProvider {
    fn id(&self) -> ProviderId {
        ProviderId::Cursor
    }

    fn discover(&self, _env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        Ok(None)
    }

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
        let mut events = Vec::new();
        for file in &loc.files {
            let contents = read_to_string(ProviderId::Cursor, file)?;
            let value: Value = match serde_json::from_str(&contents) {
                Ok(value) => value,
                Err(_) => continue,
            };
            if let Some(items) = value.get("usage_events").and_then(Value::as_array) {
                for item in items {
                    if let Some(event) = parse_cursor_usage(item) {
                        events.push(event);
                    }
                }
            }
        }
        Ok(events)
    }

    fn parse_limits(&self, _loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
        Ok(vec![unavailable_limit(
            ProviderId::Cursor,
            LimitKind::Weekly,
        )])
    }
}

fn choose_limit(current: Option<LimitWindow>, next: Option<LimitWindow>) -> Option<LimitWindow> {
    match (current, next) {
        (None, value) => value,
        (Some(_), Some(next)) if limit_has_data(&next) => Some(next),
        (Some(current), Some(_)) => Some(current),
        (Some(current), None) => Some(current),
    }
}

fn limit_has_data(limit: &LimitWindow) -> bool {
    limit.used_fraction.is_some() || limit.resets_at.is_some()
}

pub fn default_providers() -> Vec<Box<dyn Provider>> {
    vec![
        Box::new(ClaudeCodeProvider),
        Box::new(CodexProvider),
        Box::new(CursorProvider),
    ]
}

fn detect_windows_home(is_wsl: bool) -> Option<PathBuf> {
    if let Some(profile) = env::var_os("USERPROFILE") {
        let path = windows_profile_to_wsl_path(&PathBuf::from(profile));
        if path.is_some() {
            return path;
        }
    }
    if !is_wsl {
        return None;
    }
    env::var_os("USER").map(|user| PathBuf::from("/mnt/c/Users").join(user))
}

fn windows_profile_to_wsl_path(path: &Path) -> Option<PathBuf> {
    let raw = path.to_string_lossy();
    let bytes = raw.as_bytes();
    if bytes.len() < 3 || bytes[1] != b':' {
        return None;
    }
    let drive = (bytes[0] as char).to_ascii_lowercase();
    let rest = raw[2..].replace('\\', "/");
    let rest = rest.trim_start_matches('/');
    Some(PathBuf::from(format!("/mnt/{drive}/{rest}")))
}

fn claude_config_dir_roots() -> Vec<PathBuf> {
    env::var_os("CLAUDE_CONFIG_DIR")
        .map(|value| {
            value
                .to_string_lossy()
                .split(',')
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .map(PathBuf::from)
                .collect()
        })
        .unwrap_or_default()
}

fn dedupe_paths(paths: Vec<PathBuf>) -> Vec<PathBuf> {
    let mut seen = BTreeSet::new();
    let mut deduped = Vec::new();
    for path in paths {
        if seen.insert(path.clone()) {
            deduped.push(path);
        }
    }
    deduped
}

fn collect_jsonl_files(provider: ProviderId, root: &Path) -> Result<Vec<PathBuf>, ProviderError> {
    let mut files = Vec::new();
    if !root.exists() {
        return Ok(files);
    }
    collect_jsonl_files_inner(provider, root, &mut files)?;
    files.sort();
    Ok(files)
}

fn collect_jsonl_files_inner(
    provider: ProviderId,
    root: &Path,
    files: &mut Vec<PathBuf>,
) -> Result<(), ProviderError> {
    let entries = fs::read_dir(root).map_err(|source| ProviderError::Io {
        provider,
        path: root.to_path_buf(),
        source,
    })?;
    for entry in entries {
        let entry = entry.map_err(|source| ProviderError::Io {
            provider,
            path: root.to_path_buf(),
            source,
        })?;
        let path = entry.path();
        let file_type = entry.file_type().map_err(|source| ProviderError::Io {
            provider,
            path: path.clone(),
            source,
        })?;
        if file_type.is_dir() {
            collect_jsonl_files_inner(provider, &path, files)?;
        } else if file_type.is_file() && path.extension().is_some_and(|ext| ext == "jsonl") {
            files.push(path);
        }
    }
    Ok(())
}

fn read_jsonl_values(provider: ProviderId, path: &Path) -> Result<Vec<Value>, ProviderError> {
    let file = File::open(path).map_err(|source| ProviderError::Io {
        provider,
        path: path.to_path_buf(),
        source,
    })?;
    let reader = BufReader::new(file);
    let mut values = Vec::new();
    for line in reader.lines() {
        let line = line.map_err(|source| ProviderError::Io {
            provider,
            path: path.to_path_buf(),
            source,
        })?;
        if line.trim().is_empty() {
            continue;
        }
        if let Ok(value) = serde_json::from_str::<Value>(&line) {
            values.push(value);
        }
    }
    Ok(values)
}

fn read_to_string(provider: ProviderId, path: &Path) -> Result<String, ProviderError> {
    fs::read_to_string(path).map_err(|source| ProviderError::Io {
        provider,
        path: path.to_path_buf(),
        source,
    })
}

fn parse_claude_usage(value: &Value, access_path: AccessPath) -> Option<UsageEvent> {
    if value.get("type").and_then(Value::as_str) != Some("assistant") {
        return None;
    }
    if value.pointer("/message/role").and_then(Value::as_str) != Some("assistant") {
        return None;
    }
    let usage = value.pointer("/message/usage")?;
    let model = value.pointer("/message/model").and_then(Value::as_str)?;
    let timestamp = parse_rfc3339(value.get("timestamp").and_then(Value::as_str)?)?;
    let event = UsageEvent {
        tool: ProviderId::ClaudeCode,
        model: model.to_string(),
        timestamp,
        input_tokens: number_u64(usage.get("input_tokens")),
        output_tokens: number_u64(usage.get("output_tokens")),
        cache_read_tokens: number_u64(usage.get("cache_read_input_tokens")),
        cache_write_tokens: number_u64(usage.get("cache_creation_input_tokens")),
        project: value
            .get("cwd")
            .and_then(Value::as_str)
            .map(ToString::to_string),
        access_path,
    };
    has_any_tokens(&event).then_some(event)
}

fn claude_access_path(root: &Path) -> AccessPath {
    if env::var_os("ANTHROPIC_API_KEY").is_some() {
        return AccessPath::Api;
    }
    if root.join(".credentials.json").exists() || root.join("credentials.json").exists() {
        return AccessPath::Subscription;
    }
    AccessPath::Unknown
}

#[derive(Debug, Default)]
struct ParsedCodexFile {
    usage_events: Vec<UsageEvent>,
    primary_limit: Option<LimitWindow>,
    secondary_limit: Option<LimitWindow>,
}

fn parse_codex_file(
    path: &Path,
    access_path: AccessPath,
) -> Result<ParsedCodexFile, ProviderError> {
    let mut parsed = ParsedCodexFile::default();
    let mut current_model = None;
    let mut current_cwd = None;

    for value in read_jsonl_values(ProviderId::Codex, path)? {
        update_codex_context(&value, &mut current_model, &mut current_cwd);
        if let Some((primary, secondary)) = parse_codex_limits(&value) {
            parsed.primary_limit = choose_limit(parsed.primary_limit, Some(primary));
            parsed.secondary_limit = choose_limit(parsed.secondary_limit, Some(secondary));
        }
        if let Some(event) = parse_codex_usage(&value, access_path, &current_model, &current_cwd) {
            parsed.usage_events.push(event);
        }
    }

    Ok(parsed)
}

fn update_codex_context(
    value: &Value,
    current_model: &mut Option<String>,
    current_cwd: &mut Option<String>,
) {
    if let Some(model) = value
        .pointer("/payload/collaboration_mode/settings/model")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
    {
        *current_model = Some(model.to_string());
    } else if let Some(model) = value
        .pointer("/payload/model")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
    {
        *current_model = Some(model.to_string());
    }

    if let Some(cwd) = value
        .pointer("/payload/cwd")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
    {
        *current_cwd = Some(cwd.to_string());
    }
}

fn parse_codex_usage(
    value: &Value,
    access_path: AccessPath,
    current_model: &Option<String>,
    current_cwd: &Option<String>,
) -> Option<UsageEvent> {
    let usage = value.pointer("/payload/info/last_token_usage")?;
    let timestamp = value
        .get("timestamp")
        .and_then(Value::as_str)
        .and_then(parse_rfc3339)
        .or_else(|| {
            value
                .pointer("/payload/timestamp")
                .and_then(Value::as_str)
                .and_then(parse_rfc3339)
        })?;
    let event = UsageEvent {
        tool: ProviderId::Codex,
        model: current_model
            .clone()
            .unwrap_or_else(|| "unknown".to_string()),
        timestamp,
        input_tokens: number_u64(usage.get("input_tokens")),
        output_tokens: number_u64(usage.get("output_tokens")),
        cache_read_tokens: number_u64(usage.get("cached_input_tokens")),
        cache_write_tokens: 0,
        project: current_cwd.clone(),
        access_path,
    };
    has_any_tokens(&event).then_some(event)
}

fn parse_codex_limits(value: &Value) -> Option<(LimitWindow, LimitWindow)> {
    let rate_limits = value.pointer("/payload/rate_limits")?;
    let primary = parse_codex_limit(
        rate_limits.get("primary"),
        LimitKind::FiveHour,
        rate_limits.get("plan_type").and_then(Value::as_str),
    )?;
    let secondary = parse_codex_limit(
        rate_limits.get("secondary"),
        LimitKind::Weekly,
        rate_limits.get("plan_type").and_then(Value::as_str),
    )?;
    Some((primary, secondary))
}

fn parse_codex_limit(
    value: Option<&Value>,
    kind: LimitKind,
    plan: Option<&str>,
) -> Option<LimitWindow> {
    let value = value?;
    let used_fraction = value
        .get("used_percent")
        .and_then(Value::as_f64)
        .map(|pct| pct / 100.0);
    let resets_at = value
        .get("resets_at")
        .and_then(Value::as_i64)
        .and_then(epoch_seconds);
    Some(LimitWindow {
        tool: ProviderId::Codex,
        plan: plan.map(ToString::to_string),
        kind,
        used_fraction,
        resets_at,
        label: None,
    })
}

fn codex_has_rate_limits(loc: &DataLocation) -> Result<bool, ProviderError> {
    for file in &loc.files {
        for value in read_jsonl_values(ProviderId::Codex, file)? {
            if value.pointer("/payload/rate_limits").is_some() {
                return Ok(true);
            }
        }
    }
    Ok(false)
}

fn parse_cursor_usage(value: &Value) -> Option<UsageEvent> {
    let timestamp = parse_rfc3339(value.get("timestamp").and_then(Value::as_str)?)?;
    let event = UsageEvent {
        tool: ProviderId::Cursor,
        model: value.get("model").and_then(Value::as_str)?.to_string(),
        timestamp,
        input_tokens: number_u64(value.get("input_tokens")),
        output_tokens: number_u64(value.get("output_tokens")),
        cache_read_tokens: number_u64(value.get("cache_read_tokens")),
        cache_write_tokens: number_u64(value.get("cache_write_tokens")),
        project: value
            .get("project")
            .and_then(Value::as_str)
            .map(ToString::to_string),
        access_path: AccessPath::Unknown,
    };
    has_any_tokens(&event).then_some(event)
}

fn unavailable_limit(provider: ProviderId, kind: LimitKind) -> LimitWindow {
    LimitWindow {
        tool: provider,
        plan: None,
        kind,
        used_fraction: None,
        resets_at: None,
        label: Some("unavailable".to_string()),
    }
}

fn parse_rfc3339(value: &str) -> Option<DateTime<Utc>> {
    DateTime::parse_from_rfc3339(value)
        .ok()
        .map(|value| value.with_timezone(&Utc))
}

fn epoch_seconds(value: i64) -> Option<DateTime<Utc>> {
    match Utc.timestamp_opt(value, 0) {
        LocalResult::Single(value) => Some(value),
        LocalResult::Ambiguous(_, _) | LocalResult::None => None,
    }
}

fn number_u64(value: Option<&Value>) -> u64 {
    value.and_then(Value::as_u64).unwrap_or(0)
}

fn has_any_tokens(event: &UsageEvent) -> bool {
    event.input_tokens > 0
        || event.output_tokens > 0
        || event.cache_read_tokens > 0
        || event.cache_write_tokens > 0
}

#[cfg(test)]
mod tests {
    use super::*;

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

    #[test]
    fn provider_ids_match_documented_values() {
        assert_eq!(ProviderId::ClaudeCode.to_string(), "claude-code");
        assert_eq!(ProviderId::Codex.to_string(), "codex");
        assert_eq!(ProviderId::Cursor.to_string(), "cursor");
    }

    #[test]
    fn wsl_roots_include_linux_and_windows_candidates() {
        let env = HostEnv::new(
            PathBuf::from("/home/example"),
            Some(PathBuf::from("/mnt/c/Users/example")),
            true,
        );
        let claude_roots = env.claude_roots();
        let codex_roots = env.codex_roots();

        assert!(claude_roots.contains(&PathBuf::from("/home/example/.config/claude")));
        assert!(claude_roots.contains(&PathBuf::from("/mnt/c/Users/example/.claude")));
        assert!(codex_roots.contains(&PathBuf::from("/home/example/.codex")));
        assert!(codex_roots.contains(&PathBuf::from("/mnt/c/Users/example/.codex")));
    }

    #[test]
    fn claude_fixture_parses_usage_and_unavailable_limits() {
        let provider = ClaudeCodeProvider;
        let loc = DataLocation {
            provider: ProviderId::ClaudeCode,
            root: fixture_path(&["claude-code"]),
            files: vec![fixture_path(&["claude-code", "project-transcript.jsonl"])],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("claude fixture should parse: {err}"),
        };
        let limits = match provider.parse_limits(&loc) {
            Ok(value) => value,
            Err(err) => panic!("claude limits should parse: {err}"),
        };

        assert_eq!(usage.len(), 1);
        assert_eq!(usage[0].model, "claude-sonnet-example");
        assert_eq!(usage[0].input_tokens, 10);
        assert_eq!(usage[0].output_tokens, 20);
        assert_eq!(usage[0].cache_read_tokens, 30);
        assert_eq!(usage[0].cache_write_tokens, 40);
        assert_eq!(limits.len(), 2);
        assert!(limits.iter().all(|limit| limit.used_fraction.is_none()));
        assert!(limits.iter().all(|limit| limit.resets_at.is_none()));
    }

    #[test]
    fn codex_fixture_parses_usage_and_limits() {
        let provider = CodexProvider;
        let loc = DataLocation {
            provider: ProviderId::Codex,
            root: fixture_path(&["codex"]),
            files: vec![fixture_path(&["codex", "rollout.jsonl"])],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("codex fixture should parse: {err}"),
        };
        let limits = match provider.parse_limits(&loc) {
            Ok(value) => value,
            Err(err) => panic!("codex limits should parse: {err}"),
        };

        assert_eq!(usage.len(), 1);
        assert_eq!(usage[0].model, "example-model");
        assert_eq!(usage[0].access_path, AccessPath::Subscription);
        assert_eq!(usage[0].cache_read_tokens, 300);
        assert_eq!(limits.len(), 2);
        assert!(
            limits
                .iter()
                .any(|limit| limit.kind == LimitKind::FiveHour
                    && close_to(limit.used_fraction, 0.425))
        );
        assert!(limits
            .iter()
            .any(|limit| limit.kind == LimitKind::Weekly && close_to(limit.used_fraction, 0.1825)));
    }

    #[test]
    fn cursor_fixture_parses_partial_usage_only() {
        let provider = CursorProvider;
        let loc = DataLocation {
            provider: ProviderId::Cursor,
            root: fixture_path(&["cursor"]),
            files: vec![fixture_path(&["cursor", "local-partial.json"])],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("cursor fixture should parse: {err}"),
        };
        let limits = match provider.parse_limits(&loc) {
            Ok(value) => value,
            Err(err) => panic!("cursor limits should parse: {err}"),
        };

        assert_eq!(usage.len(), 1);
        assert_eq!(usage[0].access_path, AccessPath::Unknown);
        assert_eq!(limits.len(), 1);
        assert_eq!(limits[0].label.as_deref(), Some("unavailable"));
    }

    fn close_to(value: Option<f64>, expected: f64) -> bool {
        value
            .map(|value| (value - expected).abs() < 0.000_001)
            .unwrap_or(false)
    }
}
