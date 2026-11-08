# Hold an agent to a budget with a LiteLLM gateway

A gateway is the one place a budget stops a request before it is paid. With a [LiteLLM](https://github.com/BerriAI/litellm) proxy you run, Costroid gives each run its own virtual key: a budget equal to the run's budget, an expiry equal to the job's timeout, and the repository, pull request, head commit and run id in its metadata. Claude Code or Codex sends every request through the gateway on that key. The key is deleted when the job ends, also when it fails. The rows read `gateway-precheck`.

## What LiteLLM gives, and under which licence

The key's `max_budget`, its expiry (`duration`), its metadata, its deletion (`POST /key/delete`), team budgets and user budgets sit in LiteLLM's MIT-licensed code, outside its `enterprise/` directory, and none of them checks for an Enterprise licence. Read at [`v1.108.1`](https://github.com/BerriAI/litellm/tree/v1.108.1), commit `93f52b591ee2c91617a8c6975f047f4ad722d7e3`, on 2026-11-06: [`LICENSE`](https://github.com/BerriAI/litellm/blob/v1.108.1/LICENSE), the budget check `virtual_key_max_budget_check` in [`litellm/proxy/auth/auth_checks.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/auth/auth_checks.py), and `/key/generate`, `/key/info` and `/key/delete` in [`key_management_endpoints.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/management_endpoints/key_management_endpoints.py). The [virtual keys page](https://docs.litellm.ai/docs/proxy/virtual_keys) (read 2026-11-06) marks key rotation as Enterprise, and none of the fields above.

Organizations are Enterprise: every route in [`organization_endpoints.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/management_endpoints/organization_endpoints.py) depends on `_enterprise_license_required`.

## How the key reaches the agent

The gateway's admin key is LiteLLM's master key, or a key limited to the routes minting needs ([below](#mint-with-a-key-limited-to-its-routes)). It never enters the job that runs the agent. Only the job that mints the key and the job that deletes it hold it.

A job cannot hand a secret to another job through its outputs: GitHub refuses to pass an output that holds a masked value, and an output that is not masked is printed in the next job's log. So the per-run key never crosses a job at all. The agent's job makes it.

| Step | Job               | What happens                                                                                                                                                                                                                                                                               |
| ---- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1    | Agent             | `costroid gateway request` draws a random key and writes it to a file only the runner's user can read. It seals the key to the gateway's sealing key with [HPKE](https://www.rfc-editor.org/rfc/rfc9180) (X25519, HKDF-SHA256, AES-256-GCM), bound to the repository, the run and the attempt |
| 2    | Agent             | `actions/upload-artifact` keeps the sealed request as an artifact of this run. It holds no secret: only the minting job can open it                                                                                                                                                       |
| 3    | Mint, in parallel | `costroid gateway mint` waits for that artifact, opens it with the sealing key's private half, checks that it names this repository, run and attempt, and registers the key on the gateway with its budget, expiry and metadata. With a ledger named, it then attests the key's id for this run to the ledger |
| 4    | Agent             | `costroid run --gateway` waits until the key authenticates, reads the key back from the gateway, and starts the agent with the gateway's URL and the key in the agent's environment alone                                                                                                 |
| 5    | Close, always     | `costroid gateway close` runs after mint and the agent's job, and deletes the key by its alias, which names the run and the attempt. With a ledger named, it first reads what the gateway counted on the key, and after the delete attests that figure to the ledger |

The sealing key pair is drawn at random, once, on your own machine:

```sh
costroid gateway keygen --out sealing-key
```

It writes the private half to `sealing-key`, readable by you alone, and prints the public half. Store the file's content as the secret `COSTROID_GATEWAY_SEALING_KEY` of the `costroid-gateway` environment, store the public half as the repository variable `COSTROID_GATEWAY_PUBLIC_KEY`, then delete the file. Store the gateway's host, with its port when the URL names one, as the repository variable `COSTROID_GATEWAY_HOST`: the agent's job reaches only the hosts it allows, and a workflow expression cannot take the host out of `COSTROID_GATEWAY_URL` ([`docs/recipes.md`](recipes.md#egress-in-the-agents-job)). `costroid gateway public-key` prints the public half of the private half in `COSTROID_GATEWAY_SEALING_KEY`, to check the variable against it.

Versions up to 1.0.0-alpha.8 derived the pair from the admin key, so anyone who read the public key, a repository variable, could test guesses at the admin key offline. `costroid gateway mint` now refuses a request sealed to a key derived that way, and a request from an earlier version, with a sentence that says to run `keygen`. If your public key was derived, run `keygen`, replace the variable, and rotate the admin key.

No value of the key reaches an output or a log. The agent's job keeps `contents: read` and nothing else: it uploads an artifact through the runner's own token, and it learns that the key is ready by asking the gateway, never the GitHub API. The minting job needs `actions: read` to download the artifact and `id-token: write` to attest the key, and checks out nothing. The closing job needs `id-token: write` to attest what the gateway counted, and nothing else.

An injected agent holds its own key, which is what it needs to run. It can spend that key's budget and no other. It cannot open another run's request, because every request is sealed to a key the agent's job never holds. A request copied from another run is refused, because the repository, run and attempt it names are checked.

The gateway's URL must be `https`. Every call sends the admin key or the run's key in its `Authorization` header, so `costroid gateway` and `costroid run` refuse an `http` URL, except on `localhost` and `127.0.0.1`.

## How each agent reaches the gateway

`costroid run --gateway` points the agent at the gateway with the agent's own settings, and takes away any provider key the job holds beside the run's key. An agent with no way to name a gateway is refused before the run asks for its key.

| Agent       | What `costroid run` sets                                                                                                                                                         | What it removes     |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| Claude Code | `ANTHROPIC_BASE_URL` to the gateway's URL, and `ANTHROPIC_AUTH_TOKEN` to the run's key, in its environment and in a [settings file of its own](#claude-codes-settings-file), which the run names with `--settings` at the end of the command's options | `ANTHROPIC_API_KEY` |
| Codex       | `CODEX_API_KEY` to the run's key, and two arguments at the end of the command's options: `--config openai_base_url="<gateway>/v1"` and `--config model_provider="openai"` | `OPENAI_API_KEY`    |

Codex reads its base URL from configuration alone. It stopped reading the `OPENAI_BASE_URL` environment variable in [openai/codex#16720](https://github.com/openai/codex/pull/16720) (2026-04-03), which `rust-v0.164.0-alpha.11.1` holds. Its [advanced configuration page](https://learn.chatgpt.com/docs/config-file/config-advanced) says: "To change the base URL for the built-in OpenAI provider, use `openai_base_url`". Its [basic configuration page](https://learn.chatgpt.com/docs/config-file/config-basic) puts "CLI flags and `--config` overrides" above every configuration file. A later `--config` overrides an earlier one: `build_cli_overrides_layer` in [`codex-rs/config/src/overrides.rs`](https://github.com/openai/codex/blob/rust-v0.164.0-alpha.11.1/codex-rs/config/src/overrides.rs) applies them in order. So the run's two arguments come after every option the command holds, and `model_provider` keeps a provider set in `~/.codex/config.toml` or a profile from sending requests around the gateway. A command that sets either key itself is refused ([below](#commands-a-gateway-run-refuses)). Both pages were read on 2026-11-07.

`codex exec` takes `CODEX_API_KEY` before any other sign-in, so Codex runs on the run's key.

### Claude Code's settings file

A repository's `.claude/settings.json` can set `ANTHROPIC_BASE_URL` in its `env`, and a pull request can change that file. Claude Code applies a project file's `env` over the environment it starts with. So the run does not rely on the environment alone. It writes a settings file for Claude Code and passes it with `--settings`, after every option the command holds. The file's `env` sets `ANTHROPIC_BASE_URL` to the gateway's URL and `ANTHROPIC_AUTH_TOKEN` to the run's key. It sets `ANTHROPIC_API_KEY`, the five provider switches and their endpoints to the empty string, which Claude Code reads as unset.

Claude Code's [settings page](https://code.claude.com/docs/en/settings#settings-precedence) ranks the command line above project local, shared project and user settings: "JSON you pass with `--settings <file-or-json>` when you start `claude`, for that session only ... A key you set there overrides the same key in your project and user settings files". It adds: "An `env` block inside a settings file is an ordinary key and follows the levels above." Its [settings reference](https://code.claude.com/docs/en/settings-reference#how-env-values-interact-with-your-shell) says of `env`: "A value here overwrites the same variable exported in your shell, and when more than one settings file sets a variable, the highest-precedence one applies", and "To cancel a shell export, set the variable to `""`. Claude Code treats an empty value as unset for provider selection". It applies project and local `env` "at startup in `-p` mode, which never shows the trust dialog". Both pages were read on 2026-11-08.

Neither page says what Claude Code does with two `--settings`. On 2026-11-08, Claude Code 2.1.319 ran on a machine with no route out, against local listeners. Given two, it applied the last one alone: an earlier one's base URL, `model` and headers did not reach any request. So the run's file carries the command's own last `--settings`, every key as written, with the gateway's `env` set over it. In the same runs, a project `.claude/settings.json` that named another base URL, another token, an API key or `CLAUDE_CODE_USE_BEDROCK` received no request once the run's file was passed.

Each run writes a new file in the temporary directory, which only the run's user can read (mode `0600`), because it holds the key. The run removes it when the agent has ended, also when the agent failed to start, and warns when it cannot. A run that is killed leaves the file behind, and the key in it stops working when the closing job deletes the key or at the key's expiry. A command whose own `--settings` names a routing variable is still [refused](#commands-a-gateway-run-refuses).

The rows of both agents read `measured`. The gateway bills every request per token on its own provider key, whatever key the job holds. So neither the `apiKeySource` Claude Code states nor a `CODEX_API_KEY` in Costroid's own environment changes the basis.

The gateway needs each Codex model on its OpenAI route. Codex calls the Responses API, `/v1/responses`. On 2026-11-07, LiteLLM v1.108.1 served it, streamed and not, to an upstream mock, and charged each request USD 0.0045 for 10,000 input and 10,000 output tokens of `gpt-5-nano` at the catalog's prices. On a key with a USD 0.01 budget it admitted three requests and refused the fourth with HTTP 422, "Budget has been exceeded! ... Current cost: 0.013500000000000003, Max budget: 0.01". The refused request never reached the upstream.

## Commands a gateway run refuses

A run reads `gateway-precheck` only when every request it makes passes through the gateway on the run's key. Under `--gateway`, `costroid run` refuses a command that holds a flag that sends requests anywhere else. It refuses before it asks the gateway anything, and starts no agent:

```text
costroid: run: <flag> sends <agent>'s requests around the gateway, and a gateway cannot hold a run that routes around it; remove it
```

| Agent       | Refused                                                                                                  | Why                                                                                                                                                                                                                                                         |
| ----------- | -------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Codex       | `--oss`, `--local-provider`                                                                              | `--oss` selects a local provider, LM Studio or Ollama, and Codex sets it above every configuration layer, the run's `--config model_provider` included. `--local-provider` names that provider                                                             |
| Codex       | `-c` or `--config` for `model_provider`, `openai_base_url`, or any key under `model_providers`           | These keys choose the provider or its base URL. The run sets the first two itself                                                                                                                                                                         |
| Claude Code | `--cloud`, `--remote`, `--environment`                                                                   | Each starts the session on another machine, which holds none of the run's environment                                                                                                                                                                      |
| Claude Code | `--settings` whose `env` names `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, a provider or its endpoint | A settings file's `env` overwrites the variable the run sets. The providers are `CLAUDE_CODE_USE_BEDROCK`, `_MANTLE`, `_VERTEX`, `_FOUNDRY` and `_ANTHROPIC_AWS`, and their endpoints `ANTHROPIC_BEDROCK_BASE_URL`, `ANTHROPIC_BEDROCK_MANTLE_BASE_URL`, `ANTHROPIC_VERTEX_BASE_URL`, `ANTHROPIC_FOUNDRY_BASE_URL`, `ANTHROPIC_FOUNDRY_RESOURCE` and `ANTHROPIC_AWS_BASE_URL` |

Codex at `rust-v0.164.0-alpha.11.1` declares `--oss` as "Use open-source provider" and `--local-provider` as "Specify which local provider to use (lmstudio or ollama)" in [`codex-rs/utils/cli/src/shared_options.rs`](https://github.com/openai/codex/blob/rust-v0.164.0-alpha.11.1/codex-rs/utils/cli/src/shared_options.rs). Under `--oss`, `codex exec` passes the local provider as an override ([`codex-rs/exec/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.164.0-alpha.11.1/codex-rs/exec/src/lib.rs)), and the provider is chosen in this order: a provider the machine's requirements name, then that override, then `model_provider` from the merged configuration, `--config` included ([`codex-rs/core/src/config/mod.rs`](https://github.com/openai/codex/blob/rust-v0.164.0-alpha.11.1/codex-rs/core/src/config/mod.rs)). Codex splits a `--config` key on every dot, so `model_providers.<id>.base_url` is a key under `model_providers` ([`codex-rs/config/src/overrides.rs`](https://github.com/openai/codex/blob/rust-v0.164.0-alpha.11.1/codex-rs/config/src/overrides.rs)). Codex itself refuses `--remote` on `codex exec`. Read on 2026-11-08.

Claude Code's [CLI reference](https://code.claude.com/docs/en/cli-reference) says `--settings` takes a "Path to a settings JSON file or an inline JSON string", `--cloud` creates "a new cloud session", `--remote` is a "Deprecated alias for `--cloud`", and `--environment` creates "a new cloud session that runs on the self-hosted environment". Its [settings reference](https://code.claude.com/docs/en/settings-reference#how-env-values-interact-with-your-shell) says of `env`: "A value here overwrites the same variable exported in your shell". The variables are listed in its [environment variables reference](https://code.claude.com/docs/en/env-vars). All three pages were read on 2026-11-08. The run reads each `--settings` value: a file it cannot read, or a value that is not a JSON object, stops the run too.

A repository's `.claude/settings.json` can set the same variables, and a pull request can change it. The run does not refuse it: its own [settings file](#claude-codes-settings-file) outranks it.

## When mint is late

The mint job does not wait for the agent's job. The close job waits for both, so a key that mint registers late is still deleted, with no wait. A key minted after close, such as one whose minting job a reviewer approved after close ended, lives until its expiry, `--lifetime-minutes`. No runner holds it: the agent's job has ended, and only the sealing key opens its request. A workflow that runs close beside mint can pass `--retry-after` to wait for a late mint and delete once more.

## How the rows earn `exact`

`SubAccountId` is what the rows state, and a row could state any key, a key every run shares among them. So the ledger grades a row `exact` only when the minting job attested its key for the run.

When `COSTROID_INGEST_URL` is set, `costroid gateway mint` posts the key's id, the repository, the run and the attempt to the ledger's `/v1/credentials`, beside the ingest URL's `/v1/rows`. It sends them under the minting job's OIDC token, for `COSTROID_INGEST_AUDIENCE`, as the ingest job does. The ledger takes the attestation only when the token's `environment` claim names the gateway's environment, `costroid-gateway` by default, and the token's repository, run and attempt are the ones attested. A row reads `exact` only when its key was attested and the row's own token names that repository, run and attempt. [`docs/serve.md`](serve.md) has the rest.

## How a budget passes on the gateway's count

The rows' token counts come from the agent's job, so the ledger lets no budget pass on them alone. The gateway counts what passed through it, so its figure can stand behind the rows.

When `COSTROID_INGEST_URL` is set, `costroid gateway close` reads the run's key by its alias from `GET /key/list` with `return_full_object=true`, before it deletes the key. It reads the key's id and its `spend`. LiteLLM writes spend to its store in batches, so close reads the figure again every 2 seconds until it has not moved for 12 seconds, for at most 30 seconds. After the delete, it posts the key's id, the repository, the run, the attempt and the figure in USD to the ledger's `/v1/credentials/{key}/spend`, under the closing job's OIDC token, for `COSTROID_INGEST_AUDIENCE`. A figure with more than 12 fractional digits is rounded up to 12. The recipe gives the closing job `id-token: write` and both variables, as it gives the minting job, and `costroid lint-recipes` allows `id-token: write` on a job that runs close or mint only when the job names an environment. [`docs/serve.md`](serve.md#the-gateways-count) says what the ledger checks.

Close deletes the key even when it cannot read the figure, and then fails the step: the run's budget cannot pass on the gateway's count. A ledger that refuses the figure fails the step too. Without `COSTROID_INGEST_URL`, close reads nothing and attests nothing.

`GET /key/list` sits in the same MIT-licensed `key_management_endpoints.py` as the routes above and checks for no Enterprise licence. Read on 2026-11-07 in the `ghcr.io/berriai/litellm:v1.108.1` image the e2e journey pins.

Two runs of the e2e journey `TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob` were logged in full on 2026-11-07, against LiteLLM v1.108.1. In both, the gateway refused a request with "Current cost: 0.012", and close attested USD 0.012, the rows' total. In one of the two, the admin's `GET /key/info` for the key, read after the run and before close, answered a `spend` of `0`. No reason was traced. The wait is there for that.

The environment is what makes an attestation worth trusting. Any job that references `costroid-gateway` can attest, and on a pull request from a branch of this repository the workflow file is the pull request's own. The protection rules you set on the environment decide which runs reach it. With required reviewers, a person you named approves each run before it can mint or attest. Without a rule, anyone who can push a branch can attest a key for their own run.

## What the rows say

| Column or tag                    | Value                                                                                                                         |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `SubAccountId`                   | The key's id: the SHA-256 of the key in hex, the value LiteLLM stores and its spend logs carry. Never the key                 |
| `costroid/enforcement_grade`     | `gateway-precheck`, or `none` with the reason in `ChargeDescription`                                                          |
| `x_CostroidOvershootBound`       | The most a run can pass its budget by, in USD: the key's parallel-request limit plus one, times the largest request, times the highest unit price the model's sheet holds |

The gateway checks a key's spend before each request. A request that starts under the budget runs to its end, and so does every request in flight beside it. A request that has ended and that the gateway has not counted yet is still concurrent for that check. The bound covers them all: `max_parallel_requests` on the key plus one, times `max_request_tokens` from the key's metadata, times the highest unit price of any billed class for that model at that context length. A key without a parallel-request limit or a request-size limit has no bound, and its rows read `none`.

A run reads `gateway-precheck` only when the key the gateway returns has a budget at or under the run's, an expiry, a parallel-request limit, a request size, and metadata naming the run the job belongs to. Otherwise the rows read `none` and say which fact was missing.

The plus one comes from how LiteLLM counts. LiteLLM counts a request's cost after it answers, so a request sent at once after the one before it can be admitted before that one is counted. On 2026-11-06, LiteLLM v1.108.1 took requests back to back on a key with a USD 0.01 budget and one request at a time, each USD 0.006 in its prices and the catalog's (its `x-litellm-response-cost` header read `0.006`). In 4 of 6 runs it admitted a third request after two had passed USD 0.01. Its refusal of the fourth read "Current cost: 0.013951" while the third was still being counted, and later refusals read "0.018000000000000002". Those runs came to USD 0.018, over the budget by USD 0.008. In none of the six was a request admitted while two that had ended were still uncounted, so the bound counts one: the plus one above. The journey's bound is 2 × 20000 tokens × USD 0.50 per million tokens, USD 0.02, for Claude Code, and 2 × 20000 tokens × USD 0.40 per million tokens, USD 0.016, for Codex on `gpt-5-nano`.

The plus one rests on that observation of LiteLLM v1.108.1, not on anything LiteLLM promises. So each run checks itself. When the rows, priced at the catalog, pass the key's budget by more than the bound they state, the run warns with both decimal strings, and its rows read `none` with the reason in `ChargeDescription`:

```text
costroid: warning: the rows, priced at the catalog, come to USD <rows' total> against the key's budget of USD <budget>: an overshoot of USD <overshoot>, above the bound of USD <bound>, so the rows read none
```

## The gateway holds the budget in its own prices

LiteLLM checks `max_budget` against its own count of the key's spend, priced by its own model prices. The rows are priced by the Costroid catalog. When the gateway prices a model lower than the catalog, a key lets through more catalog-priced spend than the run's budget.

When the agent ends, `costroid run` reads the key's spend from `/key/info`. It waits up to 30 seconds for the figure to settle, because LiteLLM writes spend to its store every 10 seconds by default (`PROXY_BATCH_WRITE_AT` in [`litellm/constants.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/constants.py)). When the gateway's figure differs from the rows' total, the run warns and names both decimal strings:

```text
costroid: warning: the gateway counted USD <gateway's figure> on the run's key, and the rows, priced at the catalog, come to USD <rows' total>. The gateway holds the budget in its own figure, and the rows keep theirs
```

LiteLLM sums spend in binary floating point, and its counter read `0.018000000000000002` for three requests of `0.006`. A float64 sum of n terms drifts by about n × 1.1 × 10⁻¹⁶ of the total. So the run reads the gateway's figure exactly, exponent forms such as `1.2e-05` included, and takes it as the rows' total when the two differ by at most 10⁻⁹ of the total. Past that, it warns and prints the gateway's figure as the decimal it read. The gateway's figure is never written to a row.

To make the two agree, give each model on the gateway the catalog's prices. A LiteLLM price is per token, and a catalog price is per million tokens, so divide by 1,000,000. For `claude-haiku-5-5` in catalog `0.1.0`:

| Catalog class, up to 100,000 input tokens | Catalog price, USD per million tokens | LiteLLM field in `litellm_params`         |
| ----------------------------------------- | ------------------------------------- | ----------------------------------------- |
| `input_uncached`                          | 0.10                                  | `input_cost_per_token: 0.0000001`         |
| `cache_read`                              | 0.01                                  | `cache_read_input_token_cost: 0.00000001` |
| `cache_write_5m`                          | 0.125                                 | `cache_creation_input_token_cost: 0.000000125` |
| `cache_write_1h`                          | 0.20                                  | `cache_creation_input_token_cost_above_1hr: 0.0000002` |
| `output`                                  | 0.50                                  | `output_cost_per_token: 0.0000005`        |

Above 100,000 input tokens the catalog's `over-100000` rows apply, and LiteLLM's fields end in `_above_100k_tokens`. LiteLLM's own price map at v1.108.1 holds the same five prices for `claude-haiku-5-5` ([`model_prices_and_context_window.json`](https://github.com/BerriAI/litellm/blob/v1.108.1/model_prices_and_context_window.json)), so a gateway on that release agrees for this model without a setting.

When the prices cannot match, lower the key's budget by the ratio. If the gateway prices a model at a fraction `r` below 1 of the catalog, mint with `--budget-usd` set to the run's budget times `r`, so the gateway refuses at the run's budget in catalog prices. For a model priced at 0.8 of the catalog and a USD 5 run, mint with USD 4.

The journey once showed USD 0.013951, and that figure was not a price. The mock model is priced in [`e2e/testdata/litellm.yaml`](../e2e/testdata/litellm.yaml) at the catalog's prices, and LiteLLM charged each request USD 0.006, USD 0.001 input and USD 0.005 output by its `x-litellm-response-cost-input` and `-output` headers. USD 0.013951 was the key's counter read while the third request's cost was still being settled. LiteLLM reserves an estimate of a request's cost before it runs and settles it to the real cost after (`reserve_budget_for_request` and `reconcile_budget_reservation` in [`budget_reservation.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/spend_tracking/budget_reservation.py)). How the counter came to that exact figure was not traced.

## When the gateway's store fails

LiteLLM keeps spend in a database. With `allow_requests_on_db_unavailable: true` it keeps serving while the database is down: in the e2e journey, with its database stopped, LiteLLM v1.108.1 answered the next request with HTTP 200. `costroid run` asks the gateway's public `/health/readiness` before the agent starts, every two seconds while it runs, and after it ends. If any answer names a database that is not `connected`, or the gateway does not answer, the run's rows read `none`. An outage that starts and ends between two probes is not seen.

## Keep the admin key and the sealing key behind an environment

On a pull request from a branch of the same repository, GitHub runs the workflow file from the pull request's head, with the repository's secrets. Anyone who can push a branch can change the mint job and read the admin key. That is GitHub's model for every secret a workflow can reach, not something particular to Costroid. A pull request from a fork gets no secrets: "secrets are not passed to the runner when a workflow is triggered from a forked repository" ([Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets), read 2026-11-06).

Keep the admin key and the sealing key as secrets of a GitHub environment, `costroid-gateway`, and set its protection rules yourself. The recipe names that environment on the mint and close jobs, and on no other job. `costroid lint-recipes` refuses a job that sets `COSTROID_GATEWAY_ADMIN_KEY` or `COSTROID_GATEWAY_SEALING_KEY` and names no environment, and refuses either on the agent's job. "Secrets stored in an environment are only available to workflow jobs that reference the environment", and with required reviewers "a job cannot access environment secrets until one of the required reviewers approves it" ([Deployments and environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments), read 2026-11-06). With required reviewers, each run waits for a person you named before a key is minted. Without a protection rule, the environment only limits which jobs see the key.

## Mint with a key limited to its routes

The master key can do everything on the proxy. Mint and close call nine routes: `/key/generate`, `/key/list`, `/key/delete`, `/team/info`, `/team/new`, `/team/update`, `/user/info`, `/user/new` and `/user/update`. Close calls `/key/list` only when a ledger is named. A LiteLLM key can be limited to them with `allowed_routes`, a field of its MIT-licensed code. The key's user is a proxy admin, so the key may create teams and set users' budgets; whether a lesser role suffices was not tested. Create the user and the key with the master key, once:

```sh
curl -sS -X POST "$LITELLM_URL/user/new" -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H 'Content-Type: application/json' -d '{"user_id": "costroid-minter", "user_role": "proxy_admin"}'
curl -sS -X POST "$LITELLM_URL/key/generate" -H "Authorization: Bearer $LITELLM_MASTER_KEY" \
  -H 'Content-Type: application/json' -d '{"user_id": "costroid-minter", "allowed_routes": ["/key/generate", "/key/list", "/key/delete", "/team/info", "/team/new", "/team/update", "/user/info", "/user/new", "/user/update"]}'
```

Store the `key` it answers as the environment's admin key in place of the master key.

Verified on 2026-11-07 against LiteLLM v1.108.1 by the e2e journey `TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob`: both of its keys were minted and closed with such a key, the first with a ledger named, and the key was refused with HTTP 403 on `GET /team/list`, `POST /model/new`, `POST /config/update` and `POST /v1/messages`.

`/key/list` is a proxy admin's route, so the limited key can list every key on the proxy, with each key's id, alias, metadata and spend. Leave it out of `allowed_routes` when no ledger is named.

The limit is not a boundary. The same journey shows that the limited key can call `/key/generate` for its own user, a proxy admin, with no `allowed_routes`, and the key it makes reaches `/team/list`. Whoever holds the limited key can therefore make a key with its user's proxy-admin role and no limit. The limit refuses any other route called with the key itself, and nothing more. The environment's protection rules are what keep the key.

## Daily caps

| Cap              | Carried as                                                                                                         |
| ---------------- | ------------------------------------------------------------------------------------------------------------------ |
| Per repository   | A LiteLLM team per repository, with `max_budget` and `budget_duration: 1d`. Each run's key belongs to it            |
| Per author       | A LiteLLM user per author, with `max_budget` and `budget_duration: 1d`. Each run's key belongs to it. The proxy needs `general_settings.apply_user_budget_to_team_keys: true`, or LiteLLM skips a user's budget on a team's key |
| Per pull request | Not supported. A LiteLLM key belongs to one team and one user, and both are taken                                  |
| Per organization | Not supported. LiteLLM organizations need an Enterprise licence                                                    |

`costroid gateway mint --repository-daily-cap-usd N --author-daily-cap-usd N` creates the team and the user when they are missing and sets their budgets. A cap left out sets nothing. The caps come from the workflow file, and a pull request can change its own workflow, as it can change `budget-usd`.

## claude-code-action

No recording shows `claude-code-action` sending its requests to a gateway's base URL. Until one does, the recipe runs `claude -p` under `costroid run`, which sets the base URL itself.
