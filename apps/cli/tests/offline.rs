//! Offline guarantee — static proof, **per-binary** (re-scoped for the connections
//! subsystem in T7; split per-binary in T21 for the AccessKit carve-out).
//!
//! Costroid's **default, local-only build reads local logs only and must make no
//! network call and emit no telemetry, ever.** The strongest guarantee is
//! structural: assert that the *resolved* dependency graph of Costroid's own crates
//! contains no HTTP/TLS/socket client and no telemetry SDK, so there is nothing in
//! the shipped binary that *could* phone home.
//!
//! Costroid ships **two binaries** whose graphs differ, so the guarantee is resolved
//! **per binary** (the BFS roots at one package, not the whole workspace):
//!
//! * **`costroid` (the CLI)** — the local-only product. Its graph forbids *everything*
//!   that can reach the network or emit telemetry, including the sanctioned trio
//!   (`ureq`/`rustls`/`keyring`) and **`async-io`** — there is no async runtime in the
//!   CLI. `--features connect` then admits **only** the sanctioned trio. This is the
//!   byte-for-byte guarantee that has held since T7; turning the bar's AccessKit on
//!   (T21) does NOT touch it (the bar's local-IPC subtree is unreachable from the CLI).
//! * **`costroid-bar` (the taskbar)** — a GUI binary. egui's AccessKit backend
//!   (`accesskit_unix → zbus → async-io`) is **local AT-SPI/D-Bus IPC, not network**,
//!   but `async-io` is in [`ALWAYS_FORBIDDEN_CRATES`]. T21 admits exactly that reviewed
//!   local-IPC subtree for the bar via [`BAR_ACCESSKIT_ALLOWED`] — and **every**
//!   network/TLS/telemetry crate stays forbidden even here. The static name-ban is
//!   backed by a *runtime* no-`AF_INET` proof for the `costroid-bar` binary in
//!   `scripts/offline_acceptance.sh`, turning the allowlist into a behavioral
//!   no-network guarantee for the AccessKit subtree.
//!
//! Why the *resolved* graph and not `cargo metadata`'s `packages` array: `packages`
//! is a feature-independent superset (it lists optional dependencies whether or not
//! their feature is on), so it cannot tell the default build apart from the
//! `connect`/`accesskit` build. Walking the resolved graph for an explicit feature set
//! is what makes the distinction real — a crate gated behind `connect` is absent when
//! the feature is off and present when it is on, and the accesskit subtree is present
//! only when the bar's (default) `accesskit` feature is enabled.
//!
//! The dynamic counterpart — running every command (and the bar) under network
//! isolation and proving no outbound connection is attempted — lives in
//! `scripts/offline_acceptance.sh`. Together they make "no network, no telemetry"
//! airtight for both binaries.

use std::collections::{BTreeSet, VecDeque};
use std::process::Command;

/// Crates that grant the ability to make outbound network calls or emit telemetry
/// and are **never** permitted in the CLI build — not even inside `costroid-connect`.
/// They encode the project's standing choices: blocking `ureq` (so no async runtime),
/// `rustls` (so no OpenSSL), and zero telemetry. (The bar admits exactly the reviewed
/// `async-io` local-IPC subtree — [`BAR_ACCESSKIT_ALLOWED`] — and nothing else here.)
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
    // async runtimes that pull in network I/O (Costroid's HTTP is blocking `ureq`).
    // `async-io` is banned in the CLI; the bar admits it ONLY as part of the reviewed
    // AccessKit local-IPC subtree (BAR_ACCESSKIT_ALLOWED) + the runtime no-AF_INET proof.
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
/// (T8 added `keyring` — the credential store; T9a added `ureq` + `rustls` — the
/// generic authorized-host HTTP client. The full trio is now present in the
/// `connect` build and asserted so below; the default-build test must continue to
/// NOT see any of it.)
const CONNECT_GATED_CRATES: &[&str] = &["ureq", "rustls", "keyring"];

/// The designated network/credential home: excluded as a graph *root* (it is the
/// thing being gated), but still reached as a *dependency* when `connect` is on.
const CONNECT_CRATE: &str = "costroid-connect";

/// The CLI binary package — the local-only product. Its reachable graph is the
/// byte-for-byte async-io/network/telemetry-free guarantee.
const CLI_CRATE: &str = "costroid";

/// The taskbar binary package — the GUI that admits the reviewed AccessKit local-IPC
/// subtree ([`BAR_ACCESSKIT_ALLOWED`]) and nothing else network-shaped.
const BAR_CRATE: &str = "costroid-bar";

/// The local HTTP API + web-UI binary package (⚑ Readiness gate A1, §6.11). It admits the
/// reviewed local-listen subtree ([`SERVER_ALLOWED`], in practice just `tiny_http` + its
/// transitives) and nothing else network-shaped. It is NEVER linked into `costroid`/`costroid-bar`
/// — the CLI/bar graphs are rooted elsewhere, so `tiny_http` cannot pollute them.
const SERVER_CRATE: &str = "costroid-server";

