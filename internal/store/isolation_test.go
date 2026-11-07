package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	repoA = "github.com/acme/widgets"
	prA   = "12"
)

var testDay = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func open(t *testing.T, db storetest.Database) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

func rowID(n int) string {
	return fmt.Sprintf("%064x", n)
}

// record builds a stored row by hand. Pricing is the server's and is tested there.
// record builds a row. An exact one names a credential the service proved
// minted for its run, as the service writes it.
func record(id string, list string, grade, identity string) store.Record {
	version := "0.1.0"
	cost := decimal.MustParse(list)
	basis := ""
	if grade == "exact" {
		basis = store.CredentialMinted
	}
	return store.Record{
		Row: focus.Row{
			RowID: id, BillingCurrency: "USD", ServiceCategory: "AI and Machine Learning",
			SkuMeter: focus.Output, ConsumedUnit: "Tokens", ConsumedQuantity: decimal.FromInt(100),
			ListCost: cost, EffectiveCost: cost, ContractedCost: cost, CatalogVersion: &version,
			ChargePeriodStart: "2026-11-01T10:00:00Z", ChargePeriodEnd: "2026-11-01T10:00:01Z",
			Tags: map[string]string{
				focus.TagAgent: "claude-code", focus.TagModel: "claude-opus-5-5", focus.TagCostSource: "otlp",
				focus.TagCostBasis: "measured", focus.TagAttributionGrade: grade, focus.TagEnforcementGrade: "none",
				focus.TagRunIdentity: identity, store.TagRepo: repoA, store.TagPR: prA,
			},
		},
		Actor:           "octo-dev",
		CredentialBasis: basis,
	}
}

func batch(records ...store.Record) store.Batch {
	return store.Batch{Records: records, Day: testDay}
}

func tenants(t *testing.T, db storetest.Database) (store.TenantID, store.TenantID) {
	t.Helper()
	a := db.Tenant(t, "a", []store.Binding{{Host: "github.com", Owner: "acme"}}, rowID(1001))
	b := db.Tenant(t, "b", []store.Binding{{Host: "github.com", Owner: "beta"}}, rowID(1002))
	return a, b
}

func TestEveryTableForcesRowLevelSecurityAndTheAppRoleOwnsNothing(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	conn := connect(t, db.SuperURL)
	rows, err := conn.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity, pg_get_userbyid(c.relowner)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`)
	if err != nil {
		t.Fatal(err)
	}
	var name, owner string
	var enabled, forced bool
	checked := 0
	_, err = pgx.ForEachRow(rows, []any{&name, &enabled, &forced, &owner}, func() error {
		if owner == storetest.AppRole {
			t.Errorf("%s is owned by the application role", name)
		}
		if name == "schema_migrations" {
			return nil
		}
		checked++
		if !enabled || !forced {
			t.Errorf("%s: row security %t, forced %t", name, enabled, forced)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("checked only %d tables", checked)
	}
	var super, bypass bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1`, storetest.AppRole).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("superuser %t, bypassrls %t", super, bypass)
	}
}

func TestOpenRefusesARoleThatReadsPastRowLevelSecurity(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	for name, url := range map[string]string{"owner": db.OwnerURL, "superuser": db.SuperURL} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Open(ctx, url); !errors.Is(err, store.ErrUnsafeRole) {
				t.Fatalf("got %v", err)
			}
		})
	}
	conn := connect(t, db.SuperURL)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s BYPASSRLS`, storetest.AppRole)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s NOBYPASSRLS`, storetest.AppRole)) }()
	if _, err := store.Open(ctx, db.AppURL); !errors.Is(err, store.ErrUnsafeRole) {
		t.Fatalf("bypassrls: got %v", err)
	}
}

