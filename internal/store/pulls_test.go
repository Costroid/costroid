package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// pullsMigration is the migration from which an attestation names its pull
// requests.
const pullsMigration = "0013_credential_pulls.sql"

// naming is minted's attestation, naming the pull requests given.
func naming(key, run string, pulls ...string) store.MintedCredential {
	m := minted(key, run)
	m.PullRequests = store.PullRequestsOf(pulls)
	return m
}

// TestASecondStatementOfAnAttestationWithOtherPullRequestsIsRefused stores an
// attestation naming two pull requests. The same set again, in another order
// or with a number twice, is the same attestation. Another set from the same
// attesting run is ErrAttestingRunUsed, and from another
// ErrAttestedElsewhere, as another head is. The set is read back as stored,
// in the tenant alone. The database holds a pull request to a number and to a
// key attested in the tenant, and lets the service's role change no row.
func TestASecondStatementOfAnAttestationWithOtherPullRequestsIsRefused(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	key := rowID(31)
	first := naming(key, "1", "12", "9")
	if err := s.Attest(ctx, a, first); err != nil {
		t.Fatal(err)
	}
	for name, again := range map[string]store.MintedCredential{
		"the same":              first,
		"in another order":      naming(key, "1", "9", "12"),
		"with a number twice":   naming(key, "1", "12", "9", "12"),
		"read from the numbers": naming(key, "1", first.PullRequests.Numbers()...),
	} {
		if err := s.Attest(ctx, a, again); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, pulls := range map[string][]string{
		"one of the two":  {"12"},
		"none":            nil,
		"a third besides": {"9", "12", "13"},
		"another":         {"13"},
	} {
		if err := s.Attest(ctx, a, naming(key, "1", pulls...)); !errors.Is(err, store.ErrAttestingRunUsed) {
			t.Errorf("%s from the same attesting run: %v", name, err)
		}
		later := naming(key, "1", pulls...)
		later.AttestingRunAttempt = "2"
		if err := s.Attest(ctx, a, later); !errors.Is(err, store.ErrAttestedElsewhere) {
			t.Errorf("%s from a later attempt of the attesting run: %v", name, err)
		}
	}
	found, err := s.MintedCredentials(ctx, a, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if found[key] != first || found[key].PullRequests != "9 12" {
		t.Fatalf("read %+v, stored %+v", found[key], first)
	}
	if err := s.Attest(ctx, b, naming(key, "1", "13")); err != nil {
		t.Fatalf("another tenant's attestation of the same key id: %v", err)
	}
	if found, err := s.MintedCredentials(ctx, a, []string{key}); err != nil || found[key].PullRequests != "9 12" {
		t.Fatalf("after another tenant's attestation: %+v, %v", found[key], err)
	}

	conn := connect(t, db.AppURL)
	insert := `INSERT INTO credential_pulls (tenant_id, key_sha256, pr) VALUES (costroid_tenant(), $1, $2)`
	for name, statement := range map[string]struct {
		sql       string
		arguments []any
		state     string
	}{
		"a key no attestation holds": {insert, []any{rowID(32), "12"}, foreignKeyViolation},
		"a pull request 0":           {insert, []any{key, "0"}, checkViolation},
		"a pull request named":       {insert, []any{key, "#14"}, checkViolation},
		"the same pull request":      {insert, []any{key, "12"}, uniqueViolation},
		"an update":                  {`UPDATE credential_pulls SET pr = '14'`, nil, insufficientPrivilege},
		"a delete":                   {`DELETE FROM credential_pulls`, nil, insufficientPrivilege},
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement.sql, statement.arguments...)
			return err
		})
		if err == nil {
			t.Errorf("%s was taken", name)
			continue
		}
		requireSQLState(t, err, statement.state)
	}
}

// TestAKeyNamingTwoPullRequestsCountsOnEach attests a key for two pull
// requests and counts USD 10 on it, with no row of it on either. Each pull
// request's figure holds the key once beside its own run's key, counted at
// USD 0.5, and the figure stands behind each one's rows. A key attested for a
// pull request of the same number in another repository counts on neither.
func TestAKeyNamingTwoPullRequestsCountsOnEach(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	own, other, shared, gadgets := minted(rowID(41), "1"), naming(rowID(42), "2", "13"), naming(rowID(43), "3", prA, "13"), naming(rowID(44), "4", prA, "13")
	gadgets.Repository = "github.com/acme/gadgets"
	for _, m := range []store.MintedCredential{own, other, shared, gadgets} {
		if err := s.Attest(ctx, a, m); err != nil {
			t.Fatal(err)
		}
	}
	otherRow := runRow(rowID(2), "0.2", other.KeySHA256, "2", "1")
	otherRow.Row.Tags[store.TagPR] = "13"
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(1), "0.1", own.KeySHA256, "1", "1"), otherRow)); err != nil {
		t.Fatal(err)
	}
	for m, figure := range map[store.MintedCredential]string{own: "0.5", other: "0.5", shared: "10", gadgets: "100"} {
		if err := s.AttestSpend(ctx, a, spent(m, figure)); err != nil {
			t.Fatal(err)
		}
	}
	for pr, wantRows := range map[string]int64{prA: 1, "13": 1} {
		rows, usd, err := s.GatewaySpend(ctx, a, repoA, pr)
		if err != nil {
			t.Fatal(err)
		}
		if rows != wantRows || usd.String() != "10.5" {
			t.Errorf("pull request %s: %d rows, USD %s, want %d rows, USD 10.5", pr, rows, usd, wantRows)
		}
	}
}

