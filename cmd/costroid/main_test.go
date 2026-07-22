// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/demodata"
	"github.com/Costroid/costroid/internal/devtools/fakeblob"
	"github.com/Costroid/costroid/internal/devtools/fakes3"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/storage"
)

const s3Fixture = "../../testdata/aws-focus-s3/fixture"

const azureFixture = "../../testdata/azure-focus/fixture"

// hermeticAzureEnv scrubs the ambient Azure credential chain and enables
// the documented http-only test escape, so the azure-focus CLI ingest
// talks to the fakeblob endpoint anonymously and identically on any machine.
func hermeticAzureEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET",
		"AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_USERNAME", "AZURE_PASSWORD",
		"AZURE_FEDERATED_TOKEN_FILE", "AZURE_TOKEN_CREDENTIALS",
	} {
		t.Setenv(v, "")
	}
	t.Setenv(azurefocus.InsecureNoAuthEnv, "1")
}

func TestPrepareDemoIsolatedSyntheticOnly(t *testing.T) {
	normalDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", normalDir)
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(normalDir, "credentials.key"))
	t.Setenv("COSTROID_ADDR", "")

	asOf := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	prepared, stop, err := prepareDemo(context.Background(), nil, asOf)
	if err != nil || stop {
		t.Fatalf("prepareDemo = (stop %v, err %v)", stop, err)
	}
	defer prepared.close()
	if prepared.dataDir == normalDir {
		t.Fatalf("demo opened COSTROID_DATA_DIR %s", normalDir)
	}
	if prepared.addr != "127.0.0.1:8080" {
		t.Errorf("default demo addr = %q, want loopback", prepared.addr)
	}
	if !prepared.asOf.Equal(asOf) {
		t.Errorf("prepared demo asOf = %s, want %s", prepared.asOf, asOf)
	}
	if filepath.Dir(prepared.allocationPath) != prepared.dataDir {
		t.Errorf("allocation path %s is outside isolated demo dir %s", prepared.allocationPath, prepared.dataDir)
	}
	entries, err := os.ReadDir(normalDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("normal data/credential directory was touched: %+v", entries)
	}

	handler := api.NewHandler("test", os.DirFS(t.TempDir()), prepared.store, prepared.allocationPath,
		api.WithReadOnly(), api.WithDemo(),
		api.WithSyncSchedule(demoSyncSchedule(demodata.SyncSchedule(prepared.asOf))))
	for _, path := range []string{"/api/v1/meta", "/api/v1/sync/status", "/api/v1/costs/daily?groupBy=allocation"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if path == "/api/v1/meta" && !strings.Contains(rec.Body.String(), `"demo":true`) {
			t.Errorf("demo meta = %s", rec.Body.String())
		}
		if path == "/api/v1/sync/status" {
			var status api.SyncStatusResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if !status.Enabled || len(status.Sources) != 4 || status.Sources[2].LastRun == nil || status.Sources[2].LastRun.Outcome != api.Error {
				t.Errorf("demo sync status = %+v, want enabled four-source schedule with third source failing", status)
			}
		}
		if strings.Contains(path, "allocation") && (!strings.Contains(rec.Body.String(), `"key":"Production"`) || !strings.Contains(rec.Body.String(), `"key":"Unallocated"`)) {
			t.Errorf("allocation story buckets missing: %s", rec.Body.String())
		}
	}

	nonempty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonempty, "real-data"), []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareDemo(context.Background(), []string{"--data-dir", nonempty}, time.Now()); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("prepareDemo(nonempty) error = %v, want synthetic-only refusal", err)
	}
}

func TestPrepareDemoIgnoresDatabaseEncryptionKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "db-encryption.key")
	if err := os.WriteFile(keyFile, []byte("demo-must-not-use-this-key\n"), 0o600); err != nil {
		t.Fatalf("writing database-encryption key file: %v", err)
	}
	t.Setenv(dbEncryptionKeyFileEnvVar, keyFile)
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	demoDir := t.TempDir()
	prepared, stop, err := prepareDemo(context.Background(), []string{"--data-dir", demoDir}, time.Now())
	if err != nil || stop {
		t.Fatalf("prepareDemo = (stop %v, err %v)", stop, err)
	}
	prepared.close()

	contents, err := os.ReadFile(filepath.Join(demoDir, storage.DatabaseFile))
	if err != nil {
		t.Fatalf("reading demo database: %v", err)
	}
	if !bytes.Contains(contents, []byte("Synthetic daily usage")) {
		t.Fatal("plaintext demo database does not expose its synthetic marker")
	}
	store, err := storage.Open(context.Background(), demoDir)
	if err != nil {
		t.Fatalf("reopening demo without an encryption key: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing reopened demo: %v", err)
	}
}

func TestOpenStoreDatabaseEncryptionKeyFileResolution(t *testing.T) {
	writeKey := func(t *testing.T, contents string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "db-encryption.key")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing key file: %v", err)
		}
		return path
	}
	reopenWithKey := func(t *testing.T, dir, key string) {
		t.Helper()
		store, err := storage.Open(context.Background(), dir, storage.WithEncryptionKey(key))
		if err != nil {
			t.Fatalf("reopening encrypted store: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("closing reopened store: %v", err)
		}
	}

	t.Run("flag overrides env and trims one newline", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, writeKey(t, "environment-key"))
		flagKeyFile := writeKey(t, " flag key \n")
		store, err := openStore(context.Background(), flagKeyFile)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		reopenWithKey(t, dir, " flag key ")
	})

	t.Run("env carries the path", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, writeKey(t, "environment-key\n"))
		store, err := openStore(context.Background(), "")
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		reopenWithKey(t, dir, "environment-key")
	})

	t.Run("unset stays plaintext", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		store, err := openStore(context.Background(), "")
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		plaintext, err := storage.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("reopening plaintext store: %v", err)
		}
		if err := plaintext.Close(); err != nil {
			t.Fatalf("closing plaintext store: %v", err)
		}
	})

	t.Run("requested encryption fails closed", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.key")
		empty := writeKey(t, "")
		newlineOnly := writeKey(t, "\n")
		unreadable := t.TempDir()
		for name, keyFile := range map[string]string{
			"missing":      missing,
			"unreadable":   unreadable,
			"empty":        empty,
			"newline only": newlineOnly,
		} {
			t.Run(name, func(t *testing.T) {
				t.Setenv("COSTROID_DATA_DIR", t.TempDir())
				t.Setenv(dbEncryptionKeyFileEnvVar, writeKey(t, "must-not-fall-back"))
				_, err := openStore(context.Background(), keyFile)
				if err == nil || !strings.Contains(err.Error(), "the db-encryption key file "+keyFile+" is empty or unreadable") {
					t.Fatalf("openStore error = %v, want fail-closed error naming %s", err, keyFile)
				}
			})
		}
	})
}

func TestPrepareDemoRefusesServeDataDirectory(t *testing.T) {
	serveDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", serveDir)

	alias := serveDir + string(os.PathSeparator) + "."
	_, _, err := prepareDemo(context.Background(), []string{"--data-dir", alias}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "refusing to seed the demo into the serve data directory") {
		t.Fatalf("prepareDemo(--data-dir %s) error = %v, want serve-directory refusal", alias, err)
	}
}

// hermeticAWSEnv pins the ambient AWS credential chain to test-local
// values (mirroring the awsfocuss3 tests) so CLI-level ingest tests pass
// identically on any machine.
func hermeticAWSEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, body, 0o644)
	})
	if err != nil {
		t.Fatalf("copying %s to %s: %v", from, to, err)
	}
}

