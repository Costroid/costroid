// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightOpenAPI, { openAPISidebarGroups } from 'starlight-openapi';

export default defineConfig({
  site: 'https://docs.costroid.com',
  integrations: [
    starlight({
      title: 'Costroid Docs',
      description:
        'Documentation for Costroid — the open-source, self-hosted, FOCUS-native FinOps platform.',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/Costroid/costroid' },
      ],
      plugins: [
        starlightOpenAPI([
          { base: 'api', label: 'API Reference', schema: '../contracts/openapi.yaml' },
        ]),
      ],
      sidebar: [
        { label: 'Introduction', link: '/' },
        { label: 'Getting started', slug: 'getting-started' },
        {
          label: 'Security & deployment',
          items: [
            { label: 'Overview', slug: 'security' },
            { label: 'Threat model', slug: 'security/threat-model' },
          ],
        },
        {
          label: 'Connectors',
          items: [
            { label: 'Overview', slug: 'connectors' },
            { label: 'AWS', slug: 'connectors/aws' },
            { label: 'Azure', slug: 'connectors/azure' },
            { label: 'Google Cloud (Preview)', slug: 'connectors/gcp' },
            { label: 'AI vendors', slug: 'connectors/ai-vendors' },
            { label: 'FOCUS / CSV files', slug: 'connectors/focus-csv' },
          ],
        },
        {
          label: 'Guides',
          items: [
            { label: 'Multi-currency', slug: 'guides/multi-currency' },
            { label: 'Operations', slug: 'guides/operations' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'CLI flags', slug: 'reference/cli-flags' },
            { label: 'FOCUS coverage', slug: 'reference/focus-coverage' },
          ],
        },
        ...openAPISidebarGroups,
      ],
    }),
  ],
});
