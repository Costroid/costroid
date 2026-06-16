# Design system

This document specifies Costroid's visual language and TUI/CLI UX in enough detail to implement: the brand, the braille rendering primitive, each component's dot math and states, the two screens, the accessibility fallbacks, and the voice. The render-mode plumbing (`Braille` / `Ascii` / `Plain`, capability detection, terminal restore) is in [ARCHITECTURE.md](ARCHITECTURE.md); this document defines what gets drawn.

One thing to internalize up front, because it shapes every component: **a terminal cell has a single foreground color.** Braille gives sub-cell *shape and texture* (2×4 dots per character) but not sub-cell *color*. So in the terminal, "used vs remaining" is expressed as **bright vs dim cells**, and fills advance at cell granularity (use more cells for finer reads). The "solid dot vs hollow ring" look from the design mockups is only achievable in **raster/SVG** surfaces (the website, marketing) — there it's allowed; in the terminal, ring → dim dot.

## Brand basics

- **Mark.** A pixel `C` beside the braille cell `⠉` (dots 1 and 4 — the two top dots, i.e. a meter at full). The mark reads `C⠉`.
- **Wordmark.** `costroid`, lowercase, with `cost` in the strong weight/primary color and `roid` muted. Carry this split into the UI: dollar figures and the active metric use the strong weight; labels and context are muted.
- **Palette.** Monochrome — black / grey / white, adapting to the terminal theme via foreground/dim. A **single amber accent**, reserved exclusively for the warning/near-limit state. No other colors. (Critical/over-limit may use red as an intensification of the warning state; still always paired with a non-color cue.)
- **Font.** JetBrains Mono. Braille glyphs render in any monospace with braille coverage (JetBrains Mono, Cascadia Code, DejaVu); the ASCII fallback covers terminals without it.
- **Tone of the visuals.** Sparse and precise. Dots are the accent and the data; keep surrounding typography clean. Don't drown the screen in braille.

## The braille rendering primitive

Unicode braille (block `U+2800`–`U+28FF`) packs **2 columns × 4 rows = 8 dots** per character — the densest graphics a monospace terminal offers. Costroid draws its meters, bars, sparklines, spinner, and statusline glyph in these dots.

**Compute glyphs from the codepoint, not from library constants.** (Ratatui v0.30 changed some `symbols::braille` constants; computing directly is stable.) Each dot has a bit value; the cell layout and bits are:

```
dot layout      bit values
 1  4           1   8
 2  5           2   16
 3  6           4   32
 7  8           64  128

glyph = char::from_u32(0x2800 + bitmask)
```

Examples: `⠉` (dots 1,4) = `0x2800 + 1 + 8`; full cell `⣿` = `0x2800 + 255`; left column `⡇` (dots 1,2,3,7) = `0x2800 + 71`.

**Everything is hand-rasterized** — Costroid computes braille cells directly from the codepoint (above), **never** via Ratatui's `Canvas` or `symbols::braille` constants (`Canvas` is TUI-only and drifts across versions, which would break one-shot / `--plain` parity — ARCHITECTURE §7/§12). Two patterns over the same primitive:

1. **Styled glyph runs** — for horizontal meters and bars. Compose a row of braille cells, color the run. Simple, exact, no canvas. Used for meters, cost bars, the statusline glyph.
2. **Hand-rasterized 2D plot** — for the sparkline, which needs vertical resolution. Bucket the values, compute each column's dot-height, set the bottom-up dot bits in each cell's bitmask, and emit the glyph run (dot math under Spend sparkline). Because it's the same primitive as the meters, the **one styled document feeds the TUI and the one-shot/`--plain` adapters identically**.

In `RenderMode::Ascii` (braille unsupported), the same rasterizer emits a block / `.:-=+*#` ramp instead of braille; in `RenderMode::Plain`, no plot is drawn — emit the plain-text substitute (see Accessibility).

## Components

