// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import DailyCosts from "./DailyCosts";
import { HEIGHT, MARGIN } from "./viz";
import type { components } from "./api/schema";

type DailyCostsResponse = components["schemas"]["DailyCosts"];
type Anomaly = components["schemas"]["Anomaly"];
type AnomaliesResponse = components["schemas"]["Anomalies"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : ""),
  } as Response;
}

// anomaliesBody builds a valid /api/v1/anomalies response with the given flags.
function anomaliesBody(flags: Anomaly[] = []): AnomaliesResponse {
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Mirrors MARGIN.top in DailyCosts.tsx: the top y-axis tick sits exactly
// here, so no bar segment may reach a smaller y.
const MARGIN_TOP = 20;

/** Topmost y coordinate drawn by a segmentPath `d` string. */
function pathTop(d: string): number {
  const move = /^M[\d.-]+,([\d.-]+)/.exec(d);
  if (!move) {
    throw new Error(`unparseable segment path: ${d}`);
  }
  let top = Number(move[1]);
  // Rounded-top segments start at y+r and arc up by r.
  const arc = / a([\d.-]+),/.exec(d);
  if (arc) {
    top -= Number(arc[1]);
  }
  return top;
}

function renderChart(costs: DailyCostsResponse): ReturnType<typeof render> {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
  );
  return render(<DailyCosts />);
}

function fetchedURLs(): string[] {
  return vi.mocked(fetch).mock.calls.map(([input]) => String(input));
}

