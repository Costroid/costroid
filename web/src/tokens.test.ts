// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import baseCss from "./tokens.css?raw";
import demoCss from "./tokens.demo.css?raw";

// Demo builds swap ./tokens.css for ./tokens.demo.css (vite.config.ts), so a
// custom property defined in only one file silently collapses every var()
// that references it in the other mode — an invalid var() inside clamp()
// makes the whole declaration fall back to inherited values. This happened
// live once (--text-3xl); this pin catches any future drift.
describe("design-token parity", () => {
  const names = (css: string): Set<string> =>
    new Set(css.match(/--[\w-]+(?=\s*:)/g) ?? []);

  it("tokens.css and tokens.demo.css define the same custom properties", () => {
    const base = names(baseCss);
    const demo = names(demoCss);
    expect([...base].filter((n) => !demo.has(n))).toEqual([]);
    expect([...demo].filter((n) => !base.has(n))).toEqual([]);
  });
});
