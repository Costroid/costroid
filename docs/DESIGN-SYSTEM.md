# Design system

Costroid's visual language: the semantic palette, the braille primitive, the components, the accessibility fallbacks, and the voice. **The CLI/TUI renders in color**; the egui taskbar (`apps/bar`) renders the same language in true-color. The render-mode plumbing (`Braille`/`Ascii`/`Plain`, capability detection, terminal restore) lives in [ARCHITECTURE.md](ARCHITECTURE.md); this doc defines *what gets drawn*.

**One thing first:** a terminal cell has a single foreground color. Braille gives sub-cell *shape* (2×4 dots) but not sub-cell *color*, so used-vs-remaining is carried by **glyph shape** (`⣿` vs `⣀`) — the load-bearing cue that survives `NO_COLOR`/`--plain`/color-blindness — with color layered on top, never the only signal. (Sub-cell two-tone "solid dot vs hollow ring" lands only on raster surfaces — the taskbar, web, marketing.)

## Palette — the `SemanticStyle` system (CANON)

One enum `SemanticStyle` (in `apps/cli/src/render.rs`) drives **both** the one-shot ANSI path (`StyledSpan::render`) and the TUI (`ratatui_style` → `Color::Indexed`) from one palette, so the surfaces never drift. Renderers assign *semantic* styles — **never a raw ANSI code or a `ratatui::Color`**.

| `SemanticStyle` | xterm-256 | Role |
|---|---|---|
| `Plain` | default fg | body text |
| `Muted` | `245` (Ash) | labels, captions, scope, sub-notes, inactive tab, hint descriptions |
| `Strong` | bold | dollar figures, model names, the active metric |
| `Accent` | `154` (Signal-lime), bold | the `C⠉` mark, the active tab/selection, the `◆` insight marker, the live badge — **sparingly** |
| `Data` / `DataDim` | `39` / `24` (cyan / dim cyan) | the used fill / unfilled track of a quota meter / budget bar / spend sparkline |
| `Series(n)` | `SERIES_PALETTE` = `38 79 75 141 215 210`, cycled by rank | per-category coding — the n-th model's `●`/`*` dot + spend-bar fill. **Lime `154` is excluded** (reserved for `Accent`). |
| `Heat(0..=4)` | `HEAT_PALETTE` = `239 38 154 215 210` (idle→cyan→lime→gold→coral) | density cells (Activity heatmap); the glyph ink `·░▒▓█` encodes the level too |
| `Warn` / `Critical` | `33;1` / `31;1` (amber / red) | near-/over-limit — **always** paired with a `!`/`!!`/`OVER` text cue |

**Three load-bearing rules (enforced by tests):**

1. **Color layers on a shape/text cue, never alone.** Meter = glyph shape; per-model row = its `●` dot + name; heat = its ink ramp; warning = its `!`/`!!`/`OVER` word; active tab = reverse-video. (Covers `NO_COLOR` + color-blindness.)
2. **All color is gated on `options.ansi`.** `--plain`/`NO_COLOR` emit zero escapes; the bytes are identical to the colored render and **pure ASCII** in `Plain`/`Ascii` (the `*_is_pure_ascii` tests pin this).
3. **One color lane only.** Cyan + Signal-lime + neutrals + the `Series`/`Heat` ramps. No third-party hue (no Claude purple), no warm SYNC coral in the CLI/taskbar.

**Amber/red is reserved** for *near-/over-limit* and over-budget only — advisory surfaces (Providers/Models/History/Forecast/Anomalies/Activity/reconcile) never use them but color freely from the rest. The legacy `*_document_is_monochrome` guards assert *no `Warn`/`Critical` span* (not the absence of color); keep that guard on a new advisory tab.

## Compose helpers

Build `StyledDocument`/`StyledLine`/`StyledSpan` and reuse the shared helpers so palette + accessibility come for free: `push_header_line` (brand lockup + scope/money), `push_section` (Muted label), `push_meter` (two-tone fill+track), `push_insight` (Accent `◆`), `push_caveat` (Muted sub-note), `series_style(rank)` / `mode_dot` (per-category dot), `mark` / `push_brand`, `push_provider_notes`. Add a new `SemanticStyle` variant only when no role fits — and map it in **both** `StyledSpan::render` and `ratatui_style`.

