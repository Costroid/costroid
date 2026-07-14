---
title: FOCUS coverage
description: Supported FOCUS versions, known-column counts, and the required columns Costroid validates.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Internal target

Costroid's internal model targets FOCUS 1.4. Every declared input version has a
version-specific transform into that 1.4-shaped model, and
`GET /api/v1/meta` reports `focusVersion` as `1.4`. See the
[metadata operation](/api/operations/getmeta/).

## Supported versions

The known-column count is the set of column names recognized for a declared
FOCUS version during strict CSV header validation.

| Declarable value | Known columns | Notes |
| --- | ---: | --- |
| `1.0` | 43 | Spec-conformant input only: timestamps use RFC3339 and an empty cell represents null. |
| `1.0r2` | 43 | Azure-declarable alias, column-identical to `1.0`; canonicalizes to `1.0`. It is not a FOCUS specification release and is accepted only as a `focus-csv --focus-version` input. |
| `1.1` | 50 | Spec-conformant input only. |
| `1.2` | 57 | Implemented transform to the internal 1.4 shape. |
| `1.3` | 65 | Implemented transform to the internal 1.4 shape. |
| `1.4` | 65 | Internal target version. |

The five FOCUS releases `1.0`, `1.1`, `1.2`, `1.3`, and `1.4` all have
implemented transforms. `1.0r2` is the only alias.

FOCUS 1.3 and 1.4 each have 65 known columns, but two names changed. The 1.4
set drops `ProviderName` and `PublisherName`, and adds
`CommitmentProgramEligibilityDetails` and `InvoiceDetailId`.

## Required columns

These 15 must-not-be-null columns are required:

1. `BilledCost`
2. `EffectiveCost`
3. `ListCost`
4. `ContractedCost`
5. `BillingCurrency`
6. `ChargeCategory`
7. `ChargePeriodStart`
8. `ChargePeriodEnd`
9. `BillingPeriodStart`
10. `BillingPeriodEnd`
11. `BillingAccountId`
12. `ServiceName`
13. `ServiceCategory`
14. `ServiceProviderName`
15. `InvoiceIssuerName`

A file declared as FOCUS 1.4 fails import when any of these columns is missing.
If another mandatory-but-nullable column is missing, Costroid emits a warning
instead of failing the import.

## Currency columns

`BillingCurrency` is required and must match the ISO 4217 three-letter uppercase
format `^[A-Z]{3}$`.

These optional multi-currency columns first appear in FOCUS 1.2:

- `PricingCurrency`
- `PricingCurrencyContractedUnitPrice`
- `PricingCurrencyEffectiveCost`
- `PricingCurrencyListUnitPrice`

FOCUS defines no exchange-rate column in any supported version. Costroid does
not convert across currencies; see the [multi-currency guide](/guides/multi-currency/).

## Up-conversion

Each input row is rewritten into Costroid's internal 1.4-shaped record before
typed validation and storage. Provider-proprietary `x_` columns are accepted at
the CSV boundary and dropped during this transform.

The `1.0`, `1.1`, and `1.2` transforms synthesize newer provider columns from
their older counterparts without inventing unrelated data. The `1.3` and `1.4`
paths pass their native successor columns through.
