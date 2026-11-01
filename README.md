# Costroid

Costroid runs a coding agent under a budget and records what the run cost, per token class, as FOCUS rows.

## Why it exists

An agent's own cost field is an estimate, and a budget flag can be weaker than it sounds. Costroid prices the agent's token counts against a pinned, dated catalog, and it writes down how the budget was enforced. A grade is never shown as stronger than it is.

This is the first slice. It covers `costroid run` for Claude Code and Codex. The pull request check comes later.

## Install

```sh
go install github.com/Costroid/costroid/cmd/costroid@latest
```

Or build the static binary from a checkout, with Go 1.27 or later:

```sh
make build
```

## First command

```sh
costroid run --budget-usd 5 --out rows.jsonl -- claude -p "Fix the failing test."
```

The agent's stream passes through to stdout. Costroid prints this to stderr, here for a recorded run with one subagent on a Claude subscription:

```text
costroid: claude-code, 4 requests
costroid: 19 rows in rows.jsonl, catalog 0.1.0
costroid: USD 0.001619725 at list, List-equivalent, not billed
costroid: budget USD 5, agent-flag: client-side estimate, can pass the cap
```

`rows.jsonl` holds one FOCUS row per request per token class:

```json
{"ChargeDescription":"claude-haiku-5-5 input_uncached tokens; agent-flag: client-side estimate, can pass the cap","ConsumedQuantity":"2","ListCost":"0.0000002","ListUnitPrice":"0.1","SkuMeter":"input_uncached","SkuPriceId":"anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached"}
```

That row is shortened. A full row has every column of the [row contract](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.1), and every amount is a decimal string.

## What the budget means

| Agent       | Grade              | What holds the run to the budget                                                                                                                            |
| ----------- | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Claude Code | `agent-flag`       | Costroid passes `--max-budget-usd`. Claude Code checks its own estimate between requests, so the request that crosses the cap still runs and is still paid. |
| Codex       | `wrapper-watchdog` | Costroid prices `codex exec --json` and stops the process at the cap. Usage arrives at turn end, so overshoot is bounded by one turn.                       |

One `codex exec` prompt is one turn, however many model requests it makes. A one-prompt run is priced when it ends, so the watchdog cannot stop it partway. [`docs/adapters.md`](docs/adapters.md) has the recorded runs behind both rows.

## Exit codes

| Code            | Meaning                                            |
| --------------- | -------------------------------------------------- |
| The agent's own | The run finished, or the agent stopped itself      |
| 124             | Costroid stopped the agent at the cap              |
| 125             | Costroid could not run the agent or record the run |

## Check

```sh
make check
```

It runs `gofmt -l`, `go vet`, `staticcheck` and `go test ./...`.

## Licence

Apache-2.0.
