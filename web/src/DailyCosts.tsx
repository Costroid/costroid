// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";

type DailyCosts = components["schemas"]["DailyCosts"];

type CostsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; costs: DailyCosts };

// Series slots are assigned to services sorted by name, so a service
// keeps its color across reloads and date ranges (validated palette;
// slots beyond the palette fall back to the muted ink).
const SERIES_SLOTS = 8;

function seriesColor(slot: number): string {
  return slot < SERIES_SLOTS
    ? `var(--viz-series-${slot + 1})`
    : "var(--viz-muted)";
}

// Chart geometry (SVG user units).
const WIDTH = 640;
const HEIGHT = 220;
const MARGIN = { top: 20, right: 8, bottom: 24, left: 48 };
const MAX_BAR_WIDTH = 24;
const SEGMENT_GAP = 2;

/** Y-axis tick values from 0 to a "nice" ceiling of max. */
function yTicks(max: number): number[] {
  if (max <= 0) {
    return [0];
  }
  const rough = max / 4;
  const power = 10 ** Math.floor(Math.log10(rough));
  const step =
    [1, 2, 5, 10].map((m) => m * power).find((s) => s >= rough) ?? rough;
  const ticks: number[] = [];
  for (let v = 0; v < max + step; v += step) {
    ticks.push(v);
  }
  return ticks;
}

/** SVG path for a bar segment; only the topmost gets rounded top corners. */
function segmentPath(
  x: number,
  y: number,
  w: number,
  h: number,
  roundedTop: boolean,
): string {
  if (!roundedTop) {
    return `M${x},${y} h${w} v${h} h${-w} Z`;
  }
  const r = Math.min(4, h, w / 2);
  return `M${x},${y + r} a${r},${r} 0 0 1 ${r},${-r} h${w - 2 * r} a${r},${r} 0 0 1 ${r},${r} v${h - r} h${-w} Z`;
}

export default function DailyCosts() {
  const [state, setState] = useState<CostsState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const res = await fetch("/api/v1/costs/daily", {
          signal: controller.signal,
        });
        if (!res.ok) {
          throw new Error(`GET /api/v1/costs/daily returned ${res.status}`);
        }
        const costs = (await res.json()) as DailyCosts;
        setState({ status: "ready", costs });
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
    <section className="daily-costs">
      <h2>Daily cost by service</h2>
      {state.status === "loading" && <p>Loading daily costs…</p>}
      {state.status === "error" && (
        <p role="alert">Failed to load daily costs: {state.message}</p>
      )}
      {state.status === "ready" &&
        (state.costs.days.length === 0 ? (
          <EmptyState />
        ) : (
          <Chart costs={state.costs} />
        ))}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="daily-costs-empty">
      <p>
        No cost data yet. Ingest an AWS FOCUS export, then reload this page:
      </p>
      <pre>
        <code>
          costroid ingest --connector aws-focus --path &lt;export.csv.gz&gt;
        </code>
      </pre>
      <p>
        Stop <code>costroid serve</code> while ingesting — the embedded store
        allows a single process at a time.
      </p>
    </div>
  );
}

function Chart({ costs }: { costs: DailyCosts }) {
  const services = [
    ...new Set(costs.days.flatMap((d) => d.services.map((s) => s.serviceName))),
  ].sort();
  const slotOf = new Map(services.map((name, i) => [name, i]));

  const dayTotals = costs.days.map((d) => Number(d.total));
  const ticks = yTicks(Math.max(...dayTotals));
  const top = ticks[ticks.length - 1] || 1;

  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;
  const band = plotWidth / costs.days.length;
  const barWidth = Math.min(MAX_BAR_WIDTH, band * 0.6);
  const yOf = (value: number) => baseline - (value / top) * plotHeight;

  return (
    <div>
      <p className="daily-costs-total">
        Period total: <strong>{costs.total}</strong> {costs.currency}
      </p>
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label="Stacked daily cost by service"
        className="daily-costs-chart"
      >
        {ticks.map((tick) => (
          <g key={tick}>
            <line
              x1={MARGIN.left}
              x2={WIDTH - MARGIN.right}
              y1={yOf(tick)}
              y2={yOf(tick)}
              className={tick === 0 ? "viz-baseline" : "viz-grid"}
            />
            <text
              x={MARGIN.left - 8}
              y={yOf(tick) + 3}
              className="viz-tick"
              textAnchor="end"
            >
              {tick}
            </text>
          </g>
        ))}
        {costs.days.map((day, i) => {
          const x = MARGIN.left + i * band + (band - barWidth) / 2;
          let cursor = baseline;
          return (
            <g key={day.date}>
              {day.services.map((svc, j) => {
                const height = (Number(svc.cost) / top) * plotHeight;
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
                    fill={seriesColor(
                      slotOf.get(svc.serviceName) ?? SERIES_SLOTS,
                    )}
                  >
                    <title>{`${svc.serviceName}: ${svc.cost} ${costs.currency} (${day.date})`}</title>
                  </path>
                );
              })}
              <text
                x={x + barWidth / 2}
                y={cursor - 6}
                className="viz-cap"
                textAnchor="middle"
              >
                {day.total}
              </text>
              <text
                x={x + barWidth / 2}
                y={baseline + 16}
                className="viz-tick"
                textAnchor="middle"
              >
                {day.date.slice(5)}
              </text>
            </g>
          );
        })}
      </svg>
      <ul className="daily-costs-legend">
        {services.map((name) => (
          <li key={name}>
            <span
              className="daily-costs-swatch"
              style={{
                background: seriesColor(slotOf.get(name) ?? SERIES_SLOTS),
              }}
            />
            {name}
          </li>
        ))}
      </ul>
      <details className="daily-costs-table">
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
            {costs.days.map((day) => (
              <tr key={day.date}>
                <th scope="row">{day.date}</th>
                {services.map((name) => (
                  <td key={name}>
                    {day.services.find((s) => s.serviceName === name)?.cost ??
                      "—"}
                  </td>
                ))}
                <td>{day.total}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}
