package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
)

// writeLifetimes are the cache write classes. A request's writes take one
// split, so a row of one lifetime and a row of another for the same request
// count its writes twice.
var writeLifetimes = []focus.TokenClass{focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m, focus.CacheWrite12h}

// lockWrites serializes the batches that write one request's cache writes, so
// two of them cannot both find the other's rows absent. A record that names
// its request locks the id of each lifetime, and one that names none locks its
// own id, so a batch from either path waits on the other. Keys are taken in
// order, so two batches never wait on each other.
const lockWrites = `SELECT count(pg_advisory_xact_lock(k)) FROM unnest($1::bigint[]) AS t(k)`

const (
	storedSiblings = `SELECT row_id, COALESCE(workflow_run_id, '') FROM focus_rows WHERE row_id = ANY($1::text[])`
	recordedWrites = `SELECT row_id, workflow_run_id FROM request_write_ids WHERE row_id = ANY($1::text[])`
	recordWrites   = `
INSERT INTO request_write_ids (tenant_id, row_id, workflow_run_id)
SELECT costroid_tenant(), id, run FROM unnest($1::text[], $2::text[]) AS t(id, run)
ON CONFLICT (tenant_id, row_id) DO NOTHING`
)

// batchWrites holds a batch's cache write records by the ids that can match
// them.
type batchWrites struct {
	// siblings maps an id another lifetime of a named request takes to the
	// records of that request.
	siblings map[string][]int
	// unnamed maps the id of a record that names no request to the record.
	unnamed map[string][]int
	// named maps the id every lifetime of a named request takes to the run of
	// the earliest record that named it.
	named map[string]string
	// locks lists the ids the batch locks.
	locks []string
}

func writesOf(records []Record) batchWrites {
	w := batchWrites{siblings: map[string][]int{}, unnamed: map[string][]int{}, named: map[string]string{}}
	for i := range records {
		request, class := records[i].RequestID, records[i].Row.SkuMeter
		if class.Lifetime() == "" {
			continue
		}
		if request == "" {
			id := records[i].Row.RowID
			w.unnamed[id] = append(w.unnamed[id], i)
			w.locks = append(w.locks, id)
			continue
		}
		for _, lifetime := range writeLifetimes {
			id := focus.RowIdentity(request, lifetime)
			if lifetime != class {
				w.siblings[id] = append(w.siblings[id], i)
			}
			if _, stated := w.named[id]; !stated {
				w.named[id] = records[i].Row.Tags[TagWorkflowRunID]
			}
			w.locks = append(w.locks, id)
		}
	}
	return w
}

// stored finds the cache write records whose request already has a write row
// of another lifetime stored in the same run. The first batch that holds a
// request's writes decides their split: rows are never rewritten, so a later
// split is left out even when it was stated and the stored one assumed. A
// record that names its request is matched by the ids of its other lifetimes
// among the stored rows. A record that names none is matched by its own id
// among the ids an earlier record of its request recorded.
func (w batchWrites) stored(ctx context.Context, tx pgx.Tx, tenant TenantID, records []Record) (map[int]bool, error) {
	if len(w.locks) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, lockWrites, lockKeys(tenant, w.locks)); err != nil {
		return nil, fmt.Errorf("store: lock requests: %w", err)
	}
	skip := map[int]bool{}
	if err := sameRun(ctx, tx, storedSiblings, w.siblings, records, skip); err != nil {
		return nil, err
	}
	if err := sameRun(ctx, tx, recordedWrites, w.unnamed, records, skip); err != nil {
		return nil, err
	}
	return skip, nil
}

// sameRun marks each record that query finds an id of, in the record's run.
func sameRun(ctx context.Context, tx pgx.Tx, query string, ids map[string][]int, records []Record, skip map[int]bool) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, query, slices.Collect(maps.Keys(ids)))
	if err != nil {
		return fmt.Errorf("store: read stored writes: %w", err)
	}
	var id, run string
	_, err = pgx.ForEachRow(rows, []any{&id, &run}, func() error {
		for _, i := range ids[id] {
			if records[i].Row.Tags[TagWorkflowRunID] == run {
				skip[i] = true
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: read stored writes: %w", err)
	}
	return nil
}

// record keeps the ids each named request's writes take, so a later record of
// the request that names none is known by its id.
func (w batchWrites) record(ctx context.Context, tx pgx.Tx) error {
	if len(w.named) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(w.named))
	runs := make([]string, len(ids))
	for i, id := range ids {
		runs[i] = w.named[id]
	}
	if _, err := tx.Exec(ctx, recordWrites, ids, runs); err != nil {
		return fmt.Errorf("store: record request writes: %w", err)
	}
	return nil
}

// lockKeys names each id once, in ascending order, within its tenant.
func lockKeys(tenant TenantID, ids []string) []int64 {
	keys := make([]int64, 0, len(ids))
	for _, id := range ids {
		sum := sha256.Sum256([]byte(tenant.text + "\x00" + id))
		keys = append(keys, int64(binary.BigEndian.Uint64(sum[:8])))
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}
