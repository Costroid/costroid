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

function metricsBody(...names: string[]) {
  return {
    metrics: names.map((name) => ({
      name,
      firstDay: "2026-05-01",
      lastDay: "2026-05-02",
    })),
  };
}

function economicsBody(currency: string, currencies: string[]) {
  return {
    metric: "requests",
    currency,
    currencies,
    days: [],
    period: { coveredDays: 0, cost: "0", quantity: "0" },
  };
}

function economicsBodyWithCost(
  currency: string,
  currencies: string[],
  cost: string,
) {
  return {
    ...economicsBody(currency, currencies),
    period: { coveredDays: 1, cost, quantity: "1", unitCost: cost },
  };
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
    // Costs render at display precision; the exact value moves to the title.
    const dayCost = screen.getByTitle("1.000000000000000001 USD");
    expect(dayCost.textContent).toBe("1.00");
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

  it("hides the currency selector for a singleton currency list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/v1/business-metrics"
            ? fakeResponse(200, metricsBody("requests"))
            : fakeResponse(200, economicsBody("USD", ["USD"])),
        ),
      ),
    );

    render(<UnitEconomics />);
    await screen.findByText("Covered days");
    expect(screen.queryByRole("group", { name: "Currency" })).toBeNull();
  });

  it("renders the currency selector beside the business metric for multiple currencies", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/v1/business-metrics"
            ? fakeResponse(200, metricsBody("requests"))
            : fakeResponse(200, economicsBody("EUR", ["EUR", "USD"])),
        ),
      ),
    );

    render(<UnitEconomics />);
    const group = await screen.findByRole("group", { name: "Currency" });
    const metricSelect = screen.getByRole("combobox", {
      name: "Business metric",
    });
    expect(group.closest(".view-heading")).not.toBeNull();
    expect(metricSelect.closest(".view-heading")).toBe(
      group.closest(".view-heading"),
    );
    expect(
      screen.getByRole("button", { name: "EUR" }).getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByRole("button", { name: "USD" }).getAttribute("aria-pressed"),
    ).toBe("false");
  });

  it("selecting a currency refetches unit economics with the encoded currency", async () => {
    let resolveUSD!: (response: Response) => void;
    const heldUSD = new Promise<Response>((resolve) => {
      resolveUSD = resolve;
    });
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody("requests")));
      }
      if (url.includes("currency=USD")) return heldUSD;
      return Promise.resolve(
        fakeResponse(
          200,
          economicsBodyWithCost("EUR", ["EUR", "USD"], "1.000000000000000001"),
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<UnitEconomics />);
    await screen.findByRole("group", { name: "Currency" });
    expect(
      screen.getAllByTitle("1.000000000000000001 EUR").length,
    ).toBeGreaterThan(0);

    // Observe the committed currency-mismatch frame before passive effects run.
    screen.getByRole("button", { name: "USD" }).click();
    await Promise.resolve();

    expect(screen.queryByTitle("1.000000000000000001 EUR")).toBeNull();
    expect(screen.getByLabelText("Loading unit economics…")).toBeTruthy();

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&currency=USD",
        expect.anything(),
      ),
    );
    resolveUSD(
      fakeResponse(
        200,
        economicsBodyWithCost("USD", ["EUR", "USD"], "2.000000000000000002"),
      ),
    );
    expect(
      (await screen.findAllByTitle("2.000000000000000002 USD")).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getByRole("button", { name: "USD" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("reconciles a dropped selection against currencies rather than the echoed currency", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody("requests")));
      }
      if (url.includes("currency=USD")) {
        // Contract-faithful valid-absent response: the server echoes the requested
        // USD, while the availability list says only EUR remains selectable.
        return Promise.resolve(
          fakeResponse(200, {
            ...economicsBody("USD", ["EUR"]),
            // With costs absent, quantity-only days remain contract-valid but
            // must never be committed while the selector reconciles to EUR.
            days: [
              {
                date: "2026-05-01",
                quantity: "999.999999999999999999",
              },
            ],
          }),
        );
      }
      if (url.includes("currency=EUR")) {
        return Promise.resolve(
          fakeResponse(
            200,
            economicsBodyWithCost("EUR", ["EUR"], "7.000000000000000007"),
          ),
        );
      }
      return Promise.resolve(
        fakeResponse(200, economicsBody("EUR", ["EUR", "USD"])),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<UnitEconomics />);
    await screen.findByRole("group", { name: "Currency" });
    fireEvent.click(screen.getByRole("button", { name: "USD" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&currency=USD",
        expect.anything(),
      ),
    );
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&currency=EUR",
        expect.anything(),
      ),
    );
    await waitFor(() =>
      expect(screen.queryByRole("group", { name: "Currency" })).toBeNull(),
    );
    expect(
      (await screen.findAllByTitle("7.000000000000000007 EUR")).length,
    ).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText("999.999999999999999999")).toBeNull();
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
