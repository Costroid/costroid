package store_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	overrideBenchFailures      = 1_000_000
	overrideBenchOtherFailures = 100_000
	overrideBenchRepos         = 20
)

var (
	overrideBenchStart = time.Date(2025, 11, 20, 0, 0, 0, 0, time.UTC)
	overrideBenchRules = []string{"run_budget", "run_cost_threshold", "run_increase", "run_increase_percent", "pr_budget"}
)

// seedFailingVerdicts writes a year of failing verdicts straight into the
// table, because the benchmark measures the read. Each repository's pull
// requests merge into main and are numbered in time, and each fails every
// rule four times over four pushes. The limit moves from 4 to 5 halfway
// through the year.
const seedFailingVerdicts = `
INSERT INTO failing_verdicts (tenant_id, repo, pr, head_sha, rule, rule_limit, failed_at, base_branch)
SELECT costroid_tenant(), repo, pr, md5(seed) || substr(md5(seed), 1, 8), rule,
	CASE WHEN at < $3::timestamptz + interval '182 days' THEN '4' ELSE '5' END, at, 'main'
FROM (
	SELECT 'github.com/acme/repo-' || (i % $1) AS repo, (1 + i / 400)::text AS pr,
		($2::text[])[1 + (i / $1) % 5] AS rule,
		$4 || ':' || (i % $1) || ':' || (i / 400) || ':' || ((i / 100) % 4) AS seed,
		$3::timestamptz + (i * (365 * 86400)::numeric / $5) * interval '1 second' AS at
	FROM generate_series(0, $5::bigint - 1) AS i
) AS t`

// seedApprovals approves every head of one pull request in four, from one of
// three accounts, at the head's first failure.
const seedApprovals = `
INSERT INTO pull_request_approvals (tenant_id, repo, pr, head_sha, approver, approved_at)
SELECT costroid_tenant(), repo, pr, head_sha, (ARRAY['octo-lead', 'octo-ops', 'octo-dev'])[1 + pr::bigint % 3], min(failed_at)
FROM failing_verdicts WHERE pr::bigint % 4 = 0
GROUP BY repo, pr, head_sha`

func seedOverrides(b *testing.B, conn *pgx.Conn, tenant store.TenantID, salt string, failures int) {
	b.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, seedFailingVerdicts, overrideBenchRepos, overrideBenchRules, overrideBenchStart, salt, failures); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, seedApprovals)
		return err
	})
	if err != nil {
		b.Fatal(err)
	}
}

// explainOverrides logs the plan of one query as the application role sees
// it, through row level security.
func explainOverrides(b *testing.B, db storetest.Database, tenant store.TenantID, query store.OverrideQuery) {
	b.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.AppURL)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var plan []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+store.CountOverrides, query.Repository, query.Rule,
			query.Limit.String(), query.PullRequest, query.At.Add(-store.OverrideWindowDays*24*time.Hour), query.At,
			store.OverrideWindowPullRequests, query.BaseBranch)
		if err != nil {
			return err
		}
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("plan of one query:\n%s", strings.Join(plan, "\n"))
}

// BenchmarkOverrides measures one rule's override query over a year of
// failing verdicts in one tenant, with row level security and a second
// tenant's rows beside them. Each query asks for a random repository and rule
// under the present limit, at a random time in the year's second half.
func BenchmarkOverrides(b *testing.B) {
	db := storetest.New(b)
	a := db.Tenant(b, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	other := db.Tenant(b, "beta", []store.Binding{{Host: "github.com", Owner: "beta"}})
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	started := time.Now()
	seedOverrides(b, owner, a, "a", overrideBenchFailures)
	seedOverrides(b, owner, other, "b", overrideBenchOtherFailures)
	if _, err := owner.Exec(ctx, `VACUUM ANALYZE failing_verdicts, pull_request_approvals`); err != nil {
		b.Fatal(err)
	}
	b.Logf("seeded %d + %d failing verdicts in %s", overrideBenchFailures, overrideBenchOtherFailures, time.Since(started).Round(time.Second))
	ledger, err := store.Open(ctx, db.AppURL)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(ledger.Close)
	record := ledger.OverrideRecord()
	random := rand.New(rand.NewPCG(1, 2))
	query := func() store.OverrideQuery {
		return store.OverrideQuery{
			Repository:  fmt.Sprintf("github.com/acme/repo-%d", random.IntN(overrideBenchRepos)),
			BaseBranch:  "main",
			PullRequest: fmt.Sprint(1 + random.IntN(2500)),
			Rule:        overrideBenchRules[random.IntN(len(overrideBenchRules))],
			Limit:       decimal.MustParse("5"),
			At:          overrideBenchStart.Add(time.Duration(183+random.IntN(182)) * 24 * time.Hour),
		}
	}
	explainOverrides(b, db, a, query())
	var samples []time.Duration
	full := 0
	for b.Loop() {
		started := time.Now()
		count, err := record.Overrides(ctx, a, query())
		samples = append(samples, time.Since(started))
		if err != nil {
			b.Fatal(err)
		}
		if count.Failing == store.OverrideWindowPullRequests {
			full++
		}
	}
	b.StopTimer()
	slices.Sort(samples)
	p50, p99 := percentile(samples, 50), percentile(samples, 99)
	b.ReportMetric(p50.Seconds()*1000, "p50-ms")
	b.ReportMetric(p99.Seconds()*1000, "p99-ms")
	fmt.Printf("override query: %d queries over %d failing verdicts, %d of them on a full window of %d pull requests: p50 %s, p99 %s, max %s\n",
		len(samples), overrideBenchFailures, full, store.OverrideWindowPullRequests, p50, p99, samples[len(samples)-1])
}

func percentile(sorted []time.Duration, percent int) time.Duration {
	return sorted[((len(sorted)-1)*percent+50)/100]
}