/// Every crate `--features connect` legitimately adds to the resolved graph over the
/// default build (rooted at the SAME binary), unioned across all shipped targets
/// (`costroid-connect` itself excluded — it is the gated home). This is an
/// **allowlist**, not a denylist: the `connect` test asserts the *real* connect-delta
/// is a **subset** of it, so a future dependency bump that pulls a NEW crate (a
/// socket/TLS/telemetry crate under an unlisted name, say) fails the gate until a human
/// reviews it and adds it here. That converts "we ban the network crates we thought of"
/// into "nothing new reaches the network-on build unreviewed".
///
/// It includes the zbus / async-`secret-service` ecosystem because the per-target metadata
/// union reaches it (via dev/build deps + the cross-target union); only `dbus-secret-service`
/// (the **sync** backend) actually links into the shipped binary (`cargo tree -e normal`),
/// and the async *runtimes* (`tokio`/`async-io`/`async-std`/`smol`) stay independently and
/// unconditionally banned by [`ALWAYS_FORBIDDEN_CRATES`] above — so allowlisting the async
/// *plumbing* here cannot let a runtime slip in.
///
/// It is the **union** of both binaries' connect-deltas (`costroid` and `costroid-bar`): the
/// T21 per-binary split shrank each reference graph, so support crates the old whole-workspace
/// default already carried (the RustCrypto primitives `sha2`/`digest`/`block-buffer`/
/// `crypto-common`/`generic-array`/`typenum`/`subtle`/`cpufeatures` for the encrypted
/// secret-service session, `base64`, `nix` syscall bindings, `jobserver` for `cc`) now show up
/// in the delta where they were previously masked. Each was reviewed: NONE is a
/// network/TLS/telemetry path. Regenerate after a deliberate dependency change with the
/// `#[ignore]` `print_connect_delta` test:
/// `cargo test -p costroid --test offline print_connect_delta -- --ignored --nocapture`.
const CONNECT_ALLOWED: &[&str] = &[
    "aes",
    "async-broadcast",
    "async-trait",
    "base64",
    "block-buffer",
    "block-padding",
    "byteorder",
    "cbc",
    "cc",
    "cipher",
    "concurrent-queue",
    "core-foundation",
    "cpufeatures",
    "crossbeam-utils",
    "crypto-common",
    "dbus",
    "dbus-secret-service",
    "digest",
    "endi",
    "enumflags2",
    "enumflags2_derive",
    "event-listener",
    "event-listener-strategy",
    "find-msvc-tools",
    "futures-core",
    "futures-macro",
    "futures-sink",
    "futures-task",
    "futures-util",
    "generic-array",
    "hkdf",
    "hmac",
    "http",
    "httparse",
    "inout",
    "jobserver",
    "keyring",
    "libdbus-sys",
    "nix",
    "num",
    "num-bigint",
    "num-complex",
    "num-integer",
    "num-iter",
    "num-rational",
    "openssl-probe",
    "ordered-stream",
    "parking",
    "percent-encoding",
    "pin-project-lite",
    "pkg-config",
    "ring",
    "rpassword",
    "rtoolbox",
    "rustls",
    "rustls-native-certs",
    "rustls-pki-types",
    "rustls-webpki",
    "schannel",
    "secrecy",
    "secret-service",
    "security-framework",
    "security-framework-sys",
    "serde_repr",
    "sha1",
    "sha2",
    "shlex",
    "slab",
    "subtle",
    "tracing",
    "tracing-attributes",
    "tracing-core",
    "typenum",
    "untrusted",
    "ureq",
    "ureq-proto",
    "utf8-zero",
    "windows-targets",
    "windows_x86_64_msvc",
    "xdg-home",
    "zbus",
    "zbus_macros",
    "zbus_names",
    "zeroize",
    "zeroize_derive",
    "zvariant",
    "zvariant_derive",
    "zvariant_utils",
];

/// Every crate the bar's (default) `accesskit` feature adds to the `costroid-bar` graph
/// over the same build with the feature off, unioned across all shipped targets. This is
/// the reviewed **AccessKit local-IPC subtree** — egui's `accesskit_winit` →
/// `accesskit_unix → zbus → async-io` (Linux AT-SPI/D-Bus), `accesskit_windows` →
/// `windows-*` (UI Automation), `accesskit_macos` (NSAccessibility) — none of which is
/// network egress, and the lone [`ALWAYS_FORBIDDEN_CRATES`] member among them, `async-io`,
/// is a local epoll/kqueue reactor used here only for the D-Bus AF_UNIX socket (proven at
/// RUNTIME by `scripts/offline_acceptance.sh`'s no-`AF_INET` check).
///
/// It is a **subset-allowlist**, exactly like [`CONNECT_ALLOWED`]: the bar test asserts the
/// real accesskit delta is a SUBSET of it, so a future egui/accesskit bump that pulls a NEW
/// crate (an HTTP/TLS/telemetry path under an unlisted name) fails the gate until a human
/// reviews it. Every genuine network/TLS/telemetry crate is absent from this list, so one
/// slipping into the subtree trips the gate. Regenerate after a deliberate dependency change
/// with the `#[ignore]` `print_bar_accesskit_delta` test:
/// `cargo test -p costroid --test offline print_bar_accesskit_delta -- --ignored --nocapture`.
const BAR_ACCESSKIT_ALLOWED: &[&str] = &[
    "accesskit_atspi_common",
    "accesskit_consumer",
    "accesskit_macos",
    "accesskit_unix",
    "accesskit_windows",
    "accesskit_winit",
    "async-broadcast",
    "async-channel",
    "async-executor",
    "async-io",
    "async-lock",
    "async-process",
    "async-recursion",
    "async-signal",
    "async-task",
    "async-trait",
    "atomic-waker",
    "atspi",
    "atspi-common",
    "atspi-proxies",
    "blocking",
    "concurrent-queue",
    "endi",
    "enumflags2",
    "enumflags2_derive",
    "event-listener",
    "event-listener-strategy",
    "fastrand",
    "futures-lite",
    "hex",
    "ordered-stream",
    "parking",
    "phf",
    "phf_generator",
    "phf_macros",
    "phf_shared",
    "piper",
    "serde_repr",
    "signal-hook-registry",
    "siphasher",
    "windows",
    "windows-collections",
    "windows-core",
    "windows-future",
    "windows-implement",
    "windows-interface",
    "windows-numerics",
    "windows-result",
    "windows-strings",
    "windows-threading",
    "zbus",
    "zbus-lockstep",
    "zbus-lockstep-macros",
    "zbus_macros",
    "zbus_names",
    "zbus_xml",
    "zvariant",
    "zvariant_derive",
    "zvariant_utils",
];

