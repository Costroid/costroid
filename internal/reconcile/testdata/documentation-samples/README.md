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

The files that split cost by token type follow the cost endpoint's `token_type` and `line_item` dimensions:

| Provider  | Shape                                                        | Source, retrieved 2026-11-07                                                                                                        |
| --------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| Anthropic | `group_by[]=cost_type&group_by[]=token_type`, five token types | <https://platform.claude.com/docs/en/api/beta/organization/analytics/cost>                                                          |
| OpenAI    | `group_by=line_item`, a line item such as `gpt-6-astra, input_tokens` | <https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs.md> |

`anthropic/cost-*by-token-type*.json`, `anthropic/cost-tiny-day*.json`, `anthropic/usage-tiny-day.json` and `openai/costs-line-items.json` are hand-written in those shapes. OpenAI's page shows one line item kind, `input_tokens`. The other kinds in `openai/costs-line-items.json` are the usage report's own field names, and no response has yet shown them on a line item.
