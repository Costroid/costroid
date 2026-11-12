# Read the price catalog

Costroid prices a run's tokens from a catalog of list prices, never from the agent's own figure. The catalog is a set of dated price sheets, one per provider. Each binary carries one release of it, and every row a run writes names the catalog version and the price it used.

## Where it is published

The sheets are published in [Costroid/costroid-catalog](https://github.com/Costroid/costroid-catalog), one directory per release under `sheets/`. A published release never changes in place. For each release, [`reports/`](https://github.com/Costroid/costroid-catalog/tree/main/reports) holds a cross-check against LiteLLM's price map and OpenRouter's listing, the diff from the release before, and what the sheets leave out.

From 0.3.0, the catalog's release workflow signs each release's `SHA256SUMS` at the release's tag. This repository embeds the same files under `catalog/<version>/`, and its CI checks that signature, then each sheet against the signed `SHA256SUMS`.

Catalog 0.3.0 holds five sheets, each dated 2026-11-08: `anthropic-2026-11-08.json`, `cursor-2026-11-08.json`, `github-copilot-2026-11-08.json`, `google-2026-11-08.json` and `openai-2026-11-08.json`. Their prices were read on 2026-11-01 and 2026-11-08.

## How a price names its source

A sheet's `unit_price` row is one list price in USD for a model, a service tier, a region, a context tier and a token class, per the row's `PricingUnit`, such as `1000000 Tokens`. Each row carries three fields that say where the price came from:

| Field | What it holds |
| ----- | ------------- |
| `SourceUrl` | The provider's page the price was read from |
| `RetrievalDate` | The UTC day the page was read |
| `SourceSha256` | The SHA-256 of the page as it was read, so a later change to the page shows |

`EffectiveStart` is the first day the price holds. The column names follow FOCUS's pricing columns, and the catalog's [schema](https://github.com/Costroid/costroid-catalog/tree/main/schema/v0.1) defines each.

A row Costroid writes names its price in `SkuPriceId`, such as `anthropic:claude-fable-5-1:global:standard:all:input_uncached`, and the catalog release in `x_CostroidCatalogVersion`. Those two find the sheet row, and the sheet row finds the page.

## The fallback tier

Each sheet holds one `fallback` row per token class. Its price is the highest list price for that class across every `unit_price` row of the sheet, and its `FallbackBasis` names the row that set it.

A figure uses the fallback tier when the sheet names no price for the request's model, service tier and token class. The figure is then the sheet's highest price for that class, not the model's own. The row says so three ways: its `SkuPriceId` reads `<provider>:fallback:<class>`, its `ChargeDescription` adds `priced at the catalog fallback tier: the sheet does not name this model, tier and class`, and the comment names the model and the fallback row that priced it. A provider with no sheet has no fallback tier, and Costroid does not price its requests.

## See which catalog a binary carries

```sh
costroid version
```

It prints the release and the catalog, such as `costroid 1.0.0-alpha.23, catalog 0.3.0`. `costroid run` names the same version in its summary line, and the action's `catalog_version` output on the report job states it. The dates are in that release's sheets: their file names and `SheetDate` give the sheet's day, and each row's `RetrievalDate` the day its price was read.

## A newer catalog

No command imports a catalog. The catalog moves with Costroid's releases: a release embeds the catalog it was built with, and pinning a newer release of Costroid moves the prices with it. `costroid version` names the catalog of the release you run.
