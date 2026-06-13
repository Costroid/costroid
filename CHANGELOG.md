# Changelog

All notable changes to Costroid are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Costroid adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Costroid is local-first: every release in this log reads only the logs your AI coding
tools already write to disk, makes no network calls in the default build, and sends no
telemetry. All cost figures are estimates (your tokens × current prices), reconcilable
against your provider invoice, which is the source of truth.

## [Unreleased]

Groundwork for the 0.4.0 connections line, plus a cross-cutting review fix pass. Nothing
here turns networking on: the new `costroid-connect` crate is feature-gated and **off by
default** (the default `costroid` binary does not even link it), there is no user-facing
connect flow until v0.4.0, and the default build still makes zero network calls — now
proven by stricter guards.

### Added

- **`costroid-connect` crate skeleton (feature-gated, off by default)** — the single
  future home of all network and credential code. With it, the no-network guarantee is
  re-scoped into a two-tier guard over the resolved dependency graph: the default build
  is proven to link no networking, TLS, or keychain code at all (and no
  `costroid-connect`), while a `--features connect` build may admit only the sanctioned
  `ureq`/`rustls`/`keyring` trio.
- **OS-keychain credential store** in `costroid-connect` — `CredentialStore` keeps your
  own usage/billing API keys only in the OS keychain (via `keyring`, secrets wrapped in
  `secrecy`), alongside a non-secret `ConnectionRegistry` index and the `ApiVendor`
  billing-vendor axis. Library-only and off by default: no network and no CLI yet — the
  HTTP client and the `costroid connect`/`disconnect` commands arrive with v0.4.0. The
  offline-acceptance gate gains a feature-on baseline proving that even a
  `--features connect` run makes zero network calls and writes no stray files to `$HOME`.
- **Generic authorized-host HTTPS client** in `costroid-connect` — the foundation of the
  opt-in connections feature's network half. A small, blocking, provider-agnostic client
  (`ureq` + `rustls`, no async runtime, no OpenSSL) that is bound in the type to **one**
  explicitly authorized host: any off-host request is a typed error before any I/O,
  redirects are refused (never followed), proxy env vars are ignored, requests are
  HTTPS-only and GET-only with bounded timeouts and body size, TLS trust comes from your
  **OS-native certificate store** (never a compiled-in bundle), and auth headers ride in
  redacted secret strings that can never reach logs or error text. **Nothing calls it
  yet** — there is still no user-facing connect flow and no provider adapter, so every
  build (default *and* `--features connect`) still performs zero network calls: the
  strace/offline-acceptance baseline keeps proving the zero-call property, while the
  forbidden-crates test proves sanctioned-only *linkage* (the full
  `ureq`/`rustls`/`keyring` trio links only behind `--features connect`, and the default
  build links none of it).
- **Anthropic + OpenAI usage-API adapters (and a first-class Gemini "unavailable")** in
  `costroid-connect` — parse a stored admin key's billed-cost and token-usage reports
  into provider-neutral shapes in `costroid-core` (so reconciliation stays pure-core).
  Money is exact end to end (`rust_decimal`, never `f64`) and unit-tagged at the parse
  boundary so Anthropic's decimal-cents and OpenAI's float-dollars encodings cannot mix.
  Honesty caveats ride as typed data — Anthropic's totals omit Priority-Tier dollars, and
  OpenAI's per-model dollars are best-effort (and its token lane may not cover the
  Responses API that Codex uses). Gemini has no adapter and reports "unavailable — no
  sanctioned static-key usage API". **This does not change the default build's behavior
  at all:** nothing calls the adapters yet (the `costroid connect` flow arrives in
  v0.4.0), keys ride only in the OS keychain and only in request headers (never a URL,
  log, or error), and every build still performs zero network calls.
- **MSRV CI job** — the documented minimum supported Rust version (Rust 1.88) is now
  built in CI.
- **Security-advisory CI job** — `cargo deny check advisories` now runs in CI as a
  dedicated online job (CI-only; the shipped tool is unchanged and still makes no
  network calls).
- **T9 usage-API endpoint pins** recorded in-repo as a proposal
  (`docs/proposals/T9-PIN-PROPOSAL.md`), **signed off 2026-06-10** — the Anthropic and
  OpenAI own-admin-key usage endpoints are pinned, and Gemini is deferred to a
  first-class "unavailable" state (no sanctioned static-key usage API). The generic
  authorized-host HTTPS client above is the first scheduled work these pins unblocked.

### Fixed

- **`--plain` and the plain statusline carry a textual warning/critical cue** —
  `(near limit)` / `(critical)` / `(over limit)`, matching the styled paths' `!` / `!!`
  — so limit state never relies on color alone.
- **"capture time unknown"** — a captured quota reading whose timestamp is the epoch
  sentinel (no observation instant recorded) now renders "capture time unknown" instead
  of a fabricated "as of 00:00" freshness stamp.
- **Codex quota readings are raw-range-sanitized** like Claude's: an out-of-range
  `used_percent` in Codex's local windows is dropped — the window degrades (keeping its
  reset stamp) rather than rendering a confident wrong number.
