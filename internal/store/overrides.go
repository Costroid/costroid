package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
)

// The window a rule's override rate is read over: the latest pull requests on
// which the rule failed, no older than a span of days.
const (
	OverrideWindowPullRequests = 20
	OverrideWindowDays         = 90
)

// OverrideRecord is the ledger's record of the failing verdicts serve answered
// and the approvals serve verified. It only grows: it has no method to change
// or remove a row, and the application role holds no grant to.
type OverrideRecord struct {
	store *Store
}

// OverrideRecord returns the ledger's override record.
func (s *Store) OverrideRecord() OverrideRecord {
	return OverrideRecord{store: s}
}

// FailingVerdict is one rule failing on one head commit of a pull request,
// under the limit the verdict read.
type FailingVerdict struct {
	Repository string
	// BaseBranch is the branch the pull request merges into, as the host
	// stated it. The record is of a rule at one base branch.
	BaseBranch  string
	PullRequest string
	HeadSHA     string
	Rule        string
	Limit       decimal.Decimal
	At          time.Time
}

// PullRequestApproval is one approval of one head commit of a pull request.
type PullRequestApproval struct {
	Repository  string
	PullRequest string
	HeadSHA     string
	Approver    string
	At          time.Time
}

// OverrideQuery names a rule's window: the repository and base branch, the
// rule and its present limit, the pull request being judged, the time of the verdict, and
// when the rule was last re-armed.
type OverrideQuery struct {
	Repository  string
	BaseBranch  string
	PullRequest string
	Rule        string
	Limit       decimal.Decimal
	Rearmed     time.Time
	At          time.Time
}

// since is when the window opens: the later of the re-arm and the window's
// span of days before the verdict.
func (q OverrideQuery) since() time.Time {
	opens := q.At.Add(-OverrideWindowDays * 24 * time.Hour)
	if q.Rearmed.After(opens) {
		return q.Rearmed
	}
	return opens
}

// OverrideCount is a rule's record over its window. Failing counts the pull
// requests on which the rule failed, Approved those of them with an approval
// of a head commit on which it failed, and Approvers the accounts that gave
// those approvals.
type OverrideCount struct {
	Failing   int
	Approved  int
	Approvers int
}

const (
	insertFailingVerdict = `
INSERT INTO failing_verdicts (tenant_id, repo, pr, head_sha, rule, rule_limit, failed_at, base_branch)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5, $6, $7)`
	insertApproval = `
INSERT INTO pull_request_approvals (tenant_id, repo, pr, head_sha, approver, approved_at)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5)`

	// countOverrides reads the latest pull requests into one base branch,
	// other than the one being judged, on which the rule failed under its
	// present limit since the window opened. Each counts once, however many pushes and approvals it
	// had. An approval counts when it binds a head commit on which the rule
	// failed in the window. Logins are one account in any case.
	countOverrides = `
WITH failed AS (
	SELECT pr, head_sha, failed_at FROM failing_verdicts
	WHERE repo = $1 AND base_branch = $8 AND rule = $2 AND rule_limit = $3 AND pr <> $4
		AND failed_at >= $5 AND failed_at <= $6
), latest AS (
	SELECT pr FROM failed GROUP BY pr ORDER BY max(failed_at) DESC, pr DESC LIMIT $7
), overridden AS (
	SELECT a.pr, lower(a.approver) AS approver FROM pull_request_approvals a
	WHERE a.repo = $1 AND a.pr IN (SELECT pr FROM latest)
		AND a.approved_at >= $5 AND a.approved_at <= $6
		AND EXISTS (SELECT 1 FROM failed f WHERE f.pr = a.pr AND f.head_sha = a.head_sha)
)
SELECT (SELECT count(*) FROM latest),
	(SELECT count(DISTINCT pr) FROM overridden),
	(SELECT count(DISTINCT approver) FROM overridden)`
)

// AddFailure records a failing verdict.
func (r OverrideRecord) AddFailure(ctx context.Context, tenant TenantID, verdict FailingVerdict) error {
	err := r.store.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertFailingVerdict, verdict.Repository, verdict.PullRequest, verdict.HeadSHA,
			verdict.Rule, verdict.Limit.String(), verdict.At, verdict.BaseBranch)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record a failing verdict of %s: %w", verdict.Rule, err)
	}
	return nil
}

// AddApproval records an approval.
func (r OverrideRecord) AddApproval(ctx context.Context, tenant TenantID, approval PullRequestApproval) error {
	err := r.store.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertApproval, approval.Repository, approval.PullRequest, approval.HeadSHA,
			approval.Approver, approval.At)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: record an approval: %w", err)
	}
	return nil
}

