package catalog

import (
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"

	catalogdata "github.com/Costroid/costroid/catalog"
)

// newestRelease is the embedded release with the highest version.
func newestRelease(t *testing.T) string {
	t.Helper()
	entries, err := fs.ReadDir(catalogdata.Sheets, ".")
	if err != nil {
		t.Fatal(err)
	}
	newest, highest := "", []int(nil)
	for _, entry := range entries {
		var version []int
		for _, part := range strings.Split(entry.Name(), ".") {
			number, err := strconv.Atoi(part)
			if err != nil {
				t.Fatalf("the release directory %s is not a version", entry.Name())
			}
			version = append(version, number)
		}
		if slices.Compare(version, highest) > 0 {
			newest, highest = entry.Name(), version
		}
	}
	return newest
}

// TestOneOwnerForEveryModelPrefix names the provider whose sheet prices a
// model by the start of its name: each prefix a reader of agent usage knows,
// in any case, and gemini- for Google's sheet. A model that no provider with
// a sheet publishes has no owner. Every model that the newest release's
// Anthropic, OpenAI and Google sheets price, or name in a multiplier, is
// owned by that sheet's provider.
func TestOneOwnerForEveryModelPrefix(t *testing.T) {
	for model, want := range map[string]string{
		"claude-haiku-5-5":   ProviderAnthropic,
		"Claude-Sonnet-4.6":  ProviderAnthropic,
		"CLAUDE":             ProviderAnthropic,
		"gpt-5.5":            ProviderOpenAI,
		"gpt-5.3-codex":      ProviderOpenAI,
		"chatgpt-4o-latest":  ProviderOpenAI,
		"chat-latest":        ProviderOpenAI,
		"codex-mini-latest":  ProviderOpenAI,
		"o1":                 ProviderOpenAI,
		"o3":                 ProviderOpenAI,
		"o3-pro":             ProviderOpenAI,
		"O4-mini":            ProviderOpenAI,
		"gemini-3.8-flash":   ProviderGoogle,
		"mai-code-1.1-flash": "",
		"grok-4.7":           "",
		"kimi-k3":            "",
		"composer-2.5":       "",
		"gemini":             "",
		"unknown":            "",
		"":                   "",
	} {
		if got := OwnerOf(model); got != want {
			t.Errorf("OwnerOf(%q) = %q, want %q", model, got, want)
		}
	}

	version := newestRelease(t)
	release, err := fs.Sub(catalogdata.Sheets, version)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(version, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{ProviderAnthropic, ProviderOpenAI, ProviderGoogle} {
		s, ok := c.sheets[provider]
		if !ok {
			t.Errorf("release %s holds no %s sheet", version, provider)
			continue
		}
		models := map[string]bool{}
		for _, r := range s.Rows {
			if r.RowType == "unit_price" {
				models[r.ModelID] = true
			}
			for _, model := range r.AppliesToModels {
				models[model] = true
			}
		}
		if len(models) == 0 {
			t.Errorf("the %s sheet of release %s names no model", provider, version)
		}
		for model := range models {
			if got := OwnerOf(model); got != provider {
				t.Errorf("the %s sheet of release %s prices %q, which OwnerOf gives to %q", provider, version, model, got)
			}
		}
	}
}
