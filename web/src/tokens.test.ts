// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Read via fs, NOT `?raw` imports: vitest's default `test.css: false` blanks
// CSS modules BEFORE vite's raw handling runs, so a `.css?raw` import
// resolves to an EMPTY STRING (all vitest majors; vitest-dev/vitest#10788,
// closed 2026-07-17 as not-planned: upstream considers `test.css: false`
// disabling everything CSS-related, `?raw` included, the intended behavior —
// so fs reads are the permanent approach here, not a stopgap). That made the
// original parity pin below pass vacuously (an empty set equals an empty
// set). The guard below makes that failure mode loud.
const read = (rel: string): string =>
  readFileSync(new URL(rel, import.meta.url), "utf8");
const baseCss = read("./tokens.css");
const demoCss = read("./tokens.demo.css");
const indexCss = read("./index.css");

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
    // Guard against vacuous success on an empty/failed read.
    expect(base.size).toBeGreaterThan(20);
    expect([...base].filter((n) => !demo.has(n))).toEqual([]);
    expect([...demo].filter((n) => !base.has(n))).toEqual([]);
  });

  // The dark palette exists TWICE per file: device mode (the
  // prefers-color-scheme media block, scoped :root:not([data-theme="light"]))
  // and the explicit :root[data-theme="dark"] override. If someone tweaks a
  // dark token in one block only, forced-dark silently diverges from
  // device-dark — the same drift class as above, within one file.
  const darkBlocks = (css: string): [string, string] => {
    const media = css.match(
      /@media \(prefers-color-scheme: dark\) \{\s*:root:not\(\[data-theme="light"\]\) \{([^}]*)\}/,
    );
    const attr = css.match(/\n:root\[data-theme="dark"\] \{\n([^}]*--[^}]*)\}/);
    if (!media || !attr) throw new Error("dark token block not found");
    return [media[1], attr[1]];
  };
  const declarations = (block: string): Map<string, string> =>
    new Map(
      [...block.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [
        m[1],
        m[2].trim(),
      ]),
    );

  it.each([
    ["tokens.css", baseCss],
    ["tokens.demo.css", demoCss],
  ])("%s: the two dark blocks are declaration-identical", (_name, css) => {
    const [media, attr] = darkBlocks(css);
    expect(Object.fromEntries(declarations(attr))).toEqual(
      Object.fromEntries(declarations(media)),
    );
  });
});

// index.css styles everything against the token palette and defines no custom
// property of its own, so every var() it references must exist in tokens.css.
// A name that does not resolve is not a loud failure: the declaration silently
// falls back (or collapses), leaving a rule that looks live but paints nothing.
// A misspelled hover token shipped exactly that way once.
describe("design-token references", () => {
  const referenced = (css: string): Set<string> =>
    new Set([...css.matchAll(/var\(\s*(--[\w-]+)/g)].map((match) => match[1]));
  const defined = (css: string): Set<string> =>
    new Set(css.match(/--[\w-]+(?=\s*:)/g) ?? []);

  it("index.css defines no custom properties of its own", () => {
    // Guard against vacuous success on an empty/failed read.
    expect(indexCss.length).toBeGreaterThan(1000);
    expect([...defined(indexCss)]).toEqual([]);
  });

  it("every token index.css references is defined in tokens.css", () => {
    const names = referenced(indexCss);
    expect(names.size).toBeGreaterThan(20);
    // tokens.demo.css needs no separate pass: the parity pin above already
    // holds the two files to the same set of names.
    const palette = defined(baseCss);
    expect([...names].filter((name) => !palette.has(name))).toEqual([]);
  });
});
