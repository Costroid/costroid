<!-- costroid:comment -->
### Costroid

This run cost USD 0.0283136 at list (Measured). Cap: USD 0.001 (wrapper-watchdog: the wrapper stops the process at the cap). Over the cap by USD 0.0273136 and 16960 tokens.

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

| Cost | Basis | Attribution | Enforcement |
| --- | --- | --- | --- |
| USD 0.0283136 | Measured | unattributed | wrapper-watchdog: the wrapper stops the process at the cap |

Unverified: the agent's job wrote these figures.

<details>
<summary>Cap, agents, models and catalog</summary>

Cap: USD 0.001 (wrapper-watchdog: the wrapper stops the process at the cap). Cost: USD 0.0283136, Measured. Over the cap by USD 0.0273136 and 16960 tokens.

Agents: `codex`. Models: `gpt-5.6-sol`. 4 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>

<details>
<summary>How USD 0.0283136 was computed</summary>

| Catalog | Token class | Price id | Unit price | Tokens | Amount |
| --- | --- | --- | --- | --- | --- |
| `0.1.0` | `input_uncached` | `openai:gpt-5.6-sol:global:standard:to-272000:input_uncached` | USD 4 per 1,000,000 tokens | 5632 | USD 0.022528 |
| `0.1.0` | `cache_read` | `openai:gpt-5.6-sol:global:standard:to-272000:cache_read` | USD 0.4 per 1,000,000 tokens | 11264 | USD 0.0045056 |
| `0.1.0` | `output` | `openai:gpt-5.6-sol:global:standard:to-272000:output` | USD 20 per 1,000,000 tokens | 64 | USD 0.00128 |
| `0.1.0` | `cached_input_reported` | `openai/gpt-5.6-sol/cached_input_reported/informational` | informational, not priced | 11264 | USD 0 |

</details>

[Costroid Platform (not open yet)](https://costroid.com/account/) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, the check run was not posted</summary>

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

</details>
