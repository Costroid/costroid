<!-- costroid:comment -->
**Within budget, by the job's own figures: this run is within its run budget, priced at list (List-equivalent, not billed). Nothing outside the job checked the figures, so the check is neutral. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Enforcement |
| --- | --- |
| List-equivalent, not billed | client-side estimate, can pass the cap |

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

Last reconciliation of `apikey_01Example`: 2026-11-05, tokens matched the provider's report within 1%.

Last reconciliation of `apikey_02Example`: 2026-11-05, a token class missed the provider's report by more than 1%.

Last reconciliation of `apikey_03Example`: 2026-11-05, the provider had not finished reporting the day.

<details>
<summary>Policy, agents, models and catalog</summary>

Over budget, the rule would warn until the warn-only period ends, then fail. [The policy page](https://docs.costroid.com/policy) says what lets a budget pass. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

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
  "rows": 19,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "cost_basis": "list_equivalent",
      "attribution_grade": "unattributed",
      "enforcement_grade": "agent-flag",
      "rows": 19,
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
        "reason": "unverified_rows"
      }
    ],
    "joinable_rows": 0
  }
}
```

</details>
