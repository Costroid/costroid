//! The `costroid reconcile` command core (T10c) — surfaces T9c's estimate-vs-invoice
//! engine ([`costroid_core::reconcile_cost`]) on screen. For each vendor in scope it
//! fetches the billed-cost report (reusing T10a's stored key + authorized client — **no new
//! secret or network boundary**), scopes the local FOCUS rows to that one vendor + the
//! completed-day window, builds the local estimate, reconciles, and renders honestly via
//! [`crate::render::render_reconciliation`].
//!
//! Everything here is `#[cfg(feature = "connect")]` (the module is only declared under the
//! feature), so a default build links none of it and makes no network call. The network
//! half is reached only through the [`AdapterSet`] trait, so the Layer-1 test (at the
//! bottom, under `connect-test-support`) drives the exact same code against the loopback
//! `MockServer` + keyring mock — zero real network, zero real keychain. The renderer itself
//! is a pure function of the core `CostReconciliation` type and is snapshot-tested in the
//! default suite (`render.rs`).
//!
//! Honesty (T9c carries it; this surface must not flatten it): the local figure is ALWAYS
//! labeled an estimate and never presented as the bill; typed vendor-side absence renders as
//! TEXT, never a fabricated `$0`; signed variance carries its over/under direction as text
//! (never color alone); the report's caveats are footnoted. The stored key is only READ to
//! fetch — never logged, serialized, or echoed.

use std::io::Write;

use chrono::{DateTime, Duration, Utc};
use costroid_connect::{
    ApiVendor, ConnectionRegistry, CostReportOutcome, CredentialStore, DateRange,
    VendorReportUnavailable,
};
use costroid_core::{reconcile_cost, FocusRecord, LocalCostEstimate, Period};

use crate::connect::AdapterSet;
use crate::render::{self, RenderOptions};

/// `costroid reconcile [--vendor ..] [--period ..]`: for each vendor in scope, fetch the
/// billed-cost report, compare it to the local API-lane estimate over `window`, and render
/// the signed variance with every typed caveat/absence intact. Returns the process exit
/// code (always 0 — an unavailable invoice is honest output, not an error).
///
/// `vendor_filter` = `Some(v)` reconciles only `v` (shown even when not connected, so the
/// user sees their estimate beside "connect <vendor> first"); `None` reconciles every
/// **connected** billing vendor, always followed by a Gemini "unavailable" section.
#[allow(clippy::too_many_arguments)]
pub fn run_reconcile(
    vendor_filter: Option<ApiVendor>,
    window: DateRange,
    rows: &[FocusRecord],
    adapters: &dyn AdapterSet,
    store: &CredentialStore,
    registry: &ConnectionRegistry,
    out: &mut dyn Write,
    options: RenderOptions,
) -> anyhow::Result<i32> {
    let vendors = vendors_in_scope(vendor_filter, store, registry)?;
    let label = window_label(window);

    // A friendly nudge when nothing billable is connected (the all-vendors case shows only
    // the Gemini unavailable section otherwise).
    if vendor_filter.is_none() && !vendors.iter().any(|vendor| *vendor != ApiVendor::Gemini) {
        writeln!(
            out,
            "No billing vendor connected. Connect one with: costroid connect <anthropic|openai>\n"
        )?;
    }

    for (index, vendor) in vendors.iter().enumerate() {
        if index > 0 {
            writeln!(out)?;
        }
        // Build the local estimate first so it always renders, even when the vendor fetch
        // fails. The fetch reuses the stored key via the adapter (Gemini resolves to the
        // pinned unavailable WITHOUT any network — handled inside `AdapterSet::cost_report`).
        let scoped = scope_rows(rows, *vendor, window);
        let local = LocalCostEstimate::from_focus_records(&scoped)?;
        // A hard fetch error (transport / oversized-or-unparseable body / keychain read —
        // the soft 401/403/429/5xx/4xx outages already degrade to `Unavailable` inside the
        // adapter) degrades to a per-vendor `FetchFailed` so the local estimate still shows
        // and the OTHER connected vendors still reconcile — never aborting the whole view.
        // The reason carries no detail string, so nothing about the error can leak.
        let outcome = match adapters.cost_report(*vendor, store, window) {
            Ok(outcome) => outcome,
            Err(_) => CostReportOutcome::Unavailable(VendorReportUnavailable::FetchFailed),
        };
        let recon = reconcile_cost(&local, &outcome);
        let section = render::render_reconciliation(&vendor.to_string(), &label, &recon, options);
        write!(out, "{section}")?;
    }

    Ok(0)
}

