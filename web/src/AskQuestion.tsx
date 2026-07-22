// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useRef, useState, type FormEvent } from "react";
import type { components } from "./api/schema";
import { postQuery } from "./api";

type InsightLink = components["schemas"]["InsightLink"];
type QueryPlan = components["schemas"]["QueryPlan"];

type Result = {
  link: InsightLink;
  caption: string;
};

const MAX_TEXT_BYTES = 8192;
const SEND_ERROR =
  "Could not send that question. Reload the page and try again.";
const BUSY_ERROR =
  "Too many questions are being translated right now. Try again in a few seconds.";
const TRANSLATION_ERROR =
  "Could not turn that question into a dashboard view. Try naming a provider, a date range, or how to group the costs.";
const UNCONFIGURED_ERROR =
  "Natural-language questions are not configured on this instance.";
// Costroid holds no session of its own: authentication is header-based and the
// dashboard sends no credential. A 401 here means the proxy in front of it
// stopped accepting the request, which is the only topology that reaches this.
const REJECTED_ERROR =
  "The server rejected the request. If a proxy signs you in to Costroid, sign in again and reload the page.";
const UNREACHABLE_ERROR =
  "Could not reach the server. Check that it is still running, then try again.";
const LENGTH_ERROR =
  "That question is too long. Questions are limited to 8192 bytes.";

function optional(value: string | null): string | undefined {
  return value ?? undefined;
}

function capped(value: string | null): string | undefined | false {
  if (value === null) return undefined;
  return new TextEncoder().encode(value).byteLength <= MAX_TEXT_BYTES
    ? value
    : false;
}

function describe(
  subject: string,
  link: InsightLink,
  fields: {
    grouping?: boolean;
    currency?: boolean;
    provider?: boolean;
    metric?: boolean;
  },
): string {
  let opening = subject;
  if (link.start && link.end) {
    opening += ` for ${link.start} to ${link.end}`;
  } else if (link.start) {
    opening += ` from ${link.start}`;
  } else if (link.end) {
    opening += ` through ${link.end}`;
  } else {
    opening += " for all time";
  }

  const details: string[] = [];
  if (fields.grouping && link.groupBy) {
    details.push(
      link.groupBy === "tag" && link.tagKey
        ? `grouped by tag ${link.tagKey}`
        : `grouped by ${link.groupBy}`,
    );
  }
  if (fields.currency && link.currency) {
    details.push(`currency ${link.currency}`);
  }
  if (fields.provider && link.provider) {
    details.push(`provider ${link.provider}`);
  }
  if (fields.metric && link.metric) {
    details.push(`metric ${link.metric}`);
  }
  // Deliberately phrased as a reading of the QUESTION, not a description of the
  // chart. A view silently substitutes a currency, provider or tag key that the
  // requested window does not contain, so a sentence claiming to describe what
  // is displayed would be false in exactly the case a reader most needs it.
  return `Your question was read as: ${[opening, ...details].join(", ")}.`;
}

function resolvePlan(plan: QueryPlan): Result | undefined {
  const start = optional(plan.start);
  const end = optional(plan.end);
  const tagKey = capped(plan.tagKey);
  const provider = capped(plan.provider);
  const metric = capped(plan.metric);
  if (tagKey === false || provider === false || metric === false) {
    return undefined;
  }

  if (
    plan.endpoint === "costs-daily" ||
    plan.endpoint === "costs-summary" ||
    plan.endpoint === "anomalies"
  ) {
    const link: InsightLink = {
      view: "costs",
      start,
      end,
      groupBy: optional(plan.groupBy),
      tagKey,
      currency: optional(plan.currency),
      provider,
    };
    return {
      link,
      caption: describe(
        plan.endpoint === "anomalies" ? "costs with anomaly markers" : "costs",
        link,
        { grouping: true, currency: true, provider: true },
      ),
    };
  }

  if (plan.endpoint === "tokens" || plan.endpoint === "usage") {
    const link: InsightLink = {
      view: plan.endpoint,
      start,
      end,
    };
    return {
      link,
      caption: describe(plan.endpoint, link, {}),
    };
  }

  if (plan.endpoint === "unit-economics") {
    const link: InsightLink = {
      view: "unit-economics",
      start,
      end,
      currency: optional(plan.currency),
      provider,
      metric,
    };
    return {
      link,
      caption: describe("unit economics", link, {
        currency: true,
        provider: true,
        metric: true,
      }),
    };
  }

  return undefined;
}

