//! Offline guarantee — static proof (re-scoped for the connections subsystem, T7).
//!
//! Costroid's **default, local-only build reads local logs only and must make no
//! network call and emit no telemetry, ever.** The strongest guarantee is
//! structural: assert that the *resolved* dependency graph of Costroid's own crates
//! contains no HTTP/TLS/socket client and no telemetry SDK, so there is nothing in
//! the shipped binary that *could* phone home.
//!
//! Network code is allowed in exactly one place — the feature-gated `costroid-connect`
//! crate (PRODUCT-PLAN §2c / Step 4), **off by default**. So the guarantee is now
//! two-tier:
//!
//! * **Default build (`connect` off)** — forbids *everything* that can reach the
//!   network or emit telemetry, including the sanctioned trio (`ureq`/`rustls`/
//!   `keyring`). `costroid-connect` must not even be linked. This is the local-only
//!   product that ships today.
//! * **`--features connect`** — admits **only** the sanctioned trio that the
//!   connections subsystem will use; async runtimes, non-rustls TLS (OpenSSL), other
//!   HTTP clients, and *all* telemetry stay forbidden in this build too.
//!
//! Why the *resolved* graph and not `cargo metadata`'s `packages` array: `packages`
//! is a feature-independent superset (it lists optional dependencies whether or not
//! their feature is on), so it cannot tell the default build apart from the
//! `connect` build. Walking the resolved graph for an explicit feature set is what
//! makes the distinction real — a crate gated behind `connect` is absent here when
//! the feature is off and present when it is on.
//!
//! The dynamic counterpart — running every command under network isolation and
//! proving no outbound connection is attempted — lives in
//! `scripts/offline_acceptance.sh`. Together they make "no network, no telemetry"
//! airtight.

use std::collections::{BTreeSet, VecDeque};
use std::process::Command;

/// Crates that grant the ability to make outbound network calls or emit telemetry
/// and are **never** permitted in any Costroid build — not even inside
/// `costroid-connect`. They encode the project's standing choices: blocking `ureq`
/// (so no async runtime), `rustls` (so no OpenSSL), and zero telemetry.
const ALWAYS_FORBIDDEN_CRATES: &[&str] = &[
    // HTTP / networking clients & servers other than the sanctioned `ureq`
    "reqwest",
    "hyper",
    "hyper-util",
    "h2",
    "isahc",
    "surf",
    "attohttpc",
    "curl",
    "curl-sys",
    "tiny_http",
    "actix-web",
    "axum",
    "warp",
    "rouille",
    "minreq",
    // websocket / ssh clients — outbound channels no Costroid build may carry.
    // (Raw socket primitives like `socket2`/`mio` are deliberately NOT listed: `mio`
    // is in the legitimate tree via crossterm, and a primitive alone is not egress.)
    "tungstenite",
    "tokio-tungstenite",
    "websocket",
    "ssh2",
    "libssh2-sys",
    "russh",
    // async runtimes that pull in network I/O (Costroid's HTTP is blocking `ureq`)
    "tokio",
    "async-std",
    "smol",
    "async-io",
    // TLS stacks other than `rustls`
    "openssl",
    "openssl-sys",
    "native-tls",
    // DNS resolvers
    "trust-dns-resolver",
    "hickory-resolver",
    // telemetry / analytics / crash reporting
    "sentry",
    "sentry-core",
    "opentelemetry",
    "tracing-opentelemetry",
    "posthog",
    "posthog-rs",
    "segment",
    "amplitude",
    "mixpanel",
    "datadog-apm",
    "metrics-exporter-prometheus",
];

/// The sanctioned trio the connections subsystem (`costroid-connect`) is permitted
/// to link — and **only** it, **only** when the `connect` feature is on: `ureq`
/// (blocking HTTP), `rustls` (its TLS), and `keyring` (the OS keychain). Forbidden
/// in the default/local-only build; permitted once `connect` is enabled.
///
/// (T8 added `keyring` to `costroid-connect`, so it is present in the `connect` build
/// and asserted so below; `ureq` + `rustls` arrive with the HTTP client in T9. Either
/// way the default-build test must continue to NOT see any of the trio, and the
/// `connect`-build test must continue to permit them.)
const CONNECT_GATED_CRATES: &[&str] = &["ureq", "rustls", "keyring"];

