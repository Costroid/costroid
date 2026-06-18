//! The shared Costroid user-config crate: a non-secret TOML at
//! `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`, hand-edited by the user and
//! **read-only** here — Costroid never writes it (no writer, no `budget set` command). It is
//! forward-compatible: every section/field defaults and unknown keys are ignored, so an older
//! build reads a newer file (and vice-versa) without error.
//!
//! Both apps consume this one schema + loader: the `costroid` CLI/TUI (T14/T17) and the
//! `costroid-bar` taskbar (Step 6, T20) — extracted out of `apps/cli/src/config.rs` so the bar
//! gets the same `[budget]`/`[alerts]` parsing without depending on `apps/cli`. A pure leaf of the
//! workspace: it depends only on `costroid-core` (for the config-neutral [`AlertThresholds`] /
//! [`BudgetTargets`] input types) + `serde`/`toml`/`rust_decimal`; it links no network/keychain code.
//!
//! **Secrets never live here** — credentials are keychain-only (`costroid-connect`). Today the
//! file carries `[budget]` targets (monthly $ caps, API-lane only; money is
//! `rust_decimal::Decimal`, never f64) and the `[alerts]` opt-in (T17, default off).
//!
//! ```toml
//! [budget]
//! total_monthly_usd = 100.00
//!
//! [budget.per_tool]
//! claude-code = 60.00
//! codex = 40.00
//!
//! [alerts]
//! enabled = true        # opt-in; default false (quiet) — the master switch
//! # quota_warn = 0.80   # optional per-class overrides (default 0.80 / 0.95)
//! # quota_critical = 0.95
//! # forecast = true     # advisory (T17b): month-end projection over the TOTAL budget; default off
//! # anomalies = true    # advisory (T17b): a daily spend spike vs your own norm; default off
//! ```
//!
//! `forecast` and `anomalies` are opt-in advisory SUB-flags — each off by default and each still
//! requiring `enabled = true`. They surface heads-up advisories (a projected total-budget overrun
//! and a spend spike) in the same banner / `alerts` list as the hard quota and budget crossings.

use std::collections::BTreeMap;
use std::fmt;
use std::path::{Path, PathBuf};
use std::str::FromStr;

use costroid_core::{AlertThresholds, BudgetTargets};
use rust_decimal::Decimal;
use serde::de::{self, Deserializer, Visitor};
use serde::Deserialize;

/// The parsed user config. Forward-compatible: every section defaults, so a missing or partial
/// file is valid and unknown keys are silently ignored (serde's default behavior — no
/// `deny_unknown_fields`).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Config {
    budget: BudgetConfig,
    alerts: AlertsConfig,
}

/// The `[budget]` section: optional monthly $ caps. API-lane only — a flat-fee subscription is
/// never given a $ target (the core [`budget_view`](costroid_core::budget_view) enforces this).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
struct BudgetConfig {
    /// An optional overall monthly cap across all API-lane spend.
    total_monthly_usd: Option<Money>,
    /// Per-tool monthly caps, keyed by the `x_Tool` id (`claude-code`/`codex`/`cursor`).
    per_tool: BTreeMap<String, Money>,
}

/// The `[alerts]` section (T17): opt-in threshold alerts, OFF by default. `enabled` is the master
/// switch for the whole feature — the inline `now` banner AND the `costroid alerts` /
/// `alerts --check` command. The optional per-class quota overrides default to the core's
/// canonical near-limit fractions (0.80 / 0.95) when unset. Budget alerts carry no threshold here:
/// the crossing is the monthly $ target itself (an over-budget [`costroid_core::BudgetRow`]).
/// Forward-compat: `#[serde(default)]`, so an absent section ⇒ off and a newer file is tolerated.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
struct AlertsConfig {
    /// Master switch. Default false — alerts are opt-in and quiet.
    enabled: bool,
    /// Optional WARN quota fraction override (default [`costroid_core::ALERT_WARN_FRACTION`]).
    quota_warn: Option<f64>,
    /// Optional CRITICAL quota fraction override (default
    /// [`costroid_core::ALERT_CRITICAL_FRACTION`]).
    quota_critical: Option<f64>,
    /// Opt-in advisory source (T17b): a month-end TOTAL-budget projection over target. Default
    /// false; the master `enabled` switch is still required for it to do anything.
    forecast: bool,
    /// Opt-in advisory source (T17b): a daily spend-spike anomaly vs your own norm. Default false;
    /// the master `enabled` switch is still required.
    anomalies: bool,
}

