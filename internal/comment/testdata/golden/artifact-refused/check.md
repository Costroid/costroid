# Title

No figure (artifact_refused): the rows artifact held files besides the rows and the run object, so this report read none of it and the check fails.

# Summary

**No figure (artifact_refused): the rows artifact held files besides the rows and the run object, so this report read none of it and the check fails.**

The `costroid-rows` artifact held 2 entries besides the rows and the run object: `notes.txt` and `extra/rows.jsonl`.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

# Text

```json
{
  "schema": "costroid.result/0.9",
  "conclusion": "failure",
  "state": "rows_unreadable",
  "reason": "no_rows_file",
  "run_reason": "artifact_refused",
  "agent_error": null,
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
