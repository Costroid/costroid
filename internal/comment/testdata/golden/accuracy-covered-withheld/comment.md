<!-- costroid:comment -->
**No budget set: this run was priced at list (Measured). No rule decides this check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Enforcement |
| --- | --- |
| Measured | the wrapper stops the process at the cap |

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Policy, agents, models and catalog</summary>

No policy ran on this change.

Agents: `claude-code`. Models: `claude-sonnet-5`. 1 row, catalog `0.1.0`, prices read 2026-11-01.

Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 7 of the 13 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.1.0` | `output` |

Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.

</details>

[Costroid Platform (not open yet)](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

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
  "amounts": "withheld",
  "rows": 1,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 1,
      "fallback_rows": 0
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "claude-code"
    ],
    "models": [
      "claude-sonnet-5"
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

</details>
