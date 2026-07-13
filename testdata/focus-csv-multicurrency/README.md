<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

# Multicurrency FOCUS CSV sample export

`sample-export.csv` is a strict FOCUS 1.2 fixture with EUR and USD cost rows
on each of two UTC charge days. Its column set matches
`testdata/focus-csv/focus-1.2.csv`, and every money value has exactly 18
fractional digits so the ingest-to-API path proves `DECIMAL(38,18)` exactness.

## Independently recomputed `BilledCost` totals

| Charge day | EUR rows | EUR daily total | USD rows | USD daily total |
| --- | ---: | ---: | ---: | ---: |
| 2026-05-01 | `1.123456789012345678` | `1.123456789012345678` | `10.987654321098765432` | `10.987654321098765432` |
| 2026-05-02 | `2.000000000000000001` | `2.000000000000000001` | `20.000000000000000002` | `20.000000000000000002` |
| **Period** | `1.123456789012345678 + 2.000000000000000001` | **`3.123456789012345679`** | `10.987654321098765432 + 20.000000000000000002` | **`30.987654321098765434`** |

The currencies sort as `EUR`, `USD`. Therefore an unfiltered daily-cost API
request defaults to the EUR series, while `?currency=USD` returns the USD
series. The two series deliberately differ on every day and in the period total,
so a currency-filter leak cannot satisfy the coupled exact assertions.

## Coupled test

These rows and totals are coupled to
`TestOfflineE2EFocusCSVMulticurrency` in `cmd/costroid/e2e_test.go`. Update the
test and this arithmetic together whenever the fixture changes.