// TestIngestTenantSwitchRehomesInsteadOfSkipping proves the tuple skip
// is tenant-aware end to end (slice-3 review fix-up): re-running an
// unchanged export under a DIFFERENT --tenant must not print 'skipped'
// and exit — it must fall through to the hash path and re-home the
// stored records — while a same-tenant re-run still skips with zero
// GetObject calls.
func TestIngestTenantSwitchRehomesInsteadOfSkipping(t *testing.T) {
	ctx := context.Background()
	fake := fakes3.New(s3Fixture)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	hermeticAWSEnv(t, srv.URL)
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	args := []string{"ingest", "--connector", "aws-focus-s3", "--bucket", "demo", "--prefix", "exports/costroid-demo"}

	// Fresh ingest under the default tenant.
	if err := run(args); err != nil {
		t.Fatalf("fresh ingest: %v", err)
	}

	// Same tenant, unchanged export: tuple-skipped, zero GetObject calls.
	before := len(fake.GetObjectKeys())
	if err := run(args); err != nil {
		t.Fatalf("same-tenant re-run: %v", err)
	}
	if calls := fake.GetObjectKeys()[before:]; len(calls) != 0 {
		t.Fatalf("same-tenant re-run performed %d GetObject call(s): %v", len(calls), calls)
	}

	// Different tenant, unchanged export: NOT skipped — the periods fall
	// through to the hash path (GetObject calls happen) and re-home.
	before = len(fake.GetObjectKeys())
	if err := run(append(args, "--tenant", "acme")); err != nil {
		t.Fatalf("tenant-switch run: %v", err)
	}
	if calls := fake.GetObjectKeys()[before:]; len(calls) == 0 {
		t.Fatal("tenant-switch run was tuple-skipped: zero GetObject calls")
	}

	store, err := storage.Open(ctx, os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	rehomed, err := store.DailyCostsByService(ctx, "acme", time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService(acme): %v", err)
	}
	if len(rehomed.Days) != 14 {
		t.Fatalf("tenant acme sees %d day(s), want all 14 re-homed", len(rehomed.Days))
	}
	old, err := store.DailyCostsByService(ctx, "default", time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService(default): %v", err)
	}
	if len(old.Days) != 0 {
		t.Fatalf("tenant default still sees %d day(s), want none after re-homing", len(old.Days))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	// Same new tenant again: the tuple skip applies once more.
	before = len(fake.GetObjectKeys())
	if err := run(append(args, "--tenant", "acme")); err != nil {
		t.Fatalf("same-new-tenant re-run: %v", err)
	}
	if calls := fake.GetObjectKeys()[before:]; len(calls) != 0 {
		t.Fatalf("same-new-tenant re-run performed %d GetObject call(s): %v", len(calls), calls)
	}
}

// TestIngestAzureTenantSwitchRehomesInsteadOfSkipping is the azure-focus
// twin of TestIngestTenantSwitchRehomesInsteadOfSkipping (slice-4 review
// fix-up: the azure ingest wiring duplicates the aws-focus-s3 path but had
// no azure-side e2e of the tenant filter). A same-tenant unchanged re-sync
// costs ZERO Get Blob calls; a --tenant switch is NOT skipped — it falls
// through to the hash path and re-homes the stored records.
func TestIngestAzureTenantSwitchRehomesInsteadOfSkipping(t *testing.T) {
	ctx := context.Background()
	fake := fakeblob.New(azureFixture)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	hermeticAzureEnv(t)
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	accountURL := srv.URL + "/devaccount"
	args := []string{"ingest", "--connector", "azure-focus", "--account-url", accountURL,
		"--container", "exports", "--prefix", "costroid-demo"}

	// Fresh ingest under the default tenant.
	if err := run(args); err != nil {
		t.Fatalf("fresh ingest: %v", err)
	}

	// Same tenant, unchanged export: tuple-skipped, zero Get Blob calls.
	before := len(fake.GetBlobKeys())
	if err := run(args); err != nil {
		t.Fatalf("same-tenant re-run: %v", err)
	}
	if calls := fake.GetBlobKeys()[before:]; len(calls) != 0 {
		t.Fatalf("same-tenant re-run performed %d Get Blob call(s): %v", len(calls), calls)
	}

	// Different tenant, unchanged export: NOT skipped — the periods fall
	// through to the hash path (Get Blob calls happen) and re-home.
	before = len(fake.GetBlobKeys())
	if err := run(append(args, "--tenant", "acme")); err != nil {
		t.Fatalf("tenant-switch run: %v", err)
	}
	if calls := fake.GetBlobKeys()[before:]; len(calls) == 0 {
		t.Fatal("tenant-switch run was tuple-skipped: zero Get Blob calls")
	}

	store, err := storage.Open(ctx, os.Getenv("COSTROID_DATA_DIR"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	rehomed, err := store.DailyCostsByService(ctx, "acme", time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService(acme): %v", err)
	}
	if len(rehomed.Days) != 6 {
		t.Fatalf("tenant acme sees %d day(s), want all 6 re-homed", len(rehomed.Days))
	}
	old, err := store.DailyCostsByService(ctx, "default", time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService(default): %v", err)
	}
	if len(old.Days) != 0 {
		t.Fatalf("tenant default still sees %d day(s), want none after re-homing", len(old.Days))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	// Same new tenant again: the tuple skip applies once more.
	before = len(fake.GetBlobKeys())
	if err := run(append(args, "--tenant", "acme")); err != nil {
		t.Fatalf("same-new-tenant re-run: %v", err)
	}
	if calls := fake.GetBlobKeys()[before:]; len(calls) != 0 {
		t.Fatalf("same-new-tenant re-run performed %d Get Blob call(s): %v", len(calls), calls)
	}
}

// TestIngestPoisonedPeriodDoesNotBlockOthers proves per-period discovery
// degradation at the CLI (slice-3 review fix-up): a manifest anomaly in
// one period fails that period alone — --period on another period
// succeeds, and a full run ingests the healthy period while reporting
// the poisoned one in a non-zero exit.
func TestIngestPoisonedPeriodDoesNotBlockOthers(t *testing.T) {
	tree := t.TempDir()
	copyTree(t, s3Fixture, tree)
	stray := filepath.Join(tree, "demo", "exports/costroid-demo/metadata/BILLING_PERIOD=2026-05/stray-copy-Manifest.json")
	if err := os.WriteFile(stray, []byte(`{"dataFiles": []}`), 0o644); err != nil {
		t.Fatalf("writing stray manifest: %v", err)
	}
	srv := httptest.NewServer(fakes3.New(tree))
	t.Cleanup(srv.Close)
	hermeticAWSEnv(t, srv.URL)
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	args := []string{"ingest", "--connector", "aws-focus-s3", "--bucket", "demo", "--prefix", "exports/costroid-demo"}

	// Targeting the healthy period succeeds despite the poisoned one.
	if err := run(append(args, "--period", "2026-06")); err != nil {
		t.Fatalf("--period 2026-06 with poisoned 2026-05: %v", err)
	}

	// A full run ingests what it can and reports the poisoned period.
	err := run(args)
	if err == nil {
		t.Fatal("full run succeeded despite the poisoned period")
	}
	if !strings.Contains(err.Error(), "1 of 2 period(s) failed (2026-05)") {
		t.Errorf("full-run error %q does not report the poisoned period", err)
	}
}

// TestCredentialsDeleteCLI proves `credentials delete` removes a slot (so
// `list` no longer shows it) and that deleting a missing slot exits non-zero
// with the actionable message.
func TestCredentialsDeleteCLI(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "credentials.key"))

	if _, err := runCLI([]string{"credentials", "init"}, ""); err != nil {
		t.Fatalf("credentials init: %v", err)
	}
	if _, err := runCLI([]string{"credentials", "set", "anthropic-cost"}, "test-secret-value"); err != nil {
		t.Fatalf("credentials set: %v", err)
	}

	listed, err := runCLI([]string{"credentials", "list"}, "")
	if err != nil {
		t.Fatalf("credentials list: %v", err)
	}
	if !strings.Contains(listed, "anthropic-cost") {
		t.Fatalf("list before delete = %q, want the slot present", listed)
	}

	deleted, err := runCLI([]string{"credentials", "delete", "anthropic-cost"}, "")
	if err != nil {
		t.Fatalf("credentials delete: %v", err)
	}
	if !strings.Contains(deleted, `deleted credential "anthropic-cost"`) {
		t.Errorf("delete output = %q, want the deletion confirmation", deleted)
	}

	after, err := runCLI([]string{"credentials", "list"}, "")
	if err != nil {
		t.Fatalf("credentials list after delete: %v", err)
	}
	if strings.Contains(after, "anthropic-cost") {
		t.Errorf("list after delete = %q, want the slot gone", after)
	}

	// Deleting a now-missing slot fails (exit 1) with the actionable message.
	out, err := runCLI([]string{"credentials", "delete", "anthropic-cost"}, "")
	if err == nil {
		t.Fatalf("deleting a missing slot succeeded; out=%q", out)
	}
	if !strings.Contains(err.Error(), "nothing to delete") {
		t.Errorf("missing-slot delete error = %v, want the actionable nothing-to-delete message", err)
	}
}

// TestIngestHelpWarnsAdminKey proves the ingest --help surface carries the
// Anthropic full-org-admin warning (it previously lived only in godoc).
func TestIngestHelpWarnsAdminKey(t *testing.T) {
	out, err := runCLI([]string{"ingest", "--help"}, "")
	if err != nil {
		t.Fatalf("ingest --help: %v", err)
	}
	if !strings.Contains(out, "full-org-admin") {
		t.Errorf("ingest --help output does not warn about the unscopeable admin key:\n%s", out)
	}
}

// TestIngestConnectorStringsIncludeFocusCSV proves focus-csv is enumerated in
// the three connector-name surfaces: the --connector flag usage (ingest
// --help), the empty-connector "required" error, and the unknown-connector
// error. Dropping focus-csv from any of them fails the matching assertion.
func TestIngestConnectorStringsIncludeFocusCSV(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())

	help, err := runCLI([]string{"ingest", "--help"}, "")
	if err != nil {
		t.Fatalf("ingest --help: %v", err)
	}
	if !strings.Contains(help, "focus-csv") {
		t.Errorf("ingest --help does not enumerate focus-csv:\n%s", help)
	}

	// Empty --connector → the "required" error must list focus-csv.
	if _, err := runCLI([]string{"ingest"}, ""); err == nil || !strings.Contains(err.Error(), "focus-csv") {
		t.Errorf("empty-connector error = %v, want it to enumerate focus-csv", err)
	}

	// Unknown --connector → the "unknown connector" error must list focus-csv.
	if _, err := runCLI([]string{"ingest", "--connector", "not-a-connector"}, ""); err == nil ||
		!strings.Contains(err.Error(), "focus-csv") {
		t.Errorf("unknown-connector error = %v, want it to enumerate focus-csv", err)
	}
}

// TestFocusCSVMandatoryNullableWarningCLI drives the 1.4 absent-Mandatory-
// nullable WARNING through the CLI (main.go's `for _, w := range warnings`
// print loop). The minimal 1.4 fixture carries only the 15 not-null columns, so
// Discover returns one DatasetConfiguration warning; the ingest still succeeds
// (warn, not fail). Only the Discover-level warning was proven before — the CLI
// print loop had no failing-on-removal test. Deleting the print loop reddens
// this.
func TestFocusCSVMandatoryNullableWarningCLI(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	out, err := runCLI([]string{"ingest", "--connector", "focus-csv",
		"--path", "../../testdata/focus-csv/negative/focus-1.4-minimal.csv", "--focus-version", "1.4"}, "")
	if err != nil {
		t.Fatalf("minimal-1.4 ingest should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DatasetConfiguration") {
		t.Errorf("CLI did not print the Mandatory-but-nullable absence warning:\n%s", out)
	}
	// The warn arm still ingests both months (warn, do not fail).
	if !strings.Contains(out, "period 2026-05:") || !strings.Contains(out, "period 2026-06:") {
		t.Errorf("minimal-1.4 warn arm did not still ingest both months:\n%s", out)
	}
}

// TestIngestHelpDocumentsLenient asserts the --lenient flag and its scope are
// documented in `ingest --help` (the flag usage). Dropping the flag or rewording
// its scope out of the description reddens this.
func TestIngestHelpDocumentsLenient(t *testing.T) {
	out, err := runCLI([]string{"ingest", "--help"}, "")
	if err != nil {
		t.Fatalf("ingest --help: %v", err)
	}
	for _, want := range []string{"-lenient", "tolerate UTC timestamp FORMAT variants", "still rejects zone-less"} {
		if !strings.Contains(out, want) {
			t.Errorf("ingest --help does not document %q:\n%s", want, out)
		}
	}
	// The flag is a bool (like -force), so its help line must NOT render an
	// arg-name placeholder. A backtick-quoted word in the usage string makes
	// flag.UnquoteUsage treat it as the flag's value name, so "-lenient" wrongly
	// renders as "-lenient UTC". Assert that placeholder is ABSENT (the plain
	// "-lenient" substring above cannot catch it — it is a prefix of "-lenient
	// UTC"). A regression that re-quotes any word reddens here.
	if strings.Contains(out, "-lenient UTC") {
		t.Errorf("ingest --help renders a value placeholder for the bool -lenient flag (a backticked usage word leaked as UnquoteUsage's arg name):\n%s", out)
	}
}

// TestUsageDocumentsPartFileLimitation asserts the focus-csv part-file
// limitation sentence is present in the top-level usage/help text (main.go's
// `usage` const, surfaced here via the no-command error that appends it). The
// connector-strings help test only checks the "focus-csv" substring; this pins
// the documented-limitation wording so dropping it from the usage string is
// caught.
func TestUsageDocumentsPartFileLimitation(t *testing.T) {
	_, err := runCLI([]string{}, "")
	if err == nil || !strings.Contains(err.Error(), "part-file replaces the month with that part alone") {
		t.Errorf("top-level usage does not document the focus-csv part-file limitation: %v", err)
	}
}

func TestResolveAddr(t *testing.T) {
	tests := []struct {
		name     string
		flagAddr string
		envAddr  string
		want     string
	}{
		{name: "default", flagAddr: "", envAddr: "", want: "127.0.0.1:8080"},
		{name: "env only", flagAddr: "", envAddr: ":9090", want: ":9090"},
		{name: "flag only", flagAddr: ":7070", envAddr: "", want: ":7070"},
		{name: "flag wins over env", flagAddr: ":7070", envAddr: ":9090", want: ":7070"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAddr(tt.flagAddr, tt.envAddr); got != tt.want {
				t.Errorf("resolveAddr(%q, %q) = %q, want %q", tt.flagAddr, tt.envAddr, got, tt.want)
			}
		})
	}
}

// TestResolveAllocationRulesPath pins the allocation-rules path precedence
// (flag > env > <config-dir>/costroid/allocation.json). Every subtest pins the
// ambient environment so none reads the developer's real config dir.
func TestResolveAllocationRulesPath(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(allocationRulesEnvVar, "/env/rules.json")
		if got := resolveAllocationRulesPath("/flag/rules.json"); got != "/flag/rules.json" {
			t.Errorf("got %q, want the flag value", got)
		}
	})
	t.Run("env used when flag empty", func(t *testing.T) {
		t.Setenv(allocationRulesEnvVar, "/env/rules.json")
		if got := resolveAllocationRulesPath(""); got != "/env/rules.json" {
			t.Errorf("got %q, want the env value", got)
		}
	})
	t.Run("default is under the config dir", func(t *testing.T) {
		cfg := setConfigDir(t)
		t.Setenv(allocationRulesEnvVar, "") // and the env override
		want := filepath.Join(cfg, "costroid", "allocation.json")
		if got := resolveAllocationRulesPath(""); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// setConfigDir redirects os.UserConfigDir to a temp directory for the
// duration of the test. On Windows UserConfigDir reads AppData; elsewhere
// it honors XDG_CONFIG_HOME.
func setConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	return dir
}

// hermeticServeEnv pins every serve-related env var so ambient developer
// configuration cannot leak into serveConfig. Auth is scrubbed to EMPTY; a
// subtest that needs serve to reach a non-error result sets a silent bearer
// token itself (COSTROID_AUTH_TOKEN) rather than --no-auth, which would emit the
// loud warning and break the allocation-warning assertions.
func hermeticServeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("COSTROID_ADDR", "")
	t.Setenv(allocationRulesEnvVar, "")
	t.Setenv(sourcesEnvVar, "")
	t.Setenv(dbEncryptionKeyFileEnvVar, "")
	t.Setenv(envAuthToken, "")
	t.Setenv(envAuthTokenFile, "")
	t.Setenv(envAuthTrustedHeader, "")
	t.Setenv(envAuthTrustedProxies, "")
	t.Setenv(envModelEndpoint, "")
	t.Setenv(envModelName, "")
	t.Setenv(envModelCredentialFile, "")
	t.Setenv(envModelTimeout, "")
}

// TestServeConfig exercises serve's real FlagSet without opening the store or
// starting a listener. Every subtest pins ALL serve env vars (via
// hermeticServeEnv) so ambient developer configuration cannot leak, then
// configures a silent bearer token to satisfy the fail-closed policy without
// perturbing the allocation-warning assertions.
func TestServeConfig(t *testing.T) {
	t.Run("database encryption key-file flag is distinct", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		keyFile := filepath.Join(t.TempDir(), "database.key")
		cfg, warning, stop, err := serveConfig([]string{"--db-encryption-key-file", keyFile})
		if err != nil || stop || cfg.dbEncryptionKeyFile != keyFile {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v), want database key path %q", cfg, warning, stop, err, keyFile)
		}
		for _, phrase := range []string{"at-rest DATABASE-encryption", "distinct from --key-file", "CREDENTIAL-store key"} {
			if !strings.Contains(dbEncryptionKeyFileUsage, phrase) {
				t.Errorf("dbEncryptionKeyFileUsage = %q, want phrase %q", dbEncryptionKeyFileUsage, phrase)
			}
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		rules := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(rules, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("COSTROID_ADDR", ":9090")
		t.Setenv(allocationRulesEnvVar, filepath.Join(t.TempDir(), "env.json"))
		cfg, warning, stop, err := serveConfig([]string{"--addr", ":7070", "--allocation-rules", rules})
		if err != nil || stop || warning != "" || cfg.addr != ":7070" || cfg.allocationRulesPath != rules {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v)", cfg, warning, stop, err)
		}
	})

	t.Run("env when flag empty", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		rules := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(rules, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("COSTROID_ADDR", ":9090")
		t.Setenv(allocationRulesEnvVar, rules)
		cfg, warning, stop, err := serveConfig(nil)
		if err != nil || stop || warning != "" || cfg.addr != ":9090" || cfg.allocationRulesPath != rules {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v)", cfg, warning, stop, err)
		}
	})

	t.Run("default under config dir warns when missing", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		cfgDir := setConfigDir(t)
		cfg, warning, stop, err := serveConfig(nil)
		wantPath := filepath.Join(cfgDir, "costroid", "allocation.json")
		wantWarning := "allocation rules file not found: " + wantPath + " — groupBy=allocation will return 400 until it exists"
		if err != nil || stop || cfg.addr != "127.0.0.1:8080" || cfg.allocationRulesPath != wantPath || warning != wantWarning {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v), want path %q warning %q", cfg, warning, stop, err, wantPath, wantWarning)
		}
	})

	t.Run("unresolvable path warns as unconfigured", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("AppData", "")
		cfg, warning, stop, err := serveConfig(nil)
		want := "no allocation rules path could be resolved — groupBy=allocation will return 400 as unconfigured"
		if err != nil || stop || cfg.allocationRulesPath != "" || warning != want {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v), want warning %q", cfg, warning, stop, err, want)
		}
	})

	t.Run("help stops without error", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, stop, err := serveConfig([]string{"-h"})
		if err != nil || !stop {
			t.Fatalf("serveConfig(-h) = stop %v, err %v", stop, err)
		}
	})

	t.Run("non-ENOENT stat error also warns (still non-fatal)", func(t *testing.T) {
		// A resolvable path whose os.Stat fails with something OTHER than
		// ErrNotExist (here ENOTDIR: the path's parent is a regular file) must
		// still produce a startup warning and let serve start — the finding is
		// that only ErrNotExist warned before.
		if runtime.GOOS == "windows" {
			t.Skip("Windows reports a file-as-parent path as not-found; the non-ENOENT warning branch is unreachable via this setup")
		}
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		regular := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(regular, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		notDir := filepath.Join(regular, "child.json") // stat → ENOTDIR (not ErrNotExist)
		t.Setenv(allocationRulesEnvVar, notDir)
		cfg, warning, stop, err := serveConfig(nil)
		if err != nil || stop || cfg.allocationRulesPath != notDir {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v)", cfg, warning, stop, err)
		}
		if !strings.Contains(warning, notDir) || !strings.Contains(warning, "not accessible") {
			t.Errorf("warning = %q, want it to name the path and flag the non-ENOENT stat error", warning)
		}
	})
}

