# Run an agent on a pull request

These recipes run a coding agent on a pull request under a budget and record what it cost. They use the repository's own token. They need no App, no server and no account.

Each GitHub recipe pins Costroid's action to the commit of `v1.0.0-alpha.24`, and the GitLab template installs the same release. The action installs the release its own commit belongs to. It checks that release's `SHA256SUMS` by its signature, then the binary against it, on `amd64` and `arm64` runners alike, so a GitHub repository sets one secret, the provider key, and names no version and no checksum. Set `COSTROID_VERSION` and `COSTROID_SHA256` only to install a different release. [`docs/verify.md`](verify.md) says what the action checks.

## Claude Code

Copy [`recipes/github-claude-code.yml`](../recipes/github-claude-code.yml) to `.github/workflows/costroid.yml`. Already on `claude-code-action`? Use [`recipes/github-claude-code-action.yml`](../recipes/github-claude-code-action.yml) instead, described below.

Set the secret on the repository:

| Kind     | Name                  | Value                                                                    |
| -------- | --------------------- | ------------------------------------------------------------------------ |
| Secret   | `ANTHROPIC_API_KEY`   | The provider key                                                         |
| Variable | `COSTROID_CREDENTIAL` | Optional. The id of the provider key, for [reconciliation](reconcile.md) |

Create a label named `costroid-agent`. A maintainer applies it to let a first contributor's branch run.

The recipe installs Claude Code 2.1.316, the version Costroid's fixtures record. Raise `npm install` and `minimum-agent-version` together. Costroid passes `--max-budget-usd`, so the grade is `agent-flag`: Claude Code checks its own estimate between requests, and the request that crosses the cap still runs.

Each push to the pull request runs the agent again, and each run has a budget of its own. At `budget-usd: "5"`, a pull request with ten pushes can spend USD 50 at list, and more, because the request that crosses a cap still runs.

The agent runs with `--bare`, `--restricted` and `--strict-mcp-config`. These skip hooks and `CLAUDE.md`, ignore the repository's settings files and remove the tools that run commands. Costroid's recorded fixtures ran without these flags. The daily run behind [`docs/accuracy.md`](accuracy.md) uses all three.

Then open a test pull request. The `report` job posts the `costroid` check and the comment. It passes the agent job's result as `agent-result`, so when the workflow skipped the agent, for a first contributor without the label, the comment says the agent did not run. [`docs/troubleshooting.md`](troubleshooting.md) explains each line of the job log, and [`docs/required-check.md`](required-check.md) makes the check required.

## Codex

Copy [`recipes/github-codex.yml`](../recipes/github-codex.yml) to `.github/workflows/costroid.yml`, and set `OPENAI_API_KEY` in place of `ANTHROPIC_API_KEY`. Everything else in the table above is the same.

The recipe installs Codex 0.164.0-alpha.11.1. Codex 0.167.0 is recorded too. Costroid reads Codex's undocumented rollout file by its shape, not by its version: a version without a recording runs, reads per request when its file has the shape the reader knows, and the run names the version.

As with Claude Code, each push runs the agent again under a budget of its own.

Then open a test pull request.

## GitLab CI

Copy [`recipes/gitlab-ci.yml`](../recipes/gitlab-ci.yml) into your repository, for example as `.gitlab/costroid.yml`, and include it from your `.gitlab-ci.yml`:

```yaml
include:
  - local: .gitlab/costroid.yml

# Only if your stages list has no stage named test.
.costroid-agent:
  stage: build
```

The agent's jobs run in the stage `test`. If your `.gitlab-ci.yml` lists its own `stages` without `test`, set `stage` on `.costroid-agent` to one of yours, as above. `costroid:ingest` and `costroid:report` run in `.post`, which every pipeline has.

The template pins one release in `COSTROID_VERSION` and the commit its tag names in `COSTROID_COMMIT`. It installs cosign at a pinned checksum, verifies that Costroid's release workflow signed the release's `SHA256SUMS` at that tag and that commit, and checks the binary against its line in that file, as [`docs/verify.md`](verify.md) describes. A tag can be moved and a commit cannot, so change the two together. Nothing is set by hand except `COSTROID_CREDENTIAL`, if you reconcile. Set `ANTHROPIC_API_KEY` or `CODEX_API_KEY` too, scoped to the environment `costroid-agent`, so the key reaches the agent's job alone. `COSTROID_AGENT` picks the agent. It reads `claude` unless you set `codex`.

