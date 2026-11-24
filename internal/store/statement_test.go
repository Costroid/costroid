package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// ownersMigration is the migration from which a statement of a row has an
// owner.
const ownersMigration = "0014_statement_owner.sql"

// unverified is r as an ingest key's sender states it: unattributed, with
// every join key a claim.
func unverified(r store.Record) store.Record {
	r = cloneRecord(r)
	r.Row.Tags[focus.TagAttributionGrade] = "unattributed"
	r.Row.Tags[focus.TagRunIdentity] = "unverified"
	r.CredentialBasis = ""
	return r
}

// unnamed is r as the result stream states it, naming no request.
func unnamed(r store.Record) store.Record {
	r.RequestID = ""
	return r
}

// pullLines reads a pull request's lines of repoA.
func (l ledger) pullLines(t *testing.T, pr string) []store.Line {
	t.Helper()
	lines, err := l.s.PullRequest(context.Background(), l.tenant, repoA, pr)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// met is how a row met other statements of its charge: contested by another
// run, and outranking an unverified statement.
type met struct {
	contested, outranked int64
}

// requireOnlyTheRunsRow holds the run's pull request to the run's own row:
// one line, of one row, of its meter, at its list cost and grade, eligible for
// a budget, and meeting other statements as want says.
func (l ledger) requireOnlyTheRunsRow(t *testing.T, meter focus.TokenClass, list, grade string, want met) {
	t.Helper()
	lines := l.pullLines(t, prA)
	if len(lines) != 1 || lines[0].Rows != 1 || lines[0].SkuMeter != string(meter) || lines[0].ListCost.String() != list ||
		lines[0].AttributionGrade != grade || !lines[0].BudgetEligible || (met{lines[0].Contested, lines[0].Outranked}) != want {
		t.Fatalf("the run's pull request reads %+v, want the run's own %s row at %s, %s, %+v", lines, meter, list, grade, want)
	}
}

// requireCountedOnTheRunsKey holds the pull request's gateway figure to the
// count on the run's key, standing behind the run's one row.
func (l ledger) requireCountedOnTheRunsKey(t *testing.T) {
	t.Helper()
	if rows, usd := gatewaySpend(t, l.s, l.tenant); rows != 1 || usd != "0.5" {
		t.Fatalf("the gateway's figure: %d rows, USD %s, want the run's key's USD 0.5 behind its one row", rows, usd)
	}
}

// TestAStatementOfAnotherRunNeverDisplacesARunsOwn states a run's exact row,
// on a key attested and counted for the run, and has another run of the tenant
// state the same row id at another amount, before the run and after it. The
// other run is a run of another pull request, a run of another repository that
// carries the same run number, or a push run that names no pull request.
// Whichever came first, the run's statement is kept, its pull request reads it
// at its own amount and grade with the key's count behind it, and the other
// statement stands where it claims. Both are contested.
func TestAStatementOfAnotherRunNeverDisplacesARunsOwn(t *testing.T) {
	others := map[string]func() store.Record{
		"a run of another pull request": func() store.Record {
			r := record(rowID(51), "9", "windowed", "token_verified")
			r.Row.Tags[store.TagWorkflowRunID], r.Row.Tags[store.TagPR] = "8", "13"
			return r
		},
		"a run of another repository": func() store.Record {
			r := record(rowID(51), "9", "windowed", "token_verified")
			r.Row.Tags[store.TagWorkflowRunID], r.Row.Tags[store.TagRepo] = restatedRun, "github.com/acme/gadgets"
			return r
		},
		"a push run": func() store.Record {
			r := record(rowID(51), "9", "windowed", "token_verified")
			r.Row.Tags[store.TagWorkflowRunID] = "9"
			delete(r.Row.Tags, store.TagPR)
			return r
		},
	}
	for name, other := range others {
		for _, otherFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, stated first %t", name, otherFirst), func(t *testing.T) {
				l := newLedger(t)
				own := runRow(rowID(51), "0.25", l.key, restatedRun, "1")
				batches := [][]store.Record{{own}, {other()}}
				if otherFirst {
					slices.Reverse(batches)
				}
				ingest(t, l.s, l.tenant, batches[0])
				if receipt := ingest(t, l.s, l.tenant, batches[1]); receipt != (store.Receipt{Inserted: 1, Contested: 1}) {
					t.Fatalf("the later statement's batch: %+v, want it kept and contested", receipt)
				}
				l.requireOnlyTheRunsRow(t, focus.Output, "0.25", "exact", met{contested: 1})
				l.requireCountedOnTheRunsKey(t)
				if stands := l.standing(t, rowID(51)); len(stands) != 2 || !stands[0].contested || !stands[1].contested {
					t.Fatalf("stands %+v, want both statements, each contested", stands)
				}
			})
		}
	}
}

