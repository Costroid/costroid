// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getMeta } from "./api";
import { DEMO_PRESETS } from "./demo/ranges";
import DateRangeControl from "./DateRangeControl";
import DailyCosts from "./DailyCosts";
import DailyTokens from "./DailyTokens";
import {
  BrandIcon,
  CostsIcon,
  OverviewIcon,
  SourcesIcon,
  TokensIcon,
  UnitEconomicsIcon,
  UsageIcon,
  WarningIcon,
} from "./icons";
import { ThemeSwitch } from "./ThemeSwitch";
import Overview from "./Overview";
import type { Range } from "./range";
import Sources from "./Sources";
import UsageMetrics from "./UsageMetrics";
import UnitEconomics from "./UnitEconomics";

type Meta = components["schemas"]["Meta"];

type MetaState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; meta: Meta };

type View =
  "overview" | "costs" | "tokens" | "usage" | "unit-economics" | "sources";

const VIEWS = [
  { id: "overview", label: "Overview", icon: OverviewIcon },
  { id: "costs", label: "Costs", icon: CostsIcon },
  { id: "tokens", label: "Tokens", icon: TokensIcon },
  { id: "usage", label: "Usage", icon: UsageIcon },
  {
    id: "unit-economics",
    label: "Unit economics",
    icon: UnitEconomicsIcon,
  },
  { id: "sources", label: "Sources", icon: SourcesIcon },
] satisfies { id: View; label: string; icon: typeof CostsIcon }[];

function rangeIndicator(range: Range): string {
  if (range.start === "" && range.end === "") {
    return "Showing all time";
  }
  if (range.start !== "" && range.end !== "") {
    return `Showing ${range.start} → ${range.end}`;
  }
  if (range.start !== "") {
    return `Showing from ${range.start}`;
  }
  return `Showing through ${range.end}`;
}

export default function App() {
  const [state, setState] = useState<MetaState>({ status: "loading" });
  const [view, setView] = useState<View>("overview");
  const [range, setRange] = useState<Range>({ start: "", end: "" });

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const meta = await getMeta(controller.signal);
        setState({ status: "ready", meta });
        if (meta.demo) {
          // Demo mode serves only the captured preset ranges; open on the full
          // window (which carries the anomaly story) instead of all-time.
          const full = DEMO_PRESETS.find((preset) => preset.id === "full");
          if (full) {
            setRange((current) =>
              current.start === "" && current.end === ""
                ? { start: full.start, end: full.end }
                : current,
            );
          }
        }
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
    <main className="app-shell">
      <a className="skip-link" href="#view-panel">
        Skip to content
      </a>
      <header className="app-header">
        <div className="brand">
          <span className="brand-mark">
            <BrandIcon size={22} />
          </span>
          <div>
            <h1>Costroid</h1>
            <p className="brand-subtitle">FOCUS-native cost intelligence</p>
          </div>
        </div>
        <div className="header-tools">
          {state.status === "loading" && (
            <div className="instance-meta" role="status">
              <div>
                <span>Instance</span>
                <strong>Loading…</strong>
              </div>
            </div>
          )}
          {state.status === "error" && (
            <div className="instance-meta" role="alert">
              <div>
                <WarningIcon size={14} />
                <span>Failed to load instance metadata: {state.message}</span>
              </div>
            </div>
          )}
          {state.status === "ready" && (
            <dl className="instance-meta">
              <div>
                <dt>Name</dt>
                <dd>{state.meta.name}</dd>
              </div>
              <div>
                <dt>Version</dt>
                <dd>{state.meta.version}</dd>
              </div>
              <div>
                <dt>FOCUS</dt>
                <dd>{state.meta.focusVersion}</dd>
              </div>
            </dl>
          )}
          <ThemeSwitch />
        </div>
      </header>
      <div className="toolbar">
        <div className="range-bar">
          <DateRangeControl
            range={range}
            onChange={setRange}
            presets={
              state.status === "ready" && state.meta.demo
                ? DEMO_PRESETS
                : undefined
            }
          />
          <p className="range-indicator" aria-live="polite">
            {rangeIndicator(range)}
          </p>
        </div>
        <nav aria-label="Dashboard views">
          <div className="view-nav">
            {VIEWS.map((v) => {
              const ViewIcon = v.icon;
              return (
                <button
                  key={v.id}
                  type="button"
                  aria-current={view === v.id ? "page" : undefined}
                  onClick={() => setView(v.id)}
                >
                  <ViewIcon size={17} />
                  <span>{v.label}</span>
                </button>
              );
            })}
          </div>
        </nav>
      </div>
      <div className="view-panel" id="view-panel" tabIndex={-1}>
        {view === "overview" && <Overview range={range} />}
        {view === "costs" && <DailyCosts range={range} />}
        {view === "tokens" && <DailyTokens range={range} />}
        {view === "usage" && <UsageMetrics range={range} />}
        {view === "unit-economics" && <UnitEconomics range={range} />}
        {view === "sources" && <Sources />}
      </div>
    </main>
  );
}
