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
        { label: 'Security & deployment', slug: 'security' },
        ...openAPISidebarGroups,
      ],
    }),
  ],
});
