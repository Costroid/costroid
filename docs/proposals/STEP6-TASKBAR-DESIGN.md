# Step 6 — egui taskbar (`apps/bar`) — GUI/UX design pin

**Status: APPROVED — canon (Eren signed off 2026-06-17).** This is the design pin PRODUCT-PLAN
§297–299 / §308 require before Step 6 is carded ("the GUI/UX design — none exists yet — pin before the
handoff"); the §12.26 T18 card and its successors reference it. **Resolved decisions (Eren-confirmed
2026-06-17):** scope = **Glance + live cockpit**; window model = **toggle window**; visual identity = the
**dot/braille brand system** (§0); chrome font = **JetBrains Mono** (Neue Haas Grotesk is commercial — not
bundled); the warm **SYNC** coral ramp is **not used** in the taskbar. The brand identity is folded into
the canonical `docs/DESIGN-SYSTEM.md` (Brand basics) so it governs the whole product.

The taskbar is the **last surface** (PRODUCT-PLAN §3 Step 6 / §4). It is a *consumer* of
`costroid-core`, **never a second brain**: every figure it shows the core already computes; it adds no
new data path, no new network call beyond what `costroid-connect` already authorizes, and no telemetry.

---

## 0. Brand identity — the visual language this renders in (approved canon, Eren-confirmed 2026-06-17)

**The taskbar must read as a distinctive dot/braille terminal-native financial instrument — NOT a generic
GUI dashboard.** Even as an egui app it is **terminal-first**: it renders the *same* braille/dot language
as the TUI, in monospace, on a dark carbon ground, with the brand's dot-density warning system. Compact,
precise, interactive in the style of high-quality terminal tools (interaction *feel* inspired by Claude
Code + Mistral — never their assets/branding/colors/logos/layouts).

This is product canon (saved cross-session; the repo's `docs/DESIGN-SYSTEM.md` is the doc to fold it into
when broadening beyond the taskbar):