/// Every crate the local HTTP/web-UI binary (`costroid-server`) adds to its resolved graph over
/// the default CLI build, unioned across all shipped targets (`costroid-server` itself excluded —
/// it is the root). This is the reviewed **local-listen subtree**: `tiny_http` (the blocking,
/// loopback-bound HTTP *server*) and its three pure-Rust transitives (`ascii`, `chunked_transfer`,
/// `httpdate`).
///
/// `tiny_http` is in [`ALWAYS_FORBIDDEN_CRATES`] because the CLI/bar must carry no HTTP server;
/// it is admitted **only here**, for the server binary, and it is an **inbound** local listener —
/// NOT an outbound client. The server binds `127.0.0.1` only and makes no `connect()` egress; that
/// behavior is proven at RUNTIME by the loopback-only check in `scripts/offline_acceptance.sh`
/// (allow a loopback bind; forbid any non-loopback bind/connect), turning this static allowlist
/// into a behavioral no-egress guarantee.
///
/// It is a **subset-allowlist**, exactly like [`CONNECT_ALLOWED`] / [`BAR_ACCESSKIT_ALLOWED`]: the
/// server test asserts the real server-delta is a SUBSET of it, so a future dependency bump that
/// pulls a NEW crate (an outbound HTTP/TLS/telemetry path, or a `tiny_http` feature that adds an
/// SSL/socket crate) fails the gate until a human reviews it. Every genuine outbound
/// network/TLS/telemetry crate is absent from this list. Regenerate after a deliberate dependency
/// change with the `#[ignore]` `print_server_delta` test:
/// `cargo test -p costroid --test offline print_server_delta -- --ignored --nocapture`.
const SERVER_ALLOWED: &[&str] = &["ascii", "chunked_transfer", "httpdate", "tiny_http"];

/// Every crate the CLI's off-by-default `store` feature adds to its resolved graph over the
/// default CLI build, unioned across all shipped targets (`costroid-store` is the gated home —
/// it appears in the delta and is allowlisted here, mirroring how `costroid-server` is the
/// server root). This is the reviewed **local-SQLite subtree**: `rusqlite` (the pure-local
/// SQLite binding), its `bundled` C-amalgamation build (`libsqlite3-sys` + the `cc` build
/// toolchain: `cc`/`jobserver`/`shlex`/`find-msvc-tools` on Linux, `pkg-config`/`vcpkg` for
/// system-lib discovery on other targets), and rusqlite's small pure-Rust support crates
/// (`hashlink`, `fallible-iterator`, `fallible-streaming-iterator`).
///
/// Every entry is a LOCAL crate: SQLite is an embedded, in-process database engine — it links
/// NO network/TLS/telemetry/async-runtime crate. "Linking rusqlite ≠ a network call" exactly
/// as "linking the HTTP client ≠ a call" for `connect`; the runtime no-`AF_INET` proof for the
/// store-linked build lives in `scripts/offline_acceptance.sh`, turning this static allowlist
/// into a behavioral no-network guarantee for the SQLite subtree.
///
/// `cc` / `pkg-config` / `find-msvc-tools` / `jobserver` / `shlex` are ALSO members of
/// [`CONNECT_ALLOWED`] (the `cc` build toolchain is shared plumbing), but `STORE_ALLOWED` is its
/// own independent reviewed list — the store delta is computed against the default build, not
/// the connect build, so the overlap is incidental, not a dependency.
///
/// It is a **subset-allowlist**, exactly like [`CONNECT_ALLOWED`] / [`SERVER_ALLOWED`] /
/// [`BAR_ACCESSKIT_ALLOWED`]: the store test asserts the real store-delta is a SUBSET of it, so a
/// future dependency bump that pulls a NEW crate (a socket/TLS/telemetry path, or a rusqlite
/// feature that adds one) fails the gate until a human reviews it. Every genuine
/// network/TLS/telemetry crate is absent from this list. Regenerate after a deliberate dependency
/// change with the `#[ignore]` `print_store_delta` test:
/// `cargo test -p costroid --test offline print_store_delta -- --ignored --nocapture`.
const STORE_ALLOWED: &[&str] = &[
    "cc",             // also in CONNECT_ALLOWED — the `cc` build toolchain (shared plumbing).
    "costroid-store", // the store crate itself (the gated home, like costroid-server is its root).
    "fallible-iterator",
    "fallible-streaming-iterator",
    "find-msvc-tools", // also in CONNECT_ALLOWED — `cc`'s MSVC toolchain locator.
    "hashlink",
    "jobserver",      // also in CONNECT_ALLOWED — `cc`'s parallel-build jobserver.
    "libsqlite3-sys", // the bundled SQLite C amalgamation binding.
    "pkg-config",     // also in CONNECT_ALLOWED — system-lib discovery (non-bundled targets).
    "rusqlite",       // the local SQLite binding (load-bearing — the store's whole point).
    "shlex",          // also in CONNECT_ALLOWED — `cc` build-flag splitting.
    "vcpkg",          // Windows system-lib discovery for `libsqlite3-sys`.
];

