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

	// A gateway key's id is the SHA-256 of the key in lower-case hex.
	gatewayKeyIDLength = 64
	lowerHex           = "0123456789abcdef"
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
	ChargePeriodEnd     time.Time                  `json:"ChargePeriodEnd"`
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
	// crossings holds, per source and credential, the first and last day of
	// each row that ran past midnight UTC into the range or out of it.
	crossings map[account][]crossing
	// Repeats counts rows whose id was already read. A file concatenated
	// from two runs counts each request once.
	Repeats int
	// Uncredentialed counts rows that name no credential and were given none.
	Uncredentialed int
	// Gatewayed counts, per source, the rows drawn on a gateway's run key
	// when no upstream credential names the provider key the gateway holds.
	// They stay under the run key, which no provider's report names.
	Gatewayed map[string]int
}

// LedgerOptions says how a row's credential is read.
//
// Credential names the credential of rows whose SubAccountId is null.
//
// Upstream names, per source, the provider key a gateway holds. A gateway
// mints a key for each run, and a row's SubAccountId is that key's id, the
// SHA-256 of the key in hex. The provider never sees that key: it reports the
// gateway's traffic under the key the gateway sends with, so a row drawn on a
// gateway key is read as that key's.
type LedgerOptions struct {
	Credential string
	Upstream   map[string]string
}

type account struct {
	source, credential string
}

// crossing is the first and the last UTC day of one row's charge period.
type crossing struct {
	from, to string
}

type ledgerGroup struct {
	tokens map[focus.TokenClass]decimal.Decimal
	list   decimal.Decimal
	// upstream is set when the group holds rows drawn on a gateway key.
	upstream bool
	// classList is list split by class.
	classList map[focus.TokenClass]decimal.Decimal
	// rows counts the rows summed into list.
	rows            int64
	billed          bool
	assumedLifetime bool
	measured        map[recordKey]*Usage
	// skus names the catalog rows each model's class was priced at, so a
	// price proof knows which context tier the day's tokens fell in.
	skus map[priceKey][]string
}

func newLedgerGroup() *ledgerGroup {
	return &ledgerGroup{
		tokens:    map[focus.TokenClass]decimal.Decimal{},
		classList: map[focus.TokenClass]decimal.Decimal{},
		measured:  map[recordKey]*Usage{},
		skus:      map[priceKey][]string{},
	}
}

type recordKey struct {
	model, tier, region string
}

// ReadLedger totals the JSON-line rows charged inside the range. Rows that are
// not token rows from a reconciled provider are left out. A row whose
// SubAccountId is null is read as fallbackCredential when one is given.
func ReadLedger(rows io.Reader, within Range, fallbackCredential string) (*Ledger, error) {
	return ReadLedgerWith(rows, within, LedgerOptions{Credential: fallbackCredential})
}

// ReadLedgerWith is ReadLedger with every way a credential is read.
func ReadLedgerWith(rows io.Reader, within Range, options LedgerOptions) (*Ledger, error) {
	ledger := &Ledger{groups: map[Key]*ledgerGroup{}, crossings: map[account][]crossing{}, Gatewayed: map[string]int{}}
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
		if !row.reconciled() {
			continue
		}
		ledger.cross(row, within, options)
		if !within.holds(row.firstDay()) {
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
		if err := ledger.add(row, options); err != nil {
			return nil, fmt.Errorf("reconcile: read row %d: %w", number, err)
		}
	}
}

// reconciled reports whether a provider's report holds the row's tokens.
func (r ledgerRow) reconciled() bool {
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
	return basis == focus.Measured || basis == focus.ListEquivalent
}

// firstDay is the UTC day a row is dated by: the day its charge period starts.
func (r ledgerRow) firstDay() string {
	return r.ChargePeriodStart.UTC().Format(dayLayout)
}

// lastDay is the UTC day of the last instant a row's charge period holds. The
// period ends before ChargePeriodEnd, so a row that ends at midnight ends the
// day before. A row with no end, or none later than its start, holds one day.
func (r ledgerRow) lastDay() string {
	if !r.ChargePeriodEnd.After(r.ChargePeriodStart) {
		return r.firstDay()
	}
	return r.ChargePeriodEnd.Add(-time.Nanosecond).UTC().Format(dayLayout)
}

// credential reads the credential the provider reports a row's tokens under,
// and whether that is a gateway's upstream key. An empty credential is none.
func (o LedgerOptions) credential(row ledgerRow) (credential string, upstream bool) {
	credential = o.Credential
	if row.SubAccountID != nil {
		credential = *row.SubAccountID
	}
	if !isGatewayKeyID(credential) {
		return credential, false
	}
	if key, named := o.Upstream[sources[row.ServiceProviderName]]; named {
		return key, true
	}
	return credential, false
}