- **Palette.** Backgrounds = **Carbon `#0B0C0E`** / **Slate `#16181C`** / **Graphite `#2C2C2A`**
  (darkest→dark). Text = **Bone `#E9E7DF`** (primary), **Ash `#888780`** (muted/secondary). Accent =
  **Signal `#C8FF3D`** (lime) — the *active/selected/"live"* highlight, used **sparingly**. Cost/data viz
  = the **COSTROID·CLI cold cyan-blue ramp** (`#042C53 #185FA5 #378ADD #85B7EB` — "logs, data, raw
  compute"). The warm **COSTROID·SYNC coral ramp** (`#712B13 #D85A30 #F0997B #F5C4B3`) is the sibling
  SYNC surface's identity — **NOT used in the Costroid taskbar** (Eren-confirmed 2026-06-17).
- **Typography.** **JetBrains Mono** for everything measurable — numbers, costs, all braille glyphs,
  tabular nums ("anything measurable is mono") — **AND the chrome too**: Neue Haas Grotesk is a commercial
  font that **cannot be bundled** in an Apache-2.0 binary, so the shipped taskbar uses JetBrains Mono
  throughout (Eren-confirmed 2026-06-17). Neue Haas stays web/marketing only. Bundle JetBrains Mono
  (OFL/Apache) via egui font loading; no system-font dependency.
- **The 9-step DOT-DENSITY warning system (0 idle → 8 critical) is the universal severity cue, and it
  IS the "never rely on color alone" guarantee — baked into the brand.** Severity is encoded by the
  **number/fill of dots in a 3×3 grid** (plus a color progression), so it reads in grayscale and for
  color-blind users: `0 idle` (empty ring) · `1–2` green · `3` yellow · `4` orange · `5–6` red · `7`
  brown/over · `8` full black grid. **This dot-grid REPLACES the ad-hoc `!`/`!!` badges** from the
  earlier draft of this pin everywhere a severity is shown (the tray, the meters, the alert lines).
- **Mark.** The **`C⠉` braille mark** (a block "C" beside braille dots) + the `costroid` wordmark — the
  tray-icon and window-title glyph.

Map a consumed-quota fraction (or an alert level) onto the **0–8 dot scale** for every meter, the tray
icon, and every alert state. Everything below renders in this language.

---

## 1. Scope (LOCKED) — "Glance + live cockpit" for v0.6.0

The taskbar's distinct value over the TUI is the **always-on glance** + **live status** + **proactive
alerts** — not deep analysis (the TUI serves Trends/Models/History/Frontier well, and they are cramped
in a tray window). So v0.6.0 ships the live cockpit and defers the analysis tabs.

**IN (v0.6.0):**
- **Tray icon** — the most-constrained quota meter at a glance (state-encoded, non-color-safe) + a full
  tooltip; left-click toggles the window, right-click opens a menu.
- **Overview** (the window header) — this-period spend + every quota meter + the alert banner.
- **Four live panels:** **Budget**, **Forecast**, **Anomalies**, **Providers**.

**OUT (deferred fast-follow, not v0.6.0):** **Trends**, **Models**, **History**, **Frontier**. The TUI
remains the home for those; the bar adds them later (same `bench_view`/`trends_summary`/`models_view`
core fns, so it is purely render work when it lands). Document this in the bar's README so users know
where to find them.

**Non-goals (v0.6.0):** no connect/disconnect/reconcile **actions** in the GUI (those stay CLI — the
bar *displays* connection state read-only); no OS desktop notifications (the notify-rust deferral
holds, §12.23); no new config keys beyond the existing `[budget]`/`[alerts]`.

---

## 2. Crate & dependency shape

- **Package** `apps/bar`, **binary `costroid-bar`** — a new 6th workspace member.
- **Stack:** `eframe` + `egui` + `tray-icon` (all MIT/Apache-2.0, no webview, no Tauri). Depends only on
  `costroid-core` (+ `costroid-connect` behind the **same `connect` feature gate as the CLI** — the bar
  defines its own `connect` feature that turns on `costroid-connect`, exactly mirroring `apps/cli`).
- **AccessKit:** egui ships AccessKit — wire it on (it is a required, not optional, obligation).
- **⛔ dependency-license gate (CLAUDE.md "ask before adding deps"):** `eframe`/`egui`/`tray-icon` pull a
  large transitive tree (`winit`, `wgpu` or `glow`, `raw-window-handle`, font stacks, the platform tray
  shims). Before these land, a human verifies the **whole resolved subtree is permissive** (MIT/Apache-2.0/
  BSD/ISC/Zlib/Unicode) and `cargo deny check licenses bans` stays green. Prefer the **`glow` (OpenGL)
  renderer** over `wgpu` if it trims the tree / licenses cleaner — decide at build time with the actual
  `cargo tree`/`cargo deny` output. **No copyleft, openssl stays banned.**

**Dependency direction stays acyclic:** `apps/bar → core → {providers, focus}`; with `--features connect`,
`apps/bar → costroid-connect → core`. The bar links **no** network/keychain crate in its default build.

---

## 3. The tray icon (the glance) — the `C⠉` mark as a live dot-grid

The tray icon is the brand's `C⠉` mark whose **braille dots ARE the warning meter** — the glance is
"how close am I to my most-constrained limit," rendered in the 0–8 dot-density language.

- **What it encodes:** the **most-constrained quota window** across all detected tools (the same
  "most-pressing meter" the Now screen leads with). Source = `now_summary(...).limits`: pick the
  `LimitAvailability::Available` window with the highest consumed fraction; if none is `Available`,
  render the **idle/`?` state** (a muted grid) — never a fabricated number.
- **The icon = the `C⠉` mark + the 3×3 dot grid filled to the 0–8 severity step** of that fraction
  (`0 idle` empty → `8 critical` full black grid), tinted along the warning ramp (green→yellow→
  orange→red→black). Because severity is the **dot count/fill**, the glance survives grayscale and
  color-blindness with no extra badge — the brand's warning system *is* the non-color cue.
- **Degraded readings never show a confident fill:** an `Unverified`/`Estimated`/`Partial`/`Unavailable`
  most-constrained window renders the **idle/`?` muted grid**, not a filled severity — honesty over a
  guessed level.
- **Tooltip (always, JetBrains Mono):** the precise line, e.g.
  `claude code 5h — 92% used · resets in 41m · as of 15:32`. On macOS the menu-bar may additionally show
  a short `92%` text label beside the mark.
- **Left-click:** toggle the window (show/hide). **Right-click:** a menu — **Open Costroid · Refresh now ·
  Quit**.
- **Build note:** `tray-icon` takes a rasterized RGBA icon — generate the `C⠉`+dot-grid glyph as a small
  bitmap per severity step (9 pre-rendered icons, swapped as the fraction crosses a step), so the tray
  never depends on a system font. Keep the dot geometry identical to the in-window braille meters.

---

## 4. The window (toggle model)

A normal small **resizable** `eframe` window that **shows/hides** on tray left-click (not a popover —
robust across macOS/Windows/Linux, sidesteps popover-anchoring fragility). Remembers size + position
across sessions (egui persistence). On show, it triggers an immediate refresh so the glance is fresh.

```
┌─ Costroid ───────────────────────────  ⟳  ─┐   ⟳ = manual refresh
│ this week    ~$42.18  (estimate)            │   header: period spend, always ~-hedged + labeled
│ ─────────────────────────────────────────  │
│ claude code 5h  ███████░  92% !  resets 41m │   the quota meters (now_summary.limits),
│ claude code 7d  ████░░░░  51%    resets 3d  │   each: meter + % + non-color cue + reset + "as of"
│ codex 5h        ██░░░░░░  23%    resets 2h  │
│ cursor          unavailable: no sanctioned… │   typed-absence states render honestly
│ ─────────────────────────────────────────  │
│ ! codex budget over by ~$10.00              │   the alert banner = active_alerts (only when
│ ! projected over your ~$100 budget          │   [alerts] enabled + a crossing); amber/red + cue
│ ─────────────────────────────────────────  │
│ [Overview] [Budget] [Forecast] [Anomalies] [Providers]   ← tab strip
│                                             │
│  …selected panel content…                   │
└─────────────────────────────────────────────┘
   as of 15:32 · estimates · q quit · ? help
```

The **Overview** is the default panel (the header IS the overview — meters + spend + banner). The tab
strip switches the lower region between the four live panels. Keyboard: digit/arrow/Tab navigation
mirrors the TUI; `q` quits to tray, `r` refreshes.

---

## 5. The panels (each = one existing core fn, rendered as egui)

| Panel | Core fn (verbatim) | What it shows |
|---|---|---|
| **Overview** | `now_summary(snapshot, NowOptions::default())` + `active_alerts(…)` | the header meters + period spend + the alert banner |
| **Budget** | `budget_view(snapshot, &targets)` | per-scope spent vs target, pace, over-by; honest "no budget set" / excluded-tool states |
| **Forecast** | `forecast_view(snapshot)` | month-end `$` projection (or "insufficient data"), the actual-vs-projected sparkline, per-window quota ETAs |
| **Anomalies** | `anomalies_view(snapshot)` | spend-spike + model-mix callouts vs the user's own norm; the transient "no usage"/thin-history states |
| **Providers** | `snapshot.capabilities` + `snapshot.providers` (+ connect: read-only connection entries) | each provider's lane sources + what's unavailable + (gated) connection status — **display only** |

No panel computes anything; each maps a Serialize core view to egui widgets. Money is `Decimal`, never
`f64`; every dollar is `~`-hedged + estimate-labeled; typed absence renders honestly (never a fabricated
`$0`/`0%`).

---

## 6. Visual language (the dot/braille brand system, in egui)

Render in the §0 brand language — a dot/braille terminal-native instrument, not a GUI dashboard:

- **Ground & type:** a **Carbon/Slate** dark ground, **Bone** primary text, **Ash** for secondary/muted
  labels, **Signal** lime ONLY for the active tab / selected row / "live" accent (sparing). **Everything
  measurable is JetBrains Mono with tabular nums** — the meters, `%`, `$`, countdowns, dates, every
  braille glyph; Neue Haas Grotesk only for chrome titles/labels. Tight, compact spacing.
- **Braille meters (mirror the TUI exactly):** the quota/cost bars are the same braille blocks the TUI
  draws (`⠿`/`⠟`/`⠂` fill), not a smooth egui progress bar — the meter is a row of braille cells filled
  to the consumed fraction, in JetBrains Mono so the cells align. Cost rows use the **cold cyan-blue
  ramp** for the bar fill ("data/compute"); the quota meter fill is tinted by its **warning step**.
- **Severity = the 0–8 dot-grid, never `!`/`!!` (the §0 system):** every warning state — a near-limit
  meter, an alert line, the tray — shows the **3×3 dot grid at its severity step** (+ the ramp tint), so
  the cue survives grayscale and is unmistakably on-brand. `Warn`/`Critical` map to grid steps, not bare
  amber/red. This *is* the never-color-alone guarantee.
- **The five availability arms render distinctly (honesty over a confident bar):** `Available` (a real
  braille meter + the dot-grid step), `Partial` ("partial: reason"), `Unverified` (the meter + a muted
  `? unverified`, never a confident severity), `Estimated` ("usage: N tokens · quota % unavailable" — no
  meter), `Unavailable` ("unavailable: reason"). A degraded reading is never dressed as a confident fill
  or a high dot-grid.
- **Money & stamps:** every `$` is `~`-hedged + estimate-labeled (Decimal, never f64); the **"as of
  HH:MM"** freshness stamp shows when a reading is ≥10 min old — same fields as `render_limit_line`.
- **Voice:** sentence case, no emoji, one insight at a time; quota framed as quota-extension (never "save
  money"), budget in dollars — the DESIGN-SYSTEM voice rules, unchanged.
- **Motion:** minimal and precise (terminal-native, not animated-dashboard); honor OS reduced-motion. A
  refresh is a quiet state swap, not a flashy transition.

---

## 7. Alerts in the bar

- The **tray icon** already carries severity via its dot-grid fill (§3); the **in-window banner** mirrors
  `active_alerts(…)`, each alert line tagged with its **0–8 dot-grid step** (§0) — gated by the **same
  `[alerts] enabled` + sub-flags** as the CLI (default OFF). When alerts are off or there is no crossing,
  the banner is absent and the tray shows the plain meter fill.
- **NO OS desktop notifications** in v0.6.0 (the notify-rust deferral holds). The bar's "notification" is
  the passive tray-icon state + the banner — no daemon, no pop-ups.

---

## 8. Refresh & threading

- **Cadence:** background re-`collect_local_snapshot` on a **slow timer (~30 s, battery-friendly)** —
  far slower than the TUI's 2 s `--live`, since the bar is always-on — plus an **immediate refresh on
  window-show** and a **manual ⟳** (and the tray "Refresh now").
- **Off the UI thread:** `collect_local_snapshot` is synchronous file I/O; run it on a **worker thread**
  and hand the fresh `EngineSnapshot` back to the egui frame, so collection never hitches the UI. One
  snapshot fans out to all panels (same as the TUI).
- **Config** (`[budget]`/`[alerts]`) is read at startup (zero-config defaults when absent) and re-read on
  manual refresh; a malformed config surfaces the existing typed error in-window, never a crash.

---

## 9. Connect feature (display-only in v0.6.0)

Behind the bar's `connect` feature (off by default), the **Providers** panel shows the read-only
connection entries (`is_connected` + key-present, no network) exactly like the TUI's Providers tab.
**No connect/disconnect/reconcile actions in the GUI for v0.6.0** — those remain CLI, so the bar
introduces **no new credential or network surface**. The default `costroid-bar` build links no
network/keychain crate.

---

## 10. Cross-platform

- macOS menu-bar extra · Windows system tray · Linux tray via StatusNotifierItem/AppIndicator.
- **Document supported Linux desktops** — the Linux tray is fragile across environments; the toggle-
  window model (vs popover) reduces the blast radius. State the tested desktops in the README; degrade
  to "window only, no tray" honestly where SNI is unavailable rather than crash.

---

## 11. Invariants & gates (must hold)

- **No new network / data path / telemetry**; no new core compute; the bar is a pure core consumer.
- **Offline-acceptance + forbidden-crates parity:** extend the existing guarantees to the **new binary** —
  the default `costroid-bar` build must make **zero** network calls (add it to the offline-acceptance /
  forbidden-crates coverage so a future GUI dep that phones home fails the gate), and `connect` stays the
  only path to network/keychain.
- **`cargo deny` green** over the new egui/tray subtree (the ⛔ license check, §2).
- **Accessibility:** AccessKit on; never color alone; keyboard-navigable.
- **No `unwrap`/`expect`/`panic!`** in any library path; `apps/bar` is a binary so `anyhow` is fine, but
  the UI must degrade (a failed collect → a visible "refresh failed" state, never a panic/crash).
- **Release:** cargo-dist gains a **second binary** (`costroid-bar`) — decide whether it ships in the same
  release artifacts or its own; keep `precise-builds` correct (the bar is connect-OFF by default, like the
  CLI binary). This is a release-mechanics ⛔ at the end of Step 6.

---

## 12. Build sequencing (within Step 6 — each a fresh-agent card per §12.0)

The plan rates Step 6 **XL / "poor" for auto-mode** (§308) and a **human-gated step** (§291). Sequence it:

- **T18 — Scaffold + tray + window shell + collect/refresh** (⛔ deps): the `apps/bar` crate, the
  eframe app, the `tray-icon` (state-encoded, non-color-safe) + tooltip, the toggle-window, the
  worker-thread `collect_local_snapshot` refresh loop, the connect feature gate. *The ⛔ dependency-
  license review lands here.*
- **T19 — Overview + meters + alert banner:** the header (period spend + the quota meters across all five
  availability arms + "as of" stamp) + the `active_alerts` banner; the `SemanticStyle`→egui palette +
  the mandatory non-color cue; AccessKit labels for these.
- **T20 — the four live panels:** Budget, Forecast, Anomalies, Providers — each mapping its core view to
  egui, honest degraded states throughout.
- **T21 — AccessKit pass + cross-platform + offline/deny/release wiring (⛔ release):** the a11y audit,
  the supported-desktop matrix, the offline-acceptance/forbidden-crates extension to the new binary, the
  `cargo deny` confirmation, and the cargo-dist second-binary release wiring + the v0.6.0 cut.

Each card is built fresh-context + the §11.1 independent-review loop, exactly as T11–T17/T16b/T17b were.

---

## 13. Open items to confirm at build time (not blockers to the pin)

*(Resolved at sign-off 2026-06-17: chrome font = JetBrains Mono, bundled (Apache-2.0 — JetBrains Mono v2.x
relicensed from OFL); Neue Haas Grotesk not bundled. Warm SYNC ramp = not used in the taskbar.)*

- **Dot-grid glyph generation — ✅ T18:** the 9 `C⠉`+severity tray bitmaps are hand-rasterized RGBA (a
  64×64 `C` arc + a 3×3 dot grid), deterministic per step, in `apps/bar/src/glyph.rs`; the unit-square dot
  geometry is shared with the in-window egui painter. The fraction→0–8 *curve* (`severity_step`, a linear
  round-with-min-visibility-floor, 8 reserved for ≥100%) and the step→color ramp *hexes* were chosen at build
  time (the pin fixed the named colors, not the exact values) — T19's in-window braille meters should reuse
  `severity_step` + this geometry so the language stays identical edge-to-edge, and may refine the hexes.
- **In-window braille meter — ✅ T19:** §6's "row of braille cells … in JetBrains Mono so the cells align"
  assumed braille glyph coverage that the bundled JetBrains Mono lacks (T18 `fonts.rs`), so the meter is a
  **PAINTED** `W = 12` row of 2×4 dot cells (`apps/bar/src/meter.rs::paint_bar`, `painter.circle_filled` —
  the same primitive as the tray mark), NOT typeset braille. Fill **length** = the TUI's `meter_segments`
  (floor + boundary half-cell + min-visibility); fill **tint** = the 0–8 ramp (`severity_step` +
  `glyph::step_fill_color`). The never-color-alone cue is the dot **density**; the ramp tint is secondary. It
  reuses `glyph.rs`'s color toolkit + `severity_step` (the geometry is meter-specific — `glyph`'s 3×3
  `dot_centers`/`DOT_RADIUS` are the mark-grid's). All five `LimitAvailability` arms render honestly per the
  CLI's `render_limit_line`; no degraded arm paints a confident fill (§6).
- **Money display for a `rust_decimal`-free bar — ✅ T19:** the period-spend header + every `$` route through
  two new pure `costroid-core` helpers — `now_api_spend_display(&NowSummary)` (the `~`-hedged API-lane spend,
  mirroring the CLI now-header) and `format_money_usd(&Decimal, estimated)` — so money stays `Decimal` in the
  engine and `apps/bar` names no money type (no `rust_decimal` dep; `Decimal`s flow through by inference). The
  `Estimated` arm carries the estimate-labeled `~$` suffix exactly as the CLI does. **Signal-lime** is used
  sparingly in T19 (a thin header accent rule); the active-tab/selected-row lime arrives with T20's tab strip.
- **egui renderer — ✅ T18: `glow`** (not `wgpu`) — it trims the transitive tree and licenses cleaner (no
  `wgpu-hal`/`naga` graphics stack), confirmed by `cargo tree`/`cargo deny`.
- **egui persistence for window size/pos — ✅ T18:** eframe `persist_window: true` + the `persistence`
  feature; it pulls `ron`+`directories` (permissive, no network), no blocker.
- **⚠ NEW (T18) — AccessKit vs the async-io ban (T21 must resolve):** egui's Linux AccessKit backend
  `accesskit_unix` pulls `zbus` → `async-io`, which the offline/forbidden-crates gate bans workspace-wide. With
  `apps/bar` now an `offline.rs` root, AccessKit-on turns the default gate RED, so **T18 ships AccessKit OFF.**
  T21 (the AccessKit card) must reconcile the required AccessKit obligation with the no-async-runtime invariant
  — e.g. a reviewed `apps/bar` subtree allowlist (`CONNECT_ALLOWED` precedent) or an explicit policy carve-out
  — then re-enable it.
- **⚠ NEW (T18) — MSRV:** `eframe`/`egui` 0.34 require **Rust 1.92** (> the workspace's 1.88). `apps/bar`
  declares `rust-version = "1.92"`, and the CI MSRV job now **excludes `apps/bar`** (`cargo check --workspace
  --all-targets --exclude costroid-bar`) so the CLI + libraries stay tested at 1.88 (no CLI MSRV bump); T21/
  release may revisit raising the whole workspace to 1.92.
- **⚠ NEW (T18) — one `MPL-2.0` dep:** `tray-icon` → `dirs` → `dirs-sys` → `option-ext` is MPL-2.0
  (file-level copyleft, outside the GPL/AGPL/LGPL/SSPL ban), not droppable without forking the tray crate — a
  single `MPL-2.0` allow was added to `deny.toml` under the T18 ⛔ gate.
- **Linux SNI reliability + the tested-desktop matrix — T21.** (T18: Linux tray runs on a dedicated GTK-main
  thread and degrades to window-only on failure; macOS/Windows tray paths compile but are unverified on the
  Linux dev box. Linux appindicator left-click-activate is unreliable → the menu's "Open Costroid" is the show
  path.)
- **⚠ NEW (post-T18) — GTK3 `unmaintained` RustSec advisories ignored:** the Linux tray's archived gtk-rs
  GTK3 stack (atk/gdk/gtk/`*-sys`/gtk3-macros + `proc-macro-error`) trips 8 `unmaintained` advisories with no
  safe upgrade; the IDs are in `deny.toml` `[advisories].ignore` (justified, confined to `apps/bar`). **T21
  re-evaluates** when `tray-icon` ships a gtk4 / gtk-free Linux backend. NOTE: a bar dep change must run the
  ONLINE `cargo deny check advisories`, not just `licenses bans` (the offline gate misses this — the T18 gap).
- cargo-dist two-binary packaging (same release vs separate) + installer/Homebrew/npm implications — **T21.**
