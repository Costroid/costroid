// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import type { components } from "./api/schema";
import type { DayGroup } from "./DailyTokens";
import {
  dailyCostsCsvFilename,
  dailyCostsToCsv,
  dailyTokensCsvFilename,
  dailyTokensToCsv,
  unitEconomicsCsvFilename,
  unitEconomicsToCsv,
  usageMetricsCsvFilename,
  usageMetricsToCsv,
} from "./csv";
import { sumIntegerStrings } from "./viz";

type DailyCosts = components["schemas"]["DailyCosts"];
type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

describe("dailyCostsToCsv", () => {
  it("serializes the visible wide table with BOM, CRLF, and empty cells", () => {
    const costs: DailyCosts = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      tagKeys: [],
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
      provider: "",
      providers: ["Amazon Web Services"],
      tagKeys: [],
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
      provider: "",
      providers: ["Amazon Web Services"],
      tagKeys: [],
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
      provider: "",
      providers: ["Amazon Web Services"],
      tagKeys: [],
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
    provider: "",
    providers: ["Amazon Web Services"],
    tagKeys: [],
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

  it.each([
    ["Amazon Web Services", "amazon-web-services"],
    ["GOOGLE.Cloud / AI", "google-cloud-ai"],
    ["  Microsoft!!!Azure  ", "microsoft-azure"],
  ])("sanitizes provider %s for the filename", (provider, segment) => {
    expect(dailyCostsCsvFilename({ ...costs, provider }, "service")).toBe(
      `costroid-daily-costs-service-${segment}-USD-2026-05-01_2026-05-03.csv`,
    );
  });

  it("omits a provider that sanitizes to an empty segment", () => {
    expect(
      dailyCostsCsvFilename({ ...costs, provider: "!!!" }, "service"),
    ).toBe("costroid-daily-costs-service-USD-2026-05-01_2026-05-03.csv");
  });

  it("adds a sanitized tag key segment only for tag grouping", () => {
    expect(dailyCostsCsvFilename(costs, "tag", "Cost Center / Team")).toBe(
      "costroid-daily-costs-tag-cost-center-team-USD-2026-05-01_2026-05-03.csv",
    );
    expect(dailyCostsCsvFilename(costs, "service", "Cost Center")).toBe(
      "costroid-daily-costs-service-USD-2026-05-01_2026-05-03.csv",
    );
  });
});

describe("dailyTokensToCsv", () => {
  it("serializes the wide table with BOM, CRLF, sorted columns, and quoting", () => {
    // First appearance is Zeta then Alpha so the sorted-columns rule is
    // load-bearing (default .sort() reorders them).
    const days: DayGroup[] = [
      {
        date: "2026-05-01",
        services: [
          { serviceName: "Zeta, Inc", quantity: "100" },
          { serviceName: 'Alpha "Prime"', quantity: "200" },
        ],
        total: "300",
      },
      {
        date: "2026-05-02",
        services: [{ serviceName: "Zeta, Inc", quantity: "50" }],
        total: "50",
      },
    ];

    const csv = dailyTokensToCsv(days);

    expect(csv.charCodeAt(0)).toBe(0xfeff);
    expect(csv).toBe(
      '\uFEFFDate,"Alpha ""Prime""","Zeta, Inc",Total\r\n' +
        "2026-05-01,200,100,300\r\n" +
        "2026-05-02,,50,50\r\n",
    );
  });

  it("writes empty cells for absent day+service and null Total", () => {
    const days: DayGroup[] = [
      {
        date: "2026-05-01",
        services: [{ serviceName: "A", quantity: "10" }],
        total: "10",
      },
      {
        date: "2026-05-02",
        services: [{ serviceName: "B", quantity: "not-int" }],
        total: null,
      },
    ];

    const csv = dailyTokensToCsv(days);

    expect(csv).toBe(
      "\uFEFFDate,A,B,Total\r\n" +
        "2026-05-01,10,,10\r\n" +
        "2026-05-02,,not-int,\r\n",
    );
    expect(csv).toContain(",,");
    expect(csv).not.toContain(",0,");
  });

  it("mirrors the component grouping cell layout for a DayGroup[]", () => {
    // Build groups the same way the component does: sum integer quantities
    // per service, fall back to the latest raw string, null total when any
    // quantity is non-integer.
    const wire = [
      {
        date: "2026-05-02",
        serviceName: "claude",
        consumedQuantity: "100",
      },
      {
        date: "2026-05-01",
        serviceName: "gpt",
        consumedQuantity: "50",
      },
      {
        date: "2026-05-01",
        serviceName: "claude",
        consumedQuantity: "25",
      },
      {
        date: "2026-05-02",
        serviceName: "gpt",
        consumedQuantity: "3.5",
      },
    ];
    const byDate = new Map<string, Map<string, string>>();
    for (const row of wire) {
      let services = byDate.get(row.date);
      if (!services) {
        services = new Map();
        byDate.set(row.date, services);
      }
      const prev = services.get(row.serviceName);
      if (prev === undefined) {
        services.set(row.serviceName, row.consumedQuantity);
      } else {
        const summed = sumIntegerStrings([prev, row.consumedQuantity]);
        services.set(row.serviceName, summed ?? row.consumedQuantity);
      }
    }
    const days: DayGroup[] = [...byDate.keys()].sort().map((date) => {
      const services = [...(byDate.get(date) ?? new Map()).entries()]
        .map(([serviceName, quantity]) => ({ serviceName, quantity }))
        .sort((a, b) => a.serviceName.localeCompare(b.serviceName));
      return {
        date,
        services,
        total: sumIntegerStrings(services.map((s) => s.quantity)),
      };
    });

    expect(dailyTokensToCsv(days)).toBe(
      "\uFEFFDate,claude,gpt,Total\r\n" +
        "2026-05-01,25,50,75\r\n" +
        "2026-05-02,100,3.5,\r\n",
    );
  });
});

describe("dailyTokensCsvFilename", () => {
  it("uses the first and last grouped dates as the span", () => {
    const days: DayGroup[] = [
      { date: "2026-05-01", services: [], total: "0" },
      { date: "2026-05-04", services: [], total: "0" },
    ];
    expect(dailyTokensCsvFilename(days)).toBe(
      "costroid-daily-tokens-2026-05-01_2026-05-04.csv",
    );
  });
});

describe("usageMetricsToCsv", () => {
  it("serializes the long wire form with BOM, CRLF, and quoting", () => {
    // Two units so the multi-unit fixture rule is load-bearing.
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "Zeta, Inc",
        serviceTier: "",
        metricName: 'input "tokens"',
        unit: "Tokens",
        quantity: "100",
      },
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "priority",
        metricName: "web_search",
        unit: "Calls",
        quantity: "42",
      },
    ];

    const csv = usageMetricsToCsv(rows);

    expect(csv.charCodeAt(0)).toBe(0xfeff);
    expect(csv).toBe(
      "\uFEFFDate,Service,Tier,Metric,Unit,Quantity\r\n" +
        '2026-05-01,"Zeta, Inc",,"input ""tokens""",Tokens,100\r\n' +
        "2026-05-01,OpenAI API,priority,web_search,Calls,42\r\n",
    );
  });

  it("writes an empty Tier cell for a wire empty serviceTier", () => {
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "gpt-4o",
        serviceTier: "",
        metricName: "uncached_input_tokens",
        unit: "Tokens",
        quantity: "10",
      },
    ];

    const csv = usageMetricsToCsv(rows);
    expect(csv).toBe(
      "\uFEFFDate,Service,Tier,Metric,Unit,Quantity\r\n" +
        "2026-05-01,gpt-4o,,uncached_input_tokens,Tokens,10\r\n",
    );
    expect(csv).toContain(",,");
  });

  it("preserves wire order and exact-duplicate rows in place", () => {
    const dup: DailyUsageMetric = {
      date: "2026-05-03",
      serviceName: "A",
      serviceTier: "",
      metricName: "m",
      unit: "Tokens",
      quantity: "1",
    };
    // Deliberately out of date-order, with one exact duplicate in place.
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-03",
        serviceName: "B",
        serviceTier: "t",
        metricName: "m",
        unit: "Calls",
        quantity: "2",
      },
      dup,
      {
        date: "2026-05-01",
        serviceName: "C",
        serviceTier: "",
        metricName: "m",
        unit: "Tokens",
        quantity: "3",
      },
      dup,
    ];

    expect(usageMetricsToCsv(rows)).toBe(
      "\uFEFFDate,Service,Tier,Metric,Unit,Quantity\r\n" +
        "2026-05-03,B,t,m,Calls,2\r\n" +
        "2026-05-03,A,,m,Tokens,1\r\n" +
        "2026-05-01,C,,m,Tokens,3\r\n" +
        "2026-05-03,A,,m,Tokens,1\r\n",
    );
  });
});

