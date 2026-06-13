//! Shared HTTP-fetch plumbing for the T9b usage-API adapters: bounded retry/backoff
//! policy, dependency-free query-string encoding, and the single-page fetch loop that
//! turns [`AuthorizedClient::get`]'s typed errors into either a page body or a
//! first-class [`VendorReportUnavailable`].
//!
//! T9a's client *classifies* HTTP failures; T9b *decides* the backoff policy here
//! (proposal §6): honor `Retry-After`, bound the retries, and **degrade 429/5xx/404 to
//! a typed unavailable outcome — never a hard failure** (OpenAI's `/costs` endpoint has
//! had a real ~1-day 404 outage). Everything stays loopback-testable: the delay is a
//! pure function and tests use a zero-delay policy, so no test ever sleeps or touches
//! the real network.

use std::time::Duration;

use costroid_core::vendor_report::{AccessForbiddenHint, VendorReportUnavailable};

use crate::{AuthHeader, AuthorizedClient, ConnectError};

/// Bounded retry/backoff policy for a single page fetch.
#[derive(Debug, Clone, Copy)]
pub(crate) struct RetryPolicy {
    /// How many times to retry a retryable status (429/5xx/404) before degrading.
    pub(crate) max_retries: u32,
    /// Base backoff delay (doubles per attempt, unless `Retry-After` overrides it).
    pub(crate) base_delay: Duration,
    /// Upper bound on any single backoff delay (also caps a large `Retry-After`).
    pub(crate) max_delay: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        // Anthropic asks for ≤1 req/min sustained (bursts for paginated downloads are
        // fine); these bounds keep a transient blip recoverable without hammering.
        RetryPolicy {
            max_retries: 3,
            base_delay: Duration::from_secs(2),
            max_delay: Duration::from_secs(30),
        }
    }
}

impl RetryPolicy {
    /// A zero-delay policy for tests: still exercises the retry-then-degrade path
    /// (set `max_retries`) but never actually sleeps.
    #[cfg(test)]
    pub(crate) fn test() -> Self {
        RetryPolicy {
            max_retries: 2,
            base_delay: Duration::ZERO,
            max_delay: Duration::ZERO,
        }
    }
}

/// The delay before retry `attempt` (0-based): `Retry-After` if the server sent it,
/// otherwise exponential `base_delay · 2^attempt` — both capped at `max_delay`. Pure,
/// so it is unit-tested directly and a zero `max_delay` makes every delay zero.
pub(crate) fn backoff_delay(
    attempt: u32,
    retry_after_seconds: Option<u64>,
    policy: &RetryPolicy,
) -> Duration {
    let candidate = match retry_after_seconds {
        Some(seconds) => Duration::from_secs(seconds),
        None => policy
            .base_delay
            .saturating_mul(2u32.saturating_pow(attempt)),
    };
    candidate.min(policy.max_delay)
}

/// The result of fetching one page.
pub(crate) enum PageOutcome {
    /// The 2xx response body.
    Body(Vec<u8>),
    /// A first-class unavailable reason (429/5xx/404 backoff-exhausted, 401, 403, or an
    /// unexpected 4xx) — never an error loop.
    Unavailable(VendorReportUnavailable),
}

/// Fetch one page with bounded backoff, mapping T9a's typed errors to a [`PageOutcome`].
/// `classify_forbidden` turns a 403 body into a vendor-specific [`AccessForbiddenHint`].
/// `Err` is reserved for genuinely-unexpected failures (redirect, timeout, transport,
/// trust-roots) the caller should surface as-is — the documented unavailable states all
/// come back as `Ok(PageOutcome::Unavailable(..))`.
pub(crate) fn fetch_page(
    client: &AuthorizedClient,
    path_and_query: &str,
    headers: &[AuthHeader],
    policy: &RetryPolicy,
    classify_forbidden: impl Fn(Option<&str>) -> AccessForbiddenHint,
) -> Result<PageOutcome, ConnectError> {
    let url = client.url_for(path_and_query);
    let mut attempt: u32 = 0;
    loop {
        match client.get(&url, headers) {
            Ok(response) => return Ok(PageOutcome::Body(response.into_body())),
            Err(ConnectError::RateLimited {
                retry_after_seconds,
            }) => {
                if attempt >= policy.max_retries {
                    return Ok(PageOutcome::Unavailable(
                        VendorReportUnavailable::RateLimited,
                    ));
                }
                sleep_if_nonzero(backoff_delay(attempt, retry_after_seconds, policy));
                attempt = attempt.saturating_add(1);
            }
            Err(ConnectError::ServerError { status }) => {
                if attempt >= policy.max_retries {
                    return Ok(PageOutcome::Unavailable(
                        VendorReportUnavailable::ServerUnavailable { status },
                    ));
                }
                sleep_if_nonzero(backoff_delay(attempt, None, policy));
                attempt = attempt.saturating_add(1);
            }
            // 404 is grouped with the server-side degrade class: OpenAI's `/costs` had a
            // real transient 404 outage, so retry-then-degrade rather than hard-fail.
            Err(ConnectError::ClientError { status: 404, .. }) => {
                if attempt >= policy.max_retries {
                    return Ok(PageOutcome::Unavailable(
                        VendorReportUnavailable::ServerUnavailable { status: 404 },
                    ));
                }
                sleep_if_nonzero(backoff_delay(attempt, None, policy));
                attempt = attempt.saturating_add(1);
            }
            Err(ConnectError::ClientError { status: 401, .. }) => {
                return Ok(PageOutcome::Unavailable(
                    VendorReportUnavailable::AuthenticationFailed,
                ));
            }
            Err(ConnectError::ClientError {
                status: 403, body, ..
            }) => {
                return Ok(PageOutcome::Unavailable(
                    VendorReportUnavailable::AccessForbidden {
                        hint: classify_forbidden(body.as_deref()),
                    },
                ));
            }
            Err(ConnectError::ClientError { status, .. }) => {
                return Ok(PageOutcome::Unavailable(
                    VendorReportUnavailable::RequestRejected { status },
                ));
            }
            // Redirect / Timeout / Transport / NativeRoots / etc. are genuine errors.
            Err(other) => return Err(other),
        }
    }
}

