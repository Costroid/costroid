package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const ingestBatchRows = 10_000

// BenchmarkIngestRows posts batches of 10,000 rows through the whole path:
// token check, parse, price and one batched write.
func BenchmarkIngestRows(b *testing.B) {
	db := storetest.New(b)
	db.Tenant(b, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		b.Fatal(err)
	}
	defer ledger.Close()
	w := newWorld(b, ledger)
	token := w.token()
	batch := 0
	var written time.Duration
	for b.Loop() {
		b.StopTimer()
		rows := make([]map[string]any, ingestBatchRows)
		for i := range rows {
			rows[i] = row(fmt.Sprintf("bench-%d-%d", batch, i), focus.Output, 1000+i)
		}
		body := lines(b, rows...)
		batch++
		b.StartTimer()
		started := time.Now()
		w.rows(token, body).want(b, http.StatusOK)
		written += time.Since(started)
	}
	perBatch := written / time.Duration(batch)
	b.ReportMetric(ingestBatchRows/perBatch.Seconds(), "rows/s")
}
