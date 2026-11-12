# What a cost basis promises

Every amount Costroid shows carries a basis: what the amount was computed from, and what it promises. A row states it in the tag `costroid/cost_basis`. `costroid run` prints it after the amount, and the comment prints it beside each figure.

| Basis           | Tag value         | Source                                                                                                                  | Promise                                                                                                                         | Written by                                                                                                                                                     |
| --------------- | ----------------- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Measured        | `measured`        | The agent's own token counts, priced at list from the pinned catalog                                                    | Tokens match the provider's usage report within 1% per credential per UTC day. The amount is at list price, not your contract's | `costroid run` and `costroid ingest-run`, for a run that bills an API key. The ledger's OTLP intake, when the telemetry carries `costroid.cost_basis=measured` |
| List-equivalent | `list_equivalent` | A subscription run's token counts, priced at the provider's published API rate from the pinned catalog                  | Tokens as for Measured. No invoice exists for these tokens, and the amount is never shown as billed                             | The same commands, for a run on a subscription. The ledger's OTLP intake, otherwise                                                                            |
| Billed          | `billed`          | The provider's usage and cost reports, per credential per UTC day                                                       | The provider's own figure, which the provider can restate after the month ends                                                  | No row yet. `costroid reconcile` reads it                                                                                                                      |
| Estimate        | `estimate`        | An infrastructure estimator's figure for what the change's resources will cost, read from a FOCUS cost file your own workflow wrote | As good as the estimator and the usage it assumes. Costroid neither checks nor reconciles it: no bill exists for a change that has not merged | `costroid ingest-run --from focus=FILE`, and the action's `infrastructure-file` input. [`docs/infrastructure.md`](infrastructure.md) has the file |
| Derived         | `derived`         | Token counts priced at a rate derived from a GPU's hourly cost divided by its measured throughput, for a model you host | As good as the throughput measurement behind the rate                                                                           | No command yet. The catalog holds no derived rate                                                                                                              |

`costroid run` and the comment print `measured` as Measured and `list_equivalent` as "List-equivalent, not billed". The comment prints an estimate in a section of its own under the agent's figure, as "Infrastructure, estimated", and Billed and Derived as such. The ledger stores an `estimate` row as its sender states it, apart from the agent's spend, and refuses a row whose basis is `billed` or `derived`.

## Where two bases meet

Two bases are two different claims. The comment shows one figure per currency and basis, and never adds two bases into one figure. When a run's rows hold two bases, the action's `cost_basis` output reads `multiple`, its `cost` output is empty, and its `result` output holds each figure. [`docs/agents.md`](agents.md) shows the object.

The policy's budget rules read the list amount of every USD row of an agent's, whatever its basis. A row that states a cost source and names no agent, as an estimate does, is not an agent's: no budget rule reads it, so it never raises or satisfies a budget, in the report and in the ledger alike. [`docs/policy.md`](policy.md) has the rules.

## Measured

The token counts come from the agent's own stream or files, read as [`docs/adapters.md`](adapters.md) describes. `costroid reconcile` tests the promise: it compares each token class with the provider's usage report, per credential per UTC day, and a miss over 1% is a defect. [`docs/reconcile.md`](reconcile.md) has the method, and [`docs/accuracy.md`](accuracy.md) publishes the comparison, one day at a time, for the key Costroid's own test repository spends on.

The amount is the tokens times the catalog's list price. A contract discount, a commitment or a credit makes your bill differ. `costroid reconcile` compares the list amount with the provider's cost report within 10% and splits the difference into named causes.

A run read as one set of rows per model sums each model's requests, so the sum never selects a long-context tier. Claude Code's own figure for the model chooses the tier. When nothing shows the tier, the highest tier the sum could reach prices the rows, they say so, and the amount may read high. [`docs/adapters.md`](adapters.md#claude-code) has the rule.

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

[`recipes/github-claude-code-action.yml`](../recipes/github-claude-code-action.yml) passes `ANTHROPIC_API_KEY` to `claude-code-action`, and Costroid's `read` stage holds no key. The rows read `measured` when the execution file's `apiKeySource` names a key. A run on a Claude subscription's token in place of the key reads `list_equivalent` by the same rule. `costroid init github --agent claude-code-action --auth subscription` writes this recipe with the secret `CLAUDE_CODE_OAUTH_TOKEN` in place of the key, and `costroid init github --existing` reads a workflow you already run on either. A recorded run on a subscription's token, `claude-code-action` v1.0.248 on 2026-11-11, reads `list_equivalent`, and the comment says so beside its figure ([`docs/adapters.md`](adapters.md)).

[`recipes/github-claude-agent-sdk.yml`](../recipes/github-claude-agent-sdk.yml) gives the program the secret `ANTHROPIC_API_KEY`, and [`recipes/github-codex-sdk.yml`](../recipes/github-codex-sdk.yml) gives it `OPENAI_API_KEY` as `CODEX_API_KEY`. The rows of both read `measured`. A program that hands the agent its credential another way reads as [`docs/sdk.md`](sdk.md) says.

[`recipes/github-claude-code-litellm.yml`](../recipes/github-claude-code-litellm.yml) gives the agent no provider key. Your LiteLLM proxy holds that key. The agent's job draws a key for the run. The minting job registers it with the secrets `LITELLM_MASTER_KEY` and `COSTROID_GATEWAY_SEALING_KEY` of the `costroid-gateway` environment, and the closing job deletes it with `LITELLM_MASTER_KEY`. Claude Code receives the run's key as `ANTHROPIC_AUTH_TOKEN`, and Codex through a gateway receives it as `CODEX_API_KEY`. The rows of both read `measured`: the gateway bills every request per token on its own provider key, so the basis does not follow `apiKeySource` or `CODEX_API_KEY` on this path. [`docs/gateway.md`](gateway.md) has the setup.

[`recipes/gitlab-ci.yml`](../recipes/gitlab-ci.yml) takes `ANTHROPIC_API_KEY` or `CODEX_API_KEY` as a CI/CD variable scoped to the `costroid-agent` environment, and `COSTROID_GITLAB_TOKEN`, scoped to `costroid-report`, for the note. Its Claude Code job runs `claude -p --bare` as well, so it needs the key. The rows read as the GitHub recipe for the same agent.

[`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) takes no secret. It runs no agent and writes no row.

On your own machine, the same rules read the agent as it is signed in. Claude Code on a subscription reads `list_equivalent`, and on an API key `measured`. Codex reads `measured` only when `CODEX_API_KEY` is set in the environment `costroid run` starts it in.
