//! Costroid's network + credential boundary — **the only crate allowed to make a
//! network call or touch a secret**. Everything else in the workspace is provably
//! local-only; this crate is the single, deliberate exception, and it is **off by
//! default** (compiled in only behind a consumer's `connect` Cargo feature — today
//! `apps/cli`'s, off by default).
//!
//! # Status: keychain credential store (T8) + the generic HTTP client (T9a)
//!
//! This crate carries the keychain-backed store for the user's own usage/billing
//! API keys ([`CredentialStore`]), a *non-secret* on-disk index of which vendors are
//! linked ([`ConnectionRegistry`]), and — since T9a — the **generic authorized-host
//! HTTPS client** ([`AuthorizedClient`]): blocking `ureq` + `rustls`, OS-native trust
//! roots, bound to one explicitly authorized host, redirects disabled, GET-only.
//! The client has **no caller and no provider knowledge** — the per-provider
//! usage-API adapters are **T9b**, and no network call can occur without the
//! explicit, user-initiated `connect` action that lands with the
//! `costroid connect`/`disconnect` CLI + Connections view in **T10**.
//!
//! ## What lives here
//!
//! * [`CredentialStore`] — `store` / `retrieve` / `delete` an API key in the OS
//!   keychain via the [`keyring`] crate. Secrets are wrapped in
//!   [`secrecy::SecretString`] in memory and are **never** written to disk, config,
//!   or logs — only the keychain holds them.
//! * [`ConnectionRegistry`] — the *non-secret* answer to "what is linked?", a small
//!   JSON index at `${XDG_STATE_HOME:-~/.local/state}/costroid/connections.json`. The
//!   OS keychain is not portably enumerable, so this records the connected vendors
//!   (slugs only — zero secret material) for T10's Connections view to read.
//! * [`ApiVendor`] — the *billing-vendor* axis (whose usage/billing API a key calls):
//!   Anthropic / OpenAI / Gemini. This is deliberately distinct from
//!   `costroid-providers::ProviderId` (the *tool* axis: Claude Code / Codex / Cursor)
//!   — Cursor has no key, and "Anthropic" is not the Claude-Code tool — so this crate
//!   stays free of a `costroid-core`/`costroid-focus` dependency (T9a kept it that
//!   way: the generic client needs no internal type; they arrive with T9b if needed).
//! * [`AuthorizedClient`] (+ [`AuthHeader`], [`HttpResponse`], [`RequestLimits`]) —
//!   the client described above (in `src/http.rs`): it can only talk to the one host
//!   it was constructed over (off-host requests are a typed error **before any I/O**),
//!   classifies failures (redirect / timeout / 429 / 5xx / other-4xx / transport /
//!   body-too-large) and leaves retry *policy* to its caller, and never lets a
//!   secret-valued header reach logs, `Debug`, or error text.
//!
//! # The gate
//!
//! `costroid-connect` is compiled into a binary **only** when that binary opts in via
//! its `connect` Cargo feature. A default `cargo build` of the `costroid` binary never
//! links it, so the shipped local-only build contains no keychain/HTTP/TLS code at
//! all. Two tests keep that honest:
//!
//! * `apps/cli/tests/offline.rs` — the default build links none of the sanctioned
//!   trio (`keyring`/`ureq`/`rustls`); with `connect` on, exactly that trio is
//!   permitted (and asserted present, since T9a) while async runtimes, OpenSSL,
//!   other HTTP clients, and all telemetry stay forbidden.
//! * `scripts/offline_acceptance.sh` — the default build runs every command under
//!   network isolation and proves no outbound IP traffic is attempted; its
//!   feature-on baseline proves a normal `--features connect` run attempts none
//!   either (the client existing ≠ a call happening — nothing calls it until T10).
//!
//! ## Why the *sync* Secret Service backend (Linux)
//!
//! [`keyring`] is configured with the **`sync-secret-service`** backend (blocking
//! C libdbus, via `dbus-secret-service`) plus pure-Rust crypto (`crypto-rust`), **not**
//! the `async-secret-service` (zbus) path. That choice keeps **no async runtime** in
//! the dependency graph — `async-io`/`tokio` stay globally forbidden even under
//! `--features connect` — at the cost of the C build-deps `libdbus-1-dev` +
//! `libsecret-1-dev` (installed in CI; PRODUCT-PLAN Step 4). macOS uses the Security
//! framework (`apple-native`) and Windows the Credential Manager (`windows-native`).
//!
//! # The auth source ladder (the rule the future code must obey)
//!
//! Every datum is sourced by descending an explicit ladder, most-sanctioned first —
//! **only tiers 0–3 are ever built** (PRODUCT-PLAN §5):
//!
//! 0. Local artifacts — provider logs on disk (today's default, *not* this crate).
//! 1. Sanctioned push/hook — e.g. Claude's `statusLine` capture (also not this crate).
//! 2. Sanctioned OAuth (first-party; system browser + loopback redirect, PKCE) — T9+.
//! 3. The user's own API key — official provider *usage* APIs, the user's own key.
//! 4. **Never** reuse any credential, session, or cookie against a non-sanctioned,
//!    undocumented, or internal endpoint — that datum stays "unavailable", never
//!    fetched.
//!
//! No telemetry — ever. Network occurs only on an explicit, user-initiated `connect`
//! action to a provider endpoint the user authorized (T9/T10).
//!
//! [`keyring`]: https://crates.io/crates/keyring

