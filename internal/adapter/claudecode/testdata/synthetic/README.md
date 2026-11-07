# Not recordings

No run of Claude Code wrote these files. Each one holds shapes the reader must survive, built by hand from the recorded `2.1.316` files and from Claude Code's documented `stream-json` messages. Every content field holds a sentinel.

- `stream/every-shape.jsonl`: lines of every shape the stream reader skips, beside a result whose counts are stated as `10`, `10.0`, `"10"`, `1e2` and `null`.
- `stream/auth-failure.jsonl`: a run whose key was refused. The assistant line states the class in its `error` field, and the result says `is_error` with no usage. The shape is inferred from the documented message types, not observed.
- `stream/usage-without-model-usage.jsonl`: a result that states usage and no per-model totals, as a Claude Code version without `modelUsage` would write.
- `execution/odd-elements.json`: an execution file with elements that are not messages, a numeric timestamp and a fractional `duration_ms`.
- `execution/truncated.json`: an execution file cut off inside its array.
