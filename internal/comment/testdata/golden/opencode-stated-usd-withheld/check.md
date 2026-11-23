# Title

No budget set: this run was priced at list (Measured). No rule decides this check. Amounts are withheld here.

# Summary

**No budget set: this run was priced at list (Measured). No rule decides this check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Enforcement |
| --- | --- |
| Measured | the wrapper stops the process at the cap |

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Policy, agents, models and catalog</summary>

No policy ran on this change.

Agents: `opencode`. Models: `claude-haiku-5-5`. 7 rows, catalog `0.3.0`, prices read 2026-11-08.

Costroid's own test, not this run: `opencode` 1.19.4 on Anthropic passed 1 of the 1 day it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.3.0` | `input_uncached` |
| `0.3.0` | `cache_read` |
| `0.3.0` | `cache_write_5m` |
| `0.3.0` | `output` |

Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.

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
