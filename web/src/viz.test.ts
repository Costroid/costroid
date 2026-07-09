// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import {
  SERIES_SLOTS,
  compactAxisLabel,
  segmentPath,
  serviceColor,
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
