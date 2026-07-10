<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

# aws-focus-anomaly sample export

A gzipped FOCUS 1.2 export (same column layout as
`testdata/aws-focus-1.2/sample-export.csv.gz`) that exercises query-time cost
anomaly detection. It carries a single service — **Amazon Elastic Compute
Cloud**, provider **AWS** (via `PublisherName`) — over 18 consecutive days
(`2026-06-01` … `2026-06-18`), one `BilledCost` row per day, single currency
`USD`. Because there is exactly one service, the per-day **total** series and the
**key** series are identical, so every flag appears under both scopes.

## Daily `BilledCost` series (the observations)

| Day | Date       | BilledCost | Note              |
| --- | ---------- | ---------- | ----------------- |
| 1   | 2026-06-01 | 98         | baseline          |
| 2   | 2026-06-02 | 102        | baseline          |
| 3   | 2026-06-03 | 98         | baseline          |
| 4   | 2026-06-04 | 102        | baseline          |
| 5   | 2026-06-05 | 98         | baseline          |
| 6   | 2026-06-06 | 102        | baseline          |
| 7   | 2026-06-07 | 98         | baseline          |
| 8   | 2026-06-08 | 102        | baseline          |
| 9   | 2026-06-09 | 98         | baseline          |
| 10  | 2026-06-10 | 102        | baseline          |
| 11  | 2026-06-11 | 98         | mundane (scored)  |
| 12  | 2026-06-12 | 102        | mundane (scored)  |
| 13  | 2026-06-13 | 98         | mundane (scored)  |
| 14  | 2026-06-14 | 102        | mundane (scored)  |
| 15  | 2026-06-15 | **200**    | **SPIKE**         |
| 16  | 2026-06-16 | 100        | mundane (scored)  |
| 17  | 2026-06-17 | **40**     | **DIP**           |
| 18  | 2026-06-18 | 100        | mundane (scored)  |

The baseline oscillates tightly around 100 (±2), so every mundane day's deviation
(≤4) stays **below the relative floor** (`0.1 × median ≈ 10`) and is never flagged
— even at the odd-count windows where the MAD collapses to 0. Days 1–10 are never
scored (fewer than 10 prior observations); the spike and dip both sit on/after the
11th observed day, so each has ≥10 baseline days.

## Detector constants

`k = 3`, `windowDays = 30`, `minObservations = 10`,
`consistencyConstant = 1.4826`, `relativeFloor = 0.1`. Flag iff
`deviation > k × mad × 1.4826` **and** `deviation ≥ 0.1 × |median|` (strict `>`).

## Expected flags (both scopes on each day)

**Spike — 2026-06-15 (increase).** Baseline = days 1–14 = seven 98s + seven 102s
(14 observations, an even-count median):

- median = (98 + 102) / 2 = **100**
- deviations are all 2 → mad = **2**
- scaledMad = 2 × 1.4826 = **2.9652**; threshold = 3 × 2.9652 = **8.8956**
- observed = 200 → deviation = |200 − 100| = **100** > 8.8956 and ≥ 10 → **flag**

**Dip — 2026-06-17 (decrease).** Baseline = days 1–16 = seven 98s + seven 102s +
one 200 (day 15) + one 100 (day 16) (16 observations, even-count median):

- sorted middle pair is (100, 102) → median = **101**
- deviations sorted are eight 1s, seven 3s, one 99 → mad = (1 + 3) / 2 = **2**
- scaledMad = **2.9652**; threshold = **8.8956**
- observed = 40 → deviation = |40 − 101| = **61** > 8.8956 and ≥ 10.1 → **flag**

Days 11–14, 16, and 18 are scored but not flagged (their deviations are below the
threshold or, where the MAD is 0, below the relative floor). No other day flags.

## Regenerating

The file is generated from the 1.2 sample's column template; only the date,
`BilledCost`/`EffectiveCost`/`ListCost`/`ContractedCost`, `ServiceName`, and
`ProviderName`/`PublisherName` vary per row. Keep this table and the detector math
in sync with `cmd/costroid` `TestOfflineE2EAnomalies`.
