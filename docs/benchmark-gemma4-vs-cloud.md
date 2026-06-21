# What Gemma 4 actually costs to run locally on a 128 GB APU — vs Bedrock/Anthropic

This is the **blog-ready** companion to Costroid's local-inference economics: a concrete,
reproducible look at what it costs to run **Gemma 4** (Apache-2.0) **locally** on a 128 GB AMD
Strix Halo APU versus paying **cloud** list prices (Anthropic / AWS Bedrock) for the same tokens.
It pairs with the technical [`methodology.md`](methodology.md) (the exact formulas) and
[`limitations.md`](limitations.md) (what Costroid cannot know). When this doc disagrees with the
code, **the code wins** — every number below is what the engine emits.

> **Honesty stamp (R8/R10).** Every local cost, energy, and throughput figure in this writeup is
> **estimated — pending M3b measurement**. The throughput (tok/s) numbers are **community-analog
> estimates** from the bundled Gemma 4 manifest, not measurements on this hardware; the energy is
> **derived** from a dated, stamped, overridable power profile, not a real captured joules/token;
> the cloud dollars are a **counterfactual list-price estimate** (your tokens × current catalog
> list prices), never an actual cloud invoice. Model **quality** is **as published** (cited, never
> re-derived). Real captured numbers fill these placeholders in a documented post-M3b refresh
> ([`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md)). **Cost is always an estimate** — reconcile
> against the provider bill.

## The honest headline

**At low daily volume, running locally LOSES on pure dollars-per-token.** The hardware capex
(amortized over its calendar life) dominates a light workload — you would have to push a lot of
tokens every day, every day, for years, before the machine pays for itself versus simply paying
cloud list prices. Local wins on **privacy** (nothing leaves the machine), **unlimited use** (no
per-token meter, no rate-limit windows), and **marginal cost** (the energy floor is tiny). But the
crossover is a **volume** story, not a free lunch — so this writeup presents **ranges and a
break-even volume**, never a single hero number.

For the estimated **Gemma 4 31B Dense** run benchmarked here, local breaks even versus
`claude-opus-4-8` list pricing at roughly **81,000 tokens/day** (sensitivity band ≈ **65,000 –
99,000 tokens/day**) — *estimated — pending M3b measurement*. Below that, cloud is cheaper on
dollars alone.

## The benchmark

Hardware target: **AMD Strix Halo / Ryzen AI Max+ 395**, Radeon 8060S iGPU (gfx1151, RDNA3.5),
**128 GB** unified memory (~215 GB/s — inference is memory-bandwidth-bound). Profile
`strix-halo-128gb@2026-06-20`: **155 W** system load (an estimated, overridable assumption — *estimated
— pending M3b measurement*), **$2,000** hardware price, **3-year** (1,095-day) amortization,
electricity **$0.16/kWh** (a dated `global-household-average-template`).

Workload per run: **2,000 prompt + 18,000 generated = 20,000 tokens**, `Q4_K_M`, `ollama`,
estimated mode (no model weights, no hardware, no subprocess). The three benchmarked models, their
**estimated** throughput (tok/s — community analog, *estimated — pending M3b measurement*), and the
engine's per-run local economics:

| Model (Apache-2.0) | est. tok/s | Energy (Wh) | Energy-only cost | Amortized HW | Local run cost |
|---|---|---|---|---|---|
| Gemma 4 31B Dense | ~12 | 64.583333 Wh | $0.0103333333 | $0.031709792 | $0.0420431253 |
| Gemma 4 12B Unified | ~30 | 25.833333 Wh | $0.0041333333 | $0.0126839168 | $0.0168172501 |
| Gemma 4 26B A4B (fast MoE) | ~96 | 8.072917 Wh | $0.0012916667 | $0.003963724 | $0.0052553907 |

*(All figures **estimated — pending M3b measurement**. Energy = `155 W × wall_seconds / 3600`,
where `wall_seconds = tokens_out / estimated_tok_s` — **decode time dominates**, so the wall-clock is
estimated from the **generated (output) tokens only**, matching the engine
([`crates/costroid-power/src/harness.rs`](../crates/costroid-power/src/harness.rs)); the slower dense
model burns more wall-clock time, hence more Wh and more amortized capex for the same token count.)*

### The energy floor (marginal cost)

The **marginal** local cost is energy-only — the per-token rate `e` over the **total (in+out)**
token basis (see [`methodology.md`](methodology.md) §4). It excludes the amortized capex (that is
the break-even *fixed* term, not a marginal cost). For these estimated runs (**estimated — pending
M3b measurement**):

| Model | energy-only `e` |
|---|---|
| Gemma 4 31B Dense | ~$0.52 per million tokens |
| Gemma 4 12B Unified | ~$0.21 per million tokens |
| Gemma 4 26B A4B | ~$0.065 per million tokens |

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

So one 20k-token run whose **marginal energy cost** is roughly **$0.001–$0.010** locally
(energy-only, **estimated — pending M3b measurement**) would cost **$0.28–$0.46** at cloud list
price — the cloud is **~30–350×** the marginal energy cost per run. If marginal cost were the whole
story, local would win overwhelmingly. It is not: the amortized hardware capex (here **$0.004–$0.032
per run** — the energy-plus-capex per-run total is **$0.005–$0.042**) is what flips the economics,
which is the break-even story below.

## Break-even — where local actually wins

The crossover is the pure `costroid-core::breakeven` math (no `core → power` edge):

```
hw_fixed_per_day = hardware_price ÷ depreciation_period_days   # $2000 / 1095 ≈ $1.83/day
V*               = hw_fixed_per_day ÷ (c − e)                   # tokens/day to break even
```

where `c` is the blended cloud per-token rate and `e` is the energy-only local rate. For the
benchmarked 31B-dense run versus `claude-opus-4-8` (**estimated — pending M3b measurement**):

| Compared cloud model | break-even volume | sensitivity band |
|---|---|---|
| 31B Dense vs `claude-opus-4-8` | ~**81,237 tokens/day** | ~64,990 – 98,818 tokens/day |
| 31B Dense vs `claude-sonnet-4-6` | ~**137,502 tokens/day** | ~110,002 – 167,824 tokens/day |
| 26B A4B vs `claude-opus-4-8` | ~**79,636 tokens/day** | ~63,709 – 96,459 tokens/day |
| 12B Unified vs `claude-opus-4-8` | ~**80,132 tokens/day** | ~64,106 – 97,188 tokens/day |

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

Everything here is deterministic and offline. The versioned dataset lives in
[`benchmarks/gemma4-vs-cloud-2026-06/`](../benchmarks/gemma4-vs-cloud-2026-06/) (manifest + raw
`costroid bench` outputs + `.sha256` sidecars), guarded by `scripts/check_benchmarks.sh`.

```bash
# Determinism (D5): pin the bench-row timestamp to the manifest as_of (2026-06-20).
EPOCH=1781913600

# Per-model local economics (one FOCUS-1.3 local_inference row each):
SOURCE_DATE_EPOCH=$EPOCH cargo run -q -p costroid --features power -- \
  bench --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 --out json

# The local-vs-cloud break-even (cloud priced from the bundled catalog):
cargo run -q -p costroid --features power -- \
  breakeven --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 \
  --compare-to claude-opus-4-8 --plain

# The full one-command demo (import → bench → break-even → export FOCUS), offline:
make demo
```

The raw outputs regenerate byte-for-byte (the `SOURCE_DATE_EPOCH` pin); `check_benchmarks.sh`
verifies the committed `.sha256` sidecars. See the dataset
[`README.md`](../benchmarks/gemma4-vs-cloud-2026-06/README.md) for the exact regen loop.

## Caveats (read these)

- **Package power vs wall power.** The 155 W is calibrated to **system** draw. On an APU the iGPU
  shares a rail with the CPU, so no on-chip sensor isolates GPU-only watts — a wall meter (true
  total draw) is typically **~20–40% higher** than the on-chip package figure. The measured ladder
  *leads* with the wall meter for exactly this reason. See [`methodology.md`](methodology.md) §2.
- **Estimated, not measured.** Until the M3b on-hardware wall-meter run lands, every figure is
  **estimated — pending M3b measurement**; the throughput is a community analog, not a reading on
  this machine. [`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md) is the closed checklist of exactly
  which figures the real run replaces.
- **A range, never a hero number.** The break-even moves with electricity, hardware price, and the
  output mix; treat the band as the answer, not its midpoint.

## Cross-references

- The exact formulas (energy/token, `e` over total tokens, the break-even math, the `NEVER` /
  `INFEASIBLE` cases): [`methodology.md`](methodology.md).
- What Costroid cannot know (sub-agent attribution, package-vs-wall, the uncertain-row annotation):
  [`limitations.md`](limitations.md).
- The post-M3b figure refresh runbook: [`POST-M3B-REFRESH.md`](POST-M3B-REFRESH.md).
