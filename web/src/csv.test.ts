// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import type { components } from "./api/schema";
import { dailyCostsCsvFilename, dailyCostsToCsv } from "./csv";

type DailyCosts = components["schemas"]["DailyCosts"];

describe("dailyCostsToCsv", () => {
  it("serializes the visible wide table with BOM, CRLF, and empty cells", () => {
    const costs: DailyCosts = {
      currency: "USD",
      currencies: ["USD"],
      total: "10.00",
      days: [
        {
          date: "2026-05-01",
          total: "3.00",
          services: [{ key: "A", cost: "3.00" }],
        },
        {
          date: "2026-05-02",
          total: "7.00",
          services: [{ key: "B", cost: "7.00" }],
        },
      ],
    };

    const csv = dailyCostsToCsv(costs);

    expect(csv).toBe(
      "\uFEFFDate,A,B,Total (net)\r\n" +
        "2026-05-01,3.00,,3.00\r\n" +
        "2026-05-02,,7.00,7.00\r\n",
    );
    expect(csv).toContain(",,");
    expect(csv).not.toContain(",0,");
  });

  it("preserves exact wire decimal strings verbatim", () => {
    const costs: DailyCosts = {
      currency: "USD",
      currencies: ["USD"],
      total: "1234.567890123456789012",
      days: [
        {
          date: "2026-05-01",
          total: "1234.567890123456789012",
          services: [
            { key: "Large", cost: "1234.567890123456789012" },
            { key: "Small", cost: "0.000000000000000001" },
          ],
        },
      ],
    };

    const csv = dailyCostsToCsv(costs);

    expect(csv.includes("1234.567890123456789012")).toBe(true);
    expect(csv.includes("0.000000000000000001")).toBe(true);
  });

  it("quotes commas, double-quotes, and newlines per RFC 4180", () => {
    const costs: DailyCosts = {
      currency: "USD",
      currencies: ["USD"],
      total: "6",
      days: [
        {
          date: "2026-05-01",
          total: "6",
          services: [
            { key: "Alpha, Inc", cost: "1" },
            { key: 'Beta "Prime"', cost: "2" },
            { key: "Line\nBreak", cost: "3" },
          ],
        },
      ],
    };

    const csv = dailyCostsToCsv(costs);

    expect(csv).toContain('"Alpha, Inc"');
    expect(csv).toContain('"Beta ""Prime"""');
    expect(csv).toContain('"Line\nBreak"');
  });

  it("prepends the UTF-8 BOM", () => {
    const costs: DailyCosts = {
      currency: "USD",
      currencies: ["USD"],
      total: "1",
      days: [
        {
          date: "2026-05-01",
          total: "1",
          services: [{ key: "Turkce", cost: "1" }],
        },
      ],
    };

    expect(dailyCostsToCsv(costs).charCodeAt(0)).toBe(0xfeff);
  });
});

describe("dailyCostsCsvFilename", () => {
  const costs: DailyCosts = {
    currency: "USD",
    currencies: ["USD"],
    total: "2",
    days: [
      {
        date: "2026-05-01",
        total: "1",
        services: [{ key: "A", cost: "1" }],
      },
      {
        date: "2026-05-03",
        total: "1",
        services: [{ key: "A", cost: "1" }],
      },
    ],
  };

  it("includes grouping, currency, and the actual data span", () => {
    expect(dailyCostsCsvFilename(costs, "service")).toBe(
      "costroid-daily-costs-service-USD-2026-05-01_2026-05-03.csv",
    );
  });

  it("omits the currency segment when the response currency is empty", () => {
    expect(dailyCostsCsvFilename({ ...costs, currency: "" }, "service")).toBe(
      "costroid-daily-costs-service-2026-05-01_2026-05-03.csv",
    );
  });
});
