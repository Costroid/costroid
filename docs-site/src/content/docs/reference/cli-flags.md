---
title: CLI flags
description: Every Costroid command and flag, including scheduled sources, serve, demo, store, export, ingest, credentials, metrics, and allocation.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Command overview

Costroid has nine top-level commands: `demo`, `serve`, `allocation`, `sources`,
`metrics`, `credentials`, `store`, `ingest`, and `export`. Running `costroid`
without a command prints this usage block:

```text
usage: costroid <command> [flags]

commands:
  demo    seed an isolated synthetic store and serve the real dashboard read-only
          costroid demo [--addr host:port] [--data-dir <empty-directory>]
          (uses a fresh temporary directory by default; never reads the normal
          data directory, credential store, or connectors. The synthetic API is
          unauthenticated, read-only, and binds 127.0.0.1:8080 by default.)
  serve   serve the HTTP API and dashboard
          costroid serve [--addr host:port] [--allocation-rules <path>]
                         [--sync] [--sources <path>]
                         (--auth-token-file <path> | --auth-trusted-header <name> | --no-auth)
          (binds 127.0.0.1:8080 by default — loopback only; pass a non-loopback
          --addr to expose it. serve refuses to start unless authentication is
          configured: a bearer token via --auth-token-file/$COSTROID_AUTH_TOKEN(_FILE),
          forward-auth via --auth-trusted-header (recommended header X-WEBAUTH-USER)
          behind a trusted reverse proxy, or --no-auth to opt out explicitly. See
          docs/security.md and 'costroid serve -h'. --sync runs sources from the
          strict sources JSON inside serve; --sources overrides its resolved path.)
  allocation  validate the query-time cost-allocation (virtual tagging) rules file
          costroid allocation validate [--rules <path>]
          (the rules path resolves from --rules, then $COSTROID_ALLOCATION_RULES,
          then <config-dir>/costroid/allocation.json; reads only the JSON file —
          no store, so it is safe to run while 'costroid serve' is running)
  sources  validate the scheduled-ingestion sources file
          costroid sources validate [--sources <path>]
          (the path resolves from --sources, then $COSTROID_SOURCES, then
          <config-dir>/costroid/sources.json; performs structural validation
          only and does not open the store, check credentials, or contact sources)
  metrics  import user-authored business metrics for unit economics
          costroid metrics import --path <file.csv> [--source-label <label>]
                                  [--tenant default]
          (strict CSV format: date,metric,quantity; dates are YYYY-MM-DD and
          quantities are exact positive decimals. Re-importing under the same
          tenant and source label REPLACES that label entirely; a header-only
          file clears it. --source-label defaults to the file's base name.)
  credentials  manage the encrypted credential store (decision D32)
          costroid credentials init [--key-file <path>]
          costroid credentials set <name>     (reads the secret from stdin)
          costroid credentials list
          costroid credentials delete <name>
  store   convert the embedded store between at-rest encryption states offline
          costroid store encrypt --new-db-encryption-key-file <path>
          costroid store rekey   [--db-encryption-key-file <path>] --new-db-encryption-key-file <path>
          costroid store decrypt [--db-encryption-key-file <path>] --allow-plaintext
          (stop 'costroid serve' first; needs free disk roughly the size of the
          store; the original is kept as costroid.duckdb.bak; decrypt rewrites
          the store as plaintext and requires --allow-plaintext)
  export  one-shot offline CSV/JSON export of dashboard data (no network, no auth)
          costroid export <resource> [--format csv|json] [--out <path>]
                   [--start YYYY-MM-DD] [--end YYYY-MM-DD]
                   [--group-by service|provider|allocation|subaccount|region|tag]
                   [--tag-key <key>] [--currency CODE] [--provider <name>]
                   [--metric <name>] [--allocation-rules <path>]
                   [--db-encryption-key-file <path>]
          (resources: costs-daily, costs-summary, anomalies, tokens, usage,
          unit-economics. Mirrors the dashboard numbers via the same HTTP
          handler serve uses, in process. Offline only: stop 'costroid serve'
          first. Success is silent - stdout is EXACTLY the export bytes; --out
          writes the file and leaves stdout empty. CSV on stdout has no BOM;
          CSV --out prepends the UTF-8 BOM for Excel. json never gets a BOM.
          One-shot only - scheduling and delivery are deliberately out of scope.)
  ingest  ingest a cost export into the store
          local file:  costroid ingest --connector aws-focus --path <file> [--tenant default]
          live S3:     costroid ingest --connector aws-focus-s3 --bucket <b> --prefix <p>
                       [--period YYYY-MM] [--tenant default] [--force]
                       (--prefix is the export root: the configured S3 prefix plus the
                       export name; auth via the ambient AWS credential chain only;
                       without --period every discovered billing period is ingested;
                       periods whose stored manifest state is unchanged are skipped
                       without fetching anything — --force re-processes them)
          live Azure:  costroid ingest --connector azure-focus --account-url <u>
                       --container <c> --prefix <p>
                       [--period YYYY-MM] [--tenant default] [--force]
                       (--account-url is the storage account's blob endpoint, e.g.
                       https://<account>.blob.core.windows.net/; --prefix is the export
                       root: the export's storage directory plus the export name; auth
                       via the ambient Azure credential chain only — no SAS, no keys;
                       the same --period/--force/skip semantics as aws-focus-s3)
	  live GCP:    costroid ingest --connector gcp-focus-bq --dataset-project <p>
	               --dataset <d> --table <t> --location <loc>
	               [--job-project <p>] [--credential <slot>] [--since YYYY-MM]
	               [--period YYYY-MM] [--tenant default] [--force]
	               (Google's FOCUS BigQuery linked export is Preview. The service
	               account comes from an explicit encrypted-vault slot, otherwise
	               $GOOGLE_APPLICATION_CREDENTIALS, otherwise the default
	               gcp-focus-bq vault slot. Runtime access should use the inferred
	               minimal dataViewer + jobUser pair and be verified on first use.)
          AI vendors:  costroid ingest --connector anthropic-cost|openai-cost
                       [--credential <slot>] [--base-url <url>] [--since YYYY-MM]
                       [--period YYYY-MM] [--tenant default] [--force]
                       (one UTC calendar month per billing period; default window is the
                       last 12 months; the Admin API key comes from the encrypted
                       credential store — set it first with 'costroid credentials set
                       <slot>' (slot defaults to the connector name); --force is a
                       documented no-op for these connectors — they keep no sync state)
                       WARNING: an Anthropic Admin key is an UNSCOPEABLE full-org-admin
                       credential (it cannot be restricted to cost/usage reads), so the
                       encrypted credential store carries the whole least-privilege
                       burden — guard the key file accordingly (decisions D32, D17)
          FOCUS CSV:   costroid ingest --connector focus-csv --path <file>
                       --focus-version 1.0|1.0r2|1.1|1.2|1.3|1.4 [--source-label <label>]
                       [--period YYYY-MM] [--tenant default] [--force]
                       (the generic FOCUS import: a plain or gzip-compressed CSV export
                       whose FOCUS version you DECLARE — there is no sniffing; magic bytes
                       decide gzip vs plain. A strict importer: unknown non-x_ columns,
                       missing mandatory columns, and unparseable rows FAIL with an
                       actionable message; no gap-fill or column repair. 1.0/1.1 are
                       accepted for spec-conformant exports (RFC3339 timestamps, empty-cell
                       nulls); 1.0r2 canonicalizes to 1.0. Rows split into
                       one batch per BillingPeriodStart month, keyed <source-label>/<month>
                       (--source-label defaults to the file's base name); re-importing a
                       month under the same label REPLACES it. One import must carry the
                       COMPLETE data for each month it touches under that label — a
                       part-file replaces the month with that part alone. Takes no
                       credentials; --force is a documented no-op — it keeps no sync state)

The store location is $COSTROID_DATA_DIR (default ./data). The embedded
store allows a single process at a time. Manual 'costroid ingest' and
'costroid metrics import' require stopping serve; use 'costroid serve --sync'
for scheduled ingestion inside the serving process
```

