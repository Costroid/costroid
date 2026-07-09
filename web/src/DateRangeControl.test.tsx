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
      <DateRangeControl range={{ start: "", end: "" }} onChange={onChange} />,
    );

    fireEvent.change(screen.getByLabelText(/start date/i), {
      target: { value: "2026-05-01" },
    });
    expect(onChange).toHaveBeenLastCalledWith({
      start: "2026-05-01",
      end: "",
    });

    fireEvent.click(screen.getByRole("button", { name: "All time" }));
    expect(onChange).toHaveBeenLastCalledWith({ start: "", end: "" });
  });
});
