// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import {
  HEIGHT,
  MARGIN,
  SERIES_SLOTS,
  WIDTH,
  capLabelPositions,
  compareDecimalMagnitude,
  compactAxisLabel,
  lineChartGeometry,
  segmentPath,
  serviceColor,
  sparklineGeometry,
  sparklinePoints,
  sumIntegerStrings,
  yTicks,
} from "./viz";

describe("serviceColor", () => {
  it("is deterministic for a given name", () => {
    expect(serviceColor("AWS Lambda")).toBe(serviceColor("AWS Lambda"));
  });

  it("maps every name onto a slot in 1..SERIES_SLOTS", () => {
    const names = [
      "AWS Lambda",
      "Amazon Elastic Compute Cloud",
      "OpenAI API",
      "claude-opus-4-6",
      "gpt-4o",
      "x",
      "",
      "service-with-a-very-long-name-that-still-hashes",
    ];
    for (const name of names) {
      const match = /^var\(--viz-series-(\d)\)$/.exec(serviceColor(name));
      expect(match).toBeTruthy();
      const slot = Number(match![1]);
      expect(slot).toBeGreaterThanOrEqual(1);
      expect(slot).toBeLessThanOrEqual(SERIES_SLOTS);
    }
  });
});

describe("yTicks", () => {
  it("returns a zero tick when max is non-positive", () => {
    expect(yTicks(0)).toEqual([{ value: 0, label: "0" }]);
    expect(yTicks(-1)).toEqual([{ value: 0, label: "0" }]);
  });

  it("produces nice steps without float-noise labels", () => {
    const ticks = yTicks(0.3);
    expect(ticks.map((t) => t.label)).toEqual(["0.0", "0.1", "0.2", "0.3"]);
    for (const t of ticks) {
      expect(t.label).not.toMatch(/00000/);
      expect(t.label).not.toBe("0.30000000000000004");
    }
  });

  it("covers the max with a nice ceiling", () => {
    const ticks = yTicks(9.3618);
    const top = ticks[ticks.length - 1];
    expect(top.value).toBeGreaterThanOrEqual(9.3618);
    expect(ticks[0]).toEqual({ value: 0, label: expect.any(String) });
  });
});

describe("segmentPath", () => {
  it("draws a square-top rectangle when roundedTop is false", () => {
    expect(segmentPath(10, 20, 24, 40, false)).toBe("M10,20 h24 v40 h-24 Z");
  });

  it("draws rounded top corners when roundedTop is true", () => {
    const d = segmentPath(10, 20, 24, 40, true);
    expect(d).toContain(" a");
    expect(d.startsWith("M10,")).toBe(true);
    expect(d.endsWith(" Z")).toBe(true);
    // Radius is min(4, h, w/2) = 4.
    expect(d).toContain("a4,4");
  });

  it("clamps the radius when the segment is short", () => {
    const d = segmentPath(0, 0, 24, 2, true);
    // r = min(4, 2, 12) = 2
    expect(d).toContain("a2,2");
  });
});

describe("compactAxisLabel", () => {
  it("formats large magnitudes with SI suffixes", () => {
    expect(compactAxisLabel(0)).toBe("0");
    expect(compactAxisLabel(5e17)).toBe("500P");
    expect(compactAxisLabel(1e18)).toBe("1000P");
    expect(compactAxisLabel(1.5e18)).toBe("1500P");
    expect(compactAxisLabel(300e6)).toBe("300M");
    expect(compactAxisLabel(500000)).toBe("500k");
    expect(compactAxisLabel(1.2e18)).toBe("1200P");
    expect(compactAxisLabel(1_500_000)).toBe("1.5M");
    expect(compactAxisLabel(2500)).toBe("2.5k");
  });
});

describe("sumIntegerStrings", () => {
  it("sums integer decimal strings with BigInt precision", () => {
    expect(
      sumIntegerStrings(["1234567890125856789", "9876543210987654321"]),
    ).toBe("11111111101113511110");
  });

  it("returns null when any member is not an integer string", () => {
    expect(sumIntegerStrings(["1", "1.5"])).toBeNull();
    expect(sumIntegerStrings(["1e3"])).toBeNull();
    expect(sumIntegerStrings(["-1"])).toBeNull();
  });

  it("returns 0 for an empty list", () => {
    expect(sumIntegerStrings([])).toBe("0");
  });
});

