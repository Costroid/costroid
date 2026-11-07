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
	// held is the run's gateway key, nil without one. Its id is the rows'
	// credential, and its limits state the overshoot bound.
	held *heldKey
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
		if err := request.Tokens.Check(); err != nil {
			return nil, fmt.Errorf("run: request %s: %w", request.ID, err)
		}
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
		Credential:          b.credentialID(),
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
	if b.enforcement.Grade == adapter.GradeGatewayPrecheck && b.held != nil {
		bound, err := overshootBound(b.catalog, b.held.grant, request)
		if err != nil {
			return focus.Row{}, err
		}
		charge.OvershootBound = &bound
	}
	var own []string
	if request.Note != "" {
		own = []string{request.Note}
	}
	charge.Notes = slices.Concat(notes, own, charge.Notes, []string{b.enforcement.Note})
	return focus.NewRow(charge), nil
}

// credentialID is the gateway key's id when the run holds one: a credential the
// wrapper drew and the gateway confirmed, in place of any the caller stated.
func (b rowBuilder) credentialID() string {
	if b.held != nil {
		return b.held.grant.KeyID
	}
	return b.credential
}

func priceNotes(price catalog.Price, request adapter.Request) []string {
	var notes []string
	if price.Fallback {
		notes = append(notes, focus.NoteFallback)
	}
	if price.ResidencyMultiplier != nil {
		notes = append(notes, focus.ResidencyNote(*price.ResidencyMultiplier, request.Region))
	}
	if note := tierNote(price, request); note != "" {
		notes = append(notes, note)
	}
	if request.AssumedWrite != "" {
		notes = append(notes, "cache lifetime assumed")
	}
	return notes
}

// tierNote says how far the context tier of an entry coarser than a request
// holds. The entry's whole input bounds each request's in it: at or below the
// matched tier's top, every request is in that tier; above a tier's floor, a
// request may be below it, and the price is an upper bound.
func tierNote(price catalog.Price, request adapter.Request) string {
	switch {
	case request.Grain == adapter.PerRequest:
		return ""
	case request.ContextInputTokens == 0:
		return fmt.Sprintf("one %s's total, so the lowest context tier applies", request.Grain)
	case price.ContextTierMin != nil:
		return fmt.Sprintf("one %s's input of %d tokens passes the %d-token context tier boundary, and its requests' own inputs are unknown, so the tier above prices it as an upper bound", request.Grain, request.ContextInputTokens, *price.ContextTierMin)
	case price.ContextTierMax != nil:
		return fmt.Sprintf("one %s's input of %d tokens is within the %d-token context tier, so each of its requests is too", request.Grain, request.ContextInputTokens, *price.ContextTierMax)
	}
	return ""
}

func (b rowBuilder) tags(report adapter.Report, request adapter.Request, class focus.TokenClass) map[string]string {
	source := report.CostSource
	if request.CostSource != "" {
		source = request.CostSource
	}
	tags := map[string]string{
		focus.TagAgent:            b.agent,
		focus.TagModel:            request.Model,
		focus.TagCostSource:       source,
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