describe("DailyCosts", () => {
  it("renders totals, legend, and table from the API response", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "9.3618",
      days: [
        {
          date: "2026-05-01",
          total: "4.6809",
          services: [
            { key: "AWS Lambda", cost: "0.1896" },
            { key: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { key: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
        {
          date: "2026-05-02",
          total: "4.6809",
          services: [
            { key: "AWS Lambda", cost: "0.1896" },
            { key: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { key: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    const { container } = render(<DailyCosts />);

    // Period total.
    expect(await screen.findByText("9.3618")).toBeTruthy();
    // Per-day totals on the column caps.
    expect(screen.getAllByText("4.6809").length).toBeGreaterThanOrEqual(2);
    // Legend structurally lists every service once.
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(3);
    // The chart itself is rendered.
    expect(
      screen.getByRole("img", { name: "Stacked daily cost by service" }),
    ).toBeTruthy();
    // The table view carries the exact per-service values.
    expect(screen.getAllByText("3.6288").length).toBeGreaterThanOrEqual(1);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/costs/daily",
      expect.anything(),
    );
  });

  it("fetches daily costs for a provided range", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect((await screen.findAllByText("1.00")).length).toBeGreaterThan(0);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31",
      expect.anything(),
    );
  });

  it("refetches and renders daily costs grouped by provider", async () => {
    const serviceCosts: DailyCostsResponse = {
      currency: "USD",
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    const providerCosts: DailyCostsResponse = {
      currency: "USD",
      total: "1.333333333333333334",
      days: [
        {
          date: "2026-05-01",
          total: "1.333333333333333334",
          services: [
            {
              key: "Amazon Web Services",
              cost: "1.000000000000000001",
            },
            { key: "OpenAI", cost: "0.333333333333333333" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(
            200,
            String(input).includes("groupBy=provider")
              ? providerCosts
              : serviceCosts,
          ),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    expect((await screen.findAllByText("AWS Lambda")).length).toBeGreaterThan(
      0,
    );
    fireEvent.click(screen.getByRole("button", { name: "Provider" }));

    expect(
      await screen.findByRole("img", {
        name: "Stacked daily cost by provider",
      }),
    ).toBeTruthy();
    expect(
      (await screen.findAllByText("Amazon Web Services")).length,
    ).toBeGreaterThan(0);
    expect((await screen.findAllByText("OpenAI")).length).toBeGreaterThan(0);
    expect(
      (await screen.findAllByText("0.333333333333333333")).length,
    ).toBeGreaterThan(0);
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(2);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/costs/daily?groupBy=provider"),
    );
    expect(fetchedURLs()).toContain("/api/v1/costs/daily");
  });

  it("appends provider grouping after the range query", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "Amazon Web Services", cost: "1.00" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect(
      (await screen.findAllByText("Amazon Web Services")).length,
    ).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Provider" }));

    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31&groupBy=provider",
      ),
    );
    expect(fetchedURLs()).toContain(
      "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31",
    );
  });

  it("refetches daily costs when the range changes", async () => {
    const response = (total: string): DailyCostsResponse => ({
      currency: "USD",
      total,
      days: [
        {
          date: "2026-05-01",
          total,
          services: [{ key: "AWS Lambda", cost: total }],
        },
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, response("1.00")))),
    );

    const { rerender } = render(
      <DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    expect((await screen.findAllByText("1.00")).length).toBeGreaterThan(0);
    rerender(<DailyCosts range={{ start: "2026-06-01", end: "2026-06-30" }} />);

    // Each param set fetches both costs and anomalies, so assert the new-range
    // costs URL was fetched rather than a brittle total call count.
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30",
        expect.anything(),
      ),
    );
  });

  it("shows loading synchronously while a grouping refetch is pending", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "1",
      days: [
        {
          date: "2026-05-01",
          total: "1",
          services: [{ key: "AWS Lambda", cost: "1" }],
        },
      ],
    };
    let resolveProviderCosts!: (response: Response) => void;
    const providerCosts = new Promise<Response>((resolve) => {
      resolveProviderCosts = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("groupBy=provider")) {
          return providerCosts; // the grouping refetch stays pending
        }
        return Promise.resolve(fakeResponse(200, costs));
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("img", { name: "Stacked daily cost by service" });

    fireEvent.click(screen.getByRole("button", { name: "Provider" }));
    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();

    resolveProviderCosts(fakeResponse(200, costs));
    expect(
      await screen.findByRole("img", {
        name: "Stacked daily cost by provider",
      }),
    ).toBeTruthy();
  });

  it("commits no [new heading + stale chart] frame on a grouping switch", async () => {
    // The pre-effect committed frame is what this pins. A native button.click()
    // OUTSIDE act commits React's synchronous re-render (groupBy already
    // "provider") but does NOT flush the passive effect (which would setState to
    // loading). After exactly ONE microtask the mismatch frame has committed;
    // asserting there catches the buggy [provider heading + old service chart]
    // frame. (An act-wrapped fireEvent flushes the effect first and makes this
    // vacuous — hence the raw dispatch + single microtask.)
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "1",
      days: [
        {
          date: "2026-05-01",
          total: "1",
          services: [{ key: "AWS Lambda", cost: "1" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );
    render(<DailyCosts />);
    await screen.findByRole("img", { name: "Stacked daily cost by service" });

    screen.getByRole("button", { name: "Provider" }).click();
    await Promise.resolve();

    // The committed frame shows the NEW heading and the loading state — never the
    // stale service chart.
    expect(
      screen.getByRole("heading", { name: "Daily cost by provider" }),
    ).toBeTruthy();
    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("keeps credit days inside the plot and reports net totals", async () => {
    // Day 1's positive segments sum to 5.00 while its net total is only
    // 1.00: with a net-derived y-scale the positive stack would overflow
    // the top tick.
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "3.50",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [
            { key: "Amazon Elastic Compute Cloud", cost: "3.00" },
            { key: "Amazon Simple Storage Service", cost: "2.00" },
            { key: "Savings Plan Credit", cost: "-4.00" },
          ],
        },
        {
          date: "2026-05-02",
          total: "2.50",
          services: [{ key: "Amazon Elastic Compute Cloud", cost: "2.50" }],
        },
      ],
    };
    const { container } = renderChart(costs);

    // Net day total and net grand total are reported as text.
    expect(await screen.findByText("3.50")).toBeTruthy();
    expect(screen.getByText(/Period total \(net\)/)).toBeTruthy();
    expect(screen.getAllByText("1.00").length).toBeGreaterThanOrEqual(1);

    // Only the positive segments render: 2 on day 1, 1 on day 2 — the
    // credit renders no bar segment.
    const paths = [...container.querySelectorAll("path")];
    expect(paths.length).toBe(3);
    for (const path of paths) {
      expect(path.querySelector("title")?.textContent).not.toContain(
        "Savings Plan Credit",
      );
    }

    // No segment renders above the top tick / outside the plot area.
    for (const path of paths) {
      expect(pathTop(path.getAttribute("d") ?? "")).toBeGreaterThanOrEqual(
        MARGIN_TOP - 1e-6,
      );
    }
  });

  it("formats y-axis ticks without float noise on small ranges", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "0.30",
      days: [
        {
          date: "2026-05-01",
          total: "0.30",
          services: [
            { key: "AWS Lambda", cost: "0.15" },
            { key: "Amazon Simple Storage Service", cost: "0.15" },
          ],
        },
      ],
    };
    renderChart(costs);

    // The 0.1-step ticks must read 0.1/0.2/0.3, never an accumulated
    // float like 0.30000000000000004.
    expect(await screen.findByText("0.3")).toBeTruthy();
    expect(screen.getByText("0.1")).toBeTruthy();
    expect(screen.queryByText(/0\.30000/)).toBeNull();
  });

  it("renders a positive sub-gap segment below a larger segment", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "100.000001",
      days: [
        {
          date: "2026-05-01",
          total: "100.000001",
          services: [
            { key: "Tiny lower segment", cost: "0.000001" },
            { key: "Large top segment", cost: "100" },
          ],
        },
      ],
    };
    const { container } = renderChart(costs);
    await screen.findByRole("img", { name: /Stacked daily cost/ });
    const tiny = [...container.querySelectorAll("path")].find(
      (path) =>
        path.querySelector("title")?.textContent ===
        "Tiny lower segment: 0.000001 USD (2026-05-01)",
    );
    expect(tiny).toBeTruthy();
  });

  it("anchors a clamped sub-pixel bottom segment to its bin bottom", async () => {
    // A 0.0001 service stacked UNDER a 2.4192 one: the tiny bottom segment's
    // natural height-minus-gap is far below the 1px floor, so drawnHeight clamps
    // up to 1. The clamped sliver's BOTTOM edge (y + drawnHeight) must land
    // exactly at its bin bottom — which, for the bottom segment, is the zero
    // baseline — never protruding below it. This asserts the drawn GEOMETRY, not
    // mere existence.
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "2.4193",
      days: [
        {
          date: "2026-05-01",
          total: "2.4193",
          services: [
            { key: "Tiny lower segment", cost: "0.0001" },
            { key: "Large top segment", cost: "2.4192" },
          ],
        },
      ],
    };
    const { container } = renderChart(costs);
    await screen.findByRole("img", { name: /Stacked daily cost/ });

    const tiny = [...container.querySelectorAll("path")].find(
      (path) =>
        path.querySelector("title")?.textContent ===
        "Tiny lower segment: 0.0001 USD (2026-05-01)",
    );
    expect(tiny).toBeTruthy();
    // The bottom (non-top) segment path is `M{x},{y} h{w} v{h} h{-w} Z`.
    const match = /^M[\d.-]+,([\d.-]+) h[\d.-]+ v([\d.-]+)/.exec(
      tiny?.getAttribute("d") ?? "",
    );
    expect(match).toBeTruthy();
    const y = Number(match![1]);
    const drawnHeight = Number(match![2]);
    // segmentBottom of the bottom segment is the plot baseline.
    const baseline = HEIGHT - MARGIN.bottom;
    expect(y + drawnHeight).toBe(baseline);
    // The sliver stays inside its bin: above the baseline, below the top tick.
    expect(y).toBeGreaterThanOrEqual(MARGIN.top);
    expect(y).toBeLessThanOrEqual(baseline);
  });

  it("keeps a service's color stable when the service set changes", async () => {
    const day = (services: { key: string; cost: string }[]) => ({
      currency: "USD",
      total: "9",
      days: [{ date: "2026-05-01", total: "9", services }],
    });

    const fillOf = (container: HTMLElement, service: string) => {
      const path = [...container.querySelectorAll("path")].find((p) =>
        p.querySelector("title")?.textContent?.startsWith(service),
      );
      expect(path).toBeTruthy();
      return path?.getAttribute("fill");
    };

    const first = renderChart(
      day([
        { key: "AWS Lambda", cost: "3" },
        { key: "Amazon Elastic Compute Cloud", cost: "3" },
        { key: "Amazon Simple Storage Service", cost: "3" },
      ]),
    );
    await screen.findByRole("img", { name: /Stacked daily cost/ });
    const before = fillOf(first.container, "AWS Lambda");
    cleanup();
    vi.unstubAllGlobals();

    const second = renderChart(
      day([
        { key: "AWS Lambda", cost: "9" },
        { key: "Amazon S3 Glacier", cost: "0.5" },
      ]),
    );
    await screen.findByRole("img", { name: /Stacked daily cost/ });
    const after = fillOf(second.container, "AWS Lambda");

    expect(before).toMatch(/^var\(--viz-series-\d\)$/);
    expect(after).toBe(before);
  });

  it("thins x-axis date labels to at most ~12 for long ranges", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "30",
      days: Array.from({ length: 30 }, (_, i) => ({
        date: `2026-05-${String(i + 1).padStart(2, "0")}`,
        total: "1",
        services: [{ key: "AWS Lambda", cost: "1" }],
      })),
    };
    const { container } = renderChart(costs);
    await screen.findByRole("img", { name: /Stacked daily cost/ });

    const dateLabels = [...container.querySelectorAll("text")].filter((t) =>
      /^\d{2}-\d{2}$/.test(t.textContent ?? ""),
    );
    expect(dateLabels.length).toBeGreaterThan(0);
    expect(dateLabels.length).toBeLessThanOrEqual(12);
  });

  it("shows the ingest hint when the store is empty", async () => {
    const empty: DailyCostsResponse = { currency: "", total: "0", days: [] };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, empty))),
    );

    render(<DailyCosts />);

    expect(await screen.findByText(/No cost data yet/)).toBeTruthy();
    expect(
      screen.getByText(/costroid ingest --connector aws-focus --path/),
    ).toBeTruthy();
    // The single-writer store means the server must be stopped first.
    expect(screen.getByText(/single process at a time/)).toBeTruthy();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<DailyCosts />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
  });

  it("refetches and renders daily costs grouped by allocation", async () => {
    const serviceCosts: DailyCostsResponse = {
      currency: "USD",
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    const allocationCosts: DailyCostsResponse = {
      currency: "USD",
      total: "32.7663",
      days: [
        {
          date: "2026-05-01",
          total: "32.7663",
          services: [
            { key: "compute", cost: "25.4016" },
            { key: "Unallocated", cost: "6.0375" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(
            200,
            String(input).includes("groupBy=allocation")
              ? allocationCosts
              : serviceCosts,
          ),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    expect((await screen.findAllByText("AWS Lambda")).length).toBeGreaterThan(
      0,
    );
    fireEvent.click(screen.getByRole("button", { name: "Allocation" }));

    expect(
      await screen.findByRole("img", {
        name: "Stacked daily cost by allocation",
      }),
    ).toBeTruthy();
    // The allocation label and the reserved Unallocated bucket both render, with
    // exact money.
    expect((await screen.findAllByText("compute")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("Unallocated")).length).toBeGreaterThan(
      0,
    );
    expect((await screen.findAllByText("25.4016")).length).toBeGreaterThan(0);
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(2);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/costs/daily?groupBy=allocation"),
    );
    expect(fetchedURLs()).toContain("/api/v1/costs/daily");
  });

  it("renders the server error body on an allocation 400", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(
            400,
            "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)",
          ),
        ),
      ),
    );

    render(<DailyCosts />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("returned 400");
    expect(alert.textContent).toContain("no allocation rules configured");
  });

  it("marks flagged days with a direction-aware overlay and a math tooltip", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "400",
      days: [
        {
          date: "2026-06-01",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
        {
          date: "2026-06-02",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
        {
          date: "2026-06-03",
          total: "200",
          services: [{ key: "AWS Lambda", cost: "200" }],
        },
      ],
    };
    const flags: Anomaly[] = [
      {
        date: "2026-06-03",
        scope: "total",
        direction: "increase",
        observed: "200",
        median: "100",
        mad: "2",
        scaledMad: "2.9652",
        threshold: "8.8956",
        deviation: "100",
      },
      {
        date: "2026-06-03",
        scope: "key",
        key: "AWS Lambda",
        direction: "increase",
        observed: "200",
        median: "100",
        mad: "2",
        scaledMad: "2.9652",
        threshold: "8.8956",
        deviation: "100",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(200, anomaliesBody(flags))
            : fakeResponse(200, costs),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);
    await screen.findByRole("img", { name: /Stacked daily cost/ });

    // A chart marker appears EXACTLY on the flagged day (one per day, keyed by
    // data-date) — never on the mundane days.
    await waitFor(() =>
      expect(
        [...container.querySelectorAll(".viz-chart .viz-anomaly")].map((m) =>
          m.getAttribute("data-date"),
        ),
      ).toEqual(["2026-06-03"]),
    );
    const marker = container.querySelector(".viz-chart .viz-anomaly")!;
    expect(marker.getAttribute("data-direction")).toBe("increase");
    // The tooltip carries the API's decimal strings verbatim (no reformatting).
    const tooltip = marker.querySelector("title")?.textContent ?? "";
    expect(tooltip).toContain("observed 200");
    expect(tooltip).toContain("median 100");
    expect(tooltip).toContain("threshold 8.8956");

    // The table row carries a badge per flag (total + key), listing scope/key and
    // direction.
    const badges = [...container.querySelectorAll(".viz-anomaly-badge")];
    expect(badges.map((b) => b.textContent)).toEqual([
      "total increase",
      "AWS Lambda increase",
    ]);
  });

  it("keeps the chart alive when the anomaly fetch fails", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "100",
      days: [
        {
          date: "2026-06-01",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(500, null)
            : fakeResponse(200, costs),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    // The chart still renders despite the anomaly fetch failing...
    expect(
      await screen.findByRole("img", { name: /Stacked daily cost/ }),
    ).toBeTruthy();
    // ...with a non-blocking notice and no markers, and NOT the cost error alert.
    await screen.findByText(/Anomaly overlay unavailable/);
    expect(container.querySelectorAll(".viz-chart .viz-anomaly")).toHaveLength(
      0,
    );
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
