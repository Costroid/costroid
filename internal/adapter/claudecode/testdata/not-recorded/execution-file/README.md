# Not a recording

No run of `claude-code-action` made this file.

It follows what `research/2026-11-adapter-matrix.md` and the action's source state: a JSON array of every SDK message, pretty-printed, at `$RUNNER_TEMP/claude-execution-output.json` ([`execution-file.ts`](https://github.com/anthropics/claude-code-action/blob/d535f5172dec02b204866b393d6b20b9b2aaf98d/base-action/src/execution-file.ts), read 2026-11-01). The messages are the recorded `2.1.316/subagent/stream.jsonl` with its `stream_event` lines left out, plus one synthetic `user` message that holds prompt text. A test proves that text never reaches a row.

Replace it with a real file when one exists.