describe("capLabelPositions", () => {
  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const estWidth = (s: string) => Math.max(7, s.length * 6.2);

  it("clamps a long last-day total so the label stays inside the plot", () => {
    const long = "123456789012"; // 12 digits
    const totals = Array.from({ length: 10 }, (_, i) => (i === 9 ? long : "1"));
    const positions = capLabelPositions(totals);
    const p = positions[9];
    expect(p).not.toBeNull();
    const width = estWidth(long);
    expect(p! + width / 2).toBeLessThanOrEqual(WIDTH - MARGIN.right);
  });

  it("clamps a long first-day total so the label stays inside the plot", () => {
    const long = "123456789012";
    const totals = Array.from({ length: 10 }, (_, i) => (i === 0 ? long : "1"));
    const positions = capLabelPositions(totals);
    const p = positions[0];
    expect(p).not.toBeNull();
    const width = estWidth(long);
    expect(p! - width / 2).toBeGreaterThanOrEqual(MARGIN.left);
  });

  it("thins colliding dense long labels and keeps kept labels non-overlapping", () => {
    const long = "1234567890"; // 10 digits → width ≈ 62
    const totals = Array.from({ length: 40 }, () => long);
    const positions = capLabelPositions(totals);
    const kept = positions
      .map((p, i) => (p === null ? null : { p, i }))
      .filter((x): x is { p: number; i: number } => x !== null);
    expect(kept.length).toBeGreaterThan(0);
    expect(kept.length).toBeLessThan(totals.length);
    // At least one null between first and last if multiple kept.
    expect(positions.some((p) => p === null)).toBe(true);

    const width = estWidth(long);
    for (let k = 1; k < kept.length; k++) {
      const prevRight = kept[k - 1].p + width / 2;
      const left = kept[k].p - width / 2;
      expect(left).toBeGreaterThanOrEqual(prevRight + 4);
    }
  });

  it("returns null for a null total", () => {
    expect(capLabelPositions([null, "10"])[0]).toBeNull();
  });

  it("returns null when a single label is wider than the plot", () => {
    // plotWidth ≈ 584; need length * 6.2 > 584 → length > 94.2
    const huge = "9".repeat(100);
    expect(estWidth(huge)).toBeGreaterThan(plotWidth);
    expect(capLabelPositions([huge])).toEqual([null]);
  });

  it("returns the band center for a short mid-plot total (unclamped)", () => {
    const n = 10;
    const totals = Array.from({ length: n }, () => "1");
    const positions = capLabelPositions(totals);
    const mid = 4;
    const band = plotWidth / n;
    const expectedCenter = MARGIN.left + mid * band + band / 2;
    expect(positions[mid]).toBeCloseTo(expectedCenter, 10);
  });
});

describe("compareDecimalMagnitude", () => {
  it("orders 18-digit near-equal magnitudes that Number() misorders", () => {
    // These are equal as IEEE doubles (beyond mantissa) but a > b as decimals.
    const a = "9007199254740993"; // 2^53 + 1
    const b = "9007199254740992"; // 2^53
    expect(Number(a) === Number(b)).toBe(true); // proves the Number trap
    expect(compareDecimalMagnitude(a, b)).toBeGreaterThan(0);
    expect(compareDecimalMagnitude(b, a)).toBeLessThan(0);
  });

  it("treats signed values by absolute magnitude", () => {
    expect(compareDecimalMagnitude("-100.5", "50")).toBeGreaterThan(0);
    expect(compareDecimalMagnitude("-3", "3")).toBe(0);
    expect(compareDecimalMagnitude("+12.0", "12")).toBe(0);
  });

  it("is stable for equal magnitudes", () => {
    expect(compareDecimalMagnitude("1.2300", "1.23")).toBe(0);
    expect(
      compareDecimalMagnitude("0.000000000000000001", "0.000000000000000001"),
    ).toBe(0);
  });

  it("orders by integer-digit count first", () => {
    expect(compareDecimalMagnitude("99.999", "100")).toBeLessThan(0);
    expect(compareDecimalMagnitude("1000", "999.999")).toBeGreaterThan(0);
  });
});

