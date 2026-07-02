// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import App from "./App";

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

describe("App", () => {
  it("renders meta values fetched from the API", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(200, {
            name: "costroid",
            version: "0.1.0-test",
            focusVersion: "1.4",
          }),
        ),
      ),
    );

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

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
  });
});