## Brand basics

Costroid reads as a **distinctive dot/braille terminal-native financial instrument — not a generic dashboard.**

- **Mark.** A pixel `C` beside braille `⠉` (dots 1,4 — top two, a meter at full): `C⠉`. On raster the mark's dots double as a live meter (the tray icon).
- **Wordmark.** `costroid`, lowercase, `cost` strong, `roid` muted — carried into the UI: figures/active-metric = strong, labels/context = muted.
- **Palette hexes (master).** Carbon `#0B0C0E` / Slate `#16181C` / Graphite `#2C2C2A` (backgrounds) · Ash `#888780` (muted) · Bone `#E9E7DF` (primary) · Signal `#C8FF3D` (lime accent) · the cold cyan data ramp `#042C53 #185FA5 #378ADD #85B7EB`. The terminal renders this lane in 256-color via the table above.
- **Typography.** JetBrains Mono for everything measurable and for chrome (Neue Haas Grotesk is commercial — web/marketing only, never bundled in the Apache-2.0 binaries). Braille renders in any monospace with braille coverage; the ASCII fallback covers the rest.
- **Tone.** Sparse, precise; dots are the accent and the data; minimal motion.

## The braille rendering primitive

Unicode braille (`U+2800`–`U+28FF`) packs 2 cols × 4 rows = 8 dots/char. **Compute glyphs from the codepoint, never via Ratatui's `Canvas` / `symbols::braille` constants** (TUI-only, drifts across versions, breaks one-shot/`--plain` parity):

```
dot layout   bit values        glyph = char::from_u32(0x2800 + bitmask)
 1  4         1   8             ⠉ (1,4) = 0x2800+1+8   ⣿ (all) = 0x2800+255
 2  5         2   16            ⡇ (1,2,3,7) = 0x2800+71
 3  6         4   32
 7  8         64  128
```

Two patterns over this primitive: **styled glyph runs** (horizontal meters/bars/statusline) and a **hand-rasterized 2-D plot** (the sparkline — bucket values, set bottom-up dot bits). One styled document feeds the TUI and the one-shot/`--plain` adapters identically. In `Ascii` the rasterizer emits a `.:-=+*#` ramp; in `Plain` it emits the plain-text substitute.

## The 9-step dot-density warning ladder

Severity is encoded by the **count/fill of dots in a 3×3 grid** (`0 idle → 8 critical`, plus a color progression idle→green→yellow→orange→red→brown→black), so it reads in grayscale and for color-blind users — this **IS** the never-rely-on-color-alone guarantee. Map a consumed quota fraction / alert level onto `0–8`. **Raster (taskbar/web):** the painted dot grid. **Terminal:** the `Data`→amber→red meter color **plus** bright/dim glyph shape **plus** the `!`/`!!`/`OVER` (or `? unverified`) text cue — same severity semantics; the text cue keeps it readable when color is stripped.

## Components

Bright dots = used (`Data` cyan), dim = remaining (`DataDim`), amber/red = warning (always with a non-color cue). Labels/scope render Muted; figures/model-name Strong; the `C⠉` mark + `◆` marker Accent.

