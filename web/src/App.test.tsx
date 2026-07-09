// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import App from "./App";

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

const emptyCosts = { currency: "", total: "0", days: [] };

function mockFetch() {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/v1/meta") {
      return Promise.resolve(
        fakeResponse(200, {
          name: "costroid",
          version: "0.1.0-test",
          focusVersion: "1.4",
        }),
      );
    }
    if (url === "/api/v1/usage/tokens/daily") {
      return Promise.resolve(fakeResponse(200, []));
    }
    if (url === "/api/v1/usage/metrics/daily") {
      return Promise.resolve(fakeResponse(200, []));
    }
    // costs/daily and any other path
    return Promise.resolve(fakeResponse(200, emptyCosts));
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders meta values fetched from the API", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    expect(await screen.findByText("costroid")).toBeTruthy();
    expect(screen.getByText("0.1.0-test")).toBeTruthy();
    expect(screen.getByText("1.4")).toBeTruthy();
    expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything());
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<App />);

    const alerts = await screen.findAllByRole("alert");
    expect(alerts.some((alert) => alert.textContent?.includes("500"))).toBe(
      true,
    );
  });

  it("defaults to the Costs view", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Daily cost by service" }),
    ).toBeTruthy();
    const costsTab = screen.getByRole("tab", { name: "Costs" });
    expect(costsTab.getAttribute("aria-selected")).toBe("true");
  });

  it("switches to the Tokens view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    await screen.findByRole("heading", { name: "Daily cost by service" });

    fireEvent.click(screen.getByRole("tab", { name: "Tokens" }));

    expect(
      await screen.findByRole("heading", {
        name: "Daily token usage by service",
      }),
    ).toBeTruthy();
    expect(
      screen.getByRole("tab", { name: "Tokens" }).getAttribute("aria-selected"),
    ).toBe("true");
    // Empty store → AI-connector ingest hint.
    expect(await screen.findByText(/No token usage yet/)).toBeTruthy();
  });

  it("switches to the Usage view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    await screen.findByRole("heading", { name: "Daily cost by service" });

    fireEvent.click(screen.getByRole("tab", { name: "Usage" }));

    expect(
      await screen.findByRole("heading", { name: "Daily usage metrics" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("tab", { name: "Usage" }).getAttribute("aria-selected"),
    ).toBe("true");
    expect(await screen.findByText(/No usage metrics yet/)).toBeTruthy();
  });
});
