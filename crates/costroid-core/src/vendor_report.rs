//! Provider-neutral **vendor usage/cost report** shapes — the "invoice side" of the
//! ledger, parsed from a vendor's own usage/billing API.
//!
//! These types live in `costroid-core` (not `costroid-connect`) on purpose: the
//! `costroid-connect` adapters (T9b) fetch vendor JSON and parse it *into* these
//! types, while the reconciliation engine (T9c) consumes them — so reconciliation
//! stays pure-core and fixture-tested with **no `costroid-connect` dependency**. The
//! dependency direction is `connect → core`, **never** `core → connect`.
//!
//! ## The money-encoding trap (read before touching [`UsdAmount`])
//!
//! Several vendor endpoints feed one reconciliation pipe with **different money
//! encodings**: Anthropic `cost_report` emits a **decimal string in CENTS**
//! (fractional possible — `"123.78912"` = `$1.2378912`); OpenAI `costs` emits a
//! **float-dollars** JSON number. Mixing them silently is a **100× error**. The guard
//! is structural: [`UsdAmount`] is the one canonical money type (always **US dollars**,
//! always exact [`Decimal`] — never `f64`), and it can be built **only** through a
//! unit-tagged parse boundary ([`UsdAmount::from_decimal_cents_str`] /
//! [`UsdAmount::from_json_dollars_str`]). Pick the constructor that matches the
//! encoding at the parse site; once a value is a `UsdAmount` it is unambiguously
//! dollars and cannot be re-interpreted.
//!
//! ## Honesty caveats ride as typed data
//!
//! Two known truthfulness limits are carried **in the types** (not as documentation),
//! so T9c/T10 cannot silently drop them: [`CostReportCaveats::priority_tier_absent`]
//! (Anthropic Priority-Tier dollars are absent from `cost_report`, so totals
//! understate the bill for priority users) and
//! [`CostReportCaveats::per_model_derived_best_effort`] (OpenAI per-model dollars are
//! derived from the undocumented `line_item` string). Per-model dollar figures
//! additionally carry an [`AmountConfidence`].

use chrono::{DateTime, NaiveDate, SecondsFormat, Utc};
use rust_decimal::Decimal;
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// The exact, pinned render string for a vendor with **no sanctioned static-key usage
/// API** (Gemini). Pinned by `docs/proposals/T9-PIN-PROPOSAL.md` §4 — do not reword.
/// (Carries an em dash; a `--plain` renderer must ASCII-fold it at the render boundary,
/// the same way the Cursor detect-only note is folded — that is T10's job, not this
/// data layer's.)
pub const GEMINI_UNAVAILABLE_MESSAGE: &str = "unavailable — no sanctioned static-key usage API";

// ---------------------------------------------------------------------------
// Money — one canonical USD type, built only at a unit-tagged parse boundary
// ---------------------------------------------------------------------------

/// A money amount whose canonical unit is **US dollars**, stored exactly as a
/// [`Decimal`] (never `f64`).
///
/// Construct it **only** through a unit-tagged parse boundary so the vendor money
/// encodings can never mix silently (see the module docs):
/// [`from_decimal_cents_str`](Self::from_decimal_cents_str) for Anthropic's
/// decimal-cents strings, [`from_json_dollars_str`](Self::from_json_dollars_str) for
/// OpenAI's float-dollars JSON literal. [`from_usd`](Self::from_usd) is for values that
/// are *already* canonical dollars (e.g. a per-model rollup summed from other
/// `UsdAmount`s, or a test).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
pub struct UsdAmount(Decimal);

impl UsdAmount {
    /// Zero dollars.
    pub const ZERO: UsdAmount = UsdAmount(Decimal::ZERO);

    /// Wrap a value that is **already** canonical US dollars.
    pub fn from_usd(dollars: Decimal) -> Self {
        UsdAmount(dollars)
    }

    /// The amount in US dollars.
    pub fn as_usd(self) -> Decimal {
        self.0
    }