// writeOf is one lifetime of a provider request's cache writes in the run, on
// the run's pull request.
func writeOf(class focus.TokenClass) store.Record {
	return requestRow("req_written", class, restatedRun)
}

// TestARunsStatementOutranksAnUnverifiedOne has an ingest key's sender and the
// run state one charge, in both orders. The sender claims the run's pull
// request and the run, at another amount. Whichever came first, the pull
// request reads the run's statement alone, eligible for its budget, and the
// gateway's count on the run's key stands behind it. A request's cache writes
// are one charge at every lifetime, so the run's write outranks the sender's
// at another lifetime too, whichever path named the request. The later batch
// counts its record outranked and never contested, and the run's row counts
// outranked once the sender's statement is kept beside it.
func TestARunsStatementOutranksAnUnverifiedOne(t *testing.T) {
	t.Run("one row id", func(t *testing.T) {
		for _, unverifiedFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("unverified first %t", unverifiedFirst), func(t *testing.T) {
				l := newLedger(t)
				own := runRow(rowID(61), "0.25", l.key, restatedRun, "1")
				claim := unverified(own)
				claim.Row.ListCost = own.Row.ListCost.Add(own.Row.ListCost)
				batches := [][]store.Record{{own}, {claim}}
				if unverifiedFirst {
					slices.Reverse(batches)
				}
				ingest(t, l.s, l.tenant, batches[0])
				want := store.Receipt{Duplicates: 1, Outranked: 1}
				if unverifiedFirst {
					want = store.Receipt{Inserted: 1, Outranked: 1}
				}
				if receipt := ingest(t, l.s, l.tenant, batches[1]); receipt != want {
					t.Fatalf("the later statement's batch: %+v, want %+v", receipt, want)
				}
				outranked := int64(0)
				if unverifiedFirst {
					outranked = 1
				}
				l.requireOnlyTheRunsRow(t, focus.Output, "0.25", "exact", met{outranked: outranked})
				l.requireCountedOnTheRunsKey(t)
			})
		}
	})
	t.Run("a request's cache writes at another lifetime", func(t *testing.T) {
		for name, tc := range map[string]struct {
			first, later store.Record
			run          focus.TokenClass
			// kept says the sender's write is stored, beside the run's.
			kept bool
		}{
			"the sender named the request, then the run's rows": {
				first: unverified(writeOf(focus.CacheWrite1h)), later: unnamed(writeOf(focus.CacheWrite5m)), run: focus.CacheWrite5m, kept: true,
			},
			"the sender's rows, then the run named the request": {
				first: unnamed(unverified(writeOf(focus.CacheWrite5m))), later: writeOf(focus.CacheWrite1h), run: focus.CacheWrite1h, kept: true,
			},
			"the run named the request, then the sender's rows": {
				first: writeOf(focus.CacheWrite1h), later: unnamed(unverified(writeOf(focus.CacheWrite5m))), run: focus.CacheWrite1h,
			},
			"the run's rows, then the sender named the request": {
				first: unnamed(writeOf(focus.CacheWrite5m)), later: unverified(writeOf(focus.CacheWrite1h)), run: focus.CacheWrite5m,
			},
		} {
			t.Run(name, func(t *testing.T) {
				l := newLedger(t)
				ingest(t, l.s, l.tenant, []store.Record{tc.first})
				if receipt := ingest(t, l.s, l.tenant, []store.Record{tc.later}); receipt.Outranked != 1 || receipt.Contested != 0 {
					t.Fatalf("the later write's batch: %+v, want it outranked, not contested", receipt)
				}
				want := met{}
				if tc.kept {
					want.outranked = 1
				}
				l.requireOnlyTheRunsRow(t, tc.run, "1", "windowed", want)
			})
		}
	})
}

