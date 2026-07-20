// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors
//
// Authoritative size-budget gate for the backendless static demo bundle
// (web/demo-dist, produced by `make demo-build`). Node built-ins only
// (fs, zlib, path) — zero package.json dependencies. Exits non-zero on any
// breach so CI's build-gate job fails loudly.
//
// Three INDEPENDENT sub-budgets, each measured the way it actually ships:
//   - First-paint app (index.html + referenced JS + all CSS): summed gzip
//     size at level 9 (each file is served gzip-compressed) <= 150 KB.
//   - Lazy chunks (unreferenced JS/MJS): summed gzip size at level 9
//     <= 192 KB.
//   - Font (assets/*.woff2): raw WIRE bytes (woff2 is already compressed,
//     so gz is the wrong metric) <= 80 KB.
// The app and font gates bound the combined first-paint transfer to <= 230 KB
// worst case. The lazy gate separately bounds on-demand fixture payload.
//
// Filtered fixtures are deliberately code-split. Base fixtures remain in the
// entry chunk, while provider-filtered fixtures load on demand as same-origin
// static chunks and are charged to the separate lazy budget.

import { readdirSync, readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join, extname, relative } from "node:path";

// gzip level 9 reproduces `gzip -9` within a few hundred bytes; Node's default
// level 6 is looser than the ground-truth measurement, so pin level 9.
const GZIP = (buf) => gzipSync(buf, { level: 9 }).length;

const APP_BUDGET = 150 * 1024; // 153,600 B — first-paint html+css+referenced js
const LAZY_BUDGET = 192 * 1024; // 196,608 B, summed gz@9 of lazy js/mjs
const FONT_BUDGET = 80 * 1024; //  81,920 B — woff2 wire bytes
const COMBINED_CEILING = APP_BUDGET + FONT_BUDGET; // 230 KB worst case

const FIRST_PAINT_EXTS = new Set([".html", ".css", ".js", ".mjs"]);
const JS_EXTS = new Set([".js", ".mjs"]);
const FONT_EXTS = new Set([".woff2"]);

// CI passes no argument -> measures web/demo-dist. An optional first CLI arg
// points at an alternate built dir (used only to prove the gate FAILS on a
// deliberately-oversized copy); it can never weaken the deployed bundle's gate
// because the workflow always invokes this with no argument.
const DIST = process.argv[2] || "web/demo-dist";
const ARTIFACTS = "demo-artifacts";
const SIZES_JSON = join(ARTIFACTS, "sizes.json");

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full));
    else if (entry.isFile()) out.push(full);
  }
  return out;
}

let files;
let indexHtml;
try {
  files = walk(DIST);
  indexHtml = readFileSync(join(DIST, "index.html"), "utf8");
} catch (err) {
  console.error(
    `::error::Cannot read ${DIST} — run 'make demo-build' first. (${err.message})`,
  );
  process.exit(1);
}
if (files.length === 0) {
  console.error(`::error::${DIST} is empty — run 'make demo-build' first.`);
  process.exit(1);
}

const referencedAssets = new Set(
  Array.from(indexHtml.matchAll(/\b(?:src|href)="([^"]+)"/g), (match) =>
    match[1].replace(/^\.\//, ""),
  ),
);

const appAssets = [];
const lazyAssets = [];
const fontAssets = [];
const otherAssets = []; // ungated; surfaced so nothing unexpected hides
let rawJsBytes = 0;

for (const file of files.sort()) {
  const buf = readFileSync(file);
  const ext = extname(file).toLowerCase();
  const name = relative(DIST, file);
  if (JS_EXTS.has(ext)) rawJsBytes += buf.length;

  if (FONT_EXTS.has(ext)) {
    fontAssets.push({ name, wireBytes: buf.length });
  } else if (
    FIRST_PAINT_EXTS.has(ext) &&
    (name === "index.html" || ext === ".css" || referencedAssets.has(name))
  ) {
    appAssets.push({ name, rawBytes: buf.length, gzBytes: GZIP(buf) });
  } else if (JS_EXTS.has(ext)) {
    lazyAssets.push({ name, rawBytes: buf.length, gzBytes: GZIP(buf) });
  } else {
    otherAssets.push({ name, rawBytes: buf.length });
  }
}

const appGzTotal = appAssets.reduce((n, a) => n + a.gzBytes, 0);
const lazyGzTotal = lazyAssets.reduce((n, a) => n + a.gzBytes, 0);
const fontWireTotal = fontAssets.reduce((n, a) => n + a.wireBytes, 0);
const combinedFirstPaint = appGzTotal + fontWireTotal;

