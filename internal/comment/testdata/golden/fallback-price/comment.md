<!-- costroid:comment -->
### Costroid

This run cost USD 180.1 at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 180.1, fallback price | Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Fallback price: `gpt-9-preview` is not in catalog `0.1.0` for the tier and token class used, so `openai:fallback:output` priced it.

Unverified: the agent's job wrote these figures.

Cap: USD 5 (wrapper-watchdog: the wrapper stops the process at the cap). Cost: USD 180.1, Measured. Over the cap by USD 175.1 and 1000000 tokens.

Agents: `codex`. Models: `gpt-5.5`, `gpt-9-preview`. 2 rows, catalog `0.1.0`.

<details>
<summary>How USD 180.1 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `output` | `openai:fallback:output` | USD 180 per 1,000,000 tokens | 1000000 | USD 180 |
| `0.1.0` | `output` | `openai:gpt-5.5:global:standard:output` | USD 0.1 per 1,000,000 tokens | 1000000 | USD 0.1 |

</details>

[Your account on costroid.com](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.result/0.7",
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
  "rows": 2,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "180.1",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 2,
      "fallback_rows": 1,
      "tokens": {
        "output": "2000000"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": {
    "currency": "USD",
    "amount": "5",
    "enforcement_grade": "wrapper-watchdog",
    "cost": "180.1",
    "cost_basis": "measured",
    "remainder": "0",
    "overshoot": "175.1",
    "overshoot_tokens": "1000000"
  },
  "data": {
    "agents": [
      "codex"
    ],
    "models": [
      "gpt-5.5",
      "gpt-9-preview"
    ],
    "models_omitted": 0,
    "catalog_versions": [
      "0.1.0"
    ],
    "fallback_models": [
      "gpt-9-preview"
    ],
    "fallback_models_omitted": 0,
    "fallback_price_ids": [
      "openai:fallback:output"
    ],
    "fallback_price_ids_omitted": 0
  },
  "lifetime": null,
  "agent_stated": null
}
```

</details>
