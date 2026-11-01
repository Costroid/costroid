package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/store/storetest"
)

func TestEgressCountersAddUpAndReadNewestFirst(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	first := []egress.Count{
		{Day: "2026-11-01", Host: "gitlab.com", Outcome: egress.Refused, Count: 2},
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Refused, Count: 1},
	}
	second := []egress.Count{
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Refused, Count: 4},
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Completed, Count: 7},
	}
	for _, counts := range [][]egress.Count{first, second} {
		if err := s.AddEgress(ctx, counts); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Egress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []egress.Count{
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Completed, Count: 7},
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Refused, Count: 5},
		{Day: "2026-11-01", Host: "gitlab.com", Outcome: egress.Refused, Count: 2},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestTheAppRoleCanOnlyCountEgress(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if err := open(t, db).AddEgress(ctx, []egress.Count{{Day: "2026-11-01", Host: "gitlab.com", Outcome: egress.Refused, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	_, err := conn.Exec(ctx, `DELETE FROM egress_counters`)
	requireSQLState(t, err, insufficientPrivilege)
}

func TestTheEgressTableHoldsDayHostOutcomeAndCountOnly(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	rows, err := connect(t, db.SuperURL).Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'egress_counters' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(columns, []string{"day", "host", "outcome", "count"}) {
		t.Fatalf("%v", columns)
	}
}

func TestTheDatabaseRefusesAnOutcomeOutsideTheThree(t *testing.T) {
	db := storetest.New(t)
	err := open(t, db).AddEgress(context.Background(), []egress.Count{{Day: "2026-11-01", Host: "gitlab.com", Outcome: "/a/path", Count: 1}})
	requireSQLState(t, err, checkViolation)
}
