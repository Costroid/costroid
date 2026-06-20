//! Provider-facing interfaces and local parsers for AI-tool usage data.

pub mod focus_import;

use std::collections::{BTreeSet, HashMap};
use std::env;
use std::fmt;
use std::fs::{self, File};
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};

use chrono::{DateTime, LocalResult, TimeZone, Utc};
use rust_decimal::Decimal;
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
    pub windows_home_dirs: Vec<PathBuf>,
    pub is_wsl: bool,
}

impl HostEnv {
    pub fn new(home_dir: PathBuf, windows_home_dirs: Vec<PathBuf>, is_wsl: bool) -> Self {
        Self {
            home_dir,
            windows_home_dirs,
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
        let windows_home_dirs = detect_windows_homes(is_wsl);

        Self::new(home_dir, windows_home_dirs, is_wsl)
    }

    pub fn claude_roots(&self) -> Vec<PathBuf> {
        let mut roots = Vec::new();
        roots.extend(claude_config_dir_roots());
        roots.push(self.home_dir.join(".config").join("claude"));
        roots.push(self.home_dir.join(".claude"));
        for windows_home in &self.windows_home_dirs {
            roots.push(windows_home.join(".config").join("claude"));
            roots.push(windows_home.join(".claude"));
        }
        dedupe_paths(roots)
    }

    pub fn codex_roots(&self) -> Vec<PathBuf> {
        self.codex_roots_from(codex_home_root(env::var_os("CODEX_HOME")))
    }

    /// Codex log roots, with the `CODEX_HOME` override (if any) taking priority
    /// over the `~/.codex` default and the WSL Windows `.codex`, then deduped.
    /// `CODEX_HOME` is honored before the defaults — mirroring `CLAUDE_CONFIG_DIR`
    /// for Claude — so a relocated Codex home is never silently under-counted.
    /// Takes the override as a parameter so the ordering is testable without
    /// mutating the process environment.
    fn codex_roots_from(&self, codex_home: Option<PathBuf>) -> Vec<PathBuf> {
        let mut roots = Vec::new();
        roots.extend(codex_home);
        roots.push(self.home_dir.join(".codex"));
        for windows_home in &self.windows_home_dirs {
            roots.push(windows_home.join(".codex"));
        }
        dedupe_paths(roots)
    }

    pub fn cursor_roots(&self) -> Vec<PathBuf> {
        self.cursor_roots_from(cursor_data_dir_root(env::var_os("CURSOR_DATA_DIR")))
    }

    /// Cursor data roots, with the `CURSOR_DATA_DIR` override (if any) taking
    /// priority over the `~/.cursor` default and the WSL Windows `.cursor`, then
    /// deduped — mirroring [`HostEnv::codex_roots_from`]. The Cursor CLI keeps its
    /// data under `~/.cursor`; Costroid only ever reads `cli-config.json` from a
    /// root (see [`discover_cursor_location`]), never the chat stores or credentials.
    /// Takes the override as a parameter so the ordering is testable without
    /// mutating the process environment.
    fn cursor_roots_from(&self, cursor_data_dir: Option<PathBuf>) -> Vec<PathBuf> {
        let mut roots = Vec::new();
        roots.extend(cursor_data_dir);
        roots.push(self.home_dir.join(".cursor"));
        for windows_home in &self.windows_home_dirs {
            roots.push(windows_home.join(".cursor"));
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
    /// `true` when this turn was a sub-agent (sidechain) turn — Claude transcripts carry
    /// a top-level `isSidechain` flag. Costroid keeps counting sidechain usage; this only
    /// annotates it (its attribution is less certain). Codex has no sidechain concept, so
    /// its events are always `false`. Metadata only (R4) — a bool, never content.
    pub is_sidechain: bool,
}

/// The canonical, lane-spanning event model. Every datum Costroid ingests is one
/// of these three lanes:
///
/// - [`CanonicalEvent::Tool`] — an AI-coding-tool usage row parsed from local
///   logs (the existing [`UsageEvent`], the default local lane).
/// - [`CanonicalEvent::Cloud`] — a cloud-billing row imported via the M1
///   FOCUS-v1.2 bridge / M2 cloud lane (see [`CloudUsageEvent`]).
/// - [`CanonicalEvent::Local`] — a local-inference run measured at M3 (see
///   [`LocalRunEvent`]).
///
/// Metadata only: by the **Cardinal Rule (R4)** no lane variant may ever carry
/// prompt/completion/response/content/message/text. Costroid meters cost and
/// quota, never conversation content. The structural guard in this crate's tests
/// destructures the metadata variants field-exhaustively (no `..`) so any new
/// field forces a conscious review against R4.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CanonicalEvent {
    Tool(UsageEvent),
    /// Boxed: a `CloudUsageEvent` carries the full foreign pricing detail (M2 T4), so it is
    /// much larger than the other variants — boxing keeps `CanonicalEvent` (and any
    /// `Vec<CanonicalEvent>` an import builds) small.
    Cloud(Box<CloudUsageEvent>),
    Local(LocalRunEvent),
}

/// Minimal metadata for a FOCUS-v1.2-imported cloud-billing row.
///
/// Populated by the M2 cloud lane / the M1 v1.2 import bridge — defined here so
/// the canonical event model spans all lanes. Metadata only (R4): no
/// prompt/completion/content/text fields, ever.
///
/// The source-authoritative cost is carried as a decimal **string**
/// ([`billed_cost`](Self::billed_cost)) — not `f64` money and not a
/// `rust_decimal` type. `costroid-providers` stays dep-light at the parsing
/// boundary; the `costroid-core` bridge parses the string into its money type.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CloudUsageEvent {
    pub timestamp: DateTime<Utc>,
    pub service_name: String,
    pub service_provider_name: String,
    pub model: Option<String>,
    pub token_count: Option<u64>,
    /// Source-authoritative billed cost, carried verbatim as a decimal string
    /// (e.g. `"0.0123"`). Parsed by the core bridge, never as `f64` here.
    pub billed_cost: Option<String>,
    /// Source EffectiveCost / ListCost / ContractedCost (decimal strings) — carried for
    /// cost-column fidelity; the core bridge falls back to `billed_cost` when absent.
    pub effective_cost: Option<String>,
    pub list_cost: Option<String>,
    pub contracted_cost: Option<String>,
    /// The foreign export's own per-token pricing detail (M2 — closes the M1 per-token-rate
    /// deferral): SkuPriceId + unit-prices + priced quantity + bounded category/unit/currency
    /// labels. All bounded metadata (R4): ids, decimals-as-strings, enum-ish labels — never
    /// content. Carried so a source-priced cloud row is *fully* priced, not just costed.
    pub sku_price_id: Option<String>,
    pub pricing_category: Option<String>,
    pub pricing_quantity: Option<String>,
    pub pricing_unit: Option<String>,
    pub list_unit_price: Option<String>,
    pub contracted_unit_price: Option<String>,
    /// The currency the unit prices / costs are quoted in (FOCUS `PricingCurrency`). Carried
    /// for the M2 multi-currency lane; the core bridge keeps the row in its native currency.
    pub pricing_currency: Option<String>,
    pub consumed_unit: Option<String>,
}

/// Minimal metadata for a local-inference run.
///
/// Populated at M3; defining the struct now lets the canonical event model span
/// the local-inference lane. Adds **no** export columns at M1. Metadata only
/// (R4): no prompt/completion/content/text fields, ever.
///
/// [`measurement_mode`](Self::measurement_mode) is a plain `String` at this
/// crate boundary — the typed enum lives in `costroid-power`, which
/// `costroid-providers` must not depend on.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LocalRunEvent {
    pub timestamp: DateTime<Utc>,
    pub model: String,
    pub runtime_kind: String,
    pub tokens_in: u64,
    pub tokens_out: u64,
    pub run_seconds: f64,
    pub avg_power_watts: f64,
    pub measurement_mode: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum LimitKind {
    FiveHour,
    Weekly,
    Daily,
    Monthly,
    BillingCycle,
}

/// What a quota window meters. One generalized shape for every provider/feature
/// (§2a): Claude/Codex/Antigravity report a token-fraction; Cursor (paid) and
/// post-June-2026 Copilot report a dollar-denominated credit pool. The legacy
/// pre-June-2026 Copilot request-count model is intentionally **not** modeled.
///
/// Carries `f64`, so it is `PartialEq` but not `Eq` (mirroring [`LimitWindow`]).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LimitMeasure {
    /// Fraction of the window consumed, `0.0..=1.0`.
    TokenFraction(f64),
    /// A dollar credit pool: `used_usd` spent against an optional `included_usd`
    /// allowance (overage runs past it). Producers/rendering land with T4/T6.
    Spend {
        used_usd: Decimal,
        included_usd: Option<Decimal>,
    },
}

/// Confidence in a single quota reading (from the statusLine brief). `Verified` =
/// trusted local/sanctioned data; `Unverified` = present but failed cross-check
/// (wired by T4); `Unavailable` = no usable reading. Distinct from core's
/// `LimitAvailability`, whose `Estimated` arm lives only at the availability layer.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LimitStatus {
    Verified,
    Unverified,
    Unavailable,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LimitWindow {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub measure: Option<LimitMeasure>,
    pub resets_at: Option<DateTime<Utc>>,
    /// When this reading was taken — every window carries freshness. An
    /// `Unavailable` window (no reading) uses the UNIX epoch sentinel; the
    /// availability map ignores it for that case.
    pub captured_at: DateTime<Utc>,
    pub status: LimitStatus,
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

/// Where a provider sources one lane of data, named on the auth ladder (§5,
/// most-sanctioned first). A lane with no clean source declares
/// [`DataSource::Unavailable`] — never a fabricated one — so the Providers view
/// (T11) can render *what is unavailable and why* (§2b). Serialize/Deserialize for
/// consistency with [`ProviderId`]/[`AccessPath`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DataSource {
    /// Logs the tool already writes to disk — today's default, no network.
    LocalArtifact,
    /// A vendor-built third-party hook (Claude Code's `statusLine` `rate_limits`).
    SanctionedHook,
    /// The provider's own first-class third-party OAuth (e.g. GitHub).
    SanctionedOauth,
    /// The user's own usage/billing API key.
    ApiKey,
    /// No sanctioned source exists for this datum — unavailable, never fetched.
    /// (Reusing a credential/session against a non-sanctioned, undocumented, or
    /// internal endpoint is never an option — that is the ToS line; see ARCHITECTURE)
    Unavailable,
}

/// How a provider authenticates for the sources it declares. `None` = nothing to
/// log into (local artifacts only). Serialize/Deserialize for consistency with
/// [`ProviderId`]/[`AccessPath`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthMethod {
    None,
    Oauth,
    ApiKey,
}

/// A provider's declared data/auth/quota shape (§2b). Each adapter returns one from
/// [`Provider::capability`]; the Providers view reads it to render each lane's
/// source and what is unavailable, and a future adapter (Copilot/Antigravity) slots
/// in by filling this descriptor + the adapter, with no core/UI change.
/// `quota_kinds` lists the [`LimitKind`]s the provider can report (empty = no local
/// quota window).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Capability {
    pub api_cost: DataSource,
    pub subscription_quota: DataSource,
    pub model_mix: DataSource,
    pub auth: AuthMethod,
    pub quota_kinds: &'static [LimitKind],
}