/// The designated network/credential home: excluded as a graph *root* (it is the
/// thing being gated), but still reached as a *dependency* when `connect` is on.
const CONNECT_CRATE: &str = "costroid-connect";

/// The target triples Costroid ships (mirrors `deny.toml` `[graph].targets`). The
/// reachable graph is resolved once **per target** and unioned (see
/// [`reachable_crate_names`]).
const SHIPPED_TARGETS: &[&str] = &[
    "x86_64-unknown-linux-gnu",
    "aarch64-unknown-linux-gnu",
    "x86_64-unknown-linux-musl",
    "x86_64-apple-darwin",
    "aarch64-apple-darwin",
    "x86_64-pc-windows-msvc",
];

/// Names of every crate reachable in the **resolved** dependency graph from Costroid's
/// own crates — every workspace member except `costroid-connect` — under the given
/// extra cargo args (e.g. `--features connect`), unioned across every shipped target.
///
/// Resolving **per target** (`--filter-platform`) rather than via one unfiltered
/// `cargo metadata` is deliberate: the unfiltered resolve is an all-targets *superset*
/// that reports phantom optional dependencies a feature would prune — e.g. keyring's
/// unused `async-secret-service` path (`zbus`/`async-io`), which the sync
/// `dbus-secret-service` backend we select never links (confirmed with `cargo tree`).
/// Per-target resolution applies real feature+target pruning, so a phantom `async-io`
/// can't trip the ban; unioning all six triples still catches a network dependency
/// gated to any single platform. `--locked` ties the check to the committed
/// `Cargo.lock`, so it is fully offline and deterministic.
fn reachable_crate_names(extra_args: &[&str]) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    for target in SHIPPED_TARGETS {
        names.extend(reachable_for_target(target, extra_args));
    }
    names
}

