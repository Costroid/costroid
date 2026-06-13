//! The generic **authorized-host** HTTPS client — `costroid-connect`'s network
//! half's foundation (T9a).
//!
//! This is a small, provider-agnostic, **blocking** client on `ureq` + `rustls`
//! that can only ever talk to the **one** host it was constructed over. It has no
//! provider knowledge — no endpoint, no parameter, no response shape (those are the
//! T9b adapters') — and **nothing calls it** until the explicit, user-initiated
//! `connect` action lands in T10.
//!
//! The pinned posture (PRODUCT-PLAN §12.11 — do not weaken):
//!
//! * **HTTPS-only, GET-only.** The only request method exposed is [`AuthorizedClient::get`];
//!   a non-`https://` URL is a typed error.
//! * **Authorized-host enforcement in the type.** A client value is constructed over
//!   one allowlisted host; any request whose URL is not on that host fails with
//!   [`ConnectError::UnauthorizedHost`] **before any I/O** (no DNS, no socket).
//! * **Redirects disabled entirely.** A redirect response is
//!   [`ConnectError::Redirect`] — following one could leave the authorized host.
//! * **Proxies disabled.** `ureq` honors `HTTP(S)_PROXY` env vars by default, which
//!   would route the request to a *different* host; this client opts out, keeping
//!   traffic strictly device ↔ authorized provider endpoint.
//! * **Secrets never leak.** Auth headers carry [`secrecy::SecretString`] values;
//!   [`AuthHeader`]'s `Debug` redacts, and no error/`Debug` text ever embeds a
//!   header value (pinned by tests).
//! * **`User-Agent: costroid/<version>`** on every request (T9 proposal §6 —
//!   Anthropic asks integrations to identify themselves; zero telemetry implication,
//!   it rides the user's own authorized request).
//! * **Bounded.** Connect + overall timeouts and a response-body size cap
//!   ([`RequestLimits`]) — typed errors, never a hang or an unbounded buffer.
//! * **Classify, don't retry.** The client classifies failures
//!   (unauthorized-host / redirect / timeout / 429 / 5xx / other-4xx / transport /
//!   body-too-large); retry/backoff *policy* belongs to the caller (T9b).
//! * **OS-native trust roots** via `rustls-native-certs` — never the compiled-in
//!   `webpki-roots` bundle (license-incompatible with the deny.toml allowlist, and
//!   the OS trust store is the user's own).

use std::fmt;
use std::io::Read;
use std::time::Duration;

use secrecy::{ExposeSecret, SecretString};

use crate::ConnectError;

/// The `User-Agent` value sent on every request.
const USER_AGENT: &str = concat!("costroid/", env!("CARGO_PKG_VERSION"));

/// Default connect timeout (TCP + TLS establishment).
const DEFAULT_CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

/// Default overall timeout for one whole call (request, response, body read).
const DEFAULT_OVERALL_TIMEOUT: Duration = Duration::from_secs(30);

/// Default response-body cap: 8 MiB — far above any pinned usage-API page size,
/// far below an unbounded buffer.
const DEFAULT_MAX_BODY_BYTES: u64 = 8 * 1024 * 1024;

/// Bounds applied to every request a client makes: connect + overall timeouts and
/// the response-body size cap. Exceeding any of them is a typed error
/// ([`ConnectError::Timeout`] / [`ConnectError::BodyTooLarge`]), never a hang.
#[derive(Debug, Clone, Copy)]
pub struct RequestLimits {
    /// TCP + TLS connection-establishment timeout.
    pub connect_timeout: Duration,
    /// End-to-end timeout for the whole call, body read included.
    pub overall_timeout: Duration,
    /// Maximum accepted response-body size in bytes.
    pub max_body_bytes: u64,
}

impl Default for RequestLimits {
    fn default() -> Self {
        Self {
            connect_timeout: DEFAULT_CONNECT_TIMEOUT,
            overall_timeout: DEFAULT_OVERALL_TIMEOUT,
            max_body_bytes: DEFAULT_MAX_BODY_BYTES,
        }
    }
}

/// One caller-supplied request header whose value is secret-class.
///
/// T9b composes these from the keychain ([`crate::CredentialStore`]) — e.g.
/// `x-api-key: <admin key>` + `anthropic-version: 2023-06-01`, or
/// `Authorization: Bearer <admin key>`. Every value rides in a
/// [`SecretString`], so it cannot be accidentally `Debug`-printed; non-secret
/// values (like a version pin) simply get wrapped too — one type, uniform
/// redaction.
///
/// **This is the ONLY place credentials may travel — never the URL.** The
/// redaction guarantee (redacting `Debug`, secret-free error text) covers
/// header values only; `ureq` error text can echo the full request URI, so a
/// credential in the URL path/query would sit outside the guarantee. See
/// [`AuthorizedClient::get`].
pub struct AuthHeader {
    name: String,
    value: SecretString,
}

