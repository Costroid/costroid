# demo

`rows.jsonl` holds the rows `costroid demo` renders its comment from. `costroid ingest-run` wrote them, outside CI, from the recording in `internal/adapter/claudecode/testdata/2.1.316/action-session/`: Claude Code 2.1.316 under claude-code-action, workflow run 42368537950 on 2026-11-07. It read the recorded session file from `CLAUDE_CONFIG_DIR`, and the execution file with its modification time set to 2026-11-07 11:33:30 UTC.

The rows come to 0.0391529 USD at list, the `total_cost_usd` the run itself reported. `TestDemoRowsAreWhatIngestRunReadsFromTheRecording` writes them again from the recording and compares them byte for byte.
