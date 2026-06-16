//! The `connect` / `disconnect` / `connections` command core (T10a) — the **first
//! caller** of `costroid-connect` and the first real network in the product.
//!
//! Everything here is `#[cfg(feature = "connect")]` (the module is only declared under the
//! feature). The logic is an **injectable command core**: the network half is reached
//! only through the [`AdapterSet`] trait and all output goes to an injected `Write`, so the
//! Layer-1 connect-action test (`#[cfg(feature = "connect-test-support")]`, at the bottom)
//! can drive the exact same code against the loopback `MockServer` + keyring mock — zero
//! real network, zero real keychain. The production wiring in `main.rs` injects
//! [`RealAdapters`] (HTTPS via the built `AnthropicAdapter`/`OpenAiAdapter`), the real
//! `CredentialStore`, and the real `ConnectionRegistry`.
//!
//! Secret discipline (CLAUDE.md golden rules, proposal §12): the pasted key arrives as a
//! [`SecretString`] (read from stdin in `main.rs`, never argv/env), is **wrong-class
//! prefix-checked before any I/O** by the adapter, rides the wire only as an
//! `AuthHeader`, lands **only** in the OS keychain, and is **never** echoed — only a short
//! class prefix is shown on a wrong-class error (proposal §1.1). The connections registry
//! and the optional org label stay non-secret.

use std::io::{self, Write};

use chrono::{DateTime, Duration, Utc};
use costroid_connect::{
    AnthropicAdapter, ApiVendor, ConnectError, ConnectionRegistry, CostReportOutcome,
    CredentialStore, DateRange, ExposeSecret, OpenAiAdapter, OrgLabel, OrgValidation, SecretString,
    VendorReportUnavailable,
};
use costroid_core::vendor_report::AccessForbiddenHint;

/// How the one-shot connect/connections output renders. The commands emit plain text with
/// a non-color status cue, so the only mode concern is whether to ASCII-fold the em-dash
/// (folded for `--plain` / a non-UTF-8 terminal, kept for a UTF-8 TTY) — mirroring the
/// `cursor_detected_message` convention.
#[derive(Clone, Copy)]
pub struct OutputStyle {
    /// Fold the em-dash (`—`) to an ASCII hyphen.
    pub ascii: bool,
}