use std::collections::BTreeSet;
use std::fmt;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::str::FromStr;

use keyring::{Entry, Error as KeyringError};
use secrecy::{ExposeSecret, SecretString};
use serde::{Deserialize, Serialize};

mod http;

pub use http::{AuthHeader, AuthorizedClient, HttpResponse, RequestLimits};

/// The OS-keychain *service* name under which every Costroid secret is filed. Stable
/// (changing it would orphan stored keys); the per-secret *account* namespaces the
/// kind (`apikey:<vendor>` today; `oauth:<vendor>` reserved for the deferred tier-2
/// OAuth in T9/T10).
const KEYCHAIN_SERVICE: &str = "costroid";

/// The keychain account for a vendor's own usage/billing **API key**.
fn apikey_account(vendor: ApiVendor) -> String {
    format!("apikey:{}", vendor.slug())
}

// ---------------------------------------------------------------------------
// ApiVendor — the billing-vendor axis
// ---------------------------------------------------------------------------

/// The billing-API vendor a stored key authenticates against — i.e. *whose*
/// usage/billing API the key calls.
///
/// This is the **billing-vendor** axis, deliberately distinct from
/// `costroid-providers::ProviderId` (the *tool* axis: Claude Code / Codex / Cursor):
/// Cursor exposes no individual API key, and "Anthropic" is the billing vendor behind
/// the Claude-Code *tool*, not the tool itself. Keeping them separate is what lets
/// `costroid-connect` stay free of a providers/core dependency in T8.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ApiVendor {
    /// Anthropic (the Claude usage/billing API).
    Anthropic,
    /// OpenAI (the Codex/GPT usage/billing API).
    OpenAI,
    /// Google Gemini (the Gemini usage/billing API).
    Gemini,
}

impl ApiVendor {
    /// Every vendor T8 can store a key for, in a stable order.
    pub const ALL: [ApiVendor; 3] = [ApiVendor::Anthropic, ApiVendor::OpenAI, ApiVendor::Gemini];

    /// The stable lowercase slug used in the keychain account and the registry file.
    /// Must never change (it is part of the on-disk/keychain key).
    pub fn slug(self) -> &'static str {
        match self {
            ApiVendor::Anthropic => "anthropic",
            ApiVendor::OpenAI => "openai",
            ApiVendor::Gemini => "gemini",
        }
    }

    /// Dense index into a per-vendor array (mirrors [`ApiVendor::ALL`]).
    fn index(self) -> usize {
        match self {
            ApiVendor::Anthropic => 0,
            ApiVendor::OpenAI => 1,
            ApiVendor::Gemini => 2,
        }
    }
}

