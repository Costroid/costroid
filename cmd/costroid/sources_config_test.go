// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const validAllSourcesJSON = `{
  "defaultInterval": "24h",
  "sources": [
    {"name":"aws-file","connector":"aws-focus","path":"sample.csv.gz"},
    {"name":"aws-s3","connector":"aws-focus-s3","bucket":"billing","prefix":"exports/costroid"},
    {"name":"azure","connector":"azure-focus","accountURL":"https://example.blob.core.windows.net/","container":"billing","prefix":"exports/costroid"},
    {"name":"gcp","connector":"gcp-focus-bq","datasetProject":"billing-host","dataset":"focus","table":"export","location":"EU"},
    {"name":"anthropic","connector":"anthropic-cost","interval":"12h"},
    {"name":"openai","connector":"openai-cost","tenant":"acme"},
    {"name":"csv","connector":"focus-csv","path":"focus.csv","focusVersion":"1.4","sourceLabel":"upload","lenient":true}
  ]
}`

func TestParseSourcesStrictValidation(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantSources int
		wantError   string
	}{
		{name: "all seven connectors", body: validAllSourcesJSON, wantSources: 7},
		{name: "unknown top-level field", body: `{"sources":[],"typo":true}`, wantError: `unknown field "typo"`},
		{name: "unknown per-source field", body: `{"sources":[{"name":"aws","connector":"aws-focus","path":"x","typo":true}]}`, wantError: `unknown field "typo"`},
		{name: "scheduled force rejected", body: `{"sources":[{"name":"aws","connector":"aws-focus","path":"x","force":true}]}`, wantError: `unknown field "force"`},
		{name: "scheduled period rejected", body: `{"sources":[{"name":"aws","connector":"aws-focus","path":"x","period":"2026-06"}]}`, wantError: `unknown field "period"`},
		{name: "bad duration", body: `{"defaultInterval":"tomorrow","sources":[]}`, wantError: "must be a Go duration"},
		{name: "interval below minimum", body: `{"sources":[{"name":"aws","connector":"aws-focus","path":"x","interval":"14m"}]}`, wantError: "scheduled runs re-query the source"},
		{name: "invalid name", body: `{"sources":[{"name":"Bad_Name","connector":"aws-focus","path":"x"}]}`, wantError: "must match [a-z0-9-]+"},
		{name: "duplicate name", body: `{"sources":[{"name":"same","connector":"aws-focus","path":"x"},{"name":"same","connector":"aws-focus","path":"y"}]}`, wantError: "duplicated"},
		{name: "unknown connector", body: `{"sources":[{"name":"unknown","connector":"nope"}]}`, wantError: "unknown connector"},
		{name: "focus csv missing focusVersion", body: `{"sources":[{"name":"csv","connector":"focus-csv","path":"x"}]}`, wantError: `"focusVersion"`},
		{name: "focus csv unknown focusVersion", body: `{"sources":[{"name":"csv","connector":"focus-csv","path":"x","focusVersion":"9.9"}]}`, wantError: "supported values"},
		{name: "missing connector field", body: `{"sources":[{"name":"s3","connector":"aws-focus-s3","prefix":"exports"}]}`, wantError: `"bucket"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := parseSources(strings.NewReader(test.body))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("parseSources error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSources: %v", err)
			}
			if len(cfg.sources) != test.wantSources {
				t.Fatalf("sources = %d, want %d", len(cfg.sources), test.wantSources)
			}
			if cfg.sources[5].tenant != "acme" || cfg.sources[0].tenant != "default" {
				t.Fatalf("tenant defaults/override = %q/%q", cfg.sources[0].tenant, cfg.sources[5].tenant)
			}
			if cfg.sources[4].interval != 12*time.Hour || cfg.sources[0].interval != 24*time.Hour {
				t.Fatalf("interval override/default = %s/%s", cfg.sources[4].interval, cfg.sources[0].interval)
			}
		})
	}
}

