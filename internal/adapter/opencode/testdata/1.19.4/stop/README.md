# stop

A recording of OpenCode 1.19.4, workflow run 43223648675 on 2026-11-12, under Costroid at `1.0.0-alpha.24`. `go run ./scripts/record verify` passed on it before and after it was placed here.

OpenCode's own figure for the run: 0.001438275 USD, the `cost` of the one `step_finish` line in `stream.jsonl`. OpenCode prices each step from its own price list.

The job ran `costroid run --agent opencode --budget-usd 0.001 --out FILE -- opencode run --model anthropic/claude-haiku-5-5 PROMPT` on the released Costroid 1.0.0-alpha.24 binary, on an Anthropic API key, with OpenCode's config and data directories made for the job. `costroid run` added `--title=`, so OpenCode sent no request for a title.

`stream.jsonl` is the run's stdout. It holds one step: `step_start` at 13:04:01.669Z, two tool calls, `read` and `glob`, both `completed`, and `step_finish` at 13:04:02.799Z with the reason `tool-calls`. The model asked for tools, so OpenCode would send their results in a second request. No second `step_start` follows.

| Step               | Uncached input | Cache read | Cache write | Output | Reasoning | OpenCode's `cost` |
| ------------------ | -------------- | ---------- | ----------- | ------ | --------- | ----------------- |
| `prt_fixture_0004` | 4              | 0          | 10543       | 173    | 67        | 0.001438275       |

OpenCode's output leaves out the reasoning, so the rows count 240 output tokens, 173 + 67, and the 67 again as informational reasoning. At the pinned catalog's list prices for `claude-haiku-5-5` (`catalog/0.4.1/anthropic-2026-11-12.json`: 0.10, 0.125 and 0.50 USD per million uncached input, five-minute write and output tokens), the step comes to (4 × 0.10 + 10543 × 0.125 + 240 × 0.50) / 1,000,000 = 0.001438275 USD, `measured`, the figure OpenCode stated. It passes the 0.001 USD cap by 0.000438275 USD. OpenCode reports a step's tokens when the step ends, and the wrapper stops the run between steps, so the step that crossed the cap was paid in full.

The run printed `costroid: opencode, 1 request`, `USD 0.001438275 at list, Measured`, `stopped the agent at the cap` and `over budget by USD 0.000438275`, wrote 4 rows and exited 124. Costroid 1.0.0-alpha.24 planned OpenCode at the grade `none`, so those rows read `none`. The stop is the same in this release, which plans `wrapper-watchdog`.

OpenCode's own store, read after the run, holds the prompt and one assistant message, with this step's tokens and cost. Its event log ends with that message's update at 13:04:02.796Z. In this run OpenCode stored the step's message at 13:03:59.971Z, before it sent the step's request at 13:04:00.641Z by its log. No second message was stored. The provider's own count of requests is not in the recording.

The scrub dropped each tool's input, output, title, metadata and times, the call ids and the snapshot hashes, and gave the session, part and message ids synthetic forms. The prompt, the file the agent read and every path are gone. The run's rows, its stderr and OpenCode's data directory, with its SQLite store and its log, are not kept: they hold paths and the id of the key. This README states what was read from them. `TestOpenCodeRecordedStopHoldsTheCap` replays the stream under `costroid run` with the recorded cap.
