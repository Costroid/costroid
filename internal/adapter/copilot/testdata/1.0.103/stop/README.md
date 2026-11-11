# stop

A recording of Copilot CLI 1.0.103, workflow run 42945131550 on 2026-11-11, under Costroid at `e91e45780b83e9de2319e0062bf61f1b8321c5f2`. `go run ./scripts/record verify` passed on it before and after it was placed here.

No session.shutdown line states the run's own total.

The job ran `costroid run --budget-usd 0.001 --out rows.jsonl -- copilot -p PROMPT --allow-all-tools --deny-tool=write --deny-tool=shell` on Costroid 1.0.0-alpha.21, with `COPILOT_HOME` set to a directory made for the job, on the Anthropic API key of the repository that recorded it, model `claude-haiku-5-5`. `costroid run` adds `--output-format json` and `--session-id` with a new UUID, and reads the named session's file during the run. No GitHub account was signed in.

`stream.jsonl` is the run's stdout. It holds no `result` line: the run was stopped before Copilot CLI printed one. `events.jsonl` is the session's `session-state/<session>/events.jsonl` under `COPILOT_HOME`. It holds one `session.usage_record`, at 02:18:57.034Z, and no `session.shutdown`:

| Request            | `inputTokens` | Cache read | Cache write | Uncached input | Output |
| ------------------ | ------------- | ---------- | ----------- | -------------- | ------ |
| `msg_fixture_0001` | 20147         | 0          | 20143       | 4              | 97     |

The request asked for a tool, and Copilot CLI refused it. The last line it wrote, at 02:18:57.180Z in `stream.jsonl`, ends that tool. No second request started. `costroid run` stopped the agent at the cap and exited 124.

At the pinned catalog's list prices for `claude-haiku-5-5` (`catalog/0.3.0/anthropic-2026-11-08.json`: 0.10, 0.125 and 0.50 USD per million uncached input, five-minute write and output tokens), the request comes to 0.002566775 USD, `measured`: its usage record states `isByok` with `byokKind` `remote_user`. That is the list price of the tokens the key paid for, not the provider's bill. It passes the 0.001 USD cap by 0.001566775 USD. Usage is reported only when a request ends, so the request in flight when the cap was crossed was paid in full. `TestCopilotRecordedStopAtTheCap` checks it.