func TestSharedSourceValidationCLIAndConfig(t *testing.T) {
	cliErr := ingestCmd([]string{"--connector", "aws-focus-s3", "--bucket", "billing"})
	if cliErr == nil {
		t.Fatal("CLI missing --prefix = nil error")
	}
	const wantCLI = "--bucket and --prefix are required for the aws-focus-s3 connector"
	if cliErr.Error() != wantCLI {
		t.Fatalf("CLI error = %q, want byte-identical %q", cliErr, wantCLI)
	}

	_, configErr := parseSources(strings.NewReader(`{"sources":[{"name":"s3","connector":"aws-focus-s3","bucket":"billing"}]}`))
	if configErr == nil {
		t.Fatal("config missing prefix = nil error")
	}
	if !strings.Contains(configErr.Error(), `"prefix"`) || strings.Contains(configErr.Error(), "--prefix") {
		t.Fatalf("config error does not use JSON field spelling: %v", configErr)
	}
	for path, err := range map[string]error{"CLI": cliErr, "config": configErr} {
		if !errors.Is(err, errInvalidSourceConfig) {
			t.Errorf("%s errors.Is(errInvalidSourceConfig) = false: %v", path, err)
		}
		var validation *sourceValidationError
		if !errors.As(err, &validation) {
			t.Errorf("%s errors.As(sourceValidationError) = false: %v", path, err)
			continue
		}
		if validation.connector != "aws-focus-s3" || !slices.Equal(validation.fields, []sourceField{sourceFieldPrefix}) {
			t.Errorf("%s validation = %+v", path, validation)
		}
	}
}

func TestResolveSourcesPath(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(sourcesEnvVar, "/env/sources.json")
		if got := resolveSourcesPath("/flag/sources.json"); got != "/flag/sources.json" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("env wins over default", func(t *testing.T) {
		t.Setenv(sourcesEnvVar, "/env/sources.json")
		if got := resolveSourcesPath(""); got != "/env/sources.json" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("config directory default", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", root)
		t.Setenv(sourcesEnvVar, "")
		want := filepath.Join(root, "costroid", "sources.json")
		if got := resolveSourcesPath(""); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestSourcesValidateCLI(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "sources.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	validPath := write(validAllSourcesJSON)
	out, err := runCLI([]string{"sources", "validate", "--sources", validPath}, "")
	if err != nil {
		t.Fatalf("valid sources: %v\n%s", err, out)
	}
	for _, want := range []string{"7 source(s)", "credential slots", "remote reachability"} {
		if !strings.Contains(out, want) {
			t.Errorf("valid output missing %q: %s", want, out)
		}
	}
	invalidPath := write(`{"sources":[{"name":"bad","connector":"aws-focus-s3"}]}`)
	out, err = runCLI([]string{"sources", "validate", "--sources", invalidPath}, "")
	if err == nil || !strings.Contains(fmt.Sprint(err), "bucket") {
		t.Fatalf("invalid sources error = %v\n%s", err, out)
	}
}

func TestServeConfigSyncOptInReadsSources(t *testing.T) {
	hermeticServeEnv(t)
	t.Setenv("COSTROID_AUTH_TOKEN", "test-token")
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(`{"sources":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sourcesEnvVar, path)
	if _, _, stop, err := serveConfig(nil); stop || err != nil {
		t.Fatalf("serveConfig without --sync read invalid sources: stop=%v err=%v", stop, err)
	}
	if _, _, stop, err := serveConfig([]string{"--sync"}); stop || err == nil || !strings.Contains(err.Error(), "sources") {
		t.Fatalf("serveConfig --sync invalid sources: stop=%v err=%v", stop, err)
	}
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{"sources":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := serveConfig([]string{"--sync", "--sources", empty}); err == nil || !strings.Contains(err.Error(), "empty sources array") {
		t.Fatalf("serveConfig --sync empty sources error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, _, _, err := serveConfig([]string{"--sync", "--sources", missing}); err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("serveConfig --sync missing sources error = %v", err)
	}
}

func TestServeConfigSyncDoesNotOpenCredentialVault(t *testing.T) {
	hermeticServeEnv(t)
	t.Setenv("COSTROID_AUTH_TOKEN", "test-token")
	t.Setenv("COSTROID_CREDENTIALS_KEY_FILE", filepath.Join(t.TempDir(), "missing-credentials.key"))
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(`{"sources":[{"name":"ai","connector":"anthropic-cost"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, stop, err := serveConfig([]string{"--sync", "--sources", path})
	if stop || err != nil || !cfg.sync {
		t.Fatalf("serveConfig should structurally parse without opening vault: cfg.sync=%v stop=%v err=%v", cfg.sync, stop, err)
	}
}

func TestUsageDocumentsScheduledSources(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("run(nil) = nil")
	} else {
		for _, want := range []string{"--sync", "--sources <path>", "costroid sources validate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("top-level usage missing %q", want)
			}
		}
	}
	out, err := runCLI([]string{"serve", "--help"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-sync", "-sources"} {
		if !strings.Contains(out, want) {
			t.Errorf("serve --help missing %q: %s", want, out)
		}
	}
	out, err = runCLI([]string{"sources", "validate", "--help"}, "")
	if err != nil || !strings.Contains(out, "-sources") {
		t.Fatalf("sources validate --help = %v\n%s", err, out)
	}
}
