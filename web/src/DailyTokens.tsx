// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getTokensDaily } from "./api";
import { EmptyIcon } from "./icons";
import type { Range } from "./range";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";
import {
  HEIGHT,
  MARGIN,
  MAX_BAR_WIDTH,
  SEGMENT_GAP,
  WIDTH,
  capLabelPositions,
  compactAxisLabel,
  segmentPath,
  serviceColor,
  sumIntegerStrings,
  yTicks,
} from "./viz";

type DailyTokenUsage = components["schemas"]["DailyTokenUsage"];

// The ready state carries the params it was fetched FOR, so a render can
// detect synchronously that the current props no longer match it (same
// staleness pattern as DailyCosts).
type TokensState =
  | { status: "loading" }
  | {
      status: "error";
      message: string;
      params: { start: string; end: string };
    }
  | {
      status: "ready";
      rows: DailyTokenUsage[];
      params: { start: string; end: string };
    };

type DayGroup = {
  date: string;
  services: { serviceName: string; quantity: string }[];
  total: string | null;
};

/** Group the flat day×service×unit array into per-day service segments. */
function groupByDate(rows: DailyTokenUsage[]): DayGroup[] {
  const byDate = new Map<string, Map<string, string>>();
  for (const row of rows) {
    let services = byDate.get(row.date);
    if (!services) {
      services = new Map();
      byDate.set(row.date, services);
    }
    // One row per day×service×unit and unit is always Tokens; if a
    // service appears twice, sum integer quantities when possible.
    const prev = services.get(row.serviceName);
    if (prev === undefined) {
      services.set(row.serviceName, row.consumedQuantity);
    } else {
      const summed = sumIntegerStrings([prev, row.consumedQuantity]);
      services.set(
        row.serviceName,
        summed ?? row.consumedQuantity, // fall back to latest raw string
      );
    }
  }
  const dates = [...byDate.keys()].sort();
  return dates.map((date) => {
    const services = [...(byDate.get(date) ?? new Map()).entries()]
      .map(([serviceName, quantity]) => ({ serviceName, quantity }))
      .sort((a, b) => a.serviceName.localeCompare(b.serviceName));
    return {
      date,
      services,
      total: sumIntegerStrings(services.map((s) => s.quantity)),
    };
  });
}