describe("usageMetricsCsvFilename", () => {
  it("uses the first and last wire row dates as the span", () => {
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-03",
        serviceName: "A",
        serviceTier: "",
        metricName: "m",
        unit: "Tokens",
        quantity: "1",
      },
      {
        date: "2026-05-01",
        serviceName: "B",
        serviceTier: "",
        metricName: "m",
        unit: "Tokens",
        quantity: "2",
      },
    ];
    expect(usageMetricsCsvFilename(rows)).toBe(
      "costroid-usage-metrics-2026-05-03_2026-05-01.csv",
    );
  });
});

describe("unitEconomicsToCsv", () => {
  it("serializes fixed columns with BOM, CRLF, and verbatim decimals", () => {
    const economics: UnitEconomics = {
      metric: "requests",
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      days: [
        {
          date: "2026-05-01",
          cost: "10.00",
          quantity: "5",
          unitCost: "1.234567890123456789",
        },
        {
          date: "2026-05-02",
          cost: "0.000000000000000001",
          quantity: "1",
          unitCost: "0.000000000000000001",
        },
      ],
      period: {
        coveredDays: 2,
        cost: "10.000000000000000001",
        quantity: "6",
        unitCost: "1.666666666666666667",
      },
    };

    const csv = unitEconomicsToCsv(economics);

    expect(csv.charCodeAt(0)).toBe(0xfeff);
    expect(csv).toBe(
      "\uFEFFDate,Cost,Quantity,Unit cost\r\n" +
        "2026-05-01,10.00,5,1.234567890123456789\r\n" +
        "2026-05-02,0.000000000000000001,1,0.000000000000000001\r\n",
    );
    expect(csv.includes("1.234567890123456789")).toBe(true);
  });

  it("writes empty cells for absent fields and literal 0 for unitCost zero", () => {
    const economics: UnitEconomics = {
      metric: "requests",
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      days: [
        { date: "2026-05-01" },
        {
          date: "2026-05-02",
          cost: "0",
          quantity: "0",
          unitCost: "0",
        },
      ],
      period: { coveredDays: 0, cost: "0", quantity: "0" },
    };

    const csv = unitEconomicsToCsv(economics);

    expect(csv).toBe(
      "\uFEFFDate,Cost,Quantity,Unit cost\r\n" +
        "2026-05-01,,,\r\n" +
        "2026-05-02,0,0,0\r\n",
    );
  });
});

