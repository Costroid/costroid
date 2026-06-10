# T9 pin proposal — usage-API endpoint + auth pins (Anthropic · OpenAI · Gemini)

> **STATUS: PROPOSED — awaiting ⛔ human sign-off. Researched and adversarially verified 2026-06-10. NOT canon: PRODUCT-PLAN §12 carding and the §8 Antigravity classification change happen only after sign-off.**

This is the concrete pin proposal that PRODUCT-PLAN §12.8 (the pin-then-card prompt) requires before T9 can be carded: *which provider usage endpoints + auth schemes (Anthropic/OpenAI/Gemini, tier-3 own-key); pin each endpoint+auth as a concrete proposal and ⛔ stop for human sign-off — never guess an endpoint.* It was produced by a 7-agent workflow (3 researchers → 3 adversarial doc-verifiers → a completeness critic), with **every endpoint claim checked against live official docs as of 2026-06-10**. This file is the durable record of that research (the raw research output lived in a temp dir that does not survive reboot).

**T9 scope constraint this proposal is pinned against** (PRODUCT-PLAN §5 tier 3): the user's **own key** — a single static, pasteable secret string per vendor, stored only in the OS keychain, sent strictly device↔provider over `ureq`+`rustls`. No OAuth flows, no browser, no session reuse, no undocumented endpoint, ever (tier 4 is the ToS line).

---

## 1. The pins at a glance

| Vendor | Verdict | Billed-$ endpoint | Tokens-by-model endpoint | Auth |
|---|---|---|---|---|
| **Anthropic** | **pin** | `GET api.anthropic.com/v1/organizations/cost_report` | `GET …/v1/organizations/usage_report/messages` | `x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01` |
| **OpenAI** | **pin** | `GET api.openai.com/v1/organization/costs` | `GET …/v1/organization/usage/completions` (+ siblings) | `Authorization: Bearer sk-admin-…` |
| **Gemini** | **defer** | none exists for a static key | none exists for a static key | n/a — first-class "unavailable" |

T9 ships the Anthropic + OpenAI adapters; `ApiVendor::Gemini` (already in `costroid-connect`) stays and renders **"unavailable — no sanctioned static-key usage API"**. The T9a/T9b/T9c split from the §11.4 backlog entry stands, with one amendment carried by this proposal — T9b is **two** adapters plus the Gemini first-class-unavailable state, not the "3 per-provider usage-API adapters" the backlog line anticipated: **T9a** HTTP infra (`ureq`+`rustls`, generic authorized-host client, ⛔ guarantee-redefinition, no provider knowledge) · **T9b** the Anthropic + OpenAI adapters reading keys via `CredentialStore::retrieve(ApiVendor)`, each a §8-style live-shape confirm, plus the Gemini unavailable render · **T9c** the reconciliation engine (pure `costroid-core`, fixture-tested, no network).

---

## 2. Anthropic — Admin API Usage & Cost reports

### 2.1 Endpoints

**`GET https://api.anthropic.com/v1/organizations/cost_report`** — actual billed cost in USD per day; the reconciliation source of truth for the API bill.

