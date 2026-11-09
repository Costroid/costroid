# byok

A recording of Copilot CLI 1.0.103, workflow run 42629874786 on 2026-11-09. `go run ./scripts/record verify` passed on it before and after it was placed here.

The run's own total, the `modelMetrics` of `session.shutdown` in `events.jsonl`: `claude-haiku-5-5` 65384 input, cache included, 43418 cache read, 21958 cache write, 229 output, 0 reasoning tokens. Copilot states a `totalNanoAiu` of 0, its own unit, which Costroid does not convert.

The job ran `copilot -p PROMPT --output-format json --allow-all-tools --deny-tool=write --deny-tool=shell` with `COPILOT_HOME` set to a directory made for the job, on the Anthropic API key of the repository that recorded it. It set `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_PROVIDER_TYPE=anthropic` and `COPILOT_MODEL=claude-haiku-5-5`, the variables `copilot help providers` lists on 1.0.103, read on workflow run 42629666568. No GitHub account was signed in.

`stream.jsonl` is the run's stdout. It holds no token count, and only its `result` line names the session. `events.jsonl` is the session's `session-state/<session>/events.jsonl` under `COPILOT_HOME`. Its three `session.usage_record` lines are the run's three model requests, and its `session.shutdown` states their totals:

| Request            | `inputTokens` | Cache read | Cache write | Uncached input | Output |
| ------------------ | ------------- | ---------- | ----------- | -------------- | ------ |
| `msg_fixture_0001` | 21660         | 0          | 21656       | 4              | 77     |
| `msg_fixture_0002` | 21764         | 21656      | 106         | 2              | 73     |
| `msg_fixture_0003` | 21960         | 21762      | 196         | 2              | 79     |

Each `inputTokens` is the uncached input plus the cache reads and writes. At the pinned catalog's list prices for `claude-haiku-5-5` (`catalog/0.3.0/anthropic-2026-11-08.json`: 0.10, 0.01, 0.125 and 0.50 USD per million uncached input, cache read, five-minute write and output tokens), the run comes to 0.00329423 USD. That is the list price of the tokens the key paid for, not the provider's bill. `TestCopilotUsageRowsEqualShutdownMetrics` checks it.
