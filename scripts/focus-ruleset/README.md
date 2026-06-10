# Vendored FOCUS 1.3.0.1 validator ruleset

`model-1.3.0.1.json` is the official FOCUS 1.3.0.1 machine-readable model/ruleset,
vendored verbatim so `scripts/focus_conformance.sh` can validate against FOCUS 1.3
**fully offline** (`--block-download`).

- **Source:** the FOCUS specification's published release assets —
  <https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/releases> (asset
  `model-1.3.0.1.json`; fetched 2026-06-10).
- **License:** CC-BY-4.0 (FOCUS Series, Joint Development Foundation Projects, LLC —
  see the FOCUS_Spec repository's `license.md`). A data artifact for the dev/CI
  conformance gate only — it is **not** compiled into, linked by, or shipped with any
  Costroid binary, so it is outside the crate-dependency license policy.
- **Why vendored:** the PyPI `focus-validator` wheel bundles only the 1.2.0.1 model;
  validating 1.3 otherwise requires a network download at validation time, which the
  offline gate forbids. `focus_conformance.sh` points the validator here via
  `--rule-set-path`.
- **Updating:** replace the file with a newer published `model-1.3.x.json` (or a 1.4
  model when Costroid moves to 1.4) and re-check `scripts/focus_known_failures.txt` —
  the three allowlisted entries are defects in THIS ruleset revision and may be fixed
  in later ones.
