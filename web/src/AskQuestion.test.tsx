// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "./api/schema";
import AskQuestion from "./AskQuestion";

type QueryPlan = components["schemas"]["QueryPlan"];

const basePlan: QueryPlan = {
  endpoint: "costs-daily",
  start: "2026-06-01",
  end: "2026-06-30",
  groupBy: "service",
  tagKey: null,
  currency: null,
  provider: "AWS",
  metric: null,
};

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

function renderAsk(onNavigate = vi.fn(() => true)) {
  const onAnnouncement = vi.fn();
  const view = render(
    <AskQuestion
      onNavigate={onNavigate}
      onAnnouncement={onAnnouncement}
      dismissToken={0}
    />,
  );
  const dismiss = () =>
    view.rerender(
      <AskQuestion
        onNavigate={onNavigate}
        onAnnouncement={onAnnouncement}
        dismissToken={1}
      />,
    );
  return { onNavigate, onAnnouncement, dismiss };
}

function enterAndSubmit(question = "What did AWS cost?") {
  const input = screen.getByLabelText("Ask a question");
  fireEvent.change(input, { target: { value: question } });
  fireEvent.submit(input.closest("form")!);
  return input;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
  document.title = "";
  window.history.replaceState(null, "", "/");
});

describe("AskQuestion data boundary", () => {
  it("posts exactly the typed question without placing it in browser state", async () => {
    const question = "private-team incident cost";
    const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(response(200, basePlan)),
    );
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState(null, "", "/#view=overview");
    document.title = "Costroid dashboard";
    const href = window.location.href;
    const historyLength = window.history.length;
    const pushSpy = vi.spyOn(window.history, "pushState");
    const consoleSpies = (
      ["debug", "error", "info", "log", "warn"] as const
    ).map((method) => vi.spyOn(console, method));
    const { onNavigate } = renderAsk();
    const input = screen.getByLabelText("Ask a question");
    expect(input.getAttribute("name")).toBeNull();
    fireEvent.change(input, { target: { value: question } });
    const event = new Event("submit", { bubbles: true, cancelable: true });

    act(() => {
      input.closest("form")!.dispatchEvent(event);
    });

    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [path, init] = fetchMock.mock.calls[0]!;
    if (!init) throw new Error("missing request init");
    expect(path).toBe("/api/v1/query");
    expect(init.method).toBe("POST");
    expect(init.headers).toEqual({ "Content-Type": "application/json" });
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(Object.keys(body)).toEqual(["question"]);
    expect(body).toEqual({ question });
    await waitFor(() => expect(onNavigate).toHaveBeenCalledTimes(1));
    expect(window.location.href).toBe(href);
    expect(window.history.length).toBe(historyLength);
    expect(pushSpy).not.toHaveBeenCalled();
    expect(localStorage.getItem(question)).toBeNull();
    expect(sessionStorage.getItem(question)).toBeNull();
    expect(JSON.stringify({ ...localStorage })).not.toContain(question);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(question);
    expect(document.title).toBe("Costroid dashboard");
    for (const spy of consoleSpies) expect(spy).not.toHaveBeenCalled();
  });

  it("refuses a multibyte question over 8192 bytes before fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    renderAsk();
    const question = "€".repeat(4097);
    expect(question.length).toBeLessThan(8192);
    expect(new TextEncoder().encode(question).byteLength).toBeGreaterThan(8192);

    enterAndSubmit(question);

    expect(
      await screen.findByText(
        "That question is too long. Questions are limited to 8192 bytes.",
      ),
    ).toBeTruthy();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("AskQuestion plan handling", () => {
  it("renders a costs interpretation from applied plan fields", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response(200, basePlan))),
    );
    renderAsk();
    enterAndSubmit();

    expect(
      await screen.findByText(
        "Your question was read as: costs for 2026-06-01 to 2026-06-30, grouped by service, provider AWS.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("omits fields that the resolved Tokens view does not apply", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(200, {
            ...basePlan,
            endpoint: "tokens",
            groupBy: null,
            provider: "model-authored provider",
          }),
        ),
      ),
    );
    const { onNavigate } = renderAsk();
    enterAndSubmit();

    const caption = await screen.findByText(
      "Your question was read as: tokens for 2026-06-01 to 2026-06-30.",
    );
    expect(caption.textContent).not.toContain("provider");
    expect(onNavigate).toHaveBeenCalledWith({
      view: "tokens",
      start: "2026-06-01",
      end: "2026-06-30",
    });
  });

  it("rejects an unknown endpoint without navigating", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(response(200, { ...basePlan, endpoint: "unknown" })),
      ),
    );
    const { onNavigate } = renderAsk();
    enterAndSubmit();

    expect(
      await screen.findByText(
        "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.",
      ),
    ).toBeTruthy();
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it.each(["provider", "tagKey", "metric"] as const)(
    "rejects an overlong model-authored %s before navigation",
    async (field) => {
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve(
            response(200, { ...basePlan, [field]: "x".repeat(8193) }),
          ),
        ),
      );
      const { onNavigate } = renderAsk();
      enterAndSubmit();

      expect(
        await screen.findByText(
          "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.",
        ),
      ).toBeTruthy();
      expect(onNavigate).not.toHaveBeenCalled();
    },
  );
});

describe("AskQuestion status messages", () => {
  it.each([
    [
      400,
      "Could not send that question. Reload the page and try again.",
      false,
    ],
    [
      413,
      "Could not send that question. Reload the page and try again.",
      false,
    ],
    [
      429,
      "Too many questions are being translated right now. Try again in a few seconds.",
      true,
    ],
    [
      500,
      "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.",
      true,
    ],
    [
      503,
      "Natural-language questions are not configured on this instance.",
      false,
    ],
    [
      401,
      "The server rejected the request. If a proxy signs you in to Costroid, sign in again and reload the page.",
      false,
    ],
  ] as const)(
    "maps HTTP %i to its exact message and retry posture",
    async (status, message, retry) => {
      vi.stubGlobal(
        "fetch",
        vi.fn(() => Promise.resolve(response(status, "server detail"))),
      );
      renderAsk();
      enterAndSubmit();

      expect(await screen.findByText(message)).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Try again" }) !== null).toBe(
        retry,
      );
    },
  );

  it.each([
    "metadata discovery failed",
    "prompt encoding failed",
    "model transport failed",
    "model reply parse failed",
    "model reply validation failed",
  ])("collapses the 500 body %s into one message", async (body) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response(500, body))),
    );
    renderAsk();
    enterAndSubmit();

    expect(
      await screen.findByText(
        "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText(body)).toBeNull();
  });
});

it("does not blame the question when the request never reached the handler", async () => {
  // A rejected fetch and a 502 both mean the translator was never asked, so
  // neither may advise the reader to rephrase a question the server never saw.
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
  );
  renderAsk();
  enterAndSubmit();
  expect(
    await screen.findByText(
      "Could not reach the server. Check that it is still running, then try again.",
    ),
  ).toBeTruthy();
  expect(
    screen.queryByText(
      "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.",
    ),
  ).toBeNull();
});

it("stops asserting an answer once the reader navigates away from it", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(response(200, basePlan))),
  );
  const { dismiss } = renderAsk();
  enterAndSubmit();
  const caption = await screen.findByText(/^Your question was read as:/);
  expect(caption).toBeTruthy();
  dismiss();
  await waitFor(() =>
    expect(screen.queryByText(/^Your question was read as:/)).toBeNull(),
  );
});