// TestAnUnverifiedClaimNeverSkipsARunsCacheWrite has an ingest key's sender
// claim the run and state one lifetime of a request's cache writes, then the
// run state another. The run's write is kept: a claim of a run is not the run,
// so it never takes the run's split of the request's writes. The run's own
// split still holds after the claim: once the run named the request, its later
// write of the request at another lifetime, naming none, is left out.
func TestAnUnverifiedClaimNeverSkipsARunsCacheWrite(t *testing.T) {
	t.Run("the run's own split after a claim", func(t *testing.T) {
		l := newLedger(t)
		ingest(t, l.s, l.tenant, []store.Record{unverified(writeOf(focus.CacheWrite1h))})
		if receipt := ingest(t, l.s, l.tenant, []store.Record{writeOf(focus.CacheWrite1h)}); receipt.Inserted != 1 {
			t.Fatalf("the run's write naming the request: %+v, want it kept", receipt)
		}
		if receipt := ingest(t, l.s, l.tenant, []store.Record{unnamed(writeOf(focus.CacheWrite5m))}); receipt.Inserted != 0 {
			t.Fatalf("the run's later write at another lifetime: %+v, want it left out", receipt)
		}
	})
	for name, tc := range map[string]struct{ claim, own store.Record }{
		"the claim named the request, and the run's write names none": {
			claim: unverified(writeOf(focus.CacheWrite1h)), own: unnamed(writeOf(focus.CacheWrite5m)),
		},
		"the claim named none, and the run's write names the request": {
			claim: unnamed(unverified(writeOf(focus.CacheWrite5m))), own: writeOf(focus.CacheWrite1h),
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLedger(t)
			if tc.claim.Row.Tags[store.TagWorkflowRunID] != tc.own.Row.Tags[store.TagWorkflowRunID] {
				t.Fatal("the claim must name the run")
			}
			ingest(t, l.s, l.tenant, []store.Record{tc.claim})
			if receipt := ingest(t, l.s, l.tenant, []store.Record{tc.own}); receipt.Inserted != 1 {
				t.Fatalf("the run's write: %+v, want it kept", receipt)
			}
		})
	}
}