- **Limit meter (5h / weekly).** A run of `W=12` braille cells via `meter_segments` (floor + one boundary half-cell `⡇` + a min-visibility half for any nonzero usage; track = light `⣀`, never a full cell). Thresholds `WARN_FRACTION=0.80` / `CRITICAL_FRACTION=0.95` (over at `f≥1.0`). Cue strings are per-mode (`state_cue`/`plain_state_phrase`): `Braille`+`Ascii` append ` !` / ` !!` / ` !! OVER`; `Plain` (no meter) spells ` (near limit)` / ` (critical)` / ` (over limit)`. A cross-check-failed reading draws **neutral** (never amber/red) with ` ? unverified`. Readings ≥~10 min old carry an `as of HH:MM` (UTC) stamp (`capture time unknown` if none). Example: `⣿⣿⣿⣿⣿⣿⣿⣿⣿⣀⣀⣀ 78%  resets 2h 14m`.
- **Measure variants.** Spend pool → `$used / $included used`, no meter, never a fabricated %. Estimated → `usage: N tokens (~$value, estimated) — quota % unavailable`, price omitted if unpriced. Unavailable → `unavailable: <reason>`. Claude windows carry the claude.ai chat caveat sub-note.
- **Spend sparkline.** Hand-rasterized braille, ink only (`H=4`), `h_i = clamp(round((v_i/max)*H), v_i>0?1:0, H)`, drawn bottom-up in `Data`. Sparse period labels.
- **API cost bar.** Same `meter_segments` primitive, one row/model sorted desc; each row color-coded by `Series(rank)` — leading `●` dot + bar fill take the model hue, track stays `DataDim`, figure right-aligned Strong. Cost bars never go amber. Example: `● claude opus 4.8   ⣿⣿⣿⣿⣿⡇⣀⣀⣀⣀⣀⣀   $24.10`.
- **Reconciliation (`costroid reconcile`).** A plain table (no meter, no amber); direction is text (`over`/`under`/`exact`), signed variance = local − billed, % at 1 dp. Typed vendor-side absence is text (`report doesn't cover this day` / `not attributed by the vendor` / `connect <vendor> first` / `the invoice request could not be completed`), variance `—`. ASCII-folds in `--plain`/Ascii.
- **Statusline glyph.** One fixed line — mark, hedged spend, a `STATUS_BAR_WIDTH=4` meter, pct, state cue, compact reset (`amber + !` at ≥warn; neutral `? unverified`). Flags: `--capture-only`, `--wrap '<cmd>'`. Example: `C⠉ ~$4.18  ⣿⣿⣿⣀ 78% 2h14m`.
- **Spinner.** `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` (~80 ms/frame); ASCII `| / - \`.

**Reset-countdown format** — two largest non-zero units: `≥1d` `"{d}d {h}h"`; `≥1h` `"{h}h {m}m"`; `<1h` `"{m}m"`; `<1m` `"<1m"`.

## TUI chrome — tab strip & hint bar

The TUI frames content between two one-line bars (`draw_app` in `tui.rs`):

```
1 now 2 trends 3 prov 4 models 5 history 6 budget 7 fcast 8 anom 9 actv   ← tab strip
C⠉ costroid                                   this week  $42.18           ← header
 now  live  ·  1-9/tab switch  ·  a frontier  ·  r refresh  ·  ? help  ·  q quit  ← hint bar
```

- **Tab strip.** Nine numbered tabs; the active tab is a reverse-video lime chip (reverse = the non-color cue), the rest `N name` Ash-muted. Single-space separators + a strip-only clip of the four over-long labels (`tab_strip_label`: providers→`prov`, forecast→`fcast`, anomalies→`anom`, activity→`actv`) keep all nine inside 80 cols. The full name shows in the footer chip, `?` help, and header. The `a`/`esc` Frontier overlay appends its own chip.
- **Hint bar.** The current screen as a lime chip, a `live`/`manual` badge, then contextual keys (keys lime, labels muted; Trends adds `d/w/m/y period` + `g group`).

**Activity heatmap (`9 activity`).** A GitHub-style contribution grid (columns = ISO weeks ≤52, rows = weekdays Mon→Sun with Mon/Wed/Fri labeled), each cell's token volume mapped to `Heat(0..=4)` — ink `·░▒▓█` (ASCII `.:+*#`) AND color rise together. Below it a Stats panel (`total tokens` / `active days` / `busiest day` / `top model` / `streak`, label Muted + value Strong) and exactly one hedged `◆ … (rough)` comparison line (the one sanctioned playful line). `Plain` draws no grid — the labeled facts are the screen-reader view.

## Taskbar (`costroid-bar`)

The egui/eframe + `tray-icon` app renders the **same palette** in true-color — a lean glance surface, not a CLI clone. Pure `costroid-core` consumer: no new data path, no compute, no network beyond what `costroid-connect` authorizes (display-only, behind its own off-by-default `connect` feature), no telemetry.

