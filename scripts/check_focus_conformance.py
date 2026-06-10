#!/usr/bin/env python3
"""Check a focus-validator console report against Costroid's known-failure allowlist.

Costroid's FOCUS export is conformant with FOCUS 1.3 (Milestone 6b): all
mandatory column-presence, type, allowed-value, nullability, provider, and
account checks pass, including the per-token PricingUnit/quantity representation
and the SkuPriceId-null row nulling. The only rules still expected to fail are
genuine defects in the shipped validator ruleset (model-1.3.0.1.json) that no
conformant producer can satisfy — most notably the cost = unit-price x quantity
checks, which the validator evaluates in zero-tolerance float64 even though
Costroid's decimal arithmetic is exact. They are reported upstream.

These are enumerated in scripts/focus_known_failures.txt, which is an
EXACT-MATCH contract, not a tolerance list:

  * every entry pins a rule id AND its expected violation count
    (`<rule-id> violations=<N>`), and a `report-fail-count = <N>` directive pins
    the report's total "Fail:" figure (which also covers the composite
    "AND failed" aggregate lines that mirror the root failures);
  * this checker fails on ANY deviation, in either direction — a failing rule
    not in the allowlist, a known rule failing with a different violation count
    (a new violating row hiding inside a known-defective rule is still a
    regression), a known rule that no longer fails (stale entry — remove it),
    or a changed report total. The allowlist can therefore never silently mask
    a regression and must be kept current.

Usage: check_focus_conformance.py <validator-report.txt> <allowlist.txt>
"""

import re
import sys
from pathlib import Path

# "❌ CAU-<Column>-<code>: FAIL  (violations=N, msg=...)"
FAIL_LINE = re.compile(r"❌\s*(?P<rule>[A-Za-z0-9._-]+):\s*FAIL\b.*?\(violations=(?P<count>\d+)")
# "Total: 764 | Pass: 333 | Fail: 9 | Skipped: 422"
SUMMARY_LINE = re.compile(
    r"Total:\s*\d+\s*\|\s*Pass:\s*\d+\s*\|\s*Fail:\s*(?P<fail>\d+)\s*\|\s*Skipped:\s*\d+"
)
ENTRY_LINE = re.compile(r"^(?P<rule>[A-Za-z0-9._-]+)\s+violations=(?P<count>\d+)$")
DIRECTIVE_LINE = re.compile(r"^report-fail-count\s*=\s*(?P<count>\d+)$")


