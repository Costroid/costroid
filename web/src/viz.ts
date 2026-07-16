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
      // Trim trailing zeros after a decimal point, preserving integer zeroes.
      const body = scaled.toFixed(decimals).replace(/\.0+$|(\.\d*?)0+$/, "$1");
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

/**
 * Sign-aware absolute magnitude compare for decimal STRINGS — positions /
 * ordering only. Never computes a numeric value (D23 untouched). Strips an
 * optional leading sign, compares integer-digit counts, then aligned
 * lexicographic bodies so near-equal 18-digit magnitudes that Number() would
 * misorder still rank correctly. Equal magnitudes return 0 (stable sort ok).
 */
export function compareDecimalMagnitude(a: string, b: string): number {
  const body = (s: string): string => {
    const t = s.trim();
    if (t.startsWith("-") || t.startsWith("+")) {
      return t.slice(1);
    }
    return t;
  };
  const ba = body(a);
  const bb = body(b);
  const intDigits = (s: string): number => {
    const dot = s.indexOf(".");
    return dot < 0 ? s.length : dot;
  };
  const ia = intDigits(ba);
  const ib = intDigits(bb);
  if (ia !== ib) {
    return ia - ib;
  }
  // Align fractional lengths so lexicographic compare is magnitude-correct.
  const fa = ba.includes(".") ? ba.length - ba.indexOf(".") - 1 : 0;
  const fb = bb.includes(".") ? bb.length - bb.indexOf(".") - 1 : 0;
  const pad = Math.max(fa, fb);
  const norm = (s: string, frac: number): string => {
    if (!s.includes(".")) {
      return s + (pad > 0 ? "." + "0".repeat(pad) : "");
    }
    return s + "0".repeat(pad - frac);
  };
  const na = norm(ba, fa);
  const nb = norm(bb, fb);
  if (na < nb) return -1;
  if (na > nb) return 1;
  return 0;
}

// Shared gap-aware segmentation: maps each non-null value through the given
// coordinate functions; null/non-finite entries split the polyline into
// separate segments (no interpolation across an uncovered day).
function pointSegments(
  values: (number | null)[],
  xOf: (i: number) => number,
  yOf: (v: number) => number,
): { x: number; y: number }[][] {
  const segments: { x: number; y: number }[][] = [];
  let current: { x: number; y: number }[] = [];
  for (let i = 0; i < values.length; i++) {
    const v = values[i];
    if (v === null || !Number.isFinite(v)) {
      if (current.length > 0) {
        segments.push(current);
        current = [];
      }
      continue;
    }
    current.push({ x: xOf(i), y: yOf(v) });
  }
  if (current.length > 0) {
    segments.push(current);
  }
  return segments;
}

/**
 * Sparkline polyline points for a series of numeric y-values (positions only).
 * Null entries are uncovered days: they open a gap (no interpolation across
 * the null). Returns an array of contiguous path segments, each a list of
 * {x,y} points in the given width×height box (y grows downward).
 */
export function sparklinePoints(
  values: (number | null)[],
  width: number,
  height: number,
): { x: number; y: number }[][] {
  if (values.length === 0) {
    return [];
  }
  const nums = values.filter(
    (v): v is number => v !== null && Number.isFinite(v),
  );
  if (nums.length === 0) {
    return [];
  }
  const min = Math.min(...nums);
  const max = Math.max(...nums);
  const span = max - min || 1;
  const n = values.length;
  const xOf = (i: number) => (n === 1 ? width / 2 : (i / (n - 1)) * width);
  const yOf = (v: number) => height - ((v - min) / span) * height;
  return pointSegments(values, xOf, yOf);
}

export type SparklineGeometry = {
  paths: string[];
  dots: { x: number; y: number }[];
};

export type LineChartGeometry = SparklineGeometry & {
  /** Per-day center x in chart coordinates (for date labels etc.). */
  xs: number[];
};

/**
 * Full line-chart geometry on the shared chart frame (WIDTH/HEIGHT/MARGIN):
 * maps per-day values onto the plot area with a 0..top y-scale (the yTicks
 * domain, so grid lines and the line share one scale). Null entries are
 * uncovered days and open a gap; multi-point segments become paths and
 * singletons become dots (a bare SVG move-to paints no pixels). Positions
 * only — the caller renders values as strings elsewhere (D40).
 */
export function lineChartGeometry(
  values: (number | null)[],
  top: number,
): LineChartGeometry {
  const n = values.length;
  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const xOf = (i: number) =>
    n === 1
      ? MARGIN.left + plotWidth / 2
      : MARGIN.left + (i / (n - 1)) * plotWidth;
  const xs = Array.from({ length: n }, (_, i) => xOf(i));
  const geometry: LineChartGeometry = { paths: [], dots: [], xs };
  if (n === 0 || !(top > 0)) {
    return geometry;
  }
  const yOf = (v: number) => MARGIN.top + plotHeight - (v / top) * plotHeight;
  for (const segment of pointSegments(values, xOf, yOf)) {
    if (segment.length === 1) {
      geometry.dots.push(segment[0]);
      continue;
    }
    geometry.paths.push(
      segment
        .map(
          (point, index) =>
            `${index === 0 ? "M" : "L"}${point.x.toFixed(2)},${point.y.toFixed(2)}`,
        )
        .join(" "),
    );
  }
  return geometry;
}

/**
 * Converts sparkline points into visible SVG geometry. Multi-point segments
 * become paths; singleton segments become dots because a bare SVG move-to
 * command paints no pixels.
 */
export function sparklineGeometry(
  values: (number | null)[],
  width: number,
  height: number,
): SparklineGeometry {
  const geometry: SparklineGeometry = { paths: [], dots: [] };
  for (const segment of sparklinePoints(values, width, height)) {
    if (segment.length === 1) {
      geometry.dots.push(segment[0]);
      continue;
    }
    geometry.paths.push(
      segment
        .map(
          (point, index) =>
            `${index === 0 ? "M" : "L"}${point.x.toFixed(2)},${point.y.toFixed(2)}`,
        )
        .join(" "),
    );
  }
  return geometry;
}

/**
 * Cap-label x-positions for a per-day bar chart. Returns one entry per day:
 * the clamped center-x at which to anchor (textAnchor="middle") that day's
 * cap label, or null to omit it (no value, too wide for the plot, or it would
 * collide with the previously placed label). Positions only — the caller
 * renders the SAME strings it passed in (display-formatted money for
 * costs, verbatim counts for tokens). Uses the shared WIDTH/MARGIN so
 * a cap never clips the viewBox edge. ~6.2px/char matches the 11px
 * tabular-nums `.viz-cap` glyph advance.
 */
export function capLabelPositions(
  totals: (string | null)[],
): (number | null)[] {
  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const band = plotWidth / totals.length;
  let previousCapRight = -Infinity;
  return totals.map((total, i) => {
    if (total === null) return null;
    const width = Math.max(7, total.length * 6.2);
    if (width > plotWidth) return null;
    const center = MARGIN.left + i * band + band / 2;
    const x = Math.max(
      MARGIN.left + width / 2,
      Math.min(WIDTH - MARGIN.right - width / 2, center),
    );
    const left = x - width / 2;
    if (left < previousCapRight + 4) return null;
    previousCapRight = x + width / 2;
    return x;
  });
}
