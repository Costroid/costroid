<!-- costroid:comment -->
**Within budget, by the job's own figures: this run cost USD 0.0391529 at list (Measured) of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Enforcement |
| --- | --- | --- |
| USD 0.0391529 | Measured | no cap held the run |

<details>
<summary>Cap, budget, policy, agents, models and catalog</summary>

No cap held this run.

Run budget: USD 5.00, from `policy.run_budget_usd` in `costroid.yml`.

Over budget, the rule would warn until the warn-only period ends, then fail. [The policy page](https://docs.costroid.com/policy) says what lets a budget pass. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`, `claude-opus-5-5`. 12 rows, catalog `0.4.3`, prices read 2026-11-21.

</details>

<details>
<summary>How USD 0.0391529 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.4.3` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.10 per million tokens | 1200 | USD 0.00012 |
| `0.4.3` | `input_uncached` | `anthropic:claude-opus-5-5:global:standard:all:input_uncached` | USD 4.00 per million tokens | 4 | USD 0.000016 |
| `0.4.3` | `cache_read` | `anthropic:claude-opus-5-5:global:standard:all:cache_read` | USD 0.20 per million tokens | 28767 | USD 0.0057534 |
| `0.4.3` | `cache_write_5m` | `anthropic:claude-opus-5-5:global:standard:all:cache_write_5m` | USD 5.00 per million tokens | 5959 | USD 0.029795 |
| `0.4.3` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.50 per million tokens | 17 | USD 0.0000085 |
| `0.4.3` | `output` | `anthropic:claude-opus-5-5:global:standard:all:output` | USD 20.00 per million tokens | 173 | USD 0.00346 |
| `0.4.3` | `cached_input_reported` | `anthropic:informational:cached_input_reported` | informational, not priced | 28767 | No amount: not priced |

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
  "amounts": "shown",
  "rows": 12,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.0391529",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "none",
      "rows": 12,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "28767",
        "cache_write_5m": "5959",
        "cached_input_reported": "28767",
        "input_uncached": "1204",
        "output": "190"
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
      "claude-opus-5-5"
    ],
    "models_omitted": 0,
    "catalog_versions": [
      "0.4.3"
    ],
    "fallback_models": [],
    "fallback_models_omitted": 0,
    "fallback_price_ids": [],
    "fallback_price_ids_omitted": 0
  },
  "lifetime": null,
  "agent_stated": null,
  "recorded": null,
  "policy": {
    "conclusion": "neutral",
    "trust": "unverified",
    "pack_sha256": "3bf380556f54cfd4284e2c843f43cfac4cf4fdfca7e8a5ea148fe54bd1e0d3d6",
    "config_sha256": "d3cbc6bf0befd44c89f0089e36ad6998cc03013cd13de8e31bf49846be83d364",
    "config_ref": "1111111111111111111111111111111111111111",
    "config": "found",
    "config_from_base": true,
    "evaluated_at": "2026-11-07T11:40:00Z",
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
        "cost_usd": "0.0391529"
      }
    ],
    "joinable_rows": 0
  }
}
```

</details>
