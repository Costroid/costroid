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
import {
  GROUPINGS,
  VIEWS as URL_VIEWS,
  readUrlState,
  writeUrlState,
  type GroupBy,
  type View,
} from "./urlstate";

type Meta = components["schemas"]["Meta"];
type InsightLink = components["schemas"]["InsightLink"];

type MetaState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; meta: Meta };

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

function narrowView(value: string | undefined): View | undefined {
  if (value === undefined) return undefined;
  return URL_VIEWS.find((candidate) => candidate === value);
}

function narrowGroupBy(
  value: string | undefined,
): { ok: true; groupBy: GroupBy | undefined } | { ok: false } {
  if (value === undefined) return { ok: true, groupBy: undefined };
  if (value === "tag") return { ok: true, groupBy: "tag" };
  const found = GROUPINGS.find((candidate) => candidate === value);
  if (found !== undefined) return { ok: true, groupBy: found };
  return { ok: false };
}

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
  const [view, setView] = useState<View>(
    () => readUrlState().view ?? "overview",
  );
  const [range, setRange] = useState<Range>(() => {
    const urlState = readUrlState();
    return { start: urlState.start ?? "", end: urlState.end ?? "" };
  });

  useEffect(() => {
    writeUrlState({ view, start: range.start, end: range.end });
  }, [view, range.start, range.end]);

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
              DEMO_PRESETS.some(
                (preset) =>
                  preset.start === current.start && preset.end === current.end,
              )
                ? current
                : { start: full.start, end: full.end },
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

  // Programmatic deep link from the Overview insights panel. Replaces drill-down
  // state (explicit undefined clears keys the link omits) rather than merging.
  function onNavigate(link: InsightLink) {
    const nextView = narrowView(link.view);
    if (nextView === undefined) return;
    const grouping = narrowGroupBy(link.groupBy);
    if (!grouping.ok) return;

    writeUrlState({
      view: nextView,
      start: link.start,
      end: link.end,
      groupBy: grouping.groupBy,
      tagKey: link.tagKey,
      currency: link.currency,
      provider: link.provider,
      metric: link.metric,
    });
    setRange({
      start: link.start ?? "",
      end: link.end ?? "",
    });
    setView(nextView);
  }

  return (
    <main className="app-shell">
      <a
        className="skip-link"
        href="#view-panel"
        onClick={(event) => {
          // A real fragment navigation would replace the state hash and push
          // a history entry; move focus to the panel without navigating.
          event.preventDefault();
          document.getElementById("view-panel")?.focus();
        }}
      >
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
        {view === "overview" && (
          <Overview range={range} onNavigate={onNavigate} />
        )}
        {view === "costs" && <DailyCosts range={range} />}
        {view === "tokens" && <DailyTokens range={range} />}
        {view === "usage" && <UsageMetrics range={range} />}
        {view === "unit-economics" && <UnitEconomics range={range} />}
        {view === "sources" && <Sources />}
      </div>
    </main>
  );
}