/// The reviewed crate(s) `--features power` (CLI) is allowed to ADD over the default build (M3).
///
/// `costroid-power` is the local-inference economics engine — a pure-compute leaf: its only deps
/// (`serde`, `serde_json`, `thiserror`) are already in the default CLI graph, so the power-on
/// delta is exactly the crate itself. The runner is a `std::process` subprocess and the sysfs
/// read is `std::fs` (no crate), so the power build links NO network/TLS/telemetry/async-runtime
/// crate; the runtime no-`AF_INET` proof for the power-on build is the offline-acceptance script.
///
/// A **subset-allowlist** like the others: if a future dependency bump pulls a NEW crate into the
/// power graph, this gate trips until a human reviews it. Regenerate with the `#[ignore]`
/// `print_power_delta` test:
/// `cargo test -p costroid --test offline print_power_delta -- --ignored --nocapture`.
const POWER_ALLOWED: &[&str] = &[
    "costroid-power", // the local-inference engine crate itself (the gated home).
];

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

/// Names of every crate reachable in the **resolved** dependency graph from the given
/// root package(s), under the given extra cargo args (e.g. `--features connect` or
/// `--no-default-features`), unioned across every shipped target.
///
/// Rooting at one named binary (rather than every workspace member) is what makes the
/// guarantee **per-binary**: the CLI graph is resolved from `costroid` alone (so the bar's
/// AccessKit local-IPC subtree is unreachable and cannot pollute it), and the bar graph
/// from `costroid-bar` alone.
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
fn reachable_crate_names(roots: &[&str], extra_args: &[&str]) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    for target in SHIPPED_TARGETS {
        names.extend(reachable_for_target(target, roots, extra_args));
    }
    names
}

/// The resolved reachable set for a single target triple (see [`reachable_crate_names`]).
fn reachable_for_target(target: &str, roots: &[&str], extra_args: &[&str]) -> BTreeSet<String> {
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

    // Roots: the workspace members whose package name is in `roots`.
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
        let is_root = id_to_name
            .get(id)
            .copied()
            .is_some_and(|name| roots.contains(&name));
        if is_root && visited.insert(id) {
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

/// Forbidden crates (from `forbidden`) that are present in `names` and NOT excused by the
/// reviewed `allow` list — the load-bearing assertion both binaries share.
fn forbidden_hits<'a>(
    names: &BTreeSet<String>,
    forbidden: impl IntoIterator<Item = &'a str>,
    allow: &[&str],
) -> Vec<&'a str> {
    let allowed: BTreeSet<&str> = allow.iter().copied().collect();
    forbidden
        .into_iter()
        .filter(|crate_name| names.contains(*crate_name) && !allowed.contains(crate_name))
        .collect()
}

// =====================================================================================
// CLI binary (`costroid`) — the local-only product, byte-for-byte async-io/network-free
// =====================================================================================

/// Default CLI build: the gate is **off**, so `costroid-connect` must not be linked and
/// the graph must contain no networking, TLS, async-runtime, or telemetry crate at all
/// (the sanctioned trio AND `async-io` included — there is no async runtime in the CLI).
#[test]
fn cli_default_build_links_no_network_tls_or_telemetry_crate() {
    let names = reachable_crate_names(&[CLI_CRATE], &[]);

    assert!(
        !names.contains(CONNECT_CRATE),
        "the default CLI build must not link `{CONNECT_CRATE}` — the `connect` feature \
         must be off by default so the local-only build links no network/keychain code."
    );

    // No allowlist for the CLI: every forbidden crate (incl. `async-io`) must be absent.
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES
            .iter()
            .chain(CONNECT_GATED_CRATES.iter())
            .copied(),
        &[],
    );
    assert!(
        hits.is_empty(),
        "the default/local-only CLI build forbids networking/TLS/async-runtime/telemetry \
         dependencies, but the resolved graph contains: {hits:?}.\n\
         Costroid's CLI must read local logs only and make no network call. Network code \
         belongs solely in `costroid-connect`, behind the off-by-default `connect` \
         feature — if a crate must move there, update CONNECT_GATED_CRATES, deny.toml, \
         and the `connect`-build test together."
    );
}

