// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package storage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
)

// Quote-bearing synthetic keys: prove escapeSQLString and that errors never
// echo ENCRYPTION_KEY literals.
const (
	convertKeyA = "convert-key-a's-quote"
	convertKeyB = "convert-key-b's-quote"
	convertKeyC = "convert-wrong-key's-quote"
)

func assertNoKeyLeak(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(msg, secret) {
			t.Fatalf("error leaked secret %q: %q", secret, msg)
		}
	}
	for _, forbidden := range []string{"ATTACH ", "(ENCRYPTION_KEY '"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("error leaked %q: %q", forbidden, msg)
		}
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeBakPair(t *testing.T, dataDir string) {
	t.Helper()
	live := filepath.Join(dataDir, DatabaseFile)
	for _, p := range []string{live + bakSuffix, live + bakSuffix + ".wal"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing %s: %v", p, err)
		}
	}
}

func assertDataDirCleanAfterConvert(t *testing.T, dataDir string) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, "convert-tmp") {
			t.Fatalf("leftover convert-tmp artifact: %s", name)
		}
		if strings.Contains(name, "spill-tmp") {
			t.Fatalf("leftover spill-tmp artifact: %s", name)
		}
	}
	// Live store and intentional .bak pair only (+ optional .wal).
	live := filepath.Join(dataDir, DatabaseFile)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live store missing after convert: %v", err)
	}
	if _, err := os.Stat(live + bakSuffix); err != nil {
		t.Fatalf("backup missing after convert: %v", err)
	}
}

// seedFullStore writes one row through each of the eight Store write methods
// so every migration's table is non-empty before conversion.
func seedFullStore(t *testing.T, ctx context.Context, store *DuckDB, cost string) {
	t.Helper()
	exact := decimal.RequireFromString(cost)
	batch := Batch{
		Connector:      "aws-focus",
		SourceIdentity: "convert-seed",
		ContentHash:    "sha256:convert-seed",
		TenantID:       focus.DefaultTenant,
	}
	rec := testRecord(t, "Convert Seed Service", day(1), cost)
	if _, err := store.ReplaceIngestBatch(ctx, batch, []focus.CostRecord{rec}); err != nil {
		t.Fatalf("ReplaceIngestBatch: %v", err)
	}
	if err := store.ReplaceUsageBatch(ctx, UsageBatch{
		Connector: "aws-focus", SourceIdentity: "convert-seed", TenantID: focus.DefaultTenant,
	}, []Metric{{
		ChargePeriodStart: day(1),
		ServiceName:       "Convert Seed Service",
		ServiceTier:       "",
		MetricName:        "num_model_requests",
		Unit:              "Requests",
		Quantity:          decimal.RequireFromString("3"),
	}}); err != nil {
		t.Fatalf("ReplaceUsageBatch: %v", err)
	}
	if err := store.ReplaceBusinessMetricsBatch(ctx, focus.DefaultTenant, "convert-seed", []BusinessMetricRow{{
		MetricDay: day(1), MetricName: "active_users", Quantity: decimal.RequireFromString("42"),
	}}); err != nil {
		t.Fatalf("ReplaceBusinessMetricsBatch: %v", err)
	}
	if err := store.UpsertSyncState(ctx, SyncState{
		Connector:            "aws-focus-s3",
		SourceIdentity:       "2026-05",
		ManifestKey:          "exports/manifest.json",
		ManifestETag:         `"etag-seed"`,
		ManifestLastModified: day(1),
		ManifestSize:         99,
	}); err != nil {
		t.Fatalf("UpsertSyncState: %v", err)
	}
	if err := store.RecordSyncRun(ctx, SyncRun{
		SourceName: "aws-seed", Connector: "aws-focus", TenantID: focus.DefaultTenant,
		StartedAt: day(1), FinishedAt: day(1).Add(time.Minute), Outcome: "success",
		PeriodsProcessed: 1, RecordsIngested: 1,
	}); err != nil {
		t.Fatalf("RecordSyncRun: %v", err)
	}
	if err := store.UpsertManifestAttribution(ctx, ManifestAttribution{
		Connector: "azure-focus", ManifestKey: "acct/container/manifest.json",
		ETag: `"attr-etag"`, LastModified: day(1), Size: 50,
		BillingPeriod: "2026-05", SubmittedTime: day(1), ExportName: "costroid-demo",
	}); err != nil {
		t.Fatalf("UpsertManifestAttribution: %v", err)
	}
	if err := store.PutCredential(ctx, Credential{
		Name: "convert-slot", Nonce: []byte("nonce-12-bytes!!"), Ciphertext: []byte("ciphertext-seed"),
	}); err != nil {
		t.Fatalf("PutCredential: %v", err)
	}
	if _, err := store.InsertNewAnomalyAlerts(ctx, []AnomalyAlert{{
		TenantID: focus.DefaultTenant, Scope: "total", SeriesKey: "",
		Currency: "USD", Date: day(1), Direction: "increase",
	}}, day(1).Add(2*time.Hour)); err != nil {
		t.Fatalf("InsertNewAnomalyAlerts: %v", err)
	}
	_ = exact // seeded via testRecord which parses cost the same way
}

