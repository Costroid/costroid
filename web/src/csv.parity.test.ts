// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import type { components } from "./api/schema";
import type { DayGroup } from "./DailyTokens";
import {
  dailyCostsToCsv,
  dailyTokensToCsv,
  unitEconomicsToCsv,
  usageMetricsToCsv,
} from "./csv";

type DailyCosts = components["schemas"]["DailyCosts"];
type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

// Helper indirection defeats Vite's static rewrite of string-literal
// new URL(..., import.meta.url). Encoding must be the literal "utf8"
// (web/src/node-fs.d.ts shim). A utf8 read keeps the BOM as U+FEFF.
const read = (rel: string): string =>
  readFileSync(new URL(rel, import.meta.url), "utf8");

describe("CSV golden parity with CLI fixtures", () => {
  it("dailyCostsToCsv matches daily-costs.expected.csv (UTF-16 key order)", () => {
    const costs = JSON.parse(
      read("../../testdata/export/daily-costs.json"),
    ) as DailyCosts;
    const expected = read("../../testdata/export/daily-costs.expected.csv");
    expect(dailyCostsToCsv(costs)).toBe(expected);
  });

  it("dailyTokensToCsv matches tokens.expected.csv from pivoted days", () => {
    const days = JSON.parse(
      read("../../testdata/export/tokens.days.json"),
    ) as DayGroup[];
    const expected = read("../../testdata/export/tokens.expected.csv");
    expect(dailyTokensToCsv(days)).toBe(expected);
  });

  it("usageMetricsToCsv matches usage.expected.csv (wire order)", () => {
    const rows = JSON.parse(
      read("../../testdata/export/usage.json"),
    ) as DailyUsageMetric[];
    const expected = read("../../testdata/export/usage.expected.csv");
    expect(usageMetricsToCsv(rows)).toBe(expected);
  });

  it("unitEconomicsToCsv matches unit-economics.expected.csv", () => {
    const economics = JSON.parse(
      read("../../testdata/export/unit-economics.json"),
    ) as UnitEconomics;
    const expected = read("../../testdata/export/unit-economics.expected.csv");
    expect(unitEconomicsToCsv(economics)).toBe(expected);
  });
});
