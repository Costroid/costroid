// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command demofixtures captures the static demo dashboard's API fixtures.
//
// It seeds an isolated synthetic store exactly as `costroid demo` does (the
// real demodata.Seed with a PINNED asOf, the demo allocation rules, read-only
// + demo handler options), issues every dashboard request across the demo
// preset date ranges and groupings, and writes each response verbatim into
// web/src/demo/fixtures/. It also generates web/src/demo/ranges.ts with the
// preset ranges pinned to the same asOf. Re-running reproduces both
// byte-identically. The web `--mode demo` build serves these fixtures with no
// backend.
//
// Usage: go run ./internal/demofixtures [-out web/src/demo]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/demodata"
	"github.com/Costroid/costroid/internal/storage"
)

// captureAsOf pins the synthetic window so the fixtures are byte-reproducible.
// It is UTC midnight so demodata.Window computes a stable [start, end].
var captureAsOf = time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

// demoVersion is the instance version surfaced in the meta fixture. Pinned so
// the fixture never drifts with the build's -ldflags version.
const demoVersion = "0.1.0"

var nonSlugCharacters = regexp.MustCompile(`[^a-z0-9]+`)

type demoSyncSchedule []demodata.SyncScheduleSource

func (s demoSyncSchedule) SyncSchedule() []api.SyncScheduleSource {
	result := make([]api.SyncScheduleSource, len(s))
	for i, source := range s {
		result[i] = api.SyncScheduleSource(source)
	}
	return result
}

// preset is one capturable demo date range, pinned to captureAsOf.
type preset struct {
	id    string
	label string
	start time.Time
	end   time.Time
}

func presets(asOf time.Time) []preset {
	start, end := demodata.Window(asOf)
	return []preset{
		{id: "last30", label: "Last 30 days", start: end.AddDate(0, 0, -29), end: end},
		{id: "last90", label: "Last 90 days", start: end.AddDate(0, 0, -89), end: end},
		{id: "full", label: "Full window", start: start, end: end},
	}
}

func main() {
	outDir := flag.String("out", filepath.Join("web", "src", "demo"), "output directory for fixtures and ranges.ts")
	flag.Parse()

	if err := run(*outDir); err != nil {
		log.Fatalf("demofixtures: %v", err)
	}
}

