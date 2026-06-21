# `samples/` — curated demo datasets (synthetic, never real user data)

This tree holds the **curated, discoverable demo datasets** the README quickstart, the
`make demo` path, and the docs read. It is **distinct from `fixtures/`** (the CI test
corpus) but follows the **same synthetic discipline**: every value is hand-authored or
deterministically generated — **NO real prompts, responses, billing data, account
identity, credentials, or model weights** (Cardinal Rule R4: only bounded metadata in any
committed row, never prompt/response content).

Costroid's whole point is the **unified three-lane FOCUS ledger**, so the samples come in three
packs — one per lane:

| Pack | Lane | What it is | Discipline |
|---|---|---|---|
| [`local-logs/`](local-logs/) | `developer_tool` | Synthetic Claude Code + Codex on-disk logs (the "local-usage ledger" Costroid reads by default). | SYNTHETIC + ESTIMATED (cost = tokens × current prices). |
| [`cloud-focus/`](cloud-focus/) | `cloud_api` | A synthetic AWS Bedrock FOCUS v1.2 export for `costroid import`. | SYNTHETIC, source-priced. |
| [`benchmark/`](benchmark/) | `local_inference` | Deterministic `costroid bench` outputs keyed to the Gemma 4 manifest. | SYNTHETIC + **ESTIMATED — pending M3b measurement** (every row `x_MeasurementMode == "estimated"`). |

Each pack carries its own `README.md` with the exact regeneration command and the pinned
numbers. See those for details.

## The three lanes in one ledger

```bash
# (a) developer_tool — synthetic dev-tool logs
CLAUDE_CONFIG_DIR=samples/local-logs/claude CODEX_HOME=samples/local-logs/codex \
  costroid export --format csv                              # 14 rows, 1.865000 USD

# (b) cloud_api — synthetic AWS Bedrock FOCUS v1.2 export
costroid import --format focus-csv --out csv \
  samples/cloud-focus/aws-focus-v12.csv                     # 4 rows, 9.6000 USD

# (c) local_inference — deterministic Gemma 4 bench (estimated mode, no hardware)
SOURCE_DATE_EPOCH=1781913600 costroid bench \
  --model gemma-4-31b-dense --tokens-in 2000 --tokens-out 18000 --out csv  # 1 row
```

All three are offline by construction — nothing leaves the machine.

## Honesty stamps (R8/R10)

- The benchmark pack is **estimated, not measured** — every figure is stamped *"estimated —
  pending M3b measurement"* and every committed `local_inference` row asserts
  `x_MeasurementMode == "estimated"`. The real captured joules/token fill those placeholders in a
  documented post-M3b refresh (`docs/POST-M3B-REFRESH.md`).
- The cloud-API pack is **source-priced** (the FOCUS export carries an authoritative `BilledCost`).
- The developer-tool pack is **estimated** (your tokens × the bundled current prices, never the
  provider's authoritative bill — design-for-reconciliation).

## Validation

A dedicated `samples/` leg in [`scripts/focus_conformance.sh`](../scripts/focus_conformance.sh)
exports each pack to a FOCUS 1.3 ledger and runs the vendored validator (offline), with a
**row-count guard** per lane (so an empty/short export can't pass vacuously) and the merged
three-lane union (20 rows). The Rust integration test `apps/cli/tests/samples_datasets.rs`
loads each pack, pins its row counts/token totals, asserts the inverse measurement-mode guard,
and round-trips every lane through `export_focus_csv`/`export_focus_json`.
