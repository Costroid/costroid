# Read a result from an agent

Every Costroid report writes one result object: the figures of one run per currency and basis, the token counts by class, the grades, the budget and what is left or over. An agent reads that object, never the comment's prose. Its contract is [`result/v0.1`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.1) in `Costroid/costroid-schemas`.

## Where the object is

| Where                                    | What it holds                                                                                                                  |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `costroid run --json`, on stdout         | The run just finished. `conclusion` is null, because no check has concluded yet.                                               |
| `costroid report --json`, on stdout      | The object the check run carries, byte for byte.                                                                               |
| The `costroid` check run's `output.text` | The same object, inside one fenced `json` block. Every amount is in it.                                                        |
| The pull request comment                 | A copy that adds `check_run_id`. On a public repository with `comment.show_cost: false` it withholds amounts and token counts. |
| The action's `result` output             | The check run's object as one line of JSON.                                                                                    |

Under `run --json` the agent's own stream goes to stderr, so stdout holds the object alone. `run` prints no object when it exits 125.

## Read it for a head commit

The check run sits on the pull request's head commit. With the GitHub CLI:

```sh
gh api "repos/OWNER/REPO/commits/$HEAD_SHA/check-runs?check_name=costroid" \
  --jq '.check_runs | max_by(.completed_at) | .output.text | ltrimstr("```json\n") | rtrimstr("\n```") | fromjson'
```

A commit can carry more than one `costroid` check run, one per report, so the program takes the one that completed last. A check run cannot name itself in its own text, so its id is the `id` field beside `output`, and the comment's copy names it as `check_run_id`.

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

`cost_basis` says what the amount is: `measured` from an API key's usage, `list_equivalent` for a subscription run at list price, which nobody billed. `enforcement_grade` says what held the run to its budget, and [`adapters.md`](adapters.md) defines each grade. `run_identity` reads `unverified` on every object a CI job writes, because the agent's own job wrote the rows.

## Read the budget

`budget` is null when the report was not told the budget. Otherwise it sets the budget beside the figures in its currency:

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

`cost` and the fields after it are null when the figures in USD span more than one basis, because no single figure compares with the budget.

## When there are no figures

`state` is `reported` when the rows gave figures. Otherwise `figures` is empty and `reason` is one of a closed list:

| `reason`               | Meaning                                                                           |
| ---------------------- | --------------------------------------------------------------------------------- |
| `no_rows_file`         | No rows file reached the report. The agent's job log says why.                    |
| `empty_rows_file`      | The rows file holds no rows.                                                      |
| `rows_path_not_a_file` | The rows path is not a regular file, so it was not read.                          |
| `rows_file_unread`     | The rows file could not be read.                                                  |
| `invalid_row`          | A line is not a valid row, and `reason_line` names it. No partial total is shown. |

## In a workflow

The action sets the same figures as step outputs on the report job: `cost`, `currency`, `cost_basis`, `tokens_input_uncached`, `tokens_cache_read`, `tokens_cache_write`, `tokens_output`, `enforcement_grade`, `catalog_version`, `check_run_id`, `conclusion` and `result`. When the figures hold more than one currency or basis, `currency` or `cost_basis` reads `multiple`, `cost` and the token outputs are empty, and `result` holds each figure.
