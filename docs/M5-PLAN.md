# Costroid-Next — M5 detailed implementation plan

> **Provenance:** synthesized from `docs/COSTROID-NEXT.md` §3.1.F (Interfaces), §6.11 (web-UI &
> serving model, the stack decision), the **⚑ Readiness gate A1/A3/A4** (lines 14-17), §3.3 line
> 163 (the M5 milestone), §6.12 (DoD — the "offline guarantee intact" + "minimal three-view web UI"
> rows, lines 324/328); the `docs/M4-PLAN.md` house-style; `PROGRESS.md`; and a **verified** read of
> the real symbols this milestone builds on — every symbol below was confirmed against the working
> tree (file:line) by a research + map sweep. Standing rule: **re-verify before editing — the code
> wins.**
>
> **Status: 📝 PLANNED — NO CODE WRITTEN. D1–D4 ✅ SIGNED OFF 2026-06-21 (all recommended ★)** via
> the question panel: D1 = (a) `tiny_http`; D2 = (a) Maud/Askama + htmx + uPlot, vendored + embedded
> (`rust-embed`), zero CDN; D3 = (a) server → `core` (+`store`) only, never `power`, energy-only `e`;
> D4 = (a) single `loopback_addr` + regenerated `SERVER_ALLOWED` + real-serve strace + embedded-only
> test. **Rev 2 (2026-06-21): coordinator review folded in — "decisions faithful, e-math correct" +
> 1 BLOCKER / 8 med / 10 low deltas (see the Revision log below).** Now awaiting the coordinator's
> confirmation of these deltas BEFORE any code (T0–T9). Branch `costroid-next` @ `b888565` (M0, M1,
> M2, M3a, **M4 merged to `main`**; M3b is a separate human handoff that does **not** block M5).
> Per-task dev-loop (build → independent adversarial review → fix → present). Golden rules hold.
>
> **Revision log — Rev 2 (coordinator deltas folded in, each with its deciding test):**
> - **BLOCKER — the break-even token basis must be TOTAL tokens (in+out), not output-only.** The
>   canonical break-even basis is **total** tokens — it matches the cloud blended `c` (priced over
>   in+out), the workload volume `V` (tokens/day), and the M4 CLI's `energy_only_rate`
>   (`energy_cost / (tokens_in + tokens_out)`, `breakeven.rs:169`). But M3a's
>   `costroid-core::local_run_to_focus` stamps `token_count: local.tokens_out` (**output-only**,
>   `lib.rs:364`), so a stored `local_inference` row's `x_consumed_tokens` is output-only. The
>   server's `e = (effective_cost − x_amortized_hw_cost) / x_consumed_tokens` would then divide by
>   *output* tokens while the CLI divides by *total* — a silent cross-interface mismatch. **Fix
>   (new T2):** stamp `token_count = tokens_in + tokens_out` so `x_consumed_tokens` is **total**;
>   update the M3a test (`lib.rs:8629` → `1_700`); add a pure **core** helper `local_energy_only_rate`
>   (the server's `e`, tested) and a **cross-interface deciding test** that the server's `e` (from a
>   stored row) **equals** the CLI's `energy_only_rate` for the same run, with `amortized > 0`
>   (non-vacuous). The server `e` formula is unchanged — it is now *correct* because the basis is
>   total. Downstream tasks renumber **T2→T3 … T8→T9** (T0/T1 unchanged so the references below hold).
> - **MED1 — `rust-embed debug-embed`.** Tests build in debug, where `rust-embed` reads from disk by
>   default; enable the `debug-embed` feature (or add a release / assets-absent acceptance leg) so the
>   embed tests actually prove *binary*-embedding, not a disk read (T6).
> - **MED2/3/4 — the `e` derivation is filtered + fail-closed (T2 core helper).** It filters to
>   `lane == LocalInference && x_amortized_hw_cost.is_some()`; a `local_inference` row with a **null**
>   `x_amortized_hw_cost` is a **typed error / skip, never →0** (silently treating it as 0 would
>   inflate `e`); unit-tested with **mixed-lane** and **missing-amortized** fixtures.
> - **MED5 — no-local-rows honesty is a deciding test (T2/T3/T6).** Zero stored `local_inference`
>   rows → the helper returns `Ok(None)` and every surface renders an honest "no local runs recorded
>   yet" state, **never** a fabricated `e = 0` / a free break-even.
> - **MED6 — the comparison view RENDERS its honesty label (tested) (T1 + T6).** Both the TUI
>   comparison facet and the web comparison view render a "cloud = counterfactual list-price
>   estimate" label **and** the pricing-snapshot stamp; asserted in their deciding tests.
> - **MED7 — the break-even view RENDERS `measurement_mode = estimated` (tested) (T1 + T6).** The
>   surface shows the local figure's measurement mode (from the M4 `AssumptionStamp.measurement_mode`)
>   so the user knows it is an estimate, not a measured bill; asserted.
> - **MED8 — pin T1(i) to an exact value.** The TUI overlay deciding test (i) asserts an **exact
>   hand-computed crossover `V*`** (energy-only), not merely "well-formed" — mirroring the M4
>   `breakeven_cli` end-to-end pin.
> - **LOW — T0** reconciles **all six** chosen-tech "Axum" mentions in `docs/COSTROID-NEXT.md`
>   (lines 5/120/163/277/316/324) → `tiny_http` (the banned-crate rationale at line 14 + the
>   "name-banned" clause inside 316 **stay**); **T1** fixes the `strip_ansi` cite to `breakeven.rs:814`
>   and adds a `cfg(not(feature = "power"))` overlay-absence test; **T3** resolves `FORBIDDEN_SUBSTRINGS`
>   being `#[cfg(test)]`-private (store `lib.rs:690`) by **promoting** it to a `pub const` (single
>   source of truth) — or re-declaring it server-side; **T7** adds a server **no-`core→power`** negative
>   graph assert; **T6** tightens the no-CDN scan to **parse fetch-triggering attributes**
>   (`src`/`href`/`srcset`/`url()`/`import`), not a raw grep; **T4/T7** add a race-free
>   `--serve-once` / readiness-signal harness for the runtime acceptance leg.
>
> **What M5 is:** surface the merged 3-lane ledger + the M4 break-even engine through **two
> interfaces** — (A) a break-even / comparison surface in the existing Ratatui **TUI** (`apps/cli`,
> behind the off-by-default `power` feature, like `costroid bench`/`breakeven`), and (B) the
> **`costroid-server`** loopback HTTP API + a minimal **three-view web UI** (timeline / comparison /
> break-even). Deliverable: *"a coherent local app over the ledger."* **No cloud backend; the CLI
> stays byte-for-byte no-network; the server is loopback-bind with no outbound egress.**

---

# M5 detailed plan — Interfaces (TUI break-even surface + loopback web app)

## 0. Scope in one sentence

Make the 3-lane ledger and the M4 break-even engine **usable** through (A) a power-gated break-even
/ comparison **overlay in the TUI** that reuses the M4 `breakeven` engine + `render_breakeven`
renderer, and (B) a **separate loopback `costroid-server` binary** that reads the stored 3-lane
ledger (`costroid-store::all_focus_rows`), aggregates and computes break-even via `costroid-core`
(**never** linking `costroid-power`), and serves three views (timeline / comparison / break-even) as
**static assets embedded in the binary** with **zero external requests** — every surface carrying a
`--plain` / no-JS accessible equivalent and never relying on color alone, with the **CLI byte-for-byte
no-network guarantee and the server's loopback-only / no-egress guarantee both proven by extended
static + runtime gates** (R4: only bounded ledger metadata is ever exposed, never prompt/response
content), and the **break-even token basis is TOTAL tokens (in+out)** consistently across both
interfaces (the BLOCKER fix).

---

## 1. M5 "done" criteria (close against this mechanically — never self-judged prose)

1. **The M5 deciding test is green** (§6.12 line 328 — the offline guarantee, the milestone's
   load-bearing gate): the `costroid` CLI graph stays byte-for-byte no-network
   (`cli_default_build_*` unchanged), and the `costroid-server` graph admits **only** the reviewed
   local-listen + local-ledger subtree (`server_build_admits_only_the_reviewed_local_listen_subtree`
   stays green with the regenerated `SERVER_ALLOWED`, and a **negative** assert that the server links
   **no `costroid-power`**); `scripts/offline_acceptance.sh`'s server leg is upgraded from
   `--self-check` to a **real serve + loopback GET** (via a race-free `--serve-once`/readiness
   harness) and asserts **no `AF_INET` egress**; plus a deciding test that the served HTML/JS
   reference **only embedded assets** (no `http(s)://`, no CDN), parsed from fetch-triggering
   attributes, not a raw grep.
2. **The break-even token basis is TOTAL tokens (in+out), consistently (the BLOCKER).**
   `local_run_to_focus` stamps `x_consumed_tokens = tokens_in + tokens_out`; the CLI's
   `energy_only_rate` and the server's `e = (effective_cost − x_amortized_hw_cost)/x_consumed_tokens`
   therefore divide by the **same** basis. A **cross-interface deciding test** asserts the server's
   `e` (from a stored row) equals the CLI's `energy_only_rate` for the same run, with `amortized > 0`
   (non-vacuous).
3. **Two interfaces exist, both consuming the existing engines** — no new data path, no new compute,
   no new FOCUS column: (A) a TUI break-even/comparison surface behind `#[cfg(feature = "power")]`
   reusing `breakeven::render_breakeven` + a shared report builder; (B) `costroid-server` serving
   `/`, the three views, a small JSON API, and `/healthz`.
4. **`costroid` CLI stays byte-for-byte no-network.** The TUI surface is **only** compiled under
   `--features power`; with `power` off, the 9-tab TUI, the `const TAB_SCREENS` array, the digit
   dispatch, and the offline graph are **unchanged** (`cli_default_build_is_power_free` +
   `cli_power_feature_admits_only_power_allowed` still pass; a `cfg(not(feature = "power"))` test
   asserts the break-even overlay is **absent**; `POWER_ALLOWED` stays exactly `["costroid-power"]`).
5. **The server binds `127.0.0.1` ONLY**, via the single `loopback_addr` constructor (no second
   bind-address constructor is introduced); it makes **no `connect()` / no outbound `bind()`** —
   proven at runtime by the upgraded offline-acceptance leg. The server links **`costroid-core` +
   `costroid-store` only** (decision D3): **never `costroid-power`, never `costroid-connect`, no
   async runtime, no HTTP client, no TLS** (a negative graph assert for `costroid-power`).
6. **The web page issues ZERO external requests.** All assets (the chart lib + htmx + CSS + fonts)
   are **vendored into the repo and embedded in the binary** (`rust-embed`, with `debug-embed` so the
   embed is proven in debug-built tests); the served markup contains **no external-host reference**
   in any fetch-triggering attribute. The page works fully offline with no network adapter present.
7. **R4 — the API exposes only bounded ledger metadata, never content.** Every JSON field and every
   rendered cell is a bounded number / enum / identifier drawn from the metadata-allowlist columns
   (`costroid-store::USAGE_ROWS_COLUMNS`); no `ChargeDescription`, `ResourceName`, `Tags`, or any
   free-text column is read, serialized, or rendered. A deciding test asserts the response shape
   carries none of the forbidden substrings (sharing one `FORBIDDEN_SUBSTRINGS` source of truth).
8. **The energy-only invariant (M4) is carried into M5, and fail-closed.** The local marginal rate
   `e` is **energy-only** and **total-token-based**: the **TUI** computes it via the M4
   `energy_only_rate` over a `costroid-power` estimate (full precision, `from_f64_retain`, never
   `round_dp`); the **server** derives it via the new pure core helper `local_energy_only_rate`,
   which filters to `lane == LocalInference && x_amortized_hw_cost.is_some()`, treats a null
   `x_amortized_hw_cost` on a local row as a **typed error/skip (never →0)**, and returns `Ok(None)`
   when there are no local rows. **NEVER** `effective_cost / tokens` (double-counts the capex).
9. **No-local-rows honesty.** With zero stored `local_inference` rows, the helper returns `Ok(None)`
   and every surface renders an honest "no local runs recorded yet" state — never a fabricated `e = 0`
   or a "free" break-even. A deciding test pins this.
10. **The comparison + break-even surfaces RENDER their honesty cues (tested).** The comparison view
    renders a "cloud = counterfactual list-price estimate" label + the pricing-snapshot stamp; the
    break-even view renders `measurement_mode = estimated` (from the M4 `AssumptionStamp`). Asserted
    on both the TUI and the web.
11. **Money is `Decimal`/`UsdAmount` end-to-end** — never `f64` for a dollar amount, on the server
    path too (JSON money is serialized from `Decimal` strings, never via `f64`).
12. **Every visual has a `--plain` / accessible equivalent; never color-alone.** The TUI overlay
    threads the global `RenderOptions` (`--plain`/`NO_COLOR` → byte-identical content minus escapes,
    warn/critical paired with a text cue). The web UI ships a **no-JS / text fallback** for each view
    (a `<noscript>` data table + a plain-text/`?plain` response), and every chart's signal is also
    carried as text, never color alone.
13. **No `core→power` edge, no new copyleft dependency.** Core's internal deps stay
    `costroid-focus` + `costroid-providers` (the new `local_energy_only_rate` helper takes
    `&[FocusRecord]` — no power). New deps are **permissive only** and flagged for sign-off (§1.5 D2 /
    §4): `rust-embed` (MIT), the template lib (`maud`/`askama` — MIT/Apache-2.0), and the **vendored,
    non-crate** front-end assets `htmx` (0BSD) + `uPlot` (MIT) with licenses committed + attributed.
14. **Offline proof extended, fail-closed.** `SERVER_ALLOWED` is regenerated via the `#[ignore]`
    `print_server_delta` test to cover the new server subtree (the `rust-embed`/template macro
    transitives + the `costroid-store` local-SQLite subtree), each entry **reviewed as not a
    network/TLS/telemetry path**; the subset-allowlist assertion stays load-bearing.
15. **Pre-PR gate green** (`cargo fmt --all -- --check && cargo clippy --workspace --all-targets --
    -D warnings && cargo test --workspace`), plus the `--features power` clippy/test legs, the
    `costroid-server` build/clippy/test, MSRV (server may raise its own `rust-version` per A4 if the
    web stack demands it — flagged, not assumed), license/advisories, and the focus-conformance +
    offline-acceptance CI legs.
16. **Docs reconciled in lockstep** — `README.md` (the local web UI + the TUI surface),
    `docs/ARCHITECTURE.md` §10 (the server, its data path, the web-UI stack, "no core→power edge",
    the total-token basis), `PROGRESS.md` (M5 box + checklist), the §6.12 M5 checkbox, and **all six
    chosen-tech "Axum" mentions reconciled to `tiny_http`** (T0).
17. **No prompt/response content anywhere** in either interface (the Cardinal Rule, R4); inputs are
    numeric scenario knobs + bounded ledger metadata only.

---

## 1.5 ⚑ DECISIONS TO SIGN OFF BEFORE CODING (CLAUDE.md "ask first")

> These four decisions touch the **public CLI/UI surface** (a new binary's behavior + a new TUI
> surface), **new dependencies**, and the **offline guarantee**, so they are surfaced for the
> coordinator's sign-off **before any code** (CLAUDE.md "Decide vs. ask"). Recommended defaults
> marked **★**. The plan below is written assuming the ★ options; a different choice revises the
> affected tasks.
>
> **✅ SIGNED OFF 2026-06-21 (all recommended ★):** D1 = (a) `tiny_http`; D2 = (a) Maud/Askama +
> htmx + uPlot, vendored + embedded, zero CDN; D3 = (a) server → `core` (+`store`) only, never
> `power`, energy-only `e`; D4 = (a) single `loopback_addr` + regenerated `SERVER_ALLOWED` +
> real-serve strace + embedded-only test. The plan below stands as written. Still gated on the
> coordinator's review of the **Rev 2 deltas** before any code.

- **D1 — Server framework (architecture + offline surface).**
  - **(a) ★ Confirm `tiny_http`** — the M0 spike's choice, already scaffolded (`apps/server`,
    `loopback_addr`, `Mode::Serve`, `DEFAULT_PORT 7878`), already proven by `SERVER_ALLOWED` +
    the loopback self-check. Blocking, **no `tokio`**, matches the repo's blocking-`ureq`
    philosophy; the async-runtime ban (`ALWAYS_FORBIDDEN_CRATES`) stays intact.
  - **(b) rejected: Axum in a separate binary.** It pulls `tokio`/`hyper`/`h2` — the banned async
    surface — into the allowlist + the loopback proof, for no functional gain on a single-user
    loopback server. T0 reconciles the canon's stale "Axum" wording → `tiny_http`.

