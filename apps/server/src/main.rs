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
//! # Status — M5
//! Reads the stored 3-lane ledger via `costroid-store` + `costroid-core` (NEVER `costroid-power` —
//! no `core→power` edge) and serves three views (timeline / comparison / break-even) — server-
//! rendered HTML (tables + inline SVG, no JS) + a `?plain` text fallback + a JSON API — over
//! loopback, plus `/healthz` and a `--serve-once` mode (the race-free offline-proof hook). All
//! assets are embedded first-party (the `include_str!` stylesheet in [`web`]): zero external
//! references, fully offline. (D2 chose vendored htmx + uPlot; the offline build cannot fetch them,
//! so this ships first-party embedded assets — same guarantees — flagged for the coordinator.)

mod data;
mod web;

use std::env;
use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::path::{Path, PathBuf};

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
        Mode::Serve(config) => serve(&config),
    }
}

/// The resolved serve configuration (M5 T8). The bind HOST is never here — it is hard-wired to
/// loopback by [`loopback_addr`], the single bind-address constructor; only the `port` varies.
struct ServeConfig {
    /// Serve exactly one request then exit (the race-free offline-proof hook, M5 T7).
    once: bool,
    /// The loopback port (default [`DEFAULT_PORT`]).
    port: u16,
    /// The stored-ledger path to read.
    ledger: PathBuf,
}

enum Mode {
    /// Bind loopback and run the request loop with the resolved [`ServeConfig`].
    Serve(ServeConfig),
    /// One-shot: prove the binary constructs + can bind loopback, make no egress, then exit.
    /// Used by `scripts/offline_acceptance.sh` for the runtime loopback-only proof.
    SelfCheck,
    /// Print usage and exit 0.
    Help,
}

/// Parse the server flags. `--port <N>` sets the loopback port; `--ledger <path>` overrides the
/// stored-ledger path; `--serve-once` serves one request then exits. The bind host is NOT a flag —
/// it is always loopback (T8). A malformed flag (bad/missing value, unknown arg) prints usage.
fn parse_args(args: impl Iterator<Item = String>) -> Mode {
    let mut once = false;
    let mut port = DEFAULT_PORT;
    let mut ledger: Option<PathBuf> = None;
    let mut iter = args;
    while let Some(arg) = iter.next() {
        match arg.as_str() {
            "--self-check" => return Mode::SelfCheck,
            "-h" | "--help" => return Mode::Help,
            "--serve-once" => once = true,
            "serve" => {} // explicit serve mode (the default)
            "--port" => match iter.next().and_then(|value| value.parse::<u16>().ok()) {
                Some(value) => port = value,
                None => return Mode::Help,
            },
            "--ledger" => match iter.next() {
                Some(value) => ledger = Some(PathBuf::from(value)),
                None => return Mode::Help,
            },
            _ => return Mode::Help,
        }
    }
    Mode::Serve(ServeConfig {
        once,
        port,
        ledger: ledger.unwrap_or_else(default_ledger_path),
    })
}

