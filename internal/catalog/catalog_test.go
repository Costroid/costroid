package catalog

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

var requestDay = time.Date(2026, 11, 1, 11, 15, 0, 0, time.UTC)

func pinned(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func query(provider, model string, class focus.TokenClass) Query {
	return Query{Provider: provider, Model: model, Class: class, ServiceTier: TierStandard, Region: RegionGlobal, At: requestDay}
}

func TestEmbeddedReleaseCarriesTheTwoSheets(t *testing.T) {
	c := pinned(t)
	if c.Version() != "0.1.0" {
		t.Errorf("version %s", c.Version())
	}
	for provider, name := range map[string]string{"anthropic": "Anthropic", "openai": "OpenAI"} {
		got, err := c.ServiceProviderName(provider)
		if err != nil || got != name {
			t.Errorf("%s: %q %v", provider, got, err)
		}
	}
}

func TestLookupReadsThePublishedPrice(t *testing.T) {
	cases := []struct {
		provider, model string
		class           focus.TokenClass
		sku, price      string
	}{
		{"anthropic", "claude-haiku-5-5", focus.InputUncached, "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached", "0.1"},
		{"anthropic", "claude-haiku-5-5", focus.CacheWrite5m, "anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_5m", "0.125"},
		{"anthropic", "claude-haiku-5-5", focus.CacheWrite1h, "anthropic:claude-haiku-5-5:global:standard:to-100000:cache_write_1h", "0.2"},
		{"anthropic", "claude-opus-5-5", focus.Output, "anthropic:claude-opus-5-5:global:standard:all:output", "20"},
		{"openai", "gpt-5.6-sol", focus.CacheRead, "openai:gpt-5.6-sol:global:standard:to-272000:cache_read", "0.4"},
		{"openai", "gpt-5.6-sol", focus.CacheWrite30m, "openai:gpt-5.6-sol:global:standard:to-272000:cache_write_30m", "5"},
	}
	c := pinned(t)
	for _, tc := range cases {
		got, err := c.Lookup(query(tc.provider, tc.model, tc.class))
		if err != nil {
			t.Fatal(err)
		}
		if got.SkuPriceID != tc.sku || got.ListUnitPrice.String() != tc.price || got.Fallback {
			t.Errorf("%s %s: %+v", tc.model, tc.class, got)
		}
	}
}

func TestLongContextTierStartsAboveItsBound(t *testing.T) {
	c := pinned(t)
	q := query("anthropic", "claude-haiku-5-5", focus.Output)
	q.ContextInputTokens = 100000
	at, err := c.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	q.ContextInputTokens = 100001
	over, err := c.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if at.ListUnitPrice.String() != "0.5" || over.ListUnitPrice.String() != "2.5" {
		t.Errorf("at the bound %s, over it %s", at.ListUnitPrice, over.ListUnitPrice)
	}
}

func TestUnknownModelFallsToANonZeroTierAndSaysSo(t *testing.T) {
	c := pinned(t)
	got, err := c.Lookup(query("anthropic", "claude-unreleased-9", focus.Output))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fallback || got.ListUnitPrice.Sign() <= 0 || got.SkuPriceID != "anthropic:fallback:output" || got.FallbackBasis == "" {
		t.Errorf("%+v", got)
	}
}

func TestEveryClassAnAdapterEmitsHasAFallback(t *testing.T) {
	c := pinned(t)
	classes := map[string][]focus.TokenClass{
		"anthropic": {focus.InputUncached, focus.CacheRead, focus.CacheWrite5m, focus.CacheWrite1h, focus.Output},
		"openai":    {focus.InputUncached, focus.CacheRead, focus.CacheWrite30m, focus.Output},
	}
	for provider, list := range classes {
		for _, class := range list {
			got, err := c.Lookup(query(provider, "no-such-model", class))
			if err != nil {
				t.Fatalf("%s %s: %v", provider, class, err)
			}
			if !got.Fallback || got.ListUnitPrice.Sign() <= 0 {
				t.Errorf("%s %s: %+v", provider, class, got)
			}
		}
	}
}

func TestTierTheSheetDoesNotNameFallsBackRatherThanBorrowingStandard(t *testing.T) {
	q := query("anthropic", "claude-opus-5-5", focus.CacheRead)
	q.ServiceTier = "fast"
	got, err := pinned(t).Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fallback {
		t.Errorf("%+v", got)
	}
}

func TestDataResidencyMultipliesThePublishedPrice(t *testing.T) {
	q := query("anthropic", "claude-haiku-5-5", focus.Output)
	q.Region = "us"
	got, err := pinned(t).Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if got.ListUnitPrice.String() != "0.55" || got.ResidencyMultiplier == nil {
		t.Errorf("%+v", got)
	}
}

func TestEveryPublishedPriceFitsTheRowScaleForAnyTokenCount(t *testing.T) {
	c := pinned(t)
	const tokenScale = 6
	for provider, s := range c.sheets {
		for i := range s.Rows {
			r := &s.Rows[i]
			if r.ListUnitPrice == "" {
				continue
			}
			price, err := decimal.Parse(r.ListUnitPrice)
			if err != nil {
				t.Fatalf("%s %s: %v", provider, r.SkuPriceID, err)
			}
			if price.Scale()+1+tokenScale > focus.MaxScale {
				t.Errorf("%s has %d fractional digits", r.SkuPriceID, price.Scale())
			}
		}
	}
}

func TestSheetWithoutAFallbackRowIsAnErrorNotAZero(t *testing.T) {
	release := fstest.MapFS{"x.json": {Data: []byte(`{"Provider":"openai","ServiceProviderName":"OpenAI","rows":[]}`)}}
	c, err := Parse("0.0.1", release)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Lookup(query("openai", "gpt-5.6-sol", focus.Output))
	if !errors.Is(err, ErrNoPrice) {
		t.Errorf("err = %v", err)
	}
}
