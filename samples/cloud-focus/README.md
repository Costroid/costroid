# `samples/cloud-focus/` — synthetic AWS Bedrock FOCUS v1.2 export (pack b)

**SYNTHETIC data — NO real billing data, NO real account identity, NO profile NAMES
(Cardinal Rule R4).** Every value here is hand-authored to the published FOCUS 1.2 spec
shape. Nothing came from a real cloud bill, a real AWS account, or any user's export.

This is the **cloud/API cost-lane** demo input. `costroid import` ingests it into the
`cloud_api` lane of the unified FOCUS 1.3 ledger.

## Files

| File | Purpose |
|---|---|
| `aws-focus-v12.csv` | A complete FOCUS 1.2 document (full mandatory column set, mirroring `fixtures/focus/v1.2/synthetic-aws-v12-full.csv`) of four synthetic Amazon Bedrock rows: Claude Sonnet + Claude Opus, input + output meters. Each carries a **bounded** `x_InferenceProfileId` (an opaque `aip-…` id — **never** a profile NAME, R4). |
| `aws-focus-v12.csv.sha256` | Integrity sidecar (`sha256sum` of the CSV). |

## How the demo uses it

```bash
costroid import --format focus-csv --version auto --out csv samples/cloud-focus/aws-focus-v12.csv
```

→ 4 `cloud_api`-lane FOCUS 1.3 rows, **source-priced** (`BilledCost` is authoritative;
`x_Estimated = false`), total **9.60 USD**. The bounded `x_InferenceProfileId` is carried for
Bedrock workload attribution; any provider-specific free-text columns (e.g. an inference-profile
NAME, `x_ServiceCode`, `x_UsageType`) would be **dropped** by the importer (R4 — none are present
here).

## Notes on shape

- **Numeric columns:** `PricingQuantity` is a Decimal (`200000.0`) — the FOCUS 1.2 validator's
  `PricingQuantity MUST be Decimal` rule. `ConsumedQuantity` is an integer (`200000`) — the
  Costroid importer parses it as a token count (`u64`). Both are valid token quantities; they
  differ only in serialization so the file is **both importable and 1.3-validatable**.
- **Cost columns** (`BilledCost`/`EffectiveCost`/`ListCost`/`ContractedCost`) are equal and in
  `USD` — the source-priced bridge is lossless.
- This pack is validated by the **dedicated `samples/` leg** in `scripts/focus_conformance.sh`:
  imported to a FOCUS 1.3 ledger and validated (subset of the documented validator defects) with
  a row-count guard (4 rows). The byte-exact **v1.2 input-validation** EXACT-match contract is
  covered by `fixtures/focus/v1.2/synthetic-aws-v12-full.csv` (the CI fixture), not this demo pack.

## How to regenerate

This file is hand-authored (it represents a *foreign* AWS export, not Costroid output). To edit
it, change the rows by hand, then regenerate the integrity sidecar:

```bash
cd samples/cloud-focus && sha256sum aws-focus-v12.csv > aws-focus-v12.csv.sha256
```
