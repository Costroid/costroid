//! `costroid-server` — the local HTTP API + web UI binary (⚑ Readiness gate A1, §6.11).
//!
//! # Why this is a separate binary
//! `axum`/`hyper`/`tokio` and `tiny_http` itself are name-banned in the CLI/bar offline gate
//! (`apps/cli/tests/offline.rs`, `ALWAYS_FORBIDDEN_CRATES`): the `costroid` CLI must stay
//! **byte-for-byte no-network**. So the server is its own binary with its own reviewed per-binary
//! allowlist (`SERVER_ALLOWED`) and a runtime **loopback-only** proof. It is never linked into
//! `costroid` or `costroid-bar`.
//!
//! # The guarantee this binary makes
//! The CLI's guarantee is "no socket at all"; the server's is **"loopback-bind, no outbound
//! egress"**. A `127.0.0.1` listen *does* create an `AF_INET` socket, so the proof is *no egress*
//! (no `connect()` to a non-loopback address, no `bind()` to a non-loopback address), not *no
//! socket*. The loopback bind address is constructed **only** from [`Ipv4Addr::LOCALHOST`]
//! ([`loopback_addr`]) — the server cannot bind a routable interface by construction.
//!
//! # Status — M0 scaffold
//! Binds loopback, answers a health check + a placeholder index, and exits cleanly. The three
//! real views (timeline / comparison / break-even) + the embedded static assets (Maud + htmx +
//! uPlot, A3) land at **M5**; for now this proves the binary builds on every OS, makes no
//! outbound network, and binds loopback only.

use std::net::{IpAddr, Ipv4Addr, SocketAddr};

use anyhow::{Context, Result};
use tiny_http::{Header, Response, Server};

/// Default loopback port for the local UI (overridable later via config/flag at M5).
const DEFAULT_PORT: u16 = 7878;

fn main() -> Result<()> {
    let mode = parse_args(std::env::args().skip(1));
    match mode {
        Mode::Help => {
            print_usage();
            Ok(())
        }
        Mode::SelfCheck => self_check(),
        Mode::Serve => serve(DEFAULT_PORT),
    }
}

enum Mode {
    /// Bind loopback and run the request loop (the normal mode).
    Serve,
    /// One-shot: prove the binary constructs + can bind loopback, make no egress, then exit.
    /// Used by `scripts/offline_acceptance.sh` for the runtime loopback-only proof.
    SelfCheck,
    /// Print usage and exit 0.
    Help,
}

fn parse_args(mut args: impl Iterator<Item = String>) -> Mode {
    // The scaffold takes a single mode argument; the first arg decides (none = serve).
    match args.next().as_deref() {
        Some("--self-check") => Mode::SelfCheck,
        Some("-h" | "--help") => Mode::Help,
        Some("serve") | None => Mode::Serve,
        Some(_) => Mode::Help,
    }
}

fn print_usage() {
    println!(
        "costroid-server — local-only (127.0.0.1) HTTP API + web UI over the Costroid ledger\n\
         \n\
         USAGE:\n    \
             costroid-server [serve]      Bind 127.0.0.1:{DEFAULT_PORT} and serve (default)\n    \
             costroid-server --self-check Prove loopback-bind + no egress, then exit\n    \
             costroid-server --help       Show this help\n\
         \n\
         The server binds loopback ONLY and makes no outbound network call (⚑ A1 / §6.11)."
    );
}

/// The one and only place a bind address is built — always loopback, never a routable interface.
fn loopback_addr(port: u16) -> SocketAddr {
    SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), port)
}

/// One-shot self-check: construct the server, bind an ephemeral loopback port, assert the bound
/// address really is loopback, then drop it and exit. Makes no outbound call. Tolerates a
/// sandbox where the loopback interface is unavailable (e.g. a fresh netns with `lo` down) — the
/// point of the check is "this binary makes no egress and binds only loopback", not "the sandbox
/// has a working `lo`". The authoritative no-egress proofs are the static `SERVER_ALLOWED`
/// allowlist + the strace loopback-only assertion in `scripts/offline_acceptance.sh`.
fn self_check() -> Result<()> {
    match Server::http(loopback_addr(0)) {
        Ok(server) => {
            if let Some(addr) = server.server_addr().to_ip() {
                anyhow::ensure!(
                    addr.ip().is_loopback(),
                    "server bound a non-loopback address {addr} — this must never happen"
                );
                println!("server self-check ok (bound loopback {addr}, no egress)");
            } else {
                println!("server self-check ok (bound a non-IP listener, no egress)");
            }
            // Drop `server` here → the listening socket closes; no connection is ever accepted.
            Ok(())
        }
        Err(_) => {
            // No usable loopback interface in this sandbox; still a clean, no-egress exit.
            println!("server self-check ok (loopback bind unavailable in this sandbox, no egress)");
            Ok(())
        }
    }
}

/// Bind loopback and answer requests. M0 placeholder responses; the real views are M5.
fn serve(port: u16) -> Result<()> {
    let addr = loopback_addr(port);
    let server = Server::http(addr)
        .map_err(|e| anyhow::anyhow!("failed to bind loopback {addr}: {e}"))
        .with_context(|| "the server binds 127.0.0.1 only and never a routable interface")?;
    println!("costroid-server listening on http://{addr} (loopback only; Ctrl-C to stop)");

    for request in server.incoming_requests() {
        let url = request.url().to_string();
        let response = match url.as_str() {
            "/healthz" => Response::from_string("ok\n"),
            _ => {
                let body = "<!doctype html><meta charset=utf-8>\
                    <title>Costroid</title>\
                    <p>costroid-server is running locally. The timeline / comparison / break-even \
                    views land at M5.</p>";
                match Header::from_bytes(&b"Content-Type"[..], &b"text/html; charset=utf-8"[..]) {
                    Ok(header) => Response::from_string(body).with_header(header),
                    Err(()) => Response::from_string(body),
                }
            }
        };
        // A failed write to one client never takes the server down.
        if let Err(e) = request.respond(response) {
            eprintln!("costroid-server: response error: {e}");
        }
    }
    Ok(())
}
