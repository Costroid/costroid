<!-- costroid:comment -->
### Costroid

This run was priced at list (Measured). No cap held it.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| Measured | unattributed | none: no cap held the run |

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Agents, models and catalog</summary>

Agents: `gh-aw`. Models: `claude-haiku-5-5`, `claude-sonnet-5`. 15 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.1.0` | `input_uncached` |
| `0.1.0` | `cache_read` |
| `0.1.0` | `cache_write_5m` |
| `0.1.0` | `output` |
| `0.1.0` | `reasoning_output` |

Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.

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
  "amounts": "withheld",
  "rows": 15,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "none",
      "rows": 15,
      "fallback_rows": 0
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "gh-aw"
    ],
    "models": [
      "claude-haiku-5-5",
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
