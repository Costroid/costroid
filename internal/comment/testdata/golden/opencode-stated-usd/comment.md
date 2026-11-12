<!-- costroid:comment -->
### Costroid

This run cost USD 0.00153317 at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.00153317 | Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

OpenCode stated USD 0.00153317 for this run by its own prices, which Costroid never adds to its own figures.

Unverified: the agent's job wrote these figures.

<details>
<summary>Cap, agents, models and catalog</summary>

Cap: held by wrapper-watchdog (the wrapper stops the process at the cap). The rows do not record its amount, and the report was not given it.

Agents: `opencode`. Models: `claude-haiku-5-5`. 7 rows, catalog `0.3.0`, prices read 2026-11-08.

</details>

<details>
<summary>How USD 0.00153317 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.3.0` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.1 per 1,000,000 tokens | 6 | USD 0.0000006 |
| `0.3.0` | `cache_read` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_read` | USD 0.01 per 1,000,000 tokens | 10532 | USD 0.00010532 |
| `0.3.0` | `cache_write_5m` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_5m` | USD 0.125 per 1,000,000 tokens | 10806 | USD 0.00135075 |
| `0.3.0` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.5 per 1,000,000 tokens | 153 | USD 0.0000765 |

</details>

[Your account on costroid.com](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.result/0.9",
  "check_run_id": 16873409921,
  "conclusion": "neutral",
  "state": "reported",
  "agent_error": null,
  "agent_skipped": null,
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "ci_run": null,
  "rows_sha256": null,
  "rows_file_sha256": null,
  "amounts": "shown",
  "rows": 7,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.00153317",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 7,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "10532",
        "cache_write_5m": "10806",
        "input_uncached": "6",
        "output": "153"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "opencode"
    ],
    "models": [
      "claude-haiku-5-5"
    ],
    "models_omitted": 0,
    "catalog_versions": [
      "0.3.0"
    ],
    "fallback_models": [],
    "fallback_models_omitted": 0,
    "fallback_price_ids": [],
    "fallback_price_ids_omitted": 0
  },
  "lifetime": null,
  "agent_stated": {
    "unit": "USD",
    "quantity": "0.00153317",
    "source": "opencode"
  },
  "recorded": null
}
```

</details>
