//! The first user-config file (T14): a non-secret TOML at
//! `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`, hand-edited by the user and
//! **read-only** here — Costroid never writes it (no writer, no `budget set` command). It is
//! forward-compatible: every section/field defaults and unknown keys are ignored, so an older
//! build reads a newer file (and vice-versa) without error.
//!
//! **Secrets never live here** — credentials are keychain-only (`costroid-connect`). Today the
//! file carries only `[budget]` targets (monthly $ caps, API-lane only); money is
//! `rust_decimal::Decimal`, never f64.
//!
//! ```toml
//! [budget]
//! total_monthly_usd = 100.00
//!
//! [budget.per_tool]
//! claude-code = 60.00
//! codex = 40.00
//! ```

use std::collections::BTreeMap;
use std::fmt;
use std::path::{Path, PathBuf};
use std::str::FromStr;

use costroid_core::BudgetTargets;
use rust_decimal::Decimal;
use serde::de::{self, Deserializer, Visitor};
use serde::Deserialize;

/// The parsed user config. Forward-compatible: every section defaults, so a missing or partial
/// file is valid and unknown keys are silently ignored (serde's default behavior — no
/// `deny_unknown_fields`).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub(crate) struct Config {
    pub(crate) budget: BudgetConfig,
}

/// The `[budget]` section: optional monthly $ caps. API-lane only — a flat-fee subscription is
/// never given a $ target (the core [`budget_view`](costroid_core::budget_view) enforces this).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub(crate) struct BudgetConfig {
    /// An optional overall monthly cap across all API-lane spend.
    total_monthly_usd: Option<Money>,
    /// Per-tool monthly caps, keyed by the `x_Tool` id (`claude-code`/`codex`/`cursor`).
    per_tool: BTreeMap<String, Money>,
}

impl Config {
    /// Project the parsed config into the core's config-neutral budget input.
    pub(crate) fn budget_targets(&self) -> BudgetTargets {
        BudgetTargets {
            total_monthly_usd: self.budget.total_monthly_usd.map(|money| money.0),
            per_tool: self
                .budget
                .per_tool
                .iter()
                .map(|(tool, money)| (tool.clone(), money.0))
                .collect(),
        }
    }
}

/// A monthly $ amount parsed from TOML into an exact [`Decimal`]. Accepts a TOML integer
/// (`60`), a float (`60.00`), or a quoted string (`"60.00"`). Integers and quoted strings are
/// exact; a *bare float* transits f64 (TOML has no decimal type) and is converted best-effort —
/// quote the value for guaranteed exactness. The internal money type is always `Decimal`,
/// never f64.
#[derive(Debug, Clone, Copy)]
struct Money(Decimal);

impl<'de> Deserialize<'de> for Money {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct MoneyVisitor;

        impl Visitor<'_> for MoneyVisitor {
            type Value = Money;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("a dollar amount (a number or a quoted decimal string)")
            }

            fn visit_i64<E: de::Error>(self, value: i64) -> Result<Money, E> {
                Ok(Money(Decimal::from(value)))
            }

            fn visit_u64<E: de::Error>(self, value: u64) -> Result<Money, E> {
                Ok(Money(Decimal::from(value)))
            }

            fn visit_f64<E: de::Error>(self, value: f64) -> Result<Money, E> {
                Decimal::from_f64_retain(value)
                    .map(Money)
                    .ok_or_else(|| de::Error::custom("dollar amount must be a finite number"))
            }

            fn visit_str<E: de::Error>(self, value: &str) -> Result<Money, E> {
                Decimal::from_str(value.trim()).map(Money).map_err(|err| {
                    de::Error::custom(format!("invalid dollar amount '{value}': {err}"))
                })
            }
        }

        // TOML is self-describing, so `deserialize_any` dispatches on the actual scalar
        // (integer / float / string) to the matching visitor method above.
        deserializer.deserialize_any(MoneyVisitor)
    }
}

/// Resolve the config-file path `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`.
/// `None` when neither `$XDG_CONFIG_HOME` nor `$HOME` yields a base directory (treated as "no
/// config" — the zero-config default, never an error). Mirrors `costroid-connect`'s
/// `default_registry_path`, but rooted at the *config* dir (this file is non-secret).
pub(crate) fn config_path() -> Option<PathBuf> {
    let base = std::env::var_os("XDG_CONFIG_HOME")
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
        .or_else(|| std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".config")))?;
    Some(base.join("costroid").join("config.toml"))
}

/// A config-load failure. A *missing* file is NOT one of these — it yields the zero-config
/// default. These are surfaced as a TUI status line (never a crash).
#[derive(Debug)]
pub(crate) enum ConfigError {
    /// The file exists but could not be read.
    Read {
        path: PathBuf,
        source: std::io::Error,
    },
    /// The file exists but is not valid TOML / carries an invalid value.
    Parse {
        path: PathBuf,
        source: toml::de::Error,
    },
}

impl fmt::Display for ConfigError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ConfigError::Read { path, source } => {
                write!(
                    formatter,
                    "could not read config {}: {source}",
                    path.display()
                )
            }
            ConfigError::Parse { path, source } => {
                // TOML errors are multi-line; collapse to the first line for the status bar.
                let detail = source.to_string();
                let first = detail.lines().next().unwrap_or(&detail);
                write!(formatter, "invalid config {}: {first}", path.display())
            }
        }
    }
}

impl std::error::Error for ConfigError {}

