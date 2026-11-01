package store_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
	"github.com/Costroid/costroid/migrations"
)

func TestEveryMigrationIsForwardOnlyByName(t *testing.T) {
	names, err := fs.Glob(migrations.Files, "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations embedded")
	}
	for _, name := range names {
		if strings.Contains(name, "down") || strings.Contains(name, "rollback") || strings.Contains(name, "undo") {
			t.Errorf("%s reads as a rollback", name)
		}
	}
}

func oneMigration() fstest.MapFS {
	return fstest.MapFS{"0001_first.sql": {Data: []byte(`CREATE TABLE first_table (id integer)`)}}
}

func TestMigrateAppliesOnceAndIsANoOpAfter(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	for range 2 {
		if err := store.MigrateFrom(ctx, db.OwnerURL, oneMigration()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateMovesForwardFromAnOlderVersionKeepingItsData(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	files := oneMigration()
	if err := store.MigrateFrom(ctx, db.OwnerURL, files); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `INSERT INTO first_table VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	files["0002_second.sql"] = &fstest.MapFile{Data: []byte(`ALTER TABLE first_table ADD COLUMN note text DEFAULT 'kept'`)}
	if err := store.MigrateFrom(ctx, db.OwnerURL, files); err != nil {
		t.Fatal(err)
	}
	var id int
	var note string
	if err := conn.QueryRow(ctx, `SELECT id, note FROM first_table`).Scan(&id, &note); err != nil {
		t.Fatal(err)
	}
	if id != 7 || note != "kept" {
		t.Fatalf("got %d %q", id, note)
	}
}

func TestMigrateRefusesAnEditedMigration(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	if err := store.MigrateFrom(ctx, db.OwnerURL, oneMigration()); err != nil {
		t.Fatal(err)
	}
	edited := fstest.MapFS{"0001_first.sql": {Data: []byte(`CREATE TABLE first_table (id bigint)`)}}
	if err := store.MigrateFrom(ctx, db.OwnerURL, edited); !errors.Is(err, store.ErrMigrationEdited) {
		t.Fatalf("got %v", err)
	}
}

func TestMigrateRefusesADatabaseAheadOfTheBinary(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	files := oneMigration()
	files["0002_second.sql"] = &fstest.MapFile{Data: []byte(`SELECT 1`)}
	if err := store.MigrateFrom(ctx, db.OwnerURL, files); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateFrom(ctx, db.OwnerURL, oneMigration()); !errors.Is(err, store.ErrDatabaseAhead) {
		t.Fatalf("got %v", err)
	}
}

func TestAFailedMigrationLeavesNothingBehind(t *testing.T) {
	db := storetest.Empty(t)
	ctx := context.Background()
	files := fstest.MapFS{"0001_bad.sql": {Data: []byte(`CREATE TABLE half (id integer); SELECT missing_column FROM half`)}}
	if err := store.MigrateFrom(ctx, db.OwnerURL, files); err == nil {
		t.Fatal("a failing migration passed")
	}
	conn, err := pgx.Connect(ctx, db.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var found *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('half')::text`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatal("the failed migration's table survived")
	}
}

func TestAMigrationNamedOutOfPatternIsRefused(t *testing.T) {
	db := storetest.Empty(t)
	files := fstest.MapFS{"0001_first.down.sql": {Data: []byte(`DROP TABLE first_table`)}}
	if err := store.MigrateFrom(context.Background(), db.OwnerURL, files); err == nil {
		t.Fatal("a down migration was accepted")
	}
}
