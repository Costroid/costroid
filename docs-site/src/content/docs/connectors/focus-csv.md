---
title: FOCUS / CSV files
description: Import a declared FOCUS version from a strict local plain or gzip-compressed CSV file.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Run `focus-csv`

Declare the file path and FOCUS version explicitly:

```sh
costroid ingest --connector focus-csv \
  --path <export.csv> \
  --focus-version 1.2 \
  [--source-label <label>] \
  [--lenient]
```

Both `--path` and `--focus-version` are required. Costroid does not sniff the
FOCUS version. This local connector needs no credential, IAM permission, or
network access.

## Supported FOCUS versions

`--focus-version` accepts exactly six values: `1.0`, `1.0r2`, `1.1`, `1.2`,
`1.3`, and `1.4`. The Azure-declarable `1.0r2` alias is column-identical to
`1.0` and canonicalizes to `1.0` during import.

For `1.0` and `1.1`, the input must be spec-conformant: timestamps use RFC3339,
and only an empty cell represents null. In the default strict mode,
space-separated or seconds-less timestamps and literal `NULL` or `NONE`
sentinels are rejected with a row-numbered error.

## Source label and monthly replacement

`--source-label <label>` sets the logical source used in each month's batch
identity. It defaults to the file's base name. Re-importing the same label and
month replaces the stored batch.

One import must contain the complete data for every month it touches. Costroid
cannot validate that an input is a complete export: importing a part-file under
an existing label replaces that whole month's batch with the part alone.

## Optional timestamp tolerance

`--lenient` belongs only to `focus-csv`. It accepts these UTC timestamp format
variants on `BillingPeriodStart`, `BillingPeriodEnd`, `ChargePeriodStart`, and
`ChargePeriodEnd`:

- Missing seconds.
- A space between the date and time.
- A trailing ` UTC` suffix.

Every accepted value must still carry an explicit zone. Lenient mode rejects
zone-less timestamps and literal null tokens, and it changes no numeric value.

## Plain and gzip file handling

`focus-csv` is the one connector that accepts either plain or gzip-compressed
CSV. It detects gzip from the `1f 8b` magic bytes regardless of the file name. A
`.gz`-named file without those bytes is rejected as a name/content mismatch.

Headers match exact, case-sensitive PascalCase names. Unknown `x_` extension
columns are accepted and dropped. Any unknown non-`x_` column fails the import.
Run `costroid ingest -h` for the complete current flag list.

Money remains exact decimal data. The importer does not use floating-point
money math or combine currencies through conversion; a missing currency fails
the affected period.