    /// Parse Anthropic's `cost_report` money encoding: a decimal string in **CENTS**
    /// (fractional cents possible), e.g. `"123.78912"` → `$1.2378912`. The conversion
    /// to dollars is an exact decimal-point shift (÷100 with no rounding), never `f64`.
    pub fn from_decimal_cents_str(raw: &str) -> Result<Self, MoneyParseError> {
        let cents = parse_plain_decimal(raw)?;
        // Divide by 100 EXACTLY by shifting the decimal point two places (scale + 2),
        // never a lossy `/`: a cents value with scale s becomes dollars with scale s+2.
        let scaled =
            Decimal::try_from_i128_with_scale(cents.mantissa(), cents.scale().saturating_add(2))
                .map_err(|_| MoneyParseError::OutOfRange {
                    raw: raw.to_string(),
                })?;
        Ok(UsdAmount(scaled))
    }

    /// Parse OpenAI's `costs` money encoding: a JSON number's **literal text** already
    /// in **dollars**, e.g. `"1.23"`. Parsed straight from the text into [`Decimal`]
    /// (the caller must hand the raw JSON number text, e.g. via `serde_json`'s
    /// `RawValue`) — **never** through `f64` arithmetic.
    pub fn from_json_dollars_str(raw: &str) -> Result<Self, MoneyParseError> {
        Ok(UsdAmount(parse_json_number_decimal(raw)?))
    }

    /// Exact addition; `None` on overflow (the caller turns that into a typed error).
    pub fn checked_add(self, other: Self) -> Option<Self> {
        self.0.checked_add(other.0).map(UsdAmount)
    }
}

/// Parse a plain decimal string (no scientific notation) exactly.
fn parse_plain_decimal(raw: &str) -> Result<Decimal, MoneyParseError> {
    Decimal::from_str_exact(raw.trim()).map_err(|err| MoneyParseError::Invalid {
        raw: raw.to_string(),
        reason: err.to_string(),
    })
}

/// Parse a JSON number's literal text exactly, tolerating scientific notation
/// (`1.5e-3`) which a vendor *could* emit; both forms map to an exact [`Decimal`].
fn parse_json_number_decimal(raw: &str) -> Result<Decimal, MoneyParseError> {
    let trimmed = raw.trim();
    let parsed = if trimmed.contains(['e', 'E']) {
        Decimal::from_scientific(trimmed)
    } else {
        Decimal::from_str_exact(trimmed)
    };
    parsed.map_err(|err| MoneyParseError::Invalid {
        raw: raw.to_string(),
        reason: err.to_string(),
    })
}

/// A money-parse failure. **Carries the offending amount text, never any secret** —
/// money amounts come from a vendor *response* body, never from the API key (which
/// rides only in a request header, never in a response).
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum MoneyParseError {
    /// The amount text was not a valid decimal number.
    #[error("invalid money amount {raw:?}: {reason}")]
    Invalid {
        /// The offending amount text (non-secret).
        raw: String,
        /// Why it failed to parse.
        reason: String,
    },
    /// The amount parsed but cannot be represented after the unit conversion
    /// (e.g. dividing cents by 100 would exceed the decimal's maximum scale).
    #[error("money amount {raw:?} is out of representable range")]
    OutOfRange {
        /// The offending amount text (non-secret).
        raw: String,
    },
}

// ---------------------------------------------------------------------------
// Cost report (billed $ per UTC day) — the invoice side
// ---------------------------------------------------------------------------

/// How much to trust a per-model dollar figure.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AmountConfidence {
    /// The vendor itself attributed this amount to this model (Anthropic
    /// `cost_report` grouped by `description`, which carries a `model` field).
    Exact,
    /// Derived from an **undocumented / uncontracted** field (OpenAI's `line_item`
    /// string) — best-effort, not a vendor contract.
    DerivedBestEffort,
}

impl AmountConfidence {
    /// The weaker of two confidences (`DerivedBestEffort` wins) — used when rolling
    /// several line items up into one per-model figure.
    fn weakest(self, other: AmountConfidence) -> AmountConfidence {
        match (self, other) {
            (AmountConfidence::Exact, AmountConfidence::Exact) => AmountConfidence::Exact,
            _ => AmountConfidence::DerivedBestEffort,
        }
    }
}