// TestIsLoopbackAddr covers the loopback/public bind classifier (required test
// 5). isLoopbackAddr lives in package main (it gates the --no-auth escalation),
// so its table test lives here rather than in internal/api.
func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"127.0.0.5:8080", true}, // anywhere in 127.0.0.0/8
		{":8080", false},         // empty host binds every interface
		{"0.0.0.0:8080", false},  // unspecified → public
		{"[::]:8080", false},     // IPv6 unspecified → public
		{"203.0.113.7:8080", false},
		{"example.com:8080", false}, // bare hostname → conservatively public
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tt.addr); got != tt.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// TestServeConfigDefaultBind pins the loopback-by-default bind and the explicit
// public opt-in via --addr (required test 7).
func TestServeConfigDefaultBind(t *testing.T) {
	t.Run("default is loopback", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		cfg, _, stop, err := serveConfig(nil)
		if err != nil || stop || cfg.addr != "127.0.0.1:8080" {
			t.Fatalf("serveConfig addr = %q (stop %v, err %v), want 127.0.0.1:8080", cfg.addr, stop, err)
		}
	})
	t.Run("--addr overrides to a public bind", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "silent-token")
		cfg, _, stop, err := serveConfig([]string{"--addr", "0.0.0.0:8080"})
		if err != nil || stop || cfg.addr != "0.0.0.0:8080" {
			t.Fatalf("serveConfig addr = %q (stop %v, err %v), want 0.0.0.0:8080", cfg.addr, stop, err)
		}
	})
}

