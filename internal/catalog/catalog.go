// Package catalog prices token classes against the pinned sheets.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	catalogdata "github.com/Costroid/costroid/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Region and tier values the sheets use for default routing.
const (
	RegionGlobal = focus.RegionGlobal
	TierStandard = "standard"
	// TierFast is fast mode, which the sheets name as a tier.
	TierFast = "fast"
)

const dayLayout = "2006-01-02"

// ErrNoPrice means a sheet has neither a price nor a fallback for a class.
var ErrNoPrice = errors.New("catalog: no price and no fallback")

// Query names one token class of one request.
type Query struct {
	Provider    string
	Model       string
	Class       focus.TokenClass
	ServiceTier string
	Region      string
	// ContextInputTokens selects a long-context tier. Zero means the agent
	// reported no per-request input, and the lowest tier applies.
	ContextInputTokens int64
	At                 time.Time
}

// Price is the answer to a Query.
type Price struct {
	SkuPriceID          string
	ListUnitPrice       decimal.Decimal
	PricingUnit         string
	Fallback            bool
	FallbackBasis       string
	ResidencyMultiplier *decimal.Decimal
	// CacheMultiplier is the sheet's prompt_cache multiplier when the sheet
	// states the tier's input price and not the class's, and the price is the
	// one times the other. Nil when a row states the class's price.
	CacheMultiplier *Multiplier
	// ContextTierMin and ContextTierMax are the matched row's context tier,
	// as the sheet states it: a request's input is above the first and at or
	// below the second. Nil means the tier is open on that side.
	ContextTierMin, ContextTierMax *int64
}

// Multiplier is one multiplier row of a sheet.
type Multiplier struct {
	SkuPriceID string
	Factor     decimal.Decimal
}

// CacheNote names the prompt_cache multiplier a price was composed with.
func (m Multiplier) CacheNote(class focus.TokenClass) string {
	return fmt.Sprintf("input price times %s for %s, by %s", m.Factor, class, m.SkuPriceID)
}

// Catalog is one pinned release.
type Catalog struct {
	version string
	sheets  map[string]*sheet
}

type sheet struct {
	Provider            string `json:"Provider"`
	ServiceProviderName string `json:"ServiceProviderName"`
	SheetDate           string `json:"SheetDate"`
	Rows                []row  `json:"rows"`
}

type row struct {
	RowType                   string   `json:"RowType"`
	SkuPriceID                string   `json:"SkuPriceId"`
	ModelID                   string   `json:"ModelId"`
	RegionID                  string   `json:"RegionId"`
	ServiceTier               string   `json:"ServiceTier"`
	ContextTierMinInputTokens *int64   `json:"ContextTierMinInputTokens"`
	ContextTierMaxInputTokens *int64   `json:"ContextTierMaxInputTokens"`
	TokenClass                string   `json:"TokenClass"`
	ListUnitPrice             string   `json:"ListUnitPrice"`
	PricingUnit               string   `json:"PricingUnit"`
	EffectiveStart            string   `json:"EffectiveStart"`
	EffectiveEnd              *string  `json:"EffectiveEnd"`
	FallbackBasis             string   `json:"FallbackBasis"`
	ModifierKind              string   `json:"ModifierKind"`
	Multiplier                string   `json:"Multiplier"`
	AppliesToModels           []string `json:"AppliesToModels"`
	AppliesToTokenClasses     []string `json:"AppliesToTokenClasses"`
}

// Load reads the release embedded in the binary.
func Load() (*Catalog, error) {
	sub, err := fs.Sub(catalogdata.Sheets, catalogdata.Version)
	if err != nil {
		return nil, fmt.Errorf("catalog: open embedded release: %w", err)
	}
	return Parse(catalogdata.Version, sub)
}

