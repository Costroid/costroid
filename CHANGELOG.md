# Changelog

All notable changes to Costroid are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Costroid adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Costroid is local-first: every release reads only the logs your AI coding tools already write
to disk, makes no network calls in the default build, and sends no telemetry. All cost figures
are estimates (your tokens × current prices), reconcilable against your provider invoice.

## [Unreleased]

### Added

- Activity TUI tab (`9`): weeks×weekdays token heatmap in the dot-density language, plus stats (total tokens, active days, busiest day, top model, current/longest streak); `--plain` lists facts.
- Per-model color coding: stable per-model hue by spend rank (leading `●`/`*` dot + colored spend-bar) on Now and Models.
- `costroid_core::now_model_spend_breakdown` — pure display helper (per-model API-lane spend, highest first, `~`-hedged + share fraction) for the taskbar.

### Changed

- CLI/TUI now renders in color via the `SemanticStyle` palette (cyan data, lime accent, ash-muted labels, bold figures); near/over-limit lane keeps its `!`/`!!`/`OVER` text cue.
- TUI gains a top tab strip (active tab as reverse-video lime chip) and a colorized contextual hint bar.
- Color gated on a color TTY; `--plain`/`NO_COLOR` emit zero escapes with byte-identical output; color never the only cue.
- Taskbar refresh: per-model color coding, colored state chips (Providers/Budget/connection, each paired with its word), filled lime active-tab chip, leaner panels (header carries the `· estimates` caveat once; deduped Claude chat caveat; 2-line Budget empty state).

### Fixed

- ASCII-mode headers no longer render the wordmark twice (`costroid costroid` → `costroid`).

## [0.6.0] - 2026-06-18

### Added

- `costroid-bar` — the egui/eframe + `tray-icon` taskbar (Step 6, last surface):
  - tray icon: the `C⠉` mark whose braille dots ARE your most-constrained quota meter in the 9-step dot-density warning language, with tooltip; left-click toggles the window, right-click opens Open/Refresh/Quit.
  - small resizable toggle window that remembers size/position and refreshes on show.
  - Overview: this-period spend (`~`-hedged, estimate-labeled) above painted dot/braille quota meters honest across all five availability arms, plus the opt-in alert banner.
  - four panels — Budget, Forecast, Anomalies, Providers — each mapping one core view; Providers can display read-only connection state under `--features connect` (no credential/network surface).
  - shared `costroid-config` crate: the `[budget]`/`[alerts]` TOML schema, now read by both CLI and taskbar.
  - AccessKit on: screen reader announces each meter, badge, tab, the tray mark, the refresh button, and the forecast sparkline; Linux AT-SPI speaks local D-Bus only, never network. Trends/Models/History/Frontier stay in the CLI.
- Two opt-in advisory alert sub-flags (off by default, require `enabled = true`): `forecast = true` fires when month-end projection exceeds total budget (settled projection only, not when already over); `anomalies = true` fires on a daily spend spike vs your recent norm. Both surface in the banner, `alerts` list, and `alerts --check`; distinct `(projected over budget)`/`(spend spike)` cue.
- Anomalies model-mix now measures all-lane token share (so subscription-only users get callouts); spend-spike stays API-lane dollars.

### Release

