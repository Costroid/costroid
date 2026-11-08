package server

import (
	"context"
	"maps"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
)

// storedWrites reads the stored cache write rows, class to tokens.
func (w *ledgerWorld) storedWrites(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT sku_meter, consumed_quantity::text FROM focus_rows WHERE sku_meter LIKE 'cache_write_%'`)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{}
	var class, tokens string
	if _, err := pgx.ForEachRow(rows, []any{&class, &tokens}, func() error {
		writes[class] = tokens
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return writes
}

// apiRequest is one Claude Code api_request event. With split set it states
// its cache writes' lifetime, and without it the lifetime is assumed.
func apiRequest(requestID string, split bool) map[string]string {
	event := map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5",
		"input_tokens": "10", "output_tokens": "2", "cache_creation_tokens": "100",
	}
	if requestID != "" {
		event["request_id"] = requestID
	}
	if split {
		event["cache_creation_5m_tokens"] = "100"
	}
	return event
}

// One request's usage reaches the edge in two OTLP requests: once with its
// cache writes split by lifetime and once with the lifetime assumed. Its
// writes are stored once, at the lifetime of the batch that came first.
func TestOneRequestInTwoBatchesStoresOneWriteRow(t *testing.T) {
	resource := map[string]string{"service.name": "claude-code"}
	for name, tc := range map[string]struct {
		first, second bool
		want          map[string]string
	}{
		"stated first":  {first: true, second: false, want: map[string]string{"cache_write_5m": "100.000000000000"}},
		"assumed first": {first: false, second: true, want: map[string]string{"cache_write_1h": "100.000000000000"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := postgresWorld(t)
			for _, split := range []bool{tc.first, tc.second} {
				body := claudeLogs(t, resource, apiRequest("req_1", split))
				w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
			}
			if got := w.storedWrites(t); !maps.Equal(got, tc.want) {
				t.Fatalf("write rows %v, want %v", got, tc.want)
			}
			if got := w.count(t); got != 3 {
				t.Fatalf("%d rows, want input, output and one write", got)
			}
		})
	}
}

// A request that carries no provider id is counted as it was before: its
// stand-in id matches no other batch's writes.
func TestARequestWithNoIDInTwoBatchesKeepsBothWriteRows(t *testing.T) {
	w := postgresWorld(t)
	resource := map[string]string{"service.name": "claude-code"}
	for _, split := range []bool{true, false} {
		body := claudeLogs(t, resource, apiRequest("", split))
		w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	}
	if got := w.storedWrites(t); len(got) != 2 {
		t.Fatalf("write rows %v, want both lifetimes", got)
	}
}
