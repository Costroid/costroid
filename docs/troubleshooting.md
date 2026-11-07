# Troubleshoot a run

Every line Costroid prints starts with `costroid:`. This page lists each one with what it means and what to do. A word in angle brackets stands for a value the line fills in, quotes included.

## Installing the binary

The action installs the binary in every stage. In stages `report` and `ingest` it fetches the rows first. A line here other than the first ends the step with exit code 1.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: stage is empty: set it to one of the stages below` | The step names no `stage`. The action has no default stage, because `all` gives the agent's job the report's permissions. Six lines follow, one per stage, each with what it does | Set `stage` as the recipes do: `run` or `read` in the agent's job, and `ingest` and `report` in jobs of their own |
| `costroid: stage '<stage>' is not one of the stages below` | `stage` holds a value the action does not have. Stage names are lower case | The same |
| `costroid: the costroid-rows artifact holds <count> entries besides the rows and the run object, so nothing was installed: <entries>` | In stage `report` or `ingest`, the artifact from the agent's job holds a file or a folder besides `costroid-rows.jsonl` and `costroid-rows.run.json`, or one of those two is not a plain file. The `run` and `read` stages keep those two files and nothing else, so a step in the agent's job added the rest. Nothing from the artifact is used, and the report does not post. The line names up to ten entries, with any character outside letters, digits, `.`, `_`, `/` and `-` shown as `?` | Find the step in the agent's job that wrote into the artifact, and keep that job at `contents: read` with no other upload named `costroid-rows` |
| `costroid: installing <version>, the release at this action's commit, checked by its signature` | The action found the release its own commit belongs to, and cosign accepted the signature on its `SHA256SUMS` | None |
| `costroid: the action's ref '<ref>' is not a commit SHA: pin Costroid/costroid/action to the full commit SHA of a release tag, as the recipes do, or set both version and sha256` | Neither the action's checkout path nor its ref holds a commit. The action reads its commit from the path the runner checked it out to, `_actions/Costroid/costroid/<commit>/action`, then from `github.action_ref`. A workflow that names the action by a branch, a tag or a local path gives neither. An action that calls this one from its own steps gets an empty ref, so the path has to name the commit | Pin the action to a release's commit, as [`docs/recipes.md`](recipes.md#pins) lists, or pass `version` and `sha256` |
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

The GitLab template installs the release its `COSTROID_VERSION` names, the release the GitHub recipes pin. It downloads cosign at the version and SHA-256 that `sigstore/cosign-installer` v4.1.2 pins, checks the release's `SHA256SUMS` by its signature, and then the binary against its line. No checksum is set by hand. Each of its lines starts with `costroid:`, and each but the first ends the job with exit code 1.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `installed <version>, checked by its signature` | cosign accepted the signature on `SHA256SUMS`, and the binary matched its line | None |
| `could not download cosign <version>` | The runner could not reach github.com | Check the runner's egress, then run the job again |
| `cosign <version> does not match its pinned SHA-256` | The downloaded cosign is not the one the template pins | Do not run the job again on this runner until the download is explained |
| `v<version> has no SHA256SUMS`, `v<version> has no signature for SHA256SUMS` | As in the action's table above | The same |
| `the signature on SHA256SUMS of v<version> does not verify, so nothing was installed` | As in the action's table above | The same |
| `v<version> has no costroid-linux-amd64` | The release has no `amd64` Linux binary, or github.com did not answer | Run the job again. If it repeats, set `COSTROID_VERSION` to another release |
| `the binary for <version> does not match its line in the signed SHA256SUMS` | The downloaded binary is not the one the signed checksum names | Do not install this binary. Report it as [`SECURITY.md`](../SECURITY.md) says |

## Running the agent

The `run` stage. A line from the action's own checks ends the step with exit code 125, before the agent starts.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: agent must be claude or codex, got '<agent>'` | The `agent` input names another agent | Set `agent` to `claude` or `codex` |
| `costroid: the run stage needs these inputs, which are empty: <inputs>` | Each input the line names is empty. One line names all of them | Set each. Write `prompt` into the recipe as a literal. An empty `api-key` means the secret did not reach the step: set `ANTHROPIC_API_KEY` or `CODEX_API_KEY` on the repository. A fork's pull request receives no secrets. Set `minimum-agent-version` to the version the recipe installs |
| `costroid: agent-version must be a release number, got '<version>'` | `agent-version` is not a number such as `2.1.316` | Drop the leading `v`, or leave it empty for the version the recipes install |
| `costroid: <agent> is not on this runner, so the action installs <package>` | No step before this one installed the agent, so the action runs `npm install --global` at `agent-version`, or at the version the recipes install when it is empty | None. Set `agent-version` to install another version |
| `costroid: could not install <package>` | npm failed, or the runner has no npm. npm's own lines above it say why | Use a runner with Node.js, or install the agent in a step before this one, as the recipes do |
| `costroid: could not read the version of <agent>` | `--version` printed no version | Check the agent's install step |
| `costroid: <agent> <version> is older than the minimum <version>` | The installed agent is older than the floor | Raise the install step's version, or lower the floor to a version the recipe records |
| `costroid: the agent's output is in <path> on this runner, not in this log. show-output: true prints it.` | The agent's own stream went to a file on the runner, so the log does not show its reasoning or replies | None. On a private repository, set `show-output: true` to see it |
| `costroid: run needs <arguments>` | `costroid run` was called without the arguments the line names, from `--budget-usd`, `--out` and an agent command after `--` | Pass each one the line names |
| `costroid: --budget-usd must be above 0` | The budget is zero or negative | Pass a positive amount |
| `costroid: --credential takes the key's id, never the key: this value reads like a key` | The credential looks like a provider key | Pass the key's id from the provider's console, never the key |
| `costroid: run: no adapter for <command>: costroid runs claude and codex` | The command after `--` is not `claude` or `codex` | Run one of those two |
| `costroid: <agent> is not on PATH: install it with <command>` | The command after `--` is `claude` or `codex`, and no program of that name is on `PATH`. The line names the npm command that installs the version the recipes install | Run that command, or install the agent in a step before this one, as the recipes do |
| `costroid: run refused, reason <reason>: <error>` | Costroid did not start the agent and wrote no rows. The reason `concurrent_run` means another `costroid run` of Claude Code holds the same config directory. The first run would count the second's session as nested, and the second would count it again | Run one agent at a time per config directory, or give each run its own `CLAUDE_CONFIG_DIR` |
| `costroid: warning: 1 line of the agent's stdout was not read: <error>` | A line the agent printed was not the JSON Costroid reads. The run went on. Blank lines are skipped and not counted. The line comes once, after the agent's output ends | Check that the agent runs with the flags the recipe passes. A line a wrapper script prints lands here too |
| `costroid: warning: <count> lines of the agent's stdout were not read; the first: <error>` | The same, for more than one line. The error is the first line's | The same |
| `costroid: price the run: <error>` | The run's tokens could not be priced, so the run wrote no rows | Report it with the agent's version |
| `costroid: reason reader_refused: <error>` | The reader refused the agent's stream, so the run wrote no rows. The rows file is empty, and the object beside it carries the reason and the error's first line, which the report shows. The error `claude-code: the stream held no result object and no session file was found` means Claude Code ended before it reported usage, and wrote no session file | Read the agent's output file on the runner, or rerun with `show-output: true` on a private repository |

## The run's summary

`costroid run` and `costroid ingest-run` end with these lines. They state what was recorded, and none of them stops the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: <agent>, <count> requests` | How many requests or turns the rows cover, by the grain the agent reported | None |
| `costroid: <agent>, one run, <count> models` | The agent reported one total per model for the whole run | None |
| `costroid: <count> rows in <path>, catalog <version>` | Where the rows are, and which price catalog priced them | None |
| `costroid: USD <amount> at list, <label>` | The run's cost at list price, with its accuracy label, for example `List-equivalent, not billed`. A run that wrote no rows measured nothing, so it prints no such line | None |
| `costroid: budget USD <amount>, <grade>: <note>` | The budget, and how it was enforced, for example `agent-flag: client-side estimate, can pass the cap` | None. [`docs/adapters.md`](adapters.md) says what each grade means |
| `costroid: <grade>: <note>` | How a run Costroid did not start was enforced, from `ingest-run` | None |
| `costroid: stopped the agent at the cap` | Costroid stopped the agent when its cost reached the budget. The step exits 124 | Raise `budget-usd` if the run needed more |
| `costroid: over budget by USD <amount>` | The run cost more than its budget. A client-side cap can be passed | None. The grade says how far the cap holds |
| `costroid: warning: BillingAccountId reads unknown: pass --billing-account or set COSTROID_BILLING_ACCOUNT` | The rows name no billing account | Set `billing-account`, as the recipes do |
| `costroid: warning: the rows state no pull request: <error>` | The CI event file could not be read, so the rows carry no pull request | Run the recipe on `pull_request` |
| `costroid: warning: reason <reason>: <sentence>` | The run wrote no rows, and the rows file is empty. The reason `no_usage` means the agent ended and reported no usage. The reason `agent_error` means the agent ended in an error before it reported usage, and the sentence names the error's class and its fix, as [the classes below](#agent-errors) list | For `no_usage`, read the agent's output: a run that failed before its first request reports none. For `agent_error`, the fix the line names |
| `costroid: warning: the agent ended with <class>: <fix>; the rows hold the requests it made before` | The agent ended in an error after some requests. The rows hold those requests | The fix the line names |
| `costroid: warning: the run ended inside a turn with 1 model request in flight, sent at <time>: Codex writes usage only when a request completes, so that request in flight is not counted and its tokens reach no row` | Costroid stopped Codex after a tool's output went to the model and before the model's answer. The rows hold every request that completed | None. The provider still bills that request, so reconciliation shows it |
| `costroid: warning: the run ended inside a turn, and the rollout files show no model request in flight, so the rows hold every request they show sent` | Costroid stopped Codex while a tool ran, so no model request was lost | None |
| `costroid: warning: the run's result object was not written: <error>` | The rows were written, and the object beside them, which carries the reason for a run that wrote no rows, was not | Make the rows file's directory writable. The run never writes through a link the agent left at that path |
| `costroid: warning: <warning>` | The agent's stream or files were incomplete. The rows were written, and the warning says what they lack | Read the warning. Most mean the run ended early |

### Agent errors

Costroid reads an agent error's class from the agent's own fields, never from its text: Claude Code's `is_error` on the result and `error` on the assistant message. The report shows the class beside the figures, or as the reason `agent_error` when the run wrote no rows.

| Class | Meaning | Fix |
| ----- | ------- | --- |
| `authentication_failed` | The provider refused the API key | Check the API key the agent runs with, the `api-key` input in the action |
| `billing_error` | The provider refused the request for the account's billing | Check the credit balance of the provider account the API key belongs to |
| `rate_limit` | The provider limited the key's request rate | Run the job again later, or raise the API key's rate limit with the provider |
| `invalid_request` | The provider refused a request as invalid | Check the model name and the arguments the agent runs with |
| `server_error` | The provider failed the request | Run the job again |
| `unknown` | The agent stated an error in no class this list names | Run the agent alone with the same arguments to read its error |

## Reading a claude-code-action run

The `read` stage. A line here ends the step with exit code 125.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: execution-file is empty: pass the claude-code-action step's execution_file output` | The step got no file | Pass the action step's `execution_file` output, as the recipe does |
| `costroid: execution-file <path> does not exist` | The action wrote no file, often because it failed early | Read the action step's log |
| `costroid: could not read the version of claude` | `claude` is not on the path | Keep the recipe's install step |
| `costroid: claude <version> is older than the minimum <version>` | The installed Claude Code is older than the floor | Raise the install step's version |
| `costroid: ingest-run needs --execution-file and --out` | `costroid ingest-run` was called without one of them | Pass both |
| `costroid: reason reader_refused: <error>` | The execution file is not the array of messages Costroid reads, so no rows were written. The rows file is empty, and the object beside it carries the reason and the error's first line, which the report shows | Report it with the error line and the claude-code-action version |

## Sending rows to a ledger

The `ingest` stage. Each line here is a warning annotation, and the job still passes: a ledger or platform outage never blocks a merge.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: ingest-url and platform-url are empty, so no rows were sent` | Neither a ledger nor Costroid Platform is named | Set `COSTROID_INGEST_URL` or the `platform-url` input, or drop the job |
| `costroid: platform-token is empty, so no rows were sent to Costroid Platform` | `platform-url` is set and the integration token is not | Pass the token's secret as `platform-token`. [`docs/export.md`](export.md#costroid-platform) says which scopes it needs |
| `costroid: installation-id is empty, so no rows were sent to Costroid Platform` | `platform-url` is set and the installation is not | Set `installation-id`, as [`docs/export.md`](export.md#from-the-action) shows |
| `costroid: Costroid Platform did not take the rows` | `costroid export` refused the batch or could not send it. The line above it says why | Read that line. [Sending rows to Costroid Platform](#sending-rows-to-costroid-platform) lists them |
| `costroid: the run left no rows` | The agent's job wrote no rows | Read the agent job's log |
| `costroid: this job has no id-token permission` | The job cannot ask for an OIDC token | Give the `ingest` job `id-token: write`, and that job alone |
| `costroid: this runner has no /usr/bin/curl or no /usr/bin/jq, so no rows were sent` | On a GitHub runner, the `ingest` stage calls the system's own `curl` and `jq` by those paths, so no program a step put on `PATH` handles the job's OIDC token | Use a runner image with both at those paths |
| `costroid: GitHub issued no OIDC token` | GitHub refused the token request | Run the job again |
| `costroid: the ledger refused the rows` | The ledger answered an error or could not be reached | Read the ledger's log. [`docs/serve.md`](serve.md) lists its refusals |

## Sending rows to Costroid Platform

`costroid export`, which the `ingest` stage runs when `platform-url` is set.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: batch <id>, <count> rows, sha256 <digest>: accepted for ingest` | The platform took the batch. It states the batch's verdict at `GET /exports/{batch_id}` | None |
| `costroid: batch <id>, <count> rows, sha256 <digest>: already held, so nothing changed` | These bytes were sent before under this batch id | None |
| `costroid: <path> holds no rows, so nothing was sent` | The rows file is empty | Read the agent job's log |
| `costroid: warning: Costroid Platform refused <batch>: <status> <code>. Nothing in the rows is wrong, and <path> is kept to send again` | 402 `PLAN_WRITES_REFUSED`: the tenant's plan does not allow writes at present. 429 and a `QUOTA_` code: a quota is spent, and the line adds the seconds to wait. The command exits 0 | Send the rows again later. The artifact keeps them for 7 days |
| `costroid: warning: Costroid Platform's export quota is <state>` | `approaching` past 80% of a quota, or `exhausted` | Send fewer batches, or wait for the next UTC day |
| `costroid: Costroid Platform refused <batch>: <status> <code>` | The platform refused, and stored nothing. The command exits 1 | The contract's [refusals](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#refusals) say what each code means |
| `costroid: COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform` | The variable is empty or holds another kind of key | Pass the integration token the platform showed |
| `costroid: this job has no id-token permission, so no run token can name the run: give the job id-token: write` | On GitHub Actions the batch goes with the job's OIDC token, and the job cannot ask for one | Give the `ingest` job `id-token: write` |
| `costroid: outside GitHub Actions, export needs --batch-id` | Only a GitHub Actions run gives a batch id | Pass `--batch-id` |

## Posting the report

`costroid report` exits 0 once it finds a CI system, whatever the host answers, so a failed post never fails the job.

The action's `agent-result` input passes the agent job's result to the report, as `needs.<job>.result` gives it. When that job was skipped and left no rows, the comment says the agent did not run on the pull request, with the reason `agent_skipped`, in place of a report with no rows.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: check run <id>, conclusion <conclusion>` | The `costroid` check run was posted with the policy's conclusion: `success`, `neutral` or `failure` | None. [`docs/policy.md`](policy.md) says how each is reached |
| `costroid: comment on pull request <number>` | The sticky comment was posted or edited | None |
| `costroid: report needs --rows` | No rows file was named | Pass `--rows` |
| `costroid: report refused: neither GitHub Actions nor GitLab CI was detected` | The command ran outside CI. It exits 125 | Run it in a job |
| `costroid: not posted: <variable> is not set` | The job has no token: `GITHUB_TOKEN` on GitHub, `COSTROID_GITLAB_TOKEN` on GitLab | On GitHub, keep the action's `github-token` default. On GitLab, create the token as [`docs/recipes.md`](recipes.md#give-the-report-a-token) says |
| `costroid: not posted: no head commit: GITHUB_SHA and the event file name none` | The job is not on a commit Costroid can post to | Run the recipe on `pull_request` |
| `costroid: not posted: this pipeline is for a fork's merge request` | GitLab ran the job for a fork | None. Forks get no note |
| `costroid: not posted: <error>` | Nothing was posted, for the reason given | Read the reason |
| `costroid: check not posted: <error>` | The check run was refused. A fork's pull request gets a read-only token, so this is expected there | Give the `report` job `checks: write`, as the recipes do |
| `costroid: comment not posted: <error>` | The comment was refused | Give the `report` job `pull-requests: write` |
| `costroid: warning: check run <id> does not name itself: <error>` | The check run was posted, and the second request that writes its id into its result object was refused. The object has no `check_run_id`, and the comment still names the check run | Give the `report` job `checks: write`, as the recipes do |
| `costroid: warning: the pull request's total is left out: <error>` | The report could not read every `costroid` check run on the pull request's commits within 6 seconds, so the comment shows this run's figures and no total across the pull request. A partial total would read as a smaller cost | Give the `report` job `checks: write` and `pull-requests: write`, as the recipes do |
| `costroid: warning: the run's result object <path> is ignored: <error>` | The object `costroid run` writes beside the rows, `costroid-rows.run.json` beside `costroid-rows.jsonl`, could not be read. The report shows no reason for an empty rows file | Keep both files in the artifact, as the action and the recipes do |
| `costroid: warning: the run's reason <reason> is not one this release knows, so it is ignored` | A newer `costroid run` wrote a reason this report does not know | Report with the release that ran the agent |
| `costroid: warning: the run's reason <reason> is ignored, because only an empty rows file carries one` | The rows file holds rows, or could not be read, so it says more than the run's reason | None |
| `costroid: warning: the agent's error class <class> is not one this release knows, so it is ignored` | The run's object names an error class this report does not list | Report with the release that ran the agent |
| `costroid: warning: the agent's result <result> is not success, failure, cancelled or skipped, so it is ignored` | The action's `agent-result` input, or `--agent-result`, holds a value GitHub Actions does not give a job's result | Pass `${{ needs.<job>.result }}` of the agent's job |
| `costroid: warning: the agent's job reads skipped, and the rows file is not empty, so the result is ignored` | The report was told the agent's job was skipped, and the rows file holds rows or a line that is not one. The report shows what the file holds | Pass the result of the job that wrote the rows |
| `costroid: warning: costroid.yml is read at the event's base commit <commit>, not the base branch's head: <error>` | The report could not read the head of the pull request's base branch, so it read `costroid.yml` and the workflow's history at the merge-base the event names. A setting changed on the base branch since the pull request last updated does not apply to it | Give the `report` job `contents: read`, as the recipes do |
| `costroid: warning: costroid.yml: <error>` | `costroid.yml` at the base branch's head could not be read or parsed. A public repository's comment then withholds amounts | Give the `report` job `contents: read`, and run `costroid validate` on the file |
| `costroid: warning: policy: read <file> at <commit>: <error>` | The policy could not read its settings at that commit, so no rule ran and the check concludes `neutral` | Give the `report` job `contents: read` |
| `costroid: warning: policy: GITHUB_WORKFLOW_REF names no workflow file of this repository` | The report could not tell which workflow it runs from, so the warn-only period counts as running and a rule that fails warns | Run the report from the workflow file in `.github/workflows/`, as the recipes do. `policy.enforce: true` ends the period without it |
| `costroid: warning: policy: no base commit to read the workflow's history at` | The event names no base commit, so the warn-only period counts as running | Run the recipe on `pull_request` |
| `costroid: warning: repository visibility: <error>` | The repository's visibility could not be read, so the comment withholds amounts | Give the `report` job `contents: read` |
| `costroid: warning: <error>` | The event file or the rows file could not be read. The check and the comment still post and say so | Read the reason |

## costroid.yml

`costroid report` names each key it does not read. None of these stops the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: warning: costroid.yml: <key> is reserved and not read yet, so it has no effect` | The schema names the key for a later release | None. [`docs/configuration.md`](configuration.md) lists the reserved keys |
| `costroid: warning: costroid.yml: <key> is not a key Costroid reads, so it has no effect` | The key is misspelled or does not exist | Correct it. `costroid validate` names it as well |
| `costroid: warning: costroid.yml: a key with characters outside A-Z, a-z, 0-9, _, . and - is not one Costroid reads, so it has no effect` | The same, for a key whose name the log does not print | The same |
| `costroid: warning: costroid.yml: comment.show_cost is a YAML 1.1 word and reads as <value>; write true or false` | `show_cost` reads `yes`, `no`, `on`, `off` or the like, which YAML 1.2 reads as a string. The report reads it as the old word meant | Write `true` or `false` |
| `costroid: warning: policy: policy.<key> must be a whole number or a quoted decimal of at most <digits> fractional digits, such as 5 or <amount>` | An amount is a YAML float such as `0.1`, is negative, or is a quoted string that is not a decimal of at most 12 fractional digits. No rule ran, and the check concludes `neutral` | Write a whole number, or quote the amount. `costroid validate` names it as well |
| `costroid: warning: policy: policy.models must list distinct model ids, each of at most <n> characters, or prefixes that end in *` | `models` is empty, not a list, repeats an entry, or holds an entry that is not a model id. No rule ran | Correct the list. `costroid validate` names it as well |
| `costroid: warning: policy: policy.enforce must be true or false` | `enforce` holds another value. No rule ran | Set it to `true` or remove it |
| `costroid: warning: policy: costroid.yml names a policy key Costroid does not read` | A key under `policy` is misspelled or does not exist. No rule ran | Correct it. `costroid validate` names it |

## Other commands

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: <error>` | A command could not go on, and exits 125. The error says why | Read the error. `costroid help` lists the commands, and `costroid help <command>` gives one command's flags |
| `costroid: unknown command <name>` | No such command | Run `costroid help` |
| `costroid: validate takes one path` | `validate` got more than one file | Validate one file at a time |
| `costroid: lint-recipes needs at least one path` | No file to lint | Name a recipe or a directory |
| `costroid: <count> files, <count> rejected lines` | What `lint-recipes` checked. It exits 1 when a line was rejected | Fix each line it printed above |
| `costroid: <count> files, <count> with an agent: <count> high, <count> medium, <count> notes` | What `lint` judged. It exits 1 on a high finding, and on a medium one under `--strict` | Fix each finding it printed above. [`docs/lint.md`](lint.md) says what each rule asks |
| `costroid: warning: <count> repeated rows in <path> were counted once` | `reconcile` read the same row more than once | None |