- Params: `starting_at` (required, RFC 3339; buckets snapped to UTC day start, inclusive), `ending_at` (exclusive), `bucket_width` (**`1d` only** — daily is the sole granularity), `group_by[]=workspace_id` and/or `description` (**grouping by `description` is what unlocks the parsed `model`/`token_type`/`service_tier`/`context_window`/`inference_geo` fields** — always pass it), `limit`, `page`.
- Response: `data[]` of time buckets `{starting_at, ending_at, results[]}`; each result carries `amount`, `currency` (always `"USD"`), `description`, `cost_type` (`tokens|web_search|code_execution|session_usage`), `model`, `token_type` (5 values incl. the two `cache_creation.ephemeral_*` variants), `service_tier`, `context_window`, `inference_geo`, `workspace_id` (null = default workspace).
- **`amount` is a DECIMAL STRING in CENTS, fractional cents possible** (the doc's own response example is `"amount": "123.78912"` = $1.2378912). Parse as arbitrary-precision decimal and divide by 100; never integer-cents, never float-and-round.
- Pagination: `has_more` + `next_page` token passed back as `?page=…`; the token can look timestamp-like in doc samples — treat as opaque, pass back verbatim.
- Data appears ~5 minutes after request completion; no documented hard history-depth cap (paginate backward; probe retention empirically in T9).

**`GET https://api.anthropic.com/v1/organizations/usage_report/messages`** — token counts by model per time bucket; reconciliation priority #2 (and the only place Priority Tier usage shows up).

- Params: `starting_at` (required), `ending_at` (exclusive), `bucket_width` (`1m|1h|1d`), `group_by[]` any of nine values (`api_key_id|workspace_id|model|service_tier|context_window|inference_geo|speed|account_id|service_account_id` — use **`group_by[]=model`** for Costroid), filters (`models[]`, `api_key_ids[]`, `workspace_ids[]`, `account_ids[]`, `service_account_ids[]`, `service_tiers[]`, `context_window[]`, `inference_geos[]`, `speeds[]` — the last needs the `fast-mode-2026-02-01` beta header), `limit`, `page`.
- Response per result: `uncached_input_tokens`, `cache_read_input_tokens`, `cache_creation{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`, `output_tokens`, `server_tool_use{web_search_requests}`, plus grouped dimensions (each null unless grouped by it). **No cost field** — tokens only.
- Buckets-per-page caps: `1d` default 7 / max 31 · `1h` 24/168 · `1m` 60/1440; paginate for longer ranges.

### 2.2 Auth

- Scheme: **`x-api-key: <admin key>` header plus `anthropic-version: 2023-06-01`** (NOT `Authorization: Bearer`).
- Key class: **Admin API key, prefix `sk-ant-admin…`** — a distinct class; standard keys (`sk-ant-api03…`) are explicitly rejected on these endpoints ("require an Admin API key … that differs from standard API keys").
- Minted: Claude Console → Settings → Admin keys (`platform.claude.com/settings/admin-keys`), by organization members with the **admin role** only.
- A static pasteable secret string → fits T9's tier-3 constraint.

### 2.3 Availability gates — first-class "unavailable" states, not errors

- **Individual (non-organization) accounts are locked out of the Admin API entirely** ("The Admin API is unavailable for individual accounts" — verbatim on three doc pages). A solo dev — Costroid's primary persona — must convert to/create an organization (Console → Settings → Organization) first. Connect UX must model **"unavailable (org required)"** with that remediation copy, never an error loop. *(Whether the org creator automatically gets the admin role is implied but undocumented — one-time live check before writing the connect copy.)*
- **Claude Platform on AWS orgs get none of these endpoints** (usage/cost/rate-limit reports explicitly listed as unavailable) → degrade to unavailable with console remediation.
- **Blast radius:** the Admin key is an org-wide credential (manages members, invites, workspaces; can update/deactivate existing API keys — though it cannot *create* keys or remove admin members). **No scoped read-only variant exists for this platform Admin API** → connect flow must warn at paste time and recommend a dedicated, named, instantly-revocable key.
- **Priority Tier dollars are absent from `cost_report`** (different billing model — cost totals understate the bill for priority users; footnote it); track Priority Tier via `usage_report` with `service_tier=priority`. Conversely, code-execution costs appear *only* in `cost_report`.

### 2.4 Adversarial-verification notes (what the verifiers caught)

- All load-bearing claims re-verified verbatim against live docs 2026-06-10 (cents-as-decimal-string, the enums, the bucket caps, the auth headers, the individual-account exclusion). Overall verdict: **sound**.
- **Do not validate parsers against Anthropic's doc examples — they are internally inconsistent**: the prose says `"123.45"` represents `$1.23` (it's actually $1.2345), and a sample pairs description "Claude Sonnet 4 Usage" with model `claude-opus-4-6`.
- `cost_report`'s `service_tier` enum is only `standard|batch` — narrower than `usage_report`'s six values. Use separate types, don't share an enum.
- `cost_report`'s `inference_geo` doc text is self-contradictory ("null if not grouping by inference geo" on an endpoint whose `group_by` has no such option) — parse as best-effort nullable.
- Docs are mid-host-migration (`docs.anthropic.com` → 301 → `platform.claude.com`). **Pin implementation and tests to the `api.anthropic.com` endpoint paths + the `anthropic-version: 2023-06-01` header, not to doc URLs** — expect the cited links to rot. The API host and paths themselves are unchanged, no rename or deprecation as of 2026-06-10.

