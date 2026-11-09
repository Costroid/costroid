package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

var (
	widgets = store.Repository{ID: 101, FullName: "example/widgets"}
	gadgets = store.Repository{ID: 102, FullName: "example/gadgets"}
)

func TestAnInstallationIsKeptRenamedSuspendedAndDeleted(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	if _, err := s.Installation(ctx, 58); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an empty table answered %v", err)
	}
	if err := s.AddRepositories(ctx, 58, "example", []store.Repository{gadgets, widgets}); err != nil {
		t.Fatal(err)
	}
	renamed := store.Repository{ID: widgets.ID, FullName: "example/widgets-v2"}
	if err := s.AddRepositories(ctx, 58, "example-org", []store.Repository{renamed}); err != nil {
		t.Fatal(err)
	}
	if err := s.SuspendInstallation(ctx, 58, "example-org", true); err != nil {
		t.Fatal(err)
	}
	held, err := s.Installation(ctx, 58)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Installation{ID: 58, Account: "example-org", Suspended: true, Repositories: []store.Repository{renamed, gadgets}}
	if held.ID != want.ID || held.Account != want.Account || !held.Suspended || !slices.Equal(held.Repositories, want.Repositories) {
		t.Fatalf("held %+v, want %+v", held, want)
	}
	if err := s.RemoveRepositories(ctx, 58, []int64{gadgets.ID, 999}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRepositories(ctx, 58, "example-org", nil); err != nil {
		t.Fatal(err)
	}
	if held, err = s.Installation(ctx, 58); err != nil || !held.Suspended || !slices.Equal(held.Repositories, []store.Repository{renamed}) {
		t.Fatalf("held %+v, %v: adding no repositories changed the suspension or the list", held, err)
	}
	if err := s.DeleteInstallation(ctx, 58); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Installation(ctx, 58); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a deleted installation answered %v", err)
	}
	var left int
	if err := connect(t, db.SuperURL).QueryRow(ctx, `SELECT count(*) FROM app_installation_repositories`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d repositories outlived their installation: %v", left, err)
	}
}

func TestTheInstallationTablesHoldIdsAndNamesOnly(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	conn := connect(t, db.SuperURL)
	for table, want := range map[string][]string{
		"app_installations":             {"installation_id", "account", "suspended"},
		"app_installation_repositories": {"installation_id", "repository_id", "full_name"},
	} {
		rows, err := conn.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_name = $1 ORDER BY ordinal_position`, table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(columns, want) {
			t.Errorf("%s holds %v", table, columns)
		}
	}
}

func TestTheDatabaseRefusesAnInstallationNameOutsideItsForm(t *testing.T) {
	db := storetest.New(t)
	s := open(t, db)
	ctx := context.Background()
	requireSQLState(t, s.AddRepositories(ctx, 58, "<script>", nil), checkViolation)
	requireSQLState(t, s.AddRepositories(ctx, 58, "example", []store.Repository{{ID: 1, FullName: "no-slash"}}), checkViolation)
	requireSQLState(t, s.AddRepositories(ctx, 0, "example", nil), checkViolation)
}

func TestTheAppRoleCannotRenumberAnInstallation(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if err := open(t, db).AddRepositories(ctx, 58, "example", []store.Repository{widgets}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, db.AppURL)
	_, err := conn.Exec(ctx, `UPDATE app_installations SET installation_id = 59`)
	requireSQLState(t, err, insufficientPrivilege)
	_, err = conn.Exec(ctx, `UPDATE app_installation_repositories SET repository_id = 7`)
	requireSQLState(t, err, insufficientPrivilege)
}
