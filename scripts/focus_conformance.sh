#!/usr/bin/env bash
# Run the official FOCUS validator against Costroid's export of the committed
# fixtures and check the result against the documented known-failure allowlist
# (scripts/focus_known_failures.txt). The allowlist is an EXACT-MATCH contract:
# it pins each expected failure's rule id AND violation count plus the report's
# total "Fail:" figure, and the checker fails on any deviation in either
# direction — a new failure (even inside an already-known-defective rule), a
# changed count, or an allowlisted entry that stopped failing (stale entry).
#
# Runs fully offline (--block-download uses the validator's bundled FOCUS 1.3
# ruleset); Costroid itself makes no network calls. The validator is an external
# Python dev/CI tool, not a Costroid dependency.
#
# Requirements: a Python (>=3.12) with `focus-validator` installed, exposed via
#   FOCUS_VALIDATOR_PYTHON (default: python3)
# and a built `costroid` binary (this script builds it).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
py="${FOCUS_VALIDATOR_PYTHON:-python3}"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

echo "==> Building costroid"
cargo build -q -p costroid
bin="$repo_root/target/debug/costroid"

echo "==> Exporting FOCUS CSV from committed fixtures"
# Export priced AND unpriced rows in one CSV: the unpriced fixtures (models not in
# the pricing table) exercise the SkuPriceId-null nullability rules, while the
# priced fixture (claude-sonnet-4-6) exercises the per-token PricingUnit/quantity
# representation and the (allowlisted, defective) cost = unit-price x quantity check.
home="$workdir/home"
mkdir -p "$home/.claude/projects/fixture" "$home/.claude/projects/fixture-priced" \
  "$home/.claude/projects/fixture-dated" "$home/.codex/sessions/fixture"
cp "$repo_root/fixtures/claude-code/project-transcript.jsonl" "$home/.claude/projects/fixture/"
cp "$repo_root/fixtures/claude-code/project-transcript-priced.jsonl" "$home/.claude/projects/fixture-priced/"
# Dated snapshot (claude-haiku-4-5-20251001): priced via the suffix-tolerant base
# alias, so its priced FOCUS columns (non-null SkuPriceId referencing the base,
# PricingQuantity, per-token unit price) are validated end-to-end. x_Model stays
# the dated id; SkuPriceId references the base claude-haiku-4-5 rate.
cp "$repo_root/fixtures/claude-code/project-transcript-dated.jsonl" "$home/.claude/projects/fixture-dated/"
cp "$repo_root/fixtures/codex/rollout.jsonl" "$home/.codex/sessions/fixture/"
export_csv="$workdir/focus.csv"
# Neutralize EVERY env override the discovery code honors (CODEX_HOME, CURSOR_DATA_DIR,
# XDG_STATE_HOME too) so the validated CSV can only contain the committed fixtures,
# never the developer's real logs.
HOME="$home" USERPROFILE="" CLAUDE_CONFIG_DIR="" ANTHROPIC_API_KEY="" \
  CODEX_HOME="" CURSOR_DATA_DIR="" XDG_STATE_HOME="" \
  "$bin" export --format csv > "$export_csv"

# The validator reads a CWD-relative currency_codes.csv, so run from its package root.
# The 1.3.0.1 ruleset is VENDORED at scripts/focus-ruleset/ (see its README): the PyPI
# focus-validator wheel bundles only the 1.2.0.1 model, so without --rule-set-path a
# --block-download run cannot validate 1.3 at all — it crashes with UnsupportedVersion
# (which the checker below now correctly treats as a FAILURE, never a vacuous pass).
site_packages="$("$py" -c 'import os, focus_validator; print(os.path.dirname(os.path.dirname(focus_validator.__file__)))')"

# Validate a FOCUS 1.3 CSV against the vendored ruleset (offline) and check the report
# against the known-failure allowlist. $1=csv $2=label $3=optional checker flags
# (`--subset` for the import leg, whose smaller synthetic row set has its own per-rule
# counts but must add NO new failing rule beyond the documented validator defects).
validate_csv() {
  local data_file="$1" label="$2" checker_flags="${3:-}"
  local report="$workdir/report-$label.txt"
  ( cd "$site_packages" && "$py" -m focus_validator.main \
      --data-file "$data_file" \
      --validate-version 1.3 \
      --rule-set-path "$repo_root/scripts/focus-ruleset" \
      --block-download \
      --output-type console ) > "$report" 2>&1 || true
  # shellcheck disable=SC2086 # checker_flags is intentionally word-split (may be empty).
  "$py" "$repo_root/scripts/check_focus_conformance.py" $checker_flags \
    "$report" "$repo_root/scripts/focus_known_failures.txt"
}

