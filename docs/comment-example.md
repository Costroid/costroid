<!-- costroid:comment -->
### Costroid

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | unattributed | agent-flag: client-side estimate, can pass the cap |

Unverified: the agent's job wrote these figures.

Budget: USD 5.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.1.0`.

<details>
<summary>How USD 0.001619725 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.1 per 1000000 Tokens | 8 | USD 0.0000008 |
| `0.1.0` | `cache_read` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_read` | USD 0.01 per 1000000 Tokens | 15605 | USD 0.00015605 |
| `0.1.0` | `cache_write_5m` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_5m` | USD 0.125 per 1000000 Tokens | 3495 | USD 0.000436875 |
| `0.1.0` | `cache_write_1h` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_1h` | USD 0.2 per 1000000 Tokens | 4450 | USD 0.00089 |
| `0.1.0` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.5 per 1000000 Tokens | 272 | USD 0.000136 |
| `0.1.0` | `reasoning_output` | `anthropic/claude-haiku-5-5/reasoning_output/informational` | informational, not priced | 121 | USD 0 |
| `0.1.0` | `cached_input_reported` | `anthropic/claude-haiku-5-5/cached_input_reported/informational` | informational, not priced | 15605 | USD 0 |

</details>

[Your account on costroid.com](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.check/0.1",
  "check_run_id": 16873409921,
  "conclusion": "neutral",
  "state": "reported",
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "amounts": "shown",
  "rows": 19,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.001619725",
      "cost_basis": "list_equivalent",
      "attribution_grade": "unattributed",
      "enforcement_grade": "agent-flag",
      "rows": 19,
      "fallback_rows": 0
    }
  ],
  "figures_omitted": 0,
  "budget": {
    "currency": "USD",
    "amount": "5"
  },
  "data": {
    "agents": [
      "claude-code"
    ],
    "models": [
      "claude-haiku-5-5"
    ],
    "models_omitted": 0,
    "catalog_versions": [
      "0.1.0"
    ],
    "fallback_models": [],
    "fallback_models_omitted": 0,
    "fallback_price_ids": [],
    "fallback_price_ids_omitted": 0
  }
}
```

</details>