impl Config {
    /// Project the parsed config into the core's config-neutral budget input.
    pub fn budget_targets(&self) -> BudgetTargets {
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

    /// Whether the alerts feature is enabled (default false — opt-in). The master switch — it also
    /// gates the two advisory sub-flags below.
    pub fn alerts_enabled(&self) -> bool {
        self.alerts.enabled
    }

    /// Whether the advisory forecast-projection source is on (T17b). Requires BOTH the master
    /// `enabled` switch and the `forecast` sub-flag — the sub-flag alone does nothing.
    pub fn alerts_forecast_enabled(&self) -> bool {
        self.alerts.enabled && self.alerts.forecast
    }

    /// Whether the advisory spend-spike source is on (T17b). Requires BOTH the master `enabled`
    /// switch and the `anomalies` sub-flag.
    pub fn alerts_anomalies_enabled(&self) -> bool {
        self.alerts.enabled && self.alerts.anomalies
    }

    /// Project the `[alerts]` overrides into the core's config-neutral [`AlertThresholds`]. An
    /// override is applied only when finite and strictly positive (a `NaN`/`inf`/zero/negative
    /// value falls back to the canonical default), so the pure detector never sees a threshold it
    /// can't reason about.
    pub fn alert_thresholds(&self) -> AlertThresholds {
        let mut thresholds = AlertThresholds::default();
        if let Some(warn) = sane_fraction(self.alerts.quota_warn) {
            thresholds.quota_warn_fraction = warn;
        }
        if let Some(critical) = sane_fraction(self.alerts.quota_critical) {
            thresholds.quota_critical_fraction = critical;
        }
        // Cross-field sanity: a self-contradictory pair (warn > critical) would make CRITICAL fire
        // below the user's intended WARN floor and leave WARN unreachable. Reject the whole pair back
        // to the canonical defaults — mirroring sane_fraction's per-field discipline, so the detector
        // never sees an inverted pair it can't reason about.
        if thresholds.quota_warn_fraction > thresholds.quota_critical_fraction {
            thresholds = AlertThresholds::default();
        }
        thresholds
    }
}

/// Accept a user-supplied fraction override only when finite and strictly positive; anything else
/// (`NaN`/`inf`/zero/negative) falls back to the canonical default. A value above `1.0` is left
/// as-is — a legitimate way to silence a class (the threshold can never be reached).
fn sane_fraction(value: Option<f64>) -> Option<f64> {
    value.filter(|fraction| fraction.is_finite() && *fraction > 0.0)
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
pub fn config_path() -> Option<PathBuf> {
    let base = std::env::var_os("XDG_CONFIG_HOME")
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
        .or_else(|| std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".config")))?;
    Some(base.join("costroid").join("config.toml"))
}

/// A config-load failure. A *missing* file is NOT one of these — it yields the zero-config
/// default. These are surfaced as a TUI/taskbar status line (never a crash).
#[derive(Debug)]
pub enum ConfigError {
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
pub fn load() -> Result<Config, ConfigError> {
    match config_path() {
        Some(path) => load_from(&path),
        None => Ok(Config::default()),
    }
}

/// Load the config from an explicit path — the testable seam (no env access). A missing file =>
/// default; an unreadable file or invalid TOML => a typed [`ConfigError`].
pub fn load_from(path: &Path) -> Result<Config, ConfigError> {
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

    // ----- [alerts] (T17) -----

    #[test]
    fn alerts_default_off_with_canonical_thresholds() {
        // No file, and a budget-only file, both yield alerts OFF + the canonical default thresholds.
        let dir = TempDir::new();
        for contents in ["", "[budget]\ntotal_monthly_usd = 100\n"] {
            let path = dir.write(contents);
            let config = match load_from(&path) {
                Ok(config) => config,
                Err(err) => panic!("should default: {err}"),
            };
            assert!(
                !config.alerts_enabled(),
                "alerts must default OFF: {contents:?}"
            );
            let thresholds = config.alert_thresholds();
            assert_eq!(
                thresholds.quota_warn_fraction,
                costroid_core::ALERT_WARN_FRACTION
            );
            assert_eq!(
                thresholds.quota_critical_fraction,
                costroid_core::ALERT_CRITICAL_FRACTION
            );
        }
    }

    #[test]
    fn alerts_enabled_parses_and_keeps_default_thresholds() {
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = true\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("alerts config should parse: {err}"),
        };
        assert!(config.alerts_enabled());
        let thresholds = config.alert_thresholds();
        assert_eq!(
            thresholds.quota_warn_fraction,
            costroid_core::ALERT_WARN_FRACTION
        );
        assert_eq!(
            thresholds.quota_critical_fraction,
            costroid_core::ALERT_CRITICAL_FRACTION
        );
    }