pub trait Provider: Send + Sync {
    fn id(&self) -> ProviderId;

    /// The provider's declared data/auth/quota shape (§2b) — each lane's source,
    /// how it authenticates, and which quota windows it can report. Honest by
    /// construction: a lane with no clean source declares
    /// [`DataSource::Unavailable`] rather than a fabricated one.
    fn capability(&self) -> Capability;

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

    /// API cost + model mix from local transcripts; subscription quota from the
    /// sanctioned `statusLine` `rate_limits` push (T4). No login (`setup-statusline`).
    fn capability(&self) -> Capability {
        Capability {
            api_cost: DataSource::LocalArtifact,
            subscription_quota: DataSource::SanctionedHook,
            model_mix: DataSource::LocalArtifact,
            auth: AuthMethod::None,
            quota_kinds: &[LimitKind::FiveHour, LimitKind::Weekly],
        }
    }

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        discover_claude_location(env.claude_roots())
    }

    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
        let access_path = claude_access_path(&loc.root);
        // Claude transcripts duplicate the same assistant turn two ways: (1) resumed
        // or branched sessions copy finalized messages verbatim into new files, and
        // (2) a streaming/multi-block turn writes several lines that share one
        // (message.id, requestId) while output_tokens grows toward the final count
        // (input/cache stay fixed). Both must collapse to ONE record keyed on
        // (message.id, requestId), or the same usage is counted 2-3x. We keep the
        // occurrence with the largest output_tokens — i.e. the completed message —
        // wholesale (its full token set, not per-field maxima). This deliberately
        // diverges from ccusage's keep-first, which would keep a streaming partial
        // and undercount output; Costroid therefore reads very slightly ABOVE
        // ccusage on Claude output, which is correct, not a regression.
        //
        // Entries without BOTH ids are keyless and are NEVER collapsed (a real entry
        // missing requestId, and every Codex/Cursor event, must pass through as-is).
        let mut events: Vec<UsageEvent> = Vec::new();
        let mut seen: HashMap<(String, String), usize> = HashMap::new();
        for file in &loc.files {
            for value in read_jsonl_values(ProviderId::ClaudeCode, file)? {
                if let Some(event) = parse_claude_usage(&value, access_path) {
                    match claude_dedupe_key(&value) {
                        None => events.push(event),
                        Some(key) => match seen.get(&key) {
                            None => {
                                seen.insert(key, events.len());
                                events.push(event);
                            }
                            Some(&index) => {
                                if let Some(slot) = events.get_mut(index) {
                                    if event.output_tokens > slot.output_tokens {
                                        *slot = event;
                                    }
                                }
                            }
                        },
                    }
                }
            }
        }
        Ok(events)
    }

    /// Claude's 5h/weekly quota is not in the transcripts (`_loc`); it arrives only
    /// through the sanctioned `statusLine` `rate_limits` push, captured into a local
    /// no-secret cache (ARCHITECTURE). Read + sanitize that cache into two
    /// provisional windows; an absent/unreadable cache degrades to two `Unavailable`
    /// windows. The `Verified`/`Unavailable` status set here is PROVISIONAL — the core
    /// cross-check (which alone sees usage volume) may demote a high-but-trivial
    /// `Verified` reading to `Unverified`.
    fn parse_limits(&self, _loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
        Ok(read_claude_rate_limits(
            claude_rate_limits_cache_path().as_deref(),
        ))
    }
}

#[derive(Debug, Default)]
pub struct CodexProvider;

impl Provider for CodexProvider {
    fn id(&self) -> ProviderId {
        ProviderId::Codex
    }

    /// Everything from local rollout logs — API cost, model mix, and the 5h/weekly
    /// quota windows alike. No login.
    fn capability(&self) -> Capability {
        Capability {
            api_cost: DataSource::LocalArtifact,
            subscription_quota: DataSource::LocalArtifact,
            model_mix: DataSource::LocalArtifact,
            auth: AuthMethod::None,
            quota_kinds: &[LimitKind::FiveHour, LimitKind::Weekly],
        }
    }

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        discover_codex_location(env.codex_roots())
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

    /// Cost + quota are served live server-side by Cursor with no sanctioned source,
    /// and Costroid never reuses a session to fetch them — so both lanes are
    /// [`DataSource::Unavailable`], never fetched (live quota is discovery-gated;
    /// ROADMAP). Only the selected model (model mix) is a
    /// local artifact (`cli-config.json`). No local quota window, so `quota_kinds` is empty.
    fn capability(&self) -> Capability {
        Capability {
            api_cost: DataSource::Unavailable,
            subscription_quota: DataSource::Unavailable,
            model_mix: DataSource::LocalArtifact,
            auth: AuthMethod::None,
            quota_kinds: &[],
        }
    }

    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError> {
        discover_cursor_location(env.cursor_roots())
    }