impl fmt::Display for ApiVendor {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.slug())
    }
}

/// Returned by [`ApiVendor::from_str`] for an unrecognized vendor slug.
#[derive(Debug, thiserror::Error)]
#[error("unknown API vendor {0:?} (expected one of: anthropic, openai, gemini)")]
pub struct ParseVendorError(String);

impl FromStr for ApiVendor {
    type Err = ParseVendorError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        match s {
            "anthropic" => Ok(ApiVendor::Anthropic),
            "openai" => Ok(ApiVendor::OpenAI),
            "gemini" => Ok(ApiVendor::Gemini),
            other => Err(ParseVendorError(other.to_string())),
        }
    }
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/// Anything that can go wrong reaching the OS keychain, the connection registry, or
/// (since T9a) the authorized host. Library-crate errors are propagated, never
/// `unwrap`/`panic`, and **no variant ever carries secret material** in its fields,
/// `Debug`, or `Display` text (pinned by tests).
#[derive(Debug, thiserror::Error)]
pub enum ConnectError {
    /// The OS keychain backend reported an error (other than "no such entry", which
    /// the store maps to `None`/`Ok` so callers never special-case it). Built only via
    /// the scrubbing `From<KeyringError>` below — never construct it with an
    /// unscrubbed `BadEncoding`.
    #[error("OS keychain error: {0}")]
    Keyring(#[source] KeyringError),

    /// Reading or writing the non-secret connection registry failed.
    #[error("connection registry I/O at {path}: {source}")]
    RegistryIo {
        /// The path being read or written.
        path: PathBuf,
        /// The underlying I/O error.
        source: io::Error,
    },

    /// The connection registry file exists but is not valid JSON of the expected shape.
    #[error("connection registry is corrupt ({path}): {source}")]
    RegistryFormat {
        /// The registry path.
        path: PathBuf,
        /// The parse/serialize error.
        source: serde_json::Error,
    },

    /// Neither `$XDG_STATE_HOME` nor `$HOME` resolves a base directory, so the default
    /// registry path cannot be computed.
    #[error("no state directory: set $HOME or $XDG_STATE_HOME")]
    NoStateDir,

    // ---- the T9a HTTP-client taxonomy (PRODUCT-PLAN §12.11 pins) ----------
    //
    /// The request URL is not on the client's single authorized host. Raised
    /// **before any I/O** — the authorized-host guarantee lives in the type.
    #[error("refusing request to {requested:?}: not the authorized host {authorized:?}")]
    UnauthorizedHost {
        /// The (normalized) host the request asked for.
        requested: String,
        /// The one host this client is authorized to talk to.
        authorized: String,
    },

    /// The authorized host given at construction is not a bare hostname.
    #[error("invalid authorized host: {reason}")]
    InvalidHost {
        /// Why the host was rejected (never echoes secret material).
        reason: String,
    },

    /// The request URL could not be validated (not absolute, wrong scheme,
    /// userinfo, or invalid host characters). Raised before any I/O.
    #[error("invalid request URL: {reason}")]
    InvalidUrl {
        /// Why the URL was rejected (never echoes secret material).
        reason: String,
    },

    /// The authorized host answered with a redirect. Redirects are disabled
    /// entirely — following one could leave the authorized host — so any 3xx is
    /// refused, never followed.
    #[error("redirect response (HTTP {status}) — redirects are disabled, refusing to follow")]
    Redirect {
        /// The 3xx status received.
        status: u16,
    },

    /// The connect or overall deadline elapsed ([`RequestLimits`]).
    #[error("request timed out")]
    Timeout,

    /// HTTP 429 — the vendor is rate-limiting. Backoff *policy* is the caller's;
    /// the parsed `Retry-After` seconds (when present) support it.
    #[error("rate limited by the authorized host (HTTP 429)")]
    RateLimited {
        /// `Retry-After` in seconds, when the response carried the seconds form.
        retry_after_seconds: Option<u64>,
    },

    /// HTTP 5xx — the vendor's side failed; degrade, don't fabricate.
    #[error("server error from the authorized host (HTTP {status})")]
    ServerError {
        /// The 5xx status received.
        status: u16,
    },

    /// HTTP 4xx other than 429 — wrong key class, missing permission, bad request.
    #[error("client error from the authorized host (HTTP {status})")]
    ClientError {
        /// The 4xx status received.
        status: u16,
    },

    /// A transport-level failure: DNS, TCP connect, TLS, or protocol. The message
    /// comes from the transport stack and never contains header values.
    #[error("transport error: {message}")]
    Transport {
        /// The transport stack's (secret-free) description.
        message: String,
    },

    /// The response body exceeded [`RequestLimits::max_body_bytes`].
    #[error("response body exceeds the {limit_bytes}-byte limit")]
    BodyTooLarge {
        /// The configured cap that was exceeded.
        limit_bytes: u64,
    },

    /// The OS-native trust store could not be loaded (or yielded no certificates),
    /// so an HTTPS client cannot be built.
    #[error("could not load OS-native TLS roots: {detail}")]
    NativeRoots {
        /// The trust-store loader's (secret-free) description.
        detail: String,
    },
}

impl From<KeyringError> for ConnectError {
    /// Scrub the one keyring variant that carries secret material: `BadEncoding`
    /// attaches the RAW stored secret bytes ("available for examination in the attached
    /// value"), which a `Debug` render of this error would otherwise transit into a
    /// message or log. The payload is emptied at this boundary — keyring's `Display` is
    /// already redacted; this keeps `Debug` (and any future serialization) safe too.
    fn from(err: KeyringError) -> Self {
        match err {
            KeyringError::BadEncoding(_) => {
                ConnectError::Keyring(KeyringError::BadEncoding(Vec::new()))
            }
            other => ConnectError::Keyring(other),
        }
    }
}

// ---------------------------------------------------------------------------
// CredentialStore — secrets, keychain only
// ---------------------------------------------------------------------------

/// A keychain-backed store for the user's own usage/billing API keys.
///
/// Secrets live **only** in the OS keychain (service `costroid`, account
/// `apikey:<vendor>`); nothing is written to disk, config, or logs. In memory a secret
/// is always a [`SecretString`], so it cannot be accidentally `Debug`-printed or logged.
///
/// The store owns one [`keyring::Entry`] per vendor. Caching the entries (rather than
/// rebuilding one per call) is what lets the platform-independent **mock** backend —
/// which persists a secret only inside its own `Entry` — round-trip in tests; for the
/// real OS backends it is a harmless, cheap handle reuse.
pub struct CredentialStore {
    entries: [Entry; 3],
}

impl CredentialStore {
    /// Open the keychain store. Fails only if the OS keychain rejects the
    /// (service, account) identifiers, which the fixed constants never do in practice.
    pub fn new() -> Result<Self, ConnectError> {
        Ok(Self {
            entries: [
                Entry::new(KEYCHAIN_SERVICE, &apikey_account(ApiVendor::Anthropic))?,
                Entry::new(KEYCHAIN_SERVICE, &apikey_account(ApiVendor::OpenAI))?,
                Entry::new(KEYCHAIN_SERVICE, &apikey_account(ApiVendor::Gemini))?,
            ],
        })
    }