// TestTheLedgerViewReadsOnlyItsTenant reads ledger_rows as the service's role,
// on a ledger migrated by the owner role and on one a superuser migrated, who
// then owns the view. One row id stands in two tenants, verified in one and
// unverified in the other, and so does one request's cache writes, at another
// lifetime in each. Each tenant reads its own statement alone, never contested
// by the other's, and no tenant set reads nothing. Read past row level
// security, every unverified statement still stands in its tenant, and no row
// is contested or outranked: a statement meets only its own tenant's.
func TestTheLedgerViewReadsOnlyItsTenant(t *testing.T) {
	ctx := context.Background()
	for name, migrator := range map[string]func(storetest.Database) string{
		"migrated by the owner role": func(db storetest.Database) string { return db.OwnerURL },
		"migrated by a superuser":    func(db storetest.Database) string { return db.SuperURL },
	} {
		t.Run(name, func(t *testing.T) {
			db := storetest.Empty(t)
			if err := store.Migrate(ctx, migrator(db)); err != nil {
				t.Fatal(err)
			}
			tenants := map[string]store.TenantID{}
			for owner, identity := range map[string]string{"acme": "token_verified", "beta": "unverified"} {
				id, err := store.NewTenantID()
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Provision(ctx, migrator(db), store.Tenant{ID: id, Name: owner, Bindings: []store.Binding{{Host: "github.com", Owner: owner}}}); err != nil {
					t.Fatal(err)
				}
				tenants[identity] = id
			}
			s := open(t, db)
			verified := record(rowID(71), "1", "windowed", "token_verified")
			verified.Row.Tags[store.TagWorkflowRunID] = restatedRun
			ingest(t, s, tenants["token_verified"], []store.Record{verified, writeOf(focus.CacheWrite5m)})
			ingest(t, s, tenants["unverified"], []store.Record{
				unverified(verified), record(rowID(72), "2", "windowed", "token_verified"), unverified(writeOf(focus.CacheWrite1h)),
			})

			conn := connect(t, db.AppURL)
			for identity, tenant := range tenants {
				err := inTenant(t, conn, tenant, func(tx pgx.Tx) error {
					rows, err := tx.Query(ctx, `SELECT run_identity, contested FROM ledger_rows WHERE row_id = $1`, rowID(71))
					if err != nil {
						return err
					}
					read, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
						Identity  string
						Contested bool
					}])
					if err != nil {
						return err
					}
					if len(read) != 1 || read[0].Identity != identity || read[0].Contested {
						t.Errorf("tenant %s reads %+v, want its own %s statement alone, uncontested", identity, read, identity)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			var seen int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM ledger_rows`).Scan(&seen); err != nil {
				t.Fatal(err)
			}
			if seen != 0 {
				t.Errorf("with no tenant set the view reads %d rows", seen)
			}
			var standing, unverifiedWrites, contested, outranked int
			err := connect(t, db.SuperURL).QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE row_id = $1), count(*) FILTER (WHERE row_id = $2 AND run_identity = 'unverified'),
					count(*) FILTER (WHERE contested), count(*) FILTER (WHERE outranked)
				FROM ledger_rows`, rowID(71), writeOf(focus.CacheWrite1h).Row.RowID).Scan(&standing, &unverifiedWrites, &contested, &outranked)
			if err != nil {
				t.Fatal(err)
			}
			if standing != 2 || unverifiedWrites != 1 || contested != 0 || outranked != 0 {
				t.Errorf("read past row level security, %d statements of the row stand, %d unverified writes, %d contested and %d outranked rows, want 2, 1, 0 and 0",
					standing, unverifiedWrites, contested, outranked)
			}
		})
	}
}

// insertEarlierWrite writes a token-verified cache write row of a run's pull
// request, as the service wrote one before owners.
const insertEarlierWrite = `
INSERT INTO focus_rows (
	tenant_id, row_id, billing_currency, service_category, sku_meter, consumed_unit, consumed_quantity,
	list_cost, effective_cost, charge_period_start, charge_period_end, cost_basis, cost_source,
	attribution_grade, enforcement_grade, run_identity, repo, pr, workflow_run_id, document)
VALUES (costroid_tenant(), $1, 'USD', 'AI and Machine Learning', 'cache_write_1h', 'Tokens', 100, 1, 1,
	'2026-11-01T10:00:00Z', '2026-11-01T10:00:01Z', 'measured', 'otlp', 'windowed', 'none',
	'token_verified', $2, $3, $4, '{}')`

