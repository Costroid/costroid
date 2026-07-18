// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { components } from "./api/schema";
import Sources, { formatUTCDateTime } from "./Sources";

type SyncStatusResponse = components["schemas"]["SyncStatusResponse"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

const readyStatus: SyncStatusResponse = {
  enabled: true,
  sources: [
    {
      name: "aws-prod",
      connector: "aws",
      tenant: "default",
      interval: "6h",
      nextRunAt: "2026-07-11T09:00:00Z",
      lastRun: {
        startedAt: "2026-07-11T03:00:00Z",
        finishedAt: "2026-07-11T03:00:47Z",
        outcome: "success",
        periodsProcessed: 1,
        periodsSkipped: 5,
        recordsIngested: 12904,
      },
      lastSuccessAt: "2026-07-11T03:00:47Z",
    },
    {
      name: "azure-main",
      connector: "azure",
      tenant: "default",
      interval: "6h",
      nextRunAt: "2026-07-11T09:00:00Z",
      lastRun: {
        startedAt: "2026-07-11T03:00:00Z",
        finishedAt: "2026-07-11T03:01:12Z",
        outcome: "success",
        periodsProcessed: 1,
        periodsSkipped: 5,
        recordsIngested: 8231,
      },
      lastSuccessAt: "2026-07-11T03:01:12Z",
    },
    {
      name: "openai-cost",
      connector: "openai-cost",
      tenant: "default",
      interval: "6h",
      nextRunAt: "2026-07-11T09:00:00Z",
      lastRun: {
        startedAt: "2026-07-11T03:00:00Z",
        finishedAt: "2026-07-11T03:00:05Z",
        outcome: "error",
        error: "openai cost API request failed: 429 Too Many Requests",
        periodsProcessed: 0,
        periodsSkipped: 0,
        recordsIngested: 0,
      },
      lastSuccessAt: "2026-07-10T21:00:30Z",
    },
    {
      name: "anthropic-cost",
      connector: "anthropic-cost",
      tenant: "default",
      interval: "6h",
      nextRunAt: "2026-07-11T09:00:00Z",
      lastRun: {
        startedAt: "2026-07-11T03:00:00Z",
        finishedAt: "2026-07-11T03:00:20Z",
        outcome: "success",
        periodsProcessed: 1,
        periodsSkipped: 5,
        recordsIngested: 1455,
      },
      lastSuccessAt: "2026-07-11T03:00:20Z",
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("formatUTCDateTime", () => {
  it("formats a fixed timestamp as absolute UTC without locale output", () => {
    expect(formatUTCDateTime("2026-07-11T12:34:56+03:00")).toBe(
      "2026-07-11 09:34 UTC",
    );
  });
});

describe("Sources", () => {
  it("renders the four-source ready state and failing source details", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, readyStatus))),
    );

    render(<Sources />);

    expect(await screen.findByText("aws-prod")).toBeTruthy();
    expect(screen.getByText("azure-main")).toBeTruthy();
    expect(screen.getAllByText("openai-cost")).toHaveLength(2);
    expect(screen.getAllByText("anthropic-cost")).toHaveLength(2);
    expect(screen.getByText("On")).toBeTruthy();
    expect(screen.getByText("12904")).toBeTruthy();
    expect(screen.getAllByText("error")).toHaveLength(1);
    expect(screen.getByText("error").classList.contains("outcome-error")).toBe(
      true,
    );
    expect(screen.getAllByText("2026-07-11 03:00 UTC")).toHaveLength(3);
    expect(screen.getAllByText("2026-07-11 09:00 UTC")).toHaveLength(4);
    expect(
      screen.getByText("openai cost API request failed: 429 Too Many Requests"),
    ).toBeTruthy();
    expect(screen.getByText("Last success: 2026-07-10 21:00 UTC")).toBeTruthy();
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/sync/status",
      expect.anything(),
    );
  });

  it("renders the loading state while the request is pending", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => undefined)),
    );

    const { container } = render(<Sources />);

    expect(screen.getByRole("status").textContent).toContain("Loading");
    expect(container.querySelector(".skeleton-card")).toBeTruthy();
  });

  it("renders an error and retries the request", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        calls += 1;
        return Promise.resolve(
          calls === 1
            ? fakeResponse(500, null)
            : fakeResponse(200, { enabled: true, sources: [] }),
        );
      }),
    );

    render(<Sources />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("GET /api/v1/sync/status returned 500");
    screen.getByRole("button", { name: "Retry" }).click();
    expect(await screen.findByText("No sync sources yet")).toBeTruthy();
    expect(calls).toBe(2);
  });

  it("renders the enabled empty state with setup guidance", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(fakeResponse(200, { enabled: true, sources: [] })),
      ),
    );

    render(<Sources />);

    expect(await screen.findByText("No sync sources yet")).toBeTruthy();
    expect(screen.getByText("costroid serve --sync")).toBeTruthy();
    expect(screen.getByText("sources.json")).toBeTruthy();
    expect(
      screen.queryByText("Scheduled ingestion is off for this instance."),
    ).toBeNull();
  });

  it("renders disabled history without a next run", async () => {
    const disabled: SyncStatusResponse = {
      enabled: false,
      sources: [
        {
          name: "history-only",
          connector: "aws",
          tenant: "tenant-a",
          lastRun: {
            startedAt: "2026-07-11T03:00:00Z",
            finishedAt: "2026-07-11T03:00:47Z",
            outcome: "success",
            periodsProcessed: 1,
            periodsSkipped: 5,
            recordsIngested: 42,
          },
          lastSuccessAt: "2026-07-11T03:00:47Z",
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, disabled))),
    );

    render(<Sources />);

    expect(await screen.findByText("history-only")).toBeTruthy();
    expect(screen.getByText("tenant-a")).toBeTruthy();
    expect(screen.getByText("Off")).toBeTruthy();
    expect(
      screen.getByText("Scheduled ingestion is off for this instance."),
    ).toBeTruthy();
    const row = screen.getByText("history-only").closest("tr");
    expect(row).toBeTruthy();
    expect(within(row as HTMLElement).getByText("42")).toBeTruthy();
    expect(within(row as HTMLElement).getByText("—")).toBeTruthy();
  });
});