    fn entry(&self, vendor: ApiVendor) -> &Entry {
        &self.entries[vendor.index()]
    }

    /// Store (or overwrite) the API key for `vendor` in the OS keychain.
    pub fn store(&self, vendor: ApiVendor, secret: &SecretString) -> Result<(), ConnectError> {
        self.entry(vendor).set_password(secret.expose_secret())?;
        Ok(())
    }

    /// Retrieve the API key for `vendor`, or `None` if none is stored.
    pub fn retrieve(&self, vendor: ApiVendor) -> Result<Option<SecretString>, ConnectError> {
        match self.entry(vendor).get_password() {
            Ok(password) => Ok(Some(SecretString::from(password))),
            Err(KeyringError::NoEntry) => Ok(None),
            Err(err) => Err(err.into()),
        }
    }

    /// Delete the API key for `vendor`. Deleting a key that is not present is a no-op
    /// (`Ok`), so `disconnect` is idempotent.
    pub fn delete(&self, vendor: ApiVendor) -> Result<(), ConnectError> {
        match self.entry(vendor).delete_credential() {
            Ok(()) | Err(KeyringError::NoEntry) => Ok(()),
            Err(err) => Err(err.into()),
        }
    }
}

impl fmt::Debug for CredentialStore {
    /// Deliberately opaque: never render the entries, whose mock backend would hold a
    /// secret in memory. The real backends hold no secret here, but a redacting `Debug`
    /// keeps the no-leak guarantee true regardless of backend.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("CredentialStore").finish_non_exhaustive()
    }
}