/// The network half of the command core, behind a trait so the Layer-1 test can inject a
/// loopback-backed implementation. Both methods reuse the built adapters' wrong-key-class
/// check, which refuses a wrong-class key **before any I/O**.
pub trait AdapterSet {
    /// Validate an Anthropic admin key via `GET /v1/organizations/me` (reads no billing).
    fn validate_anthropic(&self, key: &SecretString) -> Result<OrgValidation, ConnectError>;
    /// Probe OpenAI by fetching `GET /v1/organization/costs` over `range` (the signed-off
    /// connect-time validation — a cost fetch, so "Connected" predicts the cost fetch).
    fn probe_openai(
        &self,
        key: &SecretString,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError>;
    /// Fetch a vendor's billed-cost report for `range`, reading the **stored** key from
    /// `store` (the T10c `reconcile` fetch). Reuses the exact authorized-client + stored-key
    /// path the connect probe rides — no new endpoint, no new secret/network boundary. Gemini
    /// has no sanctioned static-key usage API, so it resolves to the pinned unavailable
    /// **without any fetch**. Returns [`VendorReportUnavailable::NotConnected`] (as data) when
    /// no key is stored for the vendor.
    fn cost_report(
        &self,
        vendor: ApiVendor,
        store: &CredentialStore,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError>;
}

/// The production [`AdapterSet`]: the real HTTPS adapters bound to `api.anthropic.com` /
/// `api.openai.com`. Built per call (a one-shot human action; the cost is loading the OS
/// trust roots once).
pub struct RealAdapters;

impl AdapterSet for RealAdapters {
    fn validate_anthropic(&self, key: &SecretString) -> Result<OrgValidation, ConnectError> {
        AnthropicAdapter::new()?.validate(key)
    }

    fn probe_openai(
        &self,
        key: &SecretString,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError> {
        OpenAiAdapter::new()?.fetch_cost_report(key, range)
    }

    fn cost_report(
        &self,
        vendor: ApiVendor,
        store: &CredentialStore,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError> {
        match vendor {
            ApiVendor::Anthropic => AnthropicAdapter::new()?.cost_report(store, range),
            ApiVendor::OpenAI => OpenAiAdapter::new()?.cost_report(store, range),
            // Gemini: first-class unavailable (no sanctioned static-key usage API) — no fetch.
            ApiVendor::Gemini => Ok(CostReportOutcome::Unavailable(
                VendorReportUnavailable::NoSanctionedStaticKeyApi,
            )),
        }
    }
}

/// The normalized outcome of validating a vendor key (both vendors mapped onto one shape).
enum ValidationResult {
    /// The key works; carries the non-secret org label when the vendor returned one
    /// (Anthropic `me`; `None` for OpenAI's `/costs` probe).
    Valid { label: Option<OrgLabel> },
    /// A typed reason the key is unusable (rejected / ineligible / wrong class / …).
    Unavailable(VendorReportUnavailable),
}

/// `costroid connect <anthropic|openai>`: validate the pasted key without reading billing
/// beyond the user's own data, then — only on success — store it in the OS keychain and
/// mark it connected. Returns the process exit code (0 = connected, 1 = a typed validation
/// failure). Gemini is handled by [`gemini_connect`] (no key, never reaches here).
pub fn run_connect(
    vendor: ApiVendor,
    key: SecretString,
    adapters: &dyn AdapterSet,
    store: &CredentialStore,
    registry: &ConnectionRegistry,
    out: &mut dyn Write,
    style: OutputStyle,
) -> anyhow::Result<i32> {
    match validate_vendor(vendor, &key, adapters)? {
        ValidationResult::Valid { label } => {
            // Store ONLY after a successful validation; the key reaches disk nowhere but
            // the OS keychain, and the registry/label carry no secret material.
            store.store(vendor, &key)?;
            registry.mark_connected_with_label(vendor, label.clone())?;
            emit(out, style, &connected_message(vendor, label.as_ref()))?;
            Ok(0)
        }
        ValidationResult::Unavailable(reason) => {
            // Do NOT store; report the typed reason + remediation; exit nonzero.
            emit(
                out,
                style,
                &format!(
                    "Could not connect {vendor}: {} Nothing was stored.",
                    remediation(vendor, &reason, &key)
                ),
            )?;
            Ok(1)
        }
    }
}

/// `costroid connect gemini`: a recognized vendor with a known answer — print the pinned
/// unavailable line + why, and exit 0 **without** prompting for or accepting a key.
pub fn gemini_connect(out: &mut dyn Write, style: OutputStyle) -> anyhow::Result<i32> {
    emit(
        out,
        style,
        &format!("gemini: {}", gemini_unavailable_message()),
    )?;
    emit(
        out,
        style,
        "Google publishes no sanctioned static-key usage/billing API, so a key cannot be \
         connected. (No key was prompted for, read, or stored.)",
    )?;
    Ok(0)
}

/// `costroid disconnect <vendor>`: delete the key from the keychain and unlink it — both
/// idempotent, no network. Exits 0 even if nothing was stored.
pub fn run_disconnect(
    vendor: ApiVendor,
    store: &CredentialStore,
    registry: &ConnectionRegistry,
    out: &mut dyn Write,
    style: OutputStyle,
) -> anyhow::Result<i32> {
    store.delete(vendor)?;
    registry.mark_disconnected(vendor)?;
    emit(out, style, &format!("Disconnected {vendor}."))?;
    Ok(0)
}

/// `costroid connections [--check]`: list each vendor's link status. Local-only (zero
/// network) by default; `--check` re-runs the validation call per **connected** vendor.
/// Status is a non-color text cue; the em-dash is ASCII-folded for `--plain`.
pub fn run_connections(
    check: bool,
    adapters: &dyn AdapterSet,
    store: &CredentialStore,
    registry: &ConnectionRegistry,
    out: &mut dyn Write,
    style: OutputStyle,
) -> anyhow::Result<i32> {
    emit(
        out,
        style,
        "Connections (local; pass --check to verify each over the network):",
    )?;
    for vendor in ApiVendor::ALL {
        let line = match vendor {
            // Gemini is a first-class "unavailable", never a network call.
            ApiVendor::Gemini => {
                format!("  {vendor:<10} {}", gemini_unavailable_message())
            }
            ApiVendor::Anthropic | ApiVendor::OpenAI => {
                // "connected" requires the key present in the keychain AND the registry
                // mark (the keychain is the source of truth for the secret's presence).
                let connected = registry.is_connected(vendor)? && store.retrieve(vendor)?.is_some();
                if !connected {
                    format!("  {vendor:<10} not connected")
                } else {
                    let mut line = format!(
                        "  {vendor:<10} connected{}",
                        label_suffix(registry, vendor)?
                    );
                    if check {
                        if let Some(key) = store.retrieve(vendor)? {
                            match validate_vendor(vendor, &key, adapters)? {
                                ValidationResult::Valid { .. } => {
                                    line.push_str("; verified just now")
                                }
                                ValidationResult::Unavailable(reason) => {
                                    line.push_str(&format!("; check failed — {}", reason.message()))
                                }
                            }
                        }
                    }
                    line
                }
            }
        };
        emit(out, style, &line)?;
    }
    emit(
        out,
        style,
        "Disconnect any vendor instantly with: costroid disconnect <vendor>",
    )?;
    Ok(0)
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

/// Validate a vendor key, normalizing both vendors onto [`ValidationResult`]. The adapter
/// performs the wrong-key-class prefix check before any network, so a wrong-class key
/// returns `Unavailable(WrongKeyClass)` with **no** request made.
fn validate_vendor(
    vendor: ApiVendor,
    key: &SecretString,
    adapters: &dyn AdapterSet,
) -> Result<ValidationResult, ConnectError> {
    match vendor {
        ApiVendor::Anthropic => Ok(match adapters.validate_anthropic(key)? {
            OrgValidation::Valid(label) => ValidationResult::Valid { label: Some(label) },
            OrgValidation::Unavailable(reason) => ValidationResult::Unavailable(reason),
        }),
        ApiVendor::OpenAI => {
            // The signed-off probe: fetch /costs over a recent COMPLETED 1-day window.
            let outcome = adapters.probe_openai(key, completed_day_window())?;
            Ok(match outcome {
                CostReportOutcome::Available(_) => ValidationResult::Valid { label: None },
                CostReportOutcome::Unavailable(reason) => ValidationResult::Unavailable(reason),
            })
        }
        // Gemini never validates: it is handled before this point (no key is read).
        ApiVendor::Gemini => Ok(ValidationResult::Unavailable(
            VendorReportUnavailable::NoSanctionedStaticKeyApi,
        )),
    }
}

/// The most recent COMPLETED UTC day, `[yesterday-00:00, today-00:00)` — the window the
/// OpenAI `/costs` probe uses (the cost reports serve completed days only). Computed from
/// Unix seconds so UTC-midnight alignment needs no fallible `NaiveDate` juggling.
fn completed_day_window() -> DateRange {
    const DAY: i64 = 86_400;
    let now = Utc::now();
    let today_midnight = now.timestamp() - now.timestamp().rem_euclid(DAY);
    match (
        DateTime::<Utc>::from_timestamp(today_midnight - DAY, 0),
        DateTime::<Utc>::from_timestamp(today_midnight, 0),
    ) {
        (Some(start), Some(end)) => DateRange::new(start, end),
        // Unreachable for a "yesterday" instant; degrade to a trailing 24h window.
        _ => DateRange::new(now - Duration::days(1), now),
    }
}

/// The success line. Shows the non-secret org label when present (Anthropic `me`).
fn connected_message(vendor: ApiVendor, label: Option<&OrgLabel>) -> String {
    let tail = "Key stored in your OS keychain.";
    match label {
        Some(OrgLabel { name, id: Some(id) }) => {
            format!("Connected {vendor} — organization {name} ({id}). {tail}")
        }
        Some(OrgLabel { name, id: None }) => {
            format!("Connected {vendor} — organization {name}. {tail}")
        }
        None => format!("Connected {vendor}. {tail}"),
    }
}

/// The ` — organization …` suffix for a connected vendor in the `connections` list, or
/// empty when no label was captured.
fn label_suffix(registry: &ConnectionRegistry, vendor: ApiVendor) -> Result<String, ConnectError> {
    Ok(match registry.label(vendor)? {
        Some(OrgLabel { name, id: Some(id) }) => format!(" — organization {name} ({id})"),
        Some(OrgLabel { name, id: None }) => format!(" — organization {name}"),
        None => String::new(),
    })
}

/// Map a typed unavailable reason to user-facing remediation. Only the wrong-class arm
/// inspects the key, and only its short, non-secret class prefix (see [`redacted_prefix`]).
fn remediation(vendor: ApiVendor, reason: &VendorReportUnavailable, key: &SecretString) -> String {
    match reason {
        VendorReportUnavailable::WrongKeyClass { expected_prefix } => format!(
            "that looks like a \"{}\" key; {vendor} usage needs a {expected_prefix}… admin key.",
            redacted_prefix(key)
        ),
        VendorReportUnavailable::AuthenticationFailed => {
            "the key was rejected (check it is a current admin key).".to_string()
        }
        VendorReportUnavailable::AccessForbidden { hint } => match hint {
            AccessForbiddenHint::IndividualAccount => {
                "the Admin API is unavailable for individual accounts — create an organization first."
                    .to_string()
            }
            AccessForbiddenHint::MemberNotOwner => {
                "use an admin key created by an organization Owner.".to_string()
            }
            AccessForbiddenHint::AwsOrg => {
                "the Admin API is unavailable for Claude-on-AWS organizations.".to_string()
            }
            AccessForbiddenHint::Unknown => "access was forbidden for this key.".to_string(),
        },
        // RateLimited / ServerUnavailable / RequestRejected / NotConnected / NoSanctioned…
        other => format!("{}.", other.message()),
    }
}

/// The leading, **non-secret** class prefix of a pasted key, for an error message —
/// capped at 8 chars (enough to show the class family like `sk-proj-` / `sk-ant-a` /
/// `sk-admin`, short enough to never reach the random secret body of any known key
/// format). The key is inspected ONLY here, via `expose_secret()`, never logged or fully
/// echoed; the result is plain ASCII so it needs no folding.
fn redacted_prefix(key: &SecretString) -> String {
    const MAX: usize = 8;
    let exposed = key.expose_secret();
    let mut prefix: String = exposed.chars().take(MAX).collect();
    if exposed.chars().nth(MAX).is_some() {
        prefix.push_str("...");
    }
    prefix
}

/// The pinned Gemini "unavailable" line (`GEMINI_UNAVAILABLE_MESSAGE`), via the typed
/// reason so the exact string stays single-sourced in `costroid-core`.
fn gemini_unavailable_message() -> String {
    VendorReportUnavailable::NoSanctionedStaticKeyApi.message()
}

/// Write one connect line, sanitized. First strip every control character (defense in
/// depth: a terminal escape smuggled through a server-provided org label must never reach
/// the terminal — labels are also stripped at ingestion via [`OrgLabel::from_server`]). On
/// a UTF-8 (braille) TTY printable Unicode is then kept; for `--plain` / a non-UTF-8
/// terminal, fold the known glyphs (em-dash `—`, ellipsis `…`) and map any remaining
/// non-ASCII to `?` so `--plain` output is guaranteed pure ASCII regardless of label content.
fn emit(out: &mut dyn Write, style: OutputStyle, line: &str) -> io::Result<()> {
    let sanitized: String = line.chars().filter(|ch| !ch.is_control()).collect();
    let folded: String = if style.ascii {
        sanitized
            .replace('—', "-")
            .replace('…', "...")
            .chars()
            .map(|ch| if ch.is_ascii() { ch } else { '?' })
            .collect()
    } else {
        sanitized
    };
    writeln!(out, "{folded}")
}

/// The connect-time blast-radius warning (T9 pin §2.3/§6, ⛔-signed-off): an admin key is
/// **organization-wide**, so warn at paste time and recommend a dedicated, instantly-
/// revocable key. Shown for anthropic/openai only — gemini reads no key, so
/// [`gemini_connect`] never calls this. Routed through [`emit`], so `--plain` stays pure ASCII.
pub fn print_connect_warning(
    out: &mut dyn Write,
    style: OutputStyle,
    vendor: ApiVendor,
) -> io::Result<()> {
    emit(
        out,
        style,
        &format!(
            "Heads up: a {vendor} admin key is organization-wide — it can read your whole \
             organization's usage and billing (and, depending on the key, manage members and \
             keys). Treat it like a root credential."
        ),
    )?;
    emit(
        out,
        style,
        &format!(
            "Best practice: create a dedicated admin key you can revoke instantly, and revoke it \
             in the {vendor} console if this machine is ever compromised. Costroid stores it only \
             in your OS keychain — never on disk, in a config file, or in a log — and sends it \
             only to {vendor}."
        ),
    )
}

// ---------------------------------------------------------------------------
// Layer-1 connect-action test (proposal §7): drives this command core against the
// cfg(test) loopback MockServer + keyring mock — zero real network, zero real keychain.
// Gated on the CLI `connect-test-support` feature so plain `--features connect` (the CI
// lint) does not pull in `costroid-connect/test-support`.
// ---------------------------------------------------------------------------
#[cfg(all(test, feature = "connect-test-support"))]
mod tests {
    use super::*;
    use costroid_connect::test_support::{
        install_mock_keychain, ok_json, reply, serve_sequence, MockServer,
    };
    use std::ffi::OsString;
    use std::fs;
    use std::path::PathBuf;

    // The workspace clippy lints deny `.unwrap()`/`.expect()` even in tests.
    #[track_caller]
    fn okv<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }
    #[track_caller]
    fn somev<T>(value: Option<T>) -> T {
        match value {
            Some(value) => value,
            None => panic!("expected Some, got None"),
        }
    }

    fn style() -> OutputStyle {
        OutputStyle { ascii: true }
    }

    /// An auto-cleaned temp dir (no `tempfile` dep, to keep the offline forbidden-crates
    /// graph clean), mirroring the connect crate's own test helper.
    struct TempDir {
        path: PathBuf,
    }
    impl TempDir {
        fn new(tag: &str) -> Self {
            static COUNTER: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
            let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let path = std::env::temp_dir()
                .join(format!("costroid-t10a-{tag}-{}-{n}", std::process::id()));
            let _ = fs::remove_dir_all(&path);
            okv(fs::create_dir_all(&path));
            Self { path }
        }
    }
    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.path);
        }
    }

    /// A loopback [`AdapterSet`]: drives the REAL adapters over a local `TcpListener`
    /// (the off-host guarantee is enforced by the type before any I/O — already
    /// T9a-tested — so the only egress this can ever make is to 127.0.0.1).
    struct LoopbackAdapters {
        server: MockServer,
    }
    impl AdapterSet for LoopbackAdapters {
        fn validate_anthropic(&self, key: &SecretString) -> Result<OrgValidation, ConnectError> {
            self.server.anthropic_adapter().validate(key)
        }
        fn probe_openai(
            &self,
            key: &SecretString,
            range: DateRange,
        ) -> Result<CostReportOutcome, ConnectError> {
            self.server.openai_adapter().fetch_cost_report(key, range)
        }
        fn cost_report(
            &self,
            vendor: ApiVendor,
            store: &CredentialStore,
            range: DateRange,
        ) -> Result<CostReportOutcome, ConnectError> {
            match vendor {
                ApiVendor::Anthropic => self.server.anthropic_adapter().cost_report(store, range),
                ApiVendor::OpenAI => self.server.openai_adapter().cost_report(store, range),
                ApiVendor::Gemini => Ok(CostReportOutcome::Unavailable(
                    VendorReportUnavailable::NoSanctionedStaticKeyApi,
                )),
            }
        }
    }

    const ME_BODY: &str = r#"{"id":"org-abc","name":"Erens Org","type":"organization"}"#;
    const EMPTY_COSTS: &str = r#"{"object":"page","has_more":false,"next_page":null,"data":[]}"#;

    #[test]
    fn connect_anthropic_stores_secret_only_in_keychain_and_records_the_label() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connect-anthropic");
        let reg_path = dir.path.join("connections.json");
        let registry = ConnectionRegistry::at(reg_path.clone());
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![ok_json(ME_BODY)]),
        };
        let key = SecretString::from("sk-ant-admin-FAKE0001".to_string());

        let mut out = Vec::new();
        let code = okv(run_connect(
            ApiVendor::Anthropic,
            key,
            &adapters,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);

        // (b) The secret lands ONLY in the (mock) keychain; the org label is recorded.
        assert!(somev(okv(store.retrieve(ApiVendor::Anthropic)))
            .expose_secret()
            .starts_with("sk-ant-admin"));
        assert!(okv(registry.is_connected(ApiVendor::Anthropic)));
        assert_eq!(
            somev(okv(registry.label(ApiVendor::Anthropic))).name,
            "Erens Org"
        );

        // (b cont.) The ONLY disk artifact is the registry file — the keychain wrote
        // nothing to disk — and it carries no secret material.
        let files: Vec<OsString> = okv(fs::read_dir(&dir.path))
            .filter_map(Result::ok)
            .map(|entry| entry.file_name())
            .collect();
        assert_eq!(files, vec![OsString::from("connections.json")]);
        let raw = okv(fs::read_to_string(&reg_path));
        assert!(
            !raw.contains("sk-ant-admin"),
            "no secret in registry: {raw}"
        );

        assert!(String::from_utf8_lossy(&out).contains("Connected anthropic"));
    }

    #[test]
    fn connect_warning_is_shown_for_real_vendors_and_absent_for_gemini() {
        // The mandated org-wide blast-radius warning (T9 pin §2.3/§6) shows for the two
        // key-taking vendors, and is pure ASCII under --plain.
        for vendor in [ApiVendor::Anthropic, ApiVendor::OpenAI] {
            let mut out = Vec::new();
            okv(print_connect_warning(&mut out, style(), vendor));
            let text = String::from_utf8_lossy(&out);
            assert!(
                text.contains("organization-wide") && text.contains("revoke"),
                "{vendor} connect must warn about the org-wide admin key + revocation: {text}"
            );
            assert!(
                text.is_ascii(),
                "plain connect warning must be ASCII: {text}"
            );
        }
        // gemini reads no key, so its connect path must NOT carry the admin-key warning.
        let mut out = Vec::new();
        okv(gemini_connect(&mut out, style()));
        assert!(
            !String::from_utf8_lossy(&out).contains("organization-wide"),
            "gemini connect must not show the admin-key warning"
        );
    }

    #[test]
    fn org_label_from_server_is_sanitized_and_renders_safely() {
        // A hostile, server-controlled org name: a terminal escape + printable non-ASCII.
        let label = OrgLabel::from_server(
            "\u{1b}[31mEvil\u{1b}[0m Café",
            Some("org-\u{1b}123".to_string()),
        );
        // Ingestion strips every control char (incl. ESC) but keeps printable non-ASCII.
        assert!(!label.name.chars().any(|c| c.is_control()));
        assert!(label.name.contains("Café"));
        assert!(!somev(label.id.as_ref()).chars().any(|c| c.is_control()));

        // Rendered in --plain (ascii): no control char leaks, and pure ASCII (the café 'é'
        // folds to '?' at the render boundary so the ASCII floor holds for any label).
        let mut out = Vec::new();
        okv(emit(
            &mut out,
            style(),
            &connected_message(ApiVendor::Anthropic, Some(&label)),
        ));
        let text = String::from_utf8_lossy(&out);
        assert!(text.is_ascii(), "plain output must be ASCII: {text}");
        assert!(!text.contains('\u{1b}'), "no escape leaks: {text}");
    }

    #[test]
    fn connect_openai_probes_costs_endpoint_and_stores_on_200() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connect-openai");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![ok_json(EMPTY_COSTS)]),
        };
        let key = SecretString::from("sk-admin-FAKE0002".to_string());

        let mut out = Vec::new();
        let code = okv(run_connect(
            ApiVendor::OpenAI,
            key,
            &adapters,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        assert!(somev(okv(store.retrieve(ApiVendor::OpenAI)))
            .expose_secret()
            .starts_with("sk-admin-"));
        assert!(okv(registry.is_connected(ApiVendor::OpenAI)));
        // The probe hit the exact endpoint T10c reconciliation depends on.
        let request = okv(adapters.server.next_request());
        assert!(
            request.contains("GET /v1/organization/costs"),
            "the OpenAI probe must hit /costs: {request}"
        );
        assert!(String::from_utf8_lossy(&out).contains("Connected openai"));
    }

    #[test]
    fn a_rejected_key_is_not_stored() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connect-rejected");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![reply("401 Unauthorized", &[], "{}")]),
        };
        let key = SecretString::from("sk-ant-admin-REJECTED".to_string());

        let mut out = Vec::new();
        let code = okv(run_connect(
            ApiVendor::Anthropic,
            key,
            &adapters,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 1);
        assert!(
            okv(store.retrieve(ApiVendor::Anthropic)).is_none(),
            "a rejected key must NOT be stored"
        );
        assert!(!okv(registry.is_connected(ApiVendor::Anthropic)));
        assert!(String::from_utf8_lossy(&out)
            .to_lowercase()
            .contains("rejected"));
    }

    #[test]
    fn a_wrong_class_key_is_refused_without_any_request() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connect-wrongclass");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        // No reply queued: a wrong-class key must be refused BEFORE any request.
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![]),
        };
        let key = SecretString::from("sk-ant-api03-not-admin-secret".to_string());

        let mut out = Vec::new();
        let code = okv(run_connect(
            ApiVendor::Anthropic,
            key,
            &adapters,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 1);
        assert!(okv(store.retrieve(ApiVendor::Anthropic)).is_none());
        let rendered = String::from_utf8_lossy(&out);
        assert!(
            rendered.contains("sk-ant-admin"),
            "expected-prefix remediation: {rendered}"
        );
        // The full secret body is never echoed — only a short class prefix.
        assert!(
            !rendered.contains("not-admin-secret"),
            "the key must not be echoed: {rendered}"
        );
        // ascii style folds the em-dash AND the `…` ellipsis — --plain output is pure ASCII.
        assert!(
            rendered.is_ascii(),
            "ascii-mode output must be ASCII: {rendered}"
        );
    }

    #[test]
    fn disconnect_removes_key_registry_entry_and_label() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("disconnect");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![ok_json(ME_BODY)]),
        };
        let _ = okv(run_connect(
            ApiVendor::Anthropic,
            SecretString::from("sk-ant-admin-FAKE0003".to_string()),
            &adapters,
            &store,
            &registry,
            &mut Vec::new(),
            style(),
        ));
        assert!(okv(store.retrieve(ApiVendor::Anthropic)).is_some());

        let mut out = Vec::new();
        let code = okv(run_disconnect(
            ApiVendor::Anthropic,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        assert!(okv(store.retrieve(ApiVendor::Anthropic)).is_none());
        assert!(!okv(registry.is_connected(ApiVendor::Anthropic)));
        assert!(okv(registry.label(ApiVendor::Anthropic)).is_none());
        assert!(String::from_utf8_lossy(&out).contains("Disconnected anthropic"));
    }

    #[test]
    fn disconnect_is_idempotent_with_nothing_stored() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("disconnect-empty");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let mut out = Vec::new();
        let code = okv(run_disconnect(
            ApiVendor::OpenAI,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        assert!(String::from_utf8_lossy(&out).contains("Disconnected openai"));
    }

    #[test]
    fn connections_lists_local_state_without_network() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connections-list");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        // Connect anthropic so it shows as connected with a label.
        let _ = okv(run_connect(
            ApiVendor::Anthropic,
            SecretString::from("sk-ant-admin-FAKE0004".to_string()),
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(ME_BODY)]),
            },
            &store,
            &registry,
            &mut Vec::new(),
            style(),
        ));

        // Local-only list: adapters are unused (no --check) — a no-reply server proves it
        // makes no request.
        let dummy = LoopbackAdapters {
            server: serve_sequence(vec![]),
        };
        let mut out = Vec::new();
        let code = okv(run_connections(
            false,
            &dummy,
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);
        assert!(rendered.contains("anthropic") && rendered.contains("connected"));
        assert!(rendered.contains("Erens Org"));
        assert!(rendered.contains("openai") && rendered.contains("not connected"));
        assert!(
            rendered.contains("gemini") && rendered.contains("no sanctioned static-key usage API")
        );
        // The em-dash was ASCII-folded (style().ascii = true).
        assert!(
            !rendered.contains('—'),
            "em-dash must be folded in ascii mode: {rendered}"
        );
    }

    #[test]
    fn connections_check_revalidates_a_connected_vendor() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connections-check");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let _ = okv(run_connect(
            ApiVendor::Anthropic,
            SecretString::from("sk-ant-admin-FAKE0005".to_string()),
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(ME_BODY)]),
            },
            &store,
            &registry,
            &mut Vec::new(),
            style(),
        ));
        // --check re-validates the one connected vendor (one more /me request).
        let mut out = Vec::new();
        let code = okv(run_connections(
            true,
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(ME_BODY)]),
            },
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        assert!(String::from_utf8_lossy(&out).contains("verified just now"));
    }

    #[test]
    fn connections_check_surfaces_a_failed_revalidation() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connections-check-fail");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let _ = okv(run_connect(
            ApiVendor::Anthropic,
            SecretString::from("sk-ant-admin-FAKE0006".to_string()),
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(ME_BODY)]),
            },
            &store,
            &registry,
            &mut Vec::new(),
            style(),
        ));
        // --check re-validates and this time the key is rejected (401) → surfaced as text.
        let mut out = Vec::new();
        let code = okv(run_connections(
            true,
            &LoopbackAdapters {
                server: serve_sequence(vec![reply("401 Unauthorized", &[], "{}")]),
            },
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);
        assert!(
            rendered.contains("check failed"),
            "a failed re-validation must be surfaced as text: {rendered}"
        );
        assert!(
            rendered.is_ascii(),
            "ascii-mode output must be ASCII: {rendered}"
        );
    }

    #[test]
    fn connections_check_revalidates_a_connected_openai_vendor() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("connections-check-openai");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        let _ = okv(run_connect(
            ApiVendor::OpenAI,
            SecretString::from("sk-admin-FAKE0007".to_string()),
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(EMPTY_COSTS)]),
            },
            &store,
            &registry,
            &mut Vec::new(),
            style(),
        ));
        // --check drives the OpenAI /costs probe branch (distinct from Anthropic's /me).
        let mut out = Vec::new();
        let code = okv(run_connections(
            true,
            &LoopbackAdapters {
                server: serve_sequence(vec![ok_json(EMPTY_COSTS)]),
            },
            &store,
            &registry,
            &mut out,
            style(),
        ));
        assert_eq!(code, 0);
        assert!(String::from_utf8_lossy(&out).contains("verified just now"));
    }

    #[test]
    fn gemini_connect_prints_unavailable_and_stores_nothing() {
        let mut out = Vec::new();
        let code = okv(gemini_connect(&mut out, style()));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);
        assert!(rendered.contains("no sanctioned static-key usage API"));
        // The pinned message's em-dash is ASCII-folded in ascii style.
        assert!(
            rendered.is_ascii(),
            "ascii-mode output must be ASCII: {rendered}"
        );
    }
}
