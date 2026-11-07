# What a cost basis promises

Every amount Costroid shows carries a basis: what the amount was computed from, and what it promises. A row states it in the tag `costroid/cost_basis`. `costroid run` prints it after the amount, and the comment prints it beside each figure.

| Basis           | Tag value         | Source                                                                                                                  | Promise                                                                                                                         | Written by                                                                                                                                                     |
| --------------- | ----------------- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Measured        | `measured`        | The agent's own token counts, priced at list from the pinned catalog                                                    | Tokens match the provider's usage report within 1% per credential per UTC day. The amount is at list price, not your contract's | `costroid run` and `costroid ingest-run`, for a run that bills an API key. The ledger's OTLP intake, when the telemetry carries `costroid.cost_basis=measured` |
| List-equivalent | `list_equivalent` | A subscription run's token counts, priced at the provider's published API rate from the pinned catalog                  | Tokens as for Measured. No invoice exists for these tokens, and the amount is never shown as billed                             | The same commands, for a run on a subscription. The ledger's OTLP intake, otherwise                                                                            |
| Billed          | `billed`          | The provider's usage and cost reports, per credential per UTC day                                                       | The provider's own figure, which the provider can restate after the month ends                                                  | No row yet. `costroid reconcile` reads it                                                                                                                      |
| Estimate        | `estimate`        | Token counts from an inventory of the places code calls a model, times stated assumptions                               | An order of magnitude, with the tokenizer named for each place                                                                  | No command yet                                                                                                                                                 |
| Derived         | `derived`         | Token counts priced at a rate derived from a GPU's hourly cost divided by its measured throughput, for a model you host | As good as the throughput measurement behind the rate                                                                           | No command yet. The catalog holds no derived rate                                                                                                              |

`costroid run` and the comment print `measured` as Measured and `list_equivalent` as "List-equivalent, not billed". The comment prints the other three as Estimate, Billed and Derived. The ledger refuses a row whose basis is neither `measured` nor `list_equivalent`.

## Where two bases meet

Two bases are two different claims. The comment shows one figure per currency and basis, and never adds two bases into one figure. When a run's rows hold two bases, the action's `cost_basis` output reads `multiple`, its `cost` output is empty, and its `result` output holds each figure. [`docs/agents.md`](agents.md) shows the object.

The policy's budget rules read the list amount of every USD row, whatever its basis. [`docs/policy.md`](policy.md) has the rules.

## Measured

The token counts come from the agent's own stream or files, read as [`docs/adapters.md`](adapters.md) describes. `costroid reconcile` tests the promise: it compares each token class with the provider's usage report, per credential per UTC day, and a miss over 1% is a defect. [`docs/reconcile.md`](reconcile.md) has the method, and [`docs/accuracy.md`](accuracy.md) publishes the comparison, one day at a time, for the key Costroid's own test repository spends on.

The amount is the tokens times the catalog's list price. A contract discount, a commitment or a credit makes your bill differ. `costroid reconcile` compares the list amount with the provider's cost report within 10% and splits the difference into named causes.

## List-equivalent

A run on a subscription is not billed per token, so no invoice holds these tokens. Costroid prices them at the provider's published API rate, which says what the run would cost on an API key. The comment and the CLI always say "not billed" beside the amount. `costroid reconcile` compares the tokens and states that no API bill exists to compare the amount with.

Claude Code states how a run bills in `apiKeySource`. A value that names a key reads `measured`. Any other value, `none` or no value reads `list_equivalent`, because that is the weaker claim. Codex reads `measured` when `CODEX_API_KEY` is set, and `list_equivalent` otherwise. Under a gateway, both read `measured` ([below](#which-key-each-recipe-takes)).

## Billed

The provider's cost report states what it billed for a credential's day: `provider_effective` in the rows `costroid reconcile` writes, beside the ledger's own list amount. The reports carry the provider's freshness stamp where it gives one, as `data_refreshed_at`, and the provider can restate a day after the month ends. Rerun `costroid reconcile` through the eighth day after month end.

## Which key each recipe takes

The basis follows the key the agent runs on.

[`recipes/github-claude-code.yml`](../recipes/github-claude-code.yml) takes the secret `ANTHROPIC_API_KEY` as the action's `api-key`. It runs `claude -p --bare`, and under `--bare` Claude Code authenticates with `ANTHROPIC_API_KEY` or an `apiKeyHelper` alone, so a Claude subscription's token does not work here (`claude --help`, Claude Code 2.1.319, read 2026-11-07). The rows read `measured`. A recorded Claude Code 2.1.319 run on an API key reads `measured`, and a test holds it.

[`recipes/github-codex.yml`](../recipes/github-codex.yml) takes the secret `OPENAI_API_KEY` as the action's `api-key`, which the action gives Codex as `CODEX_API_KEY`. The rows read `measured`.

[`recipes/github-codex-action.yml`](../recipes/github-codex-action.yml) gives `openai/codex-action` the secret `CODEX_API_KEY` or `OPENAI_API_KEY` as its `openai-api-key`. That action takes only an API key, so the rows read `measured`.

The action's `run` stage refuses to start without `api-key`, so neither recipe runs an agent on a subscription.

[`recipes/github-claude-code-action.yml`](../recipes/github-claude-code-action.yml) passes `ANTHROPIC_API_KEY` to `claude-code-action`, and Costroid's `read` stage holds no key. The rows read `measured` when the execution file's `apiKeySource` names a key. A run that you give a subscription's token in place of the key reads `list_equivalent` by the same rule. No run on a subscription's token has been recorded, so that reading is unverified.

[`recipes/github-claude-code-litellm.yml`](../recipes/github-claude-code-litellm.yml) gives the agent no provider key. Your LiteLLM proxy holds that key. The agent's job draws a key for the run. The minting job registers it with the secrets `LITELLM_MASTER_KEY` and `COSTROID_GATEWAY_SEALING_KEY` of the `costroid-gateway` environment, and the closing job deletes it with `LITELLM_MASTER_KEY`. Claude Code receives the run's key as `ANTHROPIC_AUTH_TOKEN`, and Codex through a gateway receives it as `CODEX_API_KEY`. The rows of both read `measured`: the gateway bills every request per token on its own provider key, so the basis does not follow `apiKeySource` or `CODEX_API_KEY` on this path. [`docs/gateway.md`](gateway.md) has the setup.

[`recipes/gitlab-ci.yml`](../recipes/gitlab-ci.yml) takes `ANTHROPIC_API_KEY` or `CODEX_API_KEY` as a CI/CD variable scoped to the `costroid-agent` environment, and `COSTROID_GITLAB_TOKEN`, scoped to `costroid-report`, for the note. Its Claude Code job runs `claude -p --bare` as well, so it needs the key. The rows read as the GitHub recipe for the same agent.

[`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) takes no secret. It runs no agent and writes no row.

On your own machine, the same rules read the agent as it is signed in. Claude Code on a subscription reads `list_equivalent`, and on an API key `measured`. Codex reads `measured` only when `CODEX_API_KEY` is set in the environment `costroid run` starts it in.