There is no `costroid version` command. Read the running version from the
`serve` or `demo` startup line, or from `GET /api/v1/meta`; the
[operations guide](/guides/operations/) covers upgrades and version checks.

## Global environment variables

Flags take precedence over their corresponding environment variables.

| Variable | Purpose and default |
| --- | --- |
| `$COSTROID_DATA_DIR` | Data directory containing `costroid.duckdb`; default `./data`. |
| `$COSTROID_ADDR` | Listen address for `serve` and `demo`; default `127.0.0.1:8080`. |
| `$COSTROID_AUTH_TOKEN_FILE` / `$COSTROID_AUTH_TOKEN` | Bearer authentication token file, preferred, or the weaker direct token value. A direct value remains an environment source, never an argv flag. |
| `$COSTROID_AUTH_TRUSTED_HEADER` | Identity header for forward-auth; empty disables that mode. |
| `$COSTROID_AUTH_TRUSTED_PROXIES` | Comma-separated trusted proxy CIDRs; effective default `127.0.0.0/8,::1/128` when forward-auth is configured. |
| `$COSTROID_ALLOCATION_RULES` | Path to the allocation-rules JSON file; default `<config-dir>/costroid/allocation.json`. |
| `$COSTROID_SOURCES` | Path to the scheduled-ingestion sources JSON file; default `<config-dir>/costroid/sources.json`. It carries a path, never credential material. |
| `$COSTROID_CREDENTIALS_KEY_FILE` | Path to the credential key file; it carries a path, never key material. Default `~/.config/costroid/credentials.key`. |
| `$GOOGLE_APPLICATION_CREDENTIALS` | Path to Google Cloud service-account JSON when no explicit encrypted-vault slot is selected. It carries a path, never the JSON value. |
| `$AWS_*` | The AWS SDK ambient identity/configuration chain used by `aws-focus-s3`, including environment, shared configuration or SSO, and IAM roles. Costroid does not store these credentials. |
| `$AZURE_*` | The Azure SDK ambient identity chain used by `azure-focus`, including workload or managed identity and supported developer credentials. Costroid does not store these credentials. |

