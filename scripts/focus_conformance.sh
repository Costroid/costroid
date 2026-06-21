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

echo "==> Building costroid (with the power feature for the local-inference bench row)"
# The `power` feature only ADDS the `bench` subcommand + links costroid-power; it changes
# neither `export` nor `import` output (byte-identical), so building it for the whole script is
# safe and lets the merged-ledger leg below include a real `local_inference` row.
cargo build -q -p costroid --features power
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

# Sibling of validate_csv for the v1.2 INPUT leg: validate a COMPLETE FOCUS 1.2
# document against the vendored 1.2.0.1 ruleset (offline) and check the report
# against the v1.2 known-failure allowlist. EXACT-match (no --subset): this is a
# complete document, so its per-rule counts and report total are pinned. $1=csv
# $2=label.
validate_csv_v12() {
  local data_file="$1" label="$2"
  local report="$workdir/report-$label.txt"
  ( cd "$site_packages" && "$py" -m focus_validator.main \
      --data-file "$data_file" \
      --validate-version 1.2 \
      --rule-set-path "$repo_root/scripts/focus-ruleset-1.2" \
      --block-download \
      --output-type console ) > "$report" 2>&1 || true
  "$py" "$repo_root/scripts/check_focus_conformance.py" \
    "$report" "$repo_root/scripts/focus_known_failures_v12.txt"
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

# Merged-ledger leg (M2 T10 — the deciding test in script form): a SINGLE FOCUS ledger built
# from BOTH the developer-tool export AND the imported AWS Bedrock cloud rows must validate as
# a subset of the documented defects — proving the two lanes' rows share one schema (identical
# headers, so the union is a well-formed FOCUS CSV) and the union is 1.3-conformant. The
# lane-SEPARATION invariant (totals never cross-summed; only grand_total crosses) is proven in
# the Rust `merged_dev_tool_and_cloud_ledger_keeps_lanes_separate` test.
# M3: a `local_inference` row from `costroid bench` (estimated mode — no subprocess, no
# hardware) so the merged ledger below spans ALL THREE lanes (developer_tool + cloud_api +
# local_inference). Estimated mode emits exactly one row; it carries the §6.4 local economics
# columns + the measured/estimated stamp.
echo "==> Generating a local_inference row (costroid bench, estimated mode)"
local_csv="$workdir/local-inference.csv"
"$bin" bench --out csv > "$local_csv"
got_local=$(($(wc -l < "$local_csv") - 1))
if [ "$got_local" -ne 1 ]; then
  echo "FAIL: costroid bench emitted $got_local data rows, expected 1" >&2
  exit 1
fi

echo "==> Merged-ledger leg: developer-tool + imported cloud + local-inference rows validate"
merged_csv="$workdir/merged-ledger.csv"
cp "$export_csv" "$merged_csv"          # header + developer_tool rows
tail -n +2 "$workdir/v12-aws.csv" >> "$merged_csv"   # append the cloud_api rows (skip header)
tail -n +2 "$local_csv" >> "$merged_csv"             # append the local_inference row (skip header)
validate_csv "$merged_csv" "merged-ledger" --subset

# v1.2 INPUT-validation leg (T9): unlike the round-trip legs above (which validate
# the 1.3 OUTPUT of importing a metadata-SUBSET fixture), this validates a COMPLETE
# synthetic FOCUS 1.2 document — the full mandatory column set — directly against the
# vendored 1.2.0.1 ruleset under an EXACT-match contract. It is validation-only (never
# imported), so its committed known-failure list (focus_known_failures_v12.txt) pins the
# single residual ruleset defect (the contradictory unconditional InvoiceId-C-004/005-C
# pair) exactly. This is the leg the M1 READMEs called a fast-follow.
echo "==> v1.2 input leg: validating a complete FOCUS 1.2 document (offline, exact-match)"
validate_csv_v12 "$v12_dir/synthetic-aws-v12-full.csv" "v12-input-full"

# Dedicated samples/ leg (M6 T1 — §6.7): the curated demo datasets under samples/ (distinct
# from the CI fixtures/ above) must ALSO round-trip to a schema-valid FOCUS 1.3 ledger, with a
# ROW-COUNT GUARD on every lane so an empty/short export can't pass vacuously. This is the leg
# the README/`make demo` reads, so it is validated separately from the merged-ledger leg above.
# Three lanes, all synthetic + (local) ESTIMATED:
#   (a) developer_tool — the synthetic Claude/Codex logs under samples/local-logs/
#   (b) cloud_api       — the synthetic AWS Bedrock FOCUS v1.2 export (imported to 1.3)
#   (c) local_inference — the deterministic gemma4 bench rows (estimated; SOURCE_DATE_EPOCH pinned)
echo "==> samples/ leg: validating the curated demo datasets (FOCUS 1.3, offline)"
samples_dir="$repo_root/samples"

# Integrity first (M6 T1 L1): the committed samples carry .sha256 sidecars — verify them so a
# drifted sample (or a hand-edited cloud CSV / bench JSON) fails CLOSED here, not silently. Each
# sidecar holds a bare filename, so `sha256sum -c` runs from the sidecar's own directory.
echo "==> samples/ integrity: verifying committed .sha256 sidecars"
while IFS= read -r sidecar; do
  ( cd "$(dirname "$sidecar")" && sha256sum -c "$(basename "$sidecar")" ) || {
    echo "FAIL: samples integrity check failed for $sidecar (a committed sample drifted from its sidecar)" >&2
    exit 1
  }
done < <(find "$samples_dir" -name '*.sha256' | sort)

# (a) developer_tool: export the synthetic logs. Point ONLY at samples/local-logs (neutralize
# every other discovery override) so the export can only contain the committed sample logs.
samples_dev_csv="$workdir/samples-dev.csv"
HOME="$workdir/nohome" USERPROFILE="" ANTHROPIC_API_KEY="" CURSOR_DATA_DIR="" XDG_STATE_HOME="" \
  CLAUDE_CONFIG_DIR="$samples_dir/local-logs/claude" \
  CODEX_HOME="$samples_dir/local-logs/codex" \
  "$bin" export --format csv > "$samples_dev_csv"
samples_dev_rows=$(($(wc -l < "$samples_dev_csv") - 1))
# Row-count guard: the synthetic logs produce exactly 14 developer_tool rows (3 Claude turns +
# 2 Codex turns, expanded per token-meter). A silent discovery miss (0 rows) fails loudly here.
if [ "$samples_dev_rows" -ne 14 ]; then
  echo "FAIL: samples/local-logs export produced $samples_dev_rows rows, expected 14" >&2
  exit 1
fi
validate_csv "$samples_dev_csv" "samples-dev" --subset

# (b) cloud_api: import the synthetic AWS Bedrock FOCUS v1.2 export to a 1.3 ledger
# (import_and_validate already enforces its own row-count guard — 4 rows).
import_and_validate "$samples_dir/cloud-focus/aws-focus-v12.csv" focus-csv "samples-cloud" 4

# (c) local_inference: regenerate the deterministic bench rows for the two committed benchmark
# models (SOURCE_DATE_EPOCH = the gemma4 manifest as_of, 2026-06-20, so the rows are byte-stable
# and equal to the committed samples/benchmark/*.bench.json — guarded byte-for-byte by the Rust
# samples_datasets_* test + the .sha256 sidecars). Estimated mode → exactly one row each.
samples_local_csv="$workdir/samples-local.csv"
first=1
for model in gemma-4-31b-dense gemma-4-26b-a4b; do
  row_csv="$workdir/samples-bench-$model.csv"
  SOURCE_DATE_EPOCH=1781913600 "$bin" bench \
    --model "$model" --tokens-in 2000 --tokens-out 18000 --out csv > "$row_csv"
  if [ "$first" -eq 1 ]; then
    cat "$row_csv" > "$samples_local_csv"      # header + first local row
    first=0
  else
    tail -n +2 "$row_csv" >> "$samples_local_csv"   # append the second local row (skip header)
  fi
done
samples_local_rows=$(($(wc -l < "$samples_local_csv") - 1))
if [ "$samples_local_rows" -ne 2 ]; then
  echo "FAIL: samples/benchmark bench rows produced $samples_local_rows, expected 2" >&2
  exit 1
fi

# Merged samples ledger: all three lanes in one FOCUS 1.3 document must validate (subset of the
# documented defects), with a row-count guard on the union (14 dev + 4 cloud + 2 local = 20).
echo "==> samples/ merged leg: developer_tool + cloud_api + local_inference (all three lanes)"
samples_merged_csv="$workdir/samples-merged.csv"
cp "$samples_dev_csv" "$samples_merged_csv"                       # header + developer_tool rows
tail -n +2 "$workdir/samples-cloud.csv" >> "$samples_merged_csv" # append cloud_api rows
tail -n +2 "$samples_local_csv" >> "$samples_merged_csv"         # append local_inference rows
samples_merged_rows=$(($(wc -l < "$samples_merged_csv") - 1))
if [ "$samples_merged_rows" -ne 20 ]; then
  echo "FAIL: merged samples ledger has $samples_merged_rows rows, expected 20 (14+4+2)" >&2
  exit 1
fi
validate_csv "$samples_merged_csv" "samples-merged" --subset

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
