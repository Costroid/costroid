//! The OpenAI Organization **Usage + Costs** API adapter (T9b).
//!
//! Turns a stored admin key (`sk-admin-…`) + a [`DateRange`] into authorized GET calls
//! against the two pinned endpoints, parses the documented response shapes into
//! provider-neutral [`costroid_core::vendor_report`] values, and surfaces the documented
//! unavailable states as first-class data.
//!
//! Pinned by `docs/proposals/T9-PIN-PROPOSAL.md` §3 (⛔ signed off 2026-06-10):
//!
//! * Host `api.openai.com`; auth **`Authorization: Bearer <key>`** (no version header);
//!   key class **`sk-admin-…`** (project/standard keys cannot call these endpoints);
//!   use the **`/v1`** path form.
//! * `GET /v1/organization/costs` — billed USD/day; money is **float dollars**
//!   (`amount.value`), parsed from the JSON number's **literal text** (never `f64`).
//!   `group_by` has **no `model`** option → per-model dollars are **derived/best-effort**
//!   from the undocumented `line_item` string (a typed caveat + per-row confidence).
//! * `GET /v1/organization/usage/completions` — tokens by model (`group_by=model`);
//!   `input_tokens` **includes** cached, so the uncached input is `input_tokens −
//!   input_cached_tokens`. May **not** cover Responses-API traffic (Codex) — a typed
//!   caveat until the Gate-2 live check confirms it.
//! * Times are Unix seconds; pagination is a `page` cursor, stop on `has_more=false`.
//!
//! Secrets ride only in [`AuthHeader`] values (redacting `Debug`), **never** in a URL.

use std::time::Duration;

use secrecy::{ExposeSecret, SecretString};
use serde::Deserialize;
use serde_json::value::RawValue;

use costroid_core::vendor_report::{
    utc_date_from_unix_seconds, AccessForbiddenHint, AmountConfidence, CostLineItem,
    CostReportCaveats, CostReportOutcome, DateRange, ModelTokenUsage, MoneyParseError,
    UsageReportCaveats, UsageReportOutcome, UsdAmount, VendorCostDay, VendorCostReport,
    VendorReportUnavailable, VendorUsageDay, VendorUsageReport,
};

use crate::fetch::{build_query, fetch_page, PageOutcome, RetryPolicy};
use crate::{AuthHeader, AuthorizedClient, ConnectError, CredentialStore};

const HOST: &str = "api.openai.com";
/// Admin-key class these endpoints require; project/standard keys are rejected.
const ADMIN_KEY_PREFIX: &str = "sk-admin-";
const COSTS_PATH: &str = "/v1/organization/costs";
const USAGE_PATH: &str = "/v1/organization/usage/completions";
/// Costs accepts up to 180 buckets/page (1d) — request the max and paginate beyond it.
const COSTS_PAGE_LIMIT: &str = "180";
/// Usage caps `1d` at 31 buckets/page.
const USAGE_PAGE_LIMIT: &str = "31";
/// A hard ceiling on pages, so a misbehaving `has_more=true` server cannot loop forever.
const MAX_PAGES: u32 = 1024;
/// This adapter's OWN explicit request bounds (T9a contract note 2 — never pass arbitrary
/// caller limits through). The body cap is raised above the 8 MiB default because a
/// multi-model, full-month usage page can be large; timeouts are conservative.
const REQUEST_LIMITS: crate::RequestLimits = crate::RequestLimits {
    connect_timeout: Duration::from_secs(10),
    overall_timeout: Duration::from_secs(30),
    max_body_bytes: 16 * 1024 * 1024,
};

/// The OpenAI Usage + Costs adapter, bound to `api.openai.com`.
pub struct OpenAiAdapter {
    client: AuthorizedClient,
    retry: RetryPolicy,
}

impl OpenAiAdapter {
    /// Build the adapter over the pinned host with this adapter's explicit
    /// [`RequestLimits`] and the default retry policy. Fails only if the OS-native TLS
    /// roots cannot be loaded.
    ///
    /// [`RequestLimits`]: crate::RequestLimits
    pub fn new() -> Result<Self, ConnectError> {
        Ok(Self {
            client: AuthorizedClient::with_limits(HOST, REQUEST_LIMITS)?,
            retry: RetryPolicy::default(),
        })
    }