impl AuthHeader {
    /// Build a header. The `value` is treated as secret regardless of content.
    pub fn new(name: impl Into<String>, value: SecretString) -> Self {
        Self {
            name: name.into(),
            value,
        }
    }

    /// The header name (never secret).
    pub fn name(&self) -> &str {
        &self.name
    }
}

impl fmt::Debug for AuthHeader {
    /// Renders the name and a fixed `<redacted>` marker — never the value.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("AuthHeader")
            .field("name", &self.name)
            .field("value", &"<redacted>")
            .finish()
    }
}

/// A successful (2xx) response: the status code plus the bounded raw body.
/// Parsing into typed shapes is the caller's job (T9b).
pub struct HttpResponse {
    status: u16,
    body: Vec<u8>,
}

impl HttpResponse {
    /// The HTTP status code — always in `200..=299`, exactly: every other final
    /// status (1xx included) classifies as a typed error and never constructs
    /// an `HttpResponse`.
    pub fn status(&self) -> u16 {
        self.status
    }

    /// The raw response body bytes.
    pub fn body(&self) -> &[u8] {
        &self.body
    }

    /// The response body, consumed.
    pub fn into_body(self) -> Vec<u8> {
        self.body
    }

    /// The body as UTF-8 text, or `None` if it is not valid UTF-8.
    pub fn text(&self) -> Option<&str> {
        std::str::from_utf8(&self.body).ok()
    }
}

impl fmt::Debug for HttpResponse {
    /// Status + body length only — response bodies stay out of logs/debug output.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("HttpResponse")
            .field("status", &self.status)
            .field("body_len", &self.body.len())
            .finish()
    }
}

/// A blocking HTTPS client bound to **one** explicitly authorized host.
///
/// Construct it over the single host the user authorized (e.g.
/// `api.anthropic.com`); every [`get`](Self::get) whose URL is not on exactly that
/// host fails with [`ConnectError::UnauthorizedHost`] before any I/O. See the
/// module docs for the full pinned posture.
pub struct AuthorizedClient {
    /// `"https"` always in the public API. The test-only loopback constructor —
    /// compiled solely under `cfg(test)`, never part of the prod surface — sets
    /// `"http"` so the suite can exercise the full request path against a local
    /// `TcpListener` without real TLS or any real network.
    scheme: &'static str,
    /// The normalized (lowercase, default-port-stripped) authorized authority.
    authorized: String,
    limits: RequestLimits,
    agent: ureq::Agent,
}

impl AuthorizedClient {
    /// Build a client authorized to talk to exactly `authorized_host` over HTTPS,
    /// with default [`RequestLimits`].
    ///
    /// `authorized_host` must be a bare hostname (`api.anthropic.com`) — no scheme,
    /// port, path, or userinfo. TLS trust comes from the **OS-native root store**
    /// (loaded once here via `rustls-native-certs`).
    pub fn new(authorized_host: &str) -> Result<Self, ConnectError> {
        Self::with_limits(authorized_host, RequestLimits::default())
    }

    /// [`new`](Self::new) with explicit [`RequestLimits`].
    pub fn with_limits(authorized_host: &str, limits: RequestLimits) -> Result<Self, ConnectError> {
        let authorized = validate_authorized_host(authorized_host)?;
        let roots = load_native_roots()?;
        let tls = ureq::tls::TlsConfig::builder()
            .root_certs(ureq::tls::RootCerts::new_with_certs(&roots))
            .build();
        Ok(Self {
            scheme: "https",
            authorized,
            limits,
            agent: build_agent(limits, Some(tls)),
        })
    }

    /// Test-only: a plain-HTTP client over a loopback `host:port` authority, so the
    /// in-crate test suites (the http tests **and** the T9b adapter tests) can drive the
    /// complete request/response/classification path against a local `TcpListener` — no
    /// TLS, no real network, passes offline. `pub(crate)` so the adapter modules reach
    /// it, but still compiled **only** under `cfg(test)`: it is invisible to real builds,
    /// to dependent crates, and to integration tests, so the public production surface
    /// stays HTTPS-only with no escape hatch.
    #[cfg(test)]
    pub(crate) fn loopback_http_for_tests(authority: &str, limits: RequestLimits) -> Self {
        Self {
            scheme: "http",
            authorized: normalize_authority(authority, "http"),
            limits,
            agent: build_agent(limits, None),
        }
    }

