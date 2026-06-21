#!/usr/bin/env bash
# check_doc_stamps.sh — R8/R10 doc-honesty gate.
#
# Scans the docs that present an ESTIMATED/SYNTHETIC local-inference HERO figure (a cost, an energy,
# or a throughput number) and FAILS if such a doc presents a hero number WITHOUT the one canonical
# honesty stamp present in that document. Offline; pure text scan. Wired into the focus-conformance
# CI job alongside check_power_profiles.sh / check_pricing_snapshots.sh.
#
# ── Why presence-of-stamp, not number-classification ──────────────────────────────────────────────
# Classifying every numeric token as "hero vs structural" line-by-line is brittle and false-positive
# prone. Instead the gate is: if a scanned doc contains ANY hero-metric pattern (see HERO_PATTERN),
# it MUST also contain the canonical stamp at least once. That is conservative (it cannot flag a
# structural number as un-stamped) yet load-bearing (a writeup that prints local cost/energy/tok-s
# but forgets the "estimated — pending M3b measurement" stamp fails the build).
#
# ── The single source of truth for the stamp text ────────────────────────────────────────────────
# The canonical stamp is defined ONCE in Rust as `costroid_core::PENDING_M3B_STAMP`. This script reads
# that literal straight out of the source (crates/costroid-core/src/lib.rs) so the stamp text can
# never drift between the docs, the Rust const, and this gate. (A Rust test —
# `apps/cli/tests/docs_presence.rs::check_doc_stamps_script_uses_the_canonical_stamp` — also asserts
# this script references the const by name, closing the loop from the other side.)
#
# ── Allowlist (what is NOT a hero number) ─────────────────────────────────────────────────────────
# HERO_PATTERN deliberately matches ONLY local-inference hero metrics:
#   * a dollar figure tied to tokens         — "$X/Mtok", "X USD/token", "$0.52 per million tokens"
#   * an energy figure                        — "X Wh"
#   * a power figure                          — "X W" / "X watts"  (the package/wall draw)
#   * a throughput figure                     — "X tok/s" / "X tokens/s"
# It does NOT match (these are structural, never hero numbers, so they need no stamp):
#   * dates / ISO timestamps / `as_of` values        (e.g. 2026-06-20)
#   * version + section + milestone numbers           (v0.6.0, §3.2, M3b, FOCUS 1.3, MSRV 1.88)
#   * token-count INPUTS                               (2,000 in / 18,000 out / 20,000 total tokens)
#   * the dated electricity rate                       (0.16 USD/kWh — itself a stamped R8 assumption)
#   * raw "tokens/day" break-even volumes              (V* is a derived volume, not a $/energy hero)
# The electricity rate / power-watt lines DO appear, but the docs that print them already carry the
# stamp (so the presence rule passes); the allowlist note here documents WHY those are not the trigger.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Read the canonical stamp out of the Rust source (single source of truth). The const line is:
#   pub const PENDING_M3B_STAMP: &str = "estimated — pending M3b measurement";
stamp_src="crates/costroid-core/src/lib.rs"
if [ ! -f "$stamp_src" ]; then
  echo "FAIL: cannot find the canonical-stamp source $stamp_src" >&2
  exit 1
fi
STAMP="$(sed -n 's/.*pub const PENDING_M3B_STAMP: &str = "\(.*\)";.*/\1/p' "$stamp_src" | head -n1)"
if [ -z "$STAMP" ]; then
  echo "FAIL: could not extract PENDING_M3B_STAMP from $stamp_src" >&2
  exit 1
fi
echo "==> Canonical stamp (from $stamp_src): \"$STAMP\""

# The known list of docs that present estimated/synthetic local-inference hero figures. Scoped to a
# fixed list (not a glob) so a new doc is a deliberate addition here, and so prose-only docs are not
# scanned vacuously. The benchmark writeup (T8) is included pre-emptively but only checked if present.
DOCS=(
  "docs/methodology.md"
  "samples/README.md"
  "samples/benchmark/README.md"
  "docs/benchmark-gemma4-vs-cloud.md"   # T8 writeup — checked only if it exists
)

# A hero-metric pattern: a $-figure tied to tokens, an energy (Wh), a power (W/watts), or a
# throughput (tok/s). Case-insensitive, extended regex. NOTE: this is intentionally broad enough to
# trip on a real hero figure but, by design, the gate only USES it to decide "does this doc present a
# hero number at all" — it never tries to classify an individual number as un-stamped.
HERO_PATTERN='(\$[0-9][0-9.,]*[[:space:]]*(/|per)[[:space:]]*(M?tok|million[[:space:]]+tokens|token)|[0-9][0-9.,]*[[:space:]]*USD/(M?tok|token)|[0-9][0-9.,]*[[:space:]]*Wh\b|[0-9][0-9.,]*[[:space:]]*(W\b|watts\b)|[0-9][0-9.,]*[[:space:]]*tok(ens)?/s\b)'

status=0
scanned=0
for doc in "${DOCS[@]}"; do
  if [ ! -f "$doc" ]; then
    # Only the T8 writeup is allowed to be absent (it lands with the benchmark dataset). The core
    # methodology page + the sample READMEs MUST exist.
    case "$doc" in
      docs/benchmark-gemma4-vs-cloud.md)
        echo "  skip: $doc (not present yet — lands with the T8 benchmark writeup)"
        continue
        ;;
      *)
        echo "FAIL: required doc $doc is missing" >&2
        status=1
        continue
        ;;
    esac
  fi
  scanned=$((scanned + 1))
  if grep -E -i -q "$HERO_PATTERN" "$doc"; then
    # The doc presents a hero figure → it MUST carry the canonical stamp.
    if grep -F -q "$STAMP" "$doc"; then
      echo "  ok:   $doc (hero figure present + stamped)"
    else
      echo "FAIL: $doc presents a local-inference hero figure but is MISSING the canonical stamp:" >&2
      echo "        \"$STAMP\"" >&2
      grep -E -i -n "$HERO_PATTERN" "$doc" | head -n3 | sed 's/^/        > /' >&2
      status=1
    fi
  else
    echo "  ok:   $doc (no hero figure to stamp)"
  fi
done

if [ "$scanned" -eq 0 ]; then
  echo "FAIL: no docs were scanned — the DOCS list is empty or all missing" >&2
  exit 1
fi

if [ "$status" -ne 0 ]; then
  echo "==> Doc-stamp check FAILED — add the canonical stamp to the doc(s) above." >&2
  exit 1
fi
echo "==> All scanned docs carry the canonical honesty stamp."
