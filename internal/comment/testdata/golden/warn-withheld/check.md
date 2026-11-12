# Title

Verdict: neutral

# Summary

This run was priced at list (List-equivalent, not billed). Held by agent-flag: client-side estimate, can pass the cap.

**Verdict: neutral.** Run budget: the run is over the budget. Warn-only until 2026-11-09 09:00 UTC: for 7 days from the commit that added this workflow to the base branch, a rule that fails warns. Add `enforce: true` under `policy` in `costroid.yml` to enforce now. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | unattributed | agent-flag: client-side estimate, can pass the cap |

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Agents, models and catalog</summary>

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.3.0`, prices read 2026-11-08.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.3.0` | `input_uncached` |
| `0.3.0` | `cache_read` |
| `0.3.0` | `cache_write_5m` |
| `0.3.0` | `cache_write_1h` |
| `0.3.0` | `output` |
| `0.3.0` | `reasoning_output` |
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
      "0.3.0"
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
    "pack_sha256": "0a36ca52cf9123f580e21c44a00d804f62558b6c0f96179917521684e70c3f30",
    "config_sha256": "64c800be4f61bd3360ff4793d728b552525b17840e98d7aafc4e4bf7d81a3e36",
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
        "outcome": "warn",
        "reason": "over_limit",
        "held_by_warn_only": true,
        "limit_usd": "0.001",
        "cost_usd": "0.001619725"
      }
    ],
    "joinable_rows": 0
  }
}
```