// TestServeConfigFailClosed covers the fail-closed policy (required test 8): no
// auth, both modes, --no-auth+mode, an all-addresses proxy CIDR, and the absent
// --auth-token value flag.
func TestServeConfigFailClosed(t *testing.T) {
	tokenFile := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("no auth and no --no-auth is an error", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, _, err := serveConfig(nil)
		if err == nil || !strings.Contains(err.Error(), "no authentication configured") {
			t.Fatalf("err = %v, want a no-authentication-configured error", err)
		}
	})

	t.Run("both modes is an error", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, _, err := serveConfig([]string{"--auth-token-file", tokenFile(t), "--auth-trusted-header", "X-WEBAUTH-USER"})
		if err == nil || !strings.Contains(err.Error(), "exactly one auth mode") {
			t.Fatalf("err = %v, want an exactly-one-mode error", err)
		}
	})

	t.Run("--no-auth with a mode is an error", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, _, err := serveConfig([]string{"--no-auth", "--auth-token-file", tokenFile(t)})
		if err == nil || !strings.Contains(err.Error(), "--no-auth cannot be combined") {
			t.Fatalf("err = %v, want a --no-auth-conflict error", err)
		}
	})

	t.Run("all-addresses IPv4 trusted proxy is refused", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, _, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "0.0.0.0/0"})
		if err == nil || !strings.Contains(err.Error(), "implausibly broad") {
			t.Fatalf("err = %v, want an all-addresses-refused error", err)
		}
	})

	t.Run("all-addresses IPv6 trusted proxy is refused", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, _, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "::/0"})
		if err == nil || !strings.Contains(err.Error(), "implausibly broad") {
			t.Fatalf("err = %v, want an all-addresses-refused error", err)
		}
	})

	t.Run("IPv4 broad-prefix boundary", func(t *testing.T) {
		hermeticServeEnv(t)
		if _, _, _, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "10.0.0.0/7"}); err == nil || !strings.Contains(err.Error(), "implausibly broad") {
			t.Fatalf("/7 err = %v, want an implausibly-broad error", err)
		}
		cfg, _, stop, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "10.0.0.0/8"})
		if err != nil || stop || len(cfg.trustedProxies) != 1 || cfg.trustedProxies[0].Bits() != 8 {
			t.Fatalf("/8 serveConfig = (%+v, stop %v, err %v), want accepted boundary", cfg, stop, err)
		}
	})

	t.Run("IPv6 broad-prefix boundary", func(t *testing.T) {
		hermeticServeEnv(t)
		if _, _, _, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "2001:db8::/15"}); err == nil || !strings.Contains(err.Error(), "implausibly broad") {
			t.Fatalf("/15 err = %v, want an implausibly-broad error", err)
		}
		cfg, _, stop, err := serveConfig([]string{"--auth-trusted-header", "X-WEBAUTH-USER", "--auth-trusted-proxies", "2001:db8::/16"})
		if err != nil || stop || len(cfg.trustedProxies) != 1 || cfg.trustedProxies[0].Bits() != 16 {
			t.Fatalf("/16 serveConfig = (%+v, stop %v, err %v), want accepted boundary", cfg, stop, err)
		}
	})

	t.Run("no --auth-token value flag exists (parse error)", func(t *testing.T) {
		hermeticServeEnv(t)
		_, _, stop, err := serveConfig([]string{"--auth-token", "s3cret"})
		if err == nil || !stop {
			t.Fatalf("serveConfig(--auth-token) = stop %v, err %v; want a parse error proving the value flag does not exist", stop, err)
		}
	})
}

