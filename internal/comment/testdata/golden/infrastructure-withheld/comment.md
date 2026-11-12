<!-- costroid:comment -->
### Costroid

This run was priced at list (List-equivalent, not billed). Held by agent-flag: client-side estimate, can pass the cap.

**Verdict: neutral.** Run budget: unverified rows cannot satisfy a budget. The run is within the budget. The verdict is neutral because the agent's own job wrote the rows. Over budget, the rule would warn until the warn-only period ends, then fail. [The policy page](https://docs.costroid.com/policy) says what lets a budget pass. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | unattributed | agent-flag: client-side estimate, can pass the cap |

Infrastructure, estimated, in EUR for 2026-12, from `infrastructure`.

Infrastructure, estimated, in USD for 2026-12, from `infrastructure`.

The agent figure and these infrastructure figures are not added together, and no currency is added to another. The rows carry their amounts, and the machine block carries none of them.

<details>
<summary>Infrastructure rows</summary>

| Resource | Service | Period |
| --- | --- | --- |
| `aws_instance.web` | `Amazon EC2` | 2026-12 |
| `google_compute_instance.batch` | `Compute Engine` | 2026-12 |

</details>

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Agents, models and catalog</summary>

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.1`, prices read 2026-11-12.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.4.1` | `input_uncached` |
| `0.4.1` | `cache_read` |
| `0.4.1` | `cache_write_5m` |
| `0.4.1` | `cache_write_1h` |
| `0.4.1` | `output` |
| `0.4.1` | `reasoning_output` |
| `0.4.1` | `cached_input_reported` |

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
      "0.4.1"
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
