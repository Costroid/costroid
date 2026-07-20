// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
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
    provider: "",
    providers: ["Amazon Web Services"],
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
  window.history.replaceState(null, "", "/");
});

describe("UnitEconomics", () => {
  it("applies hash filters to the first economics request from a loading frame", async () => {
    window.location.hash =
      "#groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=active+users";
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(
          fakeResponse(200, metricsBody("requests", "active users")),
        );
      }
      const params = new URL(url, "http://x").searchParams;
      return Promise.resolve(
        fakeResponse(200, {
          ...economicsBody(params.get("currency") ?? "", ["USD", "EUR"]),
          metric: params.get("metric") ?? "",
          provider: params.get("provider") ?? "",
          providers: ["Amazon Web Services", "Microsoft"],
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<UnitEconomics />);

    expect(screen.getByText("Loading business metrics…")).toBeTruthy();
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=active%20users&currency=USD&provider=Amazon%20Web%20Services",
        expect.anything(),
      ),
    );
    expect(window.location.hash).toBe(
      "#groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=active+users",
    );
  });

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
            provider: "",
            providers: ["Amazon Web Services"],
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
            provider: "",
            providers: ["Amazon Web Services"],
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
    await waitFor(() =>
      expect(window.location.hash).toBe("#metric=active+users"),
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

  it("renders the provider selector only for multiple providers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/business-metrics") {
          return Promise.resolve(fakeResponse(200, metricsBody("requests")));
        }
        const multiple = url.includes("start=2026-06-01");
        return Promise.resolve(
          fakeResponse(200, {
            ...economicsBody("USD", ["USD"]),
            provider: "",
            providers: multiple
              ? ["Amazon Web Services", "Microsoft"]
              : ["Amazon Web Services"],
          }),
        );
      }),
    );
    const { rerender } = render(
      <UnitEconomics range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    await screen.findByText("Covered days");
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();

    rerender(
      <UnitEconomics range={{ start: "2026-06-01", end: "2026-06-30" }} />,
    );
    const selector = await screen.findByRole("group", { name: "Provider" });
    expect(
      within(selector)
        .getByRole("button", { name: "All providers" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      Array.from(selector.querySelectorAll("button")).map(
        (button) => button.textContent,
      ),
    ).toEqual(["All providers", "Amazon Web Services", "Microsoft"]);
  });

  it("selecting a provider sends its encoded value", async () => {
    const providers = ["Amazon Web Services", "Microsoft"];
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody("requests")));
      }
      const requested =
        new URL(url, "http://x").searchParams.get("provider") ?? "";
      return Promise.resolve(
        fakeResponse(200, {
          ...economicsBody("USD", ["USD"]),
          provider: requested,
          providers,
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<UnitEconomics />);

    const selector = await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      within(selector).getByRole("button", { name: "Amazon Web Services" }),
    );

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&provider=Amazon%20Web%20Services",
        expect.anything(),
      ),
    );
    expect(
      within(screen.getByRole("group", { name: "Provider" }))
        .getByRole("button", { name: "Amazon Web Services" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("snaps a dropped provider selection to All without committing the stale body", async () => {
    const selectedProvider = "Amazon Web Services";
    let selectionDropped = false;
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody("requests")));
      }
      const requested =
        new URL(url, "http://x").searchParams.get("provider") ?? "";
      if (requested === selectedProvider) {
        selectionDropped = true;
        // The server echoes a valid absent provider while the unscoped list
        // proves it is no longer selectable.
        return Promise.resolve(
          fakeResponse(200, {
            ...economicsBody("", []),
            provider: requested,
            providers: ["Microsoft"],
            days: [
              {
                date: "2026-05-01",
                quantity: "999.999999999999999999",
              },
            ],
          }),
        );
      }
      return Promise.resolve(
        fakeResponse(200, {
          ...economicsBodyWithCost("USD", ["USD"], "7.000000000000000007"),
          provider: "",
          providers: selectionDropped
            ? ["Microsoft"]
            : [selectedProvider, "Microsoft"],
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<UnitEconomics />);

    const selector = await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      within(selector).getByRole("button", { name: selectedProvider }),
    );

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&provider=Amazon%20Web%20Services",
        expect.anything(),
      ),
    );
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(
          ([input]) =>
            String(input) === "/api/v1/unit-economics/daily?metric=requests",
        ).length,
      ).toBeGreaterThanOrEqual(2),
    );
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();
    expect(
      (await screen.findAllByTitle("7.000000000000000007 USD")).length,
    ).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText("999.999999999999999999")).toBeNull();
    await waitFor(() => expect(window.location.hash).toBe("#metric=requests"));
  });

  it("snaps an unknown hash metric to the first metric and recovers to ready", async () => {
    window.location.hash =
      "#currency=USD&provider=Amazon+Web+Services&metric=retired";
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, metricsBody("requests")));
      }
      const params = new URL(url, "http://x").searchParams;
      if (params.get("metric") === "retired") {
        return Promise.resolve(fakeResponse(404, null));
      }
      return Promise.resolve(
        fakeResponse(200, {
          ...economicsBody("USD", ["USD"]),
          metric: "requests",
          provider: params.get("provider") ?? "",
          providers: ["Amazon Web Services", "Microsoft"],
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<UnitEconomics />);

    expect(screen.getByText("Loading business metrics…")).toBeTruthy();
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=retired&currency=USD&provider=Amazon%20Web%20Services",
        expect.anything(),
      );
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/unit-economics/daily?metric=requests&currency=USD&provider=Amazon%20Web%20Services",
        expect.anything(),
      );
    });
    expect(await screen.findByText("Covered days")).toBeTruthy();
    expect(
      (
        screen.getByRole("combobox", {
          name: "Business metric",
        }) as HTMLSelectElement
      ).value,
    ).toBe("requests");
    expect(window.location.hash).toBe(
      "#currency=USD&provider=Amazon+Web+Services&metric=requests",
    );
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
    expect(screen.getByText("Loading unit economics…")).toBeTruthy();

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

  it("commits no stale content frame on a provider switch", async () => {
    let holdRequests = false;
    const pending = new Promise<Response>(() => undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/business-metrics") {
          return Promise.resolve(fakeResponse(200, metricsBody("requests")));
        }
        if (holdRequests) return pending;
        return Promise.resolve(
          fakeResponse(200, {
            ...economicsBodyWithCost("USD", ["USD"], "1.000000000000000001"),
            provider: "",
            providers: ["Amazon Web Services", "Microsoft"],
          }),
        );
      }),
    );

    render(<UnitEconomics />);
    const selector = await screen.findByRole("group", { name: "Provider" });
    expect(
      screen.getAllByTitle("1.000000000000000001 USD").length,
    ).toBeGreaterThan(0);

    holdRequests = true;
    // Native click OUTSIDE act commits the synchronous provider-mismatch frame;
    // one microtask observes it before the passive effect publishes loading.
    within(selector)
      .getByRole("button", { name: "Amazon Web Services" })
      .click();
    await Promise.resolve();

    expect(screen.queryAllByTitle("1.000000000000000001 USD")).toHaveLength(0);
    expect(screen.getByText("Loading unit economics…")).toBeTruthy();
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

  it("renders the daily unit-cost line chart with the table collapsed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/v1/business-metrics"
            ? fakeResponse(200, metricsBody("requests"))
            : fakeResponse(200, {
                metric: "requests",
                currency: "USD",
                currencies: ["USD"],
                provider: "",
                providers: ["Amazon Web Services"],
                days: [
                  {
                    date: "2026-05-01",
                    cost: "1",
                    quantity: "2",
                    unitCost: "0.5",
                  },
                  {
                    date: "2026-05-02",
                    cost: "2",
                    quantity: "2",
                    unitCost: "1",
                  },
                  // Uncovered day: no unitCost → a gap in the line.
                  { date: "2026-05-03", cost: "0" },
                  {
                    date: "2026-05-04",
                    cost: "3",
                    quantity: "3",
                    unitCost: "1",
                  },
                ],
                period: {
                  coveredDays: 3,
                  cost: "6",
                  quantity: "7",
                  unitCost: "0.857142857142857143",
                },
              }),
        ),
      ),
    );
    const { container } = render(<UnitEconomics />);

    expect(
      await screen.findByRole("img", { name: "Daily unit cost for requests" }),
    ).toBeTruthy();
    // Days 1–2 form a path; the gap isolates day 4 into a dot.
    expect(container.querySelectorAll(".viz-line-path")).toHaveLength(1);
    expect(container.querySelectorAll(".viz-line-dot")).toHaveLength(1);
    // The zero grid line sits on the plot baseline (y = HEIGHT - MARGIN.bottom
    // = 196) — pins the view's own tick scale against an orientation flip.
    expect(container.querySelector(".viz-baseline")?.getAttribute("y1")).toBe(
      "196",
    );
    // The full table survives behind a collapsed disclosure.
    expect(screen.getByText("View as table")).toBeTruthy();
    expect(container.querySelector("details.viz-table")).toBeTruthy();
    expect(container.querySelector("details.viz-table[open]")).toBeNull();
    // Exact values still reach the DOM inside the table.
    expect(screen.getAllByTitle("0.5 USD").length).toBeGreaterThan(0);
  });

  it("renders no chart when no day carries a unit cost", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input) === "/api/v1/business-metrics"
            ? fakeResponse(200, metricsBody("requests"))
            : fakeResponse(200, {
                ...economicsBody("USD", ["USD"]),
                // Covered-but-unmetered: days exist, none has a unitCost.
                // Pins the guard on NUMERIC values, not day count.
                days: [{ date: "2026-05-01", cost: "1" }],
              }),
        ),
      ),
    );
    render(<UnitEconomics />);

    expect(await screen.findByText("View as table")).toBeTruthy();
    expect(screen.queryByRole("img", { name: /Daily unit cost/ })).toBeNull();
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
