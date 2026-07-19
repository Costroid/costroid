// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
// Money renders at display precision; the exact string moves to the title.
const PERIOD_TOTAL_DISPLAY = "964,050.63";
const UNIT_COST = "0.044569658748120211";
const UNIT_COST_DISPLAY = "0.04457";

function summaryBody(overrides: Partial<CostsSummary> = {}): CostsSummary {
  return {
    currency: "USD",
    currencies: ["USD"],
    provider: "",
    providers: ["Amazon Web Services"],
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

function unitBody(overrides: Partial<UnitEconomics> = {}): UnitEconomics {
  return {
    currency: "USD",
    currencies: ["USD"],
    provider: "",
    providers: ["Amazon Web Services"],
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
    ...overrides,
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

type RouteHandler = (url: string) => Promise<Response> | Response;

type RouteHandlers = {
  summary?: RouteHandler;
  anomalies?: RouteHandler;
  metrics?: RouteHandler;
  unit?: RouteHandler;
};

function mockRoutes(handlers: RouteHandlers = {}) {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    const path = new URL(url, "http://x").pathname;
    if (path === "/api/v1/costs/summary") {
      return Promise.resolve(
        handlers.summary
          ? handlers.summary(url)
          : fakeResponse(200, summaryBody()),
      );
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        handlers.anomalies
          ? handlers.anomalies(url)
          : fakeResponse(200, anomaliesBody()),
      );
    }
    if (path === "/api/v1/business-metrics") {
      return Promise.resolve(
        handlers.metrics
          ? handlers.metrics(url)
          : fakeResponse(200, metricsBody()),
      );
    }
    if (path === "/api/v1/unit-economics/daily") {
      return Promise.resolve(
        handlers.unit ? handlers.unit(url) : fakeResponse(200, unitBody()),
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
  it("formats the 18-digit period total for display with the exact value in the title", async () => {
    vi.stubGlobal("fetch", mockRoutes());
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    const total = await screen.findByText(PERIOD_TOTAL_DISPLAY);
    expect(total.getAttribute("title")).toBe(`${PERIOD_TOTAL} USD`);
    // The raw 18-digit string never renders as text — display precision only.
    expect(screen.queryByText(PERIOD_TOTAL)).toBeNull();
    // The period total is the hero stat of the Overview grid.
    expect(
      total.closest(".overview-card")?.classList.contains("overview-hero"),
    ).toBe(true);
    // Currency appears as StatCard subtitle for the period total.
    const currencyHits = screen.getAllByText("USD");
    expect(currencyHits.length).toBeGreaterThan(0);
  });

  it("renders the currency selector only when the summary lists more than one currency", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) => {
          const mixed = url.includes("start=2026-06-01");
          return fakeResponse(
            200,
            summaryBody({
              currency: mixed ? "EUR" : "USD",
              currencies: mixed ? ["EUR", "USD"] : ["USD"],
            }),
          );
        },
      }),
    );
    const { rerender } = render(
      <Overview range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    await screen.findByText(PERIOD_TOTAL_DISPLAY);
    expect(screen.queryByRole("group", { name: "Currency" })).toBeNull();

    rerender(<Overview range={{ start: "2026-06-01", end: "2026-06-30" }} />);
    expect(await screen.findByRole("group", { name: "Currency" })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "EUR" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("renders the provider selector only when the summary lists more than one provider", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) => {
          const multiple = url.includes("start=2026-06-01");
          return fakeResponse(
            200,
            summaryBody({
              provider: "",
              providers: multiple
                ? ["Amazon Web Services", "Microsoft"]
                : ["Amazon Web Services"],
            }),
          );
        },
      }),
    );
    const { rerender } = render(
      <Overview range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    await screen.findByText(PERIOD_TOTAL_DISPLAY);
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();

    rerender(<Overview range={{ start: "2026-06-01", end: "2026-06-30" }} />);
    const selector = await screen.findByRole("group", { name: "Provider" });
    expect(
      screen
        .getByRole("button", { name: "All providers" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      Array.from(selector.querySelectorAll("button")).map(
        (button) => button.textContent,
      ),
    ).toEqual(["All providers", "Amazon Web Services", "Microsoft"]);
  });

  it("drills a provider selection into services and threads the provider to every chain", async () => {
    const providerFrom = (url: string) =>
      new URL(url, "http://x").searchParams.get("provider") ?? "";
    const providers = ["Amazon Web Services", "Microsoft"];
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) => {
          const requested = providerFrom(url);
          if (requested === "Microsoft") {
            return fakeResponse(
              200,
              summaryBody({
                provider: requested,
                providers,
                currencies: ["USD"],
                keys: [
                  { key: "Virtual Machines", total: "7" },
                  { key: "Blob Storage", total: "3" },
                ],
              }),
            );
          }
          if (requested !== "") {
            // Previous-window fields require a BOUNDED request (contract
            // condition i), so this test renders a bounded range below; the
            // dates echo the server's derivation for June (prevEnd =
            // start - 1 day, prevStart = prevEnd - (end - start)).
            return fakeResponse(
              200,
              summaryBody({
                provider: requested,
                providers,
                currencies: ["USD"],
                previousTotal: "6",
                previousStart: "2026-05-02",
                previousEnd: "2026-05-31",
                keys: [
                  {
                    key: "Amazon EC2",
                    total: "10",
                    previousTotal: "4",
                    delta: "6",
                  },
                  {
                    key: "Amazon S3",
                    total: "5",
                    previousTotal: "2",
                    delta: "3",
                  },
                ],
              }),
            );
          }
          return fakeResponse(
            200,
            summaryBody({ provider: "", providers, currencies: ["USD"] }),
          );
        },
        unit: (url) =>
          fakeResponse(
            200,
            unitBody({
              provider: providerFrom(url),
              providers,
              currencies: ["USD"],
            }),
          ),
      }),
    );
    render(<Overview range={{ start: "2026-06-01", end: "2026-06-30" }} />);

    const selector = await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      within(selector).getByRole("button", { name: "Amazon Web Services" }),
    );

    const encoded = "Amazon%20Web%20Services";
    const bounded = "start=2026-06-01&end=2026-06-30";
    await waitFor(() => {
      const urls = fetchedURLs();
      const summaryURL = urls.find(
        (url) =>
          url.startsWith("/api/v1/costs/summary") &&
          url.includes(`provider=${encoded}`),
      );
      expect(summaryURL).toBe(
        `/api/v1/costs/summary?${bounded}&provider=${encoded}`,
      );
      expect(summaryURL).not.toContain("groupBy=provider");
      expect(summaryURL).not.toContain("groupBy=service");
      expect(urls).toContain(
        `/api/v1/anomalies?${bounded}&provider=${encoded}`,
      );
      expect(urls).toContain(
        `/api/v1/unit-economics/daily?metric=requests%20served&${bounded}&provider=${encoded}`,
      );
    });
    expect(await screen.findByText("Spend by service")).toBeTruthy();
    expect(
      screen.getByRole("img", { name: "Service spend split" }),
    ).toBeTruthy();
    const movers = screen.getByText("Top movers").closest("article");
    expect(within(movers!).getByText("Service")).toBeTruthy();

    fireEvent.click(
      within(screen.getByRole("group", { name: "Provider" })).getByRole(
        "button",
        { name: "Microsoft" },
      ),
    );
    expect(await screen.findByText("Largest services")).toBeTruthy();
    expect(screen.queryByText("Largest providers")).toBeNull();
  });

  it("snaps a dropped provider selection to All using the providers list, not the echo", async () => {
    const selectedProvider = "Amazon Web Services";
    let selectionDropped = false;
    const providerFrom = (url: string) =>
      new URL(url, "http://x").searchParams.get("provider") ?? "";
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) => {
          const requested = providerFrom(url);
          if (requested === selectedProvider) {
            selectionDropped = true;
            // Contract-faithful valid-absent response: echo the request while
            // the unscoped selector list proves the provider is gone.
            return fakeResponse(
              200,
              summaryBody({
                provider: requested,
                providers: ["Microsoft"],
                currency: "",
                currencies: [],
                total: "0",
                keys: [],
              }),
            );
          }
          return fakeResponse(
            200,
            summaryBody({
              provider: "",
              providers: selectionDropped
                ? ["Microsoft"]
                : [selectedProvider, "Microsoft"],
            }),
          );
        },
        unit: (url) => {
          const requested = providerFrom(url);
          return fakeResponse(
            200,
            unitBody({
              provider: requested,
              providers: selectionDropped
                ? ["Microsoft"]
                : [selectedProvider, "Microsoft"],
              ...(requested
                ? {
                    currency: "",
                    currencies: [],
                    days: [],
                    period: { coveredDays: 0, cost: "0", quantity: "0" },
                  }
                : {}),
            }),
          );
        },
      }),
    );
    render(<Overview />);

    const selector = await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      within(selector).getByRole("button", { name: selectedProvider }),
    );

    await waitFor(() => {
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/summary?provider=Amazon%20Web%20Services",
      );
      expect(
        fetchedURLs().filter(
          (url) => url === "/api/v1/costs/summary?groupBy=provider",
        ).length,
      ).toBeGreaterThanOrEqual(2);
    });
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();
    expect(await screen.findByText("Spend by provider")).toBeTruthy();
  });

  it("refetches summary, anomalies, and unit economics for the selected currency", async () => {
    const requestedCurrency = (url: string) =>
      new URL(url, "http://x").searchParams.get("currency") ?? "EUR";
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) =>
          fakeResponse(
            200,
            summaryBody({
              currency: requestedCurrency(url),
              currencies: ["EUR", "USD"],
            }),
          ),
        anomalies: (url) =>
          fakeResponse(200, {
            ...anomaliesBody(),
            currency: requestedCurrency(url),
          }),
        unit: (url) =>
          fakeResponse(
            200,
            unitBody({
              currency: requestedCurrency(url),
              currencies: ["EUR", "USD"],
            }),
          ),
      }),
    );
    render(<Overview />);
    await screen.findByRole("group", { name: "Currency" });

    fireEvent.click(screen.getByRole("button", { name: "USD" }));

    await waitFor(() => {
      const urls = fetchedURLs();
      expect(urls).toContain(
        "/api/v1/costs/summary?groupBy=provider&currency=USD",
      );
      expect(urls).toContain("/api/v1/anomalies?currency=USD");
      expect(urls).toContain(
        "/api/v1/unit-economics/daily?metric=requests%20served&currency=USD",
      );
    });
    expect(
      screen.getByRole("button", { name: "USD" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("reconciles a dropped selection against currencies, not the echoed currency", async () => {
    const requestedCurrency = (url: string) =>
      new URL(url, "http://x").searchParams.get("currency");
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        summary: (url) => {
          const requested = requestedCurrency(url);
          if (requested === "USD") {
            // Contract-faithful valid-absent response: echo USD even though the
            // fresh in-range list has dropped it.
            return fakeResponse(
              200,
              summaryBody({
                currency: "USD",
                currencies: ["EUR"],
                total: "0",
                keys: [],
              }),
            );
          }
          if (requested === "EUR") {
            return fakeResponse(
              200,
              summaryBody({
                currency: "EUR",
                currencies: ["EUR"],
                total: "7",
                keys: [{ key: "A", total: "7" }],
              }),
            );
          }
          return fakeResponse(
            200,
            summaryBody({ currency: "EUR", currencies: ["EUR", "USD"] }),
          );
        },
        anomalies: (url) =>
          fakeResponse(200, {
            ...anomaliesBody(),
            currency: requestedCurrency(url) ?? "EUR",
          }),
        unit: (url) => {
          const requested = requestedCurrency(url);
          return fakeResponse(
            200,
            unitBody({
              currency: requested ?? "EUR",
              currencies: ["EUR"],
              ...(requested === "USD"
                ? {
                    days: [],
                    period: { coveredDays: 0, cost: "0", quantity: "0" },
                  }
                : {}),
            }),
          );
        },
      }),
    );
    render(<Overview />);
    await screen.findByRole("group", { name: "Currency" });

    fireEvent.click(screen.getByRole("button", { name: "USD" }));

    await waitFor(() => {
      const urls = fetchedURLs();
      expect(urls).toContain(
        "/api/v1/costs/summary?groupBy=provider&currency=USD",
      );
      expect(urls).toContain(
        "/api/v1/costs/summary?groupBy=provider&currency=EUR",
      );
      expect(urls).toContain("/api/v1/anomalies?currency=EUR");
      expect(urls).toContain(
        "/api/v1/unit-economics/daily?metric=requests%20served&currency=EUR",
      );
    });
    expect(screen.getAllByText("7.00").length).toBeGreaterThan(0);
    expect(screen.queryByRole("group", { name: "Currency" })).toBeNull();
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
    // Unit cost card shows data at display precision, exact in the title.
    const unitCost = await screen.findByText(UNIT_COST_DISPLAY);
    expect(unitCost.getAttribute("title")).toBe(`${UNIT_COST} USD`);
    // No loading skeletons left for anomalies/unit.
    expect(screen.queryByText("Loading overview…")).toBeNull();
    expect(document.querySelectorAll(".skeleton-card")).toHaveLength(0);
  });

  it("retries all three fetches from one error card's Retry", async () => {
    let failAnomalies = true;
    const fetchMock = mockRoutes({
      anomalies: () =>
        failAnomalies
          ? fakeResponse(500, null)
          : fakeResponse(200, anomaliesBody()),
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(/Failed to load anomalies/)).toBeTruthy();
    const callsBeforeRetry = fetchMock.mock.calls.length;

    failAnomalies = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    // The shared token re-runs ALL of the view's fetch effects.
    expect(await screen.findByText("Flagged days")).toBeTruthy();
    expect(screen.queryByText(/Failed to load anomalies/)).toBeNull();
    expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(
      callsBeforeRetry + 3,
    );
  });

  it("isolates anomalies 500: only card 4 degrades", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        anomalies: () => fakeResponse(500, null),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(PERIOD_TOTAL_DISPLAY)).toBeTruthy();
    expect(
      await screen.findByText(
        /Failed to load anomalies: GET \/api\/v1\/anomalies returned 500/,
      ),
    ).toBeTruthy();
    expect(await screen.findByText(UNIT_COST_DISPLAY)).toBeTruthy();
  });

  it("isolates metrics 500: only card 5 degrades", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        metrics: () => fakeResponse(500, null),
      }),
    );
    render(<Overview range={{ start: "2026-01-12", end: "2026-07-11" }} />);

    expect(await screen.findByText(PERIOD_TOTAL_DISPLAY)).toBeTruthy();
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
    expect(screen.getAllByText("500.00").length).toBeGreaterThan(0);
    expect(screen.getAllByText("300.00").length).toBeGreaterThan(0);
    // No signed delta column present under the movers card.
    const movers = screen.getByText("Largest providers").closest("article");
    expect(movers!.querySelector(".overview-key-delta")).toBeNull();
  });

  it("labels Change and Total while ranking movers by |delta| with signed display deltas", async () => {
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
    // Deltas carry an explicit sign; the exact wire value sits in the title.
    const negativeDelta = screen.getByText("-50.00");
    expect(negativeDelta.getAttribute("title")).toBe("-50 USD");
    expect(screen.getByText("+30.00")).toBeTruthy();
    expect(screen.getByText("+10.00")).toBeTruthy();
    expect(screen.getByText("Change")).toBeTruthy();
    expect(screen.getByText("Total")).toBeTruthy();

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

  it("renders a singleton unit-cost sparkline segment as a visible dot", async () => {
    vi.stubGlobal(
      "fetch",
      mockRoutes({
        unit: () =>
          fakeResponse(
            200,
            unitBody({
              days: [
                {
                  date: "2026-05-01",
                  cost: "1",
                  quantity: "2",
                  unitCost: "0.5",
                },
              ],
            }),
          ),
      }),
    );
    const { container } = render(<Overview />);

    await screen.findByRole("img", {
      name: "Daily unit cost sparkline for requests served",
    });
    const dot = container.querySelector(".overview-sparkline-dot");
    expect(dot).toBeTruthy();
    expect(dot?.getAttribute("cx")).toBe("100");
    expect(dot?.getAttribute("cy")).toBe("40");
    expect(dot?.getAttribute("r")).toBe("1.5");
    expect(container.querySelector(".overview-sparkline-path")).toBeNull();
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

    expect((await screen.findAllByText("99.00")).length).toBeGreaterThan(0);

    // Stale first response must not overwrite the new total.
    resolveSlow!(fakeResponse(200, summaryBody({ total: PERIOD_TOTAL })));
    await waitFor(() => {
      expect(screen.queryByText(PERIOD_TOTAL_DISPLAY)).toBeNull();
    });
    expect(screen.getAllByText("99.00").length).toBeGreaterThan(0);

    // Second range used rangeQuery form.
    expect(
      fetchedURLs().some((u) =>
        u.includes("/api/v1/costs/summary?start=2026-06-12&end=2026-07-11"),
      ),
    ).toBe(true);
    // Every request pins its mode independently. Unfiltered summaries group by
    // provider; filtered drill-down uses the service default and omits groupBy.
    const summaryURLs = fetchedURLs().filter((u) =>
      u.includes("/api/v1/costs/summary"),
    );
    expect(summaryURLs.length).toBeGreaterThan(1);
    for (const u of summaryURLs) {
      const filtered = new URL(u, "http://x").searchParams.has("provider");
      if (filtered) {
        expect(u).not.toContain("groupBy=provider");
      } else {
        expect(u).toContain("groupBy=provider");
      }
    }
  });

  it("downgrades stale error states synchronously when fetch params change", async () => {
    let holdRequests = false;
    const pending = new Promise<Response>(() => undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        holdRequests ? pending : Promise.resolve(fakeResponse(500, null)),
      ),
    );

    function RangeHarness() {
      const [range, setRange] = useState({
        start: "2026-05-01",
        end: "2026-05-31",
      });
      return (
        <>
          <button
            type="button"
            onClick={() => setRange({ start: "2026-06-01", end: "2026-06-30" })}
          >
            Change range
          </button>
          <Overview range={range} />
        </>
      );
    }

    render(<RangeHarness />);
    expect(await screen.findAllByRole("alert")).toHaveLength(3);
    expect(screen.getByText(/Failed to load cost summary/)).toBeTruthy();
    expect(screen.getByText(/Failed to load anomalies/)).toBeTruthy();
    expect(screen.getByText(/Failed to load unit cost/)).toBeTruthy();

    holdRequests = true;
    // A native click outside testing-library act commits the synchronous render
    // without first flushing Overview's passive effects. This pins the exact
    // mismatch frame where stale errors used to flash.
    screen.getByRole("button", { name: "Change range" }).click();
    await Promise.resolve();

    expect(screen.queryByText(/Failed to load cost summary/)).toBeNull();
    expect(screen.queryByText(/Failed to load anomalies/)).toBeNull();
    expect(screen.queryByText(/Failed to load unit cost/)).toBeNull();
    expect(screen.getByText("Loading overview…")).toBeTruthy();
    expect(document.querySelectorAll(".skeleton-card")).toHaveLength(3);
  });

  it("downgrades stale terminal states synchronously when currency changes", async () => {
    let holdRequests = false;
    const pending = new Promise<Response>(() => undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        if (holdRequests) return pending;

        const path = new URL(String(input), "http://localhost").pathname;
        if (path === "/api/v1/costs/summary") {
          return Promise.resolve(
            fakeResponse(
              200,
              summaryBody({
                currency: "EUR",
                currencies: ["EUR", "USD"],
              }),
            ),
          );
        }
        if (path === "/api/v1/anomalies") {
          return Promise.resolve(fakeResponse(500, null));
        }
        if (path === "/api/v1/business-metrics") {
          return Promise.resolve(fakeResponse(200, metricsBody()));
        }
        if (path === "/api/v1/unit-economics/daily") {
          return Promise.resolve(fakeResponse(500, null));
        }
        return Promise.resolve(fakeResponse(404, null));
      }),
    );

    render(<Overview />);
    await screen.findByRole("group", { name: "Currency" });
    expect(await screen.findAllByRole("alert")).toHaveLength(2);
    expect(screen.getByText(/Failed to load anomalies/)).toBeTruthy();
    expect(screen.getByText(/Failed to load unit cost/)).toBeTruthy();

    holdRequests = true;
    // As in the range regression above, leave the click outside testing-library
    // act so this observes the committed currency-mismatch frame before passive
    // effects replace each state with an explicit loading state.
    screen.getByRole("button", { name: "USD" }).click();
    await Promise.resolve();

    expect(screen.queryByText(/Failed to load anomalies/)).toBeNull();
    expect(screen.queryByText(/Failed to load unit cost/)).toBeNull();
    expect(screen.getByText("Loading overview…")).toBeTruthy();
    expect(document.querySelectorAll(".skeleton-card")).toHaveLength(3);
  });

  it("commits no stale card frame on a provider switch", async () => {
    let holdRequests = false;
    const pending = new Promise<Response>(() => undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        if (holdRequests) return pending;

        const path = new URL(String(input), "http://localhost").pathname;
        if (path === "/api/v1/costs/summary") {
          return Promise.resolve(
            fakeResponse(
              200,
              summaryBody({
                provider: "",
                providers: ["Amazon Web Services", "Microsoft"],
              }),
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
      }),
    );

    render(<Overview />);
    const selector = await screen.findByRole("group", { name: "Provider" });
    expect(await screen.findByText(PERIOD_TOTAL_DISPLAY)).toBeTruthy();
    expect(await screen.findByText("Flagged days")).toBeTruthy();
    expect(await screen.findByText(UNIT_COST_DISPLAY)).toBeTruthy();

    holdRequests = true;
    // Native click OUTSIDE act commits the synchronous provider-mismatch frame;
    // one microtask observes it before passive effects publish loading state.
    within(selector)
      .getByRole("button", { name: "Amazon Web Services" })
      .click();
    await Promise.resolve();

    expect(screen.queryByText(PERIOD_TOTAL_DISPLAY)).toBeNull();
    expect(screen.queryByText("Flagged days")).toBeNull();
    expect(screen.queryByText(UNIT_COST_DISPLAY)).toBeNull();
    expect(screen.getByText("Loading overview…")).toBeTruthy();
    expect(document.querySelectorAll(".skeleton-card")).toHaveLength(3);
  });
});
