// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Theme preference: "device" follows prefers-color-scheme; "dark"/"light"
// force a theme via html[data-theme] (tokens.css carries the override
// blocks). The stored key is also read by the pre-paint inline script in
// index.html so a forced theme applies before first paint.

export type ThemePreference = "device" | "dark" | "light";

const STORAGE_KEY = "costroid-theme";

// tokens.css --bg values; the browser-chrome color must track a forced theme
// (the media attributes on the meta tags only follow the OS preference).
const THEME_COLOR = { dark: "#0d1119", light: "#f6f7f9" } as const;

export function readStoredTheme(): ThemePreference {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    if (value === "dark" || value === "light") return value;
  } catch {
    // Storage unavailable (private mode) — per-session switching still works.
  }
  return "device";
}

export function storeTheme(preference: ThemePreference): void {
  try {
    if (preference === "device") localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, preference);
  } catch {
    // Storage unavailable — the in-memory preference still applies.
  }
}

export function applyTheme(preference: ThemePreference): void {
  const root = document.documentElement;
  if (preference === "device") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", preference);

  const metas = document.querySelectorAll<HTMLMetaElement>(
    'meta[name="theme-color"]',
  );
  for (const meta of metas) {
    if (!meta.dataset.base) meta.dataset.base = meta.content;
    meta.content =
      preference === "device"
        ? (meta.dataset.base ?? meta.content)
        : THEME_COLOR[preference];
  }
}
