# Documentation samples

These files are not recordings. No provider was called to make them.

Each one follows a response shape published in the provider's documentation. Files named `published-*` hold the documentation's own sample. Every other file holds synthetic figures and example ids in that shape, so a test can state its arithmetic by hand.

| Provider  | Endpoint                                  | Source, retrieved 2026-11-01                                                                                  |
| --------- | ----------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Anthropic | `GET /v1/organizations/analytics/usage`   | <https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api>                                  |
| Anthropic | `GET /v1/organizations/analytics/cost`    | <https://platform.claude.com/docs/en/api/beta/organization/analytics/cost.md>                                 |
| OpenAI    | `GET /v1/organization/usage/completions`  | <https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/completions.md> |
| OpenAI    | `GET /v1/organization/costs`              | <https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs.md> |

`anthropic/published-cost-row.json` is one row, as published. The documentation abbreviates its `group` object, and the file keeps only the keys shown there.