// Parse reads every sheet in a release directory. A release holds one sheet
// per provider; a second one is refused, because the lookup would read only
// one of them. Two releases are two catalogs, each parsed on its own.
func Parse(version string, release fs.FS) (*Catalog, error) {
	names, err := fs.Glob(release, "*.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: list sheets: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("catalog: release %s holds no sheet", version)
	}
	c := &Catalog{version: version, sheets: map[string]*sheet{}}
	files := map[string]string{}
	for _, name := range names {
		data, err := fs.ReadFile(release, name)
		if err != nil {
			return nil, fmt.Errorf("catalog: read %s: %w", name, err)
		}
		var s sheet
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("catalog: parse %s: %w", name, err)
		}
		if first, ok := files[s.Provider]; ok {
			return nil, fmt.Errorf("catalog: release %s holds two sheets for %s: %s and %s", version, s.Provider, first, name)
		}
		files[s.Provider] = name
		c.sheets[s.Provider] = &s
	}
	return c, nil
}

// Version names the release.
func (c *Catalog) Version() string {
	return c.version
}

// ServiceProviderName is the provider's name as its sheet states it.
func (c *Catalog) ServiceProviderName(provider string) (string, error) {
	s, ok := c.sheets[provider]
	if !ok {
		return "", fmt.Errorf("catalog: no sheet for provider %q", provider)
	}
	return s.ServiceProviderName, nil
}

// Lookup prices one token class. At the fast tier, a cache class the sheet
// prices only as a multiple of the input price is the fast input price times
// the class's prompt_cache multiplier. A model, tier or class the sheet does
// not name falls to the sheet's non-zero fallback row, and the Price says so.
func (c *Catalog) Lookup(q Query) (Price, error) {
	s, ok := c.sheets[q.Provider]
	if !ok {
		return Price{}, fmt.Errorf("catalog: no sheet for provider %q", q.Provider)
	}
	if r := s.unitPrice(q); r != nil {
		return s.priced(r, q, nil)
	}
	if q.ServiceTier == TierFast {
		cache, err := s.promptCache(q)
		if err != nil {
			return Price{}, err
		}
		input := q
		input.Class = focus.InputUncached
		if r := s.unitPrice(input); cache != nil && r != nil {
			return s.priced(r, q, cache)
		}
	}
	for i := range s.Rows {
		candidate := &s.Rows[i]
		if candidate.RowType == "fallback" && candidate.TokenClass == string(q.Class) && s.holdsOn(candidate, q.At) {
			return fallback(candidate)
		}
	}
	return Price{}, fmt.Errorf("%w: %s %s %s", ErrNoPrice, q.Provider, q.Model, q.Class)
}

// unitPrice is the first unit_price row that answers the query, nil when
// none does.
func (s *sheet) unitPrice(q Query) *row {
	for i := range s.Rows {
		if s.matches(&s.Rows[i], q) {
			return &s.Rows[i]
		}
	}
	return nil
}

// promptCache is the prompt_cache multiplier for the query's model and
// class. A row that names the model is narrower than one that names none,
// and wins. Nil means no row applies.
func (s *sheet) promptCache(q Query) (*Multiplier, error) {
	var general *row
	for i := range s.Rows {
		r := &s.Rows[i]
		if r.RowType != "multiplier" || r.ModifierKind != "prompt_cache" || !s.holdsOn(r, q.At) ||
			!slices.Contains(r.AppliesToTokenClasses, string(q.Class)) {
			continue
		}
		if slices.Contains(r.AppliesToModels, q.Model) {
			return r.multiplier()
		}
		if len(r.AppliesToModels) == 0 && general == nil {
			general = r
		}
	}
	if general == nil {
		return nil, nil
	}
	return general.multiplier()
}

func (r *row) multiplier() (*Multiplier, error) {
	factor, err := decimal.Parse(r.Multiplier)
	if err != nil {
		return nil, fmt.Errorf("catalog: %s: %w", r.SkuPriceID, err)
	}
	return &Multiplier{SkuPriceID: r.SkuPriceID, Factor: factor}, nil
}

