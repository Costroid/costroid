<!-- costroid:comment -->
**Pack rule failed (no_opus_on_docs): this run was priced at list (List-equivalent, not billed). The rule is this repository's own, read from the base branch. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

The policy pack `.costroid/policy.rego` returned:

- `no_opus_on_docs` failed with `model_on_docs_change`

[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check.

### Costroid

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Enforcement |
| --- | --- |
| List-equivalent, not billed | client-side estimate, can pass the cap |

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Policy, agents, models and catalog</summary>

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 8 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report.

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
  "schema": "costroid.result/0.12",
  "check_run_id": 16873409921,
  "conclusion": "failure",
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
    "conclusion": "failure",
    "trust": "unverified",
    "pack_sha256": "3bf380556f54cfd4284e2c843f43cfac4cf4fdfca7e8a5ea148fe54bd1e0d3d6",
    "config_sha256": "30d1ab92ba61eafe42aef585f6b19a9289e859d4f3fe4208f9d249a94614d8e3",
    "config_ref": "1111111111111111111111111111111111111111",
    "config": "found",
    "config_from_base": true,
    "evaluated_at": "2026-11-12T09:00:00Z",
    "warn_only": {
      "active": false,
      "reason": "ended",
      "started": "2026-11-02T09:00:00Z",
      "ends": "2026-11-09T09:00:00Z"
    },
    "results": [],
    "pack": {
      "path": ".costroid/policy.rego",
      "sha256": "835ba38d86cdb606f7dce50cd19b4a1ce77427bd8dcdcf535143d26b2f96e1e1",
      "results": [
        {
          "rule": "no_opus_on_docs",
          "outcome": "fail",
          "reason": "model_on_docs_change"
        }
      ]
    },
    "joinable_rows": 0
  }
}
```

</details>
