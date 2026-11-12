# Title

Verdict: neutral

# Summary

This run was priced at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Basis | Attribution | Enforcement |
| --- | --- | --- |
| Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Unverified: the agent's job wrote these figures.

Amounts are off on this public repository (`comment.show_cost: false`). The rows and the check run's machine block carry them.

<details>
<summary>Agents, models and catalog</summary>

Agents: `codex`. Models: `gpt-5.6-sol`. 4 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>Token classes behind line 1</summary>

| Catalog | Token class |
| --- | --- |
| `0.1.0` | `input_uncached` |
| `0.1.0` | `cache_read` |
| `0.1.0` | `output` |
| `0.1.0` | `cached_input_reported` |

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
  "rows": 4,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.0283136",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 4,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "11264",
        "cached_input_reported": "11264",
        "input_uncached": "5632",
        "output": "64"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": {
    "currency": "USD",
    "amount": "0.001",
    "enforcement_grade": "wrapper-watchdog",
    "cost": "0.0283136",
    "cost_basis": "measured",
    "remainder": "0",
    "overshoot": "0.0273136",
    "overshoot_tokens": "16960"
  },
  "data": {
    "agents": [
      "codex"
    ],
    "models": [
      "gpt-5.6-sol"
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
  "recorded": null
}
```
