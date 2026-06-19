# costroid-bar

The Costroid **taskbar** — an always-on tray glance plus a small live cockpit window for
what your AI coding tools cost and how close you are to your subscription limits. It is a
pure consumer of `costroid-core`: every figure it shows the engine already computes, from
local data, with no extra network call and no telemetry.

> **Status: Step 6 complete — release-ready at v0.6.0 (T18→T21 built).** The tray glance (the
> `C⠉` dot-grid of your most-constrained quota meter), the toggle window, and the background
> refresh loop are up (T18); the window shows the Overview — the period-spend header + the
> painted dot/braille quota meters (T19) — plus the tab strip, the opt-in alert banner, and the
> four live panels: Budget, Forecast, Anomalies, Providers (T20). T21 turned **AccessKit on**
> (a screen reader announces each painted meter, alert, and tab), documented the
> cross-platform matrix below, extended the offline/forbidden-crates/deny guarantees to this
> binary, and wired the cargo-dist release. The actual v0.6.0 tag + publish is the maintainer's
> manual cut.

## Scope — "glance + live cockpit"

The taskbar's value over the terminal UI is the **always-on glance** and **live status**,
not deep analysis. So it ships:

- a **tray icon** — the most-constrained quota meter at a glance, in the 9-step
  dot-density warning language (non-color-safe by construction), with a full tooltip;
- an **Overview** header — this-period spend, every quota meter, the alert banner;
- four **live panels** — Budget, Forecast, Anomalies, Providers.

**Trends, Models, History, and Frontier stay in the terminal app (`costroid`)** — they are
cramped in a small tray window, and the TUI serves them well. Run `costroid` for those.

### Look & feel (refreshed 2026-06-19)

The cockpit renders the brand's evolved color language in true-color, **lean by design** — a glance
surface, not a CLI clone:

- **Per-model color** matching the terminal: the Overview "by model" rows lead with a spend-rank
  `Series` legend dot + a single-row share dot-bar in that hue.
- **Colored state chips** — Providers health, Budget pace, and connection state each pair a color
  with its word (never color alone). The active tab is a filled **Signal-lime** chip; severity is
  still the 9-step dot grid (tray mark, meters, alert badges), never `!`/`!!`.
- **Less text:** the header status carries the `· estimates` honesty caveat once (every `$` is still
  `~`-hedged + estimate-labeled), so panels drop the per-panel scope/estimate notes the CLI keeps;
  the Budget "no budget set" state is a 2-line hint, and Providers shows the cost + quota pillars.

## Connections (display-only)

Built with `--features connect`, the Providers panel *displays* read-only connection state.
Connecting, disconnecting, and reconciling stay in the CLI (`costroid connect …`), so the
taskbar introduces no new credential or network surface. The default build links no
network or keychain crate.

## Platforms

The taskbar uses a toggle window (not a popover) plus a native tray; the window is the robust
core and the tray is a best-effort glance that **degrades to window-only** wherever it cannot
be created (no crash). The honest support matrix:

| Platform | Tray mechanism | Status |
|---|---|---|
| **Linux** (X11 / Wayland) | StatusNotifierItem / AppIndicator on a dedicated GTK-3 thread (`tray-icon` → `libappindicator`) | **Built + run on the Linux dev box.** Tray visibility depends on the desktop having an **SNI host** (KDE Plasma natively; GNOME needs the *AppIndicator/KStatusNotifierItem* extension; bare wlroots/Sway via `waybar`/`status-notifier-item`). Where no SNI host is present, the app runs **window-only**. |
| **macOS** | Menu-bar extra, pumped by eframe's event loop | **Compiles; UNVERIFIED.** No macOS hardware in the build environment — the code path is built but not field-tested. |
| **Windows** | System tray (notification area), pumped by eframe's event loop | **Compiles; UNVERIFIED.** No Windows hardware in the build environment — built but not field-tested. |

Degradation is deliberate and tested: if `gtk::init()` or the tray build fails (Linux), or the
tray cannot be created (macOS/Windows), `costroid-bar` logs a one-line note to stderr and runs
**window-only** — never a crash (`src/tray.rs`).

**Linux interaction note:** AppIndicator left-click-to-toggle is unreliable across environments,
so the **right-click menu's "Open Costroid"** is the canonical way to show the window on Linux
(left-click toggle works where the SNI host forwards activation, e.g. KDE).

**Accessibility:** AccessKit is on — Linux AT-SPI (`accesskit_unix`), macOS NSAccessibility, and
Windows UI Automation. The painted widgets (the quota meters, the alert badges, the tray mark,
the refresh button, the forecast sparkline) carry accessible names, so a screen reader announces
each; the never-color-alone dot-density cue holds throughout. The Linux AT-SPI backend speaks
local D-Bus (AF_UNIX) only — never network (proven by `scripts/offline_acceptance.sh` + the
per-binary static allowlist in `apps/cli/tests/offline.rs`).

**Release shape (v0.6.0):** because the macOS/Windows tray paths are unverified, `costroid-bar`
ships as downloadable binary **archives** + `cargo install costroid-bar` (crates.io). The
one-click **npm/Homebrew** installers stay **CLI-only** (`costroid`) until the desktop matrix is
field-verified; the GUI joins them in a later 0.6.x once macOS/Windows are confirmed.

## Fonts

Bundles **JetBrains Mono** Regular v2.304 (**OFL-1.1**; `assets/JetBrainsMono-Regular.ttf`,
license in `assets/JetBrainsMono-LICENSE.txt`) so the chrome needs no system font. OFL-1.1 is
a permissive, non-copyleft font license — fine to bundle in Costroid's Apache-2.0 binaries.
