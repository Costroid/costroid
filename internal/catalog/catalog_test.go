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
	for _, class := range []focus.TokenClass{focus.InputUncached, focus.CacheRead} {
		q := query("anthropic", "claude-haiku-5-5", class)
		q.ServiceTier = "fast"
		got, err := pinned(t).Lookup(q)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Fallback || got.CacheMultiplier != nil {
			t.Errorf("%s: %+v", class, got)
		}
	}
}

// TestFastModeCacheReadComposesTheMultiplier holds that a cache class the
// sheet prices at the fast tier only through its prompt_cache multiplier is
// the fast input price times that multiplier, under a price id that names the
// fast tier and the class, and that the multiplier a narrower row states for
// the model wins over the general one. Data residency stacks on top.
func TestFastModeCacheReadComposesTheMultiplier(t *testing.T) {
	cases := []struct {
		model      string
		class      focus.TokenClass
		region     string
		price      string
		multiplier string
	}{
		{"claude-opus-5-5", focus.CacheRead, RegionGlobal, "0.4", "anthropic:multiplier:prompt-cache-read-claude-opus-5-5"},
		{"claude-opus-5-5", focus.CacheWrite5m, RegionGlobal, "10", "anthropic:multiplier:prompt-cache-write-5m"},
		{"claude-opus-5-5", focus.CacheWrite1h, RegionGlobal, "16", "anthropic:multiplier:prompt-cache-write-1h"},
		{"claude-opus-5", focus.CacheRead, RegionGlobal, "1", "anthropic:multiplier:prompt-cache-read"},
		{"claude-opus-5-5", focus.CacheRead, "us", "0.44", "anthropic:multiplier:prompt-cache-read-claude-opus-5-5"},
	}
	c := pinned(t)
	for _, tc := range cases {
		q := query("anthropic", tc.model, tc.class)
		q.ServiceTier, q.Region = "fast", tc.region
		got, err := c.Lookup(q)
		if err != nil {
			t.Fatal(err)
		}
		sku := "anthropic:" + tc.model + ":global:fast:all:" + string(tc.class)
		if got.Fallback || got.ListUnitPrice.String() != tc.price || got.SkuPriceID != sku ||
			got.CacheMultiplier == nil || got.CacheMultiplier.SkuPriceID != tc.multiplier {
			t.Errorf("%s %s in %s: %+v, multiplier %+v", tc.model, tc.class, tc.region, got, got.CacheMultiplier)
		}
	}
	q := query("anthropic", "claude-opus-5-5", focus.CacheRead)
	q.ServiceTier = "fast"
	got, err := c.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if note := got.CacheMultiplier.CacheNote(focus.CacheRead); note != "input price times 0.05 for cache_read, by anthropic:multiplier:prompt-cache-read-claude-opus-5-5" {
		t.Errorf("note %q", note)
	}
	standard, err := c.Lookup(query("anthropic", "claude-opus-5-5", focus.CacheRead))
	if err != nil {
		t.Fatal(err)
	}
	if standard.CacheMultiplier != nil || standard.SkuPriceID != "anthropic:claude-opus-5-5:global:standard:all:cache_read" {
		t.Errorf("a class a row prices is read from the row: %+v", standard)
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

// knownExcess lists, per release, the unit_price rows the release's own
// fallback does not bound. Release 0.1.0's fallbacks were the highest
// standard-tier, short-context prices of the models then current, so the
// retired Opus 4 and 4.1 rows, the long-context and fast tiers of some OpenAI
// models, and o1-pro pass them. A later release takes the maximum over every
// row and lists none.
var knownExcess = map[string][]string{
	"0.1.0": {
		"anthropic:claude-opus-4-1-20250805:global:standard:all:input_uncached",
		"anthropic:claude-opus-4-1-20250805:global:standard:all:cache_write_5m",
		"anthropic:claude-opus-4-1-20250805:global:standard:all:cache_write_1h",
		"anthropic:claude-opus-4-1-20250805:global:standard:all:cache_read",
		"anthropic:claude-opus-4-1-20250805:global:standard:all:output",
		"anthropic:claude-opus-4-20250514:global:standard:all:input_uncached",
		"anthropic:claude-opus-4-20250514:global:standard:all:cache_write_5m",
		"anthropic:claude-opus-4-20250514:global:standard:all:cache_write_1h",
		"anthropic:claude-opus-4-20250514:global:standard:all:cache_read",
		"anthropic:claude-opus-4-20250514:global:standard:all:output",
		"openai:gpt-6-astra:global:standard:over-272000:cache_read",
		"openai:gpt-6-astra:global:standard:over-272000:cache_write_30m",
		"openai:gpt-6-astra:global:standard:over-272000:cache_write_12h",
		"openai:gpt-5.5-pro:global:standard:over-272000:input_uncached",
		"openai:gpt-5.5-pro:global:standard:over-272000:output",
		"openai:gpt-5.4-pro:global:standard:over-272000:input_uncached",
		"openai:gpt-5.4-pro:global:standard:over-272000:output",
		"openai:gpt-4o:global:standard:all:cache_read",
		"openai:o1:global:standard:all:cache_read",
		"openai:o1-pro:global:standard:all:input_uncached",
		"openai:o1-pro:global:standard:all:output",
		"openai:o1-pro:global:batch:all:input_uncached",
		"openai:o1-pro:global:batch:all:output",
		"openai:gpt-6-astra:global:fast:over-272000:input_uncached",
		"openai:gpt-6-astra:global:fast:to-272000:cache_read",
		"openai:gpt-6-astra:global:fast:over-272000:cache_read",
		"openai:gpt-6-astra:global:fast:to-272000:cache_write_30m",
		"openai:gpt-6-astra:global:fast:over-272000:cache_write_30m",
		"openai:gpt-6-astra:global:fast:to-272000:cache_write_12h",
		"openai:gpt-6-astra:global:fast:over-272000:cache_write_12h",
		"openai:gpt-5.6-sol:global:fast:over-272000:cache_read",
		"openai:gpt-5.6-sol:global:fast:over-272000:cache_write_30m",
		"openai:gpt-5.6-sol:global:fast:over-272000:cache_write_12h",
		"openai:gpt-5.5:global:fast:to-272000:cache_read",
		"openai:gpt-4o:global:fast:all:cache_read",
		"openai:gpt-6-astra:global:ultrafast:to-272000:input_uncached",
		"openai:gpt-6-astra:global:ultrafast:over-272000:input_uncached",
		"openai:gpt-6-astra:global:ultrafast:to-272000:output",
		"openai:gpt-6-astra:global:ultrafast:over-272000:output",
		"openai:gpt-6-astra:global:ultrafast:to-272000:cache_read",
		"openai:gpt-6-astra:global:ultrafast:over-272000:cache_read",
		"openai:gpt-6-astra:global:ultrafast:to-272000:cache_write_30m",
		"openai:gpt-6-astra:global:ultrafast:over-272000:cache_write_30m",
		"openai:gpt-6-astra:global:ultrafast:to-272000:cache_write_12h",
		"openai:gpt-6-astra:global:ultrafast:over-272000:cache_write_12h",
		"openai:gpt-6.1-sol:global:ultrafast:over-272000:cache_read",
		"openai:gpt-6.1-sol:global:ultrafast:to-272000:cache_write_30m",
		"openai:gpt-6.1-sol:global:ultrafast:over-272000:cache_write_30m",
		"openai:gpt-6.1-sol:global:ultrafast:to-272000:cache_write_12h",
		"openai:gpt-6.1-sol:global:ultrafast:over-272000:cache_write_12h",
		"openai:gpt-5.6-cyber:global:standard:to-272000:cache_read",
		"openai:gpt-5.6-cyber:global:standard:to-272000:cache_write_30m",
		"openai:gpt-5.6-cyber:global:standard:to-272000:cache_write_12h",
		"openai:gpt-5.5-cyber:global:standard:to-272000:cache_read",
		"openai:gpt-6-rosalind:global:standard:to-272000:cache_read",
		"openai:gpt-6-rosalind:global:standard:over-272000:cache_read",
		"openai:gpt-6-rosalind:global:standard:to-272000:cache_write_30m",
		"openai:gpt-6-rosalind:global:standard:over-272000:cache_write_30m",
		"openai:gpt-6-rosalind:global:standard:to-272000:cache_write_12h",
		"openai:gpt-6-rosalind:global:standard:over-272000:cache_write_12h",
	},
}

// classPrice is one price a sheet answers for one class.
type classPrice struct {
	class string
	Price
}

// everyPrice is every price a lookup can answer from a sheet without its
// fallback: each unit_price row, and each cache class composed from a row's
// input price and a prompt_cache multiplier. Each is asked on its own first
// day, inside its own context tier.
func everyPrice(t *testing.T, c *Catalog, provider string, s *sheet) []classPrice {
	t.Helper()
	var prices []classPrice
	for i := range s.Rows {
		r := &s.Rows[i]
		if r.RowType != "unit_price" {
			continue
		}
		price, err := decimal.Parse(r.ListUnitPrice)
		if err != nil {
			t.Fatalf("%s: %v", r.SkuPriceID, err)
		}
		prices = append(prices, classPrice{r.TokenClass, Price{SkuPriceID: r.SkuPriceID, ListUnitPrice: price, PricingUnit: r.PricingUnit}})
		if r.TokenClass != string(focus.InputUncached) {
			continue
		}
		for _, class := range []focus.TokenClass{focus.CacheRead, focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m} {
			q := Query{Provider: provider, Model: r.ModelID, Class: class, ServiceTier: r.ServiceTier, Region: RegionGlobal,
				ContextInputTokens: insideTier(r), At: firstDay(t, r)}
			got, err := c.Lookup(q)
			if errors.Is(err, ErrNoPrice) {
				continue
			}
			if err != nil {
				t.Fatalf("%s %s: %v", r.SkuPriceID, class, err)
			}
			if got.CacheMultiplier != nil {
				prices = append(prices, classPrice{string(class), got})
			}
		}
	}
	return prices
}

func insideTier(r *row) int64 {
	if r.ContextTierMinInputTokens != nil {
		return *r.ContextTierMinInputTokens + 1
	}
	return 0
}

func firstDay(t *testing.T, r *row) time.Time {
	t.Helper()
	day, err := time.Parse(dayLayout, r.EffectiveStart)
	if err != nil {
		t.Fatalf("%s: start %v", r.SkuPriceID, err)
	}
	return day
}

// TestFallbackBoundsEveryPrice holds that each embedded sheet's fallback for
// a class is at or above every price the sheet states or composes for that
// class, so a model or tier the sheet does not name is never priced under one
// it does. A class with no fallback row is refused, not priced, and is not
// held here. The release's known excess is listed above, and each listed row
// must still exceed, so the list cannot outlive the release it names.
func TestFallbackBoundsEveryPrice(t *testing.T) {
	c := pinned(t)
	excess := map[string]bool{}
	for _, id := range knownExcess[c.Version()] {
		excess[id] = false
	}
	checked := 0
	for provider, s := range c.sheets {
		fallbacks := map[string]decimal.Decimal{}
		for i := range s.Rows {
			if r := &s.Rows[i]; r.RowType == "fallback" {
				fallbacks[r.TokenClass] = decimal.MustParse(r.ListUnitPrice)
			}
		}
		for _, price := range everyPrice(t, c, provider, s) {
			class := price.class
			bound, ok := fallbacks[class]
			if !ok {
				continue
			}
			checked++
			_, listed := excess[price.SkuPriceID]
			switch over := price.ListUnitPrice.Cmp(bound) > 0; {
			case over && listed:
				excess[price.SkuPriceID] = true
			case over:
				t.Errorf("%s at %s passes the %s fallback of %s", price.SkuPriceID, price.ListUnitPrice, class, bound)
			case listed:
				t.Errorf("%s at %s is listed as known excess but is within the fallback of %s", price.SkuPriceID, price.ListUnitPrice, bound)
			}
		}
	}
	for id, seen := range excess {
		if !seen {
			t.Errorf("%s is listed as known excess of release %s and no such price exists", id, c.Version())
		}
	}
	if checked == 0 {
		t.Error("no price was checked")
	}
}

// overlapsInContext reports whether some input falls in both rows' context
// tiers, as the lookup reads them: above min, at or below max, open on a nil
// side.
func overlapsInContext(a, b *row) bool {
	low, lowSet := a.ContextTierMinInputTokens, a.ContextTierMinInputTokens != nil
	if b.ContextTierMinInputTokens != nil && (!lowSet || *b.ContextTierMinInputTokens > *low) {
		low, lowSet = b.ContextTierMinInputTokens, true
	}
	high, highSet := a.ContextTierMaxInputTokens, a.ContextTierMaxInputTokens != nil
	if b.ContextTierMaxInputTokens != nil && (!highSet || *b.ContextTierMaxInputTokens < *high) {
		high, highSet = b.ContextTierMaxInputTokens, true
	}
	return !lowSet || !highSet || *low < *high
}

// overlapsInDays reports whether some day holds both rows, as the lookup
// reads their dates.
func (s *sheet) overlapsInDays(a, b *row) bool {
	start := max(s.startsOn(a), s.startsOn(b))
	for _, end := range []*string{a.EffectiveEnd, b.EffectiveEnd} {
		if end != nil && *end <= start {
			return false
		}
	}
	return true
}

// TestNoTwoPriceRowsOverlap holds that no two unit_price rows of an embedded
// sheet can answer one query: none share a model, tier, region and class with
// context tiers and days that meet. The lookup takes the first row that
// answers, so an overlap would price by row order.
func TestNoTwoPriceRowsOverlap(t *testing.T) {
	c := pinned(t)
	pairs := 0
	for _, s := range c.sheets {
		for i := range s.Rows {
			a := &s.Rows[i]
			if a.RowType != "unit_price" {
				continue
			}
			for j := i + 1; j < len(s.Rows); j++ {
				b := &s.Rows[j]
				if b.RowType != "unit_price" || a.ModelID != b.ModelID || a.ServiceTier != b.ServiceTier ||
					a.RegionID != b.RegionID || a.TokenClass != b.TokenClass {
					continue
				}
				pairs++
				if overlapsInContext(a, b) && s.overlapsInDays(a, b) {
					t.Errorf("%s and %s answer the same query", a.SkuPriceID, b.SkuPriceID)
				}
			}
		}
	}
	if pairs == 0 {
		t.Error("no two rows share a model, tier, region and class, so nothing was compared")
	}
}

// TestTwoSheetsForOneProviderAreRefused holds that one release holds one
// sheet per provider, and that two releases, each with its own sheet for one
// provider, each parse.
func TestTwoSheetsForOneProviderAreRefused(t *testing.T) {
	sheet := func(date string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"Provider":"openai","ServiceProviderName":"OpenAI","SheetDate":"` + date + `","rows":[]}`)}
	}
	release := fstest.MapFS{"openai-2026-11-01.json": sheet("2026-11-01"), "openai-2026-11-07.json": sheet("2026-11-07")}
	_, err := Parse("0.0.1", release)
	if err == nil || err.Error() != "catalog: release 0.0.1 holds two sheets for openai: openai-2026-11-01.json and openai-2026-11-07.json" {
		t.Errorf("err = %v", err)
	}
	for version, file := range map[string]string{"0.0.1": "openai-2026-11-01.json", "0.0.2": "openai-2026-11-07.json"} {
		if _, err := Parse(version, fstest.MapFS{file: release[file]}); err != nil {
			t.Errorf("release %s alone: %v", version, err)
		}
	}
}

// TestRowStartingAfterTheSheetDateHoldsFromItsStart holds that a price the
// provider announced for a later day answers from that day, and the row it
// replaces answers until then, while a row that starts on the sheet's date
// still prices a request made before it. The later row comes first, so row
// order cannot pass the test.
func TestRowStartingAfterTheSheetDateHoldsFromItsStart(t *testing.T) {
	release := fstest.MapFS{"x.json": {Data: []byte(`{"Provider":"google","ServiceProviderName":"Google","SheetDate":"2026-11-07","rows":[
{"RowType":"unit_price","SkuPriceId":"later","ModelId":"m","RegionId":"global","ServiceTier":"standard","TokenClass":"output","ListUnitPrice":"12","PricingUnit":"1000000 Tokens","EffectiveStart":"2026-12-01","EffectiveEnd":null},
{"RowType":"unit_price","SkuPriceId":"now","ModelId":"m","RegionId":"global","ServiceTier":"standard","TokenClass":"output","ListUnitPrice":"10","PricingUnit":"1000000 Tokens","EffectiveStart":"2026-11-07","EffectiveEnd":"2026-12-01"}]}`)}}
	c, err := Parse("0.0.1", release)
	if err != nil {
		t.Fatal(err)
	}
	for day, want := range map[string]string{"2026-11-01": "now", "2026-11-30": "now", "2026-12-01": "later"} {
		at, _ := time.Parse(dayLayout, day)
		q := query("google", "m", focus.Output)
		q.At = at
		got, err := c.Lookup(q)
		if err != nil || got.SkuPriceID != want {
			t.Errorf("%s: %s, %v; want %s", day, got.SkuPriceID, err, want)
		}
	}
	s := c.sheets["google"]
	if s.overlapsInDays(&s.Rows[0], &s.Rows[1]) {
		t.Error("a row and the row that replaces it overlap")
	}
}