    /// Cursor keeps **no** token usage on disk — usage is served live server-side by
    /// Cursor (ARCHITECTURE.md §4), and the only local numbers are chat content
    /// (off-limits, §8) and a context-window size snapshot (not spend). So there are
    /// no local usage events to produce: the now/trends/export screens carry no Cursor
    /// rows, and `costroid-core` reports Cursor as *detected* with usage **unavailable —
    /// no sanctioned source** (discovery-gated; §8).
    fn parse_usage(&self, _loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError> {
        Ok(Vec::new())
    }

    /// Cursor's quota (paid: a monthly $-credit pool; free: a daily token window) is
    /// served live by Cursor with **no sanctioned source** Costroid may read, so
    /// `parse_limits` emits no window — the "unavailable — no sanctioned source" status
    /// is surfaced in the provider's detected-status message in `costroid-core`. A live
    /// fetch is **discovery-gated** (ROADMAP): pursued only via a future
    /// *sanctioned* Cursor API/OAuth, **never** by reusing a local session against the
    /// undocumented `api2.cursor.sh` RPC (a ToS violation; §5 tier 4). (The generalized
    /// `LimitKind`/`Spend` shape that would render it already landed in T2.)
    fn parse_limits(&self, _loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError> {
        Ok(Vec::new())
    }
}

fn choose_limit(current: Option<LimitWindow>, next: Option<LimitWindow>) -> Option<LimitWindow> {
    match (current, next) {
        (None, value) => value,
        (Some(current), Some(next)) => {
            if !limit_has_data(&next) {
                Some(current)
            } else if !limit_has_data(&current) || next.captured_at >= current.captured_at {
                // The LATEST data-bearing reading wins by `captured_at`, not scan order —
                // merged multi-root discovery scans roots in priority order, so a stale
                // root's window must not override a fresher one. The epoch sentinel
                // (no timestamp) loses to any real stamp; ties keep `next`, preserving
                // single-file behavior where later lines are newer.
                Some(next)
            } else {
                Some(current)
            }
        }
        (Some(current), None) => Some(current),
    }
}

fn limit_has_data(limit: &LimitWindow) -> bool {
    limit.measure.is_some() || limit.resets_at.is_some()
}

pub fn default_providers() -> Vec<Box<dyn Provider>> {
    vec![
        Box::new(ClaudeCodeProvider),
        Box::new(CodexProvider),
        Box::new(CursorProvider),
    ]
}

fn detect_windows_homes(is_wsl: bool) -> Vec<PathBuf> {
    resolve_windows_homes(
        env::var_os("USERPROFILE"),
        is_wsl,
        Path::new("/mnt/c/Users"),
        env::var_os("USER"),
    )
}

/// Resolve the Windows-side home root(s) seen from WSL. Arg-injected (the raw
/// `USERPROFILE` and `USER` values, plus the Windows users directory) so the
/// *dispatch itself* — including the unset-vs-empty `USERPROFILE` rule below — is
/// unit-testable without mutating the process environment, mirroring how
/// [`codex_home_root`] takes its env value as a parameter.
fn resolve_windows_homes(
    userprofile: Option<std::ffi::OsString>,
    is_wsl: bool,
    users_dir: &Path,
    user: Option<std::ffi::OsString>,
) -> Vec<PathBuf> {
    // EXPLICIT MODE — `USERPROFILE` is *present*, even if it is empty.
    // A set `USERPROFILE` means "use exactly this Windows home, or none"; we never
    // auto-scan in that case. The unset-vs-empty distinction is load-bearing:
    //   * unset (`None`)        → real zero-config WSL → AUTO MODE below (scan).
    //   * set-but-empty (`""`)  → explicit "no Windows home" → returns empty, NO scan.
    // This is also what keeps `scripts/offline_acceptance.sh` hermetic: it runs every
    // command with `USERPROFILE=""`, so the scan never touches the real `/mnt/c` — the
    // harness's neutralizer doubles as the auto-detect off-switch, with no script edit
    // and no new env surface. A real WSL user leaves `USERPROFILE` unset, so they scan.
    if let Some(profile) = userprofile {
        return windows_profile_to_wsl_path(&PathBuf::from(profile))
            .into_iter()
            .collect();
    }
    // AUTO MODE — `USERPROFILE` unset.
    if !is_wsl {
        return Vec::new();
    }
    let mut homes = windows_profiles_with_logs(users_dir);
    if homes.is_empty() {
        // Strict superset of the old behavior: fall back to the legacy same-username
        // guess so we never resolve *fewer* roots than before (harmless if absent).
        homes.extend(user.map(|user| users_dir.join(user)));
    }
    homes
}

/// Windows user profiles under `users_dir` that actually hold AI-tool logs — the
/// evidence-based fix for WSL hosts where the Linux username differs from the Windows
/// profile name and `USERPROFILE` is unset. Pure and arg-injected so it is testable
/// against a fixture directory rather than the real `/mnt/c`. Degrades gracefully
/// (§9.2): an unreadable directory or entry is skipped, never an error or panic.
/// Sorted for deterministic ordering.
fn windows_profiles_with_logs(users_dir: &Path) -> Vec<PathBuf> {
    let Ok(entries) = fs::read_dir(users_dir) else {
        return Vec::new();
    };
    let mut profiles: Vec<PathBuf> = entries
        .flatten()
        .map(|entry| entry.path())
        .filter(|path| path.is_dir() && profile_has_logs(path))
        .collect();
    profiles.sort();
    profiles
}

/// Whether a Windows user profile directory holds any Claude Code, Codex, or Cursor
/// data — the same root subdirectories [`HostEnv::claude_roots`],
/// [`HostEnv::codex_roots`], and [`HostEnv::cursor_roots`] read (`.claude`,
/// `.config/claude`, `.codex`, `.cursor`). The `.cursor` arm lets the WSL scan
/// surface a Windows profile that has only Cursor installed.
fn profile_has_logs(profile: &Path) -> bool {
    profile.join(".claude").is_dir()
        || profile.join(".config").join("claude").is_dir()
        || profile.join(".codex").is_dir()
        || profile.join(".cursor").is_dir()
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

/// `CODEX_HOME` override for the Codex log root. Unlike Claude's comma-separated
/// `CLAUDE_CONFIG_DIR`, Codex's convention is a single directory, so this honors
/// exactly one path (an unset or empty value yields `None`). Pure; takes the raw
/// env value as an argument so it is testable without touching the environment.
fn codex_home_root(value: Option<std::ffi::OsString>) -> Option<PathBuf> {
    value
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
}

/// `CURSOR_DATA_DIR` override for the Cursor data root. Like Codex's `CODEX_HOME`,
/// the Cursor CLI's convention is a single directory, so this honors exactly one
/// path (an unset or empty value yields `None`). Pure; takes the raw env value as an
/// argument so it is testable without touching the environment.
fn cursor_data_dir_root(value: Option<std::ffi::OsString>) -> Option<PathBuf> {
    value
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
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
        // Sub-agent turns carry a top-level `isSidechain: true`. Absent/non-bool → false
        // (a mainline turn). Costroid keeps counting these; the FOCUS row annotates them.
        is_sidechain: value
            .get("isSidechain")
            .and_then(Value::as_bool)
            .unwrap_or(false),
    };
    has_any_tokens(&event).then_some(event)
}

/// De-duplication key for a Claude transcript entry: `(message.id, requestId)`.
///
/// Returns `Some` only when BOTH are present and non-empty — matching ccusage's
/// rule of only de-duping when a full key exists. Any entry missing either id is
/// keyless and must never be collapsed with another. Pure; never panics.
///
/// Real-log reconciliation note (verified vs ccusage on real logs, 2026-06):
/// mainline usage matches ccusage to the cent for every model. The only residual
/// is `claude-opus-4-8` landing ~0.08% under ccusage, located *entirely* in how
/// much sub-agent (sidechain) cache-read each tool retains after this de-dup —
/// both collapse re-logged subagent transcripts, just by slightly different
/// amounts. It is a known, benign methodology difference, not an under-count of
/// distinct billable turns; the provider invoice is the real ground truth
/// (Phase 2+). Do not rework this de-dup to chase ccusage parity — the cost core
/// is sacred (ARCHITECTURE.md §9.1).
fn claude_dedupe_key(value: &Value) -> Option<(String, String)> {
    let message_id = value.pointer("/message/id").and_then(Value::as_str)?;
    let request_id = value.get("requestId").and_then(Value::as_str)?;
    if message_id.is_empty() || request_id.is_empty() {
        return None;
    }
    Some((message_id.to_string(), request_id.to_string()))
}

/// Build one [`DataLocation`] from **every** Claude root that holds data, not just
/// the first. Claude transcripts can be split across `~/.claude/projects` and
/// `~/.config/claude/projects` (`CLAUDE_CONFIG_DIR`), plus a WSL Linux-vs-Windows
/// split — stopping at the first non-empty root silently under-counts the rest.
///
/// Files from all roots are merged into one `files` list; `dedupe_paths` removes
/// any lexically-identical path. Content that genuinely appears in two roots (the
/// same session copied across them) is collapsed later by the
/// `(message.id, requestId)` de-dup in [`ClaudeCodeProvider::parse_usage`], so
/// merging here cannot double-count.
///
/// `root` is set to the FIRST root that has data, in `claude_roots()` priority
/// order — exactly the root the old early-return would have picked — so
/// `claude_access_path` classification is unchanged on single-root machines and
/// deterministic across the merged set. Taking `roots` as a parameter makes this
/// testable with injected fixture roots, independent of the host environment.
fn discover_claude_location(roots: Vec<PathBuf>) -> Result<Option<DataLocation>, ProviderError> {
    let mut chosen_root: Option<PathBuf> = None;
    let mut files: Vec<PathBuf> = Vec::new();
    for root in roots {
        let root_files = collect_jsonl_files(ProviderId::ClaudeCode, &root.join("projects"))?;
        if root_files.is_empty() {
            continue;
        }
        if chosen_root.is_none() {
            chosen_root = Some(root);
        }
        files.extend(root_files);
    }
    let files = dedupe_paths(files);
    Ok(chosen_root.map(|root| DataLocation {
        provider: ProviderId::ClaudeCode,
        root,
        files,
    }))
}

/// Build one [`DataLocation`] from **every** Codex root that holds data, not just
/// the first. Codex sessions can be split across `~/.codex`, a `CODEX_HOME`
/// override, and (under WSL) one or more Windows-profile `.codex` roots discovered
/// by [`windows_profiles_with_logs`]. Stopping at the first non-empty root silently
/// dropped the rest — the WSL Windows-side gap this fixes — and contradicted
/// ARCHITECTURE.md §4's "merge all roots"; merging brings Codex into line with
/// [`discover_claude_location`], which already does this.
///
/// Cross-root de-dup is *session-level*. One rollout file is one session, named
/// `rollout-<timestamp>-<uuid>.jsonl` with a globally-unique session id, so the
/// file name identifies the session. Genuinely distinct sessions — the normal
/// case, e.g. a Linux machine's logs vs a Windows machine's — have distinct names
/// and are additive. The SAME session reached through two roots (a symlink or a
/// double-mount) has the identical file name under different absolute paths, which
/// `dedupe_paths` (lexical full-path) would NOT collapse; keying on the file name
/// counts that session exactly once. De-dup deliberately lives here, not in
/// [`CodexProvider::parse_usage`], which must keep counting every event it is
/// handed (see the `codex_usage_is_never_deduped` test).
///
/// `root` is the FIRST root with data, in `codex_roots()` priority order — the same
/// root the old early-return picked — kept for deterministic metadata. Takes
/// `roots` as a parameter so it is testable with injected fixture roots.
fn discover_codex_location(roots: Vec<PathBuf>) -> Result<Option<DataLocation>, ProviderError> {
    let mut chosen_root: Option<PathBuf> = None;
    let mut files: Vec<PathBuf> = Vec::new();
    let mut seen_sessions: BTreeSet<std::ffi::OsString> = BTreeSet::new();
    for root in roots {
        let root_files = collect_jsonl_files(ProviderId::Codex, &root.join("sessions"))?;
        if root_files.is_empty() {
            continue;
        }
        if chosen_root.is_none() {
            chosen_root = Some(root);
        }
        for file in root_files {
            // New session iff its file name has not been seen via an earlier root.
            // A file with no name (unexpected) is kept rather than silently dropped.
            let is_new_session = match file.file_name() {
                Some(name) => seen_sessions.insert(name.to_os_string()),
                None => true,
            };
            if is_new_session {
                files.push(file);
            }
        }
    }
    Ok(chosen_root.map(|root| DataLocation {
        provider: ProviderId::Codex,
        root,
        files,
    }))
}

/// Detect the Cursor CLI by **presence only** — Cursor keeps no token usage or
/// quota on disk (those are live server RPCs; ARCHITECTURE.md §4), so there is
/// nothing local to parse for cost. "Present" is the first `cursor_roots()` root
/// that is an existing directory, in priority order.
///
/// Unlike the Claude/Codex discoverers this NEVER enumerates the data tree: it only
/// ever names `cli-config.json` (the selected-model + logged-in signal read by
/// [`read_cursor_config`]). It deliberately does not touch `chats/`, `projects/`, or
/// `auth.json`, so chat content and credentials are never read — the metadata-only
/// guarantee (§8) enforced at the discovery layer. `files` holds `cli-config.json`
/// iff it exists, else is empty (the install is still "present"). Takes `roots` as a
/// parameter so it is testable with injected fixture roots; degrades to `Ok(None)`
/// when no root directory exists.
fn discover_cursor_location(roots: Vec<PathBuf>) -> Result<Option<DataLocation>, ProviderError> {
    for root in roots {
        if !root.is_dir() {
            continue;
        }
        let config = root.join("cli-config.json");
        let files = if config.is_file() {
            vec![config]
        } else {
            Vec::new()
        };
        return Ok(Some(DataLocation {
            provider: ProviderId::Cursor,
            root,
            files,
        }));
    }
    Ok(None)
}

/// What Costroid reads from a Cursor `cli-config.json` for its detected-status line:
/// the selected model and whether a session is logged in. Deliberately minimal —
/// never the auth token, email, or user id (PII), and never chat content.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CursorConfig {
    pub model_id: Option<String>,
    pub display_name: Option<String>,
    pub logged_in: bool,
}

/// Read the Cursor selected model and logged-in flag from the `cli-config.json` in
/// `loc.files`, if present. Pure metadata only: it reads `selectedModel.modelId` /
/// `model.modelId` / `model.displayName` and detects the *presence* of an `authInfo`
/// object (the logged-in signal) — it NEVER reads `auth.json`, never surfaces the
/// email/userId inside `authInfo`, and never touches chat content. Degrades
/// gracefully (§9.2): a missing or unreadable file, or malformed JSON, yields the
/// default (`model_id` `None`, not logged in) — "present, model unknown", never an
/// error.
pub fn read_cursor_config(loc: &DataLocation) -> CursorConfig {
    let Some(path) = loc.files.iter().find(|path| {
        path.file_name()
            .is_some_and(|name| name == "cli-config.json")
    }) else {
        return CursorConfig::default();
    };
    let Ok(contents) = fs::read_to_string(path) else {
        return CursorConfig::default();
    };
    let Ok(value) = serde_json::from_str::<Value>(&contents) else {
        return CursorConfig::default();
    };
    let model_id = value
        .pointer("/selectedModel/modelId")
        .and_then(Value::as_str)
        .or_else(|| value.pointer("/model/modelId").and_then(Value::as_str))
        .map(ToString::to_string);
    let display_name = value
        .pointer("/model/displayName")
        .and_then(Value::as_str)
        .map(ToString::to_string);
    let logged_in = value.get("authInfo").and_then(Value::as_object).is_some();
    CursorConfig {
        model_id,
        display_name,
        logged_in,
    }
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
            parsed.primary_limit = choose_limit(parsed.primary_limit, primary);
            parsed.secondary_limit = choose_limit(parsed.secondary_limit, secondary);
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
    // OpenAI/Codex `input_tokens` is the FULL prompt size and INCLUDES
    // `cached_input_tokens` (a subset of it). We price each meter once at its own
    // rate, so the input bucket must be the *uncached* remainder — otherwise the
    // cached tokens are billed twice (input rate + cache-read rate). Subtract with
    // `saturating_sub` so a malformed log where cached > input clamps to 0 rather
    // than panicking. NOTE: Anthropic `input_tokens` is cache-EXCLUSIVE, so the
    // Claude parser must NOT do this subtraction.
    let cached = number_u64(usage.get("cached_input_tokens"));
    let event = UsageEvent {
        tool: ProviderId::Codex,
        model: current_model
            .clone()
            .unwrap_or_else(|| "unknown".to_string()),
        timestamp,
        input_tokens: number_u64(usage.get("input_tokens")).saturating_sub(cached),
        output_tokens: number_u64(usage.get("output_tokens")),
        cache_read_tokens: cached,
        cache_write_tokens: 0,
        project: current_cwd.clone(),
        access_path,
        // Codex has no sub-agent/sidechain concept; its turns are always mainline.
        is_sidechain: false,
    };
    has_any_tokens(&event).then_some(event)
}

fn parse_codex_limits(value: &Value) -> Option<(Option<LimitWindow>, Option<LimitWindow>)> {
    let rate_limits = value.pointer("/payload/rate_limits")?;
    let plan = rate_limits.get("plan_type").and_then(Value::as_str);
    // captured_at is the rollout entry that carried these rate_limits; choose_limit
    // keeps the latest such entry, so the surviving window's freshness is its line's.
    let captured_at = codex_entry_timestamp(value).unwrap_or_else(epoch_utc);
    // Each window parses independently: an entry carrying only `primary` (or only
    // `secondary`) still surfaces the window that IS present, never dropping data.
    let primary = parse_codex_limit(
        rate_limits.get("primary"),
        LimitKind::FiveHour,
        plan,
        captured_at,
    );
    let secondary = parse_codex_limit(
        rate_limits.get("secondary"),
        LimitKind::Weekly,
        plan,
        captured_at,
    );
    Some((primary, secondary))
}

/// Timestamp of a Codex rollout line: the top-level `timestamp`, else
/// `payload.timestamp`. Pure; `None` when neither is a parseable RFC3339 string.
fn codex_entry_timestamp(value: &Value) -> Option<DateTime<Utc>> {
    value
        .get("timestamp")
        .and_then(Value::as_str)
        .and_then(parse_rfc3339)
        .or_else(|| {
            value
                .pointer("/payload/timestamp")
                .and_then(Value::as_str)
                .and_then(parse_rfc3339)
        })
}

fn parse_codex_limit(
    value: Option<&Value>,
    kind: LimitKind,
    plan: Option<&str>,
    captured_at: DateTime<Utc>,
) -> Option<LimitWindow> {
    let value = value?;
    let measure = value
        .get("used_percent")
        .and_then(Value::as_f64)
        // Provider logs are untrusted input (ARCHITECTURE): sanitize the RAW
        // percentage before ÷100, mirroring Claude's guard — an out-of-range value
        // yields no measure (core degrades it to Estimated/Unavailable), never a
        // confident wrong meter.
        .filter(|pct| (0.0..=100.0).contains(pct))
        .map(|pct| LimitMeasure::TokenFraction(pct / 100.0));
    let resets_at = value
        .get("resets_at")
        .and_then(Value::as_i64)
        .and_then(epoch_seconds);
    Some(LimitWindow {
        tool: ProviderId::Codex,
        plan: plan.map(ToString::to_string),
        kind,
        measure,
        resets_at,
        captured_at,
        status: LimitStatus::Verified,
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

/// Resolve the sanctioned rate-limits cache path
/// `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json`. The cache
/// is Costroid's own no-secret state (written by `setup-statusline`, T5), so it always
/// lives Linux-side — no Windows-path handling needed. `None` when neither
/// `XDG_STATE_HOME` nor `HOME` resolves a base directory.
///
/// Public so the T5 capture *writer* (`apps/cli`) resolves the exact same path the T4
/// *reader* above uses — a single source of truth keeps the two from drifting.
pub fn claude_rate_limits_cache_path() -> Option<PathBuf> {
    let base = env::var_os("XDG_STATE_HOME")
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
        .or_else(|| {
            env::var_os("HOME")
                .map(PathBuf::from)
                // Same emptiness guard as XDG_STATE_HOME: an empty-but-set HOME must
                // yield None, never a cwd-relative ".local/state/…" path.
                .filter(|path| !path.as_os_str().is_empty())
                .map(|home| home.join(".local").join("state"))
        })?;
    Some(base.join("costroid").join("claude-rate-limits.json"))
}

/// Read + sanitize the sanctioned Claude rate-limits cache (ARCHITECTURE). The
/// captured field is UNTRUSTED input: a missing/unreadable/malformed cache degrades to
/// two `Unavailable` windows — never an error, never a crash. Always returns exactly
/// two windows (`FiveHour`, `Weekly`). The status set here is PROVISIONAL; the core
/// cross-check may demote a `Verified` reading to `Unverified`.
fn read_claude_rate_limits(path: Option<&Path>) -> Vec<LimitWindow> {
    let cache = path
        .and_then(|path| fs::read(path).ok())
        .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok());
    match cache {
        Some(cache) => {
            // captured_at is the snippet's write time read from the cache; absent or
            // unparseable falls back to the epoch sentinel (no observation instant).
            let captured_at = cache
                .get("captured_at")
                .and_then(Value::as_str)
                .and_then(parse_rfc3339)
                .unwrap_or_else(epoch_utc);
            vec![
                claude_limit_window(cache.get("five_hour"), LimitKind::FiveHour, captured_at),
                claude_limit_window(cache.get("seven_day"), LimitKind::Weekly, captured_at),
            ]
        }
        None => vec![
            unavailable_limit(ProviderId::ClaudeCode, LimitKind::FiveHour),
            unavailable_limit(ProviderId::ClaudeCode, LimitKind::Weekly),
        ],
    }
}

/// Sanitize one window of the cache into a provisional [`LimitWindow`]. ARCHITECTURE
/// §9.2 — sanitize the RAW `used_percentage` BEFORE ÷100; order matters. Three failure
/// modes yield no measure and a provisional `Unavailable` (never a confident wrong
/// number):
/// * an absent window key,
/// * an out-of-range percentage (`>100` or `<0` — the 900% bug),
/// * the poisoned-epoch leak, where `used_percentage` equals the `resets_at` epoch.
///
/// Only an in-range reading survives to `Verified` carrying a `TokenFraction`.
/// `resets_at` is parsed defensively as either an integer epoch or an RFC3339 string
/// (both appear across Claude Code versions, ARCHITECTURE).
fn claude_limit_window(
    window: Option<&Value>,
    kind: LimitKind,
    captured_at: DateTime<Utc>,
) -> LimitWindow {
    let Some(window) = window else {
        return unavailable_limit(ProviderId::ClaudeCode, kind);
    };
    let raw_resets = window.get("resets_at");
    let resets_at = raw_resets.and_then(parse_reset_stamp);
    let raw_pct = window.get("used_percentage").and_then(Value::as_f64);
    // The poisoned-epoch leak surfaces as used_percentage == resets_at (the same large
    // integer in both fields). Compare the RAW numbers, before any scaling.
    let poisoned = match (raw_pct, raw_resets.and_then(Value::as_f64)) {
        (Some(pct), Some(reset)) => pct == reset,
        _ => false,
    };
    let measure = match raw_pct {
        Some(pct) if !poisoned && (0.0..=100.0).contains(&pct) => {
            Some(LimitMeasure::TokenFraction(pct / 100.0))
        }
        _ => None,
    };
    let status = if measure.is_some() {
        LimitStatus::Verified
    } else {
        LimitStatus::Unavailable
    };
    LimitWindow {
        tool: ProviderId::ClaudeCode,
        plan: None,
        kind,
        measure,
        resets_at,
        captured_at,
        status,
        label: (status == LimitStatus::Unavailable)
            .then(|| "no usable reading in the statusline cache".to_string()),
    }
}

/// Parse a Claude `resets_at` value defensively: an integer epoch (reusing
/// [`epoch_seconds`]) first, then an RFC3339 string. Both forms appear across Claude
/// Code versions (ARCHITECTURE). `None` when it is neither.
fn parse_reset_stamp(value: &Value) -> Option<DateTime<Utc>> {
    if let Some(epoch) = value.as_i64() {
        return epoch_seconds(epoch);
    }
    value.as_str().and_then(parse_rfc3339)
}

fn unavailable_limit(provider: ProviderId, kind: LimitKind) -> LimitWindow {
    // A descriptive, per-provider reason (rendered as "unavailable: <label>") instead
    // of the redundant "unavailable: unavailable"; the Claude wording doubles as
    // onboarding guidance toward the capture setup.
    // ASCII-only wording: this string flows verbatim into every render mode,
    // including the Ascii fallback for non-UTF-8 terminals.
    let label = match provider {
        ProviderId::ClaudeCode => "no captured reading; run `costroid setup-statusline`",
        ProviderId::Codex => "no rate-limit data in local rollout logs",
        ProviderId::Cursor => "no sanctioned source",
    };
    LimitWindow {
        tool: provider,
        plan: None,
        kind,
        measure: None,
        resets_at: None,
        captured_at: epoch_utc(),
        status: LimitStatus::Unavailable,
        label: Some(label.to_string()),
    }
}

/// The UNIX epoch as a `captured_at` sentinel for windows with no real reading
/// (the `Unavailable` case). Infallible — never a panic in library code.
fn epoch_utc() -> DateTime<Utc> {
    Utc.timestamp_nanos(0)
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
    use std::ffi::OsString;

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

    fn sample_cloud_usage_event() -> CloudUsageEvent {
        CloudUsageEvent {
            timestamp: Utc
                .timestamp_opt(1_700_000_000, 0)
                .single()
                .unwrap_or_default(),
            service_name: "Bedrock".to_string(),
            service_provider_name: "AWS".to_string(),
            model: Some("anthropic.claude-3".to_string()),
            token_count: Some(12_345),
            billed_cost: Some("0.0123".to_string()),
            effective_cost: Some("0.0123".to_string()),
            list_cost: Some("0.0123".to_string()),
            contracted_cost: Some("0.0123".to_string()),
            sku_price_id: Some("anthropic.claude-3-output".to_string()),
            pricing_category: Some("Standard".to_string()),
            pricing_quantity: Some("12345".to_string()),
            pricing_unit: Some("Tokens".to_string()),
            list_unit_price: Some("0.000001".to_string()),
            contracted_unit_price: Some("0.000001".to_string()),
            pricing_currency: Some("USD".to_string()),
            consumed_unit: Some("Tokens".to_string()),
        }
    }

    fn sample_local_run_event() -> LocalRunEvent {
        LocalRunEvent {
            timestamp: Utc
                .timestamp_opt(1_700_000_000, 0)
                .single()
                .unwrap_or_default(),
            model: "llama-3-8b".to_string(),
            runtime_kind: "ollama".to_string(),
            tokens_in: 100,
            tokens_out: 200,
            run_seconds: 4.5,
            avg_power_watts: 75.0,
            measurement_mode: "estimated".to_string(),
        }
    }

    /// R4 (Cardinal Rule) structural guard: field-exhaustively destructure the
    /// metadata-only lane events with NO `..` rest-pattern, so any added field
    /// breaks compilation here and forces a conscious review. No field may ever
    /// name or hold prompt/completion/response/content/message/text — Costroid
    /// meters cost and quota, never conversation content.
    #[test]
    fn lane_events_stay_metadata_only_r4_guard() {
        let CloudUsageEvent {
            timestamp: _,
            service_name: _,
            service_provider_name: _,
            model: _,
            token_count: _,
            billed_cost: _,
            // Foreign pricing detail (T4) — all bounded metadata strings, never content.
            effective_cost: _,
            list_cost: _,
            contracted_cost: _,
            sku_price_id: _,
            pricing_category: _,
            pricing_quantity: _,
            pricing_unit: _,
            list_unit_price: _,
            contracted_unit_price: _,
            pricing_currency: _,
            consumed_unit: _,
        } = sample_cloud_usage_event();

        let LocalRunEvent {
            timestamp: _,
            model: _,
            runtime_kind: _,
            tokens_in: _,
            tokens_out: _,
            run_seconds: _,
            avg_power_watts: _,
            measurement_mode: _,
        } = sample_local_run_event();
    }

    #[test]
    fn cloud_usage_event_round_trips_through_json() {
        let original = sample_cloud_usage_event();
        let Ok(json) = serde_json::to_string(&original) else {
            panic!("CloudUsageEvent should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<CloudUsageEvent>(&json) else {
            panic!("CloudUsageEvent should deserialize from JSON");
        };
        assert_eq!(original, restored);
    }

    #[test]
    fn local_run_event_round_trips_through_json() {
        let original = sample_local_run_event();
        let Ok(json) = serde_json::to_string(&original) else {
            panic!("LocalRunEvent should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<LocalRunEvent>(&json) else {
            panic!("LocalRunEvent should deserialize from JSON");
        };
        assert_eq!(original, restored);
    }

    #[test]
    fn canonical_event_round_trips_through_json() {
        let cloud = CanonicalEvent::Cloud(Box::new(sample_cloud_usage_event()));
        let Ok(json) = serde_json::to_string(&cloud) else {
            panic!("CanonicalEvent::Cloud should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<CanonicalEvent>(&json) else {
            panic!("CanonicalEvent::Cloud should deserialize from JSON");
        };
        assert_eq!(cloud, restored);

        let local = CanonicalEvent::Local(sample_local_run_event());
        let Ok(json) = serde_json::to_string(&local) else {
            panic!("CanonicalEvent::Local should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<CanonicalEvent>(&json) else {
            panic!("CanonicalEvent::Local should deserialize from JSON");
        };
        assert_eq!(local, restored);

        let tool = CanonicalEvent::Tool(UsageEvent {
            tool: ProviderId::ClaudeCode,
            model: "claude-opus".to_string(),
            timestamp: Utc
                .timestamp_opt(1_700_000_000, 0)
                .single()
                .unwrap_or_default(),
            input_tokens: 10,
            output_tokens: 20,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: None,
            access_path: AccessPath::Subscription,
            is_sidechain: false,
        });
        let Ok(json) = serde_json::to_string(&tool) else {
            panic!("CanonicalEvent::Tool should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<CanonicalEvent>(&json) else {
            panic!("CanonicalEvent::Tool should deserialize from JSON");
        };
        assert_eq!(tool, restored);
    }

    /// R14: the quota model is generalized to Daily/Monthly/BillingCycle and a
    /// dollar-denominated `Spend` measure. These round-trips lock the EXACT wire
    /// forms (read from the `#[serde(rename_all = ...)]` attrs) so a future rename
    /// can't silently change the on-disk/wire format the store-replay path reads.
    fn sample_limit_window(kind: LimitKind, measure: LimitMeasure) -> LimitWindow {
        LimitWindow {
            tool: ProviderId::ClaudeCode,
            plan: Some("max".to_string()),
            kind,
            measure: Some(measure),
            resets_at: Utc.timestamp_opt(1_700_003_600, 0).single(),
            captured_at: Utc
                .timestamp_opt(1_700_000_000, 0)
                .single()
                .unwrap_or_default(),
            status: LimitStatus::Verified,
            label: Some("primary".to_string()),
        }
    }

    fn round_trip_limit_window(window: &LimitWindow) -> LimitWindow {
        let Ok(json) = serde_json::to_string(window) else {
            panic!("LimitWindow should serialize to JSON");
        };
        let Ok(restored) = serde_json::from_str::<LimitWindow>(&json) else {
            panic!("LimitWindow should deserialize from JSON");
        };
        restored
    }

    #[test]
    fn limit_window_round_trips_for_generalized_kinds_r14() {
        for kind in [
            LimitKind::Daily,
            LimitKind::Monthly,
            LimitKind::BillingCycle,
        ] {
            let original = sample_limit_window(kind, LimitMeasure::TokenFraction(0.5));
            assert_eq!(original, round_trip_limit_window(&original));
        }
    }

    #[test]
    fn limit_window_round_trips_for_both_measures_r14() {
        let fraction = sample_limit_window(LimitKind::Daily, LimitMeasure::TokenFraction(0.42));
        assert_eq!(fraction, round_trip_limit_window(&fraction));

        let spend = sample_limit_window(
            LimitKind::BillingCycle,
            LimitMeasure::Spend {
                used_usd: Decimal::new(1234, 2),
                included_usd: Some(Decimal::new(2000, 2)),
            },
        );
        assert_eq!(spend, round_trip_limit_window(&spend));

        let spend_no_allowance = sample_limit_window(
            LimitKind::Monthly,
            LimitMeasure::Spend {
                used_usd: Decimal::new(500, 2),
                included_usd: None,
            },
        );
        assert_eq!(
            spend_no_allowance,
            round_trip_limit_window(&spend_no_allowance)
        );
    }

    #[test]
    fn limit_kind_wire_tokens_are_kebab_case_r14() {
        let cases = [
            (LimitKind::FiveHour, "\"five-hour\""),
            (LimitKind::Weekly, "\"weekly\""),
            (LimitKind::Daily, "\"daily\""),
            (LimitKind::Monthly, "\"monthly\""),
            (LimitKind::BillingCycle, "\"billing-cycle\""),
        ];
        for (kind, expected) in cases {
            let Ok(json) = serde_json::to_string(&kind) else {
                panic!("LimitKind should serialize to JSON");
            };
            assert_eq!(json, expected, "LimitKind wire token drifted");
            let Ok(restored) = serde_json::from_str::<LimitKind>(&json) else {
                panic!("LimitKind should deserialize from JSON");
            };
            assert_eq!(kind, restored);
        }
    }

    #[test]
    fn limit_measure_wire_tokens_are_snake_case_r14() {
        let Ok(fraction_json) = serde_json::to_string(&LimitMeasure::TokenFraction(0.25)) else {
            panic!("LimitMeasure should serialize to JSON");
        };
        assert!(
            fraction_json.contains("\"token_fraction\""),
            "TokenFraction wire token drifted: {fraction_json}"
        );

        let spend = LimitMeasure::Spend {
            used_usd: Decimal::new(1234, 2),
            included_usd: Some(Decimal::new(2000, 2)),
        };
        let Ok(spend_json) = serde_json::to_string(&spend) else {
            panic!("LimitMeasure should serialize to JSON");
        };
        assert!(
            spend_json.contains("\"spend\""),
            "Spend wire token drifted: {spend_json}"
        );
        assert!(
            spend_json.contains("\"used_usd\""),
            "Spend.used_usd field name drifted: {spend_json}"
        );
        assert!(
            spend_json.contains("\"included_usd\""),
            "Spend.included_usd field name drifted: {spend_json}"
        );

        let Ok(restored) = serde_json::from_str::<LimitMeasure>(&spend_json) else {
            panic!("LimitMeasure should deserialize from JSON");
        };
        assert_eq!(spend, restored);
    }

    #[test]
    fn wsl_roots_include_linux_and_windows_candidates() {
        let env = HostEnv::new(
            PathBuf::from("/home/example"),
            vec![PathBuf::from("/mnt/c/Users/example")],
            true,
        );
        let claude_roots = env.claude_roots();
        let codex_roots = env.codex_roots();
        let cursor_roots = env.cursor_roots();

        assert!(claude_roots.contains(&PathBuf::from("/home/example/.config/claude")));
        assert!(claude_roots.contains(&PathBuf::from("/mnt/c/Users/example/.claude")));
        assert!(codex_roots.contains(&PathBuf::from("/home/example/.codex")));
        assert!(codex_roots.contains(&PathBuf::from("/mnt/c/Users/example/.codex")));
        assert!(cursor_roots.contains(&PathBuf::from("/home/example/.cursor")));
        assert!(cursor_roots.contains(&PathBuf::from("/mnt/c/Users/example/.cursor")));
    }

    #[test]
    fn codex_home_root_honors_single_path_and_ignores_empty() {
        assert_eq!(
            codex_home_root(Some(std::ffi::OsString::from("/custom/codex"))),
            Some(PathBuf::from("/custom/codex")),
        );
        assert_eq!(codex_home_root(Some(std::ffi::OsString::from(""))), None);
        assert_eq!(codex_home_root(None), None);
    }

    #[test]
    fn codex_home_root_takes_priority_over_default_codex_dir() {
        let env = HostEnv::new(
            PathBuf::from("/home/example"),
            vec![PathBuf::from("/mnt/c/Users/example")],
            true,
        );

        // CODEX_HOME comes first, ahead of the ~/.codex default and the WSL
        // Windows .codex; the defaults are still present (merged, not replaced).
        let roots = env.codex_roots_from(Some(PathBuf::from("/custom/codex")));
        assert_eq!(roots.first(), Some(&PathBuf::from("/custom/codex")));
        assert!(roots.contains(&PathBuf::from("/home/example/.codex")));
        assert!(roots.contains(&PathBuf::from("/mnt/c/Users/example/.codex")));

        // With no override, the default ordering is unchanged.
        let roots = env.codex_roots_from(None);
        assert_eq!(roots.first(), Some(&PathBuf::from("/home/example/.codex")));
        assert!(!roots.contains(&PathBuf::from("/custom/codex")));
    }

    // The log-bearing Windows-profile fixtures, in the sorted order the scan must
    // return them (excludes `no-logs` and the stray `desktop.ini` file). `with-cursor`
    // holds only `.cursor`, exercising the Cursor arm of `profile_has_logs`.
    fn expected_scanned_profiles(users_dir: &Path) -> Vec<PathBuf> {
        vec![
            users_dir.join("with-claude"),
            users_dir.join("with-codex"),
            users_dir.join("with-config-claude"),
            users_dir.join("with-cursor"),
        ]
    }

    #[test]
    fn windows_scan_finds_only_profiles_with_logs() {
        // The username-mismatch core case: scan a Windows users dir and surface exactly
        // the profiles that actually hold .claude / .config/claude / .codex — sorted,
        // with non-log profiles (`no-logs`) and non-directory entries (`desktop.ini`)
        // excluded. This is what makes discovery work when the WSL username differs
        // from the Windows profile name and USERPROFILE is unset.
        let users_dir = fixture_path(&["discovery", "windows-users"]);
        let found = windows_profiles_with_logs(&users_dir);
        assert_eq!(found, expected_scanned_profiles(&users_dir));
    }

    #[test]
    fn windows_scan_missing_dir_is_graceful_noop() {
        // §9.2: scanning a directory that does not exist must degrade to empty, never
        // error or panic.
        let found = windows_profiles_with_logs(&fixture_path(&["discovery", "does-not-exist"]));
        assert!(found.is_empty());
    }

    #[test]
    fn resolve_empty_userprofile_suppresses_scan() {
        // A set-but-empty USERPROFILE is explicit "no Windows home": it must NOT scan,
        // even when pointed at a directory full of scannable profiles. This is the
        // behavior `scripts/offline_acceptance.sh` (USERPROFILE="") leans on to stay
        // hermetic — pinned here so it can't silently regress.
        let users_dir = fixture_path(&["discovery", "windows-users"]);
        let homes = resolve_windows_homes(
            Some(OsString::from("")),
            true,
            &users_dir,
            Some(OsString::from("eren")),
        );
        assert!(
            homes.is_empty(),
            "set-but-empty USERPROFILE must never scan"
        );
    }

    #[test]
    fn resolve_unset_userprofile_on_wsl_scans() {
        // Real zero-config WSL: USERPROFILE unset → scan the users dir and merge the
        // profiles that hold logs. Because the scan is non-empty, the legacy same-name
        // $USER guess is NOT appended.
        let users_dir = fixture_path(&["discovery", "windows-users"]);
        let homes = resolve_windows_homes(None, true, &users_dir, Some(OsString::from("eren")));
        assert_eq!(homes, expected_scanned_profiles(&users_dir));
        assert!(!homes.contains(&users_dir.join("eren")));
    }

    #[test]
    fn resolve_unset_userprofile_empty_dir_falls_back_to_user_guess() {
        // WSL, USERPROFILE unset, but the scan finds nothing (dir absent): fall back to
        // the legacy `/mnt/c/Users/$USER` guess so behavior is a strict superset of the
        // old code — never fewer roots than before.
        let users_dir = fixture_path(&["discovery", "does-not-exist"]);
        let homes = resolve_windows_homes(None, true, &users_dir, Some(OsString::from("eren")));
        assert_eq!(homes, vec![users_dir.join("eren")]);
    }

    #[test]
    fn resolve_set_userprofile_uses_it_and_skips_scan() {
        // USERPROFILE set to a real Windows path resolves to its /mnt mapping and never
        // scans — the no-regression case for hosts that already set USERPROFILE.
        let users_dir = fixture_path(&["discovery", "windows-users"]);
        let homes = resolve_windows_homes(
            Some(OsString::from("C:\\Users\\foo")),
            true,
            &users_dir,
            Some(OsString::from("eren")),
        );
        assert_eq!(homes, vec![PathBuf::from("/mnt/c/Users/foo")]);
    }

    #[test]
    fn resolve_unset_userprofile_not_wsl_is_empty() {
        // Not WSL and no USERPROFILE: there is no Windows side to find.
        let homes = resolve_windows_homes(
            None,
            false,
            &fixture_path(&["discovery", "windows-users"]),
            Some(OsString::from("eren")),
        );
        assert!(homes.is_empty());
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

        assert_eq!(usage.len(), 1);
        assert_eq!(usage[0].model, "claude-sonnet-example");
        // Anthropic `input_tokens` is cache-EXCLUSIVE: cache_read_input_tokens and
        // cache_creation_input_tokens are separate, additive fields. So the Claude
        // parser stores input_tokens verbatim and does NOT subtract cache — the
        // mirror of the Codex fix would over-correct here. This asserts input stays
        // the raw exclusive value (10) and cache stays separate (30 read / 40 write).
        assert_eq!(usage[0].input_tokens, 10);
        assert_eq!(usage[0].output_tokens, 20);
        assert_eq!(usage[0].cache_read_tokens, 30);
        assert_eq!(usage[0].cache_write_tokens, 40);

        // Quota now comes from the sanctioned rate_limits cache, never the
        // transcripts (`loc`). With no cache present — None and a nonexistent path
        // alike — both windows degrade to Unavailable, never a confident wrong number.
        // (Routed through the pure reader, not the env-resolving `parse_limits`, so the
        // test never reads a developer's real cache — golden rule: no real user data.)
        for limits in [
            read_claude_rate_limits(None),
            read_claude_rate_limits(Some(&fixture_path(&["claude-code", "does-not-exist.json"]))),
        ] {
            assert_eq!(limits.len(), 2);
            assert!(limits.iter().all(|limit| limit.measure.is_none()));
            assert!(limits.iter().all(|limit| limit.resets_at.is_none()));
            assert!(limits
                .iter()
                .all(|limit| limit.status == LimitStatus::Unavailable));
            assert!(limits.iter().all(|limit| limit.captured_at == epoch_utc()));
        }
    }

    /// Read a Claude rate-limits fixture cache and find the parsed window for `kind`.
    fn claude_cache_window(fixture: &str, kind: LimitKind) -> LimitWindow {
        let limits = read_claude_rate_limits(Some(&fixture_path(&["claude-code", fixture])));
        assert_eq!(limits.len(), 2, "always exactly two windows");
        match limits.into_iter().find(|limit| limit.kind == kind) {
            Some(limit) => limit,
            None => panic!("{kind:?} window should be present"),
        }
    }

    #[test]
    fn claude_cache_happy_path_is_verified_with_fraction() {
        // The positive path: an in-range reading survives to Verified, carrying the
        // token fraction, the parsed reset, and the cache's captured_at.
        let five = claude_cache_window("rate-limits-happy.json", LimitKind::FiveHour);
        assert_eq!(five.status, LimitStatus::Verified);
        assert!(close_to(token_fraction(&five), 0.78));
        assert!(five.resets_at.is_some());
        assert_ne!(five.captured_at, epoch_utc());

        let weekly = claude_cache_window("rate-limits-happy.json", LimitKind::Weekly);
        assert_eq!(weekly.status, LimitStatus::Verified);
        assert!(close_to(token_fraction(&weekly), 0.415));
    }

    #[test]
    fn claude_cache_impossible_percentage_is_sanitized_out() {
        // The 900% bug: a raw percentage > 100 is out of range → no measure, status
        // Unavailable. Sanitized BEFORE ÷100 so it never becomes a 9.0 fraction.
        let window = claude_cache_window("rate-limits-impossible-900.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Unavailable);
        assert!(window.measure.is_none());
    }

    #[test]
    fn claude_cache_poisoned_epoch_is_sanitized_out() {
        // The poisoned-epoch leak: used_percentage == resets_at (the same epoch) →
        // recognized as a leak → no measure, Unavailable. Never a ~178000000% reading.
        let window = claude_cache_window("rate-limits-poisoned-epoch.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Unavailable);
        assert!(window.measure.is_none());
    }

    #[test]
    fn claude_cache_false_100_is_provisionally_verified() {
        // A flat 100% (the #31820 false-in-range) is in range, so the PROVIDER keeps
        // it Verified with a full fraction — the provider cannot see usage volume.
        // The core cross-check (which can) demotes it to Unverified; see the core test.
        let window = claude_cache_window("rate-limits-false-100.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Verified);
        assert!(close_to(token_fraction(&window), 1.0));
    }

    #[test]
    fn claude_cache_absent_window_key_is_unavailable() {
        // The cache is present and valid but omits five_hour → that window is
        // Unavailable while the present seven_day window parses to Verified.
        let five = claude_cache_window("rate-limits-absent.json", LimitKind::FiveHour);
        assert_eq!(five.status, LimitStatus::Unavailable);
        assert!(five.measure.is_none());

        let weekly = claude_cache_window("rate-limits-absent.json", LimitKind::Weekly);
        assert_eq!(weekly.status, LimitStatus::Verified);
        assert!(close_to(token_fraction(&weekly), 0.22));
    }

    #[test]
    fn claude_cache_stale_reset_parses_as_past_epoch() {
        // resets_at in the past parses fine here (the provider does not judge
        // staleness — that is the core's age-out against generated_at). The reading is
        // in range, so it arrives Verified with a past reset for core to age out.
        let window = claude_cache_window("rate-limits-stale.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Verified);
        let resets_at = match window.resets_at {
            Some(value) => value,
            None => panic!("stale fixture should carry a parseable past reset"),
        };
        assert!(resets_at < epoch_seconds(1_800_000_000).unwrap_or_else(epoch_utc));
    }

    #[test]
    fn claude_cache_iso_resets_parses_rfc3339() {
        // resets_at as an ISO-8601 string (some Claude Code versions) parses via the
        // RFC3339 fallback, not just integer epochs.
        let window = claude_cache_window("rate-limits-iso-resets.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Verified);
        assert!(window.resets_at.is_some());
    }

    #[test]
    fn claude_cache_inrange_poisoned_equality_is_sanitized_out() {
        // Pins the used_percentage == resets_at equality guard INDEPENDENTLY of the
        // range check: both fields read 50 (in range), so only the equality comparison
        // can catch the leak. (The poisoned-epoch fixture's value also fails the >100
        // check, so it alone could not detect a deleted equality guard.)
        let five = claude_cache_window("rate-limits-poisoned-inrange.json", LimitKind::FiveHour);
        assert_eq!(five.status, LimitStatus::Unavailable);
        assert!(five.measure.is_none());

        // The untouched sibling window still parses.
        let weekly = claude_cache_window("rate-limits-poisoned-inrange.json", LimitKind::Weekly);
        assert_eq!(weekly.status, LimitStatus::Verified);
    }

    #[test]
    fn claude_cache_negative_percentage_is_sanitized_out() {
        // The documented `<0` half of the out-of-range sanitize (ARCHITECTURE).
        let window = claude_cache_window("rate-limits-negative.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Unavailable);
        assert!(window.measure.is_none());
    }

    #[test]
    fn claude_cache_non_numeric_percentage_is_sanitized_out() {
        // A string "78" is not a number → no measure (`Value::as_f64` is the gate).
        let window = claude_cache_window("rate-limits-string-pct.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Unavailable);
        assert!(window.measure.is_none());
    }

    #[test]
    fn claude_cache_malformed_json_degrades_to_unavailable() {
        // A torn/truncated cache (e.g. a crashed writer) degrades to two Unavailable
        // windows — never an error, never a crash.
        let limits = read_claude_rate_limits(Some(&fixture_path(&[
            "claude-code",
            "rate-limits-malformed.json",
        ])));
        assert_eq!(limits.len(), 2);
        assert!(limits
            .iter()
            .all(|limit| limit.status == LimitStatus::Unavailable));
        assert!(limits.iter().all(|limit| limit.measure.is_none()));
    }

    #[test]
    fn claude_cache_missing_captured_at_keeps_reading_with_epoch_sentinel() {
        // A present cache whose captured_at is missing keeps its Verified reading but
        // carries the epoch sentinel — the render layer then discloses "capture time
        // unknown" instead of stamping a bogus "as of 00:00" (see the render test).
        let window = claude_cache_window("rate-limits-no-captured-at.json", LimitKind::FiveHour);
        assert_eq!(window.status, LimitStatus::Verified);
        assert_eq!(window.captured_at, epoch_utc());
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
        // Codex `input_tokens` (1200) INCLUDES the cached subset (300); the parser
        // subtracts so the input bucket is the uncached remainder, billed once at
        // the input rate while the 300 cached tokens are billed once at the
        // cache-read rate. Regression guard for the cache-read double-count.
        assert_eq!(usage[0].input_tokens, 900);
        assert_eq!(usage[0].cache_read_tokens, 300);
        assert_eq!(limits.len(), 2);
        // Codex windows are Verified TokenFraction readings, each stamped with the
        // rollout line's captured_at (the rate_limits-bearing entry, not the epoch).
        assert!(limits
            .iter()
            .all(|limit| limit.status == LimitStatus::Verified));
        assert!(limits.iter().all(|limit| limit.captured_at != epoch_utc()));
        assert!(limits.iter().any(
            |limit| limit.kind == LimitKind::FiveHour && close_to(token_fraction(limit), 0.425)
        ));
        assert!(limits.iter().any(
            |limit| limit.kind == LimitKind::Weekly && close_to(token_fraction(limit), 0.1825)
        ));
    }

    /// Parse a synthetic Codex rollout entry (inline JSON, never real user data).
    fn codex_limits_entry(json: &str) -> Value {
        match serde_json::from_str(json) {
            Ok(value) => value,
            Err(err) => panic!("synthetic codex entry should parse: {err}"),
        }
    }

    #[test]
    fn codex_out_of_range_used_percent_is_sanitized_out() {
        // Provider logs are untrusted input (ARCHITECTURE): a corrupt out-of-range
        // used_percent must never become a confident Verified "900%"/negative meter —
        // the same raw-range guard Claude's identically-shaped field gets.
        let entry = codex_limits_entry(
            r#"{"timestamp":"2026-06-02T09:00:00Z","payload":{"rate_limits":{
                "primary":{"used_percent":900,"resets_at":1781000000},
                "secondary":{"used_percent":-5,"resets_at":1781400000}}}}"#,
        );
        let (primary, secondary) = match parse_codex_limits(&entry) {
            Some(pair) => pair,
            None => panic!("entry carries rate_limits"),
        };
        let primary = match primary {
            Some(window) => window,
            None => panic!("primary window present"),
        };
        let secondary = match secondary {
            Some(window) => window,
            None => panic!("secondary window present"),
        };
        assert!(primary.measure.is_none(), "900 must be sanitized out");
        assert!(secondary.measure.is_none(), "-5 must be sanitized out");
        // The reset stamp itself still parses; core degrades the measure-less window.
        assert!(primary.resets_at.is_some());
    }

    #[test]
    fn codex_boundary_used_percent_survives() {
        // Exactly 0 and exactly 100 are in range and must survive (the false-100 class
        // is a Claude-cache phenomenon; Codex rollout logs are trusted on arrival).
        let entry = codex_limits_entry(
            r#"{"timestamp":"2026-06-02T09:00:00Z","payload":{"rate_limits":{
                "primary":{"used_percent":100,"resets_at":1781000000},
                "secondary":{"used_percent":0,"resets_at":1781400000}}}}"#,
        );
        let (primary, secondary) = match parse_codex_limits(&entry) {
            Some(pair) => pair,
            None => panic!("entry carries rate_limits"),
        };
        let primary = match primary {
            Some(window) => window,
            None => panic!("primary window present"),
        };
        let secondary = match secondary {
            Some(window) => window,
            None => panic!("secondary window present"),
        };
        assert!(close_to(token_fraction(&primary), 1.0));
        assert!(close_to(token_fraction(&secondary), 0.0));
    }

    #[test]
    fn codex_lone_primary_window_still_surfaces() {
        // An entry carrying only `primary` keeps the window that IS present — the two
        // windows parse independently (no all-or-nothing coupling).
        let entry = codex_limits_entry(
            r#"{"timestamp":"2026-06-02T09:00:00Z","payload":{"rate_limits":{
                "primary":{"used_percent":42.5,"resets_at":1781000000}}}}"#,
        );
        let (primary, secondary) = match parse_codex_limits(&entry) {
            Some(pair) => pair,
            None => panic!("entry carries rate_limits"),
        };
        let primary = match primary {
            Some(window) => window,
            None => panic!("lone primary must surface"),
        };
        assert!(close_to(token_fraction(&primary), 0.425));
        assert!(secondary.is_none());
    }

    #[test]
    fn choose_limit_keeps_latest_reading_not_scan_order() {
        // Multi-root discovery scans roots in priority order, so the LAST-scanned file
        // is not necessarily the NEWEST — the fold must keep the later captured_at
        // (e.g. a fresher Linux reading must survive a stale Windows root scanned
        // after it), and the epoch sentinel must lose to any real stamp.
        let stamp = |epoch: i64| epoch_seconds(epoch).unwrap_or_else(epoch_utc);
        let window = |captured_at: DateTime<Utc>, fraction: f64| LimitWindow {
            tool: ProviderId::Codex,
            plan: None,
            kind: LimitKind::FiveHour,
            measure: Some(LimitMeasure::TokenFraction(fraction)),
            resets_at: None,
            captured_at,
            status: LimitStatus::Verified,
            label: None,
        };
        let newer = window(stamp(1_750_000_000), 0.9);
        let older = window(stamp(1_700_000_000), 0.1);

        // Stale data scanned AFTER fresh data must not override it…
        let kept = choose_limit(Some(newer.clone()), Some(older.clone()));
        assert!(kept
            .as_ref()
            .is_some_and(|limit| close_to(token_fraction(limit), 0.9)));
        // …and fresh data scanned after stale data wins as before.
        let kept = choose_limit(Some(older.clone()), Some(newer.clone()));
        assert!(kept
            .as_ref()
            .is_some_and(|limit| close_to(token_fraction(limit), 0.9)));

        // The epoch sentinel (no recorded capture instant) loses to any real stamp.
        let sentinel = window(epoch_utc(), 0.5);
        let kept = choose_limit(Some(newer.clone()), Some(sentinel.clone()));
        assert!(kept
            .as_ref()
            .is_some_and(|limit| close_to(token_fraction(limit), 0.9)));
        let kept = choose_limit(Some(sentinel), Some(newer));
        assert!(kept
            .as_ref()
            .is_some_and(|limit| close_to(token_fraction(limit), 0.9)));

        // A data-bearing window is never displaced by a no-data one, however fresh.
        let no_data = LimitWindow {
            measure: None,
            resets_at: None,
            ..window(stamp(1_760_000_000), 0.0)
        };
        let kept = choose_limit(Some(older), Some(no_data));
        assert!(kept
            .as_ref()
            .is_some_and(|limit| close_to(token_fraction(limit), 0.1)));
    }

    #[test]
    fn cursor_fixture_discovers_present_and_defers() {
        // The Cursor CLI keeps no usage or quota locally, so discovery detects the
        // install (naming ONLY cli-config.json — never the chat store next to it) and
        // both parsers return empty: zero events, zero limits.
        let provider = CursorProvider;
        let root = fixture_path(&["cursor", "home", ".cursor"]);
        let loc = match discover_cursor_location(vec![root.clone()]) {
            Ok(Some(loc)) => loc,
            other => panic!("present .cursor should discover Some: {other:?}"),
        };

        assert_eq!(loc.provider, ProviderId::Cursor);
        assert_eq!(loc.root, root);
        assert_eq!(loc.files, vec![root.join("cli-config.json")]);
        // Never enumerate the chat store (no store.db path leaks into files).
        assert!(loc
            .files
            .iter()
            .all(|f| f.file_name().is_some_and(|n| n == "cli-config.json")));

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("cursor usage should parse: {err}"),
        };
        let limits = match provider.parse_limits(&loc) {
            Ok(value) => value,
            Err(err) => panic!("cursor limits should parse: {err}"),
        };
        assert!(usage.is_empty(), "Cursor has no local usage events");
        assert!(limits.is_empty(), "Cursor has no local limit windows");
    }

    #[test]
    fn cursor_fixture_reads_selected_model_and_login() {
        let root = fixture_path(&["cursor", "home", ".cursor"]);
        let loc = match discover_cursor_location(vec![root]) {
            Ok(Some(loc)) => loc,
            other => panic!("present .cursor should discover Some: {other:?}"),
        };
        let config = read_cursor_config(&loc);
        assert_eq!(config.model_id.as_deref(), Some("composer-2.5"));
        assert_eq!(config.display_name.as_deref(), Some("Composer 2.5 Fast"));
        assert!(config.logged_in, "authInfo present ⇒ logged in");
    }

    #[test]
    fn cursor_fixture_missing_dir_is_graceful_noop() {
        // §9.2: a non-existent root degrades to "not present", never an error.
        let missing = fixture_path(&["cursor", "does-not-exist"]);
        match discover_cursor_location(vec![missing]) {
            Ok(None) => {}
            other => panic!("missing .cursor should be Ok(None): {other:?}"),
        }
    }

    #[test]
    fn cursor_fixture_garbled_config_present_with_no_model() {
        // Present install, unreadable config: still detected, but model unknown and
        // not logged in — never guessed, never an error.
        let root = fixture_path(&["cursor", "garbled", ".cursor"]);
        let loc = match discover_cursor_location(vec![root]) {
            Ok(Some(loc)) => loc,
            other => panic!("garbled .cursor should still discover Some: {other:?}"),
        };
        let config = read_cursor_config(&loc);
        assert_eq!(config.model_id, None);
        assert_eq!(config.display_name, None);
        assert!(!config.logged_in);
    }

    #[test]
    fn cursor_data_dir_root_honors_single_path_and_ignores_empty() {
        assert_eq!(
            cursor_data_dir_root(Some(OsString::from("/custom/cursor"))),
            Some(PathBuf::from("/custom/cursor")),
        );
        assert_eq!(cursor_data_dir_root(Some(OsString::from(""))), None);
        assert_eq!(cursor_data_dir_root(None), None);
    }

    #[test]
    fn cursor_roots_honor_data_dir_override_then_defaults() {
        let env = HostEnv::new(
            PathBuf::from("/home/example"),
            vec![PathBuf::from("/mnt/c/Users/example")],
            true,
        );

        // CURSOR_DATA_DIR comes first, ahead of ~/.cursor and the WSL Windows .cursor;
        // the defaults are still present (merged, not replaced).
        let roots = env.cursor_roots_from(Some(PathBuf::from("/custom/cursor")));
        assert_eq!(roots.first(), Some(&PathBuf::from("/custom/cursor")));
        assert!(roots.contains(&PathBuf::from("/home/example/.cursor")));
        assert!(roots.contains(&PathBuf::from("/mnt/c/Users/example/.cursor")));

        // With no override, the default ordering is unchanged.
        let roots = env.cursor_roots_from(None);
        assert_eq!(roots.first(), Some(&PathBuf::from("/home/example/.cursor")));
        assert!(!roots.contains(&PathBuf::from("/custom/cursor")));
    }

    fn find_event(events: &[UsageEvent], message_marker: u64) -> &UsageEvent {
        match events.iter().find(|e| e.input_tokens == message_marker) {
            Some(event) => event,
            None => panic!("expected an event with input_tokens == {message_marker}"),
        }
    }

    #[test]
    fn claude_dedupes_streaming_and_cross_file_duplicates() {
        // golden-a holds entry X streamed three times (output 1 -> 20000 -> 40000,
        // same input/cache) and entry Y once; golden-b copies both finalized
        // messages verbatim (resume-style). Six assistant lines, two real turns.
        let provider = ClaudeCodeProvider;
        let loc = DataLocation {
            provider: ProviderId::ClaudeCode,
            root: fixture_path(&["claude-code"]),
            files: vec![
                fixture_path(&["claude-code", "dedup-golden-a.jsonl"]),
                fixture_path(&["claude-code", "dedup-golden-b.jsonl"]),
            ],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("dedup fixtures should parse: {err}"),
        };

        // Collapses 6 occurrences -> 2 unique (message.id, requestId) turns.
        assert_eq!(usage.len(), 2);
        // Streaming entry X (input 100000) keeps the COMPLETE output (40000), not a
        // partial (1 / 20000) — the max-output occurrence wins, kept wholesale.
        let x = find_event(&usage, 100_000);
        assert_eq!(x.output_tokens, 40_000);
        assert_eq!(x.cache_read_tokens, 1_000_000);
        assert_eq!(x.cache_write_tokens, 40_000);
        // Cross-file entry Y (input 200000) appears in both files -> counted once.
        let y = find_event(&usage, 200_000);
        assert_eq!(y.output_tokens, 60_000);
        assert_eq!(
            usage.iter().filter(|e| e.input_tokens == 200_000).count(),
            1
        );
    }

    #[test]
    fn claude_never_collapses_keyless_entries() {
        // Two entries with the same message.id but NO requestId -> keyless (no full
        // (message.id, requestId) key) -> must NEVER merge, even though they are
        // otherwise identical. Guards keyless Codex/Cursor events too.
        let provider = ClaudeCodeProvider;
        let loc = DataLocation {
            provider: ProviderId::ClaudeCode,
            root: fixture_path(&["claude-code"]),
            files: vec![fixture_path(&["claude-code", "dedup-keyless.jsonl"])],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("keyless fixture should parse: {err}"),
        };

        assert_eq!(usage.len(), 2);
    }

    #[test]
    fn codex_usage_is_never_deduped() {
        // Codex events carry no (message.id, requestId), so even identical events
        // must all be counted — Codex totals must not change from de-dup.
        let provider = CodexProvider;
        let loc = DataLocation {
            provider: ProviderId::Codex,
            root: fixture_path(&["codex"]),
            files: vec![
                fixture_path(&["codex", "rollout.jsonl"]),
                fixture_path(&["codex", "rollout.jsonl"]),
            ],
        };

        let usage = match provider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("codex fixture should parse: {err}"),
        };

        // rollout.jsonl yields one usage event; reading it twice yields two.
        assert_eq!(usage.len(), 2);
    }

    #[test]
    fn claude_dedupe_key_requires_both_ids() {
        let full = serde_json::json!({"requestId":"req_1","message":{"id":"msg_1"}});
        assert_eq!(
            claude_dedupe_key(&full),
            Some(("msg_1".to_string(), "req_1".to_string()))
        );
        // Missing requestId, missing id, or empty strings -> keyless.
        assert_eq!(
            claude_dedupe_key(&serde_json::json!({"message":{"id":"msg_1"}})),
            None
        );
        assert_eq!(
            claude_dedupe_key(&serde_json::json!({"requestId":"req_1","message":{}})),
            None
        );
        assert_eq!(
            claude_dedupe_key(&serde_json::json!({"requestId":"","message":{"id":"msg_1"}})),
            None
        );
    }

    #[test]
    fn discover_reads_all_roots_not_just_first() {
        // Two roots each hold one transcript file: root-a has entry P (input 111),
        // root-b has entry R (input 333). The old early-return stopped at root-a and
        // never saw R; the merge must read BOTH files and surface BOTH events.
        let root_a = fixture_path(&["discovery", "root-a"]);
        let root_b = fixture_path(&["discovery", "root-b"]);
        let loc = match discover_claude_location(vec![root_a.clone(), root_b]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected merged discovery to find data"),
            Err(err) => panic!("discovery should not error: {err}"),
        };

        // Both files merged (session-a.jsonl + session-b.jsonl).
        assert_eq!(loc.files.len(), 2);
        // root is the FIRST root with data — the deterministic pick that drives
        // access-path classification across the whole merged set.
        assert_eq!(loc.root, root_a);

        let usage = match ClaudeCodeProvider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("merged location should parse: {err}"),
        };
        // P lives only in root-a, R only in root-b: both present iff both roots read.
        assert_eq!(find_event(&usage, 111).model, "claude-sonnet-4-6");
        assert_eq!(find_event(&usage, 333).model, "claude-sonnet-4-6");
    }

    #[test]
    fn discover_dedupes_sessions_present_in_two_roots() {
        // Entry Q (input 222) is copied verbatim into BOTH roots. Merge yields two
        // files containing it, but the (message.id, requestId) de-dup in parse_usage
        // must collapse it to a single event — merging cannot double-count.
        let loc = match discover_claude_location(vec![
            fixture_path(&["discovery", "root-a"]),
            fixture_path(&["discovery", "root-b"]),
        ]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected merged discovery to find data"),
            Err(err) => panic!("discovery should not error: {err}"),
        };

        let usage = match ClaudeCodeProvider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("merged location should parse: {err}"),
        };
        // P (once) + Q (deduped across roots) + R (once) = 3 unique turns.
        assert_eq!(usage.len(), 3);
        assert_eq!(usage.iter().filter(|e| e.input_tokens == 222).count(), 1);
    }

    #[test]
    fn discover_single_root_regression() {
        // A single root with data plus a non-existent second root behaves exactly as
        // before: only the present root's files, that root reported. collect_jsonl_files
        // tolerates the missing root (empty, no error).
        let root_a = fixture_path(&["discovery", "root-a"]);
        let absent = fixture_path(&["discovery", "does-not-exist"]);
        let loc = match discover_claude_location(vec![root_a.clone(), absent]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected single-root discovery to find data"),
            Err(err) => panic!("discovery should not error: {err}"),
        };

        assert_eq!(loc.root, root_a);
        assert_eq!(loc.files.len(), 1);
        let usage = match ClaudeCodeProvider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("single-root location should parse: {err}"),
        };
        // session-a.jsonl holds P and Q (distinct keys) -> two events.
        assert_eq!(usage.len(), 2);
    }

    #[test]
    fn codex_discover_reads_all_roots_not_just_first() {
        // The Codex analogue of the Claude all-roots merge — the WSL Windows-side fix.
        // codex-root-a holds session A (input 111), codex-root-b holds session B
        // (input 333). The old first-root-wins stopped at root-a and never saw B; the
        // merge must read BOTH roots' sessions/ and surface BOTH events.
        let root_a = fixture_path(&["discovery", "codex-root-a"]);
        let root_b = fixture_path(&["discovery", "codex-root-b"]);
        let loc = match discover_codex_location(vec![root_a.clone(), root_b]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected merged codex discovery to find data"),
            Err(err) => panic!("codex discovery should not error: {err}"),
        };

        // root is the FIRST root with data, exactly as the old early-return picked.
        assert_eq!(loc.root, root_a);

        let usage = match CodexProvider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("merged codex location should parse: {err}"),
        };
        // A lives only in root-a, B only in root-b: both present iff both roots read.
        assert_eq!(usage.iter().filter(|e| e.input_tokens == 111).count(), 1);
        assert_eq!(usage.iter().filter(|e| e.input_tokens == 333).count(), 1);
    }