// TestServeConfigNoAuthWarning covers --no-auth (required test 9): stop=false,
// no error, a loud warning, escalated for a non-loopback bind. A valid
// --allocation-rules silences the allocation warning so only the auth warning
// remains; assertions use Contains, not exact-match.
func TestServeConfigNoAuthWarning(t *testing.T) {
	validRules := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "rules.json")
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("loopback bind warns without escalation", func(t *testing.T) {
		hermeticServeEnv(t)
		cfg, warning, stop, err := serveConfig([]string{"--no-auth", "--allocation-rules", validRules(t)})
		if err != nil || stop || !cfg.noAuth {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v)", cfg, warning, stop, err)
		}
		if !strings.Contains(warning, "WITHOUT AUTHENTICATION") {
			t.Errorf("warning = %q, want it to name WITHOUT AUTHENTICATION", warning)
		}
		if strings.Contains(warning, "network-exposed") {
			t.Errorf("warning = %q, must not escalate for a loopback bind", warning)
		}
	})

	t.Run("non-loopback bind escalates", func(t *testing.T) {
		hermeticServeEnv(t)
		cfg, warning, stop, err := serveConfig([]string{"--no-auth", "--addr", "0.0.0.0:8080", "--allocation-rules", validRules(t)})
		if err != nil || stop || !cfg.noAuth {
			t.Fatalf("serveConfig = (%+v, %q, %v, %v)", cfg, warning, stop, err)
		}
		if !strings.Contains(warning, "WITHOUT AUTHENTICATION") || !strings.Contains(warning, "network-exposed") {
			t.Errorf("warning = %q, want the escalated network-exposed warning", warning)
		}
	})
}

