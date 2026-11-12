# Show an infrastructure estimate beside the agent's cost

A pull request can cost money twice: the agent's run that wrote it, and the infrastructure the change will run on. `costroid ingest-run --from focus=FILE` adds the second to the rows the report reads, from a FOCUS cost file that a step of your own workflow wrote. The comment then states both, apart. The two figures are never added together.

Costroid does not estimate infrastructure. It reads the estimate your workflow made, with whatever tool and keys your workflow already holds.

## The command

```sh
costroid ingest-run --from focus=infrastructure.csv --optional --out costroid-rows.jsonl
```

The command reads `infrastructure.csv` and adds one row for each of its rows to the end of `costroid-rows.jsonl`, after the agent's rows. It leaves the agent's rows and the run's result object, `costroid-rows.run.json`, as they are. Run it after the agent's run has written its rows: `costroid run` and the other kinds of `ingest-run` replace the rows file they write.

Every row of the file is checked before one is added, so a file the command refuses adds nothing.

| Flag                      | What it does                                                                                                                                                          |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--from focus=FILE`       | The FOCUS cost file to read                                                                                                                                           |
| `--out FILE`              | The rows file the report reads, created when it is not there                                                                                                          |
| `--source NAME`           | What the estimate came from, as `costroid/cost_source` and in the comment. `infrastructure` unless given. 1 to 64 lower-case letters, digits and single hyphens     |
| `--optional`              | When FILE is not there, add no rows, say why and exit 0. Without it, a missing file ends the command with exit code 125                                              |
| `--billing-account ID`    | `BillingAccountId` of a row whose file states none                                                                                                                    |
| `--tag`, `--focus-profile` | As for the agent's rows: [`docs/tags.md`](tags.md) and [`docs/focus-profile.md`](focus-profile.md)                                                                    |

`--credential` is not read here. `SubAccountId` is the file's.

## The file

The file is CSV with a header line, in UTF-8, of at most 4 MiB (4,194,304 bytes). The header names FOCUS columns, from FOCUS 1.0 to FOCUS 1.4. The command reads the columns below by name and reads no other column.

The figure comes from one column, in this order:

1. `ListCost`, when the header has it. FOCUS defines it as "the cost calculated by multiplying the list unit price and the corresponding Pricing Quantity" ([ListCost, FOCUS 1.4](https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/blob/v1.4/specification/datasets/cost_and_usage/columns/listcost.md), retrieved 2026-11-12). It is the column the row contract keeps an estimate in.
2. Without `ListCost`, the one of `EffectiveCost`, `ContractedCost` and `BilledCost` the header has. A header with two or more of them and no `ListCost` is refused: each means a different amount, and nothing in the file says which one is the estimate.

The command prints the column it read from.

| Column                                             | Required | What the row keeps                                                                                                                           |
| -------------------------------------------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| The cost column, as above                          | Yes      | The figure: a plain decimal, 0 or more, with at most 26 digits before the point and 12 after it                                              |
| `BillingCurrency`                                  | Yes      | The figure's currency, an ISO 4217 code. Each row keeps its own, and nothing is converted                                                    |
| `ChargePeriodStart`, `ChargePeriodEnd`             | Yes      | The period the figure covers, in FOCUS's form, UTC to the second, such as `2026-12-01T00:00:00Z`. The end is exclusive                       |
| `ResourceName`, `ResourceId`                       | No       | What the figure is for. The comment names the resource by its name, else its id                                                              |
| `ChargeDescription`                                | No       | The file's own words for the row                                                                                                             |
| `ServiceName`, `ServiceCategory`, `ServiceSubcategory` | No   | The service. A category FOCUS 1.4 does not define reads `Other`                                                                             |
| `ServiceProviderName`, else `ProviderName`         | No       | Who provides the service. FOCUS 1.4 removed `ProviderName`, as [the row contract](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.5#the-rows-declare-focus-14) records, so both are read |
| `HostProviderName`, `InvoiceIssuerName`            | No       | As stated, else the provider                                                                                                                 |
| `BillingAccountId`, `BillingAccountName`           | No       | The account, else `--billing-account`, else `unknown`                                                                                        |
| `SubAccountId`, `SubAccountName`                   | No       | The sub-account, project or subscription                                                                                                     |
| `RegionId`, `RegionName`                           | No       | The region. An id outside lower-case letters, digits and hyphens is left out                                                                 |
| `SkuId`, `SkuMeter`, `SkuPriceId`                  | No       | As stated                                                                                                                                    |
| `ChargeCategory`                                   | No       | Empty or `Usage`. Any other category is refused                                                                                              |
| `ChargeClass`                                      | No       | Empty. A correction is refused                                                                                                               |
| `Tags`                                             | No       | Dropped. The command counts the rows that carried tags and prints one warning                                                                |

A file can carry any tag, so none is kept. The pull request, the commit, the repository and the CI run come from the CI system, as they do for the agent's rows, never from the file. [`docs/tags.md`](tags.md) lists them.

Every row of the file is read, whatever its resource, sub-account or project. Rows that share a currency, a period and a source make one figure, the exact sum of their amounts.

## What a row says

Each row follows the row contract [`focus-row/v0.5`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.5), as the agent's rows do. Its basis is `estimate`. The contract keeps an estimate in `ListCost`:

| Column                                     | Value                                    |
| ------------------------------------------ | ---------------------------------------- |
| `BilledCost`                               | 0. Costroid issues no invoice            |
| `EffectiveCost`                            | 0. An estimate is not a cost incurred    |
| `ListCost`, `ListUnitPrice`                | The figure                               |
| `ContractedCost`, `ContractedUnitPrice`    | The figure                               |
| `PricingQuantity`, `ConsumedQuantity`      | 1                                        |
| `PricingUnit`, `ConsumedUnit`              | `Estimate`                               |
| `ChargeCategory`, `ChargeFrequency`        | `Usage` and `Usage-Based`                |
| `ChargeDescription`                        | `resource name <name>`, `resource id <id>` and the file's description, apart by `; ` |
| `costroid/cost_source`                     | `infrastructure`, or `--source`          |
| `costroid/cost_basis`                      | `estimate`                               |
| `costroid/enforcement_grade`               | `none`                                   |
| `costroid/attribution_grade`               | `unattributed`                           |

A row names no agent and no model. A row that states a cost source and names no agent is not an agent's, and that is how the report and the policy tell the two apart.

`x_CostroidRowId` hashes a stand-in for a request id: the source, the head commit, the CI run, the SHA-256 of the file and the row's line. The same file read in the same run gives the same ids, and another run gives others.

## What Estimated means

[`docs/bases.md`](bases.md) says what each basis promises. The comment prints the basis `estimate` as "estimated". An infrastructure estimate is as good as the estimator and the usage it assumes. Costroid neither checks nor reconciles it: no bill exists for a change that has not merged.

## The period

The file states the period each figure covers, and the comment prints it. An estimator's monthly figure is a month's charge period: write `ChargePeriodStart` as the first instant of the month and `ChargePeriodEnd` as the first instant of the next, and the comment prints `2026-12`. A period of whole UTC days prints its first and last day, and any other period prints its two instants.

The comment never restates a figure to another period. A file that holds two months makes two lines.

## What the comment shows

Under the agent's figure and its table, the comment adds one line per currency, basis, source and period, then says once that the figures are not added together, then folds the rows into a table of at most 20 rows:

```markdown
Infrastructure, estimated: EUR 25.488 for 2026-12, from `infrastructure`.