AI Admin keys and Google Cloud service-account JSON stored by Costroid enter
through `costroid credentials set <slot>` on stdin only—never argv or an
environment value. AWS and Azure instead use their SDK ambient identity chains.

## `costroid serve`

| Flag | Verbatim help |
| --- | --- |
| `-addr` | `listen address (overrides $COSTROID_ADDR; default "127.0.0.1:8080" — loopback. Pass a non-loopback address, e.g. 0.0.0.0:8080, to expose it on the network)` |
| `-allocation-rules` | `allocation rules JSON path (overrides $COSTROID_ALLOCATION_RULES; default <config-dir>/costroid/allocation.json)` |
| `-auth-token-file` | `bearer auth: path to a file holding the API token (overrides $COSTROID_AUTH_TOKEN_FILE; preferred over the weaker $COSTROID_AUTH_TOKEN). There is no --auth-token value flag — argv is world-readable` |
| `-auth-trusted-header` | `forward-auth: the identity header your reverse proxy sets (overrides $COSTROID_AUTH_TRUSTED_HEADER; empty disables forward-auth; recommended value X-WEBAUTH-USER)` |
| `-auth-trusted-proxies` | `forward-auth: comma-separated trusted proxy CIDRs whose identity header is honored (overrides $COSTROID_AUTH_TRUSTED_PROXIES; default 127.0.0.0/8,::1/128; IPv4 prefixes broader than /8 and IPv6 broader than /16 are refused)` |
| `-no-auth` | `serve WITHOUT authentication — the ONLY way to run unauthenticated (not recommended on a network-exposed address)` |
| `-sources` | `sources JSON path (overrides $COSTROID_SOURCES; default <config-dir>/costroid/sources.json)` |
| `-sync` | `run configured sources immediately and on their intervals inside this serve process` |

There is no `--auth-token` value flag because argv is world-readable through
tools such as `ps` and `/proc/<pid>/cmdline`. There are no TLS flags; terminate
TLS at a reverse proxy. See [Security & deployment](/security/) for the complete
authentication and exposure model.

## `costroid demo`

| Flag | Verbatim help |
| --- | --- |
| `-addr` | `listen address (overrides $COSTROID_ADDR; default "127.0.0.1:8080" — loopback. Pass a non-loopback address, e.g. 0.0.0.0:8080, to expose it on the network)` |
| `-data-dir` | `empty directory for the isolated synthetic store (default: fresh temporary directory)` |

Demo mode is unauthenticated and read-only. It binds `127.0.0.1:8080` by
default and uses an isolated store containing synthetic data.

## `costroid ingest`