/// One raw vendor cost result row, preserved losslessly so T9c keeps full fidelity
/// (cost-type / token-type / tier breakdown, or the raw OpenAI `line_item` string).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct CostLineItem {
    /// The vendor's own label for the row: Anthropic `description`, or OpenAI's raw
    /// `line_item` string (uninterpreted).
    pub label: String,
    /// The dollar amount for this row.
    pub amount: UsdAmount,
    /// The model this row is attributed to, when the vendor provides it
    /// (Anthropic `model`), or a best-effort parse of OpenAI's `line_item`.
    pub model: Option<String>,
    /// Anthropic `cost_type` (`tokens` | `web_search` | `code_execution` |
    /// `session_usage`); `None` for OpenAI.
    pub cost_type: Option<String>,
    /// Anthropic `cost_report` `service_tier` (`standard` | `batch` — narrower than the
    /// usage report's tier enum); `None` for OpenAI.
    pub service_tier: Option<String>,
    /// Confidence in this row's `model` attribution.
    pub confidence: AmountConfidence,
}

/// A per-model dollar rollup for a day.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelCostAmount {
    /// The model id.
    pub model: String,
    /// Summed dollars attributed to this model that day.
    pub amount: UsdAmount,
    /// The weakest confidence among the contributing line items.
    pub confidence: AmountConfidence,
}

/// One UTC day of billed cost.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct VendorCostDay {
    /// The UTC day this bucket covers.
    pub date: NaiveDate,
    /// The day's total billed dollars (the sum of every line item — the invoice
    /// number for the day).
    pub total: UsdAmount,
    /// Per-model rollup (may be empty when the vendor attributes nothing to a model).
    pub by_model: Vec<ModelCostAmount>,
    /// Every raw result row, preserved (lossless for T9c).
    pub line_items: Vec<CostLineItem>,
}

impl VendorCostDay {
    /// Build a day from its raw line items, computing the exact `total` and the
    /// per-model rollup. The per-model rollup includes only rows that carry a `model`;
    /// rows without one still count toward `total`. `Err` only on a money-overflow.
    pub fn from_line_items(
        date: NaiveDate,
        line_items: Vec<CostLineItem>,
    ) -> Result<Self, MoneyParseError> {
        let mut total = UsdAmount::ZERO;
        // Preserve first-seen order of models while accumulating.
        let mut model_order: Vec<String> = Vec::new();
        let mut model_amounts: std::collections::HashMap<String, (UsdAmount, AmountConfidence)> =
            std::collections::HashMap::new();

        for item in &line_items {
            total = total
                .checked_add(item.amount)
                .ok_or_else(|| MoneyParseError::OutOfRange {
                    raw: "<daily total>".to_string(),
                })?;
            if let Some(model) = &item.model {
                match model_amounts.get_mut(model) {
                    Some((amount, confidence)) => {
                        *amount = amount.checked_add(item.amount).ok_or_else(|| {
                            MoneyParseError::OutOfRange {
                                raw: format!("<model total {model}>"),
                            }
                        })?;
                        *confidence = confidence.weakest(item.confidence);
                    }
                    None => {
                        model_order.push(model.clone());
                        model_amounts.insert(model.clone(), (item.amount, item.confidence));
                    }
                }
            }
        }

        let by_model = model_order
            .into_iter()
            .filter_map(|model| {
                model_amounts
                    .get(&model)
                    .map(|(amount, confidence)| ModelCostAmount {
                        model: model.clone(),
                        amount: *amount,
                        confidence: *confidence,
                    })
            })
            .collect();

        Ok(VendorCostDay {
            date,
            total,
            by_model,
            line_items,
        })
    }
}