    #[test]
    fn alerts_threshold_overrides_apply() {
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = true\nquota_warn = 0.5\nquota_critical = 0.9\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("alert overrides should parse: {err}"),
        };
        let thresholds = config.alert_thresholds();
        assert_eq!(thresholds.quota_warn_fraction, 0.5);
        assert_eq!(thresholds.quota_critical_fraction, 0.9);
    }

    #[test]
    fn alerts_hostile_threshold_overrides_fall_back_to_defaults() {
        // Zero / negative are not sane fractions → the canonical default stands (never breaks the
        // detector). A present-but-odd value is not a parse error.
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nquota_warn = 0.0\nquota_critical = -0.5\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("odd-but-valid values should parse: {err}"),
        };
        let thresholds = config.alert_thresholds();
        assert_eq!(
            thresholds.quota_warn_fraction,
            costroid_core::ALERT_WARN_FRACTION
        );
        assert_eq!(
            thresholds.quota_critical_fraction,
            costroid_core::ALERT_CRITICAL_FRACTION
        );
    }

    #[test]
    fn alerts_inverted_threshold_pair_falls_back_to_defaults() {
        // warn > critical is self-contradictory (CRITICAL would fire below the WARN floor and WARN
        // would be dead) — the whole pair falls back to the canonical defaults, never a nonsense pair.
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = true\nquota_warn = 0.9\nquota_critical = 0.5\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("inverted-but-valid values should parse: {err}"),
        };
        let thresholds = config.alert_thresholds();
        assert_eq!(
            thresholds.quota_warn_fraction,
            costroid_core::ALERT_WARN_FRACTION
        );
        assert_eq!(
            thresholds.quota_critical_fraction,
            costroid_core::ALERT_CRITICAL_FRACTION
        );
        // A single low-critical override that inverts against the DEFAULT warn also falls back.
        let path2 = dir.write("[alerts]\nenabled = true\nquota_critical = 0.5\n");
        let config2 = match load_from(&path2) {
            Ok(config) => config,
            Err(err) => panic!("should parse: {err}"),
        };
        let t2 = config2.alert_thresholds();
        assert_eq!(t2.quota_warn_fraction, costroid_core::ALERT_WARN_FRACTION);
        assert_eq!(
            t2.quota_critical_fraction,
            costroid_core::ALERT_CRITICAL_FRACTION
        );
    }

    #[test]
    fn malformed_alerts_value_is_a_typed_error_not_a_panic() {
        // `enabled` must be a bool — a string is a typed Parse error (the non-crash contract),
        // never a panic.
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = \"yes\"\n");
        match load_from(&path) {
            Ok(config) => panic!("malformed alerts should error, got {config:?}"),
            Err(ConfigError::Parse { .. }) => {}
            Err(other) => panic!("expected a Parse error, got {other}"),
        }
    }

    // ----- advisory sub-flags (T17b) -----

    #[test]
    fn advisory_subflags_default_off() {
        // Absent section, and an enabled-but-no-subflags section, both leave BOTH advisory sources off.
        let dir = TempDir::new();
        for contents in ["", "[alerts]\nenabled = true\n"] {
            let path = dir.write(contents);
            let config = match load_from(&path) {
                Ok(config) => config,
                Err(err) => panic!("should default: {err}"),
            };
            assert!(
                !config.alerts_forecast_enabled(),
                "forecast must default OFF: {contents:?}"
            );
            assert!(
                !config.alerts_anomalies_enabled(),
                "anomalies must default OFF: {contents:?}"
            );
        }
    }

    #[test]
    fn advisory_subflags_parse_on_when_master_enabled() {
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = true\nforecast = true\nanomalies = true\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("advisory sub-flags should parse: {err}"),
        };
        assert!(config.alerts_forecast_enabled());
        assert!(config.alerts_anomalies_enabled());
    }

    #[test]
    fn advisory_subflags_require_the_master_switch() {
        // A sub-flag set WITHOUT the master `enabled` does nothing — `enabled` is still required.
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nforecast = true\nanomalies = true\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("should parse: {err}"),
        };
        assert!(!config.alerts_enabled());
        assert!(
            !config.alerts_forecast_enabled(),
            "forecast must stay off without the master switch"
        );
        assert!(
            !config.alerts_anomalies_enabled(),
            "anomalies must stay off without the master switch"
        );
    }

    #[test]
    fn advisory_subflags_are_independent() {
        // Each sub-flag is independent — enabling one does not enable the other.
        let dir = TempDir::new();
        let path = dir.write("[alerts]\nenabled = true\nforecast = true\n");
        let config = match load_from(&path) {
            Ok(config) => config,
            Err(err) => panic!("should parse: {err}"),
        };
        assert!(config.alerts_forecast_enabled());
        assert!(!config.alerts_anomalies_enabled());
    }
}
