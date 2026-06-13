//! The Anthropic Admin **Usage & Cost** API adapter (T9b).
//!
//! Turns a stored admin key (`sk-ant-admin…`) + a [`DateRange`] into authorized GET
//! calls against the two pinned endpoints, parses the documented response shapes into
//! provider-neutral [`costroid_core::vendor_report`] values, and surfaces the documented
//! unavailable states as first-class data. **No provider knowledge leaks into the HTTP
//! layer** — this module composes URLs/headers and parses JSON; T9a's
//! [`AuthorizedClient`] does the I/O, and all date/money mechanics live in
//! `costroid-core` (so connect stays a thin HTTP+JSON layer with no `chrono`/`rust_decimal`).
//!
//! Pinned by `docs/proposals/T9-PIN-PROPOSAL.md` §2 (⛔ signed off 2026-06-10):
//!
//! * Host `api.anthropic.com`; auth **`x-api-key: <admin key>` + `anthropic-version:
//!   2023-06-01`** (never `Authorization: Bearer`); key class **`sk-ant-admin…`**.
//! * `GET /v1/organizations/cost_report` — billed USD/day; money is a **decimal-string
//!   in CENTS** (÷100, never `f64`); always `group_by[]=description` (unlocks the
//!   `model`/`token_type`/`service_tier` fields). Priority-Tier dollars are **absent**
//!   (a typed caveat).
//! * `GET /v1/organizations/usage_report/messages` — tokens by model (`group_by[]=model`);
//!   no cost field.
//! * Pagination: `has_more` + an **opaque** `next_page` token, passed back verbatim.
//!
//! Secrets ride only in [`AuthHeader`] values (redacting `Debug`), **never** in a URL.

use secrecy::{ExposeSecret, SecretString};
use serde::Deserialize;

use costroid_core::vendor_report::{
    utc_date_from_rfc3339, AccessForbiddenHint, AmountConfidence, CostLineItem, CostReportCaveats,
    CostReportOutcome, DateRange, ModelTokenUsage, MoneyParseError, UsageReportCaveats,
    UsageReportOutcome, UsdAmount, VendorCostDay, VendorCostReport, VendorReportUnavailable,
    VendorUsageDay, VendorUsageReport,
};

use crate::fetch::{build_query, fetch_page, PageOutcome, RetryPolicy};
use crate::{AuthHeader, AuthorizedClient, ConnectError, CredentialStore};

/// The pinned host. Tests pin to the API *paths* + version header, not doc URLs.
const HOST: &str = "api.anthropic.com";
/// The pinned API-version header value (NOT a doc URL — see proposal §2.4).
const API_VERSION: &str = "2023-06-01";
/// The admin-key class these endpoints require; standard `sk-ant-api03…` keys are rejected.
const ADMIN_KEY_PREFIX: &str = "sk-ant-admin";
const COST_REPORT_PATH: &str = "/v1/organizations/cost_report";
const USAGE_REPORT_PATH: &str = "/v1/organizations/usage_report/messages";
/// Buckets per page. The usage report caps `1d` at 31; cost_report's per-page cap is
/// undocumented (a Gate-2 live check) — 31 is a safe daily-month request that paginates.
const PAGE_LIMIT: &str = "31";
/// A hard ceiling on pages, so a misbehaving `has_more=true` server cannot loop forever.
const MAX_PAGES: u32 = 1024;

/// The Anthropic Usage & Cost adapter, bound to `api.anthropic.com`.
pub struct AnthropicAdapter {
    client: AuthorizedClient,
    retry: RetryPolicy,
}

impl AnthropicAdapter {
    /// Build the adapter over the pinned host with default [`RequestLimits`] and the
    /// default retry policy. Fails only if the OS-native TLS roots cannot be loaded.
    ///
    /// [`RequestLimits`]: crate::RequestLimits
    pub fn new() -> Result<Self, ConnectError> {
        Ok(Self {
            client: AuthorizedClient::with_limits(HOST, crate::RequestLimits::default())?,
            retry: RetryPolicy::default(),
        })
    }