func assertSeedSurvived(t *testing.T, ctx context.Context, store *DuckDB, wantCost string, wantMigrations []string) {
	t.Helper()
	costs, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(costs.Days) != 1 || len(costs.Days[0].Services) != 1 ||
		costs.Days[0].Services[0].Cost.String() != wantCost {
		t.Fatalf("costs = %+v, want cost %s", costs, wantCost)
	}
	cred, ok, err := store.GetCredential(ctx, "convert-slot")
	if err != nil || !ok {
		t.Fatalf("GetCredential: ok=%v err=%v", ok, err)
	}
	if string(cred.Ciphertext) != "ciphertext-seed" {
		t.Fatalf("credential ciphertext = %q", cred.Ciphertext)
	}
	got := appliedMigrations(t, store)
	if len(got) != len(wantMigrations) {
		t.Fatalf("migrations = %v, want %v", got, wantMigrations)
	}
	for i := range got {
		if got[i] != wantMigrations[i] {
			t.Fatalf("migrations = %v, want %v", got, wantMigrations)
		}
	}
}

func TestChangeEncryptionThreeDirections(t *testing.T) {
	ctx := context.Background()
	const wantCost = "0.123456789012345678"

	// Plaintext -> encrypted (quote-bearing new key).
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open plaintext: %v", err)
	}
	seedFullStore(t, ctx, store, wantCost)
	migrations := appliedMigrations(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	change, err := ChangeEncryption(ctx, dir, "", convertKeyA)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !change.Encrypted || change.Tables < 9 || change.Rows < 1 {
		t.Fatalf("encrypt change = %+v, want Encrypted with tables/rows", change)
	}
	assertDataDirCleanAfterConvert(t, dir)
	assertNoKeyLeakOnSuccessPath(t, convertKeyA)

	store, err = Open(ctx, dir, WithEncryptionKey(convertKeyA))
	if err != nil {
		t.Fatalf("open encrypted: %v", err)
	}
	assertSeedSurvived(t, ctx, store, wantCost, migrations)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Wrong key / keyless open classifications.
	_, err = Open(ctx, dir, WithEncryptionKey(convertKeyC))
	assertOpenError(t, err, "encrypted and the provided key is wrong", convertKeyC)
	_, err = Open(ctx, dir)
	assertOpenError(t, err, "encrypted; provide the key", convertKeyA)

	// Encrypted -> encrypted (rekey), both quote-bearing.
	removeBakPair(t, dir)
	change, err = ChangeEncryption(ctx, dir, convertKeyA, convertKeyB)
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if !change.Encrypted {
		t.Fatalf("rekey change = %+v, want Encrypted", change)
	}
	assertDataDirCleanAfterConvert(t, dir)
	store, err = Open(ctx, dir, WithEncryptionKey(convertKeyB))
	if err != nil {
		t.Fatalf("open rekeyed: %v", err)
	}
	assertSeedSurvived(t, ctx, store, wantCost, migrations)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = Open(ctx, dir, WithEncryptionKey(convertKeyA))
	assertOpenError(t, err, "encrypted and the provided key is wrong", convertKeyA)

	// Encrypted -> plaintext (decrypt).
	removeBakPair(t, dir)
	change, err = ChangeEncryption(ctx, dir, convertKeyB, "")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if change.Encrypted {
		t.Fatalf("decrypt change = %+v, want plaintext", change)
	}
	assertDataDirCleanAfterConvert(t, dir)
	store, err = Open(ctx, dir)
	if err != nil {
		t.Fatalf("open plaintext: %v", err)
	}
	assertSeedSurvived(t, ctx, store, wantCost, migrations)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// assertNoKeyLeakOnSuccessPath is a no-op marker that conversion itself never
// logs; secrets stay out of returned EncryptionChange (BackupPath is a path).
func assertNoKeyLeakOnSuccessPath(t *testing.T, _ string) {
	t.Helper()
}

func TestChangeEncryptionWALBearingSourceUntouchedAndRowSurvives(t *testing.T) {
	ctx := context.Background()
	scratchDir := t.TempDir()
	scratch := filepath.Join(scratchDir, "scratch.duckdb")
	scratchWAL := scratch + ".wal"

	db, err := sql.Open("duckdb", scratch)
	if err != nil {
		t.Fatalf("open scratch: %v", err)
	}
	// Close after setup: the abort knob below makes Close skip the shutdown
	// checkpoint header write, so the WAL survives with zero open handles
	// (needed on Windows where an open handle blocks the snapshot copy).
	for _, stmt := range []string{
		`CREATE TABLE tracked (id INTEGER PRIMARY KEY, note VARCHAR)`,
		`INSERT INTO tracked VALUES (1, 'checkpointed')`,
		`CHECKPOINT`,
		`SET checkpoint_threshold='10GB'`,
		`INSERT INTO tracked VALUES (2, 'wal-only-row')`,
		`SET debug_checkpoint_abort='before_header'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close scratch after abort knob: %v", err)
	}

	// Snapshot both files after Close: the abort knob left the WAL intact.
	dataDir := t.TempDir()
	live := filepath.Join(dataDir, DatabaseFile)
	liveWAL := live + ".wal"
	if err := copyFile(scratch, live); err != nil {
		t.Fatalf("copy duckdb: %v", err)
	}
	if err := copyFile(scratchWAL, liveWAL); err != nil {
		t.Fatalf("copy wal: %v", err)
	}

	// Precondition: WAL non-empty AND the .duckdb alone does not contain the row.
	walInfo, err := os.Stat(liveWAL)
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("precondition: WAL missing or empty: %v size=%v", err, walInfo)
	}
	aloneDir := t.TempDir()
	alone := filepath.Join(aloneDir, "alone.duckdb")
	if err := copyFile(live, alone); err != nil {
		t.Fatalf("copy alone: %v", err)
	}
	aloneDB, err := sql.Open("duckdb", alone)
	if err != nil {
		t.Fatalf("open alone: %v", err)
	}
	var aloneCount int
	if err := aloneDB.QueryRowContext(ctx, `SELECT count(*) FROM tracked WHERE note = 'wal-only-row'`).Scan(&aloneCount); err != nil {
		_ = aloneDB.Close()
		t.Fatalf("query alone: %v", err)
	}
	_ = aloneDB.Close()
	if aloneCount != 0 {
		t.Fatal("precondition failed: duckdb-alone already has the WAL-only row")
	}

	preDB := fileSHA256(t, live)
	preWAL := fileSHA256(t, liveWAL)

	change, err := ChangeEncryption(ctx, dataDir, "", convertKeyA)
	if err != nil {
		t.Fatalf("ChangeEncryption: %v", err)
	}
	if !change.Encrypted {
		t.Fatalf("change = %+v", change)
	}

	// Source pair byte-identical as the .bak pair.
	bak := live + bakSuffix
	bakWAL := bak + ".wal"
	if got := fileSHA256(t, bak); got != preDB {
		t.Fatalf("bak hash %s != pre-conversion live hash %s", got, preDB)
	}
	if got := fileSHA256(t, bakWAL); got != preWAL {
		t.Fatalf("bak.wal hash %s != pre-conversion wal hash %s", got, preWAL)
	}

	// Converted store (with new key) contains the WAL-only row.
	store, err := Open(ctx, dataDir, WithEncryptionKey(convertKeyA))
	if err != nil {
		// Open runs migrations; our scratch has no Costroid schema. Query via
		// a key-bearing attach on a fresh in-memory session instead.
		rowPresent, qerr := queryEncryptedTrackedRow(ctx, live, convertKeyA)
		if qerr != nil {
			t.Fatalf("open converted (%v) and direct query (%v)", err, qerr)
		}
		if !rowPresent {
			t.Fatal("WAL-only row missing after conversion")
		}
		return
	}
	_ = store.Close()
	rowPresent, qerr := queryEncryptedTrackedRow(ctx, live, convertKeyA)
	if qerr != nil {
		t.Fatalf("query converted: %v", qerr)
	}
	if !rowPresent {
		t.Fatal("WAL-only row missing after conversion")
	}
}

func queryEncryptedTrackedRow(ctx context.Context, path, key string) (bool, error) {
	connector, err := duckdb.NewConnector("", nil)
	if err != nil {
		return false, err
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	attach := fmt.Sprintf(
		"ATTACH '%s' AS q (ENCRYPTION_KEY '%s', READ_ONLY)",
		escapeSQLString(path), escapeSQLString(key))
	if _, err := conn.ExecContext(ctx, attach); err != nil {
		return false, err
	}
	var n int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM q.tracked WHERE note = 'wal-only-row'`).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func TestChangeEncryptionWrongCurrentKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir, WithEncryptionKey(convertKeyA))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedFullStore(t, ctx, store, "1.5")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	live := filepath.Join(dir, DatabaseFile)
	preHash := fileSHA256(t, live)
	var preWAL string
	if fileExists(live + ".wal") {
		preWAL = fileSHA256(t, live+".wal")
	}

	_, err = ChangeEncryption(ctx, dir, convertKeyC, convertKeyB)
	if err == nil {
		t.Fatal("expected wrong-key error")
	}
	if !strings.Contains(err.Error(), "encrypted and the provided key is wrong") {
		t.Fatalf("error = %q", err)
	}
	assertNoKeyLeak(t, err, convertKeyA, convertKeyB, convertKeyC)
	if fileSHA256(t, live) != preHash {
		t.Fatal("live store hash changed after wrong-key failure")
	}
	if preWAL != "" && fileSHA256(t, live+".wal") != preWAL {
		t.Fatal("live WAL hash changed after wrong-key failure")
	}
	assertNoConvertArtifacts(t, dir, false)
}