// TestARowStoredBeforeStatementsHadOwnersReadsAsItWas stores a row and the ids
// of its request's writes as a release before owners did, then migrates. The
// row is not changed, and it stands. The recorded ids name a run alone, so a
// later write of the request that names none is left out in that run, as
// before, and kept in another.
func TestARowStoredBeforeStatementsHadOwnersReadsAsItWas(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsBefore(t, ownersMigration)); err != nil {
		t.Fatal(err)
	}
	a := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	owner := connect(t, db.OwnerURL)
	err := inTenant(t, owner, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, insertEarlierWrite, writeOf(focus.CacheWrite1h).Row.RowID, repoA, prA, restatedRun); err != nil {
			return err
		}
		for _, lifetime := range []focus.TokenClass{focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m, focus.CacheWrite12h} {
			if _, err := tx.Exec(ctx, `INSERT INTO request_write_ids (tenant_id, row_id, workflow_run_id) VALUES (costroid_tenant(), $1, $2)`,
				focus.RowIdentity("req_written", lifetime), restatedRun); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	superuser := connect(t, db.SuperURL)
	snapshot := func() string {
		t.Helper()
		var text string
		if err := superuser.QueryRow(ctx, `SELECT string_agg(to_jsonb(f)::text, ' ' ORDER BY row_id) FROM focus_rows f`).Scan(&text); err != nil {
			t.Fatal(err)
		}
		var recorded string
		if err := superuser.QueryRow(ctx, `SELECT string_agg(row_id || ' ' || workflow_run_id, ' ' ORDER BY row_id) FROM request_write_ids`).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		return text + "\n" + recorded
	}
	before := snapshot()
	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("the migration changed stored rows:\n%s\nwas\n%s", after, before)
	}
	s := open(t, db)
	l := ledger{db: db, s: s, tenant: a}
	if stands := l.standing(t, writeOf(focus.CacheWrite1h).Row.RowID); len(stands) != 1 || stands[0].contested {
		t.Fatalf("the earlier row stands as %+v", stands)
	}
	if receipt := ingest(t, s, a, []store.Record{unnamed(writeOf(focus.CacheWrite5m))}); receipt.Inserted != 0 {
		t.Fatalf("a later write of the request in its run: %+v, want it left out", receipt)
	}
	elsewhere := unnamed(requestRow("req_written", focus.CacheWrite5m, "8"))
	if receipt := ingest(t, s, a, []store.Record{elsewhere}); receipt.Inserted != 1 {
		t.Fatalf("a write of the request in another run: %+v, want it kept", receipt)
	}
}

// TestTheDatabaseHoldsARecordedWriteToAWholeOwner has the service's role
// record a request's write id with part of an owner, and with a request key
// that is not a row id. Postgres refuses each: an id recorded with no owner is
// one recorded before owners, and nothing written since may read as one.
func TestTheDatabaseHoldsARecordedWriteToAWholeOwner(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, _ := tenants(t, db)
	conn := connect(t, db.AppURL)
	insert := `INSERT INTO request_write_ids (tenant_id, row_id, workflow_run_id, verified, repo, request_key)
		VALUES (costroid_tenant(), $1, '7', $2, $3, $4)`
	for name, arguments := range map[string][]any{
		"no request key":          {rowID(81), true, repoA, nil},
		"no repository":           {rowID(82), true, nil, rowID(1)},
		"no verification":         {rowID(83), nil, repoA, rowID(1)},
		"a request key too short": {rowID(84), true, repoA, "k"},
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, insert, arguments...)
			return err
		})
		if err == nil {
			t.Errorf("%s was taken", name)
			continue
		}
		requireSQLState(t, err, checkViolation)
	}
}

