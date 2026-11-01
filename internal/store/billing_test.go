package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/store/storetest"
)

func TestABillingAccountPastTheContractsLimitIsRefusedByTheDatabase(t *testing.T) {
	db := storetest.New(t)
	a, _ := tenants(t, db)
	r := record(rowID(1), "1", "unattributed", "unverified")
	r.Row.BillingAccountID = strings.Repeat("a", 513)
	_, err := open(t, db).Ingest(context.Background(), a, batch(r))
	requireSQLState(t, err, checkViolation)
}
