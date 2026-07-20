// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/duckdb/duckdb-go/v2"
)

// Artifact and backup suffixes beside DatabaseFile during offline conversion.
const (
	convertTmpSuffix = ".convert-tmp"
	bakSuffix        = ".bak"
	spillDirSuffix   = ".spill-tmp"
)

// ErrStoreNotEncrypted is returned when the source is plaintext but a current
// key was supplied (verb-neutral; the CLI adds direction advice).
var ErrStoreNotEncrypted = errors.New("the store in the data directory is not encrypted")

// ErrStoreAlreadyEncrypted is returned when the source is encrypted but no
// current key was supplied (verb-neutral; the CLI adds direction advice).
var ErrStoreAlreadyEncrypted = errors.New("the store is already encrypted")

// EncryptionChange reports what ChangeEncryption verified and where the
// pre-conversion store was retained.
type EncryptionChange struct {
	// Tables is the number of tables copied and verified (all tables,
	// including schema_migrations).
	Tables int
	// Rows is the total row count verified across those tables.
	Rows int64
	// BackupPath is where the pre-conversion store now lives
	// (costroid.duckdb.bak under dataDir).
	BackupPath string
	// Encrypted is the target's final at-rest state.
	Encrypted bool
}

// escapeSQLString doubles single quotes for safe SQL string literals. Shared
// by openEncrypted and ChangeEncryption so path/key escaping stays one place.
func escapeSQLString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// quoteIdent double-quotes a SQL identifier, doubling embedded quotes.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ChangeEncryption converts the store in dataDir between at-rest encryption
// states, offline, by copying it into a freshly written database file and
// swapping it into place. currentKey "" means the source is plaintext; newKey
// "" means the target is plaintext.
func ChangeEncryption(ctx context.Context, dataDir, currentKey, newKey string) (result EncryptionChange, err error) {
	if currentKey == "" && newKey == "" {
		return EncryptionChange{}, errors.New("nothing to convert")
	}
	if currentKey != "" && newKey != "" && currentKey == newKey {
		return EncryptionChange{}, errors.New("the current and new db-encryption keys are identical - nothing to convert")
	}

	livePath := filepath.Join(dataDir, DatabaseFile)
	bakPath := livePath + bakSuffix
	tmpPath := livePath + convertTmpSuffix
	tmpWalPath := tmpPath + ".wal"
	bakWalPath := bakPath + ".wal"
	liveWalPath := livePath + ".wal"
	spillDir := livePath + spillDirSuffix

	liveInfo, liveErr := os.Stat(livePath)
	livePresent := liveErr == nil && !liveInfo.IsDir()
	if liveErr != nil && !os.IsNotExist(liveErr) {
		return EncryptionChange{}, fmt.Errorf("stat %s: %w", livePath, liveErr)
	}
	bakPresent := fileExists(bakPath)
	tmpPresent := fileExists(tmpPath) || fileExists(tmpWalPath)

	// Preflight: first match wins.
	switch {
	case !livePresent && bakPresent:
		msg := fmt.Sprintf(
			"interrupted store conversion in %s: the live database is missing but %s is present. "+
				"Restore it with `mv %s %s`",
			dataDir, bakPath, bakPath, livePath)
		if fileExists(bakWalPath) {
			msg += fmt.Sprintf(" and `mv %s %s`", bakWalPath, liveWalPath)
		}
		msg += fmt.Sprintf(
			", then remove any leftover %s* artifacts if present and re-run the conversion "+
				"(the temp target is never trusted after a crash)",
			tmpPath)
		return EncryptionChange{}, errors.New(msg)
	case !livePresent:
		return EncryptionChange{}, fmt.Errorf("no Costroid database exists in %s", dataDir)
	case tmpPresent:
		return EncryptionChange{}, fmt.Errorf(
			"previous store conversion in %s left temporary artifacts (%s and/or %s); "+
				"they are safe to delete (the live store is intact) - remove them and re-run",
			dataDir, tmpPath, tmpWalPath)
	case pathExists(spillDir):
		// Stale engine spill only; never refuse. Best-effort remove and continue.
		_ = os.RemoveAll(spillDir)
	}
	if bakPresent {
		msg := fmt.Sprintf(
			"a previous conversion backup still exists at %s", bakPath)
		if fileExists(bakWalPath) {
			msg += fmt.Sprintf(" (and %s)", bakWalPath)
		}
		msg += " - move or remove it before converting again " +
			"(it may be a PLAINTEXT copy of your data; never overwrite a backup)"
		return EncryptionChange{}, errors.New(msg)
	}

	// Header sniff for direction validation before any driver error.
	flag, sniffErr := sniffDuckDBEncryptionFlag(livePath)
	if sniffErr != nil {
		return EncryptionChange{}, sniffErr
	}
	switch flag {
	case 0x40: // plaintext
		if currentKey != "" {
			return EncryptionChange{}, ErrStoreNotEncrypted
		}
	case 0x44: // encrypted
		if currentKey == "" {
			return EncryptionChange{}, ErrStoreAlreadyEncrypted
		}
	}

	// Failure cleanup for temp artifacts created this run (never .bak).
	createdTemp := false
	defer func() {
		if err != nil && createdTemp {
			_ = os.Remove(tmpPath)
			_ = os.Remove(tmpWalPath)
			_ = os.RemoveAll(spillDir)
		}
	}()

	connector, connErr := duckdb.NewConnector("", nil)
	if connErr != nil {
		return EncryptionChange{}, convertError(connErr, dataDir)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	conn, connErr := db.Conn(ctx)
	if connErr != nil {
		return EncryptionChange{}, convertError(connErr, dataDir)
	}
	defer func() { _ = conn.Close() }()

	exec := func(statement string) error {
		if _, e := conn.ExecContext(ctx, statement); e != nil {
			return convertError(e, dataDir)
		}
		return nil
	}

	// SET on this single pinned connection so spill is encrypted and local.
	if err = exec("SET temp_file_encryption=true"); err != nil {
		return EncryptionChange{}, err
	}
	if err = exec(fmt.Sprintf("SET temp_directory='%s'", escapeSQLString(spillDir))); err != nil {
		return EncryptionChange{}, err
	}

	// Source READ_ONLY: WAL is replayed in memory; source files stay byte-identical.
	var srcAttach string
	if currentKey != "" {
		srcAttach = fmt.Sprintf(
			"ATTACH '%s' AS src (ENCRYPTION_KEY '%s', READ_ONLY)",
			escapeSQLString(livePath), escapeSQLString(currentKey))
	} else {
		srcAttach = fmt.Sprintf("ATTACH '%s' AS src (READ_ONLY)", escapeSQLString(livePath))
	}
	if err = exec(srcAttach); err != nil {
		return EncryptionChange{}, err
	}

	// Fresh target: ATTACH creates the file. Mark for cleanup before ATTACH so a
	// partial create is still removed.
	createdTemp = true
	var dstAttach string
	if newKey != "" {
		dstAttach = fmt.Sprintf(
			"ATTACH '%s' AS dst (ENCRYPTION_KEY '%s')",
			escapeSQLString(tmpPath), escapeSQLString(newKey))
	} else {
		dstAttach = fmt.Sprintf("ATTACH '%s' AS dst", escapeSQLString(tmpPath))
	}
	if err = exec(dstAttach); err != nil {
		return EncryptionChange{}, err
	}

	if err = exec("COPY FROM DATABASE src TO dst"); err != nil {
		return EncryptionChange{}, err
	}

	// Verification before any swap.
	srcTables, err := listDBTables(ctx, conn, dataDir, "src")
	if err != nil {
		return EncryptionChange{}, err
	}
	dstTables, err := listDBTables(ctx, conn, dataDir, "dst")
	if err != nil {
		return EncryptionChange{}, err
	}
	if !sameStringSet(srcTables, dstTables) {
		return EncryptionChange{}, fmt.Errorf(
			"converting the Costroid database in %s failed: table set mismatch between source and target",
			dataDir)
	}

	var totalRows int64
	for _, table := range srcTables {
		q := quoteIdent(table)
		var srcCount, dstCount int64
		if err = conn.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM src.%s", q)).Scan(&srcCount); err != nil {
			return EncryptionChange{}, convertError(err, dataDir)
		}
		if err = conn.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM dst.%s", q)).Scan(&dstCount); err != nil {
			return EncryptionChange{}, convertError(err, dataDir)
		}
		if srcCount != dstCount {
			return EncryptionChange{}, fmt.Errorf(
				"converting the Costroid database in %s failed: row count mismatch for table %s",
				dataDir, table)
		}
		var diff int64
		diffSQL := fmt.Sprintf(
			`SELECT count(*) FROM (
				(SELECT * FROM src.%[1]s EXCEPT ALL SELECT * FROM dst.%[1]s)
				UNION ALL
				(SELECT * FROM dst.%[1]s EXCEPT ALL SELECT * FROM src.%[1]s)
			)`, q)
		if err = conn.QueryRowContext(ctx, diffSQL).Scan(&diff); err != nil {
			return EncryptionChange{}, convertError(err, dataDir)
		}
		if diff != 0 {
			return EncryptionChange{}, fmt.Errorf(
				"converting the Costroid database in %s failed: content mismatch for table %s",
				dataDir, table)
		}
		totalRows += srcCount
	}

	wantEncrypted := newKey != ""
	var dstEncrypted bool
	if err = conn.QueryRowContext(ctx,
		`SELECT encrypted FROM duckdb_databases() WHERE database_name = 'dst'`).Scan(&dstEncrypted); err != nil {
		return EncryptionChange{}, convertError(err, dataDir)
	}
	if dstEncrypted != wantEncrypted {
		return EncryptionChange{}, fmt.Errorf(
			"converting the Costroid database in %s failed: target encryption state mismatch",
			dataDir)
	}

	// Detach and close before the durable rename swap.
	if err = exec("DETACH dst"); err != nil {
		return EncryptionChange{}, err
	}
	if err = exec("DETACH src"); err != nil {
		return EncryptionChange{}, err
	}
	if err = conn.Close(); err != nil {
		return EncryptionChange{}, convertError(err, dataDir)
	}
	if err = db.Close(); err != nil {
		return EncryptionChange{}, convertError(err, dataDir)
	}

	if err = fsyncPath(tmpPath); err != nil {
		return EncryptionChange{}, fmt.Errorf("fsync temp store %s: %w", tmpPath, err)
	}
	if err = fsyncDir(dataDir); err != nil {
		return EncryptionChange{}, fmt.Errorf("fsync data directory %s: %w", dataDir, err)
	}

	// Swap: live -> bak, optional live.wal -> bak.wal, then convert-tmp -> live.
	if err = os.Rename(livePath, bakPath); err != nil {
		return EncryptionChange{}, fmt.Errorf("renaming live store to backup %s: %w", bakPath, err)
	}
	if fileExists(liveWalPath) {
		if err = os.Rename(liveWalPath, bakWalPath); err != nil {
			return EncryptionChange{}, fmt.Errorf("renaming live WAL to backup %s: %w", bakWalPath, err)
		}
	}
	if err = os.Rename(tmpPath, livePath); err != nil {
		return EncryptionChange{}, fmt.Errorf("installing converted store at %s: %w", livePath, err)
	}
	// After a successful install, leftover convert-tmp.wal (if any) is no longer
	// the live name; drop it so dataDir stays artifact-free.
	_ = os.Remove(tmpWalPath)
	if err = fsyncDir(dataDir); err != nil {
		return EncryptionChange{}, fmt.Errorf("fsync data directory %s after swap: %w", dataDir, err)
	}
	_ = os.RemoveAll(spillDir)

	// Success: suppress failure cleanup (temp is already renamed away).
	createdTemp = false
	return EncryptionChange{
		Tables:     len(srcTables),
		Rows:       totalRows,
		BackupPath: bakPath,
		Encrypted:  wantEncrypted,
	}, nil
}