func TestServeConfigRejectsExposedUnauthenticatedQueryModel(t *testing.T) {
	configureModel := func(t *testing.T) {
		t.Helper()
		t.Setenv(envModelEndpoint, "https://model.example.invalid/v1/chat/completions")
		t.Setenv(envModelName, "local-model")
		t.Setenv(envModelTimeout, "45s")
	}

	t.Run("all conditions are rejected", func(t *testing.T) {
		hermeticServeEnv(t)
		configureModel(t)
		_, _, _, err := serveConfig([]string{"--no-auth", "--addr", "0.0.0.0:8080"})
		if err == nil || !strings.Contains(err.Error(), "require authentication on a network-exposed address") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("loopback bind starts", func(t *testing.T) {
		hermeticServeEnv(t)
		configureModel(t)
		cfg, _, stop, err := serveConfig([]string{"--no-auth", "--addr", "127.0.0.1:8080"})
		if err != nil || stop || !cfg.model.configured() || cfg.model.timeout != 45*time.Second {
			t.Fatalf("stop = %v, configured = %v, timeout = %s, error = %v", stop, cfg.model.configured(), cfg.model.timeout, err)
		}
	})

	t.Run("authenticated public bind starts", func(t *testing.T) {
		hermeticServeEnv(t)
		configureModel(t)
		t.Setenv(envAuthToken, "silent-token")
		cfg, _, stop, err := serveConfig([]string{"--addr", "0.0.0.0:8080"})
		if err != nil || stop || cfg.authModeName != "bearer" || !cfg.model.configured() {
			t.Fatalf("stop = %v, auth mode = %q, configured = %v, error = %v", stop, cfg.authModeName, cfg.model.configured(), err)
		}
	})

	t.Run("unconfigured public bind starts", func(t *testing.T) {
		hermeticServeEnv(t)
		cfg, _, stop, err := serveConfig([]string{"--no-auth", "--addr", "0.0.0.0:8080"})
		if err != nil || stop || cfg.model.configured() {
			t.Fatalf("stop = %v, configured = %v, error = %v", stop, cfg.model.configured(), err)
		}
	})
}

// TestServeConfigBearerTokenResolution covers the bearer-token source precedence
// and hygiene (required test 10): a file read with exactly one trailing newline
// trimmed (interior/edge spaces survive), an empty file → error, an explicit
// unreadable file → error naming the path with NO env fall-through, and
// file-over-env precedence.
func TestServeConfigBearerTokenResolution(t *testing.T) {
	t.Run("--auth-token-file trims exactly one trailing newline", func(t *testing.T) {
		hermeticServeEnv(t)
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte(" tok en \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, stop, err := serveConfig([]string{"--auth-token-file", p})
		if err != nil || stop {
			t.Fatalf("serveConfig = stop %v, err %v", stop, err)
		}
		if cfg.bearerToken != " tok en " || cfg.authModeName != "bearer" {
			t.Fatalf("bearerToken = %q mode %q, want %q bearer (only one trailing newline trimmed)", cfg.bearerToken, cfg.authModeName, " tok en ")
		}
	})

	t.Run("trims one of two trailing newlines", func(t *testing.T) {
		hermeticServeEnv(t)
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("tok\n\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, stop, err := serveConfig([]string{"--auth-token-file", p})
		if err != nil || stop || cfg.bearerToken != "tok\n" {
			t.Fatalf("bearerToken = %q (stop %v, err %v), want one trailing newline retained", cfg.bearerToken, stop, err)
		}
	})

	t.Run("trims one trailing CRLF", func(t *testing.T) {
		hermeticServeEnv(t)
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("tok\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, stop, err := serveConfig([]string{"--auth-token-file", p})
		if err != nil || stop || cfg.bearerToken != "tok" {
			t.Fatalf("bearerToken = %q (stop %v, err %v), want CRLF removed", cfg.bearerToken, stop, err)
		}
	})

	t.Run("empty file is an error", func(t *testing.T) {
		hermeticServeEnv(t)
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := serveConfig([]string{"--auth-token-file", p})
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("err = %v, want an empty-token-file error", err)
		}
	})

	t.Run("explicit unreadable file errors naming the path, no env fall-through", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "env-token") // must NOT be used when a file source is selected
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		_, _, _, err := serveConfig([]string{"--auth-token-file", missing})
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("err = %v, want a read error naming %q with no env fall-through", err, missing)
		}
	})

	t.Run("file source beats the env value", func(t *testing.T) {
		hermeticServeEnv(t)
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte("file-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envAuthTokenFile, p)
		t.Setenv(envAuthToken, "env-token")
		cfg, _, _, err := serveConfig(nil)
		if err != nil || cfg.bearerToken != "file-token" {
			t.Fatalf("bearerToken = %q err %v, want file-token (file beats env)", cfg.bearerToken, err)
		}
	})

	t.Run("env value is used when no file source is set", func(t *testing.T) {
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, "env-token")
		cfg, _, _, err := serveConfig(nil)
		if err != nil || cfg.bearerToken != "env-token" || cfg.authModeName != "bearer" {
			t.Fatalf("bearerToken = %q mode %q err %v, want env-token bearer", cfg.bearerToken, cfg.authModeName, err)
		}
	})
}

// TestServeAuthOptionsWiring drives the production serveConfig -> authOptions
// -> api.NewHandler seam. Each auth arm has an independent deny assertion, so
// replacing either authOptions arm with nil makes its subtest fail.
func TestServeAuthOptionsWiring(t *testing.T) {
	newHandler := func(t *testing.T, args []string) (serveSettings, http.Handler) {
		t.Helper()
		hermeticServeEnv(t)
		cfg, _, stop, err := serveConfig(args)
		if err != nil || stop {
			t.Fatalf("serveConfig(%v) = stop %v, err %v", args, stop, err)
		}
		return cfg, api.NewHandler("test", os.DirFS(t.TempDir()), nil, "", authOptions(cfg)...)
	}

	t.Run("bearer", func(t *testing.T) {
		const token = "configured-token"
		// Configure through the same environment source serve uses.
		hermeticServeEnv(t)
		t.Setenv(envAuthToken, token)
		cfg, _, stop, err := serveConfig(nil)
		if err != nil || stop {
			t.Fatalf("serveConfig(bearer) = stop %v, err %v", stop, err)
		}
		handler := api.NewHandler("test", os.DirFS(t.TempDir()), nil, "", authOptions(cfg)...)

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("request without bearer = %d, want 401", w.Code)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request with configured bearer = %d, want 200", w.Code)
		}
	})

	t.Run("forward-auth", func(t *testing.T) {
		cfg, handler := newHandler(t, []string{"--auth-trusted-header", "X-WEBAUTH-USER"})
		if cfg.authModeName != "forward-auth" {
			t.Fatalf("authModeName = %q, want forward-auth", cfg.authModeName)
		}
		want := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
		if len(cfg.trustedProxies) != len(want) || cfg.trustedProxies[0] != want[0] || cfg.trustedProxies[1] != want[1] {
			t.Fatalf("trustedProxies = %v, want loopback defaults %v", cfg.trustedProxies, want)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-WEBAUTH-USER", "alice")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("trusted peer with identity = %d, want 200", w.Code)
		}

		req = httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		req.RemoteAddr = "203.0.113.1:1234"
		req.Header.Set("X-WEBAUTH-USER", "mallory")
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("untrusted peer with identity = %d, want 401", w.Code)
		}
	})
}

// TestMetricsImportTenantReachesStore proves the CLI `metrics import --tenant`
// flag reaches the store's per-tenant keying end to end: rows imported under a
// non-default tenant are visible only under that tenant, never the default one.
// (The store-level tenant keying is proven in the storage package; this pins the
// CLI flag pass-through so dropping --tenant from metricsImport reddens here.)
func TestMetricsImportTenantReachesStore(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	path := filepath.Join(t.TempDir(), "metrics.csv")
	if err := os.WriteFile(path, []byte("date,metric,quantity\n2026-05-01,requests,10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI([]string{"metrics", "import", "--path", path, "--tenant", "acme"}, ""); err != nil {
		t.Fatalf("metrics import --tenant acme: %v\n%s", err, out)
	}

	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer func() { _ = store.Close() }()
	acme, err := store.DailyBusinessMetricQuantities(context.Background(), "acme", "requests", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyBusinessMetricQuantities(acme): %v", err)
	}
	if len(acme) != 1 || acme[0].Quantity.String() != "10" {
		t.Fatalf("acme quantities = %+v, want the imported row (10)", acme)
	}
	def, err := store.DailyBusinessMetricQuantities(context.Background(), "default", "requests", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyBusinessMetricQuantities(default): %v", err)
	}
	if len(def) != 0 {
		t.Fatalf("default tenant sees %+v, want none (the --tenant flag homed the rows under acme)", def)
	}
}

// TestAllocationValidateCLI covers `costroid allocation validate`: a valid file
// prints a one-line summary naming the dimension and rule count; an invalid file
// and a missing file each exit non-zero with an actionable message. Every case
// passes --rules explicitly, so none reads the developer's config dir or ambient
// env.
func TestAllocationValidateCLI(t *testing.T) {
	writeRules := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "allocation.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing rules file: %v", err)
		}
		return p
	}

	t.Run("valid file prints a summary", func(t *testing.T) {
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[
			{"label":"platform","match":[{"dimension":"service_name","operator":"starts_with","value":"Amazon EC2"}]},
			{"label":"data","match":[{"dimension":"tag:env","operator":"equals","value":"prod"}]}
		]}]}`)
		out, err := runCLI([]string{"allocation", "validate", "--rules", rules}, "")
		if err != nil {
			t.Fatalf("allocation validate (valid): %v\n%s", err, out)
		}
		if !strings.Contains(out, `dimension "team"`) || !strings.Contains(out, "2 rule(s)") {
			t.Errorf("summary = %q, want the dimension name and rule count", out)
		}
	})

	t.Run("invalid file exits non-zero with an actionable message", func(t *testing.T) {
		rules := writeRules(t, `{"dimensions":[{"name":"team","rules":[{"label":"Unallocated","match":[{"dimension":"service_name","operator":"exists"}]}]}]}`)
		_, err := runCLI([]string{"allocation", "validate", "--rules", rules}, "")
		if err == nil {
			t.Fatal("allocation validate (invalid) = nil error, want non-zero exit")
		}
		if !strings.Contains(err.Error(), "Unallocated") || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("error = %v, want the reserved-label message", err)
		}
	})

	t.Run("missing file exits non-zero", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.json")
		wantOS := "no such file"
		if runtime.GOOS == "windows" {
			wantOS = "cannot find the file"
		}
		if _, err := runCLI([]string{"allocation", "validate", "--rules", missing}, ""); err == nil {
			t.Fatal("allocation validate (missing) = nil error, want non-zero exit")
		} else if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), wantOS) {
			t.Errorf("error = %v, want missing path and actionable OS message", err)
		}
	})
}

// TestUsageDocumentsAllocation pins that the top-level usage/help text documents
// both the serve --allocation-rules flag and the allocation subcommand (surfaced
// via the no-command error that appends the usage string).
func TestUsageDocumentsAllocation(t *testing.T) {
	_, err := runCLI([]string{}, "")
	if err == nil {
		t.Fatal("no-command invocation should error with the usage text")
	}
	for _, want := range []string{"--allocation-rules", "allocation validate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("top-level usage does not document %q: %v", want, err)
		}
	}
}

func TestUsageDocumentsMetricsImport(t *testing.T) {
	_, err := runCLI([]string{}, "")
	if err == nil {
		t.Fatal("no-command invocation should error with usage")
	}
	for _, want := range []string{"costroid metrics import", "date,metric,quantity", "REPLACES", "header-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("top-level usage does not document %q: %v", want, err)
		}
	}
}

func TestMetricsImportCLISummary(t *testing.T) {
	t.Setenv("COSTROID_DATA_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "metrics.csv")
	if err := os.WriteFile(path, []byte("date,metric,quantity\n2026-05-02,requests,10\n2026-05-01,customers,2\n2026-05-03,requests,12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI([]string{"metrics", "import", "--path", path, "--source-label", "business"}, "")
	if err != nil {
		t.Fatalf("metrics import: %v\n%s", err, out)
	}
	for _, want := range []string{"3 business metric row(s)", "2 metric(s)", "2026-05-01 through 2026-05-03", `source label "business"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

