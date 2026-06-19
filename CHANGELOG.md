# Changelog

All notable changes to Costroid are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Costroid adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Costroid is local-first: every release in this log reads only the logs your AI coding
tools already write to disk, makes no network calls in the default build, and sends no
telemetry. All cost figures are estimates (your tokens × current prices), reconcilable
against your provider invoice, which is the source of truth.

## [Unreleased]

### Added

- **Activity tab (`9`) — a contribution heatmap + Stats.** A new TUI tab renders a GitHub-style
  weeks×weekdays heatmap of your daily token volume in the brand's dot-density language (each
  cell's ink *and* color scale with the day's activity, so it reads in grayscale too), plus
  at-a-glance facts — total tokens, active days, busiest day, top model, and current/longest
  streak — and one tasteful, clearly-rough comparison line. Built from the same local FOCUS rows
  the History tab reads: no new data path, no network. `--plain` lists the facts (no 2-D grid) for
  screen readers.
- **Per-model color coding.** Each model now gets a stable hue by spend rank: a leading `●`/`*`
  dot + its spend-bar fill in that color across the Now and Models surfaces (the dot + name keep
  models distinguishable with color stripped).
- **`costroid_core::now_model_spend_breakdown`** — a pure display helper (per-model API-lane spend,
  highest first, `~`-hedged + a `0.0..=1.0` share fraction) the taskbar uses to paint its colored
  per-model share list without naming a money type, mirroring the existing `forecast_daily_fractions`
  pattern.

### Changed

- **Terminal UI refresh — the CLI/TUI now renders in color.** The `costroid` CLI and its interactive
  TUI carry the brand's own **COSTROID·CLI** palette instead of the former monochrome projection:
  a cold **cyan** data fill (with a dim-cyan track) on quota meters, cost bars and the spend
  sparkline; the **Signal-lime** accent on the `C⠉` mark, the active tab and the `◆` insight marker;
  **Ash-muted** section labels, captions and scope; **bold** dollar figures and model names. The
  amber/red near-/over-limit lane is unchanged and still always carries its `!`/`!!`/`OVER` text cue.
  It uses Costroid's own identity (cyan + lime), not a borrowed third-party hue.
- **TUI gains a top tab strip and a contextual hint bar.** The eight numbered tabs render across the
  top with the active tab as a reverse-video lime chip (legible under `NO_COLOR` too); the footer is
  now a colorized, screen-aware keybinding hint row (keys in lime, labels muted), including the
  Trends `d/w/m/y period` / `g group` hints.
- **Accessibility unchanged and re-verified.** All color is gated on a color TTY — `--plain` and
  `NO_COLOR` emit zero escape codes with byte-identical output (the `is_ascii()` purity gates still
  pass), the glyph-shape and text cues remain the load-bearing signals, and color is never the only
  cue.
- **Taskbar (`costroid-bar`) — a colorful, lean cockpit refresh.** The taskbar now carries the same
  evolved color language as the CLI, and is tighter as a glance surface:
  - **Per-model color coding** matching the terminal — the Overview "by model" rows lead with a
    spend-rank `Series` legend dot + a single-row share dot-bar in that hue (the dot + name + dollar
    keep models distinguishable with color stripped).
  - **Colored state chips** — Providers health (green available · cyan detected · amber partial · red
    error), Budget pace (green on-track · amber ahead-of-pace · red over-budget), and connection
    state, each pairing the color with its word (never color alone) and named for AccessKit. The
    active tab is now a filled Signal-lime chip (the fill is the non-color cue), and the footer keys
    are lime.
  - **Leaner panels** — the persistent header status carries the `· estimates` caveat once, so the
    panels drop the per-panel scope lines + estimate-note paragraphs (every `$` is still `~`-hedged +
    `(estimated)`-tagged); the Claude chat caveat shows once under the meter stack (deduped); the
    Budget empty state is a 2-line hint instead of the full TOML dump; Providers shows cost + quota
    sources and sheds the model-mix/auth reference lines. The severity dot grid, the honest five
    availability arms, and the no-new-network/keychain/telemetry guarantees are unchanged.

### Fixed

- ASCII-mode screen headers no longer render the wordmark twice (`costroid costroid` → `costroid`).

## [0.6.0] - 2026-06-18

This release ships the **`costroid-bar` taskbar** — the last surface — an always-on tray glance
plus a small live cockpit window for what your AI coding tools cost and how close you are to your
subscription limits. It is a pure consumer of the same local data the CLI reads: no new network
call, no telemetry. It also lands two opt-in advisory alert sources and a wider anomaly model-mix.

### Added