- **D2 — Web-UI rendering (new dependencies + accessibility).**
  - **(a) ★ Server-rendered templates (`maud` or `askama`) + `htmx` + `uPlot`, ALL assets vendored
    and embedded (`rust-embed`, `debug-embed`), ZERO external CDN refs.** The page works fully
    offline; the chart lib + htmx + CSS are committed to the repo and baked into the binary.
    Lightweight, no build toolchain beyond `cargo`, and the `<noscript>`/text fallback is trivial.
    New deps (all permissive, flagged): `rust-embed` (MIT), the template lib (MIT/Apache-2.0),
    vendored `htmx` (0BSD) + `uPlot` (MIT).
  - **(b) rejected: a WASM SPA (Leptos/Dioxus).** Heavy toolchain (`wasm32` target, `trunk`/bundler),
    a much larger reviewed dep surface, and harder to give a clean no-JS accessible fallback —
    overkill for three local read-only views.

- **D3 — Break-even data path (NO `core→power` edge).**
  - **(a) ★ The server links `costroid-core` (+ `costroid-store`) ONLY.** It reads the stored 3-lane
    ledger via `Store::all_focus_rows()`, aggregates via `core::aggregate_rows`, prices the cloud
    side via `core::cloud_price_per_token` + `core::blended_cloud_per_token`, and runs
    `core::breakeven` — sourcing the local energy `e` from **stored `local_inference` rows** via the
    new pure core helper `local_energy_only_rate` (total-token, energy-only:
    `(effective_cost − x_amortized_hw_cost)/x_consumed_tokens`) or scenario inputs. It **NEVER**
    links `costroid-power` (no `core→power` edge). Linking `costroid-store` grows `SERVER_ALLOWED`
    by the reviewed, all-local SQLite subtree (T7).
  - **(b) rejected: the server links `costroid-power`** — it would add a subprocess/measurement
    surface to a loopback web server, contradict the M4 "no `core→power` edge" rule, and bloat the
    offline allowlist. The TUI (which already links `power` under its feature) uses the live estimate;
    the server reads the persisted result.

