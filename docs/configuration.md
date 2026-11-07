# Configure a repository

`costroid.yml` sits at the repository's root. Every key is optional, and a repository with no file gets the defaults. The report reads it at the head of the pull request's base branch, or a GitLab merge request's target branch, when it runs, so a change cannot alter its own setting.

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Costroid/costroid/main/costroid.schema.json
comment:
  show_cost: false
```

| Key                 | Default | What it does                                                                                                                                               |
| ------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `comment.show_cost` | `true`  | Off, a public repository's comment and job summary show the verdict and the grades, without amounts or token counts. The rows, the check run's machine block and the action's outputs keep them. A private repository always prints them, and a GitLab project set to internal counts as private |
| `policy.run_budget_usd` | none | The budget each run is held to, in USD, such as `5` or `"5.00"`. Unset, the rule does not run |
| `policy.max_run_cost_usd` | none | A ceiling on one run's cost at list, in USD. Unset, the rule does not run |
| `policy.max_increase_usd` | none | How far a run may cost more than the median of this agent's 10 or more earlier runs, in USD. Unset, the rule does not run |
| `policy.max_increase_percent` | none | The same increase, in percent of the median, such as `25` or `"12.5"`. Unset, the rule does not run |
| `policy.pr_budget_usd` | none | The budget a pull request's runs are held to together, in USD. Unset, the rule does not run |
| `policy.monthly_budget_usd` | none | The budget the repository's runs are held to over a calendar month, in USD. It reads a ledger's month to date only. Unset, the rule does not run |
| `policy.models` | none | The models a run may use: exact model ids, or prefixes that end in `*`. Unset, the rule does not run |
| `policy.enforce` | `false` | `true` ends the warn-only period now, so a run over a limit fails the check |

An amount is a whole number, or a quoted decimal of at most 12 fractional digits. A quoted amount stays the decimal it reads as. An unquoted `0.1` is a YAML float, whose text and value can differ, so it is a problem. [`docs/policy.md`](policy.md) says what each rule concludes, which rules need figures the check does not read yet, and when the warn-only period ends.

```yaml
policy:
  run_budget_usd: 5
  max_run_cost_usd: "2.50"
  pr_budget_usd: 20
  max_increase_percent: 50
  models:
    - claude-sonnet-5-5
    - "gpt-5*"
```

`comment.show_cost` is `true` or `false`. A YAML 1.1 word such as `yes` or `no` is read as it meant, and the report's log names it so it can be rewritten. Any other value cannot be read: a repository not known to be private then withholds amounts, and the comment says the setting could not be read.

The schema also names `policy_pack`, `budget_owner`, `assumptions`, `catalog` and `agents`. They are reserved: this release accepts them and reads none of them. `costroid report` prints a warning in the job log for each reserved key it finds, and for each key it does not know, and still posts.

## Check a file

```sh
costroid validate costroid.yml
```

It prints `costroid.yml: valid` and exits 0, or names each problem and exits 1. A misspelled key is a problem. With no argument it reads `costroid.yml` in the current directory, and a missing file there is not an error. It exits 125 when it cannot read a file you named.

The schema is [`costroid.schema.json`](../costroid.schema.json), and the binary embeds the same file, so `validate` works offline.
