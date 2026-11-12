# Title

Verdict: neutral

# Summary

This run cost USD 0.04974 at list (Measured). No cap held it.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.04974 | Measured | unattributed | none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid |

gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits.

Unverified: the agent's job wrote these figures.

<details>
<summary>Cap, agents, models and catalog</summary>

No cap held this run: its enforcement grade is none.

Agents: `gh-aw`. Models: `claude-haiku-5-5`, `claude-sonnet-5`. 15 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>How USD 0.04974 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `input_uncached` | `anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached` | USD 0.1 per 1,000,000 tokens | 310 | USD 0.000031 |
| `0.1.0` | `input_uncached` | `anthropic:claude-sonnet-5:global:standard:all:input_uncached` | USD 2 per 1,000,000 tokens | 2963 | USD 0.005926 |
| `0.1.0` | `cache_read` | `anthropic:claude-sonnet-5:global:standard:all:cache_read` | USD 0.2 per 1,000,000 tokens | 24610 | USD 0.004922 |
| `0.1.0` | `cache_write_5m` | `anthropic:claude-sonnet-5:global:standard:all:cache_write_5m` | USD 2.5 per 1,000,000 tokens | 12770 | USD 0.031925 |
| `0.1.0` | `output` | `anthropic:claude-haiku-5-5:global:standard:to-100000:output` | USD 0.5 per 1,000,000 tokens | 12 | USD 0.000006 |
| `0.1.0` | `output` | `anthropic:claude-sonnet-5:global:standard:all:output` | USD 10 per 1,000,000 tokens | 693 | USD 0.00693 |
| `0.1.0` | `reasoning_output` | `anthropic:informational:reasoning_output` | informational, not priced | 64 | USD 0 |

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
  "rows": 15,
  "run_identity": "unverified",
  "figures": [
    {
      "currency": "USD",
      "list_cost": "0.04974",
      "cost_basis": "measured",
      "attribution_grade": "unattributed",
      "enforcement_grade": "none",
      "rows": 15,
      "fallback_rows": 0,
      "tokens": {
        "cache_read": "24610",
        "cache_write_5m": "12770",
        "input_uncached": "3273",
        "output": "705",
        "reasoning_output": "64"
      }
    }
  ],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [
      "gh-aw"
    ],
    "models": [
      "claude-haiku-5-5",
      "claude-sonnet-5"
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
  "agent_stated": {
    "unit": "AIC",
    "quantity": "7.47",
    "source": "gh-aw"
  },
  "recorded": null
}
```