func (s *sheet) matches(r *row, q Query) bool {
	return r.RowType == "unit_price" &&
		r.ModelID == q.Model &&
		r.TokenClass == string(q.Class) &&
		r.ServiceTier == q.ServiceTier &&
		r.RegionID == RegionGlobal &&
		s.holdsOn(r, q.At) &&
		r.holdsContext(q.ContextInputTokens)
}

func (r *row) holdsContext(tokens int64) bool {
	if r.ContextTierMinInputTokens != nil && tokens <= *r.ContextTierMinInputTokens {
		return false
	}
	return r.ContextTierMaxInputTokens == nil || tokens <= *r.ContextTierMaxInputTokens
}

// holdsOn compares calendar days. A row's end is the first day it no longer
// holds, and its start is the day startsOn names.
func (s *sheet) holdsOn(r *row, at time.Time) bool {
	day := at.UTC().Format(dayLayout)
	if day < s.startsOn(r) {
		return false
	}
	return r.EffectiveEnd == nil || day < *r.EffectiveEnd
}

// startsOn is the first day a row holds. A row that starts by the sheet's
// date starts the day it was seen in force, and a request made earlier is
// still priced by the pinned release, so it holds from any day: empty. A row
// that starts after the sheet's date is a change the provider announced, and
// holds from its start.
func (s *sheet) startsOn(r *row) string {
	if r.EffectiveStart > s.SheetDate {
		return r.EffectiveStart
	}
	return ""
}

// priced answers from one unit_price row. With a cache multiplier the row is
// the tier's input price, and the answer is that price times the multiplier,
// under the row's id with the query's class in place of the input class.
func (s *sheet) priced(r *row, q Query, cache *Multiplier) (Price, error) {
	price, err := decimal.Parse(r.ListUnitPrice)
	if err != nil {
		return Price{}, fmt.Errorf("catalog: %s: %w", r.SkuPriceID, err)
	}
	answer := Price{
		SkuPriceID:     r.SkuPriceID,
		ListUnitPrice:  price,
		PricingUnit:    r.PricingUnit,
		ContextTierMin: r.ContextTierMinInputTokens,
		ContextTierMax: r.ContextTierMaxInputTokens,
	}
	if cache != nil {
		base, ok := strings.CutSuffix(r.SkuPriceID, ":"+string(focus.InputUncached))
		if !ok {
			return Price{}, fmt.Errorf("catalog: %s does not end in its class, so no price can be composed from it", r.SkuPriceID)
		}
		price = price.Mul(cache.Factor)
		answer.SkuPriceID, answer.ListUnitPrice, answer.CacheMultiplier = base+":"+string(q.Class), price, cache
	}
	if q.Region == "" || q.Region == RegionGlobal {
		return answer, nil
	}
	factor, err := s.residency(q)
	if err != nil || factor == nil {
		return answer, err
	}
	answer.ListUnitPrice = price.Mul(*factor)
	answer.ResidencyMultiplier = factor
	return answer, nil
}

func (s *sheet) residency(q Query) (*decimal.Decimal, error) {
	for i := range s.Rows {
		r := &s.Rows[i]
		if r.RowType != "multiplier" || r.ModifierKind != "data_residency" || r.RegionID != q.Region {
			continue
		}
		if len(r.AppliesToModels) > 0 && !slices.Contains(r.AppliesToModels, q.Model) {
			continue
		}
		factor, err := decimal.Parse(r.Multiplier)
		if err != nil {
			return nil, fmt.Errorf("catalog: %s: %w", r.SkuPriceID, err)
		}
		return &factor, nil
	}
	return nil, nil
}

func fallback(r *row) (Price, error) {
	price, err := decimal.Parse(r.ListUnitPrice)
	if err != nil {
		return Price{}, fmt.Errorf("catalog: %s: %w", r.SkuPriceID, err)
	}
	return Price{
		SkuPriceID:    r.SkuPriceID,
		ListUnitPrice: price,
		PricingUnit:   r.PricingUnit,
		Fallback:      true,
		FallbackBasis: r.FallbackBasis,
	}, nil
}
