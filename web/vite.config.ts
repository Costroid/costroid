// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

/// <reference types="vitest/config" />
import { defineConfig, type AliasOptions } from "vite";
import react from "@vitejs/plugin-react";

// Demo build (`vite build --mode demo`): swap the transport seam for the
// fixture-backed one and the full variable font for the Latin+Turkish subset.
// The finds are ANCHORED regexes so they match ONLY the bare specifiers
// "./api" and "./tokens.css" — never a nested path like "./api/schema" (a
// plain string alias would prefix-match and rewrite that too). Relative
// replacements keep node:url (and @types/node) out of the config.
const demoAlias: AliasOptions = [
  { find: /^\.\/api$/, replacement: "./api.demo" },
  { find: /^\.\/tokens\.css$/, replacement: "./tokens.demo.css" },
];

// The dev server proxies API paths to the Go server (default :8080) so
// `make dev` works without CORS.
export default defineConfig(({ mode }) => ({
  plugins: [react()],
  base: mode === "demo" ? "./" : "/",
  resolve: { alias: mode === "demo" ? demoAlias : ({} as AliasOptions) },
  build: { outDir: mode === "demo" ? "demo-dist" : "dist" },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
  },
}));
