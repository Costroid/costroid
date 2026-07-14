---
title: AI vendors
description: Ingest aggregated AI cost and usage metadata without ingesting prompt or response content.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Cardinal Rule: content-blind ingestion

Costroid fetches only aggregated cost and usage metadata: amounts, currency, day
buckets, model identity, line-item labels, and usage quantities such as
per-model token counts and request counts. It never ingests prompt or response
content. Token counts are aggregated quantities, not the text of prompts or
completions.

Credentials never appear in logs, errors, command arguments, URLs, or any logged
header. A connector sends the credential only in the vendor-required outbound
authentication header, keeps it in memory, and never logs request headers.

Store AI credentials only through `costroid credentials set <slot>` standard
input, as described in the [connector overview](/connectors/).

## Anthropic: `anthropic-cost`

```sh
costroid ingest --connector anthropic-cost \
  [--credential <slot>] \
  [--since YYYY-MM] \
  [--period YYYY-MM]
```

The connector reads two aggregated organization endpoints:

- `GET https://api.anthropic.com/v1/organizations/cost_report` for cost.
- `GET https://api.anthropic.com/v1/organizations/usage_report/messages` for
  per-token usage counts that enrich cost rows, never message content.

Requests use `x-api-key` and `anthropic-version: 2023-06-01` headers. The
Anthropic Admin key (`sk-ant-admin01-…`) is an unscopeable full-organization
administrator credential: Anthropic cannot narrow it. The encrypted vault and
its guarded key file therefore carry the entire least-privilege burden.

Paste the key interactively, then press Ctrl-D:

```sh
costroid credentials set anthropic-cost
```

The slot defaults to `anthropic-cost`; `--credential <slot>` selects another.
`--force` is accepted but is a documented no-op because the connector keeps no
incremental sync state.

## OpenAI: `openai-cost`

```sh
costroid ingest --connector openai-cost \
  [--credential <slot>] \
  [--since YYYY-MM] \
  [--period YYYY-MM]
```

The connector reads `GET https://api.openai.com/v1/organization/costs` for
aggregated cost and ten read-only
`GET https://api.openai.com/v1/organization/usage/<name>` endpoints. Requests
use an `Authorization: Bearer` header. These calls share one **Usage** read
permission; costs is a method under the Usage resource.

The ten usage endpoints provide non-token quantities only: requests, images,
characters, seconds, sessions, bytes, and search calls. Token quantities come
from the costs endpoint's `quantity` field, not from those usage endpoints.

OpenAI supports Restricted admin keys. Create one that reads only the **Usage**
resource, and verify that scope when creating it. Store it through standard
input:

```sh
costroid credentials set openai-cost
```

The slot defaults to `openai-cost`; `--credential <slot>` selects another.
`--force` is accepted but is a documented no-op because the connector keeps no
incremental sync state.

:::note[Different least-privilege boundaries]
Anthropic Admin keys are unscopeable, so the encrypted vault carries the
burden. OpenAI Admin keys are restrictable, so scope the key to the Usage
resource. Do not use one vendor's credential assumptions for the other.
:::

## Default ingest window

Without `--since` or `--period`, both AI connectors ingest a fixed last-12-months
window: the 11 months before the current month plus the current month. This is
not an unbounded history backfill. Use `--since YYYY-MM` to begin farther back,
or `--period YYYY-MM` to ingest exactly one month.

All amounts remain exact decimals. A period with a missing currency fails;
Costroid never assumes USD or converts one currency into another.
