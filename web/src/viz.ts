// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// A service's color is a deterministic function of its name alone
// (an FNV-1a-style hash onto the validated palette — "style" because it
// hashes UTF-16 code units via charCodeAt, whereas canonical FNV-1a is
// defined over octets), so it never shifts when other services appear or
// disappear across ingests, reloads, or date ranges.
// Distinct services can hash to the same slot and then share a color —
// an accepted trade-off of a fixed 8-color palette.
export const SERIES_SLOTS = 8;

export function serviceColor(name: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    hash ^= name.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `var(--viz-series-${((hash >>> 0) % SERIES_SLOTS) + 1})`;
}

// Chart geometry (SVG user units).
export const WIDTH = 640;
export const HEIGHT = 220;
export const MARGIN = { top: 20, right: 8, bottom: 24, left: 48 };
export const MAX_BAR_WIDTH = 24;
export const SEGMENT_GAP = 2;

/**
 * Y-axis ticks from 0 to a "nice" ceiling of max. Values are computed as
 * step multiples and labels formatted to the step's decimal places, so
 * labels never show float-accumulation noise ("0.30000000000000004").
 */
export function yTicks(max: number): { value: number; label: string }[] {
  if (max <= 0) {
    return [{ value: 0, label: "0" }];
  }
  const rough = max / 4;
  const exp = Math.floor(Math.log10(rough));
  const mult = [1, 2, 5, 10].find((m) => m * 10 ** exp >= rough) ?? 10;
  const step = mult * 10 ** exp;
  const decimals = Math.max(0, mult === 10 ? -(exp + 1) : -exp);
  const count = Math.ceil(max / step - 1e-9);
  return Array.from({ length: count + 1 }, (_, i) => ({
    value: i * step,
    label: (i * step).toFixed(decimals),
  }));
}

/** SVG path for a bar segment; only the topmost gets rounded top corners. */
export function segmentPath(
  x: number,
  y: number,
  w: number,
  h: number,
  roundedTop: boolean,
): string {
  if (!roundedTop) {
    return `M${x},${y} h${w} v${h} h${-w} Z`;
  }
  const r = Math.min(4, h, w / 2);
  return `M${x},${y + r} a${r},${r} 0 0 1 ${r},${-r} h${w - 2 * r} a${r},${r} 0 0 1 ${r},${r} v${h - r} h${-w} Z`;
}

/**
 * Compact SI-suffix label for large axis-scale values (token counts can
 * reach ~1e18). Displayed data values stay exact decimal strings — this
 * is for the y-axis scale guide only.
 */
export function compactAxisLabel(value: number): string {
  if (value === 0) {
    return "0";
  }
  const abs = Math.abs(value);
  const sign = value < 0 ? "-" : "";
  const suffixes: { threshold: number; suffix: string }[] = [
    { threshold: 1e15, suffix: "P" },
    { threshold: 1e12, suffix: "T" },
    { threshold: 1e9, suffix: "G" },
    { threshold: 1e6, suffix: "M" },
    { threshold: 1e3, suffix: "k" },
  ];
  for (const { threshold, suffix } of suffixes) {
    if (abs >= threshold) {
      const scaled = abs / threshold;
      const decimals = scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2;
      // Trim trailing zeros after toFixed.
      const body = scaled.toFixed(decimals).replace(/\.?0+$/, "");
      return `${sign}${body}${suffix}`;
    }
  }
  // Small values: mirror yTicks-style fixed decimals without float noise.
  if (Number.isInteger(value)) {
    return String(value);
  }
  return value.toPrecision(3).replace(/\.?0+e/, "e");
}

/**
 * Sum integer decimal-string quantities with BigInt. Returns null if any
 * member is not a non-negative integer string (so BigInt never throws).
 */
export function sumIntegerStrings(quantities: string[]): string | null {
  if (quantities.length === 0) {
    return "0";
  }
  if (!quantities.every((q) => /^\d+$/.test(q))) {
    return null;
  }
  let total = 0n;
  for (const q of quantities) {
    total += BigInt(q);
  }
  return total.toString();
}