/// The resolved reachable set for a single target triple (see [`reachable_crate_names`]).
fn reachable_for_target(target: &str, extra_args: &[&str]) -> BTreeSet<String> {
    let mut args = vec![
        "metadata",
        "--format-version",
        "1",
        "--locked",
        "--filter-platform",
        target,
    ];
    args.extend_from_slice(extra_args);

    let output = match Command::new(env!("CARGO")).args(&args).output() {
        Ok(output) => output,
        Err(err) => panic!(
            "failed to run `cargo metadata --filter-platform {target} {extra_args:?}`: {err}"
        ),
    };
    assert!(
        output.status.success(),
        "`cargo metadata --filter-platform {target} {extra_args:?}` failed:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );

    let meta: serde_json::Value = match serde_json::from_slice(&output.stdout) {
        Ok(value) => value,
        Err(err) => panic!("`cargo metadata` emitted invalid JSON: {err}"),
    };

    let packages = match meta["packages"].as_array() {
        Some(packages) => packages,
        None => panic!("`cargo metadata` output had no `packages` array"),
    };
    let id_to_name: std::collections::HashMap<&str, &str> = packages
        .iter()
        .filter_map(|pkg| Some((pkg["id"].as_str()?, pkg["name"].as_str()?)))
        .collect();

    let resolve = &meta["resolve"];
    let nodes = match resolve["nodes"].as_array() {
        Some(nodes) => nodes,
        None => panic!("`cargo metadata` output had no `resolve.nodes` array"),
    };
    // id -> resolved dependency ids (feature-pruned by the current selection).
    let mut edges: std::collections::HashMap<&str, Vec<&str>> = std::collections::HashMap::new();
    for node in nodes {
        let Some(id) = node["id"].as_str() else {
            continue;
        };
        let deps: Vec<&str> = node["deps"]
            .as_array()
            .map(|deps| deps.iter().filter_map(|d| d["pkg"].as_str()).collect())
            .unwrap_or_default();
        edges.insert(id, deps);
    }

    // Roots: every workspace member except the gated network home.
    let members = match meta["workspace_members"].as_array() {
        Some(members) => members,
        None => panic!("`cargo metadata` output had no `workspace_members` array"),
    };
    let mut queue: VecDeque<&str> = VecDeque::new();
    let mut visited: BTreeSet<&str> = BTreeSet::new();
    for member in members {
        let Some(id) = member.as_str() else {
            continue;
        };
        if id_to_name.get(id).copied() != Some(CONNECT_CRATE) && visited.insert(id) {
            queue.push_back(id);
        }
    }

    // Breadth-first over the resolved edges (all kinds: normal, build, and dev — so
    // a test-only network dependency would be caught too).
    while let Some(id) = queue.pop_front() {
        if let Some(deps) = edges.get(id) {
            for &dep in deps {
                if visited.insert(dep) {
                    queue.push_back(dep);
                }
            }
        }
    }

    visited
        .iter()
        .filter_map(|id| id_to_name.get(id).map(|name| name.to_string()))
        .collect()
}

/// Default/local-only build: the gate is **off**, so `costroid-connect` must not be
/// linked and the graph must contain no networking, TLS, or telemetry crate at all
/// (the sanctioned trio included).
#[test]
fn default_build_links_no_network_tls_or_telemetry_crate() {
    let names = reachable_crate_names(&[]);

    assert!(
        !names.contains(CONNECT_CRATE),
        "the default build must not link `{CONNECT_CRATE}` — the `connect` feature \
         must be off by default so the local-only build links no network/keychain code."
    );

    let hits: Vec<&str> = ALWAYS_FORBIDDEN_CRATES
        .iter()
        .chain(CONNECT_GATED_CRATES.iter())
        .copied()
        .filter(|crate_name| names.contains(*crate_name))
        .collect();
    assert!(
        hits.is_empty(),
        "the default/local-only build forbids networking/TLS/telemetry dependencies, \
         but the resolved graph contains: {hits:?}.\n\
         Costroid must read local logs only and make no network call. Network code \
         belongs solely in `costroid-connect`, behind the off-by-default `connect` \
         feature — if a crate must move there, update CONNECT_GATED_CRATES, deny.toml, \
         and the `connect`-build test together."
    );
}

/// `--features connect`: the connections subsystem is linked, so the sanctioned trio
/// (`ureq`/`rustls`/`keyring`) is permitted — but async runtimes, OpenSSL, other HTTP
/// clients, and *all* telemetry stay forbidden even here.
#[test]
fn connect_feature_admits_only_the_sanctioned_trio() {
    let names = reachable_crate_names(&["--features", "connect"]);

    assert!(
        names.contains(CONNECT_CRATE),
        "`--features connect` must link `{CONNECT_CRATE}` — otherwise the gate is not \
         actually wired to the connections subsystem."
    );

    let hits: Vec<&str> = ALWAYS_FORBIDDEN_CRATES
        .iter()
        .copied()
        .filter(|crate_name| names.contains(*crate_name))
        .collect();
    assert!(
        hits.is_empty(),
        "even with `connect` on, Costroid forbids async runtimes, non-rustls TLS \
         (OpenSSL), other HTTP clients, and all telemetry, but the resolved graph \
         contains: {hits:?}.\n\
         The connections subsystem uses only `ureq` + `rustls` + `keyring`."
    );
    // T8 landed the keychain: assert `keyring` is now actually linked under `connect`
    // (the gate must really pull in the OS-keychain backend the credential store uses).
    // `ureq` + `rustls` arrive with the HTTP client in T9 — permitted here, not yet
    // asserted present.
    assert!(
        names.contains("keyring"),
        "`--features connect` must link `keyring` — T8's credential store (the OS \
         keychain) depends on it, so the gate has to pull it in."
    );
}