For every component below: bright dots/cells = used/spent (primary fg), dim = remaining (a muted gray), amber = the warning state — and amber is **always** accompanied by a non-color cue.

### Limit meter (5-hour and weekly)

A horizontal run of `W` braille cells (`W = 12`, a fixed const today; configurability arrives with the planned config file). Given a usage `fraction f ∈ [0, 1+]`:

```
full = floor(f * W)                          # full `⣿` cells (f ≥ 1 ⇒ full = W, no half)
half = (f * W − full) ≥ 0.5                  # one boundary half-cell `⡇` (dots 1,2,3,7)
if f > 0 and full == 0 and not half:
    half = true                              # min-visibility: any nonzero usage shows ⡇
track = W − full − (1 if half else 0)        # `⣀` cells
```

(This is `meter_segments` in render.rs — floor plus a half-cell, **not** `round(f * W)` full cells; the min-visibility cell is the half-cell `⡇`, never a full `⣿`.)

- Render `full` as `⣿`, the boundary `half` (when present) as left-column `⡇`, and the `track` as the light glyph `⣀` (dots 7,8 only) — never a full cell. As shipped, fill and track render in a single span sharing the line's style (plain, or amber/red near limit) — the **glyph shapes** distinguish used from remaining, so the meter reads correctly under `NO_COLOR` and color-blindness alike.
- **Thresholds** (fixed consts today — `WARN_FRACTION`/`CRITICAL_FRACTION` in render.rs; configurability arrives with the planned config file): `warn = 0.80`, `critical = 0.95`. Below warn, used color = primary. At ≥ warn, used color = amber; at ≥ critical (or `f ≥ 1.0`, over limit), used color = red. The cue is what makes the state readable without color, and **the exact cue string is per render mode** (as built — `state_cue` / `plain_state_phrase` in render.rs): in `Braille` **and** `Ascii` modes the cue appended after the percentage is ` !` (warn) / ` !!` (critical) / ` !! OVER` (over); in `Plain` mode (no meter at all) the cue is spelled out as ` (near limit)` / ` (critical)` / ` (over limit)`. Below warn there is no cue in any mode.
- **Unverified (cross-check-failed) reading.** When a quota reading fails the `rate_limits` sanitize/cross-check (ARCHITECTURE §9.2), the meter draws in a **neutral (non-alarm) color** — never amber/red even at a near-max fraction — and the threshold `!`/`!!`/`OVER` cue is replaced by the distinct color-free cue ` ? unverified`. A maxed-looking but unverified reading must never render as a confident alarm.
- **Freshness stamp.** Every `Available`/`Unverified` — and measure-carrying `Partial` — reading that is at least ~10 minutes older than the render carries an always-on `as of HH:MM` (UTC) stamp, so an hours-old cached reading never renders as a bare, confident meter. A reading with no recorded capture instant discloses `capture time unknown` instead.
- Always show the percentage and reset countdown beside the meter: `⣿⣿⣿⣿⣿⣿⣿⣿⣿⣀⣀⣀ 78%  resets 2h 14m`.

**Reset-countdown format** — compact, two largest non-zero units:

```
>= 1 day:  "{d}d {h}h"      e.g. "3d 4h"   (or "{d}d" if h == 0)
>= 1 hour: "{h}h {m}m"      e.g. "2h 14m"  (or "{h}h" if m == 0)
<  1 hour: "{m}m"           e.g. "46m"
<  1 min:  "<1m"
```

Each provider shows two meters (5-hour and weekly), labeled, stacked.

### Limit measure variants (Spend / Estimated / Unavailable)

Not every window meters a token fraction; the line is **measure-aware**:

- **Spend pool** (a dollar-denominated allowance): render `$used / $included used` (or `$used used` when no published allowance) — **no meter, never a fabricated %**.
- **Estimated** (quota source absent, volume inferred from local logs): render `usage: N tokens (~$value, estimated) — quota % unavailable` — no meter; the `~`/`estimated` hedge is mandatory and the price is omitted entirely when the model is unpriced (never guessed).
- **Unavailable**: render `unavailable: <reason>` — no meter, no number.