| Flag | Verbatim help |
| --- | --- |
| `-connector` | `connector name (available: "aws-focus", "aws-focus-s3", "azure-focus", "gcp-focus-bq", "anthropic-cost", "openai-cost", "focus-csv")` |
| `-path` | `path to the export file to ingest (aws-focus, focus-csv)` |
| `-bucket` | `S3 bucket holding the AWS Data Export (aws-focus-s3)` |
| `-account-url` | `Azure storage account blob endpoint, e.g. https://<account>.blob.core.windows.net/ (azure-focus)` |
| `-container` | `Azure blob container holding the Cost Management export (azure-focus)` |
| `-prefix` | `export root prefix: the export's configured directory/prefix plus its name (aws-focus-s3, azure-focus)` |
| `-dataset-project` | `project containing the Google-managed FOCUS linked dataset (gcp-focus-bq)` |
| `-dataset` | `Google-managed FOCUS linked dataset name (gcp-focus-bq)` |
| `-table` | `FOCUS export table name (gcp-focus-bq)` |
| `-location` | `BigQuery dataset/job location; required on every query call (gcp-focus-bq)` |
| `-job-project` | `project that runs and is billed for BigQuery query jobs (gcp-focus-bq; default: dataset project)` |
| `-period` | `ingest only this billing period, e.g. 2026-06 (aws-focus-s3, azure-focus, gcp-focus-bq, anthropic-cost, openai-cost, focus-csv; default: all discovered)` |
| `-tenant` | `tenant identifier recorded on the ingested records` (default `default`) |
| `-force` | `re-process every period even when unchanged (aws-focus-s3, azure-focus, gcp-focus-bq; a documented no-op for anthropic-cost/openai-cost/focus-csv, which keep no sync state)` |
| `-focus-version` | `declared FOCUS version of the export: 1.0, 1.0r2, 1.1, 1.2, 1.3, or 1.4 (focus-csv; REQUIRED, no sniffing; 1.0/1.1 accept spec-conformant exports only, 1.0r2 canonicalizes to 1.0)` |
| `-source-label` | `logical source label for the per-month batch identity (focus-csv; default: the file's base name)` |
| `-lenient` | `focus-csv only, opt-in: tolerate UTC timestamp FORMAT variants (missing seconds, space separator, 'UTC' suffix); still rejects zone-less timestamps, literal null tokens, and non-RFC3339 numbers` |
| `-credential` | `credential slot name (AI Admin API key, or gcp-focus-bq service-account JSON; default: the connector name). WARNING: an Anthropic Admin key is an unscopeable full-org-admin credential — the encrypted credential store carries the whole least-privilege burden (D32)` |
| `-base-url` | `API base URL (anthropic-cost, openai-cost, gcp-focus-bq; default: the vendor's production endpoint; plain HTTP is loopback-only)` |
| `-token-url` | `OAuth token endpoint (gcp-focus-bq; default: Google's production endpoint; plain HTTP is loopback-only)` |
| `-since` | `ingest calendar months from this one forward, YYYY-MM (gcp-focus-bq, anthropic-cost, openai-cost; AI default: the last 12 months)` |
| `-key-file` | `key file path (overrides $COSTROID_CREDENTIALS_KEY_FILE; default ~/.config/costroid/credentials.key)` |

See [Connectors](/connectors/) for each connector's required flags and usage.

## `costroid credentials`

```text
usage: costroid credentials <subcommand>

subcommands:
  init [--key-file <path>]  generate the 256-bit key file (refuses to overwrite)
  set <name>                store/replace a secret, read from stdin only
  list                      list credential names and timestamps (no secrets)
  delete <name>             remove a credential

The key file defaults to ~/.config/costroid/credentials.key; override its
path with --key-file or $COSTROID_CREDENTIALS_KEY_FILE (the env var carries
the path, never key material). Secrets are AES-256-GCM encrypted at rest in
the store and never printed, logged, or passed via argv or the environment
```

Both `credentials init` and `credentials set` accept this flag:

| Flag | Verbatim help |
| --- | --- |
| `-key-file` | `key file path (overrides $COSTROID_CREDENTIALS_KEY_FILE; default ~/.config/costroid/credentials.key)` |

Secret values enter `credentials set <name>` through stdin only. They never
enter through argv or an environment value, and the store encrypts them at rest
with AES-256-GCM. See [AI vendors](/connectors/ai-vendors/) and
[Google Cloud](/connectors/gcp/) for connector-specific setup.

## `costroid metrics import`

```text
usage: costroid metrics <subcommand>

subcommands:
  import --path <file.csv> [--source-label <label>] [--tenant default]

The CSV header is exactly date,metric,quantity. Re-importing under the same
tenant and source label replaces that label entirely; a header-only file clears
it. Stop 'costroid serve' before importing because the embedded store is
single-writer.
```

| Flag | Verbatim help |
| --- | --- |
| `-path` | `path to the strict date,metric,quantity CSV` |
| `-source-label` | `logical replace label (default: the file's base name)` |
| `-tenant` | `tenant identifier recorded on the imported metrics` (default `default`) |

## `costroid allocation validate`

```text
usage: costroid allocation <subcommand>

subcommands:
  validate [--rules <path>]  parse and validate the allocation rules file

The rules path resolves from --rules, then $COSTROID_ALLOCATION_RULES (which
carries the path, never rule content), then <config-dir>/costroid/allocation.json.
validate reads only the JSON file — no store — so it is safe to run alongside
'costroid serve'
```

