package receipt

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
)

const (
	// provider is the price sheet a Claude Code entry is priced at.
	provider = "anthropic"
	// rowNote says in every row's description what the row sums.
	rowNote = "the requests of one session on one model, summed, counted on the author's machine"
)

// The tags every row from a developer's machine carries, each one value, as
// tag set 0.5 holds them.
var receiptTags = map[string]string{
	focus.TagCostSource:       "agent_session_file",
	focus.TagCostBasis:        string(focus.ListEquivalent),
	focus.TagAttributionGrade: "unattributed",
	focus.TagEnforcementGrade: "none",
	focus.TagRunIdentity:      "unverified",
	policy.TagOrigin:          policy.OriginDeveloperMachine,
}

// Rows prices a receipt's entries at the catalog the report's own binary
// loads, one row per entry and billed class that holds tokens. The receipt
// states counts and no price, so every price is the catalog's. A model the
// sheet does not name takes the sheet's fallback, which is never zero. ci
// holds the tags the CI system states, the repository, the pull request and
// the head; a CI run is never one, because the sessions ran in none. An entry
// whose session the run's own rows also hold is left out: held reports it.
// The rows are for the report's memory, and no file.
func Rows(r Receipt, prices *catalog.Catalog, ci map[string]string, held func(session string) bool) (Priced, error) {
	serviceProvider, err := prices.ServiceProviderName(provider)
	if err != nil {
		return Priced{}, fmt.Errorf("receipt: %w", err)
	}
	var rows []focus.Row
	for _, session := range r.Sessions {
		if held != nil && held(session.SessionID) {
			continue
		}
		priced, err := session.rows(prices, serviceProvider, ci)
		if err != nil {
			return Priced{}, fmt.Errorf("receipt: session %s, model %s: %w", session.SessionID, session.Model, err)
		}
		rows = append(rows, priced...)
	}
	return Priced{rows: rows}, nil
}

// classes are an entry's billed classes in the row contract's order.
func (s Session) classes() []struct {
	class focus.TokenClass
	count string
} {
	return []struct {
		class focus.TokenClass
		count string
	}{
		{focus.InputUncached, s.Tokens.InputUncached},
		{focus.CacheRead, s.Tokens.CacheRead},
		{focus.CacheWrite5m, s.Tokens.CacheWrite5m},
		{focus.CacheWrite1h, s.Tokens.CacheWrite1h},
		{focus.Output, s.Tokens.Output},
	}
}

func (s Session) rows(prices *catalog.Catalog, serviceProvider string, ci map[string]string) ([]focus.Row, error) {
	first, err := day(s.FirstDay)
	if err != nil {
		return nil, err
	}
	last, err := day(s.LastDay)
	if err != nil {
		return nil, err
	}
	largest, err := strconv.ParseInt(s.LargestInput, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("largest_input: %w", err)
	}
	tier, region := cmp.Or(orEmpty(s.ServiceTier), catalog.TierStandard), cmp.Or(orEmpty(s.Region), catalog.RegionGlobal)
	standIn := fmt.Sprintf("receipt:%s:%s:%s:%s:%s", s.Agent, s.SessionID, s.Model, orEmpty(s.ServiceTier), orEmpty(s.Region))
	var rows []focus.Row
	for _, entry := range s.classes() {
		tokens, err := strconv.ParseInt(entry.count, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.class, err)
		}
		if tokens == 0 {
			continue
		}
		price, err := prices.Lookup(catalog.Query{
			Provider: provider, Model: s.Model, Class: entry.class, ServiceTier: tier, Region: region,
			ContextInputTokens: largest, At: first,
		})
		if err != nil {
			return nil, err
		}
		notes := []string{rowNote}
		if price.Fallback {
			notes = append(notes, focus.NoteFallback)
		}
		row := focus.NewRow(focus.Charge{
			RowID:               focus.RowIdentity(standIn, entry.class),
			Provider:            provider,
			ServiceProviderName: serviceProvider,
			Model:               s.Model,
			Class:               entry.class,
			Tokens:              tokens,
			Start:               first,
			End:                 last.AddDate(0, 0, 1),
			Region:              region,
			ServiceTier:         tier,
			CatalogVersion:      prices.Version(),
			Price:               &focus.Price{SkuPriceID: price.SkuPriceID, PricingUnit: price.PricingUnit, ListUnitPrice: price.ListUnitPrice},
			Basis:               focus.ListEquivalent,
			Tags:                s.tags(entry.class, ci),
			Notes:               notes,
		})
		if err := row.Check(); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// tags are a row's tags: the CI system's, the receipt's closed values, and
// the entry's agent, version, session and model.
func (s Session) tags(class focus.TokenClass, ci map[string]string) map[string]string {
	tags := map[string]string{}
	for _, name := range []string{focus.TagRepo, focus.TagPR, focus.TagCommitSHA} {
		if value := ci[name]; value != "" {
			tags[name] = value
		}
	}
	maps.Copy(tags, receiptTags)
	tags[focus.TagAgent] = s.Agent
	tags[focus.TagAgentVersion] = s.AgentVersion
	tags[focus.TagSessionID] = s.SessionID
	tags[focus.TagModel] = s.Model
	if slices.Contains(s.AssumedTTL, class.Lifetime()) {
		tags[focus.TagAssumedTTL] = class.Lifetime()
	}
	return tags
}

// Priced are the rows Rows built from a receipt, in memory. Only Rows makes
// them, so no row a file held can reach a receipt's figure: every reader of a
// rows file refuses a row that states costroid/origin, and none can make
// Priced.
type Priced struct {
	rows []focus.Row
}

// Figure is the exact sum of ListCost over a receipt's rows in one currency.
type Figure struct {
	Currency string
	Cost     decimal.Decimal
	Rows     int
}

// Figures sums the rows from a developer's machine, one figure per currency,
// sorted by currency. A row policy.Classify does not read as one is left
// out, so a run's row never joins a receipt's figure.
func Figures(priced Priced) []Figure {
	sums := map[string]*Figure{}
	for _, row := range priced.rows {
		if policy.ClassifyRow(row) != policy.ReceiptRow {
			continue
		}
		figure, found := sums[row.BillingCurrency]
		if !found {
			figure = &Figure{Currency: row.BillingCurrency}
			sums[row.BillingCurrency] = figure
		}
		figure.Cost = figure.Cost.Add(row.ListCost)
		figure.Rows++
	}
	figures := make([]Figure, 0, len(sums))
	for _, currency := range slices.Sorted(maps.Keys(sums)) {
		figures = append(figures, *sums[currency])
	}
	return figures
}

// Days are the first and last UTC day of the receipt's entries that held
// does not leave out, and how many sessions those entries are.
func Days(r Receipt, held func(session string) bool) (first, last string, sessions int) {
	seen := map[string]bool{}
	for _, session := range r.Sessions {
		if held != nil && held(session.SessionID) {
			continue
		}
		seen[session.SessionID] = true
		if first == "" || session.FirstDay < first {
			first = session.FirstDay
		}
		if session.LastDay > last {
			last = session.LastDay
		}
	}
	return first, last, len(seen)
}