- **D4 — Loopback-only proof, extended (the offline guarantee).**
  - **(a) ★ Keep the single `loopback_addr` constructor**; extend `SERVER_ALLOWED` (regenerated via
    `print_server_delta`) for the `rust-embed`/template + `costroid-store` transitives, each
    **reviewed no-network**; upgrade `scripts/offline_acceptance.sh`'s server leg from `--self-check`
    to the **real serve path** via a race-free `--serve-once`/readiness harness (bind loopback, GET a
    view over `127.0.0.1`, assert **no `AF_INET` egress** under strace); add a deciding test that the
    served HTML/JS reference **only embedded assets** (parsed from fetch-triggering attributes).
  - **(b) rejected: a looser proof** (self-check only, or trusting the allowlist without a real-serve
    runtime leg). The whole point of M5 is shipping a *server* — the no-egress guarantee must be
    proven on the *real* request path.

---

## 2. Ordered task list (dependency-correct; reuse the engines, prove the offline guarantee)

> Each task notes its **Do**, its **Deciding test**, and its top **Risk + Mitigation**. Ordering:
> the docs reconcile (T0), the TUI surface (T1, the smallest coherent interface, reusing M4), the
> **BLOCKER token-basis fix + the core `e` helper + the cross-interface test (T2)** — independent of
> T1 and may be built first — then the server data path (T3) → JSON API + routing (T4) → vendored
> embedded assets (T5) → the three views end-to-end (T6), the extended offline proof (T7, lands with
> T5's deps), the server config / single-bind discipline (T8), and wire-up + CI + docs (T9). All
> `§`/`R#`/`A#` refs point into `docs/COSTROID-NEXT.md`.

### T0 — Reconcile the interfaces canon (`tiny_http`, not Axum) (docs) **[LOW]**
- **Do:** Edit the **six** chosen-tech "Axum" mentions in `docs/COSTROID-NEXT.md` to `tiny_http`:
  the orientation note (**line 5**, "local Axum + embedded assets"), §3.1.F (**line 120**, "a local
  HTTP API (Axum)"), the M5 milestone (**line 163**, "Axum API"), the §6 component list (**line
  277**, "Axum local HTTP API"), the §6.11 decision paragraph (**line 316**, lock the choice to
  `tiny_http` as the chosen server), and the §6.12 DoD checklist (**line 324**, "localhost Axum").
  **Keep** the banned-crate rationale that correctly names `axum`/`hyper`/`tokio` as forbidden — the
  ⚑ Readiness gate (**line 14**) and the "`axum`/`hyper`/`tokio` are name-banned" clause **inside**
  line 316 — those are explaining *why* they are banned, not naming the chosen tech. No source-code
  change.
- **Deciding test:** none (docs); a reviewer diff shows the six chosen-tech mentions now read
  `tiny_http`, and `grep -n "Axum\|axum" docs/COSTROID-NEXT.md` returns only the banned-crate
  rationale (line 14 + the "name-banned" clause in 316).
- **Risk:** over-replacing the banned-crate rationale (it must still name `axum` as forbidden), or
  missing a mention. **Mitigation:** the explicit six-line list + the post-edit grep assertion.

### T1 — TUI break-even / comparison surface (`apps/cli`, power-gated) **[the first interface]**
- **Do:** Add a **break-even/comparison overlay** to the Ratatui TUI, compiled **only** under
  `#[cfg(feature = "power")]` (it needs the local scalar via `costroid-power`, like the `costroid
  breakeven` subcommand). Follow the **Frontier overlay precedent** (`tui.rs:357-374`, `a`/`esc`): a
  new key (e.g. `b`) sets a `Screen::Breakeven` overlay; `esc` returns to the previous screen. The
  `Screen` enum, the dispatch, the tab-strip indicator (`tui.rs:740-774`), and the hint footer
  (`tui.rs:789-836`) gain the overlay **only** under the `power` cfg — so the default build's
  `const TAB_SCREENS: [Screen; 9]` + digit dispatch are byte-unchanged. Reuse the M4 engine: extract
  the report-building flow inside `breakeven::run_breakeven` (`breakeven.rs:40`) into a shared
  `fn breakeven_report_for_scenario(...) -> Result<(BreakevenReport, …)>` seam that both
  `run_breakeven` and the overlay call, and render via `breakeven::render_breakeven(&BreakevenReport,
  cloud_model, tokens_per_day) -> StyledDocument` (`breakeven.rs:351`). The overlay's scenario comes
  from `[breakeven]` config (M4 T6) defaults; the **comparison** facet shows actual stored local
  spend vs counterfactual cloud list price for the same token volume — and **RENDERS** a "cloud =
  counterfactual list-price estimate" label + the pricing-snapshot stamp (MED6), and the break-even
  facet **RENDERS** `measurement_mode = estimated` from the `AssumptionStamp` (MED7).
- **Deciding test (`apps/cli`, `#![cfg(feature = "power")]`):** a headless test builds the overlay
  document via the shared seam + `render_breakeven` and asserts: **(i)** the rendered crossover is an
  **exact hand-computed `V*`** (energy-only — pinned to a value, not just "well-formed"; MED8),
  matching the M4 `breakeven_cli` end-to-end approach; (ii) `--plain`/`RenderOptions::plain()` is
  **byte-identical content minus ANSI** to the styled run (reuse the `strip_ansi` helper,
  **`breakeven.rs:814`**); (iii) never/infeasible carry their text cue (never color-alone); (iv) the
  comparison label + snapshot stamp + `measurement_mode = estimated` are present (MED6/MED7). Plus a
  **`#[cfg(not(feature = "power"))]` overlay-absence test** asserting the default TUI exposes exactly
  the 9 tabs and no break-even screen/keybinding; and the standing graph tests
  (`cli_default_build_is_power_free`, `cli_power_feature_admits_only_power_allowed`) stay green.
- **Risk:** the overlay leaks into the default (non-power) TUI surface (a 10th tab in the const
  array, a non-cfg keybinding) → breaks the byte-for-byte CLI guarantee. **Mitigation:** overlay
  (not a numbered tab) like Frontier; every new symbol behind the `power` cfg; the overlay-absence
  test + the default-build graph test are the gates.

### T2 — BLOCKER: total-token basis + the pure core `e` helper + the cross-interface test (`costroid-core`, `apps/cli`) **[BLOCKER L2-1]**
- **Do:** *(Independent of T1; may be built first.)*
  1. **Fix the token basis.** In `costroid-core::local_run_to_focus` (`lib.rs:357`), stamp
     `token_count = local.tokens_in + local.tokens_out` (currently `local.tokens_out`, `lib.rs:364`),
     so a stored `local_inference` row's `x_consumed_tokens` is **total** tokens. The row stays a
     single combined meter (`token_type` remains `Output` — a modeling choice, now carrying the total
     basis); update the doc comment at `lib.rs:193` ("only the lane + token count are carried") to say
     the token count is the **total (in+out)** basis. Update the M3a test
     `canonical_local_event_maps_all_economics_columns_estimated` (`lib.rs:8629`): `x_consumed_tokens
     == Decimal::from(1_700)` (= 500 + 1_200); `x_token_type == "output"` unchanged (`lib.rs:8630`).
  2. **Add the pure core `e` helper.** `pub fn local_energy_only_rate(rows: &[FocusRecord]) ->
     Result<Option<Decimal>, CoreError>` — the server's `e`, as a tested core surface (no `power`,
     pure `Decimal`). It **filters to `lane == LocalInference && x_amortized_hw_cost.is_some()`**
     (MED2), computes `Σ(effective_cost − x_amortized_hw_cost) / Σ x_consumed_tokens` at **full
     precision** (never `round_dp`), returns **`Ok(None)`** when no local rows match (MED5), and a
     **typed `CoreError`** when a `local_inference` row has a **null** `x_amortized_hw_cost` (MED3 —
     never silently →0). *(Small new public core surface, covered by D3 sign-off.)*
  3. **Cross-interface deciding test** (`apps/cli`, `#[cfg(feature = "power")]` unit test in
     `breakeven.rs`, which can reach the private `energy_only_rate`): for one `estimate_run` result,
     build the same run both ways — the CLI's `energy_only_rate(&report)` and a `FocusRecord` via
     `local_run_to_focus` fed to `core::local_energy_only_rate(&[row])` — and assert they are
     **equal at full precision**, with `amortized_hw_cost > 0` so the `(effective − amortized)`
     subtraction is **non-vacuous** (pre-fix, the row's output-only `x_consumed_tokens` would make
     them unequal → the test fails, locking the basis). Under `#[cfg(feature = "store")]`, additionally
     round-trip the row through `Store` first (byte-identical replay is already proven).
