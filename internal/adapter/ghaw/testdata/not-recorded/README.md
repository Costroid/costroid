# Usage artifacts written by hand

Each directory here is laid out as gh-aw's `usage` artifact: `agent/token_usage.jsonl`, `detection/token_usage.jsonl` and `aw_info.json`. Every record follows gh-aw's token-usage schema of firewall release v0.29.8 (https://github.com/github/gh-aw-firewall/releases/download/v0.29.8/token-usage.schema.json, read 2026-11-07). The values were written by hand, and none came from a run.

`claude` holds a router request, two agent requests, a 529 with no tokens, a 500 that carried tokens, and one threat detection request. `codex` holds the two requests of the recorded Codex 0.167.0 run in `internal/adapter/codex/testdata/0.167.0`, written as the proxy states OpenAI's usage: `input_tokens` holds the cached tokens and the cache writes.
