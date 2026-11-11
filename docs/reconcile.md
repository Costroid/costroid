# Compare a ledger with a provider's report

`costroid reconcile` compares rows with a provider's usage and cost reports, per credential per UTC day. Tokens must match within 1% by class, and a miss is a defect. Each price the cost report implies must equal the catalog's, and a miss is a defect. List-price dollars are compared within 1% per class, then within 10% for the day, and the residual is split into named causes.

It runs in your CI, where the admin key lives, not in the service's container, so the service's egress allowlist does not bind it.

## Name the credential

A row's credential is its `SubAccountId`. Set it when the run happens, to the id of the key, workspace or project the run spends on:

```sh
costroid run --credential apikey_01Example --budget-usd 5 --out rows.jsonl -- claude -p "Fix the failing test."
```

`COSTROID_CREDENTIAL` does the same. A value that reads like a key is refused, because it would land on every row. For rows with a null `SubAccountId`, `reconcile --credential` names one for the whole file.

A run on a Workload Identity Federation token, such as `claude-code-action` with `anthropic_federation_rule_id`, spends on no API key. Anthropic's report gives it a null `api_key_id` and attributes it to the service account the token acts as ([Usage and Cost Analytics API](https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api), retrieved 2026-11-08). Name that service account as the credential, such as `svac_01Example`, and `reconcile` reads Anthropic's report by actor for it. Requests on the service account's own API keys are attributed to it too, so its figure counts them.

## A gateway's rows

A run behind a [LiteLLM gateway](gateway.md) names its own key in `SubAccountId`: the key's id, the SHA-256 of the key in hex. The provider never sees that key. It reports the gateway's traffic under the key the gateway sends with. Name that key once per provider:

```sh
ANTHROPIC_ADMIN_KEY=... costroid reconcile --rows rows.jsonl --from 2026-10-01 --to 2026-10-31 --upstream-credential anthropic=apikey_01Example
```

Each row whose `SubAccountId` has the shape of a gateway key's id is then compared under `apikey_01Example`, and the row's notes say so. The provider's figure counts every request the gateway sent with that key, so the traffic of runs missing from the rows file reads as a token miss, as on any shared key. Without the flag, `reconcile` stops and names it, because no provider's report names a run's key.

## Run it where the key lives

```sh
ANTHROPIC_ADMIN_KEY=... costroid reconcile --rows rows.jsonl --from 2026-10-01 --to 2026-10-31 --out reconciliation.jsonl
```

`OPENAI_ADMIN_KEY` does the same for OpenAI rows. The key stays in the process, and goes in one header to the provider's own host. `--out` keeps one row per source, credential and day, and a rerun replaces it. Rerun through the eighth day after month end, when restating stops.

## The pull agent

Where a provider offers only a full admin key, that key never leaves your side. Costroid does not store an Anthropic admin key in a hosted service, so for Anthropic the pull agent is the only path. OpenAI's reports read under an admin key today too, so the same holds until a read-only key is tested.

The pull agent is the same image, run where the key lives:

```sh
docker run --rm --user "$(id -u)" -e ANTHROPIC_ADMIN_KEY -v "$PWD:/work" ghcr.io/costroid/costroid:1.0.0-alpha.23 \
  reconcile --rows /work/rows.jsonl --from 2026-10-01 --to 2026-10-31 --out /work/reconciliation.jsonl
```

The output holds daily totals per credential: token counts, dollar figures and causes. Nothing schedules it yet, and nothing sends the output anywhere. Run it from your own scheduler.

## Saved reports

A report saved earlier reconciles with no key:

```sh
costroid reconcile --rows rows.jsonl --from 2026-11-01 --to 2026-11-01 --reports saved --as-of 2026-11-04T00:00:00Z
```

`--as-of` is when the files were saved, and it decides which days they could have finished. The directory holds one provider response per file, and a range must fit one response:

| File                   | Response of                               |
| ---------------------- | ----------------------------------------- |
| `anthropic-usage.json` | `GET /v1/organizations/analytics/usage`   |
| `anthropic-cost.json`  | `GET /v1/organizations/analytics/cost`    |
| `openai-usage.json`    | `GET /v1/organization/usage/completions`  |
| `openai-costs.json`    | `GET /v1/organization/costs`              |

Keep each response's body as the provider sent it, or cut to the records of the credentials you reconcile, as `scripts/accuracy -save` keeps them. A range that needs a second page, or a second provider window, cannot be saved as one file.

## A run across midnight

A row is dated by the start of its charge period. A row that covers a whole run or turn and crosses 00:00 UTC puts all its tokens on its first day, while the provider dates each request by when it was served. Each day then misses on its own.

When a day misses and a row in the file ran past midnight on the same credential, `reconcile` compares the days that row spans as one. If every class matches within 1% over those days together, each day reads `matched` and its `span` names the cause `run_spans_days` with the first and last day. The first day's row holds the joined token comparison and the joined dollars by class, and a later day's row holds none. Each day's dollar residual names `run_spans_days` for the tokens that moved, priced at the standard tier through the pinned catalog. If the days miss together too, each day keeps its own verdict.

The days are joined only when every one of them is in the range and reported. Otherwise the day reads `pending`, and its note names the range to reconcile or the day the provider has not finished. Half a run is not a miss.