// ---------------------------------------------------------------------------
// ConnectionRegistry — the non-secret "what is linked?" index
// ---------------------------------------------------------------------------

/// On-disk shape of the connection registry. Holds **only** vendor slugs — never any
/// secret material. A `BTreeSet` keeps the list deduplicated and deterministically
/// ordered (so the file is stable across writes).
#[derive(Debug, Default, Serialize, Deserialize)]
struct RegistryFile {
    connected: BTreeSet<ApiVendor>,
}

/// Resolve the default registry path
/// `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/connections.json`. `None` when
/// neither `$XDG_STATE_HOME` nor `$HOME` resolves a base directory. (Mirrors the
/// `claude_rate_limits_cache_path` convention in `costroid-providers`.)
pub fn default_registry_path() -> Option<PathBuf> {
    let base = std::env::var_os("XDG_STATE_HOME")
        .map(PathBuf::from)
        .filter(|path| !path.as_os_str().is_empty())
        .or_else(|| {
            std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".local").join("state"))
        })?;
    Some(base.join("costroid").join("connections.json"))
}

/// A *non-secret* record of which [`ApiVendor`]s are currently linked.
///
/// The OS keychain cannot be enumerated portably, so this small JSON index answers
/// "what is connected?" for the Connections view (T10). It stores vendor slugs only —
/// **no secret ever lands here**; the keys themselves live solely in the keychain.
/// Writes are atomic (temp file + rename) so a concurrent reader never sees a torn file.
pub struct ConnectionRegistry {
    path: PathBuf,
}

impl ConnectionRegistry {
    /// Open the registry at its default path (see [`default_registry_path`]).
    pub fn open() -> Result<Self, ConnectError> {
        Ok(Self {
            path: default_registry_path().ok_or(ConnectError::NoStateDir)?,
        })
    }

    /// Open the registry at an explicit path — the testable seam (no env access).
    pub fn at(path: PathBuf) -> Self {
        Self { path }
    }

    /// The vendors currently marked connected, in deterministic order.
    pub fn list(&self) -> Result<Vec<ApiVendor>, ConnectError> {
        Ok(self.load()?.connected.into_iter().collect())
    }

    /// Whether `vendor` is currently marked connected.
    pub fn is_connected(&self, vendor: ApiVendor) -> Result<bool, ConnectError> {
        Ok(self.load()?.connected.contains(&vendor))
    }

    /// Mark `vendor` connected (idempotent).
    pub fn mark_connected(&self, vendor: ApiVendor) -> Result<(), ConnectError> {
        let mut file = self.load()?;
        file.connected.insert(vendor);
        self.save(&file)
    }

