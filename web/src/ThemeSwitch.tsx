// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { ReactElement } from "react";
import { MonitorIcon, MoonIcon, SunIcon } from "./icons";
import { applyTheme, readStoredTheme, storeTheme } from "./theme";
import type { ThemePreference } from "./theme";

const OPTIONS: Array<{
  value: ThemePreference;
  label: string;
  icon: ReactElement;
}> = [
  { value: "device", label: "Device theme", icon: <MonitorIcon size={14} /> },
  { value: "dark", label: "Dark theme", icon: <MoonIcon size={14} /> },
  { value: "light", label: "Light theme", icon: <SunIcon size={14} /> },
];

export function ThemeSwitch() {
  const [preference, setPreference] = useState<ThemePreference>(() =>
    readStoredTheme(),
  );

  // The pre-paint script in index.html only sets data-theme; this also syncs
  // the theme-color metas on mount. (No pageshow handler needed: a bfcache
  // restore freezes React state, the attribute, and the metas together, so
  // they stay mutually consistent.)
  useEffect(() => {
    applyTheme(preference);
  }, [preference]);

  const select = (next: ThemePreference) => {
    storeTheme(next);
    setPreference(next);
  };

  return (
    <div className="theme-switch" role="group" aria-label="Theme">
      {OPTIONS.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={preference === option.value}
          aria-label={option.label}
          title={option.label}
          onClick={() => select(option.value)}
        >
          {option.icon}
        </button>
      ))}
    </div>
  );
}