func run(outDir string) error {
	ctx := context.Background()

	storeDir, err := os.MkdirTemp("", "costroid-demofixtures-")
	if err != nil {
		return fmt.Errorf("creating temp store: %w", err)
	}
	defer func() { _ = os.RemoveAll(storeDir) }()

	// Write the demo allocation rules so groupBy=allocation returns the
	// Production/Unallocated split instead of a 400, exactly as `costroid demo`.
	allocationPath := filepath.Join(storeDir, "allocation.json")
	if err := os.WriteFile(allocationPath, []byte(demodata.AllocationRules+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing allocation rules: %w", err)
	}

	store, err := storage.Open(ctx, storeDir)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() { _ = store.Close() }()

	if err := demodata.Seed(ctx, store, captureAsOf, demodata.DefaultSeed); err != nil {
		return fmt.Errorf("seeding demo data: %w", err)
	}

	var static fs.FS = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("demo")}}
	handler := api.NewHandler(demoVersion, static, store, allocationPath,
		api.WithReadOnly(), api.WithDemo(),
		api.WithSyncSchedule(demoSyncSchedule(demodata.SyncSchedule(captureAsOf))))

	fixturesDir := filepath.Join(outDir, "fixtures")
	if err := resetDir(fixturesDir); err != nil {
		return err
	}

	ps := presets(captureAsOf)
	groupings := []string{"service", "provider", "allocation", "subaccount", "region"}

	capture := func(name, path string) error {
		body, err := getOK(handler, path)
		if err != nil {
			return fmt.Errorf("capturing %s (%s): %w", name, path, err)
		}
		return os.WriteFile(filepath.Join(fixturesDir, name+".json"), body, 0o644)
	}

	// Range-independent fixtures.
	if err := capture("meta", "/api/v1/meta"); err != nil {
		return err
	}
	if err := capture("sync-status", "/api/v1/sync/status"); err != nil {
		return err
	}
	if err := capture("business-metrics", "/api/v1/business-metrics"); err != nil {
		return err
	}

	for _, p := range ps {
		rq := rangeQuery(p.start, p.end)
		for _, gb := range groupings {
			gbq := ""
			if gb != "service" {
				gbq = "&groupBy=" + gb
			}
			if err := capture(fmt.Sprintf("costs.%s.%s", p.id, gb), "/api/v1/costs/daily"+rq+gbq); err != nil {
				return err
			}
			if err := capture(fmt.Sprintf("costs-summary.%s.%s", p.id, gb), "/api/v1/costs/summary"+rq+gbq); err != nil {
				return err
			}
			if err := capture(fmt.Sprintf("anomalies.%s.%s", p.id, gb), "/api/v1/anomalies"+rq+gbq); err != nil {
				return err
			}
		}
		if err := capture("tokens."+p.id, "/api/v1/usage/tokens/daily"+rq); err != nil {
			return err
		}
		if err := capture("usage-metrics."+p.id, "/api/v1/usage/metrics/daily"+rq); err != nil {
			return err
		}
		econPath := "/api/v1/unit-economics/daily?metric=" + url.QueryEscape(demodata.BusinessMetricName()) +
			"&start=" + p.start.Format(time.DateOnly) + "&end=" + p.end.Format(time.DateOnly)
		if err := capture("unit-economics."+p.id, econPath); err != nil {
			return err
		}
		// Insights are range-only (no groupBy / provider); one fixture per preset.
		if err := capture("insights."+p.id, "/api/v1/insights"+rq); err != nil {
			return err
		}
	}

	tagKeys, err := capturedTagKeys(filepath.Join(fixturesDir, "costs.full.service.json"))
	if err != nil {
		return err
	}
	if err := validateTagKeySlugCollisions(tagKeys); err != nil {
		return err
	}
	for _, tagKey := range tagKeys {
		tagSlug := providerSlug(tagKey)
		tagQuery := "&groupBy=tag&tagKey=" + url.QueryEscape(tagKey)
		for _, p := range ps {
			rq := rangeQuery(p.start, p.end)
			if err := capture(fmt.Sprintf("costs.%s.tag.%s", p.id, tagSlug), "/api/v1/costs/daily"+rq+tagQuery); err != nil {
				return err
			}
			if err := capture(fmt.Sprintf("costs-summary.%s.tag.%s", p.id, tagSlug), "/api/v1/costs/summary"+rq+tagQuery); err != nil {
				return err
			}
			if err := capture(fmt.Sprintf("anomalies.%s.tag.%s", p.id, tagSlug), "/api/v1/anomalies"+rq+tagQuery); err != nil {
				return err
			}
		}
	}

	providers, err := capturedProviders(filepath.Join(fixturesDir, "costs.full.service.json"))
	if err != nil {
		return err
	}
	filteredDir := filepath.Join(fixturesDir, "filtered")
	if err := os.MkdirAll(filteredDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filteredDir, err)
	}
	for _, provider := range providers {
		providerQuery := "&provider=" + url.QueryEscape(provider)
		slug := providerSlug(provider)
		for _, p := range ps {
			rq := rangeQuery(p.start, p.end)
			for _, gb := range groupings {
				gbq := ""
				if gb != "service" {
					gbq = "&groupBy=" + gb
				}
				if err := capture(filepath.Join("filtered", fmt.Sprintf("costs.%s.%s.%s", p.id, gb, slug)), "/api/v1/costs/daily"+rq+gbq+providerQuery); err != nil {
					return err
				}
				if err := capture(filepath.Join("filtered", fmt.Sprintf("costs-summary.%s.%s.%s", p.id, gb, slug)), "/api/v1/costs/summary"+rq+gbq+providerQuery); err != nil {
					return err
				}
				if err := capture(filepath.Join("filtered", fmt.Sprintf("anomalies.%s.%s.%s", p.id, gb, slug)), "/api/v1/anomalies"+rq+gbq+providerQuery); err != nil {
					return err
				}
			}
			for _, tagKey := range tagKeys {
				tagSlug := providerSlug(tagKey)
				tagQuery := "&groupBy=tag&tagKey=" + url.QueryEscape(tagKey)
				if err := capture(filepath.Join("filtered", fmt.Sprintf("costs.%s.tag.%s.%s", p.id, tagSlug, slug)), "/api/v1/costs/daily"+rq+tagQuery+providerQuery); err != nil {
					return err
				}
				if err := capture(filepath.Join("filtered", fmt.Sprintf("costs-summary.%s.tag.%s.%s", p.id, tagSlug, slug)), "/api/v1/costs/summary"+rq+tagQuery+providerQuery); err != nil {
					return err
				}
				if err := capture(filepath.Join("filtered", fmt.Sprintf("anomalies.%s.tag.%s.%s", p.id, tagSlug, slug)), "/api/v1/anomalies"+rq+tagQuery+providerQuery); err != nil {
					return err
				}
			}
			econPath := "/api/v1/unit-economics/daily?metric=" + url.QueryEscape(demodata.BusinessMetricName()) +
				"&start=" + p.start.Format(time.DateOnly) + "&end=" + p.end.Format(time.DateOnly) + providerQuery
			if err := capture(filepath.Join("filtered", fmt.Sprintf("unit-economics.%s.%s", p.id, slug)), econPath); err != nil {
				return err
			}
		}
	}

	if err := writeRanges(filepath.Join(outDir, "ranges.ts"), ps); err != nil {
		return err
	}

	fmt.Printf("captured fixtures into %s and generated %s (asOf %s)\n",
		fixturesDir, filepath.Join(outDir, "ranges.ts"), captureAsOf.Format(time.DateOnly))
	return nil
}

