// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { ReactNode } from "react";
import { WarningIcon } from "./icons";

export function LoadingSkeleton({ label }: { label: string }) {
  return (
    <div
      className="skeleton-card"
      role="status"
      aria-live="polite"
      aria-label={label}
    >
      <span className="sr-only">{label}</span>
      <div className="skeleton-line" />
      <div className="skeleton-chart" />
    </div>
  );
}

export function ErrorState({ children }: { children: ReactNode }) {
  return (
    <div className="state-card" role="alert" aria-live="assertive">
      <div className="state-content">
        <WarningIcon className="state-icon" size={28} />
        <p className="state-title">Unable to load this view</p>
        <p className="state-message">{children}</p>
      </div>
    </div>
  );
}

export function StatCard({
  label,
  value,
  subtitle,
}: {
  label: string;
  value: ReactNode;
  subtitle?: ReactNode;
}) {
  return (
    <article className="stat-card">
      <p className="stat-label">{label}</p>
      <p className="stat-value">{value}</p>
      {subtitle && <p className="stat-subtitle">{subtitle}</p>}
    </article>
  );
}
