package store_test

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
	"github.com/Costroid/costroid/migrations"
)

const overrideRule = "run_budget"

// judgedAt is the time of the verdict every query here is asked at.
var judgedAt = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func headOf(pr, push int) string {
	return fmt.Sprintf("%040x", pr*1000+push)
}

func failure(pr, push int, limit string, at time.Time) store.FailingVerdict {
	return store.FailingVerdict{
		Repository: repoA, PullRequest: fmt.Sprint(pr), HeadSHA: headOf(pr, push),
		Rule: overrideRule, Limit: decimal.MustParse(limit), At: at,
	}
}

func approval(pr, push int, approver string, at time.Time) store.PullRequestApproval {
	return store.PullRequestApproval{
		Repository: repoA, PullRequest: fmt.Sprint(pr), HeadSHA: headOf(pr, push), Approver: approver, At: at,
	}
}

// overrideLedger is one tenant's override record, with a second tenant beside
// it.
type overrideLedger struct {
	t       *testing.T
	db      storetest.Database
	record  store.OverrideRecord
	tenant  store.TenantID
	another store.TenantID
}

func newOverrideLedger(t *testing.T) overrideLedger {
	t.Helper()
	db := storetest.New(t)
	a, b := tenants(t, db)
	return overrideLedger{t: t, db: db, record: open(t, db).OverrideRecord(), tenant: a, another: b}
}

func (l overrideLedger) fail(verdicts ...store.FailingVerdict) {
	l.t.Helper()
	for _, verdict := range verdicts {
		if err := l.record.AddFailure(context.Background(), l.tenant, verdict); err != nil {
			l.t.Fatal(err)
		}
	}
}

func (l overrideLedger) approve(approvals ...store.PullRequestApproval) {
	l.t.Helper()
	for _, approval := range approvals {
		if err := l.record.AddApproval(context.Background(), l.tenant, approval); err != nil {
			l.t.Fatal(err)
		}
	}
}

func (l overrideLedger) count(query store.OverrideQuery) store.OverrideCount {
	l.t.Helper()
	count, err := l.record.Overrides(context.Background(), l.tenant, query)
	if err != nil {
		l.t.Fatal(err)
	}
	return count
}

// judging asks for run_budget's record under limit 5 for pull request 999 at
// judgedAt, never re-armed.
func judging() store.OverrideQuery {
	return store.OverrideQuery{
		Repository: repoA, PullRequest: "999", Rule: overrideRule, Limit: decimal.MustParse("5"), At: judgedAt,
	}
}

