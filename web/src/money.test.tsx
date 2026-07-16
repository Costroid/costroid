// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { formatMoney, Money } from "./money";

describe("formatMoney", () => {
  it("rounds values at or above one to 2 fraction digits with grouping", () => {
    expect(formatMoney("964050.632653589793238462")).toBe("964,050.63");
    expect(formatMoney("577334.682653589793238462")).toBe("577,334.68");
    expect(formatMoney("1.005")).toBe("1.01");
    expect(formatMoney("1234567.891")).toBe("1,234,567.89");
  });

  it("pads values at or above one to exactly 2 fraction digits", () => {
    expect(formatMoney("192367.59")).toBe("192,367.59");
    expect(formatMoney("5.1")).toBe("5.10");
    expect(formatMoney("7")).toBe("7.00");
    expect(formatMoney("28874.8")).toBe("28,874.80");
    // Sub-one values keep significant digits without padding.
    expect(formatMoney("0.5")).toBe("0.5");
  });

  it("keeps 4 significant digits below one", () => {
    expect(formatMoney("0.044569658748120211")).toBe("0.04457");
    expect(formatMoney("0.1896")).toBe("0.1896");
    expect(formatMoney("0.123456")).toBe("0.1235");
    // Tiny values keep leading zeros + 4 significant digits, so a nonzero
    // value never displays as all zeros.
    expect(formatMoney("0.000000000000000123")).toBe("0.000000000000000123");
  });

  it("re-trims to 4 significant digits when a carry consumes a leading zero", () => {
    expect(formatMoney("0.099995")).toBe("0.1000");
    expect(formatMoney("0.09999999")).toBe("0.1000");
    expect(formatMoney("0.0099996")).toBe("0.01000");
    expect(formatMoney("-0.099995")).toBe("-0.1000");
  });

  it("re-rounds at 2 when a carry lifts a sub-one value to one", () => {
    expect(formatMoney("0.99999")).toBe("1.00");
    expect(formatMoney("-0.99999")).toBe("-1.00");
  });

  it("propagates a rounding carry through the integer part", () => {
    expect(formatMoney("999.995")).toBe("1,000.00");
    expect(formatMoney("9.999")).toBe("10.00");
  });

  it("rounds half-up away from zero", () => {
    expect(formatMoney("2.345")).toBe("2.35");
    expect(formatMoney("2.344")).toBe("2.34");
    expect(formatMoney("-2.345")).toBe("-2.35");
  });

  it("keeps zero unsigned at 0.00", () => {
    expect(formatMoney("0")).toBe("0.00");
    expect(formatMoney("0.000000")).toBe("0.00");
    expect(formatMoney("-0.00")).toBe("0.00");
    expect(formatMoney("0", { signed: true })).toBe("0.00");
  });

  it("prefixes + on nonzero positives only when signed", () => {
    expect(formatMoney("10", { signed: true })).toBe("+10.00");
    expect(formatMoney("-50", { signed: true })).toBe("-50.00");
    expect(formatMoney("1234.567", { signed: true })).toBe("+1,234.57");
    expect(formatMoney("10")).toBe("10.00");
  });

  it("normalizes leading integer zeros", () => {
    expect(formatMoney("007.50")).toBe("7.50");
  });

  it("passes non-decimal strings through unchanged", () => {
    expect(formatMoney("—")).toBe("—");
    expect(formatMoney("")).toBe("");
    expect(formatMoney("1e5")).toBe("1e5");
    expect(formatMoney("1.2.3")).toBe("1.2.3");
  });
});

describe("Money", () => {
  afterEach(cleanup);

  it("renders the display value with the exact wire value and currency in the title", () => {
    render(<Money value="964050.632653589793238462" currency="USD" />);
    const el = screen.getByText("964,050.63");
    expect(el.getAttribute("title")).toBe("964050.632653589793238462 USD");
  });

  it("omits the currency from the title when not given", () => {
    render(<Money value="5.1" />);
    expect(screen.getByText("5.10").getAttribute("title")).toBe("5.1");
  });

  it("passes signed through to the formatter", () => {
    render(<Money value="10" currency="EUR" signed />);
    expect(screen.getByText("+10.00").getAttribute("title")).toBe("10 EUR");
  });

  it("renders the em-dash placeholder for nullish and empty values", () => {
    const { container: nullish } = render(<Money value={null} />);
    expect(nullish.textContent).toBe("—");
    cleanup();
    const { container: undef } = render(<Money value={undefined} />);
    expect(undef.textContent).toBe("—");
    cleanup();
    const { container: empty } = render(<Money value="" />);
    expect(empty.textContent).toBe("—");
  });
});
