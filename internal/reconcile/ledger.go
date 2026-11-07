package reconcile

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	tokensUnit = "Tokens"
	currency   = "USD"

	// Notes the wrapper writes into ChargeDescription. A row before version 0.3
	// has no tag for the first, and a row of version 0.1 no column for the second.
	lifetimeAssumedNote = "cache lifetime assumed"
	residencyNote       = "for data residency in "

	skuPriceFields = 6
	skuPriceTier   = 3
)

// sources maps a row's ServiceProviderName to its report.
var sources = map[string]string{
	"Anthropic": SourceAnthropic,
	"OpenAI":    SourceOpenAI,
}

// ledgerRow is the part of a row reconciliation reads.
type ledgerRow struct {
	BillingCurrency     string                     `json:"BillingCurrency"`
	ChargeDescription   string                     `json:"ChargeDescription"`
	ChargePeriodStart   time.Time                  `json:"ChargePeriodStart"`
	ConsumedQuantity    string                     `json:"ConsumedQuantity"`
	ConsumedUnit        string                     `json:"ConsumedUnit"`
	HostProviderName    *string                    `json:"HostProviderName"`
	ListCost            string                     `json:"ListCost"`
	RegionID            *string                    `json:"RegionId"`
	ServiceProviderName string                     `json:"ServiceProviderName"`
	SkuMeter            focus.TokenClass           `json:"SkuMeter"`
	SkuPriceID          string                     `json:"SkuPriceId"`
	SubAccountID        *string                    `json:"SubAccountId"`
	Tags                map[string]json.RawMessage `json:"Tags"`
	RowID               string                     `json:"x_CostroidRowId"`
	ServiceTier         *string                    `json:"x_CostroidServiceTier"`
}

// tag reads one tag. A value that is not a string reads as absent, as a tag
// the row never set: the convention's tags are strings.
func (r ledgerRow) tag(key string) string {
	var value string
	if err := json.Unmarshal(r.Tags[key], &value); err != nil {
		return ""
	}
	return value
}

// Ledger is the rows of one range, totalled per credential per day.
type Ledger struct {
	groups map[Key]*ledgerGroup
	// Repeats counts rows whose id was already read. A file concatenated
	// from two runs counts each request once.
	Repeats int
	// Uncredentialed counts rows that name no credential and were given none.
	Uncredentialed int
}

type ledgerGroup struct {
	tokens map[focus.TokenClass]decimal.Decimal
	list   decimal.Decimal
	// rows counts the rows summed into list.
	rows            int64
	billed          bool
	assumedLifetime bool
	measured        map[recordKey]*Usage
}

type recordKey struct {
	model, tier, region string
}

// ReadLedger totals the JSON-line rows charged inside the range. Rows that are
// not token rows from a reconciled provider are left out. A row whose
// SubAccountId is null is read as fallbackCredential when one is given.
func ReadLedger(rows io.Reader, within Range, fallbackCredential string) (*Ledger, error) {
	ledger := &Ledger{groups: map[Key]*ledgerGroup{}}
	seen := map[string]bool{}
	decoder := json.NewDecoder(rows)
	for number := 1; ; number++ {
		var row ledgerRow
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return ledger, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("reconcile: the rows file ends inside row %d, so a row is missing from the total", number)
		}
		if err != nil {
			return nil, fmt.Errorf("reconcile: read row %d: %w", number, err)
		}
		if !row.inScope(within) {
			continue
		}
		if row.RowID == "" {
			return nil, fmt.Errorf("reconcile: read row %d: no x_CostroidRowId", number)
		}
		if seen[row.RowID] {
			ledger.Repeats++
			continue
		}
		seen[row.RowID] = true
		if err := ledger.add(row, fallbackCredential); err != nil {
			return nil, fmt.Errorf("reconcile: read row %d: %w", number, err)
		}
	}
}

func (r ledgerRow) inScope(within Range) bool {
	source, known := sources[r.ServiceProviderName]
	if !known || r.SkuMeter.Informational() || r.ConsumedUnit != tokensUnit {
		return false
	}
	// A provider's model served by a cloud is billed by the cloud, and its
	// tokens are not on the provider's report.
	if r.HostProviderName != nil && sources[*r.HostProviderName] != source {
		return false
	}
	basis := focus.CostBasis(r.tag(focus.TagCostBasis))
	if basis != focus.Measured && basis != focus.ListEquivalent {
		return false
	}
	return within.holds(r.ChargePeriodStart.UTC().Format(dayLayout))
}

