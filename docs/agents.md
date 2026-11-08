# Read a result from an agent

Every Costroid report writes one result object: the figures of one run per currency and basis, the token counts by class, the grades, the cap and what is left or over. An agent reads that object, never the comment's prose. Its contract is [`result/v0.7`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.7) in `Costroid/costroid-schemas`.

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

## Name the rows

Two digests name what the figures were built from, each as lower-case hex:

| Field              | Value                                                                                                                  |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| `rows_sha256`      | The SHA-256 of the rows' `x_CostroidRowId` values, sorted, each followed by a line feed. Null when there are no figures |
| `rows_file_sha256` | The SHA-256 of the rows file, byte for byte, as the report read it. Null when the report could not read it to its end   |

Two objects with the same `rows_sha256` report the same rows, whatever order or line endings each file gave them. `rows_sha256` is null as well when a row names no `x_CostroidRowId` of 64 lower-case hex digits. Both can be checked by hand against the rows file:

```sh
jq -r .x_CostroidRowId costroid-rows.jsonl | LC_ALL=C sort | sha256sum
sha256sum costroid-rows.jsonl
```

`costroid report` fills both.

## Read the pull request's total

`lifetime` holds the totals the comment states under the figures: the reports on every commit of the pull request, this run's included, added up per currency and basis.

```json
{
  "figures": [
    {
      "currency": "USD",
      "cost_basis": "measured",
      "list_cost": "0.15",
      "reports": 2,
      "may_repeat": false
    }
  ],
  "figures_omitted": 0,
  "capped": false,
  "unread": 0
}
```

Reports with the same `rows_sha256` count once. An object older than `result/v0.6` states no digest, so it counts once per check run, and `may_repeat` is `true` when such a report shares a total with another. `figures` holds at most 10 totals, and `figures_omitted` counts the rest. `capped` is `true` when GitHub listed 250 commits, its limit, so a later commit can be missing. `unread` counts the check runs whose object could not be read or added up.

`lifetime` is null off a pull request, on GitLab, when a commit's check runs could not be read, and in a comment's copy that withholds amounts. Any job of the repository can post a check run named `costroid`, so the total is a claim, never a measurement.

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

The report writes `artifact_refused` when the action refused the artifact that carried the rows; the check then fails, and an approval does not change that.

With `reader_refused`, `reader_error` carries the reader's first error line. The error can quote the agent's stream, so every character outside letters, digits, the space and `.,:;()'=_/+-` reads `?`, and the line is cut at 160 bytes.

`run` and `ingest-run` write it into the object beside the rows file. `report` reads it from there, with no flag, and the comment then states the reason in words beside its code. A reason the report does not know, from a newer run, is ignored with a warning.

Three fields say how the agent's job ended and which run wrote the object. Each is in every object, and null when nothing stated it.

| Field           | Value                                                                                                                                                                                         |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `agent_error`   | `{"class": ...}`, one of `authentication_failed`, `billing_error`, `rate_limit`, `invalid_request`, `server_error` or `unknown`, read from the agent's result fields and never from its text |
| `agent_skipped` | `true` when the CI system skipped the agent's job, with the reason `agent_skipped`; `false` when the job ran                                                                                 |
| `ci_run`        | `{"id": ..., "attempt": ...}` from `GITHUB_RUN_ID` and `GITHUB_RUN_ATTEMPT`, a claim the environment makes                                                                                    |

An agent can fail after some requests, so `agent_error` can sit beside figures. With no rows it comes with the run reason `agent_error`. This release fills all three. `costroid ingest-run` fills `agent_error` and `ci_run` as `costroid run` does.

`agent_stated` is a figure the agent states about its own run, in its own unit, null when it states none and in a copy that withholds amounts. gh-aw states its AI credits: `{"unit": "AIC", "quantity": "7.47", "source": "gh-aw"}`. Costroid never converts it and never adds it to `figures`, `cap` or `lifetime`. [`docs/gh-aw.md`](gh-aw.md) says how the quantity is read.

## Read the run's status

While the agent works, `costroid run` writes a status file: what the run has spent so far, its cap, what is left and what holds it. The agent, or a later step of the job, reads it to learn what the run costs before the run ends. Its contract is [`status/v0.1`](https://github.com/Costroid/costroid-schemas/tree/main/status/v0.1) in `Costroid/costroid-schemas`.

The file is at `$COSTROID_STATUS_FILE`. When that is unset, it is `$RUNNER_TEMP/costroid-status.json`, and GitHub Actions empties `RUNNER_TEMP` at the start and end of each job. When neither is set, as on GitLab unless the job sets `COSTROID_STATUS_FILE`, `run` writes no file.

`run` writes the file when it reads the agent's first line, after each request it sees settle while it watches the agent, and when the agent ends. Each write replaces the whole file by renaming a new file over it, so a reader never meets part of one. No file means no `costroid run` has started an agent in this job. A file that reads `running`, with an empty `spend` and `requests` at `0`, is a run that has priced nothing yet.

This is a recorded Codex run after its first request, under a 5 USD cap:

```json
{
  "schema": "costroid.status/0.1",
  "state": "running",
  "agent": "codex",
  "updates": "each_request",
  "started_at": "2026-11-01T11:17:09Z",
  "updated_at": "2026-11-01T11:17:10.25Z",
  "spend": [
    {
      "currency": "USD",
      "cost_basis": null,
      "list_cost": "0.0291216"
    }
  ],
  "requests": 1,
  "cap": {
    "currency": "USD",
    "amount": "5",
    "remainder": "4.9708784",
    "overshoot": "0"
  },
  "enforcement_grade": "wrapper-watchdog"
}
```

`updates` says when `spend` changes. `each_request` is a Codex run the wrapper watches: each request is priced from Codex's rollout file as it settles. `agent_end` is every other run, Claude Code's among them: the agent states its usage only when it ends, so `spend` and `requests` are null until then.

`spend` holds one entry per currency and basis, at the catalog's list prices. Entries are never added across a currency or a basis. `cost_basis` is null while Codex works, because its reader states the basis when Codex ends. `requests` counts the model requests priced so far, and is null when the agent states some usage as a turn's or a run's total. `cap.remainder` and `cap.overshoot` set the spend in USD beside the cap, and both are null when that spend is not known.

The file holds amounts, counts, times and words from closed lists. It never holds a prompt, a path, a model name or the model's answer.

When the agent has ended, `state` reads `ended`. The run's last write states the grade the run earned once its rows were checked, the same grade as the run's object beside the rows file, which states what the run came to.

## In a workflow

The action sets the same figures as step outputs on the report job: `cost`, `currency`, `cost_basis`, `tokens_input_uncached`, `tokens_cache_read`, `tokens_cache_write`, `tokens_output`, `enforcement_grade`, `catalog_version`, `check_run_id`, `conclusion` and `result`. When the figures hold more than one currency or basis, `currency` or `cost_basis` reads `multiple`, `cost` and the token outputs are empty, and `result` holds each figure.
