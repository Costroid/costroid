<!-- costroid:comment -->
### Costroid

This run was priced at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.

**Verdict: success.** Run budget: the run is within the budget. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

Agents: `codex`. Models: `gpt-5.5`. 1 row, catalog `0.1.0`.

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.1.0` | `output` |

Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts.

</details>

[Your account on costroid.com](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.result/0.8",
  "check_run_id": 16873409921,
  "conclusion": "success",
  "state": "reported",
  "agent_error": null,
  "agent_skipped": null,
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "ci_run": null,
  "rows_sha256": null,
  "rows_file_sha256": null,
  "amounts": "withheld",
  "rows": 1,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 1,
      "fallback_rows": 0
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "codex"
    ],
    "models": [
      "gpt-5.5"
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
  "lifetime": null,
  "agent_stated": null,
  "recorded": null,
  "policy": {
    "conclusion": "success",
    "trust": "token_verified",
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
        "outcome": "pass",
        "reason": "within_limit"
      }
    ],
    "joinable_rows": 0
  }
}
```

</details>
