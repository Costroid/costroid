<!-- costroid:comment -->
### Costroid

**Verdict: neutral.** Run budget: unverified rows cannot satisfy a budget. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base commit's file.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | unattributed | agent-flag: client-side estimate, can pass the cap |

Unverified: the agent's job wrote these figures.

Cap: USD 5 (agent-flag: client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

Run budget: USD 5, from `policy.run_budget_usd` in `costroid.yml`. The verdict checks this budget, never the cap.

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
  "schema": "costroid.result/0.4",
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
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "15605",
        "cache_write_1h": "4450",
        "cache_write_5m": "3495",
        "cached_input_reported": "15605",
        "input_uncached": "8",
        "output": "272",
        "reasoning_output": "121"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": {
    "currency": "USD",
    "amount": "5",
    "enforcement_grade": "agent-flag",
    "cost": "0.001619725",
    "cost_basis": "list_equivalent",
    "remainder": "4.998380275",
    "overshoot": "0",
    "overshoot_tokens": "0"
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
  },
  "policy": {
    "conclusion": "neutral",
    "trust": "unverified",
    "pack_sha256": "749c060beeef1e1d37f7412dcaa364b648738880dc3a72b6953cfc251817c1a9",
    "config_sha256": "d3cbc6bf0befd44c89f0089e36ad6998cc03013cd13de8e31bf49846be83d364",
    "config_ref": "1111111111111111111111111111111111111111",
    "config": "found",
    "config_from_base": true,
    "evaluated_at": "2026-11-06T09:00:00Z",
    "warn_only": {
      "active": true,
      "reason": "in_period",
      "started": "2026-11-02T09:00:00Z",
      "ends": "2026-11-09T09:00:00Z"
    },
    "results": [
      {
        "rule": "run_budget",
        "outcome": "unproven",
        "reason": "unverified_rows",
        "limit_usd": "5",
        "cost_usd": "0.001619725"
      }
    ],
    "joinable_rows": 0
  }
}
```

</details>
