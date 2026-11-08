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

// lockRequests serializes the batches that write one request's cache writes,
// so two of them cannot both find the other's rows absent. Keys are taken in
// order, so two batches never wait on each other.
const lockRequests = `SELECT count(pg_advisory_xact_lock(k)) FROM unnest($1::bigint[]) AS t(k)`

const storedSiblings = `SELECT row_id, COALESCE(workflow_run_id, '') FROM focus_rows WHERE row_id = ANY($1::text[])`

// storedWrites finds the cache write records whose request already has a write
// row of another lifetime stored in the same run. The first batch that holds a
// request's writes decides their split: rows are never rewritten, so a later
// split is left out even when it was stated and the stored one assumed. A
// record with no request id is never matched.
func storedWrites(ctx context.Context, tx pgx.Tx, tenant TenantID, records []Record) (map[int]bool, error) {
	siblings := map[string][]int{}
	var requests []string
	for i := range records {
		request, class := records[i].RequestID, records[i].Row.SkuMeter
		if request == "" || class.Lifetime() == "" {
			continue
		}
		requests = append(requests, request)
		for _, other := range writeLifetimes {
			if other != class {
				id := focus.RowIdentity(request, other)
				siblings[id] = append(siblings[id], i)
			}
		}
	}
	if len(requests) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, lockRequests, lockKeys(tenant, requests)); err != nil {
		return nil, fmt.Errorf("store: lock requests: %w", err)
	}
	rows, err := tx.Query(ctx, storedSiblings, slices.Collect(maps.Keys(siblings)))
	if err != nil {
		return nil, fmt.Errorf("store: read stored writes: %w", err)
	}
	skip := map[int]bool{}
	var id, run string
	_, err = pgx.ForEachRow(rows, []any{&id, &run}, func() error {
		for _, i := range siblings[id] {
			if records[i].Row.Tags[TagWorkflowRunID] == run {
				skip[i] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read stored writes: %w", err)
	}
	return skip, nil
}

// lockKeys names each request once, in ascending order, within its tenant.
func lockKeys(tenant TenantID, requests []string) []int64 {
	keys := make([]int64, 0, len(requests))
	for _, request := range requests {
		sum := sha256.Sum256([]byte(tenant.text + "\x00" + request))
		keys = append(keys, int64(binary.BigEndian.Uint64(sum[:8])))
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}
