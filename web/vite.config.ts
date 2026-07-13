// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

/// <reference types="vitest/config" />
import { defineConfig, type AliasOptions, type Plugin } from "vite";
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

// Demo-only: emit a Cloudflare Pages `_headers` file that restricts who may
// frame the public demo to the marketing site (defense-in-depth). The demo is
// public/backendless/read-only, so this is brand-posture hardening, not risk
// removal. frame-ancestors MUST be an HTTP header (a <meta> CSP frame-ancestors
// is ignored by browsers); X-Frame-Options is deliberately OMITTED because it
// cannot express a cross-host allowlist (ALLOW-FROM is dead in Chrome), so it
// would only block the legitimate marketing-site embed.
function demoFrameAncestors(): Plugin {
  return {
    name: "demo-frame-ancestors",
    apply: "build",
    generateBundle() {
      this.emitFile({
        type: "asset",
        fileName: "_headers",
        source:
          "/*\n  Content-Security-Policy: frame-ancestors https://costroid.com https://www.costroid.com\n",
      });
    },
  };
}

// The dev server proxies API paths to the Go server (default :8080) so
// `make dev` works without CORS.
export default defineConfig(({ mode }) => ({
  plugins: mode === "demo" ? [react(), demoFrameAncestors()] : [react()],
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
