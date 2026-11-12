# Check your own agent workflows

`costroid lint` reads the GitHub workflows you already run and judges each one that starts a coding agent. It asks two questions: who can start a run that spends your key, and what the agent's job holds while it runs.

```sh
costroid lint
```

With no path it reads every `.yml` and `.yaml` file under `.github/workflows`. Name files or directories to read those instead. It reads files and sends nothing.

## Which workflows it judges

A workflow is judged when one of its jobs has an agent step:

- a step that uses `anthropics/claude-code-action`, whether its `prompt` input or a `@claude` mention gives it the task;
- a step that uses `openai/codex-action`;
- a step that uses Costroid's action with stage `run` or `all`;
- a script that runs `claude -p`, `claude --print` or `codex exec`;
- a script that runs `costroid run --agent`, such as a program on the Claude Agent SDK or the Codex SDK ([docs/sdk.md](sdk.md));
- a script that runs `costroid run` with no `--agent` on a command named `claude`, `codex`, `copilot` or `opencode`, the agent `costroid run` picks by that name.

Any other workflow passes with nothing said. The last line counts the files read and the workflows judged.

## The rules

| Severity | Rule                   | What it finds                                                                                                                                                   |
| -------- | ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| high     | `pull-request-target`  | `pull_request_target` starts the workflow, which hands a stranger's pull request to an agent that holds your secrets, or a job there checks out the pull request's head and runs its code beside them. GitHub blocks the event by default in public repositories from 2026-12-02, and its runs fail during the brownouts of 2026-11-09 and 2026-11-16, 16:00 to 18:00 UTC ([changelog](https://github.blog/changelog/2026-11-02-pull_request_target-blocked-by-default-december-2), read 2026-11-08) |
| high     | `comment-trigger`      | A comment, review, issue or discussion event starts the agent's job, the job's `if` tests neither `author_association` nor `github.actor`, and the agent's action does not check who started it (see below) |
| high     | `agent-open`           | `allowed_non_write_users: "*"` or `allowed_bots: "*"` on `claude-code-action`, which lets anyone, or any bot, start the agent on your key |
| high     | `agent-id-token`       | The agent's job holds `id-token: write`, so an injected agent can mint a token that claims your repository's identity                                          |
| high     | `expression-in-script` | A `run` script holds `${{ }}`. The runner pastes the value into the shell before it runs, so the value can run commands. Pass it through `env`                  |
| medium   | `unpinned-action`      | An action in the agent's job is not pinned to a full commit SHA or an image digest. An action in your own repository counts as pinned                           |
| medium   | `agent-prompt-input`   | A comment, review, issue, discussion, title, body or branch name reaches the agent's job |
| medium   | `agent-open`           | `claude-code-action` names users in `allowed_non_write_users` or bots in `allowed_bots`; or `codex-action` sets `allow-users: "*"` or `allow-bots` |
| medium   | `two-caps`             | Two steps that share a run state different caps in USD: `budget-usd` on Costroid's action, `--max-budget-usd` in `claude_args` or `claude-args`, or `--budget-usd` in a script. Claude Code stops at one, the `read` step grades the run against the one it is told, and the report states the one the run recorded, so a second figure misreads the run. Jobs that `needs` does not link may hold different caps, and a figure the file does not set, such as `${{ vars.CAP }}`, is not read |
| note     | `agent-contents-write` | The agent's job can write to your code, or declares no permissions and takes the repository's default token                                                    |
| note     | `subscription-token`   | The agent signs in with `claude_code_oauth_token` or `CLAUDE_CODE_OAUTH_TOKEN`. Costroid reports such a run's tokens at API list price as list-equivalent, never as billed |
| note     | `agent-budget`         | A `claude-code-action` step's `claude_args` hold no `--max-budget-usd`, so no cap stops a run, whatever it spends. A line of `claude_args` that starts with `#` is a comment, and a `claude_args` that is one expression is not read |
| note     | `egress-audit`         | `step-security/harden-runner` in the agent's job blocks nothing with `egress-policy: audit`, lets the agent reach no provider in block mode with no `allowed-endpoints`, or hides hosts behind a line break in `allowed-endpoints`, which it splits on spaces alone |
| note     | `join-attributes`      | A step that runs `claude-code-action`, `codex-action`, `claude -p` or `codex exec` outside `costroid run` lacks one of `vcs.change.id`, `vcs.ref.head.revision`, `vcs.ref.base.revision`, `vcs.repository.url.full`, `cicd.pipeline.run.id` and `cicd.pipeline.task.run.id` in `OTEL_RESOURCE_ATTRIBUTES`, so the agent's own telemetry names no pull request. [`docs/adapters.md`](adapters.md#join-attributes) lists the values. `costroid run` sets them for the agents it starts |
| note     | `infrastructure-file`  | A step of Costroid's action sets `infrastructure-source` or `infrastructure-optional` without `infrastructure-file`, sets `infrastructure-file` on a stage that keeps no rows, names a source the command refuses, or sets `infrastructure-optional` to neither `true` nor `false`. The estimate never reaches the report, or the step fails. [`docs/infrastructure.md`](infrastructure.md) has the inputs |
| high     | `unreadable`           | The file is not valid YAML, so nothing in it could be judged                                                                                                    |

## Who may start the agent

`claude-code-action` checks who started a run. By default it refuses a user without write access to the repository, and it refuses bots. Two inputs change that. Its `action.yml` describes `allowed_non_write_users` as one that "bypasses security checks", and `allowed_bots` with "Empty string (default) allows no bots" ([`action.yml`](https://github.com/anthropics/claude-code-action/blob/main/action.yml), read 2026-11-07). So a `claude-code-action` step with both inputs empty or absent counts as guarded, and a comment event needs no test in the job's `if`. With either input set, the job's `if` must test the author.

`codex-action` has `allow-users` and `allow-bots`. Costroid has not read how its check works, so its step never counts as guarded, and its inputs are medium findings at most.

A note says something you should know. Many teams need an agent that writes to the pull request, and a note never fails the command.

`costroid lint` does not hold your workflows to the rules of Costroid's own recipes, such as an exact agent version or telemetry content flags. `costroid lint-recipes` does that, for a file you copied from `recipes/`.

## Exit codes

| Code | Meaning                                                    |
| ---- | ---------------------------------------------------------- |
| 0    | No high finding, and under `--strict` no medium one        |
| 1    | A high finding, or under `--strict` a medium one           |
| 125  | A path could not be read, or it holds no YAML file         |

```sh
costroid lint --strict .github/workflows/claude.yml
```

Each finding prints as `file:line: severity: rule: message`. High and medium findings go to standard error, notes to standard output.

## Report from the workflow you have

To report what a `claude-code-action` run cost without replacing your workflow, see [`docs/existing-workflow.md`](existing-workflow.md).
