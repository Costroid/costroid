# Statusline-capture build brief

> **Status: committed** — the implementation brief for Step 2 (v0.3.0) of [PRODUCT-PLAN.md](PRODUCT-PLAN.md): capturing Claude Code's live 5h/7d quota through its `statusLine` hook and surfacing it honestly. A few values are still open (see §12) and get settled during implementation — chiefly the cross-check floor `N`. It binds to the real code as of commit `0dcb885` and is governed by ARCHITECTURE §4/§8/§9.2 and [DATA-MODEL.md](DATA-MODEL.md). Where this brief and those docs disagree, the canon wins and this brief is wrong — flag it.

---

## 0. Scope

**Builds:** (1) a `costroid setup-statusline` command that wires Claude Code's `statusLine` to capture its `rate_limits` block into a local no-secret cache; (2) a Claude-provider `parse_limits` that reads, sanitizes, and cross-checks that cache into `LimitWindow`s; (3) the `LimitWindow` + render shape changes those need; (4) the bad-data fixtures and snapshots that prove it degrades correctly.

**Does NOT build:** any network call, any credential read, any Anthropic-endpoint hit (ARCHITECTURE §8 — the whole point is that this is sanctioned and local). No Cursor live quota (Phase 2). No Opus per-model quota % (unobservable — §6).

**Invariant that gates the whole thing:** the captured field is *untrusted input* with four documented failure modes. Showing nothing, "unverified", or a labeled estimate is correct; **showing a confident wrong number is fatal** (§9.2). Every decision below serves that.

---

## 1. Data-model changes

### 1.1 `LimitWindow` (provider layer) — gains two always-present fields

