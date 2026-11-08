# Troubleshoot a run

Every line Costroid prints starts with `costroid:`. This page lists each one with what it means and what to do. A word in angle brackets stands for a value the line fills in, quotes included.

## Installing the binary

The action installs the binary in every stage. In stages `report` and `ingest` it fetches the rows first. A line here other than the first and the refused artifact's line in stage `report` ends the step with exit code 1.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: stage is empty: set it to one of the stages below` | The step names no `stage`. The action has no default stage, because `all` gives the agent's job the report's permissions. Six lines follow, one per stage, each with what it does | Set `stage` as the recipes do: `run` or `read` in the agent's job, and `ingest` and `report` in jobs of their own |
| `costroid: stage '<stage>' is not one of the stages below` | `stage` holds a value the action does not have. Stage names are lower case | The same |
| `costroid: the costroid-rows artifact holds <count> entries besides the rows and the run object, so nothing was installed: <entries>` | In stage `ingest`, the artifact from the agent's job holds a file or a folder besides `costroid-rows.jsonl` and `costroid-rows.run.json`, or one of those two is not a plain file. The `run` and `read` stages keep those two files and nothing else, so a step in the agent's job added the rest. Nothing from the artifact is used, and no rows are sent. The line names up to ten entries, with any character outside letters, digits, `.`, `_`, `/` and `-` shown as `?` | Find the step in the agent's job that wrote into the artifact, and keep that job at `contents: read` with no other upload named `costroid-rows` |
| `costroid: the costroid-rows artifact holds <count> entries besides the rows and the run object, so the report reads none of it and fails its check: <entries>` | The same artifact, in stage `report`. The action deletes it unread and installs the binary from the release, as in every stage. The report then posts a `failure` check with the run reason `artifact_refused` and a sentence naming the same entries | The same |
| `costroid: GITHUB_OUTPUT is not set, so the report cannot be told the artifact was refused` | `install.sh` ran in stage `report` outside a GitHub Actions step, over such an artifact. A runner always sets `GITHUB_OUTPUT` | Run the action on a GitHub runner |
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
| `costroid: the run stage needs these inputs, which are empty: <inputs>` | Each input the line names is empty. One line names all of them | Set each. Write `prompt` into the recipe as a literal. An empty `api-key` means the secret did not reach the step: set `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` on the repository. A fork's pull request receives no secrets. Set `minimum-agent-version` to the version the recipe installs |
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
| `costroid: run: <flag> sends <agent>'s requests around the gateway, and a gateway cannot hold a run that routes around it; remove it` | Under `--gateway`, the command holds a flag that sends requests somewhere other than the gateway, or on a key other than the run's. Costroid asked the gateway nothing and started no agent. A `--settings` value the run cannot read as a JSON object stops it the same way, with `under a gateway the run reads --settings` in the line | Remove the flag: under a gateway the run sets the provider and its address itself. [Commands a gateway run refuses](gateway.md#commands-a-gateway-run-refuses) lists each flag |
| `costroid: warning: the settings file the run wrote for the agent holds the run's key and could not be removed: <error>` | Under `--gateway`, the run gave Claude Code a settings file that holds the run's key, and could not delete it after the agent ended. The error names the file. The rows are written as usual | Delete the file. The key in it stops working when the closing job deletes the key, or at the key's expiry. [Claude Code's settings file](gateway.md#claude-codes-settings-file) says what the file holds |
| `costroid: <agent> is not on PATH: install it with <command>` | The command after `--` is `claude` or `codex`, and no program of that name is on `PATH`. The line names the npm command that installs the version the recipes install | Run that command, or install the agent in a step before this one, as the recipes do |
| `costroid: run refused, reason <reason>: <error>` | Costroid did not start the agent and wrote no rows. The reason `concurrent_run` means another `costroid run` of Claude Code holds the same config directory. The first run would count the second's session as nested, and the second would count it again | Run one agent at a time per config directory, or give each run its own `CLAUDE_CONFIG_DIR` |
| `costroid: warning: --installation-id <value> is not github:<id> or a GitHub App installation's bare number, so the comment links no account` | The installation id is in another form, such as `github-owner:` | Set `github:<id>` or the installation's number |
| `costroid: warning: 1 line of the agent's stdout was not read: <error>` | A line the agent printed was not the JSON Costroid reads. The run went on. Blank lines are skipped and not counted. The line comes once, after the agent's output ends | Check that the agent runs with the flags the recipe passes. A line a wrapper script prints lands here too |
| `costroid: warning: <count> lines of the agent's stdout were not read; the first: <error>` | The same, for more than one line. The error is the first line's | The same |
| `costroid: price the run: <error>` | The run's tokens could not be priced, so the run wrote no rows | Report it with the agent's version |
| `costroid: reason reader_refused: <error>` | The reader refused the agent's stream, so the run wrote no rows. The rows file is empty, and the object beside it carries the reason and the error's first line, which the report shows. The error `claude-code: the stream held no result object and no session file was found` means Claude Code ended before it reported usage, and wrote no session file | Read the agent's output file on the runner, or rerun with `show-output: true` on a private repository |
| `domain not allowed: <host>`, in harden-runner's log when the job ends | A step of the agent's job called a host outside the job's `allowed-endpoints`, and harden-runner blocked it. A download, an install or the agent's request that failed before it follows from this line | If the step needs the host, add it as [Add an endpoint](recipes.md#add-an-endpoint) says. In the LiteLLM recipe, set the variable `COSTROID_GATEWAY_HOST` to the gateway's host |

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
| `costroid: execution-file and codex-home are both set: set the one for the agent's action` | The step got both inputs | Keep `execution-file` for `claude-code-action` or `codex-home` for `codex-action` |
| `costroid: codex-home <path> is not a directory: pass the codex-home the codex-action step was given` | The path names no directory, often because the `codex-action` step did not run | Pass the same `codex-home` as the `codex-action` step, and read that step's log |
| `costroid: ingest-run needs --from KIND=PATH and --out, with KIND one of <kinds>` | `costroid ingest-run` was called without a source or without `--out` | Pass both. `--execution-file F` and `--codex-home D` still name a source |
| `costroid: reason reader_refused: <error>` | The execution file is not the array of messages Costroid reads, so no rows were written. The rows file is empty, and the object beside it carries the reason and the error's first line, which the report shows | Report it with the error line and the claude-code-action version |

## Reading a gh-aw run

`costroid ingest-run --from gh-aw=DIR`, as [`docs/gh-aw.md`](gh-aw.md) shows. A line that ends the command does so with exit code 125.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: gh-aw <path> does not exist: the run uploaded no usage artifact, or its download failed` | The directory is not there, so nothing was read and no rows file was written. gh-aw's conclusion job uploads `usage`, so a run cancelled before that job left none | Read the run's artifacts list. Download `usage`, not `agent`, into a directory of its own |
| `costroid: gh-aw <path> is not a directory: download the run's usage artifact into a directory of its own` | The path names a file | Pass the directory `gh run download` wrote to |
| `costroid: reason reader_refused: <error>` | The artifact could not be read as gh-aw writes it, so no rows were written. The rows file is empty, and the object beside it carries the reason and the error, which the report shows. The errors are listed below | The fix beside the error |
| `costroid: gh-aw stated <count> AI credits for this run. Costroid does not convert credits.` | gh-aw's own figure for the run, in its own unit, apart from the USD figure | None |
| `costroid: warning: <count> requests in <file> are left out: the catalog holds no price for provider <provider>` | Records from a provider the catalog does not price, such as `gemini`, wrote no row | None. The rows hold every other request |
| `costroid: warning: <requests> Copilot served bill by subscription and the rest bill a key, so every row reads list_equivalent, the weaker claim` | The run mixed Copilot with a provider's key | None |
| `costroid: warning: <file> states no AI credits, so the run's credits are not stated` | The threat detection job made requests and stated no credits, so gh-aw's total for the run is unknown | None |

The errors after `reason reader_refused:` for a gh-aw run:

| Error | Meaning | Fix |
| ----- | ------- | --- |
| `gh-aw: the usage artifact holds no agent/token_usage.jsonl: download the usage artifact of a gh-aw run whose firewall was on` | The artifact holds no agent records. The firewall's proxy writes them, so a workflow with the firewall off writes none | Turn the firewall on in the workflow, or read a run that had it on |
| `gh-aw: the usage artifact holds <entry>, which gh-aw <version> does not write there` | The directory holds a file the named gh-aw release does not put in the usage artifact, so none of it was read. A newer gh-aw that adds a file gets this error too | Download into an empty directory. If the file is gh-aw's own, report it with the gh-aw version |
| `gh-aw: the usage artifact holds the directory <entry>, which gh-aw <version> does not write there` | The same, for a directory | The same |
| `gh-aw: the usage artifact's <entry> is not a plain file` | A link or another special file stands where gh-aw writes a file | Download into an empty directory |
| `gh-aw: request_id <id> appears in <file> and <file>` | Two records name one request, and a row's id is built from its request's | Report it with the gh-aw version |
| `gh-aw: <file> line <number>: <problem>` | A record breaks the token-usage schema in a field Costroid reads, so none of the run was read | Report it with the line's `_schema` |

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
| `costroid: batch <id>, <count> rows, sha256 <digest>: accepted for ingest` | The platform took the batch. The command then asks for its verdict for up to 60 s | None |
| `costroid: batch <id>, <count> rows, sha256 <digest>: already held, so nothing changed` | These bytes were sent before under this batch id | None |
| `costroid: <count> of <count> rows name run <run> of <repository>, so they went as token_verified and the rest as unverified` | On GitHub Actions a row goes as `token_verified` only when it names this run and this repository and its join is not `windowed`. A fork's rows, sent from the follow-up's run, all go as `unverified` | None. [`docs/export.md`](export.md#what-a-row-claims) says what each claim proves |
| `costroid: batch <id> ingested, <count> rows` | The platform stored the batch's rows | None |
| `costroid: Costroid Platform dropped <count> Tags entries outside costroid/, and stored the rows without them` | The rows carried tags outside the `costroid/` prefix, which the platform never keeps | None |
| `costroid: pull request <number> of <repository>: <link>` | The pull request's page on Costroid Platform, one line for each pull request the rows name | Open the link |
| `costroid: batch <id> failed on ingest: <code>. Costroid Platform stored none of its rows` | The platform took the batch and then refused it whole. The command exits 1 | The contract's [failed batches](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#failed-batches) say what each code means |
| `costroid: warning: batch <id> has no verdict after <seconds> s. The platform took batch <id>, and states its verdict at GET /exports/<id>` | The batch was still ingesting when the wait ended. The command exits 0 | Read the batch on Costroid Platform later |
| `costroid: warning: Costroid Platform refused <batch>: <status> <code>. The tenant holds no binding of <installation>: run costroid connect, as docs/connect.md shows, and <path> is kept to send again` | 409 `INSTALLATION_NOT_BOUND`: the integration token's tenant has not bound the owner the rows came through, or its binding was revoked or displaced. Nothing was stored. The command exits 0 | Add [`recipes/github-connect.yml`](../recipes/github-connect.yml), as [`docs/connect.md`](connect.md) shows, then run the agent's workflow again |
| `costroid: <path> holds no rows, so nothing was sent` | The rows file is empty | Read the agent job's log |
| `costroid: warning: Costroid Platform refused <batch>: <status> <code>. Nothing in the rows is wrong, and <path> is kept to send again` | 402 `PLAN_WRITES_REFUSED`: the tenant's plan does not allow writes at present. 429 and a `QUOTA_` code: a quota is spent, and the line adds the seconds to wait. The command exits 0 | Send the rows again later. The artifact keeps them for 7 days |
| `costroid: warning: Costroid Platform's export quota is <state>` | `approaching` past 80% of a quota, or `exhausted` | Send fewer batches, or wait for the next UTC day |
| `costroid: Costroid Platform refused <batch>: <status> <code>` | The platform refused, and stored nothing. The command exits 1 | The contract's [refusals](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#refusals) say what each code means |
| `costroid: COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform` | The variable is empty or holds another kind of key | Pass the integration token the platform showed |
| `costroid: this job has no id-token permission, so no run token can name the run: give the job id-token: write` | On GitHub Actions the batch goes with the job's OIDC token, and the job cannot ask for one | Give the `ingest` job `id-token: write` |
| `costroid: outside GitHub Actions, export needs --batch-id` | Only a GitHub Actions run gives a batch id | Pass `--batch-id` |

## Connecting to Costroid Platform

`costroid connect`, which binds the job's owner to the integration token's tenant. [`docs/connect.md`](connect.md) gives the job.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: <installation> is bound to tenant <tenant>, proven at <time>` | The platform took the job's proof, and the binding is current for 7 days after that time | None |
| `costroid: <installation> is bound to tenant <tenant>, proven at <time>, so no proof was sent` | The binding was proven less than 20 hours ago, so the command sent nothing | None |
| `costroid: Costroid Platform refused the proof for <installation>: <status> <code>. <sentence>` | The platform refused the proof and bound nothing. The command exits 1 | Do what the sentence says. [`docs/connect.md`](connect.md#what-connect-prints) lists each code's sentence |
| `costroid: Costroid Platform could not read the CI system's keys, so connect sends the proof again in <seconds> s` | The platform answered `INSTALLATION_PROOF_KEYS_UNAVAILABLE`. The command waits the platform's `Retry-After`, at most 120 seconds, or 60 seconds when it names no whole seconds, and sends the same proof once more | None. A second refusal, or a `Retry-After` past 120 seconds, ends the command with the refusal line above |
| `costroid: Costroid Platform refused the identity request: <status> <code>. <sentence>` | The platform refused the token before any proof was sent. The command exits 1 | `AUTH_TOKEN_INVALID`: create a token on Costroid Platform. `AUTH_TOKEN_SCOPE`: give it `identity:read` and `installations:write` |
| `costroid: this job has no id-token permission, so it cannot prove its owner: give the job id-token: write` | On GitHub Actions the proof is the job's OIDC token, and the job cannot ask for one | Give the connect job `id-token: write` |
| `costroid: no proof: <error>` | GitHub refused the job's request for its OIDC token | Run the job again |
| `costroid: COSTROID_PLATFORM_PROOF is empty, so this job has no proof: give the job an ID token of that name with aud <audience>` | On GitLab CI the job's `id_tokens` set no token of that name | Add the `id_tokens` block [`docs/connect.md`](connect.md#on-gitlab-ci) shows, with the audience the line names |
| `costroid: COSTROID_PLATFORM_PROOF does not hold an ID token: set it only through the job's id_tokens` | The variable holds something other than a signed token, often a value set by hand | Remove the variable from the project's settings, and keep the job's `id_tokens` |
| `costroid: GITHUB_REPOSITORY_OWNER_ID is <value>, not an owner's number` | The variable GitHub sets is absent or changed | Run the command in a GitHub Actions job, and do not set the variable by hand |
| `costroid: CI_PROJECT_NAMESPACE_ID is <value>, not a namespace's number` | The variable GitLab sets is absent or changed | Run the command in a GitLab CI job, and do not set the variable by hand |
| `costroid: CI_SERVER_HOST is <host>, and Costroid Platform binds a GitLab namespace only on gitlab.com, because a self-managed instance's namespace ids are not unique` | The job runs on a self-managed GitLab. The command sends nothing and exits 125 | None in this version. The contract takes proofs from GitLab.com only |
| `costroid: --installation-id is <id>, and this job runs under <installation>: a job proves only its own owner` | The flag names another owner than the job's | Drop `--installation-id` in CI, where the job's own variables name the owner |
| `costroid: --installation-id must be github-owner:<id> or gitlab-namespace:<id>, the ids a CI job proves` | The flag holds a GitHub App installation or another form | Pass the owner's or the namespace's id in one of the two forms |
| `costroid: outside GitHub Actions and GitLab CI, connect needs --installation-id, and only reads whether it is bound` | The command ran on a machine, with no installation named | Pass `--installation-id`, or run the command in CI |
| `costroid: <installation> has no binding to tenant <tenant> proven in the last 20 hours, and outside GitHub Actions and GitLab CI no proof exists: run connect in a scheduled job` | Outside CI, the command found no current binding and has no proof to send. It exits 125 | Run the command in a scheduled job, as [`docs/connect.md`](connect.md) shows |
| `costroid: the answer of Costroid Platform to the proof for <installation> shows no active binding` | The platform answered 200 with a body that is not an active binding of that installation | Report it with the release that ran |
| `costroid: connect needs --platform-url` | No platform was named | Pass `--platform-url https://app.costroid.com` |

## Posting the report

`costroid report` exits 0 once it finds a CI system, whatever the host answers, so a failed post never fails the job.

On GitLab CI, `--exit-on-verdict` makes the verdict the job's exit code: 1 on `failure`, 3 on `neutral`, 0 on `success`. [`docs/gitlab.md`](gitlab.md) says how the template turns it on.

The action's `agent-result` input passes the agent job's result to the report, as `needs.<job>.result` gives it. When that job was skipped and left no rows, the comment says the agent did not run on the pull request, with the reason `agent_skipped`, in place of a report with no rows.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: check run <id>, conclusion <conclusion>` | The `costroid` check run was posted with the policy's conclusion: `success`, `neutral` or `failure` | None. [`docs/policy.md`](policy.md) says how each is reached |
| `costroid: comment on pull request <number>` | The sticky comment was posted or edited | None |
| `costroid: the verdict is <conclusion>, so the report exits <code>` | `--exit-on-verdict` on GitLab CI: `failure` exits 1 and fails the pipeline, `neutral` exits 3 and shows a warning, `success` exits 0. A report that could not reach GitLab has no verdict and exits 3 | For a 1, read the note's verdict. For a 3 you did not expect, read the lines above it. [`docs/gitlab.md`](gitlab.md) lists what concludes `neutral` |
| `costroid: warning: --exit-on-verdict applies on GitLab CI only. On GitHub Actions the costroid check is the gate, so the report exits 0` | The flag was passed on GitHub Actions, where the `costroid` check concludes `failure` instead | Drop the flag, and require the check as [`docs/required-check.md`](required-check.md) says |
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
| `costroid: warning: the run's object states a figure of the agent's own in a unit or from a source this release does not know, so it is ignored` | The run's object states `agent_stated` in a unit or from an agent `result/v0.7` does not list, or with a quantity that is not a decimal string. The comment states no such figure | Report with the release that ran the agent |
| `costroid: warning: COSTROID_ARTIFACT_REFUSED_COUNT is <count>, which is fewer than the entries the artifact was refused for, so the report counts <count>` | The action refused the rows artifact, and the count it passed is not a whole number at or above the entries it named. The check still fails with the run reason `artifact_refused` | Report it as [`SECURITY.md`](../SECURITY.md) says: the action's install sets this count |
| `costroid: warning: the agent's result <result> is not success, failure, cancelled or skipped, so it is ignored` | The action's `agent-result` input, or `--agent-result`, holds a value GitHub Actions does not give a job's result | Pass `${{ needs.<job>.result }}` of the agent's job |
| `costroid: warning: the agent's job reads skipped, and the rows file is not empty, so the result is ignored` | The report was told the agent's job was skipped, and the rows file holds rows or a line that is not one. The report shows what the file holds | Pass the result of the job that wrote the rows |
| `costroid: warning: costroid.yml is read at the event's base commit <commit>, not the base branch's head: <error>` | The report could not read the head of the pull request's base branch, so it read `costroid.yml` and the workflow's history at the merge-base the event names. A setting changed on the base branch since the pull request last updated does not apply to it | Give the `report` job `contents: read`, as the recipes do |
| `costroid: warning: costroid.yml: <error>` | `costroid.yml` at the base branch's head could not be read or parsed. A public repository's comment then withholds amounts | Give the `report` job `contents: read`, and run `costroid validate` on the file |
| `costroid: warning: policy: read <file> at <commit>: <error>` | The policy could not read its settings at that commit, so no rule ran and the check concludes `neutral` | Give the `report` job `contents: read` |
| `costroid: warning: policy: GITHUB_WORKFLOW_REF names no workflow file of this repository` | The report could not tell which workflow it runs from, so the warn-only period counts as running and a rule that fails warns | Run the report from the workflow file in `.github/workflows/`, as the recipes do. `policy.enforce: true` ends the period without it |
| `costroid: warning: policy: no base commit to read the workflow's history at` | The event names no base commit, so the warn-only period counts as running | Run the recipe on `pull_request` |
| `costroid: warning: repository visibility: <error>` | The repository's visibility could not be read, so the comment withholds amounts | Give the `report` job `contents: read` |
| `costroid: warning: --budget-usd is empty, so the report states no cap. A variable that gives it, such as "$COSTROID_BUDGET_USD", reads empty where the job does not set it` | No result object of the run was read, `--budget-usd` was given, and the variable that gives it was empty or blank in this job. The report states no cap, never a cap of 0 | Drop `--budget-usd`: the report reads the cap from the run's result object, which `costroid run` and `costroid ingest-run --cap-usd` write beside the rows |
| `costroid: warning: --budget-usd <amount> is ignored, because the run's result object states a cap of USD <amount>, and the report states that one. The report reads the cap from that object, so its step needs no budget-usd` | The report's step was given another cap than the run recorded. The report states the run's | Drop `budget-usd` from the report's step, and give the cap once, to the step that runs or reads the agent |
| `costroid: warning: --budget-usd <amount> is ignored, because the run's result object states no cap, so the report states none: it states the cap the run or the read step was given` | The `read` step was given no cap, and the report's step was | On `claude-code-action`, give `budget-usd` to the `read` step, beside `claude-args`. Drop it from the report's step. Costroid holds no cap on an `openai/codex-action` run |
| `costroid: warning: the run's result object states a cap that is not an amount in USD above 0, so the report states no cap` | The result object beside the rows holds a cap in another currency, or an amount that is not above 0. The agent's job wrote that file, so the report does not take it | Read what wrote the object. `costroid run` and `costroid ingest-run` write a cap in USD |
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
| `costroid: wrote <file> from recipes/<recipe>, pinned to <release> at <commit>` | `init` wrote the workflow, with Costroid pinned to the release the binary belongs to | Run the commands it prints after it. [`docs/install.md`](install.md#write-the-workflow) says more |
| `costroid: wrote costroid.yml, with every key commented out` | `init` wrote the configuration, which sets nothing yet | To set a key, remove the `#` before it and before each key it sits under |
| `costroid: costroid.yml exists, and init left it as it is` | The repository has a configuration, and `init` never replaces it | None |
| `costroid: init: <file> exists: pass --force to replace it` | `init` wrote nothing, because the workflow would replace a file | Pass `--force`, or name another path with `--out` |
| `costroid: init: this is a development build, version <version>, which belongs to no release, so init pins nothing: <fix>` | The binary was built from a checkout, and `init` does not pin a commit that was never released | Install a release as the README shows, or pass the full commit SHA to pin with `--action-ref` |
| `costroid: init: <error>` | `init` could not read or write a file, or refused its arguments, and exits 125 | Read the error. `costroid help init` gives the flags |
| `costroid: init wrote nothing: recipes/<recipe> breaks a rule of lint-recipes, which is a defect in costroid` | The shipped recipe failed the linter, and `init` exits 1 | Report it as a bug, with the lines printed above it |
| `costroid: printed a patch for <file>, and wrote nothing` | `init --existing` printed the patch on stdout | Save it, and apply it with `git apply` from the repository's root |
| `costroid: printed a patch for <file> and .github/workflows/costroid-connect.yml, and wrote nothing` | `init --existing --platform-url` printed the patch on stdout. Its last block creates the connect workflow, pinned to the same release as the other blocks | Save it, and apply it with `git apply` from the repository's root |
| `costroid: .github/workflows/costroid-connect.yml exists, and init left it as it is` | `init --existing --platform-url` found a connect workflow, so the patch does not create one | None. To take the shipped workflow in its place, delete the file and run `init` again |
| `costroid: <file> already calls Costroid/costroid/action, so init has nothing to add` | The workflow already reports | None |
| `costroid: <file> already runs <action>, so init prints the patch that reports from it, as --existing <file> does` | `init` ran with no `--agent` in a repository whose workflow runs `claude-code-action` or `codex-action`, so it printed a patch in place of a second agent workflow | Save the patch and apply it with `git apply`. To write a new workflow anyway, pass `--agent` |
| `costroid: init: this directory is in no git repository, and init writes at a repository's root: run it inside your repository` | `init` found no repository around the current directory, wrote nothing and exits 2 | Run it inside your repository, or run `git init` first |
| `costroid: wrote .gitlab-ci.yml, which includes <file>` | The repository had no `.gitlab-ci.yml`, so `init` wrote one that includes the template | Commit it with the template |
| `costroid: added <file> to the include of .gitlab-ci.yml` | `init` added the template to the pipeline's `include:` | Commit `.gitlab-ci.yml` with the template |
| `costroid: .gitlab-ci.yml already includes <file>` | The pipeline already reads the template, so `init` left `.gitlab-ci.yml` as it is | None |
| `costroid: wrote <file>, the skill that reads what a run cost` | `init --skills` wrote the skill where Codex or Claude Code reads it | Commit it with the workflow |
| `costroid: the repository has the secret <name> already, so init leaves it as it is` | `init --pr` found the agent's key among the repository's secrets, so it asked for no key and set none | None. To replace the key, run `gh secret set <name>` |
| `costroid: the run posted its comment at <url>` | `init --pr` opened the pull request, watched its run to the end and found Costroid's comment | Open the address |
| `costroid: init: <command> failed: <error>` | A command `init --pr` ran failed, and `init` exits 125. The lines above it list the commands left, the failed one first | Fix what the command's own error names, then run the commands left by hand |
| `costroid: a repository secret takes precedence over an organization secret of the same name, so the run reads the <name> set here. Pass --keep-org-secret to keep the organization's` | `init --pr` is about to set the agent's key as a repository secret, which the run reads in place of an organization secret of that name | None. To keep the organization's secret, stop `init` and run it again with `--keep-org-secret` |
| `costroid: init sets no repository secret <name>, so the run reads the organization's` | `init --pr` ran with `--keep-org-secret`, so it listed and set no repository secret | Check that the organization's secret `<name>` is available to this repository |

## Checking a repository

`costroid doctor` checks a repository, from its root, before a pull request runs Costroid. It prints one line per check: `ok`, `warn` or `fail`, the fact, and after `warn` or `fail` the fix. It exits 1 when a line reads `fail`, and 0 otherwise.

It reads the names of the repository's secrets and variables through `gh secret list` and `gh variable list` when `gh` is on `PATH`, and whether the repository is private through `gh repo view`. It never prints a secret's value, prefix or length, and it never prints a variable's value. Without `gh` it says what it did not check.

`costroid doctor --ci`, run as a step of a GitHub Actions job, adds what only the job sees: the event, whether the pull request's head is a fork, whether the job holds `id-token: write`, and the job's declared permissions.

With `COSTROID_PLATFORM_URL` set in the shell, `doctor` asks Costroid Platform for the scopes of the integration token in `COSTROID_PLATFORM_TOKEN`, through `GET /tokens/current`, and for the binding of the repository's owner, through `GET /identity`. It names the token by that variable alone. It reads the owner from `GITHUB_REPOSITORY_OWNER_ID` or `CI_PROJECT_NAMESPACE_ID` in a CI job, as `costroid connect` does, and through `gh api` elsewhere.

| Check | A `warn` or `fail` line | Fix |
| ----- | ----------------------- | --- |
| The binary | `warn: costroid <version>, and <file>:<line> pins <release>` | Install the release the workflow pins, as the [README](../README.md#install) shows |
| | `warn: costroid <version> is a development build, which belongs to no release` | The same |
| `costroid.yml` | `fail: costroid.yml: <problem>` | The problem is the one `costroid validate` names. [`docs/configuration.md`](configuration.md) lists each key |
| The workflows | `warn: no workflow in .github/workflows calls Costroid's action` | Run `costroid init github --agent claude`, or another agent, as [`docs/install.md`](install.md#write-the-workflow) shows |
| | `warn: no workflow in .github/workflows calls Costroid's action: run costroid init github --agent claude-code-action --existing <file>` | A workflow already runs `claude-code-action`, or `codex-action` with `--agent codex-action`. Run the command the line names: it prints the patch that reports from that workflow, as [`docs/existing-workflow.md`](existing-workflow.md) shows |
| | `fail: <file>:<line>: high: <rule>: <message>`, `warn: <file>:<line>: medium: <rule>: <message>` | The finding `costroid lint` prints for that workflow. [`docs/lint.md`](lint.md) says what each rule guards |
| Costroid's pin | `fail: <file>:<line>: Costroid/costroid/action@<ref> is not a full commit SHA` | Pin the full commit SHA of a release tag, with its version in a comment, as `costroid init` writes it. The action installs only the release its own commit belongs to |
| | `fail: <file>:<line>: Costroid/costroid/action@<ref> is the zero SHA, which names no release` | The same |
| | `warn: <file>:<line>: Costroid/costroid/action@<ref> records no release in its comment` | Add the release's tag after the SHA, as a comment `# v<version>` |
| Two agents | `warn: <file> and <file> each run an agent on pull_request, so each pull request pays for 2 runs` | Keep one. To keep a workflow that runs `claude-code-action`, delete the others and add Costroid's two blocks to it, as [`docs/existing-workflow.md`](existing-workflow.md) shows |
| The agent | `warn: <agent> is not on PATH, so costroid run cannot start it on this machine, though CI installs its own` | Run the `npm install` command the line names, for a local `costroid run` |
| | `warn: <agent> --version printed no version` | Reinstall the agent |
| The keys | `fail: COSTROID_CREDENTIAL reads like a key, and every row would carry it` | Set it to the key's id from the provider's console, never the key |
| Secrets | `warn: gh secret list names no repository secret <name>; an organization or environment secret of that name is not listed here` | Run `gh secret set <name>`, unless the organization or the environment already holds it. `gh secret list` lists repository secrets only, so this is a warning, not a failure |
| | `warn: gh is not on PATH, so doctor did not list the repository's secrets` | Install `gh`, or check the secrets the line names in the repository's settings |
| | `warn: gh secret list failed, so doctor did not check the repository's secrets: <error>` | Run `gh auth login`, then `costroid doctor` again |
| | `warn: gh variable list failed, so doctor did not read the repository's secrets, variables or visibility: gh exited 1: no git remotes found` | The repository has no remote, so `gh` cannot name it. Add a GitHub remote, or push the repository first |
| Variables | `warn: the repository sets COSTROID_INGEST_URL and not COSTROID_INGEST_AUDIENCE` | Set `COSTROID_INGEST_AUDIENCE` to the audience your ledger requires, as [`docs/recipes.md`](recipes.md#send-rows-to-a-ledger) says |
| Visibility | `warn: <file> runs step-security/harden-runner, which needs a paid plan on a private repository` | Read [Egress in the agent's job](recipes.md#egress-in-the-agents-job), then keep the step or delete it |
| Costroid Platform | `ok: COSTROID_PLATFORM_TOKEN is not set in this shell, so doctor did not read its scopes or the binding; set it to the integration token to check them` | Nothing: the recipes read the token from the repository's secrets. To check its scopes and the binding, set it in the shell and run `costroid doctor` again |
| | `fail: COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_` | Create one on Costroid Platform, as [`docs/connect.md`](connect.md#one-token-per-owner) says |
| | `warn: COSTROID_PLATFORM_TOKEN in this shell carries <scopes>, and expires at <time>, and lacks <scopes>` | Create one with `identity:read`, `installations:write` and `rows:write`. `connect` uses the first two and `export` the first and the third |
| | `warn: Costroid Platform refused <request>: <status> <code>` | `AUTH_TOKEN_INVALID`: the token is unknown, expired or revoked, so create one. Any other code: [`docs/connect.md`](connect.md) says what it means |
| | `warn: tenant <tenant> holds no binding of <installation>, so Costroid Platform refuses its rows with INSTALLATION_NOT_BOUND` | Run `costroid connect` from a scheduled job, as [`docs/connect.md`](connect.md) says |
| | `warn: <installation> is bound to tenant <tenant>, last proven at <time>, more than 7 days ago, so another tenant's proof can displace it` | The same. A daily run renews the binding |
| | `warn: <installation> was bound to tenant <tenant> and is <status>, so Costroid Platform refuses its rows with INSTALLATION_NOT_BOUND` | The same |
| | `ok: tenant <tenant> also holds owner <owner> through the GitHub App installation <id>, proven at <time>` | Nothing for the App. Rows the recipes send name `github-owner:`, which only a job's proof binds, so the line above it is the one that decides whether they are taken |
| | `warn: doctor did not read the binding, because it could not name the repository's owner: <error>` | Run `doctor` where `gh` can read the repository, or as a step of the connect job |
| The event, with `--ci` | `warn: the event is <event>, and costroid report posts only on pull_request` | Run the report job on `pull_request` |
| The head, with `--ci` | `warn: the pull request's head is the fork <repository>, which receives no secrets and cannot post the check` | Add [`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml), which posts the check from your repository |
| The job, with `--ci` | `warn: job <job> of <file> declares no permissions, so it holds the repository's default token` | Declare the permissions the job needs, as the recipes do |

doctor ends with one line of its own.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: doctor: <count> fail, <count> warn, <count> ok` | What doctor checked. It exits 1 when one line reads `fail` | Fix each `fail` line above it, then each `warn` line |
| `costroid: doctor: <error>` | doctor could not run its checks, and exits 125. `--ci` outside a GitHub Actions job is one such error | Read the error. `costroid help doctor` gives the flags |

## Posting the agent's answer

With `post-review: "true"`, the action runs `costroid review --keep` in the agent's job and `costroid review --post` in the report's job. [`docs/review.md`](review.md) says what the comment shows. Neither step fails its job: `--keep` exits 0 when the run left no answer, its step continues on an error, and `--post` exits 0 whatever the host answers.

The answer travels as `costroid-review.md` in the `costroid-rows` artifact. A report step without `post-review: "true"` refuses an artifact that holds it, with the refused artifact's line in [Installing the binary](#installing-the-binary), and the report's check fails. An ingest step takes it whatever `post-review` says, deletes it unread and sends the rows.

| Line | Meaning | Fix |
| ---- | ------- | --- |
| `costroid: kept the agent's answer, <count> bytes, for the review comment` | The agent's job kept the run's final answer, and nothing else it wrote | None |
| `costroid: no answer was kept: <error>` | The run ended without an answer: it stopped at its cap, its last turn failed, or it never finished. The error says which. In stage `run` with `show-output: true`, the agent's stream went to the log and not to a file, so there is nothing to read | None, when the error names a cap or a failed turn. In stage `run`, leave `show-output` false |
| `costroid: warning: the answer is <count> bytes, so its first <count> were kept` | The answer is longer than 1 MiB. The comment shows its first 30000 bytes in any case | None |
| `costroid: the agent's job kept no answer` | The artifact holds no `costroid-review.md`. A line below says what was done instead. The agent's job's log says why | Read the agent's job's log. Set `post-review: "true"` on the agent's step as well |
| `costroid: warning: the kept answer was not read: <error>` | `costroid-review.md` could not be read, or is not a plain file, so the run counts as one with no answer | Read the error |
| `costroid: no answer to post, and the pull request holds no earlier answer` | The run kept no answer, and no earlier run posted one, so no comment was created | None |
| `costroid: the earlier answer now says this run kept none` | The run kept no answer, so the comment an earlier run posted was edited to say so | None |
| `costroid: warning: the earlier answer was not replaced: <error>` | The run kept no answer, and the host refused the look for an earlier comment or its edit. An earlier answer can still show on the pull request | Give the report's job `pull-requests: write`, as the recipes do |
| `costroid: no answer to post, and this host cannot look for an earlier one` | The run kept no answer, on a host where Costroid cannot find an earlier answer without writing, so nothing was posted | None |
| `costroid: posted the agent's answer in a comment of its own` | The comment was posted, or the one an earlier run posted was edited | None |
| `costroid: warning: the agent's answer was not posted: <error>` | The host refused the comment. A fork's pull request gets a read-only token, so this is expected there | Give the report's job `pull-requests: write`, as the recipes do |
| `costroid: not posted: review posts on the pull_request event, and this run's event is <event>` | On GitHub Actions, the answer is posted on the `pull_request` event only, as the report is | Run the report's job on `pull_request` |
| `costroid: review refused: neither GitHub Actions nor GitLab CI was detected` | `costroid review --post` ran outside CI. It exits 125 | Run it in the report's job |