- **Deciding test:** the cross-interface equality above (the BLOCKER gate); the M3a test now pins
  `1_700`; the core helper's unit tests — **mixed-lane fixture** (dev/cloud rows ignored), a
  **missing-amortized** `local_inference` row → typed error (MED3/MED4), and a **no-local-rows**
  input → `Ok(None)` (MED5).
- **Risk:** changing the token basis silently shifts an existing M3a/aggregation assertion elsewhere;
  the helper double-counts capex or treats null amortized as 0. **Mitigation:** the cross-interface
  equality + the helper's filter/typed-error/`Ok(None)` tests; a workspace test run to catch any
  dependent assertion (the basis is total tokens — `Σ x_consumed_tokens` on the local lane).

### T3 — Server ledger read + view data models (`apps/server` → `core` + `store`) **[D3]**
- **Do:** Add `costroid-store` to `apps/server/Cargo.toml` (alongside `costroid-core` + `tiny_http`)
  and a `data` module that: opens the store (`Store::open(path)` — path from config/flag, default
  XDG), reads `all_focus_rows()` (`store/src/lib.rs:404`), and builds three **bounded, R4-safe**
  view models — **timeline** (spend per period bucket × group via `core::aggregate_rows(&rows,
  GroupBy)` (`lib.rs:2420`) + `period_range_for` (`lib.rs:1633`)), **comparison** (actual
  `local_inference`/`developer_tool` effective cost vs counterfactual cloud list price for the same
  token volume, via `core::cloud_price_per_token` + `blended_cloud_per_token`; the model carries the
  "counterfactual list-price estimate" flag + the pricing-snapshot id for the view to render —
  MED6), and **break-even** (run `core::breakeven_report` with the local `e` from
  `core::local_energy_only_rate(&rows)` (T2) — `Ok(None)` → an honest no-local-rows model, MED5; the
  model carries `measurement_mode` for the view to render — MED7). Money serialized from `Decimal`
  strings (never `f64`); every field is a bounded number/enum/id from `USAGE_ROWS_COLUMNS`. If
  time-bucketing needs more than `aggregate_rows` + `period_range_for` exposes, add **one** narrow
  public core helper (covered by D3 sign-off).