    /// Test seam: build over an injected (loopback) client + retry policy.
    #[cfg(test)]
    pub(crate) fn with_client(client: AuthorizedClient, retry: RetryPolicy) -> Self {
        Self { client, retry }
    }

    /// Fetch the billed-cost report for `range`, reading the admin key from `store`.
    /// Returns [`VendorReportUnavailable::NotConnected`] (as data) when no key is stored.
    /// This is the full key flow: retrieve from the keychain → compose `AuthHeader` →
    /// use → never log/echo/serialize the secret.
    pub fn cost_report(
        &self,
        store: &CredentialStore,
        range: DateRange,
    ) -> Result<CostReportOutcome, ConnectError> {
        match store.retrieve(crate::ApiVendor::Anthropic)? {
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
        match store.retrieve(crate::ApiVendor::Anthropic)? {
            Some(key) => self.fetch_usage_report(&key, range),
            None => Ok(UsageReportOutcome::Unavailable(
                VendorReportUnavailable::NotConnected,
            )),
        }
    }

    /// Fetch the cost report with a caller-supplied key (the testable seam; `cost_report`
    /// wraps it with the keychain retrieve). The key is wrong-class-checked from its
    /// prefix before any request is sent.
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
            let query = cost_query(range, page.as_deref());
            let path = format!("{COST_REPORT_PATH}{query}");
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
                    let parsed: CostResponse = parse_json(&bytes, "cost_report")?;
                    for bucket in parsed.data {
                        days.push(cost_bucket_to_day(bucket)?);
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
                // Priority-Tier dollars are absent from cost_report — always footnote it.
                priority_tier_absent: true,
                per_model_derived_best_effort: false,
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
            let path = format!("{USAGE_REPORT_PATH}{query}");
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
                    let parsed: UsageResponse = parse_json(&bytes, "usage_report")?;
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
            // Anthropic's usage report covers all message traffic — the Responses-API
            // coverage gap is OpenAI-only.
            caveats: UsageReportCaveats::default(),
        }))
    }

    /// Compose the two pinned auth headers. The version pin is non-secret but is wrapped
    /// in `SecretString` too — `AuthHeader` uses one type with uniform redaction.
    fn headers(&self, key: &SecretString) -> [AuthHeader; 2] {
        [
            AuthHeader::new("x-api-key", key.clone()),
            AuthHeader::new(
                "anthropic-version",
                SecretString::from(API_VERSION.to_string()),
            ),
        ]
    }
}

/// Reject a non-admin key from its prefix before any request is sent (the connect-time
/// copy itself is T10's; this is the fail-fast guard). Only the prefix is inspected; the
/// secret is never logged or stored.
fn wrong_key_class(key: &SecretString) -> Option<VendorReportUnavailable> {
    if key.expose_secret().starts_with(ADMIN_KEY_PREFIX) {
        None
    } else {
        Some(VendorReportUnavailable::WrongKeyClass {
            expected_prefix: ADMIN_KEY_PREFIX.to_string(),
        })
    }
}

/// Map a 403 body to a finer hint. Matches the **documented** individual-account phrase
/// (proposal §2.3); finer AWS-org detection awaits the Gate-2 live body shapes.
fn classify_forbidden(body: Option<&str>) -> AccessForbiddenHint {
    match body {
        Some(text) => {
            let lower = text.to_ascii_lowercase();
            if lower.contains("individual account") {
                AccessForbiddenHint::IndividualAccount
            } else if lower.contains("aws") || lower.contains("bedrock") {
                AccessForbiddenHint::AwsOrg
            } else {
                AccessForbiddenHint::Unknown
            }
        }
        None => AccessForbiddenHint::Unknown,
    }
}

fn cost_query(range: DateRange, page: Option<&str>) -> String {
    let mut params: Vec<(&str, String)> = vec![
        ("starting_at", range.start_rfc3339()),
        ("ending_at", range.end_rfc3339()),
        ("bucket_width", "1d".to_string()),
        // group_by[]=description unlocks the per-result model/token_type/service_tier.
        ("group_by[]", "description".to_string()),
        ("limit", PAGE_LIMIT.to_string()),
    ];
    if let Some(token) = page {
        params.push(("page", token.to_string()));
    }
    build_query(&params)
}