    /// Test seam: build over an injected (loopback) client + retry policy. Compiled under
    /// `feature = "test-support"` too, so [`crate::test_support`] can hand a dependent
    /// crate a loopback-backed adapter; it stays `pub(crate)` (no public escape hatch).
    #[cfg(any(test, feature = "test-support"))]
    pub(crate) fn with_client(client: AuthorizedClient, retry: RetryPolicy) -> Self {
        Self { client, retry }
    }

    /// Fetch the billed-cost report for `range`, reading the admin key from `store`.
    /// The full key flow: retrieve from the keychain → compose `AuthHeader` → use →
    /// never log/echo/serialize the secret.
    pub fn cost_report(
        &self,
        store: &CredentialStore,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError> {
        match store.retrieve(crate::ApiVendor::OpenAI)? {
            Some(key) => self.fetch_cost_report(&key, range),
            None => Ok(CostReportOutcome::Unavailable(
                VendorReportUnavailable::NotConnected,
            )),
        }
    }

    /// Fetch the token-usage report for `range`, reading the admin key from `store`.
    pub fn usage_report(
        &self,
        store: &CredentialStore,
        range: DateRange,
    ) -> Result<UsageReportOutcome, ConnectError> {
        match store.retrieve(crate::ApiVendor::OpenAI)? {
            Some(key) => self.fetch_usage_report(&key, range),
            None => Ok(UsageReportOutcome::Unavailable(
                VendorReportUnavailable::NotConnected,
            )),
        }
    }

    /// Fetch the cost report with a caller-supplied key (the testable seam).
    pub fn fetch_cost_report(
        &self,
        key: &SecretString,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError> {
        if let Some(unavailable) = wrong_key_class(key) {
            return Ok(CostReportOutcome::Unavailable(unavailable));
        }
        let headers = self.headers(key);
        let mut days: Vec<VendorCostDay> = Vec::new();
        let mut page: Option<String> = None;
        let mut fetched = 0u32;
        loop {
            let query = costs_query(range, page.as_deref());
            let path = format!("{COSTS_PATH}{query}");
            match fetch_page(
                &self.client,
                &path,
                &headers,
                &self.retry,
                classify_forbidden,
            )? {
                PageOutcome::Unavailable(reason) => {
                    return Ok(CostReportOutcome::Unavailable(reason))
                }
                PageOutcome::Body(bytes) => {
                    let parsed: CostsResponse = parse_json(&bytes, "costs")?;
                    for bucket in parsed.data {
                        days.push(costs_bucket_to_day(bucket)?);
                    }
                    match next_page(parsed.has_more, parsed.next_page, &mut fetched)? {
                        Some(token) => page = Some(token),
                        None => break,
                    }
                }
            }
        }
        Ok(CostReportOutcome::Available(VendorCostReport {
            days,
            caveats: CostReportCaveats {
                priority_tier_absent: false,
                // Per-model $ is derived from the undocumented line_item — best-effort.
                per_model_derived_best_effort: true,
            },
        }))
    }

    /// Fetch the usage report with a caller-supplied key (the testable seam).
    pub fn fetch_usage_report(
        &self,
        key: &SecretString,
        range: DateRange,
    ) -> Result<UsageReportOutcome, ConnectError> {
        if let Some(unavailable) = wrong_key_class(key) {
            return Ok(UsageReportOutcome::Unavailable(unavailable));
        }
        let headers = self.headers(key);
        let mut days: Vec<VendorUsageDay> = Vec::new();
        let mut page: Option<String> = None;
        let mut fetched = 0u32;
        loop {
            let query = usage_query(range, page.as_deref());
            let path = format!("{USAGE_PATH}{query}");
            match fetch_page(
                &self.client,
                &path,
                &headers,
                &self.retry,
                classify_forbidden,
            )? {
                PageOutcome::Unavailable(reason) => {
                    return Ok(UsageReportOutcome::Unavailable(reason))
                }
                PageOutcome::Body(bytes) => {
                    let parsed: UsageResponse = parse_json(&bytes, "usage/completions")?;
                    for bucket in parsed.data {
                        days.push(usage_bucket_to_day(bucket)?);
                    }
                    match next_page(parsed.has_more, parsed.next_page, &mut fetched)? {
                        Some(token) => page = Some(token),
                        None => break,
                    }
                }
            }
        }
        Ok(UsageReportOutcome::Available(VendorUsageReport {
            days,
            caveats: UsageReportCaveats {
                // Responses-API (Codex) coverage CONFIRMED by the 2026-06-14 GATE-2b live
                // run: a Responses call (15 in / 27 out) surfaced in `usage/completions` in
                // the same bucket as a Chat call (total num_model_requests: 2, 30 in / 57
                // out). The token side is complete, so this is `false` and T10c carries no
                // token-undercount caveat. (§11.5 ✅ T10a / T10-LIVE-ROWS §12.16.)
                responses_api_coverage_unconfirmed: false,
            },
        }))
    }

    /// Compose the single `Authorization: Bearer <key>` header. Built with **exact**
    /// capacity so the `String → SecretString` conversion (`into_boxed_str` →
    /// `shrink_to_fit`) does not reallocate and leave an un-zeroized plaintext remnant on
    /// the heap; the live `SecretString` zeroizes on drop. (The value is still copied into
    /// `ureq`'s non-zeroizing `HeaderValue` when sent — an inherent limit of this layer —
    /// so this minimizes, not eliminates, in-memory copies. Same limit as the keychain's
    /// `retrieve`.)
    fn headers(&self, key: &SecretString) -> [AuthHeader; 1] {
        const PREFIX: &str = "Bearer ";
        let exposed = key.expose_secret();
        let mut value = String::with_capacity(PREFIX.len() + exposed.len());
        value.push_str(PREFIX);
        value.push_str(exposed);
        [AuthHeader::new("authorization", SecretString::from(value))]
    }
}

/// Reject a non-admin key from its prefix before any request is sent.
fn wrong_key_class(key: &SecretString) -> Option<VendorReportUnavailable> {
    if key.expose_secret().starts_with(ADMIN_KEY_PREFIX) {
        None
    } else {
        Some(VendorReportUnavailable::WrongKeyClass {
            expected_prefix: ADMIN_KEY_PREFIX.to_string(),
        })
    }
}

/// Map a 403 body to a finer hint. The documented dominant cause of an admin-endpoint
/// 403 is a non-Owner member; the exact body string is uncontracted (a Gate-2 live
/// check), so match only the specific `owner` signal and otherwise stay `Unknown` — a
/// broad match (e.g. on "admin"/"permission") risks mis-hinting a generic 403.
fn classify_forbidden(body: Option<&str>) -> AccessForbiddenHint {
    match body {
        Some(text) if text.to_ascii_lowercase().contains("owner") => {
            AccessForbiddenHint::MemberNotOwner
        }
        _ => AccessForbiddenHint::Unknown,
    }
}

fn costs_query(range: DateRange, page: Option<&str>) -> String {
    let mut params: Vec<(&str, String)> = vec![
        ("start_time", range.start_unix().to_string()),
        ("end_time", range.end_unix().to_string()),
        ("bucket_width", "1d".to_string()),
        // line_item is the only breakdown that hints at per-model $ (best-effort).
        ("group_by", "line_item".to_string()),
        ("limit", COSTS_PAGE_LIMIT.to_string()),
    ];
    if let Some(token) = page {
        params.push(("page", token.to_string()));
    }
    build_query(&params)
}

fn usage_query(range: DateRange, page: Option<&str>) -> String {
    let mut params: Vec<(&str, String)> = vec![
        ("start_time", range.start_unix().to_string()),
        ("end_time", range.end_unix().to_string()),
        ("bucket_width", "1d".to_string()),
        ("group_by", "model".to_string()),
        ("limit", USAGE_PAGE_LIMIT.to_string()),
    ];
    if let Some(token) = page {
        params.push(("page", token.to_string()));
    }
    build_query(&params)
}

fn next_page(
    has_more: bool,
    token: Option<String>,
    fetched: &mut u32,
) -> Result<Option<String>, ConnectError> {
    *fetched = fetched.saturating_add(1);
    if !has_more {
        return Ok(None);
    }
    if *fetched >= MAX_PAGES {
        return Err(ConnectError::ResponseFormat {
            detail: "openai pagination exceeded the page ceiling".to_string(),
        });
    }
    Ok(token)
}

fn parse_json<'a, T: Deserialize<'a>>(bytes: &'a [u8], what: &str) -> Result<T, ConnectError> {
    serde_json::from_slice(bytes).map_err(|err| ConnectError::ResponseFormat {
        detail: format!("openai {what}: {err}"),
    })
}

fn money_err(err: MoneyParseError) -> ConnectError {
    ConnectError::ResponseFormat {
        detail: format!("openai money: {err}"),
    }
}

fn costs_bucket_to_day(bucket: CostsBucket) -> Result<VendorCostDay, ConnectError> {
    // Inlined (not a helper) so `NaiveDate` need never be named in this crate.
    let date = utc_date_from_unix_seconds(bucket.start_time).ok_or_else(|| {
        ConnectError::ResponseFormat {
            detail: format!("openai bucket time {}", bucket.start_time),
        }
    })?;
    let mut line_items = Vec::with_capacity(bucket.results.len());
    for result in bucket.results {
        let amount =
            UsdAmount::from_json_dollars_str(result.amount.value.get()).map_err(money_err)?;
        let model = result
            .line_item
            .as_deref()
            .and_then(best_effort_model_from_line_item);
        line_items.push(CostLineItem {
            label: result.line_item.unwrap_or_default(),
            amount,
            model,
            cost_type: None,
            service_tier: None,
            // Per-model attribution is derived from the undocumented line_item string.
            confidence: AmountConfidence::DerivedBestEffort,
        });
    }
    VendorCostDay::from_line_items(date, line_items).map_err(money_err)
}

fn usage_bucket_to_day(bucket: UsageBucket) -> Result<VendorUsageDay, ConnectError> {
    let date = utc_date_from_unix_seconds(bucket.start_time).ok_or_else(|| {
        ConnectError::ResponseFormat {
            detail: format!("openai bucket time {}", bucket.start_time),
        }
    })?;
    let usages = bucket
        .results
        .into_iter()
        .map(|result| {
            // OpenAI's input_tokens INCLUDES cached; the uncached input is the remainder.
            let uncached_input = result
                .input_tokens
                .saturating_sub(result.input_cached_tokens);
            ModelTokenUsage {
                model: result.model.unwrap_or_default(),
                input_tokens: uncached_input,
                output_tokens: result.output_tokens,
                cache_read_tokens: result.input_cached_tokens,
                // OpenAI does not report cache-creation tokens.
                cache_creation_tokens: 0,
                num_requests: Some(result.num_model_requests),
            }
        })
        .collect();
    Ok(VendorUsageDay::from_model_usages(date, usages))
}

/// Best-effort model id from an undocumented `line_item` like `"gpt-5.5, input"` →
/// `"gpt-5.5"` (text before the first comma). `None` if empty. Format is uncontracted —
/// every figure derived from it is labeled `DerivedBestEffort` and the report carries the
/// `per_model_derived_best_effort` caveat. (Exact format is a Gate-2 live check.)
fn best_effort_model_from_line_item(line_item: &str) -> Option<String> {
    let head = line_item.split(',').next().unwrap_or("").trim();
    if head.is_empty() {
        None
    } else {
        Some(head.to_string())
    }
}

// ---------------------------------------------------------------------------
// Documented response shapes. `amount.value` is captured as a raw JSON number so it is
// parsed from its literal text into Decimal, never through f64.
// ---------------------------------------------------------------------------

#[derive(Deserialize)]
struct CostsResponse {
    #[serde(default)]
    data: Vec<CostsBucket>,
    #[serde(default)]
    has_more: bool,
    #[serde(default)]
    next_page: Option<String>,
}

#[derive(Deserialize)]
struct CostsBucket {
    start_time: i64,
    #[serde(default)]
    results: Vec<CostsResult>,
}

#[derive(Deserialize)]
struct CostsResult {
    amount: CostAmount,
    #[serde(default)]
    line_item: Option<String>,
}

#[derive(Deserialize)]
struct CostAmount {
    /// The float-dollars value, captured as raw JSON text (parsed via literal text).
    value: Box<RawValue>,
}

#[derive(Deserialize)]
struct UsageResponse {
    #[serde(default)]
    data: Vec<UsageBucket>,
    #[serde(default)]
    has_more: bool,
    #[serde(default)]
    next_page: Option<String>,
}

#[derive(Deserialize)]
struct UsageBucket {
    start_time: i64,
    #[serde(default)]
    results: Vec<UsageResult>,
}

#[derive(Deserialize)]
struct UsageResult {
    #[serde(default)]
    input_tokens: u64,
    #[serde(default)]
    output_tokens: u64,
    #[serde(default)]
    input_cached_tokens: u64,
    #[serde(default)]
    num_model_requests: u64,
    #[serde(default)]
    model: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::{ok, ok_json, reply, serve_sequence, MockServer};

    fn range() -> DateRange {
        ok(DateRange::from_unix_seconds(1_780_272_000, 1_780_444_800).ok_or("range"))
    }

    fn admin_key() -> SecretString {
        SecretString::from("sk-admin-fixture-0001".to_string())
    }

    fn dollars(text: &str) -> UsdAmount {
        ok(UsdAmount::from_json_dollars_str(text))
    }

    const COSTS_PAGE_1: &str = r#"{
        "object":"page",
        "data":[
            {"object":"bucket","start_time":1780272000,"end_time":1780358400,"results":[
                {"object":"organization.costs.result","amount":{"value":1.23,"currency":"usd"},"line_item":"gpt-5.5, input"},
                {"object":"organization.costs.result","amount":{"value":0.77,"currency":"usd"},"line_item":"gpt-5.5, output"}
            ]}
        ],
        "has_more":true,
        "next_page":"page_AAA"
    }"#;

    const COSTS_PAGE_2: &str = r#"{
        "object":"page",
        "data":[
            {"object":"bucket","start_time":1780358400,"end_time":1780444800,"results":[
                {"object":"organization.costs.result","amount":{"value":2.5,"currency":"usd"},"line_item":"o4-mini, input"}
            ]}
        ],
        "has_more":false,
        "next_page":null
    }"#;

