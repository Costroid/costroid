# Troubleshoot a run

Every line Costroid prints starts with `costroid:`. This page lists each one with what it means and what to do. A word in angle brackets stands for a value the line fills in, quotes included.

## Installing the binary

The action's first step installs the binary. A line here other than the first ends the step with exit code 1.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: installing <version>, the release at this action's commit, checked by its signature` | The action found the release its own commit belongs to, and cosign accepted the signature on its `SHA256SUMS` | None |
| `costroid: the action's ref '<ref>' is not a commit SHA: pin Costroid/costroid/action to the full commit SHA of a release tag, as the recipes do, or set both version and sha256` | The workflow names the action by a branch, a tag or a local path, so there is no commit to find a release for | Pin the action to a release's commit, as [`docs/recipes.md`](recipes.md#pins) lists, or pass `version` and `sha256` |
| `costroid: <commit> is not the commit of a release of Costroid/costroid: pin Costroid/costroid/action to the full commit SHA of a release tag, as the recipes do, or set both version and sha256` | No release tag points at the pinned commit, so no signed binary exists for it | The same |
| `costroid: could not list the tags of Costroid/costroid` | The runner could not reach github.com | Check the runner's egress, then run the job again |
| `costroid: v<version> has no SHA256SUMS` | The release has no checksum file, or github.com did not answer | Run the job again. If it repeats, pin another release |
| `costroid: v<version> has no signature for SHA256SUMS` | The release has no `SHA256SUMS.sigstore.json` | The same |
| `costroid: the signature on SHA256SUMS of v<version> does not verify, so nothing was installed` | cosign refused the signature: Costroid's release workflow did not make it at that tag and commit. cosign's own lines above it say why | Do not install this release. Report it as [`SECURITY.md`](../SECURITY.md) says |
| `costroid: SHA256SUMS of v<version> holds no line for costroid-linux-<arch>` | The release has no binary for this runner's architecture | Use an `amd64` or `arm64` Linux runner |
| `costroid: no release for <machine>` | The runner is neither `x86_64` nor `arm64` | The same |
| `costroid: set both version and sha256, or neither to install the release this action is pinned to` | One of the two inputs is set | Set both, or remove both |
| `costroid: version must be a release number, got '<version>'` | `version` is not a number such as `1.0.0-alpha.3` | Drop the leading `v` and any spaces |
| `costroid: sha256 must be 64 lowercase hex characters` | `sha256` is not a SHA-256 | Copy the line for your runner's binary from the release's `SHA256SUMS` |
| `costroid: the binary for <version> does not match the pinned SHA-256` | The downloaded binary is not the one the checksum names | Check that `sha256` is for the binary your runner uses, `costroid-linux-amd64` or `costroid-linux-arm64`. If it is, do not install this binary, and report it |

## Running the agent

The `run` stage. A line from the action's own checks ends the step with exit code 125, before the agent starts.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: agent must be claude or codex, got '<agent>'` | The `agent` input names another agent | Set `agent` to `claude` or `codex` |
| `costroid: prompt is empty` | The `prompt` input is empty | Write the prompt into the recipe as a literal |
| `costroid: budget-usd is empty` | No budget was given | Set `budget-usd` |
| `costroid: api-key is empty` | The secret did not reach the step | Set `ANTHROPIC_API_KEY` or `CODEX_API_KEY` on the repository. A fork's pull request receives no secrets |
| `costroid: minimum-agent-version is empty` | No version floor was given | Set `minimum-agent-version` to the version the recipe installs |
| `costroid: could not read the version of <agent>` | The agent is not installed, or `--version` printed no version | Keep the recipe's install step before this one |
| `costroid: <agent> <version> is older than the minimum <version>` | The installed agent is older than the floor | Raise the install step's version, or lower the floor to a version the recipe records |
| `costroid: the agent's output is in <path> on this runner, not in this log. show-output: true prints it.` | The agent's own stream went to a file on the runner, so the log does not show its reasoning or replies | None. On a private repository, set `show-output: true` to see it |
| `costroid: run needs --budget-usd, --out and an agent command after --` | `costroid run` was called without one of them | Pass all three |
| `costroid: --budget-usd must be above 0` | The budget is zero or negative | Pass a positive amount |
| `costroid: --credential takes the key's id, never the key: this value reads like a key` | The credential looks like a provider key | Pass the key's id from the provider's console, never the key |
| `costroid: run: no adapter for <command>: costroid runs claude and codex` | The command after `--` is not `claude` or `codex` | Run one of those two |
| `costroid: unreadable stream line: <error>` | A line of the agent's stream was not the JSON Costroid reads. The run goes on | Check that the agent runs with the flags the recipe passes |
| `costroid: price the run: <error>` | The run's tokens could not be priced, so the run wrote no rows | Report it with the agent's version |
| `costroid: claude-code: the stream held no result object and no session file was found` | Claude Code ended before it reported usage, and wrote no session file | Read the agent's output file on the runner, or rerun with `show-output: true` on a private repository |

## The run's summary

`costroid run` and `costroid ingest-run` end with these lines. They state what was recorded, and none of them stops the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: <agent>, <count> requests` | How many requests or turns the rows cover, by the grain the agent reported | None |
| `costroid: <agent>, one run, <count> models` | The agent reported one total per model for the whole run | None |
| `costroid: <count> rows in <path>, catalog <version>` | Where the rows are, and which price catalog priced them | None |
| `costroid: USD <amount> at list, <label>` | The run's cost at list price, with its accuracy label, for example `List-equivalent, not billed` | None |
| `costroid: budget USD <amount>, <grade>: <note>` | The budget, and how it was enforced, for example `agent-flag: client-side estimate, can pass the cap` | None. [`docs/adapters.md`](adapters.md) says what each grade means |
| `costroid: <grade>: <note>` | How a run Costroid did not start was enforced, from `ingest-run` | None |
| `costroid: stopped the agent at the cap` | Costroid stopped the agent when its cost reached the budget. The step exits 124 | Raise `budget-usd` if the run needed more |
| `costroid: over budget by USD <amount>` | The run cost more than its budget. A client-side cap can be passed | None. The grade says how far the cap holds |
| `costroid: warning: BillingAccountId reads unknown: pass --billing-account or set COSTROID_BILLING_ACCOUNT` | The rows name no billing account | Set `billing-account`, as the recipes do |
| `costroid: warning: the rows state no pull request: <error>` | The CI event file could not be read, so the rows carry no pull request | Run the recipe on `pull_request` |
| `costroid: warning: <warning>` | The agent's stream or files were incomplete. The rows were written, and the warning says what they lack | Read the warning. Most mean the run ended early |

## Reading a claude-code-action run

The `read` stage. A line here ends the step with exit code 125.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: execution-file is empty: pass the claude-code-action step's execution_file output` | The step got no file | Pass the action step's `execution_file` output, as the recipe does |
| `costroid: execution-file <path> does not exist` | The action wrote no file, often because it failed early | Read the action step's log |
| `costroid: could not read the version of claude` | `claude` is not on the path | Keep the recipe's install step |
| `costroid: claude <version> is older than the minimum <version>` | The installed Claude Code is older than the floor | Raise the install step's version |
| `costroid: ingest-run needs --execution-file and --out` | `costroid ingest-run` was called without one of them | Pass both |

## Sending rows to a ledger

The `ingest` stage. Each line here is a warning annotation, and the job still passes: a ledger outage never blocks a merge.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: ingest-url is empty, so no rows were sent` | No ledger is named | Set `COSTROID_INGEST_URL`, or drop the job |
| `costroid: the run left no rows` | The agent's job wrote no rows | Read the agent job's log |
| `costroid: this job has no id-token permission` | The job cannot ask for an OIDC token | Give the `ingest` job `id-token: write`, and that job alone |
| `costroid: GitHub issued no OIDC token` | GitHub refused the token request | Run the job again |
| `costroid: the ledger refused the rows` | The ledger answered an error or could not be reached | Read the ledger's log. [`docs/serve.md`](serve.md) lists its refusals |

## Posting the report

`costroid report` exits 0 once it finds a CI system, whatever the host answers, so a failed post never fails the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: check run <id>, conclusion neutral` | The `costroid` check run was posted | None |
| `costroid: comment on pull request <number>` | The sticky comment was posted or edited | None |
| `costroid: report needs --rows` | No rows file was named | Pass `--rows` |
| `costroid: report refused: neither GitHub Actions nor GitLab CI was detected` | The command ran outside CI. It exits 125 | Run it in a job |
| `costroid: not posted: <variable> is not set` | The job has no token: `GITHUB_TOKEN` on GitHub, `COSTROID_GITLAB_TOKEN` on GitLab | On GitHub, keep the action's `github-token` default. On GitLab, create the token as [`docs/recipes.md`](recipes.md#give-the-report-a-token) says |
| `costroid: not posted: no head commit: GITHUB_SHA and the event file name none` | The job is not on a commit Costroid can post to | Run the recipe on `pull_request` |
| `costroid: not posted: this pipeline is for a fork's merge request` | GitLab ran the job for a fork | None. Forks get no note |
| `costroid: not posted: <error>` | Nothing was posted, for the reason given | Read the reason |
| `costroid: check not posted: <error>` | The check run was refused. A fork's pull request gets a read-only token, so this is expected there | Give the `report` job `checks: write`, as the recipes do |
| `costroid: comment not posted: <error>` | The comment was refused | Give the `report` job `pull-requests: write` |
| `costroid: warning: costroid.yml: <error>` | `costroid.yml` at the base commit could not be read or parsed. A public repository's comment then withholds amounts | Give the `report` job `contents: read`, and run `costroid validate` on the file |
| `costroid: warning: repository visibility: <error>` | The repository's visibility could not be read, so the comment withholds amounts | Give the `report` job `contents: read` |
| `costroid: warning: <error>` | The event file or the rows file could not be read. The check and the comment still post and say so | Read the reason |

## costroid.yml

`costroid report` names each key it does not read. None of these stops the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: warning: costroid.yml: <key> is reserved and not read yet, so it has no effect` | The schema names the key for a later release | None. [`docs/configuration.md`](configuration.md) lists the reserved keys |
| `costroid: warning: costroid.yml: <key> is not a key Costroid reads, so it has no effect` | The key is misspelled or does not exist | Correct it. `costroid validate` names it as well |
| `costroid: warning: costroid.yml: a key with characters outside A-Z, a-z, 0-9, _, . and - is not one Costroid reads, so it has no effect` | The same, for a key whose name the log does not print | The same |

## Other commands

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: <error>` | A command could not go on, and exits 125. The error says why | Read the error. `costroid help` lists every command and flag |
| `costroid: unknown command <name>` | No such command | Run `costroid help` |
| `costroid: validate takes one path` | `validate` got more than one file | Validate one file at a time |
| `costroid: lint-recipes needs at least one path` | No file to lint | Name a recipe or a directory |
| `costroid: <count> files, <count> rejected lines` | What `lint-recipes` checked. It exits 1 when a line was rejected | Fix each line it printed above |
| `costroid: warning: <count> repeated rows in <path> were counted once` | `reconcile` read the same row more than once | None |
