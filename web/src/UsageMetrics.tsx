// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getUsageMetricsDaily } from "./api";
import { EmptyIcon } from "./icons";
import type { Range } from "./range";
import { sumIntegerStrings } from "./viz";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";

type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];

// The ready state carries the params it was fetched FOR, so a render can
// detect synchronously that the current props no longer match it (same
// staleness pattern as DailyCosts).
type MetricsState =
  | { status: "loading" }
  | {
      status: "error";
      message: string;
      params: { start: string; end: string };
    }
  | {
      status: "ready";
      rows: DailyUsageMetric[];
      params: { start: string; end: string };
    };

type SeriesKey = {
  serviceName: string;
  serviceTier: string;
  metricName: string;
};

type UnitSection = {
  unit: string;
  dates: string[];
  series: SeriesKey[];
  /** quantity by `${serviceName}\0${serviceTier}\0${metricName}` → date → quantity */
  cells: Map<string, Map<string, string>>;
};

const ADDITIVE_UNITS = new Set([
  "Tokens",
  "Requests",
  "Calls",
  "Images",
  "Characters",
  "Seconds",
  "Sessions",
]);

function seriesKey(s: SeriesKey): string {
  return `${s.serviceName}\0${s.serviceTier}\0${s.metricName}`;
}

/** Group flat rows into one table section per unit (never cross-unit sum). */
function groupByUnit(rows: DailyUsageMetric[]): UnitSection[] {
  const byUnit = new Map<
    string,
    {
      dates: Set<string>;
      series: Map<string, SeriesKey>;
      cells: Map<string, Map<string, string>>;
    }
  >();

  for (const row of rows) {
    let section = byUnit.get(row.unit);
    if (!section) {
      section = {
        dates: new Set(),
        series: new Map(),
        cells: new Map(),
      };
      byUnit.set(row.unit, section);
    }
    section.dates.add(row.date);
    const key: SeriesKey = {
      serviceName: row.serviceName,
      serviceTier: row.serviceTier,
      metricName: row.metricName,
    };
    const sk = seriesKey(key);
    section.series.set(sk, key);
    let byDate = section.cells.get(sk);
    if (!byDate) {
      byDate = new Map();
      section.cells.set(sk, byDate);
    }
    byDate.set(row.date, row.quantity);
  }

  return [...byUnit.keys()].sort().map((unit) => {
    const section = byUnit.get(unit)!;
    return {
      unit,
      dates: [...section.dates].sort(),
      series: [...section.series.values()].sort((a, b) => {
        const sn = a.serviceName.localeCompare(b.serviceName);
        if (sn !== 0) return sn;
        const st = a.serviceTier.localeCompare(b.serviceTier);
        if (st !== 0) return st;
        return a.metricName.localeCompare(b.metricName);
      }),
      cells: section.cells,
    };
  });
}

export default function UsageMetrics({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [state, setState] = useState<MetricsState>({ status: "loading" });
  const [retryToken, setRetryToken] = useState(0);
  const { start, end } = range;

  useEffect(() => {
    setState({ status: "loading" });
    const controller = new AbortController();

    async function load() {
      try {
        const rows = await getUsageMetricsDaily(
          { start, end },
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        setState({ status: "ready", rows, params: { start, end } });
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end },
        });
      }
    }

    void load();
    return () => controller.abort();
  }, [start, end, retryToken]);

  // Derive staleness synchronously (see DailyCosts): held data — or a held
  // error — fetched for a different range must not render beside the new
  // range for even one frame.
  const view: MetricsState =
    (state.status === "ready" || state.status === "error") &&
    (state.params.start !== start || state.params.end !== end)
      ? { status: "loading" }
      : state;

  return (
    <section className="usage-metrics" aria-labelledby="usage-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Operational signals</p>
          <h2 id="usage-title">Daily usage metrics</h2>
        </div>
      </div>
      <ViewStatus
        message={
          view.status === "loading"
            ? "Loading daily usage metrics…"
            : view.status === "ready"
              ? "Daily usage metrics loaded"
              : ""
        }
      />
      {view.status === "loading" && <LoadingSkeleton />}
      {view.status === "error" && (
        <ErrorState onRetry={() => setRetryToken((t) => t + 1)}>
          Failed to load daily usage metrics: {view.message}
        </ErrorState>
      )}
      {view.status === "ready" &&
        (view.rows.length === 0 ? (
          <EmptyState />
        ) : (
          <MetricsTables rows={view.rows} />
        ))}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="viz-empty">
      <div className="state-content">
        <EmptyIcon className="state-icon" size={30} />
        <p className="state-title">No usage metrics yet</p>
        <p className="state-message">
          Store a connector credential, ingest from an AI connector, then reload
          this page:
        </p>
        <pre>
          <code>
            costroid credentials set &lt;slot&gt;
            {"\n"}
            costroid ingest --connector openai-cost|anthropic-cost --credential
            &lt;slot&gt; [--since YYYY-MM]
          </code>
        </pre>
        <p>
          Stop <code>costroid serve</code> while ingesting; the embedded store
          allows a single process at a time.
        </p>
      </div>
    </div>
  );
}

function MetricsTables({ rows }: { rows: DailyUsageMetric[] }) {
  const sections = groupByUnit(rows);
  const sectionTotals = sections.map((section) => ({
    section,
    total: sectionTotal(section),
  }));

  return (
    <div>
      <div className="usage-card-grid">
        {sectionTotals.map(({ section, total }) =>
          total === null ? null : (
            <StatCard
              key={section.unit}
              label={`${section.unit} range total`}
              value={total}
              subtitle={section.unit}
            />
          ),
        )}
      </div>
      <div className="usage-metrics-list">
        {sectionTotals.map(({ section, total }) => {
          return (
            <div key={section.unit} className="usage-metrics-unit">
              <h3>{section.unit}</h3>
              <div>
                <table>
                  <thead>
                    <tr>
                      <th scope="col">Service</th>
                      <th scope="col">Tier</th>
                      <th scope="col">Metric</th>
                      {section.dates.map((date) => (
                        <th scope="col" key={date}>
                          {date}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {section.series.map((s) => {
                      const sk = seriesKey(s);
                      const byDate = section.cells.get(sk);
                      return (
                        <tr key={sk}>
                          <th scope="row">{s.serviceName}</th>
                          <td>{s.serviceTier === "" ? "—" : s.serviceTier}</td>
                          <td>{s.metricName}</td>
                          {section.dates.map((date) => (
                            <td key={date}>{byDate?.get(date) ?? "—"}</td>
                          ))}
                        </tr>
                      );
                    })}
                  </tbody>
                  {total !== null && (
                    <tfoot>
                      <tr>
                        <th scope="row" colSpan={3}>
                          Range total
                        </th>
                        <td colSpan={section.dates.length}>{total}</td>
                      </tr>
                    </tfoot>
                  )}
                </table>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function sectionTotal(section: UnitSection): string | null {
  if (!ADDITIVE_UNITS.has(section.unit)) {
    return null;
  }
  const quantities = section.series.flatMap((s) => {
    const byDate = section.cells.get(seriesKey(s));
    return section.dates
      .map((date) => byDate?.get(date))
      .filter((quantity): quantity is string => quantity !== undefined);
  });
  return sumIntegerStrings(quantities);
}
