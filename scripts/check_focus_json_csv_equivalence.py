#!/usr/bin/env python3
"""Assert a FOCUS JSON export carries byte-equivalent data to the CSV export.

The CSV export is what `scripts/focus_conformance.sh` validates against the FOCUS
1.3 ruleset. The JSON export (`export --format json`, a `{focusVersion, rows}`
envelope) is a different serialization of the SAME rows — the validator reads
tabular CSV, not the envelope, so rather than re-validating we prove the two
serializations are equivalent: same row count, same columns, same values
(decimal-normalized so the JSON number `0.3` and the CSV string `0.30` compare
equal, and booleans/nulls/strings compare by normalized form). If they match, the
JSON export is exactly as FOCUS-conformant as the validated CSV export.

Usage: check_focus_json_csv_equivalence.py <export.json> <export.csv>
"""

import csv
import json
import sys
from decimal import Decimal, InvalidOperation
from pathlib import Path


def normalize(value) -> str:
    """A canonical comparable form: decimals compare by value (0.30 == 0.3), bools
    lowercase, null/missing as empty, everything else as its trimmed string."""
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        # JSON numbers — compare by exact decimal value (via the repr, not binary float).
        try:
            return str(Decimal(repr(value)).normalize())
        except InvalidOperation:
            return str(value)
    text = str(value).strip()
    if text == "":
        return ""
    try:
        return str(Decimal(text).normalize())
    except InvalidOperation:
        return text.lower() if text in ("true", "false") else text


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    envelope = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    rows_json = envelope["rows"] if isinstance(envelope, dict) else envelope
    with open(sys.argv[2], newline="", encoding="utf-8") as handle:
        rows_csv = list(csv.DictReader(handle))

    if len(rows_json) != len(rows_csv):
        print(f"FAIL: row count differs — JSON {len(rows_json)} vs CSV {len(rows_csv)}")
        return 1
    if not rows_json:
        print("FAIL: zero rows — refusing a vacuous equivalence pass")
        return 1

    problems = []
    for i, (rj, rc) in enumerate(zip(rows_json, rows_csv)):
        keys = set(rj) | set(rc)
        for key in sorted(keys):
            nj = normalize(rj.get(key))
            nc = normalize(rc.get(key))
            if nj != nc:
                problems.append(f"row {i} column {key!r}: JSON {nj!r} != CSV {nc!r}")

    if problems:
        print("FAIL: JSON and CSV exports diverge:")
        for problem in problems[:40]:
            print(f"  - {problem}")
        if len(problems) > 40:
            print(f"  ... and {len(problems) - 40} more")
        return 1

    print(f"OK: JSON and CSV exports are row-equivalent ({len(rows_json)} rows).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
