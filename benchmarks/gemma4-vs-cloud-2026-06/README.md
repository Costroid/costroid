# `benchmarks/gemma4-vs-cloud-2026-06/` — versioned benchmark dataset

**MIXED measured/estimated data (M3b Phase 2).** `gemma-4-31b-dense` is a real **wall-meter
measurement**; `gemma-4-26b-a4b` + `gemma-4-12b-unified` stay **"estimated — pending M3b
measurement" (R8/R10)**. This is the **published, versioned** benchmark dataset the writeup
[`docs/benchmark-gemma4-vs-cloud.md`](../../docs/benchmark-gemma4-vs-cloud.md) cites: what
Gemma 4 (Apache-2.0) costs to run **locally** on a 128 GB AMD Strix Halo APU vs the
**cloud** (Anthropic/Bedrock list prices). It is **distinct from `samples/benchmark/`** (the
T1 demo pack); this directory is the dated artifact a reader can verify byte-for-byte.

> The `gemma-4-31b-dense` local energy/cost/tok-s numbers are a **real captured wall-meter run**
> (`measurement_mode == "measured_wallmeter"`, llama.cpp Vulkan on the Strix Halo, 2026-06-25). The
> `gemma-4-26b-a4b` + `gemma-4-12b-unified` numbers are still **derived** from the bundled Gemma 4
> manifest's `estimated_tok_s` + the dated power profile, **NOT** a captured joules/token. The cloud
> numbers are **counterfactual list-price estimates** from the bundled catalog (your tokens ×
> current list prices), **never** an actual cloud bill. The remaining estimated models flip in a
> later pass of the documented post-M3b refresh
> ([`docs/POST-M3B-REFRESH.md`](../../docs/POST-M3B-REFRESH.md)); the provenance guard
> (`apps/cli/tests/post_m3b_refresh.rs`) enforces that a run may claim a measured mode **only** if
> its model is on `costroid_power::MEASURED_MODELS` (31b is; 26b/12b are not).

## Layout

| File | What it is |
|---|---|
| `manifest.v1.json` | The versioned benchmark manifest (header + per-run records, money as exact decimal strings). |
| `manifest.v1.json.sha256` | Integrity sidecar (`sha256sum` of the manifest). |
| `raw/<model>.bench.json` | The deterministic `costroid bench` FOCUS-1.3 output for each model. |
| `raw/<model>.bench.json.sha256` | Integrity sidecar for each raw output. |

Each `raw/*.bench.json` is one `local_inference`-lane FOCUS 1.3 row, `Q4_K_M`, against the
`strix-halo-128gb@2026-06-20` hardware profile and the `gemma4-local-v1` benchmark suite. The
**26b/12b** rows are **estimated** `ollama` runs of a **2,000-prompt + 18,000-generated =
20,000-token** workload. The **31b** row is the **measured** `llama.cpp` (Vulkan) run — a real
captured decode of **18,000 generated tokens** from the fixed ~18-token benchmark prompt
(**18,018-token** total), at **96 W** / **9.698 tok/s**. The manifest's per-run records add the
cloud comparison (the compared cloud model, its catalog list-price cost for the same tokens, the
blended cloud rate, and the break-even outcome/volume).

## How to regenerate / verify

**The two estimated rows (`26b`/`12b`) regenerate byte-for-byte.** Determinism comes from **D5**:
`bench` honors `SOURCE_DATE_EPOCH` for the row timestamp; export it as the gemma4 manifest `as_of`
(2026-06-20 → epoch `1781913600`) so the output is byte-stable. (Without it, `bench` stamps
`Utc::now()` and the output is not reproducible.)

```bash
EPOCH=1781913600   # 2026-06-20T00:00:00Z, the gemma4.v1.json as_of
cd benchmarks/gemma4-vs-cloud-2026-06
for model in gemma-4-26b-a4b gemma-4-12b-unified; do
  SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
    bench --model "$model" --tokens-in 2000 --tokens-out 18000 --out json \
    > "raw/$model.bench.json"
  ( cd raw && sha256sum "$model.bench.json" > "$model.bench.json.sha256" )
done
```

**The `gemma-4-31b-dense` row is a captured wall-meter MEASUREMENT — not byte-regenerable.** It was
produced once on the Strix Halo (Radeon 8060S, gfx1151) via llama.cpp **Vulkan** (Windows GPU driven
from WSL2) + a PM 231 E wall meter (96 W steady decode), and is pinned by its `.sha256` sidecar (the
integrity anchor — a re-measurement varies run-to-run). The capture command (needs the hardware +
meter + the user's GGUF) was:

```bash
SOURCE_DATE_EPOCH=1781913600 cargo run -q -p costroid --features power -- bench \
  --model gemma-4-31b-dense --runtime llama.cpp --binary <gpu-wrapper> \
  --runtime-model <gemma-4-31B-it-Q4_K_M.gguf> \
  --tokens-out 18000 --tokens-in 2000 --measure --wall-meter-watts 96 --out json
```

Then the manifest sidecar (after any manifest edit):

```bash
sha256sum manifest.v1.json > manifest.v1.json.sha256
```

> **Break-even note.** The manifest's `gemma-4-31b-dense` `cloud_comparison` break-even is computed
> from the **measured** energy rate (96 W / 9.698 tok/s) over the comparable 20,000-token workload.
> Because the bundled profile/manifest stay estimated (the partial-flip rule), `costroid breakeven
> --model gemma-4-31b-dense` reproduces the **estimated** crossover (~81,237/day), **not** the
> committed measured one (80,803/day) — the measured value follows the documented
> `costroid-core::breakeven` formula `V* = (capex / days) / (c − e)` with `e = energy_only_cost ÷
> 20000`. The 26b/12b break-even rows are reproduced by `costroid breakeven --model <m> --tokens-in
> 2000 --tokens-out 18000 --compare-to claude-opus-4-8`.

## Integrity

[`scripts/check_benchmarks.sh`](../../scripts/check_benchmarks.sh) enumerates every committed
manifest + raw output under `benchmarks/` and `sha256sum -c` checks each from its own directory
(fail-closed — a missing, added, or hand-edited artifact fails the build). It is wired into the
`focus-conformance` CI job alongside `check_power_profiles.sh` / `check_doc_stamps.sh`.