- **The FOCUS-conformance CI gate now performs a real validation.** It had been passing
  vacuously (the PyPI validator ships no FOCUS 1.3 ruleset, and its crash was
  swallowed); the official 1.3.0.1 ruleset is now vendored, the checker hard-fails when
  the validator produces no results, and the known-failure allowlist is matched exactly.
- **Costroid-generated `--plain` text and Ascii-mode output are pure ASCII (test-pinned)**
  — the Cursor detect-only note no longer carries em dashes into plain output, and the
  frontier header / point-note separators no longer carry them into `RenderMode::Ascii`
  output.

## [0.3.0] - 2026-06-06

The 0.3.0 milestone: the generalized quota model plus Claude Code's live 5h/7d quota —
**captured** from the `statusLine` hook (T5), **read + sanitized + cross-checked** from a
no-secret local cache (T4), and **rendered** on the `now` screen and status line (T6), all
on top of the cost lane — no new network code, no telemetry, still zero network calls in
the default build.

### Added

- **`costroid setup-statusline`** (and **`--undo`**) — wires Claude Code's `statusLine` so
  each assistant turn tees its `rate_limits` into a no-secret local cache: it injects a
  capture snippet into an existing `statusLine`, or makes Costroid the status line if you
  have none. Idempotent, backs up `settings.json` first, and fully reversible.
- **`costroid statusline --capture-only`** — the internal capture step the snippet calls:
  reads the `statusLine` JSON on stdin and writes only two percentages, two reset stamps,
  and a capture time to the cache — never a token, prompt, or credential. Emits nothing,
  exits 0 always, and never fails your prompt.
- **`costroid statusline --wrap '<command>'`** — a manual escape hatch that captures and
  then runs an existing status-line command on the same input.
- **Live Claude 5h/7d quota on the `now` screen and status line** — the captured
  `rate_limits` cache is read, **sanitized and cross-checked**, then rendered: a meter for
  token-fraction limits, `$used / $included used` (no fabricated %) for dollar limits, and
  an estimate fallback. Readings degrade to a color-free " ? unverified" cue or to
  "unavailable" rather than ever showing a confident wrong number, carry an always-on
  "as of HH:MM" freshness stamp, and note that claude.ai chat usage may make true usage
  higher. Still no network calls in the default build.
- **Generalized quota model** — limit windows, measures (token-fraction vs. dollar spend),
  kinds (5-hour / weekly / daily / monthly / billing-cycle) and availability states are
  normalized across providers, so each provider's quota maps onto one shared shape.

## [0.2.0] - 2026-06-05

Ships the already-built local **cost lane** on top of v0.1.0 — no new network code, no
telemetry, still zero network calls in the default build.

### Added

- **`costroid frontier`** — the cost-vs-quality frontier view. Plots the bundled, dated,
  sourced cost-vs-quality frontier (DeepSWE + CursorBench) and where your own spend sits
  on it. Advisory and sourced, **API-cost rows only**; un-benchmarked models are shown as
  gaps, never guessed. Renders in braille, ASCII, and `--plain` modes.

### Changed

- **Cursor: detect-and-defer (beta).** Cursor is detected when installed, but its live
  subscription quota is deferred to a later release (Cursor keeps no local quota log).
  The quota lane degrades to "unavailable" rather than presenting a guessed number.
- **WSL Windows-root auto-detection.** Log discovery now auto-detects the Windows user
  root from inside WSL, so logs that AI tools write under `/mnt/c/Users/<user>/...` (when
  the tools run on Windows) are found alongside logs written inside WSL under `~`.

## [0.1.0] - 2026-06-03

First public release — the local cost lane's foundation.

### Added

- **`costroid` (now)** — current API spend by model, plus Codex 5-hour and weekly
  subscription limits with reset countdowns, from local data with no network calls.
  (Live Claude subscription quota is not yet wired; Cursor quota is detect-and-defer.)
- **`costroid trends`** — spend over time with `--period day|week|month|year` and
  `--group model|app|total`.
- **`costroid statusline`** — a compact one-line status for a shell prompt, tmux, or
  Starship.
- **`costroid export`** — FOCUS 1.3-conformant records (`--format json|csv`).
- **`--live`** auto-refreshing interactive view and **`--plain`** ASCII mode
  (screen-reader- and pipe-friendly; never relies on color alone).
- WSL-aware multi-root log discovery for Claude Code, Codex, and Cursor; degrades
  gracefully when a provider is absent.
- Exact-`Decimal` `tokens × price` cost from bundled, dated pricing; estimates verified
  to the cent against ccusage.
- Packaged releases via cargo-dist (shell, PowerShell, Homebrew, npm) plus crates.io
  (`cargo install costroid` / `cargo binstall costroid`), each artifact SHA-256-checksummed
  and build-provenance-attested.

[Unreleased]: https://github.com/Costroid/costroid/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Costroid/costroid/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Costroid/costroid/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Costroid/costroid/releases/tag/v0.1.0
