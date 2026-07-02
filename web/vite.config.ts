// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies API paths to the Go server (default :8080) so
// `make dev` works without CORS.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
  },
});