type capturedDaily struct {
	TagKeys []string `json:"tagKeys"`
}

func capturedTagKeys(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading captured tag keys from %s: %w", path, err)
	}
	var response capturedDaily
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding captured tag keys from %s: %w", path, err)
	}
	return pinnedCapturedTagKeys(response)
}

func pinnedCapturedTagKeys(response capturedDaily) ([]string, error) {
	want := []string{"environment"}
	if !slices.Equal(response.TagKeys, want) {
		return nil, fmt.Errorf("captured tag keys = %q, want exactly %q", response.TagKeys, want)
	}
	return append([]string{}, response.TagKeys...), nil
}

func validateTagKeySlugCollisions(keys []string) error {
	seen := make(map[string]string, len(keys))
	for _, key := range keys {
		slug := providerSlug(key)
		if previous, ok := seen[slug]; ok && previous != key {
			return fmt.Errorf("captured tag keys %q and %q share slug %q", previous, key, slug)
		}
		seen[slug] = key
	}
	return nil
}

func capturedProviders(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading captured providers from %s: %w", path, err)
	}
	var response struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding captured providers from %s: %w", path, err)
	}

	expected := map[string]struct{}{
		"Amazon Web Services": {},
		"Anthropic":           {},
		"Google":              {},
		"Microsoft":           {},
		"OpenAI":              {},
	}
	if len(response.Providers) != len(expected) {
		return nil, fmt.Errorf("captured providers = %q, want exactly the five pinned providers", response.Providers)
	}
	seen := make(map[string]struct{}, len(response.Providers))
	for _, provider := range response.Providers {
		if _, ok := expected[provider]; !ok {
			return nil, fmt.Errorf("captured providers = %q, want exactly the five pinned providers", response.Providers)
		}
		if _, duplicate := seen[provider]; duplicate {
			return nil, fmt.Errorf("captured providers = %q, want exactly the five pinned providers", response.Providers)
		}
		seen[provider] = struct{}{}
	}
	return response.Providers, nil
}

func providerSlug(provider string) string {
	return strings.Trim(nonSlugCharacters.ReplaceAllString(strings.ToLower(provider), "-"), "-")
}

// rangeQuery mirrors web/src/range.ts for a present [start, end] range.
func rangeQuery(start, end time.Time) string {
	return "?start=" + start.Format(time.DateOnly) + "&end=" + end.Format(time.DateOnly)
}

func getOK(handler http.Handler, path string) ([]byte, error) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes(), nil
}

func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return nil
}

// writeRanges generates the demo-only ranges.ts consumed by the dashboard's
// demo-mode date control and the fixture-backed api.demo seam.
func writeRanges(path string, ps []preset) error {
	var b []byte
	add := func(s string) { b = append(b, s...) }
	add("// SPDX-License-Identifier: Apache-2.0\n")
	add("// Copyright 2026 The Costroid Authors\n")
	add("//\n")
	add("// Code generated by internal/demofixtures; DO NOT EDIT.\n")
	add("// Regenerate with `make demo-fixtures`.\n")
	add("//\n")
	add("// Demo-mode preset date ranges, pinned to the capture asOf (never the\n")
	add("// visitor's wall clock). A backendless demo can only serve the ranges whose\n")
	add("// fixtures were captured, so the date control offers these presets.\n")
	add("\n")
	add("export type DemoPresetId = \"last30\" | \"last90\" | \"full\";\n")
	add("\n")
	add("export type DemoPreset = {\n")
	add("  id: DemoPresetId;\n")
	add("  label: string;\n")
	add("  start: string;\n")
	add("  end: string;\n")
	add("};\n")
	add("\n")
	add("export const DEMO_PRESETS: DemoPreset[] = [\n")
	for _, p := range ps {
		add(fmt.Sprintf("  { id: %q, label: %q, start: %q, end: %q },\n",
			p.id, p.label, p.start.Format(time.DateOnly), p.end.Format(time.DateOnly)))
	}
	add("];\n")
	return os.WriteFile(path, b, 0o644)
}
