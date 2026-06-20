# Vendored FOCUS 1.2.0.1 validator ruleset

`model-1.2.0.1.json` is the official FOCUS 1.2.0.1 machine-readable model/ruleset,
vendored verbatim so a **complete** FOCUS 1.2 document can be validated fully offline
(`--block-download`) against a wheel-version-independent ruleset.

> **What `scripts/focus_conformance.sh` validates against this ruleset:** the v1.2
> INPUT leg (T9) validates a **complete** synthetic FOCUS 1.2 document
> (`fixtures/focus/v1.2/synthetic-aws-v12-full.csv`) — the full mandatory column set —
> directly against this 1.2.0.1 ruleset, offline (`--validate-version 1.2
> --block-download`), under an EXACT-match contract against the pinned 1.2
> known-failure list (`scripts/focus_known_failures_v12.txt`). Separately, the
> round-trip legs validate the FOCUS **1.3 OUTPUT** of Costroid's importer (the
> v1.2-in → v1.3-out bridge) against the sibling `scripts/focus-ruleset/` 1.3 ruleset.
> The round-trip legs do **not** input-validate the v1.2 *subset* fixtures: those are a
> deliberate metadata subset (only the columns Costroid's importer reads), **not
> complete FOCUS 1.2 documents**, so they legitimately fail full-1.2 column-presence
> rules (`ChargeDescription`, `InvoiceId`, `InvoiceIssuerName`, …) and stay for the
> importer round-trip only. This ruleset is also kept for CI-independence from the
> wheel's bundled model and for validating a **real** complete AWS export locally (the
> `COSTROID_REAL_AWS_FOCUS` leg).

- **Source:** the FOCUS specification's published release assets —
  <https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/releases>
  (release tag `v1.2`, asset `model-1.2.0.1.json`; fetched 2026-06-19).
  `Details.ModelVersion` = `1.2.0.1`, `Details.FOCUSVersion` = `1.2`.
- **SHA-256:** `639b302ace9edd05922e3d15fcedf62723c92e7cf25e0a7a6684dd4fd4076fec`
- **License:** CC-BY-4.0 (FOCUS Series, Joint Development Foundation Projects, LLC —
  see the FOCUS_Spec repository's `license.md`). A data artifact for the dev/CI
  conformance gate only — it is **not** compiled into, linked by, or shipped with any
  Costroid binary, so it is outside the crate-dependency license policy (same posture
  as the sibling `scripts/focus-ruleset/` 1.3.0.1 ruleset).
- **Why vendored:** Costroid's importer accepts **FOCUS 1.2 input** (the v1.2-in /
  v1.3-out bridge, M1). The PyPI `focus-validator` wheel *does* bundle the 1.2.0.1
  model, so a 1.2 document can be validated without `--rule-set-path` too; this vendored
  copy pins an exact, wheel-version-independent 1.2 ruleset so an offline check can't
  silently drift when the wheel updates. To validate a complete 1.2 document against it:
  `python -m focus_validator.main --data-file <file> --validate-version 1.2
  --rule-set-path scripts/focus-ruleset-1.2 --block-download`.
- **Updating:** replace the file with a newer published `model-1.2.x.json`; the
  full-document input-validation leg (T9) is wired, so re-check its pinned 1.2 known
  failures (`scripts/focus_known_failures_v12.txt`) in the same change.
