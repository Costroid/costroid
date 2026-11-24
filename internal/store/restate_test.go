package store_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// restatedRun is the run whose rows every restatement below sends again.
const restatedRun = "7"

// The provider requests of the run's two cache writes. The first reached the
// ledger as the telemetry path writes a row, naming its request. The second
// reached it as the result stream writes one, naming none.
const (
	namedRequest   = "req_named"
	unnamedRequest = "req_unnamed"
)

// ledger is a store with one tenant, and the key the run's exact rows name,
// attested for the run, with the gateway's count on it.
type ledger struct {
	db     storetest.Database
	s      *store.Store
	tenant store.TenantID
	key    string
}

func newLedger(t *testing.T) ledger {
	t.Helper()
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	key := rowID(700)
	ctx := context.Background()
	if err := s.Attest(ctx, a, minted(key, restatedRun)); err != nil {
		t.Fatal(err)
	}
	if err := s.AttestSpend(ctx, a, spent(minted(key, restatedRun), "0.5")); err != nil {
		t.Fatal(err)
	}
	return ledger{db: db, s: s, tenant: a, key: key}
}

// runRecords is the run's rows: two exact rows on the attested key, a
// windowed row, and two cache writes, one from each path.
func (l ledger) runRecords() []store.Record {
	exact := runRow(rowID(11), "0.25", l.key, restatedRun, "1")
	exact.Row.Tags[focus.TagTeam] = "platform"
	exact.Row.BillingAccountID = "acct-1"
	second := runRow(rowID(12), "0.5", l.key, restatedRun, "1")
	second.Row.SkuMeter = focus.InputUncached
	second.Row.Tags[focus.TagTeam] = "platform"
	windowed := record(rowID(13), "1", "windowed", "token_verified")
	windowed.Row.Tags[store.TagWorkflowRunID] = restatedRun
	windowed.RunAttempt = "1"
	named := priced(requestRow(namedRequest, focus.CacheWrite1h, restatedRun), "0.125")
	unnamed := priced(requestRow(unnamedRequest, focus.CacheWrite5m, restatedRun), "0.0625")
	unnamed.RequestID = ""
	return []store.Record{exact, second, windowed, named, unnamed}
}

// priced sets a record's costs to amount.
func priced(r store.Record, amount string) store.Record {
	cost := decimal.MustParse(amount)
	r.Row.ListCost, r.Row.EffectiveCost, r.Row.ContractedCost = cost, cost, cost
	return r
}

func cloneRecord(r store.Record) store.Record {
	r.Row.Tags = maps.Clone(r.Row.Tags)
	return r
}

func cloneRecords(records []store.Record) []store.Record {
	copied := make([]store.Record, len(records))
	for i := range records {
		copied[i] = cloneRecord(records[i])
	}
	return copied
}

func ingestBatches(t *testing.T, l ledger, batches [][]store.Record) {
	t.Helper()
	for _, records := range batches {
		ingest(t, l.s, l.tenant, records)
	}
}

