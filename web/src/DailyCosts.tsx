// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";

type DailyCosts = components["schemas"]["DailyCosts"];

type CostsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; costs: DailyCosts };

// A service's color is a deterministic function of its name alone
// (an FNV-1a-style hash onto the validated palette — "style" because it
// hashes UTF-16 code units via charCodeAt, whereas canonical FNV-1a is
// defined over octets), so it never shifts when other services appear or
// disappear across ingests, reloads, or date ranges.
// Distinct services can hash to the same slot and then share a color —
// an accepted trade-off of a fixed 8-color palette.
const SERIES_SLOTS = 8;

function serviceColor(name: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    hash ^= name.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `var(--viz-series-${((hash >>> 0) % SERIES_SLOTS) + 1})`;
}

// Chart geometry (SVG user units).
const WIDTH = 640;
const HEIGHT = 220;
const MARGIN = { top: 20, right: 8, bottom: 24, left: 48 };
const MAX_BAR_WIDTH = 24;
const SEGMENT_GAP = 2;

/**
 * Y-axis ticks from 0 to a "nice" ceiling of max. Values are computed as
 * step multiples and labels formatted to the step's decimal places, so
 * labels never show float-accumulation noise ("0.30000000000000004").
 */
function yTicks(max: number): { value: number; label: string }[] {
  if (max <= 0) {
    return [{ value: 0, label: "0" }];
  }
  const rough = max / 4;
  const exp = Math.floor(Math.log10(rough));
  const mult = [1, 2, 5, 10].find((m) => m * 10 ** exp >= rough) ?? 10;
  const step = mult * 10 ** exp;
  const decimals = Math.max(0, mult === 10 ? -(exp + 1) : -exp);
  const count = Math.ceil(max / step - 1e-9);
  return Array.from({ length: count + 1 }, (_, i) => ({
    value: i * step,
    label: (i * step).toFixed(decimals),
  }));
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

  // The stacked bars render positive costs only: FOCUS Credit/Adjustment
  // rows can be negative, and a diverging below-baseline geometry is a
  // later slice. The y-scale therefore spans each day's positive-segment
  // sum — the rendered stack height. Net day totals (which can be lower)
  // appear only as the cap labels and the grand total, labeled as net.
  const positiveServices = (day: DailyCosts["days"][number]) =>
    day.services.filter((s) => Number(s.cost) > 0);
  const dayPositiveSums = costs.days.map((d) =>
    positiveServices(d).reduce((sum, s) => sum + Number(s.cost), 0),
  );
  const ticks = yTicks(Math.max(...dayPositiveSums));
  const top = ticks[ticks.length - 1].value || 1;

  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;
  const band = plotWidth / costs.days.length;
  const barWidth = Math.min(MAX_BAR_WIDTH, band * 0.6);
  const yOf = (value: number) => baseline - (value / top) * plotHeight;

  // Long ranges: label every k-th day so at most ~12 date labels render.
  const labelEvery = Math.max(1, Math.ceil(costs.days.length / 12));

  return (
    <div>
      <p className="daily-costs-total">
        Period total (net): <strong>{costs.total}</strong> {costs.currency}
      </p>
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label="Stacked daily cost by service"
        className="daily-costs-chart"
      >
        {ticks.map((tick) => (
          <g key={tick.label}>
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
              {tick.label}
            </text>
          </g>
        ))}
        {costs.days.map((day, i) => {
          const x = MARGIN.left + i * band + (band - barWidth) / 2;
          const positive = positiveServices(day);
          let cursor = baseline;
          return (
            <g key={day.date}>
              {positive.map((svc, j) => {
                const height = (Number(svc.cost) / top) * plotHeight;
                const isTop = j === positive.length - 1;
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
                <title>Net day total</title>
                {day.total}
              </text>
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
      <ul className="daily-costs-legend">
        {services.map((name) => (
          <li key={name}>
            <span
              className="daily-costs-swatch"
              style={{ background: serviceColor(name) }}
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
              <th scope="col">Total (net)</th>
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