fn usage_query(range: DateRange, page: Option<&str>) -> String {
    let mut params: Vec<(&str, String)> = vec![
        ("starting_at", range.start_rfc3339()),
        ("ending_at", range.end_rfc3339()),
        ("bucket_width", "1d".to_string()),
        ("group_by[]", "model".to_string()),
        ("limit", PAGE_LIMIT.to_string()),
    ];
    if let Some(token) = page {
        params.push(("page", token.to_string()));
    }
    build_query(&params)
}

/// Advance pagination: returns the next opaque token, or `None` when done. Errors only
/// if the page ceiling is hit (a runaway `has_more=true`).
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
            detail: "anthropic pagination exceeded the page ceiling".to_string(),
        });
    }
    // `has_more` with no token is treated as the end (defensive; never loop on null).
    Ok(token)
}

fn parse_json<'a, T: Deserialize<'a>>(bytes: &'a [u8], what: &str) -> Result<T, ConnectError> {
    serde_json::from_slice(bytes).map_err(|err| ConnectError::ResponseFormat {
        detail: format!("anthropic {what}: {err}"),
    })
}

fn money_err(err: MoneyParseError) -> ConnectError {
    ConnectError::ResponseFormat {
        detail: format!("anthropic money: {err}"),
    }
}

fn cost_bucket_to_day(bucket: CostBucket) -> Result<VendorCostDay, ConnectError> {
    let date =
        utc_date_from_rfc3339(&bucket.starting_at).ok_or_else(|| ConnectError::ResponseFormat {
            detail: format!("anthropic bucket timestamp {:?}", bucket.starting_at),
        })?;
    let mut line_items = Vec::with_capacity(bucket.results.len());
    for result in bucket.results {
        let amount = UsdAmount::from_decimal_cents_str(&result.amount).map_err(money_err)?;
        line_items.push(CostLineItem {
            label: result.description.unwrap_or_default(),
            amount,
            model: result.model,
            cost_type: result.cost_type,
            service_tier: result.service_tier,
            // Anthropic attributes the amount to the model itself — exact.
            confidence: AmountConfidence::Exact,
        });
    }
    VendorCostDay::from_line_items(date, line_items).map_err(money_err)
}

fn usage_bucket_to_day(bucket: UsageBucket) -> Result<VendorUsageDay, ConnectError> {
    let date =
        utc_date_from_rfc3339(&bucket.starting_at).ok_or_else(|| ConnectError::ResponseFormat {
            detail: format!("anthropic bucket timestamp {:?}", bucket.starting_at),
        })?;
    let usages = bucket
        .results
        .into_iter()
        .map(|result| {
            let cache_creation = result
                .cache_creation
                .map(|cache| {
                    cache
                        .ephemeral_5m_input_tokens
                        .saturating_add(cache.ephemeral_1h_input_tokens)
                })
                .unwrap_or(0);
            ModelTokenUsage {
                model: result.model.unwrap_or_default(),
                input_tokens: result.uncached_input_tokens,
                output_tokens: result.output_tokens,
                cache_read_tokens: result.cache_read_input_tokens,
                cache_creation_tokens: cache_creation,
                num_requests: None,
            }
        })
        .collect();
    Ok(VendorUsageDay::from_model_usages(date, usages))
}

// ---------------------------------------------------------------------------
// Documented response shapes (deserialized from the SCHEMA, not the broken doc
// examples — proposal §2.4). Unknown fields are ignored; missing optional fields
// default, so the parser tolerates the docs' mid-migration drift.
// ---------------------------------------------------------------------------

#[derive(Deserialize)]
struct CostResponse {
    #[serde(default)]
    data: Vec<CostBucket>,
    #[serde(default)]
    has_more: bool,
    #[serde(default)]
    next_page: Option<String>,
}