func requireCount(t *testing.T, got store.OverrideCount, failing, approved, approvers int) {
	t.Helper()
	if want := (store.OverrideCount{Failing: failing, Approved: approved, Approvers: approvers}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTheOverrideRecordIsAppendOnly(t *testing.T) {
	kind := reflect.TypeFor[store.OverrideRecord]()
	var methods []string
	for i := range kind.NumMethod() {
		methods = append(methods, kind.Method(i).Name)
	}
	if want := []string{"AddApproval", "AddFailure", "Overrides"}; !slices.Equal(methods, want) {
		t.Fatalf("the override record's methods are %v, want %v", methods, want)
	}

	l := newOverrideLedger(t)
	at := judgedAt.Add(-time.Hour)
	l.fail(failure(1, 1, "5", at))
	l.approve(approval(1, 1, "octo-lead", at))
	conn := connect(t, l.db.AppURL)
	for _, statement := range []string{
		`UPDATE failing_verdicts SET rule_limit = 9`,
		`DELETE FROM failing_verdicts`,
		`TRUNCATE failing_verdicts`,
		`UPDATE pull_request_approvals SET approver = 'someone-else'`,
		`DELETE FROM pull_request_approvals`,
		`TRUNCATE pull_request_approvals`,
	} {
		err := inTenant(t, conn, l.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), statement)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
	requireCount(t, l.count(judging()), 1, 1, 1)
}

func TestOverridesCountAPullRequestOnce(t *testing.T) {
	l := newOverrideLedger(t)
	at := judgedAt.Add(-48 * time.Hour)
	l.fail(failure(1, 1, "5", at), failure(1, 1, "5", at.Add(time.Minute)), failure(1, 2, "5", at.Add(time.Hour)), failure(1, 3, "5", at.Add(2*time.Hour)))
	l.approve(approval(1, 1, "octo-lead", at.Add(time.Minute)), approval(1, 3, "octo-lead", at.Add(3*time.Hour)))
	requireCount(t, l.count(judging()), 1, 1, 1)
}

func TestOverridesNeverCountThePullRequestJudged(t *testing.T) {
	l := newOverrideLedger(t)
	at := judgedAt.Add(-time.Hour)
	l.fail(failure(999, 1, "5", at), failure(2, 1, "5", at))
	l.approve(approval(999, 1, "octo-lead", at), approval(999, 1, "octo-ops", at))
	requireCount(t, l.count(judging()), 1, 0, 0)
}

func TestOverridesCountOnlyAnApprovalOfAHeadOnWhichTheRuleFailed(t *testing.T) {
	l := newOverrideLedger(t)
	at := judgedAt.Add(-time.Hour)
	l.fail(failure(1, 1, "5", at))
	l.approve(approval(1, 2, "octo-lead", at))
	requireCount(t, l.count(judging()), 1, 0, 0)
	l.approve(approval(2, 1, "octo-lead", at))
	requireCount(t, l.count(judging()), 1, 0, 0)
}

func TestOverridesCountApproversByAccount(t *testing.T) {
	l := newOverrideLedger(t)
	at := judgedAt.Add(-time.Hour)
	l.fail(failure(1, 1, "5", at), failure(2, 1, "5", at), failure(3, 1, "5", at))
	l.approve(approval(1, 1, "octo-lead", at), approval(2, 1, "Octo-Lead", at), approval(3, 1, "OCTO-LEAD", at))
	requireCount(t, l.count(judging()), 3, 3, 1)
	l.approve(approval(3, 1, "octo-ops", at))
	requireCount(t, l.count(judging()), 3, 3, 2)
}

func TestOverridesReadThePresentLimitOnly(t *testing.T) {
	l := newOverrideLedger(t)
	at := judgedAt.Add(-time.Hour)
	l.fail(failure(1, 1, "4", at), failure(2, 1, "5.00", at), failure(3, 1, "5", at))
	l.approve(approval(1, 1, "octo-lead", at), approval(2, 1, "octo-ops", at))
	requireCount(t, l.count(judging()), 2, 1, 1)
	under4 := judging()
	under4.Limit = decimal.MustParse("4")
	requireCount(t, l.count(under4), 1, 1, 1)
}

func TestOverridesReadTheLatestTwentyPullRequestsWithinNinetyDays(t *testing.T) {
	l := newOverrideLedger(t)
	for pr := 1; pr <= 25; pr++ {
		at := judgedAt.Add(-time.Duration(26-pr) * time.Hour)
		l.fail(failure(pr, 1, "5", at))
		if pr <= 5 {
			l.approve(approval(pr, 1, fmt.Sprintf("octo-%d", pr), at))
		}
	}
	requireCount(t, l.count(judging()), 20, 0, 0)
	l.approve(approval(6, 1, "octo-lead", judgedAt.Add(-time.Hour)))
	requireCount(t, l.count(judging()), 20, 1, 1)

	old := newOverrideLedger(t)
	old.fail(failure(1, 1, "5", judgedAt.Add(-91*24*time.Hour)), failure(2, 1, "5", judgedAt.Add(-89*24*time.Hour)))
	old.approve(approval(1, 1, "octo-lead", judgedAt.Add(-91*24*time.Hour)), approval(2, 1, "octo-lead", judgedAt.Add(-89*24*time.Hour)))
	requireCount(t, old.count(judging()), 1, 1, 1)
}

func TestOverridesStartEmptyAtARearm(t *testing.T) {
	l := newOverrideLedger(t)
	rearmed := judgedAt.Add(-24 * time.Hour)
	l.fail(failure(1, 1, "5", rearmed.Add(-time.Hour)), failure(2, 1, "5", rearmed.Add(-time.Hour)))
	l.approve(approval(1, 1, "octo-lead", rearmed.Add(-time.Minute)))
	query := judging()
	requireCount(t, l.count(query), 2, 1, 1)
	query.Rearmed = rearmed
	requireCount(t, l.count(query), 0, 0, 0)
	l.fail(failure(2, 1, "5", rearmed.Add(time.Hour)))
	l.approve(approval(1, 1, "octo-ops", rearmed.Add(time.Hour)))
	requireCount(t, l.count(query), 1, 0, 0)
	l.approve(approval(2, 1, "octo-ops", rearmed.Add(2*time.Hour)))
	requireCount(t, l.count(query), 1, 1, 1)
}

func TestOverridesStayInTheirTenantRepositoryAndRule(t *testing.T) {
	l := newOverrideLedger(t)
	ctx := context.Background()
	at := judgedAt.Add(-time.Hour)
	elsewhere := failure(1, 1, "5", at)
	elsewhere.Repository = "github.com/acme/gadgets"
	otherRule := failure(2, 1, "5", at)
	otherRule.Rule = "pr_budget"
	l.fail(elsewhere, otherRule)
	if err := l.record.AddFailure(ctx, l.another, failure(3, 1, "5", at)); err != nil {
		t.Fatal(err)
	}
	if err := l.record.AddApproval(ctx, l.another, approval(3, 1, "octo-lead", at)); err != nil {
		t.Fatal(err)
	}
	requireCount(t, l.count(judging()), 0, 0, 0)
	theirs, err := l.record.Overrides(ctx, l.another, judging())
	if err != nil {
		t.Fatal(err)
	}
	requireCount(t, theirs, 1, 1, 1)
}

func TestTheOverrideRecordRefusesAMalformedRow(t *testing.T) {
	l := newOverrideLedger(t)
	ctx := context.Background()
	at := judgedAt.Add(-time.Hour)
	shortHead := failure(1, 1, "5", at)
	shortHead.HeadSHA = "abc123"
	upperRepo := failure(1, 1, "5", at)
	upperRepo.Repository = "github.com/Acme/widgets"
	app := approval(1, 1, "costroid[bot]", at)
	for name, err := range map[string]error{
		"a short head":         l.record.AddFailure(ctx, l.tenant, shortHead),
		"an upper-case path":   l.record.AddFailure(ctx, l.tenant, upperRepo),
		"an App's approval":    l.record.AddApproval(ctx, l.tenant, app),
		"no pull request":      l.record.AddApproval(ctx, l.tenant, store.PullRequestApproval{Repository: repoA, HeadSHA: headOf(1, 1), Approver: "octo-lead", At: at}),
		"a limit below nought": l.record.AddFailure(ctx, l.tenant, store.FailingVerdict{Repository: repoA, PullRequest: "1", HeadSHA: headOf(1, 1), Rule: overrideRule, Limit: decimal.MustParse("0").Sub(decimal.MustParse("1")), At: at}),
	} {
		t.Run(name, func(t *testing.T) { requireSQLState(t, err, checkViolation) })
	}
}

// TestTheOverrideRecordMigratesOntoALedgerOfTheReleaseBefore migrates a
// ledger through every migration but the record's, writes a row, and then
// migrates it the rest of the way, as an upgrade does.
func TestTheOverrideRecordMigratesOntoALedgerOfTheReleaseBefore(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "0009" {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	if len(before) != 8 {
		t.Fatalf("the release before holds %d migrations, want 8", len(before))
	}
	if err := store.MigrateFrom(ctx, db.OwnerURL, before); err != nil {
		t.Fatal(err)
	}
	a, _ := tenants(t, db)
	s := open(t, db)
	if _, err := s.Ingest(ctx, a, batch(record(rowID(1), "1", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatal(err)
	}
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("the upgrade kept %d lines, want 1", len(lines))
	}
	overrides := s.OverrideRecord()
	if err := overrides.AddFailure(ctx, a, failure(1, 1, "5", judgedAt.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	count, err := overrides.Overrides(ctx, a, judging())
	if err != nil {
		t.Fatal(err)
	}
	requireCount(t, count, 1, 0, 0)
}
