# Bundled local-model manifest — Gemma 4 family (M3, R10)

`gemma4.v1.json` is the dated, sha256-stamped manifest of the **Gemma 4 family (Apache-2.0)**
Costroid standardizes its local-inference benchmarks on (the M3 benchmark family — see
[`docs/ARCHITECTURE.md`](../../../docs/ARCHITECTURE.md) §10). It is
a **vendored data artifact**, compiled into `costroid-power` via `include_str!` — **never
fetched**. Costroid ships **no weights** (user-downloaded GGUF).

## The family (one permissive family, edge → flagship — all on the 128 GB APU)

| id | class | params (total / active) | ctx | what it answers |
|---|---|---|---|---|
| `gemma-4-26b-a4b` | fast MoE | 25.2B / 3.8B | 256K | the speed point (near-4B latency, higher reasoning) |
| `gemma-4-31b-dense` | dense flagship | 30.7B | 256K | the honest slow/expensive coding counterexample |
| `gemma-4-12b-unified` | compute-efficient | 12B | 256K | a mid floor point |
| `gemma-4-e2b` / `gemma-4-e4b` | edge | ≤4B | 128K | cheap floor points |

Default quant **`Q4_K_M`** (with `Q8_0` for the quality-sensitive variant). Every model ships a
speculative-decoding draft model (a throughput lever to **measure**, not assume).

## Honesty (R10)

- **Quality** (`quality.score`) is **PUBLISHED**, taken from the cited model card — **never
  re-derived here**. It is `null` in this manifest (a structural placeholder marked `"as
  published"`): the quality axis is wired into the Frontier at M4/M6 from the cited source; M3
  needs only the specs + the tok/s estimate. Where no coding-specific score is published, it
  stays `null` (`as published / n/a`), never guessed.
- **`estimated_tok_s`** is an **ESTIMATE** (`tok_s_estimated: true`), from the community §5.2
  analogs (the 26B-A4B MoE ≈ the Qwen3-30B-A3B ~96–100 tok/s class; the dense 31B is
  bandwidth-bound and slow). The harness produces the **real** throughput on real hardware at
  **M3b** — this manifest never claims a measured number.

## Provenance

| Field | Value |
|---|---|
| Source | Gemma 4 model card (`ai.google.dev/gemma/docs/core/model_card_4`) + Google's 2026 launch |
| License | **Apache-2.0** (verified against the model card) — on Costroid's permissive allowlist |
| `as_of` | 2026-06-20 |
| Integrity | `gemma4.v1.json.sha256` (fail-closed `sha256sum -c` in CI via `scripts/check_power_profiles.sh`) |

## Refresh

To revise (e.g. fill a published quality score once cited, or refresh a tok/s estimate after
M3b), edit the JSON, bump `as_of`, and regenerate the sidecar:

```bash
cd crates/costroid-power/models && sha256sum gemma4.v1.json > gemma4.v1.json.sha256
```

After editing a bundled JSON, a local re-verify needs `cargo clean -p costroid-power` (the
`include_str!` warm-cache hazard).
