# Record a program that runs an agent through its SDK

The [Claude Agent SDK](https://code.claude.com/docs/en/agent-sdk/typescript) and the [Codex SDK](https://github.com/openai/codex/tree/main/sdk/typescript) run Claude Code and Codex inside a program you write. The program's output is its own, so Costroid cannot read usage from it. `costroid run --agent` names the agent, runs the program as it is, and reads the files the agent writes while the program runs.

| `--agent`         | For                                                       | Read from                                         | Grade                                     |
| ----------------- | --------------------------------------------------------- | ------------------------------------------------- | ----------------------------------------- |
| `claude-code-sdk` | A program that calls the Claude Agent SDK's `query()`     | Claude Code's session files in `CLAUDE_CONFIG_DIR` | `none`, or `gateway-precheck` on a gateway |
| `codex-sdk`       | A program that runs a Codex SDK thread                    | Codex's rollout files in `CODEX_HOME`             | `wrapper-watchdog`, as with `codex exec`   |
| `claude-code`     | Claude Code started through `npx`, `bunx` or `node`       | Claude Code's stream and session files            | `agent-flag`, as with `claude -p`          |
| `codex`           | Codex started through `npx`, `bunx` or `node`             | Codex's stream and rollout file                   | `wrapper-watchdog`, as with `codex exec`   |

Without `--agent`, `costroid run` picks the agent by the command's name, `claude` or `codex`.

## The Claude Agent SDK

```sh
costroid run --agent claude-code-sdk --budget-usd 0.05 --out rows.jsonl -- node dist/agent.js
```

Costroid starts the program with its arguments unchanged and passes its output through unread. When the program ends, Costroid reads every Claude Code session that started and ended while it ran. In CI a session in any project directory counts, because the runner hosts one job. Outside CI only a session whose working directory is the run's counts, because a developer's machine runs other sessions. A session that started before the program ran, or wrote after it ended, is not counted, and a warning names it. Each session's rows carry its own session id.

The rows come from the session file. It holds each request the conversation made, and at its end a `cost-state` line with the per-model totals Claude Code states for the session, the same figures as a result's `modelUsage`. When the requests add up to those totals, each request is its own set of rows. When they do not, the totals stand, one set of rows per model, and a warning says so. In both recorded runs below, the totals counted a request Claude Code made outside the conversation on the conversation's own model, so the rows came one set per model.

Set `CLAUDE_CONFIG_DIR` to a directory made for the run. Claude Code writes its sessions there, and two runs cannot share it: a second `costroid run` in the same directory is refused. The SDK writes its sessions to disk unless the program sets `persistSession: false`, which leaves Costroid nothing to read (the `persistSession` option, [TypeScript reference](https://code.claude.com/docs/en/agent-sdk/typescript), read 2026-11-07).

The rows read `measured` when the program inherits `ANTHROPIC_API_KEY`. A program that hands Claude Code its credential another way reads `list_equivalent`, the weaker claim.

### The budget

The program holds its own budget in `maxBudgetUsd`, or `max_budget_usd` in Python. Costroid cannot see it, so the rows grade `none` with the note "the budget sits in the program's maxBudgetUsd, which Costroid cannot see". Claude Code stops a query "when the client-side cost estimate reaches this USD value. The estimate can pass this value" ([TypeScript reference](https://code.claude.com/docs/en/agent-sdk/typescript), read 2026-11-08). Pass the same figure as `--budget-usd`, so the report states the budget the program held. Run it through a [gateway](#a-gateway) to have the budget held outside the program.

### Measured

Two programs ran on 2026-11-08, each a single `query()` with one `Read` call on `claude-haiku-5-5`. Each printed the `total_cost_usd` of its own final result. Costroid's rows, priced at catalog 0.2.0, equal each figure to the last digit:

| Program                                  | Workflow run  | The program's `total_cost_usd` | Costroid's rows |
| ---------------------------------------- | ------------- | ------------------------------ | --------------- |
| TypeScript, `@anthropic-ai/claude-agent-sdk` 0.3.316 | 42445899154 | 0.000468745 | USD 0.000468745 |
| Python, `claude-agent-sdk` 0.2.176       | 42445901806   | 0.000303215                    | USD 0.000303215 |

Both SDK versions run Claude Code 2.1.316. The recordings, with their content removed, are in [`internal/adapter/claudecode/testdata/2.1.316`](../internal/adapter/claudecode/testdata/2.1.316), and `TestSDKRowsEqualTheProgramsModelUsage` and `TestSDKPythonRecording` in [`internal/run/sdk_test.go`](../internal/run/sdk_test.go) hold the rows to both figures.

## The Codex SDK

```sh
costroid run --agent codex-sdk --budget-usd 5 --out rows.jsonl -- node dist/agent.js
```

Costroid starts the program with its arguments unchanged and passes its output through unread. Before it starts the program, Costroid notes every rollout file under `CODEX_HOME`. Each new file that appears while the program runs is a thread the program started, and Costroid follows it, with every subagent thread it starts, as it follows `codex exec`'s. When the priced requests reach `--budget-usd`, it stops the program and the Codex it started. Requests in flight can pass the cap, as with `codex exec`. Outside CI a thread in another working directory is not counted, and a warning names it.

A rollout file that was in `CODEX_HOME` before the run and is written while the program runs belongs to a resumed thread, which holds an earlier run's spend. Costroid refuses that run and writes no rows. Start a new thread, or give the program a `CODEX_HOME` made for the run.

The rows read `measured` when the program inherits `CODEX_API_KEY`, which the Codex SDK hands to Codex. Set `[otel] log_user_prompt = false` in `CODEX_HOME/config.toml` to keep prompts out of Codex's telemetry, as the recipe does.

A program on 2026-11-08, workflow run 42445904430, ran one turn of `@openai/codex-sdk` 0.164.0-alpha.11.1 on `gpt-6-luna`: one request of 10038 input tokens, 10035 of them written to the cache, and 5 output tokens, the counts of its own `turn.completed` event. At catalog 0.2.0's USD 0.10, 0.125 and 0.50 per million tokens, the rows come to USD 0.001257175. `TestCodexSDKRollout` in [`internal/adapter/codex/sdk_test.go`](../internal/adapter/codex/sdk_test.go) holds the rows to those counts.

## npx, bunx and node

```sh
costroid run --agent claude-code --budget-usd 5 --out rows.jsonl -- npx -y @anthropic-ai/claude-code@2.1.316 -p "Review the diff"
```

With `--agent claude-code` or `--agent codex`, a command that starts the agent through `npx`, `bunx` or `node` is planned as the agent's own. The launcher keeps its options before the package or the script, and Costroid places its flags after it: the command above runs `npx -y @anthropic-ai/claude-code@2.1.316 --output-format stream-json --verbose --max-budget-usd 5 -p "Review the diff"`.

Costroid reads these launcher options as taking the next argument: `-p`, `--package`, `--registry`, `--cache`, `--userconfig`, `--prefix`, `-w` and `--workspace` for `npx`, `-p` and `--package` for `bunx`, and `-r`, `--require`, `--import`, `--loader`, `--experimental-loader`, `-C` and `--conditions` for `node`. Give any other option that takes a value with `=`. `npx --call` and `node --eval` run code of their own, so they are refused. Under `npx`, Codex's version comes from its rollout file, because `npx --version` prints npm's.

## A gateway

`claude-code-sdk` runs through a [LiteLLM gateway](gateway.md) on the run's own key, as `claude -p` does, and the rows read `gateway-precheck`. No command line reaches the Claude Code the program starts, so the run sets its environment instead: `ANTHROPIC_BASE_URL` names the gateway, `ANTHROPIC_AUTH_TOKEN` holds the run's key, and `ANTHROPIC_API_KEY` and every other variable that picks a provider or its address are removed.

Claude Code applies a settings file's `env` over the environment it starts with, and the run cannot give it a `--settings` of its own. So a gateway run is refused, before the gateway is asked for the key, when one of these files sets a variable that picks where requests go, or names an `apiKeyHelper`:

- `.claude/settings.json` in the run's directory
- `.claude/settings.local.json` in the run's directory
- `settings.json` in `CLAUDE_CONFIG_DIR`, or in `~/.claude` without it

A program that sets its own base URL or key in the SDK's `env` option sends its requests around the gateway, and Costroid cannot see that. The run then sets the gateway's count of the key's spend beside the rows' figure, and a gap between them shows it.

`codex-sdk` cannot run through a gateway. Codex reads its base URL from its configuration alone ([How each agent reaches the gateway](gateway.md#how-each-agent-reaches-the-gateway)), and no command line reaches the Codex the program starts. A gateway run of `codex-sdk` is refused before the gateway is asked anything. Without a gateway, the run stops the program at the cap.

## The recipes

[`recipes/github-claude-agent-sdk.yml`](../recipes/github-claude-agent-sdk.yml) and [`recipes/github-codex-sdk.yml`](../recipes/github-codex-sdk.yml) run a program on a pull request and report what it cost, as the [agent recipes](recipes.md) do. `costroid init` writes one to `.github/workflows/costroid.yml`, with Costroid pinned to the release the binary belongs to:

```sh
costroid init github --agent claude-agent-sdk
costroid init github --agent codex-sdk
```

Each expects the program at `agent/review.mjs`, with its `package.json` and `package-lock.json` in `agent/`. Change the path in the recipe to yours. `init` prints what the program must do, and that it prints its output on standard output: the recipe keeps that in a file on the runner, and standard error reaches the job's log. `--post-review` does not apply, because Costroid does not read the program's output. `--pr` needs `agent/package-lock.json` in the repository, because the run it opens installs from it.

| Recipe                     | Secret              | The agent's environment                                                         |
| -------------------------- | ------------------- | ------------------------------------------------------------------------------- |
| `github-claude-agent-sdk`  | `ANTHROPIC_API_KEY` | `CLAUDE_CONFIG_DIR` made for the run, and the five `OTEL_LOG_*` content flags at `0` |
| `github-codex-sdk`         | `OPENAI_API_KEY`    | The key as `CODEX_API_KEY`, and a `CODEX_HOME` made for the run with `log_user_prompt = false` |

`npm ci --ignore-scripts` installs the program's packages from its lock file, and no package's install script runs. The Agent SDK's package carries the Claude Code it runs, and the Codex SDK's depends on the Codex it runs, so the lock file pins the agent too. The job reaches the provider, GitHub and the npm registry and nothing else; [Egress in the agent's job](recipes.md#egress-in-the-agents-job) lists each host and why. A Python program installs from a package index instead: add the hosts its install reaches to `allowed-endpoints`, and `costroid lint-recipes` notes each one it does not know.

`costroid lint-recipes` holds a job that runs `costroid run --agent` to every rule of an agent's job, as it holds one that runs `claude -p`: the guard on who starts it, `contents: read` and nothing else, the content flags, the install from the lock file, and egress. `costroid lint` judges a workflow with such a job as an agent workflow, as [Check your own agent workflows](lint.md) says.

The program's prompt is the program's own. Never build it from the pull request's title, description or comments. The program runs from the pull request's branch, so someone who can push a branch can change it, as they can change the workflow file; [Residual risk](recipes.md#residual-risk) says what limits that.
