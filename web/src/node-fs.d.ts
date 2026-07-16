// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Minimal declaration for the one Node builtin the test suite uses
// (tokens.test.ts reads the token files off disk — `?raw` imports resolve
// empty under vitest). The app tsconfig deliberately ships without
// @types/node; declaring exactly what we consume keeps it that way.
declare module "node:fs" {
  export function readFileSync(path: string | URL, encoding: "utf8"): string;
}
