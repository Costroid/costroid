// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  // src/api/schema.d.ts is generated from contracts/openapi.yaml — never lint it.
  { ignores: ["dist", "src/api/schema.d.ts"] },
  js.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    extends: [tseslint.configs.recommended],
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
    },
  },
);
