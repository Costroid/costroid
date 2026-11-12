<!-- costroid:comment -->
### Costroid

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

This report shows no figures. Reason `agent_error`: the agent ended in an error before it reported usage, so the run wrote no rows. It ended with `authentication_failed`: check the API key the agent runs with, the api-key input in the action.

[Costroid Platform (not open yet)](https://app.costroid.com/?installation=github%3A58120394) · [Export to your FinOps platform or warehouse](https://docs.costroid.com/export)

<details>
<summary>Machine block, copied from check run 16873409921</summary>

```json
{
  "schema": "costroid.result/0.9",
  "check_run_id": 16873409921,
  "conclusion": "neutral",
  "state": "no_rows",
  "reason": "empty_rows_file",
  "run_reason": "agent_error",
  "agent_error": {
    "class": "authentication_failed"
  },
  "agent_skipped": null,
  "head_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
  "ci_run": null,
  "rows_sha256": null,
  "rows_file_sha256": null,
  "amounts": "shown",
  "rows": 0,
  "run_identity": "unverified",
  "figures": [],
  "figures_omitted": 0,
  "cap": null,
  "data": {
    "agents": [],
    "models": [],
    "models_omitted": 0,
    "catalog_versions": [],
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