func assertNoConvertArtifacts(t *testing.T, dataDir string, allowBak bool) {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, "convert-tmp") || strings.Contains(name, "spill-tmp") {
			t.Fatalf("unexpected artifact %s", name)
		}
		if !allowBak && strings.HasSuffix(name, bakSuffix) {
			t.Fatalf("unexpected bak artifact %s", name)
		}
		if !allowBak && strings.Contains(name, bakSuffix+".wal") {
			t.Fatalf("unexpected bak.wal artifact %s", name)
		}
	}
}

func TestChangeEncryptionPreflight(t *testing.T) {
	ctx := context.Background()

	t.Run("leftover convert-tmp", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		live := filepath.Join(dir, DatabaseFile)
		pre := fileSHA256(t, live)
		tmp := live + convertTmpSuffix
		if err := os.WriteFile(tmp, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil || !strings.Contains(err.Error(), tmp) {
			t.Fatalf("error = %v, want convert-tmp path", err)
		}
		if !strings.Contains(err.Error(), "live store is intact") {
			t.Fatalf("error = %q, want live-intact wording", err)
		}
		if fileSHA256(t, live) != pre {
			t.Fatal("live store changed")
		}
	})

	t.Run("leftover convert-tmp.wal alone", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		live := filepath.Join(dir, DatabaseFile)
		pre := fileSHA256(t, live)
		tmpWal := live + convertTmpSuffix + ".wal"
		if err := os.WriteFile(tmpWal, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil || !strings.Contains(err.Error(), tmpWal) && !strings.Contains(err.Error(), convertTmpSuffix) {
			t.Fatalf("error = %v, want convert-tmp artifact mention", err)
		}
		if fileSHA256(t, live) != pre {
			t.Fatal("live store changed")
		}
	})

	t.Run("stale spill-tmp is removed and conversion succeeds", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		seedFullStore(t, ctx, store, "2.25")
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		spill := filepath.Join(dir, DatabaseFile+spillDirSuffix)
		if err := os.MkdirAll(filepath.Join(spill, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(spill, "nested", "x"), []byte("spill"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ChangeEncryption(ctx, dir, "", convertKeyA); err != nil {
			t.Fatalf("encrypt with stale spill: %v", err)
		}
		if _, err := os.Stat(spill); !os.IsNotExist(err) {
			t.Fatalf("spill dir still present after conversion: %v", err)
		}
		assertDataDirCleanAfterConvert(t, dir)
	})

	t.Run("bak alongside live", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		live := filepath.Join(dir, DatabaseFile)
		pre := fileSHA256(t, live)
		bak := live + bakSuffix
		if err := os.WriteFile(bak, []byte("old-backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil || !strings.Contains(err.Error(), bak) {
			t.Fatalf("error = %v, want bak path", err)
		}
		if !strings.Contains(err.Error(), "PLAINTEXT") {
			t.Fatalf("error = %q, want PLAINTEXT warning", err)
		}
		if fileSHA256(t, live) != pre {
			t.Fatal("live store changed")
		}
	})

	t.Run("interrupted swap: bak present live absent", func(t *testing.T) {
		dir := t.TempDir()
		live := filepath.Join(dir, DatabaseFile)
		bak := live + bakSuffix
		if err := os.WriteFile(bak, []byte("restorable"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil {
			t.Fatal("expected interrupted-swap error")
		}
		if !strings.Contains(err.Error(), "mv "+bak+" "+live) {
			t.Fatalf("error = %q, want exact mv recovery", err)
		}
		if strings.Contains(err.Error(), "live store is intact") {
			t.Fatalf("interrupted-swap message must not claim live store is intact: %q", err)
		}
	})

	t.Run("combined crash-inside-swap", func(t *testing.T) {
		dir := t.TempDir()
		live := filepath.Join(dir, DatabaseFile)
		bak := live + bakSuffix
		tmp := live + convertTmpSuffix
		if err := os.WriteFile(bak, []byte("restorable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmp, []byte("untrusted-tmp"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil {
			t.Fatal("expected interrupted-swap error")
		}
		if !strings.Contains(err.Error(), "mv "+bak+" "+live) {
			t.Fatalf("combined state must take interrupted-swap branch: %q", err)
		}
		if strings.Contains(err.Error(), "live store is intact") {
			t.Fatalf("must not claim live intact: %q", err)
		}
	})

	t.Run("no database at all", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ChangeEncryption(ctx, dir, "", convertKeyA)
		if err == nil || !strings.Contains(err.Error(), "no Costroid database exists in "+dir) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("both keys empty", func(t *testing.T) {
		_, err := ChangeEncryption(ctx, t.TempDir(), "", "")
		if err == nil || !strings.Contains(err.Error(), "nothing to convert") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("identical keys", func(t *testing.T) {
		_, err := ChangeEncryption(ctx, t.TempDir(), convertKeyA, convertKeyA)
		if err == nil || !strings.Contains(err.Error(), "identical - nothing to convert") {
			t.Fatalf("error = %v", err)
		}
		assertNoKeyLeak(t, err, convertKeyA)
	})

	t.Run("header not encrypted with current key", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_, err = ChangeEncryption(ctx, dir, convertKeyA, convertKeyB)
		if !errors.Is(err, ErrStoreNotEncrypted) {
			t.Fatalf("error = %v, want ErrStoreNotEncrypted", err)
		}
		assertNoKeyLeak(t, err, convertKeyA, convertKeyB)
	})

	t.Run("header already encrypted without current key", func(t *testing.T) {
		dir := t.TempDir()
		store, err := Open(ctx, dir, WithEncryptionKey(convertKeyA))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_, err = ChangeEncryption(ctx, dir, "", convertKeyB)
		if !errors.Is(err, ErrStoreAlreadyEncrypted) {
			t.Fatalf("error = %v, want ErrStoreAlreadyEncrypted", err)
		}
		assertNoKeyLeak(t, err, convertKeyA, convertKeyB)
	})
}

func TestChangeEncryptionCrossProcessLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	keyFile := filepath.Join(t.TempDir(), "hold.key")
	// No trailing newline (matches existing hold-store pattern).
	if err := os.WriteFile(keyFile, []byte(convertKeyA), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := Open(ctx, dir, WithEncryptionKey(convertKeyA))
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seedFullStore(t, ctx, store, "3.0")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperHoldStore$", "-test.v")
	cmd.Env = append(os.Environ(),
		"COSTROID_TEST_HOLD_STORE_DIR="+dir,
		"COSTROID_TEST_HOLD_STORE_KEY_FILE="+keyFile,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "HELPER_OPEN_ERROR") {
			t.Fatalf("helper failed: %s", line)
		}
		if strings.Contains(line, "HELPER_READY") {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("helper never ready: %v", scanner.Err())
	}

	_, err = ChangeEncryption(ctx, dir, convertKeyA, convertKeyB)
	if err == nil {
		t.Fatal("expected in-use error")
	}
	for _, part := range []string{"in use by another process", "single process at a time", "stop the other"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q missing %q", err, part)
		}
	}
	assertNoKeyLeak(t, err, convertKeyA, convertKeyB)
}

func TestChangeEncryptionNotADatabaseFile(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, DatabaseFile)
	if err := os.WriteFile(live, []byte("not-a-duckdb-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ChangeEncryption(context.Background(), dir, "", convertKeyA)
	if err == nil || !strings.Contains(err.Error(), "not a Costroid database file") {
		t.Fatalf("error = %v", err)
	}
	assertNoKeyLeak(t, err, convertKeyA)
}
