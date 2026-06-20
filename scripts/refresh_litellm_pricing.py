#!/usr/bin/env python3
"""Refresh the vendored LiteLLM pricing snapshot.

DEV/CI-ONLY tool — it is **never** run by the `cargo build` or by the `costroid`
binary. It fetches a PINNED upstream revision of LiteLLM's
`model_prices_and_context_window.json` (MIT-licensed) and transforms it into
Costroid's own `PricingCatalog` JSON schema (the `pricing.v1.json` shape), pruned
to the cloud-API providers Costroid reprices. The result is vendored as a dated,
content-hashed, attributed data artifact (R8) — never fetched at build or runtime.

Pricing integrity (R8):
  - The upstream revision is PINNED by commit SHA + the upstream content's sha256.
  - Prices are read as exact `decimal.Decimal` (parse_float=Decimal) and converted
    per-1M-tokens losslessly — never via binary float.
  - The emitted artifact records source URL, pinned SHA, upstream date, and the
    transform's own sha256 (the .sha256 sidecar; checked by
    scripts/check_pricing_snapshots.sh, and — once the loader lands in T3 — by a Rust
    loader test asserting the embedded source/as_of/content_hash).

Usage (offline-reproducible):
    python3 scripts/refresh_litellm_pricing.py            # fetch the pinned URL
    python3 scripts/refresh_litellm_pricing.py --from-file /tmp/litellm_raw.json
A non-pinned refresh (bump the snapshot): edit PIN_SHA / PIN_DATE / RAW_SHA256 to a
newer upstream commit, run, then re-pin the Rust snapshot test + commit deliberately.
"""
from __future__ import annotations

import argparse
import decimal
import hashlib
import json
import sys
import urllib.request
from pathlib import Path

# --- Pinned upstream revision (R8) ------------------------------------------------
UPSTREAM_REPO = "BerriAI/litellm"
PIN_SHA = "4c25b7a13d50462103af64daadf696410393e1b4"
PIN_DATE = "2026-06-18"  # the upstream commit date — the snapshot's `as_of`.
RAW_URL = (
    f"https://raw.githubusercontent.com/{UPSTREAM_REPO}/{PIN_SHA}/"
    "model_prices_and_context_window.json"
)
RAW_SHA256 = "36c8994e4d65edcfe396c64737d90aa0f7f303784067a26dfc2090994c6fde4d"

# Cloud-API providers Costroid reprices. LiteLLM `litellm_provider` -> (Costroid
# provider, FOCUS ServiceName). Embedding/image/audio modes are skipped (per-token
# token economics only). The curated `pricing.v1.json` stays authoritative for the
# dev-tool models via the layered catalog (this is the long-tail tier).
PROVIDER_MAP = {
    "anthropic": ("anthropic", "Anthropic API"),
    "openai": ("openai", "OpenAI API"),
    "azure": ("azure_openai", "Azure OpenAI"),
    "azure_ai": ("azure_openai", "Azure OpenAI"),
    "bedrock": ("bedrock", "Amazon Bedrock"),
    "bedrock_converse": ("bedrock", "Amazon Bedrock"),
    "gemini": ("google", "Google Gemini API"),
    "vertex_ai": ("vertex_ai", "Google Vertex AI"),
    "mistral": ("mistral", "Mistral API"),
    "xai": ("xai", "xAI API"),
    "deepseek": ("deepseek", "DeepSeek API"),
    "cohere": ("cohere", "Cohere API"),
    "cohere_chat": ("cohere", "Cohere API"),
}
# Dedup priority: a bare model name claimed by its FIRST-PARTY provider wins over a
# re-seller duplicate, so e.g. `claude-...` resolves to the Anthropic rate and
# `mistral-large-latest` to Mistral's own rate (NOT Azure's marked-up resale). The
# re-sellers (azure/vertex/bedrock) sort LAST so they only fill models no first party
# offers under that bare name.
PROVIDER_PRIORITY = [
    "anthropic", "openai", "google", "mistral", "xai", "deepseek", "cohere",
    "azure_openai", "vertex_ai", "bedrock",
]
TEXT_MODES = {"chat", "completion", "responses"}
ROUTING_PREFIXES = (
    "bedrock/", "vertex_ai/", "gemini/", "azure/", "azure_ai/",
    "openai/", "anthropic/", "mistral/", "cohere/",
)
# (LiteLLM per-token cost field, Costroid meter name)
METERS = [
    ("input_cost_per_token", "input"),
    ("output_cost_per_token", "output"),
    ("cache_read_input_token_cost", "cache_read"),
    ("cache_creation_input_token_cost", "cache_write"),
]