func (l *Ledger) add(row ledgerRow, fallbackCredential string) error {
	credential := fallbackCredential
	if row.SubAccountID != nil {
		credential = *row.SubAccountID
	}
	if credential == "" {
		l.Uncredentialed++
		return nil
	}
	if row.BillingCurrency != currency {
		return fmt.Errorf("currency %q: list prices are in USD and currencies never mix", row.BillingCurrency)
	}
	tokens, err := decimal.Parse(row.ConsumedQuantity)
	if err != nil {
		return fmt.Errorf("ConsumedQuantity: %w", err)
	}
	list, err := decimal.Parse(row.ListCost)
	if err != nil {
		return fmt.Errorf("ListCost: %w", err)
	}
	key := Key{sources[row.ServiceProviderName], credential, row.ChargePeriodStart.UTC().Format(dayLayout)}
	group := l.groups[key]
	if group == nil {
		group = &ledgerGroup{tokens: map[focus.TokenClass]decimal.Decimal{}, measured: map[recordKey]*Usage{}}
		l.groups[key] = group
	}
	group.tokens[row.SkuMeter] = group.tokens[row.SkuMeter].Add(tokens)
	if row.tag(focus.TagAssumedTTL) != "" || strings.Contains(row.ChargeDescription, lifetimeAssumedNote) {
		group.assumedLifetime = true
	}
	if focus.CostBasis(row.tag(focus.TagCostBasis)) == focus.Measured {
		group.billed = true
		group.list = group.list.Add(list)
		group.rows++
		group.addMeasured(row, tokens)
	}
	return nil
}

func (g *ledgerGroup) addMeasured(row ledgerRow, tokens decimal.Decimal) {
	key := recordKey{row.tag(focus.TagModel), row.tier(), row.region()}
	record := g.measured[key]
	if record == nil {
		record = &Usage{Model: key.model, Tier: key.tier, Region: key.region, Tokens: map[focus.TokenClass]decimal.Decimal{}}
		g.measured[key] = record
	}
	record.Tokens[row.SkuMeter] = record.Tokens[row.SkuMeter].Add(tokens)
}

// tier reads the row's own column. A version 0.1 row has none, so it reads the
// tier from provider:model:region:tier:context:class, and a fallback row names
// none there: it was priced at no tier.
func (r ledgerRow) tier() string {
	if r.ServiceTier != nil {
		return *r.ServiceTier
	}
	fields := strings.Split(r.SkuPriceID, ":")
	if len(fields) != skuPriceFields {
		return catalog.TierStandard
	}
	return fields[skuPriceTier]
}

// region reads RegionId. A version 0.1 row has no such column, and states the
// region a residency multiplier was applied for in its description.
func (r ledgerRow) region() string {
	if r.RegionID != nil {
		return *r.RegionID
	}
	_, rest, found := strings.Cut(r.ChargeDescription, residencyNote)
	if !found {
		return catalog.RegionGlobal
	}
	region, _, _ := strings.Cut(rest, ";")
	return region
}

func (g *ledgerGroup) records() []Usage {
	records := make([]Usage, 0, len(g.measured))
	for _, record := range g.measured {
		records = append(records, *record)
	}
	slices.SortFunc(records, func(a, b Usage) int {
		return cmp.Or(cmp.Compare(a.Model, b.Model), cmp.Compare(a.Tier, b.Tier), cmp.Compare(a.Region, b.Region))
	})
	return records
}

// group answers for a day the ledger holds nothing on with an empty group, so
// a provider's traffic on it reads as a miss.
func (l *Ledger) group(key Key) *ledgerGroup {
	if group := l.groups[key]; group != nil {
		return group
	}
	return &ledgerGroup{tokens: map[focus.TokenClass]decimal.Decimal{}, measured: map[recordKey]*Usage{}, billed: true}
}

// Sources lists the reports this ledger needs, in order.
func (l *Ledger) Sources() []string {
	var names []string
	for key := range l.groups {
		if !slices.Contains(names, key.Source) {
			names = append(names, key.Source)
		}
	}
	slices.Sort(names)
	return names
}

// Credentials lists the credentials the ledger names for one source, in order.
func (l *Ledger) Credentials(source string) []string {
	var names []string
	for key := range l.groups {
		if key.Source == source && !slices.Contains(names, key.Credential) {
			names = append(names, key.Credential)
		}
	}
	slices.Sort(names)
	return names
}