#[derive(Deserialize)]
struct CostBucket {
    starting_at: String,
    #[serde(default)]
    results: Vec<CostResult>,
}

#[derive(Deserialize)]
struct CostResult {
    amount: String,
    #[serde(default)]
    description: Option<String>,
    #[serde(default)]
    cost_type: Option<String>,
    #[serde(default)]
    model: Option<String>,
    #[serde(default)]
    service_tier: Option<String>,
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
    starting_at: String,
    #[serde(default)]
    results: Vec<UsageResult>,
}

#[derive(Deserialize)]
struct UsageResult {
    #[serde(default)]
    uncached_input_tokens: u64,
    #[serde(default)]
    cache_read_input_tokens: u64,
    #[serde(default)]
    cache_creation: Option<CacheCreation>,
    #[serde(default)]
    output_tokens: u64,
    #[serde(default)]
    model: Option<String>,
}

#[derive(Deserialize)]
struct CacheCreation {
    #[serde(default)]
    ephemeral_5m_input_tokens: u64,
    #[serde(default)]
    ephemeral_1h_input_tokens: u64,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::{err, ok, ok_json, reply, serve_sequence, MockServer};

    fn range() -> DateRange {
        // 2026-06-01T00:00:00Z .. 2026-06-03T00:00:00Z
        ok(DateRange::from_unix_seconds(1_780_272_000, 1_780_444_800).ok_or("range"))
    }

    fn admin_key() -> SecretString {
        SecretString::from("sk-ant-admin-fixture-0001".to_string())
    }

    /// Expected dollars from a CENTS string (so tests never name `Decimal` directly).
    fn dollars_from_cents(cents: &str) -> UsdAmount {
        ok(UsdAmount::from_decimal_cents_str(cents))
    }