/// Which vendors to reconcile: a single explicit vendor, or every connected billing vendor
/// (Anthropic/OpenAI) plus an always-present Gemini "unavailable" section.
fn vendors_in_scope(
    vendor_filter: Option<ApiVendor>,
    store: &CredentialStore,
    registry: &ConnectionRegistry,
) -> anyhow::Result<Vec<ApiVendor>> {
    if let Some(vendor) = vendor_filter {
        return Ok(vec![vendor]);
    }
    let mut vendors = Vec::new();
    for vendor in [ApiVendor::Anthropic, ApiVendor::OpenAI] {
        // "connected" = the registry mark AND the key present in the keychain (the keychain
        // is the source of truth for the secret).
        if registry.is_connected(vendor)? && store.retrieve(vendor)?.is_some() {
            vendors.push(vendor);
        }
    }
    // Gemini is always shown as a first-class "unavailable" — never a network call.
    vendors.push(ApiVendor::Gemini);
    Ok(vendors)
}

/// Scope FOCUS rows to one vendor's tool **and** the completed-day window before building
/// the estimate (the T9c "scope to one vendor before building" rule): `claude-code` →
/// Anthropic, `codex` → OpenAI, `cursor` → EXCLUDED (no admin key, no invoice). The
/// API-lane filter is left to [`LocalCostEstimate::from_focus_records`].
fn scope_rows(rows: &[FocusRecord], vendor: ApiVendor, window: DateRange) -> Vec<FocusRecord> {
    let tool = match vendor {
        ApiVendor::Anthropic => "claude-code",
        ApiVendor::OpenAI => "codex",
        // No local tool maps to a Gemini admin invoice (Antigravity's $ lane is deferred).
        ApiVendor::Gemini => return Vec::new(),
    };
    rows.iter()
        .filter(|row| row.x_tool == tool)
        .filter(|row| {
            row.charge_period_start >= window.start && row.charge_period_start < window.end
        })
        .cloned()
        .collect()
}

/// The most recent COMPLETED-day window for `period`, ending at today's UTC midnight
/// (EXCLUSIVE — today's incomplete UTC day is excluded so the cost-report fetch never 400s
/// on a current/future window). `day` = the last completed UTC day; `week`/`month`/`year` =
/// the rolling completed window ending at that day. Mirrors T10a's `completed_day_window`.
pub fn completed_window(period: Period) -> DateRange {
    const DAY: i64 = 86_400;
    let days_back = match period {
        Period::Day => 1,
        Period::Week => 7,
        Period::Month => 30,
        Period::Year => 365,
    };
    let now = Utc::now();
    let today_midnight = now.timestamp() - now.timestamp().rem_euclid(DAY);
    match (
        DateTime::<Utc>::from_timestamp(today_midnight - days_back * DAY, 0),
        DateTime::<Utc>::from_timestamp(today_midnight, 0),
    ) {
        (Some(start), Some(end)) => DateRange::new(start, end),
        // Unreachable for these instants; degrade to a trailing window.
        _ => DateRange::new(now - Duration::days(days_back), now),
    }
}

/// A human label for the window: the completed UTC day(s) it spans (`[start, end)` →
/// `start ..= end-1 day`). Plain ASCII (the renderer folds its own glyphs).
fn window_label(window: DateRange) -> String {
    let first = window.start.date_naive();
    let last = (window.end - Duration::days(1)).date_naive();
    if first >= last {
        format!("{first} (UTC, completed day)")
    } else {
        format!("{first} to {last} (UTC, completed days)")
    }
}

