// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import Overview from "./Overview";
import type { components } from "./api/schema";

type CostsSummary = components["schemas"]["CostsSummary"];
type Anomalies = components["schemas"]["Anomalies"];
type UnitEconomics = components["schemas"]["UnitEconomics"];
type BusinessMetrics = components["schemas"]["BusinessMetrics"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

const PERIOD_TOTAL = "964050.632653589793238462";

function summaryBody(overrides: Partial<CostsSummary> = {}): CostsSummary {
  return {
    currency: "USD",
    total: PERIOD_TOTAL,
    keys: [
      { key: "Amazon Web Services", total: "500000.000000000000000000" },
      { key: "Microsoft", total: "300000.000000000000000000" },
      { key: "Google", total: "164050.632653589793238462" },
    ],
    ...overrides,
  };
}

function anomaliesBody(flags: Anomalies["anomalies"] = []): Anomalies {
  return {
    currency: "USD",
    parameters: {
      k: "3",
      consistencyConstant: "1.4826",
      windowDays: 30,
      minObservations: 10,
      relativeFloor: "0.1",
      groupBy: "service",
    },
    anomalies: flags,
  };
}

function unitBody(): UnitEconomics {
  return {
    currency: "USD",
    metric: "requests served",
    period: {
      coveredDays: 3,
      cost: PERIOD_TOTAL,
      quantity: "1000",
      unitCost: "0.044569658748120211",
    },
    days: [
      {
        date: "2026-01-12",
        cost: "10",
        quantity: "100",
        unitCost: "0.1",
      },
      {
        date: "2026-01-13",
        cost: "20",
        quantity: "100",
        unitCost: "0.2",
      },
      {
        date: "2026-01-14",
        // Uncovered day: no unitCost → sparkline gap.
        cost: "0",
        quantity: null as unknown as string,
      },
      {
        date: "2026-01-15",
        cost: "30",
        quantity: "100",
        unitCost: "0.3",
      },
    ],
  };
}

function metricsBody(
  metrics: BusinessMetrics["metrics"] = [
    {
      name: "requests served",
      firstDay: "2026-01-12",
      lastDay: "2026-07-11",
    },
  ],
): BusinessMetrics {
  return { metrics };
}

type RouteHandlers = {
  summary?: () => Promise<Response> | Response;
  anomalies?: () => Promise<Response> | Response;
  metrics?: () => Promise<Response> | Response;
  unit?: () => Promise<Response> | Response;
};

function mockRoutes(handlers: RouteHandlers = {}) {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    const path = new URL(url, "http://x").pathname;
    if (path === "/api/v1/costs/summary") {
      return Promise.resolve(
        handlers.summary
          ? handlers.summary()
          : fakeResponse(200, summaryBody()),
      );
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        handlers.anomalies
          ? handlers.anomalies()
          : fakeResponse(200, anomaliesBody()),
      );
    }
    if (path === "/api/v1/business-metrics") {
      return Promise.resolve(
        handlers.metrics
          ? handlers.metrics()
          : fakeResponse(200, metricsBody()),
      );
    }
    if (path === "/api/v1/unit-economics/daily") {
      return Promise.resolve(
        handlers.unit ? handlers.unit() : fakeResponse(200, unitBody()),
      );
    }
    return Promise.resolve(fakeResponse(404, null));
  });
}