/// Typed honesty caveats that **must** travel with a cost report (T9c/T10 cannot drop
/// them — the failure mode being guarded is silently presenting an understated or
/// over-confident bill).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct CostReportCaveats {
    /// Anthropic: **Priority-Tier dollars are absent** from `cost_report` (a different
    /// billing model), so the totals **understate the bill for priority-tier users**.
    /// Priority-Tier *usage* is visible in the usage report via `service_tier=priority`.
    pub priority_tier_absent: bool,
    /// OpenAI: any per-model dollar figure is **derived** from the undocumented,
    /// uncontracted `line_item` string — best-effort, not a vendor contract.
    pub per_model_derived_best_effort: bool,
}

/// A vendor billed-cost report over a date range.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct VendorCostReport {
    /// One entry per UTC day bucket, in ascending date order.
    pub days: Vec<VendorCostDay>,
    /// Typed honesty caveats (see [`CostReportCaveats`]).
    pub caveats: CostReportCaveats,
}

// ---------------------------------------------------------------------------
// Usage report (tokens by model per UTC day)
// ---------------------------------------------------------------------------

/// Token counts for one model on one day, normalized to Costroid's four meters so it
/// lines up with the local-estimate side ([`crate`]'s `UsageEvent`). Adapters do the
/// vendor-specific normalization (e.g. OpenAI's `input_tokens` *includes* cached, so
/// the uncached `input_tokens` here is `input_tokens − input_cached_tokens`; Anthropic
/// `cache_creation_tokens` sums the 5-minute and 1-hour ephemeral buckets).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelTokenUsage {
    /// The model id.
    pub model: String,
    /// Uncached input tokens.
    pub input_tokens: u64,
    /// Output tokens.
    pub output_tokens: u64,
    /// Cache-read input tokens.
    pub cache_read_tokens: u64,
    /// Cache-creation (write) tokens (Anthropic 5m + 1h summed; `0` for OpenAI, which
    /// does not report cache creation).
    pub cache_creation_tokens: u64,
    /// Request count for the model that day, when the vendor reports it (OpenAI
    /// `num_model_requests`); `None` for Anthropic (not reported).
    pub num_requests: Option<u64>,
}

impl ModelTokenUsage {
    fn merge(&mut self, other: &ModelTokenUsage) {
        self.input_tokens = self.input_tokens.saturating_add(other.input_tokens);
        self.output_tokens = self.output_tokens.saturating_add(other.output_tokens);
        self.cache_read_tokens = self
            .cache_read_tokens
            .saturating_add(other.cache_read_tokens);
        self.cache_creation_tokens = self
            .cache_creation_tokens
            .saturating_add(other.cache_creation_tokens);
        self.num_requests = match (self.num_requests, other.num_requests) {
            (Some(a), Some(b)) => Some(a.saturating_add(b)),
            (Some(a), None) => Some(a),
            (None, b) => b,
        };
    }
}

/// One UTC day of token usage by model.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct VendorUsageDay {
    /// The UTC day this bucket covers.
    pub date: NaiveDate,
    /// Per-model token totals.
    pub by_model: Vec<ModelTokenUsage>,
}

impl VendorUsageDay {
    /// Build a day from per-model rows, merging any duplicate models defensively
    /// (with `group_by=model` a day should already carry one row per model).
    pub fn from_model_usages(date: NaiveDate, usages: Vec<ModelTokenUsage>) -> Self {
        let mut order: Vec<String> = Vec::new();
        let mut merged: std::collections::HashMap<String, ModelTokenUsage> =
            std::collections::HashMap::new();
        for usage in usages {
            match merged.get_mut(&usage.model) {
                Some(existing) => existing.merge(&usage),
                None => {
                    order.push(usage.model.clone());
                    merged.insert(usage.model.clone(), usage);
                }
            }
        }
        let by_model = order
            .into_iter()
            .filter_map(|model| merged.remove(&model))
            .collect();
        VendorUsageDay { date, by_model }
    }
}