// state is what a reader of the ledger sees after a write: the pull request's
// lines, the month's total, the gateway's count that stands behind a budget
// and the number of rows, every amount as its exact decimal string. The store
// answers no month's total, so the month is summed from the rows directly.
func (l ledger) state(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	lines, err := l.s.PullRequest(ctx, l.tenant, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		fmt.Fprintf(&b, "line %s %s %s %s %s %s %s %s %s eligible=%t: %d rows, quantity %s, list %s, effective %s, %s to %s\n",
			line.Currency, line.BillingAccountID, line.Model, line.SkuMeter, line.CostBasis, line.CostSource,
			line.AttributionGrade, line.RunIdentity, line.Agent, line.BudgetEligible, line.Rows,
			line.ConsumedQuantity, line.ListCost, line.EffectiveCost, line.FirstCharge.UTC(), line.LastCharge.UTC())
	}
	conn := connect(t, l.db.SuperURL)
	var count int
	var month string
	err = conn.QueryRow(ctx, `
		SELECT coalesce(string_agg(currency || ' ' || rows || ' ' || list || ' ' || effective, '; ' ORDER BY currency), '')
		FROM (
			SELECT billing_currency AS currency, count(*)::text AS rows,
				sum(list_cost)::text AS list, sum(effective_cost)::text AS effective
			FROM focus_rows
			WHERE tenant_id = $1 AND charge_period_start >= '2026-11-01T00:00:00Z' AND charge_period_start < '2026-12-01T00:00:00Z'
			GROUP BY billing_currency
		) m`, l.tenant.String()).Scan(&month)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM focus_rows WHERE tenant_id = $1`, l.tenant.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	rows, usd := gatewaySpend(t, l.s, l.tenant)
	fmt.Fprintf(&b, "month %s\ngateway %d rows, USD %s\nrows %d\n", month, rows, usd, count)
	return b.String()
}

// restatement sends a run's rows again, changed in one way, as the batches it
// returns.
type restatement func(t *testing.T, l ledger, records []store.Record) [][]store.Record

// each changes every record the same way and sends them in one batch.
func each(change func(*store.Record)) restatement {
	return func(_ *testing.T, _ ledger, records []store.Record) [][]store.Record {
		for i := range records {
			change(&records[i])
		}
		return [][]store.Record{records}
	}
}

// withZeros writes an amount with three more fractional zeros.
func withZeros(t *testing.T, amount decimal.Decimal) decimal.Decimal {
	t.Helper()
	text := amount.String()
	if !strings.Contains(text, ".") {
		text += "."
	}
	written, err := decimal.Parse(text + "000")
	if err != nil {
		t.Fatal(err)
	}
	return written
}

// inExponentForm writes an amount as thousandths, in exponent form.
func inExponentForm(t *testing.T, amount decimal.Decimal) decimal.Decimal {
	t.Helper()
	written, err := decimal.ParseNumber(amount.Mul(decimal.FromInt(1000)).String() + "e-3")
	if err != nil {
		t.Fatal(err)
	}
	return written
}

// restatements are the ways one run's rows arrive again while every charge
// stays the charge it was.
var restatements = map[string]restatement{
	"byte for byte": each(func(*store.Record) {}),
	"a tag's value": each(func(r *store.Record) { r.Row.Tags[focus.TagTeam] = "payments" }),
	"a tag added":   each(func(r *store.Record) { r.Row.Tags[focus.TagWorkItem] = "ENG-12" }),
	"a tag removed": each(func(r *store.Record) { delete(r.Row.Tags, focus.TagTeam) }),
	"the account":   each(func(r *store.Record) { r.Row.BillingAccountID = "acct-2" }),
	"the account's name": each(func(r *store.Record) {
		name := "Another name"
		r.Row.BillingAccountName = &name
	}),
	"the sub-account": each(func(r *store.Record) {
		other := rowID(701)
		r.Row.SubAccountID = &other
	}),
	"the sub-account's name": each(func(r *store.Record) {
		name := "Another key"
		r.Row.SubAccountName = &name
	}),
	"the order of the rows": func(_ *testing.T, _ ledger, records []store.Record) [][]store.Record {
		slices.Reverse(records)
		return [][]store.Record{records}
	},
	"an amount with trailing zeros": func(t *testing.T, _ ledger, records []store.Record) [][]store.Record {
		for i := range records {
			records[i].Row.ListCost = withZeros(t, records[i].Row.ListCost)
			records[i].Row.ConsumedQuantity = withZeros(t, records[i].Row.ConsumedQuantity)
		}
		return [][]store.Record{records}
	},
	"an amount in exponent form": func(t *testing.T, _ ledger, records []store.Record) [][]store.Record {
		for i := range records {
			records[i].Row.ListCost = inExponentForm(t, records[i].Row.ListCost)
		}
		return [][]store.Record{records}
	},
	"one batch per row": func(_ *testing.T, _ ledger, records []store.Record) [][]store.Record {
		batches := make([][]store.Record, len(records))
		for i := range records {
			batches[i] = records[i : i+1]
		}
		return batches
	},
	"the rows twice in one batch": func(_ *testing.T, _ ledger, records []store.Record) [][]store.Record {
		return [][]store.Record{append(cloneRecords(records), records...)}
	},
	"the run attempt": each(func(r *store.Record) { r.RunAttempt = "2" }),
	"a lower stated grade": each(func(r *store.Record) {
		r.Row.Tags[focus.TagAttributionGrade] = "unattributed"
		r.Row.Tags[focus.TagRunIdentity] = "unverified"
		r.CredentialBasis = store.CredentialStated
	}),
	"a higher stated grade": func(_ *testing.T, l ledger, records []store.Record) [][]store.Record {
		for i := range records {
			records[i].Row.Tags[focus.TagAttributionGrade] = "exact"
			records[i].Row.SubAccountID = &l.key
			records[i].CredentialBasis = store.CredentialMinted
		}
		return [][]store.Record{records}
	},
	"a cache write at another lifetime, naming no request": func(*testing.T, ledger, []store.Record) [][]store.Record {
		restated := requestRow(namedRequest, focus.CacheWrite5m, restatedRun)
		restated.RequestID = ""
		return [][]store.Record{{restated}}
	},
	"a cache write at another lifetime, naming its request": func(*testing.T, ledger, []store.Record) [][]store.Record {
		return [][]store.Record{{requestRow(unnamedRequest, focus.CacheWrite1h, restatedRun)}}
	},
}

// TestARestatedRowNeverCountsTwice sends a run's rows, then sends them again
// once for each thing a restatement can change while the charge stays the
// same. After each, every figure a reader sees is what it was after the first.
func TestARestatedRowNeverCountsTwice(t *testing.T) {
	for name, restate := range restatements {
		t.Run(name, func(t *testing.T) {
			l := newLedger(t)
			records := l.runRecords()
			ingest(t, l.s, l.tenant, cloneRecords(records))
			first := l.state(t)
			ingestBatches(t, l, restate(t, l, cloneRecords(records)))
			if got := l.state(t); got != first {
				t.Fatalf("after the restatement:\n%s\nafter the first statement:\n%s", got, first)
			}
		})
	}
}

// TestADifferentChargeStillCounts holds the converse: a row that is another
// charge adds its own amount and one row to every figure, so the test above
// cannot pass on a ledger that keeps nothing it is sent twice.
func TestADifferentChargeStillCounts(t *testing.T) {
	cases := map[string]func(l ledger) store.Record{
		"a row of another run": func(l ledger) store.Record {
			r := record(rowID(21), "0.75", "windowed", "token_verified")
			r.Row.Tags[store.TagWorkflowRunID] = "8"
			r.RunAttempt = "1"
			return r
		},
		"another line of the same run": func(l ledger) store.Record {
			r := runRow(rowID(22), "0.75", l.key, restatedRun, "1")
			r.Row.SkuMeter = focus.CacheRead
			return r
		},
		"another amount": func(l ledger) store.Record {
			return runRow(rowID(23), "0.75", l.key, restatedRun, "1")
		},
		"another request's cache write, naming no request": func(l ledger) store.Record {
			r := priced(requestRow("req_other", focus.CacheWrite5m, restatedRun), "0.75")
			r.RequestID = ""
			return r
		},
		"the named request's cache write in another run": func(l ledger) store.Record {
			r := priced(requestRow(namedRequest, focus.CacheWrite5m, "8"), "0.75")
			r.RequestID = ""
			return r
		},
	}
	for name, another := range cases {
		t.Run(name, func(t *testing.T) {
			l := newLedger(t)
			ingest(t, l.s, l.tenant, l.runRecords())
			listBefore, monthBefore, rowsBefore := l.totals(t)
			added := another(l)
			if receipt := ingest(t, l.s, l.tenant, []store.Record{added}); receipt.Inserted != 1 {
				t.Fatalf("another charge was left out: %+v", receipt)
			}
			list, month, rows := l.totals(t)
			want := listBefore.Add(added.Row.ListCost).String()
			if list.String() != want || month.String() != monthBefore.Add(added.Row.ListCost).String() || rows != rowsBefore+1 {
				t.Fatalf("pull request %s, month %s, %d rows; want %s, %s and %d rows",
					list, month, rows, want, monthBefore.Add(added.Row.ListCost), rowsBefore+1)
			}
		})
	}
}

// totals reads the pull request's list cost, the month's and the row count.
func (l ledger) totals(t *testing.T) (pullRequest, month decimal.Decimal, rows int) {
	t.Helper()
	ctx := context.Background()
	lines, err := l.s.PullRequest(ctx, l.tenant, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		pullRequest = pullRequest.Add(line.ListCost)
	}
	var text string
	err = connect(t, l.db.SuperURL).QueryRow(ctx, `
		SELECT coalesce(sum(list_cost), 0)::text, count(*) FROM focus_rows
		WHERE tenant_id = $1 AND charge_period_start >= '2026-11-01T00:00:00Z' AND charge_period_start < '2026-12-01T00:00:00Z'`,
		l.tenant.String()).Scan(&text, &rows)
	if err != nil {
		t.Fatal(err)
	}
	return pullRequest, decimal.MustParse(text), rows
}

// stored is what the ledger keeps of one row.
type stored struct {
	list, grade, identity, pr, credential, subAccount string
	eligible                                          bool
}

func (l ledger) stored(t *testing.T, id string) stored {
	t.Helper()
	var s stored
	err := connect(t, l.db.SuperURL).QueryRow(context.Background(), `
		SELECT list_cost::text, attribution_grade, run_identity, coalesce(pr, ''), coalesce(credential_basis, ''),
			coalesce(document->>'SubAccountId', ''), budget_eligible
		FROM focus_rows WHERE tenant_id = $1 AND row_id = $2`, l.tenant.String(), id).
		Scan(&s.list, &s.grade, &s.identity, &s.pr, &s.credential, &s.subAccount, &s.eligible)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// standing is one statement of a row id that stands, as ledger_rows reads it.
type standing struct {
	stored
	contested, outranked bool
}

// standing reads, as the service's role, every statement of id that stands,
// in the order of their pull requests.
func (l ledger) standing(t *testing.T, id string) []standing {
	t.Helper()
	var read []standing
	err := inTenant(t, connect(t, l.db.AppURL), l.tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT list_cost::text, attribution_grade, run_identity, coalesce(pr, ''), coalesce(credential_basis, ''),
				coalesce(document->>'SubAccountId', ''), budget_eligible, contested, outranked
			FROM ledger_rows WHERE row_id = $1 ORDER BY pr`, id)
		if err != nil {
			return err
		}
		var s standing
		_, err = pgx.ForEachRow(rows, []any{&s.list, &s.grade, &s.identity, &s.pr, &s.credential, &s.subAccount, &s.eligible, &s.contested, &s.outranked}, func() error {
			read = append(read, s)
			return nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// TestWhichStatementOfARowStands holds the ledger to its rule. Within one run,
// the first statement of a row id stands whole: a later one changes nothing,
// whether it states another amount, a higher grade or another pull request,
// and inside one batch the earlier line is the first statement. Every
// statement the ledger did not verify has one owner, so the first of them
// stands. A statement the ledger did not verify never stands beside a run's:
// before it, it gives way to the run's, and after it, it is a duplicate. Either
// way the receipt counts it outranked, and so does the run's row once both are
// kept. A statement of another run never displaces a run's own: both stand,
// each on its own pull request, and each is contested.
func TestWhichStatementOfARowStands(t *testing.T) {
	exact := func(l ledger) stored {
		return stored{"0.250000000000", "exact", "token_verified", prA, store.CredentialMinted, l.key, true}
	}
	for name, tc := range map[string]struct {
		first, later func(l ledger) store.Record
		together     bool
		// receipt is what the later statement's batch answers, and kept the
		// statements the ledger holds of the id.
		receipt store.Receipt
		kept    int
		want    func(l ledger) []standing
	}{
		"a later amount": {
			first:   func(l ledger) store.Record { return runRow(rowID(31), "0.25", l.key, restatedRun, "1") },
			later:   func(l ledger) store.Record { return runRow(rowID(31), "9", l.key, restatedRun, "1") },
			receipt: store.Receipt{Duplicates: 1}, kept: 1,
			want: func(l ledger) []standing { return []standing{{stored: exact(l)}} },
		},
		"a later, higher grade": {
			first: func(ledger) store.Record {
				r := record(rowID(32), "0.25", "windowed", "token_verified")
				r.Row.Tags[store.TagWorkflowRunID] = restatedRun
				return r
			},
			later:   func(l ledger) store.Record { return runRow(rowID(32), "0.25", l.key, restatedRun, "1") },
			receipt: store.Receipt{Duplicates: 1}, kept: 1,
			want: func(ledger) []standing {
				return []standing{{stored: stored{"0.250000000000", "windowed", "token_verified", prA, "", "", true}}}
			},
		},
		"a later, lower grade": {
			first:   func(l ledger) store.Record { return runRow(rowID(33), "0.25", l.key, restatedRun, "1") },
			later:   func(ledger) store.Record { return record(rowID(33), "9", "unattributed", "unverified") },
			receipt: store.Receipt{Duplicates: 1, Outranked: 1}, kept: 1,
			want: func(l ledger) []standing { return []standing{{stored: exact(l)}} },
		},
		"a later pull request": {
			first: func(l ledger) store.Record { return runRow(rowID(34), "0.25", l.key, restatedRun, "1") },
			later: func(l ledger) store.Record {
				r := runRow(rowID(34), "0.25", l.key, restatedRun, "1")
				r.Row.Tags[store.TagPR] = "13"
				return r
			},
			receipt: store.Receipt{Duplicates: 1}, kept: 1,
			want: func(l ledger) []standing { return []standing{{stored: exact(l)}} },
		},
		"a later unverified statement over an unverified one": {
			first: func(ledger) store.Record { return record(rowID(38), "0.25", "unattributed", "unverified") },
			later: func(ledger) store.Record {
				r := record(rowID(38), "9", "unattributed", "unverified")
				r.Row.Tags[store.TagWorkflowRunID] = restatedRun
				return r
			},
			receipt: store.Receipt{Duplicates: 1}, kept: 1,
			want: func(ledger) []standing {
				return []standing{{stored: stored{"0.250000000000", "unattributed", "unverified", prA, "", "", false}}}
			},
		},
		"an earlier, unverified statement": {
			first:   func(ledger) store.Record { return record(rowID(35), "9", "unattributed", "unverified") },
			later:   func(l ledger) store.Record { return runRow(rowID(35), "0.25", l.key, restatedRun, "1") },
			receipt: store.Receipt{Inserted: 1, Outranked: 1}, kept: 2,
			want: func(l ledger) []standing { return []standing{{stored: exact(l), outranked: true}} },
		},
		"an earlier run of another pull request": {
			first: func(ledger) store.Record {
				r := record(rowID(37), "9", "windowed", "token_verified")
				r.Row.Tags[store.TagWorkflowRunID], r.Row.Tags[store.TagPR] = "8", "13"
				return r
			},
			later:   func(l ledger) store.Record { return runRow(rowID(37), "0.25", l.key, restatedRun, "1") },
			receipt: store.Receipt{Inserted: 1, Contested: 1}, kept: 2,
			want: func(l ledger) []standing {
				return []standing{
					{stored: exact(l), contested: true},
					{stored: stored{"9.000000000000", "windowed", "token_verified", "13", "", "", true}, contested: true},
				}
			},
		},
		"two statements in one batch": {
			first: func(l ledger) store.Record { return runRow(rowID(36), "0.25", l.key, restatedRun, "1") },
			later: func(l ledger) store.Record {
				r := runRow(rowID(36), "9", l.key, restatedRun, "1")
				r.Row.Tags[store.TagPR] = "13"
				return r
			},
			together: true,
			receipt:  store.Receipt{Inserted: 1, Duplicates: 1}, kept: 1,
			want: func(l ledger) []standing { return []standing{{stored: exact(l)}} },
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLedger(t)
			first, later := tc.first(l), tc.later(l)
			var receipt store.Receipt
			if tc.together {
				receipt = ingest(t, l.s, l.tenant, []store.Record{first, later})
			} else {
				ingest(t, l.s, l.tenant, []store.Record{first})
				receipt = ingest(t, l.s, l.tenant, []store.Record{later})
			}
			if receipt != tc.receipt {
				t.Fatalf("the later statement's batch: %+v, want %+v", receipt, tc.receipt)
			}
			if _, _, rows := l.totals(t); rows != tc.kept {
				t.Fatalf("the ledger holds %d statements of the row, want %d", rows, tc.kept)
			}
			if got, want := l.standing(t, first.Row.RowID), tc.want(l); !slices.Equal(got, want) {
				t.Fatalf("stands %+v, want %+v", got, want)
			}
		})
	}
}

// TestTheEarlierOfTwoStatementsInOneBatchStands holds the batch's own order to
// decide which statement of a row id stands: the earlier line, whole, sent in
// either order, with one row kept and every later line a duplicate.
func TestTheEarlierOfTwoStatementsInOneBatchStands(t *testing.T) {
	for name, reverse := range map[string]bool{"in the order sent": false, "in the reverse order": true} {
		t.Run(name, func(t *testing.T) {
			l := newLedger(t)
			statements := []store.Record{
				runRow(rowID(41), "0.25", l.key, restatedRun, "1"),
				runRow(rowID(42), "0.5", l.key, restatedRun, "1"),
				runRow(rowID(41), "9", l.key, restatedRun, "1"),
				runRow(rowID(41), "5", l.key, restatedRun, "1"),
			}
			statements[2].Row.Tags[store.TagPR] = "13"
			if reverse {
				slices.Reverse(statements)
			}
			first := statements[slices.IndexFunc(statements, func(r store.Record) bool { return r.Row.RowID == rowID(41) })]
			if receipt := ingest(t, l.s, l.tenant, statements); receipt.Inserted != 2 || receipt.Duplicates != 2 {
				t.Fatalf("%+v, want two rows and two duplicates", receipt)
			}
			if _, _, rows := l.totals(t); rows != 2 {
				t.Fatalf("%d rows hold two charges", rows)
			}
			got := l.stored(t, rowID(41))
			if decimal.MustParse(got.list).String() != first.Row.ListCost.String() || got.pr != first.Row.Tags[store.TagPR] {
				t.Fatalf("stored list %s on pull request %s, want the earlier line's %s on %s",
					got.list, got.pr, first.Row.ListCost, first.Row.Tags[store.TagPR])
			}
		})
	}
}

// TestTheEarlierRecordOfARequestInOneBatchNamesItsRun holds the ids a batch
// records for a request to the run of the earlier record that named it, so a
// later write of the request that names none is matched in that run.
func TestTheEarlierRecordOfARequestInOneBatchNamesItsRun(t *testing.T) {
	l := newLedger(t)
	ingest(t, l.s, l.tenant, []store.Record{
		requestRow("req_twice", focus.CacheWrite5m, restatedRun),
		requestRow("req_twice", focus.CacheWrite1h, "8"),
	})
	later := requestRow("req_twice", focus.CacheWrite30m, restatedRun)
	later.RequestID = ""
	if receipt := ingest(t, l.s, l.tenant, []store.Record{later}); receipt.Inserted != 0 || receipt.Duplicates != 1 {
		t.Fatalf("%+v, want the write left out in the earlier record's run", receipt)
	}
}
