# Adapters

What each agent's stream carries, as recorded on November 1, 2026. The fixtures sit beside each adapter under `testdata/<agent version>/`, and every figure below is asserted by a test.

## What the budget means

| Agent       | Grade              | What holds the run to the budget                                                                                                                            |
| ----------- | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Claude Code | `gateway-precheck` | Through a LiteLLM gateway on a key made for the run: the gateway checks the key's budget before each request. The requests in flight can pass it by the bound each row states. [`gateway.md`](gateway.md) has the design. |
| Claude Code | `agent-flag`       | Costroid passes `--max-budget-usd`. Claude Code checks its own estimate between requests, so the request that crosses the cap still runs and is still paid. |
| Codex       | `gateway-precheck` | Through the same gateway: Costroid gives Codex the gateway's address and provider on its command line, which override its configuration files. [`gateway.md`](gateway.md) has the design. |
| Codex       | `wrapper-watchdog` | Costroid reads Codex's rollout file during the turn, prices each request as it settles and stops the process at the cap. Requests in flight can pass it.    |
| Codex       | `none`             | The rollout file could not be read: `--ephemeral`, a file whose shape the reader does not know, or a file that contradicts the stream. Nothing stops the run.  |

A recorded run with a 0.001 USD cap stopped mid-turn at 0.0283136 USD, 65 milliseconds after its first request settled. The tool the agent had started kept running, because Codex gives tools their own process group. A `none` run says why on every row. The recorded runs behind this table are below.

## Recorded runs

| Fixture                      | Version                  | Command shape                                                     | Model              |
| ---------------------------- | ------------------------ | ----------------------------------------------------------------- | ------------------ |
| `claudecode/.../one-request` | Claude Code 2.1.316      | `claude -p`, no tools                                             | `claude-haiku-5-5` |
| `claudecode/.../subagent`    | Claude Code 2.1.316      | `claude -p`, one subagent, partial messages, OTLP to a local sink | `claude-haiku-5-5` |
| `claudecode/.../budget-stop` | Claude Code 2.1.316      | `claude -p --max-budget-usd 0.0001`                               | `claude-haiku-5-5` |
| `codex/.../exec`             | Codex 0.164.0-alpha.11.1 | `codex exec --json`, one shell command, OTLP to a local sink      | `gpt-5.6-sol`      |
| `codex/.../resume`           | Codex 0.164.0-alpha.11.1 | `codex exec resume --json` on the thread above                    | `gpt-5.6-sol`      |
| `codex/.../stopped`          | Codex 0.164.0-alpha.11.1 | `costroid run --budget-usd 0.001 -- codex exec`, one shell tool   | `gpt-5.6-sol`      |

All six ran on a subscription, so their rows are `list_equivalent`. Two one-word Codex prompts through the built binary were smoke tests and left no fixture. The later one reported 14065 input tokens on both the stream and the rollout file, and kept `wrapper-watchdog`.

Fixtures are cut to an allowlist of keys. Prompts, tool output, paths, user and account ids, and rate-limit data are gone, and session, thread, request and message ids are replaced by synthetic ones. Each adapter has a test that scans its `testdata/` for those patterns. The next section says how a new recording is made.

## Recording a fixture

`scripts/record` turns a run into a fixture, and `make fixture` places one. Neither is part of the `costroid` binary.

On a developer's machine, `local` runs the agent and keeps only what the scrub leaves:

```
go run ./scripts/record local --agent claude-code --scenario NAME --out DIR \
  -- claude -p "PROMPT" --output-format stream-json --verbose
```

It takes the session id from the stream's `system/init` line, then copies that session's file and its `subagents/` from `CLAUDE_CONFIG_DIR`, or `~/.claude` without it. No other session in that directory is opened. With `--agent codex` and `codex exec --json`, it reads the thread from `thread.started` and copies that thread's rollout file from `CODEX_HOME`, or `~/.codex`, with the rollout files of its subagents written since the run began. The raw copy is removed when the command ends.

The scrub keeps the keys the recordings of November 1, 2026 hold, and the few an adapter reads that those runs did not write: `cwd`, Codex's `thread_source` and `session_id`, and Claude Code's `error`. A count under a `usage` object is kept even when the list does not name it. Every other key is dropped, and the scrub prints the dropped key's path, never its value. A working directory reads `/work`, an agent's own text `@content` and a prompt `@prompt`. Session, thread, request, message, tool-use, response and turn ids take the synthetic forms the fixtures already use, and one raw id takes one synthetic id in every file of the run.