// --- store encrypt / rekey / decrypt ---

const (
	cliStoreKeyA = "cli-store-key-a's-quote"
	cliStoreKeyB = "cli-store-key-b's-quote"
	cliStoreKeyC = "cli-store-env-key's-quote"
	cliExactCost = "0.123456789012345678"
)

func writeKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

func assertCLINoKeyLeak(t *testing.T, out string, err error, secrets ...string) {
	t.Helper()
	blob := out
	if err != nil {
		blob += "\n" + err.Error()
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(blob, secret) {
			t.Fatalf("CLI output/error leaked secret %q:\n%s", secret, blob)
		}
	}
	for _, forbidden := range []string{"ATTACH ", "(ENCRYPTION_KEY '"} {
		if strings.Contains(blob, forbidden) {
			t.Fatalf("CLI output/error leaked %q:\n%s", forbidden, blob)
		}
	}
}

func writeGzipFocusCSV(t *testing.T, billedCost string) string {
	t.Helper()
	// Minimal AWS FOCUS 1.2 gzip CSV (gzip-mandatory for aws-focus).
	csv := strings.Join([]string{
		"BilledCost,EffectiveCost,ListCost,ContractedCost,BillingCurrency,BillingAccountId," +
			"BillingPeriodStart,BillingPeriodEnd,ChargePeriodStart,ChargePeriodEnd,ChargeCategory," +
			"ServiceName,ServiceCategory,ProviderName,PublisherName,InvoiceIssuerName",
		fmt.Sprintf("%s,%s,%s,%s,USD,999999999999,"+
			"2026-05-01T00:00:00Z,2026-06-01T00:00:00Z,2026-05-01T00:00:00Z,2026-05-02T00:00:00Z,Usage,"+
			"Amazon Elastic Compute Cloud,Compute,AWS,AWS,\"Amazon Web Services, Inc.\"",
			billedCost, billedCost, billedCost, billedCost),
		"",
	}, "\n")
	path := filepath.Join(t.TempDir(), "convert-export.csv.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create gzip: %v", err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(csv)); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return path
}

func removeStoreBak(t *testing.T, dataDir string) {
	t.Helper()
	live := filepath.Join(dataDir, storage.DatabaseFile)
	for _, p := range []string{live + ".bak", live + ".bak.wal"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", p, err)
		}
	}
}

func assertExactCostViaOpen(t *testing.T, dataDir, key, want string) {
	t.Helper()
	ctx := context.Background()
	var opts []storage.Option
	if key != "" {
		opts = append(opts, storage.WithEncryptionKey(key))
	}
	store, err := storage.Open(ctx, dataDir, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	costs, err := store.DailyCostsByService(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatalf("DailyCostsByService: %v", err)
	}
	if len(costs.Days) != 1 || len(costs.Days[0].Services) != 1 ||
		costs.Days[0].Services[0].Cost.String() != want {
		t.Fatalf("costs = %+v, want %s", costs, want)
	}
}

func TestStoreEncryptionFullChain(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv(dbEncryptionKeyFileEnvVar, "")

	exportPath := writeGzipFocusCSV(t, cliExactCost)
	out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportPath}, "")
	if err != nil {
		t.Fatalf("seed ingest: %v\n%s", err, out)
	}

	keyA := writeKeyFile(t, cliStoreKeyA)
	keyB := writeKeyFile(t, cliStoreKeyB)

	// encrypt
	out, err = runCLI([]string{"store", "encrypt", "--new-db-encryption-key-file", keyA}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil {
		t.Fatalf("encrypt: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("encrypt lines = %d (%q), want 3", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "encrypted the Costroid store in "+dataDir+": verified ") ||
		!strings.Contains(lines[0], " tables, ") || !strings.Contains(lines[0], " rows") {
		t.Fatalf("encrypt line0 = %q", lines[0])
	}
	bak := filepath.Join(dataDir, storage.DatabaseFile+".bak")
	wantWarn := "WARNING: the ORIGINAL PLAINTEXT database remains at " + bak +
		" - remove it once you have verified the encrypted store (note: deleting a file does not securely erase it on every filesystem)"
	if lines[1] != wantWarn {
		t.Fatalf("encrypt line1 = %q\nwant %q", lines[1], wantWarn)
	}
	wantNext := "next: run costroid with --db-encryption-key-file or $COSTROID_DB_ENCRYPTION_KEY_FILE pointing at this key file"
	if lines[2] != wantNext {
		t.Fatalf("encrypt line2 = %q", lines[2])
	}
	// Read: unchanged re-ingest short-circuit with key env + exact cost.
	t.Setenv(dbEncryptionKeyFileEnvVar, keyA)
	out, err = runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportPath}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil {
		t.Fatalf("read after encrypt: %v\n%s", err, out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Fatalf("expected unchanged short-circuit after encrypt:\n%s", out)
	}
	assertExactCostViaOpen(t, dataDir, cliStoreKeyA, cliExactCost)

	// rekey
	removeStoreBak(t, dataDir)
	out, err = runCLI([]string{
		"store", "rekey",
		"--db-encryption-key-file", keyA,
		"--new-db-encryption-key-file", keyB,
	}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil {
		t.Fatalf("rekey: %v\n%s", err, out)
	}
	lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("rekey lines = %d (%q), want 3", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "re-keyed the Costroid store in "+dataDir+": verified ") {
		t.Fatalf("rekey line0 = %q", lines[0])
	}
	wantBak := "the backup at " + bak + " is encrypted with the PREVIOUS key - remove it once you have verified the re-keyed store"
	if lines[1] != wantBak {
		t.Fatalf("rekey line1 = %q\nwant %q", lines[1], wantBak)
	}
	wantRekeyNext := "next: point --db-encryption-key-file / $COSTROID_DB_ENCRYPTION_KEY_FILE at the NEW key file before running costroid"
	if lines[2] != wantRekeyNext {
		t.Fatalf("rekey line2 = %q", lines[2])
	}
	t.Setenv(dbEncryptionKeyFileEnvVar, keyB)
	out, err = runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportPath}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("read after rekey: %v\n%s", err, out)
	}
	assertExactCostViaOpen(t, dataDir, cliStoreKeyB, cliExactCost)

	// decrypt
	removeStoreBak(t, dataDir)
	out, err = runCLI([]string{
		"store", "decrypt",
		"--db-encryption-key-file", keyB,
		"--allow-plaintext",
	}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil {
		t.Fatalf("decrypt: %v\n%s", err, out)
	}
	lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("decrypt lines = %d (%q), want 3", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "decrypted the Costroid store in "+dataDir+": verified ") {
		t.Fatalf("decrypt line0 = %q", lines[0])
	}
	wantDec := "the store is now PLAINTEXT on disk; the backup at " + bak + " is encrypted with the previous key"
	if lines[1] != wantDec {
		t.Fatalf("decrypt line1 = %q\nwant %q", lines[1], wantDec)
	}
	wantDecNext := "next: unset --db-encryption-key-file / $COSTROID_DB_ENCRYPTION_KEY_FILE before running costroid"
	if lines[2] != wantDecNext {
		t.Fatalf("decrypt line2 = %q", lines[2])
	}
	t.Setenv(dbEncryptionKeyFileEnvVar, "")
	out, err = runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportPath}, "")
	assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
	if err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("keyless read after decrypt: %v\n%s", err, out)
	}
	assertExactCostViaOpen(t, dataDir, "", cliExactCost)
}

