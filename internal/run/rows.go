package run

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	attributionGrade = "unattributed"
	runIdentity      = "unverified"
	nestedNote       = "nested session: started and ended inside this run"
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
	credential  string
	ci          cicontext.Context
}

// rows prices the run's requests, then each nested session's under its own
// session id, with a note that names it nested.
func (b rowBuilder) rows(report adapter.Report) ([]focus.Row, error) {
	rows, err := b.sessionRows(report, nil)
	if err != nil {
		return nil, err
	}
	for _, nested := range report.Nested {
		more, err := b.sessionRows(nested, []string{nestedNote})
		if err != nil {
			return nil, err
		}
		rows = append(rows, more...)
	}
	return rows, nil
}

func (b rowBuilder) sessionRows(report adapter.Report, notes []string) ([]focus.Row, error) {
	var rows []focus.Row
	for _, request := range report.Requests {
		for _, count := range request.Tokens.Counts() {
			row, err := b.row(report, request, count, notes)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (b rowBuilder) row(report adapter.Report, request adapter.Request, count adapter.Count, notes []string) (focus.Row, error) {
	provider, err := b.catalog.ServiceProviderName(request.Provider)
	if err != nil {
		return focus.Row{}, err
	}
	charge := focus.Charge{
		RowID:               focus.RowIdentity(request.ID, count.Class),
		Provider:            request.Provider,
		ServiceProviderName: provider,
		Model:               request.Model,
		Class:               count.Class,
		Tokens:              count.Tokens,
		Start:               request.Start,
		End:                 request.End,
		Account:             b.account,
		Credential:          b.credential,
		Region:              request.Region,
		ServiceTier:         request.ServiceTier,
		CatalogVersion:      b.catalog.Version(),
		Basis:               report.CostBasis,
		Tags:                b.tags(report, request, count.Class),
	}
	if count.Class.Informational() {
		charge.Notes = []string{focus.NoteInformational}
	} else {
		start, _ := focus.ChargePeriod(request.Start, request.End)
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
		charge.Price = &focus.Price{SkuPriceID: price.SkuPriceID, PricingUnit: price.PricingUnit, ListUnitPrice: price.ListUnitPrice}
		charge.Notes = priceNotes(price, request)
	}
	charge.Notes = slices.Concat(notes, charge.Notes, []string{b.enforcement.Note})
	return focus.NewRow(charge), nil
}

func priceNotes(price catalog.Price, request adapter.Request) []string {
	var notes []string
	if price.Fallback {
		notes = append(notes, focus.NoteFallback)
	}
	if price.ResidencyMultiplier != nil {
		notes = append(notes, focus.ResidencyNote(*price.ResidencyMultiplier, request.Region))
	}
	if request.Grain != adapter.PerRequest {
		notes = append(notes, fmt.Sprintf("one %s's total, so the lowest context tier applies", request.Grain))
	}
	if request.AssumedWrite != "" {
		notes = append(notes, "cache lifetime assumed")
	}
	return notes
}

func (b rowBuilder) tags(report adapter.Report, request adapter.Request, class focus.TokenClass) map[string]string {
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
	if class == request.AssumedWrite {
		tags[focus.TagAssumedTTL] = class.Lifetime()
	}
	maps.Copy(tags, b.ci.Tags())
	return tags
}

func listTotal(rows []focus.Row) decimal.Decimal {
	var total decimal.Decimal
	for _, row := range rows {
		total = total.Add(row.ListCost)
	}
	return total
}
