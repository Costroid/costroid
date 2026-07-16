// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { Range } from "./range";

// DemoPreset is a fixed, capture-pinned range offered in demo mode. The static
// demo has no backend, so it can only serve the ranges whose fixtures were
// captured — hence presets instead of free-form date inputs.
type DemoPreset = { id: string; label: string; start: string; end: string };

type DateRangeControlProps = {
  range: Range;
  onChange: (range: Range) => void;
  // When provided (demo mode only), render these preset buttons instead of the
  // free-form date inputs. Production leaves this undefined and is unchanged.
  presets?: DemoPreset[];
};

export default function DateRangeControl({
  range,
  onChange,
  presets,
}: DateRangeControlProps) {
  if (presets) {
    return (
      <div
        className="date-range-control date-range-presets"
        role="group"
        aria-label="Date range"
      >
        {presets.map((preset) => (
          <button
            key={preset.id}
            type="button"
            aria-pressed={
              range.start === preset.start && range.end === preset.end
            }
            onClick={() => onChange({ start: preset.start, end: preset.end })}
          >
            {preset.label}
          </button>
        ))}
      </div>
    );
  }

  const isReversed =
    range.start !== "" && range.end !== "" && range.start > range.end;

  return (
    <div className="date-range-control" role="group" aria-label="Date range">
      <label>
        <span>Start date</span>
        <input
          type="date"
          name="start-date"
          autoComplete="off"
          value={range.start}
          aria-describedby="date-range-hint"
          onChange={(event) =>
            onChange({ start: event.currentTarget.value, end: range.end })
          }
        />
      </label>
      <label>
        <span>End date</span>
        <input
          type="date"
          name="end-date"
          autoComplete="off"
          value={range.end}
          aria-describedby="date-range-hint"
          onChange={(event) =>
            onChange({ start: range.start, end: event.currentTarget.value })
          }
        />
      </label>
      <button type="button" onClick={() => onChange({ start: "", end: "" })}>
        All time
      </button>
      {/* Persistent live region: a hint that mounts already populated is
          unreliably announced, so the node stays and the text swaps. */}
      <p className="date-range-hint" id="date-range-hint" role="status">
        {isReversed ? "Start date is after end date." : ""}
      </p>
    </div>
  );
}
