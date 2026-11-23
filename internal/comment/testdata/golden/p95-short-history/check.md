# Title

Not decided: the P95 limit needs 20 earlier runs of this agent and 7 reached the check, so the rule did not run. This run cost USD 0.001619725 at list (List-equivalent, not billed).

# Summary

**Not decided: the P95 limit needs 20 earlier runs of this agent and 7 reached the check, so the rule did not run. This run cost USD 0.001619725 at list (List-equivalent, not billed).**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Enforcement |
| --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | client-side estimate, can pass the cap |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>

<details>
<summary>How USD 0.001619725 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.4.3` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.10 per million tokens | 8 | USD 0.0000008 |
| `0.4.3` | `cache_read` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_read` | USD 0.01 per million tokens | 15605 | USD 0.00015605 |
| `0.4.3` | `cache_write_5m` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_5m` | USD 0.125 per million tokens | 3495 | USD 0.000436875 |
| `0.4.3` | `cache_write_1h` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_1h` | USD 0.20 per million tokens | 4450 | USD 0.00089 |
| `0.4.3` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.50 per million tokens | 272 | USD 0.000136 |
| `0.4.3` | `reasoning_output` | `anthropic:informational:reasoning_output` | informational, not priced | 121 | No amount: not priced |
| `0.4.3` | `cached_input_reported` | `anthropic:informational:cached_input_reported` | informational, not priced | 15605 | No amount: not priced |

</details>

# Text

```json
{
  "schema": "costroid.result/0.11",
  "conclusion": "neutral",
  "state": "reported",
  "agent_error": null,
  "agent_skipped": null,
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "ci_run": null,
  "rows_sha256": null,
  "rows_file_sha256": null,
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
    "config_sha256": "93da16fb4de5c1328a05c5620d4b4bc9d9041844d54a07ab225852aa849447bb",
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
        "rule": "run_p95",
        "outcome": "unproven",
        "reason": "no_baseline",
        "limit_factor": "1.5",
        "baseline_runs": 7
      }
    ],
    "joinable_rows": 0
  }
}
```
