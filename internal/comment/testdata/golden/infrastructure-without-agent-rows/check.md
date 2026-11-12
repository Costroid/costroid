# Title

Verdict: neutral

# Summary

**Verdict: neutral.** No policy ran on this change.

This is one run at commit `9f2c6e1`, not a total across runs. [CI run](https://github.com/example/widgets/actions/runs/19203384756)

The rows file holds no agent rows, so this report shows no agent figures. Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which.

Infrastructure, estimated: EUR 35.008 for 2026-12, from `infrastructure`.

The agent figure and these infrastructure figures are not added together.

<details>
<summary>Infrastructure rows</summary>

| Resource | Service | Period | Amount |
| --- | --- | --- | --- |
| `aws_instance.web` | `Amazon EC2` | 2026-12 | EUR 25.488 |
| `aws_ebs_volume.data` | `Amazon EBS` | 2026-12 | EUR 9.52 |

</details>

# Text

```json
{
  "schema": "costroid.result/0.9",
  "conclusion": "neutral",
  "state": "no_rows",
  "reason": "empty_rows_file",
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