function statusOf(error: unknown): number | undefined {
  if (typeof error !== "object" || error === null || !("status" in error)) {
    return undefined;
  }
  return typeof error.status === "number" ? error.status : undefined;
}

function messageFor(status: number | undefined): {
  message: string;
  retry: boolean;
} {
  if (status === 400 || status === 413) {
    return { message: SEND_ERROR, retry: false };
  }
  if (status === 429) {
    return { message: BUSY_ERROR, retry: true };
  }
  if (status === 503) {
    return { message: UNCONFIGURED_ERROR, retry: false };
  }
  if (status === 401) {
    return { message: REJECTED_ERROR, retry: false };
  }
  if (status === 500) {
    return { message: TRANSLATION_ERROR, retry: true };
  }
  // No status at all means the request never completed, and every other status
  // came from something between us and the handler. Neither is a failed
  // translation, so neither may advise the reader to rephrase the question.
  return { message: UNREACHABLE_ERROR, retry: true };
}

export default function AskQuestion({
  onNavigate,
  onAnnouncement,
  dismissToken,
}: {
  onNavigate: (link: InsightLink) => boolean;
  onAnnouncement: (message: string) => void;
  dismissToken: number;
}) {
  const [question, setQuestion] = useState("");
  const [pending, setPending] = useState(false);
  const [caption, setCaption] = useState("");
  const [error, setError] = useState<{ message: string; retry: boolean }>();

  const shown = useRef(dismissToken);
  useEffect(() => {
    // The caption describes one answer. Once the reader changes the view or the
    // range themselves, it no longer describes what is on screen, so it must go
    // rather than keep asserting a window they have left.
    if (shown.current !== dismissToken) {
      shown.current = dismissToken;
      setCaption("");
    }
  }, [dismissToken]);

  async function submit(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault();
    if (pending || question === "") return;
    if (new TextEncoder().encode(question).byteLength > MAX_TEXT_BYTES) {
      const next = { message: LENGTH_ERROR, retry: false };
      setError(next);
      setCaption("");
      onAnnouncement(next.message);
      return;
    }

    setPending(true);
    setError(undefined);
    setCaption("");
    onAnnouncement("Translating question.");
    try {
      const result = resolvePlan(await postQuery(question));
      if (!result || !onNavigate(result.link)) {
        const next = messageFor(500);
        setError(next);
        onAnnouncement(next.message);
        return;
      }
      setCaption(result.caption);
      setQuestion("");
      onAnnouncement(result.caption);
    } catch (caught) {
      const next = messageFor(statusOf(caught));
      setError(next);
      onAnnouncement(next.message);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="ask-row">
      <form onSubmit={(event) => void submit(event)}>
        <label className="sr-only" htmlFor="dashboard-question">
          Ask a question
        </label>
        <input
          id="dashboard-question"
          type="text"
          autoComplete="off"
          value={question}
          placeholder="Ask a question about your costs"
          onChange={(event) => setQuestion(event.target.value)}
        />
        <button type="submit" disabled={pending || question === ""}>
          {pending ? "Asking…" : "Ask"}
        </button>
      </form>
      {caption && <p className="ask-caption">{caption}</p>}
      {error && (
        <div className="ask-error" role="alert">
          <p>{error.message}</p>
          {error.retry && (
            <button type="button" onClick={() => void submit()}>
              Try again
            </button>
          )}
        </div>
      )}
    </div>
  );
}