// isGatewayKeyID reports whether a credential has the shape of a gateway
// key's id: the SHA-256 of the key in lower-case hex. No provider's own id
// has that shape.
func isGatewayKeyID(credential string) bool {
	if len(credential) != gatewayKeyIDLength {
		return false
	}
	for _, char := range credential {
		if !strings.ContainsRune(lowerHex, char) {
			return false
		}
	}
	return true
}

// cross keeps the first and last day of a row that ran past midnight UTC and
// touches the range, so the days it left tokens on can be compared as one.
func (l *Ledger) cross(row ledgerRow, within Range, options LedgerOptions) {
	from, to := row.firstDay(), row.lastDay()
	if from == to || to < within.From.Format(dayLayout) || from > within.To.Format(dayLayout) {
		return
	}
	credential, _ := options.credential(row)
	if credential == "" {
		return
	}
	at := account{sources[row.ServiceProviderName], credential}
	if !slices.Contains(l.crossings[at], crossing{from, to}) {
		l.crossings[at] = append(l.crossings[at], crossing{from, to})
	}
}

func (l *Ledger) add(row ledgerRow, options LedgerOptions) error {
	credential, upstream := options.credential(row)
	if credential == "" {
		l.Uncredentialed++
		return nil
	}
	if isGatewayKeyID(credential) {
		l.Gatewayed[sources[row.ServiceProviderName]]++
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
	key := Key{sources[row.ServiceProviderName], credential, row.firstDay()}
	group := l.groups[key]
	if group == nil {
		group = newLedgerGroup()
		l.groups[key] = group
	}
	group.upstream = group.upstream || upstream
	group.tokens[row.SkuMeter] = group.tokens[row.SkuMeter].Add(tokens)
	priced := priceKey{row.tag(focus.TagModel), row.tier(), row.region(), row.SkuMeter}
	if !slices.Contains(group.skus[priced], row.SkuPriceID) {
		group.skus[priced] = append(group.skus[priced], row.SkuPriceID)
	}
	if row.tag(focus.TagAssumedTTL) != "" || strings.Contains(row.ChargeDescription, lifetimeAssumedNote) {
		group.assumedLifetime = true
	}
	if focus.CostBasis(row.tag(focus.TagCostBasis)) == focus.Measured {
		group.billed = true
		group.list = group.list.Add(list)
		group.classList[row.SkuMeter] = group.classList[row.SkuMeter].Add(list)
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
	group := newLedgerGroup()
	group.billed = true
	return group
}

// Sources lists the reports this ledger needs, in order.
func (l *Ledger) Sources() []string {
	var names []string
	for _, each := range l.accounts() {
		if !slices.Contains(names, each.source) {
			names = append(names, each.source)
		}
	}
	slices.Sort(names)
	return names
}

// Credentials lists the credentials the ledger names for one source, in order.
func (l *Ledger) Credentials(source string) []string {
	var names []string
	for _, each := range l.accounts() {
		if each.source == source && !slices.Contains(names, each.credential) {
			names = append(names, each.credential)
		}
	}
	slices.Sort(names)
	return names
}

// accounts lists every source and credential the range holds tokens of,
// with those of a run that started before the range and ran into it.
func (l *Ledger) accounts() []account {
	var all []account
	for key := range l.groups {
		all = append(all, account{key.Source, key.Credential})
	}
	for each := range l.crossings {
		all = append(all, each)
	}
	return all
}

// span is the run of days joined to the key's day by rows that ran past
// midnight UTC on its credential. A day no such row touches spans itself.
func (l *Ledger) span(key Key) (from, to string) {
	from, to = key.Day, key.Day
	crossings := l.crossings[account{key.Source, key.Credential}]
	for grown := true; grown; {
		grown = false
		for _, each := range crossings {
			if each.from > to || each.to < from {
				continue
			}
			if each.from < from {
				from, grown = each.from, true
			}
			if each.to > to {
				to, grown = each.to, true
			}
		}
	}
	return from, to
}

// joined totals the tokens of a credential's days, and their list cost by
// class, as one group.
func (l *Ledger) joined(source, credential string, days []string) *ledgerGroup {
	total := newLedgerGroup()
	for _, day := range days {
		group := l.groups[Key{source, credential, day}]
		if group == nil {
			continue
		}
		for class, tokens := range group.tokens {
			total.tokens[class] = total.tokens[class].Add(tokens)
		}
		for class, list := range group.classList {
			total.classList[class] = total.classList[class].Add(list)
		}
		total.assumedLifetime = total.assumedLifetime || group.assumedLifetime
	}
	return total
}
