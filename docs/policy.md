# Hold a pull request to a cost policy

The `costroid` check concludes `success`, `neutral` or `failure` from a policy. The policy is a Rego pack built into the binary. [Open Policy Agent](https://www.openpolicyagent.org/) v1.22.2 evaluates it, with no network and no model. Its settings come from `costroid.yml` at the head of the pull request's base branch when the report runs.

## The rules

| Rule                   | Setting in `costroid.yml`     | What it checks |
| ---------------------- | ----------------------------- | -------------- |
| `run_budget`           | `policy.run_budget_usd`       | The run's cost at list, in USD, against the budget |
| `run_cost_threshold`   | `policy.max_run_cost_usd`     | The run's cost at list, in USD, against the limit |
| `run_increase`         | `policy.max_increase_usd`     | The run's cost at list, in USD, against the median of this agent's runs on merged pull requests plus the amount set |
| `run_increase_percent` | `policy.max_increase_percent` | The run's cost at list, in USD, against the median of this agent's runs on merged pull requests plus the percent set |
| `run_p95`              | `policy.max_p95_factor`       | The run's cost at list, in USD, against the factor set times the P95 of this agent's latest 20 runs on merged pull requests |
| `pr_budget`            | `policy.pr_budget_usd`        | The pull request's total at list, in USD, over every basis, this run included, against the budget |
| `monthly_budget`       | `policy.monthly_budget_usd`   | The repository's month to date, in USD, against the budget. It reads a ledger only |
| `model_allowlist`      | `policy.models`               | Each row's `costroid/model` tag against the list |
| `attribution`          | none                          | Rows that are `unattributed`, yet name a credential, a pull request and a head commit. A ledger that checked them against the job's OIDC token would grade them `windowed`, or `exact` when the credential is a gateway key a run of the protected default branch attested for their run and their head |

A rule without its setting does not run. With no `costroid.yml`, only `attribution` runs. `attribution` warns, or says nothing. It never fails and never passes.

The budget rules compare USD amounts as exact decimals. A row in another currency is never converted, and never compared with a USD limit.

No rule reads a row that carries `costroid/origin`, whatever else the row states: a receipt of your own sessions, which [`docs/receipt.md`](receipt.md) describes, never raises, satisfies or fails a budget, a limit or the model list, and neither does such a row in a rows file.

Five rules read figures from outside the run. One that is set and lacks its figure concludes `unproven`, so the check cannot conclude `success`, and the comment names the rule and says why it did not run:

| Rule                                   | Reads                                                         | In this release                                                                        |
| -------------------------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `run_increase`, `run_increase_percent` | The median of 10 or more runs of the same agent on the pull requests merged into the base branch, from their check runs | `costroid report` reads it on a GitHub pull request, and `costroid policy --baseline` from a file. With fewer runs, off a pull request, on GitLab, or when any part of it cannot be read, both conclude `unproven` with reason `no_baseline` |
| `run_p95`                              | The P95 of the latest 20 runs of the same agent on the pull requests merged into the base branch, from their check runs | Read as the median rules read their baseline. With fewer than 20 runs, or where they conclude `no_baseline`, it concludes `unproven` with reason `no_baseline` |
| `pr_budget`                            | The total of the pull request's earlier runs, from their check runs | `costroid report` reads it on a GitHub pull request, and `costroid policy --history` from a file. Off a pull request, on GitLab, under `costroid policy` without `--history`, or when a commit's check runs cannot be read, it concludes `unproven` with reason `no_lifetime` |
| `monthly_budget`                       | The repository's month to date, from a ledger                 | No ledger reaches the check, so it concludes `unproven` with reason `no_ledger`        |

`pr_budget` runs when `costroid report` posts on a GitHub pull request. The report reads the `costroid` check runs on every commit of the pull request, adds up the figures in USD of every basis, and adds this run's rows. A figure in another currency is left out of the total, and the rule then cannot pass. An earlier report with this run's `rows_sha256` is this run reported again and is left out, and a repeated `rows_sha256` counts once. The comment states the same total under the figures, per currency and basis.

The rule's line names the bases its total adds, because a Measured figure and a List-equivalent one are different claims. They stand beside the amount: over two bases `at list (Measured and List-equivalent together)`, and over one `at list (Measured)`. Where amounts are withheld the line ends `The total adds Measured and List-equivalent together, at list.` or `The total is at list (Measured).`

`pr_budget` treats a total that may count one report twice as no proof either way: the rule concludes `unproven` with reason `repeat_possible`. That happens when an earlier report states no `rows_sha256`, as every result object before `result/v0.6` does, and when this run's rows name no row ids.

Any job of the repository can post a check run named `costroid`, so the total is unverified. It can show a pull request over its budget, never within it: the rule warns or fails, or concludes `unproven`. A total that leaves out check runs, because GitHub lists at most 250 commits or an object could not be read, can only read low.

## The baseline

`run_increase` and `run_increase_percent` hold a run to the median of the same agent's runs on the pull requests merged into the base branch. The report reads those runs from the `costroid` check runs on each merged pull request's head commit. The pull request the check is about never counts, whatever its state: not its own earlier runs, not a listing of it as merged, and not a merged pull request whose head is its head. Before commit `df493ca` of 2026-11-21 the baseline was the pull request's own earlier runs, so a pull request set its own baseline: costly early runs raised the median a later run was held to.

- The report reads the baseline only when `costroid.yml` at the base sets `max_increase_usd`, `max_increase_percent` or `max_p95_factor`. It asks GitHub in one request for the 100 closed pull requests into the base branch that were updated last, and keeps the latest 30 merges among them. A merge updates its pull request, so from an answer of 100 it keeps only the merges after the answer's oldest update, and never skips a later merge for an earlier one. It then reads the check runs on each head in one request: 31 requests at most. It reads them beside the pull request's own check runs, inside the same 6 seconds.
- A run is one earlier report on rows that name one agent, the agent this run's rows name. A run that names no agent or several joins no baseline, and when this run's rows name no agent or several, there is no baseline.
- Every run on a merged head counts, not only its last: the window is the agent's latest runs, so a pull request whose agent ran three times adds three runs. The runs on a merged pull request's earlier commits are not read.
- A run's cost is its figures added up at list, over every basis, as the run rules read this run. So a median or a P95 has no one basis, and the comment gives its amount as `at list`. A run whose figures span two currencies has no single cost and joins no baseline. Each currency has its own baseline, and the rules read the one in USD.
- A run counts once, at the first check run that reported its `rows_sha256`, so an approval that posts the report again adds nothing. A report that states no `rows_sha256` may repeat another, and is left out. So is this run's own report.
- The window is the latest 20 runs with a cost in the currency, by check run id, the order in which GitHub creates check runs. Twenty runs is the count that ends the warn-only period.
- With fewer than 10 runs there is no baseline, and both rules conclude `unproven` with reason `no_baseline`. Below 10, one run moves the median.
- The median is the nearest-rank P50: the costs are sorted, and the median is the cost at rank ⌈n × 50 / 100⌉, counted from 1. Of 10 runs it is the 5th, of 20 the 10th. It is always one of the runs' costs, compared as an exact decimal, never a midpoint and never a float.

When any part of the baseline cannot be read, there is none, and every rule that reads it concludes `unproven` with reason `no_baseline`: the list, the check runs on one head, a `costroid` check run whose object this release does not read, or a head with more than 100 check runs named `costroid`. A baseline of part of its window would hold other runs than the latest. A warning on stderr names the part, and the pull request's total, read from other check runs, still reaches the comment.

Any job of the repository can post a check run named `costroid`, so a baseline is unverified, as the pull request's total is. It can show a run over it, never within it: the rules warn or fail, or conclude `unproven`. It is the one figure from a claim that can still move a limit: a check run posted on a merged head can raise the median, and a raised median spares a run a failure. A pull request's own check runs no longer can.

`model_allowlist` takes exact model ids, and prefixes that end in `*`: `claude-*` admits `claude-sonnet-5-5`, and `claude` admits only `claude`. A row whose tag is absent, or is not a model id of at most 128 letters, digits and `._:/+-`, cannot pass the rule: it concludes `unproven` with reason `unreadable_model`. A model off the list fails the rule whatever else the rows hold, and the verdict names at most 10 such models.

## The P95 limit

`run_p95` holds a run to a multiple of the P95 of the same agent's latest 20 runs on the pull requests merged into the base branch. It reads the baseline the median rules read, from the same check runs, so the pull request's own runs never count.

```yaml
policy:
  max_p95_factor: 2
```

- `max_p95_factor` is a plain decimal of at least 1 and below 10000 with at most 12 fractional digits, quoted or not, read from its text as an amount is. A run that costs more than the P95 times the factor is over the limit: it warns during the warn-only period, then fails. The limit is computed as an exact decimal. The result states the P95 as `p95_usd` and the factor as `limit_factor` in its shortest form, never their product, whose scale can pass the 12 fractional digits an amount holds. A baseline whose median or P95 is past what a ledger holds, 26 digits before the point and 12 after, is no run's cost: it is none for every rule, and this rule counts no runs.
- The P95 is the nearest-rank P95: the cost at rank ⌈n × 95 / 100⌉, counted from 1. For every n below 20 that rank is n, the highest cost, so one costly run would set the limit. Of 20 runs it is the 19th, and the window holds no more. So the rule needs all 20 runs. With fewer it does not run: it concludes `unproven` with reason `no_baseline`, and `baseline_runs` counts the runs it found. It never passes on a short history.
- Within the limit, it passes only when this run's rows and the baseline were both checked against a token, as the median rules do. On the Action path it concludes `unproven` at best.
- It sits beside the median rules. They are independent, read one baseline, and can all be set. `run_increase` and `run_increase_percent` ask whether a run is far from a typical run, and need 10 runs. `run_p95` asks whether a run costs more than nearly every earlier run, and needs 20. A verdict lists it after `run_increase_percent` and before `pr_budget`, so when it fails beside a median rule the sentence names the median rule.

## A repository's own pack

`policy_pack` in `costroid.yml` names one Rego file in the repository: a policy pack of the repository's own. Its rules can add a warning or a failure to the check. They can never make it pass, and they never change a rule of Costroid's.

```yaml
policy_pack: .costroid/policy.rego
policy:
  run_budget_usd: 5
```

```rego
package costroid.pack

# No run may use an Opus model.
results contains {
	"rule": "no_opus",
	"outcome": "fail",
	"reason": "model_not_allowed",
	"message": "Opus models are not used in this repository.",
} if {
	some usage in input.usage
	some model, _ in usage.models
	startswith(model, "claude-opus")
}

# Warn when the run budget could not be shown kept.
results contains {
	"rule": "budget_unproven",
	"outcome": "warn",
	"reason": "rows_unchecked",
} if {
	some result in input.results
	result.rule == "run_budget"
	result.outcome == "unproven"
}
```

Over the recorded run in [`internal/comment/testdata/rows.jsonl`](../internal/comment/testdata/rows.jsonl), which used `claude-haiku-5-5` and whose rows the agent's own job wrote, the first rule finds no Opus model, and the second warns, because `run_budget` cannot pass on those rows. The check concludes `neutral`, and the verdict holds:

```json
"pack": {
  "path": ".costroid/policy.rego",
  "sha256": "<SHA-256 of the file>",
  "results": [
    { "rule": "budget_unproven", "outcome": "warn", "reason": "rows_unchecked" }
  ]
}
```

### Where the pack is read

- The path is from the repository's root, has no `.` or `..` segment, ends `.rego` and is at most 256 characters of letters, digits, `.`, `_`, `-` and `/`. `costroid validate` refuses any other, and a `costroid.yml` that names one is not valid, so no rule runs.
- The report reads the pack at the commit it reads `costroid.yml` at, the head of the pull request's base branch, and never at the pull request's head. A pull request that changes the pack, or `policy_pack`, does not change its own verdict. When it changes either, the comment says the change applies after the merge. To say so, the report reads `costroid.yml` and the pack's path at the head, compares them with the base's, and runs nothing it read there.
- A fork's follow-up reads it at the default branch's commit, as it reads `costroid.yml`, and reads nothing at the head.
- `costroid policy` reads it from `--pack FILE`, or from the path `policy_pack` names in the directory that holds the `--config` file. `--pack` beside a `costroid.yml` that names no pack is refused.
- `costroid serve` runs a pack only when it read the file itself, through the GitHub App's installation the request names, at the head of the pull request's base branch. [`docs/serve.md`](serve.md#a-repositorys-own-pack) says when.
- A file over 64 KiB does not run: it concludes `pack_unread`, as an absent file does.

### What a pack returns

The module declares `package costroid.pack`, and `results` is a set or an array of at most 32 objects. Each object holds `rule`, `outcome` and `reason`, and may hold `message`, every value a string:

- `rule` and `reason` are the pack's own ids: a lower-case letter, then at most 62 lower-case letters, digits and underscores. `rule` is never one of Costroid's rule ids.
- `outcome` is `warn` or `fail`. A pack has no word for pass: a result with any other outcome stops the pack, and so does a key outside the four.
- `message` is the pack's own text. Every character outside letters, digits, the space and `.,:;()'=_/+-` becomes `?`, and it is cut at 160 bytes. The comment shows it as a code span under the title, and never in the title. A copy that withholds amounts drops it, since it can repeat an amount the pack read.

In the warn-only period a pack's `fail` warns, as Costroid's own rules do. The verdict holds the pack's results in `policy.pack.results`, apart from Costroid's `policy.results`, so a pack's rule named like one of Costroid's would still be the pack's.

### What a pack sees

| Key | Holds |
| --- | --- |
| `input.trust` | `unverified` for rows the agent's job wrote, `token_verified` for rows a ledger checked against the job's OIDC token |
| `input.rows` | The number of rows |
| `input.usage` | Per currency: `currency`, `list_cost` as a decimal string, `rows`, `unattributed_rows`, `joinable_rows`, and `models`, the rows per model id |
| `input.settings` | The `policy` keys the base's `costroid.yml` sets: each amount as a decimal string, and `models` |
| `input.baseline` | `trust`, `runs`, `p50_usd` and `p95_usd`, when 10 or more runs on merged pull requests give one |
| `input.lifetime` | `trust`, `total_usd`, `may_repeat` and `other_currency`, when the pull request's earlier runs were read |
| `input.results` | Costroid's own results: `rule`, `outcome` and `reason`, so a pack can fail a run whose budget could not pass |

A pack never sees a row or a tag, a path, a branch, a commit, the pull request's number, title or author, a reviewer, a credential, the head's `costroid.yml`, whether the warn-only period runs, or the time.

### What a pack may call

[`internal/policy/capabilities.pack.json`](../internal/policy/capabilities.pack.json) lists 15 built-ins: `=`, `==` and `!=`, `<`, `<=`, `>` and `>=`, `in` and `some k, v in`, `count`, `sum`, `object.union`, `startswith`, `endswith` and `contains`. The binary adds `costroid.compare_decimal`, which compares two amounts as exact decimals, since OPA compares a number of more than 15 significant digits as a `float64`. A pack that calls any other built-in is not valid. There is no arithmetic: amounts stay strings.

### What a pack can never do

- Make a check pass. Its results join the verdict through one function that only raises the conclusion, from `success` to `neutral` to `failure`. A failing rule of Costroid's stays a failure, and a pack's warning on a check Costroid would pass makes it `neutral`.
- Change a result of Costroid's. Rows the agent's job wrote still never satisfy a budget.
- Reach a network, a clock, randomness, a file or the environment. No such built-in is in the file.
- Run in the process that decides the verdict. A child process of the same binary compiles and evaluates the pack, with an empty environment, an empty working directory and nothing but its three pipes. The child is killed at 2 seconds, stops past 200,000 evaluation steps, and its stack is held to 64 MiB and, on Linux, its heap to 1 GiB. Its answer is read as a stranger's.

### When a pack does not run to its end

A fault the base branch alone can cause concludes `unproven`, and a fault the pull request's rows can cause fails. Costroid's own result of the rule `customer_pack` states it:

| Reason | When | Outcome |
| --- | --- | --- |
| `pack_unread` | The file is absent or over 64 KiB, or the host did not answer | `unproven`, so the check concludes `neutral` at best |
| `pack_invalid` | The file does not parse or compile, calls a built-in outside the file, or declares another package. `line` names the line the compiler named | `unproven` |
| `pack_bound` | The pack passed 200,000 steps or 2 seconds, returned more than 32 results, or the rows name more than 16 currencies or 256 model ids | `fail`, or `warn` in the warn-only period |
| `pack_result` | A result has another outcome, a value that is not a string, or an id outside the pattern | `fail`, or `warn` in the warn-only period |

Rows past the input's bound fail as `pack_bound` whether or not the pack would compile: the report judges the rows before a child starts. A child that says the pack is not valid is asked again with the pack and no rows. The same answer at the same line stands, since no row reached that child, and any other answer fails as `pack_result`. The job's log names each fault in a `costroid: warning:` line.

Under the warning on a pack that is not valid, `costroid policy` and the report print `Next: costroid policy --rows FILE --pack FILE runs the pack once corrected.` A pull request's check runs the base branch's pack, never the one the pull request changes, so a correction first runs where its author runs that command, on a job's rows file. A verdict from `costroid serve` states `pack_invalid` alone, and the same command runs the corrected pack.

## The verdict on each trust path

Who wrote the rows decides what a rule can conclude. A row whose run identity no one checked is `unattributed`, and an `unattributed` row never satisfies a rule.

| Outcome of a budget, increase, P95 or model rule | Rows the agent's job wrote (the Action path)                          | Rows a ledger checked against the job's OIDC token                            |
| ------------------------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Within the limit, or every model on the list | Cannot pass. The check concludes `neutral`, and its title opens "Within budget, by the job's own figures" | A budget, increase, P95 or model rule passes only when the gateway's attested count or a provider's report stands behind the rows' spend. On the agent's own token counts it concludes `unproven` with reason `unmeasured_spend`, and the check `neutral`. One `unattributed` row keeps any rule from passing |
| Over the limit, or a model off the list     | Warns during the warn-only period, then fails                              | Warns during the warn-only period, then fails                                 |
| No rows reached the check                   | Cannot pass                                                                | Cannot pass                                                                   |

The earlier runs behind a baseline or a pull request's total carry a trust of their own. A rule that reads them passes only when they and this run's rows were both checked against a token. A baseline or a total that the agents' jobs wrote can show a run over its limit, never within it.

| Outcome of `attribution`                         | Rows the agent's job wrote | Rows a ledger checked |
| ------------------------------------------------ | -------------------------- | --------------------- |
| An `unattributed` row that names a credential, a pull request and a head commit | Warns | Warns |
| Otherwise                                        | No result                  | No result             |

On the Action path no rule can pass, so the check concludes `neutral` or `failure`, never `success`.

The report stage reads a rows file that the agent's own job wrote. An agent that a prompt took over could write a smaller cost, or another model's name, into that file. A figure from it can show that a run went over its budget or used a model off the list. It can never show that a run stayed under, or kept to the list.

`costroid serve` checks each row's run identity against the job's OIDC token. The pack applies the second column to rows graded that way. A token proves which run sent a row, not the token counts or the model names on it, which the agent's job wrote. So on the second column a budget or the model allowlist needs the gateway's own count for a key minted for the run, which `costroid gateway close` attests from a run of the protected default branch, or a provider's report. Either can still fail on the agent's own counts.

`serve` answers the second column at `POST /v1/pulls/{pr}/verdict`, over a pull request's rows in its ledger. Its answer covers the pull request, so the run rules conclude `unproven` there. [`docs/serve.md`](serve.md#the-verdict) gives the request and the answer. `costroid report` and `costroid policy` read a rows file, and do not ask `serve`. Only `serve` can demote a rule, as [Demotion by the override rate](#demotion-by-the-override-rate) says.

`serve` concludes `success` only on a `costroid.yml` it read itself through the GitHub App's installation, at the head of the pull request's base branch. On settings a caller stated, or that it read at another commit, it concludes `neutral` at best, and its answer's `override_record` says why. A failure stays a failure. A ledger with no GitHub App therefore never answers `success`. [`docs/serve.md`](serve.md#success-only-at-the-base) says what each case reads.

## From the rules to the conclusion

| Rule outcomes                                          | Conclusion |
| ------------------------------------------------------ | ---------- |
| A rule fails                                           | `failure`  |
| No rule fails, and a rule warns or cannot pass         | `neutral`  |
| Every rule that ran passes                             | `success`  |
| No rule ran                                            | `neutral`  |

GitHub counts `neutral` as passing: "Required status checks must have a `successful`, `skipped`, or `neutral` status" ([About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches), retrieved 2026-11-06). Only `failure` blocks a merge, and only on a branch that requires the check. [`docs/required-check.md`](required-check.md) says how to require it.

## What the check says

One sentence says what is true of the run. It is the check's title, the first line of the comment and of the job summary, and the last line that `costroid policy` and `costroid demo` print. `costroid report --preview` prints it last too, on standard error, so its standard output is the comment alone. It opens with a state, then gives the figure with its currency and basis, the budget, and what was checked or what follows:

```text
Within budget, by the job's own figures: this run cost USD 0.001619725 at list (List-equivalent, not billed) of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.
Over budget: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget.
Over limit: this run cost USD 0.001619725 at list (List-equivalent, not billed), over 1.5 times the USD 0.0006 at list P95 of this agent's latest 20 runs.
Over budget, demoted to a warning: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget. After 2026-12-12 09:00 UTC this fails the check.
No budget set: this run cost USD 0.10 at list (Measured). No rule decides this check.
No figure (agent_error): the agent ended in an error before it reported usage, so the run wrote no rows. No rule ran.
```

| The sentence opens                                     | What is true                                                                                 | Conclusion |
| ------------------------------------------------------ | -------------------------------------------------------------------------------------------- | ---------- |
| `Within budget`, `Within limit`                        | The cost is within the budget or the limit, on rows a ledger checked                         | `success`  |
| Either, then ", by the job's own figures"              | The cost is within it, and only the agent's own job wrote the rows                           | `neutral`  |
| `Over budget`, `Over limit`, `Model not on the list`   | The cost is over it, or a row names a model off the list                                     | `failure`  |
| Any of the three, then ", warn-only"                   | The warn-only period holds that failure back                                                 | `neutral`  |
| `Over budget`, `Over limit`, then ", demoted to a warning" | A ledger demoted the rule by its override rate, so its failure warns until the date the sentence names | `neutral`, or `failure` when another rule fails |
| Any of the three, then ", approved by" and a login     | A reviewer approved that failure on this commit                                              | `neutral`  |
| Any of the three, then ", on a fork's own rows"        | A fork's own run wrote the rows, so the follow-up holds the check, in the warn-only period and after it | `neutral`  |
| `No budget set`                                        | `costroid.yml` sets nothing a rule reads                                                     | `neutral`  |
| `Not decided`                                          | `costroid.yml` could not be used, the rule named could not be checked, or the policy pack is not valid or could not be read | `neutral`  |
| `Pack rule failed` and the pack's rule in brackets     | A rule of the repository's own pack failed, and no rule of Costroid's failed                 | `failure`  |
| `Pack rule failed`, then ", warn-only" before the rule | The warn-only period holds that failure back                                                 | `neutral`  |
| `Pack rule warned` and the pack's rule in brackets     | A rule of the repository's own pack warned                                                   | `neutral`  |
| `Pack stopped` and a code in brackets                  | The pack passed its bound on this run's rows, `pack_bound`, or answered outside its words, `pack_result` | `failure`, or `neutral` in the warn-only period |
| `No figure` and a code in brackets                     | The report has no figures. The code is the result object's `run_reason`, `reason` or `state` | `neutral`, or `failure` for `artifact_refused` |
| `No agent run`                                         | The workflow skipped the agent's job                                                         | `neutral`  |

`Over limit` and `Within limit` name `run_cost_threshold`, the two increase rules and `run_p95`, whose amounts are limits. A budget is `run_budget`, `pr_budget` or `monthly_budget`.

When several rules ran, the sentence names one: the first, in the order of the table of rules above, of the worst outcome the verdict holds. A rule that fails comes before one the warn-only period holds, that before one that cannot pass, and that before one that passed. Every other rule's line follows in the comment, above the folds. `attribution` never names the sentence alone, and its line is under the first fold.

A rule of the pack's names the sentence after every failing rule of Costroid's: a pack rule that fails comes after a rule of Costroid's that fails and before one the warn-only period holds, and a pack rule that the period holds, then one that warns, come before a rule that cannot pass. The pack's results follow the sentence as a list, each with its rule, what it concluded, its reason and its message.

An amount shows at least two decimal places and every digit, such as `USD 5.00` and `USD 0.001619725`. Where amounts are withheld, the sentence names none and ends "Amounts are withheld here."

The sentence is at most 255 characters. A longer one is cut in a fixed order: the figures after the first are counted, then the last clause goes, then the budget. The state and the first figure are never cut.

The sentence is written for a reader, and its words can change between releases. A script reads the `conclusion` output or the result object, never the title.

## The warn-only period

For 7 days from the commit that added the Costroid workflow to the base branch, a rule that would fail warns instead, and the check concludes `neutral`. When the period holds a failure back, the comment says when the period ends and gives the one line that ends it early:

```yaml
policy:
  enforce: true
```

The report finds that commit by asking GitHub for the oldest commit on the base branch that touched the workflow file the job runs from, as `GITHUB_WORKFLOW_REF` names it. It asks at the commit it reads `costroid.yml` at. The request needs `contents: read`, which the recipes' report job already has. When the report cannot find the commit, it treats the period as running, and a rule that would fail warns. That happens on a pull request that adds the workflow, on GitLab, and when GitHub does not answer.

The period also ends at the twentieth report with a Measured figure, if its 7 days have not ended it first. The report counts them from the `costroid` check runs on the pull request's commits, this run's report included, as it reads them for `pr_budget`. A check run whose object withholds its amounts, or that cannot be read, is not counted, so the 7 days still end the period. `warn_only` then reads `ended` with no `ends`. `costroid serve` takes the count in the request instead, as `measured_pull_requests`.

## Demotion by the override rate

A cost rule whose failures reviewers approve more than one time in five warns where it would fail. Only `costroid serve` demotes a rule, and only from its own ledger's record of the verdicts it answered and the approvals it verified. A check run is never part of that record: any job of the repository can post one, and a record a branch can write must never relax a rule.

- **The record.** `serve` records a failing verdict when a rule fails at `POST /v1/pulls/{pr}/verdict`, and only when three reads it made itself through the GitHub App's installation the request names agree: `costroid.yml` at `config_ref` holds the bytes the request states, the pull request's head is a commit, and `config_ref` is the head of the pull request's base branch as GitHub names that branch. A verdict at any other commit, the pull request's own head among them, is answered and not recorded, and no rule is demoted in it: a caller could name a commit whose `costroid.yml` is looser, and leave no failure in the record. The answer's `override_record` says which read stopped it. `serve` records an approval when a reviewer presses the check's approve button and GitHub confirms the head and the reviewer's write role, as [`docs/approval.md`](approval.md#the-approve-button) says. A review that `costroid approve` counts in a job is not recorded. A failure the warn-only period held to a warning is not recorded either: nothing failed, so there was nothing to approve.
- **The window.** The record is kept per base branch: a rule's failures, its window and its demotion are those of pull requests into one branch, whose `costroid.yml` sets the limit and `auto_demote`. The window is the latest 20 pull requests on which the rule failed under its present limit, within 90 days and since the rule was last re-armed. A pull request counts once, however many pushes and approvals it had. An approval counts when it binds a head commit on which the rule failed. The pull request being judged never counts, neither its failures nor its approvals.
- **The threshold.** At least 10 pull requests in the window, approvals from at least 2 accounts, and the approved pull requests times 5 more than the failing ones. 3 of 10 demotes, 2 of 10 does not, and 4 of 20 does not. Below 10 pull requests or 2 accounts the rule fails as written.
- **What it does.** The rule's `fail` becomes `warn`. Its reason stays its own, such as `over_limit`, and `demoted` beside it states `approved`, `failing` and `until`. The check concludes `neutral`, or `failure` when another rule fails. A demoted rule never concludes `success`.
- **How long.** 30 days from the verdict that first crossed the threshold. That time is kept once, so the demotion neither ends early nor moves as the window rolls.
- **Re-arming.** At the end of the 30 days; at once when the rule's limit changes on the base branch, because the record is of that limit; and at once under `auto_demote: false`. After a re-arm the window starts empty, so 10 new failing pull requests come before another demotion.
- **Which rules.** `run_budget`, `run_cost_threshold`, `run_increase`, `run_increase_percent`, `run_p95` and `pr_budget` can be demoted. Never `model_allowlist`, `customer_pack` or a rule of the repository's own pack. Demotion runs on Costroid's own results before the pack's are joined, so a pack's failure is never demoted.
- **Which rules are demoted in practice.** A rule is demoted only where it fails at `serve`, and of the six only `pr_budget` can fail there. `serve`'s answer covers a pull request, not one run, so it concludes each of the five run rules `unproven` with reason `no_run_rows`, whatever stands behind the spend, the gateway's attested count included: none of them fails, is recorded or is demoted. `pr_budget` fails at `serve` on the agent's own counts when they show the pull request over its budget, and on the gateway's attested count when that count does, since the rule is held to the larger of the rows' USD list cost and the count. It passes only on the gateway's attested count or a provider's report.

Demotion is on unless the base branch turns it off. The key is read at the base, as every setting is, so a pull request cannot turn it on for itself:

```yaml
policy:
  pr_budget_usd: 20
  auto_demote: false
```

A demotion shows in one place: the verdict `serve` answers at `POST /v1/pulls/{pr}/verdict`, and whatever reads that answer. Nothing a reviewer sees on a pull request changes. The check and the comment the report job posts are not changed by a demotion: the report reads a rows file, never asks `serve`, and demotes no rule. The check the approve button posts repeats the report's own result object with the approval, so it shows no demotion either. On a pull request, a rule that a ledger has demoted still fails the check the report posts.

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
  "evaluated_at": "2026-11-12T09:00:00Z",
  "warn_only": { "active": false, "reason": "ended", "started": "2026-11-02T09:00:00Z", "ends": "2026-11-09T09:00:00Z" },
  "results": [
    { "rule": "run_budget", "outcome": "fail", "reason": "over_limit", "limit_usd": "0.001", "cost_usd": "0.001619725" }
  ],
  "joinable_rows": 0
}
```

A result can carry `limit_usd`, `cost_usd`, `baseline_usd` and `p95_usd`, which are amounts, `limit_percent`, `limit_factor`, `baseline_runs`, and `models`, the models off the list. `cost_usd` is the run's cost, except under `pr_budget`, where it is the pull request's total. `p95_usd`, `limit_factor` and `baseline_runs` are `run_p95`'s alone. The comment's copy of the block drops `limit_usd`, `cost_usd`, `baseline_usd` and `p95_usd` when it withholds amounts. The check run's block keeps them. A block whose verdict holds a pack, under `policy.pack`, is `costroid.result/0.12`, and its `policy.results` can hold Costroid's `customer_pack` result with `line`. Any other block whose verdict holds a `run_p95` result is `costroid.result/0.11`. Every other block is `costroid.result/0.10` when it states an estimate and `costroid.result/0.9` when it does not, as before. A block that holds a receipt, under `receipts`, is `costroid.result/0.13`, unless it holds a demoted result. A block whose verdict holds a result with `demoted` is `costroid.result/0.14`, whatever else it holds, and only a ledger's verdict holds one. The action's `conclusion` output carries the check's conclusion, and its `result` output the whole object.

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

It prints one sentence per line, in the comment's words: each rule the last line does not name, then the verdict sentence, which is the title the check gets. When `costroid.yml` names a policy pack, the command reads it from the path it names beside `base.yml`, or from `--pack`:

```sh
git show origin/main:.costroid/policy.rego > base.rego
costroid policy --rows rows.jsonl --config base.yml --pack base.rego
```

The pack runs as the report runs it, and the lines list its results. `--json` prints the verdict as the machine block carries it. `--workflow-added` and `--at` take the period's start and the time of the check, both as RFC 3339, so the command gives the same verdict the check gave. Without `--workflow-added` the period counts as not ended, as it does in the report.

`--history` takes the result objects of the pull request's earlier check runs, one object per line, each with its `check_run_id`. The command reads the pull request's total and the count that ends the warn-only period from them, as the report reads them from the check runs, with the rows as this run's report. A line that is not a JSON object, or that names no check run, refuses the file with its line number. An object the report would skip, such as one that withholds its amounts, is skipped. This collects the objects on one commit:

```sh
gh api repos/OWNER/REPO/commits/SHA/check-runs \
  --jq '.check_runs[] | select(.name == "costroid") | .output.text | ltrimstr("```json\n") | rtrimstr("\n```") | fromjson | tojson' \
  >> earlier.jsonl
```

`--baseline` takes the result objects of the check runs on the heads of the pull requests merged into the base branch, in the same form, and the command reads the baseline from them as the report does. An object the report would not read leaves no baseline, with the report's warning. `--history` without `--baseline` is refused when the `costroid.yml` named sets `max_increase_usd`, `max_increase_percent` or `max_p95_factor`: before commit `df493ca` of 2026-11-21 `--history` gave those rules their baseline, and the command does not conclude `no_baseline` without saying why. This collects the objects of the latest 30 merges into `main`, leaving out pull request `NUMBER`, the one the check is about:

```sh
gh api 'repos/OWNER/REPO/pulls?state=closed&base=main&sort=updated&direction=desc&per_page=100' \
  --jq '[.[] | select(.merged_at != null and .number != NUMBER)] | sort_by(.merged_at) | reverse | .[:30][] | .head.sha' |
  while read -r sha; do
    gh api "repos/OWNER/REPO/commits/$sha/check-runs?check_name=costroid&filter=all&per_page=100" \
      --jq '.check_runs[] | select(.status == "completed" and .app.slug == "github-actions") | .output.text | ltrimstr("```json\n") | rtrimstr("\n```") | fromjson | tojson'
  done > merged.jsonl
costroid policy --rows rows.jsonl --config base.yml --history earlier.jsonl --baseline merged.jsonl
```

It exits 0 on `success` or `neutral`, 1 on `failure`, 2 when its command line is wrong, and 125 when it cannot read a file.
