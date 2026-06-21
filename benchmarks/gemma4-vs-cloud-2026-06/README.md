# `benchmarks/gemma4-vs-cloud-2026-06/` — versioned benchmark dataset

**SYNTHETIC + ESTIMATED data — every figure here is "estimated — pending M3b
measurement" (R8/R10).** This is the **published, versioned** benchmark dataset the writeup
[`docs/benchmark-gemma4-vs-cloud.md`](../../docs/benchmark-gemma4-vs-cloud.md) cites: what
Gemma 4 (Apache-2.0) costs to run **locally** on a 128 GB AMD Strix Halo APU vs the
**cloud** (Anthropic/Bedrock list prices). It is **distinct from `samples/benchmark/`** (the
T1 demo pack); this directory is the dated artifact a reader can verify byte-for-byte.

> The local energy/cost numbers are **derived** from the bundled Gemma 4 manifest's
> `estimated_tok_s` + the dated power profile, **NOT** a real captured joules/token. The cloud
> numbers are **counterfactual list-price estimates** from the bundled catalog (your tokens ×
> current list prices), **never** an actual cloud bill. The real captured numbers land in a
> documented post-M3b refresh ([`docs/POST-M3B-REFRESH.md`](../../docs/POST-M3B-REFRESH.md));
> until then **every run carries `measurement_mode == "estimated"`** (the inverse honesty guard
> — no committed artifact may claim a measured number pre-M3b).

## Layout

| File | What it is |
|---|---|
| `manifest.v1.json` | The versioned benchmark manifest (header + per-run records, money as exact decimal strings). |
| `manifest.v1.json.sha256` | Integrity sidecar (`sha256sum` of the manifest). |
| `raw/<model>.bench.json` | The deterministic `costroid bench` FOCUS-1.3 output for each model. |
| `raw/<model>.bench.json.sha256` | Integrity sidecar for each raw output. |

Each `raw/*.bench.json` is one `local_inference`-lane FOCUS 1.3 row for a **2,000-prompt +
18,000-generated = 20,000-token** run, `Q4_K_M`, `ollama`, against the
`strix-halo-128gb@2026-06-20` hardware profile and the `gemma4-local-v1` benchmark suite. The
manifest's per-run records add the cloud comparison (the compared cloud model, its catalog
list-price cost for the same tokens, the blended cloud rate, and the break-even outcome/volume).

## How to regenerate (deterministic — byte-identical to the committed files)

Determinism comes from **D5**: `bench` honors `SOURCE_DATE_EPOCH` for the row timestamp. Export
it as the gemma4 manifest `as_of` (2026-06-20 → epoch `1781913600`) so the output is byte-stable.

```bash
EPOCH=1781913600   # 2026-06-20T00:00:00Z, the gemma4.v1.json as_of
cd benchmarks/gemma4-vs-cloud-2026-06
for model in gemma-4-31b-dense gemma-4-26b-a4b gemma-4-12b-unified; do
  SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
    bench --model "$model" --tokens-in 2000 --tokens-out 18000 --out json \
    > "raw/$model.bench.json"
  ( cd raw && sha256sum "$model.bench.json" > "$model.bench.json.sha256" )
done
sha256sum manifest.v1.json > manifest.v1.json.sha256
```

The break-even volumes + the cloud list-price comparison in `manifest.v1.json` are reproduced
from the engine:

```bash
cargo run -q -p costroid --features power -- \
  breakeven --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 \
  --compare-to claude-opus-4-8 --plain
```

(Without `SOURCE_DATE_EPOCH`, `bench` stamps `Utc::now()` and the output is not reproducible.)

## Integrity

[`scripts/check_benchmarks.sh`](../../scripts/check_benchmarks.sh) enumerates every committed
manifest + raw output under `benchmarks/` and `sha256sum -c` checks each from its own directory
(fail-closed — a missing, added, or hand-edited artifact fails the build). It is wired into the
`focus-conformance` CI job alongside `check_power_profiles.sh` / `check_doc_stamps.sh`.
