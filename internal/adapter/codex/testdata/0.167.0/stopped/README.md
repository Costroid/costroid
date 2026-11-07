# A recorded Codex 0.167.0 run

A GitHub Actions run, id 42368735876, recorded these files on 2026-11-07 with `costroid` at `f69a6ec`, scenario `codex-stopped-current` (`manifest.json`). The recording was scrubbed before it was saved: message text reads `@content`, and ids are stand-ins.

The run is one `codex exec` turn on `gpt-6.1-sol` with two model requests, each settled in the rollout file by a `token_usage_record` and a `token_count`. The scenario stops the run, but in this recording the turn ended first: the stream holds `turn.completed`, and the rollout file holds `task_complete`.

The run's own total, as the stream's `turn.completed` states it: 26442 input tokens, of them 13181 cached and 13255 cache writes, so 6 uncached; 56 output tokens, 0 of them reasoning. The two requests add up to the same total:

| Request      | Uncached input | Cache read | Cache write | Output |
| ------------ | -------------- | ---------- | ----------- | ------ |
| `response-1` | 3              | 0          | 13181       | 51     |
| `response-2` | 3              | 13181      | 74          | 5      |

At the pinned catalog's list prices for `gpt-6.1-sol` (`catalog/0.1.0/openai-2026-11-01.json`: 2.00, 0.10, 2.50 and 10.00 USD per million tokens, cache writes taken as the 30-minute class), the two requests come to 0.0350276 USD. This is a list-equivalent figure, not a bill. `TestRolloutShapeGate` checks it.

0.167.0 is newer than the version the recipes pin, 0.164.0-alpha.11.1. The rollout reader reads any version whose file keeps the shape it checks. A recorded version only reads without the note that names an unrecorded one.
