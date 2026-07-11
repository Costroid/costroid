// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  // Generated files are never linted: src/api/schema.d.ts (from
  // contracts/openapi.yaml) and src/demo/ranges.ts (from internal/demofixtures).
  {
    ignores: ["dist", "demo-dist", "src/api/schema.d.ts", "src/demo/ranges.ts"],
  },
  js.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    extends: [tseslint.configs.recommended],
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
      // Allow intentionally-unused args prefixed with _ (e.g. the demo seam's
      // signal params, which mirror the network seam's surface without using it).
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
    },
  },
);
