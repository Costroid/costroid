// Package catalog prices token classes against the pinned sheets.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	catalogdata "github.com/Costroid/costroid/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Region and tier values the sheets use for default routing.
const (
	RegionGlobal = focus.RegionGlobal
	TierStandard = "standard"
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
	// ContextTierMin and ContextTierMax are the matched row's context tier,
	// as the sheet states it: a request's input is above the first and at or
	// below the second. Nil means the tier is open on that side.
	ContextTierMin, ContextTierMax *int64
}

// Catalog is one pinned release.
type Catalog struct {
	version string
	sheets  map[string]*sheet
}

type sheet struct {
	Provider            string `json:"Provider"`
	ServiceProviderName string `json:"ServiceProviderName"`
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
}

// Load reads the release embedded in the binary.
func Load() (*Catalog, error) {
	sub, err := fs.Sub(catalogdata.Sheets, catalogdata.Version)
	if err != nil {
		return nil, fmt.Errorf("catalog: open embedded release: %w", err)
	}
	return Parse(catalogdata.Version, sub)
}

// Parse reads every sheet in a release directory.
func Parse(version string, release fs.FS) (*Catalog, error) {
	names, err := fs.Glob(release, "*.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: list sheets: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("catalog: release %s holds no sheet", version)
	}
	c := &Catalog{version: version, sheets: map[string]*sheet{}}
	for _, name := range names {
		data, err := fs.ReadFile(release, name)
		if err != nil {
			return nil, fmt.Errorf("catalog: read %s: %w", name, err)
		}
		var s sheet
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("catalog: parse %s: %w", name, err)
		}
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

// Lookup prices one token class. A model, tier or class the sheet does not
// name falls to the sheet's non-zero fallback row, and the Price says so.
func (c *Catalog) Lookup(q Query) (Price, error) {
	s, ok := c.sheets[q.Provider]
	if !ok {
		return Price{}, fmt.Errorf("catalog: no sheet for provider %q", q.Provider)
	}
	for i := range s.Rows {
		if s.Rows[i].matches(q) {
			return s.priced(&s.Rows[i], q)
		}
	}
	for i := range s.Rows {
		candidate := &s.Rows[i]
		if candidate.RowType == "fallback" && candidate.TokenClass == string(q.Class) && candidate.holdsOn(q.At) {
			return fallback(candidate)
		}
	}
	return Price{}, fmt.Errorf("%w: %s %s %s", ErrNoPrice, q.Provider, q.Model, q.Class)
}

func (r *row) matches(q Query) bool {
	return r.RowType == "unit_price" &&
		r.ModelID == q.Model &&
		r.TokenClass == string(q.Class) &&
		r.ServiceTier == q.ServiceTier &&
		r.RegionID == RegionGlobal &&
		r.holdsOn(q.At) &&
		r.holdsContext(q.ContextInputTokens)
}

func (r *row) holdsContext(tokens int64) bool {
	if r.ContextTierMinInputTokens != nil && tokens <= *r.ContextTierMinInputTokens {
		return false
	}
	return r.ContextTierMaxInputTokens == nil || tokens <= *r.ContextTierMaxInputTokens
}

// holdsOn compares calendar days. A row's start is the day it was seen in
// force, and a request made earlier is still priced by the pinned release.
func (r *row) holdsOn(at time.Time) bool {
	if r.EffectiveEnd == nil {
		return true
	}
	return at.UTC().Format(dayLayout) < *r.EffectiveEnd
}

func (s *sheet) priced(r *row, q Query) (Price, error) {
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
