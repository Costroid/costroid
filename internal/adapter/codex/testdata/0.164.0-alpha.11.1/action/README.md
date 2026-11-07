# A recorded openai/codex-action run

A GitHub Actions run, id 42377007560, recorded this file on 2026-11-07 with `costroid` at `f69a6ec`, scenario `codex-action` (`manifest.json`). The job ran `openai/codex-action` at `bdf19a4a223ec2549a3e2274a0cf61556bc07675`, the commit of its tag `v1.13`, with `codex-version` 0.164.0-alpha.11.1 and `codex-home` set to a directory made for the job. Costroid started nothing: the recording step read the action's Codex home after the action ended. `go run ./scripts/record verify` passed on it before and after it was placed here. The recording was scrubbed before it was saved: it keeps only the keys Costroid reads, and its ids are stand-ins.

`rollout.jsonl` is the one rollout file the action left in its Codex home. It holds one turn on `gpt-6.1-sol` with two model requests, each settled by a `token_usage_record` and a `token_count`. The last `token_count`'s `total_token_usage`: 21734 input tokens, of them 10770 cached and 10958 cache writes, so 6 uncached; 84 output tokens, 0 of them reasoning. The two requests add up to it:

| Request      | Uncached input | Cache read | Cache write | Output |
| ------------ | -------------- | ---------- | ----------- | ------ |
| `response-1` | 3              | 0          | 10770       | 51     |
| `response-2` | 3              | 10770      | 188         | 33     |

At the pinned catalog's list prices for `gpt-6.1-sol` (`catalog/0.1.0/openai-2026-11-01.json`: 2.00, 0.10, 2.50 and 10.00 USD per million tokens, cache writes taken as the 30-minute class), `response-1` comes to 0.027441 USD and `response-2` to 0.001883 USD, 0.029324 USD together. This is a list-price figure, not a bill. `TestCodexActionHome` checks it.