- **Deciding test (`apps/server`):** seed a temp store fixture mixing all three lanes; assert (i)
  timeline buckets + per-group totals match hand-computed values; (ii) the comparison's counterfactual
  cloud price is the catalog-priced amount + the snapshot id, and the model carries the
  counterfactual-estimate flag (MED6); (iii) the break-even crossover uses the **energy-only,
  total-token** `e` (a discriminator assert that the energy-only `e` ≠ `effective_cost/tokens`); a
  **no-local-rows** fixture yields the honest no-local model (MED5); (iv) **R4** — the serialized
  models contain none of the forbidden substrings, sharing the **promoted `pub const
  FORBIDDEN_SUBSTRINGS`** (T-LOW: promote it out of store's `#[cfg(test)]` module, `store/lib.rs:690`,
  to a single source of truth — or re-declare server-side).
- **Risk:** double-counting capex in `e`; leaking a content column; `FORBIDDEN_SUBSTRINGS` drift.
  **Mitigation:** reuse the T2 core helper (one tested `e` path); the R4 substring test over one
  shared const; read only allowlisted columns.

### T4 — Server HTTP routing + JSON API (`apps/server`, `tiny_http`) **[D1]**
- **Do:** Replace the M0 placeholder request loop (`apps/server/src/main.rs:111-139`) with a small
  router over `request.url()` + method: `GET /` (the app shell), `GET /timeline|/comparison|/breakeven`
  (the three views, T6), `GET /api/timeline|comparison|breakeven` (the JSON models from T3, for
  htmx/uPlot to fetch same-origin), `GET /healthz`, and a 404 otherwise. Keep `serve()` binding via
  the **single** `loopback_addr` (`main.rs:77`); no second bind constructor. Add a **`--serve-once`**
  mode (serve exactly one request, then exit) and/or print a **readiness line** (the bound addr) so
  the acceptance harness can connect race-free (T7). Each handler is infallible-at-the-socket (a
  failed write to one client never takes the server down). JSON via `serde_json` (already in the core
  graph) — **no new serializer crate**; money as `Decimal` strings.
