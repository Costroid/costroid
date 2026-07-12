// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors
//
// Writes demo-artifacts/manifest.json — a provenance record for the deployed
// static demo (which git SHA / app version / FOCUS version / capture window /
// bundle sizes the live demo.costroid.com corresponds to). Node built-ins only.
//
// The manifest carries a build timestamp (builtAtUTC), which is exactly why it
// lives under demo-artifacts/ (gitignored) and NOT inside the byte-reproducible
// web/demo-dist bundle — the bundle must stay deterministic across rebuilds.

import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";

const ARTIFACTS = "demo-artifacts";
const OUT = `${ARTIFACTS}/manifest.json`;
const META = "web/src/demo/fixtures/meta.json";
const RANGES = "web/src/demo/ranges.ts";
const SIZES = `${ARTIFACTS}/sizes.json`;

// git SHA: prefer the CI-provided value; spawn git only as a local fallback.
let gitSha = process.env.GITHUB_SHA || "";
if (!gitSha) {
  try {
    gitSha = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  } catch (err) {
    console.error(`::error::Cannot resolve git SHA (no GITHUB_SHA, git failed): ${err.message}`);
    process.exit(1);
  }
}
const gitShaShort = gitSha.slice(0, 7);

// App + FOCUS version from the fixtures' meta.json (single source, no hardcode).
// meta.json uses the key `version` (not `appVersion`).
let meta;
try {
  meta = JSON.parse(readFileSync(META, "utf8"));
} catch (err) {
  console.error(`::error::Cannot read ${META}: ${err.message}`);
  process.exit(1);
}
if (!meta.version || !meta.focusVersion) {
  console.error(`::error::${META} missing version/focusVersion (got ${JSON.stringify(meta)})`);
  process.exit(1);
}

// fixtureAsOf: the capture window's end date. A plain-Node .mjs cannot import a
// .ts file and the zero-dep rule bans a TS loader, so read the generated
// ranges.ts as TEXT and pull the `full` preset's `end` out by regex.
const rangesText = readFileSync(RANGES, "utf8");
const asOfMatch = rangesText.match(/id:\s*"full"[\s\S]*?end:\s*"(\d{4}-\d{2}-\d{2})"/);
if (!asOfMatch) {
  console.error(`::error::Could not extract the 'full' preset end date from ${RANGES}`);
  process.exit(1);
}
const fixtureAsOf = asOfMatch[1];

// Sizes come from the budget step's sizes.json (run before this in the
// workflow). Fail clearly rather than silently omitting them.
if (!existsSync(SIZES)) {
  console.error(`::error::${SIZES} not found — run 'make demo-budget' before the manifest.`);
  process.exit(1);
}
let sizesRaw;
try {
  sizesRaw = JSON.parse(readFileSync(SIZES, "utf8"));
} catch (err) {
  console.error(`::error::Cannot parse ${SIZES}: ${err.message}`);
  process.exit(1);
}
const sizes = {
  appPayloadGzBytes: sizesRaw.app?.gzBytes,
  appBudgetBytes: sizesRaw.app?.budgetBytes,
  fontWireBytes: sizesRaw.font?.wireBytes,
  fontBudgetBytes: sizesRaw.font?.budgetBytes,
  rawJsBytes: sizesRaw.rawJsBytes,
  combinedFirstPaintBytes: sizesRaw.combinedFirstPaintBytes,
};

const manifest = {
  demo: true,
  gitSha,
  gitShaShort,
  builtAtUTC: new Date().toISOString(),
  appVersion: meta.version,
  focusVersion: meta.focusVersion,
  fixtureAsOf,
  sizes,
};

mkdirSync(ARTIFACTS, { recursive: true });
writeFileSync(OUT, JSON.stringify(manifest, null, 2) + "\n");
console.log(`Wrote ${OUT}:`);
console.log(JSON.stringify(manifest, null, 2));
