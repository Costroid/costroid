//! Loopback HTTP test harness shared by the in-crate adapter tests **and** — under the
//! opt-in `test-support` feature — by a dependent crate's tests (the T10a CLI Layer-1
//! connect-action test). A tiny multi-response server on `127.0.0.1` drives the adapters
//! through their full fetch → paginate → parse → classify path with **hand-built fixture
//! bodies, zero real network**, and the keyring **mock** installer gives a process-global
//! in-memory keychain so a `CredentialStore` round-trips without touching the real OS
//! keychain. Compiled only under `cfg(test)` or `feature = "test-support"`, so none of it
//! reaches a real `--features connect` build or the public production surface: the loopback
//! `AuthorizedClient` constructor and the adapter `with_client` seams stay `pub(crate)` —
//! only this curated API is `pub`.

use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::mpsc::{self, Receiver, RecvError};
use std::sync::Once;
use std::thread::{self, JoinHandle};
use std::time::Duration;

use crate::anthropic::AnthropicAdapter;
use crate::fetch::RetryPolicy;
use crate::http::AuthorizedClient;
use crate::openai::OpenAiAdapter;
use crate::RequestLimits;

// The workspace clippy lints deny `.unwrap()`/`.expect()` even in tests, so these
// panic-on-failure helpers stand in (panics are allowed in tests).
#[track_caller]
pub fn ok<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
    match result {
        Ok(value) => value,
        Err(err) => panic!("expected Ok, got Err: {err:?}"),
    }
}

#[track_caller]
pub fn err<T: std::fmt::Debug, E>(result: Result<T, E>) -> E {
    match result {
        Ok(value) => panic!("expected Err, got Ok: {value:?}"),
        Err(err) => err,
    }
}

/// Install the process-global keyring **mock** backend (idempotent), so every
/// [`crate::CredentialStore`] built afterward stores secrets in an in-memory map instead
/// of the real OS keychain. Must run before any `CredentialStore` is created.
pub fn install_mock_keychain() {
    static MOCK: Once = Once::new();
    MOCK.call_once(|| {
        keyring::set_default_credential_builder(keyring::mock::default_credential_builder());
    });
}

/// Tight per-request limits so loopback tests finish fast.
pub fn test_limits() -> RequestLimits {
    RequestLimits {
        connect_timeout: Duration::from_secs(2),
        overall_timeout: Duration::from_secs(2),
        max_body_bytes: 1 << 20,
    }
}

/// A loopback server that answers a fixed sequence of raw HTTP responses (one per
/// incoming connection — every reply sends `Connection: close`, so a paginating client
/// opens a fresh connection per page). Captures each request line+headers for
/// assertions.
pub struct MockServer {
    /// The bound `127.0.0.1:<port>` authority.
    pub authority: String,
    requests: Receiver<String>,
    _handle: JoinHandle<()>,
}

impl MockServer {
    /// The next captured raw request (head only — the GET body, if any, is ignored).
    pub fn next_request(&self) -> Result<String, RecvError> {
        self.requests.recv()
    }

    /// A loopback [`AuthorizedClient`] (plain HTTP, the `pub(crate)` loopback ctor) bound
    /// to this server's authority, with [`test_limits`].
    pub fn client(&self) -> AuthorizedClient {
        AuthorizedClient::loopback_http_for_tests(&self.authority, test_limits())
    }

    /// An [`AnthropicAdapter`] bound to this loopback server (zero-delay retry policy).
    pub fn anthropic_adapter(&self) -> AnthropicAdapter {
        AnthropicAdapter::with_client(self.client(), RetryPolicy::test())
    }

    /// An [`OpenAiAdapter`] bound to this loopback server (zero-delay retry policy).
    pub fn openai_adapter(&self) -> OpenAiAdapter {
        OpenAiAdapter::with_client(self.client(), RetryPolicy::test())
    }
}

/// Serve `replies` in order, one per accepted connection.
pub fn serve_sequence(replies: Vec<Vec<u8>>) -> MockServer {
    let listener = ok(TcpListener::bind("127.0.0.1:0"));
    let authority = format!("127.0.0.1:{}", ok(listener.local_addr()).port());
    let (tx, rx) = mpsc::channel();
    let handle = thread::spawn(move || {
        for reply in replies {
            match listener.accept() {
                Ok((mut stream, _)) => {
                    let mut request = Vec::new();
                    let mut buf = [0u8; 1024];
                    while !request.windows(4).any(|window| window == b"\r\n\r\n") {
                        match stream.read(&mut buf) {
                            Ok(0) | Err(_) => break,
                            Ok(n) => request.extend_from_slice(&buf[..n]),
                        }
                    }
                    let _ = tx.send(String::from_utf8_lossy(&request).into_owned());
                    let _ = stream.write_all(&reply);
                    let _ = stream.flush();
                }
                Err(_) => break,
            }
        }
    });
    MockServer {
        authority,
        requests: rx,
        _handle: handle,
    }
}

/// Build a raw HTTP response with an explicit status line, extra headers, and a string
/// body. Always closes the connection so each request is a fresh accept.
pub fn reply(status_line: &str, extra_headers: &[&str], body: &str) -> Vec<u8> {
    let mut head = format!(
        "HTTP/1.1 {status_line}\r\nContent-Length: {}\r\n",
        body.len()
    );
    for header in extra_headers {
        head.push_str(header);
        head.push_str("\r\n");
    }
    head.push_str("Connection: close\r\n\r\n");
    let mut bytes = head.into_bytes();
    bytes.extend_from_slice(body.as_bytes());
    bytes
}

/// A `200 OK` JSON response.
pub fn ok_json(body: &str) -> Vec<u8> {
    reply("200 OK", &["Content-Type: application/json"], body)
}