- **Deciding test (`apps/server`):** a test binds an ephemeral loopback port (`loopback_addr(0)`),
  issues GETs over a **`std::net::TcpStream` to `127.0.0.1`** (std, not a banned HTTP-client crate),
  and asserts: each route returns 200 + the right `Content-Type`; `/api/*` returns valid JSON
  matching the T3 models; an unknown path returns 404; the bound address `.is_loopback()`;
  `--serve-once` exits after one request.
- **Risk:** adding a JSON/HTTP helper crate that trips the offline gate; a second bind path.
  **Mitigation:** reuse `serde_json` + `tiny_http` only; keep `loopback_addr` the sole constructor;
  T7's allowlist + runtime leg catch any new crate.

### T5 — Vendored, embedded web assets (`apps/server`, `rust-embed`) **[D2 — new deps, flag for sign-off]**
- **Do:** Vendor the front-end assets into the repo (e.g. `apps/server/assets/`): `htmx.min.js`
  (0BSD), `uPlot.iife.min.js` + `uPlot.min.css` (MIT), a small Costroid stylesheet, and (if used) a
  self-hosted font — **committing each upstream LICENSE** and attributing them
  (`apps/server/assets/VENDOR.md` + `docs/ARCHITECTURE.md`/`NOTICE`). Embed them with `rust-embed`
  using the **`debug-embed` feature** (so debug-built tests prove *binary*-embedding, not a disk
  read — MED1) and serve from `GET /assets/*` with correct content types + an `ETag`/cache header.
  **Zero** external references: the markup links only `/assets/...`. Add `rust-embed` + the template
  lib (`maud`/`askama`) to `apps/server/Cargo.toml` (permissive; flagged in §1.5 D2 + §4).
- **Deciding test (`apps/server`):** (i) every embedded asset path resolves to non-empty bytes with
  the expected content type — and resolves **even with the on-disk `assets/` dir absent/renamed** (or
  with `debug-embed` on), proving binary-embedding (MED1); (ii) the **embedded-asset / served-markup
  scan** — render the shell + all view templates and assert that every **fetch-triggering attribute**
  (`src`, `href`, `srcset`, CSS `url(...)`, `import`) resolves to a same-origin `/assets/...` path,
  with **no external host** (a parsed check, not a raw `http` grep — LOW); (iii) a vendored-license
  presence check.
- **Risk:** a template/asset references a CDN (a raw grep misses a protocol-relative or `srcset`
  ref); a copyleft asset slips in; `rust-embed` reads from disk in tests (a false pass).
  **Mitigation:** the attribute-parsed scan; the vendored-license check; `debug-embed` + the
  assets-absent resolution test; `cargo deny` for the crates.