Every merge request needs the label `costroid-agent`, because the template has no way to read whether an author is new. Without it, no Costroid job runs and no note posts. Merge requests from forks never run the agent.

Each push to a labelled merge request runs the agent again, and each run has a budget of its own, `COSTROID_BUDGET_USD`.

The template records the run and keeps `costroid-rows.jsonl` and the run's result object, `costroid-rows.run.json`, as artifacts. The `costroid:report` job then posts one sticky note on the merge request, the same comment a GitHub pull request gets.

This adapter has run against a stub built from GitLab's documentation, and end to end on a self-managed GitLab CE 19.5.1 in Docker: [`docs/gitlab.md`](gitlab.md) says what that run proved. It has not run on gitlab.com. Open a test merge request with the label to see its note on your instance.

### Give the report a token

The job token cannot post. It reads a merge request's notes and cannot create one, and it has no access to commit statuses ([job token access](https://docs.gitlab.com/ci/jobs/ci_job_token/), read November 1, 2026). Create a project access token:

| Setting | Value    | Why                                                                                                                                    |
| ------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Role    | Reporter | Enough to [comment on a merge request and read the code](https://docs.gitlab.com/user/permissions/) (read November 1, 2026)            |
| Scope   | `api`    | The only [project token scope](https://docs.gitlab.com/security/tokens/access_token_scopes/) that can write through the API (read November 1, 2026) |

Store it as the CI/CD variable `COSTROID_GITLAB_TOKEN`, masked, with the environment scope `costroid-report`. Only the job in that environment receives it, so the agent's job never sees it.

Leave it unprotected. A protected variable reaches a merge request pipeline only when [both branches are protected](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/) (read November 1, 2026), so an ordinary branch would get no note. On Premium or Ultimate, [protect the environment](https://docs.gitlab.com/ci/environments/protected_environments/) too (read November 1, 2026). Without the variable, the job does not run.

[Project access tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/) need Premium or Ultimate on GitLab.com and any license on a self-managed instance (read November 1, 2026). On GitLab.com Free, use a token of a dedicated account with the Reporter role. From GitLab 19.2, [fine-grained personal access tokens](https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens_rest/) list narrower permissions for these endpoints (read November 1, 2026), and Costroid has not tried one.

Each project access token is [its own bot user](https://docs.gitlab.com/user/project/settings/project_access_tokens/) (read November 1, 2026). The report edits only its own user's note, so a replacement token under a new bot user starts a second note on a merge request that has one.

Masking hides the token from job logs, and it does not hide it from the pipeline file. Someone who can push a branch can name `costroid-report` in their branch's `.gitlab-ci.yml` and read the token ([variable security](https://docs.gitlab.com/ci/variables/), read November 1, 2026). A protected environment stops that for anyone not allowed to deploy to it.

The job reaches the instance at `CI_API_V4_URL`, on gitlab.com or self-managed. The address must be HTTPS. The job reads `costroid.yml` at `CI_MERGE_REQUEST_DIFF_BASE_SHA`, and the project's visibility from the API, where internal counts as private. On gitlab.com the comment's account link names `gitlab-namespace:<namespace-id>`; on a self-managed instance it names no id. It never names the instance's hostname.

A [resource group](https://docs.gitlab.com/ci/resource_groups/) runs one report at a time in a project (read November 1, 2026), so two pipelines cannot each post a first note.

### No check on GitLab

GitHub's check run has no direct equal, and the report posts none. A [commit status](https://docs.gitlab.com/api/commits/) has no neutral state (read November 1, 2026), and creating one [needs the Developer role](https://docs.gitlab.com/user/permissions/) (read November 1, 2026), which is more than the note needs. The comment says the check run was not posted, and its machine block names no check. `allow_failure` keeps the job from failing a pipeline unless `COSTROID_GATE` is set, as [`docs/gitlab.md`](gitlab.md) says.

### Forks

A fork's merge request runs [in the fork](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/), with the fork's configuration and variables (read November 1, 2026). There the rules drop the agent's job and the report's job. Nothing reads your variables, no agent runs and no note posts. `costroid report` refuses such a pipeline as well.

A parent project member can [run that pipeline in the parent project](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/), and then [all variables become available](https://docs.gitlab.com/ci/variables/) to it (read November 1, 2026). The rules drop both jobs if the configuration is yours. The fork's own `.gitlab-ci.yml` can change that, so it could reach `COSTROID_GITLAB_TOKEN`. To close this, set the project's `ci_allow_fork_pipelines_to_run_in_parent_project` to false through the API.

Inside your own project, `costroid:report` holds the token, the rows artifact and nothing else. [`GIT_STRATEGY: empty`](https://docs.gitlab.com/ci/runners/configure_runners/) leaves no code on disk (read November 1, 2026), and the job has no provider key and no id token. The agent's job wrote the rows, so the comment states `unattributed` for every row it reads from them.

## claude-code-action

`claude-code-action` runs Claude Code itself, so `costroid run` cannot wrap it. [`recipes/github-claude-code-action.yml`](../recipes/github-claude-code-action.yml) runs its own `claude-code-action` step and adds Costroid around it: the action's `execution_file` output goes to the action's `read` stage in the same job, and a separate job posts the comment. Put your own `prompt` and `claude_args` in that review step.

The recipe installs Claude Code 2.1.316, the version with a recorded fixture, and passes its path to the action, so the version is fixed. The file holds prompts and tool output. Costroid reads usage from it and nothing else, and it never leaves the job.

Costroid did not start this run. When the step's `claude_args` holds `--max-budget-usd` and the read step is given the same figure, the grade reads `agent-flag`; otherwise it reads `none`, with the reason ([`docs/existing-workflow.md`](existing-workflow.md)). Each push to the pull request runs the agent again, and `--max-turns` limits each run's turns, not its cost. The rows come at one set per model for the whole run, so the log reads, for example, `one run, 2 models`. A real file from version 2.1.316, read on 2026-11-04, matched the run's own total to the last digit, and is kept as a fixture with its content removed.

It needs the same one secret, `ANTHROPIC_API_KEY`. Then open a test pull request.

## An infrastructure estimate beside the agent's cost

[`recipes/github-claude-code-action-infrastructure.yml`](../recipes/github-claude-code-action-infrastructure.yml) is the `claude-code-action` recipe with one step and two inputs added. The step is yours: it runs your estimator before the agent and writes a FOCUS cost file, a header and one row per resource with its period, cost and currency. The `read` step takes the file as `infrastructure-file`, and its rows join the agent's before the rows are kept. The report job does not change. The comment states the estimate under the agent's figure, one line per currency and period, and never adds the two together. [`docs/infrastructure.md`](infrastructure.md) lists the columns the file needs.

The estimator's key, `INFRASTRUCTURE_ESTIMATOR_KEY` in the recipe, reaches the estimate step alone, and the agent's job holds no new permission. With `infrastructure-optional: "true"`, a run whose estimate step wrote no file, because the key is not set, adds no rows and does not fail, and the comment reads "Infrastructure estimate: not available", with the reason. A fork's pull request runs no agent job in this recipe, as in the others, so its comment says the agent did not run.

## codex-action

`openai/codex-action` runs Codex itself, so `costroid run` cannot wrap it. [`recipes/github-codex-action.yml`](../recipes/github-codex-action.yml) runs its own `codex-action` step and adds Costroid around it: the step writes its rollout files to `codex-home`, a directory the job makes under `runner.temp`, and the action's `read` stage reads that directory in the same job. A separate job posts the comment. Put your own prompt in the step that writes it.

The recipe installs Codex 0.164.0-alpha.11.1 and passes the same version to the action as `codex-version`. It states `safety-strategy: drop-sudo`, the action's default, so a change shows in review. It leaves `allow-users` empty, because the job's `if` already says who may start the agent. The home's `config.toml` sets `otel.log_user_prompt=false`.

The `read` stage reads every rollout file under `codex-home` written since the job started: the run's own thread and each subagent's, line by line, as `costroid run` reads them. A home that holds no such file gets no rows, and the report says why. Costroid did not start this run, so no budget held it, and the grade reads `none`. Each push to the pull request runs the agent again. `openai/codex-action` takes only an API key, through its `openai-api-key` input, so the rows read `measured`.

A real run of `codex-action` at `v1.13` with Codex 0.164.0-alpha.11.1, read on 2026-11-07, made two requests to `gpt-6.1-sol`. Their counts, priced by hand at the pinned catalog's list prices, come to USD 0.029324, and Costroid's rows give the same figure. The run is kept as a fixture with its content removed.

`codex-action` publishes tags, not releases. The recipe pins the commit its tag named when that run checked it out:

| Action                | Tag   | SHA                                        |
| --------------------- | ----- | ------------------------------------------ |
| `openai/codex-action` | v1.13 | `bdf19a4a223ec2549a3e2274a0cf61556bc07675` |

It needs one secret, `OPENAI_API_KEY`, the one `codex-action` already takes. Then open a test pull request.

## A program on an agent's SDK

[`recipes/github-claude-agent-sdk.yml`](../recipes/github-claude-agent-sdk.yml) and [`recipes/github-codex-sdk.yml`](../recipes/github-codex-sdk.yml) run a program you wrote on the Claude Agent SDK or the Codex SDK, and report what the agent it started cost. [`docs/sdk.md`](sdk.md) says what each needs.

## OpenCode

[`recipes/github-opencode.yml`](../recipes/github-opencode.yml) runs OpenCode 1.19.4 on `anthropic/claude-haiku-5-5` with the secret `ANTHROPIC_API_KEY`, and stops it at the cap between its steps, so one step can pass the cap. Its grade reads `wrapper-watchdog`, on a recorded stop: under a USD 0.001 cap, a run of one step at USD 0.001438275 was stopped before its second step. It reads no `opencode.json` from the repository, which the pull request can change, and it denies OpenCode's edits, commands, web fetches and subagents. [`docs/adapters.md`](adapters.md#opencode) says how Costroid reads OpenCode.

## A LiteLLM gateway

[`recipes/github-claude-code-litellm.yml`](../recipes/github-claude-code-litellm.yml) runs Claude Code through a LiteLLM proxy you run, on a key made for the run alone, with the run's budget as the key's budget. [`docs/gateway.md`](gateway.md) has the setup.

## Costroid Platform

[`recipes/github-connect.yml`](../recipes/github-connect.yml) binds the repository's owner to its Costroid Platform tenant from the default branch, so the agent recipes' `ingest` job can send rows there. [`docs/connect.md`](connect.md) says what it proves and what it binds.

## Vantage and Datadog

[`recipes/github-vantage-upload.yml`](../recipes/github-vantage-upload.yml) and [`recipes/github-datadog-upload.yml`](../recipes/github-datadog-upload.yml) send one UTC day's rows to Vantage or to Datadog's Custom Costs, once a day, with your own token. Each reads the `costroid-rows` artifacts the repository's runs kept that day. Datadog counts a file sent twice twice. [`docs/export.md`](export.md#send-the-days-file) says what each needs and what a second send of a day does.

## A fork's pull request

The agent's recipes run no agent on a fork's pull request, and the fork's token cannot post the check. [`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) posts it from your repository once the agent's workflow completes. Copy it to `.github/workflows/costroid-follow-up.yml` on the default branch. It needs no secret.

A failing `costroid` check can be approved by a pull-request review. [`recipes/github-approval.yml`](../recipes/github-approval.yml) re-posts the check when a writer who is not the author approves the current head; [`docs/approval.md`](approval.md) says which reviews count and for how long.

## GitHub Agentic Workflows

gh-aw compiles its own workflow, so Costroid runs after it. [`recipes/github-gh-aw-follow-up.yml`](../recipes/github-gh-aw-follow-up.yml) reads the finished run's `usage` artifact with `costroid ingest-run --from gh-aw`, posts the `costroid` check on its pull request with `costroid follow-up --ingested`, and sends the rows on as the agent recipes do. No gh-aw file is edited. Copy it to `.github/workflows/costroid-gh-aw.yml` on the default branch, and name your gh-aw workflow in its `workflows:` line. It needs no secret. [`docs/gh-aw.md`](gh-aw.md) says what it reads.

[`docs/required-check.md`](required-check.md#a-forks-pull-request) says what a fork's pull request gets, and [`docs/security.md`](security.md#a-forks-pull-request) why the follow-up never meets its code.

## What the job log shows

The agent's own stream holds its reasoning, its replies and what it read. A public repository's job log is public, so the action writes that stream to a file on the runner, which the job never uploads, and the log shows Costroid's own lines only. Set `show-output: true` on the run step to print the stream, on a private repository.

## Egress in the agent's job

The GitHub recipes run [`step-security/harden-runner`](https://github.com/step-security/harden-runner) in block mode as the first step of the agent's job. From that step on, the job reaches only the hosts in the step's `allowed-endpoints`, so an injected agent cannot send its key or your code to any other host. A lookup of another host fails, and when the job ends the step prints `domain not allowed: <host>` in its log.

Each list names what its own job's steps call. They were read on 2026-11-08 from the source of each action at the commit the recipes pin, and the redirect host from a live request:

| Host | Why the job reaches it | Recipes |
| ---- | ---------------------- | ------- |
| `api.anthropic.com` | Claude Code's provider, and OpenCode's in its recipe | `github-claude-code`, `github-claude-code-action`, `github-claude-agent-sdk`, `github-opencode` |
| `api.openai.com` | Codex's provider. `codex-action` starts a proxy that calls it | `github-codex`, `github-codex-action`, `github-codex-sdk` |
| The variable `COSTROID_GATEWAY_HOST` | The LiteLLM gateway, in place of the provider | `github-claude-code-litellm` |
| `github.com` | `actions/checkout`. The install step lists Costroid's tags and downloads its release, and `sigstore/cosign-installer` and `oven-sh/setup-bun` download theirs | All eight |
| `api.github.com` | `claude-code-action` reads the pull request, and `codex-action` checks the actor's access | `github-claude-code-action`, `github-codex-action` |
| `results-receiver.actions.githubusercontent.com`, `productionresultssa*.blob.core.windows.net` | `actions/upload-artifact`, which keeps the rows | All eight |
| `release-assets.githubusercontent.com` | github.com answers a release download with a redirect to this host. `curl -I` on a release's `SHA256SUMS` showed it on 2026-11-08 | All eight |
| `tuf-repo-cdn.sigstore.dev` | cosign reads Sigstore's trusted root here before it checks the release's signature (`tuf.DefaultMirror` in [`sigstore-go`](https://github.com/sigstore/sigstore-go/blob/main/pkg/tuf/options.go)) | All eight |
| `registry.npmjs.org` | npm installs the agent at its pinned version, or an SDK program's packages from its lock file, and Bun installs `claude-code-action`'s packages | All eight |

Each host is listed on port 443. The LiteLLM recipe lists the gateway and not `api.anthropic.com`, so the run's own key is the only provider credential the job can use. Claude Code's [network requirements](https://code.claude.com/docs/en/network-config#network-access-requirements) (read 2026-11-08) list other hosts for sign-in, plugins, updates and error reports. A run on an API key or a gateway needs none of them, so they stay blocked.

The LiteLLM recipe reads the gateway's host from the repository variable `COSTROID_GATEWAY_HOST`: the host of `COSTROID_GATEWAY_URL`, with its port when the URL names one, such as `litellm.example.com` or `litellm.example.com:4000`. A workflow expression cannot take the host out of a URL. Without the variable the agent cannot reach the gateway, and its run fails.

What block mode leaves open, read in [`step-security/agent`](https://github.com/step-security/agent/tree/v0.18.0) v0.18.0, the agent harden-runner v2.22.3 installs:

- It allows GitHub's own hosts besides the list: the Actions domains of GitHub's [meta API](https://api.github.com/meta), which include `*.github.com`, `*.githubapp.com` and `ghcr.io`, and every Azure storage account whose name starts `productionresultssa`. An injected agent can still send what it holds to GitHub, or to the npm registry, under an account of its own.
- The step prints each blocked host name in its log. A public repository's log is public, so a host name an injected agent made up is public too.

### Telemetry

StepSecurity's [Harden-Runner documentation](https://docs.stepsecurity.io/harden-runner) (read 2026-11-08) says "Harden-Runner sends runtime telemetry from the runner to StepSecurity": network and DNS events, process and file-write events, and the workflow's and job's metadata, sent to `agent.api.stepsecurity.io`, `prod.app-api.stepsecurity.io` and an S3 bucket in AWS us-east-1 and us-west-2. It says it does not collect "Source code or file contents", "Secret values" or "Environment variable values".

The same page says "workflows running in block mode can stop sending telemetry with the disable-telemetry input", and the recipes set `disable-telemetry: true`. The step still calls `agent.api.stepsecurity.io` as it starts, with the repository, the run's id and the job's name, and reads GitHub's domains from it. It downloads its agent from github.com (`src/setup.ts` and `src/install-agent.ts` at v2.22.3).

### Private repositories

The free tier covers public repositories on GitHub-hosted runners. The same page says "The Community Tier does not support private repositories." On a private repository without a subscription, the agent sets the job back to audit mode and sends no telemetry (`agent.go` at v0.18.0). It then blocks nothing. On a self-hosted runner without a subscription "the Harden-Runner action is a no-op" (the [README](https://github.com/step-security/harden-runner/blob/v2.22.3/README.md) at v2.22.3). In either case, limit the job's egress at your network.

### Add an endpoint

Add `host:443` to the job's `allowed-endpoints`, on a line of its own inside the folded block (`>-`). harden-runner's agent splits the list on spaces alone (`config.go` at v0.18.0), so a literal block (`|`) allows neither host on either side of a line break. Another port is `host:port`, and `*.example.com:443` allows every subdomain.

`costroid lint-recipes` prints an `egress-audit` note for the step in audit mode, in block mode with no `allowed-endpoints`, and with a line break in the list, each with its fix. It prints an `egress-endpoint` note for a host outside the table above and for telemetry left on, so a recipe that needs another host says here why. A note rejects nothing.

To drop the step, delete it. Costroid does not depend on it, and the job then reaches any host. GitLab's recipe has no equal, so limit a GitLab runner's egress at its network.

## What a row says about the run

Inside GitHub Actions or GitLab CI, `costroid run` and `costroid ingest-run` put the repository, the pull request, the head commit and the CI run on every row. On a GitHub pull request the head comes from the event file, because `GITHUB_SHA` is the merge commit. On a GitLab merged results pipeline it comes from `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA`, for the same reason.

These are claims. Rows read `unverified` and `unattributed` until a ledger checks them against the job's OIDC token. `costroid run` also sets `vcs.change.id`, `vcs.ref.head.revision`, `vcs.ref.base.revision`, `vcs.repository.url.full`, `cicd.pipeline.run.id` and `cicd.pipeline.task.run.id` through `OTEL_RESOURCE_ATTRIBUTES` for the agent, beside what the job already set there. `costroid run` does not start the agent in the `claude-code-action` and `codex-action` recipes, so their agent step sets the same six itself, as [`docs/adapters.md`](adapters.md#join-attributes) shows.

## Send rows to a ledger

Set the variables `COSTROID_INGEST_URL` and `COSTROID_INGEST_AUDIENCE`. The `ingest` job then sends the rows under its CI OIDC token. The ledger takes the run's identity from that token and prices the rows itself. A failed send warns and never fails the job. A self-hosted ledger must list the issuer's host on its egress allowlist, or the send answers `EGRESS_REFUSED`: see [`docs/ledger.md`](ledger.md#egress).

## What the recipes hold to

| Rule                                                                        | Why                                                                                 |
| --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `pull_request`, never `pull_request_target`                                 | A fork's code never runs with the base repository's secrets                         |
| Permissions declared per job                                                | The workflow grants nothing, and each job names what it needs                       |
| The agent's job holds `contents: read` and nothing else                     | GitHub grants permissions per job, so the agent runs in a job of its own            |
| `id-token: write` on the `ingest` job, and on the jobs that mint and close a gateway key in its environment | An agent that could mint the token could forge its run's identity, and only a job the environment admitted may attest a key |
| Every action pinned to a full SHA, with its release beside it               | A moved tag cannot change what runs                                                 |
| Content flags off                                                           | Prompts, responses, tool input and API bodies stay out of telemetry                 |
| An agent outside `costroid run` sets the six join attributes               | The agent's own telemetry names the pull request, as under `costroid run`           |
| The prompt is a literal in the recipe                                       | No comment, title or description reaches the agent, Costroid's own comment included |
| No agent on a fork's pull request; a label before a first contributor's run | A stranger's pull request cannot spend your budget                                  |
| GitLab's note token in `costroid:report` alone                              | The job that reads the pull request's content can never write to it                 |
| `costroid:report` holds no code, no provider key and no id token            | The job with write access runs nothing the pull request wrote                       |
| The follow-up checks out nothing and names no head commit in a step         | The job with your repository's token never holds a fork's code                      |
| A follow-up reads the finished run's artifact only through `costroid follow-up --rows` and `costroid ingest-run --from gh-aw` | Another workflow wrote it, so nothing in it runs |

`costroid lint-recipes <paths>` rejects a file that breaks one of these, and exits 1. `make check` lints every file in `recipes/` and `action/action.yml`.

```sh
costroid lint-recipes recipes action/action.yml
```

## Residual risk

An injected agent can still spend its budget and edit the PR.

The diff and the repository are the agent's input, and anyone who opens a pull request writes them. An injected agent reads whatever its job holds, including the provider key, and can send it to any host its job reaches. On a public repository harden-runner limits those to the provider, GitHub and the install hosts; on a private repository it needs StepSecurity's paid plan, as [Egress in the agent's job](#egress-in-the-agents-job) says. To limit egress further, run the agent's job on a runner behind your own firewall, or give it a short-lived gateway key in place of the provider key.

The recipes give the agent no write token, so its edits stay in the job's checkout until you add a step that pushes them. Adding that step makes them part of the pull request.

The agent's job writes the rows, so an injected agent can forge them. The comment prints them as sent, and says so: "Unverified: the agent's job wrote these figures." A tag inside the file cannot remove that sentence. A ledger reprices the rows from raw token counts and holds them to the job's identity.

The guard lives in the workflow file, and `pull_request` reads that file from the branch. Someone who can push a branch can edit it there. To stop that, keep the key in an environment with required reviewers.

## GitHub Marketplace

[`Costroid/costroid-action`](https://github.com/Costroid/costroid-action) holds this action for its [GitHub Marketplace listing](https://github.com/marketplace/actions/costroid-cost-check). Its `action.yml` calls `Costroid/costroid/action` pinned to one release's commit, names that release, and passes every input through unchanged. `Costroid/costroid-action@<commit>` therefore runs the same action as the recipes' pin. Pin it to the full commit SHA of one of its tags, as with any action.

## Pins

Each SHA was read with `gh api repos/<action>/git/ref/tags/<release>` on November 1, 2026.

| Action                      | Release | SHA                                        |
| --------------------------- | ------- | ------------------------------------------ |
| `actions/checkout`          | v7.0.1  | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `step-security/harden-runner` | v2.22.3 | `46b06cea07f9e64621fdf066ba77ac6915163d43` |
| `Costroid/costroid/action` | v1.0.0-alpha.24 | `4b6ede77fc00d7a2196c5ef02472bbf0788aef28` |
| `anthropics/claude-code-action` | v1.0.248 | `1d6de8cb0c237e7c15e9e1bdf973826ebae490cc` |
| `actions/upload-artifact`   | v7.0.2  | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| `actions/download-artifact` | v8.0.2  | `9000827ccba6bdab643e8b6fd33ac0654aef8333` |
| `sigstore/cosign-installer` | v4.1.2  | `6f9f17788090df1f26f669e9d70d6ae9567deba6` |

The image in the GitLab template is `node:24-slim`, pinned by digest to the base image Costroid Platform's engine builds on.
