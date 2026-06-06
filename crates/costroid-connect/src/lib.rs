//! Costroid's network + credential boundary — **the only crate allowed to make a
//! network call or touch a secret**. Everything else in the workspace is provably
//! local-only; this crate is the single, deliberate exception, and it is **off by
//! default**.
//!
//! # Status: empty skeleton (T7)
//!
//! This crate has **no behavior yet**. It exists now only to establish the home and
//! the feature gate for the connections subsystem (PRODUCT-PLAN §2c / Step 4) before
//! any network or keychain code is written. The implementation lands in later tasks:
//!
//! * **T8 — keychain.** Secret storage via the [`keyring`] crate → the OS keychain
//!   **only**. Tokens and API keys are never written to disk, config, or logs.
//! * **T9 — HTTP + reconciliation.** A blocking HTTP client ([`ureq`] with [`rustls`]
//!   TLS, no async runtime) that talks **strictly device ↔ provider**, never through a
//!   Costroid server, to pull the user's own usage/billing data for reconciliation.
//!
//! # The gate
//!
//! `costroid-connect` is compiled into a binary **only** when that binary opts in via
//! its `connect` Cargo feature (today: `apps/cli`'s `connect`, off by default). A
//! default `cargo build` never links it, so the shipped local-only build contains no
//! HTTP/TLS/keychain code at all. Two tests keep that honest:
//!
//! * `apps/cli/tests/offline.rs` — the default build must contain no networking,
//!   TLS, or telemetry crate; with `connect` on, only the sanctioned trio
//!   (`ureq`/`rustls`/`keyring`) becomes permitted (async runtimes, OpenSSL, and all
//!   telemetry stay forbidden in either build).
//! * `scripts/offline_acceptance.sh` — the default build runs every command under
//!   network isolation and proves no outbound IP traffic is attempted.
//!
//! # The auth source ladder (the rule the future code must obey)
//!
//! Every datum is sourced by descending an explicit ladder, most-sanctioned first —
//! **only tiers 0–3 are ever built** (PRODUCT-PLAN §5):
//!
//! 0. Local artifacts — provider logs on disk (today's default, *not* this crate).
//! 1. Sanctioned push/hook — e.g. Claude's `statusLine` capture (also not this crate).
//! 2. Sanctioned OAuth (first-party; system browser + loopback redirect, PKCE).
//! 3. The user's own API key — official provider *usage* APIs, the user's own key.
//! 4. **Never** reuse any credential, session, or cookie against a non-sanctioned,
//!    undocumented, or internal endpoint — that datum stays "unavailable", never
//!    fetched.
//!
//! No telemetry — ever. Network occurs only on an explicit, user-initiated `connect`
//! action to a provider endpoint the user authorized.
//!
//! [`keyring`]: https://crates.io/crates/keyring
//! [`ureq`]: https://crates.io/crates/ureq
//! [`rustls`]: https://crates.io/crates/rustls

// Intentionally empty: no public API yet. See the module docs above — behavior
// arrives in T8 (keychain) and T9 (HTTP clients + reconciliation).