- **`costroid-bar` — the egui taskbar (Step 6, the last surface).** A new binary alongside the
  `costroid` CLI:
  - a **tray icon** — the `C⠉` mark whose 3×3 braille dots ARE your most-constrained quota meter,
    in the 9-step dot-density warning language (non-color-safe by construction), with a full
    tooltip; left-click toggles the window, right-click opens an Open / Refresh / Quit menu;
  - a small, resizable **toggle window** that remembers its size/position and refreshes on show;
  - an **Overview** — this-period spend (always `~`-hedged + estimate-labeled) above the painted
    dot/braille quota meters, honest across all five availability arms (a degraded reading is never
    dressed as a confident fill), plus the opt-in alert banner;
  - four **live panels** — Budget, Forecast, Anomalies, Providers — each mapping one core view; the
    Providers panel can *display* read-only connection state under `--features connect` (connecting/
    reconciling stay in the CLI, so the taskbar adds no credential or network surface);
  - a **shared `costroid-config` crate** — the `[budget]`/`[alerts]` TOML schema, now read by both
    the CLI and the taskbar from one source.
  - **Accessibility:** AccessKit is on — a screen reader announces each painted meter, alert badge,
    tab, the tray mark, the refresh button, and the forecast sparkline; the never-color-alone
    dot-density cue holds throughout. The Linux AT-SPI backend speaks local D-Bus only — never
    network (proven by the offline-acceptance harness + the per-binary static dependency allowlist).
  - **Trends, Models, History, and Frontier stay in the `costroid` CLI** (they are cramped in a tray
    window and the TUI serves them well).
- **Two opt-in advisory alert sources (a fast-follow to the threshold alerts).** Alongside the hard
  quota-% and budget-$ crossings, the `[alerts]` config gains two advisory sub-flags — each off by
  default and each still requiring `enabled = true`: `forecast = true` fires a heads-up when your
  month-end **projection** would exceed your **total** budget (only off a settled projection — never
  the noisy first days of the month, and never when you are already over, which the hard budget
  alert covers), and `anomalies = true` fires on a **daily spend spike** versus your own recent
  norm. Both are advisory by nature — a softer heads-up that sorts after the quota/budget crossings,
  carrying a distinct `(projected over budget)` / `(spend spike)` non-color cue — and both surface
  in the inline banner, the `costroid alerts` list, and the `alerts --check` exit code once opted in.
  With the sub-flags off, alert output is byte-identical to before. Still pure-local: no daemon, no
  network, no telemetry, no new dependency.
- **Anomalies model-mix now spans every lane.** The Anomalies model-mix-shift callout previously read
  the API-billed lane only, so a subscription-only user (e.g. Claude Code Max with no API key) saw no
  model-mix callouts. It now measures all-lane token share, so those users get callouts too, while the
  spend-spike stays API-lane dollars (subscription usage is not a summable bill).

### Release

- **`costroid-bar` ships as downloadable binary archives + `cargo install costroid-bar` (crates.io).**
  Because the macOS/Windows tray paths compile but are not yet field-verified, the one-click
  npm/Homebrew installers stay **CLI-only** (`costroid`) this cut; the GUI joins them in a later
  0.6.x once the desktop matrix is confirmed. The release toolchain is now ≥ 1.92 (the taskbar's
  MSRV); the `costroid` CLI + library crates keep their 1.88 MSRV promise.

## [0.5.0] - 2026-06-17

This release completes the analytical surface: six dedicated TUI tabs over your local data
plus opt-in threshold alerts — all pure-local, no network in the default build, no telemetry,
every figure a labeled estimate.

### Added

- **Six analytical TUI tabs, reachable by number (`1`–`8`) or Tab / Shift-Tab.** Alongside
  `now` and `trends`: **Providers** (each tool's data source, auth method, and what is available
  vs unavailable), **Models** (per-model API spend + token mix fused with the cost-vs-quality
  frontier; un-benchmarked models shown as gaps, never guessed), **History** (the full per-turn
  FOCUS record — time, model, tokens, access path, API-only estimated cost — newest-first and
  scrollable), **Budget** (your monthly $ target(s) vs actual API-lane spend, with a fill bar, a
  pace cue, and an honest over-budget state), **Forecast** (a linear run-rate month-end $
  projection + per-quota-window burn ETAs — both hedged estimates that degrade to "unavailable"
  rather than show a confident wrong number), and **Anomalies** (proactive, non-alarmist
  spend-spike + model-mix-shift callouts vs your own recent history). Every tab renders in
  `--plain` ASCII and never relies on color alone.
