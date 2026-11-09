# Title

Verdict: neutral

# Summary

This run cost USD 0.1 at list (Measured). Nothing held it to a budget.

**Verdict: neutral.** No rule ran, so the check concludes neutral: `costroid.yml` in this directory is not valid, and `costroid validate` names the problem.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.1 | Measured | unattributed | none: nothing held the run to a budget |

Unverified: the agent's job wrote these figures.

No cap held this run: its enforcement grade is none. Cap: USD 0.05, which nothing enforced. Cost: USD 0.1, Measured. Over the cap by USD 0.05 and 1000000 tokens.

Agents: `codex`. Models: `gpt-5.5`. 1 row, catalog `0.1.0`.

<details>
<summary>How USD 0.1 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `output` | `openai:gpt-5.5:global:standard:output` | USD 0.1 per 1,000,000 tokens | 1000000 | USD 0.1 |

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
  "rows": 1,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.1",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "none",
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
    "amount": "0.05",
    "enforcement_grade": "none",
    "cost": "0.1",
    "cost_basis": "measured",
    "remainder": "0",
    "overshoot": "0.05",
    "overshoot_tokens": "1000000"
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
    "conclusion": "neutral",
    "trust": "unverified",
    "pack_sha256": "0a36ca52cf9123f580e21c44a00d804f62558b6c0f96179917521684e70c3f30",
    "config_sha256": "0145a96a9c91ef16d48835586314389c456ac1ea4218c42475da8abf0e7f8c9c",
    "config": "invalid",
    "config_from_base": false,
    "evaluated_at": "2026-11-06T09:00:00Z",
    "warn_only": {
      "active": true,
      "reason": "start_unknown"
    },
    "results": [],
    "joinable_rows": 0
  }
}
```
