// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { CostGroupBy } from "./api";
import type { components } from "./api/schema";
import type { DayGroup } from "./DailyTokens";

type DailyCosts = components["schemas"]["DailyCosts"];
type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

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

// Rows are CRLF-joined and a UTF-8 BOM is prepended so Excel reads UTF-8
// (e.g. Turkish glyphs in keys) correctly.
// "\uFEFF" is the UTF-8 BOM written as an ASCII escape (never a literal
// invisible BOM byte in source): plain-ASCII, review-safe, and byte-identical
// at runtime (csv.charCodeAt(0) === 0xFEFF).
function toCsv(rows: string[][]): string {
  const lines = rows.map((row) => row.map(csvField).join(","));
  return "\uFEFF" + lines.join("\r\n") + "\r\n";
}

// slug sanitizes free-text segments for download filenames (lowercase,
// non-alphanumeric runs collapsed to "-", leading/trailing hyphens stripped).
function slug(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

// dailyCostsToCsv serializes the daily cost table EXACTLY as shown: a wide
// grid of Date, one column per (sorted, unique) grouping key, and Total (net).
// Money is written as the RAW wire decimal string (never formatMoney/Number),
// preserving exact precision. A group absent on a day is an EMPTY cell (never
// a fabricated 0).
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
  return toCsv([header, ...rows]);
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
  const provider = slug(costs.provider);
  const tagKeySegment =
    groupBy === "tag" && slug(tagKey) ? `-${slug(tagKey)}` : "";
  const providerSegment = provider ? `-${provider}` : "";
  const cur = costs.currency ? `-${costs.currency}` : "";
  return `costroid-daily-costs-${groupBy}${tagKeySegment}${providerSegment}${cur}-${first}_${last}.csv`;
}

// dailyTokensToCsv serializes the wide visible Daily Tokens table: Date, one
// column per sorted-unique service name, and Total. Input is the same DayGroup[]
// the component renders, so the CSV equals the table by construction. A
// day+service cell absent in the group is empty (never 0); a null Total is empty.
export function dailyTokensToCsv(days: DayGroup[]): string {
  const services = [
    ...new Set(days.flatMap((d) => d.services.map((s) => s.serviceName))),
  ].sort();
  const header = ["Date", ...services, "Total"];
  const rows = days.map((day) => [
    day.date,
    ...services.map(
      (name) =>
        day.services.find((s) => s.serviceName === name)?.quantity ?? "",
    ),
    day.total ?? "",
  ]);
  return toCsv([header, ...rows]);
}

// dailyTokensCsvFilename names the download after the grouped date span.
// Caller guarantees days.length > 0.
export function dailyTokensCsvFilename(days: DayGroup[]): string {
  const first = days[0].date;
  const last = days[days.length - 1].date;
  return `costroid-daily-tokens-${first}_${last}.csv`;
}

// usageMetricsToCsv serializes the long wire form of daily usage metrics:
// Date,Service,Tier,Metric,Unit,Quantity - one row per wire row, in wire
// order (no client-side sort, group, or dedupe). Empty serviceTier is an
// empty cell from the wire value.
export function usageMetricsToCsv(rows: DailyUsageMetric[]): string {
  const header = ["Date", "Service", "Tier", "Metric", "Unit", "Quantity"];
  const body = rows.map((row) => [
    row.date,
    row.serviceName,
    row.serviceTier,
    row.metricName,
    row.unit,
    row.quantity,
  ]);
  return toCsv([header, ...body]);
}

// usageMetricsCsvFilename names the download after the first/last wire row
// date. Caller guarantees rows.length > 0.
export function usageMetricsCsvFilename(rows: DailyUsageMetric[]): string {
  const first = rows[0].date;
  const last = rows[rows.length - 1].date;
  return `costroid-usage-metrics-${first}_${last}.csv`;
}

// unitEconomicsToCsv serializes the visible unit economics table: Date, Cost,
// Quantity, Unit cost - one row per economics.days entry, values as verbatim
// wire strings. A present unitCost of "0" is written as 0; only an absent
// optional field is an empty cell (never a truthiness check on the string).
export function unitEconomicsToCsv(economics: UnitEconomics): string {
  const header = ["Date", "Cost", "Quantity", "Unit cost"];
  const rows = economics.days.map((day) => [
    day.date,
    day.cost ?? "",
    day.quantity ?? "",
    day.unitCost ?? "",
  ]);
  return toCsv([header, ...rows]);
}

// unitEconomicsCsvFilename names the download after metric, optional provider
// and currency, and the day span. Caller guarantees days.length > 0.
export function unitEconomicsCsvFilename(economics: UnitEconomics): string {
  const first = economics.days[0].date;
  const last = economics.days[economics.days.length - 1].date;
  const metric = slug(economics.metric);
  const provider = slug(economics.provider);
  const providerSegment = provider ? `-${provider}` : "";
  const cur = economics.currency ? `-${economics.currency}` : "";
  return `costroid-unit-economics-${metric}${providerSegment}${cur}-${first}_${last}.csv`;
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
