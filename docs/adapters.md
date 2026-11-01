# Adapters

What each agent's stream carries, as recorded on November 1, 2026. The fixtures sit beside each adapter under `testdata/<agent version>/`, and every figure below is asserted by a test.

## What the budget means

| Agent       | Grade              | What holds the run to the budget                                                                                                                            |
| ----------- | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Claude Code | `agent-flag`       | Costroid passes `--max-budget-usd`. Claude Code checks its own estimate between requests, so the request that crosses the cap still runs and is still paid. |
| Codex       | `wrapper-watchdog` | Costroid reads Codex's rollout file during the turn, prices each request as it settles and stops the process at the cap. Requests in flight can pass it.    |
| Codex       | `none`             | The rollout file could not be read: `--ephemeral`, a Codex version with no recorded fixture, or a file that contradicts the stream. Nothing stops the run.  |

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

Fixtures are cut to an allowlist of keys. Prompts, tool output, paths, user and account ids, and rate-limit data are gone, and session, thread, request and message ids are replaced by synthetic ones. Each adapter has a test that scans its `testdata/` for those patterns.

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

Rows come from the session files, `<config>/projects/*/<session>.jsonl` and `<session>/subagents/*.jsonl`. A response's content blocks are separate lines sharing a `requestId`, and they count once. The adapter uses the files only when their billed classes equal the last result's `modelUsage`, per model. Otherwise it writes one set of rows per model, marked `agent_stream`, with a warning.

What the runs showed:

- The last result's `modelUsage` is the run total, subagents included. The subagent run ended on 8 input, 15605 cache read, 3495 five-minute writes, 4450 one-hour writes, 272 output and 121 thinking tokens. The rows sum to the same figures.
- A run can emit two results. A background subagent woke the main thread, and the second result followed. `usage` covers only that last turn, and `modelUsage` is cumulative, so adding results double counts.
- On `error_max_budget_usd`, `usage` is zero in every field, and `modelUsage` still holds the spend.
- The output count on a stream assistant message is a placeholder: 16 on the wire, 84 final. The session file holds the final figure.
- A cap of 0.0001 USD ended at 0.0007324 USD. The check runs after a request, so the first request always completes. The agent then exited 1 with the tool call unexecuted.
- The subagent's writes were five-minute and the main thread's were one-hour, on one subscription. The lifetime is per request, so the rule that assigns an unreported lifetime is a last resort, and the row says when it applied.
- The pinned catalog equals Claude Code's own `total_cost_usd` on all three runs: 0.00060589, 0.001619725 and 0.0007324 USD. The rows never read that field.

`apiKeySource` was `none` on these runs. Costroid reads `none`, an absent value and any unknown value as a subscription. The API-key values in the code are unverified by a recording.

## Codex

Costroid reads two sources. The rollout file gives per-request rows and the budget stop. The stream checks the file, and it is the record when the file cannot be read.

The rollout file is `<codex home>/sessions/<year>/<month>/<day>/rollout-<local time>-<thread>.jsonl`. Its format is undocumented, so the versions Costroid reads are pinned to the ones with a recorded fixture: `0.164.0-alpha.11.1`. The version comes from the file's `session_meta` line.

| Canonical class         | Rollout usage field and `turn.completed.usage` field                                            |
| ----------------------- | ----------------------------------------------------------------------------------------------- |
| `input_uncached`        | `input_tokens` − `cached_input_tokens` − `cache_write_input_tokens`                             |
| `cache_read`            | `cached_input_tokens`                                                                           |
| `cache_write_30m`       | `cache_write_input_tokens`, lifetime assumed. Absent reads as zero, and no run recorded a write |
| `output`                | `output_tokens`                                                                                 |
| `reasoning_output`      | `reasoning_output_tokens`                                                                       |
| `cached_input_reported` | `cached_input_tokens`                                                                           |

`cache_write_30m` is the catalog's class name. Row contract 0.2 does not list it yet, so a row carrying it fails that schema. Costroid writes it anyway, and never moves the tokens to another class.

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

A `none` run is never stopped, and every row says why. A turn row takes the lowest context tier and says so, because a sum cannot select a per-request tier. A request above 272000 input tokens is under-priced at that grain. Its id is `<thread>:turn:<n>`.

What the runs showed:

- `turn.completed.usage` is the thread's running total. The resumed turn reported 52174 input tokens, which is 33907 from the first run plus its own 18267. `costroid run` refuses `codex exec resume` for that reason.
- One `codex exec` prompt is one turn. The first run made two model requests, 16888 and 17019 input tokens, and `turn.completed` arrived once, last. The stream cannot stop a one-prompt run partway, which is why the watchdog reads the file.
- `token_count` waits for tools. In the `stopped` recording, a 20-second `sleep` was running when the `token_usage_record` of its request landed, and no `token_count` was written. Watching `token_count` alone would have stopped that run 20 seconds later.
- The stopped run ended 65 milliseconds after that record, at 0.0283136 USD, under a 0.001 USD cap. The first request always completes, because usage is reported after it.
- A stop can leave a request uncounted. If a model request is in flight, Codex is killed before it reports usage, so its tokens reach no row. The run says so in a warning.
- A stop does not reach a running tool. Codex starts tool commands in their own process group, so the `sleep` outlived the agent and ended on its own. A tool spends no model tokens, and it can still change files.
- Unverified: a Codex subagent thread may write its own rollout file, which Costroid would not read. No recorded run had one.
- The stream names no model and no request id. Without a rollout file, the model comes from `-m`, `--model`, `-c model=` or the top-level `model` key of `config.toml`, and otherwise reads `unknown` and prices at the fallback tier.
- A `codex.sse_event` reported a `response.completed` of 2 input tokens before the first turn, from startup. It is in neither the turn total nor the rollout file.

## claude-code-action

`costroid ingest-run --execution-file F` reads the file the action names in its `execution_file` output: a JSON array of every SDK message, pretty-printed, at `$RUNNER_TEMP/claude-execution-output.json` ([`execution-file.ts`](https://github.com/anthropics/claude-code-action/blob/d535f5172dec02b204866b393d6b20b9b2aaf98d/base-action/src/execution-file.ts), read 2026-11-01). The messages are the ones `claude -p` streams, so one session reads both, and the last result's `modelUsage` is the run.

| Field                  | Value                                                                                                    |
| ---------------------- | -------------------------------------------------------------------------------------------------------- |
| `costroid/cost_source` | `execution_file`, or `agent_session_file` when the runner still holds session files that agree           |
| Grain                  | One set of rows per model, unless those session files agree                                              |
| Grade                  | `none`. Costroid did not start the run, and the file states no budget                                    |
| Charge period          | The messages' own timestamps. With none, the file's save time back by the result's `duration_ms`         |
| Basis                  | `measured` when `apiKeySource` names a key, otherwise `list_equivalent`                                  |

The fixture is under `testdata/not-recorded/`. No run of the action made it. It is the recorded `subagent` stream, cut to the SDK's messages, with one synthetic prompt that a test holds out of every row. Rows come to USD 0.001619725, the recorded total.

## Stdin

Costroid passes its stdin to the agent unchanged. When that stdin is a pipe with no data, `claude -p` waits three seconds and prints a warning. The warning is Claude Code's. A caller who wants it gone redirects `< /dev/null`.

## The fixture questions

1. Does Claude Code's result `usage` carry `cache_creation`? Yes, for that result's last turn. Does `claude_code.api_request.output_tokens` equal the final `message_delta` count? Yes: 84, 60 and 124 on the three main-thread requests. The subagent's request has no `message_delta` on the stream, and its event equals the session file at 4.
2. Does `codex exec resume` repeat earlier spend on `turn.completed`? Yes. Does `OTEL_RESOURCE_ATTRIBUTES` reach Codex spans and metrics? Yes: `vcs.change.id` and `cicd.pipeline.run.id` appeared as resource attributes on traces, logs and metrics.
3. Copilot's `inputTokens` overlap and `copilot --output-format json`: not settled. No Copilot run was in scope.
4. Cursor's usage event grain and `cloudAgentId`: not settled. No Cursor run was in scope.
5. OpenCode's `step_finish` on 1.19.5 and 2.2.5: not settled. No OpenCode run was in scope.

## Recorded facts easy to get wrong

- `modelUsage` has a lifetime split on 2.1.316, in `cacheCreationEphemeral5mInputTokens` and `cacheCreationEphemeral1hInputTokens`.
- Stream assistant messages carry `request_id`.
- `model_usage_since_last_result` sits beside `modelUsage` on each result.
