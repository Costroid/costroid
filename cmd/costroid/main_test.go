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

	"github.com/Costroid/costroid/internal/devtools/fakeblob"
	"github.com/Costroid/costroid/internal/devtools/fakes3"
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
	rehomed, err := store.DailyCostsByService(ctx, "acme", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("DailyCostsByService(acme): %v", err)
	}
	if len(rehomed.Days) != 6 {
		t.Fatalf("tenant acme sees %d day(s), want all 6 re-homed", len(rehomed.Days))
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
		cfg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", cfg)    // pin os.UserConfigDir()
		t.Setenv(allocationRulesEnvVar, "") // and the env override
		want := filepath.Join(cfg, "costroid", "allocation.json")
		if got := resolveAllocationRulesPath(""); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
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
		if _, err := runCLI([]string{"allocation", "validate", "--rules", missing}, ""); err == nil {
			t.Fatal("allocation validate (missing) = nil error, want non-zero exit")
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