### 2.5 Adjacent documented endpoints (audit catches — same key, not the core pin)

- **`GET /v1/organizations/rate_limits`** (+ per-workspace variant) — the org's **configured** API-lane limits per model group (`requests_per_minute`, `input_tokens_per_minute`, `output_tokens_per_minute`, …). Docs position it as the companion to the Usage & Cost API. For a limits tool this is the documented source of **API-lane denominators**, free with the same pasted key — worth a line in the T9b card.
- **`GET /v1/organizations/me`** — the sanctioned way to validate a pasted admin key + fetch org name/id at connect time without touching billing data; ideal for the T10 connect-verification step.
- **Claude Enterprise Analytics API** (`/v1/organizations/analytics/*`, behind a **scoped `read:analytics` static key** minted by an Enterprise org's Primary Owner) — exists and is officially documented, which **falsifies any ecosystem-wide "no scoped key variant exists" claim**: the accurate connect-copy statement is "no scoped variant exists for the *platform Admin Usage & Cost API*; a scoped Enterprise Analytics key exists on the claude.ai Enterprise side." Enterprise-only → a capability-map/copy item, **not** a T9 path. (If ever added, it would be a *second* Anthropic secret, straining the one-string-per-vendor schema — see §6.)
- Alternatives considered and not recommended: the **Claude Code Analytics API** (`/v1/organizations/usage_report/claude_code` — *estimated* cost, Claude Code scope only, same admin gate; possible later cross-check for that lane), Console dashboards/CSV (not an API), and per-response `usage` fields with a standard key (that *is* Costroid's local-estimate side, not the vendor-bill side).

---

## 3. OpenAI — Org Admin Usage + Costs API

### 3.1 Endpoints

**`GET https://api.openai.com/v1/organization/costs`** — actual billed spend in USD per day for the whole organization; the primary reconciliation source.

- Params: `start_time` (required, Unix seconds, inclusive), `end_time` (exclusive), `bucket_width` (**`1d` only**), `limit` (1–180 buckets, default 7), `group_by` of `project_id | line_item | api_key_id` in any combination, `project_ids`/`api_key_ids` filters, `page` cursor.
- Response: `object="page"` → `data[]` buckets `{start_time, end_time, results[]}`; each result `object="organization.costs.result"` with `amount: {value: number, currency: "usd"}` (**float dollars**), plus `line_item`/`project_id`/`api_key_id` when grouped (`quantity` specifically when `group_by=line_item`).
- **`group_by` has NO `model` option.** Per-model dollars are only derivable from `line_item` strings (e.g. `"ft-gpt-4o-2024-08-06, input"` in the cookbook), whose format is **undocumented and uncontracted** → label any per-model $ as **derived/best-effort**, or compute per-model estimates from documented token counts × prices. The vendor-billed-USD-by-model promise cannot be met from documented fields alone for OpenAI.
- Pagination: `page` = previous `next_page`; stop on `has_more=false`. History depth, data latency, and UTC-day alignment are all undocumented — verify empirically before promising invoice-period alignment.

**`GET https://api.openai.com/v1/organization/usage/completions`** — token counts by model per bucket.

- Params: `start_time` (required), `end_time`, `bucket_width` `1m|1h|1d` (default `1d`), `group_by` of `project_id|user_id|api_key_id|model|batch|service_tier` (use **`group_by=model`**), filters `models[]`/`project_ids[]`/`api_key_ids[]`/`user_ids[]`/`batch`, per-page bucket caps `1d` 7/31 · `1h` 24/168 · `1m` 60/1440, `page` cursor.
- Response per result: `input_tokens` (includes cached), `output_tokens`, `input_cached_tokens`, `input_audio_tokens`, `output_audio_tokens`, `num_model_requests`, plus conditional grouped fields. No cost field.

**Sibling usage endpoints** (same envelope/auth; `group_by` options narrow per endpoint — e.g. `vector_stores` supports only `project_id`): `embeddings`, `images`, `audio_speeches`, `audio_transcriptions`, `moderations`, `vector_stores`, `code_interpreter_sessions`, `file_search_calls`, `web_search_calls`. The official reference enumerates **exactly 11 organization usage/costs endpoints** — verified live 2026-06-10, no more, no fewer. For T9, `completions` covers Codex/GPT token traffic and `/organization/costs` covers dollars for everything regardless.

- Note: reference tables document paths without `/v1`, but every official curl example uses `https://api.openai.com/v1/…` — **use the `/v1` form**.

### 3.2 Auth

- Scheme: **`Authorization: Bearer <key>`**, plain HTTPS header, no version header.
- Key class: **Admin API key, prefix `sk-admin-…`** (literally `"value": "sk-admin-1234abcd"` in the create-key reference). Regular project keys (`sk-proj-…`) / standard secret keys cannot call these endpoints. Conversely, **"Admin API keys cannot be used for non-administration endpoints"** (verbatim) — the pasted credential cannot be abused for inference; good isolation.
- Minted: `platform.openai.com/settings/organization/admin-keys` (Dashboard → Settings → Organization → Admin keys). A `POST /v1/organization/admin_api_keys` exists but itself requires an admin key — the console is the bootstrap path.
- **Organization Owners only** (Help Center). A solo platform account is the Owner of its own default org → can mint (a safe inference from standard OpenAI account behavior, not doc-quotable verbatim). A user who is merely Member/Reader of a company org **cannot** mint → first-class **"unavailable"**, not an error loop.
- A static pasteable secret string → fits T9's tier-3 constraint.

### 3.3 Adversarial-verification notes

- Every parameter, enum, default, and cap re-verified verbatim 2026-06-10; overall verdict: **sound**. Docs live on `developers.openai.com` (the `platform.openai.com` reference is canonical but 403s non-browser fetches — reproduced independently).
- **Legacy `GET /v1/usage` and `GET /v1/dashboard/billing/usage` are confirmed undocumented/internal** — absent from the official 11-endpoint enumeration, and community reports show them now demanding a browser **session key** and rejecting API keys outright. The wrong side of the project's tier-4 line. **Never.**
- **Demonstrated fragility:** `/v1/organization/costs` had a real transient 404 outage (official status incident Sun 2025-11-09 08:15–09:38; community reports from 2025-11-08) — infrastructure incident, not an API change. Build defensive 429/5xx backoff + degrade-to-unavailable; never hard-fail the TUI.
- **Empirical T9b check (joint blind spot the audit flagged):** does `usage/completions` include **Responses-API traffic**? Codex rides the Responses API; there is no `usage/responses` endpoint, and no doc states completions covers it. If it doesn't, token-by-model reconciliation silently undercounts exactly the traffic Costroid cares most about (Codex) while `/costs` still bills it — a divergence that would look like a Costroid bug. Fire a known Responses-API call and watch the usage endpoint before trusting token-side reconciliation.
- Rate limits for the admin/usage endpoints are undocumented; no charge for calling them is documented (management endpoints, not token-billed). API current as of June 2026, no deprecation/rename.

---

## 4. Gemini — defer: first-class "unavailable", not an adapter

**Pin: T9 builds no Gemini adapter.** `ApiVendor::Gemini` stays in `costroid-connect` and renders **"unavailable — no sanctioned static-key usage API"** — the same honest stance as Cursor quota and Antigravity compute-effort quota.

### 4.1 Why (verified June 2026)

- **The Gemini API key authenticates inference only.** The Gemini API reference index (`ai.google.dev/api`) documents no usage, quota-consumption, billing, or spend resource anywhere on `generativelanguage.googleapis.com`. The only programmatic usage data is the per-response `usageMetadata` token counts — per-call only, useless for retroactive reconciliation (and it's the same self-measured side of the ledger Costroid already computes locally).
- **All Gemini usage/cost views are UI-only**: AI Studio Dashboard → Usage, Billing, Spend pages, and the Cloud Billing console Cost management pages. Google's 2026 cost-transparency push shipped as dashboard features with **no accompanying API**.
- **The Cloud Billing REST surface has no spend endpoint** — it contains exactly the Budget, Pricing, Catalog, and Billing Account APIs; nothing returns actual incurred cost (no AWS Cost Explorer / Azure Cost Management equivalent exists as of June 2026).
- **The only machine-readable cost source is the Cloud Billing BigQuery export**, and that chain violates T9's scope on three counts: (1) querying BigQuery requires an **OAuth2 access token** ("BigQuery doesn't support the use of API keys" — literal), minted from a service-account JSON key via a signed **RS256 JWT-bearer exchange** — OAuth-class machinery, out of T9's no-OAuth scope, and it would pull an RSA/JWT-signing dependency into `costroid-connect`; (2) the secret is a **multi-KB JSON key file**, not a pasteable string — and org policy (`iam.disableServiceAccountKeyCreation`, enforced *by default* for orgs created on/after 2024-05-03) can forbid minting it at all; (3) setup burden is severe for a local-first tool (enable export, create project + dataset + service account + IAM grants), and backfill reaches at best the start of the previous month — history before enablement is permanently unobtainable. A reasonable **post-T9 "Gemini (advanced)" connector** at best; not a T9 path.
- Cloud Monitoring time-series was also examined and rejected: no cost data ever, same OAuth-only auth, and the per-model token-count metric names are not formally enumerated on any fetchable docs page — verging on the undocumented-surface line.
- **Do NOT** scrape AI Studio dashboards or replay an AI Studio browser session — undocumented surfaces, tier-4.
- Billing-model context: effective 2026-03-23 new Gemini API users default to a **prepay credits** model managed only in AI Studio (postpay at Tier 3 — and "the option to manually switch to Postpay billing is temporarily disabled" as of June 2026), which deepens the no-API problem.

### 4.2 Unlock to watch

An **AI Studio usage API**. Google's 2026 push added dashboards/spend caps only; if a documented usage/cost API ships for AI Studio keys, re-run discovery and un-defer. (Secondary watch-item: confirmation that prepay-mode usage rows land in the BigQuery export with per-SKU detail — currently a strongly-implied inference, relevant only to the post-T9 advanced connector.)

---

## 5. The pending PRODUCT-PLAN §8 canon correction (Antigravity $ lane) — applies only after sign-off

PRODUCT-PLAN §8 (and the §5 table, and CLAUDE.md's deferred-adapters block) currently asserts the Antigravity **"Gemini-API $ lane is ToS-safe — build when carded: Gemini-API cost via the user's own key (AI Studio cost/usage dashboards + Cloud Billing BigQuery export)."**

This research establishes a correction to that framing:

- **"ToS-safe" survives** — nothing about the lane touches an unsanctioned endpoint.
- **"Implementable under T9's own-key constraint" does not.** The user's own Gemini API key **reads nothing programmatically** (inference only — the dashboards it "unlocks" are browser UI, not API), and the BigQuery billing export is **OAuth-class** (service-account JSON + RS256 JWT-bearer token exchange), violating both the no-OAuth and the single-static-pasteable-string constraints simultaneously.
- Corrected stance to record in §8/§11.5 on sign-off: the Antigravity $ lane is **ToS-safe but unavailable under T9 constraints**; the BigQuery export is a post-T9 "advanced" connector at best — so the docs stop promising a lane T9 cannot build.

**No doc edit happens from this file** — PRODUCT-PLAN §8/§11.5 (and the CLAUDE.md echo) get corrected only as part of the post-sign-off carding pass.

---

## 6. Cross-cutting notes for the T9 card (carry these into the §12 body)

- **Money-encoding trap — unit-tag at every parse boundary.** Four endpoint families feed one reconciliation pipe with four encodings: Anthropic `cost_report` = **decimal-string cents** (fractional); Claude Code Analytics = **integer cents**; Enterprise Analytics = fractional cents with `amount` vs `list_amount` (post- vs pre-discount); OpenAI = **float dollars**. Mixing them silently is a 100× error. And don't validate parsers against Anthropic's doc examples (internally inconsistent — §2.4).
- **Defensive backoff + degrade-to-unavailable.** Numeric rate limits are undocumented at both vendors; Anthropic asks ≤1 req/min sustained (bursts for paginated downloads OK); OpenAI's costs endpoint has had a real ~1-day 404 outage. 429/5xx → backoff → "unavailable", never a hard failure.
- **Send a distinctive `User-Agent: costroid/x.y.z`** — Anthropic explicitly requests it for integrations; zero telemetry implication since it rides the user's own authorized request.
- **Detect wrong-key-class pastes at connect** (`sk-ant-api03…` / `sk-proj-…` where an admin key is needed): diagnose with clear copy, **don't store**. Anthropic alone now has three credential classes (`sk-ant-api03` inference / `sk-ant-admin` org / `read:analytics` Enterprise Analytics).
- **`ApiVendor` semantics = "one usage-read credential per vendor."** That holds for T9 (one admin key each for Anthropic/OpenAI). If Enterprise Analytics support is ever added it would be a second Anthropic secret and strain the schema — a known, accepted limitation for now.
- **Blast-radius copy (both vendors):** the only solo-dev-mintable credentials are org-root admin keys; neither vendor documents IP allowlisting or expiry for them. Recommend a dedicated, named, instantly-revocable key; never echo it; warn multi-member-org users explicitly. A keychain compromise = org compromise.
- **Open empirical checks for T9:** history-depth/retention on all four pinned endpoints (undocumented both vendors); OpenAI UTC-alignment + invoice-exactness of daily buckets; the OpenAI `line_item` string format; the Responses-API coverage check (§3.3); the Anthropic org-creator-gets-admin-role check (§2.3).

---

## 7. After sign-off

Per the memory/process note this proposal was delivered with: once the ⛔ sign-off arrives, a planning agent writes the full T1–T7-style T9 card(s) into PRODUCT-PLAN §12 and logs the pinned decisions in §11.5 — **including the §5/§8 Gemini/Antigravity canon correction (§5 of this file)**. Do not implement from this proposal directly; build happens in a fresh agent per §12.0 once the card exists. If sign-off is refused or amended, this file is updated (or superseded) rather than silently diverging.

---

## 8. Sources (fetched + verified 2026-06-10; expect link rot — the pins are the API paths, not these URLs)

**Anthropic** — `platform.claude.com/docs/en/api/usage-cost-api` (guide) · `…/api/admin-api/usage-cost/get-cost-report` · `…/api/admin-api/usage-cost/get-messages-usage-report` · `…/api/administration-api` (key class, admin role, individual-account exclusion) · `…/manage-claude/claude-code-analytics-api` · `platform.claude.com/settings/admin-keys` (mint page). Enterprise Analytics: support.claude.com articles 13694757 / 13703965.

**OpenAI** — `platform.openai.com/docs/api-reference/usage` (canonical) · `developers.openai.com/api/reference/resources/admin/...organization/subresources/usage` (11-endpoint enumeration) + `…/usage/methods/costs` + `…/usage/methods/completions` + `…/admin_api_keys/methods/create` (`sk-admin-` prefix) · `developers.openai.com/api/docs/guides/admin-apis` · Help Center article 9687866 (Owners-only) · cookbook `completions_usage_api` · status.openai.com incident `01K9KTXR5AHYGY96HCY71YYCKA` (costs 404 outage).

**Gemini / Google** — `ai.google.dev/api` (no usage/billing resource) · `ai.google.dev/gemini-api/docs/billing` (prepay 2026-03-23, UI-only cost views) · `docs.cloud.google.com/billing/docs/apis` (four APIs, no cost endpoint) · `…/billing/docs/how-to/export-data-bigquery-setup` + `…/export-data-bigquery-tables/standard-usage` (export schema, backfill limits) · `…/bigquery/docs/reference/rest/v2/jobs/query` (OAuth-only) · `developers.google.com/identity/protocols/oauth2/service-account` (RS256 JWT-bearer) · `…/monitoring/api/ref_v3/rest/v3/projects.timeSeries/list` · Google blog "Prepay for the Gemini API".