### T6 — The three views end-to-end + accessible fallback (`apps/server`) **[§3.1.F, accessibility]**
- **Do:** Server-render the three views over the T3 models: **timeline** (spend by project/tool/model
  over time — a uPlot line/area fed by `/api/timeline`, grouping selector via htmx), **comparison**
  (actual local vs counterfactual cloud list price — paired bars + a delta, **rendering** the "cloud
  = counterfactual list-price estimate" label + the snapshot stamp, MED6), **break-even**
  (utilization curves: local-per-day vs cloud-per-day with the `V*` crossover marked, the sensitivity
  band shaded, the assumption stamp **including `measurement_mode = estimated`** + the labeled DeepSWE
  overlay beside it — never folded into the math, MED7). **Accessibility (required):** each view ships
  a **`<noscript>` data table** and a **`?plain` text response** carrying the same numbers; never
  color-alone (the verdict + the honesty labels are always text). The no-local-rows model (MED5)
  renders an honest empty state. Reuse the M4 verdict wording.
- **Deciding test (`apps/server`):** each view renders with the T3 fixture into well-formed HTML
  containing the expected numbers; the comparison view's "counterfactual list-price estimate" label +
  snapshot stamp are present (MED6); the break-even view shows `measurement_mode = estimated` (MED7) +
  the verdict as text + the dated DeepSWE overlay labeled; the `<noscript>`/`?plain` fallback carries
  the same headline figures (a table, not a chart); the crossover figure equals `/api/breakeven` (the
  chart is a view of the one number, not a recomputation); a no-local-rows fixture renders the honest
  empty state (MED5).
- **Risk:** the chart becomes the only carrier of a signal; the page recomputes break-even differently
  from the API; an honesty label is missing. **Mitigation:** the text-fallback + label assertions;
  both the chart and table read the one T3 model.

### T7 — Extend the offline proof (static allowlist + runtime serve + embedded-only) **[D4 — the milestone gate]**
- **Do:** Regenerate `SERVER_ALLOWED` (`apps/cli/tests/offline.rs:346`) via the `#[ignore]`
  `print_server_delta` test to include the new server-delta crates — the `costroid-store`
  local-SQLite subtree (`rusqlite` + `libsqlite3-sys` + the `cc`/`pkg-config`/… toolchain +
  `hashlink`/`fallible-iterator`/… — already reviewed in `STORE_ALLOWED`) and the
  `rust-embed`/template-macro transitives — **each reviewed as NOT a network/TLS/telemetry path** and
  documented in the allowlist comment. Keep the subset-allowlist assertion load-bearing and the
  positive `tiny_http`-is-linked check; add a **negative assert that the server links no
  `costroid-power`** (mirroring the `costroid-connect`-absence assert — LOW). Upgrade
  `scripts/offline_acceptance.sh`'s server leg from `--self-check` to a **real serve** via the
  race-free `--serve-once`/readiness harness (T4): spawn `costroid-server` on a loopback port under
  strace, wait for the readiness line, GET a view + an `/api/*` endpoint over `127.0.0.1`, assert a
  200 **and no `AF_INET` egress** (loopback bind/connect allowed; any non-loopback `connect()`/`bind()`
  fails the leg). Wire the T5 attribute-parsed no-external-URL assertion into the acceptance script.
- **Deciding test:** `cargo test -p costroid --test offline
  server_build_admits_only_the_reviewed_local_listen_subtree` green with the regenerated
  `SERVER_ALLOWED` + the no-`costroid-power` negative assert; `cli_default_build_*` unchanged;
  `scripts/offline_acceptance.sh` green with the race-free real-serve loopback-only + no-external-URL
  legs.
- **Risk:** a new transitive is an outbound path and gets blanket-allowlisted unreviewed; the runtime
  leg races on startup (flaky) or a CDN ref hides in an asset. **Mitigation:** review each delta crate
  individually (the comment names why each is local); the `--serve-once`/readiness harness removes the
  race; the attribute-parsed embedded-only scan runs in both the unit test (T5) and the script.

### T8 — Server config + single-bind discipline (`apps/server`) **[D1/D4]**
- **Do:** Accept a `--port` (default `DEFAULT_PORT 7878`), a ledger-path override (default XDG store
  path), and `--serve-once` (T4), parsed in the existing `parse_args` (`main.rs:53`). The bind
  address is **always** constructed by `loopback_addr` — the port varies, the host **cannot** (it is
  hard-wired to `Ipv4Addr::LOCALHOST`); **no second constructor**. Read a `[server]` section if it
  fits the read-only `costroid-config` pattern (port only; loopback is not configurable) — or keep it
  flag-only; either way **bind host is not user-controllable**. Print the loopback URL on start.
- **Deciding test (`apps/server`):** `--port N` flows to the bound address while `.ip().is_loopback()`
  stays true; no flag/config can produce a non-loopback bind (the address is built from
  `loopback_addr`, asserted); `parse_args` round-trips the new flags.
- **Risk:** a configurable bind host (`0.0.0.0`) sneaks in → reachable off-box. **Mitigation:**
  `loopback_addr` stays the only constructor; the is-loopback assertion; documented as non-negotiable.

### T9 — Wire-up, CI, docs (all crates) **[DoD]**
- **Do:** Run the full pre-PR gate + the `--features power` clippy/test legs + the `costroid-server`
  build/clippy/test; confirm `cargo deny` (licenses for the new permissive deps + the vendored
  assets) and the offline-acceptance + focus-conformance CI legs are green; flip `apps/server`'s
  `dist`/`publish` decision **only if** the coordinator wants M5 to ship the server (else leave the
  M6 packaging note). Update `README.md` (a "Local web app" section: `costroid-server`, the three
  views, the loopback/offline guarantee + the TUI break-even surface), `docs/ARCHITECTURE.md` §10
  (the server, its `core`+`store` data path, the embedded web-UI stack, "no `core→power` edge", the
  total-token basis + the `local_energy_only_rate` helper), `PROGRESS.md` (M5 → built, the T-list),
  and check the §6.12 M5 box + the COSTROID-NEXT/PROGRESS M5 checklist lines. Reconcile the canon's
  Axum mentions (done in T0).
- **Deciding test:** the full gate green; `cargo test -p costroid --test offline` +
  `scripts/offline_acceptance.sh` green; the §6.12 M5 deciding test (the offline guarantee, T7) +
  the BLOCKER cross-interface test (T2) referenced as the milestone gates.
- **Risk:** docs drift from behavior; the server enters the CLI release/no-network surface.
  **Mitigation:** docs edited in the same commits as the behavior; the per-binary offline graph keeps
  the server's deps out of the CLI/bar entirely.

---

## 3. What lands in M5 vs what defers to M6 (honest split)

- **In M5 (agent-ownable, CI-tested, the merge target — needs NO hardware, NO network):** the
  power-gated TUI break-even/comparison overlay reusing the M4 engine + renderer (T1); the BLOCKER
  total-token basis + the core `e` helper + the cross-interface test (T2); the `costroid-server`
  ledger read + the three R4-safe view models (T3); the `tiny_http` routing + JSON API (T4); the
  vendored, embedded web assets (T5); the three views end-to-end + their accessible no-JS/text
  fallbacks (T6); the extended offline proof — static `SERVER_ALLOWED` + the no-`power` negative
  assert + a real-serve loopback-only runtime leg + the embedded-only assertion (T7); the server
  config + single-bind discipline (T8); wire-up + docs (T9). The stored 3-lane ledger (M1/M2/M3)
  supplies the data; the M4 engine supplies the math.
- **Honest limitations (stated, not hidden):**
  - **The server reads the *stored* ledger.** Surfacing live-collected dev/cloud lanes in the web UI
    without a prior `store` ingest, the store-ingest CLI flow itself, and any live-refresh/websocket
    push are **out of M5's web scope** — the web UI is a read view over what has been persisted. (The
    TUI continues to show the live snapshot.) If a coherent demo needs an ingest step, that step is
    called out, not silently assumed.
  - **No-local-rows is an honest empty state**, not a fabricated free break-even (MED5) — until a
    local run is recorded, the break-even/comparison surfaces say so.
  - **macOS/Windows browser-launch / packaging is not field-verified** here (mirrors the bar's
    tray caveat); the server runs cross-OS but is exercised on Linux/WSL in CI.
