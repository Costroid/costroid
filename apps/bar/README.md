# costroid-bar

The Costroid **taskbar** — an always-on tray glance plus a small live cockpit window for
what your AI coding tools cost and how close you are to your subscription limits. It is a
pure consumer of `costroid-core`: every figure it shows the engine already computes, from
local data, with no extra network call and no telemetry.

> **Status: live cockpit (T18→T20 built / Step 6, v0.6.0 in progress).** The tray glance (the
> `C⠉` dot-grid of your most-constrained quota meter), the toggle window, and the background
> refresh loop are up (T18); the window now shows the Overview — the period-spend header + the
> painted dot/braille quota meters (T19) — plus the tab strip, the opt-in alert banner, and the
> four live panels: Budget, Forecast, Anomalies, Providers (T20). The AccessKit pass, the
> cross-platform supported-desktop matrix, and the release wiring are T21 (the v0.6.0 cut).

## Scope — "glance + live cockpit"

The taskbar's value over the terminal UI is the **always-on glance** and **live status**,
not deep analysis. So it ships:

- a **tray icon** — the most-constrained quota meter at a glance, in the 9-step
  dot-density warning language (non-color-safe by construction), with a full tooltip;
- an **Overview** header — this-period spend, every quota meter, the alert banner;
- four **live panels** — Budget, Forecast, Anomalies, Providers.

**Trends, Models, History, and Frontier stay in the terminal app (`costroid`)** — they are
cramped in a small tray window, and the TUI serves them well. Run `costroid` for those.

## Connections (display-only)

Built with `--features connect`, the Providers panel *displays* read-only connection state.
Connecting, disconnecting, and reconciling stay in the CLI (`costroid connect …`), so the
taskbar introduces no new credential or network surface. The default build links no
network or keychain crate.

## Platforms

macOS menu-bar extra · Windows system tray · Linux tray via StatusNotifierItem /
AppIndicator. The Linux tray is fragile across desktop environments; where it is
unavailable the app degrades to **window-only** rather than failing. The tested-desktop
matrix is finalized in T21.

## Fonts

Bundles **JetBrains Mono** Regular v2.304 (**OFL-1.1**; `assets/JetBrainsMono-Regular.ttf`,
license in `assets/JetBrainsMono-LICENSE.txt`) so the chrome needs no system font. OFL-1.1 is
a permissive, non-copyleft font license — fine to bundle in Costroid's Apache-2.0 binaries.
