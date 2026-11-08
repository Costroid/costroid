package catalog

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	catalogdata "github.com/Costroid/costroid/catalog"
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

// embeddedVersions are the releases the binary carries, one directory each.
func embeddedVersions(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(catalogdata.Sheets, ".")
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	return versions
}

// embedded parses one release the binary carries, as Load parses the one a
// run prices with.
func embedded(t *testing.T, version string) *Catalog {
	t.Helper()
	release, err := fs.Sub(catalogdata.Sheets, version)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(version, release)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestEmbeddedReleaseCarriesTheThreeSheets holds that a run prices with
// release 0.2.0 and its Anthropic, OpenAI and Google sheets, and that release
// 0.1.0, embedded beside it, still parses on its own with its two sheets.
func TestEmbeddedReleaseCarriesTheThreeSheets(t *testing.T) {
	releases := []struct {
		catalog   *Catalog
		version   string
		providers map[string]string
	}{
		{pinned(t), "0.2.0", map[string]string{"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google"}},
		{embedded(t, "0.1.0"), "0.1.0", map[string]string{"anthropic": "Anthropic", "openai": "OpenAI"}},
	}
	if versions := embeddedVersions(t); !slices.Equal(versions, []string{"0.1.0", "0.2.0"}) {
		t.Errorf("the binary carries releases %v", versions)
	}
	for _, release := range releases {
		c := release.catalog
		if c.Version() != release.version || len(c.sheets) != len(release.providers) {
			t.Errorf("release %s holds %d sheets, want %s with %d", c.Version(), len(c.sheets), release.version, len(release.providers))
		}
		for provider, name := range release.providers {
			got, err := c.ServiceProviderName(provider)
			if err != nil || got != name {
				t.Errorf("%s %s: %q %v", release.version, provider, got, err)
			}
		}
	}
}

// TestGeminiProLongContextTier holds that a Gemini Pro request whose input
// passes 200,000 tokens is priced at the long-context rows, and one at the
// threshold at the rows below it, for every class the sheet prices by tier.
func TestGeminiProLongContextTier(t *testing.T) {
	cases := []struct {
		model       string
		class       focus.TokenClass
		under, over string
	}{
		{"gemini-3.1-pro-preview", focus.InputUncached, "2.00", "4.00"},
		{"gemini-3.1-pro-preview", focus.Output, "12.00", "18.00"},
		{"gemini-3.1-pro-preview", focus.CacheRead, "0.20", "0.40"},
		{"gemini-2.5-pro", focus.InputUncached, "1.25", "2.50"},
		{"gemini-2.5-pro", focus.Output, "10.00", "15.00"},
		{"gemini-2.5-pro", focus.CacheRead, "0.125", "0.25"},
	}
	c := pinned(t)
	for _, tc := range cases {
		for _, tier := range []struct {
			input int64
			sku   string
			price string
		}{
			{200000, "to-200000", tc.under},
			{200001, "over-200000", tc.over},
		} {
			q := query("google", tc.model, tc.class)
			q.ContextInputTokens = tier.input
			got, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			sku := "google:" + tc.model + ":global:standard:" + tier.sku + ":" + string(tc.class)
			if got.Fallback || got.SkuPriceID != sku || got.ListUnitPrice.Cmp(decimal.MustParse(tier.price)) != 0 {
				t.Errorf("%s %s at %d input tokens: %s at %s, want %s at %s", tc.model, tc.class, tier.input, got.SkuPriceID, got.ListUnitPrice, sku, tier.price)
			}
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
		"google":    {focus.InputUncached, focus.CacheRead, focus.Output},
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
	const tokenScale = 6
	for _, version := range embeddedVersions(t) {
		for provider, s := range embedded(t, version).sheets {
			for i := range s.Rows {
				r := &s.Rows[i]
				if r.ListUnitPrice == "" {
					continue
				}
				price, err := decimal.Parse(r.ListUnitPrice)
				if err != nil {
					t.Fatalf("%s %s %s: %v", version, provider, r.SkuPriceID, err)
				}
				if price.Scale()+1+tokenScale > focus.MaxScale {
					t.Errorf("%s: %s has %d fractional digits", version, r.SkuPriceID, price.Scale())
				}
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

// TestFallbackBoundsEveryPrice holds that each sheet's fallback for a class
// is at or above every price the sheet states or composes for that class, so
// a model or tier the sheet does not name is never priced under one it does.
// A class with no fallback row is refused, not priced, and is not held here.
// It holds every embedded release but 0.1.0, which was published before the
// fallback had to bound every row; a published release never changes.
func TestFallbackBoundsEveryPrice(t *testing.T) {
	checked := 0
	for _, version := range embeddedVersions(t) {
		if version == "0.1.0" {
			continue
		}
		c := embedded(t, version)
		for provider, s := range c.sheets {
			fallbacks := map[string]decimal.Decimal{}
			for i := range s.Rows {
				if r := &s.Rows[i]; r.RowType == "fallback" {
					fallbacks[r.TokenClass] = decimal.MustParse(r.ListUnitPrice)
				}
			}
			for _, price := range everyPrice(t, c, provider, s) {
				bound, ok := fallbacks[price.class]
				if !ok {
					continue
				}
				checked++
				if price.ListUnitPrice.Cmp(bound) > 0 {
					t.Errorf("%s: %s at %s passes the %s fallback of %s", version, price.SkuPriceID, price.ListUnitPrice, price.class, bound)
				}
			}
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

// TestNoTwoPriceRowsOverlap holds that no two unit_price rows of a sheet of
// any embedded release can answer one query: none share a model, tier, region and class with
// context tiers and days that meet. The lookup takes the first row that
// answers, so an overlap would price by row order.
func TestNoTwoPriceRowsOverlap(t *testing.T) {
	pairs := 0
	for _, version := range embeddedVersions(t) {
		for _, s := range embedded(t, version).sheets {
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
						t.Errorf("%s: %s and %s answer the same query", version, a.SkuPriceID, b.SkuPriceID)
					}
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

// TestARequestBeforeTheSheetDateIsPricedByItsRow holds that a request made
// before the default release's sheet date is priced by that release's row for
// its model, not by the fallback: a row that starts on its sheet's date holds
// from any earlier day.
func TestARequestBeforeTheSheetDateIsPricedByItsRow(t *testing.T) {
	c := pinned(t)
	for _, day := range []string{"2026-10-01", "2026-11-01", "2026-11-06", "2026-11-07"} {
		at, err := time.Parse(dayLayout, day)
		if err != nil {
			t.Fatal(err)
		}
		q := query("openai", "gpt-6.1-sol", focus.Output)
		q.At = at
		got, err := c.Lookup(q)
		if err != nil {
			t.Fatal(err)
		}
		if got.Fallback || got.SkuPriceID != "openai:gpt-6.1-sol:global:standard:to-272000:output" || got.ListUnitPrice.Cmp(decimal.MustParse("10.00")) != 0 {
			t.Errorf("%s: %s at %s, fallback %t", day, got.SkuPriceID, got.ListUnitPrice, got.Fallback)
		}
	}
}

// TestAnEndedPromotionFallsToTheFallbackNotAnOlderRelease holds that once a
// promotion row of the default release ends with no row after it, the
// fallback prices the class: the page no longer guarantees the price, and an
// older release's open-ended copy of the promotion is not read in its place.
func TestAnEndedPromotionFallsToTheFallbackNotAnOlderRelease(t *testing.T) {
	q := query("openai", "gpt-5.6-sol", focus.Output)
	q.At = time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	c := pinned(t)
	last, err := c.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if last.Fallback || last.ListUnitPrice.Cmp(decimal.MustParse("20.00")) != 0 {
		t.Errorf("2026-11-30: %s at %s", last.SkuPriceID, last.ListUnitPrice)
	}
	q.At = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	after, err := c.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Fallback || after.SkuPriceID != "openai:fallback:output" || after.ListUnitPrice.Cmp(decimal.MustParse("600.00")) != 0 {
		t.Errorf("2026-12-01: %s at %s, fallback %t", after.SkuPriceID, after.ListUnitPrice, after.Fallback)
	}
	older, err := embedded(t, "0.1.0").Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	if older.Fallback || older.ListUnitPrice.Cmp(decimal.MustParse("20.00")) != 0 {
		t.Errorf("release 0.1.0 no longer holds the open-ended promotion row this test guards against: %+v", older)
	}
}
