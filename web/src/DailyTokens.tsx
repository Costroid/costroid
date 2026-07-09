// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import {
  HEIGHT,
  MARGIN,
  MAX_BAR_WIDTH,
  SEGMENT_GAP,
  WIDTH,
  compactAxisLabel,
  segmentPath,
  serviceColor,
  sumIntegerStrings,
  yTicks,
} from "./viz";

type DailyTokenUsage = components["schemas"]["DailyTokenUsage"];

type TokensState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; rows: DailyTokenUsage[] };

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

export default function DailyTokens() {
  const [state, setState] = useState<TokensState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const res = await fetch("/api/v1/usage/tokens/daily", {
          signal: controller.signal,
        });
        if (!res.ok) {
          throw new Error(
            `GET /api/v1/usage/tokens/daily returned ${res.status}`,
          );
        }
        const rows = (await res.json()) as DailyTokenUsage[];
        setState({ status: "ready", rows });
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
    <section className="daily-tokens">
      <h2>Daily token usage by service</h2>
      {state.status === "loading" && <p>Loading daily token usage…</p>}
      {state.status === "error" && (
        <p role="alert">Failed to load daily token usage: {state.message}</p>
      )}
      {state.status === "ready" &&
        (state.rows.length === 0 ? (
          <EmptyState />
        ) : (
          <Chart rows={state.rows} />
        ))}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="viz-empty">
      <p>
        No token usage yet. Store a connector credential, ingest from an AI
        connector, then reload this page:
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
        Stop <code>costroid serve</code> while ingesting — the embedded store
        allows a single process at a time.
      </p>
    </div>
  );
}

function Chart({ rows }: { rows: DailyTokenUsage[] }) {
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

  return (
    <div>
      {periodTotal !== null && (
        <p className="daily-tokens-total">
          Period total: <strong>{periodTotal}</strong> Tokens
        </p>
      )}
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
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
            <g key={day.date}>
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
              {day.total !== null && (
                <text
                  x={x + barWidth / 2}
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
                  textAnchor="middle"
                >
                  {day.date.slice(5)}
                </text>
              )}
            </g>
          );
        })}
      </svg>
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