    /// The host this client is authorized to talk to.
    pub fn authorized_host(&self) -> &str {
        &self.authorized
    }

    /// Compose an on-host absolute URL from a non-secret `path_and_query` (which must
    /// start with `/`), using this client's own scheme + authorized authority — so an
    /// adapter builds URLs that pass [`ensure_authorized`](Self::ensure_authorized)
    /// without hardcoding the scheme (and so the same adapter code drives the real
    /// HTTPS host in production and the loopback authority under `cfg(test)`).
    ///
    /// **`path_and_query` must contain only non-secret parts** — credentials ride in
    /// [`AuthHeader`] values, never in a URL (`ureq` error text can echo the URI; the
    /// redaction guarantee covers header values only). `pub(crate)`: an in-crate adapter
    /// helper, not part of the public surface.
    pub(crate) fn url_for(&self, path_and_query: &str) -> String {
        format!("{}://{}{}", self.scheme, self.authorized, path_and_query)
    }

    /// Perform a GET against `url`, which **must** be on the authorized host
    /// (`https://<authorized-host>/...`), sending the caller's [`AuthHeader`]s.
    ///
    /// **Credentials go in [`AuthHeader`] values ONLY — never in the URL path or
    /// query.** The redaction guarantee covers header values only; `ureq` error
    /// text can echo the full request URI, so a secret embedded in the URL could
    /// leak into error/`Debug` renders. T9b composes URLs from non-secret parts
    /// exclusively.
    ///
    /// Returns the bounded body on success — success is **exactly `200..=299`**.
    /// Everything else is a typed [`ConnectError`]: off-host →
    /// [`UnauthorizedHost`](ConnectError::UnauthorizedHost)
    /// (before any I/O) · 3xx → [`Redirect`](ConnectError::Redirect) · 429 →
    /// [`RateLimited`](ConnectError::RateLimited) · other 4xx →
    /// [`ClientError`](ConnectError::ClientError) · 5xx →
    /// [`ServerError`](ConnectError::ServerError) · timeouts →
    /// [`Timeout`](ConnectError::Timeout) · oversized body →
    /// [`BodyTooLarge`](ConnectError::BodyTooLarge) · a final 1xx status and
    /// everything else → [`Transport`](ConnectError::Transport). Retry/backoff
    /// policy is the caller's (T9b).
    pub fn get(&self, url: &str, headers: &[AuthHeader]) -> Result<HttpResponse, ConnectError> {
        self.ensure_authorized(url)?;

        let mut request = self.agent.get(url);
        for header in headers {
            request = request.header(header.name.as_str(), header.value.expose_secret());
        }

        let mut response = request.call().map_err(classify_ureq_error)?;
        let status = response.status().as_u16();
        match status {
            // Success is exactly 200..=299 — nothing else ever yields HttpResponse.
            200..=299 => {
                let body =
                    read_bounded(response.body_mut().as_reader(), self.limits.max_body_bytes)?;
                Ok(HttpResponse { status, body })
            }
            300..=399 => Err(ConnectError::Redirect { status }),
            429 => Err(ConnectError::RateLimited {
                retry_after_seconds: retry_after_seconds(&response),
            }),
            400..=499 => {
                // Attach the (bounded) error body so adapters can classify the cause
                // (e.g. an individual-account 403). It is a vendor error message, never
                // a secret — the key rides only in a request header, never a response.
                let body =
                    read_bounded(response.body_mut().as_reader(), self.limits.max_body_bytes)
                        .ok()
                        .and_then(|bytes| String::from_utf8(bytes).ok());
                Err(ConnectError::ClientError { status, body })
            }
            500..=599 => Err(ConnectError::ServerError { status }),
            // A 1xx surfacing as the *final* status (or any other out-of-band
            // status code) is a protocol anomaly, never a success.
            _ => Err(ConnectError::Transport {
                message: if (100..200).contains(&status) {
                    format!("unexpected 1xx status {status}")
                } else {
                    format!("unexpected HTTP status {status}")
                },
            }),
        }
    }