| Flag | Verbatim help |
| --- | --- |
| `-rules` | `allocation rules JSON path (overrides $COSTROID_ALLOCATION_RULES; default <config-dir>/costroid/allocation.json)` |

Validation reads only the JSON file and does not open the store, so it is safe
to run while `costroid serve` is running.

## `costroid sources validate`

```text
usage: costroid sources <subcommand>

subcommands:
  validate [--sources <path>]  parse and structurally validate the sources file

The path resolves from --sources, then $COSTROID_SOURCES, then
<config-dir>/costroid/sources.json. Validation reads only the JSON file. It
does not open the store, check credential slots, or contact remote sources
```

| Flag | Verbatim help |
| --- | --- |
| `-sources` | `sources JSON path (overrides $COSTROID_SOURCES; default <config-dir>/costroid/sources.json)` |

Validation is safe alongside `serve`. It checks structure and connector fields,
but it does not open the store, read credential slots, or contact a provider.

## `costroid store`

```text
usage: costroid store <subcommand>

subcommands:
  encrypt --new-db-encryption-key-file <path>
          adopt at-rest encryption on a plaintext store
  rekey   [--db-encryption-key-file <path>] --new-db-encryption-key-file <path>
          replace the at-rest encryption key on an already-encrypted store
  decrypt [--db-encryption-key-file <path>] --allow-plaintext
          remove at-rest encryption (writes a plaintext store; requires
          --allow-plaintext)

These commands convert the embedded store offline. Stop 'costroid serve' (and
any other costroid process holding the store) before running them. Free disk
roughly the size of the store is required for the copy. The original database
is retained as costroid.duckdb.bak under the data directory until you remove
it. decrypt rewrites the store as plaintext on disk and requires
--allow-plaintext to proceed. The current key for rekey/decrypt resolves from
--db-encryption-key-file or $COSTROID_DB_ENCRYPTION_KEY_FILE (flag wins). The
new key for encrypt/rekey is only --new-db-encryption-key-file (no env var)
```

| Flag | Verbatim help |
| --- | --- |
| `-new-db-encryption-key-file` | `NEW at-rest DATABASE-encryption key file path (distinct from --key-file, the D32 CREDENTIAL-store key; no env var - explicit per invocation)` |
| `-db-encryption-key-file` | `at-rest DATABASE-encryption key file path (distinct from --key-file, the D32 CREDENTIAL-store key; overrides $COSTROID_DB_ENCRYPTION_KEY_FILE)` |
| `-allow-plaintext` | `required confirmation that decrypt rewrites the store as plaintext on disk` |

See [Operations](/guides/operations/#encryption-at-rest) for the encrypt/rekey/decrypt
workflow and backup notes.

## `costroid export`

```text
usage: costroid export <resource> [flags]

resources:
  costs-daily      GET /api/v1/costs/daily
  costs-summary    GET /api/v1/costs/summary
  anomalies        GET /api/v1/anomalies
  tokens           GET /api/v1/usage/tokens/daily
  usage            GET /api/v1/usage/metrics/daily
  unit-economics   GET /api/v1/unit-economics/daily
```

| Flag | Purpose |
| --- | --- |
| `-format` | `csv` or `json` (default `csv`) |
| `-out` | write export bytes to this path instead of stdout |
| `-start` / `-end` | inclusive date bounds `YYYY-MM-DD` |
| `-group-by` | `service\|provider\|allocation\|subaccount\|region\|tag` |
| `-tag-key` | FOCUS Tags key (required when `-group-by tag`) |
| `-currency` | billing currency filter (three-letter uppercase) |
| `-provider` | FOCUS ServiceProviderName filter |
| `-metric` | business metric name (`unit-economics`) |
| `-allocation-rules` | allocation rules JSON path (same precedence as serve; used when `-group-by allocation`) |
| `-db-encryption-key-file` | at-rest DATABASE-encryption key file path (same resolution as serve) |

Resource flag sets:

- `costs-daily` / `costs-summary` / `anomalies`: `-start` `-end` `-group-by` `-tag-key` `-currency` `-provider`
- `tokens` / `usage`: `-start` `-end`
- `unit-economics`: `-metric` `-start` `-end` `-currency` `-provider`

Offline only: stop `costroid serve` first (single-writer store). Success is
silent - stdout is exactly the export bytes; `-out` writes the file and leaves
stdout empty. CSV on stdout has no BOM; CSV `-out` prepends the UTF-8 BOM for
Excel. `json` never gets a BOM. One-shot only - scheduling and delivery are
deliberately out of scope. See [Exporting data](/guides/operations/#exporting-data).

