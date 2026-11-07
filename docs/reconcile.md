# Compare a ledger with a provider's report

`costroid reconcile` compares rows with a provider's usage and cost reports, per credential per UTC day. Tokens must match within 1% by class, and a miss is a defect. List-price dollars are compared within 10%, and the residual is split into named causes.

It runs in your CI, where the admin key lives, not in the service's container, so the service's egress allowlist does not bind it.

## Name the credential

A row's credential is its `SubAccountId`. Set it when the run happens, to the id of the key, workspace or project the run spends on:

```sh
costroid run --credential apikey_01Example --budget-usd 5 --out rows.jsonl -- claude -p "Fix the failing test."
```

`COSTROID_CREDENTIAL` does the same. A value that reads like a key is refused, because it would land on every row. For rows with a null `SubAccountId`, `reconcile --credential` names one for the whole file.

## Run it where the key lives

```sh
ANTHROPIC_ADMIN_KEY=... costroid reconcile --rows rows.jsonl --from 2026-10-01 --to 2026-10-31 --out reconciliation.jsonl
```

`OPENAI_ADMIN_KEY` does the same for OpenAI rows. The key stays in the process, and goes in one header to the provider's own host. `--out` keeps one row per source, credential and day, and a rerun replaces it. Rerun through the eighth day after month end, when restating stops.

## The pull agent

Where a provider offers only a full admin key, that key never leaves your side. Costroid does not store an Anthropic admin key in a hosted service, so for Anthropic the pull agent is the only path. OpenAI's reports read under an admin key today too, so the same holds until a read-only key is tested.

The pull agent is the same image, run where the key lives:

```sh
docker run --rm --user "$(id -u)" -e ANTHROPIC_ADMIN_KEY -v "$PWD:/work" ghcr.io/costroid/costroid:1.0.0-alpha.11 \
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

| Code | Meaning                                  |
| ---- | ---------------------------------------- |
| 0    | No token miss                            |
| 1    | A token miss, which is a defect          |
| 125  | Costroid could not read the rows or a report |

## Costroid's own figures

[Accuracy](accuracy.md) runs this comparison every day on the key Costroid's own test repository spends on, from saved reports, and recomputes every line from them.
