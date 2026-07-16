// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import DailyTokens from "./DailyTokens";
import type { components } from "./api/schema";
import { MARGIN, WIDTH } from "./viz";

type DailyTokenUsage = components["schemas"]["DailyTokenUsage"];

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

describe("DailyTokens", () => {
  it("shows verbatim day values when a chart day receives focus", async () => {
    const quantity = "1234567890125856789";
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(200, [
            {
              date: "2026-05-01",
              serviceName: "OpenAI API",
              consumedUnit: "Tokens",
              consumedQuantity: quantity,
            },
          ] satisfies DailyTokenUsage[]),
        ),
      ),
    );

    render(<DailyTokens />);
    const hitTarget = await screen.findByLabelText("2026-05-01 token details");
    fireEvent.focus(hitTarget);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.textContent).toContain(`${quantity} Tokens`);
    expect(tooltip.textContent).toContain("OpenAI API");
  });

  it("renders totals, legend, and table from the API response", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1500000",
      },
      {
        date: "2026-05-01",
        serviceName: "claude-opus-4-6",
        consumedUnit: "Tokens",
        consumedQuantity: "2500000",
      },
      {
        date: "2026-05-02",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1000000",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    const { container } = render(<DailyTokens />);

    // Period total via BigInt: 1500000+2500000+1000000 = 5000000.
    expect(await screen.findByText("5000000")).toBeTruthy();
    // Day totals.
    expect(screen.getAllByText("4000000").length).toBeGreaterThanOrEqual(1);
    // Legend structurally lists every service once.
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(2);
    expect(
      screen.getByRole("group", {
        name: "Stacked daily token usage by service",
      }),
    ).toBeTruthy();
    // Table carries exact per-service values.
    expect(screen.getAllByText("1500000").length).toBeGreaterThanOrEqual(1);
    const table = container.querySelector(".viz-table table");
    expect(table).toBeTruthy();
    const headers = [...table!.querySelectorAll("thead th")].map(
      (th) => th.textContent ?? "",
    );
    const claudeIndex = headers.indexOf("claude-opus-4-6");
    const totalIndex = headers.indexOf("Total");
    const day2Row = [...table!.querySelectorAll("tbody tr")].find(
      (row) => row.querySelector("th")?.textContent === "2026-05-02",
    );
    expect(day2Row).toBeTruthy();
    const day2Cells = [...day2Row!.querySelectorAll("th, td")];
    expect(day2Cells[claudeIndex]?.textContent).toBe("—");
    expect(day2Cells[totalIndex]?.textContent).toBe("1000000");
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/usage/tokens/daily",
      expect.anything(),
    );
  });

  it("fetches daily token usage for a provided range", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1000",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<DailyTokens range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect((await screen.findAllByText("1000")).length).toBeGreaterThan(0);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/usage/tokens/daily?start=2026-05-01&end=2026-05-31",
      expect.anything(),
    );
  });

  it("refetches daily token usage when the range changes", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1000",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    const { rerender } = render(
      <DailyTokens range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    expect((await screen.findAllByText("1000")).length).toBeGreaterThan(0);
    rerender(
      <DailyTokens range={{ start: "2026-06-01", end: "2026-06-30" }} />,
    );

    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/api/v1/usage/tokens/daily?start=2026-06-01&end=2026-06-30",
      expect.anything(),
    );
  });

  it("displays quantities above 2^53 as exact strings (never Number())", async () => {
    // Two services with DISTINCT >2^53 integer quantities so no single
    // cell equals the day/period total.
    const a = "1234567890125856789";
    const b = "9876543210987654321";
    // Number(a) rounds to this exact form (JS only switches to exp at ≥1e21).
    const roundedA = "1234567890125856800";
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: a,
      },
      {
        date: "2026-05-01",
        serviceName: "claude-opus-4-6",
        consumedUnit: "Tokens",
        consumedQuantity: b,
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<DailyTokens />);

    // Positive: exact string appears (cell + tooltip → getAllByText).
    expect((await screen.findAllByText(a)).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(b).length).toBeGreaterThanOrEqual(1);
    // BigInt day/period total.
    expect(
      screen.getAllByText("11111111101113511110").length,
    ).toBeGreaterThanOrEqual(1);
    // Negative: the Number()-rounded form must never appear.
    expect(screen.queryAllByText(roundedA)).toHaveLength(0);
  });

  it("shows the AI-connector ingest hint when the store is empty", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, []))),
    );

    render(<DailyTokens />);

    expect(await screen.findByText(/No token usage yet/)).toBeTruthy();
    expect(screen.getByText(/costroid credentials set/)).toBeTruthy();
    expect(screen.getByText(/openai-cost\|anthropic-cost/)).toBeTruthy();
    expect(screen.getByText(/single process at a time/)).toBeTruthy();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<DailyTokens />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
  });

  it("omits totals for non-integer quantities without crashing", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1.5",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<DailyTokens />);

    expect(
      await screen.findByRole("group", {
        name: "Stacked daily token usage by service",
      }),
    ).toBeTruthy();
    expect(screen.getAllByText("1.5").length).toBeGreaterThanOrEqual(1);
    expect(screen.queryByText(/Period total/)).toBeNull();
  });

  it("uses compact y-axis labels for large token magnitudes", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1200000000000000000",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<DailyTokens />);
    await screen.findByRole("group", {
      name: "Stacked daily token usage by service",
    });
    // Compact SI form (e.g. 1.2P) — not the full 19-digit string on the axis.
    // The exact data value still appears as the cap / table cell.
    expect(
      screen.getAllByText("1200000000000000000").length,
    ).toBeGreaterThanOrEqual(1);
    // At least one tick label should use a compact suffix.
    const texts = [...document.querySelectorAll("text")].map(
      (t) => t.textContent ?? "",
    );
    expect(texts.some((t) => /[kMGTP]$/.test(t))).toBe(true);
  });

  it("clamps the rightmost cap label so it does not clip the viewBox", async () => {
    // Right-edge case: contiguous days with only the last carrying an integer
    // total (prior days use non-integers so day totals are null and no prior
    // caps compete). Without clamp the last band-center + half-width exceeds
    // WIDTH - MARGIN.right for an 8-digit label.
    const big = "12345678"; // 8 digits → est width ≈ 49.6
    const rows: DailyTokenUsage[] = Array.from({ length: 14 }, (_, i) => {
      const day = String(i + 1).padStart(2, "0");
      return {
        date: `2026-05-${day}`,
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: i === 13 ? big : "1.5",
      };
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    const { container } = render(<DailyTokens />);
    await screen.findByRole("group", {
      name: "Stacked daily token usage by service",
    });

    // textContent includes the nested <title>, so match on the data string.
    const caps = [
      ...container.querySelectorAll("text.viz-cap"),
    ] as SVGTextElement[];
    const bigCap = caps.find((t) => (t.textContent ?? "").includes(big));
    expect(bigCap).toBeTruthy();
    const x = Number(bigCap!.getAttribute("x"));
    const estWidth = Math.max(7, big.length * 6.2);
    expect(x + estWidth / 2).toBeLessThanOrEqual(WIDTH - MARGIN.right);
  });

  it("commits no [new range + stale chart] frame on a range change", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1500000",
      },
    ];
    const pending = new Promise<Response>(() => undefined);
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        calls += 1;
        return calls === 1 ? Promise.resolve(fakeResponse(200, rows)) : pending;
      }),
    );

    function Harness() {
      const [range, setRange] = useState({ start: "", end: "" });
      return (
        <>
          <button
            type="button"
            onClick={() => setRange({ start: "2026-05-01", end: "2026-05-02" })}
          >
            Change range
          </button>
          <DailyTokens range={range} />
        </>
      );
    }
    render(<Harness />);
    await screen.findByRole("group", {
      name: "Stacked daily token usage by service",
    });

    // Observe the committed frame before passive effects run: the held data
    // was fetched for the old range, so the render must derive loading
    // synchronously instead of showing the stale chart beside the new range.
    screen.getByRole("button", { name: "Change range" }).click();
    await Promise.resolve();

    expect(screen.getByText("Loading daily token usage…")).toBeTruthy();
    expect(screen.queryByRole("group", { name: /token usage/ })).toBeNull();
  });

  it("retries the fetch when the error card's Retry is pressed", async () => {
    const rows: DailyTokenUsage[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        consumedUnit: "Tokens",
        consumedQuantity: "1500000",
      },
    ];
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        calls += 1;
        return calls === 1
          ? Promise.reject(new Error("boom"))
          : Promise.resolve(fakeResponse(200, rows));
      }),
    );

    render(<DailyTokens />);
    expect(
      await screen.findByText(/Failed to load daily token usage/),
    ).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(
      await screen.findByRole("group", {
        name: "Stacked daily token usage by service",
      }),
    ).toBeTruthy();
    expect(calls).toBe(2);
  });

  it("associates and Escape-dismisses the day tooltip on keyboard focus", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(200, [
            {
              date: "2026-05-01",
              serviceName: "OpenAI API",
              consumedUnit: "Tokens",
              consumedQuantity: "1500000",
            },
          ] satisfies DailyTokenUsage[]),
        ),
      ),
    );

    render(<DailyTokens />);
    const hitTarget = await screen.findByLabelText("2026-05-01 token details");
    fireEvent.focus(hitTarget);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.id).toBe("tokens-tooltip");
    expect(hitTarget.getAttribute("aria-describedby")).toBe("tokens-tooltip");

    fireEvent.keyDown(hitTarget, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(hitTarget.getAttribute("aria-describedby")).toBeNull();
  });
});