export default function DailyTokens({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [state, setState] = useState<TokensState>({ status: "loading" });
  const [retryToken, setRetryToken] = useState(0);
  const { start, end } = range;

  useEffect(() => {
    setState({ status: "loading" });
    const controller = new AbortController();

    async function load() {
      try {
        const rows = await getTokensDaily({ start, end }, controller.signal);
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
  const view: TokensState =
    (state.status === "ready" || state.status === "error") &&
    (state.params.start !== start || state.params.end !== end)
      ? { status: "loading" }
      : state;

  return (
    <section aria-labelledby="tokens-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">AI consumption</p>
          <h2 id="tokens-title">Daily token usage by service</h2>
        </div>
      </div>
      <ViewStatus
        message={
          view.status === "loading"
            ? "Loading daily token usage…"
            : view.status === "ready"
              ? "Daily token usage loaded"
              : ""
        }
      />
      {view.status === "loading" && <LoadingSkeleton />}
      {view.status === "error" && (
        <ErrorState onRetry={() => setRetryToken((t) => t + 1)}>
          Failed to load daily token usage: {view.message}
        </ErrorState>
      )}
      {view.status === "ready" &&
        (view.rows.length === 0 ? <EmptyState /> : <Chart rows={view.rows} />)}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="viz-empty">
      <div className="state-content">
        <EmptyIcon className="state-icon" size={30} />
        <p className="state-title">No token usage yet</p>
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

function Chart({ rows }: { rows: DailyTokenUsage[] }) {
  const [activeDay, setActiveDay] = useState<number | null>(null);
  // WCAG 1.4.13: Escape dismisses the tooltip whether it was opened by hover
  // or by keyboard focus, so listen at the document while it is showing.
  useEffect(() => {
    if (activeDay === null) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setActiveDay(null);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [activeDay]);
  const days = groupByDate(rows);
  const services = [
    ...new Set(days.flatMap((d) => d.services.map((s) => s.serviceName))),
  ].sort();

  const daySums = days.map((d) =>
    d.services.reduce((sum, s) => sum + Number(s.quantity), 0),
  );
  const ticks = yTicks(Math.max(...daySums, 0));
  const top = ticks[ticks.length - 1].value || 1;

  const periodTotal = sumIntegerStrings(
    days.flatMap((d) => d.services.map((s) => s.quantity)),
  );

  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;
  const band = plotWidth / days.length;
  const barWidth = Math.min(MAX_BAR_WIDTH, band * 0.6);
  const yOf = (value: number) => baseline - (value / top) * plotHeight;

  const labelEvery = Math.max(1, Math.ceil(days.length / 12));
  // Cap labels remain verbatim. Positions only — edge clamp + collision thin.
  const capPositions = capLabelPositions(days.map((d) => d.total));
  const tooltipDay = activeDay === null ? null : days[activeDay];
  const tooltipLeft =
    activeDay === null
      ? 50
      : ((MARGIN.left + activeDay * band + band / 2) / WIDTH) * 100;

  return (
    <div>
      {periodTotal !== null && (
        <div className="stat-grid">
          <StatCard
            label="Period total"
            value={periodTotal}
            subtitle="Tokens"
          />
        </div>
      )}
      <div className="viz-panel">
        <div className="chart-wrapper">
          {/* role="group", not "img": an img would declare its focusable,
              labeled hit-target rects presentational. */}
          <svg
            viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
            role="group"
            aria-label="Stacked daily token usage by service"
            className="viz-chart"
          >
            {ticks.map((tick) => (
              <g key={tick.value}>
                <line
                  x1={MARGIN.left}
                  x2={WIDTH - MARGIN.right}
                  y1={yOf(tick.value)}
                  y2={yOf(tick.value)}
                  className={tick.value === 0 ? "viz-baseline" : "viz-grid"}
                />
                <text
                  x={MARGIN.left - 8}
                  y={yOf(tick.value) + 3}
                  className="viz-tick"
                  aria-hidden="true"
                  textAnchor="end"
                >
                  {compactAxisLabel(tick.value)}
                </text>
              </g>
            ))}
            {days.map((day, i) => {
              const x = MARGIN.left + i * band + (band - barWidth) / 2;
              let cursor = baseline;
              return (
                <g key={day.date} className="viz-day">
                  {day.services.map((svc, j) => {
                    const height = (Number(svc.quantity) / top) * plotHeight;
                    const isTop = j === day.services.length - 1;
                    const segmentBottom = cursor;
                    cursor -= height;
                    const gap = isTop ? 0 : SEGMENT_GAP;
                    const drawnHeight = Math.max(height - gap, 0);
                    if (drawnHeight <= 0) {
                      return null;
                    }
                    return (
                      <path
                        key={svc.serviceName}
                        className="viz-segment"
                        d={segmentPath(
                          x,
                          segmentBottom - height + gap,
                          barWidth,
                          drawnHeight,
                          isTop,
                        )}
                        fill={serviceColor(svc.serviceName)}
                      >
                        <title>{`${svc.serviceName}: ${svc.quantity} Tokens (${day.date})`}</title>
                      </path>
                    );
                  })}
                  {capPositions[i] !== null && (
                    <text
                      x={capPositions[i]!}
                      y={cursor - 6}
                      className="viz-cap"
                      textAnchor="middle"
                    >
                      <title>Day total</title>
                      {day.total}
                    </text>
                  )}
                  {i % labelEvery === 0 && (
                    <text
                      x={x + barWidth / 2}
                      y={baseline + 16}
                      className="viz-tick"
                      aria-hidden="true"
                      textAnchor="middle"
                    >
                      {day.date.slice(5)}
                    </text>
                  )}
                  <rect
                    className="viz-hit-target"
                    x={MARGIN.left + i * band}
                    y={MARGIN.top}
                    width={band}
                    height={plotHeight}
                    tabIndex={0}
                    aria-label={`${day.date} token details`}
                    aria-describedby={
                      activeDay === i ? "tokens-tooltip" : undefined
                    }
                    onPointerEnter={() => setActiveDay(i)}
                    onPointerLeave={() => setActiveDay(null)}
                    onFocus={() => setActiveDay(i)}
                    onBlur={() => setActiveDay(null)}
                  />
                </g>
              );
            })}
          </svg>
          {tooltipDay && (
            <div
              className="chart-tooltip"
              id="tokens-tooltip"
              role="tooltip"
              style={{ left: `${tooltipLeft}%`, top: "52%" }}
            >
              <strong>{tooltipDay.date}</strong>
              {tooltipDay.services.map((service) => (
                <span className="chart-tooltip-row" key={service.serviceName}>
                  <span>{service.serviceName}</span>
                  <span>{service.quantity} Tokens</span>
                </span>
              ))}
              {tooltipDay.total !== null && (
                <span className="chart-tooltip-row">
                  <span>Total</span>
                  <span>{tooltipDay.total} Tokens</span>
                </span>
              )}
            </div>
          )}
        </div>
        <ul className="viz-legend">
          {services.map((name) => (
            <li key={name}>
              <span
                className="viz-swatch"
                style={{ background: serviceColor(name) }}
              />
              {name}
            </li>
          ))}
        </ul>
      </div>
      <details className="viz-table">
        <summary>View as table</summary>
        <table>
          <thead>
            <tr>
              <th scope="col">Date</th>
              {services.map((name) => (
                <th scope="col" key={name}>
                  {name}
                </th>
              ))}
              <th scope="col">Total</th>
            </tr>
          </thead>
          <tbody>
            {days.map((day) => (
              <tr key={day.date}>
                <th scope="row">{day.date}</th>
                {services.map((name) => (
                  <td key={name}>
                    {day.services.find((s) => s.serviceName === name)
                      ?.quantity ?? "—"}
                  </td>
                ))}
                <td>{day.total ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}
