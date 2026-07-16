// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { SVGProps } from "react";

// Stroke paths are adapted from Lucide icons (ISC License).
type IconProps = SVGProps<SVGSVGElement> & { size?: number };

function Icon({ size = 18, children, ...props }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden={props["aria-label"] ? undefined : true}
      {...props}
    >
      {children}
    </svg>
  );
}

export function BrandIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M5 7.5 12 3l7 4.5v9L12 21l-7-4.5z" />
      <path d="m8.5 10 3.5-2 3.5 2v4L12 16l-3.5-2z" />
    </Icon>
  );
}

export function OverviewIcon(props: IconProps) {
  // Lucide-derived "layout-dashboard" stroke paths (ISC).
  return (
    <Icon {...props}>
      <rect x="3" y="3" width="7" height="9" rx="1" />
      <rect x="14" y="3" width="7" height="5" rx="1" />
      <rect x="14" y="12" width="7" height="9" rx="1" />
      <rect x="3" y="16" width="7" height="5" rx="1" />
    </Icon>
  );
}

export function CostsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3 3v18h18" />
      <path d="m7 15 4-4 3 2 5-6" />
    </Icon>
  );
}

export function TokensIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="12" cy="12" r="8" />
      <path d="M9 9h6M12 9v6M9 15h6" />
    </Icon>
  );
}

export function UsageIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
    </Icon>
  );
}

export function UnitEconomicsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8" cy="8" r="4" />
      <circle cx="16" cy="16" r="4" />
      <path d="m18.5 5.5-13 13" />
    </Icon>
  );
}

export function MonitorIcon(props: IconProps) {
  // Lucide-derived "monitor" stroke paths (ISC).
  return (
    <Icon {...props}>
      <rect x="2" y="3" width="20" height="14" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </Icon>
  );
}

export function MoonIcon(props: IconProps) {
  // Lucide-derived "moon" stroke path (ISC).
  return (
    <Icon {...props}>
      <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" />
    </Icon>
  );
}

export function SunIcon(props: IconProps) {
  // Lucide-derived "sun" stroke paths (ISC).
  return (
    <Icon {...props}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M19.07 4.93l-1.41 1.41M6.34 17.66l-1.41 1.41" />
    </Icon>
  );
}

export function WarningIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M10.3 3.7 2.2 18a2 2 0 0 0 1.8 3h16a2 2 0 0 0 1.8-3L13.7 3.7a2 2 0 0 0-3.4 0Z" />
      <path d="M12 9v4M12 17h.01" />
    </Icon>
  );
}

export function EmptyIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 5h16v14H4zM4 9h16" />
      <path d="M9 14h6" />
    </Icon>
  );
}