fn sleep_if_nonzero(delay: Duration) {
    if !delay.is_zero() {
        std::thread::sleep(delay);
    }
}

/// Percent-encode one query component per RFC 3986 (unreserved = `A-Z a-z 0-9 - . _ ~`;
/// everything else `%XX`). Dependency-free (no transitive crate promoted to a direct
/// dep). Used for both keys and values, so RFC 3339 timestamps (`:` `+`), array-bracket
/// keys (`group_by[]` → `group_by%5B%5D`), and **opaque pagination tokens** are all
/// transported safely — the opaque token is passed back *verbatim* (the server
/// percent-decodes it), never parsed or altered.
pub(crate) fn encode_query_component(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    for byte in input.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(byte as char);
            }
            other => {
                out.push('%');
                out.push(hex_upper(other >> 4));
                out.push(hex_upper(other & 0x0f));
            }
        }
    }
    out
}

fn hex_upper(nibble: u8) -> char {
    match nibble {
        0..=9 => (b'0' + nibble) as char,
        _ => (b'A' + (nibble - 10)) as char,
    }
}

/// Build a `?k=v&k=v…` query string from already-ordered params (both key and value
/// percent-encoded). Returns `""` for an empty param list. Keys may repeat (e.g. an
/// array param). Credentials are **never** passed here — they ride in [`AuthHeader`]s.
pub(crate) fn build_query<S: AsRef<str>>(params: &[(&str, S)]) -> String {
    if params.is_empty() {
        return String::new();
    }
    let mut query = String::from("?");
    for (index, (key, value)) in params.iter().enumerate() {
        if index > 0 {
            query.push('&');
        }
        query.push_str(&encode_query_component(key));
        query.push('=');
        query.push_str(&encode_query_component(value.as_ref()));
    }
    query
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_honors_retry_after_capped_at_max() {
        let policy = RetryPolicy::default();
        // Retry-After wins when present, capped at max_delay (30s).
        assert_eq!(backoff_delay(0, Some(7), &policy), Duration::from_secs(7));
        assert_eq!(
            backoff_delay(0, Some(999), &policy),
            Duration::from_secs(30)
        );
    }

    #[test]
    fn backoff_grows_exponentially_then_caps() {
        let policy = RetryPolicy::default();
        assert_eq!(backoff_delay(0, None, &policy), Duration::from_secs(2));
        assert_eq!(backoff_delay(1, None, &policy), Duration::from_secs(4));
        assert_eq!(backoff_delay(2, None, &policy), Duration::from_secs(8));
        // 2 * 2^10 = 2048s, capped at 30s.
        assert_eq!(backoff_delay(10, None, &policy), Duration::from_secs(30));
    }

    #[test]
    fn test_policy_never_sleeps() {
        let policy = RetryPolicy::test();
        assert_eq!(backoff_delay(0, Some(99), &policy), Duration::ZERO);
        assert_eq!(backoff_delay(3, None, &policy), Duration::ZERO);
    }

    #[test]
    fn query_encoding_is_rfc3986_and_keeps_unreserved() {
        assert_eq!(encode_query_component("description"), "description");
        assert_eq!(
            encode_query_component("2026-06-01T00:00:00Z"),
            "2026-06-01T00%3A00%3A00Z"
        );
        assert_eq!(encode_query_component("group_by[]"), "group_by%5B%5D");
        // An opaque token with reserved bytes is encoded (server decodes to verbatim).
        assert_eq!(encode_query_component("a b/c+d"), "a%20b%2Fc%2Bd");
    }

    #[test]
    fn build_query_joins_and_encodes() {
        let query = build_query(&[("group_by[]", "model"), ("limit", "31")]);
        assert_eq!(query, "?group_by%5B%5D=model&limit=31");
        assert_eq!(build_query::<&str>(&[]), "");
    }
}
