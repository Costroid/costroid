<!-- costroid:comment -->
### Costroid

This run cost USD 0.0302309 at list (List-equivalent, not billed). Nothing held it to a budget.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.0302309; subscription: list-equivalent, no provider report to reconcile | List-equivalent, not billed | unattributed | none: nothing held the run to a budget |

Unverified: the agent's job wrote these figures.

No cap held this run: its enforcement grade is none.

Agents: `claude-code`. Models: `claude-haiku-5-5`, `claude-sonnet-5-5`. 12 rows, catalog `0.3.0`.

<details>
<summary>How USD 0.0302309 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.3.0` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.1 per 1,000,000 tokens | 1200 | USD 0.00012 |
| `0.3.0` | `input_uncached` | `anthropic:claude-sonnet-5-5:global:standard:all:input_uncached` | USD 2 per 1,000,000 tokens | 4 | USD 0.000008 |
| `0.3.0` | `cache_read` | `anthropic:claude-sonnet-5-5:global:standard:all:cache_read` | USD 0.1 per 1,000,000 tokens | 28899 | USD 0.0028899 |
| `0.3.0` | `cache_write_1h` | `anthropic:claude-sonnet-5-5:global:standard:all:cache_write_1h` | USD 4 per 1,000,000 tokens | 6341 | USD 0.025364 |
| `0.3.0` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.5 per 1,000,000 tokens | 18 | USD 0.000009 |
| `0.3.0` | `output` | `anthropic:claude-sonnet-5-5:global:standard:all:output` | USD 10 per 1,000,000 tokens | 184 | USD 0.00184 |
| `0.3.0` | `cached_input_reported` | `anthropic:informational:cached_input_reported` | informational, not priced | 28899 | USD 0 |

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

</details>