    /// Mark `vendor` disconnected (idempotent; removing an absent vendor is a no-op).
    pub fn mark_disconnected(&self, vendor: ApiVendor) -> Result<(), ConnectError> {
        let mut file = self.load()?;
        file.connected.remove(&vendor);
        self.save(&file)
    }

    /// Read the registry file. A missing file is an empty registry (not an error); a
    /// present-but-malformed file is a [`ConnectError::RegistryFormat`].
    fn load(&self) -> Result<RegistryFile, ConnectError> {
        match fs::read(&self.path) {
            Ok(bytes) => {
                serde_json::from_slice(&bytes).map_err(|source| ConnectError::RegistryFormat {
                    path: self.path.clone(),
                    source,
                })
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(RegistryFile::default()),
            Err(source) => Err(ConnectError::RegistryIo {
                path: self.path.clone(),
                source,
            }),
        }
    }

    /// Atomically write the registry: temp file in the same directory, then rename.
    fn save(&self, file: &RegistryFile) -> Result<(), ConnectError> {
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent).map_err(|source| ConnectError::RegistryIo {
                path: parent.to_path_buf(),
                source,
            })?;
        }
        let bytes =
            serde_json::to_vec_pretty(file).map_err(|source| ConnectError::RegistryFormat {
                path: self.path.clone(),
                source,
            })?;
        let tmp = tmp_sibling(&self.path);
        fs::write(&tmp, &bytes).map_err(|source| ConnectError::RegistryIo {
            path: tmp.clone(),
            source,
        })?;
        fs::rename(&tmp, &self.path).map_err(|source| ConnectError::RegistryIo {
            path: self.path.clone(),
            source,
        })?;
        Ok(())
    }
}

