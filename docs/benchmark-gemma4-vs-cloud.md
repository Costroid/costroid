# What Gemma 4 actually costs to run locally on a 128 GB APU — vs Bedrock/Anthropic

This is the **blog-ready** companion to Costroid's local-inference economics: a concrete,
reproducible look at what it costs to run **Gemma 4** (Apache-2.0) **locally** on a 128 GB AMD
Strix Halo APU versus paying **cloud** list prices (Anthropic / AWS Bedrock) for the same tokens.
It pairs with the technical [`methodology.md`](methodology.md) (the exact formulas) and
[`limitations.md`](limitations.md) (what Costroid cannot know). When this doc disagrees with the
code, **the code wins** — every number below is what the engine emits.

> **Honesty stamp (R8/R10).** This writeup is now **mixed measured/estimated** (M3b Phase 2). The
> **Gemma 4 31B Dense** local cost, energy, and throughput figures are a **real wall-meter
> measurement** (llama.cpp Vulkan on the Strix Halo, 96 W, 2026-06-25). The **26B/12B** figures are
> still **estimated — pending M3b measurement**: their throughput (tok/s) numbers are
> **community-analog estimates** from the bundled Gemma 4 manifest, not measurements on this
> hardware, and their energy is **derived** from a dated, stamped, overridable power profile, not a
> captured joules/token. The cloud dollars are a **counterfactual list-price estimate** (your tokens
> × current catalog list prices), never an actual cloud invoice. Model **quality** is **as
> published** (cited, never re-derived). The remaining estimated models flip in a later pass of the
> documented post-M3b refresh ([`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md)). **Cost is always an
> estimate** — reconcile against the provider bill.

## The honest headline

**At low daily volume, running locally LOSES on pure dollars-per-token.** The hardware capex
(amortized over its calendar life) dominates a light workload — you would have to push a lot of
tokens every day, every day, for years, before the machine pays for itself versus simply paying
cloud list prices. Local wins on **privacy** (nothing leaves the machine), **unlimited use** (no
per-token meter, no rate-limit windows), and **marginal cost** (the energy floor is tiny). But the
crossover is a **volume** story, not a free lunch — so this writeup presents **ranges and a
break-even volume**, never a single hero number.

For the **measured** **Gemma 4 31B Dense** run benchmarked here (wall meter, 96 W, llama.cpp
Vulkan, 2026-06-25), local breaks even versus `claude-opus-4-8` list pricing at roughly **80,800
tokens/day** (sensitivity band ≈ **65,000 – 98,000 tokens/day**). Below that, cloud is cheaper on
dollars alone.

## The benchmark

Hardware target: **AMD Strix Halo / Ryzen AI Max+ 395**, Radeon 8060S iGPU (gfx1151, RDNA3.5),
**128 GB** unified memory (~215 GB/s — inference is memory-bandwidth-bound). Shared assumptions:
**$2,000** hardware price, **3-year** (1,095-day) amortization, electricity **$0.16/kWh** (a dated
`global-household-average-template`). System power: the **31B Dense** run is a **measured 96 W** (PM
231 E wall meter, steady decode); the **26B/12B** runs use the profile `strix-halo-128gb@2026-06-20`
**155 W** estimate (an overridable assumption — *estimated — pending M3b measurement*).

Workload: **2,000 prompt + 18,000 generated = 20,000 tokens**, `Q4_K_M`. The **26B/12B** are
`ollama`, **estimated** mode (no weights, no hardware, no subprocess). The **31B Dense** is the
**measured** `llama.cpp` (Vulkan) run — a real decode of **18,000 generated tokens** from the fixed
~18-token benchmark prompt (**18,018 total**). The three benchmarked models, their throughput (**31B
measured; 26B/12B community-analog estimates — *estimated — pending M3b measurement***), and the
engine's per-run local economics:

| Model (Apache-2.0) | tok/s | Energy (Wh) | Energy-only cost | Amortized HW | Local run cost |
|---|---|---|---|---|---|
| Gemma 4 31B Dense **(measured)** | 9.698 | 49.494614 Wh | $0.0079191382 | $0.0392365975 | $0.0471557357 |
| Gemma 4 12B Unified *(estimated)* | ~30 | 25.833333 Wh | $0.0041333333 | $0.0126839168 | $0.0168172501 |
| Gemma 4 26B A4B (fast MoE) *(estimated)* | ~96 | 8.072917 Wh | $0.0012916667 | $0.003963724 | $0.0052553907 |

*(The **31B Dense** row is a **measured wall-meter run** (96 W, llama.cpp Vulkan, 2026-06-25) over its
real **18,018-token** decode; the **12B/26B** rows are **estimated — pending M3b measurement** over
the 20,000-token workload. Energy = `avg_watts × wall_seconds / 3600` with `wall_seconds = tokens_out
/ tok_s` — **decode time dominates**, so the wall-clock tracks the **generated (output) tokens only**,
matching the engine ([`crates/costroid-power/src/harness.rs`](../crates/costroid-power/src/harness.rs));
the measured 31B used `avg_watts = 96`, the estimated rows `155`. Energy-only cost cross-checks as
`Wh / 1000 × $0.16/kWh`.)*

### The energy floor (marginal cost)

The **marginal** local cost is energy-only — the per-token rate `e` over the **total (in+out)**
token basis (see [`methodology.md`](methodology.md) §4). It excludes the amortized capex (that is
the break-even *fixed* term, not a marginal cost):

| Model | energy-only `e` |
|---|---|
| Gemma 4 31B Dense **(measured)** | ~$0.44 per million tokens |
| Gemma 4 12B Unified *(estimated — pending M3b measurement)* | ~$0.21 per million tokens |
| Gemma 4 26B A4B *(estimated — pending M3b measurement)* | ~$0.065 per million tokens |

So the **electricity** to generate a million tokens locally is a fraction of a dollar — far below
any cloud list price. That is the seductive part. The catch is the capex you carry to get there.

## The cloud side (counterfactual list price)

Cloud dollars are sourced from Costroid's bundled **layered pricing catalog**
(`curated@2026-06-02`), priced per token — never invented. For the same 20,000-token run
(2,000 in + 18,000 out, output-heavy at a 0.9 output share):

| Cloud model | input $/1M | output $/1M | cost for the 20k-token run | blended $/1M |
|---|---|---|---|---|
| `claude-opus-4-8` | $5 | $25 | **$0.46** | $23 |
| `claude-sonnet-4-6` | $3 | $15 | **$0.276** | $13.8 |

So one ~20k-token run whose **marginal energy cost** is roughly **$0.001–$0.008** locally
(energy-only; 31B **measured**, 26B/12B **estimated — pending M3b measurement**) would cost
**$0.28–$0.46** at cloud list price — the cloud is **~35–350×** the marginal energy cost per run. If
marginal cost were the whole story, local would win overwhelmingly. It is not: the amortized hardware
capex (here **$0.004–$0.039 per run** — the energy-plus-capex per-run total is **$0.005–$0.047**) is
what flips the economics, which is the break-even story below.

## Break-even — where local actually wins

The crossover is the pure `costroid-core::breakeven` math (no `core → power` edge):

```
hw_fixed_per_day = hardware_price ÷ depreciation_period_days   # $2000 / 1095 ≈ $1.83/day
V*               = hw_fixed_per_day ÷ (c − e)                   # tokens/day to break even
```

where `c` is the blended cloud per-token rate and `e` is the energy-only local rate (for 31B, the
**measured** `e` over the comparable 20,000-token workload — see the basis note below):

| Compared cloud model | break-even volume | sensitivity band |
|---|---|---|
| 31B Dense vs `claude-opus-4-8` **(measured)** | ~**80,803 tokens/day** | ~64,643 – 98,177 tokens/day |
| 31B Dense vs `claude-sonnet-4-6` **(measured)** | ~**136,264 tokens/day** | ~109,011 – 165,983 tokens/day |
| 26B A4B vs `claude-opus-4-8` *(estimated)* | ~**79,636 tokens/day** | ~63,709 – 96,459 tokens/day |
| 12B Unified vs `claude-opus-4-8` *(estimated)* | ~**80,132 tokens/day** | ~64,106 – 97,188 tokens/day |

*(Provenance: the versioned manifest's `cloud_comparison` records the `claude-opus-4-8` break-even
per run; the `claude-sonnet-4-6` row is reproduced via the engine formula against the same catalog —
see [Reproduce it](#reproduce-it). **Token basis:** the 31B run's own marginal `e` is over its real
18,018-token measured run (~$0.44/M, the table above); its **break-even** `e` is the same measured
energy over the comparable **20,000-token** workload (~$0.40/M), so the 31B crossover stays
comparable to the unchanged 26B/12B rows and the unchanged cloud list prices.)*

Read this carefully:

- **Below ~80k tokens/day** (vs Opus list price), **cloud is cheaper on dollars.** A light user —
  a few thousand tokens a day — will never recover the $2,000 in the box. Local **loses** here.
- The cheaper the cloud model you compare against, the **higher** the local break-even volume —
  versus Sonnet you need ~**137k tokens/day**, because the cloud price `c` is closer to your local
  energy floor `e`, shrinking the margin `(c − e)` the capex has to be recovered out of.
- If the cloud price ever drops at or below your local energy floor (`c ≤ e`), the engine reports
  **`NEVER`** — the capex is never recovered, honestly, with a reason. If `V*` exceeds what the
  machine can physically generate in a day (`estimated_tok_s × utilization × 86,400`), it reports
  **`INFEASIBLE`**.

The band matters because the inputs are estimates: electricity ±50%, hardware price ±20%, and the
output mix ±0.2 all move the crossover. We **never** publish a single hero break-even number.

## Why run locally at all, then?

Dollars-per-token is the *least* compelling reason at low volume. The real reasons:

- **Privacy / data residency** — nothing leaves the machine; Costroid itself makes zero network
  calls by default. For regulated or sensitive code, that is the whole game.
- **Unlimited, un-metered use** — no per-token bill anxiety, no 5-hour / weekly rate-limit windows.
  Once the hardware is paid for, heavy iteration is "free" at the margin (the energy floor above).
- **Open weights (Apache-2.0)** — Gemma 4 is genuinely open (Gemma 1–3 used a restrictive non-OSI
  license); you own the stack, can air-gap it, and aren't exposed to a provider's pricing or
  availability changes.
- **Quality is as-published** — these economics say nothing about whether a 31B-dense local model
  *matches* a frontier cloud model on your task. Quality is **cited, never re-derived** (R10); pick
  the model on quality first, then read these economics to decide where it pays off.

## Reproduce it

The versioned dataset lives in
[`benchmarks/gemma4-vs-cloud-2026-06/`](../benchmarks/gemma4-vs-cloud-2026-06/) (manifest + raw
`costroid bench` outputs + `.sha256` sidecars), guarded by `scripts/check_benchmarks.sh`.

The **26B/12B estimated rows are deterministic and offline** — they regenerate byte-for-byte (D5
pins the bench-row timestamp to the manifest `as_of`):

```bash
EPOCH=1781913600   # 2026-06-20T00:00:00Z
SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
  bench --model gemma-4-26b-a4b --tokens-in 2000 --tokens-out 18000 --out json
cargo run -q -p costroid --features power -- \
  breakeven --model gemma-4-26b-a4b --tokens-in 2000 --tokens-out 18000 \
  --compare-to claude-opus-4-8 --plain

# The full one-command demo (import → bench → break-even → export FOCUS), offline:
make demo
```

The **31B Dense row is a captured wall-meter MEASUREMENT — not byte-regenerable** (it needs the
Strix Halo + a PM 231 E meter + the user's GGUF; a re-measurement varies run-to-run). Its committed
bytes are pinned by `gemma-4-31b-dense.bench.json.sha256`; the capture command + the exact break-even
derivation (the estimated `costroid breakeven` CLI reproduces the *estimated* ~81,237/day, **not**
the measured 80,803/day) are documented in the dataset
[`README.md`](../benchmarks/gemma4-vs-cloud-2026-06/README.md). `check_benchmarks.sh` verifies all
three `.sha256` sidecars.

## Caveats (read these)

- **Package power vs wall power.** The estimated **155 W** (26B/12B) is calibrated to **system**
  draw. On an APU the iGPU shares a rail with the CPU, so no on-chip sensor isolates GPU-only watts —
  a wall meter (true total draw) is typically **~20–40% higher** than the on-chip package figure.
  The measured ladder *leads* with the wall meter for exactly this reason — and the **31B Dense**
  figure here **is** that wall-meter reading (96 W). See [`methodology.md`](methodology.md) §2.
- **31B measured; 26B/12B estimated.** The **31B Dense** figures are a real M3b wall-meter run
  (2026-06-25). The **26B/12B** figures are still **estimated — pending M3b measurement** (the
  throughput is a community analog, not a reading on this machine).
  [`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md) is the closed checklist of exactly which figures a
  later measured pass replaces.
- **A range, never a hero number.** The break-even moves with electricity, hardware price, and the
  output mix; treat the band as the answer, not its midpoint.

## Cross-references

- The exact formulas (energy/token, `e` over total tokens, the break-even math, the `NEVER` /
  `INFEASIBLE` cases): [`methodology.md`](methodology.md).
- What Costroid cannot know (sub-agent attribution, package-vs-wall, the uncertain-row annotation):
  [`limitations.md`](limitations.md).
- The post-M3b figure refresh runbook: [`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md).
