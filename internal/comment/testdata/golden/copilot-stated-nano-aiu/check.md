# Title

Verdict: neutral

# Summary

This run cost USD 0.00329423 at list (Measured). Held by wrapper-watchdog: the wrapper stops the process at the cap.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.00329423 | Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Copilot CLI stated 0 nano-AI units for this run, which Costroid never converts to dollars.

Unverified: the agent's job wrote these figures.

<details>
<summary>Cap, agents, models and catalog</summary>

Cap: held by wrapper-watchdog (the wrapper stops the process at the cap). The rows do not record its amount, and the report was not given it.

Agents: `copilot`. Models: `claude-haiku-5-5`. 13 rows, catalog `0.3.0`, prices read 2026-11-08.

</details>

<details>
<summary>How USD 0.00329423 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.3.0` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.1 per 1,000,000 tokens | 8 | USD 0.0000008 |
| `0.3.0` | `cache_read` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_read` | USD 0.01 per 1,000,000 tokens | 43418 | USD 0.00043418 |
| `0.3.0` | `cache_write_5m` | `anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_5m` | USD 0.125 per 1,000,000 tokens | 21958 | USD 0.00274475 |
| `0.3.0` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.5 per 1,000,000 tokens | 229 | USD 0.0001145 |
| `0.3.0` | `cached_input_reported` | `anthropic:informational:cached_input_reported` | informational, not priced | 43418 | USD 0 |

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
  "rows": 13,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.00329423",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "wrapper-watchdog",
      "rows": 13,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "43418",
        "cache_write_5m": "21958",
        "cached_input_reported": "43418",
        "input_uncached": "8",
        "output": "229"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "copilot"
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
  "agent_stated": {
    "unit": "nano-AIU",
    "quantity": "0",
    "source": "copilot"
  },
  "recorded": null
}
```
