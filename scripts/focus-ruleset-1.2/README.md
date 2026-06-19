# Vendored FOCUS 1.2.0.1 validator ruleset

`model-1.2.0.1.json` is the official FOCUS 1.2.0.1 machine-readable model/ruleset,
vendored verbatim so `scripts/focus_conformance.sh` can validate **FOCUS 1.2
input** fully offline (`--block-download`) independently of whichever
`focus-validator` wheel version a CI box happens to have installed.

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
  model, so the synthetic v1.2 input also validates without `--rule-set-path`; this
  vendored copy pins an exact, wheel-version-independent 1.2 ruleset so the CI gate
  can't silently drift when the wheel updates. `focus_conformance.sh` points the
  validator here via `--rule-set-path` for the v1.2-input leg.
- **Scope note:** Costroid's *output* is FOCUS **1.3** — the primary deciding test
  validates the 1.3 export against the sibling `scripts/focus-ruleset/`
  (`model-1.3.0.1.json`). This 1.2 ruleset only validates the *imported input* side.
- **Updating:** replace the file with a newer published `model-1.2.x.json` and
  re-check `scripts/focus_known_failures.txt`.
