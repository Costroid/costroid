package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	ingestBatchRows = 10_000
	ingestStreams   = 4
)

func ingestWorld(b *testing.B) *world {
	b.Helper()
	db := storetest.New(b)
	db.Tenant(b, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(ledger.Close)
	return newWorld(b, ledger)
}

func ingestBody(b *testing.B, stream, batch int) []byte {
	b.Helper()
	rows := make([]map[string]any, ingestBatchRows)
	for i := range rows {
		rows[i] = row(fmt.Sprintf("bench-%d-%d-%d", stream, batch, i), focus.Output, 1000+i)
	}
	return lines(b, rows...)
}

// BenchmarkIngestRows posts batches of 10,000 rows through the whole path, one
// after another: token check, parse, price and one batched write.
func BenchmarkIngestRows(b *testing.B) {
	w := ingestWorld(b)
	token := w.token()
	batch := 0
	var written time.Duration
	for b.Loop() {
		b.StopTimer()
		body := ingestBody(b, 0, batch)
		batch++
		b.StartTimer()
		started := time.Now()
		w.rows(token, body).want(b, http.StatusOK)
		written += time.Since(started)
	}
	perBatch := written / time.Duration(batch)
	b.ReportMetric(ingestBatchRows/perBatch.Seconds(), "rows/s")
}

// BenchmarkIngestRowsConcurrently runs four such streams into one tenant on
// one instance and reports what the instance wrote per second.
func BenchmarkIngestRowsConcurrently(b *testing.B) {
	w := ingestWorld(b)
	token := w.token()
	var total time.Duration
	round := 0
	for b.Loop() {
		b.StopTimer()
		bodies := make([][]byte, ingestStreams)
		for stream := range bodies {
			bodies[stream] = ingestBody(b, stream, round)
		}
		round++
		b.StartTimer()
		started := time.Now()
		var group sync.WaitGroup
		for _, body := range bodies {
			group.Go(func() { w.rows(token, body).want(b, http.StatusOK) })
		}
		group.Wait()
		total += time.Since(started)
	}
	perRound := total / time.Duration(round)
	b.ReportMetric(ingestStreams*ingestBatchRows/perRound.Seconds(), "rows/s")
}