/// `--features connect` (CLI): the connections subsystem is linked, so the sanctioned trio
/// (`ureq`/`rustls`/`keyring`) is permitted — but async runtimes, OpenSSL, other HTTP
/// clients, and *all* telemetry stay forbidden even here.
#[test]
fn cli_connect_feature_admits_only_the_sanctioned_trio() {
    let names = reachable_crate_names(&[CLI_CRATE], &["--features", "connect"]);

    assert!(
        names.contains(CONNECT_CRATE),
        "`--features connect` must link `{CONNECT_CRATE}` — otherwise the gate is not \
         actually wired to the connections subsystem."
    );

    let hits = forbidden_hits(&names, ALWAYS_FORBIDDEN_CRATES.iter().copied(), &[]);
    assert!(
        hits.is_empty(),
        "even with `connect` on, the CLI forbids async runtimes, non-rustls TLS (OpenSSL), \
         other HTTP clients, and all telemetry, but the resolved graph contains: {hits:?}.\n\
         The connections subsystem uses only `ureq` + `rustls` + `keyring`."
    );
    // T8 landed the keychain (`keyring`) and T9a the HTTP client (`ureq` + `rustls`):
    // assert the whole sanctioned trio is now actually linked under `connect` — the
    // gate must really pull in the OS-keychain backend the credential store uses and
    // the blocking HTTP/TLS stack the authorized-host client uses.
    for gated in CONNECT_GATED_CRATES {
        assert!(
            names.contains(*gated),
            "`--features connect` must link `{gated}` — the connections subsystem \
             (T8 credential store, T9a authorized-host HTTP client) depends on the \
             full `ureq`/`rustls`/`keyring` trio, so the gate has to pull it in."
        );
    }

    // Subset-allowlist: bound exactly what `connect` *adds* over the default CLI build, so a
    // future dependency bump that introduces a NEW crate (a socket/TLS/telemetry path, or
    // anything else) trips this gate for a human to review — rather than silently slipping
    // past the name-denylist above. `CONNECT_ALLOWED` is the reviewed connect-delta.
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let allowed: BTreeSet<&str> = CONNECT_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&default)
        .map(String::as_str)
        .filter(|name| *name != CONNECT_CRATE && !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "`--features connect` introduced crate(s) not in the reviewed allowlist: \
         {unexpected:?}.\n\
         Every crate the connect-on graph adds must be reviewed (is it a network / TLS / \
         telemetry path?) and, if legitimate, added to CONNECT_ALLOWED. Regenerate the \
         expected delta with: \
         `cargo test -p costroid --test offline print_connect_delta -- --ignored --nocapture`."
    );
}

/// `--features store` (CLI): the off-by-default local-SQLite store is linked, so the reviewed
/// local-SQLite subtree (`rusqlite` + bundled SQLite + `cc` build toolchain + rusqlite's
/// pure-Rust support crates — [`STORE_ALLOWED`]) is permitted. SQLite is an embedded, in-process
/// engine: the store links NO network/TLS/telemetry/async-runtime crate, so EVERY
/// [`ALWAYS_FORBIDDEN_CRATES`] and [`CONNECT_GATED_CRATES`] member stays forbidden even here. The
/// store-delta over the default CLI build is asserted to be a SUBSET of `STORE_ALLOWED`
/// (fail-closed: a future dep bump pulling a NEW crate trips it).
#[test]
fn cli_store_feature_admits_only_store_allowed() {
    let names = reachable_crate_names(&[CLI_CRATE], &["--features", "store"]);

    // The store must NOT pull in the network/credential home — it is a pure local-SQLite leaf.
    assert!(
        !names.contains(CONNECT_CRATE),
        "`--features store` must not link `{CONNECT_CRATE}` — the store is a pure local-SQLite \
         leaf (no network/keychain code); it shares no graph with the connections subsystem."
    );

    // The store links NO network/TLS/telemetry/async-runtime crate — rusqlite is pure local
    // SQLite. So NO ALWAYS_FORBIDDEN_CRATES / CONNECT_GATED_CRATES member may appear at all
    // (no allowlist excuse, exactly like the default CLI build): "linking rusqlite ≠ network".
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES
            .iter()
            .chain(CONNECT_GATED_CRATES.iter())
            .copied(),
        &[],
    );
    assert!(
        hits.is_empty(),
        "the `--features store` build forbids networking/TLS/async-runtime/telemetry crates \
         (the local-SQLite store links none — rusqlite is an embedded in-process engine), but \
         the resolved graph contains: {hits:?}.\n\
         If a crate is a legitimate part of the local-SQLite subtree, it still must NOT be a \
         network/TLS/telemetry path; otherwise it is a real egress path and must be removed."
    );

    // Positive check: `rusqlite` really is linked under `--features store`, so the STORE_ALLOWED
    // carve-out is load-bearing and not vacuous (mirrors the connect-trio / tiny_http positives).
    assert!(
        names.contains("rusqlite"),
        "`--features store` must link `rusqlite` — otherwise the STORE_ALLOWED carve-out is \
         vacuous (the store needs its local SQLite binding)."
    );
    assert!(
        names.contains("costroid-store"),
        "`--features store` must link `costroid-store` — otherwise the gate is not actually \
         wired to the store subsystem."
    );

    // Subset-allowlist: bound exactly what `store` ADDS over the default CLI build, so a future
    // dependency bump that introduces a NEW crate (a socket/TLS/telemetry path, or a rusqlite
    // feature that pulls one) trips this gate for a human to review.
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let allowed: BTreeSet<&str> = STORE_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&default)
        .map(String::as_str)
        .filter(|name| !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "`--features store` introduced crate(s) not in the reviewed allowlist: {unexpected:?}.\n\
         Every crate the store-on graph adds must be reviewed (is it a network / TLS / telemetry \
         path?) and, if legitimate, added to STORE_ALLOWED. Regenerate the expected delta with: \
         `cargo test -p costroid --test offline print_store_delta -- --ignored --nocapture`."
    );
}

