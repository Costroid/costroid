---
title: Threat model
description: How Costroid structurally keeps AI prompt and response content out of its data path, and the residual risks it does not yet cover.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

Costroid connects to AI vendor cost and usage APIs (OpenAI and Anthropic). This
page states the one guarantee those connectors are built around, describes how
that guarantee is enforced in code and in CI, and lists honestly the risks it
does not cover.

## The rule

The Cardinal Rule (decision D7): Costroid ingests, stores, logs, and transmits
only cost and usage **metadata** from AI vendor cost and usage APIs. It never
ingests, stores, logs, caches, or transmits prompt or response content. What it
reads is limited to money amounts, currencies, day and month timestamps, model
and workspace identifiers, and aggregate token and request counts.

## Outbound alert payloads

Costroid can send two kinds of operational alert to an operator-configured
webhook or Slack channel: a sync-failure alert and a cost-anomaly alert. Both
are metadata only and neither ever carries a prompt, a response, or any usage
content.

They differ deliberately in one respect. A sync-failure alert carries no cost
figure at all. A cost-anomaly alert carries aggregate cost figures for the
anomalous day: the observed amount, the baseline median, the deviation, and the
threshold, each an exact decimal string, alongside a FOCUS service key, the
currency, the day, and the direction. This is a deliberate, documented widening
of the alert whitelist: those figures are aggregate cost metadata, which the
Cardinal Rule permits, and an anomaly alert is not useful without its magnitude.
The widening is bounded to a fixed payload struct; it adds no usage-metric value
and no free-form text drawn from a source.

## Data flow

Both AI connectors make outbound HTTP GET requests to a fixed allowlist of cost
and usage endpoints, and nothing else. There are no calls to `/projects`,
`/users`, or any other endpoint that would resolve an opaque identifier to a
human name, and there is no write surface.

The openai-cost connector requests exactly eleven paths:

```
/v1/organization/costs
/v1/organization/usage/completions
/v1/organization/usage/embeddings
/v1/organization/usage/moderations
/v1/organization/usage/images
/v1/organization/usage/audio_speeches
/v1/organization/usage/audio_transcriptions
/v1/organization/usage/code_interpreter_sessions
/v1/organization/usage/vector_stores
/v1/organization/usage/web_search_calls
/v1/organization/usage/file_search_calls
```

The anthropic-cost connector requests exactly two paths:

```
/v1/organizations/cost_report
/v1/organizations/usage_report/messages
```

Each response is decoded into a narrow set of Go structs. Any field the vendor
returns that those structs do not model is dropped by the JSON decoder, so a
field Costroid does not name never enters memory beyond the raw buffer.

## Structural enforcement (two layers, both CI-gated)

The guarantee does not rest on reviewers remembering the rule. Two layers make
it structural, and both are checked on every push.

**Layer 1, the shared wire chokepoint.** Every AI request goes through one small
HTTP GET package. The raw bytes of a successful (200) response live in an
unexported field that can leave the package only through a typed decode into a
caller's modeled struct: there is deliberately no accessor that returns or
renders the raw body. Any non-success (non-200) response drops the body entirely
and carries only the HTTP status plus a typed vendor error identifier (a code or
type), never the response body and never a vendor message (which could echo
request text). Transport errors are stripped of their query string before they
reach a log or an error value.

**Layer 2, the decode-field allowlist.** The complete set of JSON field names
each connector's current decode structs can read is committed to the repository
as an exact list. A reflection test walks those structs and compares the fields
it finds against the committed list, so adding or renaming any decoded field
turns the build red until a reviewer updates the list (a visible diff). A second
scan rejects the classic content field names (for example prompt, message,
content, completion, choices) outright. A third check enumerates the connectors
that route through the Layer 1 chokepoint and pins that set, so a future AI
connector cannot be added without a reviewer acknowledging the new AI vendor
fetch surface (the intended follow-through being to pin that connector's own
decode-field allowlist too).

## Appendix: the decode-field allowlist

This is the complete set of JSON fields the connectors' present decode structs
read. Every entry is a money amount, a currency, a timestamp, a model or
workspace identifier, or an aggregate token or request count. None of them is
prompt or response content.

openai-cost (20 fields):

```
amount, characters, currency, data, end_time, has_more, images, line_item,
model, next_page, num_model_requests, num_requests, num_sessions, project_id,
quantity, results, seconds, start_time, usage_bytes, value
```

anthropic-cost (24 fields):

```
amount, cache_creation, cache_read_input_tokens, context_window, cost_type,
currency, data, description, ending_at, ephemeral_1h_input_tokens,
ephemeral_5m_input_tokens, has_more, inference_geo, model, next_page,
output_tokens, results, server_tool_use, service_tier, starting_at, token_type,
uncached_input_tokens, web_search_requests, workspace_id
```

The token and request fields (for example output_tokens, uncached_input_tokens,
num_model_requests, web_search_requests) are aggregate counts, not content.

## Encryption at rest

Costroid can opt in to DuckDB-native encryption for an embedded store (create a
new store with a database-encryption key file, or convert an existing store
offline with `costroid store encrypt|rekey|decrypt`). With a key configured, the
database file, its write-ahead log (WAL), and DuckDB temporary files are
encrypted at rest. This protects data in a stolen store file, disk, or backup.

This boundary does not protect a running Costroid process, where the key and
plaintext data live in memory. It also does not protect a host where an
attacker can read the key file, a core dump of the running process, or plaintext
that remains in the operating system page cache. At-rest store encryption is
defense-in-depth alongside full-disk or filesystem encryption, not a replacement
for host hardening and key-file access controls.

## Residual risks

The guarantee above is narrow and honest. These are the things it does not do.

1. **No third-party security audit yet.** The claims on this page are backed by
   the code and its CI checks, not by an external review.
2. **The allowlist is scoped to today's decode structs.** It covers exactly the
   fields the current connectors decode. Adding a new decode target in future
   code requires extending the checked roots, and a reviewer has to keep them in
   step. The check catches a changed field on an existing struct automatically;
   it cannot see a struct nobody added to the roots.
3. **A persistence-boundary size bound is enforced, but it is not content
   classification.** Every ingested field value, and every usage-metric string,
   is length bounded at the persistence boundary: a value over 8 KiB is rejected
   rather than persisted, so a bulk prompt or response dump cannot be stored in
   any column, including a required, connector-controlled column such as a service
   name or a resource tag value. The usage-metric unit column is further
   constrained to an enforced closed allowlist. Three limits keep this claim
   honest. First, it is a size tripwire, not a content classifier, so short
   sensitive text under the bound still persists; keeping labels and resource tags
   free of sensitive values stays the deployer's responsibility. Second, tags are
   bounded per key and per string value, not in aggregate, so content spread
   across many small tag values is not caught by size alone. Third, this closes
   the oversized-dump path only; it does not claim to have solved the content
   problem.
4. **At-rest encryption is opt-in.** Without a database-encryption key file, the
   embedded store keeps its plaintext default. Adoption, re-key, and decrypt are
   offline (`costroid store encrypt|rekey|decrypt`); after encrypt, a retained
   plaintext `costroid.duckdb.bak` is itself a stolen-disk artifact until
   removed. Key custody, full-disk or filesystem encryption, and secure
   disposal of plaintext backups remain the deployer's responsibility.
5. **This is structural absence, not runtime classification.** The guarantee is
   that prompt and response content has no modeled path into Costroid at the AI
   connector boundary. It is not a runtime classifier that inspects values and
   decides whether they look like content.
