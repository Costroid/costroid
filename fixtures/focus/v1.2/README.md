# Synthetic FOCUS 1.2 import fixtures

**Synthetic data — NO real billing data, NO real account identity (Cardinal Rule R4).**
Every value here is hand-authored to the published FOCUS 1.2 spec shape; none of it
came from a real cloud bill, a real account, or any user's logs. These fixtures drive
Costroid's **v1.2-in → v1.3-out** FOCUS importer (`costroid-providers::focus_import`,
the M1 bridge) and its conformance gate.

## Files

| File | Purpose |
|---|---|
| `synthetic-v12-marked.csv` | Two AI-usage rows carrying the `x_FocusVersion=1.2` marker — the `detect_version()` *marked* case. |
| `synthetic-v12-unmarked.csv` | The same two rows with **no** version marker — the `detect_version()` *unmarked → default V1_2 + recorded caveat* case (real FOCUS exports carry the version in the export manifest, not per-row). |
| `synthetic-v12.json` | The JSON form of the marked rows — the `--format focus-json` import leg (a bare array of row objects, the common foreign-export shape). |
| `synthetic-aws-v12.csv` | AWS Data Exports-shaped rows ("FOCUS 1.2 with AWS columns"): `ProviderName=AWS`, `Amazon Bedrock`, a Bedrock SKU id, and the AWS-specific `x_ServiceCode`/`x_UsageType` extension columns populated on the first row. Proves the mapper **drops** provider-specific columns (they never reach the normalized output). No `x_FocusVersion` marker — mirroring a real AWS export, so detection defaults to V1_2 + caveat. |

## Conventions (synthetic, localized to `FocusV12Mapping`)

- **Model id** is carried in the FOCUS `SkuId` column (FOCUS 1.2 has no standard model
  column for AI usage). The mapper reads the model from `SkuId`; this is the one place
  the convention lives, so truing it to a real export's column shape is a one-file change.
- **Currency** is `USD` throughout, and the four cost columns
  (`BilledCost`/`EffectiveCost`/`ListCost`/`ContractedCost`) are equal — so the M1
  source-priced bridge (which carries one authoritative cost) is lossless on these
  fixtures. Differential/discounted cost columns and non-USD currencies are an M2
  cloud-lane concern.
- **`x_FocusVersion`** is Costroid's own detection marker (an `x_` extension column),
  not a standard FOCUS column. When absent, detection assumes V1_2 and records a caveat.

## Validation

These fixtures are a deliberate **metadata subset** of FOCUS 1.2 — only the columns
Costroid's importer reads (plus the user-specified set above). They are **not complete
FOCUS 1.2 documents**, so they intentionally fail full-1.2 column-presence rules
(`ChargeDescription`, `InvoiceId`, `InvoiceIssuerName`, `PricingQuantity`, …) and are
**not** input-validated by `scripts/focus_conformance.sh`.

What the conformance gate validates is the **1.3 OUTPUT** of importing them:
`scripts/focus_conformance.sh` runs `costroid import` on each fixture (v1.2-in →
v1.3-out) and validates the re-emitted FOCUS 1.3 against `scripts/focus-ruleset/` under
a **subset contract** (the import must add no new failing rule beyond the documented
1.3 validator defects). The value-preserving semantic net (cost preserved, lane,
model, `x_FocusInputVersion`, sidechain) lives in the Rust unit/integration tests
(`costroid-core` `v12_import_*`, the `costroid-core` round-trip golden
`tests/v12_round_trip_golden.rs` + `golden/` here, and `apps/cli/tests/import_cli.rs`).
A full-fixture 1.2 *input*-validation leg (against the vendored
`scripts/focus-ruleset-1.2/`) is a documented fast-follow.