func TestATenantReadsOnlyItsOwnRows(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	if _, err := s.Ingest(ctx, a, batch(record(rowID(1), "1.5", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, b, batch(record(rowID(2), "2.5", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	for tenant, want := range map[store.TenantID]string{a: "1.5", b: "2.5"} {
		lines, err := s.PullRequest(ctx, tenant, repoA, prA)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 1 || lines[0].ListCost.String() != want {
			t.Fatalf("tenant %s: %+v", tenant, lines)
		}
	}
}

func inTenant(t *testing.T, conn *pgx.Conn, tenant store.TenantID, fn func(tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL costroid.tenant_id = '%s'`, tenant)); err != nil {
			return err
		}
		return fn(tx)
	})
}

func TestRawSQLAsTheAppRoleCannotCrossTenants(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	if _, err := s.Ingest(ctx, b, batch(record(rowID(2), "2.5", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	tables := []string{"focus_rows", "ingest_drops", "tenant_bindings", "ingest_keys"}
	err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		for _, table := range tables {
			var count int
			query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = '%s'`, table, b)
			if err := tx.QueryRow(ctx, query).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Errorf("%s: read %d of the other tenant's rows", table, count)
			}
		}
		var tenantsSeen int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&tenantsSeen); err != nil {
			return err
		}
		if tenantsSeen != 1 {
			t.Errorf("saw %d tenants", tenantsSeen)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoTenantSetReadsNothing(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	if _, err := s.Ingest(ctx, a, batch(record(rowID(1), "1", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	for _, table := range []string{"focus_rows", "tenants", "tenant_bindings", "ingest_keys", "ingest_drops"} {
		var count int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s: %d rows with no tenant set", table, count)
		}
	}
}

func TestTheTenantSettingEndsWithItsTransaction(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, _ := tenants(t, db)
	conn := connect(t, db.AppURL)
	check := func(stage string) {
		t.Helper()
		var seen string
		if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('costroid.tenant_id', true), '')`).Scan(&seen); err != nil {
			t.Fatal(err)
		}
		if seen != "" {
			t.Fatalf("%s: tenant %q leaked onto the connection", stage, seen)
		}
	}
	if err := inTenant(t, conn, a, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check("after commit")
	sentinel := errors.New("roll back")
	if err := inTenant(t, conn, a, func(pgx.Tx) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	check("after rollback")
}

func TestTheStoreLeavesNoTenantOnAPooledConnection(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, _ := tenants(t, db)
	s, err := store.Open(ctx, db.AppURL+"&pool_max_conns=1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Ingest(ctx, a, batch(record(rowID(1), "1", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PullRequest(ctx, a, repoA, prA); err != nil {
		t.Fatal(err)
	}
	seen, err := s.SessionTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Fatalf("tenant %q stayed on the connection", seen)
	}
}

func requireSQLState(t *testing.T, err error, state string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != state {
		t.Fatalf("got %v, want SQLSTATE %s", err, state)
	}
}

const (
	insufficientPrivilege = "42501"
	checkViolation        = "23514"
)

func TestAnInsertNamingAnotherTenantIsRefusedByPolicy(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, b := tenants(t, db)
	conn := connect(t, db.AppURL)
	err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ingest_drops (tenant_id, day, field, count) VALUES ($1, '2026-11-01', 'x', 1)`, b.String())
		return err
	})
	requireSQLState(t, err, insufficientPrivilege)
}

func TestTheAppRoleCannotRewriteOrRemoveRows(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	if _, err := s.Ingest(ctx, a, batch(record(rowID(1), "1", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	for _, statement := range []string{
		`UPDATE focus_rows SET list_cost = 0`,
		`DELETE FROM focus_rows`,
		`TRUNCATE focus_rows`,
		`DROP TABLE focus_rows`,
		`ALTER TABLE focus_rows DISABLE ROW LEVEL SECURITY`,
		`INSERT INTO tenants (id, name) VALUES (gen_random_uuid(), 'x')`,
		`INSERT INTO tenant_bindings (owner_key, tenant_id) VALUES ('github.com:x', gen_random_uuid())`,
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
}

func TestTheAppRoleCannotListBindingsOrKeys(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	tenants(t, db)
	conn := connect(t, db.AppURL)
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('costroid.lookup', 'github.com:acme', true)`); err != nil {
			return err
		}
		var bindings, keys int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant_bindings`).Scan(&bindings); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ingest_keys`).Scan(&keys); err != nil {
			return err
		}
		if bindings != 1 || keys != 0 {
			t.Errorf("a lookup of one owner read %d bindings and %d keys", bindings, keys)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLookupsResolveExactlyTheirKey(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	got, err := s.TenantForOwner(ctx, "GitHub.com", "Acme")
	if err != nil || got != a {
		t.Fatalf("owner: %v %v", got, err)
	}
	if got, err := s.TenantForKey(ctx, rowID(1002)); err != nil || got != b {
		t.Fatalf("key: %v %v", got, err)
	}
	if _, err := s.TenantForOwner(ctx, "gitlab.com", "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another host's owner: %v", err)
	}
	if _, err := s.TenantForKey(ctx, rowID(1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}
}

func TestARevokedKeyResolvesToNothing(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	err := inTenant(t, connect(t, db.OwnerURL), a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ingest_keys SET revoked_at = now()`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TenantForKey(ctx, rowID(1001)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestIngestIsIdempotentOnRowID(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	first := batch(record(rowID(1), "1", "exact", "token_verified"), record(rowID(2), "2", "exact", "token_verified"))
	receipt, err := s.Ingest(ctx, a, first)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 2 || receipt.Duplicates != 0 {
		t.Fatalf("%+v", receipt)
	}
	retry := batch(record(rowID(1), "9", "exact", "token_verified"), record(rowID(3), "3", "exact", "token_verified"))
	receipt, err = s.Ingest(ctx, a, retry)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 || receipt.Duplicates != 1 {
		t.Fatalf("%+v", receipt)
	}
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].ListCost.String() != "6" || lines[0].Rows != 3 {
		t.Fatalf("%+v", lines)
	}
}

func TestARepeatInsideOneBatchCountsOnce(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	row := record(rowID(1), "1", "exact", "token_verified")
	receipt, err := s.Ingest(context.Background(), a, batch(row, row))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 || receipt.Duplicates != 1 {
		t.Fatalf("%+v", receipt)
	}
}

func TestOneRowIDInTwoTenantsIsTwoRows(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	row := record(rowID(1), "1", "exact", "token_verified")
	if _, err := s.Ingest(ctx, a, batch(row)); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Ingest(ctx, b, batch(row))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 {
		t.Fatalf("one tenant's row id blocked another's: %+v", receipt)
	}
}

func TestAFailedBatchWritesNothingAtAll(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	bad := record(rowID(2), "1", "exact", "unverified")
	failing := batch(record(rowID(1), "1", "exact", "token_verified"), bad)
	failing.Drops = map[string]int64{"prompt": 1}
	_, err := s.Ingest(ctx, a, failing)
	requireSQLState(t, err, checkViolation)
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("%+v", lines)
	}
}

func TestTheDatabaseRefusesAnUnverifiedRowClaimingAGrade(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	for _, grade := range []string{"exact", "windowed"} {
		_, err := s.Ingest(context.Background(), a, batch(record(rowID(1), "1", grade, "unverified")))
		requireSQLState(t, err, checkViolation)
	}
}

func TestTheDatabaseRefusesAnExactRowWhoseCredentialWasNotMinted(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	a, _ := tenants(t, db)
	for _, basis := range []string{store.CredentialStated, ""} {
		exact := record(rowID(1), "1", "exact", "token_verified")
		exact.CredentialBasis = basis
		_, err := s.Ingest(context.Background(), a, batch(exact))
		requireSQLState(t, err, checkViolation)
	}
	stated := record(rowID(2), "1", "windowed", "token_verified")
	stated.CredentialBasis = store.CredentialStated
	if _, err := s.Ingest(context.Background(), a, batch(stated)); err != nil {
		t.Fatalf("a windowed row with a stated credential was refused: %v", err)
	}
}

func TestTheDatabaseRefusesAHeadBasisThatContradictsTheColumns(t *testing.T) {
	db := storetest.New(t)
	a, _ := tenants(t, db)
	s := open(t, db)
	commit, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	verified := func(id int, mutate func(*store.Record)) store.Record {
		r := record(rowID(id), "1", "windowed", "token_verified")
		r.Row.Tags[store.TagCommitSHA] = commit
		mutate(&r)
		return r
	}
	cases := map[string]struct {
		record     store.Record
		constraint string
	}{
		"an unknown basis": {verified(1, func(r *store.Record) { r.HeadBasis = "guessed" }), "head_basis_known"},
		"a token head that is not the token's commit": {
			verified(2, func(r *store.Record) { r.HeadBasis, r.TokenCommitSHA = store.HeadFromToken, other }),
			"head_from_token_is_the_token_commit",
		},
		"an unverified row holding a token commit": {
			func() store.Record {
				r := record(rowID(3), "1", "unattributed", "unverified")
				r.TokenCommitSHA = commit
				return r
			}(),
			"unverified_has_no_token_commit",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Ingest(context.Background(), a, batch(tc.record))
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != tc.constraint {
				t.Fatalf("err = %v, want constraint %s", err, tc.constraint)
			}
		})
	}
}

func TestBudgetEligibilityIsDerivedByTheDatabase(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	records := batch(
		record(rowID(1), "1", "exact", "token_verified"),
		record(rowID(2), "2", "windowed", "token_verified"),
		record(rowID(3), "4", "unattributed", "token_verified"),
		record(rowID(4), "8", "unattributed", "unverified"),
	)
	if _, err := s.Ingest(ctx, a, records); err != nil {
		t.Fatal(err)
	}
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	eligible := map[string]bool{}
	for _, line := range lines {
		eligible[line.ListCost.String()] = line.BudgetEligible
	}
	want := map[string]bool{"1": true, "2": true, "4": false, "8": false}
	if fmt.Sprint(eligible) != fmt.Sprint(want) {
		t.Fatalf("got %v", eligible)
	}
}

func TestDropCountersAddUpAndHoldNoValues(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	dropping := store.Batch{Drops: map[string]int64{"prompt": 2, "gen_ai.input.messages": 1}, Day: testDay}
	for range 2 {
		if _, err := s.Ingest(ctx, a, dropping); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Ingest(ctx, b, dropping); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		var total int64
		if err := tx.QueryRow(ctx, `SELECT count FROM ingest_drops WHERE field = 'prompt'`).Scan(&total); err != nil {
			return err
		}
		if total != 4 {
			t.Errorf("prompt counted %d times", total)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCostsSurviveTheDatabaseToTwelveDigits(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	first, second := "0.000000000001", "12345678901234.000000000002"
	_, err := s.Ingest(ctx, a, batch(record(rowID(1), first, "exact", "token_verified"), record(rowID(2), second, "exact", "token_verified")))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	if want := "12345678901234.000000000003"; lines[0].ListCost.String() != want {
		t.Fatalf("got %s", lines[0].ListCost)
	}
}

func TestTenantIDsMustBeCanonical(t *testing.T) {
	for _, text := range []string{"", "x", "'; DROP TABLE focus_rows; --", strings.Repeat("a", 36), "ABCDEF00-0000-4000-8000-000000000000"} {
		if _, err := store.ParseTenantID(text); err == nil {
			t.Errorf("%q accepted", text)
		}
	}
}

func TestTwoSendersOfOneBatchWriteItOnce(t *testing.T) {
	db := storetest.New(t)
	a, _ := tenants(t, db)
	s := open(t, db)
	records := make([]store.Record, 500)
	for i := range records {
		records[i] = record(rowID(i), "1", "exact", "token_verified")
	}
	const senders = 8
	receipts := make([]store.Receipt, senders)
	errs := make([]error, senders)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range senders {
		group.Go(func() {
			<-start
			receipts[i], errs[i] = s.Ingest(context.Background(), a, batch(records...))
		})
	}
	close(start)
	group.Wait()
	inserted := 0
	for i := range senders {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if receipts[i].Inserted+receipts[i].Duplicates != len(records) {
			t.Fatalf("%+v", receipts[i])
		}
		inserted += receipts[i].Inserted
	}
	if inserted != len(records) {
		t.Fatalf("%d rows inserted across the senders", inserted)
	}
}

func TestABatchMixingNewAndStoredRowsWritesOnlyTheNew(t *testing.T) {
	db := storetest.New(t)
	a, _ := tenants(t, db)
	s := open(t, db)
	ctx := context.Background()
	old := record(rowID(1), "1", "exact", "token_verified")
	if _, err := s.Ingest(ctx, a, batch(old)); err != nil {
		t.Fatal(err)
	}
	changed := record(rowID(1), "999", "exact", "token_verified")
	receipt, err := s.Ingest(ctx, a, batch(changed, record(rowID(2), "2", "exact", "token_verified")))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 || receipt.Duplicates != 1 {
		t.Fatalf("%+v", receipt)
	}
	lines, err := s.PullRequest(ctx, a, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].ListCost.String() != "3" {
		t.Fatalf("%+v", lines)
	}
}

func TestARowInAnotherTenantIsNoDuplicateOnTheFirstAttempt(t *testing.T) {
	db := storetest.New(t)
	a, b := tenants(t, db)
	s := open(t, db)
	ctx := context.Background()
	if _, err := s.Ingest(ctx, b, batch(record(rowID(1), "1", "exact", "token_verified"))); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Ingest(ctx, a, batch(record(rowID(1), "1", "exact", "token_verified")))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 {
		t.Fatalf("%+v", receipt)
	}
}

func TestABatchThatTakesTheSecondAttemptCountsItsDropsOnce(t *testing.T) {
	db := storetest.New(t)
	a, _ := tenants(t, db)
	s := open(t, db)
	ctx := context.Background()
	repeat := record(rowID(1), "1", "exact", "token_verified")
	receipt, err := s.Ingest(ctx, a, store.Batch{
		Records: []store.Record{repeat, repeat}, Drops: map[string]int64{"gen_ai.input.messages": 3}, Day: testDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Inserted != 1 || receipt.Duplicates != 1 {
		t.Fatalf("%+v", receipt)
	}
	conn := connect(t, db.SuperURL)
	var count int64
	if err := conn.QueryRow(ctx, `SELECT count FROM ingest_drops`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("counted %d", count)
	}
}
