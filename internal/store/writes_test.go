package store_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const writesRun = "4242"

// requestRow is one class of one provider request, as the telemetry path
// stores it: the row id is the request id's and the class's.
func requestRow(request string, class focus.TokenClass, run string) store.Record {
	r := record(focus.RowIdentity(request, class), "1", "windowed", "token_verified")
	r.Row.SkuMeter = class
	r.Row.Tags[store.TagWorkflowRunID] = run
	r.RequestID = request
	return r
}

// requestRows is a request's input, output and cache writes at the lifetimes given.
func requestRows(request, run string, writes ...focus.TokenClass) []store.Record {
	records := []store.Record{
		requestRow(request, focus.InputUncached, run),
		requestRow(request, focus.Output, run),
	}
	for _, class := range writes {
		records = append(records, requestRow(request, class, run))
	}
	return records
}

// storedWriteClasses lists the cache write classes stored, in order.
func storedWriteClasses(t *testing.T, db storetest.Database) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := connect(t, db.SuperURL).Query(ctx, `SELECT sku_meter FROM focus_rows WHERE sku_meter LIKE 'cache_write_%' ORDER BY sku_meter`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var classes []string
	for rows.Next() {
		var class string
		if err := rows.Scan(&class); err != nil {
			t.Fatal(err)
		}
		classes = append(classes, class)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return classes
}

func ingest(t *testing.T, s *store.Store, tenant store.TenantID, records []store.Record) store.Receipt {
	t.Helper()
	receipt, err := s.Ingest(context.Background(), tenant, batch(records...))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

// A request's writes reach the store in one batch at one lifetime and in a
// later batch at another. The first batch's split stands, whichever was stated.
func TestALaterBatchCannotAddAnotherLifetimeToAStoredRequest(t *testing.T) {
	for name, order := range map[string][2]focus.TokenClass{
		"stated 5m first":  {focus.CacheWrite5m, focus.CacheWrite1h},
		"assumed 1h first": {focus.CacheWrite1h, focus.CacheWrite5m},
	} {
		t.Run(name, func(t *testing.T) {
			db := storetest.New(t)
			s := open(t, db)
			a, _ := tenants(t, db)
			ingest(t, s, a, requestRows("req_1", writesRun, order[0]))
			receipt := ingest(t, s, a, requestRows("req_1", writesRun, order[1]))
			if receipt.Inserted != 0 || receipt.Duplicates != 3 {
				t.Fatalf("the second batch: %+v, want three duplicates", receipt)
			}
			if got := storedWriteClasses(t, db); !slices.Equal(got, []string{string(order[0])}) {
				t.Fatalf("write rows %v, want the first batch's %s only", got, order[0])
			}
		})
	}
}

func TestOneBatchKeepsEveryLifetimeOfAStatedSplit(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	receipt := ingest(t, s, a, requestRows("req_1", writesRun, focus.CacheWrite5m, focus.CacheWrite1h))
	if receipt.Inserted != 4 {
		t.Fatalf("%+v", receipt)
	}
	receipt = ingest(t, s, a, requestRows("req_1", writesRun, focus.CacheWrite1h, focus.CacheWrite5m))
	if receipt.Inserted != 0 || receipt.Duplicates != 4 {
		t.Fatalf("the repeat: %+v", receipt)
	}
	if got := storedWriteClasses(t, db); !slices.Equal(got, []string{"cache_write_1h", "cache_write_5m"}) {
		t.Fatalf("write rows %v", got)
	}
}

// A request's writes keep out another lifetime only in their own run and
// tenant, and a record that names no request is stored as it always was.
func TestOnlyOneRequestInOneRunIsMatched(t *testing.T) {
	cases := map[string]struct {
		second  func(store.Record) store.Record
		another bool
	}{
		"another request": {second: func(r store.Record) store.Record { return requestRow("req_2", r.Row.SkuMeter, writesRun) }},
		"another run":     {second: func(r store.Record) store.Record { return requestRow("req_1", r.Row.SkuMeter, "4243") }},
		"no request id": {second: func(r store.Record) store.Record {
			r.RequestID = ""
			return r
		}},
		"another tenant": {second: func(r store.Record) store.Record { return r }, another: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := storetest.New(t)
			s := open(t, db)
			a, b := tenants(t, db)
			ingest(t, s, a, []store.Record{requestRow("req_1", focus.CacheWrite5m, writesRun)})
			tenant := a
			if tc.another {
				tenant = b
			}
			second := tc.second(requestRow("req_1", focus.CacheWrite1h, writesRun))
			if receipt := ingest(t, s, tenant, []store.Record{second}); receipt.Inserted != 1 {
				t.Fatalf("the second write was left out: %+v", receipt)
			}
		})
	}
}

// Batches racing with one request's writes at different lifetimes store one
// split between them. They share no row id, so the unique index cannot order
// them.
func TestRacingBatchesOfOneRequestStoreOneLifetime(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	const senders = 8
	start := make(chan struct{})
	errs := make([]error, senders)
	var group sync.WaitGroup
	for i := range senders {
		group.Go(func() {
			<-start
			records := make([]store.Record, 0, 200)
			for n := range 200 {
				records = append(records, requestRow(fmt.Sprintf("req_%03d", n), writeLifetimes[i%len(writeLifetimes)], writesRun))
			}
			_, errs[i] = s.Ingest(context.Background(), a, batch(records...))
		})
	}
	close(start)
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(storedWriteClasses(t, db)); got != 200 {
		t.Fatalf("%d write rows for 200 requests", got)
	}
}

var writeLifetimes = []focus.TokenClass{focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m, focus.CacheWrite12h}