// ---------------------------------------------------------------------------
// Layer-1 test: drives this command core against the loopback MockServer + keyring mock —
// zero real network, zero real keychain — exactly the T10a §7 pattern. Gated on the CLI
// `connect-test-support` feature so plain `--features connect` (the CI lint) does not pull
// in `costroid-connect/test-support`.
// ---------------------------------------------------------------------------
#[cfg(all(test, feature = "connect-test-support"))]
mod tests {
    use super::*;
    use costroid_connect::test_support::{
        install_mock_keychain, ok_json, serve_sequence, MockServer,
    };
    use costroid_connect::{ConnectError, CostReportOutcome, OrgValidation, SecretString};
    use costroid_focus::{FocusAccessPath, TokenType, UnpricedUsage};
    use std::ffi::OsString;
    use std::fs;
    use std::path::PathBuf;

    #[track_caller]
    fn okv<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }

    struct TempDir {
        path: PathBuf,
    }
    impl TempDir {
        fn new(tag: &str) -> Self {
            static COUNTER: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
            let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let path = std::env::temp_dir()
                .join(format!("costroid-t10c-{tag}-{}-{n}", std::process::id()));
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

    /// A loopback [`AdapterSet`]: drives the REAL adapters over a local `TcpListener` (the
    /// off-host guarantee is enforced by the type before any I/O — already T9a-tested — so
    /// the only egress this can make is to 127.0.0.1).
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
                    costroid_connect::VendorReportUnavailable::NoSanctionedStaticKeyApi,
                )),
            }
        }
    }

    // A verbatim api.anthropic.com cost_report body for the COMPLETED 2026-06-14 UTC day
    // (the same APPENDIX-A shape the adapter pins): claude-haiku-4-5 billed 0.0515 cents =
    // $0.000515, Exact confidence.
    const LIVE_COST: &str = r#"{"data":[{"starting_at":"2026-06-14T00:00:00Z","ending_at":"2026-06-15T00:00:00Z","results":[
        {"currency":"USD","amount":"0.0045","workspace_id":null,"description":"Input","cost_type":"tokens","context_window":"0-200k","model":"claude-haiku-4-5-20251001","service_tier":"standard","token_type":"uncached_input_tokens","inference_geo":"not_available"},
        {"currency":"USD","amount":"0.047","workspace_id":null,"description":"Output","cost_type":"tokens","context_window":"0-200k","model":"claude-haiku-4-5-20251001","service_tier":"standard","token_type":"output_tokens","inference_geo":"not_available"}]}],"has_more":false,"next_page":null}"#;

    fn utc(y: i32, m: u32, d: u32, h: u32) -> DateTime<Utc> {
        match chrono::TimeZone::with_ymd_and_hms(&Utc, y, m, d, h, 0, 0) {
            chrono::LocalResult::Single(value) => value,
            _ => panic!("bad test instant"),
        }
    }

    /// An API-lane FOCUS row for `tool` at a UTC instant with a billed estimate.
    fn api_row(at: DateTime<Utc>, tool: &str, model: &str, billed: &str) -> FocusRecord {
        let mut row = okv(FocusRecord::unpriced_usage(UnpricedUsage {
            timestamp: at,
            tool: tool.to_string(),
            model: model.to_string(),
            token_type: TokenType::Input,
            token_count: 1_000,
            project: None,
            access_path: FocusAccessPath::Api,
            service_name: "svc".to_string(),
            service_provider_name: "prov".to_string(),
            host_provider_name: "prov".to_string(),
            invoice_issuer_name: "prov".to_string(),
            billing_currency: "USD".to_string(),
        }));
        let cost = match rust_decimal::Decimal::from_str_exact(billed) {
            Ok(value) => value,
            Err(err) => panic!("bad billed {billed:?}: {err:?}"),
        };
        row.billed_cost = cost;
        row.effective_cost = cost;
        row
    }

    /// `[2026-06-14, 2026-06-15)` — covers the fixture vendor day deterministically (the
    /// production window is computed from `now`; the test pins an explicit range).
    fn fixed_window() -> DateRange {
        DateRange::new(utc(2026, 6, 14, 0), utc(2026, 6, 15, 0))
    }

    #[test]
    fn reconcile_anthropic_fetches_loopback_scopes_rows_and_renders_no_real_network() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("reconcile");
        let reg_path = dir.path.join("connections.json");
        let registry = ConnectionRegistry::at(reg_path.clone());

        // Stand up a connected Anthropic key directly in the mock keychain.
        okv(store.store(
            ApiVendor::Anthropic,
            &SecretString::from("sk-ant-admin-FAKE-T10C".to_string()),
        ));
        okv(registry.mark_connected(ApiVendor::Anthropic));

        // Local rows: an in-window claude-code row (reconciles), a cursor row (EXCLUDED — no
        // invoice), and an out-of-window claude-code row (EXCLUDED by the window filter).
        let rows = vec![
            api_row(
                utc(2026, 6, 14, 9),
                "claude-code",
                "claude-haiku-4-5-20251001",
                "0.10",
            ),
            api_row(utc(2026, 6, 14, 10), "cursor", "composer-2.5", "9.99"),
            api_row(
                utc(2026, 6, 1, 9),
                "claude-code",
                "claude-haiku-4-5-20251001",
                "5.55",
            ),
        ];

        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![ok_json(LIVE_COST)]),
        };
        let mut out = Vec::new();
        let code = okv(run_reconcile(
            Some(ApiVendor::Anthropic),
            fixed_window(),
            &rows,
            &adapters,
            &store,
            &registry,
            &mut out,
            RenderOptions::plain(),
        ));
        assert_eq!(code, 0);

        let rendered = String::from_utf8_lossy(&out);
        // The fetch hit the loopback cost_report endpoint.
        let request = okv(adapters.server.next_request());
        assert!(
            request.contains("GET /v1/organizations/cost_report"),
            "the reconcile fetch must hit cost_report: {request}"
        );
        // The in-window claude-code estimate ($0.10) reconciles against the billed $0.000515.
        assert!(
            rendered.contains("est ~$0.10"),
            "scoped estimate: {rendered}"
        );
        assert!(rendered.contains("claude-haiku-4-5-20251001"));
        assert!(
            rendered.contains("over"),
            "estimate exceeds invoice: {rendered}"
        );
        // The cursor row was excluded (no $9.99 anywhere); the out-of-window row excluded too.
        assert!(
            !rendered.contains("9.99"),
            "cursor must be excluded: {rendered}"
        );
        assert!(
            !rendered.contains("5.55"),
            "out-of-window row excluded: {rendered}"
        );
        // Plain output is pure ASCII (accessibility floor).
        assert!(
            rendered.is_ascii(),
            "plain reconcile must be ASCII: {rendered}"
        );

        // The ONLY disk artifact is the non-secret registry file — no secret on disk.
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
    }

    #[test]
    fn reconcile_not_connected_surfaces_estimate_without_network() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("reconcile-unconnected");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));

        // No key stored: cost_report returns NotConnected (as data) — no network needed.
        let rows = vec![api_row(
            utc(2026, 6, 14, 9),
            "claude-code",
            "claude-opus-4-8",
            "2.00",
        )];
        // A no-reply server proves NotConnected short-circuits before any request.
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![]),
        };
        let mut out = Vec::new();
        let code = okv(run_reconcile(
            Some(ApiVendor::Anthropic),
            fixed_window(),
            &rows,
            &adapters,
            &store,
            &registry,
            &mut out,
            RenderOptions::plain(),
        ));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);
        assert!(rendered.contains("vendor invoice unavailable: connect anthropic first"));
        // The local estimate still surfaces; no fabricated delta.
        assert!(rendered.contains("est ~$2.00"));
        assert!(!rendered.contains("over") && !rendered.contains("under"));
    }

    #[test]
    fn reconcile_all_vendors_lists_connected_plus_gemini_unavailable() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("reconcile-all");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));
        // Nothing connected: the all-vendors view shows the nudge + the Gemini section.
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![]),
        };
        let mut out = Vec::new();
        let code = okv(run_reconcile(
            None,
            fixed_window(),
            &[],
            &adapters,
            &store,
            &registry,
            &mut out,
            RenderOptions::plain(),
        ));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);
        assert!(rendered.contains("No billing vendor connected"));
        assert!(rendered.contains("gemini"));
        assert!(rendered.contains(
            costroid_core::GEMINI_UNAVAILABLE_MESSAGE
                .replace('—', "-")
                .as_str()
        ));
        // Nothing is connected, so the Gemini section is the ONLY one rendered (the keychain
        // presence gate did not include anthropic/openai). The nudge line mentions the vendor
        // names, so count sections instead of substring-matching the names.
        assert_eq!(
            rendered.matches("estimate vs invoice").count(),
            1,
            "only the gemini section should render: {rendered}"
        );
    }

    #[test]
    fn reconcile_degrades_one_vendor_fetch_failure_and_still_renders_the_others() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("reconcile-degrade");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));

        // Both billing vendors connected.
        okv(store.store(
            ApiVendor::Anthropic,
            &SecretString::from("sk-ant-admin-FAKE".to_string()),
        ));
        okv(store.store(
            ApiVendor::OpenAI,
            &SecretString::from("sk-admin-FAKE".to_string()),
        ));
        okv(registry.mark_connected(ApiVendor::Anthropic));
        okv(registry.mark_connected(ApiVendor::OpenAI));

        // In-window local rows for each vendor's tool.
        let rows = vec![
            api_row(
                utc(2026, 6, 14, 9),
                "claude-code",
                "claude-haiku-4-5-20251001",
                "0.10",
            ),
            api_row(utc(2026, 6, 14, 9), "codex", "gpt-5.5", "0.20"),
        ];

        // All-vendors order is [Anthropic, OpenAI, Gemini]. Anthropic's cost_report gets a
        // malformed body (a hard parse error → ConnectError); OpenAI's gets a valid empty
        // costs page. Gemini makes no request.
        const BAD_BODY: &str = "this is definitely not json";
        const EMPTY_COSTS: &str =
            r#"{"object":"page","has_more":false,"next_page":null,"data":[]}"#;
        let adapters = LoopbackAdapters {
            server: serve_sequence(vec![ok_json(BAD_BODY), ok_json(EMPTY_COSTS)]),
        };

        let mut out = Vec::new();
        let code = okv(run_reconcile(
            None,
            fixed_window(),
            &rows,
            &adapters,
            &store,
            &registry,
            &mut out,
            RenderOptions::plain(),
        ));
        assert_eq!(code, 0);
        let rendered = String::from_utf8_lossy(&out);

        // Anthropic degraded to a fetch-failure section — but its local estimate still shows.
        assert!(
            rendered.contains("the invoice request could not be completed"),
            "anthropic fetch failure degrades (does not abort): {rendered}"
        );
        assert!(
            rendered.contains("est ~$0.10"),
            "anthropic local estimate still surfaces under a fetch failure: {rendered}"
        );
        // OpenAI still reconciled — NOT blanked by Anthropic's failure.
        assert!(
            rendered.contains("est ~$0.20"),
            "the other vendor still reconciles: {rendered}"
        );
        // Gemini's unavailable section is still present (all-vendors view).
        assert!(
            rendered.contains("gemini"),
            "gemini section present: {rendered}"
        );
        // No secret leaked into the degraded output.
        assert!(
            !rendered.contains("sk-ant-admin") && !rendered.contains("sk-admin"),
            "no secret in degraded output: {rendered}"
        );
        // Pure ASCII (--plain floor).
        assert!(
            rendered.is_ascii(),
            "plain reconcile must be ASCII: {rendered}"
        );
    }

    #[test]
    fn completed_window_excludes_todays_incomplete_utc_day() {
        // Whatever "now" is, the window end is a past/least-current UTC midnight and the
        // start precedes it — today's partial day is never in range.
        for (period, days) in [
            (Period::Day, 1),
            (Period::Week, 7),
            (Period::Month, 30),
            (Period::Year, 365),
        ] {
            let window = completed_window(period);
            assert!(window.end <= Utc::now(), "{period:?} end excludes today");
            assert_eq!(
                window.end - window.start,
                Duration::days(days),
                "{period:?} spans {days} completed days"
            );
            // The end is UTC midnight (a whole number of days from the epoch).
            assert_eq!(
                window.end.timestamp() % 86_400,
                0,
                "{period:?} end is UTC midnight"
            );
        }
    }

    #[test]
    fn window_label_distinguishes_a_single_day_from_a_span() {
        let single = window_label(DateRange::new(utc(2026, 6, 14, 0), utc(2026, 6, 15, 0)));
        assert_eq!(single, "2026-06-14 (UTC, completed day)");
        let span = window_label(DateRange::new(utc(2026, 6, 8, 0), utc(2026, 6, 15, 0)));
        assert_eq!(span, "2026-06-08 to 2026-06-14 (UTC, completed days)");
    }
}
