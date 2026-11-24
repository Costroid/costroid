# Title

Model not on the list: this run used a model that is not on the list, priced at list (List-equivalent, not billed). Amounts are withheld here.

# Summary

**Model not on the list: this run used a model that is not on the list, priced at list (List-equivalent, not billed). Amounts are withheld here.**

Pull request budget demoted to a warning: by the ledger's own record, reviewers from at least 2 accounts approved 7 of the last 20 pull requests it failed, more than one in five. It warns until 2026-12-12 09:00 UTC, then fails again. Add `auto_demote: false` under `policy` in `costroid.yml` to keep every rule as written.

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

Pull request budget: this pull request's runs are over the budget. The total adds every basis together, at list. Model allowlist: the run used `claude-haiku-5-5`, which is not on the list.

[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Enforcement |
| --- | --- |
| List-equivalent, not billed | client-side estimate, can pass the cap |

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Policy, agents, models and catalog</summary>

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.4.3` | `input_uncached` |
| `0.4.3` | `cache_read` |
| `0.4.3` | `cache_write_5m` |
| `0.4.3` | `cache_write_1h` |
| `0.4.3` | `output` |
| `0.4.3` | `reasoning_output` |
| `0.4.3` | `cached_input_reported` |

Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.

</details>

# Text

```json
{
  "schema": "costroid.result/0.14",
  "conclusion": "failure",
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
    "conclusion": "failure",
    "trust": "unverified",
    "pack_sha256": "3bf380556f54cfd4284e2c843f43cfac4cf4fdfca7e8a5ea148fe54bd1e0d3d6",
    "config_sha256": "455c8fca0cf999ccd144a08a5291fed3f44d4fbfa666cc9b7c3cbe509b32bfc7",
    "config_ref": "1111111111111111111111111111111111111111",
    "config": "found",
    "config_from_base": true,
    "evaluated_at": "2026-11-12T09:00:00Z",
    "warn_only": {
      "active": false,
      "reason": "enforced"
    },
    "results": [
      {
        "rule": "pr_budget",
        "outcome": "warn",
        "reason": "over_limit",
        "limit_usd": "0.001",
        "cost_usd": "0.001619725",
        "demoted": {
          "reason": "override_rate",
          "approved": 7,
          "failing": 20,
          "until": "2026-12-12T09:00:00Z"
        }
      },
      {
        "rule": "model_allowlist",
        "outcome": "fail",
        "reason": "model_outside_allowlist",
        "models": [
          "claude-haiku-5-5"
        ]
      }
    ],
    "joinable_rows": 0
  }
}
```
