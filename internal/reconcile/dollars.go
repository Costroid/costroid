package reconcile

import (
	"fmt"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	tokensPerMillion = 6

	// tierPriority is Anthropic's committed tier. Its tokens carry no cost on
	// the report, because the commitment fee is billed elsewhere.
	tierPriority = "priority"
)

// dollars compares list-price ledger cost with the provider's token cost, then
// walks from one to the other. Each step of the walk prices the same tokens two
// ways through the pinned catalog, so a cause is an amount and never a guess.
// What the walk does not reach stays unattributed.
func (c comparison) dollars(key Key, group *ledgerGroup, usage []Usage, tokens []TokenCheck) (DollarCheck, []string, error) {
	cost := c.report.costOf(key)
	check := DollarCheck{
		Verdict:           NoBill,
		Ledger:            group.list,
		Provider:          cost.amount,
		ProviderEffective: cost.amount,
		ProviderList:      cost.list,
		OtherCost:         cost.other,
		Causes:            []Cause{},
	}
	if !group.billed {
		return check, []string{"every ledger row is list-equivalent, so no API bill exists to compare"}, nil
	}
	check.Verdict = Outside
	if within(check.Ledger, check.Provider, DollarTolerancePercent) {
		check.Verdict = Within
	}
	check.Residual = check.Provider.Sub(check.Ledger)
	if cost.list != nil {
		rounding, err := roundingOf(*cost.list, group)
		if err != nil {
			return DollarCheck{}, nil, err
		}
		check.Rounding = &rounding
	}
	walk := walker{catalog: c.options.Catalog, source: key.Source, at: parseDay(key.Day)}
	mine, err := walk.price(group.records())
	if err != nil {
		return DollarCheck{}, nil, err
	}
	theirs, err := walk.price(usage)
	if err != nil {
		return DollarCheck{}, nil, err
	}
	amounts := []Cause{
		{CauseTier, theirs.tierPremium().Sub(mine.tierPremium())},
		{CauseResidency, theirs.residencyPremium().Sub(mine.residencyPremium())},
	}
	if providerHigh(tokens) {
		amounts = append(amounts, Cause{CauseSharedKey, theirs.standard.Sub(mine.standard)})
	}
	if cost.list != nil {
		amounts = append(amounts, Cause{CauseContractRate, cost.amount.Sub(*cost.list)})
	}
	check.Unattributed = check.Residual
	for _, cause := range amounts {
		if cause.Amount.Sign() != 0 {
			check.Causes = append(check.Causes, cause)
			check.Unattributed = check.Unattributed.Sub(cause.Amount)
		}
	}
	return check, slices.Concat(mine.notes, theirs.notes), nil
}

// roundingOf compares the provider's list figure with the ledger's list cost,
// the same charges at the same prices, so what separates them past the
// tolerance is more than rounding.
func roundingOf(list decimal.Decimal, group *ledgerGroup) (RoundingCheck, error) {
	variance := list.Sub(group.list)
	within, err := WithinRounding(currency, group.rows, variance)
	if err != nil {
		return RoundingCheck{}, err
	}
	return RoundingCheck{Currency: currency, Rows: group.rows, Variance: variance, Within: within}, nil
}

// providerHigh reports a token miss the provider's side of. Traffic the ledger
// never saw on the credential is what a shared key looks like.
func providerHigh(tokens []TokenCheck) bool {
	return slices.ContainsFunc(tokens, func(check TokenCheck) bool {
		return !check.Within && check.Delta.Sign() > 0
	})
}

type walker struct {
	catalog *catalog.Catalog
	source  string
	at      time.Time
}

// pricing is one set of tokens at three bases: standard tier in the global
// region, the tier served in the global region, and the tier and region served.
type pricing struct {
	standard, tiered, served decimal.Decimal
	notes                    []string
}

func (p pricing) tierPremium() decimal.Decimal      { return p.tiered.Sub(p.standard) }
func (p pricing) residencyPremium() decimal.Decimal { return p.served.Sub(p.tiered) }

func (w walker) price(records []Usage) (pricing, error) {
	var total pricing
	for _, record := range records {
		for class, tokens := range record.Tokens {
			if tokens.Sign() == 0 {
				continue
			}
			standard, err := w.unitPrice(record.Model, class, catalog.TierStandard, catalog.RegionGlobal)
			if err != nil {
				return pricing{}, err
			}
			tiered, served, note, err := w.servedPrices(record, class, standard)
			if err != nil {
				return pricing{}, err
			}
			quantity := tokens.DivPow10(tokensPerMillion)
			total.standard = total.standard.Add(standard.ListUnitPrice.Mul(quantity))
			total.tiered = total.tiered.Add(tiered.Mul(quantity))
			total.served = total.served.Add(served.Mul(quantity))
			if note != "" && !slices.Contains(total.notes, note) {
				total.notes = append(total.notes, note)
			}
		}
	}
	slices.Sort(total.notes)
	return total, nil
}

// servedPrices prices one class at the tier and region served. A tier the
// sheet does not name keeps the standard price and a note, so its effect stays
// unattributed instead of taking the sheet's fallback.
func (w walker) servedPrices(record Usage, class focus.TokenClass, standard catalog.Price) (decimal.Decimal, decimal.Decimal, string, error) {
	if w.source == SourceAnthropic && record.Tier == tierPriority {
		return decimal.Decimal{}, decimal.Decimal{}, "", nil
	}
	tier, err := w.unitPrice(record.Model, class, record.Tier, catalog.RegionGlobal)
	if err != nil {
		return decimal.Decimal{}, decimal.Decimal{}, "", err
	}
	note := ""
	if tier.Fallback && !standard.Fallback {
		tier = standard
		note = fmt.Sprintf("tier %s has no catalog price for %s, so its effect stays unattributed", record.Tier, record.Model)
	}
	served := tier.ListUnitPrice
	if record.Region != "" && record.Region != catalog.RegionGlobal {
		regional, err := w.unitPrice(record.Model, class, record.Tier, record.Region)
		if err != nil {
			return decimal.Decimal{}, decimal.Decimal{}, "", err
		}
		if regional.ResidencyMultiplier != nil {
			served = tier.ListUnitPrice.Mul(*regional.ResidencyMultiplier)
		}
	}
	return tier.ListUnitPrice, served, note, nil
}

func (w walker) unitPrice(model string, class focus.TokenClass, tier, region string) (catalog.Price, error) {
	return w.catalog.Lookup(catalog.Query{
		Provider:    w.source,
		Model:       model,
		Class:       class,
		ServiceTier: tier,
		Region:      region,
		At:          w.at,
	})
}
