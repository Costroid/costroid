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

## Encryption at rest

DuckDB-native at-rest encryption is opt-in. You can create a new store already
encrypted, or convert an existing store offline with `costroid store encrypt`.
Generate a high-entropy key file with permissions that restrict it to the
Costroid operator:

```sh
umask 077
head -c 32 /dev/urandom | base64 > costroid-db.key
```

Set the environment variable to the key-file path so every command that opens
the real store uses the same key:

```sh
export COSTROID_DB_ENCRYPTION_KEY_FILE=/etc/costroid/costroid-db.key
costroid serve --auth-token-file /run/secrets/costroid-token
```

`serve` and `ingest` also accept the path explicitly:

```sh
costroid serve --db-encryption-key-file /etc/costroid/costroid-db.key \
  --auth-token-file /run/secrets/costroid-token
costroid ingest --db-encryption-key-file /etc/costroid/costroid-db.key \
  --connector aws-focus --path ./export.csv
```

The environment variable carries the path, never the key itself. The
`--db-encryption-key-file` flag is the at-rest database key and is distinct
from `ingest --key-file`, which selects the D32 credential-store key. Supply
the database key to every command that opens the store. A missing, unreadable,
or empty key file is an error and never falls back to plaintext.

Keep the key separate from store backups: losing it makes the encrypted store
unreadable.

### Changing store encryption

Convert an existing store between encryption states with the offline
`costroid store` verbs. Stop `costroid serve` (and any other process holding
the store) first. Free disk roughly the size of the store is required for the
copy. The original database is retained as `costroid.duckdb.bak` under the data
directory until you remove it.

```sh
# Plaintext store -> encrypted (new key file only; no env var for the new key)
costroid store encrypt --new-db-encryption-key-file /etc/costroid/costroid-db.key

# Encrypted store -> new key (current key via flag or $COSTROID_DB_ENCRYPTION_KEY_FILE)
costroid store rekey \
  --db-encryption-key-file /etc/costroid/costroid-db.key \
  --new-db-encryption-key-file /etc/costroid/costroid-db-new.key

# Encrypted store -> plaintext (requires explicit confirmation)
costroid store decrypt \
  --db-encryption-key-file /etc/costroid/costroid-db.key \
  --allow-plaintext
```

After `encrypt`, the backup at `costroid.duckdb.bak` is the ORIGINAL PLAINTEXT
database. Remove it once you have verified the encrypted store. Deleting a file
does not securely erase it on every filesystem. After `rekey`, the backup is
encrypted with the PREVIOUS key. After `decrypt`, the live store is plaintext
and the backup remains encrypted with the previous key.

If a conversion is interrupted mid-swap (the live `costroid.duckdb` is missing
but `costroid.duckdb.bak` is present), restore the backup first:

```sh
mv "$COSTROID_DATA_DIR/costroid.duckdb.bak" "$COSTROID_DATA_DIR/costroid.duckdb"
# and the .bak.wal sibling if present
rm -f "$COSTROID_DATA_DIR/costroid.duckdb.convert-tmp" \
  "$COSTROID_DATA_DIR/costroid.duckdb.convert-tmp.wal"
# then re-run the conversion
```

## Single writer

DuckDB allows one read-write process per database file. If another `costroid`
process already holds the data directory, opening it fails with this message:

```text
the Costroid database in <dir> is in use by another process — the embedded store allows a single process at a time, so stop the other costroid process (e.g. `costroid serve`) before running this command
```

Stop `costroid serve` before running `costroid ingest`,
`costroid metrics import`, `costroid export`, or `costroid credentials set`,
then restart it when the command finishes. See [Getting started](/getting-started/)
for the basic workflow.

Scheduled ingestion is the exception for connector refreshes: `serve --sync`
runs connectors inside the serving process and shares the already-open store.
It does not allow a separate manual `costroid ingest` process to open the same
data directory.

`costroid allocation validate` is the exception: it reads only the rules JSON
file and does not open the store, so it can run alongside `serve`.

## Exporting data

Every data view in the dashboard has a Download CSV button. The download is the
table as shown: exact wire decimal strings (never reformatted), RFC 4180
quoting, CRLF rows, and a UTF-8 BOM so Excel opens non-ASCII keys correctly.

For offline scripting, `costroid export` produces the same numbers without a
browser or network. It reuses the HTTP handler `serve` uses, in process, with
no authentication:

```sh
# stop serve first (single-writer store)
costroid export costs-daily --start 2026-05-01 --end 2026-05-31
costroid export costs-summary --group-by service
costroid export tokens --format json
costroid export unit-economics --metric requests --out unit.csv
```

Resources map 1:1 to API endpoints: `costs-daily`, `costs-summary`,
`anomalies`, `tokens`, `usage`, and `unit-economics`. Grouping uses the same
`--group-by` vocabulary as the dashboard
(`service|provider|allocation|subaccount|region|tag`; `tag` requires
`--tag-key`).

Success is silent: stdout receives exactly the export bytes. With `--out`, the
file receives the bytes and stdout is empty. CSV on stdout has no BOM (so
pipes to `cut`/`awk`/`join` stay clean); CSV `--out` prepends the UTF-8 BOM
for Excel, matching the dashboard downloads. `--format json` streams the API
response body bytes untouched and never adds a BOM.

`export` is one-shot per invocation (stdout or a single `--out` path). It is
not a reports subsystem: there is no built-in delivery or recurring run.

The current-key flag and environment variable are the same as `serve` and
`ingest` (`--db-encryption-key-file` / `$COSTROID_DB_ENCRYPTION_KEY_FILE`).

## Running in a container

Costroid publishes a multi-architecture image (`linux/amd64`, `linux/arm64`) to `ghcr.io/costroid/costroid` with each release. The image is built from a distroless base (`gcr.io/distroless/cc-debian12`), runs as a non-root user (uid 65532), and contains no shell or package manager.

The default command is `demo` (synthetic, read-only, ephemeral). To serve your own data, run `serve` with a mounted volume for the store and an auth token:

```sh
docker run --rm -p 8080:8080 \
  -v costroid-data:/data \
  -v "$PWD/token:/run/secrets/costroid-token:ro" \
  -e COSTROID_AUTH_TOKEN_FILE=/run/secrets/costroid-token \
  ghcr.io/costroid/costroid:latest serve
```

The image sets `COSTROID_ADDR=0.0.0.0:8080` and `COSTROID_DATA_DIR=/data`. The `/data` directory is owned by uid 65532 in the image, so a fresh Docker named volume inherits that ownership and `serve` can write its `costroid.duckdb` store. `demo` does not use `/data`; it writes an ephemeral store under `/tmp`.

### Kubernetes

A Kubernetes-mounted volume does not inherit the image's directory ownership, so `serve` needs `fsGroup: 65532` (a pod-level setting) to make the mounted `/data` writable. The image has no shell, so it cannot define a Docker `HEALTHCHECK`; use an `httpGet` probe against `/healthz` (always unauthenticated) instead. `fsGroup` belongs to the pod `securityContext`, while `readOnlyRootFilesystem` and dropped capabilities belong to the container one:

```yaml
spec:                              # pod spec (a Deployment's .spec.template.spec)
  securityContext:                 # pod-level
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532                 # makes the mounted /data writable by serve
  containers:
    - name: costroid
      image: ghcr.io/costroid/costroid:latest
      args: ["serve"]
      ports: [{ containerPort: 8080 }]
      securityContext:             # container-level
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - { name: tmp, mountPath: /tmp }
        - { name: data, mountPath: /data }
      livenessProbe:  { httpGet: { path: /healthz, port: 8080 } }
      readinessProbe: { httpGet: { path: /healthz, port: 8080 } }
  volumes:
    - { name: tmp, emptyDir: {} }
    - { name: data, emptyDir: {} }  # replace with a PersistentVolumeClaim to persist serve data
```

With `readOnlyRootFilesystem: true`, the writable `emptyDir` at `/tmp` covers the demo path and the `/data` volume covers `serve`. This is a pod-spec fragment (`fsGroup` at pod scope, `readOnlyRootFilesystem`/`capabilities` at container scope); do NOT collapse the two `securityContext` blocks into one, since `fsGroup` is rejected in a container-level `securityContext`.

### Verifying the image