/// The default CLI build is store-free: neither `costroid-store` nor `rusqlite` (nor the
/// bundled-SQLite `libsqlite3-sys`) is linked when the off-by-default `store` feature is off.
/// This is the mirror of the connect-off assertion — the store stays out of the default,
/// local-only graph entirely.
#[test]
fn cli_default_build_is_store_free() {
    let names = reachable_crate_names(&[CLI_CRATE], &[]);
    for absent in ["costroid-store", "rusqlite", "libsqlite3-sys"] {
        assert!(
            !names.contains(absent),
            "the default CLI build must not link `{absent}` — the `store` feature must be off by \
             default so the local-only build carries no SQLite store."
        );
    }
}

/// `--features power` (CLI, M3): the off-by-default local-inference engine (`costroid bench`) is
/// linked, so the reviewed `costroid-power` subtree ([`POWER_ALLOWED`]) is permitted. It is a
/// pure-compute leaf — the runner is a `std::process` subprocess (A2, not FFI/HTTP) and the
/// sysfs read is `std::fs` — so EVERY [`ALWAYS_FORBIDDEN_CRATES`] / [`CONNECT_GATED_CRATES`]
/// member stays forbidden even here (no allowlist excuse), and the power-delta is asserted to be
/// a SUBSET of `POWER_ALLOWED` (fail-closed).
#[test]
fn cli_power_feature_admits_only_power_allowed() {
    let names = reachable_crate_names(&[CLI_CRATE], &["--features", "power"]);

    // The engine must NOT pull the network/credential home — it is a pure local-compute leaf.
    assert!(
        !names.contains(CONNECT_CRATE),
        "`--features power` must not link `{CONNECT_CRATE}` — the local-inference engine carries \
         no network/keychain code (the runner is a std::process subprocess, not the HTTP API)."
    );

    // The power build links NO network/TLS/telemetry/async-runtime crate.
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES
            .iter()
            .chain(CONNECT_GATED_CRATES.iter())
            .copied(),
        &[],
    );
    assert!(
        hits.is_empty(),
        "the `--features power` build forbids networking/TLS/async-runtime/telemetry crates \
         (the local-inference engine links none — the runner is a subprocess, the sysfs read is \
         std::fs), but the resolved graph contains: {hits:?}."
    );

    // Positive check: `costroid-power` really is linked under `--features power`, so the
    // POWER_ALLOWED carve-out is load-bearing and not vacuous.
    assert!(
        names.contains("costroid-power"),
        "`--features power` must link `costroid-power` — otherwise the gate is not wired to the \
         local-inference engine."
    );

    // Subset-allowlist: bound exactly what `power` ADDS over the default CLI build, so a future
    // dependency bump that introduces a NEW crate trips this gate for a human to review.
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let allowed: BTreeSet<&str> = POWER_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&default)
        .map(String::as_str)
        .filter(|name| !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "`--features power` introduced crate(s) not in the reviewed allowlist: {unexpected:?}.\n\
         Every crate the power-on graph adds must be reviewed (is it a network / TLS / telemetry \
         path?) and, if legitimate, added to POWER_ALLOWED. Regenerate the expected delta with: \
         `cargo test -p costroid --test offline print_power_delta -- --ignored --nocapture`."
    );
}

/// The default CLI build is power-free: `costroid-power` is not linked when the off-by-default
/// `power` feature is off, so the default/local-only build carries no local-inference engine and
/// the `costroid bench` subcommand does not exist.
#[test]
fn cli_default_build_is_power_free() {
    let names = reachable_crate_names(&[CLI_CRATE], &[]);
    assert!(
        !names.contains("costroid-power"),
        "the default CLI build must not link `costroid-power` — the `power` feature must be off by \
         default so the local-only build carries no local-inference engine."
    );
}

// =====================================================================================
// Taskbar binary (`costroid-bar`) — admits ONLY the reviewed AccessKit local-IPC subtree
// =====================================================================================

/// Default bar build: AccessKit is on (the bar's default feature), so the graph DOES contain
/// `async-io` — but only as part of the reviewed local-IPC subtree. `costroid-connect` must
/// not be linked, and every network/TLS/telemetry crate stays forbidden; the only forbidden
/// crate excused is the reviewed accesskit subtree ([`BAR_ACCESSKIT_ALLOWED`]). The real
/// accesskit delta is asserted to be a SUBSET of that allowlist (fail-closed).
#[test]
fn bar_default_build_admits_only_the_reviewed_accesskit_subtree() {
    let names = reachable_crate_names(&[BAR_CRATE], &[]);

    assert!(
        !names.contains(CONNECT_CRATE),
        "the default `{BAR_CRATE}` build must not link `{CONNECT_CRATE}` — the bar's `connect` \
         feature is off by default, so the GUI links no network/keychain code."
    );

    // Every network/TLS/telemetry/async-runtime crate is forbidden EXCEPT the reviewed
    // AccessKit local-IPC subtree (in practice just `async-io` from this list).
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES
            .iter()
            .chain(CONNECT_GATED_CRATES.iter())
            .copied(),
        BAR_ACCESSKIT_ALLOWED,
    );
    assert!(
        hits.is_empty(),
        "the default `{BAR_CRATE}` build forbids every network/TLS/telemetry crate (only the \
         reviewed AccessKit local-IPC subtree is excused), but the resolved graph contains: \
         {hits:?}.\n\
         If a crate is a legitimate part of the local-IPC AccessKit subtree, review it and add \
         it to BAR_ACCESSKIT_ALLOWED; otherwise it is a real egress path and must be removed."
    );

    // Positive check: AccessKit really is on (mirrors the connect test asserting the trio is
    // present) — the local AT-SPI reactor `async-io` is in the bar graph, so the carve-out is
    // load-bearing and not vacuous.
    assert!(
        names.contains("async-io"),
        "AccessKit must be ON in the default `{BAR_CRATE}` build (its `accesskit` default \
         feature), so its local-IPC `async-io` reactor is linked — accessibility is required."
    );

    // Subset-allowlist: bound exactly what the bar's `accesskit` feature ADDS over the same
    // build with it off, so a future egui/accesskit bump that pulls a NEW crate trips the gate.
    let no_accesskit = reachable_crate_names(&[BAR_CRATE], &["--no-default-features"]);
    let allowed: BTreeSet<&str> = BAR_ACCESSKIT_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&no_accesskit)
        .map(String::as_str)
        .filter(|name| !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "the bar's `accesskit` feature introduced crate(s) not in the reviewed allowlist: \
         {unexpected:?}.\n\
         Every crate the accesskit-on graph adds must be reviewed (is it a network / TLS / \
         telemetry path, or local AT-SPI/UI-Automation IPC?) and, if legitimate, added to \
         BAR_ACCESSKIT_ALLOWED. Regenerate the expected delta with: \
         `cargo test -p costroid --test offline print_bar_accesskit_delta -- --ignored --nocapture`."
    );
}