// Overrides reads a rule's record over its window.
func (r OverrideRecord) Overrides(ctx context.Context, tenant TenantID, query OverrideQuery) (OverrideCount, error) {
	var count OverrideCount
	err := r.store.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, countOverrides, query.Repository, query.Rule, query.Limit.String(), query.PullRequest,
			query.since(), query.At, OverrideWindowPullRequests, query.BaseBranch).Scan(&count.Failing, &count.Approved, &count.Approvers)
	})
	if err != nil {
		return OverrideCount{}, fmt.Errorf("store: read the overrides of %s: %w", query.Rule, err)
	}
	return count, nil
}

// DemotionRecord keeps where each rule's demotion stands in a repository, as
// serve's verdicts moved it. It only grows: a state is kept as a new row, and
// the application role holds no grant to change or remove one.
type DemotionRecord struct {
	store *Store
}

// DemotionRecord returns the ledger's record of where demotions stand.
func (s *Store) DemotionRecord() DemotionRecord {
	return DemotionRecord{store: s}
}

// RuleDemotion is where one rule's demotion stands at one base branch of a
// repository: the limit its window
// counts failures under, when the window last started empty, whether
// policy.auto_demote was false, and, once a verdict's record crossed the
// threshold, when and on what counts. From is zero while the rule is armed.
type RuleDemotion struct {
	Repository string
	BaseBranch string
	Rule       string
	Limit      decimal.Decimal
	Rearmed    time.Time
	Off        bool
	From       time.Time
	Failing    int
	Approved   int
	Approvers  int
}

const (
	// selectDemotion reads the latest re-arm, and the row of the verdict that
	// started its term before the re-arm's own.
	selectDemotion = `
SELECT rule_limit, rearmed_at, off, demoted_at, failing, approved, approvers FROM rule_demotions
WHERE repo = $1 AND base_branch = $3 AND rule = $2
ORDER BY rearmed_at DESC, (demoted_at IS NULL) ASC
LIMIT 1`
	// insertDemotion keeps a state, and nothing when the same re-arm or the
	// same term is kept already.
	insertDemotion = `
INSERT INTO rule_demotions (tenant_id, repo, rule, rule_limit, rearmed_at, off, demoted_at, failing, approved, approvers, base_branch)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING`
)

// Demotion reads where a rule's demotion stands at a base branch, and false
// when no verdict has moved it.
func (r DemotionRecord) Demotion(ctx context.Context, tenant TenantID, repository, branch, rule string) (RuleDemotion, bool, error) {
	var kept RuleDemotion
	found := false
	err := r.store.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		kept, found, err = readDemotion(ctx, tx, repository, branch, rule)
		return err
	})
	if err != nil {
		return RuleDemotion{}, false, fmt.Errorf("store: read the demotion of %s: %w", rule, err)
	}
	return kept, found, nil
}

// Keep keeps a rule's demotion, and returns the one that stands after it:
// another verdict's when that one kept the same re-arm or term first.
func (r DemotionRecord) Keep(ctx context.Context, tenant TenantID, demotion RuleDemotion) (RuleDemotion, error) {
	var kept RuleDemotion
	err := r.store.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		var from *time.Time
		var failing, approved, approvers *int
		if !demotion.From.IsZero() {
			from, failing, approved, approvers = &demotion.From, &demotion.Failing, &demotion.Approved, &demotion.Approvers
		}
		if _, err := tx.Exec(ctx, insertDemotion, demotion.Repository, demotion.Rule, demotion.Limit.String(),
			demotion.Rearmed, demotion.Off, from, failing, approved, approvers, demotion.BaseBranch); err != nil {
			return err
		}
		var err error
		kept, _, err = readDemotion(ctx, tx, demotion.Repository, demotion.BaseBranch, demotion.Rule)
		return err
	})
	if err != nil {
		return RuleDemotion{}, fmt.Errorf("store: keep the demotion of %s: %w", demotion.Rule, err)
	}
	return kept, nil
}

func readDemotion(ctx context.Context, tx pgx.Tx, repository, branch, rule string) (RuleDemotion, bool, error) {
	kept := RuleDemotion{Repository: repository, BaseBranch: branch, Rule: rule}
	var limit string
	var from *time.Time
	var failing, approved, approvers *int
	err := tx.QueryRow(ctx, selectDemotion, repository, rule, branch).Scan(&limit, &kept.Rearmed, &kept.Off, &from, &failing, &approved, &approvers)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuleDemotion{}, false, nil
	}
	if err != nil {
		return RuleDemotion{}, false, err
	}
	if kept.Limit, err = decimal.Parse(limit); err != nil {
		return RuleDemotion{}, false, err
	}
	kept.Rearmed = kept.Rearmed.UTC()
	if from != nil && failing != nil && approved != nil && approvers != nil {
		kept.From, kept.Failing, kept.Approved, kept.Approvers = from.UTC(), *failing, *approved, *approvers
	}
	return kept, true, nil
}