    #[test]
    fn codex_discover_dedupes_same_session_across_roots() {
        // `rollout-shared.jsonl` (input 222) is the SAME session reached via both
        // roots — identical file name under different absolute paths, the symlink /
        // double-mount case. Session-level de-dup keys on the file name, so it is
        // surfaced once; A and B (distinct names) remain additive. Three files, three
        // events — not four.
        let root_a = fixture_path(&["discovery", "codex-root-a"]);
        let root_b = fixture_path(&["discovery", "codex-root-b"]);
        let loc = match discover_codex_location(vec![root_a, root_b]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected merged codex discovery to find data"),
            Err(err) => panic!("codex discovery should not error: {err}"),
        };
        assert_eq!(loc.files.len(), 3);

        let usage = match CodexProvider.parse_usage(&loc) {
            Ok(value) => value,
            Err(err) => panic!("merged codex location should parse: {err}"),
        };
        // A (once) + shared (deduped across roots) + B (once) = 3 unique sessions.
        assert_eq!(usage.len(), 3);
        assert_eq!(usage.iter().filter(|e| e.input_tokens == 222).count(), 1);
    }

    #[test]
    fn codex_discover_single_root_regression() {
        // A single root with data plus a non-existent second root behaves as before:
        // only the present root's session files, that root reported. collect_jsonl_files
        // tolerates the missing root (empty, no error).
        let root_a = fixture_path(&["discovery", "codex-root-a"]);
        let absent = fixture_path(&["discovery", "does-not-exist"]);
        let loc = match discover_codex_location(vec![root_a.clone(), absent]) {
            Ok(Some(loc)) => loc,
            Ok(None) => panic!("expected single-root codex discovery to find data"),
            Err(err) => panic!("codex discovery should not error: {err}"),
        };
        assert_eq!(loc.root, root_a);
        // codex-root-a holds two sessions (rollout-a + rollout-shared).
        assert_eq!(loc.files.len(), 2);
    }