func TestStoreEncryptionFlagMatrix(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("COSTROID_DATA_DIR", dataDir)
	t.Setenv(dbEncryptionKeyFileEnvVar, "")

	exportPath := writeGzipFocusCSV(t, cliExactCost)
	if out, err := runCLI([]string{"ingest", "--connector", "aws-focus", "--path", exportPath}, ""); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	keyA := writeKeyFile(t, cliStoreKeyA)
	keyB := writeKeyFile(t, cliStoreKeyB)
	keyEnv := writeKeyFile(t, cliStoreKeyC)

	t.Run("encrypt missing new key flag", func(t *testing.T) {
		out, err := runCLI([]string{"store", "encrypt"}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA)
		if err == nil || !strings.Contains(err.Error(), "--new-db-encryption-key-file") {
			t.Fatalf("err = %v out = %q", err, out)
		}
	})

	t.Run("encrypt rejects current-key flag", func(t *testing.T) {
		out, err := runCLI([]string{
			"store", "encrypt",
			"--db-encryption-key-file", keyA,
			"--new-db-encryption-key-file", keyB,
		}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
		if !errors.Is(err, errReported) {
			t.Fatalf("err = %v, want errReported", err)
		}
		if !strings.Contains(out, "flag provided but not defined: -db-encryption-key-file") {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("encrypt ignores env current key", func(t *testing.T) {
		// Env key DIFFERS from the new key; encrypt must still succeed.
		t.Setenv(dbEncryptionKeyFileEnvVar, keyEnv)
		out, err := runCLI([]string{"store", "encrypt", "--new-db-encryption-key-file", keyA}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyC)
		if err != nil {
			t.Fatalf("encrypt with stale env: %v\n%s", err, out)
		}
		assertExactCostViaOpen(t, dataDir, cliStoreKeyA, cliExactCost)
		// Opening with the env key must fail as wrong key.
		_, openErr := storage.Open(context.Background(), dataDir, storage.WithEncryptionKey(cliStoreKeyC))
		if openErr == nil || !strings.Contains(openErr.Error(), "encrypted and the provided key is wrong") {
			t.Fatalf("open with env key err = %v", openErr)
		}
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
	})

	// Store is now encrypted with keyA; remove bak for later steps.
	removeStoreBak(t, dataDir)

	t.Run("rekey missing new key flag", func(t *testing.T) {
		out, err := runCLI([]string{"store", "rekey", "--db-encryption-key-file", keyA}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA)
		if err == nil || !strings.Contains(err.Error(), "--new-db-encryption-key-file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rekey without current key source", func(t *testing.T) {
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		out, err := runCLI([]string{"store", "rekey", "--new-db-encryption-key-file", keyB}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyB)
		want := "the current db-encryption key is required: pass --db-encryption-key-file or set $COSTROID_DB_ENCRYPTION_KEY_FILE"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("decrypt without current key source", func(t *testing.T) {
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		out, err := runCLI([]string{"store", "decrypt", "--allow-plaintext"}, "")
		assertCLINoKeyLeak(t, out, err)
		want := "the current db-encryption key is required: pass --db-encryption-key-file or set $COSTROID_DB_ENCRYPTION_KEY_FILE"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("decrypt without allow-plaintext", func(t *testing.T) {
		out, err := runCLI([]string{"store", "decrypt", "--db-encryption-key-file", keyA}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA)
		if err == nil || !strings.Contains(err.Error(), "--allow-plaintext") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "plaintext") {
			t.Fatalf("err = %v, want plaintext explanation", err)
		}
	})

	t.Run("empty key files fail closed", func(t *testing.T) {
		empty := filepath.Join(t.TempDir(), "empty.key")
		if err := os.WriteFile(empty, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		want := "the db-encryption key file " + empty + " is empty or unreadable"
		// rekey with empty current key file
		out, err := runCLI([]string{
			"store", "rekey",
			"--db-encryption-key-file", empty,
			"--new-db-encryption-key-file", keyB,
		}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyB)
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		// empty new key file
		out, err = runCLI([]string{
			"store", "rekey",
			"--db-encryption-key-file", keyA,
			"--new-db-encryption-key-file", empty,
		}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA)
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
		// encrypt empty new key file (on a fresh plaintext dir)
		plainDir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", plainDir)
		store, openErr := storage.Open(context.Background(), plainDir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		out, err = runCLI([]string{"store", "encrypt", "--new-db-encryption-key-file", empty}, "")
		assertCLINoKeyLeak(t, out, err)
		if err == nil || err.Error() != want {
			t.Fatalf("encrypt empty key err = %v, want %q", err, want)
		}
	})
}

func TestStoreEncryptionDirectionAdvice(t *testing.T) {
	t.Run("encrypt on encrypted", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		keyA := writeKeyFile(t, cliStoreKeyA)
		keyB := writeKeyFile(t, cliStoreKeyB)
		store, err := storage.Open(context.Background(), dir, storage.WithEncryptionKey(cliStoreKeyA))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := runCLI([]string{"store", "encrypt", "--new-db-encryption-key-file", keyB}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
		if err == nil || !strings.Contains(err.Error(), "costroid store rekey") {
			t.Fatalf("err = %v", err)
		}
		_ = keyA
	})

	t.Run("rekey on plaintext", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		keyA := writeKeyFile(t, cliStoreKeyA)
		keyB := writeKeyFile(t, cliStoreKeyB)
		store, err := storage.Open(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := runCLI([]string{
			"store", "rekey",
			"--db-encryption-key-file", keyA,
			"--new-db-encryption-key-file", keyB,
		}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA, cliStoreKeyB)
		if err == nil || !strings.Contains(err.Error(), "costroid store encrypt") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("decrypt on plaintext", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("COSTROID_DATA_DIR", dir)
		t.Setenv(dbEncryptionKeyFileEnvVar, "")
		keyA := writeKeyFile(t, cliStoreKeyA)
		store, err := storage.Open(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := runCLI([]string{
			"store", "decrypt",
			"--db-encryption-key-file", keyA,
			"--allow-plaintext",
		}, "")
		assertCLINoKeyLeak(t, out, err, cliStoreKeyA)
		if err == nil || err.Error() != "the store is already plaintext - nothing to decrypt" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestStoreUsageText(t *testing.T) {
	err := run(nil)
	if err == nil {
		t.Fatal("run(nil) = nil")
	}
	for _, want := range []string{
		"store",
		"encrypt",
		"rekey",
		"decrypt",
		"--allow-plaintext",
		"costroid.duckdb.bak",
		"stop 'costroid serve'",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("top-level usage missing %q", want)
		}
	}
	// store without subcommand prints storeUsage via the error.
	out, err := runCLI([]string{"store"}, "")
	if err == nil {
		t.Fatal("store with no subcommand succeeded")
	}
	blob := err.Error() + out
	for _, want := range []string{
		"offline",
		"costroid.duckdb.bak",
		"--allow-plaintext",
		"costroid serve",
	} {
		if !strings.Contains(blob, want) {
			t.Errorf("store usage missing %q:\n%s", want, blob)
		}
	}
}
