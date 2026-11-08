# Read what a gh-aw run cost

GitHub Agentic Workflows (gh-aw) runs Claude, Codex or Copilot in a workflow it compiles itself. Its firewall's API proxy writes one record per model request. `costroid ingest-run --from gh-aw=DIR` turns the records of one run into FOCUS rows, priced from the pinned catalog, and states gh-aw's own figure for the run on a line of its own.

## Read a run

Download the run's `usage` artifact into an empty directory, then read the directory. `RUN_ID` is the gh-aw run's id, as `gh run list --workflow <workflow>` lists it:

```sh
gh run download "$RUN_ID" --name usage --dir gh-aw-usage
costroid ingest-run --from gh-aw=gh-aw-usage --out costroid-rows.jsonl
```

The first lines it prints, for the artifact in [`internal/adapter/ghaw/testdata/not-recorded/claude`](../internal/adapter/ghaw/testdata/not-recorded/claude), which was written by hand:

```text
costroid: gh-aw, 5 requests
costroid: 15 rows in costroid-rows.jsonl, catalog 0.1.0
costroid: USD 0.04974 at list, Measured
costroid: none: gh-aw's proxy held this run to 10 AI credits, priced by gh-aw
costroid: gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits.
```

`costroid report --preview --rows costroid-rows.jsonl` then prints the comment those rows would get. [`docs/troubleshooting.md`](troubleshooting.md#reading-a-gh-aw-run) lists every line this source prints.

## Post the check on the pull request

[`recipes/github-gh-aw-follow-up.yml`](../recipes/github-gh-aw-follow-up.yml) runs on `workflow_run` after your gh-aw workflow, so no gh-aw file is edited. It downloads the run's `usage` artifact into an empty directory under `runner.temp`, reads it with `costroid ingest-run --from gh-aw`, and posts the `costroid` check with `costroid follow-up --ingested`. Copy it to `.github/workflows/costroid-gh-aw.yml` on the default branch, and put your gh-aw workflow's name, the `name:` line of its lock file, in `workflows:`.

`costroid report` posts on the `pull_request` event alone, so the follow-up posts the check. `--ingested` says the rows were written in the follow-up's own job: it then posts for a pull request from a branch of your repository, which has no report job of its own, and reads the run's object beside the rows for the reason and gh-aw's credits. A fork's pull request gets a check that concludes `neutral` at most, as every fork's does. gh-aw runs no workflow on a fork's pull request unless its `forks:` field names the fork (https://github.github.com/gh-aw/reference/fork-support/, read 2026-11-08).

A gh-aw run that a pull request did not start, such as one on an issue or a schedule, gets no check. Its rows still go to a ledger or to Costroid Platform when `COSTROID_INGEST_URL` or `COSTROID_PLATFORM_URL` is set, as [`docs/export.md`](export.md) says.

The check and the comment state gh-aw's credits on a line of their own, under the figures: `gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits.` The result object carries them as `agent_stated`, in [`result/v0.7`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.7). Neither is shown where the comment withholds amounts.

## What is read

The `usage` artifact as gh-aw v1.2.1's conclusion job uploads it (`actions/setup/sh/collect_usage_artifact_files.sh` in `github/gh-aw`, read 2026-11-08). Three of its files are read, and nothing else:

| File | What it holds |
| --- | --- |
| `agent/token_usage.jsonl` | The agent job's records, copied byte for byte from the proxy's `token-usage.jsonl` |
| `detection/token_usage.jsonl` | The threat detection job's records. It is empty when that job made no request |
| `aw_info.json` | The AI-credit cap the workflow was compiled with, as `model_config.max_ai_credits` |

The run wrote the artifact, so it is read as data and nothing in it is run. An artifact that holds a file or a directory gh-aw does not put there, or a link in place of a file, is refused, and no row is written. A gh-aw release that adds a file to the artifact is refused the same way until Costroid's list names the file. The refusal names the entry and the gh-aw release the list was read from, v1.2.1, and the check states it as the reason `reader_refused`.

## How a record becomes rows

Each record follows the token-usage schema that gh-aw's firewall publishes with each release (https://github.com/github/gh-aw-firewall/releases/download/v0.29.8/token-usage.schema.json, read 2026-11-07). Each record is one request, and each token class it holds is one row.

| Record field | Anthropic | OpenAI |
| --- | --- | --- |
| `input_tokens` | `input_uncached` | `input_uncached`, once the cached tokens and the cache writes it holds are taken out |
| `cache_read_tokens` | `cache_read` | `cache_read`, and `cached_input_reported` |
| `cache_write_tokens` | `cache_write_5m`, lifetime assumed | `cache_write_30m`, lifetime assumed |
| `output_tokens` | `output` | `output` |
| `reasoning_tokens` | `reasoning_output`, informational | `reasoning_output`, informational |

The proxy keeps OpenAI's own `input_tokens`, which holds the cached tokens (`normalizeUsage` in `containers/api-proxy/token-parsers.js` of firewall v0.29.8). Anthropic counts its input apart from the cache. A cache write's lifetime is not in the record, so it takes the provider's default: five minutes on Anthropic, 30 minutes on OpenAI. The row carries `costroid/assumed_ttl`.

A request ends at the record's `timestamp` and starts `duration_ms` before it. The record states no service tier and no region, so each request is priced at the standard tier at the global endpoint.

Some records are not the agent's own requests:

- A record with `purpose: routing_classification` is a request gh-aw's router made to choose a model. Its rows say so.
- A record in `detection/token_usage.jsonl` is a request of the threat detection job. Its rows say so.
- A record with a 4xx or 5xx status and no tokens writes no row. One that carried tokens is kept, because the provider counted them.
- A record from `gemini`, `opencode` or `unknown` writes no row, because the catalog holds no price for those providers. A warning counts them.

A request Copilot served (`provider: copilot`) is priced at the list rate of the model's own provider, read from the model's name: `claude-` is Anthropic, and `gpt-`, `o1`, `o3` and `o4` are OpenAI. Any other model writes no row, with a warning. The proxy states Copilot's input with the cached tokens when Copilot's response lumps them in, and without them when it splits them out, and the record does not say which. Input large enough to hold the cached tokens and writes is read as holding them. No Copilot run has been recorded, so this reading is unverified, and each Copilot row's description says so.

## Basis

gh-aw runs Claude on `ANTHROPIC_API_KEY` and Codex on `OPENAI_API_KEY` or `CODEX_API_KEY`. It does not take a Claude subscription's token: "`CLAUDE_CODE_OAUTH_TOKEN` is not supported by GitHub Agentic Workflows" (https://github.github.com/gh-aw/reference/auth/, read 2026-11-08). So their rows read `measured`. Copilot bills by subscription, so its rows read `list_equivalent`. A run with both reads `list_equivalent`, the weaker claim, with a warning. [`docs/bases.md`](bases.md) says what each basis promises.

## Grade

The grade is `none`. Costroid did not start the run and minted no cap. gh-aw's proxy holds a run to its own cap of AI credits, priced by gh-aw, and the note names the cap `aw_info.json` states. A cap that gh-aw's admission lowers while the run starts is not in `aw_info.json`. Without `aw_info.json` the note reads `none: Costroid did not start this run, so no budget held it`.

## gh-aw's AI credits

gh-aw prices each request in its own unit, AI credits, and each record states the run's total so far as `ai_credits_total`. The run's figure is the last total of the agent job's records added to the last of the threat detection job's, with every digit gh-aw wrote. Costroid states it on its own line and never converts it or adds it to a USD figure. When the threat detection job made requests and states no credits, the run's total is unknown, and no figure is printed.

## What is verified

No gh-aw run has been recorded yet. The reading follows gh-aw's published schema and its source at the releases named above, and the tests run on artifacts written by hand. The two Codex requests in [`internal/adapter/ghaw/testdata/not-recorded/codex`](../internal/adapter/ghaw/testdata/not-recorded/codex) are those of the recorded Codex 0.167.0 run, written as the proxy states OpenAI's usage, and they price to the USD 0.0350276 that recording states.