    fn close_to(value: Option<f64>, expected: f64) -> bool {
        value
            .map(|value| (value - expected).abs() < 0.000_001)
            .unwrap_or(false)
    }

    /// The token-fraction carried by a window's measure, if any (`Spend` → `None`).
    fn token_fraction(limit: &LimitWindow) -> Option<f64> {
        match &limit.measure {
            Some(LimitMeasure::TokenFraction(fraction)) => Some(*fraction),
            Some(LimitMeasure::Spend { .. }) | None => None,
        }
    }

    #[test]
    fn each_provider_emits_its_expected_window_shape() {
        // The generalized quota shape, pinned per provider (T2 Done-when): Codex →
        // Verified TokenFraction windows; Claude → measure-less Unavailable windows when
        // no sanctioned rate_limits cache is present (the cache is global state, not in
        // `loc`, so this is read via the pure reader with no path — never a developer's
        // real cache); Cursor → no windows at all (served live server-side, no
        // sanctioned source; discovery-gated).
        let codex_loc = DataLocation {
            provider: ProviderId::Codex,
            root: fixture_path(&["codex"]),
            files: vec![fixture_path(&["codex", "rollout.jsonl"])],
        };
        let codex = match CodexProvider.parse_limits(&codex_loc) {
            Ok(value) => value,
            Err(err) => panic!("codex limits should parse: {err}"),
        };
        assert!(!codex.is_empty());
        assert!(codex.iter().all(|limit| {
            limit.status == LimitStatus::Verified
                && matches!(limit.measure, Some(LimitMeasure::TokenFraction(_)))
        }));

        let claude = read_claude_rate_limits(None);
        assert_eq!(claude.len(), 2);
        assert!(claude
            .iter()
            .all(|limit| limit.measure.is_none() && limit.status == LimitStatus::Unavailable));

        let cursor_loc = DataLocation {
            provider: ProviderId::Cursor,
            root: fixture_path(&["cursor", "home", ".cursor"]),
            files: Vec::new(),
        };
        let cursor = match CursorProvider.parse_limits(&cursor_loc) {
            Ok(value) => value,
            Err(err) => panic!("cursor limits should parse: {err}"),
        };
        assert!(cursor.is_empty(), "Cursor has no local limit windows");
    }

    #[test]
    fn each_provider_declares_its_capability() {
        // §2b: each adapter DECLARES its data/auth/quota shape so unavailability
        // renders honestly and future adapters slot in by descriptor. Pins today's
        // honest values (T3 Done-when): Claude = local cost/mix + sanctioned-hook
        // quota, no login; Codex = all-local, no login; Cursor = cost/quota
        // unavailable (live server-side, no sanctioned source; never session reuse)
        // with only the model a local artifact.
        assert_eq!(
            ClaudeCodeProvider.capability(),
            Capability {
                api_cost: DataSource::LocalArtifact,
                subscription_quota: DataSource::SanctionedHook,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: &[LimitKind::FiveHour, LimitKind::Weekly],
            }
        );
        assert_eq!(
            CodexProvider.capability(),
            Capability {
                api_cost: DataSource::LocalArtifact,
                subscription_quota: DataSource::LocalArtifact,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: &[LimitKind::FiveHour, LimitKind::Weekly],
            }
        );
        assert_eq!(
            CursorProvider.capability(),
            Capability {
                api_cost: DataSource::Unavailable,
                subscription_quota: DataSource::Unavailable,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: &[],
            }
        );
    }
}
