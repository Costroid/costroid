# Read a result from an agent

Every Costroid report writes one result object: the figures of one run per currency and basis, the token counts by class, the grades, the cap and what is left or over. An agent reads that object, never the comment's prose. Its contract is [`result/v0.4`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.4) in `Costroid/costroid-schemas`.

## Where the object is

| Where                                    | What it holds                                                                                                                                                           |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `costroid run --json`, on stdout         | The run just finished. `conclusion` is null, because no check has concluded yet.                                                                                        |
| `<rows>.run.json`, beside the rows file  | What `run --json` prints, written whether or not `--json` is given. `--out costroid-rows.jsonl` gives `costroid-rows.run.json`, and the action's artifact carries both. |
| `costroid report --json`, on stdout      | The object the check run carries, byte for byte.                                                                                                                        |
| The `costroid` check run's `output.text` | The same object, inside one fenced `json` block. Every amount is in it, and `check_run_id` names the check run.                                                         |
| The pull request comment                 | A copy that names `check_run_id`. On a public repository with `comment.show_cost: false` it withholds amounts and token counts.                                         |
| The action's `result` output             | The check run's object as one line of JSON.                                                                                                                             |

Under `run --json` the agent's own stream goes to stderr, so stdout holds the object alone. `run` and `ingest-run` write the object whenever they created the rows file, and exit 125 does not change that: a run that another run held, or whose reader refused the agent's output, says so in its object. Before that file exists, as when a flag is missing, they write none.

## Read it for a head commit

The check run sits on the pull request's head commit. With the GitHub CLI:

```sh
gh api "repos/OWNER/REPO/commits/$HEAD_SHA/check-runs?check_name=costroid" \
  --jq '.check_runs | max_by(.completed_at) | .output.text | ltrimstr("```json\n") | rtrimstr("\n```") | fromjson'
```

A commit can carry more than one `costroid` check run, one per report, so the program takes the one that completed last. GitHub assigns a check run its id when the report creates it, so the report writes `check_run_id` into the object with a second request. When that request fails, the object has no `check_run_id`, and the `id` field beside `output` is the same number.

## Read the figures

Each entry of `figures` is one currency, one basis and one pair of grades. Figures in two currencies or two bases are never summed, so an agent never adds one entry to another unless both fields match.

```json
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
```

Amounts and token counts are decimal strings. `reasoning_output` and `cached_input_reported` repeat tokens that `output` and `cache_read` already count, so they are not added to them.

`cost_basis` says what the amount is: `measured` from an API key's usage, `list_equivalent` for a subscription run at list price, which nobody billed. `enforcement_grade` says what held the run to its cap, and [`adapters.md`](adapters.md) defines each grade. `run_identity` reads `unverified` on every object a CI job writes, because the agent's own job wrote the rows.

## Read the cap

`cap` is the cap the run was given. It is null when the report was not told the cap. Otherwise it sets the cap beside the figures in its currency:

```json
{
  "currency": "USD",
  "amount": "0.001",
  "enforcement_grade": "wrapper-watchdog",
  "cost": "0.0283136",
  "cost_basis": "measured",
  "remainder": "0",
  "overshoot": "0.0273136",
  "overshoot_tokens": "16960"
}
```

This is a recorded Codex run, stopped at a 0.001 USD cap after its first request settled at 0.0283136 USD. `overshoot_tokens` counts the billed tokens of the request that took the cost past the cap and of every request that ended after it. `enforcement_grade` is the weakest grade of the figures. `agent-flag` is the agent's own client-side estimate and can pass the cap; `none` means nothing held the run, and the comment then states no cap. A cap is never stated in turns.

`cost` and the fields after it are null when the figures in USD span more than one basis, because no single figure compares with the cap.

The cap is not the policy's budget. The policy reads its budget from `policy.run_budget_usd` in `costroid.yml`, and `policy.results` states it as `limit_usd` beside the rule `run_budget`. No rule reads the cap. Either can be present without the other, and the comment names them apart: `Cap:` for the one, `Run budget:` for the other.

## When there are no figures

`state` is `reported` when the rows gave figures. Otherwise `figures` is empty and `reason` is one of a closed list:

| `reason`               | Meaning                                                                                                                        |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `no_rows_file`         | No rows file reached the report. The agent's job log says why.                                                                 |
| `empty_rows_file`      | The rows file holds no rows.                                                                                                   |
| `rows_path_not_a_file` | The rows path is not a regular file, so it was not read.                                                                       |
| `rows_file_unread`     | The rows file could not be read.                                                                                               |
| `invalid_row`          | A line is not a valid row, and `reason_line` names it. No partial total is shown.                                              |
| `fork_no_rows`         | A fork's pull request whose workflow run left no rows file. The recipes run no agent there, and the follow-up posts its check. |

When the rows file holds no rows and the run said why, `run_reason` carries the run's own code:

| `run_reason`     | Meaning                                                                            |
| ---------------- | ---------------------------------------------------------------------------------- |
| `no_usage`       | The agent ended and reported no usage.                                             |
| `concurrent_run` | Another run held the agent's state directory, so this run never started its agent. |
| `reader_refused` | The reader refused the agent's stream or its execution file, so no usage was read. |

With `reader_refused`, `reader_error` carries the reader's first error line. The error can quote the agent's stream, so every character outside letters, digits, the space and `.,:;()'=_/+-` reads `?`, and the line is cut at 160 bytes.

`run` and `ingest-run` write it into the object beside the rows file. `report` reads it from there, with no flag, and the comment then states the reason in words beside its code. A reason the report does not know, from a newer run, is ignored with a warning.

## In a workflow

The action sets the same figures as step outputs on the report job: `cost`, `currency`, `cost_basis`, `tokens_input_uncached`, `tokens_cache_read`, `tokens_cache_write`, `tokens_output`, `enforcement_grade`, `catalog_version`, `check_run_id`, `conclusion` and `result`. When the figures hold more than one currency or basis, `currency` or `cost_basis` reads `multiple`, `cost` and the token outputs are empty, and `result` holds each figure.