- `costroid-bar` ships as binary archives + `cargo install costroid-bar` (crates.io); npm/Homebrew stay CLI-only this cut (macOS/Windows tray not yet field-verified). Release toolchain now ≥ 1.92 (the bar's MSRV); CLI + libraries keep 1.88.

## [0.5.0] - 2026-06-17

### Added

- Six analytical TUI tabs, reachable by number (`1`–`8`) or Tab/Shift-Tab: Providers (data source, auth, available vs unavailable), Models (per-model API spend + token mix fused with the frontier; un-benchmarked shown as gaps), History (per-turn FOCUS records, newest-first, scrollable), Budget (monthly $ target vs API-lane spend, fill bar, pace cue, over-budget state), Forecast (linear run-rate month-end projection + per-window burn ETAs, degrade to "unavailable"), Anomalies (spend-spike + model-mix-shift callouts). All render `--plain`; none rely on color alone.
- First user-config file — read-only TOML at `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`: non-secret, never written by Costroid; carries `[budget]` monthly $ targets (API-lane) and `[alerts]` opt-in; money is exact decimal; absent file = zero-config, malformed file = status line not crash.
- `costroid alerts` — opt-in threshold alerts (default OFF, no daemon, no new dependency): quota window at/above warning (80%) or critical (95%) fired only off a fresh cross-checked reading, and budget strictly over its monthly $ target (API-lane). Delivered via the inline `now` banner and the `alerts` list (`!`/`!!` non-color cue survives `--plain`/`NO_COLOR`); `alerts --check` is cron-friendly (exit 0 clear / 1 crossing / 2 error). Thresholds overridable in config. Forecast/anomaly alerting deferred; no desktop notifications in this baseline.

## [0.4.0] - 2026-06-16

### Added

- `costroid reconcile` — estimate-vs-invoice on screen (opt-in, feature-gated): compares the local estimate against a connected vendor's billed invoice per completed UTC day and per model (`--vendor anthropic|openai`, `--period day|week|month|year`); no `--vendor` reconciles every connected vendor and shows Gemini "unavailable". Reuses the connected key + authorized client (no new network/secret boundary); local figure always `~`-labeled, signed variance carries direction as text, vendor-side gaps shown as typed text (never a fabricated `$0`); dollar (cost) reconciliation only; full `--plain` path.
- `costroid connect`/`disconnect`/`connections` CLI (opt-in, feature-gated): connect your own admin usage/billing key — Anthropic (`sk-ant-admin…`) or OpenAI (`sk-admin-…`) — read from stdin only, validated before storage (Anthropic `GET /v1/organizations/me`; OpenAI one-day cost probe), stored only in the OS keychain. `disconnect` revokes instantly (idempotent); `connections` lists links local-only, `--check` re-validates over the network; `gemini` prints "unavailable" without prompting. `connect` warns the admin key is org-wide before paste. `--plain` path; off by default and absent from the local-only build.
- OpenAI `/costs` token-coverage and money-shape live-confirmed: `usage/completions` covers Responses-API (Codex) traffic (no token-undercount caveat); money parser hardened for string/scientific/over-scale `amount.value`, always exact `Decimal`.
- `costroid-connect` crate skeleton (feature-gated, off by default) — the single future home of all network + credential code; no-network guarantee re-scoped to a two-tier graph guard (default links no networking/TLS/keychain code; `--features connect` admits only `ureq`/`rustls`/`keyring`).
- OS-keychain credential store in `costroid-connect`: `CredentialStore` (keys in the OS keychain via `keyring`, wrapped in `secrecy`) + non-secret `ConnectionRegistry` index + the `ApiVendor` axis. Offline-acceptance gate gains a feature-on baseline (zero network, no stray `$HOME` files).
- Generic authorized-host HTTPS client in `costroid-connect`: blocking `ureq`+`rustls` (no async, no OpenSSL) bound in the type to one authorized host; off-host = typed error before I/O; redirects refused, proxy env ignored, HTTPS-only + GET-only, bounded timeouts/body, OS-native trust roots, redacted secret auth headers.
- Anthropic + OpenAI usage-API adapters (and a first-class Gemini "unavailable") in `costroid-connect`: parse a stored admin key's cost/usage reports into provider-neutral `costroid-core` shapes; money exact (`rust_decimal`) and unit-tagged at the parse boundary; honesty caveats ride as typed data (Anthropic omits Priority-Tier; OpenAI per-model best-effort).
- Estimate-vs-invoice reconciliation engine in `costroid-core::reconcile`: pure-core comparison of the local estimate (Σ tokens × bundled prices) against a vendor's billed report per UTC day and per model, surfacing signed variance + percentage; estimate never presented as the bill, vendor gaps typed (never `$0`), money exact `Decimal`; no network, no `costroid-connect` dependency.
- MSRV CI job (Rust 1.88) and a dedicated online security-advisory CI job (`cargo deny check advisories`).
- T9 usage-API endpoint pins recorded as a proposal, signed off 2026-06-10: Anthropic + OpenAI own-admin-key endpoints pinned, Gemini deferred to a first-class "unavailable".

### Fixed

- `--plain` and the plain statusline carry a textual `(near limit)`/`(critical)`/`(over limit)` cue, so limit state never relies on color alone.
- A captured quota reading with an epoch-sentinel timestamp now renders "capture time unknown" instead of a fabricated "as of 00:00".
- Codex quota readings are raw-range-sanitized like Claude's: an out-of-range `used_percent` is dropped (window degrades, keeping its reset stamp).
- The FOCUS-conformance CI gate now performs a real validation (1.3.0.1 ruleset vendored; hard-fails on no results; allowlist matched exactly) instead of passing vacuously.
- `--plain` text and Ascii-mode output are pure ASCII (test-pinned): no em dashes in the Cursor note or frontier separators.
- Untrusted vendor org labels are stripped of control characters at ingestion and folded to ASCII under `--plain`, blocking terminal-escape injection.

## [0.3.0] - 2026-06-06

### Added

- `costroid setup-statusline` (and `--undo`) — wires Claude Code's `statusLine` to tee `rate_limits` into a no-secret local cache; idempotent, backs up `settings.json`, reversible.
- `costroid statusline --capture-only` — the internal capture step: reads `statusLine` JSON on stdin, writes only two percentages, two reset stamps, and a capture time (never a token/prompt/credential); exits 0 always.
- `costroid statusline --wrap '<command>'` — manual escape hatch that captures, then runs an existing status-line command on the same input.
- Live Claude 5h/7d quota on `now` and the status line: the captured cache is sanitized + cross-checked, then rendered (meter for token-fraction, `$used / $included` for dollar limits, estimate fallback); degrades to " ? unverified"/"unavailable", carries an "as of HH:MM" stamp + claude.ai chat caveat. No network in the default build.
- Generalized quota model — limit windows, measures (token-fraction vs dollar), kinds (5h/weekly/daily/monthly/billing-cycle), and availability states normalized across providers.

## [0.2.0] - 2026-06-05

### Added

- `costroid frontier` — cost-vs-quality frontier view (bundled DeepSWE + CursorBench data) plotting where your spend sits; advisory, API-cost rows only, un-benchmarked shown as gaps; braille/ASCII/`--plain`.

### Changed

- Cursor: detect-and-defer (beta) — detected when installed, but live subscription quota deferred (no local quota log); quota degrades to "unavailable".
- WSL Windows-root auto-detection — log discovery finds logs AI tools write under `/mnt/c/Users/<user>/...` alongside logs written inside WSL under `~`.

## [0.1.0] - 2026-06-03

### Added

- `costroid` (now) — current API spend by model + Codex 5-hour and weekly limits with reset countdowns, from local data, no network. (Claude quota not yet wired; Cursor detect-and-defer.)
- `costroid trends` — spend over time with `--period day|week|month|year` and `--group model|app|total`.
- `costroid statusline` — compact one-line status for a shell prompt, tmux, or Starship.
- `costroid export` — FOCUS 1.3-conformant records (`--format json|csv`).
- `--live` auto-refreshing view and `--plain` ASCII mode (screen-reader- and pipe-friendly).
- WSL-aware multi-root log discovery for Claude Code, Codex, Cursor; degrades gracefully when a provider is absent.
- Exact-`Decimal` `tokens × price` cost from bundled, dated pricing; verified to the cent against ccusage.
- Packaged releases via cargo-dist (shell, PowerShell, Homebrew, npm) + crates.io (`cargo install costroid` / `cargo binstall costroid`), each artifact SHA-256-checksummed and build-provenance-attested.

[Unreleased]: https://github.com/Costroid/costroid/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/Costroid/costroid/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/Costroid/costroid/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/Costroid/costroid/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Costroid/costroid/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Costroid/costroid/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Costroid/costroid/releases/tag/v0.1.0
