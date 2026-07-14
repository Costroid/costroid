---
title: Google Cloud (Preview)
description: Configure the Preview Google-managed FOCUS BigQuery connector with isolated runtime access.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Preview, build-from-source connector

Google's FOCUS BigQuery export is Preview / Pre-GA, is provided as-is, and may
change schema. It is a Google-managed, read-only linked dataset. The
`gcp-focus-bq` connector is on `main` for build-from-source users; it is not in
the v0.1.0 binaries.

Enable the export early. US and EU multi-regions backfill only to the start of
the previous month, and catch-up can take about five days. Single-region
datasets have no backfill. The managed table deletes rows after two years, so
local Costroid ingestion is the durable history.

## Run `gcp-focus-bq`

The dataset project, dataset, table, and location are required on every call:

```sh
costroid ingest --connector gcp-focus-bq \
  --dataset-project <host-project> \
  --dataset <d> \
  --table <t> \
  --location <LOCATION>
```

`--location` must match the linked dataset's location. Omitting it commonly
makes an EU dataset appear missing from the default US location.

The optional `--job-project <project>` selects the project that runs the query
jobs and defaults to the dataset project. Other optional controls are
`--credential <slot>`, `--since YYYY-MM`, `--period YYYY-MM`, and `--force`.
Run `costroid ingest -h` for the complete flag list, and see the
[connector overview](/connectors/) for shared replacement behavior.

## Separate setup authority from runtime access

Use the one-time administrator roles only to enable the export: Billing Account
Costs Manager or Billing Account Administrator, plus Project IAM Admin and
BigQuery Admin. Never grant those roles to Costroid's runtime identity.

For the runtime reader, grant:

- `roles/bigquery.dataViewer` on the linked dataset.
- `roles/bigquery.jobUser` on the job project.

This pair is Costroid's least-privilege inference to verify on first use, not a
Google-documented requirement for the FOCUS dataset. Google documents no
specific read-role pair for this dataset. Costroid needs no billing-account role
at runtime and requests only the
`https://www.googleapis.com/auth/bigquery` OAuth scope.

## Service-account credential

The connector supports service-account JSON only; full Application Default
Credentials are not supported. Credential selection follows this precedence:

1. An explicit `--credential <slot>`.
2. The service-account file path in `$GOOGLE_APPLICATION_CREDENTIALS`.
3. The default `gcp-focus-bq` encrypted-vault slot.

The environment variable carries a path, not credential material. To use the
vault, initialize it as described in the [connector overview](/connectors/),
then redirect the secured JSON file through standard input:

```sh
costroid credentials set gcp-focus-bq < /secure/path/costroid-gcp-reader.json
```

:::note[Preview schema warning]
If the Preview schema adds columns that the connector does not select,
`gcp-focus-bq` prints a warning to standard error. A removed required column
fails with an actionable error instead of silently changing the import.
:::

:::note[BigQuery query cost]
Each sync runs BigQuery queries subject to on-demand billing minimums: 10 MB per
query and per referenced table. Expect a small, non-zero BigQuery cost per sync;
a typical daily incremental pull costs well under a cent.
:::
