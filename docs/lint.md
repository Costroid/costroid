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
- a script that runs `claude -p`, `claude --print` or `codex exec`.

Any other workflow passes with nothing said. The last line counts the files read and the workflows judged.

## The rules

| Severity | Rule                   | What it finds                                                                                                                                                   |
| -------- | ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| high     | `pull-request-target`  | `pull_request_target` starts the workflow, or a job there checks out the pull request's head. Either runs code a stranger wrote beside your secrets              |
| high     | `comment-trigger`      | A comment, review, issue or discussion event starts the agent's job, and the job's `if` tests neither `author_association` nor `github.actor`                     |
| high     | `agent-id-token`       | The agent's job holds `id-token: write`, so an injected agent can mint a token that claims your repository's identity                                          |
| high     | `expression-in-script` | A `run` script holds `${{ }}`. The runner pastes the value into the shell before it runs, so the value can run commands. Pass it through `env`                  |
| medium   | `unpinned-action`      | An action in the agent's job is not pinned to a full commit SHA or an image digest. An action in your own repository counts as pinned                           |
| medium   | `agent-prompt-input`   | A comment, review, issue, discussion, title, body or branch name reaches the agent's job, or `allow-users: "*"`, `allow-bots`, `allowed_non_write_users: "*"` or `allowed_bots` opens the agent to any account or to bots |
| note     | `agent-contents-write` | The agent's job can write to your code, or declares no permissions and takes the repository's default token                                                    |
| note     | `subscription-token`   | The agent signs in with `claude_code_oauth_token` or `CLAUDE_CODE_OAUTH_TOKEN`. Costroid reports such a run's tokens at API list price as list-equivalent, never as billed |
| high     | `unreadable`           | The file is not valid YAML, so nothing in it could be judged                                                                                                    |

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