def dec_per_million(per_token: decimal.Decimal) -> str:
    """Exact per-token -> per-1M-tokens, as a plain (non-scientific) decimal string."""
    per_1m = (per_token * decimal.Decimal(1_000_000)).normalize()
    # `format(_, 'f')` expands scientific notation that `.normalize()` can introduce
    # (e.g. Decimal('6E+2') -> '600'), so Costroid's `Decimal::from_str` always parses.
    return format(per_1m, "f")


def clean_model_key(key: str) -> str:
    for prefix in ROUTING_PREFIXES:
        if key.startswith(prefix):
            return key[len(prefix):]
    return key


def costroid_provider(litellm_provider: str) -> tuple[str, str] | None:
    if litellm_provider in PROVIDER_MAP:
        return PROVIDER_MAP[litellm_provider]
    if litellm_provider.startswith("bedrock"):
        return ("bedrock", "Amazon Bedrock")
    if litellm_provider.startswith("vertex_ai"):
        return ("vertex_ai", "Google Vertex AI")
    if litellm_provider.startswith("cohere"):
        return ("cohere", "Cohere API")
    return None


def transform(raw: dict) -> list[dict]:
    """Upstream LiteLLM map -> Costroid catalog `models` list (deduped, sorted)."""
    # Sort entries by provider priority so the canonical direct provider wins a
    # bare-name collision; ties broken by model key for deterministic output.
    def sort_rank(item: tuple[str, dict]) -> tuple[int, str]:
        _key, val = item
        mapped = costroid_provider(val.get("litellm_provider", "")) if isinstance(val, dict) else None
        prov = mapped[0] if mapped else "~"
        rank = PROVIDER_PRIORITY.index(prov) if prov in PROVIDER_PRIORITY else len(PROVIDER_PRIORITY)
        return (rank, _key)

    by_model: dict[str, dict] = {}
    dups = 0
    routed = 0
    zero = 0
    for key, val in sorted(raw.items(), key=sort_rank):
        if not isinstance(val, dict):
            continue
        if val.get("mode") not in TEXT_MODES:
            continue
        mapped = costroid_provider(val.get("litellm_provider", ""))
        if mapped is None:
            continue
        provider, service_name = mapped
        model = clean_model_key(key)
        # A residual "/" after prefix-stripping is a region/route key (e.g.
        # `ap-northeast-1/anthropic.claude-...`, `bedrock_mantle/...`) that can never
        # match a real FOCUS SkuId / provider-log model id — drop it (the canonical
        # bare id is kept under its own entry).
        if "/" in model:
            routed += 1
            continue
        rates = []
        positive = False
        for field, meter in METERS:
            if field in val and val[field] is not None:
                price = val[field]
                if not isinstance(price, decimal.Decimal):
                    # parse_float=Decimal should make these Decimal; guard anyway.
                    price = decimal.Decimal(str(price))
                if price < 0:
                    continue
                if meter in ("input", "output") and price > 0:
                    positive = True
                rates.append({"meter": meter, "unit": "1M_tokens", "price": dec_per_million(price)})
        # Require a POSITIVE input or output rate — a both-zero "free/preview" upstream
        # entry would otherwise silently reprice real usage to $0 (honesty > coverage).
        if not positive:
            zero += 1
            continue
        if model in by_model:
            dups += 1
            continue
        by_model[model] = {
            "provider": provider,
            "model": model,
            "service_name": service_name,
            "rates": rates,
        }
    print(
        f"  transformed {len(by_model)} models "
        f"({dups} dup keys, {routed} region-routed, {zero} zero-priced skipped)",
        file=sys.stderr,
    )
    return [by_model[m] for m in sorted(by_model)]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--from-file", type=Path, help="read the raw upstream JSON from a local file instead of fetching")
    ap.add_argument(
        "--out",
        type=Path,
        default=Path(__file__).resolve().parent.parent / "crates/costroid-core/pricing/litellm-prices.v1.json",
        help="output path for the vendored Costroid-schema snapshot",
    )
    args = ap.parse_args()

    if args.from_file:
        raw_bytes = args.from_file.read_bytes()
        print(f"==> read upstream from {args.from_file}", file=sys.stderr)
    else:
        print(f"==> fetching pinned upstream {RAW_URL}", file=sys.stderr)
        with urllib.request.urlopen(RAW_URL, timeout=60) as resp:  # noqa: S310 (pinned https URL)
            raw_bytes = resp.read()

    got_sha = hashlib.sha256(raw_bytes).hexdigest()
    if got_sha != RAW_SHA256:
        print(
            f"FATAL: upstream sha256 {got_sha} != pinned {RAW_SHA256}.\n"
            "The upstream content changed. Bump PIN_SHA/PIN_DATE/RAW_SHA256 deliberately, "
            "then re-pin the Rust snapshot test in the same commit (R8).",
            file=sys.stderr,
        )
        return 1

    raw = json.loads(raw_bytes.decode("utf-8"), parse_float=decimal.Decimal)
    models = transform(raw)

    catalog = {
        "schema_version": "1",
        # R8 provenance the Rust loader reads to stamp x_PricingSnapshotId on every
        # row it prices: "{source}@{as_of}#{content_hash[:8]}".
        "source": "litellm",
        "as_of": PIN_DATE,
        "content_hash": RAW_SHA256,
        "currency": "USD",
        "sources": [
            {
                "name": "LiteLLM model_prices_and_context_window.json",
                "url": f"https://github.com/{UPSTREAM_REPO}/blob/{PIN_SHA}/model_prices_and_context_window.json",
                "pinned_commit": PIN_SHA,
                "upstream_sha256": RAW_SHA256,
                "license": "MIT (Copyright (c) 2023 Berri AI)",
                "fetched_for": "Costroid cloud-API lane long-tail repricing (M2)",
            }
        ],
        "notes": (
            "Generated by scripts/refresh_litellm_pricing.py from a PINNED LiteLLM "
            "revision (MIT). Pruned to cloud-API providers; per-token costs converted "
            "to per-1M-tokens as exact decimals. The curated pricing.v1.json stays "
            "authoritative for dev-tool models via the layered catalog; this is the "
            "long-tail tier. Never fetched at build or runtime (R8)."
        ),
        "models": models,
    }

    # Stable, deterministic serialization (sorted keys, trailing newline).
    text = json.dumps(catalog, indent=2, sort_keys=True, ensure_ascii=False) + "\n"
    args.out.write_text(text, encoding="utf-8")
    out_sha = hashlib.sha256(text.encode("utf-8")).hexdigest()
    # `sha256sum -c`-compatible sidecar (full-bytes integrity guard, checked in CI by
    # scripts/check_pricing_snapshots.sh) — catches any post-generation edit to the
    # artifact that did not go through this pinned, deterministic transform.
    sidecar = args.out.with_suffix(args.out.suffix + ".sha256")
    sidecar.write_text(f"{out_sha}  {args.out.name}\n", encoding="utf-8")
    print(f"==> wrote {args.out} ({len(models)} models, {len(text)} bytes)", file=sys.stderr)
    print(f"==> wrote {sidecar}", file=sys.stderr)
    print(f"==> artifact sha256: {out_sha}", file=sys.stderr)
    print(f"    (record this in the sibling README + the Rust loader test, as_of={PIN_DATE})", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