describe("sparklinePoints", () => {
  it("skips uncovered days with a gap (no interpolation)", () => {
    // values: day0=1, day1=null (gap), day2=3
    const segs = sparklinePoints([1, null, 3], 100, 20);
    expect(segs.length).toBe(2);
    expect(segs[0]).toHaveLength(1);
    expect(segs[1]).toHaveLength(1);
    // First point at x=0, second at x=100 (index 2 of 0..2).
    expect(segs[0][0].x).toBe(0);
    expect(segs[1][0].x).toBe(100);
    // y: min=1 max=3 → v=1 at bottom (y=20), v=3 at top (y=0)
    expect(segs[0][0].y).toBe(20);
    expect(segs[1][0].y).toBe(0);
  });

  it("returns one contiguous segment when every day is covered", () => {
    const segs = sparklinePoints([1, 2, 3], 100, 10);
    expect(segs).toHaveLength(1);
    expect(segs[0]).toHaveLength(3);
    expect(segs[0][0].x).toBe(0);
    expect(segs[0][1].x).toBe(50);
    expect(segs[0][2].x).toBe(100);
  });

  it("returns empty when all values are null", () => {
    expect(sparklinePoints([null, null], 100, 10)).toEqual([]);
  });
});

describe("sparklineGeometry", () => {
  it("turns a single point into a dot coordinate, not a bare move-to path", () => {
    expect(sparklineGeometry([5], 100, 20)).toEqual({
      paths: [],
      dots: [{ x: 50, y: 20 }],
    });
  });

  it("keeps multi-point segments as SVG paths", () => {
    expect(sparklineGeometry([1, 2], 100, 20)).toEqual({
      paths: ["M0.00,20.00 L100.00,0.00"],
      dots: [],
    });
  });
});

describe("lineChartGeometry", () => {
  // Shared frame: WIDTH 640, HEIGHT 220, MARGIN {top:20,right:8,bottom:24,
  // left:48} → plot 584×176, baseline y=196.
  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;

  it("maps values onto the 0..top plot scale within the chart frame", () => {
    const g = lineChartGeometry([0, 10], 10);
    // v=0 sits on the baseline, v=top on the plot's top edge.
    expect(g.paths).toEqual([
      `M${MARGIN.left.toFixed(2)},${baseline.toFixed(2)} L${(MARGIN.left + plotWidth).toFixed(2)},${MARGIN.top.toFixed(2)}`,
    ]);
    expect(g.dots).toEqual([]);
    expect(g.xs).toEqual([MARGIN.left, MARGIN.left + plotWidth]);
  });

  it("opens a gap at uncovered days and renders singletons as dots", () => {
    const g = lineChartGeometry([5, null, 5, 10], 10);
    expect(g.dots).toHaveLength(1);
    expect(g.dots[0]).toEqual({ x: MARGIN.left, y: baseline - plotHeight / 2 });
    expect(g.paths).toHaveLength(1);
    expect(g.xs).toHaveLength(4);
  });

  it("centers a single-day series as one dot", () => {
    const g = lineChartGeometry([10], 10);
    expect(g.dots).toEqual([{ x: MARGIN.left + plotWidth / 2, y: MARGIN.top }]);
    expect(g.paths).toEqual([]);
  });

  it("returns xs but no geometry when top is not positive", () => {
    const g = lineChartGeometry([1, 2], 0);
    expect(g.paths).toEqual([]);
    expect(g.dots).toEqual([]);
    expect(g.xs).toHaveLength(2);
  });

  it("returns empty geometry for an empty series", () => {
    expect(lineChartGeometry([], 10)).toEqual({ paths: [], dots: [], xs: [] });
  });
});