Infrastructure, estimated: USD 1000 for 2026-12, from `infrastructure`.

The agent figure and these infrastructure figures are not added together, and no currency is added to another.

<details>
<summary>Infrastructure rows</summary>

| Resource | Service | Period | Amount |
| --- | --- | --- | --- |
| `aws_instance.web` | `Amazon EC2` | 2026-12 | EUR 25.488 |
| `google_compute_instance.batch` | `Compute Engine` | 2026-12 | USD 1000 |

</details>
```

Each currency stands alone, and none is converted. A comment whose amounts are withheld names each figure's currency, period and source without its amount. The check run's machine block, `costroid.result/0.9`, carries the agent's figures alone: it holds no infrastructure figure and no note of an estimate that was not there.

A rows file with no estimate row gives the same comment, byte for byte, as before this command existed.

## The policy

The policy's rules read an agent's rows alone. An estimate row never raises a budget and never satisfies one: every rule of [`docs/policy.md`](policy.md), `policy.run_budget_usd` among them, reads the rows as if the estimate were not there.

No rule holds an infrastructure estimate to a budget yet.

## Make the file

Use the estimator your workflow already runs, and write its result in this shape: a header, then one row per resource with its period, its cost and its currency.

```csv
BillingCurrency,ChargePeriodStart,ChargePeriodEnd,ListCost,ResourceName,ServiceName,SubAccountId
EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,25.488,aws_instance.web,Amazon EC2,example-account-a
EUR,2026-12-01T00:00:00Z,2027-01-01T00:00:00Z,9.52,aws_ebs_volume.data,Amazon EBS,example-account-b
```

Write the cost the resources will have. The row contract holds no negative amount, so a saving cannot be one row: a file with a negative amount is refused.

## In a workflow

The order is: the agent's job records its run, your estimator step writes the file, `ingest-run --from focus` adds its rows, and the report job reads the rows as it does today. In the GitLab template, [`recipes/gitlab-ci.yml`](../recipes/gitlab-ci.yml), the two steps go at the end of the agent job's `script`, after `costroid run`, so the job's artifact carries the rows with the estimate:

```yaml
costroid:claude:
  extends: .costroid-agent
  script:
    # The template's steps, ending with costroid run --out costroid-rows.jsonl.
    - ./scripts/estimate-infrastructure > infrastructure.csv
    - costroid ingest-run --from focus=infrastructure.csv --optional --out costroid-rows.jsonl
  artifacts:
    paths:
      - costroid-rows.jsonl
      - costroid-rows.run.json
      - costroid-rows.infrastructure.json