const appPass = appGzTotal <= APP_BUDGET;
const lazyPass = lazyGzTotal <= LAZY_BUDGET;
const fontPass = fontWireTotal <= FONT_BUDGET;

// ---- report table -----------------------------------------------------------
const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
const rows = [];
for (const a of appAssets)
  rows.push([
    a.name,
    `${a.rawBytes}`,
    `${a.gzBytes} (gz@9)`,
    "first-paint",
    "",
  ]);
for (const a of lazyAssets)
  rows.push([a.name, `${a.rawBytes}`, `${a.gzBytes} (gz@9)`, "lazy", ""]);
for (const a of fontAssets)
  rows.push([a.name, `${a.wireBytes}`, `${a.wireBytes} (wire)`, "font", ""]);
for (const a of otherAssets)
  rows.push([a.name, `${a.rawBytes}`, "-", "ungated", "(unexpected)"]);
rows.push([
  "FIRST-PAINT TOTAL",
  "",
  `${appGzTotal} gz@9`,
  `<= ${APP_BUDGET}`,
  appPass ? "PASS" : `FAIL (+${appGzTotal - APP_BUDGET} B)`,
]);
rows.push([
  "LAZY TOTAL",
  "",
  `${lazyGzTotal} gz@9`,
  `<= ${LAZY_BUDGET}`,
  lazyPass ? "PASS" : `FAIL (+${lazyGzTotal - LAZY_BUDGET} B)`,
]);
rows.push([
  "FONT TOTAL",
  "",
  `${fontWireTotal} wire`,
  `<= ${FONT_BUDGET}`,
  fontPass ? "PASS" : `FAIL (+${fontWireTotal - FONT_BUDGET} B)`,
]);

const header = ["ASSET", "RAW", "GZ-OR-WIRE", "BUDGET", "STATUS"];
const widths = header.map((h, i) =>
  Math.max(h.length, ...rows.map((r) => String(r[i]).length)),
);
const fmt = (r) => r.map((c, i) => String(c).padEnd(widths[i])).join("  ");

console.log(`\nDemo bundle budget — ${DIST}\n`);
console.log(fmt(header));
console.log(widths.map((w) => "-".repeat(w)).join("  "));
for (const r of rows) console.log(fmt(r));
console.log("");
console.log(
  `Raw JS bytes (signal, ungated): ${rawJsBytes} (${kb(rawJsBytes)}) — past Vite's 500 KB warn; watch parse/TTI`,
);
console.log(
  `Combined first-paint transfer (app gz@9 + font wire): ${combinedFirstPaint} (${kb(combinedFirstPaint)}) — bounded <= ${COMBINED_CEILING} by the two sub-gates`,
);
console.log("");

// ---- persist for the manifest (one source of truth) -------------------------
const sizes = {
  distDir: DIST,
  app: {
    budgetBytes: APP_BUDGET,
    gzBytes: appGzTotal,
    pass: appPass,
    assets: appAssets,
  },
  lazy: {
    budgetBytes: LAZY_BUDGET,
    gzBytes: lazyGzTotal,
    pass: lazyPass,
    assets: lazyAssets,
  },
  font: {
    budgetBytes: FONT_BUDGET,
    wireBytes: fontWireTotal,
    pass: fontPass,
    assets: fontAssets,
  },
  rawJsBytes,
  combinedFirstPaintBytes: combinedFirstPaint,
  combinedCeilingBytes: COMBINED_CEILING,
  other: otherAssets,
};
mkdirSync(ARTIFACTS, { recursive: true });
writeFileSync(SIZES_JSON, JSON.stringify(sizes, null, 2) + "\n");
console.log(`Wrote ${SIZES_JSON}`);

if (!appPass || !lazyPass || !fontPass) {
  const offenders = [];
  if (!appPass)
    offenders.push(
      `first-paint app ${appGzTotal} B gz@9 > ${APP_BUDGET} B (+${appGzTotal - APP_BUDGET})`,
    );
  if (!lazyPass)
    offenders.push(
      `lazy chunks ${lazyGzTotal} B gz@9 > ${LAZY_BUDGET} B (+${lazyGzTotal - LAZY_BUDGET})`,
    );
  if (!fontPass)
    offenders.push(
      `font ${fontWireTotal} B wire > ${FONT_BUDGET} B (+${fontWireTotal - FONT_BUDGET})`,
    );
  console.error(`::error::Demo bundle exceeds budget: ${offenders.join("; ")}`);
  process.exit(1);
}
console.log("Budget OK: first-paint, lazy, and font all within budget.");