/// Load the user config from the resolved default path. A missing file (or an unresolved path)
/// yields the zero-config default (no budgets) — only a present-but-malformed file is an error.
pub(crate) fn load() -> Result<Config, ConfigError> {
    match config_path() {
        Some(path) => load_from(&path),
        None => Ok(Config::default()),
    }
}

/// Load the config from an explicit path — the testable seam (no env access). A missing file =>
/// default; an unreadable file or invalid TOML => a typed [`ConfigError`].
pub(crate) fn load_from(path: &Path) -> Result<Config, ConfigError> {
    let text = match std::fs::read_to_string(path) {
        Ok(text) => text,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Config::default()),
        Err(source) => {
            return Err(ConfigError::Read {
                path: path.to_path_buf(),
                source,
            })
        }
    };
    toml::from_str::<Config>(&text).map_err(|source| ConfigError::Parse {
        path: path.to_path_buf(),
        source,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A unique, auto-cleaned temp directory (no `tempfile` dep — mirrors `costroid-connect`'s
    /// test temp helper). Tests write fixture config files here, never to the user's real config.
    struct TempDir {
        path: PathBuf,
    }

    impl TempDir {
        fn new() -> Self {
            use std::sync::atomic::{AtomicU32, Ordering};
            static COUNTER: AtomicU32 = AtomicU32::new(0);
            let n = COUNTER.fetch_add(1, Ordering::Relaxed);
            let pid = std::process::id();
            let path = std::env::temp_dir().join(format!("costroid-config-test-{pid}-{n}"));
            if let Err(err) = std::fs::create_dir_all(&path) {
                panic!("temp dir should create: {err}");
            }
            Self { path }
        }

        fn write(&self, contents: &str) -> PathBuf {
            let file = self.path.join("config.toml");
            if let Err(err) = std::fs::write(&file, contents) {
                panic!("fixture config should write: {err}");
            }
            file
        }
    }

    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.path);
        }
    }

    fn cents(value: i64, scale: u32) -> Decimal {
        Decimal::new(value, scale)
    }

    #[test]
    fn absent_file_loads_the_zero_config_default() {
        let dir = TempDir::new();
        let missing = dir.path.join("does-not-exist.toml");
        let config = match load_from(&missing) {
            Ok(config) => config,
            Err(err) => panic!("absent file should default, not error: {err}"),
        };
        assert!(config.budget_targets().is_empty());
    }

    #[test]
    fn present_file_parses_total_and_per_tool_targets() {
        let dir = TempDir::new();
        let path = dir.write(
            "[budget]\n\
             total_monthly_usd = 100.00\n\n\
             [budget.per_tool]\n\
             claude-code = 60.00\n\
             codex = 40\n",
        );
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("valid config should parse: {err}"),
        };
        let targets = config.budget_targets();
        assert!(!targets.is_empty());
        assert_eq!(targets.total_monthly_usd, Some(cents(10_000, 2)));
        assert_eq!(targets.per_tool.get("claude-code"), Some(&cents(6_000, 2)));
        // A bare TOML integer (`40`) parses to an exact Decimal too.
        assert_eq!(targets.per_tool.get("codex"), Some(&cents(40, 0)));
    }

    #[test]
    fn quoted_string_money_is_exact() {
        let dir = TempDir::new();
        let path = dir.write("[budget]\ntotal_monthly_usd = \"99.99\"\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("quoted money should parse: {err}"),
        };
        assert_eq!(
            config.budget_targets().total_monthly_usd,
            Some(cents(9_999, 2))
        );
    }

    #[test]
    fn malformed_file_is_a_typed_error_not_a_panic() {
        let dir = TempDir::new();
        // Not valid TOML (unterminated table header).
        let path = dir.write("[budget\ntotal_monthly_usd = 100\n");
        match load_from(&path) {
            Ok(config) => panic!("malformed config should error, got {config:?}"),
            Err(err @ ConfigError::Parse { .. }) => {
                // The Display is a single, status-bar-friendly line.
                let message = err.to_string();
                assert!(message.contains("invalid config"), "message: {message}");
                assert!(
                    !message.contains('\n'),
                    "status must be one line: {message}"
                );
            }
            Err(other) => panic!("expected a Parse error, got {other}"),
        }
    }

    #[test]
    fn invalid_money_value_is_a_typed_error() {
        let dir = TempDir::new();
        let path = dir.write("[budget]\ntotal_monthly_usd = \"not-a-number\"\n");
        match load_from(&path) {
            Ok(config) => panic!("invalid money should error, got {config:?}"),
            Err(ConfigError::Parse { .. }) => {}
            Err(other) => panic!("expected a Parse error, got {other}"),
        }
    }

    #[test]
    fn unknown_keys_are_ignored_for_forward_compatibility() {
        let dir = TempDir::new();
        let path = dir.write(
            "schema_version = 99\n\
             [budget]\n\
             total_monthly_usd = 50\n\
             future_field = true\n\n\
             [alerts]\n\
             enabled = true\n",
        );
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("unknown keys should be ignored, not error: {err}"),
        };
        assert_eq!(
            config.budget_targets().total_monthly_usd,
            Some(cents(50, 0))
        );
    }

    #[test]
    fn empty_file_is_the_zero_config_default() {
        let dir = TempDir::new();
        let path = dir.write("");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("empty file should default: {err}"),
        };
        assert!(config.budget_targets().is_empty());
    }
}