```

`./scripts/estimate-infrastructure` stands for your own estimator step. The `costroid:report` job needs no change.

On GitHub Actions, give the file to the action's `run` or `read` step, which adds its rows before it keeps the rows as the `costroid-rows` artifact:

```yaml
      - uses: Costroid/costroid/action@<commit>
        with:
          stage: read
          # The other inputs of the step, as before.
          infrastructure-file: ${{ runner.temp }}/infrastructure.csv
          infrastructure-optional: "true"
```

| Input                     | What it does                                                                                       |
| ------------------------- | -------------------------------------------------------------------------------------------------- |
| `infrastructure-file`     | The FOCUS cost file an earlier step of the job wrote. Empty reads none                             |
| `infrastructure-source`   | `--source`: what the estimate came from. `infrastructure` unless given                             |
| `infrastructure-optional` | `true` is `--optional`. `false`, the default, fails the step when the file is not there            |

The step needs no permission the agent's job does not hold. [`recipes/github-claude-code-action-infrastructure.yml`](../recipes/github-claude-code-action-infrastructure.yml) runs an estimator before `claude-code-action` and gives the `read` step its file. The report job does not change. `costroid lint-recipes` and `costroid lint` name an estimate input that does nothing or fails its step under `infrastructure-file`.

## A fork's pull request

When the estimator step does not run, as on a fork's pull request whose workflow holds no cloud key, it writes no file. With `--optional`, the command then adds no rows, prints why, and exits 0, so the job does not fail:

```text
costroid: the infrastructure estimate is not available: there is no file at infrastructure.csv. The step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret. No rows were added to costroid-rows.jsonl
```

The command leaves a note beside the rows file, `costroid-rows.infrastructure.json` beside `costroid-rows.jsonl`, and the action keeps it in the artifact with the rows. The report reads it, and the comment prints one line under the agent's figures:

```markdown
Infrastructure estimate: not available (the step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret).
```

The note holds a reason from a closed list and the source, and nothing else. The run's result object cannot carry it: its schema, `costroid.result/0.9`, admits no other field. A later command that reads the file removes the note. In the GitLab template, add `costroid-rows.infrastructure.json` to the agent job's `artifacts` beside the other two files.

## Where the rows go

`costroid export` sends them with the agent's rows, and [`docs/export.md`](export.md) describes it. A ledger behind `costroid serve` stores an estimate row as its sender states it: its `ListCost` is the estimate and is never priced from the catalog, it names no agent, and its join is the CI token's, as for an agent's row. The per-pull-request view states it as its own total, under the label Estimate, and no budget and no verdict reads it. A row that names an agent or a model beside the basis `estimate` is refused.

## When the file is refused

The command names the line, the column, the start of the value and what to write instead, and exits with code 125. The rows file is left as it was.

| Refusal                                                                | Fix                                                                     |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| The header names no cost column                                        | Write the estimate in `ListCost`                                        |
| No `ListCost`, and more than one of the other three                    | Write the estimate in `ListCost`                                        |
| The header lacks `BillingCurrency`, `ChargePeriodStart` or `ChargePeriodEnd` | Add the column, filled on every row                               |
| A column is named twice                                                | Name it once                                                            |
| Not a currency                                                         | Write three upper-case letters, such as `EUR`                           |
| Not a plain decimal                                                    | Write digits and at most one point, with no sign, exponent, grouping or space |
| Negative                                                               | Write what the resources will cost, not the change                      |
| More than 12 digits after the point                                    | Round where the estimator computes the figure                           |
| Not a FOCUS date and time                                              | Write UTC to the second, such as `2026-12-01T00:00:00Z`                 |
| The period ends at or before it starts                                 | Write an end after the start                                            |
| A category other than Usage, or a charge class                         | Leave purchases, taxes, credits, adjustments and corrections out        |
| Longer than 512 characters                                             | Shorten the text in that column                                         |
| The file is over 4,194,304 bytes                                       | Write one row per resource, for this pull request alone                 |
| The rows file does not end with a line feed                            | Run the command after the run's rows file is complete                   |
