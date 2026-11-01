# Run an agent on a pull request

These recipes run a coding agent on a pull request under a budget and record what it cost. They use the repository's own token. They need no App, no server and no account.

Costroid has no release yet. Each GitHub recipe pins its action to the zero SHA, so it fails at start until a release replaces that pin. The GitLab template stops at `COSTROID_VERSION`.

## Claude Code

Copy [`recipes/github-claude-code.yml`](../recipes/github-claude-code.yml) to `.github/workflows/costroid.yml`.

Set these on the repository:

| Kind     | Name                | Value                                                  |
| -------- | ------------------- | ------------------------------------------------------ |
| Secret   | `ANTHROPIC_API_KEY` | The provider key                                       |
| Variable | `COSTROID_VERSION`  | The Costroid release to install, without the leading v |
| Variable | `COSTROID_SHA256`   | The SHA-256 of that release's `costroid-linux-amd64`   |
| Variable | `COSTROID_CREDENTIAL` | Optional. The id of the provider key, for [reconciliation](reconcile.md) |

Create a label named `costroid-agent`. A maintainer applies it to let a first contributor's branch run.

The recipe installs Claude Code 2.1.316, the version Costroid's fixtures record. Raise `npm install` and `minimum-agent-version` together. Costroid passes `--max-budget-usd`, so the grade is `agent-flag`: Claude Code checks its own estimate between requests, and the request that crosses the cap still runs.

The agent runs with `--bare`, `--restricted` and `--strict-mcp-config`. These skip hooks and `CLAUDE.md`, ignore the repository's settings files and remove the tools that run commands. A recording with these three flags does not exist yet.

## Codex

Copy [`recipes/github-codex.yml`](../recipes/github-codex.yml) to `.github/workflows/costroid.yml`, and set `CODEX_API_KEY` in place of `ANTHROPIC_API_KEY`. Everything else in the table above is the same.

The recipe installs Codex 0.164.0-alpha.11.1, the one version with a recorded fixture. Any other version runs, and its grade reads `none`, because Costroid reads Codex's undocumented rollout file only at recorded versions.

## GitLab CI

Copy [`recipes/gitlab-ci.yml`](../recipes/gitlab-ci.yml) into your repository and `include` it as a local file.

Set `COSTROID_VERSION` and `COSTROID_SHA256` as CI/CD variables, and `COSTROID_CREDENTIAL` if you reconcile. Set `ANTHROPIC_API_KEY` or `CODEX_API_KEY` too, scoped to the environment `costroid-agent`, so the key reaches the agent's job alone. `COSTROID_AGENT` picks the agent. It reads `claude` unless you set `codex`.

Every merge request needs the label `costroid-agent`, because the template has no way to read whether an author is new. Merge requests from forks never run the agent.

The template records the run and keeps `costroid-rows.jsonl` as an artifact. `costroid report` posts to GitHub only, so GitLab has no comment yet.

## claude-code-action

`claude-code-action` runs Claude Code itself, so `costroid run` cannot wrap it. Read its `execution_file` output in a later step of the same job:

```yaml
- id: claude
  uses: anthropics/claude-code-action@<sha> # pin it, as every recipe does
- name: Write the rows
  if: always()
  env:
    EXECUTION_FILE: ${{ steps.claude.outputs.execution_file }}
  run: costroid ingest-run --execution-file "$EXECUTION_FILE" --out "$RUNNER_TEMP/costroid-rows.jsonl"
```

The file holds prompts and tool output. Costroid reads usage from it and nothing else, and it never leaves the job. The rows come at one set per model, for the whole run, unless the session files are on the runner too.

Costroid did not start this run, so the grade reads `none`, whatever `claude_args` passed. The file does not state a budget. The fixture behind this reader was written from the action's source, and no real file has been read yet.

## What a row says about the run

Inside GitHub Actions or GitLab CI, `costroid run` and `costroid ingest-run` put the repository, the pull request, the head commit and the CI run on every row. On a GitHub pull request the head comes from the event file, because `GITHUB_SHA` is the merge commit. On a GitLab merged results pipeline it comes from `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA`, for the same reason.

These are claims. Rows read `unverified` and `unattributed` until a ledger checks them against the job's OIDC token. `costroid run` also sets `vcs.change.id`, `vcs.ref.head.revision`, `vcs.ref.base.revision`, `vcs.repository.url.full`, `cicd.pipeline.run.id` and `cicd.pipeline.task.run.id` through `OTEL_RESOURCE_ATTRIBUTES` for the agent, beside what the job already set there.

## Send rows to a ledger

Set the variables `COSTROID_INGEST_URL` and `COSTROID_INGEST_AUDIENCE`. The `ingest` job then sends the rows under its CI OIDC token. The ledger takes the run's identity from that token and prices the rows itself. A failed send warns and never fails the job. A self-hosted ledger must list the issuer's host on its egress allowlist, or the send answers `EGRESS_REFUSED`: see [`docs/install.md`](install.md).

## What the recipes hold to

| Rule                                                                        | Why                                                                                 |
| --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `pull_request`, never `pull_request_target`                                 | A fork's code never runs with the base repository's secrets                         |
| Permissions declared per job                                                | The workflow grants nothing, and each job names what it needs                       |
| The agent's job holds `contents: read` and nothing else                     | GitHub grants permissions per job, so the agent runs in a job of its own            |
| `id-token: write` on the `ingest` job alone                                 | An agent that could mint the token could forge its run's identity                   |
| Every action pinned to a full SHA, with its release beside it               | A moved tag cannot change what runs                                                 |
| Content flags off                                                           | Prompts, responses, tool input and API bodies stay out of telemetry                 |
| The prompt is a literal in the recipe                                       | No comment, title or description reaches the agent, Costroid's own comment included |
| No agent on a fork's pull request; a label before a first contributor's run | A stranger's pull request cannot spend your budget                                  |

`costroid lint-recipes <paths>` rejects a file that breaks one of these, and exits 1. `make check` lints every file in `recipes/` and `action/action.yml`.

```sh
costroid lint-recipes recipes action/action.yml
```

## Residual risk

An injected agent can still spend its budget and edit the PR.

The diff and the repository are the agent's input, and anyone who opens a pull request writes them. An injected agent reads whatever its job holds, including the provider key, and can send it to any host the runner reaches. The recipes do not limit egress. To limit it, run the agent's job on a runner behind your own firewall, or give it a short-lived gateway key in place of the provider key.

The recipes give the agent no write token, so its edits stay in the job's checkout until you add a step that pushes them. Adding that step makes them part of the pull request.

The agent's job writes the rows, so an injected agent can forge them. The comment prints them as sent, and says so: "Unverified: the agent's job wrote these figures." A tag inside the file cannot remove that sentence. A ledger reprices the rows from raw token counts and holds them to the job's identity.

The guard lives in the workflow file, and `pull_request` reads that file from the branch. Someone who can push a branch can edit it there. To stop that, keep the key in an environment with required reviewers.

## Pins

Each SHA was read with `gh api repos/<action>/git/ref/tags/<release>` on November 1, 2026.

| Action                      | Release | SHA                                        |
| --------------------------- | ------- | ------------------------------------------ |
| `actions/checkout`          | v7.0.1  | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `actions/upload-artifact`   | v7.0.2  | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| `actions/download-artifact` | v8.0.2  | `9000827ccba6bdab643e8b6fd33ac0654aef8333` |

The image in the GitLab template is `node:24-slim`, pinned by digest to the base image Costroid Platform's engine builds on.
