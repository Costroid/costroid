# Configure a repository

`costroid.yml` sits at the repository's root. Every key is optional, and a repository with no file gets the defaults. The report reads it from the pull request's base commit, or a GitLab merge request's, so a change cannot alter its own setting.

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Costroid/costroid/main/costroid.schema.json
comment:
  show_cost: false
```

| Key                 | Default | What it does                                                                                                                                               |
| ------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `comment.show_cost` | `true`  | Off, a public repository's comment and job summary show the verdict and the grades, without amounts or token counts. The rows, the check run's machine block and the action's outputs keep them. A private repository always prints them, and a GitLab project set to internal counts as private |
| `policy.run_budget_usd` | none | The budget each run is held to, in USD, as a quoted decimal such as `"5.00"`. Unset, the rule does not run |
| `policy.max_run_cost_usd` | none | A ceiling on one run's cost at list, in USD, as a quoted decimal. Unset, the rule does not run |
| `policy.enforce` | `false` | `true` ends the warn-only period now, so a run over a limit fails the check |

Amounts are quoted, so `0.1` stays the decimal it reads as. An unquoted number is a problem. [`docs/policy.md`](policy.md) says what each rule concludes and when the warn-only period ends.

```yaml
policy:
  run_budget_usd: "5.00"
  max_run_cost_usd: "2.50"
```

The schema also names `policy_pack`, `budget_owner`, `assumptions`, `catalog` and `agents`. They are reserved: this release accepts them and reads none of them. `costroid report` prints a warning in the job log for each reserved key it finds, and for each key it does not know, and still posts.

## Check a file

```sh
costroid validate costroid.yml
```

It prints `costroid.yml: valid` and exits 0, or names each problem and exits 1. A misspelled key is a problem. With no argument it reads `costroid.yml` in the current directory, and a missing file there is not an error. It exits 125 when it cannot read a file you named.

The schema is [`costroid.schema.json`](../costroid.schema.json), and the binary embeds the same file, so `validate` works offline.
