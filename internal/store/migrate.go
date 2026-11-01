package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/migrations"
)

// migrationLock keeps two instances from migrating at once.
const migrationLock = 7061823

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// ErrMigrationEdited means a migration already applied has changed on disk.
var ErrMigrationEdited = errors.New("store: an applied migration was edited")

// ErrDatabaseAhead means the database holds a version this binary does not
// know. Migrations run forward only, so the binary is what has to change.
var ErrDatabaseAhead = errors.New("store: database is ahead of this binary")

type migration struct {
	version int
	name    string
	sql     string
	sum     string
}

// Migrate applies the embedded migrations through the owner's connection.
func Migrate(ctx context.Context, ownerURL string) error {
	return MigrateFrom(ctx, ownerURL, migrations.Files)
}

// MigrateFrom applies the migrations in files, each in its own transaction.
func MigrateFrom(ctx context.Context, ownerURL string, files fs.FS) error {
	pending, err := readMigrations(files)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return fmt.Errorf("store: connect to migrate: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("store: take the migration lock: %w", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		sha256     text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	if err := checkHistory(pending, applied); err != nil {
		return err
	}
	for _, m := range pending {
		if _, done := applied[m.version]; done {
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func readMigrations(files fs.FS) ([]migration, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	slices.Sort(names)
	list := make([]migration, 0, len(names))
	for _, name := range names {
		match := migrationName.FindStringSubmatch(path.Base(name))
		if match == nil {
			return nil, fmt.Errorf("store: migration %q is not named NNNN_name.sql", name)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("store: migration %q: %w", name, err)
		}
		if len(list) > 0 && list[len(list)-1].version == version {
			return nil, fmt.Errorf("store: two migrations carry version %d", version)
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		list = append(list, migration{version: version, name: name, sql: string(data), sum: hex.EncodeToString(sum[:])})
	}
	return list, nil
}

func appliedMigrations(ctx context.Context, conn *pgx.Conn) (map[int]string, error) {
	rows, err := conn.Query(ctx, `SELECT version, sha256 FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	applied := map[int]string{}
	var version int
	var sum string
	_, err = pgx.ForEachRow(rows, []any{&version, &sum}, func() error {
		applied[version] = sum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return applied, nil
}

func checkHistory(known []migration, applied map[int]string) error {
	byVersion := make(map[int]migration, len(known))
	for _, m := range known {
		byVersion[m.version] = m
	}
	for version, sum := range applied {
		m, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: version %d", ErrDatabaseAhead, version)
		}
		if m.sum != sum {
			return fmt.Errorf("%w: %s", ErrMigrationEdited, m.name)
		}
	}
	return nil
}

func apply(ctx context.Context, conn *pgx.Conn, m migration) error {
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, sha256) VALUES ($1, $2, $3)`, m.version, m.name, m.sum)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: apply %s: %w", m.name, err)
	}
	return nil
}
