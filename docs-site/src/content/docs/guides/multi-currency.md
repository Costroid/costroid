---
title: Multi-currency
description: How Costroid keeps billing currencies separate, selects a series, and never converts between them.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Exact decimal values

All cost and quantity values in the API are decimal strings, never floating-point
numbers, so no precision is lost. Costroid stores monetary and quantity columns
as exact `DECIMAL(38,18)` values and computes comparison deltas with exact
decimal subtraction.

A missing `BillingCurrency` fails the affected billing period. Costroid never
assumes USD.

## One series per currency

Cost read endpoints keep billing currencies separate. A requested window can
contain several currencies without failing: Costroid selects one currency's
series and never adds unlike currencies together.

| Endpoint | Currency behavior |
| --- | --- |
| `/api/v1/costs/daily` | Returns the selected `currency` and a sorted `currencies[]` containing every billing currency in the requested range. |
| `/api/v1/costs/summary` | Returns the selected `currency` and a sorted `currencies[]` from the current window. The selection is pinned to both comparison windows. |
| `/api/v1/anomalies` | Scores one selected `currency`. The response identifies that currency but does not contain `currencies[]`. |
| `/api/v1/unit-economics/daily` | Merges one selected cost `currency` with the business metric and returns the sorted cost-side `currencies[]`. |

See the [API reference](/api/) for the complete response schemas. Token,
business-metric, and usage-metric endpoints are quantity-based and carry no
billing currency, so this selection model does not apply to them.

## Default selection

For daily costs, summary, and unit economics:

- Omit `currency` to select the alphabetically first billing currency in the
  requested window.
- Read `currencies[]` for every code present, sorted ascending. Use this array as
  the source for a currency selector.
- Pass `currency=XXX` to pin one series. The value must match `^[A-Z]{3}$` and
  therefore be uppercase.
- A valid code with no rows returns `200` with an empty or zero series while
  echoing the requested code.
- An empty range returns `currency: ""` and `currencies: []`.

For a summary, the selected currency is pinned to both the current window and
the preceding comparison window. A currency present only in the preceding
window cannot silently replace the current-window selection.

## Anomaly selection

When `currency` is omitted from an anomaly request, Costroid first chooses the
alphabetically first billing currency in the requested window. Only when that
window is empty does it fall back to the alphabetically first currency in the
available history up to the requested end. Detection then scores that one
currency's history.

## No cross-currency conversion

Costroid does no cross-currency conversion. There is no reporting currency and
no total across currencies. Conversion remains deferred until a named
exchange-rate source exists; FOCUS defines no exchange-rate column.

Report each billing currency separately, or normalize currencies upstream
before ingest.