- **First user-config file — a read-only TOML at `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`.**
  Non-secret (credentials stay keychain-only), forward-compatible, and never written by Costroid
  (no writer, no `set` command). Today it carries `[budget]` monthly $ targets (API-lane only) and
  the `[alerts]` opt-in. Money is exact decimal, never a float; an absent file is the zero-config
  default and a malformed file surfaces a clear status line, never a crash.
- **`costroid alerts` — opt-in threshold alerts (default OFF, no daemon, no new dependency).**
  Surfaces two never-mixed crossing classes from your local data: a **quota window** at/above its
  warning (80%) or critical (95%) threshold — fired only off a fresh, cross-checked reading, never
  an unverified/estimated/stale one — and a **budget** strictly over its monthly $ target (API-lane
  only). Quota copy is quota-extension framing ("claude code weekly limit at 92%, resets in 2d");
  budget copy is dollars, always labeled an estimate. Delivery is two built-in surfaces: an inline
  banner shown atop the `now` view (amber/red, but always paired with a `!`/`!!` or spelled-out
  non-color cue, so it survives `--plain`/`NO_COLOR`), and `costroid alerts` — a human list with
  honest "alerts off" / "no active alerts" states. `costroid alerts --check` is cron-friendly: it
  prints at most one line and sets the exit code (0 = clear, 1 = a crossing, 2 = a config/collect
  error). Enable it (and optionally override the per-class thresholds) in the config file:
  ```toml
  [alerts]
  enabled = true
  # quota_warn = 0.80      # optional overrides (defaults shown)
  # quota_critical = 0.95
  ```
  Forecast projected-hits and anomaly callouts stay advisory on their own tabs — alerting on them
  is a deferred follow-up. Pure-local: no network, no telemetry, no desktop notifications in this
  baseline (an OS-notification path is deferred behind a future Cargo feature + config opt-in).

## [0.4.0] - 2026-06-16

The 0.4.0 connections line: the `costroid connect`/`disconnect`/`connections` CLI now
exists — the first opt-in connection of your own usage/billing API key, and the first
real network in the product — and `costroid reconcile` now puts your local cost estimate
side by side with the vendor's billed invoice. It all stays **off by default**: networking
lives only in the feature-gated `costroid-connect` crate (the default `costroid` binary
does not even link it), and only the explicit `connect` / `connections --check` /
`reconcile` actions reach the network — the default build and every other command still
make **zero** network calls, proven by the offline-acceptance harness.

### Added

- **`costroid reconcile` — estimate-vs-invoice on screen (opt-in, feature-gated).**
  Compares Costroid's local cost estimate against a connected vendor's billed invoice, per
  completed UTC day and per model: `costroid reconcile [--vendor anthropic|openai]
  [--period day|week|month|year]`. With no `--vendor` it reconciles every connected
  billing vendor (each its own section) and always shows Gemini as "unavailable — no
  sanctioned static-key usage API". It reuses the key you already connected and the same
  authorized client — **no new network or secret boundary**; the only network is the
  cost-report fetch on this explicit action, and the default build neither links nor
  exposes the command. Rendering is honest: the local figure is always labeled an estimate
  (`~`); signed variance carries its direction as text (`+$X over` / `-$X under` /
  `exact`, percentage rounded at the render boundary); a vendor-side gap is shown as typed
  text (`report doesn't cover this day`, `not attributed by the vendor`, `connect <vendor>
  first`) and **never** a fabricated `$0`; the report's caveats are footnoted (Anthropic
  Priority-Tier absence; OpenAI per-model figures best-effort). This is **dollar (cost)
  reconciliation only** — it carries no token-undercount caveat (Codex/Responses-API
  traffic is fully counted). Full `--plain` ASCII path; nothing relies on color.
- **`costroid connect` / `disconnect` / `connections` CLI (opt-in, feature-gated).**
  Connect your own admin usage/billing API key — Anthropic (`sk-ant-admin…`) or OpenAI
  (`sk-admin-…`) — to read live numbers no local log carries. The key is read from
  **stdin only** (a hidden, no-echo prompt on a terminal; one line on a pipe — never a
  command-line argument or an environment variable), validated before it is stored
  (Anthropic via `GET /v1/organizations/me`, which reads no billing data; OpenAI by a
  one-day cost probe), and kept **only** in your OS keychain — never on disk, in a config
  file, or in a log. `disconnect` revokes instantly (idempotent). `connections` lists what
  is linked, local-only by default; `connections --check` re-validates each over the
  network. `gemini` is a recognized vendor with a known answer — it prints "unavailable —
  no sanctioned static-key usage API" and exits without prompting for a key. Every screen
  has a `--plain` ASCII path with a non-color status cue. Before the key is pasted,
  `connect` warns that an admin key is **organization-wide** — it can read the whole
  organization's usage and billing — and recommends a dedicated, instantly-revocable key
  (Anthropic/OpenAI only; `gemini` reads no key and skips the warning). Off by default and
  absent from the local-only build.