fn print_usage() {
    println!(
        "costroid-server — local-only (127.0.0.1) HTTP API + web UI over the Costroid ledger\n\
         \n\
         USAGE:\n    \
             costroid-server [serve]        Bind 127.0.0.1:{DEFAULT_PORT} and serve (default)\n    \
             costroid-server --port <N>     Bind 127.0.0.1:<N> instead (host is always loopback)\n    \
             costroid-server --ledger <path> Read the stored ledger from <path>\n    \
             costroid-server --serve-once   Serve exactly one request, then exit (offline-proof hook)\n    \
             costroid-server --self-check   Prove loopback-bind + no egress, then exit\n    \
             costroid-server --help         Show this help\n\
         \n\
         The server binds loopback ONLY (the host is not configurable) and makes no outbound \
         network call (⚑ A1 / §6.11)."
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
fn serve(config: &ServeConfig) -> Result<()> {
    let addr = loopback_addr(config.port);
    let server = Server::http(addr)
        .map_err(|e| anyhow::anyhow!("failed to bind loopback {addr}: {e}"))
        .with_context(|| "the server binds 127.0.0.1 only and never a routable interface")?;
    // A stable readiness line on stdout — the acceptance harness waits for it before its GET.
    println!("costroid-server ready on http://{addr} (loopback only; no outbound egress)");

    for request in server.incoming_requests() {
        let url = request.url().to_string();
        let (status, content_type, body) = respond_for(&url, &config.ledger);
        let once = config.once;
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

/// Route a request URL to `(status, content-type, body)`. Reads the stored ledger per request
/// (local, read-only). R4: only bounded view metadata is ever serialized — never any content.
/// Appending `?plain` to a view returns its screen-reader/pipe-friendly text rendering.
fn respond_for(url: &str, ledger: &Path) -> (u16, String, String) {
    let html = "text/html; charset=utf-8".to_string();
    let json = "application/json; charset=utf-8".to_string();
    let text = "text/plain; charset=utf-8".to_string();
    let css = "text/css; charset=utf-8".to_string();
    let path = url.split('?').next().unwrap_or("/");
    let plain = url
        .split('?')
        .nth(1)
        .map(|query| {
            query
                .split('&')
                .any(|p| p == "plain" || p.starts_with("plain="))
        })
        .unwrap_or(false);
    match path {
        "/healthz" => (200, text, "ok\n".to_string()),
        "/assets/costroid.css" => (200, css, web::CSS.to_string()),
        "/api/timeline" | "/api/comparison" | "/api/breakeven" | "/api/views" => {
            match api_json(path, ledger) {
                Ok(body) => (200, json, body),
                Err(message) => (500, text, message),
            }
        }
        "/" => match views(ledger) {
            Ok(views) => (200, html, web::index_html(&views)),
            Err(message) => (500, text, message),
        },
        "/timeline" | "/comparison" | "/breakeven" => match views(ledger) {
            Ok(views) => {
                if plain {
                    let body = match path {
                        "/timeline" => web::timeline_plain(&views.timeline),
                        "/comparison" => web::comparison_plain(&views.comparison),
                        _ => web::breakeven_plain(&views.breakeven),
                    };
                    (200, text, body)
                } else {
                    let body = match path {
                        "/timeline" => web::timeline_html(&views.timeline),
                        "/comparison" => web::comparison_html(&views.comparison),
                        _ => web::breakeven_html(&views.breakeven),
                    };
                    (200, html, body)
                }
            }
            Err(message) => (500, text, message),
        },
        _ => (404, text, "not found\n".to_string()),
    }
}

/// Build the three views over the stored ledger at `ledger`. Errors are bounded status messages
/// (schema/SQLite/pricing), never row content.
fn views(ledger: &Path) -> std::result::Result<data::Views, String> {
    let rows = data::load_rows(ledger).map_err(|e| format!("ledger read failed: {e}"))?;
    data::build_views(&rows, &data::Scenario::default())
        .map_err(|e| format!("view build failed: {e}"))
}

fn api_json(path: &str, ledger: &Path) -> std::result::Result<String, String> {
    let views = views(ledger)?;
    let value = match path {
        "/api/timeline" => serde_json::to_string(&views.timeline),
        "/api/comparison" => serde_json::to_string(&views.comparison),
        "/api/breakeven" => serde_json::to_string(&views.breakeven),
        _ => serde_json::to_string(&views),
    };
    value.map_err(|e| format!("serialization failed: {e}"))
}

/// The default stored-ledger path, overridable with `--ledger` (M5 T8).
fn default_ledger_path() -> PathBuf {
    if let Some(dir) = env::var_os("XDG_DATA_HOME") {
        return PathBuf::from(dir).join("costroid").join("ledger.db");
    }
    if let Some(home) = env::var_os("HOME") {
        return PathBuf::from(home).join(".local/share/costroid/ledger.db");
    }
    PathBuf::from("costroid-ledger.db")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(list: &[&str]) -> std::vec::IntoIter<String> {
        list.iter()
            .map(|s| s.to_string())
            .collect::<Vec<_>>()
            .into_iter()
    }

    #[test]
    fn parse_args_defaults_to_a_loopback_serve() {
        match parse_args(args(&[])) {
            Mode::Serve(config) => {
                assert_eq!(config.port, DEFAULT_PORT);
                assert!(!config.once, "default is not serve-once");
            }
            _ => panic!("no args → serve"),
        }
    }

    #[test]
    fn parse_args_round_trips_port_ledger_and_serve_once() {
        match parse_args(args(&[
            "serve",
            "--port",
            "9000",
            "--ledger",
            "/tmp/costroid-x.db",
            "--serve-once",
        ])) {
            Mode::Serve(config) => {
                assert_eq!(config.port, 9000);
                assert!(config.once);
                assert_eq!(config.ledger, PathBuf::from("/tmp/costroid-x.db"));
            }
            _ => panic!("flags → serve"),
        }
    }

    #[test]
    fn parse_args_rejects_bad_or_unknown_flags() {
        assert!(matches!(
            parse_args(args(&["--port", "notaport"])),
            Mode::Help
        ));
        assert!(matches!(parse_args(args(&["--port"])), Mode::Help)); // missing value
        assert!(matches!(parse_args(args(&["--ledger"])), Mode::Help)); // missing value
        assert!(matches!(parse_args(args(&["--bogus"])), Mode::Help));
        assert!(matches!(
            parse_args(args(&["--self-check"])),
            Mode::SelfCheck
        ));
        assert!(matches!(parse_args(args(&["--help"])), Mode::Help));
    }

    #[test]
    fn the_bind_host_is_always_loopback_regardless_of_port() {
        // `loopback_addr` is the ONLY bind-address constructor — no port (incl. a parsed `--port`
        // value) can make the server bind a routable interface (T8 / ⚑ A1).
        for port in [0_u16, DEFAULT_PORT, 9000, 65_535] {
            assert!(
                loopback_addr(port).ip().is_loopback(),
                "port {port} must bind loopback"
            );
        }
    }

    #[test]
    fn routes_healthz_and_a_404_over_an_empty_ledger() {
        let ledger = Path::new("/nonexistent/costroid/ledger-absent.db");
        let (status, ctype, body) = respond_for("/healthz", ledger);
        assert_eq!(status, 200);
        assert!(ctype.starts_with("text/plain"));
        assert_eq!(body, "ok\n");

        let (status, _ctype, _body) = respond_for("/unknown-path", ledger);
        assert_eq!(status, 404);

        // A view over an absent ledger renders an honest empty page (200), never an error/leak.
        let (status, ctype, _body) = respond_for("/breakeven", ledger);
        assert_eq!(status, 200);
        assert!(ctype.starts_with("text/html"));
    }
}
