<!-- costroid:comment -->
### Costroid

This run cost USD 0.1 at list (Measured). Cap: USD 5 (wrapper-watchdog: the wrapper stops the process at the cap).

**Verdict: success.** Run budget: USD 0.1 is within the USD 5 budget. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.1 | Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Verified: a ledger checked the rows the verdict read against the job's OIDC token.

<details>
<summary>Cap, budget, agents, models and catalog</summary>

Cap: USD 5 (wrapper-watchdog: the wrapper stops the process at the cap). Cost: USD 0.1, Measured. Under the cap by USD 4.9.

Run budget: USD 5, from `policy.run_budget_usd` in `costroid.yml`. The verdict checks this budget, never the cap.

Agents: `codex`. Models: `gpt-5.5`. 1 row, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>How USD 0.1 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `output` | `openai:gpt-5.5:global:standard:output` | USD 0.1 per 1,000,000 tokens | 1000000 | USD 0.1 |

</details>

[Costroid Platform (not open yet)](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.result/0.9",
  "check_run_id": 16873409921,
  "conclusion": "success",
  "state": "reported",
  "agent_error": null,
  "agent_skipped": null,
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "ci_run": null,
  "rows_sha256": null,
  "rows_file_sha256": null,
  "amounts": "shown",
  "rows": 1,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.1",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 1,
      "fallback_rows": 0,
      "tokens": {
        "output": "1000000"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": {
    "currency": "USD",
    "amount": "5",
    "enforcement_grade": "wrapper-watchdog",
    "cost": "0.1",
    "cost_basis": "measured",
    "remainder": "4.9",
    "overshoot": "0",
    "overshoot_tokens": "0"
  },
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
        "reason": "within_limit",
        "limit_usd": "5",
        "cost_usd": "0.1"
      }
    ],
    "joinable_rows": 0
  }
}
```

</details>
