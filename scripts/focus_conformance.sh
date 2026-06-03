#!/usr/bin/env bash
# Run the official FOCUS validator against Costroid's export of the committed
# fixtures and check the result against the documented known-failure allowlist.
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
HOME="$home" USERPROFILE="" CLAUDE_CONFIG_DIR="" ANTHROPIC_API_KEY="" \
  "$bin" export --format csv > "$export_csv"

echo "==> Running focus-validator (FOCUS 1.3, offline)"
# The validator reads a CWD-relative currency_codes.csv, so run from its package root.
site_packages="$("$py" -c 'import os, focus_validator; print(os.path.dirname(os.path.dirname(focus_validator.__file__)))')"
report="$workdir/report.txt"
( cd "$site_packages" && "$py" -m focus_validator.main \
    --data-file "$export_csv" \
    --validate-version 1.3 \
    --block-download \
    --output-type console ) > "$report" 2>&1 || true

echo "==> Checking against allowlist"
"$py" "$repo_root/scripts/check_focus_conformance.py" \
  "$report" "$repo_root/scripts/focus_known_failures.txt"
