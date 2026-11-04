# Configure a repository

`costroid.yml` sits at the repository's root. Every key is optional, and a repository with no file gets the defaults. The report reads it from the pull request's base commit, or a GitLab merge request's, so a change cannot alter its own setting.

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Costroid/costroid/main/costroid.schema.json
comment:
  show_cost: false
```

| Key                 | Default | What it does                                                                                                                                               |
| ------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `comment.show_cost` | `true`  | Off, a public repository's comment shows the verdict and the grades, without amounts. The rows and the check run's machine block keep them. A private repository always prints them, and a GitLab project set to internal counts as private |

The schema also names `policy_pack`, `budget_owner`, `assumptions`, `catalog` and `agents`. They are reserved: this release accepts them and reads none of them. `costroid report` prints a warning in the job log for each reserved key it finds, and for each key it does not know, and still posts.

## Check a file

```sh
costroid validate costroid.yml
```

It prints `costroid.yml: valid` and exits 0, or names each problem and exits 1. A misspelled key is a problem. With no argument it reads `costroid.yml` in the current directory, and a missing file there is not an error. It exits 125 when it cannot read a file you named.

The schema is [`costroid.schema.json`](../costroid.schema.json), and the binary embeds the same file, so `validate` works offline.