/// `--features connect` (bar): the display-only connection lane links `costroid-connect`, so
/// the sanctioned trio is permitted. AccessKit stays on (default), so `async-io` is still
/// admitted via the reviewed subtree — but every OTHER network/TLS/telemetry crate stays
/// forbidden, and the connect-delta over the default bar build is bounded by `CONNECT_ALLOWED`.
#[test]
fn bar_connect_build_forbids_network_except_the_reviewed_subtrees() {
    let names = reachable_crate_names(&[BAR_CRATE], &["--features", "connect"]);

    assert!(
        names.contains(CONNECT_CRATE),
        "`--features connect` must link `{CONNECT_CRATE}` in the bar too — otherwise the gate \
         is not wired to the display-only connection lane."
    );

    // The connect trio (CONNECT_GATED_CRATES) is now expected; among the ALWAYS_FORBIDDEN set,
    // only the reviewed accesskit subtree (`async-io`) may appear.
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES.iter().copied(),
        BAR_ACCESSKIT_ALLOWED,
    );
    assert!(
        hits.is_empty(),
        "even with `connect` on, the bar forbids async runtimes, non-rustls TLS, other HTTP \
         clients, and all telemetry (only the reviewed AccessKit subtree is excused), but the \
         resolved graph contains: {hits:?}."
    );
    for gated in CONNECT_GATED_CRATES {
        assert!(
            names.contains(*gated),
            "`--features connect` must link `{gated}` in the bar — the connection lane shares \
             the CLI's credential store, which depends on the `ureq`/`rustls`/`keyring` trio."
        );
    }

    // The connect-delta over the DEFAULT bar build (accesskit already on in both) must be a
    // subset of the reviewed CONNECT_ALLOWED — the same connect subtree the CLI admits.
    let bar_default = reachable_crate_names(&[BAR_CRATE], &[]);
    let allowed: BTreeSet<&str> = CONNECT_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&bar_default)
        .map(String::as_str)
        .filter(|name| *name != CONNECT_CRATE && !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "`--features connect` introduced crate(s) into the bar not in the reviewed allowlist: \
         {unexpected:?}.\n\
         Review each (network / TLS / telemetry?) and, if legitimate, add it to CONNECT_ALLOWED."
    );
}

// =====================================================================================
// Server binary (`costroid-server`) — loopback-only local HTTP/web-UI; no OUTBOUND network
// =====================================================================================

