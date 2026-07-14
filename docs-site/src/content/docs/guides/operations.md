---
title: Operations
description: Data directory, the single-writer store, backups, and forward-only schema migrations.
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

`costroid allocation validate` is the exception: it reads only the rules JSON
file and does not open the store, so it can run alongside `serve`.

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
the store, and records applied file names in `schema_migrations`. Seven
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
