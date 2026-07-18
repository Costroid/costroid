---
title: Operations
description: Scheduled ingestion, the single-writer store, backups, and forward-only schema migrations.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Embedded store

DuckDB is Costroid's embedded store. The data directory comes from
`$COSTROID_DATA_DIR` and defaults to `./data`; the database is the single file
`costroid.duckdb` inside that directory.

## Single writer

DuckDB allows one read-write process per database file. If another `costroid`
process already holds the data directory, opening it fails with this message:

```text
the Costroid database in <dir> is in use by another process — the embedded store allows a single process at a time, so stop the other costroid process (e.g. `costroid serve`) before running this command
```

Stop `costroid serve` before running `costroid ingest`,
`costroid metrics import`, or `costroid credentials set`, then restart it when
the command finishes. See [Getting started](/getting-started/) for the basic
workflow.

Scheduled ingestion is the exception for connector refreshes: `serve --sync`
runs connectors inside the serving process and shares the already-open store.
It does not allow a separate manual `costroid ingest` process to open the same
data directory.

`costroid allocation validate` is the exception: it reads only the rules JSON
file and does not open the store, so it can run alongside `serve`.

## Scheduled ingestion

Create a strict JSON sources file, validate it without opening the store, then
opt in when starting serve:

```sh
costroid sources validate --sources /etc/costroid/sources.json
costroid serve --sync --sources /etc/costroid/sources.json \
  --auth-token-file /run/secrets/costroid-token
```

`--sources` wins over `$COSTROID_SOURCES`, which wins over
`<config-dir>/costroid/sources.json`. Without `--sync`, serve does not resolve or
read this file and does not construct a connector. With `--sync`, a missing,
unreadable, invalid, or empty file is a startup error. `sources validate` checks
the JSON structure only; it does not open the store, check credential slots, or
contact remote sources.

This complete example shows the connector-specific naming style and interval
defaults:

```json
{
  "defaultInterval": "24h",
  "sources": [
    {
      "name": "aws-prod",
      "connector": "aws-focus-s3",
      "tenant": "default",
      "interval": "6h",
      "bucket": "billing-exports",
      "prefix": "focus/costroid"
    },
    {
      "name": "gcp-prod",
      "connector": "gcp-focus-bq",
      "datasetProject": "billing-host",
      "dataset": "gcp_billing_immutable_012345_EU",
      "table": "gcp_billing_export_focus_012345",
      "location": "EU",
      "jobProject": "costroid-query",
      "credential": "gcp-focus-bq",
      "since": "2026-01"
    },
    {
      "name": "openai-org",
      "connector": "openai-cost",
      "credential": "openai-cost",
      "interval": "12h"
    },
    {
      "name": "monthly-upload",
      "connector": "focus-csv",
      "path": "/var/lib/costroid/imports/focus.csv",
      "focusVersion": "1.4",
      "sourceLabel": "monthly-upload"
    }
  ]
}
```

Every source requires a unique lowercase `name` matching `[a-z0-9-]+` and one
of the seven connector names. `tenant` defaults to `default`. A source-level
`interval` overrides `defaultInterval`; both are Go duration strings. The
default is `24h`, and intervals below `15m` are rejected because every run
re-queries its source. Scheduled runs always perform full discovery and
incremental skip handling, so `force` and `period` are not accepted.

Connector fields use camelCase versions of the manual CLI flags:

| Connector | Required fields | Optional fields |
| --- | --- | --- |
| `aws-focus` | `path` | none |
| `aws-focus-s3` | `bucket`, `prefix` | none |
| `azure-focus` | `accountURL`, `container`, `prefix` | none |
| `gcp-focus-bq` | `datasetProject`, `dataset`, `table`, `location` | `jobProject`, `credential`, `baseURL`, `tokenURL`, `since`, `keyFile` |
| `anthropic-cost` | none | `credential`, `baseURL`, `since`, `keyFile` |
| `openai-cost` | none | `credential`, `baseURL`, `since`, `keyFile` |
| `focus-csv` | `path`, `focusVersion` | `sourceLabel`, `lenient` |

All sources run once immediately at startup. Later runs start at each interval
measured from the start of the prior run. Runs are serial in config order when
due together. Missed ticks coalesce into one immediate run, one source failure
does not stop the others, and each run has a one-hour timeout. The newest 50
attempts per source name and tenant are retained.

Read `GET /api/v1/sync/status` for whether scheduling is enabled, each configured
source's interval and next due time, its latest run, and its last successful
time. Removed sources remain visible from retained history. When serve runs
without `--sync`, the endpoint returns history only. The endpoint is under
`/api/` and uses the same authentication gate as cost data.
The dashboard's "Sources" view renders this same status.

The serve process must be able to read the D32 credential key file and makes
outbound connector requests. AWS and Azure ambient identity chains must exist
in serve's environment and short-lived SSO sessions may expire. Frequent AI
schedules multiply Admin-key API traffic; Anthropic's Admin key is unscopeable,
so prefer generous intervals. See [Security & deployment](/security/#scheduled-ingestion-process-posture).

## Backups

Cold-copy `costroid.duckdb`, or the whole data directory, while no `costroid`
process holds the database lock. A hot copy while `serve` is running is unsafe.
There is no backup or restore subcommand.

The credential key file defaults to `~/.config/costroid/credentials.key` and
deliberately lives outside the data directory. Back it up separately and keep it
out of data-directory backups. Losing the key makes stored credentials
undecryptable; leaking it defeats their encryption.

## Schema migrations

Schema migrations are versioned SQL files embedded in the binary. Costroid
applies pending files automatically in lexical filename order whenever it opens
the store, and records applied file names in `schema_migrations`. Eight
migrations are currently shipped.

Migrations are forward-only. There is no down-migration or rollback tool, and
there is no manual migration step.

## Upgrades and version checks

Upgrade by replacing the Costroid binary. Verify the signed release as described
in [Getting started](/getting-started/) and [Security & deployment](/security/),
then start Costroid; pending migrations apply on that first open.

The `serve` and `demo` startup lines include the running version. You can also
read it from `GET /api/v1/meta` in the [API reference](/api/operations/getmeta/).
There is no `costroid version` command.