    fn costs_for(replies: Vec<Vec<u8>>) -> (CostReportOutcome, MockServer) {
        let server = serve_sequence(replies);
        let adapter = OpenAiAdapter::with_client(server.client(), RetryPolicy::test());
        let outcome = ok(adapter.fetch_cost_report(&admin_key(), range()));
        (outcome, server)
    }

    #[test]
    fn costs_paginate_parse_float_dollars_and_derive_best_effort_model() {
        let (outcome, server) = costs_for(vec![ok_json(COSTS_PAGE_1), ok_json(COSTS_PAGE_2)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert!(
            report.caveats.per_model_derived_best_effort,
            "OpenAI per-model $ must be flagged best-effort"
        );
        assert!(!report.caveats.priority_tier_absent);
        assert_eq!(report.days.len(), 2);

        // Day 1: 1.23 + 0.77 = $2.00 total; per-model gpt-5.5 = $2.00 (best-effort).
        let day1 = &report.days[0];
        assert_eq!(day1.total, dollars("2.00"));
        assert_eq!(day1.by_model.len(), 1);
        assert_eq!(day1.by_model[0].model, "gpt-5.5");
        assert_eq!(day1.by_model[0].amount, dollars("2.00"));
        assert_eq!(
            day1.by_model[0].confidence,
            AmountConfidence::DerivedBestEffort
        );

        // The wire: Bearer auth, group_by=line_item, cursor passed back, no secret in URL.
        let req1 = ok(server.next_request());
        let req2 = ok(server.next_request());
        let line1 = first_line(&req1);
        let line2 = first_line(&req2);
        assert!(line1.starts_with("GET /v1/organization/costs?"));
        assert!(req1
            .to_ascii_lowercase()
            .contains("authorization: bearer sk-admin-fixture-0001"));
        assert!(req1.to_ascii_lowercase().contains("group_by=line_item"));
        assert!(
            line2.contains("page=page_AAA"),
            "cursor must be passed back: {line2}"
        );
        assert!(!line1.contains("sk-admin-"), "no secret in URL: {line1}");
        assert!(!line2.contains("sk-admin-"), "no secret in URL: {line2}");
    }

    #[test]
    fn usage_subtracts_cached_input_and_responses_coverage_is_confirmed() {
        const USAGE: &str = r#"{
            "object":"page",
            "data":[{"object":"bucket","start_time":1780272000,"end_time":1780358400,"results":[
                {"object":"organization.usage.completions.result","input_tokens":1000,"input_cached_tokens":300,"output_tokens":400,"num_model_requests":12,"model":"gpt-5.5"}
            ]}],
            "has_more":false,"next_page":null
        }"#;
        let server = serve_sequence(vec![ok_json(USAGE)]);
        let adapter = OpenAiAdapter::with_client(server.client(), RetryPolicy::test());
        let outcome = ok(adapter.fetch_usage_report(&admin_key(), range()));
        let report = match outcome {
            UsageReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert!(
            !report.caveats.responses_api_coverage_unconfirmed,
            "Responses-API/Codex coverage is confirmed (2026-06-14 live run) — no caveat"
        );
        let model = &report.days[0].by_model[0];
        assert_eq!(model.model, "gpt-5.5");
        assert_eq!(model.input_tokens, 700); // 1000 - 300 cached
        assert_eq!(model.cache_read_tokens, 300);
        assert_eq!(model.cache_creation_tokens, 0);
        assert_eq!(model.output_tokens, 400);
        assert_eq!(model.num_requests, Some(12));

        let req = ok(server.next_request());
        assert!(req.to_ascii_lowercase().contains("group_by=model"));
    }

    #[test]
    fn project_key_is_rejected_before_any_request() {
        let server = serve_sequence(vec![]);
        let adapter = OpenAiAdapter::with_client(server.client(), RetryPolicy::test());
        let key = SecretString::from("sk-proj-not-an-admin-key".to_string());
        let outcome = ok(adapter.fetch_cost_report(&key, range()));
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::WrongKeyClass { .. })
        ));
    }

    #[test]
    fn forbidden_403_classifies_as_member_not_owner() {
        let body =
            r#"{"error":{"message":"You must be an organization owner to access this resource."}}"#;
        let (outcome, _server) = costs_for(vec![reply("403 Forbidden", &[], body)]);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::AccessForbidden {
                hint: AccessForbiddenHint::MemberNotOwner
            })
        ));
    }

    #[test]
    fn rate_limit_degrades_to_unavailable() {
        // RetryPolicy::test() = 2 retries -> 3 requests, all 429 -> degrade (own-suite
        // parity with the Anthropic 429 test; both share the fetch.rs backoff path).
        let replies = vec![
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
        ];
        let (outcome, _server) = costs_for(replies);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::RateLimited)
        ));
    }

    #[test]
    fn costs_404_outage_degrades_to_unavailable() {
        // The documented ~1-day /costs 404 outage must degrade, never hard-fail.
        let replies = vec![
            reply("404 Not Found", &[], ""),
            reply("404 Not Found", &[], ""),
            reply("404 Not Found", &[], ""),
        ];
        let (outcome, _server) = costs_for(replies);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::ServerUnavailable {
                status: 404
            })
        ));
    }

    #[test]
    fn scientific_notation_dollars_parse_without_f64() {
        const COSTS: &str = r#"{"object":"page","data":[
            {"object":"bucket","start_time":1780272000,"end_time":1780358400,"results":[
                {"object":"organization.costs.result","amount":{"value":1.5e-2,"currency":"usd"},"line_item":"gpt-5.5, input"}
            ]}],"has_more":false,"next_page":null}"#;
        let (outcome, _server) = costs_for(vec![ok_json(COSTS)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert_eq!(report.days[0].total, dollars("0.015"));
    }

    fn first_line(request: &str) -> String {
        request.lines().next().unwrap_or_default().to_string()
    }

    #[test]
    fn parses_the_live_empty_results_envelope_ignoring_unknown_fields() {
        // Verbatim api.openai.com 2xx body (Gate-2 live confirm, 2026-06-13; no usage in the
        // window). Confirms the envelope + that unknown fields (`object`, `start_time_iso`,
        // `end_time_iso`) are ignored, and that daily buckets are UTC-midnight aligned. The
        // usage/completions envelope is structurally identical.
        const LIVE_COSTS: &str = r#"{"object":"page","has_more":false,"next_page":null,"data":[{"object":"bucket","start_time":1780704000,"end_time":1780790400,"start_time_iso":"2026-06-06T00:00:00+00:00","end_time_iso":"2026-06-07T00:00:00+00:00","results":[]},{"object":"bucket","start_time":1780790400,"end_time":1780876800,"start_time_iso":"2026-06-07T00:00:00+00:00","end_time_iso":"2026-06-08T00:00:00+00:00","results":[]}]}"#;
        let (outcome, _server) = costs_for(vec![ok_json(LIVE_COSTS)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert_eq!(report.days.len(), 2);
        assert!(report.days.iter().all(|day| day.total == UsdAmount::ZERO));
        assert!(report.caveats.per_model_derived_best_effort);
    }

    #[test]
    fn parses_the_live_gate2b_costs_rows_with_string_scientific_and_overlong_amounts() {
        // Verbatim api.openai.com /costs 2xx body for the COMPLETED 2026-06-14 UTC day
        // (GATE-2b live confirm, 2026-06-15; APPENDIX A). `amount.value` is a JSON STRING,
        // including the scientific-notation zero `0E-6176` and a 39-decimal value — exactly
        // the shapes the OLD parser errored on. `line_item` = "<model>, <direction>". The
        // populated bucket's `start_time_iso` lacks a tz suffix; the second (current)
        // bucket is empty — both ignored fields (the adapter uses `start_time`).
        const LIVE_COSTS: &str = r#"{"object":"page","has_more":false,"next_page":null,"data":[
 {"object":"bucket","start_time":1781395200,"end_time":1781481600,"start_time_iso":"2026-06-14T00:00:00","end_time_iso":"2026-06-15T00:00:00","results":[
   {"object":"organization.costs.result","amount":{"value":"0E-6176","currency":"usd"},"quantity":0.0,"line_item":"gpt-4o-mini-2024-07-18, cached input","project_id":"proj_x","organization_id":"org-x","project_name":"Default project","organization_name":"ErensOrg","user_id":null,"api_key_id":null,"user_email":null},
   {"object":"organization.costs.result","amount":{"value":"0.000004500000000000000000000000000000000","currency":"usd"},"quantity":30.0,"line_item":"gpt-4o-mini-2024-07-18, input","project_id":"proj_x","project_name":"Default project","organization_name":"ErensOrg"},
   {"object":"organization.costs.result","amount":{"value":"0.00003420000000000000000000000000000000","currency":"usd"},"quantity":57.0,"line_item":"gpt-4o-mini-2024-07-18, output","project_id":"proj_x","project_name":"Default project","organization_name":"ErensOrg"}]},
 {"object":"bucket","start_time":1781481600,"end_time":1781568000,"start_time_iso":"2026-06-15T00:00:00+00:00","end_time_iso":"2026-06-16T00:00:00+00:00","results":[]}]}"#;
        let (outcome, _server) = costs_for(vec![ok_json(LIVE_COSTS)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert_eq!(report.days.len(), 2);
        let day = &report.days[0];
        // 0 (the 0E-6176 cached-input row) + 0.0000045 + 0.0000342 = $0.0000387.
        assert_eq!(day.total, dollars("0.0000387"));
        assert_eq!(day.by_model.len(), 1);
        assert_eq!(day.by_model[0].model, "gpt-4o-mini-2024-07-18");
        assert_eq!(day.by_model[0].amount, dollars("0.0000387"));
        assert_eq!(
            day.by_model[0].confidence,
            AmountConfidence::DerivedBestEffort
        );
        // The empty (current) bucket totals $0 — never a fabricated figure.
        assert_eq!(report.days[1].total, UsdAmount::ZERO);
        assert!(report.caveats.per_model_derived_best_effort);
    }

    #[test]
    fn costs_401_from_non_admin_key_is_authentication_failed() {
        // GATE-2b live confirm (2026-06-15): a non-admin key on /costs returns HTTP 401.
        // The connect-time probe (fetch_cost_report over a completed window) classifies it
        // as AuthenticationFailed — the 401-vs-403 classifier branch on real data.
        let (outcome, _server) = costs_for(vec![reply("401 Unauthorized", &[], "")]);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::AuthenticationFailed)
        ));
    }
}
