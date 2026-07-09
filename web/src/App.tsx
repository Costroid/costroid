// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import DateRangeControl from "./DateRangeControl";
import DailyCosts from "./DailyCosts";
import DailyTokens from "./DailyTokens";
import type { Range } from "./range";
import UsageMetrics from "./UsageMetrics";

type Meta = components["schemas"]["Meta"];

type MetaState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; meta: Meta };

type View = "costs" | "tokens" | "usage";

const VIEWS: { id: View; label: string }[] = [
  { id: "costs", label: "Costs" },
  { id: "tokens", label: "Tokens" },
  { id: "usage", label: "Usage" },
];

export default function App() {
  const [state, setState] = useState<MetaState>({ status: "loading" });
  const [view, setView] = useState<View>("costs");
  const [range, setRange] = useState<Range>({ start: "", end: "" });

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const res = await fetch("/api/v1/meta", { signal: controller.signal });
        if (!res.ok) {
          throw new Error(`GET /api/v1/meta returned ${res.status}`);
        }
        const meta = (await res.json()) as Meta;
        setState({ status: "ready", meta });
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }

    void load();
    return () => controller.abort();
  }, []);

  return (
    <main>
      <h1>Costroid</h1>
      {state.status === "loading" && <p>Loading instance metadata…</p>}
      {state.status === "error" && (
        <p role="alert">Failed to load instance metadata: {state.message}</p>
      )}
      {state.status === "ready" && (
        <dl>
          <dt>Name</dt>
          <dd>{state.meta.name}</dd>
          <dt>Version</dt>
          <dd>{state.meta.version}</dd>
          <dt>FOCUS version</dt>
          <dd>{state.meta.focusVersion}</dd>
        </dl>
      )}
      <div className="range-bar">
        <DateRangeControl range={range} onChange={setRange} />
        <p className="range-indicator">
          {range.start === "" && range.end === ""
            ? "Showing all time"
            : `Showing ${range.start} → ${range.end}`}
        </p>
      </div>
      <nav aria-label="Dashboard views">
        <div className="view-nav">
          {VIEWS.map((v) => (
            <button
              key={v.id}
              type="button"
              aria-current={view === v.id ? "page" : undefined}
              onClick={() => setView(v.id)}
            >
              {v.label}
            </button>
          ))}
        </div>
      </nav>
      <div>
        {view === "costs" && <DailyCosts range={range} />}
        {view === "tokens" && <DailyTokens range={range} />}
        {view === "usage" && <UsageMetrics range={range} />}
      </div>
    </main>
  );
}
