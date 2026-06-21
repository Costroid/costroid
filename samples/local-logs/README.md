# `samples/local-logs/` — synthetic dev-tool logs (pack a, the local-usage ledger)

**SYNTHETIC data — NO real prompts, NO real responses, NO real account identity, NO
credentials (Cardinal Rule R4).** Every transcript line is hand-authored to the on-disk
shape Costroid's provider adapters parse. No prompt/response *content* is stored — only
bounded usage metadata (token counts, model ids, timestamps, a fictitious cwd). Nothing
came from a real user's logs.

This is the **developer-tool cost-lane** demo input — the "local-usage ledger" Costroid
reads by default, entirely from local files, with nothing leaving the machine.

## Files

| Path | Provider | Shape |
|---|---|---|
| `claude/projects/acme-rust-cli/transcript.jsonl` | Claude Code | 3 assistant turns (`claude-opus-4-8` ×1, `claude-sonnet-4-6` ×2), each with a unique `(message.id, requestId)` de-dup key and a `message.usage` block (`input_tokens`/`output_tokens`/`cache_read_input_tokens`/`cache_creation_input_tokens`). User prompts are placeholders (no content). |
| `codex/sessions/2026/06/20/rollout-demo.jsonl` | Codex | A `session_meta` line (`model: gpt-5.5`, `model_provider: openai`) + 2 `response_item` turns carrying `payload.info.last_token_usage` (`input_tokens` is the FULL prompt incl. `cached_input_tokens`, per the OpenAI convention the importer un-doubles). |

## How the demo uses it

Point the provider-discovery env vars at this tree, then export (nothing else leaks in
because the demo neutralizes every other discovery override):

```bash
CLAUDE_CONFIG_DIR=samples/local-logs/claude \
CODEX_HOME=samples/local-logs/codex \
  costroid export --format csv
```

→ **14 `developer_tool`-lane FOCUS rows** (one per model × token-meter), all **priced** from the
bundled pricing catalog (`x_Estimated = true` — cost is your tokens × current prices, never the
authoritative bill). Pinned totals: **622,000** total `x_ConsumedTokens`, **1.865000 USD** total
`BilledCost`.

Per-turn token inputs (before per-meter expansion):

| Turn | Model | uncached input | output | cache_read |
|---|---|---|---|---|
| Claude 1 | `claude-opus-4-8` | 100,000 | 5,000 | 200,000 |
| Claude 2 | `claude-sonnet-4-6` | 50,000 | 10,000 | 100,000 |
| Claude 3 | `claude-sonnet-4-6` | 50,000 | 5,000 | 0 |
| Codex 1 | `gpt-5.5` | 20,000 | 8,000 | 40,000 |
| Codex 2 | `gpt-5.5` | 20,000 | 4,000 | 10,000 |

(Codex rows are written as `input_tokens = uncached + cached`; the importer subtracts `cached`
so the priced uncached-input meter is correct — e.g. Codex turn 1 is `60000 - 40000 = 20000`.)

## How to regenerate

These are hand-authored transcript fixtures (they represent *foreign* tool logs, not Costroid
output). Edit the JSONL by hand. There is no integrity sidecar — the conformance + Rust tests pin
the resulting row count (14) and token total (622,000), so a stray edit fails loudly there.
