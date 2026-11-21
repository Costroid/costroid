package store

import (
	"context"
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
	Repository  string
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

// OverrideQuery names a rule's window: the repository, the rule and its
// present limit, the pull request being judged, the time of the verdict, and
// when the rule was last re-armed.
type OverrideQuery struct {
	Repository  string
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
INSERT INTO failing_verdicts (tenant_id, repo, pr, head_sha, rule, rule_limit, failed_at)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5, $6)`
	insertApproval = `
INSERT INTO pull_request_approvals (tenant_id, repo, pr, head_sha, approver, approved_at)
VALUES (costroid_tenant(), $1, $2, $3, $4, $5)`

	// countOverrides reads the latest pull requests, other than the one being
	// judged, on which the rule failed under its present limit since the
	// window opened. Each counts once, however many pushes and approvals it
	// had. An approval counts when it binds a head commit on which the rule
	// failed in the window. Logins are one account in any case.
	countOverrides = `
WITH failed AS (
	SELECT pr, head_sha, failed_at FROM failing_verdicts
	WHERE repo = $1 AND rule = $2 AND rule_limit = $3 AND pr <> $4
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
			verdict.Rule, verdict.Limit.String(), verdict.At)
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
			query.since(), query.At, OverrideWindowPullRequests).Scan(&count.Failing, &count.Approved, &count.Approvers)
	})
	if err != nil {
		return OverrideCount{}, fmt.Errorf("store: read the overrides of %s: %w", query.Rule, err)
	}
	return count, nil
}