// TestAnAttestedKeyWithNoSpendKeepsABudgetFromPassing attests a second key for
// the pull request with no count beside it. The figure stands behind none of
// the pull request's rows until the count arrives, and then behind them all.
func TestAnAttestedKeyWithNoSpendKeepsABudgetFromPassing(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	own, second := minted(rowID(51), "1"), naming(rowID(52), "2", prA)
	for _, m := range []store.MintedCredential{own, second} {
		if err := s.Attest(ctx, a, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(1), "0.1", own.KeySHA256, "1", "1"))); err != nil {
		t.Fatal(err)
	}
	if err := s.AttestSpend(ctx, a, spent(own, "0.5")); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 0 || usd != "0.5" {
		t.Fatalf("with a key attested for the pull request and no count: %d rows, USD %s", rows, usd)
	}
	if err := s.AttestSpend(ctx, a, spent(second, "1")); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 1 || usd != "1.5" {
		t.Fatalf("once its count arrived: %d rows, USD %s", rows, usd)
	}
}

// TestAnAttestationNamingNoPullRequestReadsAsBefore stores an attestation and
// its count as a release before the pull requests an attestation names did,
// then migrates. The attestation names none, so its key counts on the pull
// request only through a row that names it, and a later attestation that
// names none reads the same.
func TestAnAttestationNamingNoPullRequestReadsAsBefore(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	if err := store.MigrateFrom(ctx, db.OwnerURL, migrationsBefore(t, pullsMigration)); err != nil {
		t.Fatal(err)
	}
	a := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	earlier := minted(rowID(61), "1")
	owner := connect(t, db.OwnerURL)
	for _, statement := range []struct {
		sql       string
		arguments []any
	}{
		{`SELECT set_config('costroid.tenant_id', $1, false)`, []any{a.String()}},
		{`INSERT INTO credential_attestations (tenant_id, key_sha256, repo, workflow_run_id, run_attempt, head_sha, attesting_run_id, attesting_run_attempt, attesting_ref)
			VALUES (costroid_tenant(), $1, $2, $3, $4, $5, $6, $7, $8)`, []any{earlier.KeySHA256, earlier.Repository, earlier.RunID, earlier.RunAttempt,
			earlier.HeadSHA, earlier.AttestingRunID, earlier.AttestingRunAttempt, earlier.AttestingRef}},
		{`INSERT INTO credential_spend (tenant_id, key_sha256, spend, attesting_repo, attesting_run_id, attesting_run_attempt)
			VALUES (costroid_tenant(), $1, '10', $2, '81', '1')`, []any{earlier.KeySHA256, earlier.Repository}},
	} {
		if _, err := owner.Exec(ctx, statement.sql, statement.arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx, db.OwnerURL); err != nil {
		t.Fatal(err)
	}

	s := open(t, db)
	if found, err := s.MintedCredentials(ctx, a, []string{earlier.KeySHA256}); err != nil || found[earlier.KeySHA256] != earlier {
		t.Fatalf("the earlier attestation reads %+v, %v", found, err)
	}
	later := minted(rowID(62), "2")
	if err := s.Attest(ctx, a, later); err != nil {
		t.Fatal(err)
	}
	if err := s.AttestSpend(ctx, a, spent(later, "0.5")); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 0 || usd != "0" {
		t.Fatalf("keys attested for no pull request, with no row on it: %d rows, USD %s", rows, usd)
	}
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(1), "0.1", earlier.KeySHA256, "1", "1"))); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 1 || usd != "10" {
		t.Fatalf("the earlier key's row arrived: %d rows, USD %s", rows, usd)
	}
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(2), "0.1", later.KeySHA256, "2", "1"))); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 2 || usd != "10.5" {
		t.Fatalf("both keys' rows arrived: %d rows, USD %s", rows, usd)
	}
}