    /// The authorized-host gate: typed error for any URL not on the authorized
    /// host, evaluated **before** any I/O is attempted.
    fn ensure_authorized(&self, url: &str) -> Result<(), ConnectError> {
        let Some((scheme, rest)) = url.split_once("://") else {
            return Err(ConnectError::InvalidUrl {
                reason: "the URL must be absolute (missing `scheme://`)".to_string(),
            });
        };
        if !scheme.eq_ignore_ascii_case(self.scheme) {
            return Err(ConnectError::InvalidUrl {
                reason: format!("only {}:// URLs are allowed by this client", self.scheme),
            });
        }
        let authority_end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
        let authority = &rest[..authority_end];
        if authority.is_empty() {
            return Err(ConnectError::InvalidUrl {
                reason: "the URL has an empty host".to_string(),
            });
        }
        if authority.contains('@') {
            return Err(ConnectError::InvalidUrl {
                reason: "userinfo (`user@host`) is not allowed in request URLs".to_string(),
            });
        }
        if !authority.is_ascii()
            || authority
                .chars()
                .any(|c| c.is_ascii_whitespace() || c.is_ascii_control())
        {
            return Err(ConnectError::InvalidUrl {
                reason: "the URL host contains invalid characters".to_string(),
            });
        }
        let requested = normalize_authority(authority, self.scheme);
        if requested != self.authorized {
            return Err(ConnectError::UnauthorizedHost {
                requested,
                authorized: self.authorized.clone(),
            });
        }
        Ok(())
    }
}

impl fmt::Debug for AuthorizedClient {
    /// Configuration only — a client holds no secret, and keeps holding none.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("AuthorizedClient")
            .field("authorized", &self.authorized)
            .field("limits", &self.limits)
            .finish_non_exhaustive()
    }
}

/// Validate the constructor's authorized host: a bare (ASCII) hostname only.
fn validate_authorized_host(host: &str) -> Result<String, ConnectError> {
    let bad = host.is_empty()
        || !host.is_ascii()
        || host.chars().any(|c| {
            matches!(c, '/' | '?' | '#' | '@' | ':' | '\\')
                || c.is_ascii_whitespace()
                || c.is_ascii_control()
        });
    if bad {
        return Err(ConnectError::InvalidHost {
            // The example hostname stays neutral: this generic layer compiles in
            // zero provider knowledge (PRODUCT-PLAN §12.11 scope fence).
            reason: "the authorized host must be a bare hostname like `example.com` \
                     (no scheme, port, path, or userinfo)"
                .to_string(),
        });
    }
    Ok(host.to_ascii_lowercase())
}

/// Lowercase the authority and strip the scheme's default port, so
/// `Host:443` ≡ `host` for https (and `:80` for the test-only http scheme).
fn normalize_authority(authority: &str, scheme: &str) -> String {
    let lower = authority.to_ascii_lowercase();
    let default_port = if scheme == "https" { ":443" } else { ":80" };
    match lower.strip_suffix(default_port) {
        Some(host) if !host.is_empty() => host.to_string(),
        _ => lower,
    }
}

/// One agent per client: redirects disabled, proxies disabled, statuses returned
/// (not raised) so this module classifies them, bounded timeouts, pinned
/// User-Agent, and (for the real HTTPS path) OS-native trust roots.
fn build_agent(limits: RequestLimits, tls: Option<ureq::tls::TlsConfig>) -> ureq::Agent {
    let mut builder = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .max_redirects(0)
        .max_redirects_will_error(false)
        .proxy(None)
        .user_agent(USER_AGENT)
        .timeout_connect(Some(limits.connect_timeout))
        .timeout_global(Some(limits.overall_timeout));
    if let Some(tls) = tls {
        builder = builder.tls_config(tls);
    }
    ureq::Agent::new_with_config(builder.build())
}

/// Load the OS-native trust roots (macOS Security framework / Windows cert store /
/// Linux `/etc/ssl` & friends) via `rustls-native-certs`. An empty store is a
/// typed error — never a silently-untrusting client.
fn load_native_roots() -> Result<Vec<ureq::tls::Certificate<'static>>, ConnectError> {
    let result = rustls_native_certs::load_native_certs();
    if result.certs.is_empty() {
        let detail = if result.errors.is_empty() {
            "the OS trust store yielded no certificates".to_string()
        } else {
            result
                .errors
                .iter()
                .map(|e| e.to_string())
                .collect::<Vec<_>>()
                .join("; ")
        };
        return Err(ConnectError::NativeRoots { detail });
    }
    Ok(result
        .certs
        .iter()
        .map(|der| ureq::tls::Certificate::from_der(der.as_ref()).to_owned())
        .collect())
}

/// Classify a `ureq` transport-level failure. Header values never appear in
/// `ureq` error text, so the resulting messages are secret-free (pinned by test).
fn classify_ureq_error(err: ureq::Error) -> ConnectError {
    match err {
        ureq::Error::Timeout(_) => ConnectError::Timeout,
        ureq::Error::Io(io_err) => classify_io_error(io_err),
        // With redirects disabled (`max_redirects(0)`), ureq returns 3xx responses
        // instead of raising redirect errors, so any other variant here is a plain
        // transport-class failure (DNS, connection, TLS, protocol).
        other => ConnectError::Transport {
            message: other.to_string(),
        },
    }
}