echo "==> CSV leg: validating the developer-tool export (FOCUS 1.3, offline, exact-match)"
validate_csv "$export_csv" "csv-devtool"

echo "==> JSON leg: export --format json is row-equivalent to the validated CSV export"
export_json="$workdir/focus.json"
HOME="$home" USERPROFILE="" CLAUDE_CONFIG_DIR="" ANTHROPIC_API_KEY="" \
  CODEX_HOME="" CURSOR_DATA_DIR="" XDG_STATE_HOME="" \
  "$bin" export --format json > "$export_json"
"$py" "$repo_root/scripts/check_focus_json_csv_equivalence.py" "$export_json" "$export_csv"

# v1.2-in -> v1.3-out round trip: `costroid import` re-emits a synthetic foreign FOCUS
# 1.2 export as Costroid's 1.3 ledger; the re-emitted output must validate with NO new
# failing rule beyond the documented defects (subset contract — the synthetic row set is
# smaller/different than the dev-tool fixtures, so its per-rule counts legitimately differ).
# A v1.2-in -> v1.3-out conversion is a STRUCTURAL UPGRADE, never byte-identical (1.3 adds
# columns; Costroid synthesizes derived fields). So this gate proves the output is 1.3-CONFORMANT
# (subset of the documented defects); the value-preserving semantic net (cost preserved, lane,
# model, x_FocusInputVersion, sidechain) lives in the Rust tests + the committed round-trip golden.
echo "==> Synthetic v1.2 round-trip leg: v1.2-in -> v1.3-out validates (subset of defects)"
v12_dir="$repo_root/fixtures/focus/v1.2"
import_and_validate() {  # $1=fixture $2=input-format $3=label $4=expected-data-rows
  local out_csv="$workdir/$3.csv"
  "$bin" import --format "$2" --version auto --out csv "$1" > "$out_csv"
  # Row-count guard: a silent import drop (0 rows) would VACUOUSLY pass the subset check
  # (no rows -> no failures). Pin the expected data-row count (CSV = header + N rows) so an
  # import that quietly emits nothing fails loudly here instead.
  local got_rows
  got_rows=$(($(wc -l < "$out_csv") - 1))
  if [ "$got_rows" -ne "$4" ]; then
    echo "FAIL: $3 import produced $got_rows data rows, expected $4" >&2
    exit 1
  fi
  validate_csv "$out_csv" "$3" --subset
}
import_and_validate "$v12_dir/synthetic-v12-marked.csv"   focus-csv  "v12-marked"   2
import_and_validate "$v12_dir/synthetic-v12-unmarked.csv" focus-csv  "v12-unmarked" 2
import_and_validate "$v12_dir/synthetic-v12.json"         focus-json "v12-json"     2
import_and_validate "$v12_dir/synthetic-aws-v12.csv"      focus-csv  "v12-aws"      2

echo "==> Real AWS v1.2 leg (present, SKIPPED unless COSTROID_REAL_AWS_FOCUS is set)"
if [ -n "${COSTROID_REAL_AWS_FOCUS:-}" ] && [ -f "${COSTROID_REAL_AWS_FOCUS}" ]; then
  echo "    running against \$COSTROID_REAL_AWS_FOCUS=$COSTROID_REAL_AWS_FOCUS"
  import_and_validate "$COSTROID_REAL_AWS_FOCUS" focus-csv "v12-real-aws"
else
  echo "    SKIPPED (C1): a real AWS Data Exports FOCUS sample can never enter the repo"
  echo "    (privacy + offline CI). The synthetic AWS-shaped leg above covers the column"
  echo "    mapping + the x_ServiceCode/x_UsageType drop; to TRUE it against a real export,"
  echo "    run locally with COSTROID_REAL_AWS_FOCUS=/path/to/real-focus-1.2.csv."
fi