For Claude windows that show usage (Available / Unverified / Estimated), an indented sub-note carries the chat caveat: "reflects Claude Code's view; claude.ai chat usage may make true usage higher."

### Spend sparkline

A **hand-rasterized braille** plot of bucketed spend over the period — **ink only** (draw the data dots, no track), computed directly from the codepoint (no Ratatui `Canvas`; ARCHITECTURE §7). For `n` buckets with values `vᵢ` and `max = max(vᵢ)` (or a fixed ceiling), each bucket's height in dot-rows is:

```
h_i = clamp(round((v_i / max) * H), if v_i > 0 { 1 } else { 0 }, H)
```

where `H` is the vertical dot resolution (`H = 4`, i.e. 1 cell tall in the shipped renderer). Draw points at the bucket's x for rows `0..h_i` (bottom-up), in the primary color. Label the axis sparsely (period markers like `mon … sun`, `w1 … w4`, `jan … dec`). Linear scale by default. Bucket granularity follows the selected period.

### API cost bar

One horizontal dot bar per model, sorted by cost descending — drawn with the **same `meter_segments` primitive as the limit meter**. With `W` cells and `max = max(cost)`:

```
full  = floor((cost / max) * W)
half  = 1 when (cost / max) * W - full >= 0.5, else 0
        (min-visibility: if cost > 0 and full == 0 and half == 0, force half = 1)
track = W - full - half
```

Bright `⣿` for the `full` cells, the **left-column half-cell `⡇`** for the boundary `half`, and the dim **track glyph `⣀`** for the rest (all three shape-distinct, so the fill survives `NO_COLOR` — never the same glyph distinguished by color alone). The model name sits left in the strong weight; the dollar figure right-aligned in the strong weight (`Intl`-style, e.g. `$24.10`, `$1,840.00`). Cost bars never go amber — amber is for limits, not spend. Each row: `claude opus 4.8   ⣿⣿⣿⣿⣿⡇⣀⣀⣀⣀⣀⣀   $24.10`.

### Reconciliation section (`costroid reconcile`) — as built (T10c)

One section per vendor, comparing Costroid's **local estimate** against the vendor's **billed invoice** per completed UTC day + model. It surfaces the T9c `CostReconciliation` engine; the renderer is a pure function of that type (snapshot-tested). The layout is a plain monospaced table — **no braille meter** (reconciliation is numeric, not a fill), and **no amber** (amber is reserved for limits). Direction is carried as **text** (`over`/`under`), never color.

```
C⠉ costroid                                   anthropic  est ~$5.20 / inv $4.50
estimate vs invoice — 2026-06-08 to 2026-06-14 (UTC, completed days)
Local figures are estimates (your tokens x current prices); the vendor invoice is the source of truth.
────────────────────────────────────────────────────────────────
2026-06-13  est ~$1.00   report doesn't cover this day   —
    claude-opus-4-8        est ~$1.00   report doesn't cover this day   —
2026-06-14  est ~$4.20   inv $4.50   -$0.30 under (-6.7%)
    claude-ghost-9         est ~$0.00   inv $0.50   -$0.50 under (-100.0%)
    claude-opus-4-8        est ~$3.00   inv $3.00   exact
    claude-sonnet-4-6      est ~$1.20   inv $1.00   +$0.20 over (+20.0%)
Note: Anthropic Priority-Tier spend isn't in this report — the bill may be higher.
Note: the invoice total covers only the days this report spans; days outside it show "report doesn't cover this day".
```