/// Classify an I/O failure (connect or body read): timeouts are [`ConnectError::Timeout`],
/// everything else transport. `ureq` wraps its own errors (including timeouts hit
/// during a body read) in `io::Error`, so unwrap one level before deciding.
fn classify_io_error(err: std::io::Error) -> ConnectError {
    if err.kind() == std::io::ErrorKind::TimedOut {
        return ConnectError::Timeout;
    }
    if let Some(inner) = err.get_ref() {
        if let Some(ureq_err) = inner.downcast_ref::<ureq::Error>() {
            if matches!(ureq_err, ureq::Error::Timeout(_)) {
                return ConnectError::Timeout;
            }
        }
    }
    ConnectError::Transport {
        message: err.to_string(),
    }
}

/// Parse a `Retry-After: <seconds>` header (the HTTP-date form maps to `None`) so
/// the caller's backoff policy has the datum without re-touching the response.
fn retry_after_seconds(response: &ureq::http::Response<ureq::Body>) -> Option<u64> {
    response
        .headers()
        .get("retry-after")?
        .to_str()
        .ok()?
        .trim()
        .parse()
        .ok()
}

/// Read at most `max` body bytes; one byte more is [`ConnectError::BodyTooLarge`].
fn read_bounded(reader: impl Read, max: u64) -> Result<Vec<u8>, ConnectError> {
    let mut body = Vec::new();
    reader
        .take(max.saturating_add(1))
        .read_to_end(&mut body)
        .map_err(classify_io_error)?;
    if body.len() as u64 > max {
        return Err(ConnectError::BodyTooLarge { limit_bytes: max });
    }
    Ok(body)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

    // The workspace forbids `.unwrap()`/`.expect()` even in tests; panic-based
    // helpers stand in (cf. the lib.rs test module).
    #[track_caller]
    fn ok<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }
    #[track_caller]
    fn err<T: std::fmt::Debug, E>(result: Result<T, E>) -> E {
        match result {
            Ok(value) => panic!("expected Err, got Ok: {value:?}"),
            Err(err) => err,
        }
    }

    /// Tight limits so loopback tests finish fast; the timeout test relies on
    /// `overall_timeout` ≪ the server's delay.
    fn test_limits() -> RequestLimits {
        RequestLimits {
            connect_timeout: Duration::from_secs(2),
            overall_timeout: Duration::from_millis(500),
            max_body_bytes: 1024,
        }
    }

    /// Serve exactly one connection on a loopback listener: read the request head,
    /// optionally stall, then write `response` verbatim. Returns the bound
    /// `host:port` authority and a channel yielding the captured raw request.
    fn serve_once(response: Vec<u8>, stall: Duration) -> (String, mpsc::Receiver<String>) {
        let listener = ok(TcpListener::bind("127.0.0.1:0"));
        let authority = format!("127.0.0.1:{}", ok(listener.local_addr()).port());
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            if let Ok((mut stream, _)) = listener.accept() {
                let mut request = Vec::new();
                let mut buf = [0u8; 1024];
                while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                    match stream.read(&mut buf) {
                        Ok(0) | Err(_) => break,
                        Ok(n) => request.extend_from_slice(&buf[..n]),
                    }
                }
                let _ = tx.send(String::from_utf8_lossy(&request).into_owned());
                if !stall.is_zero() {
                    thread::sleep(stall);
                }
                let _ = stream.write_all(&response);
                let _ = stream.flush();
            }
        });
        (authority, rx)
    }

    fn http_response(status_line: &str, extra_headers: &[&str], body: &[u8]) -> Vec<u8> {
        let mut out = format!(
            "HTTP/1.1 {status_line}\r\nContent-Length: {}\r\n",
            body.len()
        );
        for header in extra_headers {
            out.push_str(header);
            out.push_str("\r\n");
        }
        out.push_str("Connection: close\r\n\r\n");
        let mut bytes = out.into_bytes();
        bytes.extend_from_slice(body);
        bytes
    }

    fn loopback_client(authority: &str) -> AuthorizedClient {
        AuthorizedClient::loopback_http_for_tests(authority, test_limits())
    }

    // ---- authorized-host enforcement (no I/O) ----------------------------

    #[test]
    fn off_host_url_is_refused_before_any_io() {
        // No server exists for either host: if the client attempted I/O the error
        // would be a DNS/connect Transport failure, not UnauthorizedHost.
        let client = loopback_client("127.0.0.1:9");
        let error = err(client.get("http://192.0.2.1:9/v1/anything", &[]));
        match error {
            ConnectError::UnauthorizedHost {
                requested,
                authorized,
            } => {
                assert_eq!(requested, "192.0.2.1:9");
                assert_eq!(authorized, "127.0.0.1:9");
            }
            other => panic!("expected UnauthorizedHost, got {other:?}"),
        }
        // A port mismatch on the right host is off-host too.
        let error = err(client.get("http://127.0.0.1:10/v1", &[]));
        assert!(matches!(error, ConnectError::UnauthorizedHost { .. }));
    }

    #[test]
    fn https_client_enforces_host_and_scheme() {
        // The real (https) constructor: loads OS-native roots, then refuses both
        // an off-host URL and a plain-http URL without any I/O.
        let client = ok(AuthorizedClient::new("api.example-authorized.test"));
        assert_eq!(client.authorized_host(), "api.example-authorized.test");

        let error = err(client.get("https://api.other.test/v1", &[]));
        assert!(matches!(error, ConnectError::UnauthorizedHost { .. }));

        // An explicit NON-default port (:8443) is a different authority — off-host.
        // (The :443 default-port *accept* path is covered by the normalize_authority
        // unit test and off_case_default_port_url_is_accepted_as_on_host below.)
        let error = err(client.get("https://API.Example-Authorized.TEST:8443/v1", &[]));
        assert!(matches!(error, ConnectError::UnauthorizedHost { .. }));

        let error = err(client.get("http://api.example-authorized.test/v1", &[]));
        assert!(matches!(error, ConnectError::InvalidUrl { .. }));

        let error = err(client.get("not a url", &[]));
        assert!(matches!(error, ConnectError::InvalidUrl { .. }));

        let error = err(client.get("https://evil@api.example-authorized.test/", &[]));
        assert!(matches!(error, ConnectError::InvalidUrl { .. }));
    }

    #[test]
    fn constructor_rejects_non_bare_hosts() {
        for bad in [
            "",
            "https://api.example.test",
            "api.example.test/path",
            "api.example.test:443",
            "user@api.example.test",
            "api.example .test",
        ] {
            let error = err(AuthorizedClient::new(bad));
            assert!(
                matches!(error, ConnectError::InvalidHost { .. }),
                "host {bad:?} must be rejected as InvalidHost, got {error:?}"
            );
        }
    }

    #[test]
    fn normalize_authority_strips_default_port_and_case_folds() {
        // The accept path of default-port normalization: `Host:443` over https
        // normalizes equal to the bare lowercase `host`.
        assert_eq!(normalize_authority("Host:443", "https"), "host");
        assert_eq!(normalize_authority("host", "https"), "host");
        // A NON-default port survives normalization (it stays a distinct authority).
        assert_eq!(normalize_authority("Host:8443", "https"), "host:8443");
        // The test-only http scheme strips :80, not :443.
        assert_eq!(normalize_authority("Host:80", "http"), "host");
        assert_eq!(normalize_authority("Host:443", "http"), "host:443");
    }

    #[test]
    fn off_case_default_port_url_is_accepted_as_on_host() {
        // ensure_authorized (the pre-I/O gate, exercised directly — no request is
        // made) accepts an off-case host with an explicit :443 against a client
        // authorized for the bare lowercase host.
        let client = ok(AuthorizedClient::new("host"));
        ok(client.ensure_authorized("https://HOST:443/v1/report?period=day"));
        ok(client.ensure_authorized("https://Host/v1"));
    }

    // ---- the success path -------------------------------------------------

    #[test]
    fn success_returns_body_and_sends_user_agent_and_auth_headers() {
        let (authority, captured) = serve_once(
            http_response("200 OK", &[], b"{\"ok\":true}"),
            Duration::ZERO,
        );
        let client = loopback_client(&authority);
        let secret = SecretString::from("sk-test-secret-abc".to_string());
        let response = ok(client.get(
            &format!("http://{authority}/v1/report?period=day"),
            &[AuthHeader::new("x-api-key", secret)],
        ));

        assert_eq!(response.status(), 200);
        assert_eq!(response.body(), b"{\"ok\":true}");
        assert_eq!(response.text(), Some("{\"ok\":true}"));

        let request = ok(captured.recv());
        let lower = request.to_ascii_lowercase();
        assert!(
            lower.contains(&format!(
                "user-agent: costroid/{}",
                env!("CARGO_PKG_VERSION")
            )),
            "the pinned User-Agent must be sent, got request:\n{request}"
        );
        assert!(
            lower.contains("x-api-key: sk-test-secret-abc"),
            "the auth header must reach the authorized host, got request:\n{request}"
        );
        assert!(request.starts_with("GET /v1/report?period=day "));
    }

    // ---- classification ---------------------------------------------------

    #[test]
    fn redirect_is_a_typed_error_and_never_followed() {
        // The Location points at a host that does not exist; if the client
        // followed it the result would be a Transport error instead.
        let (authority, _captured) = serve_once(
            http_response("302 Found", &["Location: https://elsewhere.invalid/"], b""),
            Duration::ZERO,
        );
        let client = loopback_client(&authority);
        let error = err(client.get(&format!("http://{authority}/v1"), &[]));
        assert!(
            matches!(error, ConnectError::Redirect { status: 302 }),
            "expected Redirect, got {error:?}"
        );
    }

    #[test]
    fn rate_limit_classifies_with_retry_after() {
        let (authority, _captured) = serve_once(
            http_response("429 Too Many Requests", &["Retry-After: 7"], b"slow down"),
            Duration::ZERO,
        );
        let client = loopback_client(&authority);
        let error = err(client.get(&format!("http://{authority}/v1"), &[]));
        match error {
            ConnectError::RateLimited {
                retry_after_seconds,
            } => assert_eq!(retry_after_seconds, Some(7)),
            other => panic!("expected RateLimited, got {other:?}"),
        }
    }

    #[test]
    fn bare_rate_limit_without_retry_after_yields_none() {
        // A 429 with no Retry-After header at all: still RateLimited, with
        // retry_after_seconds = None (no panic, no fabricated value).
        let (authority, _captured) = serve_once(
            http_response("429 Too Many Requests", &[], b""),
            Duration::ZERO,
        );
        let error = err(loopback_client(&authority).get(&format!("http://{authority}/v1"), &[]));
        match error {
            ConnectError::RateLimited {
                retry_after_seconds,
            } => assert_eq!(retry_after_seconds, None),
            other => panic!("expected RateLimited, got {other:?}"),
        }
    }

    #[test]
    fn http_date_retry_after_yields_none() {
        // The HTTP-date form of Retry-After is valid per spec but is not the
        // seconds form; it maps to None — never a misparse, never a panic.
        let (authority, _captured) = serve_once(
            http_response(
                "429 Too Many Requests",
                &["Retry-After: Wed, 21 Oct 2026 07:28:00 GMT"],
                b"",
            ),
            Duration::ZERO,
        );
        let error = err(loopback_client(&authority).get(&format!("http://{authority}/v1"), &[]));
        match error {
            ConnectError::RateLimited {
                retry_after_seconds,
            } => assert_eq!(retry_after_seconds, None),
            other => panic!("expected RateLimited, got {other:?}"),
        }
    }

    #[test]
    fn final_1xx_status_never_classifies_as_success() {
        // A 103 written as the FINAL response. Whichever way ureq surfaces it —
        // handing the 1xx through (→ this module's "unexpected 1xx status" arm)
        // or skipping the interim response and hitting the connection close
        // while awaiting a final one (→ an I/O failure) — the classification is
        // Transport, never an HttpResponse.
        let (authority, _captured) =
            serve_once(http_response("103 Early Hints", &[], b""), Duration::ZERO);
        let error = err(loopback_client(&authority).get(&format!("http://{authority}/v1"), &[]));
        assert!(
            matches!(error, ConnectError::Transport { .. }),
            "expected Transport, got {error:?}"
        );
    }

    #[test]
    fn server_and_client_errors_classify_by_status() {
        let (authority, _captured) = serve_once(
            http_response("503 Service Unavailable", &[], b""),
            Duration::ZERO,
        );
        let error = err(loopback_client(&authority).get(&format!("http://{authority}/v1"), &[]));
        assert!(matches!(error, ConnectError::ServerError { status: 503 }));

        let (authority, _captured) =
            serve_once(http_response("404 Not Found", &[], b""), Duration::ZERO);
        let error = err(loopback_client(&authority).get(&format!("http://{authority}/v1"), &[]));
        assert!(matches!(
            error,
            ConnectError::ClientError { status: 404, .. }
        ));
    }

    #[test]
    fn stalled_response_classifies_as_timeout() {
        // The server reads the request then stalls past overall_timeout (500ms).
        let (authority, _captured) = serve_once(
            http_response("200 OK", &[], b"late"),
            Duration::from_secs(3),
        );
        let client = loopback_client(&authority);
        let error = err(client.get(&format!("http://{authority}/v1"), &[]));
        assert!(
            matches!(error, ConnectError::Timeout),
            "expected Timeout, got {error:?}"
        );
    }

    #[test]
    fn oversized_body_classifies_as_body_too_large() {
        let big_body = vec![b'x'; 4096]; // > the 1024-byte test cap
        let (authority, _captured) =
            serve_once(http_response("200 OK", &[], &big_body), Duration::ZERO);
        let client = loopback_client(&authority);
        let error = err(client.get(&format!("http://{authority}/v1"), &[]));
        assert!(
            matches!(error, ConnectError::BodyTooLarge { limit_bytes: 1024 }),
            "expected BodyTooLarge, got {error:?}"
        );
    }

    #[test]
    fn connection_refused_classifies_as_transport() {
        // Bind to learn a free port, then drop the listener so the connect is refused.
        let authority = {
            let listener = ok(TcpListener::bind("127.0.0.1:0"));
            format!("127.0.0.1:{}", ok(listener.local_addr()).port())
        };
        let client = loopback_client(&authority);
        let error = err(client.get(&format!("http://{authority}/v1"), &[]));
        assert!(
            matches!(error, ConnectError::Transport { .. }),
            "expected Transport, got {error:?}"
        );
    }

    // ---- secret hygiene ---------------------------------------------------

    #[test]
    fn no_debug_display_or_error_text_ever_carries_the_secret() {
        const SECRET: &str = "sk-super-secret-token-xyz";

        // The header's own Debug redacts.
        let header = AuthHeader::new("authorization", SecretString::from(SECRET.to_string()));
        let rendered = format!("{header:?}");
        assert!(
            !rendered.contains(SECRET),
            "AuthHeader Debug must redact the value, got: {rendered}"
        );
        assert!(rendered.contains("authorization"), "the name stays visible");
        assert!(rendered.contains("<redacted>"));

        // A failing request carrying the secret: neither Debug nor Display of the
        // classified error may embed it.
        let (authority, _captured) = serve_once(
            http_response("500 Internal Server Error", &[], b""),
            Duration::ZERO,
        );
        let client = loopback_client(&authority);
        let error = err(client.get(
            &format!("http://{authority}/v1"),
            &[AuthHeader::new(
                "authorization",
                SecretString::from(SECRET.to_string()),
            )],
        ));
        let rendered = format!("{error:?} / {error}");
        assert!(
            !rendered.contains(SECRET),
            "no error render may carry the secret: {rendered}"
        );

        // A transport-class failure (connection refused) likewise.
        let dead_authority = {
            let listener = ok(TcpListener::bind("127.0.0.1:0"));
            format!("127.0.0.1:{}", ok(listener.local_addr()).port())
        };
        let client = loopback_client(&dead_authority);
        let error = err(client.get(
            &format!("http://{dead_authority}/v1"),
            &[AuthHeader::new(
                "x-api-key",
                SecretString::from(SECRET.to_string()),
            )],
        ));
        let rendered = format!("{error:?} / {error}");
        assert!(
            !rendered.contains(SECRET),
            "no transport-error render may carry the secret: {rendered}"
        );

        // And the client's own Debug stays configuration-only.
        let rendered = format!("{client:?}");
        assert!(!rendered.contains(SECRET));
    }

    #[test]
    fn invalid_header_value_error_never_carries_the_secret() {
        // An embedded LF makes the value an invalid HTTP header value, so it is
        // fed into ureq/http's own error machinery (HeaderValue::try_from) — the
        // one path where the secret VALUE itself enters foreign error types.
        // Neither Debug nor Display of the classified error may echo any part
        // of it.
        let (authority, _captured) =
            serve_once(http_response("200 OK", &[], b"{}"), Duration::ZERO);
        let client = loopback_client(&authority);
        let error = err(client.get(
            &format!("http://{authority}/v1"),
            &[AuthHeader::new(
                "x-api-key",
                SecretString::from("sk-bad\nsecret".to_string()),
            )],
        ));
        let rendered = format!("{error:?} / {error}");
        assert!(
            !rendered.contains("sk-bad"),
            "invalid-header error render must not carry the secret prefix: {rendered}"
        );
        assert!(
            !rendered.contains("secret"),
            "invalid-header error render must not carry the secret tail: {rendered}"
        );
    }

    // ---- defaults ----------------------------------------------------------

    #[test]
    fn default_limits_are_bounded() {
        let limits = RequestLimits::default();
        assert_eq!(limits.connect_timeout, Duration::from_secs(10));
        assert_eq!(limits.overall_timeout, Duration::from_secs(30));
        assert_eq!(limits.max_body_bytes, 8 * 1024 * 1024);
    }
}
