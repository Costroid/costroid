# Title

No budget set: this run cost EUR 0.10 at list (Measured) and USD 0.10 at list (Measured). No rule decides this check.

# Summary

**No budget set: this run cost EUR 0.10 at list (Measured) and USD 0.10 at list (Measured). No rule decides this check.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Enforcement |
| --- | --- | --- |
| EUR 0.10 | Measured | the wrapper stops the process at the cap |
| USD 0.10 | Measured | the wrapper stops the process at the cap |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (the wrapper stops the process at the cap). Cost: USD 0.10, Measured. Under the cap by USD 4.90.

No policy ran on this change.

Agents: `codex`. Models: `gpt-5.5`. 2 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>How EUR 0.10 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `output` | `openai:gpt-5.5:global:standard:output` | EUR 0.10 per million tokens | 1000000 | EUR 0.10 |

</details>

<details>
<summary>How USD 0.10 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `output` | `openai:gpt-5.5:global:standard:output` | USD 0.10 per million tokens | 1000000 | USD 0.10 |

</details>

# Text

```json
{
  "schema": "costroid.result/0.9",
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
      "currency": "EUR",
      "list_cost": "0.1",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 1,
      "fallback_rows": 0,
      "tokens": {
        "output": "1000000"
      }
    },
    {
      "currency": "USD",
      "list_cost": "0.1",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 1,
      "fallback_rows": 0,
      "tokens": {
        "output": "1000000"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": {
    "currency": "USD",
    "amount": "5",
    "enforcement_grade": "wrapper-watchdog",
    "cost": "0.1",
    "cost_basis": "measured",
    "remainder": "4.9",
    "overshoot": "0",
    "overshoot_tokens": "0"
  },
  "data": {
    "agents": [
      "codex"
    ],
    "models": [
      "gpt-5.5"
    ],
    "models_omitted": 0,
    "catalog_versions": [
      "0.1.0"
    ],
    "fallback_models": [],
    "fallback_models_omitted": 0,
    "fallback_price_ids": [],
    "fallback_price_ids_omitted": 0
  },
  "lifetime": null,
  "agent_stated": null,
  "recorded": null
}
```