## Prices

Costroid asks Anthropic's cost report for each day split by model and token type, under the same tier, region and speed it reads usage by. It asks OpenAI's for each line item, such as `gpt-6-astra, input_tokens`. A line's list figure divided by the usage report's tokens for the same model and class is the provider's own unit price for that day. Anthropic's list figure is `list_amount`. OpenAI's cost report states one amount, and Costroid reads it as the list figure, so a negotiated OpenAI rate shows as a miss on every line.

Each row's `prices` holds one entry per model, tier, region and class: `tokens`, `provider_list`, `implied_unit_price` in USD per million tokens, `catalog_unit_price`, the `sku_price_id` it came from, and `match`. A price matches when the tokens at the catalog's price land within one unit of the last digit the report states. Anthropic states cents to six places, so that unit is USD 0.00000001. OpenAI writes a JSON number, which drops trailing zeros, so the unit is the last digit the number shows.

Over 50,000 output tokens, a price of USD 10.01 per million against the catalog's 10 moves the line by USD 0.0005, which is 50,000 units. On a day that costs USD 0.0004, an output price 9% over the catalog's misses, while the day's dollars land within 10% and within rounding.

A miss sets the row's state to `price_miss`, and it is a defect, like a token miss. A row with both reads `tokens_miss`, and its `prices` still marks each miss. `implied_unit_price` is rounded to six places and carries the report's rounding: 203 cache writes stated as 0.002538 cents imply USD 0.125025 per million, and match the catalog's 0.125. The verdict reads the amounts, never the rounded price.

Where the catalog prices a class at more than one context tier, the report does not say which tier the tokens fell in. The line is held to the one tier the ledger's own rows were priced at. When the report's figure matches a different tier, the notes name it, because then the ledger priced the run at the wrong tier.

These lines prove no price, and the row's notes name each one:

- Anthropic's priority tier. Requests under a Priority Tier commitment "contribute zero token cost to `amount` and `list_amount`", and the commitment is billed elsewhere ([Usage and Cost Analytics API](https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api), retrieved 2026-11-07).
- A class the catalog prices at more than one context tier, when the ledger priced it at more than one that day, or at none.
- An OpenAI model served at more than one tier that day, because a line item does not name its tier.
- A token type or line item that names no class Costroid reads, and any class of the same model that has no line of its own that day.
- A report that does not split token cost by class, such as one saved by an earlier version.

A model the catalog has no price for is held to the catalog's fallback, which is an upper bound, and the notes say so.

OpenAI's costs page shows one line item kind, `input_tokens`, which Costroid reads as uncached input ([Costs](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs), retrieved 2026-11-07). It reads `input_cached_tokens`, `input_cache_write_30m_tokens`, `input_cache_write_12h_tokens` and `output_tokens` by the usage report's field names. No saved response has shown those four on a line item yet.

## Dollars by class

`dollars.classes` compares each class at list price: the ledger's list cost against the cost report's list figure. Each class is within when it lands within 1% of the report's figure, and the bound is inclusive. It is the first line of the dollar comparison, and the day within 10% is the second.

A class outside 1% with matching tokens and prices means the ledger priced the right tokens at the wrong price, such as the wrong context tier, which the day's total can hide. It is not a defect on its own, because priority-tier tokens move it too. Cache writes compare as one figure where the tokens do. `classes` is null when the report does not split token cost by class.

## Amounts and rounding

Each row's `dollars` holds two provider figures for the day's token charges. `provider_effective` is what the cost report bills, after any discount. `provider_list` is the same charges at list price. Anthropic states both, as `amount` and `list_amount`. OpenAI's cost report states one amount, so `provider_list` is null on an OpenAI row.

`rounding` compares `provider_list` with the ledger's list cost, the same charges at the same prices, under the rounding variance tolerance of FOCUS 1.4:

```text
Tolerance = MAX(100 × Subunit, SQRT(Rows) × 0.5 × Subunit)
```

`Rows` is the number of ledger rows summed, and `Subunit` is the currency's smallest unit, 0.01 for USD. Over 5 rows a variance of USD 0.02 is within and USD 5.00 is not. Over 1,000,000 rows USD 4.50 is within and USD 15.00 is not. The bound is inclusive. Costroid compares squares, so it takes no square root and uses no float. Source: [`rounding_variance_tolerance.md` at FOCUS v1.4](https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/blob/v1.4/specification/appendix/rounding_variance_tolerance.md), retrieved 2026-11-07.

A variance outside the tolerance is more than rounding. The 10% verdict and its causes still judge the day.

`data_refreshed_at` is the provider's freshness stamp on the reports that carried the day, the earliest when several did. OpenAI stamps none, so it is null there.

OpenAI's older usage responses leave uncached input and 30-minute writes to be derived by subtraction. A difference below zero means the fields do not overlap as read, and the run stops with the day and the model named.

## Exit codes

| Code | Meaning                                      |
| ---- | -------------------------------------------- |
| 0    | No token miss and no price miss              |
| 1    | A token miss or a price miss, each a defect  |
| 125  | Costroid could not read the rows or a report |

## Costroid's own figures

[Accuracy](accuracy.md) runs this comparison every day on the key Costroid's own test repository spends on, from saved reports, and recomputes every line from them.