- **Header** — the mark + vendor + section totals: the estimate always `~`-prefixed (`est ~$5.20`); the invoice total (`inv $4.50`) only when a report was available.
- **Day / model rows** — each carries `est ~$X` (always estimate-marked), the invoice cell, and the variance cell. Per-model rows are indented under their day.
- **Signed variance** — `variance = local − billed`: `+$X over (+P%)` when the estimate exceeds the invoice, `-$X under (-P%)` when the invoice exceeds it, `exact` at zero. The percentage is rounded to a uniform **1 dp at the render boundary** (full `Decimal` precision is kept upstream); `% n/a` becomes `(vs $0 billed)` when the vendor billed `$0`.
- **Typed vendor-side absence is TEXT, never `$0`** — the invoice cell shows `report doesn't cover this day` (`DayNotCovered`), `not attributed by the vendor` (`ModelNotInReport`), or the typed report reason (`connect <vendor> first` for not-connected; Gemini's pinned `unavailable — no sanctioned static-key usage API`; a per-vendor hard fetch failure renders the detail-free `the invoice request could not be completed` (`FetchFailed`) — so one vendor failing never aborts a multi-vendor reconcile); the variance cell renders `—` (no fabricated delta). A **local `$0`** against a real billed figure (e.g. `claude-ghost-9`) is genuine — a model the vendor billed but Costroid never saw — and renders as a real row.
- **Caveats footnoted** — `priority_tier_absent` → the Priority-Tier note; `per_model_derived_best_effort` → footnote + a trailing `*` on each best-effort (OpenAI per-model) row; and when a report is available but doesn't span every local day (some day `DayNotCovered`), a footnote clarifies that the header `inv` total covers only the spanned days (so the headline `est / inv` pair isn't misread as a real over-estimate).
- **`--plain` / Ascii** — the `─` rule, the `—` dashes, and any `—`/`…`/`×`/`·` in a reason message all ASCII-fold (`-`, `-`, `...`, `x`, `-`) so Plain/Ascii output is pure ASCII (locked by `is_ascii()` asserts); braille keeps those glyphs. (The hedge's "tokens x current prices" is plain ASCII `x` in every mode, including braille.) The over/under words and `*`/footnotes survive every mode, so nothing depends on color.

### Statusline glyph (`costroid statusline`)

A single line, no newline, fast — for shell prompts, tmux, Starship. Side-effect-free on interactive stdin; with piped stdin (Claude Code's `statusLine` JSON) it opportunistically captures the `rate_limits` block into the local no-secret cache first (T5 path 2). It shows the current-period spend and the **most-constrained** limit as a short meter.

**As shipped**, the statusline emits **one fixed layout** — mark, hedged spend, a short meter (`STATUS_BAR_WIDTH = 4` cells, same fill rules as the limit meter), percentage, state cue, and compact reset — and its only flags are `--capture-only` and `--wrap '<cmd>'`:

```
C⠉ ~$4.18  ⣿⣿⣿⣀ 78% 2h14m          (meter+pct turn amber with a ! at ≥ warn; an
                                     unverified pick gets a neutral meter + ? unverified)
```

Honors `NO_COLOR`/`--plain` (ASCII/plain variants under Accessibility).

> **PLANNED — not built.** The `--format <template>` flag and the preset table below do **not** exist yet; the table documents design intent for the future flag only. Nothing in it describes shipped behavior.

```
planned presets — not built
tokens:  {mark} {spend} {meter} {pct} {reset} {tool}
default: "{mark} {spend}  {meter} {pct} {reset}"
        → "C⠉ $4.18  ⣿⣿⣿⣀ 78% ⟳2h14m"
compact: "{mark} {spend} {pct}"
        → "C⠉ $4.18 78%"
minimal: "{spend}"
        → "$4.18"
```

### Spinner

The classic braille spinner for indeterminate waits (discovery, parsing):

```
frames: ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏     cadence: ~80ms/frame
```

Used only briefly; never for steady-state. ASCII fallback: `| / - \`.

## The two screens

### now

```
C⠉ costroid                                   this week  $42.18
────────────────────────────────────────────────────────────────
limits
  claude code   5h   ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣀  92% ! resets 41m     ← amber + ! cue
                wk   ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣀⣀⣀  78%   resets 2d 6h
────────────────────────────────────────────────────────────────
api costs (this week)
  claude opus 4.8   ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿   $24.10
  gpt-5.5           ⣿⣿⣿⣿⣿⣿⣀⣀⣀⣀⣀⣀   $11.30
  sonnet 4.6        ⣿⣿⣿⣀⣀⣀⣀⣀⣀⣀⣀⣀   $6.78
────────────────────────────────────────────────────────────────
◆ opus drove most of your api spend this week. (estimated)
provider cursor detected: BETA - model Composer 2.5 Fast (composer-2.5), logged in; usage unavailable - no sanctioned source; quota unavailable - no sanctioned source
```

Live limit meters (5-hour and weekly, with reset countdowns) on top; current API spend by model below; one colleague insight line at the bottom. Subscription limits and API costs are visually parallel but clearly separate sections — limits carry no dollars.

**Cursor never appears in the limits section.** Detect-only Cursor contributes zero limit windows, so it gets no limits row (and never a fabricated %); its status renders as a **bottom provider note** under the insight line — `push_provider_notes` in render.rs formats `provider cursor detected: <message>`, where the message is built by `cursor_detected_message` in costroid-core (`BETA - {model}, {login}; usage unavailable - no sanctioned source; quota unavailable - no sanctioned source`). The same note slot carries every non-`Available` provider's status (partial / missing / error), inline and non-fatal.

### trends

```
C⠉ costroid                                   this month  $168.00
  [day] [week] (month) [year]            group: (model) app total
────────────────────────────────────────────────────────────────
  spend / week
  ⢀⣀⣠⣶⣿⣷⣄⡀ …                                   (braille sparkline)
  w1      w2      w3      w4
────────────────────────────────────────────────────────────────
  claude opus 4.8   ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿   $96.00
  gpt-5.5           ⣿⣿⣿⣿⣿⣀⣀⣀⣀⣀⣀⣀   $45.00
  sonnet 4.6        ⣿⣿⣿⣀⣀⣀⣀⣀⣀⣀⣀⣀   $27.00
────────────────────────────────────────────────────────────────
◆ press a to ask why opus spend rose this month
```

A period sparkline, then the breakdown cost bars, then an insight line.

### Controls / keybindings

```
d / w / m / y   set period (day / week / month / year)        [trends]
g               cycle group (model → app → total)             [trends]
tab             switch screen (now ↔ trends)
f  or  /        filter (fuzzy select model / app)
a               ask — hand the loaded context to the insight/recommendation (frontier) view
r               refresh now
q / Ctrl-C      quit (always restores the terminal)
?               help
```

`--live` enables auto-refresh on a tick.

### States

- **Loading:** the braille spinner with a short label.
- **Empty:** no providers detected → a plain-language line on what Costroid looked for and how to point it at the data (incl. the WSL/Windows path note), not an error.
- **Partial:** some providers missing or incomplete (e.g. Cursor) → show what's available and label the gap explicitly; never fabricate.
- **Per-provider error:** shown inline next to that provider, non-fatal; the rest of the screen still renders.
- **Warning:** amber + cue on near/over-limit meters.
- **Unverified:** a quota reading that failed the sanitize/cross-check renders with a neutral (non-alarm) meter and the color-free ` ? unverified` cue — never a confident alarm (ARCHITECTURE §9.2).
- **Estimated / Unavailable:** when the quota source is absent, show the inferred token volume marked `(estimated)` with `quota % unavailable` (no meter), or `unavailable: <reason>` — never a fabricated percentage.

## Accessibility

`--plain` produces no color, no braille, in a linear top-to-bottom reading order with every value labeled and carrying its unit and context — built to be read aloud by a screen reader. The ASCII guarantee, precisely (as built and test-pinned — `plain_mode_output_is_pure_ascii` / `ascii_mode_output_is_pure_ascii` in render.rs): every **Costroid-generated** byte in `Plain` and `Ascii` output is pure ASCII; **provider-supplied names** (models, projects) pass through verbatim, so a provider's non-ASCII name appears as-is. Mode selection (the `--plain` flag, TTY detection, `NO_COLOR`, and a braille-capability check) is in ARCHITECTURE.md.

**The no-color-only rule:** the amber/red warning state is **always** paired with a textual cue, so it survives `NO_COLOR`, color-blindness, and `--plain`. The exact strings as built (see the threshold spec above): `Braille`/`Ascii` append ` !` / ` !!` / ` !! OVER` after the percentage; `Plain` spells out ` (near limit)` / ` (critical)` / ` (over limit)`. The **unverified** state is likewise carried by its own color-free cue ` ? unverified` (shown instead of the state cue, in every mode), with a neutral meter, so a cross-check-failed reading never reads as a confident alarm even without color.

> **Forward note — the egui taskbar (`apps/bar`, Step 6, planned).** The richest surface, the egui/eframe (+ `tray-icon`) taskbar app, is a later deliverable; its visual design is not specified here yet (design TBD — no detailed mockups). It shares the same semantic states defined above: the amber warning state still needs a second, non-color cue (icon/badge/text), and `--plain` has no analogue in a GUI but the equivalent obligation holds via **AccessKit** for screen readers. Scope and sequencing for this surface are governed by [PRODUCT-PLAN.md](PRODUCT-PLAN.md) (§2d / §4, Step 6).

**ASCII substitutes per component:**

```
limit meter   Ascii: "[###########-] 92% !  resets 41m"           # '#' used, '+' half-cell, '-' remaining; same ! / !! / !! OVER cues as braille
              Plain (no bar): "claude code 5h: 92% used (near limit), resets in 41m"   # cue spelled out: (near limit) / (critical) / (over limit)
unverified    "claude code 5h: 92% used ? unverified, resets in 41m  as of 14:03"   # neutral, no alarm word (same ? unverified cue in every mode)
spend pool    "copilot mo: $3.20 / $10.00 used, resets in 5d"     # dollar line, no meter, no % — illustrative of the Spend variant only: Copilot is discovery-gated, not shipped
estimated     "claude code 5h: usage 412,000 tokens (~$1.10, estimated), quota % unavailable"
sparkline     prefer a labeled numeric list; or an ASCII height ramp .:-=+*#
cost bar      "claude opus 4.8   $24.10   (57%)"                 # no bar, or "####"
statusline    Ascii: "costroid ~$4.18  [###-] 78% 2h14m"          Plain: "costroid ~$4.18, claude code 5h 78% used, resets in 2h14m"
spinner       "| / - \"  or  "working..."
```

**Font/terminal fallback:** if braille isn't supported (replacement-char risk) Costroid downshifts automatically — a block / `.:-=+*#` ramp for the sparkline, ASCII `[####--]` for meters/bars — without the user asking. Piped/non-TTY output is always plain.

## Voice & copy

The insight line is where Costroid sounds like a colleague: proactive, plain, specific, brief. State the fact, then the so-what, then (optionally) a next step. Never alarmist, never chatty, never an LLM chat box.

**Rules:**
- Cost is an estimate — hedge accordingly (`~`, "estimated", "about"). Never claim certainty about money you inferred.
- Recommendations are advisory and **sourced**, and attach only to API-cost lines — never to subscription limits.
- One insight at a time. Quiet by default. Never block the user or demand a response.
- Sentence case, no emoji, no exclamation spam, no fake urgency, no greetings ("Hey there!").

**Good:**
- "You're pacing toward ~$58 this week — Opus drove most of it. (estimated)"
- "Weekly Claude limit at 92%, resets Sunday. Codex still has headroom."
- "Sonnet could cover ~40% of these tasks at about ⅓ the cost. Advisory — sources: DeepSWE, CursorBench (vendor)."

**Avoid:**
- "🚨 WARNING!! You are SPENDING TOO MUCH!!!"
- "This will save you exactly $19.42." (false precision on an estimate)
- "Hi friend! Want to chat about your costs?" (chatbot register)