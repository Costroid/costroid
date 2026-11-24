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

// overrideBranch is the base branch every pull request here merges into.
const overrideBranch = "main"

// judgedAt is the time of the verdict every query here is asked at.
var judgedAt = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func headOf(pr, push int) string {
	return fmt.Sprintf("%040x", pr*1000+push)
}

func failure(pr, push int, limit string, at time.Time) store.FailingVerdict {
	return store.FailingVerdict{
		Repository: repoA, BaseBranch: overrideBranch, PullRequest: fmt.Sprint(pr), HeadSHA: headOf(pr, push),
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
		Repository: repoA, BaseBranch: overrideBranch, PullRequest: "999", Rule: overrideRule, Limit: decimal.MustParse("5"), At: judgedAt,
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
	// Pull request 1 fails again on the head approved before the re-arm, and
	// pull request 2 is approved after it on a head that failed only before.
	l.fail(failure(1, 1, "5", rearmed.Add(time.Hour)))
	l.approve(approval(2, 1, "octo-ops", rearmed.Add(time.Hour)))
	requireCount(t, l.count(query), 1, 0, 0)
	l.approve(approval(1, 1, "octo-ops", rearmed.Add(2*time.Hour)))
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
		"a limit below nought": l.record.AddFailure(ctx, l.tenant, store.FailingVerdict{Repository: repoA, BaseBranch: overrideBranch, PullRequest: "1", HeadSHA: headOf(1, 1), Rule: overrideRule, Limit: decimal.MustParse("0").Sub(decimal.MustParse("1")), At: at}),
	} {
		t.Run(name, func(t *testing.T) { requireSQLState(t, err, checkViolation) })
	}
}

// TestTheOverrideRecordMigratesOntoALedgerOfTheReleaseBefore migrates a
// ledger through every migration but the record's, writes a row, and then
// migrates it the rest of the way, as an upgrade does, and keeps a demotion.
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
	started := judgedAt.Add(-time.Hour)
	requireDemotion(t, keep(t, s.DemotionRecord(), a, demotionOf(time.Time{}, started)), demotionOf(time.Time{}, started))
}

// demotionOf is run_budget's demotion in repoA under limit 5, re-armed at
// rearmed, and started at from on 3 of 10 by 2 accounts unless from is zero.
func demotionOf(rearmed, from time.Time) store.RuleDemotion {
	demotion := store.RuleDemotion{Repository: repoA, BaseBranch: overrideBranch, Rule: overrideRule, Limit: decimal.MustParse("5"), Rearmed: rearmed}
	if !from.IsZero() {
		demotion.From, demotion.Failing, demotion.Approved, demotion.Approvers = from, 10, 3, 2
	}
	return demotion
}

func keep(t *testing.T, record store.DemotionRecord, tenant store.TenantID, demotion store.RuleDemotion) store.RuleDemotion {
	t.Helper()
	kept, err := record.Keep(context.Background(), tenant, demotion)
	if err != nil {
		t.Fatal(err)
	}
	return kept
}

func requireDemotion(t *testing.T, got, want store.RuleDemotion) {
	t.Helper()
	if got.Limit.Cmp(want.Limit) != 0 || !got.Rearmed.Equal(want.Rearmed) || !got.From.Equal(want.From) || got.Off != want.Off ||
		got.Failing != want.Failing || got.Approved != want.Approved || got.Approvers != want.Approvers {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestTheDemotionRecordIsAppendOnlyAndKeepsATermOnce keeps a rule's demotion
// as serve's verdicts move it, at each base branch apart. The application role can only read and insert.
// A term is kept once: a second verdict that would start the same term keeps
// nothing and reads the first one's start. The latest re-arm stands, with its
// term beside it, and a rule no verdict moved reads as none. The record of a
// term must demote: Postgres refuses one of 2 in 10, of one account, of 9, or
// beside demotion turned off.
func TestTheDemotionRecordIsAppendOnlyAndKeepsATermOnce(t *testing.T) {
	kind := reflect.TypeFor[store.DemotionRecord]()
	var methods []string
	for i := range kind.NumMethod() {
		methods = append(methods, kind.Method(i).Name)
	}
	if want := []string{"Demotion", "Keep"}; !slices.Equal(methods, want) {
		t.Fatalf("the demotion record's methods are %v, want %v", methods, want)
	}
	l := newOverrideLedger(t)
	ctx := context.Background()
	record := open(t, l.db).DemotionRecord()
	if _, found, err := record.Demotion(ctx, l.tenant, repoA, overrideBranch, overrideRule); found || err != nil {
		t.Fatalf("a rule no verdict moved: found %t, %v", found, err)
	}
	started := judgedAt.Add(-48 * time.Hour)
	first := keep(t, record, l.tenant, demotionOf(time.Time{}, started))
	requireDemotion(t, first, demotionOf(time.Time{}, started))
	requireDemotion(t, keep(t, record, l.tenant, demotionOf(time.Time{}, started.Add(time.Hour))), first)
	rearmed := started.Add(30 * 24 * time.Hour)
	requireDemotion(t, keep(t, record, l.tenant, demotionOf(rearmed, time.Time{})), demotionOf(rearmed, time.Time{}))
	again := keep(t, record, l.tenant, demotionOf(rearmed, rearmed.Add(time.Hour)))
	requireDemotion(t, again, demotionOf(rearmed, rearmed.Add(time.Hour)))
	requireDemotion(t, keep(t, record, l.tenant, demotionOf(rearmed, time.Time{})), again)
	if _, found, err := record.Demotion(ctx, l.another, repoA, overrideBranch, overrideRule); found || err != nil {
		t.Fatalf("another tenant read the demotion: found %t, %v", found, err)
	}
	// The same term at another base branch is that branch's own.
	release := demotionOf(time.Time{}, started.Add(2*time.Hour))
	release.BaseBranch = "release"
	requireDemotion(t, keep(t, record, l.tenant, release), release)
	if kept, _, err := record.Demotion(ctx, l.tenant, repoA, overrideBranch, overrideRule); err != nil || !kept.Rearmed.Equal(again.Rearmed) || !kept.From.Equal(again.From) {
		t.Fatalf("a term at release moved main's demotion: %+v, %v", kept, err)
	}

	// Each is a term of a re-arm no row holds, so only the checks can refuse
	// it.
	unkept, later := rearmed.Add(10*24*time.Hour), rearmed.Add(11*24*time.Hour)
	for name, demotion := range map[string]store.RuleDemotion{
		"2 of 10":           func() store.RuleDemotion { d := demotionOf(unkept, later); d.Approved = 2; return d }(),
		"one account":       func() store.RuleDemotion { d := demotionOf(unkept, later); d.Approvers = 1; return d }(),
		"9 failing":         func() store.RuleDemotion { d := demotionOf(unkept, later); d.Failing, d.Approved = 9, 9; return d }(),
		"21 failing":        func() store.RuleDemotion { d := demotionOf(unkept, later); d.Failing, d.Approved = 21, 21; return d }(),
		"beside off":        func() store.RuleDemotion { d := demotionOf(unkept, later); d.Off = true; return d }(),
		"before its arming": demotionOf(unkept, rearmed),
	} {
		_, err := record.Keep(ctx, l.tenant, demotion)
		t.Run(name, func(t *testing.T) { requireSQLState(t, err, checkViolation) })
	}

	conn := connect(t, l.db.AppURL)
	for _, statement := range []string{
		`UPDATE rule_demotions SET off = true`,
		`DELETE FROM rule_demotions`,
		`TRUNCATE rule_demotions`,
	} {
		err := inTenant(t, conn, l.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), statement)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
}
