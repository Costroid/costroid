// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import type { Range } from "./range";
import { rangeQuery } from "./range";
import {
  HEIGHT,
  MARGIN,
  MAX_BAR_WIDTH,
  SEGMENT_GAP,
  WIDTH,
  segmentPath,
  serviceColor,
  yTicks,
} from "./viz";

type DailyCosts = components["schemas"]["DailyCosts"];

type CostsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; costs: DailyCosts };

type CostGroupBy = "service" | "provider" | "allocation";

const GROUP_BY_OPTIONS: { id: CostGroupBy; label: string }[] = [
  { id: "service", label: "Service" },
  { id: "provider", label: "Provider" },
  { id: "allocation", label: "Allocation" },
];

// groupLabelOf maps a grouping id to the lowercase heading/aria noun.
function groupLabelOf(groupBy: CostGroupBy): string {
  return (
    GROUP_BY_OPTIONS.find((o) => o.id === groupBy)?.label.toLowerCase() ??
    groupBy
  );
}

export default function DailyCosts({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [state, setState] = useState<CostsState>({ status: "loading" });
  const [groupBy, setGroupBy] = useState<CostGroupBy>("service");
  const { start, end } = range;

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const q = rangeQuery(start, end);
        const url =
          `/api/v1/costs/daily${q}` +
          (groupBy !== "service" ? `${q ? "&" : "?"}groupBy=${groupBy}` : "");
        const res = await fetch(url, {
          signal: controller.signal,
        });
        if (!res.ok) {
          // Surface the server's message (e.g. the unconfigured-allocation
          // 400 body) so the error state is actionable.
          const body = (await res.text()).trim();
          throw new Error(
            `GET /api/v1/costs/daily returned ${res.status}` +
              (body ? `: ${body}` : ""),
          );
        }
        const costs = (await res.json()) as DailyCosts;
        if (controller.signal.aborted) {
          return;
        }
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
  }, [start, end, groupBy]);

  const groupLabel = groupLabelOf(groupBy);

  return (
    <section>
      <h2>Daily cost by {groupLabel}</h2>
      <div
        className="cost-group-control"
        role="group"
        aria-label="Group costs by"
      >
        <span>Group by:</span>
        {GROUP_BY_OPTIONS.map((option) => (
          <button
            key={option.id}
            type="button"
            aria-pressed={groupBy === option.id}
            onClick={() => setGroupBy(option.id)}
          >
            {option.label}
          </button>
        ))}
      </div>
      {state.status === "loading" && <p>Loading daily costs…</p>}
      {state.status === "error" && (
        <p role="alert">Failed to load daily costs: {state.message}</p>
      )}
      {state.status === "ready" &&
        (state.costs.days.length === 0 ? (
          <EmptyState />
        ) : (
          <Chart costs={state.costs} groupBy={groupBy} />
        ))}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="viz-empty">
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

function Chart({
  costs,
  groupBy,
}: {
  costs: DailyCosts;
  groupBy: CostGroupBy;
}) {
  const groupLabel = groupLabelOf(groupBy);
  const groups = [
    ...new Set(costs.days.flatMap((d) => d.services.map((s) => s.key))),
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
      <p>
        Period total (net): <strong>{costs.total}</strong> {costs.currency}
      </p>
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label={`Stacked daily cost by ${groupLabel}`}
        className="viz-chart"
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
                    key={svc.key}
                    d={segmentPath(
                      x,
                      segmentBottom - height + gap,
                      barWidth,
                      drawnHeight,
                      isTop,
                    )}
                    fill={serviceColor(svc.key)}
                  >
                    <title>{`${svc.key}: ${svc.cost} ${costs.currency} (${day.date})`}</title>
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
      <ul className="viz-legend">
        {groups.map((name) => (
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
              {groups.map((name) => (
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
                {groups.map((name) => (
                  <td key={name}>
                    {day.services.find((s) => s.key === name)?.cost ?? "—"}
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
