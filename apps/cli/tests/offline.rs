//! Phase-1 offline guarantee — static proof.
//!
//! Costroid reads local logs only and must make no network calls and emit no
//! telemetry, ever. The strongest guarantee is structural: assert that the
//! resolved dependency graph contains no HTTP/TLS/socket client and no telemetry
//! SDK, so there is nothing in the shipped binary that *could* phone home.
//!
//! The dynamic counterpart — running every command under network isolation and
//! proving no outbound connection is attempted — lives in
//! `scripts/offline_acceptance.sh`. Together they make "no network, no telemetry"
//! airtight.

use std::collections::BTreeSet;
use std::process::Command;

/// Crates that would give Costroid the ability to make outbound network calls or
/// emit telemetry. None may appear in the resolved dependency graph in Phase 1.
///
/// Note: TLS via `rustls` is the project's approved choice for *later* phases
/// (Phase 2+ live quota / OAuth), so its presence here is a Phase-1 guard, not a
/// permanent ban — see `deny.toml` for the persistent supply-chain policy.
const FORBIDDEN_CRATES: &[&str] = &[
    // HTTP / networking clients & servers
    "reqwest",
    "hyper",
    "hyper-util",
    "h2",
    "ureq",
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
    // async runtimes that pull in network I/O
    "tokio",
    "async-std",
    "smol",
    "async-io",
    // TLS stacks (Phase 1 uses no TLS at all)
    "openssl",
    "openssl-sys",
    "native-tls",
    "rustls",
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

#[test]
fn no_networking_or_telemetry_crate_in_dependency_tree() {
    // `--locked` ties the check to the committed Cargo.lock, so it is fully
    // offline and deterministic.
    let output = match Command::new(env!("CARGO"))
        .args(["metadata", "--format-version", "1", "--locked"])
        .output()
    {
        Ok(output) => output,
        Err(err) => panic!("failed to run `cargo metadata`: {err}"),
    };
    assert!(
        output.status.success(),
        "`cargo metadata` failed:\n{}",
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
    let names: BTreeSet<&str> = packages
        .iter()
        .filter_map(|pkg| pkg["name"].as_str())
        .collect();

    let hits: Vec<&str> = FORBIDDEN_CRATES
        .iter()
        .copied()
        .filter(|crate_name| names.contains(crate_name))
        .collect();

    assert!(
        hits.is_empty(),
        "Phase 1 forbids networking/telemetry dependencies, but the resolved tree contains: {hits:?}.\n\
         Costroid must read local logs only and make no network calls. If a later phase \
         intentionally adds one of these, update this denylist and deny.toml together."
    );
}
