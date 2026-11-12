package catalog

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
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

// TestEmbeddedReleaseCarriesTheFiveSheets holds that a run prices with
// release 0.4.1 and its Anthropic, OpenAI, Google, GitHub Copilot and Cursor
// sheets, and that releases 0.3.0, 0.2.0 and 0.1.0, embedded beside it, still
// parse on their own with five sheets, three and two.
func TestEmbeddedReleaseCarriesTheFiveSheets(t *testing.T) {
	five := map[string]string{"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google", "github-copilot": "GitHub", "cursor": "Cursor"}
	releases := []struct {
		catalog   *Catalog
		version   string
		providers map[string]string
	}{
		{pinned(t), "0.4.1", five},
		{embedded(t, "0.3.0"), "0.3.0", five},
		{embedded(t, "0.2.0"), "0.2.0", map[string]string{"anthropic": "Anthropic", "openai": "OpenAI", "google": "Google"}},
		{embedded(t, "0.1.0"), "0.1.0", map[string]string{"anthropic": "Anthropic", "openai": "OpenAI"}},
	}
	if versions := embeddedVersions(t); !slices.Equal(versions, []string{"0.1.0", "0.2.0", "0.3.0", "0.4.1"}) {
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
// the model wins over the general one. Data residency stacks on top, and the
// price id then names the region.
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
		sku := "anthropic:" + tc.model + ":" + tc.region + ":fast:all:" + string(tc.class)
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

// TestFallbackBoundsEveryRegionalPrice holds the fallback rule in every region
// and service tier a sheet of the newest embedded release names: a model the
// sheet does not name, and a named model at a tier it does not name, price at
// or above every price, stated or composed, of the same class in the same
// region. A price a residency multiplier raised names its region in its id.
func TestFallbackBoundsEveryRegionalPrice(t *testing.T) {
	versions := embeddedVersions(t)
	newest := versions[len(versions)-1]
	c := embedded(t, newest)
	checked, regional := 0, 0
	for provider, s := range c.sheets {
		regions := []string{RegionGlobal}
		for _, r := range s.Rows {
			if r.RowType == "multiplier" && r.ModifierKind == "data_residency" && !slices.Contains(regions, r.RegionID) {
				regions = append(regions, r.RegionID)
			}
		}
		for i := range s.Rows {
			r := &s.Rows[i]
			if r.RowType != "unit_price" {
				continue
			}
			classes := []focus.TokenClass{focus.TokenClass(r.TokenClass)}
			if r.TokenClass == string(focus.InputUncached) {
				classes = append(classes, focus.CacheRead, focus.CacheWrite5m, focus.CacheWrite1h, focus.CacheWrite30m)
			}
			for _, region := range regions {
				for _, class := range classes {
					q := Query{Provider: provider, Model: r.ModelID, Class: class, ServiceTier: r.ServiceTier, Region: region,
						ContextInputTokens: insideTier(r), At: firstDay(t, r)}
					named, err := c.Lookup(q)
					if errors.Is(err, ErrNoPrice) {
						continue
					}
					if err != nil {
						t.Fatalf("%s %s %s: %v", r.SkuPriceID, class, region, err)
					}
					if named.Fallback {
						continue
					}
					if named.ResidencyMultiplier != nil {
						regional++
						if fields := strings.Split(named.SkuPriceID, ":"); fields[2] != region {
							t.Errorf("%s: a price raised for %s is named %s", r.SkuPriceID, region, named.SkuPriceID)
						}
					}
					unnamedModel, unnamedTier := q, q
					unnamedModel.Model = "a-model-no-sheet-names"
					unnamedTier.ServiceTier = "a-tier-no-sheet-names"
					for _, other := range []Query{unnamedModel, unnamedTier} {
						bound, err := c.Lookup(other)
						if errors.Is(err, ErrNoPrice) {
							continue
						}
						if err != nil || !bound.Fallback {
							t.Fatalf("%s %s %s at %s: %+v %v", provider, other.Model, class, other.ServiceTier, bound, err)
						}
						checked++
						if named.ListUnitPrice.Cmp(bound.ListUnitPrice) > 0 {
							t.Errorf("%s: %s %s in %s at %s passes the fallback of %s for %s at %s", newest, named.SkuPriceID, class, region,
								named.ListUnitPrice, bound.ListUnitPrice, other.Model, other.ServiceTier)
						}
					}
				}
			}
		}
	}
	if checked == 0 || regional == 0 {
		t.Errorf("checked %d bounds, %d of them on a regional price", checked, regional)
	}
	for _, tc := range []struct {
		provider, model, region, price string
	}{
		{"openai", "a-model-no-sheet-names", "regional", "660"},
		{"openai", "a-model-no-sheet-names", RegionGlobal, "600"},
		{"anthropic", "a-model-no-sheet-names", "us", "88"},
		{"anthropic", "claude-opus-4-1-20250805", "us", "80"},
	} {
		q := query(tc.provider, tc.model, focus.Output)
		q.ServiceTier, q.Region = "a-tier-no-sheet-names", tc.region
		got, err := pinned(t).Lookup(q)
		if err != nil || !got.Fallback || got.ListUnitPrice.String() != tc.price {
			t.Errorf("%s %s output in %s: %+v %v, want the fallback at %s", tc.provider, tc.model, tc.region, got, err, tc.price)
		}
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

// TestRerouteDayPricesTheSuccessor holds Google's reroute of Gemini 2.5 Pro
// to Gemini 3.8 Flash from 2026-12-01: a 2.5 Pro request on 2026-11-30 is
// priced at 2.5 Pro's own row, in its context tier, and one on 2026-12-01 at
// the price 3.8 Flash's row states that day, whatever its context, under the
// 2.5 Pro row the catalog carries for the reroute. Neither is the fallback.
func TestRerouteDayPricesTheSuccessor(t *testing.T) {
	cases := []struct {
		class focus.TokenClass
		own   [2]string
		flash string
	}{
		{focus.InputUncached, [2]string{"1.25", "2.50"}, "0.75"},
		{focus.Output, [2]string{"10.00", "15.00"}, "3.75"},
		{focus.CacheRead, [2]string{"0.125", "0.25"}, "0.075"},
	}
	ownTiers := [2]string{"to-200000", "over-200000"}
	c := pinned(t)
	before := time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC)
	reroute := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	checked := 0
	for _, tc := range cases {
		successor := query("google", "gemini-3.8-flash", tc.class)
		successor.At = reroute
		flash, err := c.Lookup(successor)
		if err != nil {
			t.Fatal(err)
		}
		if want := "google:gemini-3.8-flash:global:standard:all:" + string(tc.class); flash.Fallback || flash.SkuPriceID != want || flash.ListUnitPrice.Cmp(decimal.MustParse(tc.flash)) != 0 {
			t.Fatalf("3.8 Flash %s on 2026-12-01: %s at %s, fallback %t; want %s at %s", tc.class, flash.SkuPriceID, flash.ListUnitPrice, flash.Fallback, want, tc.flash)
		}
		for tier, input := range []int64{200000, 200001} {
			q := query("google", "gemini-2.5-pro", tc.class)
			q.ContextInputTokens = input
			q.At = before
			got, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			id := "google:gemini-2.5-pro:global:standard:" + ownTiers[tier] + ":" + string(tc.class)
			if got.Fallback || got.SkuPriceID != id || got.ListUnitPrice.Cmp(decimal.MustParse(tc.own[tier])) != 0 {
				t.Errorf("2.5 Pro %s at %d input tokens on 2026-11-30: %s at %s, fallback %t; want %s at %s", tc.class, input, got.SkuPriceID, got.ListUnitPrice, got.Fallback, id, tc.own[tier])
			}
			q.At = reroute
			got, err = c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			id = "google:gemini-2.5-pro:global:standard:all:" + string(tc.class) + ":from-2026-12-01"
			if got.Fallback || got.SkuPriceID != id || got.ListUnitPrice.Cmp(flash.ListUnitPrice) != 0 {
				t.Errorf("2.5 Pro %s at %d input tokens on 2026-12-01: %s at %s, fallback %t; want %s at 3.8 Flash's %s", tc.class, input, got.SkuPriceID, got.ListUnitPrice, got.Fallback, id, flash.ListUnitPrice)
			}
			checked++
		}
	}
	if checked != 6 {
		t.Errorf("%d lookups checked", checked)
	}
}

// TestFable52PricesAtItsOwnRows holds that Claude Fable 5.2 and Claude Mythos
// 5.2 price at their own rows of the default release, in USD per million
// tokens: standard input 8, output 40, 5-minute cache write 10, 1-hour cache
// write 16 and cache read 0.20; fast input 16 and output 80. Fast-mode cache
// writes are the fast input times the sheet's cache write multipliers, 20 and
// 32, and they set the sheet's fallback for those classes, as the fast input
// and output set it for theirs. Release 0.3.0 named neither model, and priced
// both at its fallback of 15 input and 75 output.
func TestFable52PricesAtItsOwnRows(t *testing.T) {
	prices := []struct {
		tier  string
		class focus.TokenClass
		price string
	}{
		{TierStandard, focus.InputUncached, "8"},
		{TierStandard, focus.Output, "40"},
		{TierStandard, focus.CacheWrite5m, "10"},
		{TierStandard, focus.CacheWrite1h, "16"},
		{TierStandard, focus.CacheRead, "0.20"},
		{TierFast, focus.InputUncached, "16"},
		{TierFast, focus.Output, "80"},
	}
	c := pinned(t)
	before := embedded(t, "0.3.0")
	for _, model := range []string{"claude-fable-5-2", "claude-mythos-5-2"} {
		for _, tc := range prices {
			q := query(ProviderAnthropic, model, tc.class)
			q.ServiceTier, q.At = tc.tier, time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
			got, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			id := "anthropic:" + model + ":global:" + tc.tier + ":all:" + string(tc.class)
			if got.Fallback || got.CacheMultiplier != nil || got.SkuPriceID != id || got.ListUnitPrice.Cmp(decimal.MustParse(tc.price)) != 0 {
				t.Errorf("%s %s %s: %s at %s, fallback %t; want %s at %s", model, tc.tier, tc.class, got.SkuPriceID, got.ListUnitPrice, got.Fallback, id, tc.price)
			}
		}
		for class, price := range map[focus.TokenClass]string{focus.CacheWrite5m: "20", focus.CacheWrite1h: "32"} {
			q := query(ProviderAnthropic, model, class)
			q.ServiceTier = TierFast
			got, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			id := "anthropic:" + model + ":global:fast:all:" + string(class)
			if got.Fallback || got.CacheMultiplier == nil || got.SkuPriceID != id || got.ListUnitPrice.Cmp(decimal.MustParse(price)) != 0 {
				t.Errorf("%s fast %s: %s at %s, fallback %t; want %s at %s", model, class, got.SkuPriceID, got.ListUnitPrice, got.Fallback, id, price)
			}
		}
		for class, price := range map[focus.TokenClass]string{focus.InputUncached: "15", focus.Output: "75"} {
			got, err := before.Lookup(query(ProviderAnthropic, model, class))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Fallback || got.ListUnitPrice.Cmp(decimal.MustParse(price)) != 0 {
				t.Errorf("release 0.3.0 %s %s: %s at %s, fallback %t; want the fallback at %s", model, class, got.SkuPriceID, got.ListUnitPrice, got.Fallback, price)
			}
		}
	}
	for class, price := range map[focus.TokenClass]string{focus.InputUncached: "16", focus.Output: "80", focus.CacheWrite5m: "20", focus.CacheWrite1h: "32"} {
		got, err := c.Lookup(query(ProviderAnthropic, "a-model-no-sheet-names", class))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Fallback || got.ListUnitPrice.Cmp(decimal.MustParse(price)) != 0 || !strings.Contains(got.FallbackBasis, "claude-fable-5-2:global:fast") {
			t.Errorf("the anthropic fallback for %s: %s at %s, basis %q; want %s, set by Fable 5.2's fast mode", class, got.SkuPriceID, got.ListUnitPrice, got.FallbackBasis, price)
		}
	}
}

// TestGemini4CarbonPricesByDay holds Gemini 4 Carbon to its two dated rows in
// each tier Google prices it at, in USD per million tokens: a request on
// 2026-11-30 at the first row, and one on 2026-12-01 at the row that follows
// it. Standard input is 2.00 then 4.00, output 10.00 then 20.00 and cache
// read 0.10 then 0.20; batch and flex half that; priority 3.60 then 7.20,
// 18.00 then 36.00 and 0.18 then 0.36. None is the fallback. Release 0.3.0
// named no Gemini 4 Carbon, and priced it at its fallback of 7.20 input and
// 36.00 output.
func TestGemini4CarbonPricesByDay(t *testing.T) {
	cases := []struct {
		tier          string
		class         focus.TokenClass
		before, after string
	}{
		{TierStandard, focus.InputUncached, "2.00", "4.00"},
		{TierStandard, focus.Output, "10.00", "20.00"},
		{TierStandard, focus.CacheRead, "0.10", "0.20"},
		{"batch", focus.InputUncached, "1.00", "2.00"},
		{"batch", focus.Output, "5.00", "10.00"},
		{"batch", focus.CacheRead, "0.05", "0.10"},
		{"flex", focus.InputUncached, "1.00", "2.00"},
		{"flex", focus.Output, "5.00", "10.00"},
		{"flex", focus.CacheRead, "0.05", "0.10"},
		{"priority", focus.InputUncached, "3.60", "7.20"},
		{"priority", focus.Output, "18.00", "36.00"},
		{"priority", focus.CacheRead, "0.18", "0.36"},
	}
	c := pinned(t)
	for _, tc := range cases {
		id := "google:gemini-4-carbon:global:" + tc.tier + ":all:" + string(tc.class)
		for _, day := range []struct {
			at        time.Time
			id, price string
		}{
			{time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC), id, tc.before},
			{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), id + ":from-2026-12-01", tc.after},
		} {
			q := query(ProviderGoogle, "gemini-4-carbon", tc.class)
			q.ServiceTier, q.At = tc.tier, day.at
			got, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			if got.Fallback || got.SkuPriceID != day.id || got.ListUnitPrice.Cmp(decimal.MustParse(day.price)) != 0 {
				t.Errorf("%s %s on %s: %s at %s, fallback %t; want %s at %s", tc.tier, tc.class, day.at.Format(time.DateTime), got.SkuPriceID, got.ListUnitPrice, got.Fallback, day.id, day.price)
			}
		}
	}
	before := embedded(t, "0.3.0")
	for class, price := range map[focus.TokenClass]string{focus.InputUncached: "7.20", focus.Output: "36.00"} {
		got, err := before.Lookup(query(ProviderGoogle, "gemini-4-carbon", class))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Fallback || got.ListUnitPrice.Cmp(decimal.MustParse(price)) != 0 {
			t.Errorf("release 0.3.0 %s: %s at %s, fallback %t; want the fallback at %s", class, got.SkuPriceID, got.ListUnitPrice, got.Fallback, price)
		}
	}
}

// TestAPromotionThatEndsFallsToTheFallback holds each price of the default
// release that ends with no later price: every row of the model ends on the
// day named, a request in the last second before it prices at the row, and
// one at the first second of it prices at the sheet's fallback for the class,
// marked as a fallback. The days are those the catalog's release report names.
func TestAPromotionThatEndsFallsToTheFallback(t *testing.T) {
	ends := []struct{ provider, model, day string }{
		{ProviderOpenAI, "gpt-5.6-sol", "2026-12-01"},
		{ProviderOpenAI, "gpt-6-astra-law", "2027-01-01"},
		{"github-copilot", "gemini-3.8-flash", "2027-01-01"},
		{"github-copilot", "gemini-4-argon", "2026-12-01"},
		{"cursor", "gpt-5.6-sol", "2026-11-22"},
	}
	c := pinned(t)
	for _, end := range ends {
		s := c.sheets[end.provider]
		if s == nil {
			t.Fatalf("release %s holds no %s sheet", c.Version(), end.provider)
		}
		fallbacks := map[string]string{}
		for _, r := range s.Rows {
			if r.RowType == "fallback" {
				fallbacks[r.TokenClass] = r.ListUnitPrice
			}
		}
		last, err := time.Parse(dayLayout, end.day)
		if err != nil {
			t.Fatal(err)
		}
		rows := 0
		for i := range s.Rows {
			r := &s.Rows[i]
			if r.RowType != "unit_price" || r.ModelID != end.model {
				continue
			}
			rows++
			if r.EffectiveEnd == nil || *r.EffectiveEnd != end.day {
				t.Errorf("%s %s does not end on %s", end.provider, r.SkuPriceID, end.day)
				continue
			}
			q := Query{Provider: end.provider, Model: r.ModelID, Class: focus.TokenClass(r.TokenClass), ServiceTier: r.ServiceTier,
				Region: RegionGlobal, ContextInputTokens: insideTier(r), At: last.Add(-time.Second)}
			own, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			if own.Fallback || own.SkuPriceID != r.SkuPriceID || own.ListUnitPrice.Cmp(decimal.MustParse(r.ListUnitPrice)) != 0 {
				t.Errorf("%s the day before %s: %s at %s, fallback %t; want its row at %s", r.SkuPriceID, end.day, own.SkuPriceID, own.ListUnitPrice, own.Fallback, r.ListUnitPrice)
			}
			q.At = last
			after, err := c.Lookup(q)
			if err != nil {
				t.Fatal(err)
			}
			id := end.provider + ":fallback:" + r.TokenClass
			if !after.Fallback || after.SkuPriceID != id || after.ListUnitPrice.Cmp(decimal.MustParse(fallbacks[r.TokenClass])) != 0 {
				t.Errorf("%s on %s: %s at %s, fallback %t; want %s at %s", r.SkuPriceID, end.day, after.SkuPriceID, after.ListUnitPrice, after.Fallback, id, fallbacks[r.TokenClass])
			}
		}
		if rows == 0 {
			t.Errorf("the %s sheet prices no %s", end.provider, end.model)
		}
	}
}
