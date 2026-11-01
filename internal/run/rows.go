package run

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	currency = "USD"

	// unknownAccount fills BillingAccountId, which FOCUS makes mandatory,
	// when the run was given no account. An agent's stream names none.
	unknownAccount = "unknown"

	informationalUnit = "Informational Tokens"
	tokensPerMillion  = 6
	instantLayout     = "2006-01-02T15:04:05Z"

	attributionGrade = "unattributed"
	runIdentity      = "unverified"
)

// The tag convention's value patterns. A value that misses one is omitted,
// and a tag with no value is never written.
var (
	sessionTagPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	versionTagPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// rowBuilder prices one report into rows.
type rowBuilder struct {
	catalog     *catalog.Catalog
	agent       string
	enforcement adapter.Enforcement
	account     string
}

func (b rowBuilder) rows(report adapter.Report) ([]focus.Row, error) {
	var rows []focus.Row
	for _, request := range report.Requests {
		for _, count := range request.Tokens.Counts() {
			row, err := b.row(report, request, count)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (b rowBuilder) row(report adapter.Report, request adapter.Request, count adapter.Count) (focus.Row, error) {
	provider, err := b.catalog.ServiceProviderName(request.Provider)
	if err != nil {
		return focus.Row{}, err
	}
	start, end := chargePeriod(request.Start, request.End)
	periodStart, periodEnd := billingPeriod(start)
	version := b.catalog.Version()
	row := focus.Row{
		BillingAccountID:    cmp.Or(b.account, unknownAccount),
		BillingCurrency:     currency,
		BillingPeriodEnd:    periodEnd.Format(instantLayout),
		BillingPeriodStart:  periodStart.Format(instantLayout),
		ChargeCategory:      "Usage",
		ChargeFrequency:     "Usage-Based",
		ChargePeriodEnd:     end.Format(instantLayout),
		ChargePeriodStart:   start.Format(instantLayout),
		ConsumedQuantity:    decimal.FromInt(count.Tokens),
		HostProviderName:    &provider,
		InvoiceIssuerName:   provider,
		PricingCategory:     "Standard",
		ServiceCategory:     "AI and Machine Learning",
		ServiceName:         provider + " API",
		ServiceProviderName: provider,
		ServiceSubcategory:  "Generative AI",
		SkuID:               fmt.Sprintf("%s/%s/%s", request.Provider, request.Model, count.Class),
		SkuMeter:            count.Class,
		Tags:                b.tags(report, request),
		CatalogVersion:      &version,
		RowID:               rowID(request.ID, count.Class),
	}
	notes := []string{}
	if count.Class.Informational() {
		row.ConsumedUnit = informationalUnit
		row.PricingUnit = informationalUnit
		row.PricingQuantity = row.ConsumedQuantity
		row.SkuPriceID = row.SkuID + "/informational"
		notes = append(notes, "informational")
	} else {
		price, err := b.catalog.Lookup(catalog.Query{
			Provider:           request.Provider,
			Model:              request.Model,
			Class:              count.Class,
			ServiceTier:        request.ServiceTier,
			Region:             request.Region,
			ContextInputTokens: request.ContextInputTokens,
			At:                 start,
		})
		if err != nil {
			return focus.Row{}, err
		}
		applyPrice(&row, price, count.Tokens, report.CostBasis)
		notes = append(notes, priceNotes(price, request)...)
	}
	row.ChargeDescription = description(request.Model, count.Class, append(notes, b.enforcement.Note))
	return row, nil
}

func applyPrice(row *focus.Row, price catalog.Price, tokens int64, basis focus.CostBasis) {
	quantity := decimal.FromInt(tokens).DivPow10(tokensPerMillion)
	cost := price.ListUnitPrice.Mul(quantity)
	row.ConsumedUnit = "Tokens"
	row.PricingUnit = price.PricingUnit
	row.PricingQuantity = quantity
	row.SkuPriceID = price.SkuPriceID
	row.ListUnitPrice = price.ListUnitPrice
	row.ListCost = cost
	row.ContractedUnitPrice = price.ListUnitPrice
	row.ContractedCost = cost
	if basis == focus.Measured {
		row.EffectiveCost = cost
	}
}

func priceNotes(price catalog.Price, request adapter.Request) []string {
	var notes []string
	if price.Fallback {
		notes = append(notes, "priced at the catalog fallback tier: the sheet does not name this model, tier and class")
	}
	if price.ResidencyMultiplier != nil {
		notes = append(notes, fmt.Sprintf("list price times %s for data residency in %s", price.ResidencyMultiplier, request.Region))
	}
	if request.Grain != adapter.PerRequest {
		notes = append(notes, fmt.Sprintf("one %s's total, so the lowest context tier applies", request.Grain))
	}
	if request.AssumedTTL {
		notes = append(notes, "cache lifetime assumed")
	}
	return notes
}

func description(model string, class focus.TokenClass, notes []string) string {
	head := fmt.Sprintf("%s %s tokens", model, class)
	return strings.Join(append([]string{head}, notes...), "; ")
}

func (b rowBuilder) tags(report adapter.Report, request adapter.Request) map[string]string {
	tags := map[string]string{
		focus.TagAgent:            b.agent,
		focus.TagModel:            request.Model,
		focus.TagCostSource:       report.CostSource,
		focus.TagCostBasis:        string(report.CostBasis),
		focus.TagAttributionGrade: attributionGrade,
		focus.TagEnforcementGrade: b.enforcement.Grade,
		focus.TagRunIdentity:      runIdentity,
	}
	if sessionTagPattern.MatchString(report.SessionID) {
		tags[focus.TagSessionID] = report.SessionID
	}
	if versionTagPattern.MatchString(report.AgentVersion) {
		tags[focus.TagAgentVersion] = report.AgentVersion
	}
	return tags
}

// chargePeriod widens a span to whole UTC seconds. The end is exclusive, so a
// span inside one second still ends after it starts.
func chargePeriod(start, end time.Time) (time.Time, time.Time) {
	first := start.UTC().Truncate(time.Second)
	last := end.UTC().Truncate(time.Second)
	if last.Before(end.UTC()) || !last.After(first) {
		last = last.Add(time.Second)
	}
	return first, last
}

func billingPeriod(at time.Time) (time.Time, time.Time) {
	first := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, 1, 0)
}

func rowID(requestID string, class focus.TokenClass) string {
	sum := sha256.Sum256([]byte(requestID + ":" + string(class)))
	return hex.EncodeToString(sum[:])
}

func listTotal(rows []focus.Row) decimal.Decimal {
	var total decimal.Decimal
	for _, row := range rows {
		total = total.Add(row.ListCost)
	}
	return total
}
