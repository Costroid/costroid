# Bundled pricing snapshots

Costroid prices usage from **bundled, dated, pinned** pricing snapshots — **never** a
runtime or build-time network fetch (Golden rule / R8). Two snapshots are vendored here; the
curated one is loaded today by `costroid-core`'s `PricingCatalog`, and the LiteLLM one joins
it as a layered tier when the M2 cloud-lane loader lands (T3):

| File | `source` | `as_of` | Role |
|---|---|---|---|
| `pricing.v1.json` | `curated` | 2026-06-02 | Hand-curated, **authoritative** list prices for the dev-tool models, re-sourced from the official provider pages each release; verified to-the-cent vs ccusage. |
| `litellm-prices.v1.json` | `litellm` | 2026-06-18 | The cloud-API **long tail** (Bedrock / OpenAI / Azure / Gemini / Vertex / Mistral / xAI / DeepSeek / Cohere), derived from LiteLLM. |

**Layered precedence (M2 / decision D2 — wired in T3):** `user override > curated
(pricing.v1.json) > LiteLLM long-tail`. The curated tier always wins a model both tiers carry,
so the verified-to-the-cent dev-tool numbers never regress; LiteLLM only fills models the
curated tier lacks. Each priced row will be stamped `x_PricingSnapshotId =
"{source}@{as_of}#{hash8}"` from the **winning** tier (R8: source + date + content hash
recorded for every comparison).

## `litellm-prices.v1.json` — provenance (R8)

- **Upstream:** LiteLLM `model_prices_and_context_window.json`
  <https://github.com/BerriAI/litellm/blob/4c25b7a13d50462103af64daadf696410393e1b4/model_prices_and_context_window.json>
- **Pinned commit:** `4c25b7a13d50462103af64daadf696410393e1b4` (upstream date 2026-06-18).
- **Upstream content sha256:** `36c8994e4d65edcfe396c64737d90aa0f7f303784067a26dfc2090994c6fde4d`
  (the `content_hash` recorded inside the artifact; the source of the `#36c8994e` stamp suffix).
- **License:** **MIT** — Copyright (c) 2023 Berri AI. The LiteLLM repository is MIT; the data
  file resides at the repo root, outside the `enterprise/` directory, so it is MIT. MIT is on
  Costroid's permissive allowlist and ships inside the Apache-2.0 binary with attribution
  preserved here. It is a **data artifact**, not a Cargo crate dependency — outside the
  crate-dependency license policy (same posture as the vendored FOCUS rulesets). It **will be**
  compiled into `costroid-core` via `include_str!` when the cloud-lane loader lands (M2 T3),
  because the cloud lane must reprice **offline** at runtime.
- **Transform:** `scripts/refresh_litellm_pricing.py` (a dev/CI-only tool; never run by the
  build or the CLI). It prunes to the cloud-API providers, keeps `chat`/`completion`/`responses`
  modes, converts per-token costs to **per-1M-token exact decimals** (`decimal.Decimal`, never
  binary float), drops region-routed keys (e.g. `ap-northeast-1/…`, which can't match a real
  SkuId) and both-zero "free/preview" entries, and dedups bare-name collisions in favour of the
  **first-party** provider (re-sellers — Azure/Vertex/Bedrock — sort last, so e.g.
  `mistral-large-latest` keeps Mistral's own rate, not Azure's resale). Output: **551 models**,
  deterministic (sorted keys).
- **Integrity:** each snapshot has a `*.sha256` sidecar; `scripts/check_pricing_snapshots.sh`
  fail-closed-verifies both (`sha256sum -c`). **Pending:** the script is wired into CI in M2 T14,
  and a Rust loader test asserting the embedded `source`/`as_of`/`content_hash` against the
  pinned constants lands with the loader in M2 T3.

## Updating a snapshot (deliberate re-pin)

1. Edit `PIN_SHA` / `PIN_DATE` / `RAW_SHA256` in `scripts/refresh_litellm_pricing.py` to a newer
   upstream commit; run it (it refuses to write if the fetched bytes don't match `RAW_SHA256`).
2. Re-pin the artifact sha in the Rust loader test + this README in the **same** commit (R8).
3. Re-run the focus-conformance + pricing regression tests; any changed rate is reviewed.