`go run ./scripts/record verify DIR` exits 1 and names each problem: a key outside the allowlist, an id in no synthetic form, `sk-ant-`, `sk-`, `ghp_`, `ghs_`, `github_pat_`, `/home/`, `/Users/`, `/runner/work/`, `/tmp/`, or the text of a `--prompt-file`. It prints `ok` otherwise. `TestRecordingsHoldNoContent` runs it on every recording under a `testdata/` directory: each directory a `manifest.json` marks, and each one laid out as `testdata/<agent version>/<scenario>/` from before manifests. Fixtures written by hand sit under `synthetic/` or `not-recorded/`, and it skips them.

Runs on an API key are recorded by a workflow that is dispatched by hand, never by a pull request. It runs one scenario, scrubs and verifies its files on the runner, and uploads only the scrubbed directory, kept 3 days. The raw files never leave the runner.

```
make fixture RUN=<workflow run id> SCENARIO=<scenario>
```

downloads that artifact with `gh`, verifies it again and places it under `internal/adapter/<adapter>/testdata/<agent version>/<scenario>/`, with a `manifest.json` and a README that states the run, the date, the versions and the run's own total. A scenario already placed is refused. To record it again, remove it in its own commit first.

## Claude Code

| Canonical class         | Session file field                               | Result `modelUsage` field             |
| ----------------------- | ------------------------------------------------ | ------------------------------------- |
| `input_uncached`        | `usage.input_tokens`                             | `inputTokens`                         |
| `cache_read`            | `usage.cache_read_input_tokens`                  | `cacheReadInputTokens`                |
| `cache_write_5m`        | `usage.cache_creation.ephemeral_5m_input_tokens` | `cacheCreationEphemeral5mInputTokens` |
| `cache_write_1h`        | `usage.cache_creation.ephemeral_1h_input_tokens` | `cacheCreationEphemeral1hInputTokens` |
| `output`                | `usage.output_tokens`                            | `outputTokens`                        |
| `reasoning_output`      | `usage.output_tokens_details.thinking_tokens`    | `thinkingTokens`                      |
| `cached_input_reported` | `usage.cache_read_input_tokens`                  | `cacheReadInputTokens`                |

Input excludes cache reads and cache writes. Output includes thinking, and `reasoning_output` is never added to it.

