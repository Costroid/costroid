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
import UnitEconomics from "./UnitEconomics";

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

describe("UnitEconomics", () => {
  it("fetches encoded metric and range, then renders exact values and gaps", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/business-metrics") {
          return Promise.resolve(
            fakeResponse(200, {
              metrics: [
                {
                  name: "a b&c",
                  firstDay: "2026-05-01",
                  lastDay: "2026-05-03",
                },
              ],
            }),
          );
        }
        return Promise.resolve(
          fakeResponse(200, {
            metric: "a b&c",
            currency: "USD",
            currencies: ["USD"],
            days: [
              { date: "2026-05-01", cost: "1.000000000000000001" },
              {
                date: "2026-05-02",
                cost: "0.000000000000000123",
                quantity: "1",
                unitCost: "0.000000000000000123",
              },
              { date: "2026-05-03", quantity: "12345678901234567.89" },
            ],
            period: {
              coveredDays: 1,
              cost: "0.000000000000000123",
              quantity: "1",
              unitCost: "0.000000000000000123",
            },
          }),
        );
      }),
    );

    render(
      <UnitEconomics range={{ start: "2026-05-01", end: "2026-05-03" }} />,
    );

    expect(
      (await screen.findAllByText("0.000000000000000123")).length,
    ).toBeGreaterThanOrEqual(3);
    expect(screen.getByText("12345678901234567.89")).toBeTruthy();
    expect(screen.getAllByText("—")).toHaveLength(4);
    expect(screen.getByText("1.000000000000000001")).toBeTruthy();
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/business-metrics",
      expect.anything(),
    );
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/unit-economics/daily?metric=a%20b%26c&start=2026-05-01&end=2026-05-03",
      expect.anything(),
    );
  });

  it("refetches when the native metric select changes", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/business-metrics") {
          return Promise.resolve(
            fakeResponse(200, {
              metrics: [
                {
                  name: "requests",
                  firstDay: "2026-05-01",
                  lastDay: "2026-05-02",
                },
                {
                  name: "active users",
                  firstDay: "2026-05-01",
                  lastDay: "2026-05-02",
                },
              ],
            }),
          );
        }
        const metric = url.includes("active%20users")
          ? "active users"
          : "requests";
        return Promise.resolve(
          fakeResponse(200, {
            metric,
            currency: "USD",
            currencies: ["USD"],
            days: [],
            period: { coveredDays: 0, cost: "0", quantity: "0" },
          }),
        );
      }),
    );
    render(<UnitEconomics />);
    await screen.findByRole("combobox", { name: "Business metric" });
    fireEvent.change(screen.getByRole("combobox"), {
      target: { value: "active users" },
    });
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=active%20users",
        expect.anything(),
      ),
    );
  });

  it("shows the CLI empty state", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, { metrics: [] }))),
    );
    render(<UnitEconomics />);
    expect(await screen.findByText(/No business metrics yet/)).toBeTruthy();
    expect(screen.getByText(/costroid metrics import --path/)).toBeTruthy();
  });

  it("shows list and unit-economics errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );
    const first = render(<UnitEconomics />);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "business metrics",
    );
    first.unmount();
    vi.unstubAllGlobals();

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/v1/business-metrics"
            ? fakeResponse(200, {
                metrics: [
                  {
                    name: "requests",
                    firstDay: "2026-05-01",
                    lastDay: "2026-05-01",
                  },
                ],
              })
            : fakeResponse(500, null),
        ),
      ),
    );
    render(<UnitEconomics />);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "unit economics",
    );
  });
});
