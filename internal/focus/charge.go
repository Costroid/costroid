package focus

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	currency          = "USD"
	unknownAccount    = "unknown"
	tokensUnit        = "Tokens"
	informationalUnit = "Informational Tokens"
	tokensPerMillion  = 6

	// InstantLayout is the row contract's form of a UTC instant.
	InstantLayout = "2006-01-02T15:04:05Z"

	// RegionGlobal is default routing, which is no distinct region, so a row
	// served there carries a null RegionId.
	RegionGlobal = "global"
)

// Notes more than one writer of rows puts in ChargeDescription.
const (
	NoteInformational = "informational"
	NoteFallback      = "priced at the catalog fallback tier: the sheet does not name this model, tier and class"
)

// ResidencyNote states a residency multiplier in ChargeDescription.
func ResidencyNote(multiplier decimal.Decimal, region string) string {
	return fmt.Sprintf("list price times %s for data residency in %s", multiplier, region)
}

// Price is what a row needs from a catalog answer.
type Price struct {
	SkuPriceID    string
	PricingUnit   string
	ListUnitPrice decimal.Decimal
}

// Charge holds the facts of one request's one token class. A nil Price means
// an informational class, which never carries a cost. Its SkuPriceId takes the
// shape of the catalog's fallback ids, provider:informational:class.
type Charge struct {
	RowID               string
	Provider            string
	ServiceProviderName string
	Model               string
	Class               TokenClass
	Tokens              int64
	Start, End          time.Time
	Account             string
	Credential          string
	Region              string
	ServiceTier         string
	CatalogVersion      string
	Price               *Price
	Basis               CostBasis
	Tags                map[string]string
	Notes               []string
	OvershootBound      *decimal.Decimal
}

// NewRow builds the row of one charge. Both the CLI and the ingest service
// write rows through it, so a row reads the same whichever of them priced it.
func NewRow(c Charge) Row {
	start, end := ChargePeriod(c.Start, c.End)
	periodStart, periodEnd := BillingPeriod(start)
	row := Row{
		BillingAccountID:    cmp.Or(c.Account, unknownAccount),
		BillingCurrency:     currency,
		BillingPeriodEnd:    periodEnd.Format(InstantLayout),
		BillingPeriodStart:  periodStart.Format(InstantLayout),
		ChargeCategory:      "Usage",
		ChargeDescription:   description(c),
		ChargeFrequency:     "Usage-Based",
		ChargePeriodEnd:     end.Format(InstantLayout),
		ChargePeriodStart:   start.Format(InstantLayout),
		ConsumedQuantity:    decimal.FromInt(c.Tokens),
		HostProviderName:    &c.ServiceProviderName,
		InvoiceIssuerName:   c.ServiceProviderName,
		PricingCategory:     "Standard",
		ServiceCategory:     "AI and Machine Learning",
		ServiceName:         c.ServiceProviderName + " API",
		ServiceProviderName: c.ServiceProviderName,
		ServiceSubcategory:  "Generative AI",
		SkuID:               fmt.Sprintf("%s/%s/%s", c.Provider, c.Model, c.Class),
		SkuMeter:            c.Class,
		SubAccountID:        optional(c.Credential),
		Tags:                c.Tags,
		CatalogVersion:      &c.CatalogVersion,
		OvershootBound:      c.OvershootBound,
		RowID:               c.RowID,
		ServiceTier:         optional(c.ServiceTier),
	}
	if c.Region != "" && c.Region != RegionGlobal {
		row.RegionID, row.RegionName = &c.Region, &c.Region
	}
	if c.Price == nil {
		row.ConsumedUnit = informationalUnit
		row.PricingUnit = informationalUnit
		row.PricingQuantity = row.ConsumedQuantity
		row.SkuPriceID = fmt.Sprintf("%s:%s:%s", c.Provider, NoteInformational, c.Class)
		return row
	}
	quantity := row.ConsumedQuantity.DivPow10(tokensPerMillion)
	cost := c.Price.ListUnitPrice.Mul(quantity)
	row.ConsumedUnit = tokensUnit
	row.PricingUnit = c.Price.PricingUnit
	row.PricingQuantity = quantity
	row.SkuPriceID = c.Price.SkuPriceID
	row.ListUnitPrice = c.Price.ListUnitPrice
	row.ListCost = cost
	row.ContractedUnitPrice = c.Price.ListUnitPrice
	row.ContractedCost = cost
	if c.Basis == Measured {
		row.EffectiveCost = cost
	}
	return row
}

func optional(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

func description(c Charge) string {
	head := fmt.Sprintf("%s %s tokens", c.Model, c.Class)
	return strings.Join(append([]string{head}, c.Notes...), "; ")
}

// ChargePeriod widens a span to whole UTC seconds. The end is exclusive, so a
// span inside one second still ends after it starts.
func ChargePeriod(start, end time.Time) (time.Time, time.Time) {
	first := start.UTC().Truncate(time.Second)
	last := end.UTC().Truncate(time.Second)
	if last.Before(end.UTC()) || !last.After(first) {
		last = last.Add(time.Second)
	}
	return first, last
}

// BillingPeriod is the UTC month holding an instant.
func BillingPeriod(at time.Time) (time.Time, time.Time) {
	first := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, 1, 0)
}

// RowIdentity is the row contract's id: SHA-256 of the provider's request id
// and the token class. Two writers of one request arrive at one id.
func RowIdentity(requestID string, class TokenClass) string {
	sum := sha256.Sum256([]byte(requestID + ":" + string(class)))
	return hex.EncodeToString(sum[:])
}