/// A unique `<path>.<pid>.<n>.tmp` sibling for atomic writes (same directory, so the
/// rename stays atomic). A FIXED temp name would let two concurrent writers interleave
/// truncate/write/rename and publish a torn registry file.
fn tmp_sibling(path: &Path) -> PathBuf {
    use std::sync::atomic::{AtomicU64, Ordering};
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let serial = COUNTER.fetch_add(1, Ordering::Relaxed);
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(format!(".{}.{serial}.tmp", std::process::id()));
    PathBuf::from(tmp)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Once;

    // The workspace forbids `.unwrap()`/`.expect()` even in tests, so these
    // panic-on-failure helpers stand in (panics are allowed; cf. apps/cli/tests/offline.rs).
    #[track_caller]
    fn ok<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }
    #[track_caller]
    fn some<T>(value: Option<T>) -> T {
        match value {
            Some(value) => value,
            None => panic!("expected Some, got None"),
        }
    }

    // The mock keychain is process-global and must be installed before any `Entry` is
    // created. A `Once` does it exactly once; each test then builds its own
    // `CredentialStore`, whose owned entries give it an isolated in-memory store (the
    // mock persists a secret only inside its own `Entry`), so keychain tests are
    // independent and parallel-safe without touching any real OS keychain.
    static MOCK_KEYCHAIN: Once = Once::new();
    fn use_mock_keychain() {
        MOCK_KEYCHAIN.call_once(|| {
            keyring::set_default_credential_builder(keyring::mock::default_credential_builder());
        });
    }

    /// A unique, auto-cleaned temp directory (no `tempfile` dep — keeps the dev-dep
    /// graph clean for the offline forbidden-crates check).
    struct TempDir {
        path: PathBuf,
    }
    impl TempDir {
        fn new(tag: &str) -> Self {
            static COUNTER: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
            let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let path = std::env::temp_dir()
                .join(format!("costroid-connect-{tag}-{}-{n}", std::process::id()));
            let _ = fs::remove_dir_all(&path);
            ok(fs::create_dir_all(&path));
            Self { path }
        }
    }
    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.path);
        }
    }

    // ---- ApiVendor -------------------------------------------------------

    #[test]
    fn vendor_slug_display_and_parse_round_trip() {
        for vendor in ApiVendor::ALL {
            assert_eq!(vendor.to_string(), vendor.slug());
            assert_eq!(ok(vendor.slug().parse::<ApiVendor>()), vendor);
        }
        assert!("cursor".parse::<ApiVendor>().is_err());
        assert!("".parse::<ApiVendor>().is_err());
    }

    #[test]
    fn vendor_serializes_as_lowercase_slug() {
        assert_eq!(ok(serde_json::to_string(&ApiVendor::OpenAI)), "\"openai\"");
        assert_eq!(
            ok(serde_json::from_str::<ApiVendor>("\"gemini\"")),
            ApiVendor::Gemini
        );
    }

    // ---- CredentialStore (mock backend) ---------------------------------

    #[test]
    fn store_retrieve_delete_round_trips() {
        use_mock_keychain();
        let store = ok(CredentialStore::new());

        assert!(ok(store.retrieve(ApiVendor::Anthropic)).is_none());

        let secret = SecretString::from("sk-ant-secret-123".to_string());
        ok(store.store(ApiVendor::Anthropic, &secret));
        let got = some(ok(store.retrieve(ApiVendor::Anthropic)));
        assert_eq!(got.expose_secret(), "sk-ant-secret-123");

        ok(store.delete(ApiVendor::Anthropic));
        assert!(ok(store.retrieve(ApiVendor::Anthropic)).is_none());
        // delete is idempotent
        ok(store.delete(ApiVendor::Anthropic));
    }

    #[test]
    fn store_overwrites_existing_key() {
        use_mock_keychain();
        let store = ok(CredentialStore::new());
        ok(store.store(ApiVendor::OpenAI, &SecretString::from("old".to_string())));
        ok(store.store(ApiVendor::OpenAI, &SecretString::from("new".to_string())));
        assert_eq!(
            some(ok(store.retrieve(ApiVendor::OpenAI))).expose_secret(),
            "new"
        );
    }

    #[test]
    fn vendors_are_independent_slots() {
        use_mock_keychain();
        let store = ok(CredentialStore::new());
        ok(store.store(ApiVendor::Anthropic, &SecretString::from("a".to_string())));
        ok(store.store(ApiVendor::Gemini, &SecretString::from("g".to_string())));
        assert_eq!(
            some(ok(store.retrieve(ApiVendor::Anthropic))).expose_secret(),
            "a"
        );
        assert!(ok(store.retrieve(ApiVendor::OpenAI)).is_none());
        assert_eq!(
            some(ok(store.retrieve(ApiVendor::Gemini))).expose_secret(),
            "g"
        );
    }

    #[test]
    fn credential_store_debug_is_redacted() {
        use_mock_keychain();
        let store = ok(CredentialStore::new());
        ok(store.store(
            ApiVendor::Anthropic,
            &SecretString::from("top-secret-xyz".to_string()),
        ));
        let rendered = format!("{store:?}");
        assert!(
            !rendered.contains("top-secret-xyz"),
            "CredentialStore Debug must not leak a stored secret, got: {rendered}"
        );
    }

    /// The secret-residue guarantee at the unit level: a store→retrieve→delete
    /// round-trip writes **nothing** to the filesystem (the secret lives only in the
    /// keychain — here, the in-memory mock). Mirrors the offline-acceptance script's
    /// "no secret written to disk/config/logs" check, with no real keychain touched.
    #[test]
    fn credential_round_trip_writes_nothing_to_disk() {
        use_mock_keychain();
        let dir = TempDir::new("residue");
        let store = ok(CredentialStore::new());
        ok(store.store(ApiVendor::Gemini, &SecretString::from("k".to_string())));
        let _ = ok(store.retrieve(ApiVendor::Gemini));
        ok(store.delete(ApiVendor::Gemini));
        let entries: Vec<_> = ok(fs::read_dir(&dir.path)).collect();
        assert!(
            entries.is_empty(),
            "the keychain round-trip must write nothing to disk, found: {entries:?}"
        );
    }

    // ---- ConnectionRegistry (non-secret, on disk) -----------------------

    #[test]
    fn registry_missing_file_is_empty() {
        let dir = TempDir::new("reg-empty");
        let reg = ConnectionRegistry::at(dir.path.join("connections.json"));
        assert!(ok(reg.list()).is_empty());
        assert!(!ok(reg.is_connected(ApiVendor::Anthropic)));
    }

    #[test]
    fn registry_marks_lists_and_unmarks() {
        let dir = TempDir::new("reg-mark");
        let reg = ConnectionRegistry::at(dir.path.join("connections.json"));
        ok(reg.mark_connected(ApiVendor::Anthropic));
        ok(reg.mark_connected(ApiVendor::OpenAI));
        ok(reg.mark_connected(ApiVendor::Anthropic)); // idempotent
        assert_eq!(
            ok(reg.list()),
            vec![ApiVendor::Anthropic, ApiVendor::OpenAI]
        );
        assert!(ok(reg.is_connected(ApiVendor::OpenAI)));

        ok(reg.mark_disconnected(ApiVendor::Anthropic));
        ok(reg.mark_disconnected(ApiVendor::Gemini)); // absent → no-op
        assert_eq!(ok(reg.list()), vec![ApiVendor::OpenAI]);
    }

    #[test]
    fn registry_persists_only_vendor_slugs_no_secret() {
        let dir = TempDir::new("reg-content");
        let path = dir.path.join("connections.json");
        let reg = ConnectionRegistry::at(path.clone());
        ok(reg.mark_connected(ApiVendor::Anthropic));
        ok(reg.mark_connected(ApiVendor::Gemini));
        let raw = ok(fs::read_to_string(&path));
        // Exactly the vendor slugs, under a `connected` key — and nothing else.
        let parsed: serde_json::Value = ok(serde_json::from_str(&raw));
        assert_eq!(
            parsed["connected"],
            serde_json::json!(["anthropic", "gemini"])
        );
        assert_eq!(some(parsed.as_object()).len(), 1);
    }

    #[test]
    fn registry_rejects_corrupt_file() {
        let dir = TempDir::new("reg-corrupt");
        let path = dir.path.join("connections.json");
        ok(fs::write(&path, b"{ this is not json"));
        let reg = ConnectionRegistry::at(path);
        assert!(matches!(
            reg.list(),
            Err(ConnectError::RegistryFormat { .. })
        ));
    }

    #[test]
    fn keyring_bad_encoding_payload_is_scrubbed_at_the_boundary() {
        // keyring's BadEncoding attaches the RAW stored secret bytes; the From
        // conversion must empty that payload so no Debug render of ConnectError can
        // ever carry secret material.
        let err: ConnectError = KeyringError::BadEncoding(b"super-secret-key".to_vec()).into();
        match &err {
            ConnectError::Keyring(KeyringError::BadEncoding(bytes)) => {
                assert!(bytes.is_empty(), "payload must be scrubbed, got {bytes:?}");
            }
            other => panic!("expected a scrubbed BadEncoding, got {other:?}"),
        }
        let rendered = format!("{err:?} / {err}");
        assert!(
            !rendered.contains("super-secret-key"),
            "no render may carry the secret: {rendered}"
        );
    }

    #[test]
    fn registry_tmp_siblings_never_collide() {
        // A FIXED temp name would let two concurrent writers tear the registry; the
        // unique sibling keeps the temp in the same directory (atomic rename).
        let path = Path::new("/tmp/example/connections.json");
        let first = tmp_sibling(path);
        let second = tmp_sibling(path);
        assert_ne!(first, second);
        assert_eq!(first.parent(), path.parent());
    }
}
