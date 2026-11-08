# demo

`rows.jsonl` holds the rows `costroid demo` renders its comment from. `costroid ingest-run` wrote them, outside CI, from the recording in `internal/adapter/claudecode/testdata/2.1.316/action-session/`: Claude Code 2.1.316 under claude-code-action, workflow run 42368537950 on 2026-11-07. It read the recorded session file from `CLAUDE_CONFIG_DIR`, and the execution file with its modification time set to 2026-11-07 11:33:30 UTC.

The rows come to 0.0391529 USD at list, the `total_cost_usd` the run itself reported. `TestDemoRowsAreWhatIngestRunReadsFromTheRecording` writes them again from the recording and compares them byte for byte.

`capped.jsonl` holds the rows of `costroid demo --state capped`: a recorded Codex 0.164.0-alpha.11.1 run that costroid stopped at a USD 0.001 cap, the recording in `internal/adapter/codex/testdata/0.164.0-alpha.11.1/stopped/`. Its one request of 5632 uncached input, 11264 cached input and 64 output tokens comes to 0.0283136 USD at catalog 0.1.0. The file is a copy of `internal/comment/testdata/codex-stopped.jsonl`, which the comment's goldens read, and `TestDemoCappedRowsAreTheRecordedCodexStop` holds the two equal.
