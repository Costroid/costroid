package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Costroid/costroid/internal/otlpemit"
)

const emitOTLPHelp = `Usage:
  costroid emit-otlp --rows rows.jsonl

emit-otlp sends a rows file to an OpenTelemetry Collector or backend as
traces over OTLP/HTTP, one span per model request. A span carries the
OpenTelemetry GenAI token counts as integers, and the repository, pull
request, commit and run its rows name as vcs.* and cicd.* attributes. Its
cost travels as three strings: costroid.cost.amount, the exact sum of the
request's ListCost, costroid.cost.currency and costroid.cost.basis. No cost
goes out as a number, so no backend rounds it, and no amount is added to one
in another currency.

emit-otlp reads the variables an OpenTelemetry SDK's OTLP exporter reads.
OTEL_EXPORTER_OTLP_ENDPOINT is a base URL, and v1/traces is appended to it;
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is used as it is. With neither, spans go
to http://localhost:4318/v1/traces. OTEL_EXPORTER_OTLP_HEADERS holds
key=value pairs separated by commas. OTEL_EXPORTER_OTLP_PROTOCOL is
http/protobuf, the default, or http/json; grpc is refused.
OTEL_EXPORTER_OTLP_TIMEOUT is in milliseconds, 10000 by default.
OTEL_EXPORTER_OTLP_COMPRESSION is gzip or none. OTEL_EXPORTER_OTLP_CERTIFICATE,
OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE and OTEL_EXPORTER_OTLP_CLIENT_KEY name
PEM files. Each has an OTEL_EXPORTER_OTLP_TRACES_ form that takes precedence.

The spans name service.name costroid. An agent that sends its own telemetry
to the same backend states the same tokens there, so filter on service.name
before adding token counts up.

Exit codes of emit-otlp:
  0                 the endpoint took every span, or the rows file holds no
                    rows
  1                 the endpoint refused spans, and the line says how many
  125               costroid could not read the rows or the variables, or
                    reach the endpoint
`

func emitOTLPCommand(ctx context.Context, args []string, std stdio) int {
	return emitOTLP(ctx, args, os.Getenv, std.stdout, std.stderr)
}

func emitOTLP(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("emit-otlp", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file costroid run wrote")
	if err := parseFlags(set, args, emitOTLPHelp, stdout, stderr); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return exitFailure
	}
	if *rows == "" || set.NArg() > 0 {
		fmt.Fprintln(stderr, "costroid: emit-otlp needs --rows, and takes no argument")
		return exitFailure
	}
	config, warnings, err := otlpemit.FromEnvironment(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "costroid: warning: %s\n", warning)
	}
	data, err := os.ReadFile(*rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	endpoint := otlpemit.Redacted(config.Endpoint)
	result, err := otlpemit.Emitter{Config: config, Version: version}.Emit(ctx, data)
	var refused *otlpemit.RefusedError
	switch {
	case errors.As(err, &refused):
		fmt.Fprintf(stderr, "costroid: %s answered HTTP %d to %d spans, so they were not taken; %d spans went before them\n", endpoint, refused.Status, refused.Spans, result.Spans)
		return exitRejected
	case err != nil:
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	case result.Rows == 0:
		fmt.Fprintf(stderr, "costroid: %s holds no rows, so nothing was sent\n", *rows)
		return 0
	case result.Rejected > 0:
		fmt.Fprintf(stderr, "costroid: %s refused %d of %d spans: %q\n", endpoint, result.Rejected, result.Spans, result.Message)
		return exitRejected
	}
	fmt.Fprintf(stdout, "costroid: sent %d spans for %d rows to %s\n", result.Spans, result.Rows, endpoint)
	return 0
}
