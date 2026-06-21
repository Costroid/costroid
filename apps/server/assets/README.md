# costroid-server embedded assets

These assets are **embedded in the `costroid-server` binary** (`include_str!`) and served from
`/assets/...` over loopback. The served pages reference **only** these same-origin assets — there
are **zero external/CDN requests**, so the web UI works fully offline (enforced by
`web::tests::served_markup_references_only_embedded_same_origin_assets`).

| File | License | Source |
|---|---|---|
| `costroid.css` | Apache-2.0 (first-party) | This repository |

## Note — the M5 D2 stack decision

D2 (signed off) chose vendored **htmx** (0BSD) + **uPlot** (MIT) embedded via `rust-embed`. The
offline build environment cannot fetch those upstream files, so M5 ships **first-party embedded
assets** instead: the views are **server-rendered HTML (tables + inline SVG, no JavaScript)** over
the `include_str!` stylesheet above. This satisfies the same guarantees D2 cares about — all assets
embedded in the binary, zero CDN references, fully offline, and accessible (no-JS by default).

Swapping in the real libraries later is **additive**: drop `htmx.min.js` / `uPlot.iife.min.js` /
`uPlot.min.css` (with their upstream LICENSE files) into this directory, embed them, and add the
`<script>`/`<link>` references — the no-JS server-rendered tables remain the `<noscript>` fallback.
