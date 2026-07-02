// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import DailyCosts from "./DailyCosts";
import type { components } from "./api/schema";

type DailyCostsResponse = components["schemas"]["DailyCosts"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
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
            { serviceName: "AWS Lambda", cost: "0.1896" },
            { serviceName: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { serviceName: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
        {
          date: "2026-05-02",
          total: "4.6809",
          services: [
            { serviceName: "AWS Lambda", cost: "0.1896" },
            { serviceName: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { serviceName: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts />);

    // Period total.
    expect(await screen.findByText("9.3618")).toBeTruthy();
    // Per-day totals on the column caps.
    expect(screen.getAllByText("4.6809").length).toBeGreaterThanOrEqual(2);
    // Legend lists every service once.
    expect(screen.getAllByText("AWS Lambda").length).toBeGreaterThanOrEqual(1);
    expect(
      screen.getAllByText("Amazon Elastic Compute Cloud").length,
    ).toBeGreaterThanOrEqual(1);
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
            { serviceName: "Amazon Elastic Compute Cloud", cost: "3.00" },
            { serviceName: "Amazon Simple Storage Service", cost: "2.00" },
            { serviceName: "Savings Plan Credit", cost: "-4.00" },
          ],
        },
        {
          date: "2026-05-02",
          total: "2.50",
          services: [
            { serviceName: "Amazon Elastic Compute Cloud", cost: "2.50" },
          ],
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
            { serviceName: "AWS Lambda", cost: "0.15" },
            { serviceName: "Amazon Simple Storage Service", cost: "0.15" },
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

  it("keeps a service's color stable when the service set changes", async () => {
    const day = (services: { serviceName: string; cost: string }[]) => ({
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
        { serviceName: "AWS Lambda", cost: "3" },
        { serviceName: "Amazon Elastic Compute Cloud", cost: "3" },
        { serviceName: "Amazon Simple Storage Service", cost: "3" },
      ]),
    );
    await screen.findByRole("img", { name: /Stacked daily cost/ });
    const before = fillOf(first.container, "AWS Lambda");
    cleanup();
    vi.unstubAllGlobals();

    const second = renderChart(
      day([
        { serviceName: "AWS Lambda", cost: "9" },
        { serviceName: "Amazon S3 Glacier", cost: "0.5" },
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
        services: [{ serviceName: "AWS Lambda", cost: "1" }],
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
});