/// Typed honesty caveats for a token-usage report.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct UsageReportCaveats {
    /// OpenAI: `usage/completions` may **not** include Responses-API traffic. Codex
    /// rides the Responses API, and there is no `usage/responses` endpoint, so if this
    /// lane does not cover it, token-side reconciliation **silently undercounts Codex**
    /// while `costs` still bills it. `true` means coverage is **unconfirmed** (the
    /// Gate-2 live check has not yet established it); set `false` only once confirmed.
    pub responses_api_coverage_unconfirmed: bool,
}

/// A vendor token-usage report over a date range.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct VendorUsageReport {
    /// One entry per UTC day bucket, in ascending date order.
    pub days: Vec<VendorUsageDay>,
    /// Typed honesty caveats (see [`UsageReportCaveats`]).
    pub caveats: UsageReportCaveats,
}

// ---------------------------------------------------------------------------
// Request range + first-class "unavailable" outcomes
// ---------------------------------------------------------------------------

/// A requested UTC time range, **`[start, end)`** — `start` inclusive, `end`
/// exclusive. Callers should pass UTC-day-aligned instants; adapters format this for
/// each vendor (Anthropic RFC 3339; OpenAI Unix seconds).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct DateRange {
    /// Inclusive start.
    pub start: DateTime<Utc>,
    /// Exclusive end.
    pub end: DateTime<Utc>,
}

impl DateRange {
    /// Construct a range. (No validation that `start <= end`; an inverted range simply
    /// yields no buckets from the vendor.)
    pub fn new(start: DateTime<Utc>, end: DateTime<Utc>) -> Self {
        DateRange { start, end }
    }

    /// Construct a range from Unix-second instants, or `None` if either is out of the
    /// representable range. (Also the convenient seam for fixture tests.)
    pub fn from_unix_seconds(start: i64, end: i64) -> Option<Self> {
        Some(DateRange {
            start: DateTime::from_timestamp(start, 0)?,
            end: DateTime::from_timestamp(end, 0)?,
        })
    }

    /// `start` as RFC 3339 (`…Z`, second precision) — the form Anthropic's Admin API
    /// expects for `starting_at`.
    pub fn start_rfc3339(&self) -> String {
        self.start.to_rfc3339_opts(SecondsFormat::Secs, true)
    }

    /// `end` as RFC 3339 — Anthropic's `ending_at` (exclusive).
    pub fn end_rfc3339(&self) -> String {
        self.end.to_rfc3339_opts(SecondsFormat::Secs, true)
    }

    /// `start` as Unix seconds — OpenAI's `start_time`.
    pub fn start_unix(&self) -> i64 {
        self.start.timestamp()
    }

    /// `end` as Unix seconds — OpenAI's `end_time` (exclusive).
    pub fn end_unix(&self) -> i64 {
        self.end.timestamp()
    }
}

/// The UTC calendar date of an RFC 3339 instant (Anthropic bucket `starting_at`), or
/// `None` if it cannot be parsed.
pub fn utc_date_from_rfc3339(value: &str) -> Option<NaiveDate> {
    DateTime::parse_from_rfc3339(value)
        .ok()
        .map(|instant| instant.with_timezone(&Utc).date_naive())
}

/// The UTC calendar date of a Unix-seconds instant (OpenAI bucket `start_time`), or
/// `None` if it is out of range.
pub fn utc_date_from_unix_seconds(seconds: i64) -> Option<NaiveDate> {
    DateTime::from_timestamp(seconds, 0).map(|instant| instant.date_naive())
}

/// A finer reason for an HTTP 403 from a usage/cost endpoint. The precise cause
/// usually needs the response body and is confirmed by the Gate-2 live check / T10's
/// connect-time validation; `Unknown` is the honest default when it cannot be
/// determined.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AccessForbiddenHint {
    /// Could not be determined from the available signals.
    Unknown,
    /// Anthropic: the Admin API is unavailable for **individual (non-organization)
    /// accounts** — the user must create/convert to an organization first.
    IndividualAccount,
    /// Anthropic: Claude-Platform-on-AWS organizations do not get these endpoints.
    AwsOrg,
    /// OpenAI: the key's owner is **not an Organization Owner** (cannot read org
    /// usage/costs).
    MemberNotOwner,
}

