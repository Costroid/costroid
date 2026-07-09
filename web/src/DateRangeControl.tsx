// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { Range } from "./range";

type DateRangeControlProps = {
  range: Range;
  onChange: (range: Range) => void;
};

export default function DateRangeControl({
  range,
  onChange,
}: DateRangeControlProps) {
  const isReversed =
    range.start !== "" && range.end !== "" && range.start > range.end;

  return (
    <div className="date-range-control" role="group" aria-label="Date range">
      <label>
        <span>Start date</span>
        <input
          type="date"
          value={range.start}
          onChange={(event) =>
            onChange({ start: event.currentTarget.value, end: range.end })
          }
        />
      </label>
      <label>
        <span>End date</span>
        <input
          type="date"
          value={range.end}
          onChange={(event) =>
            onChange({ start: range.start, end: event.currentTarget.value })
          }
        />
      </label>
      <button type="button" onClick={() => onChange({ start: "", end: "" })}>
        All time
      </button>
      {isReversed && (
        <p className="date-range-hint">Start date is after end date.</p>
      )}
    </div>
  );
}