The image is keyless-signed and carries build-provenance and SBOM attestations. See [SECURITY.md](https://github.com/Costroid/costroid/blob/main/SECURITY.md#verify-release-artifacts) for the `cosign verify` and `gh attestation verify` commands.

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
so prefer generous intervals. With an `alerts` block configured, `serve --sync`
also POSTs sync-failure notifications to your own webhook or Slack endpoints
(see Alerting on sync failures below). See [Security & deployment](/security/#scheduled-ingestion-process-posture).

## Alerting on sync failures

Scheduled ingestion can notify you when a source fails. Alerting is opt-in and
off by default: it is active only under `serve --sync`, and only when the
sources file declares an `alerts` block. There is no built-in or default
endpoint, so an unconfigured Costroid notifies nowhere.

Add a top-level `alerts` array to `sources.json`. Each entry is an independent
delivery target of type `webhook` or `slack`:

```json
{
  "sources": [ ],
  "alerts": [
    {
      "name": "ops-webhook",
      "type": "webhook",
      "endpoint": "https://ops.example.com/costroid/hooks",
      "authSlot": "alert-webhook-token"
    },
    {
      "name": "team-slack",
      "type": "slack",
      "urlSlot": "alert-slack-url"
    }
  ]
}
```

| Type | Required fields | Optional fields |
| --- | --- | --- |
| `webhook` | `name`, `endpoint` | `authSlot` |
| `slack` | `name`, `urlSlot` | none |

Every channel `name` is unique and matches `[a-z0-9-]+`. A `webhook` posts the
alert as a JSON body to `endpoint`, which must use `https` unless the host is
loopback; when `authSlot` is set, its vault secret is sent as an
`Authorization: Bearer` header. A `slack` posts `{"text": ...}` to a Slack
incoming-webhook URL. Secrets are never inline: `authSlot` and `urlSlot` name
D32 credential slots, and the Slack URL is itself treated as a secret. Store the
tokens before starting serve:

```sh
costroid credentials set alert-webhook-token   # reads the token from stdin
costroid credentials set alert-slack-url        # reads the whole Slack URL from stdin
```

`costroid sources validate` checks the `alerts` block structurally (types,
required fields, endpoint shape, non-empty slot names) without opening the store
or contacting anything. At `serve --sync` startup each slot is resolved from the
vault; a missing slot is a startup error naming the channel and slot.

Delivery is edge-triggered, per source, so a persistent outage does not page you
every run:

- The first failing run of a healthy source sends a failing alert.
- A source that stays failing re-alerts at most once every 24 hours.
- The first success after a failing streak sends one recovered alert.
- Continued success sends nothing.

State is seeded from history at startup, so restarting serve during an outage
does not immediately re-page, and a later recovery still sends exactly one
recovered alert. Alerts carry operational metadata only: the source, connector,
tenant, outcome, run counts, timestamps, and the same error text shown by
`GET /api/v1/sync/status`. They never carry a cost amount, a credential, or any
AI prompt or response content. A channel that is slow or down is retried once
and then skipped for that run; it never blocks the other channels or the
scheduler.

## Anomaly alerting

Scheduled ingestion can also notify you when a day's cost is anomalous. This
reuses the SAME `alerts` channels; it adds no new endpoint and no new secret.
It is opt-in and off by default. Turn it on with a top-level `anomalyAlerts`
object alongside the `alerts` block:

```json
{
  "sources": [ ],
  "alerts": [ ],
  "anomalyAlerts": { "enabled": true }
}
```

`enabled` is the only field: a single global on/off. When it is on, every
configured channel receives anomaly alerts; there is no per-channel selection,
threshold, or sensitivity knob. The detector's parameters are fixed and
published, so an alert is hand-recomputable from the daily figures rather than
tuned per deployment. With `anomalyAlerts` absent, or `{"enabled": false}`,
anomaly alerting is off.

An anomaly is detected on the tenant's total daily spend and on each service's
own daily series, in both directions (a spike or a dip), per billing currency.
Each detected anomaly alerts exactly once: it is recorded in a persisted dedup
table, so it never re-pages on a later run and there is no reminder cadence.

The first time you enable anomaly alerting, Costroid seeds that table from your
existing history WITHOUT sending anything, so switching it on over a store full
of past data does not produce a burst of alerts for old anomalies. Only
anomalies detected after that first enable are delivered.

Unlike a sync-failure alert, an anomaly alert carries aggregate cost figures:
the observed amount, the baseline median, the deviation, and the threshold, each
as an exact decimal string, plus a FOCUS service key, the currency, the day, and
the direction. These are cost metadata; the payload still never carries a
credential or any AI prompt or response content. See the
[threat model](/security/threat-model/) for the Cardinal-Rule note on this.

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
