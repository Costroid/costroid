// Package focus holds the Costroid FOCUS row, version 0.1, and its writer.
package focus

import (
	"fmt"

	"github.com/Costroid/costroid/internal/decimal"
)

// MaxScale is the row contract's limit on fractional digits.
const MaxScale = 12

// TokenClass is a value of SkuMeter.
type TokenClass string

// The classes a row can carry. CacheWrite30m and CacheWrite12h are catalog
// classes that row version 0.1 does not yet list; they are written, never
// dropped or moved to another class.
const (
	InputUncached       TokenClass = "input_uncached"
	CacheRead           TokenClass = "cache_read"
	CacheWrite5m        TokenClass = "cache_write_5m"
	CacheWrite1h        TokenClass = "cache_write_1h"
	CacheWrite30m       TokenClass = "cache_write_30m"
	CacheWrite12h       TokenClass = "cache_write_12h"
	Output              TokenClass = "output"
	ReasoningOutput     TokenClass = "reasoning_output"
	CachedInputReported TokenClass = "cached_input_reported"
)

// Informational reports whether a class repeats tokens a billed class counts.
func (c TokenClass) Informational() bool {
	return c == ReasoningOutput || c == CachedInputReported
}

// Tag keys of the per-PR convention, version 0.1.
const (
	TagSessionID        = "costroid/session_id"
	TagAgent            = "costroid/agent"
	TagAgentVersion     = "costroid/agent_version"
	TagModel            = "costroid/model"
	TagCostSource       = "costroid/cost_source"
	TagCostBasis        = "costroid/cost_basis"
	TagAttributionGrade = "costroid/attribution_grade"
	TagEnforcementGrade = "costroid/enforcement_grade"
	TagRunIdentity      = "costroid/run_identity"
)

// CostBasis is a value of costroid/cost_basis.
type CostBasis string

// The two bases a wrapped run can carry.
const (
	Measured       CostBasis = "measured"
	ListEquivalent CostBasis = "list_equivalent"
)

// Row is one model request, or the finest grain an agent reports, for one
// token class. In JSON lines every column is present and an absent value is null.
type Row struct {
	BilledCost          decimal.Decimal   `json:"BilledCost"`
	BillingAccountID    string            `json:"BillingAccountId"`
	BillingAccountName  *string           `json:"BillingAccountName"`
	BillingCurrency     string            `json:"BillingCurrency"`
	BillingPeriodEnd    string            `json:"BillingPeriodEnd"`
	BillingPeriodStart  string            `json:"BillingPeriodStart"`
	ChargeCategory      string            `json:"ChargeCategory"`
	ChargeClass         *string           `json:"ChargeClass"`
	ChargeDescription   string            `json:"ChargeDescription"`
	ChargeFrequency     string            `json:"ChargeFrequency"`
	ChargePeriodEnd     string            `json:"ChargePeriodEnd"`
	ChargePeriodStart   string            `json:"ChargePeriodStart"`
	ConsumedQuantity    decimal.Decimal   `json:"ConsumedQuantity"`
	ConsumedUnit        string            `json:"ConsumedUnit"`
	ContractedCost      decimal.Decimal   `json:"ContractedCost"`
	ContractedUnitPrice decimal.Decimal   `json:"ContractedUnitPrice"`
	EffectiveCost       decimal.Decimal   `json:"EffectiveCost"`
	HostProviderName    *string           `json:"HostProviderName"`
	InvoiceIssuerName   string            `json:"InvoiceIssuerName"`
	ListCost            decimal.Decimal   `json:"ListCost"`
	ListUnitPrice       decimal.Decimal   `json:"ListUnitPrice"`
	PricingCategory     string            `json:"PricingCategory"`
	PricingQuantity     decimal.Decimal   `json:"PricingQuantity"`
	PricingUnit         string            `json:"PricingUnit"`
	ServiceCategory     string            `json:"ServiceCategory"`
	ServiceName         string            `json:"ServiceName"`
	ServiceProviderName string            `json:"ServiceProviderName"`
	ServiceSubcategory  string            `json:"ServiceSubcategory"`
	SkuID               string            `json:"SkuId"`
	SkuMeter            TokenClass        `json:"SkuMeter"`
	SkuPriceID          string            `json:"SkuPriceId"`
	SubAccountID        *string           `json:"SubAccountId"`
	SubAccountName      *string           `json:"SubAccountName"`
	Tags                map[string]string `json:"Tags"`
	CatalogVersion      *string           `json:"x_CostroidCatalogVersion"`
	OvershootBound      *decimal.Decimal  `json:"x_CostroidOvershootBound"`
	RowID               string            `json:"x_CostroidRowId"`
}

// Check refuses a row the contract's arithmetic rules out. Rounding to fit is
// never an option: a figure past the scale limit is an error.
func (r Row) Check() error {
	amounts := map[string]decimal.Decimal{
		"ConsumedQuantity":    r.ConsumedQuantity,
		"ContractedCost":      r.ContractedCost,
		"ContractedUnitPrice": r.ContractedUnitPrice,
		"EffectiveCost":       r.EffectiveCost,
		"ListCost":            r.ListCost,
		"ListUnitPrice":       r.ListUnitPrice,
		"PricingQuantity":     r.PricingQuantity,
	}
	for column, amount := range amounts {
		if amount.Scale() > MaxScale {
			return fmt.Errorf("focus: %s %s has more than %d fractional digits", column, amount, MaxScale)
		}
		if amount.Sign() < 0 {
			return fmt.Errorf("focus: %s %s is negative", column, amount)
		}
	}
	if r.BilledCost.Sign() != 0 {
		return fmt.Errorf("focus: BilledCost %s is not 0", r.BilledCost)
	}
	if r.ListCost.Cmp(r.ListUnitPrice.Mul(r.PricingQuantity)) != 0 {
		return fmt.Errorf("focus: ListCost %s is not ListUnitPrice %s times PricingQuantity %s", r.ListCost, r.ListUnitPrice, r.PricingQuantity)
	}
	if r.ContractedCost.Cmp(r.ContractedUnitPrice.Mul(r.PricingQuantity)) != 0 {
		return fmt.Errorf("focus: ContractedCost %s is not ContractedUnitPrice %s times PricingQuantity %s", r.ContractedCost, r.ContractedUnitPrice, r.PricingQuantity)
	}
	return nil
}
