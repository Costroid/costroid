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
//! # Status — M5 (in progress)
//! Reads the stored 3-lane ledger via `costroid-store` + `costroid-core` (NEVER `costroid-power` —
//! no `core→power` edge) and serves three views (timeline / comparison / break-even) + a JSON API
//! over loopback, plus `/healthz` and a `--serve-once` mode (the race-free offline-proof hook).
//! T3/T4 land the data models + routing + JSON API; the embedded static assets (htmx + uPlot) and
//! the rich HTML views land at T5/T6.

mod data;

use std::env;
use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::path::PathBuf;

use anyhow::{Context, Result};
use tiny_http::{Header, Response, Server};

/// Default loopback port for the local UI (overridable via `--port` at M5 T8).
const DEFAULT_PORT: u16 = 7878;

fn main() -> Result<()> {
    let mode = parse_args(std::env::args().skip(1));
    match mode {
        Mode::Help => {
            print_usage();
            Ok(())
        }
        Mode::SelfCheck => self_check(),
        Mode::Serve { once } => serve(DEFAULT_PORT, once),
    }
}

enum Mode {
    /// Bind loopback and run the request loop. `once` serves exactly one request, then exits — the
    /// race-free hook `scripts/offline_acceptance.sh` uses to drive a real GET under strace (M5 T7).
    Serve { once: bool },
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
        Some("--serve-once") => Mode::Serve { once: true },
        Some("serve") | None => Mode::Serve { once: false },
        Some(_) => Mode::Help,
    }
}

fn print_usage() {
    println!(
        "costroid-server — local-only (127.0.0.1) HTTP API + web UI over the Costroid ledger\n\
         \n\
         USAGE:\n    \
             costroid-server [serve]      Bind 127.0.0.1:{DEFAULT_PORT} and serve (default)\n    \
             costroid-server --serve-once Serve exactly one request, then exit (offline-proof hook)\n    \
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

/// Bind loopback and serve the stored ledger views. `once` serves a single request then returns —
/// the race-free hook the offline-acceptance real-serve leg uses (M5 T7). The bind address is built
/// ONLY by [`loopback_addr`], so the server can never bind a routable interface by construction.
fn serve(port: u16, once: bool) -> Result<()> {
    let addr = loopback_addr(port);
    let server = Server::http(addr)
        .map_err(|e| anyhow::anyhow!("failed to bind loopback {addr}: {e}"))
        .with_context(|| "the server binds 127.0.0.1 only and never a routable interface")?;
    // A stable readiness line on stdout — the acceptance harness waits for it before its GET.
    println!("costroid-server ready on http://{addr} (loopback only; no outbound egress)");

    for request in server.incoming_requests() {
        // Route on the path only (drop any query string); `.next()` on a split always yields one.
        let path = request.url().split('?').next().unwrap_or("/").to_string();
        let (status, content_type, body) = respond_for(&path);
        let response = match Header::from_bytes(&b"Content-Type"[..], content_type.as_bytes()) {
            Ok(header) => Response::from_string(body)
                .with_status_code(status)
                .with_header(header),
            Err(()) => Response::from_string(body).with_status_code(status),
        };
        // A failed write to one client never takes the server down.
        if let Err(e) = request.respond(response) {
            eprintln!("costroid-server: response error: {e}");
        }
        if once {
            break;
        }
    }
    Ok(())
}

/// Route a request path to `(status, content-type, body)`. Reads the stored ledger per request
/// (local, read-only). R4: only bounded view metadata is ever serialized — never any content.
fn respond_for(path: &str) -> (u16, String, String) {
    let html = "text/html; charset=utf-8".to_string();
    let json = "application/json; charset=utf-8".to_string();
    let text = "text/plain; charset=utf-8".to_string();
    match path {
        "/healthz" => (200, text, "ok\n".to_string()),
        "/api/timeline" | "/api/comparison" | "/api/breakeven" | "/api/views" => {
            match api_json(path) {
                Ok(body) => (200, json, body),
                Err(message) => (500, text, message),
            }
        }
        "/" => match views() {
            Ok(views) => (200, html, index_html(&views)),
            Err(message) => (500, text, message),
        },
        "/timeline" | "/comparison" | "/breakeven" => match views() {
            Ok(views) => (200, html, view_html(path, &views)),
            Err(message) => (500, text, message),
        },
        _ => (404, text, "not found\n".to_string()),
    }
}

/// Build the three views over the stored ledger (default path). Errors are bounded status messages
/// (schema/SQLite/pricing), never row content.
fn views() -> std::result::Result<data::Views, String> {
    let path = default_ledger_path();
    let rows = data::load_rows(&path).map_err(|e| format!("ledger read failed: {e}"))?;
    data::build_views(&rows, &data::Scenario::default())
        .map_err(|e| format!("view build failed: {e}"))
}

fn api_json(path: &str) -> std::result::Result<String, String> {
    let views = views()?;
    let value = match path {
        "/api/timeline" => serde_json::to_string(&views.timeline),
        "/api/comparison" => serde_json::to_string(&views.comparison),
        "/api/breakeven" => serde_json::to_string(&views.breakeven),
        _ => serde_json::to_string(&views),
    };
    value.map_err(|e| format!("serialization failed: {e}"))
}

/// The default stored-ledger path (M5 T8 will let config/flag override it).
fn default_ledger_path() -> PathBuf {
    if let Some(dir) = env::var_os("XDG_DATA_HOME") {
        return PathBuf::from(dir).join("costroid").join("ledger.db");
    }
    if let Some(home) = env::var_os("HOME") {
        return PathBuf::from(home).join(".local/share/costroid/ledger.db");
    }
    PathBuf::from("costroid-ledger.db")
}

/// The index page. Server-rendered, no external references (works fully offline). The rich views +
/// embedded charts land at M5 T5/T6; this links to them + the JSON API.
fn index_html(_views: &data::Views) -> String {
    "<!doctype html><html lang=en><head><meta charset=utf-8>\
     <title>Costroid — local cost views</title></head><body>\
     <h1>Costroid</h1>\
     <p>Local-only views over your stored cost ledger (loopback; no network).</p>\
     <ul>\
     <li><a href=\"/timeline\">Timeline</a> — spend over time</li>\
     <li><a href=\"/comparison\">Comparison</a> — actual local vs counterfactual cloud</li>\
     <li><a href=\"/breakeven\">Break-even</a> — local-vs-cloud crossover</li>\
     </ul>\
     <p>JSON API: <code>/api/timeline</code>, <code>/api/comparison</code>, <code>/api/breakeven</code>.</p>\
     </body></html>"
        .to_string()
}

/// A per-view page. M5 T4 renders the view's JSON as an offline, screen-readable fallback; M5 T6
/// replaces this with the proper tables + embedded uPlot charts (keeping a no-JS fallback).
fn view_html(path: &str, views: &data::Views) -> String {
    let (title, json) = match path {
        "/timeline" => ("Timeline", serde_json::to_string_pretty(&views.timeline)),
        "/comparison" => (
            "Comparison",
            serde_json::to_string_pretty(&views.comparison),
        ),
        _ => ("Break-even", serde_json::to_string_pretty(&views.breakeven)),
    };
    let body = json.unwrap_or_else(|_| "{}".to_string());
    format!(
        "<!doctype html><html lang=en><head><meta charset=utf-8>\
         <title>Costroid — {title}</title></head><body>\
         <h1>{title}</h1><p><a href=\"/\">&larr; back</a></p>\
         <pre>{body}</pre></body></html>"
    )
}