    const COST_PAGE_1: &str = r#"{
        "data": [
            {"starting_at":"2026-06-01T00:00:00Z","ending_at":"2026-06-02T00:00:00Z","results":[
                {"amount":"123.78912","currency":"USD","description":"Claude Sonnet 4 Usage","cost_type":"tokens","model":"claude-sonnet-4-6","token_type":"uncached_input_tokens","service_tier":"standard"},
                {"amount":"50","currency":"USD","description":"Web search","cost_type":"web_search"}
            ]}
        ],
        "has_more": true,
        "next_page": "page-token-002"
    }"#;

    const COST_PAGE_2: &str = r#"{
        "data": [
            {"starting_at":"2026-06-02T00:00:00Z","ending_at":"2026-06-03T00:00:00Z","results":[
                {"amount":"200","currency":"USD","description":"Claude Sonnet 4 Usage","cost_type":"tokens","model":"claude-sonnet-4-6","service_tier":"standard"}
            ]}
        ],
        "has_more": false,
        "next_page": null
    }"#;

    fn cost_report_for(server_replies: Vec<Vec<u8>>) -> (CostReportOutcome, MockServer) {
        let server = serve_sequence(server_replies);
        let adapter = AnthropicAdapter::with_client(server.client(), RetryPolicy::test());
        let outcome = ok(adapter.fetch_cost_report(&admin_key(), range()));
        (outcome, server)
    }

    #[test]
    fn cost_report_paginates_and_parses_fractional_cents() {
        let (outcome, server) = cost_report_for(vec![ok_json(COST_PAGE_1), ok_json(COST_PAGE_2)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert!(
            report.caveats.priority_tier_absent,
            "priority-tier caveat must ride along"
        );
        assert!(!report.caveats.per_model_derived_best_effort);
        assert_eq!(report.days.len(), 2);

        // Day 1: 123.78912 cents = $1.2378912, plus 50 cents = $0.50 -> total $1.7378912.
        let day1 = &report.days[0];
        assert_eq!(day1.total, dollars_from_cents("173.78912"));
        // The per-model rollup is the Sonnet row only (the web_search row has no model).
        assert_eq!(day1.by_model.len(), 1);
        assert_eq!(day1.by_model[0].model, "claude-sonnet-4-6");
        assert_eq!(day1.by_model[0].confidence, AmountConfidence::Exact);
        assert_eq!(day1.by_model[0].amount, dollars_from_cents("123.78912"));

        // Verify the wire: two requests, both carrying the pinned auth headers, the
        // opaque token passed back verbatim on page 2, and NO secret in any URL line.
        let req1 = ok(server.next_request());
        let req2 = ok(server.next_request());
        let line1 = first_line(&req1);
        let line2 = first_line(&req2);
        assert!(line1.starts_with("GET /v1/organizations/cost_report?"));
        assert!(req1
            .to_ascii_lowercase()
            .contains("x-api-key: sk-ant-admin-fixture-0001"));
        assert!(req1
            .to_ascii_lowercase()
            .contains("anthropic-version: 2023-06-01"));
        assert!(req1
            .to_ascii_lowercase()
            .contains("group_by%5b%5d=description"));
        assert!(
            line2.contains("page=page-token-002"),
            "opaque token must be passed back: {line2}"
        );
        // No secret ever appears in the request LINE (path/query).
        assert!(!line1.contains("sk-ant-admin"), "no secret in URL: {line1}");
        assert!(!line2.contains("sk-ant-admin"), "no secret in URL: {line2}");
    }

    #[test]
    fn usage_report_sums_cache_creation_and_maps_meters() {
        const USAGE: &str = r#"{
            "data":[{"starting_at":"2026-06-01T00:00:00Z","ending_at":"2026-06-02T00:00:00Z","results":[
                {"uncached_input_tokens":1000,"cache_read_input_tokens":200,
                 "cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":12},
                 "output_tokens":500,"model":"claude-opus-4-8"}
            ]}],
            "has_more":false,"next_page":null
        }"#;
        let server = serve_sequence(vec![ok_json(USAGE)]);
        let adapter = AnthropicAdapter::with_client(server.client(), RetryPolicy::test());
        let outcome = ok(adapter.fetch_usage_report(&admin_key(), range()));
        let report = match outcome {
            UsageReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert!(!report.caveats.responses_api_coverage_unconfirmed);
        let model = &report.days[0].by_model[0];
        assert_eq!(model.model, "claude-opus-4-8");
        assert_eq!(model.input_tokens, 1000);
        assert_eq!(model.cache_read_tokens, 200);
        assert_eq!(model.cache_creation_tokens, 42); // 30 + 12
        assert_eq!(model.output_tokens, 500);
        assert_eq!(model.num_requests, None);

        let req = ok(server.next_request());
        assert!(req.to_ascii_lowercase().contains("group_by%5b%5d=model"));
    }

    #[test]
    fn standard_key_is_rejected_before_any_request() {
        // A standard (non-admin) key never reaches the network — wrong-class as data.
        let server = serve_sequence(vec![]); // no reply needed; no request must be made
        let adapter = AnthropicAdapter::with_client(server.client(), RetryPolicy::test());
        let key = SecretString::from("sk-ant-api03-not-an-admin-key".to_string());
        let outcome = ok(adapter.fetch_cost_report(&key, range()));
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::WrongKeyClass { .. })
        ));
    }

    #[test]
    fn individual_account_403_classifies_as_individual_account() {
        let body = r#"{"type":"error","error":{"type":"permission_error","message":"The Admin API is unavailable for individual accounts."}}"#;
        let (outcome, _server) = cost_report_for(vec![reply("403 Forbidden", &[], body)]);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::AccessForbidden {
                hint: AccessForbiddenHint::IndividualAccount
            })
        ));
    }

    #[test]
    fn unauthorized_401_classifies_distinctly_from_403() {
        let (outcome, _server) = cost_report_for(vec![reply("401 Unauthorized", &[], "{}")]);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::AuthenticationFailed)
        ));
    }

    #[test]
    fn rate_limit_degrades_to_unavailable_after_bounded_retries() {
        // RetryPolicy::test() = 2 retries -> 3 requests total, all 429 -> degrade.
        let replies = vec![
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
            reply("429 Too Many Requests", &["Retry-After: 1"], ""),
        ];
        let (outcome, _server) = cost_report_for(replies);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::RateLimited)
        ));
    }

    #[test]
    fn server_error_degrades_to_unavailable() {
        let replies = vec![
            reply("503 Service Unavailable", &[], ""),
            reply("503 Service Unavailable", &[], ""),
            reply("503 Service Unavailable", &[], ""),
        ];
        let (outcome, _server) = cost_report_for(replies);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::ServerUnavailable {
                status: 503
            })
        ));
    }

    #[test]
    fn has_more_with_null_token_terminates_without_looping() {
        // A misbehaving page that claims has_more=true but carries no next_page must
        // terminate (not hang/loop): the server provides exactly ONE reply, so a second
        // request would block/refuse and fail the test.
        const PAGE: &str = r#"{
            "data":[{"starting_at":"2026-06-01T00:00:00Z","ending_at":"2026-06-02T00:00:00Z","results":[
                {"amount":"100","currency":"USD","description":"x","model":"m"}
            ]}],
            "has_more":true,"next_page":null
        }"#;
        let server = serve_sequence(vec![ok_json(PAGE)]);
        let adapter = AnthropicAdapter::with_client(server.client(), RetryPolicy::test());
        let outcome = ok(adapter.fetch_cost_report(&admin_key(), range()));
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert_eq!(report.days.len(), 1, "must stop after the single page");
    }

    #[test]
    fn malformed_json_is_a_typed_response_format_error() {
        let server = serve_sequence(vec![ok_json("{ not json")]);
        let adapter = AnthropicAdapter::with_client(server.client(), RetryPolicy::test());
        let error = err(adapter.fetch_cost_report(&admin_key(), range()));
        assert!(matches!(error, ConnectError::ResponseFormat { .. }));
    }

    fn first_line(request: &str) -> String {
        request.lines().next().unwrap_or_default().to_string()
    }

    #[test]
    fn parses_the_live_empty_results_envelope() {
        // Verbatim api.anthropic.com 2xx body (Gate-2 live confirm, 2026-06-13; the probe
        // window had no usage, so every `results` is empty -> each day totals $0). Confirms
        // the envelope/bucket/pagination shape against real data; the usage_report envelope
        // is structurally identical.
        const LIVE_COST: &str = r#"{"data":[{"starting_at":"2026-06-06T00:00:00Z","ending_at":"2026-06-07T00:00:00Z","results":[]},{"starting_at":"2026-06-07T00:00:00Z","ending_at":"2026-06-08T00:00:00Z","results":[]}],"has_more":false,"next_page":null}"#;
        let (outcome, _server) = cost_report_for(vec![ok_json(LIVE_COST)]);
        let report = match outcome {
            CostReportOutcome::Available(report) => report,
            other => panic!("expected Available, got {other:?}"),
        };
        assert_eq!(report.days.len(), 2);
        assert!(report.days.iter().all(|day| day.total == UsdAmount::ZERO
            && day.by_model.is_empty()
            && day.line_items.is_empty()));
        assert!(report.caveats.priority_tier_absent);
    }

    #[test]
    fn current_day_window_400_degrades_to_request_rejected() {
        // Verbatim api.anthropic.com 400 (Gate-2 live, 2026-06-13): cost_report rejects a
        // current-day/future window ("ending date must be after starting date" after its
        // internal day-snapping — it serves completed days only). The adapter degrades a
        // 400 to a typed RequestRejected, never a crash. (Caller contract for T9c/T10:
        // request completed-day ranges.)
        const LIVE_400: &str = r#"{"type":"error","error":{"type":"invalid_request_error","message":"Invalid date range: ending date must be after starting date"},"request_id":"req_x"}"#;
        let (outcome, _server) = cost_report_for(vec![reply("400 Bad Request", &[], LIVE_400)]);
        assert!(matches!(
            outcome,
            CostReportOutcome::Unavailable(VendorReportUnavailable::RequestRejected {
                status: 400
            })
        ));
    }
}
