// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/devtools/fakes3"
	"github.com/Costroid/costroid/internal/storage"
)

const s3Fixture = "../../testdata/aws-focus-s3/fixture"

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
	rehomed, err := store.DailyCostsByService(ctx, "acme", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService(acme): %v", err)
	}
	if len(rehomed.Days) != 14 {
		t.Fatalf("tenant acme sees %d day(s), want all 14 re-homed", len(rehomed.Days))
	}
	old, err := store.DailyCostsByService(ctx, "default", time.Time{}, time.Time{})
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

func TestResolveAddr(t *testing.T) {
	tests := []struct {
		name     string
		flagAddr string
		envAddr  string
		want     string
	}{
		{name: "default", flagAddr: "", envAddr: "", want: ":8080"},
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
