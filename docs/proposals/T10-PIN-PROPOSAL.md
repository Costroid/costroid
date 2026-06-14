# T10 pin proposal — the `connect`/`disconnect` CLI + Connections view + reconciliation surface (the first caller of `costroid-connect`)

> **STATUS: ⛔ SIGNED OFF 2026-06-13 — accepted with ONE amendment** (OpenAI connect-time validation probes **`GET /v1/organization/costs`** directly — the exact endpoint T10c reconciliation depends on — not `/usage/completions` and not `admin_api_keys`; §2.2). The pins below are now canon: T10a is carded at PRODUCT-PLAN §12.14, T10c at §12.15, the deferred live-confirm card **T10-LIVE-ROWS** at §12.16, and the pins logged in §11.5. This is the concrete pin proposal that PRODUCT-PLAN §12.8 (the pin-then-card prompt) required before **T10** could be carded. T10 is the FIRST caller of `costroid-connect` and the FIRST real network in the product. Where a decision was an external/ToS/product/CLI-surface call it was presented as options + a recommendation and **⛔ STOPPED for the human** (§2 OpenAI validation; §1 the CLI shape; §11 the two gates) — all resolved as recorded inline. *(Endpoint research + adversarial doc-verification done 2026-06-13 by a 7-agent workflow — 3 researchers → 3 adversarial doc-verifiers → a completeness critic — with every endpoint claim checked against live official docs; this file is the durable record of that research. The non-endpoint pins are derived from the code on disk, which is canon.)*

This proposal mirrors the structure of `docs/proposals/T9-PIN-PROPOSAL.md` (the ⛔-signed-off T9 endpoint pins). **T9 is complete** — the keychain store (T8), the authorized-host HTTP client (T9a), the Anthropic + OpenAI usage/cost adapters (T9b), and the pure-core reconciliation engine (T9c) are all built and verified — but **nothing calls them**: no `costroid` build performs a network call until T10's explicit, user-initiated `connect` action. T10 wires that first caller.

**T10 scope constraint this proposal is pinned against** (PRODUCT-PLAN §5 tier 3, unchanged from T9): the user's **own key** — a single static, pasteable secret string per vendor, stored only in the OS keychain (`CredentialStore`), sent strictly device↔provider over `ureq`+`rustls` via the already-built `AuthorizedClient`. **No OAuth, no browser, no session reuse, no undocumented/internal endpoint, ever** (tier 4 is the ToS line). HTTPS GET only. Network occurs **only** on an explicit `connect`/`reconcile`/`connections --check` action; the default build still makes **zero** network calls (the strace default-tier gate stays green).

---

## 1. The pins at a glance

| Decision | Pin | Status |
|---|---|---|
| **CLI surface** (§1.1) | `costroid connect <vendor>` (stdin key entry, never argv/env) · `costroid disconnect <vendor>` · `costroid connections [--check]` · `costroid reconcile [--vendor]` | ⛔ public-CLI surface — sign off |
| **Anthropic connect-time validation** (§2) | `GET https://api.anthropic.com/v1/organizations/me` — `x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01`; returns `{id,name,type}` only; **zero billing data**; predicts cost-fetch access (same Admin-API gate) | **pin** (live-confirmed) |
| **OpenAI connect-time validation** (§2) | **SIGNED OFF: probe `GET /v1/organization/costs`** (a recent COMPLETED 1-day window, `limit=1`) — the exact endpoint T10c depends on; 200=success→store, 401/403=failure. (Not `/usage/completions`; not `admin_api_keys`.) | **pin** |
| **Connections view** (§4) | `costroid connections` subcommand (local-only by default; `--check` re-validates live); lists Anthropic/OpenAI status + Gemini "unavailable"; non-color status cue | recommend |
| **Reconciliation display** (§5) | `costroid reconcile` subcommand surfacing T9c `CostReconciliation` (signed variance, typed vendor-absence never `$0`, caveats footnoted); --plain + non-color cue | recommend |
| **⛔ GATE 2b** (§6) | Resolved by T10a's live-confirm run with the human's own key, OR each unverified item formally deferred to a named card with a locked criterion | ⛔ gate |
| **Connect-action offline test** (§7) | Two layers: a `--features connect` integration test (loopback `MockServer` + keyring mock) proves the positive; the strace script proves fail-closed/no-residue | pin |
| **OAuth (tier 2)** (§8) | **DEFER** — T10 is own-key only | recommend defer |
| **API-lane rate-limit denominators** (§9) | **DEFER** from T10's first cut; if/when an API-lane meter is built, pin only Anthropic `GET /v1/organizations/rate_limits`, render OpenAI "unavailable" | recommend defer |
| **Split** (§10) | **T10a** (connect/disconnect/connections + validation + connect-action test + GATE-2b) → **T10c** (reconciliation display) → **T10b** (release, already carded §12.10) | recommend |

---

## 1.1 Decision 1 — the CLI surface (⛔ public-CLI surface; sign off)

Changing the public CLI surface is a CLAUDE.md "ask first". The proposed shape extends the existing `clap` enum in `apps/cli/src/main.rs` (today: `Trends`/`Frontier`/`Statusline`/`SetupStatusline`/`Export`) with four subcommands. `<vendor>` is a `clap` `ValueEnum` over **`anthropic | openai | gemini`** (the `ApiVendor` axis already in `costroid-connect`).