/// The local HTTP/web-UI binary admits the reviewed local-listen subtree ([`SERVER_ALLOWED`] —
/// `tiny_http` + transitives) and **no** outbound network/TLS/telemetry/async-runtime crate. It
/// must not link `costroid-connect` (it is local-only — it reads the ledger via `costroid-core`,
/// never the network). `tiny_http` is the lone [`ALWAYS_FORBIDDEN_CRATES`] member admitted, as an
/// INBOUND loopback listener; its no-egress / loopback-only behavior is proven at runtime by
/// `scripts/offline_acceptance.sh`. The real server-delta is asserted to be a SUBSET of the
/// allowlist (fail-closed).
#[test]
fn server_build_admits_only_the_reviewed_local_listen_subtree() {
    let names = reachable_crate_names(&[SERVER_CRATE], &[]);

    assert!(
        !names.contains(CONNECT_CRATE),
        "the `{SERVER_CRATE}` build must not link `{CONNECT_CRATE}` — the local HTTP/web-UI \
         server is local-only (it reads the ledger via `costroid-core`), so it links no \
         network/keychain code."
    );

    // Every outbound network/TLS/telemetry/async-runtime crate is forbidden EXCEPT the reviewed
    // local-listen subtree (in practice just `tiny_http` from this list). The connect trio
    // (`ureq`/`rustls`/`keyring`) is forbidden too — the server makes no outbound call.
    let hits = forbidden_hits(
        &names,
        ALWAYS_FORBIDDEN_CRATES
            .iter()
            .chain(CONNECT_GATED_CRATES.iter())
            .copied(),
        SERVER_ALLOWED,
    );
    assert!(
        hits.is_empty(),
        "the `{SERVER_CRATE}` build forbids every OUTBOUND network/TLS/telemetry/async-runtime \
         crate (only the reviewed local-listen subtree is excused), but the resolved graph \
         contains: {hits:?}.\n\
         If a crate is a legitimate part of the inbound local-listen subtree, review it and add \
         it to SERVER_ALLOWED; otherwise it is a real egress/telemetry path and must be removed."
    );

    // Positive check: the local listener really is linked, so the carve-out is load-bearing and
    // not vacuous (mirrors the connect-trio / async-io positive checks).
    assert!(
        names.contains("tiny_http"),
        "`{SERVER_CRATE}` must link `tiny_http` — otherwise the SERVER_ALLOWED carve-out is \
         vacuous (the server needs its loopback HTTP listener)."
    );

    // Subset-allowlist: bound exactly what the server adds over the default CLI build, so a future
    // dependency bump that introduces a NEW crate (an outbound HTTP/TLS/telemetry path, or a
    // `tiny_http` SSL feature) trips this gate for a human to review.
    let cli_default = reachable_crate_names(&[CLI_CRATE], &[]);
    let allowed: BTreeSet<&str> = SERVER_ALLOWED.iter().copied().collect();
    let unexpected: Vec<&str> = names
        .difference(&cli_default)
        .map(String::as_str)
        .filter(|name| *name != SERVER_CRATE && !allowed.contains(name))
        .collect();
    assert!(
        unexpected.is_empty(),
        "`{SERVER_CRATE}` introduced crate(s) not in the reviewed allowlist: {unexpected:?}.\n\
         Every crate the server graph adds over the CLI must be reviewed (is it an outbound \
         network / TLS / telemetry path?) and, if legitimate, added to SERVER_ALLOWED. Regenerate \
         the expected delta with: \
         `cargo test -p costroid --test offline print_server_delta -- --ignored --nocapture`."
    );
}

// =====================================================================================
// Maintenance helpers (not run in CI) — regenerate the allowlists after a deliberate dep change
// =====================================================================================

/// Prints the exact crates `--features connect` adds to the CLI over the default build,
/// unioned across all shipped targets. Run to regenerate [`CONNECT_ALLOWED`]:
/// `cargo test -p costroid --test offline print_connect_delta -- --ignored --nocapture`.
#[test]
#[ignore]
fn print_connect_delta() {
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let connect = reachable_crate_names(&[CLI_CRATE], &["--features", "connect"]);
    let delta: Vec<&String> = connect.difference(&default).collect();
    println!("CONNECT_DELTA ({} crates):", delta.len());
    for name in &delta {
        println!("  {name}");
    }
}

/// Prints the exact crates the bar's `accesskit` (default) feature adds, unioned across all
/// shipped targets. Run to regenerate [`BAR_ACCESSKIT_ALLOWED`]:
/// `cargo test -p costroid --test offline print_bar_accesskit_delta -- --ignored --nocapture`.
#[test]
#[ignore]
fn print_bar_accesskit_delta() {
    let with = reachable_crate_names(&[BAR_CRATE], &[]);
    let without = reachable_crate_names(&[BAR_CRATE], &["--no-default-features"]);
    let delta: Vec<&String> = with.difference(&without).collect();
    println!("BAR_ACCESSKIT_DELTA ({} crates):", delta.len());
    for name in &delta {
        println!("  {name}");
    }
}

/// Prints the exact crates `--features store` adds to the CLI over the default build, unioned
/// across all shipped targets. Run to regenerate [`STORE_ALLOWED`]:
/// `cargo test -p costroid --test offline print_store_delta -- --ignored --nocapture`.
#[test]
#[ignore]
fn print_store_delta() {
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let store = reachable_crate_names(&[CLI_CRATE], &["--features", "store"]);
    let delta: Vec<&String> = store.difference(&default).collect();
    println!("STORE_DELTA ({} crates):", delta.len());
    for name in &delta {
        println!("  {name}");
    }
}

/// Prints the exact crates `--features power` adds to the CLI over the default build, unioned
/// across all shipped targets. Run to regenerate [`POWER_ALLOWED`]:
/// `cargo test -p costroid --test offline print_power_delta -- --ignored --nocapture`.
#[test]
#[ignore]
fn print_power_delta() {
    let default = reachable_crate_names(&[CLI_CRATE], &[]);
    let power = reachable_crate_names(&[CLI_CRATE], &["--features", "power"]);
    let delta: Vec<&String> = power.difference(&default).collect();
    println!("POWER_DELTA ({} crates):", delta.len());
    for name in &delta {
        println!("  {name}");
    }
}

/// Prints the exact crates `costroid-server` adds over the default CLI build, unioned across all
/// shipped targets (`costroid-server` itself excluded). Run to regenerate [`SERVER_ALLOWED`]:
/// `cargo test -p costroid --test offline print_server_delta -- --ignored --nocapture`.
#[test]
#[ignore]
fn print_server_delta() {
    let cli = reachable_crate_names(&[CLI_CRATE], &[]);
    let server = reachable_crate_names(&[SERVER_CRATE], &[]);
    let delta: Vec<&String> = server
        .difference(&cli)
        .filter(|name| name.as_str() != SERVER_CRATE)
        .collect();
    println!("SERVER_DELTA ({} crates):", delta.len());
    for name in &delta {
        println!("  {name}");
    }
}
