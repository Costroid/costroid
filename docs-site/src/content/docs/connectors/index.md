---
title: Connectors
description: Connect cloud, AI vendor, and FOCUS file sources to Costroid's shared FOCUS model.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## One model for every source

Costroid ingests cost and usage metadata through per-source connectors. Each
connector normalizes its source into the same FOCUS model before Costroid stores
it. The available connectors are `aws-focus`, `aws-focus-s3`, `azure-focus`,
`gcp-focus-bq`, `anthropic-cost`, `openai-cost`, and `focus-csv`.

| Connector | What it ingests | File vs Live | Credential | Least-privilege |
| --- | --- | --- | --- | --- |
| `aws-focus` | AWS Data Exports FOCUS gzipped CSV | File | None | No network or IAM access |
| `aws-focus-s3` | AWS Data Exports FOCUS objects in S3 | Live | AWS SDK ambient chain; not stored by Costroid | `s3:ListBucket` on the export prefix and `s3:GetObject` on its objects |
| `azure-focus` | Azure Cost Management FOCUS export in Blob Storage | Live | Azure ambient chain; not stored by Costroid | Storage Blob Data Reader on the export container |
| `gcp-focus-bq` | Google-managed FOCUS BigQuery linked dataset (Preview) | Live | Service-account JSON | Dataset reader plus query-job runner; inferred minimum to verify on first use |
| `anthropic-cost` | Aggregated organization cost and per-token usage-count metadata | Live | Anthropic Admin key in the encrypted vault | Admin key is unscopeable; the encrypted vault carries the burden |
| `openai-cost` | Aggregated organization cost and usage-count metadata | Live | Restricted OpenAI Admin key in the encrypted vault | Restricted key scoped to the Usage resource |
| `focus-csv` | Generic FOCUS CSV export | File | None | No network or IAM access |

See the connector-specific setup for [AWS](/connectors/aws/),
[Azure](/connectors/azure/), [Google Cloud](/connectors/gcp/),
[AI vendors](/connectors/ai-vendors/), or [FOCUS / CSV files](/connectors/focus-csv/).

## Encrypted credentials

Initialize the credential vault once. By default, the 256-bit key file is
created at `~/.config/costroid/credentials.key`; initialization refuses to
overwrite an existing key.

```sh
costroid credentials init [--key-file <path>]
```

The `--key-file` option or `$COSTROID_CREDENTIALS_KEY_FILE` can override that
path. The environment variable carries a file path, never key material.

Secrets enter only through standard input. Paste one interactively and press
Ctrl-D, or redirect a secured file:

```sh
costroid credentials set <slot>
costroid credentials set <slot> < /secure/path/file
```

Vault entries use AES-256-GCM encryption at rest. Costroid never prints or logs
their values, and never passes them through command arguments or environment
variables. Listing shows names and timestamps only:

```sh
costroid credentials list
costroid credentials delete <slot>
```

The default slot name is the connector name. Use `--credential <slot>` to select
another slot. Current connectors use the vault only for Google Cloud
service-account JSON and AI Admin keys. Costroid stores no AWS or Azure
credentials; those connectors use their SDKs' ambient chains.

## Ingest behavior shared by connectors

Every ingest is tenant-aware. `--tenant <name>` selects a tenant; the default is
`default`.

Connectors that discover billing periods use one UTC calendar month per batch.
Ingesting the same source and month again replaces that month's batch
transactionally, so a restated bill does not double-count the earlier batch.
Where supported, `--period YYYY-MM` limits a run to one period. Without it,
`focus-csv` and the live cloud connectors process all discovered periods; the
local `aws-focus` connector processes the supplied export file. The AI
connectors instead use a fixed last-12-months window described on the
[AI vendors page](/connectors/ai-vendors/).

For `aws-focus-s3`, `azure-focus`, and `gcp-focus-bq`, `--force` re-processes
periods whose source state is unchanged. It is a documented no-op for
`anthropic-cost`, `openai-cost`, and `focus-csv`, which keep no incremental sync
state.

The embedded store has a single-writer rule. Stop `costroid serve` before a
manual ingest. For unattended refreshes, declare the same connector settings in
the strict sources JSON and run `costroid serve --sync`; its serial scheduler
shares serve's open store. The [operations guide](/guides/operations/#scheduled-ingestion)
covers the config format, intervals, status endpoint, and credential posture.
Run `costroid ingest -h` for the complete current manual-ingest flag list.

## Exact money and version information

Costs remain exact decimals. Costroid does not use floating-point money math or
combine currencies through conversion. A period with a missing currency fails;
Costroid never assumes USD.

There is no `costroid version` command. The version appears in the `serve` or
`demo` startup line and in `GET /api/v1/meta`; see the [API reference](/api/).