**`costroid connect <vendor>`**
- **`gemini`** → prints the pinned first-class-unavailable line `unavailable — no sanctioned static-key usage API` (the `GEMINI_UNAVAILABLE_MESSAGE` const; ASCII-folded in `--plain`/non-TTY) + one line on why, and **exits 0 without prompting for or accepting a key**. (Gemini is a recognized vendor with a known answer, not an error.)
- **`anthropic | openai`** → the connect flow:
  1. **Read the admin key from stdin — NEVER argv, NEVER env.** argv leaks to the process table (`ps`); env leaks to child processes and shell history. On a **TTY**, a hidden (no-echo) prompt; on **piped** stdin, read one line (so `echo "$KEY" | costroid connect anthropic` and secret-manager pipelines work). *(The build agent picks a lean permissive no-echo crate — e.g. `rpassword`, MIT — or a small termios shim; that is a decide-on-own dep choice. The pinned behavior is stdin-only, no-echo-on-TTY.)*
  2. **Wrong-key-class prefix check BEFORE any network** (reuse the adapters' `wrong_key_class`): `sk-ant-admin` for Anthropic, `sk-admin-` for OpenAI. On mismatch: print `that looks like a <seen-prefix> key; <vendor> usage needs a <expected-prefix>… admin key` and **do not store**, exit nonzero. (The key is never echoed; only its prefix is inspected, via `expose_secret()`.)
  3. **Validate the key** with the §2 connect-time call (touches no billing/spend data). On success, capture the org label (Anthropic `name`/`id`).
  4. **Store + record:** `CredentialStore::store(vendor, secret)` (keychain only) → `ConnectionRegistry::mark_connected(vendor)`. Print `Connected <vendor> — organization <name> (<id>). Key stored in your OS keychain.`
  5. **On validation failure** (typed `VendorReportUnavailable`): print the typed reason + remediation — `AuthenticationFailed` → "the key was rejected"; `AccessForbidden{IndividualAccount}` → "Anthropic's Admin API is unavailable for individual accounts — create an organization first"; `AccessForbidden{MemberNotOwner}` → "use an admin key created by an organization Owner"; etc. **Do not store**, exit nonzero. Never echo the key.

**`costroid disconnect <vendor>`** → `CredentialStore::delete(vendor)` (idempotent) + `ConnectionRegistry::mark_disconnected(vendor)` (idempotent). Print `Disconnected <vendor>.` and exit 0 even if nothing was stored (idempotent revoke). No network.

**`costroid connections [--check]`** and **`costroid reconcile [--vendor <v>] [--period …]`** — see §4 and §5.

**⛔ APPROVED 2026-06-13** as proposed: the four subcommand names/shapes, the stdin-only key entry (no-echo on TTY, one line on a pipe; never argv/env), the prefix wrong-class check before any network, the `gemini` → pinned-unavailable-line/exit-0/no-key behavior, and the typed-failure remediation copy.

---

## 2. Connect-time key validation — the sanctioned "is this key good?" call that touches NO billing data

The goal: at `connect`, prove the pasted admin key works (and capture an org label) **before** storing it, **without** reading any spend/cost figures — and so that a "Connected" state reliably predicts the real usage/cost fetch will succeed.

### 2.1 Anthropic — **PIN** `GET https://api.anthropic.com/v1/organizations/me`

- **Endpoint:** `GET /v1/organizations/me`. **Auth:** `x-api-key: <sk-ant-admin… key>` + `anthropic-version: 2023-06-01` — the **identical** header pair already ⛔-signed-off for the T9 `cost_report`/`usage_report` endpoints, so **no new auth surface**. **Key class:** `sk-ant-admin` (the same single pasteable secret T9 already stores).
- **Response:** a JSON object with exactly three documented fields — `id` (org id), `name` (org name), `type` (always the literal `"organization"`). **No billing/usage/cost/spend field is documented.** (`touches_billing_data = false`.) Example: `{"id":"…","name":"Organization Name","type":"organization"}`. The overview page states it is "useful for programmatically determining which organization an Admin API key belongs to."
- **Why it's the right pre-flight:** it is HTTPS-GET-only, exposes zero billing data, and is gated by the **same Admin-API org-gate** as `cost_report`/`usage_report` (all accept `sk-ant-admin`; the Admin API is "unavailable for individual accounts", and unavailable on Claude-Platform-on-AWS orgs). So a 200 on `me` reliably predicts the cost/usage fetch will succeed — an ideal cheap fail-fast that reads **no** billing data. Capture `name`/`id` for the Connections view's org label.
- **Availability/error handling:** the docs do **not** pin the exact status for an ineligible (individual / AWS-Bedrock) account — treat **any non-200 as "invalid key / not an eligible org"** (generic non-200 handling); capture the real status/body at the GATE-2b live run (§6). The `AuthorizedClient` already classifies 401→`AuthenticationFailed`, 403→`AccessForbidden{hint}`; T10 adds a tiny `me`-parsing path (or reuses the adapter's classification) — the build agent adds the `me` call to `AnthropicAdapter` as a non-billing `validate()` method.
- **Auth-source citation note (verifier catch):** the dedicated reference page (`…/admin-api/organization/get-me`) leads with the **OAuth** `Authorization: Bearer` example; the `x-api-key` + `anthropic-version` example for this exact endpoint is on the **overview** page (`…/manage-claude/admin-api`). The static-key pair is documented and is the T10-compliant choice — **do not "fix" it to OAuth** (OAuth is out of scope, §8).
- **Alternative considered & rejected:** `GET /v1/organizations/api_keys` (List API keys; same `sk-ant-admin` + `x-api-key`, no billing) — the structural mirror of OpenAI's `admin_api_keys`. Rejected for `me`: `me` is lighter (single identity object, no list, no key metadata/PII) and gives the org label directly.

### 2.2 OpenAI — **OPTIONS + recommendation** (⛔ external call; sign off)

There is **no clean OpenAI analogue of `me`**. The completeness critic surfaced a material, load-bearing gap that **must** be resolved before sign-off:

> **The scope-equivalence gap.** OpenAI uses **per-resource scopes** (`api.<resource>.read`). `GET /v1/organization/admin_api_keys`, `/projects`, `/users` are confirmed to require **`api.management.read`** (live community thread quotes the verbatim 403 `Missing scopes: api.management.read`). But the **cost/usage** endpoints the tool actually calls (`/v1/organization/costs`, `/v1/organization/usage/completions`) likely require a **different, usage-class scope** (`api.usage.read`/`api.read` — live community evidence: "Missing scopes: api.read" on the billing usage export). The exact cost/usage scope is **not stated on any fetchable official page** (the canonical reference and the permission-mapping help article both 403 non-browser fetches — independently reproduced 2026-06-13). **Consequence:** a management-endpoint pre-flight (`admin_api_keys`) can return 200 for a restricted admin key that then **fails** the cost fetch — a false-green connect. So `admin_api_keys`, the workflow's first pick, is **NOT recommended** as the validation call.

The three options:

- **Option A (SIGNED OFF — amended to probe `/costs`; see the decision block below).** Validate by calling the **same capability the tool uses**, scoped to a tiny window (one recent 1-day bucket, `limit=1`). *Originally proposed against `/usage/completions` (token counts, no spend dollars); the human amended it to probe `GET /v1/organization/costs` directly* — the exact endpoint T10c reconciliation depends on — because "Connected" must predict the **cost** fetch and probing `/costs` itself **moots the costs-vs-usage scope question** entirely. Either way it (a) exercises the **exact scope** the T9 fetch needs (so a 200 truly predicts reconcile success), and (b) is already classified by the built `OpenAiAdapter` (`WrongKeyClass` before I/O, 401→`AuthenticationFailed`, 403→`AccessForbidden`, 200→`Available`) — so it reuses existing typed outcomes with no new endpoint (`fetch_cost_report` over a 1-day completed window *is* the probe).
- **Option B — no separate validation; fail-on-first-fetch.** `connect` stores the key after only the prefix wrong-class check; the first `reconcile` produces the typed outcome the UI surfaces. Simplest, zero validation surface. *Trade-off:* a connect can "succeed" (store a key) that later fails; and the first real fetch is the **cost/spend** endpoint.
- **Option C — `admin_api_keys` pre-flight (NOT recommended).** Clean (zero billing, admin-class-only, redacted metadata) but the scope gap means it can give a **false-green**. Only viable if the live run (§6) confirms the cost/usage endpoints share `api.management.read` — which current evidence contradicts.

**⛔ SIGNED OFF 2026-06-13 — Option A, amended to probe `GET /v1/organization/costs`** (a recent **COMPLETED** 1-day window, `limit=1`), NOT `/usage/completions`. Rationale (the human's): "Connected" must predict the **cost** fetch that T10c reconciliation depends on, so validate against that exact endpoint; it is the user's own data (no real exposure beyond what they connected to read); and probing `/costs` itself **removes the costs-vs-usage scope question entirely** — a 200 proves the precise scope the tool needs. `401`/`403` = failure (reject + remediate, do **not** store); `200` = success → store. The reused `OpenAiAdapter` classification applies; `OpenAiAdapter::fetch_cost_report` over a 1-day completed window **is** the probe. **Confirm the exact behavior at the GATE-2b live run** (§6) — including any post-auth `400` on the window (cf. Anthropic's documented completed-day `400`, §11.5 ✅ T9b → `RequestRejected{400}`; the caller contract is "request completed-day ranges"). Anthropic stays on `GET /v1/organizations/me`. (`admin_api_keys`/Option C dropped: the scope-equivalence gap that motivated it is moot once we probe `/costs` directly.)

---

## 3. (reserved — Anthropic + OpenAI validation are §2)

---

## 4. Decision 3 — the Connections view

**Surface: a `costroid connections` subcommand** (one-shot, `--plain`-friendly). *Recommendation: subcommand now; a richer Providers TUI tab is Step 5 (T11), not T10 — building a tab now would front-run Step 5.*

- **Lists, from `ConnectionRegistry` + keychain presence (LOCAL-ONLY, zero network by default):** each of `anthropic`/`openai` as **`connected`** (key in keychain + registry) or **`not connected`**, and `gemini` always as **`unavailable — no sanctioned static-key usage API`**. Optional non-secret **org label** (the Anthropic `name`/`id` captured at connect) shown beside `connected` — see the registry note below.
- **`--check` (opt-in, network):** re-runs the §2 validation call per connected vendor and shows `verified just now` / the typed failure (e.g. `key rejected`). A user-initiated network action, like `connect`.
- **Disconnect/revoke** is the separate `costroid disconnect <vendor>` (instant; deletes from keychain + registry). The view documents it.
- **Accessibility:** `--plain` linear listing; **status by a text cue, never color alone** (`connected` / `not connected` / `unavailable: <reason>`); the em-dash in the Gemini string is ASCII-folded in `--plain`/`Ascii` exactly as `cursor_detected_message` is.
- **Registry note (small, non-secret schema add):** to show the org label offline, extend `RegistryFile` to optionally store a **non-secret** org label/id per vendor (captured from `me` at connect). *Recommended* (still zero secret material in the file). Alternative: store nothing extra; show the label only under `--check`. (Build-agent's call within this pin.)

---

## 5. Decision 4 — the reconciliation display

**Surface: a `costroid reconcile [--vendor anthropic|openai] [--period day|week|month|year]` subcommand** that surfaces T9c's `CostReconciliation` honestly. *Recommendation: a dedicated subcommand for T10c; folding reconciliation into a History/Models TUI tab is Step 5.*

Flow: for each connected vendor (or the `--vendor`), fetch its cost report (network, via the adapter + stored key) → build `LocalCostEstimate::from_focus_records(rows-scoped-to-that-vendor)` → `reconcile_cost(&local, &outcome)` → render.

- **Vendor scoping (pinned detail).** `LocalCostEstimate` must be built from **only the rows attributable to that billing vendor** (the T9c doc-note: "scope the rows to the one vendor before building it"). Map `FocusRecord` → `ApiVendor`: `x_Tool = "claude-code"` → Anthropic; `x_Tool = "codex"` → OpenAI; `cursor` → neither (excluded — no admin key, no invoice). Feeding two vendors' rows would compare a cross-vendor estimate against one invoice.
- **Honest rendering (every item from the engine; do not flatten):**
  - **Signed variance** per day + per model: `variance = local_estimate − vendor_billed` → render `+$X over` (estimate exceeds invoice) / `−$X under` (invoice exceeds estimate), with `variance_pct` rounded **at the render boundary** (full `Decimal` precision is preserved upstream). Sign carried as text (`over`/`under`), never color alone.
  - **Typed vendor-side absence, NEVER a fabricated `$0`:** `VendorBilled::Unavailable(DayNotCovered)` → "vendor report doesn't cover this day"; `ModelNotInReport` → "not attributed by the vendor"; `ReportUnavailable(reason)` → the typed reason (incl. `NotConnected` → "connect <vendor> first" and Gemini's `NoSanctionedStaticKeyApi` → the pinned string). When absent, `variance`/`variance_pct` are `None` — render "—", never `$0`. (A **local** `$0` against a real billed figure is a genuine "vendor billed a model Costroid never saw" signal — render it as real.)
  - **Caveats footnoted (survive on `CostReconciliation.caveats` + per-model `confidence`):** `priority_tier_absent` → "Anthropic Priority-Tier spend isn't in this report — the bill may be higher"; `per_model_derived_best_effort` → "OpenAI per-model figures are best-effort (derived from line items)" + mark each such row; `report = Unavailable(reason)` → surface the local estimate day-by-day beside "vendor invoice unavailable: <reason>", never a fabricated delta.
  - **The Codex token-undercount caveat (GATE-2b asymmetric residual).** T9c reconciles the **cost** report, whose **dollar day-totals are complete** for OpenAI (`/costs` bills all traffic, incl. the Responses API Codex rides) — so the dollar reconciliation needs no Responses caveat. **But** if T10's display ever shows **token** figures (e.g. a per-model token breakdown), it **must** surface `responses_api_coverage_unconfirmed` ("OpenAI token counts may undercount Codex/Responses-API traffic") rather than present token sums as authoritative. *Recommendation: T10c shows the dollar (cost) reconciliation only; defer any token-side view (and its caveat) to a later card.*
  - **Always labeled an estimate** (`x_Estimated`); the local side is never presented as the bill. Cost figures hedged per DESIGN-SYSTEM voice.
- **Accessibility:** full `--plain` rendering; never color alone for over/under or for absence/caveat states.

---

## 6. Decision 5 — ⛔ GATE 2b resolution (populated-row live-confirm)

⛔ **GATE 2b** (§11.5 ✅ T9b) requires that **before a real admin key first reaches the adapters in T10**, the T9b standing follow-ups are EITHER (a) live-confirmed against real usage, OR (b) each formally deferred to a named card with a locked completion criterion. T9b is confirmed only at the **envelope/params/pagination/bucket+time** level — the per-`result`-row money-bearing parse, the OpenAI Responses-API coverage, a real 403 body+status, the `line_item` format, `currency`, and history depth are **documented-schema-derived, not live-verified** (Eren's org had no raw-API usage across a 30-day window).

**Pin — GATE 2b is a prerequisite of T10a** (the first sub-unit that sends a real key — its connect-time validation + the first `reconcile` fetch). The connect-verification + first `reconcile` run **is** the live-confirm vehicle: the human runs it with their own admin key during T10a's ⛔ gate. For each follow-up, the resolution is:

| T9b follow-up | T10a resolution | If unconfirmable (no usage) → deferred-card criterion |
|---|---|---|
| Populated per-`result`-row shapes (Anthropic `amount`/`description`/`model`/`cost_type`/`service_tier`; OpenAI `amount.value`/`line_item`; token fields) | The first `reconcile` against real usage confirms each parsed field; pin a verbatim real body as a regression fixture (as T9b did for the empty envelopes) | **T10-LIVE-ROWS**: "run when the org has raw-API usage; confirm each field parses; pin a real body fixture" |
| OpenAI Responses-API coverage (Codex) | Fire a known Responses-API/Codex call, then watch `usage/completions`; if covered, flip `responses_api_coverage_unconfirmed → false` | **T10-LIVE-ROWS**: "fire a Responses-API call; confirm coverage; flip the caveat or keep it true + keep the §5 token-undercount copy" |
| A real 403 body + its **status** (401-vs-403, which the classifier branches on) | Capture from an ineligible-account / wrong-scope key if available | **T10-LIVE-ROWS**: "capture a real 401 and 403 body+status; confirm the classifier branches" |
| `line_item` format · `currency` · history depth | Confirm from the real `reconcile` body | **T10-LIVE-ROWS**: same card |
| **(from §2.2)** OpenAI `/costs` **probe behavior** | The connect-time `/costs` probe (1-day completed window, `limit=1`) IS the live check — confirm `200`/`401`/`403` + any post-auth `400` on the window. The costs-vs-usage scope question is **moot** (we probe `/costs` directly). | **T10-LIVE-ROWS**: "confirm the `/costs` probe's `200`/`401`/`403` + any window `400`, with a real key" |

**The card must show GATE 2b cleared OR each item deferred to the named card with a locked criterion.** The asymmetric residual stands: even unconfirmed, OpenAI `/costs` bills all traffic so **dollar totals are complete** — the open risk is a future **token-side** undercount, which §5's copy must surface. (The human-run live-confirm is the one allowed use of a real admin key + real data — not an automated test.)

---

## 7. Decision 6 — the connect-ACTION offline-acceptance test (replacing the `scripts/offline_acceptance.sh` `T9/T10` STUB)

The STUB must be replaced by a proof of three properties: the connect action reaches **ONLY the authorized host** · the secret lands **ONLY in the keychain** (never disk/config/logs) · **disconnect leaves no residue** — all with **zero real network**. Because the production `AuthorizedClient` is HTTPS-only and bound to `api.anthropic.com`/`api.openai.com` (it **cannot** be pointed at a loopback in a real build — by design, and we must **not** add a host-override knob that would weaken the authorized-host guarantee), the proof is **two complementary layers** (mirroring the existing static-`offline.rs` + dynamic-script split):

- **Layer 1 — the positive, as a `#[cfg(feature = "connect")]` integration test** (`cargo test`, runs under the offline/strace CI job like all tests). It drives the connect/disconnect/reconcile **command core** against the `cfg(test)` loopback `MockServer` (the T9b `test_support` pattern) + the keyring **mock** backend, asserting: (a) the only network egress is to the **loopback authorized host** (and an off-host attempt is refused by the type before I/O — already T9a-tested); (b) `connect` writes the secret **only** to the mock keychain — a fixture `$HOME` fingerprint is **unchanged** across `connect` (extends T8's `credential_round_trip_writes_nothing_to_disk` to the full command path); (c) `disconnect` removes the key + registry entry, leaving no residue. **This requires a testable seam:** the connect/disconnect/reconcile command logic must be a function taking an **injected** adapter/`AuthorizedClient` + `CredentialStore` + `ConnectionRegistry` (the adapters already expose `with_client` + `fetch_*(&SecretString, …)` seams; pin that the CLI command core is likewise injectable).
- **Layer 2 — the dynamic strace script** replaces the STUB with a fail-closed check: run `costroid connect anthropic` (the `--features connect` build) under network isolation, with a **prefix-valid-but-fake** key piped on stdin (e.g. `sk-ant-admin-FAKE`, so it passes the prefix check and *attempts* the validation call) and a fixture `$HOME`. Assert: (i) **no non-loopback `AF_INET` connect** is attempted that escapes isolation (the real-host attempt fails closed under `unshare`/strace — proving the connect path leaks nothing even when the host is unreachable); (ii) the fixture `$HOME` fingerprint is **unchanged** (no secret/file residue on the failure path); (iii) `disconnect anthropic` likewise leaves no residue. The "reaches ONLY the authorized host" **positive** is Layer 1's job (the script's binary can't reach a loopback); the script proves "**leaks nothing, contacts no rogue host**, even on the connect path."
- **CI strace gate:** treats both layers like the rest of the suite — `assert_no_inet` already allows `127.0.0.1`/`::1` (so Layer 1's loopback passes) and fails on any other `AF_INET` connect. The default-tier (connect-OFF) proof is unchanged and stays green.

*(The build agent owns the exact mechanism; the three properties + the two-layer division + the injectable-command-core seam are pinned.)*

---

## 8. Decision 7 — OAuth (tier 2): **DEFER**

T10 is **own-key only** (tier 3). Sanctioned OAuth (tier 2 — GitHub, system browser + loopback redirect + PKCE) is deferred with the Copilot adapter (PRODUCT-PLAN §8) and is a later tier. **No OAuth piece is in T10 scope.** (Note: the Anthropic docs offer an `Authorization: Bearer <org:admin OAuth token>` path on `me`/`rate_limits` — explicitly **not** adopted; T10 uses the static `x-api-key` path only.) **Recommendation: confirm deferred.** The keychain account namespace `oauth:<vendor>` is already reserved (T8) for whenever it lands.

---

## 9. Decision 8 — API-lane rate-limit denominators: **DEFER from T10's first cut**

These are the *configured* per-model API rate limits (RPM / input-TPM / output-TPM), the "denominators" an API-lane limit meter would render against — **not spend**. Live-confirmed findings:

- **Anthropic `GET /v1/organizations/rate_limits`** (+ a per-workspace variant) — clean, live-confirmed, HTTPS-GET, **zero billing data**, on the **same `sk-ant-admin` key** (no new secret/scope). Returns `data[]` of `{group_type, models, limits:[{type,value}]}`. Read-only ("Can I update rate limits with this API? No."). The genuine API-lane denominator source.
- **OpenAI** — **no org-level rate-limits endpoint exists** (confirmed `not_found` across the Administration reference). The only static-key rate-limits read is **project-scoped** (`GET /v1/organization/projects/{project_id}/rate_limits`), requiring an N+1 list-projects-then-fan-out — an awkward, asymmetric fit that doesn't match a clean org denominator.

**Recommendation: DEFER.** T10's job is connect + cost reconciliation, not an API-lane *quota* view (that's the Providers/Models analytical tabs, Step 5). Shipping a lopsided, Anthropic-only denominator add-on now would front-run Step 5. **If/when an API-lane limit meter is built**, pin **only** Anthropic `GET /v1/organizations/rate_limits` (`x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01`), defer the per-workspace variant as a refinement, and render OpenAI's API-lane denominators **"unavailable — no sanctioned org-level source"** (mirroring Gemini on the cost lane). Recorded here so the next agent need not re-research.

---

## 10. Decision 9 — the split (T10 is XL)

Per §12.0 Rule 3 (gating prerequisite → parallel sub-units, the T9a/b/c precedent) and the §10 table's "4b keychain+http → 4c keys+CLI" shape:

- **T10a — `connect`/`disconnect`/`connections` CLI + key validation + the connect-action offline test.** The gating sub-unit: the first caller, the secret/network boundary, and **all three** ⛔ gates (secret-handling approval; **GATE 2b** live-confirm; the legal review touches the flow this unit builds). Deliverables: the four-ish subcommands (`connect`/`disconnect`/`connections`), the §2 validation calls (Anthropic `me`; the chosen OpenAI option), the injectable command core, the Layer-1 integration test + the Layer-2 strace script (§7), the GATE-2b live-confirm (§6). Prereq: T9 ✅ + GATE 2b resolution path agreed.
- **T10c — the reconciliation display.** `costroid reconcile` surfacing `CostReconciliation` honestly (§5), vendor-scoped local estimate, caveats footnoted, `--plain`. Prereq: **T10a** (needs stored keys + the fetch path). Pure read/render surface — no new secret/network boundary beyond T10a's.
- **T10b — release v0.4.0** — **already carded at §12.10** (the lockstep five-crate bump + the `costroid-connect` publish-ladder slot). Prereq: T9 + ⛔ GATE 2b + T10(a+c) done + ⛔ legal review cleared.

Order: **T10a → T10c → T10b.** (Draft cards for T10a and T10c are Appendix A; T10b stands as carded.)

---

## 11. Gates — ⛔ STOP for the human on BOTH (do not bypass)

- ⛔ **GATE 2b — populated-row live-confirm** (§6, §11.5 ✅ T9b). Must be cleared (live run with the human's own key) **or** each item formally deferred to a named card (**T10-LIVE-ROWS**) with a locked criterion, **before a real admin key first reaches the adapters in T10a**. The §2.2 OpenAI-scope question folds into this run.
- ⛔ **Legal review of the connection flows** (PRODUCT-PLAN §3 Step 4 / Rule 2 / §12.10). Own-key + (deferred) sanctioned-OAuth only; confirm the flows hit only user-authorized provider endpoints, store nothing outside the keychain, and induce no ToS violation. A human gate — it reviews the flow **T10a builds**, and per §12.10 it must clear **before 0.4.0 (T10b) ships**.

Plus the standing ⛔ **CLI-surface** (§1.1) and ⛔ **secret-handling** (the connect flow reads/stores keys) approvals, both on T10a.

---

## 12. Cross-cutting notes for the T10 cards (carry into the §12 bodies)

- **Reuse, don't reinvent.** T10 adds **no** new endpoint parsing beyond the §2 validation calls — it composes the built `AuthorizedClient`/`AnthropicAdapter`/`OpenAiAdapter`/`CredentialStore`/`ConnectionRegistry`/`reconcile_cost`. The validation calls are thin additions (`me` for Anthropic; the signed-off `GET /v1/organization/costs` 1-day completed-window probe, `limit=1`, for OpenAI — §2.2) reusing the existing `fetch_page`/classification path.
- **Secret discipline (unchanged hard invariants).** Key from **stdin only** — never argv/env/disk/config/logs; lives only in the keychain via `CredentialStore`; rides the wire only as an `AuthHeader` (redacting `Debug`); never echoed (only the prefix is inspected). The Connections registry + any org-label add stay **non-secret**.
- **Network only on the explicit action.** `connect`, `reconcile`, and `connections --check` are the only network actions; `disconnect`, plain `connections`, and every default-build command make **zero** network calls — the strace default tier stays green, and the feature-on baseline keeps proving "linked ≠ called" for everything except the new explicit actions (now covered by §7).
- **Honesty over completeness.** Typed vendor-absence is rendered as text, never `$0`; caveats (`priority_tier_absent`, `per_model_derived_best_effort`, `responses_api_coverage_unconfirmed` if any token view) are surfaced, never dropped; the local figure is always labeled an estimate, never the bill. Gemini stays first-class "unavailable — no sanctioned static-key usage API" everywhere (connect/connections/reconcile).
- **Accessibility on every new visual.** `connections` and `reconcile` need full `--plain` renderings and a **non-color cue** for status / over-under / absence; ASCII-fold the em-dash (and the Gemini string) at the render boundary as `cursor_detected_message` does.
- **No `unwrap`/`expect`/`panic!` in library code; `thiserror` in libs, `anyhow` in `apps/`; any new dep permissive; `rustls` not OpenSSL.**

---

## 13. After sign-off

Per §12.8 / §11.2: once the ⛔ sign-off arrives (the CLI shape, the OpenAI validation choice, the GATE-2b handling, and the legal-review timing), a planning agent writes the full T10a / T10c card bodies into PRODUCT-PLAN §12, logs the pinned decisions in §11.5, and creates the **T10-LIVE-ROWS** deferred card. **Do not implement from this proposal directly** — each sub-unit builds in a fresh agent per §12.0 once its card exists. If sign-off is refused or amended, this file is updated rather than silently diverging.

> **✅ Done (2026-06-13):** sign-off arrived (with the OpenAI `/costs` amendment, §2.2) and the carding pass ran — §11.5 logs the pins, **T10a is carded at §12.14**, **T10c at §12.15**, and the **T10-LIVE-ROWS** deferred card at **§12.16**. This file's §2.2 (the `/costs` probe) and the §6 GATE-2b table are the binding pins for T10a/T10c; the §9 rate-limit-denominator + §8 OAuth defers stand.

---

## 14. Sources (fetched + verified 2026-06-13; expect link rot — the pins are the API paths, not these URLs)

**Anthropic** — `platform.claude.com/docs/en/api/admin-api/organization/get-me` (the `me` reference: `{id,name,type}`) · `platform.claude.com/docs/en/manage-claude/admin-api` (the `x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01` example for `me`; "The Admin API is unavailable for individual accounts"; the org:admin OAuth alternative, excluded) · `platform.claude.com/docs/en/manage-claude/rate-limits-api` + `platform.claude.com/docs/en/api/admin/rate_limits/list` + `…/workspaces/rate_limits/list` (the rate-limit denominator endpoints, deferred §9). `docs.anthropic.com` 301-redirects to `platform.claude.com` (canonical).

**OpenAI** — `developers.openai.com/api/reference/.../organization/subresources/admin_api_keys/methods/list` (Option C; admin-class, `api.management.read`, no billing) · `developers.openai.com/api/reference/.../projects/methods/list` (fallback; the verbatim 403 `Missing scopes: api.management.read`) · `developers.openai.com/api/reference/.../usage/methods/costs` + `.../usage/methods/completions` (the T9 endpoints; the signed-off connect probe is `costs` — §2.2) · `developers.openai.com/api/reference/administration/overview` (no org-level rate-limits endpoint) · `developers.openai.com/.../projects/subresources/rate_limits/methods/list_rate_limits` (project-scoped only, deferred §9) · community `community.openai.com/t/...1345041` & `...1368874` (restricted-admin-key scope failures) & `...957632` (the management-scope 403 body) & the "Missing scopes: api.read" billing-usage-export report (the scope-gap evidence, §2.2) · `help.openai.com/en/articles/8867743-assign-api-key-permissions` and `platform.openai.com/docs/guides/rbac` (the scope-mapping pages — **403 to non-browser fetch**, so the exact cost/usage scope is unresolved from docs: the §6 live check resolves it). `platform.openai.com` reference 403s non-browser fetches (the developers.openai.com mirror is the fetchable canonical).

---

## Appendix A — draft §12.x card bodies (for review; NOT yet inserted into PRODUCT-PLAN §12)

> These mirror the T1–T7 / T9b card style. **✅ Carded 2026-06-13** — the as-inserted versions live at PRODUCT-PLAN §12.14 (T10a) and §12.15 (T10c), refined per the sign-off (the OpenAI `/costs` amendment is reflected in T10a's "Pinned" + "GATE 2b" blocks). The drafts below are retained as the durable as-proposed record; §12 is canon.

### Draft — T10a · `connect`/`disconnect`/`connections` CLI + key validation + connect-action test · XL · ⛔📌 · Prereq: T9 ✅ + ⛔ GATE 2b resolution agreed

```
**Goal:** wire the FIRST caller of costroid-connect — the connect/disconnect/connections CLI — so a
  user pastes their own admin key (Anthropic sk-ant-admin / OpenAI sk-admin-), it is validated WITHOUT
  reading billing data, stored ONLY in the OS keychain, and listed; disconnect revokes instantly. This
  is the FIRST real network in the product (opt-in, behind --features connect + an explicit action).
**Spec:** docs/proposals/T10-PIN-PROPOSAL.md §1.1 (CLI), §2 (validation), §4 (connections), §6 (GATE 2b),
  §7 (the connect-action test); §11.5 ✅ T8/T9a/T9b (CredentialStore/ConnectionRegistry/AuthorizedClient/
  adapters); CLAUDE.md golden rules + decide-vs-ask.
**Files:** apps/cli/src/main.rs (clap: add Connect/Disconnect/Connections, all #[cfg(feature="connect")]);
  apps/cli/src/ (a new connect command module — the injectable command core); apps/cli/Cargo.toml (the
  `connect` feature already gates costroid-connect); crates/costroid-connect/src/anthropic.rs +
  src/openai.rs (add the non-billing validate() calls — Anthropic GET /v1/organizations/me; OpenAI per the
  signed-off option); crates/costroid-connect/src/lib.rs (optional non-secret org-label on RegistryFile);
  apps/cli/tests/ (the Layer-1 connect-action integration test); scripts/offline_acceptance.sh (replace
  the T9/T10 STUB with the Layer-2 fail-closed check); README.md + CHANGELOG.md.
**Pinned (proposal §1.1/§2):** vendor = anthropic|openai|gemini; gemini connect prints the pinned
  unavailable string + exits 0 (no key). Key entry = STDIN ONLY (no-echo on TTY; one line on a pipe) —
  NEVER argv/env. Wrong-class prefix check before any network (sk-ant-admin / sk-admin-). Anthropic
  validation = GET /v1/organizations/me (x-api-key + anthropic-version: 2023-06-01; {id,name,type}; zero
  billing; any non-200 = invalid/ineligible). OpenAI validation = [SIGNED-OFF OPTION A/B/C from §2.2 —
  resolve before building]. On success: CredentialStore::store + ConnectionRegistry::mark_connected +
  print org label; on failure: typed reason + remediation, do NOT store. disconnect = delete + mark_
  disconnected (idempotent). connections = local-only list (+ --check live re-validate); gemini always
  "unavailable"; status by a non-color text cue; --plain + em-dash ASCII-fold.
**Scope fence:** connect/disconnect/connections + the two validation calls + the connect-action test ONLY.
  NO reconciliation display (T10c — costroid reconcile). NO rate-limit denominators (deferred §9). NO
  OAuth (deferred §8). NO TUI tab (Step 5). NO new endpoint beyond the §2 validation calls. The default
  build's resolved graph + the strace default tier stay UNCHANGED.
**Connect-action test (proposal §7):** Layer 1 — a #[cfg(feature="connect")] integration test drives the
  injectable command core against the cfg(test) loopback MockServer + keyring mock: only-loopback egress;
  secret only in the mock keychain (fixture $HOME fingerprint unchanged across connect); disconnect leaves
  no residue. Layer 2 — the script runs `costroid connect anthropic` (connect build) under strace/unshare
  with a prefix-valid-but-fake key on stdin + a fixture $HOME, asserting no non-loopback AF_INET connect
  escapes + no $HOME residue (fail-closed), and disconnect likewise.
**⛔ Human gates (THREE):**
  (1) CLI surface — the subcommand shapes + stdin key entry + copy (CLAUDE.md ask-first).
  (2) Secret-handling — connect reads/stores keys (CLAUDE.md ask-first); approve the validate() API + the
      key flow (stdin → prefix-check → validate → keychain; never argv/env/disk/log; AuthHeader-only wire).
  (3) GATE 2b — the live-confirm with the human's OWN key runs here (proposal §6): the connect-validation
      + first real fetch confirm the populated rows / the OpenAI scope / a real 401-403; any item not
      confirmable (no usage) is deferred to the named T10-LIVE-ROWS card with a locked criterion. The card
      records GATE 2b CLEARED or each item deferred. (Plus the ⛔ legal review of the flow — gates T10b ship.)
**Done when:** four-command gate green; --features connect builds; connect/disconnect/connections work
  against the keyring mock + loopback (zero real network in tests); Layer-1 test + Layer-2 script prove
  the three properties; offline.rs both tiers + cargo-deny both passes + the default strace tier stay
  green; GATE 2b cleared or formally deferred; README/CHANGELOG updated; §11.4 ticked; §11.5 entry written.
**Next:** T10c surfaces reconciliation (costroid reconcile) on the stored keys + the fetch path; T10b
  cuts v0.4.0 once T10c + the ⛔ legal review land.
```

### Draft — T10c · reconciliation display (`costroid reconcile`) · L · 📌 · Prereq: T10a ✅

```
**Goal:** surface T9c's estimate-vs-invoice reconciliation honestly — a `costroid reconcile` subcommand
  that fetches a connected vendor's cost report, compares it to the local API-lane estimate per UTC day +
  model, and renders signed variance with every typed caveat/absence intact.
**Spec:** docs/proposals/T10-PIN-PROPOSAL.md §5; DATA-MODEL "Reconciliation engine"; §11.5 ✅ T9c
  (reconcile_cost / LocalCostEstimate / CostReconciliation / VendorBilled / BilledAbsence); DESIGN-SYSTEM
  (reconciliation rendering, --plain, non-color cue).
**Files:** apps/cli/src/main.rs (clap: add Reconcile, #[cfg(feature="connect")]); apps/cli/src/render.rs
  (the reconciliation renderer + --plain/Ascii variants + snapshots); apps/cli/src/ (the reconcile command
  wiring: fetch via adapter → vendor-scope rows → reconcile_cost → render); docs/DESIGN-SYSTEM.md (the
  as-built reconciliation component) + CHANGELOG.md.
**Pinned (proposal §5):** `costroid reconcile [--vendor anthropic|openai] [--period day|week|month|year]`;
  no --vendor = every connected vendor (each section; gemini = "unavailable"). Vendor-scope the local
  estimate: x_Tool claude-code → Anthropic, codex → OpenAI, cursor → excluded (the T9c "one vendor before
  building" rule). Render: signed variance (+over/-under, pct rounded at the render boundary); typed
  vendor-absence as text, NEVER $0 (DayNotCovered / ModelNotInReport / ReportUnavailable→reason incl.
  NotConnected + Gemini); caveats footnoted (priority_tier_absent; per_model_derived_best_effort + mark the
  rows); local figure always labeled an estimate, never the bill. Dollar (cost) reconciliation only — if any
  TOKEN view is shown, carry responses_api_coverage_unconfirmed (recommend: defer token-side to a later card).
**Scope fence:** the reconcile subcommand + its renderer ONLY. NO new endpoint/parse (reuse the adapters +
  reconcile_cost). NO connect/disconnect changes (T10a). NO TUI History/Models tab (Step 5). NO token-side
  reconciliation. NO rate-limit denominators.
**Accessibility:** full --plain rendering; non-color cue for over/under + absence + caveat states; em-dash
  ASCII-fold at the render boundary; insta snapshots incl. --plain.
**⛔ Human gate:** none new beyond T10a's (no new secret/network boundary — it reuses the connected key +
  the authorized client). (The ⛔ legal review + release are T10b.)
**Done when:** four-command gate green; `costroid reconcile` renders a fixture reconciliation in all render
  modes (driven by fixtures, zero real network — the adapter path is loopback-tested as in T9b); typed
  absence never shows $0; caveats survive on screen; --plain snapshot pinned; default strace tier green;
  DESIGN-SYSTEM + CHANGELOG updated; §11.4 ticked; §11.5 entry written.
**Next:** T10b cuts v0.4.0 (after the ⛔ legal review) — the connections line ships.
```
