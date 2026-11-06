# Hold Claude Code to a budget with a LiteLLM gateway

A gateway is the one place a budget stops a request before it is paid. With a [LiteLLM](https://github.com/BerriAI/litellm) proxy you run, Costroid gives each run its own virtual key: a budget equal to the run's budget, an expiry equal to the job's timeout, and the repository, pull request, head commit and run id in its metadata. Claude Code sends every request through the gateway on that key. The key is deleted when the job ends, also when it fails. The rows read `gateway-precheck`.

## What LiteLLM gives, and under which licence

The key's `max_budget`, its expiry (`duration`), its metadata, its deletion (`POST /key/delete`), team budgets and user budgets sit in LiteLLM's MIT-licensed code, outside its `enterprise/` directory, and none of them checks for an Enterprise licence. Read at [`v1.108.1`](https://github.com/BerriAI/litellm/tree/v1.108.1), commit `93f52b591ee2c91617a8c6975f047f4ad722d7e3`, on 2026-11-06: [`LICENSE`](https://github.com/BerriAI/litellm/blob/v1.108.1/LICENSE), the budget check `virtual_key_max_budget_check` in [`litellm/proxy/auth/auth_checks.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/auth/auth_checks.py), and `/key/generate`, `/key/info` and `/key/delete` in [`key_management_endpoints.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/management_endpoints/key_management_endpoints.py). The [virtual keys page](https://docs.litellm.ai/docs/proxy/virtual_keys) (read 2026-11-06) marks key rotation as Enterprise, and none of the fields above.

Organizations are Enterprise: every route in [`organization_endpoints.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/proxy/management_endpoints/organization_endpoints.py) depends on `_enterprise_license_required`.

## How the key reaches the agent

The gateway's admin key is LiteLLM's master key. It never enters the job that runs the agent. Only the job that mints the key and the job that deletes it hold it.

A job cannot hand a secret to another job through its outputs: GitHub refuses to pass an output that holds a masked value, and an output that is not masked is printed in the next job's log. So the per-run key never crosses a job at all. The agent's job makes it.

| Step | Job               | What happens                                                                                                                                                                                                                                                                               |
| ---- | ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1    | Agent             | `costroid gateway request` draws a random key and writes it to a file only the runner's user can read. It seals the key to the gateway's sealing key with [HPKE](https://www.rfc-editor.org/rfc/rfc9180) (X25519, HKDF-SHA256, AES-256-GCM), bound to the repository, the run and the attempt |
| 2    | Agent             | `actions/upload-artifact` keeps the sealed request as an artifact of this run. It holds no secret: only the minting job can open it                                                                                                                                                       |
| 3    | Mint, in parallel | `costroid gateway mint` waits for that artifact, opens it, checks that it names this repository, run and attempt, and registers the key on the gateway with its budget, expiry and metadata                                                                                               |
| 4    | Agent             | `costroid run --gateway` waits until the key authenticates, reads the key back from the gateway, and starts the agent with the gateway's URL and the key in the agent's environment alone                                                                                                 |
| 5    | Close, always     | `costroid gateway close` deletes the key by its alias, which names the run and the attempt                                                                                                                                                                                                |

The sealing key pair comes from the admin key, so it needs no secret of its own. `costroid gateway public-key` prints the public half once. Store it as a repository variable. When the admin key changes, print it again.

No value of the key reaches an output or a log. The agent's job keeps `contents: read` and nothing else: it uploads an artifact through the runner's own token, and it learns that the key is ready by asking the gateway, never the GitHub API. The minting job needs `actions: read` to download the artifact, and checks out nothing.

An injected agent holds its own key, which is what it needs to run. It can spend that key's budget and no other. It cannot open another run's request, because every request is sealed to a key the agent's job never holds. A request copied from another run is refused, because the repository, run and attempt it names are checked.

## What the rows say

| Column or tag                    | Value                                                                                                                         |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `SubAccountId`                   | The key's id: the SHA-256 of the key in hex, the value LiteLLM stores and its spend logs carry. Never the key                 |
| `costroid/enforcement_grade`     | `gateway-precheck`, or `none` with the reason in `ChargeDescription`                                                          |
| `x_CostroidOvershootBound`       | The most a run can pass its budget by, in USD: the key's parallel-request limit times the largest request times the highest unit price the model's sheet holds |

The gateway checks a key's spend before each request. A request that starts under the budget runs to its end, and so does every request in flight beside it. The bound covers them: `max_parallel_requests` on the key, times `max_request_tokens` from the key's metadata, times the highest unit price of any billed class for that model at that context length. A key without a parallel-request limit or a request-size limit has no bound, and its rows read `none`.

A run reads `gateway-precheck` only when the key the gateway returns has a budget at or under the run's, an expiry, a parallel-request limit, a request size, and metadata naming the run the job belongs to. Otherwise the rows read `none` and say which fact was missing.

The bound assumes the gateway has counted every request that has ended. LiteLLM counts a request's cost after it answers, so a request sent at once after the one before it can be admitted before that one is counted. On 2026-11-06, LiteLLM v1.108.1 took requests back to back on a key with a USD 0.01 budget and one request at a time, each USD 0.006 in its prices and the catalog's (its `x-litellm-response-cost` header read `0.006`). In 4 of 6 runs it admitted a third request after two had passed USD 0.01. Its refusal of the fourth read "Current cost: 0.013951" while the third was still being counted, and later refusals read "0.018000000000000002". Those runs came to USD 0.018, over the budget by USD 0.008, within the journey's bound of USD 0.01. A request that has ended and is not yet counted can therefore add to the overshoot. The bound above does not count it.

## The gateway holds the budget in its own prices

LiteLLM checks `max_budget` against its own count of the key's spend, priced by its own model prices. The rows are priced by the Costroid catalog. When the gateway prices a model lower than the catalog, a key lets through more catalog-priced spend than the run's budget.

When the agent ends, `costroid run` reads the key's spend from `/key/info`. It waits up to 30 seconds for the figure to settle, because LiteLLM writes spend to its store every 10 seconds by default (`PROXY_BATCH_WRITE_AT` in [`litellm/constants.py`](https://github.com/BerriAI/litellm/blob/v1.108.1/litellm/constants.py)). When the gateway's figure differs from the rows' total, the run warns and names both decimal strings:

```text
costroid: warning: the gateway counted USD <gateway's figure> on the run's key, and the rows, priced at the catalog, come to USD <rows' total>. The gateway holds the budget in its own figure, and the rows keep theirs
```

LiteLLM sums spend in binary floating point, and its counter read `0.018000000000000002` for three requests of `0.006`, so a figure equal in decimals can differ in its last digits and still warn. The gateway's figure is never written to a row.

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

## Keep the admin key behind an environment

On a pull request from a branch of the same repository, GitHub runs the workflow file from the pull request's head, with the repository's secrets. Anyone who can push a branch can change the mint job and read the admin key. That is GitHub's model for every secret a workflow can reach, not something particular to Costroid. A pull request from a fork gets no secrets: "secrets are not passed to the runner when a workflow is triggered from a forked repository" ([Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets), read 2026-11-06).

Keep the admin key as a secret of a GitHub environment, `costroid-gateway`, and set its protection rules yourself. The recipe names that environment on the mint and close jobs, and on no other job. "Secrets stored in an environment are only available to workflow jobs that reference the environment", and with required reviewers "a job cannot access environment secrets until one of the required reviewers approves it" ([Deployments and environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments), read 2026-11-06). With required reviewers, each run waits for a person you named before a key is minted. Without a protection rule, the environment only limits which jobs see the key.

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
