# Title

Verdict: neutral

# Summary

This run was priced at list (List-equivalent, not billed). Nothing held it to a budget.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed; subscription: list-equivalent, no provider report to reconcile | unattributed | none: nothing held the run to a budget |

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

Agents: `claude-code`. Models: `claude-haiku-5-5`, `claude-sonnet-5-5`. 12 rows, catalog `0.3.0`.

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.3.0` | `input_uncached` |
| `0.3.0` | `cache_read` |
| `0.3.0` | `cache_write_1h` |
| `0.3.0` | `output` |
| `0.3.0` | `cached_input_reported` |

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
  "rows": 12,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.0302309",
      "cost_basis": "list_equivalent",
      "attribution_grade": "unattributed",
      "enforcement_grade": "none",
      "rows": 12,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "28899",
        "cache_write_1h": "6341",
        "cached_input_reported": "28899",
        "input_uncached": "1204",
        "output": "202"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "claude-code"
    ],
    "models": [
      "claude-haiku-5-5",
      "claude-sonnet-5-5"
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
  "agent_stated": null,
  "recorded": null
}
```
