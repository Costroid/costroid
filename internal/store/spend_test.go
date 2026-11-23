package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

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

// spent is a figure for key's attestation, from a closing run numbered after
// the run, so each run's key has a closing run of its own.
func spent(key store.MintedCredential, figure string) store.AttestedSpend {
	return store.AttestedSpend{Key: key, Spend: figure, AttestingRunID: "8" + key.RunID, AttestingRunAttempt: "1"}
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

	if err := s.AttestSpend(ctx, a, spent(minted(first, "1"), "0.5")); err != nil {
		t.Fatal(err)
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 2 || usd != "0.5" {
		t.Fatalf("one key's count: %d rows, USD %s", rows, usd)
	}
	if err := s.AttestSpend(ctx, a, spent(minted(second, "2"), "0.700000000001")); err != nil {
		t.Fatal(err)
	}
	if err := s.AttestSpend(ctx, a, spent(minted(elsewhere, "3"), "90")); err != nil {
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
		`UPDATE credential_spend SET spend = '9'`,
		`UPDATE credential_spend SET tenant_id = gen_random_uuid()`,
		`DELETE FROM credential_spend`,
	} {
		err := inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement)
			return err
		})
		requireSQLState(t, err, insufficientPrivilege)
	}
}

// TestASpendIsWrittenOnceAndNeverRaised holds a key to its first figure and an
// attesting run to one write. The same string again from the same run is no
// error; another string from it, the same number written otherwise included,
// is ErrAttestingRunUsed, and any figure from another run ErrSpendHeld.
func TestASpendIsWrittenOnceAndNeverRaised(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	a, _ := tenants(t, db)
	key, another := minted(rowID(801), "1"), minted(rowID(802), "2")
	for _, m := range []store.MintedCredential{key, another} {
		if err := s.Attest(ctx, a, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Ingest(ctx, a, batch(runRow(rowID(1), "0.1", key.KeySHA256, "1", "1"))); err != nil {
		t.Fatal(err)
	}
	first := spent(key, "0.5")
	for _, attested := range []store.AttestedSpend{first, first} {
		if err := s.AttestSpend(ctx, a, attested); err != nil {
			t.Fatal(err)
		}
	}
	fromAnother := spent(key, "6")
	fromAnother.AttestingRunID = "77"
	anotherKey := spent(another, "0.1")
	anotherKey.AttestingRunID = first.AttestingRunID
	for name, tc := range map[string]struct {
		attested store.AttestedSpend
		want     error
	}{
		"the same number written otherwise": {spent(key, "0.50"), store.ErrAttestingRunUsed},
		"a larger figure from the same run": {spent(key, "6"), store.ErrAttestingRunUsed},
		"another key's figure from the run": {anotherKey, store.ErrAttestingRunUsed},
		"a larger figure from another run":  {fromAnother, store.ErrSpendHeld},
	} {
		if err := s.AttestSpend(ctx, a, tc.attested); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if rows, usd := gatewaySpend(t, s, a); rows != 1 || usd != "0.5" {
		t.Fatalf("the key's count after refused writes: %d rows, USD %s", rows, usd)
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
	for name, tc := range map[string]struct {
		tenant store.TenantID
		key    store.MintedCredential
	}{
		"another run":        {a, another(func(m *store.MintedCredential) { m.RunID = "2" })},
		"another attempt":    {a, another(func(m *store.MintedCredential) { m.RunAttempt = "2" })},
		"another repository": {a, another(func(m *store.MintedCredential) { m.Repository = "github.com/acme/other" })},
		"a key never minted": {a, minted(rowID(602), "1")},
		"another tenant":     {b, minted(key, "1")},
	} {
		if err := s.AttestSpend(ctx, tc.tenant, spent(tc.key, "0.5")); !errors.Is(err, store.ErrNotMinted) {
			t.Errorf("%s: %v, want %v", name, err, store.ErrNotMinted)
		}
	}
	if err := s.AttestSpend(ctx, a, spent(minted(key, "1"), "-0.5")); err == nil {
		t.Error("a negative figure was kept")
	}
	if rows, figure := gatewaySpend(t, s, a); rows != 0 || figure != "0" {
		t.Fatalf("a refused spend was kept: %d rows, USD %s", rows, figure)
	}
	var kept int
	if err := connect(t, db.SuperURL).QueryRow(ctx, `SELECT count(*) FROM credential_spend`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Fatalf("credential_spend holds %d rows", kept)
	}
}

func TestTheDatabaseKeepsSpendOnlyForAMintedKey(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, _ := tenants(t, db)
	conn := connect(t, db.AppURL)
	insert := `INSERT INTO credential_spend (tenant_id, key_sha256, spend, attesting_repo, attesting_run_id, attesting_run_attempt)
		VALUES (costroid_tenant(), $1, $2, 'github.com/acme/widgets', $3, '1')`
	err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insert, rowID(701), "1", "81")
		return err
	})
	requireSQLState(t, err, foreignKeyViolation)
	if err := open(t, db).Attest(ctx, a, minted(rowID(701), "1")); err != nil {
		t.Fatal(err)
	}
	for figure, state := range map[string]string{"-1": checkViolation, "1e5": checkViolation, "0.1000000000000": checkViolation} {
		err = inTenant(t, conn, a, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, insert, rowID(701), figure, "81")
			return err
		})
		requireSQLState(t, err, state)
	}
	if err := inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insert, rowID(701), "1", "81")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = inTenant(t, conn, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insert, rowID(701), "2", "82")
		return err
	})
	requireSQLState(t, err, uniqueViolation)
}

const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
)