// verifiedRows reads, as the service's role, every row a run verified in the
// table or view named, as its id, repository, run and eligibility, in order.
func (l ledger) verifiedRows(t *testing.T, from string) []string {
	t.Helper()
	var read []string
	err := inTenant(t, connect(t, l.db.AppURL), l.tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT row_id || ' ' || coalesce(repo, '') || ' ' || coalesce(workflow_run_id, '') || ' ' || budget_eligible::text
			FROM `+from+` WHERE run_identity = 'token_verified' ORDER BY 1`)
		if err != nil {
			return err
		}
		read, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// TestEveryRowARunVerifiedStands holds the property that makes the gateway's
// figure read ledger_rows and focus_rows alike: every row a CI token verified
// stands, whatever else holds its charge. The figure reads budget-eligible
// rows only, and Postgres derives budget_eligible true only on a verified row
// (TestBudgetEligibilityIsDerivedByTheDatabase). The ledger here holds a
// verified row in every relation a statement can have: alone, restated by its
// run, beside another run's statement of its id, beside a push run's, beside
// an unverified statement of its id in both orders, and beside an unverified
// write of its request at another lifetime in both orders. The view holds
// every verified row the table holds, and no budget-eligible row it does not.
func TestEveryRowARunVerifiedStands(t *testing.T) {
	l := newLedger(t)
	own := func(n int) store.Record { return runsOwn(l, n) }
	otherRun := func(n int, run, pr string) store.Record {
		r := record(rowID(n), "9", "windowed", "token_verified")
		r.Row.Tags[store.TagWorkflowRunID] = run
		if pr == "" {
			delete(r.Row.Tags, store.TagPR)
		} else {
			r.Row.Tags[store.TagPR] = pr
		}
		return r
	}
	for _, batch := range [][]store.Record{
		{own(91)},
		{own(92)}, {runRow(rowID(92), "9", l.key, restatedRun, "2")},
		{own(93)}, {otherRun(93, "8", "13")},
		{otherRun(94, "8", "13")}, {own(94)},
		{own(95)}, {otherRun(95, "9", "")},
		{unverified(own(96))}, {own(96)},
		{own(97)}, {unverified(own(97))},
		{unverified(requestRow("req_both", focus.CacheWrite1h, restatedRun))}, {unnamed(requestRow("req_both", focus.CacheWrite5m, restatedRun))},
		{unnamed(unverified(requestRow("req_both_again", focus.CacheWrite5m, restatedRun)))}, {requestRow("req_both_again", focus.CacheWrite1h, restatedRun)},
	} {
		ingest(t, l.s, l.tenant, batch)
	}
	table, view := l.verifiedRows(t, "focus_rows"), l.verifiedRows(t, "ledger_rows")
	if len(table) != 12 || !slices.Equal(view, table) {
		t.Fatalf("the view holds the verified rows\n%v\nand the table\n%v", view, table)
	}
	var eligibleUnverified int
	err := inTenant(t, connect(t, l.db.AppURL), l.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM focus_rows WHERE budget_eligible AND run_identity <> 'token_verified'`).Scan(&eligibleUnverified)
	})
	if err != nil {
		t.Fatal(err)
	}
	if eligibleUnverified != 0 {
		t.Fatalf("%d budget-eligible rows a run did not verify", eligibleUnverified)
	}
}

// TestARowAnotherSenderCommitsInsideTheSecondAttemptIsWrittenOnce races a
// batch of two rows against a sender of the same run that commits one of them
// inside each of the batch's attempts, after the batch read the stored
// statements and before it inserts. The first attempt meets the first row at
// the unique index and starts again. The second reads the first row as stored,
// and meets the second at the index. It answers both as duplicates and fails
// nothing, and the ledger holds each row once.
func TestARowAnotherSenderCommitsInsideTheSecondAttemptIsWrittenOnce(t *testing.T) {
	l := newLedger(t)
	other := open(t, l.db)
	rows := []store.Record{runsOwn(l, 98), runsOwn(l, 99)}
	attempt := 0
	l.s.SetBeforeInsert(func() {
		if attempt < len(rows) {
			ingest(t, other, l.tenant, rows[attempt:attempt+1])
		}
		attempt++
	})
	receipt, err := l.s.Ingest(context.Background(), l.tenant, batch(rows...))
	if err != nil {
		t.Fatalf("the race refused the batch: %v", err)
	}
	if attempt != 2 || receipt != (store.Receipt{Duplicates: 2}) {
		t.Fatalf("%d attempts answered %+v, want two attempts and two duplicates", attempt, receipt)
	}
	if _, _, kept := l.totals(t); kept != 2 {
		t.Fatalf("the ledger holds %d rows, want each of the two once", kept)
	}
}

// runsOwn is the run's exact row n on its attested key.
func runsOwn(l ledger, n int) store.Record {
	return runRow(rowID(n), "0.25", l.key, restatedRun, "1")
}