// sniffDuckDBEncryptionFlag reads the DuckDB file header. Returns the flag byte
// at offset 12, or an error if the file is not a Costroid/DuckDB database.
// When the flag is neither 0x40 nor 0x44, the caller proceeds and lets the
// engine decide (future-proofing).
func sniffDuckDBEncryptionFlag(path string) (byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("reading database header %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var hdr [13]byte
	n, err := f.Read(hdr[:])
	if err != nil || n < 13 {
		return 0, fmt.Errorf("%s is not a Costroid database file", path)
	}
	if string(hdr[8:12]) != "DUCK" {
		return 0, fmt.Errorf("%s is not a Costroid database file", path)
	}
	return hdr[12], nil
}

func listDBTables(ctx context.Context, conn *sql.Conn, dataDir, database string) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT table_name FROM duckdb_tables() WHERE database_name = ? ORDER BY table_name`,
		database)
	if err != nil {
		return nil, convertError(err, dataDir)
	}
	defer func() { _ = rows.Close() }()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, convertError(err, dataDir)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, convertError(err, dataDir)
	}
	return tables, nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Both are ORDER BY table_name from the same catalog shape.
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// convertError classifies driver failures during conversion. On any path where
// an encryption key may appear in SQL, never wrap or embed the raw driver text
// (DuckDB echoes ENCRYPTION_KEY literals in parse errors).
func convertError(err error, dataDir string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Wrong encryption key"):
		return fmt.Errorf("the Costroid database in %s is encrypted and the provided key is wrong; "+
			"check --db-encryption-key-file / $COSTROID_DB_ENCRYPTION_KEY_FILE", dataDir)
	case strings.Contains(msg, "Cannot open encrypted database"):
		return fmt.Errorf("the Costroid database in %s is encrypted; provide the key via "+
			"--db-encryption-key-file or $COSTROID_DB_ENCRYPTION_KEY_FILE", dataDir)
	case strings.Contains(msg, "Could not set lock on file"):
		return fmt.Errorf("the Costroid database in %s is in use by another process - "+
			"the embedded store allows a single process at a time, so stop the other "+
			"costroid process (e.g. `costroid serve`) before running this command", dataDir)
	default:
		return fmt.Errorf("converting the Costroid database in %s failed", dataDir)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fsyncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
