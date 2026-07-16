// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import DateRangeControl from "./DateRangeControl";

afterEach(() => {
  cleanup();
});

describe("DateRangeControl", () => {
  it("emits controlled range changes", () => {
    const onChange = vi.fn();

    render(
      <DateRangeControl
        range={{ start: "2026-05-01", end: "2026-05-31" }}
        onChange={onChange}
      />,
    );

    fireEvent.change(screen.getByLabelText(/start date/i), {
      target: { value: "2026-05-02" },
    });
    expect(onChange).toHaveBeenLastCalledWith({
      start: "2026-05-02",
      end: "2026-05-31",
    });

    fireEvent.click(screen.getByRole("button", { name: "All time" }));
    expect(onChange).toHaveBeenLastCalledWith({ start: "", end: "" });
  });

  it("shows a hint only when the start date is after the end date", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <DateRangeControl
        range={{ start: "2026-05-31", end: "2026-05-01" }}
        onChange={onChange}
      />,
    );

    expect(screen.getByText("Start date is after end date.")).toBeTruthy();

    rerender(
      <DateRangeControl
        range={{ start: "2026-05-01", end: "2026-05-31" }}
        onChange={onChange}
      />,
    );

    expect(screen.queryByText("Start date is after end date.")).toBeNull();
  });

  it("announces the reversed-range hint via a persistent live region", () => {
    const onChange = vi.fn();
    render(
      <DateRangeControl
        range={{ start: "2026-05-01", end: "2026-05-31" }}
        onChange={onChange}
      />,
    );

    // The status node exists (empty) BEFORE the hint appears — a live region
    // mounted already populated is unreliably announced.
    const status = screen.getByRole("status");
    expect(status.textContent).toBe("");
    expect(
      screen.getByLabelText(/start date/i).getAttribute("aria-describedby"),
    ).toBe("date-range-hint");
    expect(
      screen.getByLabelText(/end date/i).getAttribute("aria-describedby"),
    ).toBe("date-range-hint");
    expect(status.id).toBe("date-range-hint");
  });
});
