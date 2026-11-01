package server

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	benchRows   = 1_000_000
	benchRepos  = 20
	benchPulls  = 20_000
	benchTarget = 200 * time.Millisecond
	benchPaths  = 5_000
	// A second tenant, so row level security has rows to exclude.
	benchOtherRows = 100_000
)

// seedRows writes rows straight into the table, because the benchmark measures
// the read. Pull request numbers are skewed by a cube, which gives a few pull
// requests thousands of rows and most of them a handful, and the document is a
// full row of the size ingest stores.
const seedRows = `
INSERT INTO focus_rows (
	tenant_id, row_id, billing_currency, service_category, sku_meter, consumed_unit, consumed_quantity,
	list_cost, effective_cost, charge_period_start, charge_period_end, cost_basis, cost_source,
	attribution_grade, enforcement_grade, run_identity, agent, model, repo, pr, commit_sha,
	workflow_run_id, actor, catalog_version, document)
SELECT costroid_tenant(), encode(sha256(($1 || ':' || i)::bytea), 'hex'), 'USD', 'AI and Machine Learning',
	(ARRAY['input_uncached', 'cache_read', 'cache_write_5m', 'output', 'reasoning_output'])[1 + i % 5],
	CASE WHEN i % 5 = 4 THEN 'Informational Tokens' ELSE 'Tokens' END,
	1 + (i * 7919) % 90000,
	((1 + (i * 7919) % 90000) * 3)::numeric / 1000000,
	CASE WHEN i % 3 = 0 THEN 0 ELSE ((1 + (i * 7919) % 90000) * 3)::numeric / 1000000 END,
	timestamptz '2026-10-01' + (i % 2592000) * interval '1 second',
	timestamptz '2026-10-01' + (i % 2592000 + 2) * interval '1 second',
	CASE WHEN i % 3 = 0 THEN 'list_equivalent' ELSE 'measured' END,
	CASE WHEN i % 2 = 0 THEN 'otlp' ELSE 'agent_stream' END,
	CASE WHEN i % 11 = 0 THEN 'windowed' ELSE 'exact' END, 'agent-flag', 'token_verified',
	(ARRAY['claude-code', 'codex', 'copilot'])[1 + i % 3],
	(ARRAY['claude-opus-5-5', 'claude-haiku-5-5', 'gpt-5.6-sol', 'gpt-6-sol'])[1 + i % 4],
	'github.com/acme/repo-' || (i % $2), (1 + floor($3 * random() ^ 3))::bigint::text,
	md5(i::text) || md5(i::text), (i / 50)::text, 'octo-dev', '0.1.0',
	$4::jsonb || jsonb_build_object('x_CostroidRowId', encode(sha256(($1 || ':' || i)::bytea), 'hex'))
FROM generate_series(1, $5::bigint) AS i`

func seedTenant(b *testing.B, conn *pgx.Conn, tenant store.TenantID, salt string, rows int, document string) {
	b.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, seedRows, salt, benchRepos, benchPulls, document, rows)
		return err
	})
	if err != nil {
		b.Fatal(err)
	}
}

// benchPathsFrom samples view paths weighted by rows, so a heavy pull request
// is asked for as often as its size says it is, the way a busy repository is.
func benchPathsFrom(b *testing.B, conn *pgx.Conn, tenant store.TenantID) []string {
	b.Helper()
	ctx := context.Background()
	var paths []string
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT repo, pr FROM focus_rows TABLESAMPLE BERNOULLI (2) LIMIT $1`, benchPaths)
		if err != nil {
			return err
		}
		var repo, pr string
		_, err = pgx.ForEachRow(rows, []any{&repo, &pr}, func() error {
			paths = append(paths, "/v1/pulls/"+pr+"?repo="+repo)
			return nil
		})
		return err
	})
	if err != nil {
		b.Fatal(err)
	}
	return paths
}

type viewBench struct {
	world *world
	paths []string
	max   int64
}

func setupViewBench(b *testing.B) viewBench {
	b.Helper()
	db := storetest.New(b)
	a := db.Tenant(b, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}}, store.KeyDigest(acmeKey))
	other := db.Tenant(b, "beta", []store.Binding{{Host: "github.com", Owner: "beta"}})
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	document := string(lines(b, row("seed", "output", 1)))
	started := time.Now()
	seedTenant(b, owner, a, "a", benchRows, document)
	seedTenant(b, owner, other, "b", benchOtherRows, document)
	if _, err := owner.Exec(ctx, `ANALYZE focus_rows`); err != nil {
		b.Fatal(err)
	}
	b.Logf("seeded %d + %d rows in %s", benchRows, benchOtherRows, time.Since(started).Round(time.Second))
	ledger, err := store.Open(ctx, db.AppURL)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(ledger.Close)
	return viewBench{world: newWorld(b, ledger), paths: benchPathsFrom(b, owner, a), max: heaviestPull(b, owner, a)}
}

func heaviestPull(b *testing.B, conn *pgx.Conn, tenant store.TenantID) int64 {
	b.Helper()
	ctx := context.Background()
	var heaviest int64
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT max(n) FROM (SELECT count(*) AS n FROM focus_rows GROUP BY repo, pr) t`).Scan(&heaviest)
	})
	if err != nil {
		b.Fatal(err)
	}
	return heaviest
}

func percentile(sorted []time.Duration, percent int) time.Duration {
	return sorted[((len(sorted)-1)*percent+50)/100]
}

// BenchmarkPullRequestView measures GET /v1/pulls/{pr} over HTTP, with key
// authentication and row level security, on one million rows of one tenant.
// It fails past the 200 ms bar, so a green run is the measurement holding.
func BenchmarkPullRequestView(b *testing.B) {
	state := setupViewBench(b)
	random := rand.New(rand.NewPCG(1, 2))
	var samples []time.Duration
	for b.Loop() {
		path := state.paths[random.IntN(len(state.paths))]
		started := time.Now()
		got := state.world.send(http.MethodGet, path, acmeKey, "", nil)
		samples = append(samples, time.Since(started))
		if got.status != http.StatusOK {
			b.Fatalf("status %d: %s", got.status, got.body)
		}
	}
	b.StopTimer()
	slices.Sort(samples)
	p50, p99 := percentile(samples, 50), percentile(samples, 99)
	b.ReportMetric(p50.Seconds()*1000, "p50-ms")
	b.ReportMetric(p99.Seconds()*1000, "p99-ms")
	fmt.Printf("per-PR view: %d requests over %d rows, heaviest pull request %d rows: p50 %s, p99 %s, max %s\n",
		len(samples), benchRows, state.max, p50, p99, samples[len(samples)-1])
	if p99 >= benchTarget {
		b.Fatalf("p99 %s is at or over %s", p99, benchTarget)
	}
}