- **Tray icon (the glance).** The `C⠉` mark whose 3×3 dot grid IS the warning meter — encodes the most-constrained `Available` quota window in the 0–8 dot-density language (9 pre-rendered RGBA bitmaps in `glyph.rs`, swapped per `severity::severity_step`). A degraded reading (Unverified/Estimated/Partial/Unavailable) shows the idle/`?` muted grid, never a confident fill. Tooltip carries the precise line + `as of HH:MM`. Left-click toggles the window; right-click → Open / Refresh now / Quit.
- **Window (toggle).** A resizable eframe window (persisted size/pos, refresh-on-show). Header = the Overview (period spend `~`-hedged + meters + the opt-in alert banner); a tab strip switches Overview/Budget/Forecast/Anomalies/Providers, each mapping ONE core view fn.
- **Painted meters.** Quota/cost meters are **painted** 2×4 dot cells (`meter.rs::paint_bar`, `painter.circle_filled`) — fill length = the TUI's `meter_segments`, tint = the 0–8 ramp (`glyph::step_fill_color`). Per-model rows lead with a `Series(rank)` legend dot + a single-row `Series`-hued share dot-bar (`app::SERIES` / `series_color`, mirroring the CLI). All five `LimitAvailability` arms render honestly.
- **Colored state chips** (`app::chip` — a low-alpha tint + the word, named for AccessKit): Providers health (green available · cyan detected · amber partial · red error · Ash missing), Budget pace (green on-track · amber ahead · red over-budget), connection state. The **word always pairs the color**; **never color alone** — severity is still the 0–8 dot grid (tray / meters / alert badges), never `!`/`!!`.
- **AccessKit ON** (the bar's default `accesskit` feature) — every painted widget (meters, chips, share bars, tabs, badges, the `C⠉` mark, the refresh button) carries an accessible name; it is the GUI's `--plain` analogue. Lime stays reserved (the active tab is a filled lime chip with Carbon ink). The header carries the `· estimates` caveat once; the Claude chat caveat shows once. Money routes through core display helpers (`now_api_spend_display` / `format_money_usd` / `format_over_by_usd` / `decimal_share_percent` / `anomaly_multiple_phrase` / `forecast_daily_fractions` / `now_model_spend_breakdown`) so the bar names no `rust_decimal`.

## Accessibility

`--plain` produces no color, no braille, linear top-to-bottom with every value labeled + united + contextual, built to be read aloud. Every Costroid-generated byte in `Plain`/`Ascii` is pure ASCII (`plain_mode_output_is_pure_ascii` / `ascii_mode_output_is_pure_ascii`); provider-supplied names pass through verbatim. The amber/red state is **always** paired with its text cue; the unverified state always carries ` ? unverified` with a neutral meter — never a confident alarm without color.

**ASCII substitutes (illustrative):**

```
limit meter   "[###########-] 92% !  resets 41m"   (# used, + half, - track; same !/!!/!! OVER cues)
   Plain       "claude code 5h: 92% used (near limit), resets in 41m"
unverified    "claude code 5h: 92% used ? unverified, resets in 41m  as of 14:03"
sparkline     a labeled numeric list, or a .:-=+*# height ramp
cost bar      "claude opus 4.8   $24.10   (57%)"
statusline    "costroid ~$4.18  [###-] 78% 2h14m"
spinner       "| / - \"  or  "working..."
```

If braille is unsupported, Costroid auto-downshifts to the block / `.:-=+*#` ramp and ASCII `[####--]` without being asked; piped/non-TTY output is always plain.

## Voice & copy

The insight line sounds like a colleague: proactive, plain, specific, brief — fact, then so-what, then (optionally) a next step. Never alarmist, chatty, or a chat box.

- Cost is an estimate — hedge (`~`, "estimated", "about"); never claim certainty about inferred money.
- Recommendations are advisory + **sourced**, and attach only to API-cost lines — never to subscription limits.
- One insight at a time; quiet by default; never block or demand a response.
- Sentence case, no emoji, no exclamation spam, no fake urgency, no greetings.
- **One sanctioned exception:** the Activity/Stats tab may carry exactly one hedged `(rough)` comparison line per render — factual, never false precision, never generalized to other surfaces.

**Good:** "You're pacing toward ~$58 this week — Opus drove most of it. (estimated)" · "Sonnet could cover ~40% of these tasks at about ⅓ the cost. Advisory — sources: DeepSWE, CursorBench (vendor)."
**Avoid:** "🚨 WARNING!! SPENDING TOO MUCH!!!" · "This will save you exactly $19.42." · "Hi friend! Want to chat?"