Current ([crates/costroid-providers/src/lib.rs:155-163](../crates/costroid-providers/src/lib.rs#L155-L163)):

```rust
pub struct LimitWindow {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub used_fraction: Option<f64>,      // already Option in code
    pub resets_at: Option<DateTime<Utc>>, // already Option in code
    pub label: Option<String>,
}
```

Target:

```rust
pub struct LimitWindow {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub used_fraction: Option<f64>,
    pub resets_at: Option<DateTime<Utc>>,
    pub captured_at: DateTime<Utc>,  // NEW, always set: when this reading was observed
    pub status: LimitStatus,         // NEW, always set
    pub label: Option<String>,
}

pub enum LimitStatus { Verified, Unverified, Unavailable }
```

- `captured_at` is **not** `Option` — a window only exists because something produced it at a known time, so the producer always knows the capture instant. (For Claude it's the snippet's write time read from the cache; for Codex it's the rollout entry timestamp — §7.)
- `status` is **not** `Option` — every producer assigns one. See §4 for the Claude decision tree, §7 for Codex (`Verified`).
- `used_fraction`/`resets_at` stay `Option` (they already are) — `Unavailable` windows and the Opus volume case carry `None`.

> **Doc-drift to true up (flag, don't silently fix):** DATA-MODEL.md's `LimitWindow` listing already carries `captured_at`/`status` (added in the doc pass) and matches this target on those. But it still disagrees with the real code on three field *types* that predate this brief: `used_fraction: f64` and `resets_at: DateTime<Utc>` are typed **non-optional** (code: `Option<…>`), and `tool: String` (code: [`ProviderId`](../crates/costroid-providers/src/lib.rs#L157)). When this lands, correct those three in DATA-MODEL.md so the doc matches the binary. Listed in §11.

> **Every construction site populates the new fields.** Adding non-`Option` `captured_at`/`status` means *every* `LimitWindow` constructor must set them: the `unavailable_limit()` helper (new signature `fn unavailable_limit(provider: ProviderId, kind: LimitKind, captured_at: DateTime<Utc>, status: LimitStatus) -> LimitWindow`), the Codex constructor (§7), and **every test fixture / snapshot helper** ([e.g. `limits()`](../apps/cli/src/render.rs#L1934-L1981)). A compile error here is the feature working — but the brief calls it out so it's planned, not discovered. For windows with **no actual reading** (`Unavailable`/`Estimated`), `captured_at` is the **collection time** (`generated_at`) — there is no observation instant, so it records when Costroid looked, keeping the field meaningfully non-`Option`.

### 1.2 `LimitAvailability` (core/summary layer) — gains two render states

This is the wiring detail the research surfaced: rendering does **not** read `LimitWindow.status` directly — it reads `LimitAvailability`, derived in [`limit_availability()`](../crates/costroid-core/src/lib.rs#L659-L691) and consumed by [`render_limit_line()`](../apps/cli/src/render.rs#L1065-L1118). Current variants ([core/src/lib.rs:1007-1024](../crates/costroid-core/src/lib.rs#L1007-L1024)): `Available { used_fraction, resets_at, reset_in_seconds }`, `Partial { used_fraction?, resets_at?, reset_in_seconds?, reason }`, `Unavailable { reason }`.

The new `status` must flow into this enum so the now-screen renders **absent vs unverified vs estimated vs stale vs unavailable distinctly** (the must-nail). Add two variants:

```rust
pub enum LimitAvailability {
    Available   { used_fraction, resets_at, reset_in_seconds },       // status == Verified, fresh
    Unverified  { used_fraction, resets_at, reset_in_seconds, reason },// status == Unverified: show % but flagged
    Estimated   { window_tokens: TokenTotals, est_value: Option<Decimal>, reason }, // no trustworthy %, show volume/value
    Partial     { used_fraction?, resets_at?, reset_in_seconds?, reason }, // existing (incomplete but not flagged)
    Unavailable { reason },                                            // nothing usable
}
```

**Two responsibilities, kept separate so there's no mutate-vs-return ambiguity:**
- **A core finalize pass** (in `collect`/`now_summary`, before rendering) sets the *one* status that needs usage: the `Verified → Unverified` cross-check demotion (§4b.6). It rewrites the window's `status` on the snapshot Costroid owns, so **`LimitWindow.status` ends up the single source of truth holding the true `Verified`/`Unverified`/`Unavailable`** (honoring DATA-MODEL). The provider only ever sets the *provisional* `Verified`/`Unavailable`.
- **`limit_availability()`** ([core/src/lib.rs:659](../crates/costroid-core/src/lib.rs#L659) — today `fn(&LimitWindow, generated_at) -> LimitAvailability`) then becomes a **pure map** to the render verdict, with the volume added for the `Estimated` payload: `fn limit_availability(limit: &LimitWindow, generated_at: DateTime<Utc>, window_volume: TokenTotals) -> LimitAvailability`. It does **not** mutate the window. Final mapping:

| finalized `status` | at render (`resets_at` vs `generated_at`; `window_volume`) | → `LimitAvailability` |
|---|---|---|
| `Verified` | fraction + reset present, **not** stale | `Available` |
| `Verified` | fraction present, **reset unknown** (`resets_at` parsed to `None`), not stale | `Partial` (meter + "reset unknown") |
| `Verified` / `Unverified` | **stale** (`resets_at < generated_at`) | age out → `Estimated` if `window_volume > 0`, else `Unavailable` |
| `Unverified` | fraction present, not stale | `Unverified` (% shown, flagged; only reachable from an in-range `Verified` demoted by §4b.6, so fraction is always `Some`) |
| `Unavailable` | absent / poisoned / sanitized | `Estimated` if `window_volume > 0`, else `Unavailable` |

`Partial` (pre-existing) survives only for the "fraction known, reset unknown" case above; every other path resolves to one of the other four. Note this **also changes Codex's path** (§7): Codex windows arrive `Verified`, so they hit the same `Available`/`Partial`/age-out rows — no special-casing, which is the point.

---

## 2. The capture mechanism (ARCHITECTURE §8 — preference-ordered)

Claude Code invokes the configured `statusLine` command on each new assistant message / after `/compact`, piping a JSON session object on **stdin** whose `rate_limits` block carries the live quota. Costroid captures it by **being in that pipeline** — never by calling an Anthropic endpoint. The field only changes on a **new API response**: Claude Code's `refreshInterval` re-renders the line but does **not** freshen the quota (ARCHITECTURE §12), so every captured value is as fresh as the last assistant turn, not wall-clock-current — which is why the freshness disclosure in §8 is **always-on**, not stale-only.

**The cache file (no secrets):** atomic-written JSON at `${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json`:

```json
{ "captured_at": "2026-06-05T09:50:00Z",
  "five_hour":  { "used_percentage": 78, "resets_at": 1781000000 },
  "seven_day":  { "used_percentage": 41.5, "resets_at": 1781400000 } }
```

It holds only two percentages + two reset stamps + a capture time — **no token, no prompt, no credential** (ARCHITECTURE §8). Writes are atomic (temp file + rename) so a concurrent `costroid` read never sees a torn file.

Three capture paths, in preference order:

1. **Paste-in snippet (preferred).** The user adds one line to their *existing* `statusLine` script that tees `rate_limits` into the cache and passes the original input through untouched. Contract:
   - Read stdin **once** into a variable, then reuse it (the wrapped/real renderer also needs it — consume-once-or-blank, see path 3).
   - Extract `.rate_limits` (jq or `costroid statusline --capture-only` reading stdin), atomic-write the cache, **never** block: if parsing fails, write nothing and exit 0 so the user's line still renders.
   - This is the lowest-risk path and what `setup-statusline` emits by default.
2. **Costroid *is* the statusline.** For users with no existing `statusLine`, `setup-statusline` points Claude Code at `costroid statusline` itself, which reads stdin, captures the cache, and emits its own one-line status. One process, no wrapping.
3. **Wrapper (hazardous fallback only).** Costroid wraps the user's existing command. **Must-nail mechanics — the brief spells these out because this is the path most likely to silently break someone's prompt:**
   - **Tee stdin:** Claude Code's JSON arrives once on stdin. Costroid must capture it *and* hand the identical bytes to the wrapped command — read fully into memory, then feed a copy to the child. Consume it once without duplicating and **the wrapped command renders blank.**
   - **Debounce/cancel budget:** the status line runs on every update and a slow script blocks it; an in-flight script is cancelled when a new update arrives. Wrapping stacks Costroid's latency on top of the user's command, pushing toward that cliff. Capture must be near-instant (parse + atomic write, no scanning logs).
   - **Render-something-on-failure:** if the wrapped command errors or times out, Costroid still prints *something* (a minimal status from cache, or a blank line) and exits 0 — a non-zero exit or a panic must never take down the user's status line.

`setup-statusline` defaults to **path 1** when an existing `statusLine` is found (inject the capture snippet) and **path 2** when none exists. **It does not auto-wire path 3** — path 1's injection already covers the existing-statusline case. Wrapping is a documented **manual escape hatch only** (for a `statusLine` the user genuinely can't edit), invoked as `costroid statusline --wrap '<command>'`; its tee-stdin / debounce / fail-render mechanics above are specified so it's buildable, but it is **not** on the `setup-statusline` happy path.

---

## 3. `costroid setup-statusline` (the adoption gate — must-nail: correct root, idempotent, undoable)

New `Command::SetupStatusline(SetupStatuslineArgs)` variant alongside the existing `Trends`/`Frontier`/`Statusline`/`Export` ([apps/cli/src/main.rs:26-33](../apps/cli/src/main.rs#L26-L33)), dispatched to `run_setup_statusline()`.

**Correct root.** It must write `<config_root>/settings.json` at the **resolved** root, not a hardcoded `~/.claude` — where `config_root` is the directory holding both `settings.json` *and* `projects/` (the config root, not a logs-only path). Reuse [`HostEnv::claude_roots()`](../crates/costroid-providers/src/lib.rs#L66-L76), which honors `CLAUDE_CONFIG_DIR` (comma-separated) first, then `~/.config/claude`, `~/.claude`, then WSL Windows profiles. **`claude_roots()` returns a merged list for log *discovery*, but settings can be written to only one place** — pick the single root Claude Code reads settings from (the **first** existing; a set `CLAUDE_CONFIG_DIR`'s first entry wins) and **print it before writing**.

**Idempotent + undoable (must-nail — friction or a clobbered config here is the real adoption risk, not a cosmetic one):**
- Read the existing `settings.json` (preserve unknown keys; round-trip the JSON, don't rewrite the file from a template).
- If no `statusLine` is set → install path 2 (`costroid statusline`).
- If a `statusLine` already exists → install path 1 (inject the capture snippet into it) and record the original so it can be restored; never silently clobber a working `ccusage`/`ccstatusline` setup.
- **Idempotent:** running it twice is a no-op. The **marker is concrete and versioned**: path 1 injects the exact sentinel `# costroid:statusline-capture v1` into the user's script; path 2 sets the `statusLine` command to the known `costroid statusline` string. Detect either → skip. The version lets a future injection format migrate cleanly. The same marker drives `--undo`, which also restores from the `settings.json.costroid-bak` backup when the original `statusLine` was non-trivial.
- **Undoable:** `costroid setup-statusline --undo` restores the recorded original `statusLine` (or removes the key if there was none) and removes the cache file.
- Back up `settings.json` to `settings.json.costroid-bak` before the first write.

(There is no Costroid TOML config yet — ARCHITECTURE §4 plans `~/.config/costroid/config.toml` but it is unbuilt; this command writes Claude Code's settings, not Costroid's. The cache path is a constant for now, configurable later.)

**Companion flag — `costroid statusline --capture-only` (the surface path 1 depends on).** The preferred snippet (§2 path 1) calls this, so it must be a real flag on the existing `Statusline` command, not an assumed one:
- Read stdin **fully into memory once**, extract `.rate_limits`, sanitize/shape it into the cache JSON (§2), and **atomic-write** the cache.
- Emit **nothing** to stdout and **exit 0** — it is a side-effect, not a renderer.
- **Exit-0-on-parse-failure contract:** malformed/absent stdin or a missing `rate_limits` block → write nothing, still exit 0, so a bad payload can never break the user's prompt. Never panic, never non-zero.
- `setup-statusline` emits the path-1 snippet that pipes Claude Code's stdin through `--capture-only` and then on to the user's real renderer.

---

## 4. The pipeline, split across two layers (the layering fix)

The cross-check and the estimate need per-window *usage* volume, which exists only at the **core** layer (focus rows + `generated_at`); the provider's `parse_limits` sees only the cache. So the pipeline splits — the provider does what it can from the cache; core finalizes using usage it alone can see.

### 4a. Provider — `ClaudeCodeProvider::parse_limits` (cache → sanitize → provisional window)

Today this is a stub returning `unavailable_limit()` for both windows ([providers/src/lib.rs:246-251](../crates/costroid-providers/src/lib.rs#L246-L251)). Replace it with, per window (`five_hour`→`FiveHour`, `seven_day`→`Weekly`):

1. **Read** the cache (§2). Absent/unreadable/missing-window → `unavailable_limit(ClaudeCode, kind, captured_at, Unavailable)`. Never error the run.
2. **Sanitize the RAW percentage, before ÷100** (ARCHITECTURE §9.2 — order matters). On the raw `used_percentage` (0–100): if `> 100` (the 900% bug / out-of-range) **or** `== resets_at` (the poisoned-epoch leak) → **no data** → `status = Unavailable`, `used_fraction = None`. Only on passing: `used_fraction = Some(pct / 100.0)`.
3. **Parse `resets_at` defensively** — **both epoch seconds and ISO-8601 (RFC 3339)** appear across Claude Code versions (ARCHITECTURE §12). Try integer-epoch (reuse [`epoch_seconds()`](../crates/costroid-providers/src/lib.rs#L947-L952)), then RFC 3339; neither → `None`.
4. Set `captured_at` from the cache. **Provisional `status`:** `Verified` if a sane in-range reading survived step 2, else `Unavailable`. The provider does **not** cross-check — it can't see usage.

### 4b. Core — finalize status + render state (cross-check + age-out + estimate)

In `now_summary` / the `limit_availability()` refactor (§1.2), which has the snapshot's focus rows and `generated_at`, per window:

5. **Compute `window_volume`** for the window (§5 helper).
6. **Finalize status — the cross-check (#31820 guard; flag, don't suppress, don't trust).** If a `Verified` reading is **high** (`used_fraction ≥ X`) but `window_volume` is **trivial** (sum `< N`, §5), set the window's `status = Unverified`. The cross-check can only *flag* — a genuinely high reading can be real (shared claude.ai-chat usage, or one heavy prompt) — so it never rewrites the number; it lowers confidence (DATA-MODEL: the local estimate is a *validator when present*). This is the finalize pass (§1.2); after it, `LimitWindow.status` is the source of truth.
7. **Map** the finalized window: `limit_availability(window, generated_at, window_volume)` → a `LimitAvailability` variant (§1.2 table). Stale age-out (`resets_at < generated_at` → `Estimated`/`Unavailable`) happens **here, in the pure map** against the current `generated_at` — not as a status change — so `--live` re-evaluates it each tick.

---

## 5. The cross-check threshold + the per-window volume helper (one of the two genuinely-open items)

**New helper needed — it does not exist today, and it lives in `costroid-core`** (the cross-check and estimate run at the core layer, §4b, which has the focus rows; the provider can't). The research confirms [`AggregateTotals.tokens`/`TokenTotals`](../crates/costroid-core/src/lib.rs#L935-L945) is the per-meter shape, but **no per-window (last-5h / last-7d) token sum exists**. Add:

```rust
/// Per-window local token volume, summed from the snapshot's FOCUS rows
/// (filter by x_Tool + ChargePeriodStart inside the trailing window for `kind`).
/// Returns the per-meter TokenTotals so the Estimated render can show the breakdown;
/// the cross-check uses its scalar sum. In costroid-core, not the provider.
fn window_token_volume(rows: &[FocusRecord], tool: ProviderId, kind: LimitKind, now: DateTime<Utc>) -> TokenTotals
```

It feeds **two** consumers:
- **The cross-check (§4b.6):** a *bound*, not a conversion. We cannot turn tokens into a % (the plan's token cap is unpublished — the denominator problem). So the check is one-directional and conservative: *"the field claims ≥X% but the window logged < N tokens — implausible → `Unverified`."* It never says a reading is *correct*.
- **The estimate fallback (§6):** the per-meter volume (and its priced $ value) shown when there's no trustworthy %.

**The open threshold — `N` (trivial floor) and `X` (high):** quantified here, not hand-waved into code, but the exact `N` is the one genuinely-open number. `X = WARN_FRACTION` (0.80, reuse [the existing constant](../apps/cli/src/render.rs#L19-L20)). `N` is an **absolute summed-token floor** (not a %, because there's no denominator) below which a ≥`X` reading is demoted. **Concrete starting value so the code compiles: `const UNVERIFIED_TOKEN_FLOOR: u64 = 5_000;`** (summed across meters) — small enough that only an implausible "near-max on almost no usage" trips it, since one heavy prompt legitimately burns far more. The check is **safe-directional**: it only ever demotes to `Unverified` (flag), so an over-conservative `N` mislabels a real reading as unverified (annoying, never a confident-wrong number); an under-conservative `N` lets a false-100% through (the failure we're guarding). Bias `N` low.

**Tie to your live-install check:** whether #31820's false-in-range (flat 100%, no throttling) ever actually fires on *your* binary decides whether this cross-check is **mandatory** or merely **prudent**. Build the guard either way — but the answer sets how conservative `N` is: if you observe a false-100% in practice, tighten `N` (demote more aggressively); if it never fires, `N` can sit at the floor as a cheap insurance check.

---

## 6. The absent→estimate fallback + Opus 7d (must-nail: labeled, never blank, never fabricated)

When a window has no trustworthy % (`Unavailable` from §4: absent for API-key users, the #40094 intermittent drop, pre-first-response, sanitized-out, or aged-out-stale):

- **Do not blank it, and do not invent a %.** At the core layer (§4b/§1.2: `Unavailable` or aged-out-stale **with** nonzero `window_volume` → `Estimated`), show the window's **per-meter token volume + estimated $ value** from local logs (via §5's helper), explicitly labeled *"Claude Code's quota number is unavailable — this is your **Claude Code** usage this window (estimated; excludes claude.ai chat),"* with the quota **% marked unavailable**. The wording must not imply it is the account-wide number — it pairs with the §8 chat-under-report disclosure. No quota meter is drawn (there's no denominator to fill one). If `window_volume` is zero as well → `Unavailable` (nothing to show). The `est_value` is the **existing cost calculator** applied to `window_volume` (per-meter tokens × bundled price); `None` when the model is unpriced (`x_PricingStatus != "priced"`) — show the volume alone then, never a guessed price.
- This unifies with the **Opus 7d** treatment (ARCHITECTURE §8, DATA-MODEL "Opus weekly is not a `LimitWindow`"): the per-model Opus cap is *never* observable, so it is **always** shown as volume + value with the % unavailable. **Operationalize it as a dedicated field on `NowSummary`** (e.g. `opus_weekly: Option<{ tokens: TokenTotals, est_value: Option<Decimal> }>`) rendered as its own line by `render_now`/`plain_now` — it is **not** a `LimitWindow`, never enters `snapshot.limit_windows` or the meter path, so there is structurally **no place to put a fabricated fraction**. The only difference from the 5h/7d fallback is that Opus's is permanent, not conditional. If there is no Opus usage in the window, **omit the line entirely** (nothing to disclose) — don't render "0 tokens".

**Opus-heavy framing (the calibrated wording from the Opus decision):** lead with the overall **7d % as the real number Costroid can measure — not "your binding cap."** Show Opus 7d volume/value beside it. Disclose that for a ~97%-Opus user the **Opus weekly may bind first** and its % is invisible to the hook, so a near-limit alert on the 7d window may not be the window actually throttling. Never present the 7d % as the definitive ceiling.

---

## 7. The `LimitWindow` shape ripple (must-nail: Codex too, not Claude-only)

The §1.1 shape change touches **every** existing `LimitWindow` producer. There is exactly one besides the new Claude path: **Codex**. (Cursor produces **no** `LimitWindow` — [`parse_limits` returns `Vec::new()`](../crates/costroid-providers/src/lib.rs#L325) and its deferral rides on `ProviderStatusKind::Detected` + message — so it stays out of this path entirely and needs no change here.)

**Codex** ([`parse_codex_limit`](../crates/costroid-providers/src/lib.rs#L895-L917)) must populate the new fields:
- `status = Verified` — **always**. Codex's windows come from sanctioned local rollout logs, not the buggy `rate_limits` field, so the cross-check **never applies** to them. They are trusted on arrival.
- `captured_at` = the **latest rollout entry's timestamp** (the entry the `rate_limits` came from). `parse_codex_limit` currently doesn't receive it — thread the entry timestamp from `parse_codex_limits` into the constructor.
- **Shared age-out:** Codex windows have the same fresh-while-coding profile, so the *same* `resets_at` age-out in `limit_availability()` (§1.2) covers them — a stale Codex window ages to `Estimated`/`Unavailable` exactly like Claude's. No Codex-specific staleness logic.

**Why this is a requirement, not a footnote:** if only the Claude path sets `captured_at`/`status`, you ship a half-populated struct and the now-screen has to special-case which windows carry the fields. Populating Codex too means [`render_limit_line()`](../apps/cli/src/render.rs#L1065-L1118) branches on `status`/availability **uniformly across providers** — that uniformity is the whole reason the fields live on the struct.

---

## 8. Rendering the states (must-nail: distinct renders)

[`render_limit_line()`](../apps/cli/src/render.rs#L1065-L1118), [`plain_limit_line()`](../apps/cli/src/render.rs#L1120-L1155), and the statusline path get arms for the new `LimitAvailability` variants. Reuse the existing meter primitives ([`limit_meter_span`/`positional_meter_text`](../apps/cli/src/render.rs#L1302-L1327)) and the always-visible cue convention ([`state_cue`](../apps/cli/src/render.rs#L1240-L1268) — `!`/`!!`/`OVER`, never color-only).

**After this work `LimitAvailability` has five variants** — `Available`, `Unverified`, `Estimated`, `Partial` (pre-existing), `Unavailable`. **"Stale" is *not* a variant** — it is a *condition* (`resets_at < generated_at`) that §4b.5 resolves *into* `Estimated`/`Unavailable`, carrying an `"as of HH:MM (stale)"` stamp from `captured_at`. Render arms:

- **Available** (Verified, fresh): unchanged — meter + `%` + state cue + `resets …`.
- **Unverified:** meter + `%` + a **mandatory distinct cue**, recommended `" ? unverified"` (plain text, survives `--plain`/`NO_COLOR`), so a near-max reading renders e.g. `"96% ? unverified"` — never a confident `"96% !!"`. Carries the fraction so the meter still draws.
- **Estimated:** **no quota meter**; show `"<tool> <kind> usage: <summed window tokens> (~$<value>, estimated) — quota % unavailable"` (per-meter breakdown available from the `TokenTotals` if wanted). The Opus 7d line (§6) uses this same shape.
- **Unavailable:** unchanged — `"<tool> <kind> unavailable: <reason>"`, no meter.
- (*Stale* surfaces as one of `Estimated`/`Unavailable` above, plus the stale stamp — not its own arm.)

**Freshness & the push-only disclosure (always-on — ARCHITECTURE §9.2/§12).** Every Claude reading is a *cached push*, never live, so its age must always be visible — not only on the aged-out render. The §4b.5 age-out covers one staleness direction (*too-high after an idle reset*); this covers the other two the canon names — *silently-old* and *too-low-from-chat* — which contradict nothing and so are easy to leave half-built:
- **Always-on "as of HH:MM" stamp.** `Available` and `Unverified` renders carry an `"as of HH:MM"` derived from `captured_at` once the reading is older than `const LIMIT_FRESHNESS_STAMP_MINUTES: i64 = 10;` (starting value, tunable). A reading captured hours ago whose window hasn't reset must **never** render as a bare, confident meter with no age signal. **Codex carries the same stamp** — not because it is a push (it reads local logs), but because its windows are only as fresh as the **latest rollout entry** (`captured_at` from §7); the threshold logic is identical.
- **Chat-under-report caveat.** claude.ai chat shares the same 5h/7d limit but is invisible to the cache, so a Claude meter can read **low**. This direction is *disclosable, not fixable* (§9.2): carry a caveat such as *"reflects Claude Code's view; if you've used claude.ai chat this window your true usage may be higher."* Compact/minimal presets may shorten it, but it must remain reachable.

**Statusline selection:** the selection helpers in the render path (`most_constrained_limit` → `has_fraction` → `limit_fraction`) pick the highest-fraction limit and today exclude anything without a fraction. **`limit_fraction()` must gain an `Unverified` arm** (it currently returns `Some` only for `Available` and `Partial { Some }`) so an `Unverified` window is eligible — **and if selected, the one-line output must carry the `? unverified` cue**: a maxed-looking statusline that is actually unverified is the exact confident-wrong-number failure §0 forbids. `Estimated` has **no** quota fraction → excluded from "most constrained," like `Unavailable`.

**Format presets** (`default`/`compact`/`minimal`) named in [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md) are still unimplemented in `render_statusline_line` ([render.rs:747-783](../apps/cli/src/render.rs#L747-L783)) — implementing them is in-scope for the statusline step but orthogonal to capture; the capture cue work above must land regardless of preset.

---

## 9. Tests & fixtures (must-nail: bad-data fixtures + snapshots, offline stays green)

**Bad-data fixtures** under [`fixtures/claude-code/`](../fixtures/claude-code/) (valid JSON, semantically edge-case, **no real user data/secrets**), each a `claude-rate-limits.json`-shaped cache plus, where relevant, a transcript with matching/mismatching token volume so the cross-check is exercised:
- `rate-limits-poisoned-epoch.json` — `used_percentage == resets_at` epoch → sanitized → `Unavailable`/`Estimated`.
- `rate-limits-impossible-900.json` — `used_percentage: 900` → `> 100` → sanitized out.
- `rate-limits-false-100.json` — `used_percentage: 100` **with a trailing transcript of trivial tokens** → cross-check demotes to `Unverified`.
- `rate-limits-absent.json` — file missing / window key absent → `Estimated` (with logged volume) or `Unavailable`.
- `rate-limits-stale.json` — `resets_at` in the past → aged out.
- `rate-limits-iso-resets.json` — `resets_at` as an ISO string (defensive-parse coverage).
- (positive) a `Verified` Claude window so the happy path is snapshotted too.

**Snapshots** ([apps/cli/src/snapshots/](../apps/cli/src/snapshots/), naming `costroid__render__tests__snapshot_*`): add helper(s) alongside [`limits()`](../apps/cli/src/render.rs#L1934-L1981) producing the new availability variants, and `snapshot_now_*` / `snapshot_statusline_*` cases for **Verified, Unverified, Estimated, stale, Unavailable** across all four modes (braille+ansi, braille, ascii, plain). **Include a Codex `Verified` window in the same fixture** so the snapshot proves the renderer treats `status` uniformly and isn't quietly Claude-only (§7).

**Offline acceptance stays green:** the capture reads a local file and writes a local file — **no new dependency** may enter the tree. [`apps/cli/tests/offline.rs`](../apps/cli/tests/offline.rs) (the `FORBIDDEN_CRATES` static check) and `scripts/offline_acceptance.sh` must both still pass; no `reqwest`/`tokio`/`rustls`/telemetry crate is introduced (ARCHITECTURE §8 — Phase 1 envelope). The snippet/cache approach is chosen precisely so this holds.

---

## 10. Security invariants (ARCHITECTURE §8 — do not regress)

- The cache holds **only** two percentages, two reset stamps, and a capture time. No token, prompt, credential, or content ever written.
- **Zero** Anthropic-endpoint calls; **zero** credential reads. Claude quota is sanctioned *because* it arrives through Anthropic's own `statusLine` extension point — the opposite of the codexbar pattern.
- The captured cache is **untrusted input**: parsed defensively, malformed → "unavailable", never a crash, never evaluated.
- `setup-statusline` writes only Claude Code's `settings.json` (with backup + undo); it stores nothing secret and runs no network.

---

## 11. Doc-drift to true up when this lands

- [DATA-MODEL.md](DATA-MODEL.md) `LimitWindow`: correct three field types to match the real code (§1.1) — `used_fraction: f64` → `Option<f64>`, `resets_at: DateTime<Utc>` → `Option<DateTime<Utc>>`, and `tool: String` → `tool: ProviderId`. Add the `LimitAvailability` `Unverified`/`Estimated` variants to the doc's rendering notes if the doc enumerates them.
- [DATA-MODEL.md](DATA-MODEL.md) `LimitKind`: note that the Cursor reset-window variant (Phase 2) would add `Daily` — additive/non-breaking per the research.

---

## 12. Open items / decisions for review

1. **The cross-check floor `N`** (§5) — the one genuinely-open number. Brief proposes a low absolute token floor with `X = 0.80`; final value tied to whether #31820 fires on the live binary. *Decision needed: confirm `N`'s starting value and whether the cross-check is mandatory or prudent.*
2. **`LimitAvailability` extension shape** (§1.2) — adding `Unverified` + `Estimated` variants vs. carrying a `confidence`/`basis` tag on the existing variants. Brief recommends explicit variants (clearer match arms, snapshot-distinct). *Reviewable.*
3. **Stale → `Estimated` vs `Unavailable`** (§1.2/§4b.5) — staleness is evaluated **at render time** in `limit_availability()` against the current `generated_at` (the provider only records `resets_at`, never freezes a verdict; this covers `Verified` and `Unverified` uniformly). A stale reading ages to `Estimated` when local usage exists, else `Unavailable`. Confirms canon "age a stale reading out to unknown." *Reviewable.*
4. **Cache path** — `${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json`. *Confirm location.*
5. **Concrete starting constants** (set so the code compiles; all tunable): `UNVERIFIED_TOKEN_FLOOR = 5_000` (§5, the open one — tied to the live-install #31820 datapoint), `LIMIT_FRESHNESS_STAMP_MINUTES = 10` (§8), and the sentinel `# costroid:statusline-capture v1` (§3). *Confirm or adjust.*

---

*Review against ARCHITECTURE §8/§9.2 and the `LimitWindow` shape, as planned. The two items the canon left genuinely open — the §5 token floor and the §1/§4/§7 `rate_limits`→`LimitWindow{captured_at,status}` wiring — are resolved here as far as a spec can without the live-install datapoint for `N`.*
