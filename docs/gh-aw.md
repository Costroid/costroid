# Read what a gh-aw run cost

GitHub Agentic Workflows (gh-aw) runs Claude, Codex or Copilot in a workflow it compiles itself. Its firewall's API proxy writes one record per model request. `costroid ingest-run --from gh-aw=DIR` turns the records of one run into FOCUS rows, priced from the pinned catalog, and states gh-aw's own figure for the run on a line of its own.

## Read a run

Download the run's `usage` and `info` artifacts, then read the `usage` directory. `RUN_ID` is the gh-aw run's id, as `gh run list --workflow <workflow>` lists it. Given two names, `gh run download` writes each artifact to a directory of that name:

```sh
gh run download "$RUN_ID" --name usage --name info --dir gh-aw
costroid ingest-run --from gh-aw=gh-aw/usage --out costroid-rows.jsonl
```

The first lines it prints, for the run recorded in [`internal/adapter/ghaw/testdata/0.29.0/codex`](../internal/adapter/ghaw/testdata/0.29.0/codex), gh-aw v1.0.1 running Codex 0.164.0 on 2026-11-08:

```text
costroid: gh-aw, 5 requests
costroid: 18 rows in costroid-rows.jsonl, catalog 0.4.1
costroid: USD 0.0370553 at list, Measured
costroid: none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid
costroid: gh-aw stated 3.70553 AI credits for this run. Costroid does not convert credits.
```