/// Why a vendor report could not be produced — modeled as **first-class data**, never
/// an error loop. (`Display`/message copy for the connect UX is T10's; these carry the
/// typed reason.)
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum VendorReportUnavailable {
    /// No key is stored for this vendor yet (it has not been connected — T10).
    NotConnected,
    /// The stored key is the wrong **class** for these admin endpoints (detected from
    /// its prefix before any request is sent).
    WrongKeyClass {
        /// The key prefix these endpoints require (e.g. `sk-ant-admin` / `sk-admin-`).
        expected_prefix: String,
    },
    /// Authentication failed (HTTP 401) — the key was rejected.
    AuthenticationFailed,
    /// Access forbidden (HTTP 403) — e.g. an individual account, a non-Owner member, or
    /// an AWS org. See [`AccessForbiddenHint`].
    AccessForbidden {
        /// A best-effort finer cause.
        hint: AccessForbiddenHint,
    },
    /// The endpoint returned HTTP 429 and the bounded backoff was exhausted.
    RateLimited,
    /// The endpoint returned a 5xx (or the transient-404 outage class) and the bounded
    /// backoff was exhausted.
    ServerUnavailable {
        /// The status that was retried to exhaustion.
        status: u16,
    },
    /// The endpoint rejected the request with an unexpected 4xx (other than
    /// 401/403/429).
    RequestRejected {
        /// The 4xx status received.
        status: u16,
    },
    /// No sanctioned static-key usage API exists for this vendor (Gemini).
    NoSanctionedStaticKeyApi,
}

impl VendorReportUnavailable {
    /// A short human-facing reason. For [`NoSanctionedStaticKeyApi`] this is **exactly**
    /// the pinned [`GEMINI_UNAVAILABLE_MESSAGE`]; the connect UX (T10) owns the rest of
    /// the copy.
    ///
    /// [`NoSanctionedStaticKeyApi`]: VendorReportUnavailable::NoSanctionedStaticKeyApi
    pub fn message(&self) -> String {
        match self {
            VendorReportUnavailable::NotConnected => "not connected".to_string(),
            VendorReportUnavailable::WrongKeyClass { expected_prefix } => {
                format!("wrong key class (expected a {expected_prefix}… key)")
            }
            VendorReportUnavailable::AuthenticationFailed => {
                "authentication failed (the key was rejected)".to_string()
            }
            VendorReportUnavailable::AccessForbidden { hint } => match hint {
                AccessForbiddenHint::Unknown => "access forbidden".to_string(),
                AccessForbiddenHint::IndividualAccount => {
                    "the admin usage API is unavailable for individual accounts".to_string()
                }
                AccessForbiddenHint::AwsOrg => {
                    "the admin usage API is unavailable for Claude-on-AWS organizations".to_string()
                }
                AccessForbiddenHint::MemberNotOwner => {
                    "the key's owner is not an organization owner".to_string()
                }
            },
            VendorReportUnavailable::RateLimited => "rate limited".to_string(),
            VendorReportUnavailable::ServerUnavailable { status } => {
                format!("vendor server error (HTTP {status})")
            }
            VendorReportUnavailable::RequestRejected { status } => {
                format!("request rejected (HTTP {status})")
            }
            VendorReportUnavailable::NoSanctionedStaticKeyApi => {
                GEMINI_UNAVAILABLE_MESSAGE.to_string()
            }
        }
    }
}

/// The result of a cost-report fetch: a report, or a typed reason it is unavailable.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum CostReportOutcome {
    /// A fetched, parsed report.
    Available(VendorCostReport),
    /// A first-class unavailable reason (never an error).
    Unavailable(VendorReportUnavailable),
}

/// The result of a usage-report fetch: a report, or a typed reason it is unavailable.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum UsageReportOutcome {
    /// A fetched, parsed report.
    Available(VendorUsageReport),
    /// A first-class unavailable reason (never an error).
    Unavailable(VendorReportUnavailable),
}

#[cfg(test)]
mod tests {
    use super::*;

