# `samples/benchmark/` — deterministic `costroid bench` outputs (pack c)

**SYNTHETIC + ESTIMATED data — every figure here is "estimated — pending M3b
measurement" (R8/R10).** These are `costroid bench` outputs in **estimated / what-if**
mode (no model weights, no hardware, no subprocess). The energy/cost numbers are derived
from the bundled Gemma 4 manifest's `estimated_tok_s` + the dated power profile, NOT from a
real run. The real captured joules/token land in a documented post-M3b refresh
(`docs/POST-M3B-REFRESH.md`); until then **every row carries
`x_MeasurementMode == "estimated"`** — the inverse honesty guard (no committed artifact may
claim a measured number pre-M3b).

## Files

| File | Model | Quant | Runtime |
|---|---|---|---|
| `gemma-4-31b-dense.bench.json` | Gemma 4 31B Dense (Apache-2.0) | Q4_K_M | ollama |
| `gemma-4-26b-a4b.bench.json` | Gemma 4 26B A4B (Apache-2.0) | Q4_K_M | ollama |
| `*.bench.json.sha256` | Integrity sidecars (`sha256sum` of each JSON). |

Each file is a FOCUS 1.3 envelope (`{"focusVersion":"1.3","rows":[…]}`) with exactly one
`local_inference`-lane row: 2,000 prompt + 18,000 generated = **20,000** total
`x_ConsumedTokens`, stamped against the `strix-halo-128gb@2026-06-20` hardware profile and the
`gemma4-local-v1` benchmark suite.

## How the demo uses it

The benchmark pack is the `local_inference` lane of the merged demo ledger. Load a file and its
single row joins the developer-tool + cloud-API rows in one FOCUS 1.3 ledger. The
`scripts/focus_conformance.sh` `samples/` leg regenerates these rows deterministically and
validates the three-lane union.

## How to regenerate (deterministic — byte-identical to the committed files)

Determinism comes from **D5**: `bench` honors `SOURCE_DATE_EPOCH` for the row timestamp. Export
it as the gemma4 manifest `as_of` (2026-06-20 → epoch `1781913600`) so the output is byte-stable.

```bash
EPOCH=1781913600   # 2026-06-20T00:00:00Z, the gemma4.v1.json as_of
for model in gemma-4-31b-dense gemma-4-26b-a4b; do
  SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
    bench --model "$model" --tokens-in 2000 --tokens-out 18000 --out json \
    > "samples/benchmark/$model.bench.json"
  ( cd samples/benchmark && sha256sum "$model.bench.json" > "$model.bench.json.sha256" )
done
```

(Without `SOURCE_DATE_EPOCH`, `bench` stamps `Utc::now()` and the output is not reproducible.)
