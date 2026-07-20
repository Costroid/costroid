// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { CostGroupBy } from "./api";
import type { components } from "./api/schema";

type DailyCosts = components["schemas"]["DailyCosts"];

// csvField encodes one field per RFC 4180: a field containing a comma, a
// double-quote, CR, or LF is wrapped in double-quotes with every inner
// double-quote doubled. FOCUS ServiceName / allocation labels can contain
// commas, so this is required, not decorative.
function csvField(value: string): string {
  if (/[",\r\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

// dailyCostsToCsv serializes the daily cost table EXACTLY as shown: a wide
// grid of Date, one column per (sorted, unique) grouping key, and Total (net).
// Money is written as the RAW wire decimal string (never formatMoney/Number),
// preserving exact precision. A group absent on a day is an EMPTY cell (never
// a fabricated 0). Rows are CRLF-joined and a UTF-8 BOM is prepended so Excel
// reads UTF-8 (e.g. Turkish glyphs in keys) correctly.
export function dailyCostsToCsv(costs: DailyCosts): string {
  const keys = [
    ...new Set(costs.days.flatMap((d) => d.services.map((s) => s.key))),
  ].sort();
  const header = ["Date", ...keys, "Total (net)"];
  const rows = costs.days.map((day) => [
    day.date,
    ...keys.map((k) => day.services.find((s) => s.key === k)?.cost ?? ""),
    day.total,
  ]);
  const lines = [header, ...rows].map((row) => row.map(csvField).join(","));
  // "\uFEFF" is the UTF-8 BOM written as an ASCII escape (never a literal
  // invisible BOM byte in source): plain-ASCII, review-safe, and byte-identical
  // at runtime (csv.charCodeAt(0) === 0xFEFF).
  return "\uFEFF" + lines.join("\r\n") + "\r\n";
}

// dailyCostsCsvFilename names the download after the grouping, active provider,
// currency, and the actual data span (first..last day). Caller guarantees
// days.length > 0.
export function dailyCostsCsvFilename(
  costs: DailyCosts,
  groupBy: CostGroupBy,
  tagKey = "",
): string {
  const first = costs.days[0].date;
  const last = costs.days[costs.days.length - 1].date;
  const slug = (value: string) =>
    value
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "");
  const provider = slug(costs.provider);
  const tagKeySegment =
    groupBy === "tag" && slug(tagKey) ? `-${slug(tagKey)}` : "";
  const providerSegment = provider ? `-${provider}` : "";
  const cur = costs.currency ? `-${costs.currency}` : "";
  return `costroid-daily-costs-${groupBy}${tagKeySegment}${providerSegment}${cur}-${first}_${last}.csv`;
}

// downloadCsv triggers a browser download of csv under filename. It is the only
// impure part: a text/csv Blob, an object URL, a transient <a download> click,
// then revoke. No network, no new dependency.
export function downloadCsv(filename: string, csv: string): void {
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
