# Send spans over OTLP

`costroid emit-otlp` sends a rows file to an OpenTelemetry Collector or a tracing backend as traces over OTLP/HTTP. Each model request becomes one span.

```sh
costroid emit-otlp --rows rows.jsonl
```

The rows file is the one `costroid run --out` or `costroid ingest-run --out` wrote. The command reads it and changes nothing in it.

## What a span holds

A span is named `chat <model>`, of kind client, and runs from the request's `ChargePeriodStart` to its `ChargePeriodEnd`, in whole seconds. The rows of one request, one per token class, make one span.

| Attribute | Value |
| --------- | ----- |
| `gen_ai.operation.name` | `chat` |
| `gen_ai.provider.name` | The row's provider. A Google row reads `gcp.gen_ai`, because the row does not say which Google endpoint served it |
| `gen_ai.request.model` | The row's model |
| `gen_ai.usage.input_tokens` | Every input token: uncached, cache read and cache write |
| `gen_ai.usage.output_tokens` | Output tokens |
| `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_write.input_tokens`, `gen_ai.usage.reasoning.output_tokens` | Set when not zero. The cache write count sums every lifetime |
| `gen_ai.conversation.id`, `gen_ai.agent.name`, `gen_ai.agent.version` | The rows' session, agent and agent version |
| `vcs.repository.url.full`, `vcs.change.id`, `vcs.ref.head.revision`, `cicd.pipeline.run.id` | The repository, pull request, head commit and CI run the rows name |
| `costroid.cost.amount`, `costroid.cost.currency`, `costroid.cost.basis` | The request's cost: the exact sum of its rows' `ListCost`, their currency and their cost basis |
| `costroid.enforcement_grade` | What held the run to its cap |
| `x_CostroidServiceTier`, `x_CostroidCatalogVersion` | The rows' service tier and the catalog release that priced them |
| `x_CostroidRowId` | The ids of the request's rows, to join a span back to them |

Token counts are integers. The GenAI names follow `open-telemetry/semantic-conventions-genai` at commit `e6658f14c9d9ea0760825f029cb82e7673f48fab`, `model/gen-ai/registry.yaml`, which had no release on 2026-11-08 ([repository](https://github.com/open-telemetry/semantic-conventions-genai), retrieved 2026-11-08). The `vcs.*` and `cicd.*` names follow [semantic conventions](https://github.com/open-telemetry/semantic-conventions) v1.45.0, a release candidate when read on 2026-11-08.

Every span of one CI run shares a trace id, made from the repository and the run. Outside CI, each request is its own trace. A span's id is made from its first row's id, so sending the same file twice repeats the same ids.

The resource names `service.name` `costroid` and `service.version` the release. `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` are not read, so a backend never files these spans as the agent's own.

## The cost is a string

The amount, its currency and its basis travel as three strings. No cost goes out as a number, so no backend rounds it to a float, and no amount is added to one in another currency. `gen_ai.usage.cost` is not sent. Read `costroid.cost.basis` beside every amount: `measured` is an API key's usage, and `list_equivalent` is a subscription run priced at list, which nobody billed.

## Where the spans go

`emit-otlp` reads the variables an OpenTelemetry SDK's OTLP exporter reads ([OTLP exporter specification](https://opentelemetry.io/docs/specs/otel/protocol/exporter/), retrieved 2026-11-08). Each has an `OTEL_EXPORTER_OTLP_TRACES_` form, which takes precedence.

| Variable | Meaning |
| -------- | ------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | A base URL. `v1/traces` is appended to it. Without it, spans go to `http://localhost:4318/v1/traces` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | The full URL, used as it is |
| `OTEL_EXPORTER_OTLP_HEADERS` | `key=value` pairs separated by commas, such as an API key's header |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf`, the default, or `http/json`. `grpc` is refused |
| `OTEL_EXPORTER_OTLP_TIMEOUT` | In milliseconds, 10000 by default |
| `OTEL_EXPORTER_OTLP_COMPRESSION` | `gzip` or `none` |
| `OTEL_EXPORTER_OTLP_CERTIFICATE`, `OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE`, `OTEL_EXPORTER_OTLP_CLIENT_KEY` | PEM files for the server's certificate and a client certificate |

A protocol, timeout or compression the specification does not define draws a warning and reads as unset. At most 512 spans go in one request. An answer of 429, 502, 503 or 504 is tried again, three tries in all. The wait starts near 1 second and doubles, or is the answer's `Retry-After`, and a wait past 30 seconds ends the tries ([OTLP 1.11.1](https://opentelemetry.io/docs/specs/otlp/), retrieved 2026-11-08). A redirect is never followed, so a header never reaches another host. A printed endpoint shows its scheme, host and path only.

## Count each request once

An agent that sends its own telemetry to the same backend states the same tokens there. Filter on `service.name` before adding token counts up.

`costroid serve` reads these spans at `/otlp/v1/traces`: their token counts, `costroid.cost.basis` and `costroid.enforcement_grade`. It reprices every request from its own catalog and never reads the amount. A span carries no provider request id, so a request whose rows the same ledger already took counts twice. Send a run to a ledger one way, its rows or its spans. [`docs/serve.md`](serve.md) says how `serve` reads OTLP.

`serve` reads a span from a sender it has no dialect for as the GenAI convention asks: the cache reads and writes "SHOULD be included in `gen_ai.usage.input_tokens`" ([attribute registry](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/), semantic conventions v1.41.0, retrieved 2026-11-12). A span whose cache counts pass its input can only come from a sender that leaves them out, as Anthropic's API reports input. `serve` reads that input as uncached and keeps the cache counts, and each of the request's rows says so. Such a sender whose input is above its cache counts cannot be told apart from one that follows the convention, and its figure reads low.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | The endpoint took every span, or the rows file holds no rows |
| 1 | The endpoint refused spans, and the line says how many |
| 125 | Costroid could not read the rows or the variables, or reach the endpoint |

[`docs/troubleshooting.md`](troubleshooting.md#sending-spans-over-otlp) explains every line `emit-otlp` prints.
