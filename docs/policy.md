# Hold a pull request to a cost policy

The `costroid` check concludes `success`, `neutral` or `failure` from a policy. The policy is a Rego pack built into the binary. [Open Policy Agent](https://www.openpolicyagent.org/) v1.22.2 evaluates it, with no network and no model. Its settings come from `costroid.yml` at the head of the pull request's base branch when the report runs.

## The rules

| Rule                   | Setting in `costroid.yml`     | What it checks |
| ---------------------- | ----------------------------- | -------------- |
| `run_budget`           | `policy.run_budget_usd`       | The run's cost at list, in USD, against the budget |
| `run_cost_threshold`   | `policy.max_run_cost_usd`     | The run's cost at list, in USD, against the threshold |
| `run_increase`         | `policy.max_increase_usd`     | The run's cost at list, in USD, against the median of this agent's earlier runs plus the amount set |
| `run_increase_percent` | `policy.max_increase_percent` | The run's cost at list, in USD, against the median of this agent's earlier runs plus the percent set |
| `pr_budget`            | `policy.pr_budget_usd`        | The pull request's total at list, in USD, this run included, against the budget |
| `monthly_budget`       | `policy.monthly_budget_usd`   | The repository's month to date, in USD, against the budget. It reads a ledger only |
| `model_allowlist`      | `policy.models`               | Each row's `costroid/model` tag against the list |
| `attribution`          | none                          | Rows that are `unattributed`, yet name a credential, a pull request and a head commit. A ledger that checked them against the job's OIDC token would grade them `windowed`, or `exact` when the credential is a gateway key attested for their run |

A rule without its setting does not run. With no `costroid.yml`, only `attribution` runs. `attribution` warns, or says nothing. It never fails and never passes.

The budget rules compare USD amounts as exact decimals. A row in another currency is never converted, and never compared with a USD limit.

Four rules read figures from outside the run. One that is set and lacks its figure concludes `unproven`, so the check cannot conclude `success`, and the comment names the rule and says why it did not run:

| Rule                                   | Reads                                                         | In this release                                                                        |
| -------------------------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `run_increase`, `run_increase_percent` | The median of 10 or more earlier runs of the same agent        | No baseline reaches the check, so both conclude `unproven` with reason `no_baseline`   |
| `pr_budget`                            | The total of the pull request's earlier runs, from their check runs | `costroid report` reads it on a GitHub pull request. Off a pull request, on GitLab, under `costroid policy`, or when a commit's check runs cannot be read, it concludes `unproven` with reason `no_lifetime` |
| `monthly_budget`                       | The repository's month to date, from a ledger                 | No ledger reaches the check, so it concludes `unproven` with reason `no_ledger`        |

`pr_budget` runs when `costroid report` posts on a GitHub pull request. The report reads the `costroid` check runs on every commit of the pull request, adds up the figures in USD of every basis, and adds this run's rows. A figure in another currency is left out of the total, and the rule then cannot pass. An earlier report with this run's `rows_sha256` is this run reported again and is left out, and a repeated `rows_sha256` counts once. The comment states the same total under the figures, per currency and basis.

`pr_budget` treats a total that may count one report twice as no proof either way: the rule concludes `unproven` with reason `repeat_possible`. That happens when an earlier report states no `rows_sha256`, as every result object before `result/v0.6` does, and when this run's rows name no row ids.

Any job of the repository can post a check run named `costroid`, so the total is unverified. It can show a pull request over its budget, never within it: the rule warns or fails, or concludes `unproven`. A total that leaves out check runs, because GitHub lists at most 250 commits or an object could not be read, can only read low.

`model_allowlist` takes exact model ids, and prefixes that end in `*`: `claude-*` admits `claude-sonnet-5-5`, and `claude` admits only `claude`. A row whose tag is absent, or is not a model id of at most 128 letters, digits and `._:/+-`, cannot pass the rule: it concludes `unproven` with reason `unreadable_model`. A model off the list fails the rule whatever else the rows hold, and the verdict names at most 10 such models.

## The verdict on each trust path

Who wrote the rows decides what a rule can conclude. A row whose run identity no one checked is `unattributed`, and an `unattributed` row never satisfies a rule.

| Outcome of a budget, increase or model rule | Rows the agent's job wrote (the Action path)                               | Rows a ledger checked against the job's OIDC token                            |
| ------------------------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Within the limit, or every model on the list | Cannot pass. The check concludes `neutral`: "unverified rows cannot satisfy a budget" | Passes, and the check can conclude `success`. One `unattributed` row makes it `neutral`: "unattributed rows cannot satisfy a budget" |
| Over the limit, or a model off the list     | Warns during the warn-only period, then fails                              | Warns during the warn-only period, then fails                                 |
| No rows reached the check                   | Cannot pass                                                                | Cannot pass                                                                   |

The earlier runs behind a baseline or a pull request's total carry a trust of their own. A rule that reads them passes only when they and this run's rows were both checked against a token. A baseline or a total that the agents' jobs wrote can show a run over its limit, never within it.

| Outcome of `attribution`                         | Rows the agent's job wrote | Rows a ledger checked |
| ------------------------------------------------ | -------------------------- | --------------------- |
| An `unattributed` row that names a credential, a pull request and a head commit | Warns | Warns |
| Otherwise                                        | No result                  | No result             |

On the Action path no rule can pass, so the check concludes `neutral` or `failure`, never `success`.

The report stage reads a rows file that the agent's own job wrote. An agent that a prompt took over could write a smaller cost, or another model's name, into that file. A figure from it can show that a run went over its budget or used a model off the list. It can never show that a run stayed under, or kept to the list.

`costroid serve` checks each row's run identity against the job's OIDC token. The pack applies the second column to rows graded that way. In this release `serve` does not call the pack: `costroid report` and `costroid policy` do, and both read a rows file.

## From the rules to the conclusion

| Rule outcomes                                          | Conclusion |
| ------------------------------------------------------ | ---------- |
| A rule fails                                           | `failure`  |
| No rule fails, and a rule warns or cannot pass         | `neutral`  |
| Every rule that ran passes                             | `success`  |
| No rule ran                                            | `neutral`  |

GitHub counts `neutral` as passing: "Required status checks must have a `successful`, `skipped`, or `neutral` status" ([About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches), retrieved 2026-11-06). Only `failure` blocks a merge, and only on a branch that requires the check. [`docs/required-check.md`](required-check.md) says how to require it.

## The warn-only period

For 7 days from the commit that added the Costroid workflow to the base branch, a rule that would fail warns instead, and the check concludes `neutral`. When the period holds a failure back, the comment says when the period ends and gives the one line that ends it early:

```yaml
policy:
  enforce: true
```

The report finds that commit by asking GitHub for the oldest commit on the base branch that touched the workflow file the job runs from, as `GITHUB_WORKFLOW_REF` names it. It asks at the commit it reads `costroid.yml` at. The request needs `contents: read`, which the recipes' report job already has. When the report cannot find the commit, it treats the period as running, and a rule that would fail warns. That happens on a pull request that adds the workflow, on GitLab, and when GitHub does not answer.

The second half of the period, 20 pull requests with a measured cost, needs a reader of earlier check runs. It arrives in a later release. Until then only the 7 days count.

## Settings come from the base branch

The report reads `costroid.yml` at the head of the pull request's base branch when the report runs, never at the pull request's head. A pull request that raises its own budget, or removes `policy.enforce`, does not change its own verdict. The comment says so on every pull request: a change to `costroid.yml` applies after the merge. On GitHub the report posts on the `pull_request` event alone. On any other event it posts nothing, and one line says why.

The event's base commit is not that head. GitHub sets `pull_request.base.sha` to the merge-base, and GitLab's `CI_MERGE_REQUEST_DIFF_BASE_SHA` is the merge-base too, so either moves only when the pull request updates. The report asks the host for the head of the branch the event names, `pull_request.base.ref` on GitHub and `CI_MERGE_REQUEST_TARGET_BRANCH_NAME` on GitLab, so a budget changed on the base branch reaches every open pull request at its next report. The verdict records the commit it read as `config_ref`.

When the host does not answer with that head, the report reads the file at the event's base commit, records that commit as `config_ref`, and warns on stderr: `costroid.yml is read at the event's base commit <commit>, not the base branch's head`.

The workflow's `budget-usd` caps the run, and a pull request can change its own workflow. No rule reads it. Set `policy.run_budget_usd` to hold each run to a budget.

## What a verdict records

The check run's machine block carries the verdict under `policy`. It records each rule's outcome and reason, the figures the rule was held to, the warn-only period, the time of the check, the commit the settings came from, and the SHA-256 of the pack and of the `costroid.yml` bytes it read. A missing file hashes as zero bytes, and `config` reads `absent`. A file the report could not read or parse runs no rule, `config` reads `unread` or `invalid`, and the check concludes `neutral`.

```json
"policy": {
  "conclusion": "failure",
  "trust": "unverified",
  "pack_sha256": "<SHA-256 of the pack>",
  "config_sha256": "<SHA-256 of costroid.yml>",
  "config_ref": "<head of the base branch>",
  "config": "found",
  "config_from_base": true,
  "evaluated_at": "2026-11-09T10:00:00Z",
  "warn_only": { "active": false, "reason": "ended", "started": "2026-11-01T10:00:00Z", "ends": "2026-11-08T10:00:00Z" },
  "results": [
    { "rule": "run_budget", "outcome": "fail", "reason": "over_limit", "limit_usd": "0.001", "cost_usd": "0.001619725" }
  ],
  "joinable_rows": 0
}
```

A result can carry `limit_usd`, `cost_usd` and `baseline_usd`, which are amounts, `limit_percent`, and `models`, the models off the list. `cost_usd` is the run's cost, except under `pr_budget`, where it is the pull request's total. The comment's copy of the block drops `limit_usd`, `cost_usd` and `baseline_usd` when it withholds amounts. The check run's block keeps them. The action's `conclusion` output carries the check's conclusion, and its `result` output the whole object.

## The engine

- Open Policy Agent v1.22.2, licensed Apache-2.0, pinned in `go.mod`.
- The capabilities file, [`internal/policy/capabilities.json`](../internal/policy/capabilities.json), lists only the built-in functions the pack calls. `http.send`, `net.*`, `opa.runtime`, `time.now_ns`, `rand.*` and every other built-in that reaches a network or gives a different answer on each call are absent. A pack that calls one fails to load. `allow_net` is empty.
- OPA compares a number of more than 15 significant digits as a `float64`. The pack therefore compares amounts with `costroid.compare_decimal`, a function the binary adds beside the file, which compares exact decimals and reaches nothing.
- The pack never reads a clock. The report passes the time in.

## Check a verdict offline

```sh
git show origin/main:costroid.yml > base.yml
costroid policy --rows rows.jsonl --config base.yml
```

It prints the conclusion, then the verdict's sentences one per line, in the comment's words. `--json` prints the verdict as the machine block carries it. `--workflow-added` and `--at` take the period's start and the time of the check, both as RFC 3339, so the command gives the same verdict the check gave. Without `--workflow-added` the period counts as not ended, as it does in the report.

It exits 0 on `success` or `neutral`, 1 on `failure`, and 125 when it cannot read a file.