def parse_allowlist(text: str) -> tuple[dict[str, int], int]:
    """Parse the allowlist into (rule -> expected violations, expected report Fail total).

    Raises ValueError on any malformed line, duplicate rule, or a missing or
    repeated report-fail-count directive — a mis-parsed allowlist must never
    degrade into a looser check.
    """
    expected: dict[str, int] = {}
    fail_count: int | None = None
    for lineno, raw in enumerate(text.splitlines(), start=1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        directive = DIRECTIVE_LINE.match(line)
        if directive:
            if fail_count is not None:
                raise ValueError(f"line {lineno}: duplicate report-fail-count directive")
            fail_count = int(directive.group("count"))
            continue
        entry = ENTRY_LINE.match(line)
        if not entry:
            raise ValueError(
                f"line {lineno}: malformed entry {line!r} — expected "
                "'<rule-id> violations=<N>' or 'report-fail-count = <N>'"
            )
        rule = entry.group("rule")
        if rule in expected:
            raise ValueError(f"line {lineno}: duplicate rule {rule}")
        expected[rule] = int(entry.group("count"))
    if fail_count is None:
        raise ValueError("missing 'report-fail-count = <N>' directive")
    return expected, fail_count


def parse_fail_lines(report_text: str) -> tuple[dict[str, int], list[str], list[str]]:
    """Split the report's FAIL lines into root failures, cascades, and junk.

    Returns (root rule -> violations, cascade rule ids, unparseable lines).
    Cascades are the composite "AND failed" aggregates and "[Upstream dependency
    failed]" lines — consequences of the root failures, not independent problems;
    they are not fingerprinted individually but ARE counted into the report's
    "Fail:" total, which the allowlist pins.
    """
    root: dict[str, int] = {}
    cascades: list[str] = []
    bad: list[str] = []
    for line in report_text.splitlines():
        if "❌" not in line:
            continue
        match = FAIL_LINE.search(line)
        if not match:
            bad.append(line.strip())
            continue
        rule = match.group("rule")
        if "AND failed" in line or "Upstream dependency" in line:
            cascades.append(rule)
            continue
        if rule in root:
            bad.append(line.strip())  # duplicate result line: format anomaly
            continue
        root[rule] = int(match.group("count"))
    return root, cascades, bad


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    report = Path(sys.argv[1]).read_text(encoding="utf-8")
    try:
        expected, expected_fail_count = parse_allowlist(
            Path(sys.argv[2]).read_text(encoding="utf-8")
        )
    except ValueError as err:
        print(f"FAIL: malformed allowlist {sys.argv[2]}: {err}")
        return 2

    # Guard against a vacuous pass: a crashed validator (Python traceback) or a changed
    # console format yields zero parsed FAIL lines, which must not read as "conformant".
    # A real run always carries the results header and at least one rule-result line.
    has_summary = any(
        line.startswith("=== Validation Results") for line in report.splitlines()
    )
    has_rule_lines = any(mark in report for mark in ("✅", "❌"))
    if not has_summary or not has_rule_lines:
        print("FAIL: validator report carries no results summary / rule lines —")
        print("the validator likely crashed or changed its output format; refusing a")
        print("vacuous pass. Report tail:")
        for line in report.splitlines()[-15:]:
            print(f"  | {line}")
        return 1

    for line in report.splitlines():
        if line.startswith("=== Validation Results") or line.lstrip().startswith("Total:"):
            print(line.strip())

    failures, cascades, bad = parse_fail_lines(report)
    if bad:
        print("\nFAIL: unparseable or duplicate FAIL lines — the validator's console")
        print("format has drifted; refusing to check against the allowlist blind:")
        for line in bad:
            print(f"  | {line}")
        return 1

    summary = SUMMARY_LINE.search(report)
    if not summary:
        print("\nFAIL: no parseable 'Total: .. | Fail: ..' summary line in the report.")
        return 1
    summary_fail = int(summary.group("fail"))
    if summary_fail != len(failures) + len(cascades):
        print(
            f"\nFAIL: the summary counts {summary_fail} failing rules but "
            f"{len(failures) + len(cascades)} FAIL lines were parsed "
            f"({len(failures)} root + {len(cascades)} cascade) — console format drift;"
        )
        print("refusing to check against the allowlist blind.")
        return 1

    # Exact-match contract: any deviation from the pinned set, in either
    # direction, is a failure.
    problems: list[str] = []
    for rule in sorted(set(failures) - set(expected)):
        problems.append(
            f"NEW failing rule (not in allowlist): {rule} "
            f"(violations={failures[rule]})"
        )
    for rule in sorted(set(expected) - set(failures)):
        problems.append(
            f"allowlisted rule did NOT fail: {rule} — stale entry; remove it (and "
            "re-pin report-fail-count) so the allowlist stays current"
        )
    for rule in sorted(set(expected) & set(failures)):
        if failures[rule] != expected[rule]:
            problems.append(
                f"violation-count drift on {rule}: expected "
                f"violations={expected[rule]}, got violations={failures[rule]} — "
                "a new violating row inside a known-defective rule is still a regression"
            )
    if summary_fail != expected_fail_count:
        problems.append(
            f"report 'Fail:' total is {summary_fail}, allowlist pins "
            f"report-fail-count = {expected_fail_count} "
            f"(parsed {len(failures)} root + {len(cascades)} cascade lines)"
        )

    if problems:
        print("\nFAIL: FOCUS conformance deviates from the exact known-failure set:")
        for problem in problems:
            print(f"  - {problem}")
        print(
            "\nFix the export if this is a regression; if the deviation is "
            "intentional and documented (e.g. fixture rows changed, a validator "
            "defect was fixed upstream), update scripts/focus_known_failures.txt "
            "— entries, counts, and report-fail-count — in the same change."
        )
        return 1

    print(
        "\nOK: FOCUS conformance holds — the failure set exactly matches the "
        "documented validator defects."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