function fetchedURLs(): string[] {
  return vi.mocked(fetch).mock.calls.map(([input]) => String(input));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Overview", () => {
  it("renders the verbatim 18-digit period total and currency subtitle", async () => {
    vi.stubGlobal("fetch", mockRoutes());
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(PERIOD_TOTAL)).toBeTruthy();
    // Currency appears as StatCard subtitle for the period total.
    const currencyHits = screen.getAllByText("USD");
    expect(currencyHits.length).toBeGreaterThan(0);
  });

  it("isolates summary 500: cards 1–3 ErrorState while 4–5 render data", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: () => fakeResponse(500, null),
        anomalies: () =>
          fakeResponse(
            200,
            anomaliesBody([
              {
                date: "2026-05-12",
                scope: "total",
                direction: "increase",
                observed: "1",
                median: "1",
                mad: "1",
                scaledMad: "1",
                threshold: "1",
                deviation: "1",
              },
            ]),
          ),
      }),
    );
    render(<Overview range={{ start: "2026-04-13", end: "2026-07-11" }} />);

    expect(
      await screen.findByText(
        /Failed to load cost summary: GET \/api\/v1\/costs\/summary returned 500/,
      ),
    ).toBeTruthy();
    // Anomaly card shows data (count=1), not a skeleton.
    expect(await screen.findByText("Flagged days")).toBeTruthy();
    expect(screen.getByText("1")).toBeTruthy();
    // Unit cost card shows data.
    expect(await screen.findByText("0.044569658748120211")).toBeTruthy();
    // No loading skeletons left for anomalies/unit.
    expect(screen.queryByLabelText("Loading anomalies…")).toBeNull();
    expect(screen.queryByLabelText("Loading unit economics…")).toBeNull();
  });

  it("isolates anomalies 500: only card 4 degrades", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        anomalies: () => fakeResponse(500, null),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(PERIOD_TOTAL)).toBeTruthy();
    expect(
      await screen.findByText(
        /Failed to load anomalies: GET \/api\/v1\/anomalies returned 500/,
      ),
    ).toBeTruthy();
    expect(await screen.findByText("0.044569658748120211")).toBeTruthy();
  });

  it("isolates metrics 500: only card 5 degrades", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        metrics: () => fakeResponse(500, null),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(PERIOD_TOTAL)).toBeTruthy();
    expect(
      await screen.findByText(
        /Failed to load unit cost: GET \/api\/v1\/business-metrics returned 500/,
      ),
    ).toBeTruthy();
    // Anomaly card still OK (0-count good news).
    expect(await screen.findByText(/All clear/)).toBeTruthy();
  });

  it("renders Largest providers when no key carries delta", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: () =>
          fakeResponse(
            200,
            summaryBody({
              // No previous fields, no deltas — FULL-preset shape.
              keys: [
                { key: "Amazon Web Services", total: "500" },
                { key: "Microsoft", total: "300" },
              ],
            }),
          ),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText("Largest providers")).toBeTruthy();
    expect(screen.queryByText("Top movers")).toBeNull();
    // Totals only — no delta strings. Split + movers both list key totals.
    expect(screen.getAllByText("500").length).toBeGreaterThan(0);
    expect(screen.getAllByText("300").length).toBeGreaterThan(0);
    // No signed delta column present under the movers card.
    const movers = screen.getByText("Largest providers").closest("article");
    expect(movers!.querySelector(".overview-key-delta")).toBeNull();
  });

  it("renders Top movers ranked by |delta| with mixed-sign verbatim deltas", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: () =>
          fakeResponse(
            200,
            summaryBody({
              previousTotal: "100",
              previousStart: "2026-03-14",
              previousEnd: "2026-04-12",
              keys: [
                // |delta| order should be B (50), C (30), A (10) — not signed order.
                {
                  key: "A",
                  total: "110",
                  previousTotal: "100",
                  delta: "10",
                },
                {
                  key: "B",
                  total: "50",
                  previousTotal: "100",
                  delta: "-50",
                },
                {
                  key: "C",
                  total: "130",
                  previousTotal: "100",
                  delta: "30",
                },
              ],
            }),
          ),
      }),
    );
    render(<Overview range={{ start: "2026-04-13", end: "2026-07-11" }} />);

    expect(await screen.findByText("Top movers")).toBeTruthy();
    expect(screen.getByText("vs 2026-03-14 → 2026-04-12")).toBeTruthy();
    expect(screen.getByText("-50")).toBeTruthy();
    expect(screen.getByText("30")).toBeTruthy();
    expect(screen.getByText("10")).toBeTruthy();

    // DOM order of mover rows follows |delta| desc: B, C, A.
    const list = screen.getByText("Top movers").closest("article");
    const names = Array.from(list!.querySelectorAll(".overview-key-name")).map(
      (el) => el.textContent,
    );
    expect(names).toEqual(["B", "C", "A"]);
  });

  it("ranks movers with the exact decimal comparator, not Number()", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: () =>
          fakeResponse(
            200,
            summaryBody({
              previousTotal: "100",
              previousStart: "2026-03-14",
              previousEnd: "2026-04-12",
              keys: [
                // Number() rounds both |delta|s to exactly 1; only the exact
                // string comparator ranks Zed's |-1.000000000000000002| above
                // Alpha's 1.000000000000000001. A float-based sort ties and
                // falls back to the key tie-break, leaving Alpha first.
                {
                  key: "Alpha",
                  total: "5.000000000000000001",
                  previousTotal: "4",
                  delta: "1.000000000000000001",
                },
                {
                  key: "Zed",
                  total: "3",
                  previousTotal: "4.000000000000000002",
                  delta: "-1.000000000000000002",
                },
              ],
            }),
          ),
      }),
    );
    render(<Overview range={{ start: "2026-04-13", end: "2026-07-11" }} />);

    expect(await screen.findByText("Top movers")).toBeTruthy();
    const list = screen.getByText("Top movers").closest("article");
    const names = Array.from(list!.querySelectorAll(".overview-key-name")).map(
      (el) => el.textContent,
    );
    expect(names).toEqual(["Zed", "Alpha"]);
  });

  it("anomaly card count equals array length; 0-case is good news", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        anomalies: () => fakeResponse(200, anomaliesBody([])),
      }),
    );
    render(<Overview range={{ start: "2026-06-12", end: "2026-07-11" }} />);

    expect(await screen.findByText("Flagged days")).toBeTruthy();
    expect(screen.getByText("0")).toBeTruthy();
    expect(screen.getByText(/All clear/)).toBeTruthy();
    expect(screen.getByText(/by service/)).toBeTruthy();
  });

  it("unit-cost card shows EmptyState when no business metrics", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        metrics: () => fakeResponse(200, metricsBody([])),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText("No business metrics yet")).toBeTruthy();
  });

  it("refetches on range change with rangeQuery form and ignores stale", async () => {
    let resolveSlow: ((r: Response) => void) | undefined;
    const slowSummary = new Promise<Response>((resolve) => {
      resolveSlow = resolve;
    });

    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const path = new URL(url, "http://x").pathname;
      if (path === "/api/v1/costs/summary") {
        if (url.includes("start=2026-01-12")) {
          // Slow first response — will be stale after range change.
          return slowSummary;
        }
        return Promise.resolve(
          fakeResponse(
            200,
            summaryBody({ total: "99", keys: [{ key: "X", total: "99" }] }),
          ),
        );
      }
      if (path === "/api/v1/anomalies") {
        return Promise.resolve(fakeResponse(200, anomaliesBody()));
      }
      if (path === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody()));
      }
      if (path === "/api/v1/unit-economics/daily") {
        return Promise.resolve(fakeResponse(200, unitBody()));
      }
      return Promise.resolve(fakeResponse(404, null));
    });
    vi.stubGlobal("fetch", fetchMock);

    const { rerender } = render(
      <Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />,
    );

    await waitFor(() => {
      expect(
        fetchedURLs().some((u) =>
          u.includes("/api/v1/costs/summary?start=2026-01-12&end=2026-07-11"),
        ),
      ).toBe(true);
    });

    rerender(<Overview range={{ start: "2026-06-12", end: "2026-07-11" }} />);

    expect((await screen.findAllByText("99")).length).toBeGreaterThan(0);

    // Stale first response must not overwrite the new total.
    resolveSlow!(fakeResponse(200, summaryBody({ total: PERIOD_TOTAL })));
    await waitFor(() => {
      expect(screen.queryByText(PERIOD_TOTAL)).toBeNull();
    });
    expect(screen.getAllByText("99").length).toBeGreaterThan(0);

    // Second range used rangeQuery form.
    expect(
      fetchedURLs().some((u) =>
        u.includes("/api/v1/costs/summary?start=2026-06-12&end=2026-07-11"),
      ),
    ).toBe(true);
    // groupBy=provider present on EVERY summary request (non-default) — a
    // regression dropping it on refetch must fail, so no cumulative .some().
    const summaryURLs = fetchedURLs().filter((u) =>
      u.includes("/api/v1/costs/summary"),
    );
    expect(summaryURLs.length).toBeGreaterThan(1);
    for (const u of summaryURLs) {
      expect(u).toContain("groupBy=provider");
    }
  });
});