- **Defers to M6:** the methodology/limitations doc page + the hero demo GIF (§6 line 303), the
  `costroid-server` crates.io publish-ladder bump + the `dist` packaging choice (installers vs
  archives, mirroring `apps/bar`), and any richer interactivity (live refresh, a WASM SPA) beyond
  the three read-only views.

---

## 4. Cross-cutting risks to resolve EARLY

1. **The token basis is the BLOCKER (the whole milestone's correctness hinges on it).** Break-even is
   computed over **total** tokens (in+out) everywhere — cloud `c`, workload `V`, and local `e`. The
   M3a output-only `x_consumed_tokens` would silently corrupt the server's `e`; T2 fixes the basis +
   locks it with the cross-interface equality test. Resolve T2 first (it is independent of T1).
2. **The offline guarantee (R-network).** Shipping a *server* must not weaken "the CLI makes no
   network call." The per-binary graph isolates the server (`tiny_http` banned in the CLI/bar,
   admitted only for `costroid-server`); M5 keeps `cli_default_build_*` byte-identical and proves
   no-egress on the **real** serve path (T7), not just at construction. The `SERVER_ALLOWED` growth
   (store + embed transitives) is reviewed crate-by-crate; a no-`power` negative assert is added.
3. **New dependencies (D2).** `rust-embed` (MIT, `debug-embed`) + the template lib (MIT/Apache-2.0) +
   the **vendored non-crate** `htmx` (0BSD) / `uPlot` (MIT). All permissive — but new deps + a
   public-UI surface, so **flagged for sign-off**. Each upstream LICENSE is committed + attributed;
   `cargo deny` gates the crates.
4. **R4 — only bounded metadata, never content.** The API/UI reads **only** the `USAGE_ROWS_COLUMNS`
   allowlist; enforced by the forbidden-substring test over a single shared `FORBIDDEN_SUBSTRINGS`
   source of truth (promote it out of store's `#[cfg(test)]` module).
5. **The energy-only landmine, on a new path + fail-closed (M4 carry-over).** The server's `e`
   (`core::local_energy_only_rate`) is energy-only, total-token, filtered to local rows with a
   present `x_amortized_hw_cost`; a null amortized = typed error (never →0); no rows = `Ok(None)`.
   The discriminator + filter tests are mandatory.
6. **`loopback_addr` stays the only bind constructor.** Host hard-wired to `Ipv4Addr::LOCALHOST`;
   only the port is configurable. No `--bind`/`--host`/`0.0.0.0` knob (T8).
7. **No `core→power` edge.** Core's internal deps stay `costroid-focus` + `costroid-providers` (the
   new helper takes `&[FocusRecord]`). The server links `core` + `store` only (a negative graph
   assert); the live power estimate stays in the CLI's power-gated surface (T1).
8. **Accessibility + honesty cues, both interfaces.** TUI overlay threads `RenderOptions` (`--plain`
   byte-identical, text cues); the web UI ships a `<noscript>`/`?plain` text table per view and never
   relies on a chart's color for a signal; the comparison "counterfactual estimate" label + the
   break-even `measurement_mode = estimated` are RENDERED + tested (MED6/MED7).
9. **MSRV (A4).** The server may raise its own `rust-version` above the workspace 1.88 if the web
   stack demands it (as `apps/bar` does at 1.92). Flag the bump rather than dragging the lean core's
   MSRV up; the CLI/core/focus stay at 1.88.
10. **Embed-in-debug (MED1).** `rust-embed` reads from disk in debug by default; without `debug-embed`
    (or a release/assets-absent leg) the embed test is a false pass. Enable `debug-embed` and prove
    resolution with the on-disk assets dir absent.

---

## 5. What M5 deliberately does NOT do (defended scope)

- **No cloud backend, no hosted SaaS, no auth/RBAC/multi-tenant** (R5 / §6.11). The web UI is
  local-only, embedded static assets over a loopback server; a hosted product is a separate future
  repo, explicitly deferred.
- **No outbound network from either interface.** The CLI stays byte-for-byte no-network; the server
  binds loopback only and makes no `connect()`; the web page issues zero external requests.
- **No new FOCUS `x_` column, no new compute, no new data path.** Both interfaces are **views** over
  the existing 3-lane ledger + the M4 engine. (The break-even result remains the M4 `BreakevenReport`
  struct; the only core change is the BLOCKER token-basis fix + the small `local_energy_only_rate`
  accessor.)
- **No `core→power` edge, no `costroid-power` in the server, no credential/network code in the
  server.** If a task appears to need any of these → **stop and ask the human.**
- **No prompt/response content anywhere** (R4); inputs are numeric scenario knobs + bounded ledger
  metadata only.
- **No live-refresh / websocket / WASM SPA / store-ingest CLI flow** — read-only views over the
  persisted ledger; richer interactivity and the ingest UX defer to M6 (§3).
- **No telemetry; the default build stays byte-for-byte no-network.**
