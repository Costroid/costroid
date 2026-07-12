<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

# gcp-focus-bq fixture surface

Hermetic BigQuery v2 row-envelope fixtures for the `gcp-focus-bq` connector.
They back `fakebigquery` (HTTP fake) and the offline CLI e2e
`TestOfflineE2EGCPFocusBigQuery` in `cmd/costroid/e2e_test.go`. The fake and
the connector share one rule: **cells and `schema.fields` MUST be in
`PinnedFields` order** (order-exact, not set-equal). The e2e copies
`fixture/` into a temp dir and overlays `restated/` / `reject/` mid-run.

## Layout

| Path | Role |
| --- | --- |
| `fixture/2026-05.json` | Baseline May invoice month (2 rows) |
| `fixture/2026-06.json` | Baseline June invoice month (3 rows) |
| `restated/2026-06.json` | Restated June: 5 rows / BilledCost 13 (was 3/15) |
| `reject/2026-06.json` | Single row with null required `BilledCost` → abort |

May is left untouched in the restated and reject overlays so the
unchanged-sync path still fires for that month.

## Per-month row counts and `BilledCost` totals

**May (2 rows) = `1.123456789012345678` + `2`**

| Row | BilledCost | Service (approx) | Notes |
| --- | --- | --- | --- |
| 1 | `1.123456789012345678` | Compute Engine | 18-fractional-digit exact money |
| 2 | `2` | Cloud Billing | null `x_ExportTime`; gap-fill identities |

**June (3 rows) = `4` + `5` + `6` = 15**

| Row | BilledCost | Notes |
| --- | --- | --- |
| 1 | `4` | `x_Labels` → Tags `{"env":"prod"}` |
| 2 | `5` | empty `x_Labels` array → **Tags key ABSENT** (not `"{}"`) |
| 3 | `6` | `x_Labels` → Tags `{"team":"finance"}` |

## Per-row `x_Labels`

- **May row 1:** `env=prod`, `team=platform` → Tags
  `{"env":"prod","team":"platform"}` (JSON object order may vary; content is the contract).
- **May row 2:** `cost-center=finops`.
- **June row 1:** `env=prod`.
- **June row 2:** empty array `[]` → no Tags field on the FOCUS raw record.
- **June row 3:** `team=finance`.

## Per-month `MAX(x_ExportTime)` (int64 microseconds)

| Month | MAX micros | RFC3339 (full µs) | Skip-line form (`time.RFC3339`, no fractional seconds) |
| --- | --- | --- | --- |
| May | `1778846400123456` | `2026-05-15T12:00:00.123456Z` | `2026-05-15T12:00:00Z` |
| June | `1781524800000003` | `2026-06-15T12:00:00.000003Z` | `2026-06-15T12:00:00Z` |

Derivation: `time.UnixMicro(micros).UTC()`. The CLI skip line formats
`LastModified` with `time.RFC3339` (no fractional seconds), so the e2e pins:

```
period 2026-05: unchanged since 2026-05-15T12:00:00Z; skipped
period 2026-06: unchanged since 2026-06-15T12:00:00Z; skipped
```

May row 2 has a null `x_ExportTime`; the aggregate `MAX` ignores nulls and
lands on row 1's watermark. When every row in a month is null, the change
token is `null|<count>` and `LastModified` falls back to `tables.get`
`lastModifiedTime` (see package tests).

## Restated June (3 rows / 15 → 5 rows / 13)

`restated/2026-06.json` keeps the original three rows and appends:

| Row | BilledCost | Purpose |
| --- | --- | --- |
| 4 | `-4` | Correction credit |
| 5 | `2` | Correction charge |

Net BilledCost becomes **13** (`15 − 4 + 2`). The e2e asserts the CLI
delta line `period 2026-06: replaced (5 records; BilledCost 15 → 13)` and
that May stays tuple-skipped.

## Reject fixture

`reject/2026-06.json` is one June row with null `BilledCost`. The pipeline
must abort only that period with a row-numbered validation error containing
`BilledCost is null`, while May remains `unchanged since …; skipped`.

## Coupling consumer

These numbers, timestamps, and shapes are coupled to
**`TestOfflineE2EGCPFocusBigQuery`** (`cmd/costroid/e2e_test.go`). Update that
test in the same change if you alter any fixture total, watermark, or label.
