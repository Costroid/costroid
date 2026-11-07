package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// runRow is an exact row of run and attempt in repoA's pull request prA, naming
// key as its credential, as the service writes one.
func runRow(id, list, key, run, attempt string) store.Record {
	r := record(id, list, "exact", "token_verified")
	r.Row.SubAccountID = &key
	r.Row.Tags[store.TagWorkflowRunID] = run
	r.RunAttempt = attempt
	return r
}

func gatewaySpend(t *testing.T, s *store.Store, tenant store.TenantID) (int64, string) {
	t.Helper()
	rows, usd, err := s.GatewaySpend(context.Background(), tenant, repoA, prA)
	if err != nil {
		t.Fatal(err)
	}
	return rows, usd.String()
}

func TestSpendIsKeptForAnAttestedKey(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	first, second, elsewhere := rowID(501), rowID(502), rowID(503)
	for _, key := range []store.MintedCredential{minted(first, "1"), minted(second, "2"), minted(elsewhere, "3")} {
		if err := s.Attest(ctx, a, key); err != nil {
			t.Fatal(err)
		}
	}
	shared := record(rowID(5), "4", "windowed", "token_verified")
	shared.CredentialBasis = store.CredentialStated
	shared.Row.SubAccountID = &first
	_, err := s.Ingest(ctx, a, batch(
		runRow(rowID(1), "0.1", first, "1", "1"),
		runRow(rowID(2), "0.2", first, "1", "1"),
		runRow(rowID(3), "0.3", second, "2", "1"),
		runRow(rowID(4), "8", elsewhere, "1", "1"),
		shared,
	))
	if err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 0 || usd != "0" {
		t.Fatalf("before any attestation: %d rows, USD %s", rows, usd)
	}

	for _, figure := range []string{"0.5", "0.25", "0.000000000001"} {
		if err := s.AttestSpend(ctx, a, minted(first, "1"), decimal.MustParse(figure)); err != nil {
			t.Fatalf("USD %s: %v", figure, err)
		}
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 2 || usd != "0.5" {
		t.Fatalf("one key's count, the largest figure attested: %d rows, USD %s", rows, usd)
	}
	if err := s.AttestSpend(ctx, a, minted(second, "2"), decimal.MustParse("0.700000000001")); err != nil {
		t.Fatal(err)
	}
	if err := s.AttestSpend(ctx, a, minted(elsewhere, "3"), decimal.MustParse("90")); err != nil {
		t.Fatal(err)
	}
	// The row naming elsewhere's key belongs to run 1, which that key was not
	// minted for, and the shared row only states first's key. Neither counts,
	// and elsewhere's figure stays out of the sum.
	if rows, usd := gatewaySpend(t, s, a); rows != 3 || usd != "1.200000000001" {
		t.Fatalf("two keys, each once: %d rows, USD %s", rows, usd)
	}
	if rows, usd := gatewaySpend(t, s, b); rows != 0 || usd != "0" {
		t.Fatalf("another tenant read %d rows, USD %s", rows, usd)
	}

	conn := connect(t, db.AppURL)
	for _, statement := range []string{
		`UPDATE gateway_spend SET key_sha256 = '` + rowID(9) + `'`,
		`UPDATE gateway_spend SET tenant_id = gen_random_uuid()`,
		`DELETE FROM gateway_spend`,
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
}

func TestSpendForAnotherRunRefused(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, b := tenants(t, db)
	key := rowID(601)
	if err := s.Attest(ctx, a, minted(key, "1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(1), "0.1", key, "1", "1"))); err != nil {
		t.Fatal(err)
	}
	another := func(change func(*store.MintedCredential)) store.MintedCredential {
		stated := minted(key, "1")
		change(&stated)
		return stated
	}
	usd := decimal.MustParse("0.5")
	for name, tc := range map[string]struct {
		tenant store.TenantID
		key    store.MintedCredential
		want   error
	}{
		"another run":        {a, another(func(m *store.MintedCredential) { m.RunID = "2" }), store.ErrAttestedElsewhere},
		"another attempt":    {a, another(func(m *store.MintedCredential) { m.RunAttempt = "2" }), store.ErrAttestedElsewhere},
		"another repository": {a, another(func(m *store.MintedCredential) { m.Repository = "github.com/acme/other" }), store.ErrAttestedElsewhere},
		"a key never minted": {a, minted(rowID(602), "1"), store.ErrNotMinted},
		"another tenant":     {b, minted(key, "1"), store.ErrNotMinted},
	} {
		if err := s.AttestSpend(ctx, tc.tenant, tc.key, usd); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if err := s.AttestSpend(ctx, a, minted(key, "1"), decimal.Decimal{}.Sub(usd)); err == nil {
		t.Error("a negative figure was kept")
	}
	if rows, figure := gatewaySpend(t, s, a); rows != 0 || figure != "0" {
		t.Fatalf("a refused spend was kept: %d rows, USD %s", rows, figure)
	}
	var kept int
	if err := connect(t, db.SuperURL).QueryRow(ctx, `SELECT count(*) FROM gateway_spend`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Fatalf("gateway_spend holds %d rows", kept)
	}
}

func TestTheDatabaseKeepsSpendOnlyForAMintedKey(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, _ := tenants(t, db)
	conn := connect(t, db.AppURL)
	err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO gateway_spend (tenant_id, key_sha256, usd) VALUES (costroid_tenant(), $1, 1)`, rowID(701))
		return err
	})
	requireSQLState(t, err, foreignKeyViolation)
	if err := open(t, db).Attest(ctx, a, minted(rowID(701), "1")); err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO gateway_spend (tenant_id, key_sha256, usd) VALUES (costroid_tenant(), $1, -1)`, rowID(701))
		return err
	})
	requireSQLState(t, err, checkViolation)
}

const foreignKeyViolation = "23503"
