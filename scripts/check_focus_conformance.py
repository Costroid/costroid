#!/usr/bin/env python3
"""Check a focus-validator console report against Costroid's known-failure allowlist.

Costroid's FOCUS export is structurally conformant with FOCUS 1.3 (Milestone 6a):
all mandatory column-presence, type, allowed-value, provider, and account checks
pass. A small set of rules is expected to still fail, for two reasons:

  * Milestone 6b (cost-calculator conformance, not yet done): the
    ListCost/ContractedCost arithmetic checks, the PricingUnit UnitFormat, and
    the SkuPriceId-null row quantity-nulling.
  * Defects in the shipped validator ruleset (model-1.3.0.1.json) that no
    conformant producer can satisfy.

Both are enumerated in scripts/focus_known_failures.txt. This checker fails only
on *new* (unexpected) rule failures, so it guards against conformance
regressions (e.g. a dropped mandatory column) while tolerating the documented,
deferred items.

Usage: check_focus_conformance.py <validator-report.txt> <allowlist.txt>
"""

import sys
from pathlib import Path


def root_failures(report_text: str) -> set[str]:
    """Rule ids that failed for a real, first-order reason.

    Excludes composite "AND failed" aggregates and "[Upstream dependency failed]"
    cascades, which are consequences of the root failures rather than independent
    problems.
    """
    failures: set[str] = set()
    for line in report_text.splitlines():
        if "FAIL" not in line or "❌" not in line:
            continue
        if "AND failed" in line or "Upstream dependency" in line:
            continue
        # Format: "❌ CAU-<Column>-<code>: FAIL  (violations=N, msg=...)"
        marker = "❌"
        rest = line.split(marker, 1)[1].strip()
        rule_id = rest.split(":", 1)[0].strip()
        if rule_id:
            failures.add(rule_id)
    return failures


def allowlist(text: str) -> set[str]:
    ids: set[str] = set()
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if line:
            ids.add(line)
    return ids


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    report = Path(sys.argv[1]).read_text(encoding="utf-8")
    allowed = allowlist(Path(sys.argv[2]).read_text(encoding="utf-8"))
    failures = root_failures(report)

    unexpected = sorted(failures - allowed)

    for line in report.splitlines():
        if line.startswith("=== Validation Results") or line.lstrip().startswith("Total:"):
            print(line.strip())

    # Note: the allowlist intentionally spans both priced and unpriced rows, so
    # not every entry fires on every fixture; that is expected, not a problem.

    if unexpected:
        print("\nFAIL: unexpected FOCUS conformance failures (not in allowlist):")
        for rule_id in unexpected:
            print(f"  - {rule_id}")
        print(
            "\nThese are new conformance regressions. Fix the export, or — if "
            "intentional — add them to scripts/focus_known_failures.txt with a "
            "reason."
        )
        return 1

    print("\nOK: FOCUS conformance holds — only documented, deferred failures remain.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