Rows come from the session files, `<config>/projects/*/<session>.jsonl` and `<session>/subagents/*.jsonl`, and from any [nested session](#nested-sessions) beside them. A response's content blocks are separate lines sharing a `requestId`, and they count once. The adapter uses the files only when their billed classes equal the last result's `modelUsage`, per model. Otherwise it writes one set of rows per model, marked `agent_stream`, with a warning.

One kind of model is left out of that comparison: a model the result counts that no session file line and no assistant message on the stream names. Claude Code makes such a request outside the conversation, and writes it nowhere but the result's totals. Its rows take the result's total for that model, at run grain, marked `agent_stream`, or `execution_file` from an execution file, with the note `a request outside the conversation`. That total's whole input selects the context tier, as a [Codex turn's](#codex) does. The other models' rows stay per request.

Five `claude-code-action` runs on an API key, on 2026-11-04, each warned that the session files disagreed with the result. The three whose result was read counted `claude-haiku-5-5` at 1219 input and 18 output tokens, beside the `claude-opus-5-5` the workflow ran, and no message on the recorded stream named `claude-haiku-5-5`. The three `costroid run` runs of `claude -p --bare` that day read with no warning. A run recorded on 2026-11-07 kept the runner's session files, and they hold no line for the side request, which confirms the reading. Read with the rule above, that recording comes to 0.0391529 USD, the run's own `total_cost_usd`, with no warning. It is under `testdata/2.1.316/action-session/`.

What the runs showed:

- The last result's `modelUsage` is the run total, subagents included. The subagent run ended on 8 input, 15605 cache read, 3495 five-minute writes, 4450 one-hour writes, 272 output and 121 thinking tokens. The rows sum to the same figures.
- A run can emit two results. A background subagent woke the main thread, and the second result followed. `usage` covers only that last turn, and `modelUsage` is cumulative, so adding results double counts.
- On `error_max_budget_usd`, `usage` is zero in every field, and `modelUsage` still holds the spend.
- The output count on a stream assistant message is a placeholder: 16 on the wire, 84 final. The session file holds the final figure.
- A cap of 0.0001 USD ended at 0.0007324 USD. The check runs after a request, so the first request always completes. The agent then exited 1 with the tool call unexecuted.
- The subagent's writes were five-minute and the main thread's were one-hour, on one subscription. The lifetime is per request, so the rule that assigns an unreported lifetime is a last resort. `costroid/assumed_ttl` sits on the class that took those tokens.
- The pinned catalog equals Claude Code's own `total_cost_usd` on all three runs: 0.00060589, 0.001619725 and 0.0007324 USD. The rows never read that field.

Under `costroid run --gateway`, `SubAccountId` is the id of the key made for the run, the SHA-256 that LiteLLM's spend logs carry, and never a credential the caller states. A gateway run reads `none` when the key lacks a budget at or under the run's, an expiry, a parallel-request limit, a largest request or this run's id, when the gateway's store did not answer before, during or after the run, or when its rows, priced at the catalog, passed the key's budget by more than the bound they state. The e2e journey `TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob` runs it against LiteLLM v1.108.1 and a mock upstream: the gateway refuses a key at USD 0.01 before the run passes it by more than the bound, no refused request reaches the upstream, the key is gone after the job, and a run whose gateway store was stopped reads `none`.

A Claude Code `session.id` joins a row and never earns `exact`. Anthropic's usage `session_id` is a Managed Agents id, null outside a managed session ([usage analytics](https://platform.claude.com/docs/en/api/beta/organization/analytics/usage.md), retrieved 2026-11-01), so it cannot filter Anthropic's report. `exact` rests on the credential in `SubAccountId`.

`apiKeySource` was `none` on these runs. Costroid reads `none`, an absent value and any unknown value as a subscription. A recorded `claude -p` run on an API key, Claude Code 2.1.319, reads `ANTHROPIC_API_KEY`.

### Nested sessions

A hook or a plugin can start its own `claude -p`. That process writes its own session file under its own session id, and the run's result does not hold its spend. Costroid counts such a session when every rule holds:

- It sits in a project directory under `<config>/projects/`, the tree that holds the run's own session file.
- No line names the run's session id, and no line is a subagent's. Those files are the run's own record.
- Its first and last events fall inside the run's window.
- Outside CI, it also sits beside the main session file, in the run's project directory `<config>/projects/<directory>/`, and the first working directory it states, `cwd`, is the run's. The run's comes from the stream's `init` line, then from the main session file. A developer's machine runs unrelated sessions, and these two rules keep them out.

Inside CI, which Costroid reads from `GITHUB_ACTIONS` or `GITLAB_CI` set to `true`, the runner hosts one job. There a session in any project directory counts, whatever its `cwd`. That covers a plugin that starts its side calls in a temporary directory, because Claude Code files such a session under that directory's project directory.

Under `costroid run`, the window opens when Costroid starts the agent and closes when the agent exits. For an execution file, it opens at the earlier of the first message and the file's save time less the result's `duration_ms`, and closes at the later of the last message and the save time.

A counted session's rows carry its own `costroid/session_id`, `costroid/cost_source` `agent_session_file`, and the note `nested session: started and ended inside this run` in `ChargeDescription`. Its own subagents count with it. A request id the run already holds counts once. Its file does not state how it bills, and a process a hook starts inherits the run's environment, so its rows take the run's basis: `measured` when the run's `apiKeySource` names a key, `list_equivalent` otherwise. Nothing checks them against a result object: the nested process's result went to whatever started it.

A session that started inside the window and then wrote after the agent exited is not counted, and a warning names it. Outside CI, so is one that states another working directory. If the run states no working directory, no nested session is counted outside CI.

What Costroid cannot see:

- Outside CI, a nested process that runs in another working directory. It writes to another project directory.
- A process that writes no session file.
- A process that outlives the agent, such as a detached job started by a `SessionEnd` hook.
- A process that runs under another `CLAUDE_CONFIG_DIR`.
- Which of two agents started the other. Two agents run at once look like a run and its nested session, so a run that started before the other and ended after it would count the other's spend a second time. `costroid run` holds a lock, `costroid-run.lock` in the config directory, for as long as the agent runs. A second `costroid run` of Claude Code in that directory is refused before its agent starts, with the reason `concurrent_run`, and writes no rows. A `claude` started without Costroid takes no lock, so it still reads as a nested session.

No recorded run holds a nested session. The tests use `testdata/not-recorded/nested/`, written in the line shapes of the `subagent` recording. The recordings cut `cwd` because it holds a path. Claude Code 2.1.318 wrote `cwd` on its user, assistant and system lines on 2026-11-04. No recording shows `cwd` on the stream's `init` line, so that source is unverified.

## Codex

Costroid reads two sources. The rollout file gives per-request rows and the budget stop. The stream checks the file, and it is the record when the file cannot be read.

The rollout file is `<codex home>/sessions/<year>/<month>/<day>/rollout-<local time>-<thread>.jsonl`. Its format is undocumented, so the versions Costroid reads are pinned to the ones with a recorded fixture: `0.164.0-alpha.11.1`. The version comes from the file's `session_meta` line.

| Canonical class         | Rollout usage field and `turn.completed.usage` field                                                        |
| ----------------------- | ----------------------------------------------------------------------------------------------------------- |
| `input_uncached`        | `input_tokens` − `cached_input_tokens` − `cache_write_input_tokens`                                         |
| `cache_read`            | `cached_input_tokens`                                                                                       |
| `cache_write_30m`       | `cache_write_input_tokens`, with the tag `costroid/assumed_ttl`. Absent reads as zero, and no run wrote one |
| `output`                | `output_tokens`                                                                                             |
| `reasoning_output`      | `reasoning_output_tokens`                                                                                   |
| `cached_input_reported` | `cached_input_tokens`                                                                                       |

Codex reports one write total with no lifetime. 30 minutes is OpenAI's default ([prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching), retrieved 2026-11-01), and Codex drops `cache_write_tokens_details` ([`responses.rs`](https://github.com/openai/codex/blob/67f0994b532b8740f14b5b738af7b7b2a915b1d6/codex-rs/codex-api/src/sse/responses.rs), retrieved 2026-11-01), so a 12-hour share cannot be seen. Every write lands in `cache_write_30m` at that class's price, and the row carries `costroid/assumed_ttl: 30m`. The tokens never move to another class.

A request settles on the first of two lines. `token_usage_record` carries `usage`, `thread_token_usage` and `response_id`. `token_count` carries `last_token_usage` and `total_token_usage`. Both state the same running total, so a total that has not moved is not a new request, and a total that moved by anything but the request's own usage stops the reading.

Rows from the file are one per request. Their id is `response_id`, or `<thread>:request:<n>` when only a `token_count` was seen. Their context tier comes from that request's `input_tokens`, their model from the last `turn_context`, and their `costroid/cost_source` is `agent_session_file`.

The grade is decided when the run ends:

| What happened                                          | Grade              | Rows                                 |
| ------------------------------------------------------ | ------------------ | ------------------------------------ |
| The file was read, and its total equals the stream's   | `wrapper-watchdog` | One per request                      |
| The file was read, and the run ended inside a turn     | `wrapper-watchdog` | One per request, with a warning      |
| `--ephemeral`, which the help says persists no session | `none`             | One per turn, stream                 |
| No file appeared for the thread                        | `none`             | One per turn, stream                 |
| The file names a version with no fixture               | `none`             | One per turn, stream                 |
| The file contradicts itself or the stream              | `none`             | One per turn, stream, if it reported |

A `none` run is never stopped, and every row says why. Its id is `<thread>:turn:<n>`.

The basis follows the key Codex runs on. Under `costroid run`, rows read `measured` when `CODEX_API_KEY` is set and `list_equivalent` otherwise. Codex refused a key given only as `OPENAI_API_KEY` in a recorded run (GitHub Actions run 42379804344, scenario `codex-openai-key`), and the action's `run` stage passes a key it finds under that name to Codex as `CODEX_API_KEY`. `costroid ingest-run --codex-home` reads a run of `openai/codex-action`, which takes only an API key, its `openai-api-key` input, so those rows read `measured`.

A turn row takes its context tier from the turn's whole input, which no request in the turn can pass. At or below the sheet's boundary, 272000 input tokens on the pinned OpenAI sheet, every request in the turn is in the lowest tier, so that tier is each request's own. Above it, the stream cannot say which requests passed the boundary, so the tier above prices the whole turn and the row says the figure is an upper bound. A turn of several requests that each stayed below the boundary is then priced above its bill. The boundary is read from the sheet, never from the code.

What the runs showed:

- `turn.completed.usage` is the thread's running total. The resumed turn reported 52174 input tokens, which is 33907 from the first run plus its own 18267. `costroid run` refuses `codex exec resume` for that reason.
- One `codex exec` prompt is one turn. The first run made two model requests, 16888 and 17019 input tokens, and `turn.completed` arrived once, last. The stream cannot stop a one-prompt run partway, which is why the watchdog reads the file.
- `token_count` waits for tools. In the `stopped` recording, a 20-second `sleep` was running when the `token_usage_record` of its request landed, and no `token_count` was written. Watching `token_count` alone would have stopped that run 20 seconds later.
- The stopped run ended 65 milliseconds after that record, at 0.0283136 USD, under a 0.001 USD cap. The first request always completes, because usage is reported after it.
- A stop can leave a request uncounted. If a model request is in flight, Codex is killed before it reports usage, so its tokens reach no row. The rollout file shows when a request left: at `task_started`, and at each `function_call_output` or `custom_tool_call_output`, the tool result Codex sends as the next request. A run that ended inside a turn says how many requests were in flight and when the last left, or that the files show none. In the `stopped` recording a tool was running, so none was.
- A stop does not reach a running tool. Codex starts tool commands in their own process group, so the `sleep` outlived the agent and ended on its own. A tool spends no model tokens, and it can still change files.
- A Codex subagent thread writes its own rollout file. On 2026-11-04, the rollout files Codex 0.147.0-alpha.6.5 had left on one machine held 7 subagent threads. Each `session_meta` line carried `thread_source` `subagent` and a `session_id` naming the parent thread, and each file held its own usage lines. Costroid reads every rollout file whose `session_meta` names the run's thread, or one of its subagent threads, that way. Each subagent request is one row, under its own `response_id`, at its own model, and it counts toward the cap. No recorded 0.164.0-alpha.11.1 run had a subagent, so the fixture under `testdata/not-recorded/subagent/` is written, and whether the stream's total includes that spend is unverified. The file reading stands when the stream's total equals the run's own file or that file plus its subagents'.
- A separate `codex exec` that a plugin or a tool command starts has the shape of a Claude Code nested session: its own thread and its own rollout file, which Costroid does not read. Under `--ephemeral` it writes no rollout file, so no file can show it. Costroid counts no nested Codex session.
- The stream names no model and no request id. Without a rollout file, the model comes from `-m`, `--model`, `-c model=` or the top-level `model` key of `config.toml`, and otherwise reads `unknown` and prices at the fallback tier.
- A `codex.sse_event` reported a `response.completed` of 2 input tokens before the first turn, from startup. It is in neither the turn total nor the rollout file.

## claude-code-action

`costroid ingest-run --execution-file F` reads the file the action names in its `execution_file` output: a JSON array of every SDK message, pretty-printed, at `$RUNNER_TEMP/claude-execution-output.json` ([`execution-file.ts`](https://github.com/anthropics/claude-code-action/blob/d535f5172dec02b204866b393d6b20b9b2aaf98d/base-action/src/execution-file.ts), read 2026-11-01). The messages are the ones `claude -p` streams, so one session reads both, and the last result's `modelUsage` is the run.

| Field                  | Value                                                                                                                                                                |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `costroid/cost_source` | `execution_file`, or `agent_session_file` when the runner still holds session files that agree. A request outside the conversation reads `execution_file` either way |
| Grain                  | One set of rows per model, unless those session files agree. Then one row per request, and one set per model for requests outside the conversation                   |
| Grade                  | `agent-flag` when `--max-budget-usd` in the step's `claude_args` equals the cap the read step is given; otherwise `none`, with the reason                             |
| Charge period          | The messages' own timestamps. With none, the file's save time back by the result's `duration_ms`                                                                     |
| Basis                  | `measured` when `apiKeySource` names a key, otherwise `list_equivalent`                                                                                              |

The fixture is under `testdata/not-recorded/`. No run of the action made it. It is the recorded `subagent` stream, cut to the SDK's messages, with one synthetic prompt that a test holds out of every row. Rows come to USD 0.001619725, the recorded total.

## Join attributes

`costroid run` gives every agent the run's join keys in `OTEL_RESOURCE_ATTRIBUTES`, so the agent's own telemetry names the pull request it ran for. They are the OpenTelemetry `vcs.*` and `cicd.*` attributes:

| Attribute                   | GitHub Actions                                                                                                  | GitLab CI                                                  |
| --------------------------- | --------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `vcs.change.id`             | The pull request's number, from the event file                                                                  | `CI_MERGE_REQUEST_IID`                                     |
| `vcs.ref.head.revision`     | The pull request's head commit, from the event file; `GITHUB_SHA` on `push`, `schedule` and `workflow_dispatch` | `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA`, else `CI_COMMIT_SHA` |
| `vcs.ref.base.revision`     | The pull request's base commit, from the event file                                                             | `CI_MERGE_REQUEST_DIFF_BASE_SHA`                           |
| `vcs.repository.url.full`   | `GITHUB_SERVER_URL`, then `/` and `GITHUB_REPOSITORY`                                                           | `CI_PROJECT_URL`                                           |
| `cicd.pipeline.run.id`      | `GITHUB_RUN_ID`                                                                                                 | `CI_PIPELINE_ID`                                           |
| `cicd.pipeline.task.run.id` | `GITHUB_JOB`                                                                                                    | `CI_JOB_ID`                                                |

The job's own attributes stay, and a join key the job set already is replaced. A value that fails its tag pattern is left out, and outside CI none is set. A character the variable's syntax does not allow is percent-encoded. Like the rows' tags, these are what the CI system states, and nothing checks them before a server does.

## Stdin

Costroid passes its stdin to the agent unchanged. When that stdin is a pipe with no data, `claude -p` waits three seconds and prints a warning. The warning is Claude Code's. A caller who wants it gone redirects `< /dev/null`.

## The fixture questions

1. Does Claude Code's result `usage` carry `cache_creation`? Yes, for that result's last turn. Does `claude_code.api_request.output_tokens` equal the final `message_delta` count? Yes: 84, 60 and 124 on the three main-thread requests. The subagent's request has no `message_delta` on the stream, and its event equals the session file at 4.
2. Does `codex exec resume` repeat earlier spend on `turn.completed`? Yes. Does `OTEL_RESOURCE_ATTRIBUTES` reach Codex spans and metrics? Yes: `vcs.change.id` and `cicd.pipeline.run.id` appeared as resource attributes on traces, logs and metrics.
3. Copilot's `inputTokens` overlap and `copilot --output-format json`: not settled. No Copilot run was in scope.
4. Cursor's usage event grain and `cloudAgentId`: not settled. No Cursor run was in scope.
5. OpenCode's `step_finish` on 1.19.5 and 2.2.5: not settled. No OpenCode run was in scope.

## Price ids

Every row's `SkuPriceId` has one of two shapes. Both open with the provider and close with the token class, as `SkuId` does.

| Shape                                                  | Rows                                                                                                                                                                                                                         |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<provider>:<model>:<region>:<tier>:<context>:<class>` | A row priced by a catalog unit price, for example `openai:gpt-5.6-sol:global:standard:to-272000:input_uncached`. `<context>` is the context tier: `all`, `to-<tokens>` or `over-<tokens>`                                    |
| `<provider>:<kind>:<class>`                            | A row with no unit price of its own. `<kind>` is `fallback` when the catalog's fallback row priced a model, tier or class the sheet does not name, and `informational` for a class that repeats tokens a billed class counts |

Releases up to `1.0.0-alpha.5` wrote an informational row's id as `<provider>/<model>/<class>/informational`.

## Recorded facts easy to get wrong

- `modelUsage` has a lifetime split on 2.1.316, in `cacheCreationEphemeral5mInputTokens` and `cacheCreationEphemeral1hInputTokens`.
- Stream assistant messages carry `request_id`.
- `model_usage_since_last_result` sits beside `modelUsage` on each result.