- **OpenAI `/costs` token-coverage and money-shape, live-confirmed.** A live read of the
  OpenAI Organization usage API confirmed that `usage/completions` **covers Responses-API
  (Codex) traffic**, so Costroid carries no token-undercount caveat for it. The cost
  money parser was hardened for the real `/costs` shape — `amount.value` can be a JSON
  string, scientific notation, or carry more than 28 fractional digits — and now absorbs
  all of it exactly (always `Decimal`, never floating point) instead of erroring.

- **`costroid-connect` crate skeleton (feature-gated, off by default)** — the single
  future home of all network and credential code. With it, the no-network guarantee is
  re-scoped into a two-tier guard over the resolved dependency graph: the default build
  is proven to link no networking, TLS, or keychain code at all (and no
  `costroid-connect`), while a `--features connect` build may admit only the sanctioned
  `ureq`/`rustls`/`keyring` trio.
- **OS-keychain credential store** in `costroid-connect` — `CredentialStore` keeps your
  own usage/billing API keys only in the OS keychain (via `keyring`, secrets wrapped in
  `secrecy`), alongside a non-secret `ConnectionRegistry` index and the `ApiVendor`
  billing-vendor axis. Library-only and off by default **when it landed** — the HTTP
  client, the adapters, and the `costroid connect`/`disconnect`/`reconcile` callers then
  landed later in this same 0.4.0 line (see the entries above). The offline-acceptance gate
  gains a feature-on baseline proving that even a `--features connect` run makes zero
  network calls and writes no stray files to `$HOME`.
- **Generic authorized-host HTTPS client** in `costroid-connect` — the foundation of the
  opt-in connections feature's network half. A small, blocking, provider-agnostic client
  (`ureq` + `rustls`, no async runtime, no OpenSSL) that is bound in the type to **one**
  explicitly authorized host: any off-host request is a typed error before any I/O,
  redirects are refused (never followed), proxy env vars are ignored, requests are
  HTTPS-only and GET-only with bounded timeouts and body size, TLS trust comes from your
  **OS-native certificate store** (never a compiled-in bundle), and auth headers ride in
  redacted secret strings that can never reach logs or error text. **Nothing called it when
  it landed** — the provider adapters followed, then the `connect`/`reconcile` callers, all
  later in this same 0.4.0 line; the **default** build still performs zero network calls
  (the strace/offline-acceptance baseline keeps proving the zero-call property for it), and
  the forbidden-crates test proves sanctioned-only *linkage* (the full
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
  sanctioned static-key usage API". **The default build's behavior is unchanged:** nothing
  called the adapters when they landed (the `connect`/`reconcile` callers followed later in
  this same 0.4.0 line), keys ride only in the OS keychain and only in request headers
  (never a URL, log, or error), and the default build still performs zero network calls.
- **Estimate-vs-invoice reconciliation engine** in `costroid-core` — pure-core logic
  (`costroid-core::reconcile`) that compares Costroid's local **estimate** (Σ tokens ×
  bundled prices — always an estimate) against a vendor's **billed** cost report (the
  invoice — the source of truth), per UTC day and per model, surfacing the signed variance
  and its percentage. The estimate is never presented as the bill and never silently
  "corrected"; the vendor report's honesty caveats (Anthropic Priority-Tier-absent, OpenAI
  per-model best-effort) are carried through; vendor-side gaps are typed absence, never a
  fabricated `$0`; money stays exact `Decimal` end to end. **Pure core, no network, no
  `costroid-connect` dependency.** The engine landed first; the `reconcile` caller (T10c) —
  which fetches the report and hands both sides in — then landed later in this same 0.4.0
  line. The default build's behavior is unchanged (still zero network calls).
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
- **Untrusted vendor org labels are sanitized against terminal-escape injection** — an
  org name returned by a provider on `connect` is stripped of all control characters
  (C0/C1/DEL/`ESC`) at ingestion before it is stored or printed, and the connect output
  path also strips control characters and folds any remaining non-ASCII to pure ASCII
  under `--plain`, so a malicious or buggy label can never smuggle an escape sequence to
  the terminal or into the on-disk registry.

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

[Unreleased]: https://github.com/Costroid/costroid/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/Costroid/costroid/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Costroid/costroid/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Costroid/costroid/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Costroid/costroid/releases/tag/v0.1.0