describe("unitEconomicsCsvFilename", () => {
  const base: UnitEconomics = {
    metric: "active users",
    currency: "USD",
    currencies: ["USD"],
    provider: "Amazon Web Services",
    providers: ["Amazon Web Services"],
    days: [
      { date: "2026-05-01", cost: "1", quantity: "1", unitCost: "1" },
      { date: "2026-05-03", cost: "2", quantity: "2", unitCost: "1" },
    ],
    period: {
      coveredDays: 2,
      cost: "3",
      quantity: "3",
      unitCost: "1",
    },
  };

  it("includes metric, provider, currency, and the day span", () => {
    expect(unitEconomicsCsvFilename(base)).toBe(
      "costroid-unit-economics-active-users-amazon-web-services-USD-2026-05-01_2026-05-03.csv",
    );
  });

  it("omits provider when the slug is empty and currency when blank", () => {
    expect(
      unitEconomicsCsvFilename({
        ...base,
        provider: "!!!",
        currency: "",
      }),
    ).toBe("costroid-unit-economics-active-users-2026-05-01_2026-05-03.csv");
    expect(
      unitEconomicsCsvFilename({
        ...base,
        provider: "",
        currency: "EUR",
      }),
    ).toBe(
      "costroid-unit-economics-active-users-EUR-2026-05-01_2026-05-03.csv",
    );
  });

  it.each([
    ["Amazon Web Services", "amazon-web-services"],
    ["GOOGLE.Cloud / AI", "google-cloud-ai"],
  ])("sanitizes provider %s for the filename", (provider, segment) => {
    expect(unitEconomicsCsvFilename({ ...base, provider })).toBe(
      `costroid-unit-economics-active-users-${segment}-USD-2026-05-01_2026-05-03.csv`,
    );
  });
});
