# costroid-bar

The Costroid **taskbar** — an always-on tray glance plus a small live cockpit window for what
your AI coding tools cost and how close you are to your subscription limits. A pure consumer of
`costroid-core`: every figure it shows the engine already computes, from local data, with no
extra network call and no telemetry. Part of the [Costroid](../../README.md) workspace.

## Scope — "glance + live cockpit"

The value over the terminal UI is the always-on glance and live status, not deep analysis:

- **Tray icon** — the most-constrained quota meter at a glance, in the 9-step dot-density
  warning language (non-color-safe by construction), with a full tooltip.
- **Cockpit window** — an Overview (period spend + painted dot/braille quota meters + the
  opt-in alert banner) over a tab strip to four live panels: Budget, Forecast, Anomalies,
  Providers. Renders the brand palette in true-color, lean by design (per-model `Series` hues,
  colored state chips paired with their word, a filled lime active-tab chip; severity stays the
  dot grid, never `!`/`!!`).

**Trends, Models, History, and Frontier stay in the terminal app (`costroid`)** — run that for
deep analysis.

## Connections (display-only)

Built with `--features connect`, the Providers panel only *displays* read-only connection state;
connecting/disconnecting/reconciling stay in the CLI (`costroid connect …`), so the taskbar adds
no new credential or network surface. The default build links no network or keychain crate.

## Platforms

A toggle window (the robust core) plus a best-effort native tray that **degrades to window-only**
wherever it can't be created (never a crash; `src/tray.rs`).

| Platform | Tray mechanism | Status |
|---|---|---|
| **Linux** (X11/Wayland) | StatusNotifierItem / AppIndicator on a GTK-3 thread | **Built + run.** Needs an SNI host (KDE native; GNOME needs the AppIndicator extension); else window-only. Right-click → "Open Costroid" is the canonical show (left-click toggle is unreliable across desktops). |
| **macOS** | Menu-bar extra (eframe event loop) | **Compiles; UNVERIFIED** — no macOS hardware in the build env. |
| **Windows** | System tray (eframe event loop) | **Compiles; UNVERIFIED** — no Windows hardware in the build env. |

**Accessibility:** AccessKit on (Linux AT-SPI / macOS NSAccessibility / Windows UI Automation) —
painted widgets (meters, alert badges, tray mark, refresh button, sparkline) carry accessible
names; the dot-density (never-color-alone) cue holds throughout. The Linux AT-SPI backend speaks
local D-Bus (AF_UNIX) only, never network (proven by `scripts/offline_acceptance.sh` + the
per-binary allowlist in `apps/cli/tests/offline.rs`).

**Release shape (v0.6.0):** because the macOS/Windows tray paths are unverified, `costroid-bar`
ships as binary **archives** + `cargo install costroid-bar`. The one-click **npm/Homebrew**
installers stay CLI-only until the desktop matrix is field-verified.

## Fonts

Bundles **JetBrains Mono** Regular (OFL-1.1; `assets/JetBrainsMono-Regular.ttf`) so the chrome
needs no system font — a permissive, non-copyleft license, fine in Costroid's Apache-2.0 binaries.