`costroid report --preview --rows costroid-rows.jsonl` then prints the comment those rows would get. [`docs/troubleshooting.md`](troubleshooting.md#reading-a-gh-aw-run) lists every line this source prints.

## Post the check on the pull request

[`recipes/github-gh-aw-follow-up.yml`](../recipes/github-gh-aw-follow-up.yml) runs on `workflow_run` after your gh-aw workflow, so no gh-aw file is edited. It downloads the run's `usage` and `info` artifacts into two empty directories under `runner.temp`, reads them with `costroid ingest-run --from gh-aw`, and posts the `costroid` check with `costroid follow-up --ingested`. Copy it to `.github/workflows/costroid-gh-aw.yml` on the default branch, and put your gh-aw workflow's name, the `name:` line of its lock file, in `workflows:`.

`costroid report` posts on the `pull_request` event alone, so the follow-up posts the check. `--ingested` says the rows were written in the follow-up's own job: it then posts for a pull request from a branch of your repository, which has no report job of its own, and reads the run's object beside the rows for the reason and gh-aw's credits. A fork's pull request gets a check that concludes `neutral` at most, as every fork's does. gh-aw runs no workflow on a fork's pull request unless its `forks:` field names the fork (https://github.github.com/gh-aw/reference/fork-support/, read 2026-11-08).

A gh-aw run that a pull request did not start, such as one on an issue or a schedule, gets no check. Its rows still go to a ledger or to Costroid Platform when `COSTROID_INGEST_URL` or `COSTROID_PLATFORM_URL` is set, as [`docs/export.md`](export.md) says.

The check and the comment state gh-aw's credits on a line of their own, under the figures: `gh-aw stated 7.47 AI credits for this run. Costroid does not convert credits.` The result object carries them as `agent_stated`, in [`result/v0.7`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.7). Neither is shown where the comment withholds amounts.

## What is read

gh-aw uploads two artifacts this reads. Its conclusion job uploads `usage`, and its activation job uploads `info`, which holds `aw_info.json` alone (`pkg/workflow/notify_comment.go` and `pkg/workflow/compiler_activation_outputs.go` in `github/gh-aw`, read at v1.0.1, v1.2.0 and v1.2.1 on 2026-11-08). Three files are read, and nothing else:

| File | What it holds |
| --- | --- |
| `agent/token_usage.jsonl` in `usage` | The agent job's records, copied byte for byte from the proxy's `token-usage.jsonl` |
| `detection/token_usage.jsonl` in `usage` | The threat detection job's records. It is empty when that job made no request |
| `aw_info.json` in `info` | The AI-credit cap the workflow was compiled with, as `model_config.max_ai_credits` |

`ingest-run --from gh-aw=DIR` reads the `info` artifact from the directory named `info` beside `DIR`, where `gh run download` puts it. Without that directory the run is read all the same, and its note names no cap. For some workflows, Copilot's among them, gh-aw also puts `aw_info.json` in `usage`, and it is read there too. When the two state different caps, the note names neither, and a warning says so.

The run wrote both artifacts, so they are read as data and nothing in them is run. An artifact that holds a file or a directory gh-aw does not put there, or a link in place of a file, is refused, and no row is written. The `usage` artifact may hold the 25 files the upload step of gh-aw v1.0.1 to v1.2.1 names; v1.2.0 added `evals/evals_status.json` to v1.0.1's 24. A gh-aw release that adds a file is refused the same way until Costroid's list names the file. The refusal names the entry and the releases the list was read from, `gh-aw v1.0.1 to v1.2.1`, and the check states it as the reason `reader_refused`.

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

The grade is `none`. Costroid did not start the run and minted no cap. gh-aw checks a cap of its own, `max-ai-credits`, in AI credits it prices itself. The note names the cap `aw_info.json` states, and the comment's Enforcement cell reads the same: `none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid`.

The cap does not hold a run to its figure. gh-aw hands it to its firewall's API proxy. The proxy adds a response's credits to the run's total when the response comes back, and checks the total before it forwards each request. Once the total is at or past the cap, it refuses the request with HTTP 403 and `ai_credits_limit_exceeded` (`getAiCreditsBlockState` in `containers/api-proxy/guards/ai-credits-guard.js` and `enforceGuards` in `containers/api-proxy/proxy-guards.js` of `github/gh-aw-firewall` v0.29.0, the firewall gh-aw v1.0.1 runs, read 2026-11-08). A request forwarded under the cap completes, so a run can end past it. The recorded Claude run below did: its first request came to 8.87465 AI credits, and its second took the run to 11.80342 against a cap of 10.

When gh-aw's stated credits pass the cap, the note and the cell also state them and by how much, in AI credits, with every digit: `none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid; gh-aw stated 11.80342 AI credits, over the cap by 1.80342 AI credits`. Credits at or under the cap add nothing. Credits are never converted to dollars.

A cap that gh-aw's admission lowers while the run starts is not in `aw_info.json`. Without `aw_info.json` the note reads `none: Costroid did not start this run, so no budget held it`.

## gh-aw's AI credits

gh-aw prices each request in its own unit, AI credits, and each record states the run's total so far as `ai_credits_total`. The run's figure is the last total of the agent job's records added to the last of the threat detection job's, with every digit gh-aw wrote. Costroid states it on its own line and never converts it or adds it to a USD figure. When the threat detection job made requests and states no credits, the run's total is unknown, and no figure is printed.

gh-aw states "One AIC equals $0.01 USD" (https://github.com/github/gh-aw/blob/v1.0.1/docs/src/content/docs/reference/cost-management.md, read 2026-11-08). gh-aw prices from its own table, and Costroid from its catalog. On the two recorded runs the two agree to every digit: 3.70553 AI credits and USD 0.0370553 on the Codex run, 11.80342 AI credits and USD 0.1180342 on the Claude run. That is two runs of two models, so it is a match observed, not a rule.

## What is verified

Two runs are recorded, each with its two record files and its `aw_info.json`, cut to the keys Costroid keeps by `scripts/record`. Each ran gh-aw v1.0.1 behind firewall v0.29.0, with `max-ai-credits: 10`, on 2026-11-08. Their records state `_schema` `token-usage/v0.29.0`, and their `aw_info.json` states firewall `v0.29.0`.

- [`internal/adapter/ghaw/testdata/0.29.0/codex`](../internal/adapter/ghaw/testdata/0.29.0/codex) ran Codex 0.164.0. A test holds its 18 rows to USD 0.0370553, gh-aw's figure to 3.70553 AI credits, and its note to the cap alone.
- [`internal/adapter/ghaw/testdata/0.29.0/claude`](../internal/adapter/ghaw/testdata/0.29.0/claude) ran Claude Code 2.1.310 on `claude-sonnet-5` and ended past its cap. A test holds its 2 requests and 7 rows to USD 0.1180342, gh-aw's figure to 11.80342 AI credits, and its note and Enforcement cell to the 1.80342 AI credits past the cap.

No Copilot run has been recorded. The Copilot reading, and the router, error and threat detection records, follow gh-aw's published schema and its source at the releases named above, and their tests run on artifacts written by hand. The two Codex requests in [`internal/adapter/ghaw/testdata/not-recorded/codex`](../internal/adapter/ghaw/testdata/not-recorded/codex) are those of the recorded Codex 0.167.0 run, written as the proxy states OpenAI's usage, and they price to the USD 0.0350276 that recording states.
