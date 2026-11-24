package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
	recordedWrites = `
SELECT row_id, workflow_run_id, verified, coalesce(repo, '') FROM request_write_ids WHERE row_id = ANY($1::text[])`
	recordWrites = `
INSERT INTO request_write_ids (tenant_id, row_id, workflow_run_id, verified, repo, request_key)
SELECT costroid_tenant(), id, run, verified, repo, request
FROM unnest($1::text[], $2::text[], $3::boolean[], $4::text[], $5::text[]) AS t(id, run, verified, repo, request)
ON CONFLICT DO NOTHING`
)

// namedWrite is what a batch records for one id a named request's writes
// take: the owner of the earliest record that named the request, and the
// request's key, the id its five-minute write takes.
type namedWrite struct {
	owner   owner
	request string
}

// batchWrites holds a batch's cache write records by the ids that can match
// them.
type batchWrites struct {
	// siblings maps an id another lifetime of a named request takes to the
	// records of that request.
	siblings map[string][]int
	// unnamed maps the id of a record that names no request to the record.
	unnamed map[string][]int
	// named maps the id every lifetime of a named request takes to what the
	// batch records for it.
	named map[string]namedWrite
	// locks lists the ids the batch locks.
	locks []string
}

func writesOf(records []Record) batchWrites {
	w := batchWrites{siblings: map[string][]int{}, unnamed: map[string][]int{}, named: map[string]namedWrite{}}
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
		key := focus.RowIdentity(request, writeLifetimes[0])
		for _, lifetime := range writeLifetimes {
			id := focus.RowIdentity(request, lifetime)
			if lifetime != class {
				w.siblings[id] = append(w.siblings[id], i)
			}
			if _, stated := w.named[id]; !stated {
				w.named[id] = namedWrite{owner: ownerOf(&records[i]), request: key}
			}
			w.locks = append(w.locks, id)
		}
	}
	return w
}

// lock takes the batch's locks on its requests' writes.
func (w batchWrites) lock(ctx context.Context, tx pgx.Tx, tenant TenantID) error {
	if len(w.locks) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, lockWrites, lockKeys(tenant, w.locks)); err != nil {
		return fmt.Errorf("store: lock requests: %w", err)
	}
	return nil
}

// readIDs lists the ids whose stored statements decide the batch: its own,
// and those of its named requests' other lifetimes.
func (w batchWrites) readIDs(records []Record) []string {
	ids := make([]string, 0, len(records)+len(w.siblings))
	for i := range records {
		ids = append(ids, records[i].Row.RowID)
	}
	return append(ids, slices.Collect(maps.Keys(w.siblings))...)
}

// leaveOutStored marks the cache write records whose request already has a
// write row of another lifetime that keeps them out: the first batch that
// holds a request's writes decides their split, so a later split is left out
// even when it was stated and the stored one assumed. Rows are never
// rewritten. A record that names its request is matched by the ids of its
// other lifetimes among the stored rows. A record that names none is matched
// by its own id among the ids an earlier record of its request recorded. A
// record is kept out as a statement of its own id would be: by its own
// owner's write, or, when the ledger did not verify it, by anyone's. An
// unverified claim of a run never keeps that run's write out. m notes each
// record whose request's writes a statement of the other rank also holds.
func (w batchWrites) leaveOutStored(ctx context.Context, tx pgx.Tx, held statements, records []Record, skip map[int]bool, m meetings) error {
	for id, indexes := range w.siblings {
		for _, h := range held[id] {
			for _, i := range indexes {
				mine := ownerOf(&records[i])
				if mine.leftOutBy(h) {
					skip[i] = true
				}
				m.byRank(i, mine, h)
			}
		}
	}
	if len(w.unnamed) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, recordedWrites, slices.Collect(maps.Keys(w.unnamed)))
	if err != nil {
		return fmt.Errorf("store: read recorded writes: %w", err)
	}
	var id, run, repo string
	var verified pgtype.Bool
	_, err = pgx.ForEachRow(rows, []any{&id, &run, &verified, &repo}, func() error {
		for _, i := range w.unnamed[id] {
			if keptOutByRecord(&records[i], verified, repo, run) {
				skip[i] = true
			}
			if verified.Valid {
				m.byRank(i, ownerOf(&records[i]), owner{verified: verified.Bool, repo: repo, run: run})
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: read recorded writes: %w", err)
	}
	return nil
}

// keptOutByRecord reports whether a recorded id keeps a record out. An id
// recorded before owners were names a run alone, and keeps out a record of
// that run, as it did.
func keptOutByRecord(record *Record, verified pgtype.Bool, repo, run string) bool {
	if !verified.Valid {
		return record.Row.Tags[TagWorkflowRunID] == run
	}
	held := owner{}
	if verified.Bool {
		held = owner{verified: true, repo: repo, run: run}
	}
	return ownerOf(record).leftOutBy(held)
}

// record keeps the ids each named request's writes take, so a later record of
// the request that names none is known by its id.
func (w batchWrites) record(ctx context.Context, tx pgx.Tx) error {
	if len(w.named) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(w.named))
	runs, verified, repos, requests := make([]string, len(ids)), make([]bool, len(ids)), make([]string, len(ids)), make([]string, len(ids))
	for i, id := range ids {
		named := w.named[id]
		runs[i], verified[i], repos[i], requests[i] = named.owner.run, named.owner.verified, named.owner.repo, named.request
	}
	if _, err := tx.Exec(ctx, recordWrites, ids, runs, verified, repos, requests); err != nil {
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