    #[track_caller]
    fn ok<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }

    fn date(y: i32, m: u32, d: u32) -> NaiveDate {
        match NaiveDate::from_ymd_opt(y, m, d) {
            Some(date) => date,
            None => panic!("invalid test date"),
        }
    }

    // ---- the money 100x trap -------------------------------------------------

    #[test]
    fn anthropic_cents_string_is_divided_by_100_exactly() {
        // The proposal's own example: "123.78912" cents == $1.2378912.
        let amount = ok(UsdAmount::from_decimal_cents_str("123.78912"));
        assert_eq!(amount.as_usd(), ok(Decimal::from_str_exact("1.2378912")));
    }

    #[test]
    fn openai_dollars_string_is_taken_verbatim() {
        let amount = ok(UsdAmount::from_json_dollars_str("1.23"));
        assert_eq!(amount.as_usd(), ok(Decimal::from_str_exact("1.23")));
    }

    #[test]
    fn the_same_numeric_text_differs_by_exactly_100x_across_the_two_encodings() {
        // The whole point of unit-tagging: "150.00" as CENTS is $1.50, as DOLLARS is
        // $150.00 — a 100x gap that the constructor choice (not the value) decides.
        let as_cents = ok(UsdAmount::from_decimal_cents_str("150.00"));
        let as_dollars = ok(UsdAmount::from_json_dollars_str("150.00"));
        assert_eq!(
            as_dollars.as_usd(),
            as_cents.as_usd() * Decimal::ONE_HUNDRED,
            "the two encodings of the same text must differ by exactly 100x"
        );
    }

    #[test]
    fn money_never_uses_f64_and_keeps_full_precision() {
        // 0.1 + 0.2 in cents -> dollars stays exact (f64 would drift).
        let a = ok(UsdAmount::from_decimal_cents_str("0.1"));
        let b = ok(UsdAmount::from_decimal_cents_str("0.2"));
        let sum = match a.checked_add(b) {
            Some(sum) => sum,
            None => panic!("addition overflowed"),
        };
        assert_eq!(sum.as_usd(), ok(Decimal::from_str_exact("0.003")));
    }

    #[test]
    fn garbage_money_is_a_typed_error_not_a_panic() {
        assert!(matches!(
            UsdAmount::from_decimal_cents_str("not-a-number"),
            Err(MoneyParseError::Invalid { .. })
        ));
        assert!(matches!(
            UsdAmount::from_json_dollars_str(""),
            Err(MoneyParseError::Invalid { .. })
        ));
    }

    #[test]
    fn cents_scale_overflow_is_a_typed_out_of_range_not_a_panic() {
        // A cents string with 27 fractional digits parses (scale 27 ≤ MAX_SCALE 28) but
        // ÷100 needs scale 29 > 28 — the exact-shift conversion returns OutOfRange, never
        // a panic or a silent truncation.
        let twenty_seven_dp = "1.234567890123456789012345678";
        assert_eq!(twenty_seven_dp.split('.').nth(1).map(str::len), Some(27));
        assert!(matches!(
            UsdAmount::from_decimal_cents_str(twenty_seven_dp),
            Err(MoneyParseError::OutOfRange { .. })
        ));
    }

    #[test]
    fn money_parse_error_never_carries_a_secret_shaped_value() {
        // (Amounts are never secret, but assert the error echoes only the amount text.)
        let err = match UsdAmount::from_json_dollars_str("oops") {
            Err(err) => err,
            Ok(value) => panic!("expected Err, got {value:?}"),
        };
        let rendered = format!("{err:?} / {err}");
        assert!(rendered.contains("oops"));
    }

    // ---- cost-day rollup -----------------------------------------------------

    #[test]
    fn cost_day_rolls_up_total_and_per_model() {
        let day = ok(VendorCostDay::from_line_items(
            date(2026, 6, 1),
            vec![
                CostLineItem {
                    label: "Claude usage".to_string(),
                    amount: ok(UsdAmount::from_decimal_cents_str("100")), // $1.00
                    model: Some("claude-sonnet-4-6".to_string()),
                    cost_type: Some("tokens".to_string()),
                    service_tier: Some("standard".to_string()),
                    confidence: AmountConfidence::Exact,
                },
                CostLineItem {
                    label: "Claude usage".to_string(),
                    amount: ok(UsdAmount::from_decimal_cents_str("50")), // $0.50
                    model: Some("claude-sonnet-4-6".to_string()),
                    cost_type: Some("tokens".to_string()),
                    service_tier: Some("standard".to_string()),
                    confidence: AmountConfidence::Exact,
                },
                CostLineItem {
                    label: "Web search".to_string(),
                    amount: ok(UsdAmount::from_decimal_cents_str("25")), // $0.25, no model
                    model: None,
                    cost_type: Some("web_search".to_string()),
                    service_tier: None,
                    confidence: AmountConfidence::Exact,
                },
            ],
        ));
        assert_eq!(day.total.as_usd(), ok(Decimal::from_str_exact("1.75")));
        assert_eq!(day.by_model.len(), 1);
        assert_eq!(day.by_model[0].model, "claude-sonnet-4-6");
        assert_eq!(
            day.by_model[0].amount.as_usd(),
            ok(Decimal::from_str_exact("1.50"))
        );
        assert_eq!(day.by_model[0].confidence, AmountConfidence::Exact);
    }

    #[test]
    fn per_model_confidence_degrades_to_best_effort_if_any_contributor_is() {
        let day = ok(VendorCostDay::from_line_items(
            date(2026, 6, 1),
            vec![
                CostLineItem {
                    label: "gpt-5.5, input".to_string(),
                    amount: ok(UsdAmount::from_json_dollars_str("1.00")),
                    model: Some("gpt-5.5".to_string()),
                    cost_type: None,
                    service_tier: None,
                    confidence: AmountConfidence::DerivedBestEffort,
                },
                CostLineItem {
                    label: "gpt-5.5, output".to_string(),
                    amount: ok(UsdAmount::from_json_dollars_str("2.00")),
                    model: Some("gpt-5.5".to_string()),
                    cost_type: None,
                    service_tier: None,
                    confidence: AmountConfidence::Exact,
                },
            ],
        ));
        assert_eq!(day.by_model.len(), 1);
        assert_eq!(
            day.by_model[0].confidence,
            AmountConfidence::DerivedBestEffort
        );
        assert_eq!(
            day.by_model[0].amount.as_usd(),
            ok(Decimal::from_str_exact("3.00"))
        );
    }

    // ---- usage-day merge -----------------------------------------------------

    #[test]
    fn usage_day_merges_duplicate_models() {
        let day = VendorUsageDay::from_model_usages(
            date(2026, 6, 1),
            vec![
                ModelTokenUsage {
                    model: "m".to_string(),
                    input_tokens: 10,
                    output_tokens: 5,
                    cache_read_tokens: 1,
                    cache_creation_tokens: 2,
                    num_requests: Some(3),
                },
                ModelTokenUsage {
                    model: "m".to_string(),
                    input_tokens: 20,
                    output_tokens: 7,
                    cache_read_tokens: 0,
                    cache_creation_tokens: 1,
                    num_requests: Some(4),
                },
            ],
        );
        assert_eq!(day.by_model.len(), 1);
        let m = &day.by_model[0];
        assert_eq!(m.input_tokens, 30);
        assert_eq!(m.output_tokens, 12);
        assert_eq!(m.cache_read_tokens, 1);
        assert_eq!(m.cache_creation_tokens, 3);
        assert_eq!(m.num_requests, Some(7));
    }

    // ---- the pinned Gemini string -------------------------------------------

    #[test]
    fn gemini_unavailable_renders_the_exact_pinned_string() {
        let unavailable = VendorReportUnavailable::NoSanctionedStaticKeyApi;
        assert_eq!(unavailable.message(), GEMINI_UNAVAILABLE_MESSAGE);
        assert_eq!(
            GEMINI_UNAVAILABLE_MESSAGE,
            "unavailable — no sanctioned static-key usage API"
        );
    }
}
