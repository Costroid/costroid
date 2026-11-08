package run

import (
	"crypto/sha256"
	"encoding/hex"
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
	// unknownModel stands in for a model name outside modelPattern.
	unknownModel = "unknown-model"
)

// The tag convention's value patterns. A value that misses one is omitted,
// and a tag with no value is never written.
var (
	sessionTagPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	versionTagPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	// modelPattern is the allowlist and the 128-character cap the comment
	// holds every string from the pull request to. The agent writes the model
	// name, so a row states it only when it passes.
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+_-]{0,127}$`)
	// rowIDPattern is the shape of an x_CostroidRowId.
	rowIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// containModels returns the report with every model name outside
// modelPattern read as unknown-model, nested sessions included, and how many
// requests it renamed. The report it was given is left as it was.
func containModels(report adapter.Report) (adapter.Report, int) {
	renamed := 0
	report.Requests = slices.Clone(report.Requests)
	for i := range report.Requests {
		if !modelPattern.MatchString(report.Requests[i].Model) {
			report.Requests[i].Model = unknownModel
			renamed++
		}
	}
	report.Nested = slices.Clone(report.Nested)
	for i := range report.Nested {
		var count int
		report.Nested[i], count = containModels(report.Nested[i])
		renamed += count
	}
	return report, renamed
}

func unknownModelLine(renamed int) string {
	if renamed == 1 {
		return "1 request named a model outside the allowed characters, so its rows read " + unknownModel
	}
	return fmt.Sprintf("%d requests named a model outside the allowed characters, so their rows read %s", renamed, unknownModel)
}

// rowsDigest is the SHA-256 of the rows' x_CostroidRowId values, sorted, each
// followed by a line feed. It is empty for no rows, and for a row whose id is
// not 64 lower-case hex digits.
func rowsDigest(rows []focus.Row) string {
	if len(rows) == 0 {
		return ""
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if !rowIDPattern.MatchString(row.RowID) {
			return ""
		}
		ids = append(ids, row.RowID)
	}
	slices.Sort(ids)
	digest := sha256.New()
	for _, id := range ids {
		digest.Write([]byte(id + "\n"))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

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
		charge.Notes = priceNotes(price, request, count.Class)
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

func priceNotes(price catalog.Price, request adapter.Request, class focus.TokenClass) []string {
	var notes []string
	if price.Fallback {
		notes = append(notes, focus.NoteFallback)
	}
	if price.CacheMultiplier != nil {
		notes = append(notes, price.CacheMultiplier.CacheNote(class))
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
// matched tier's top, every request is in that tier. Past a tier's floor the
// sum says nothing about any one request. A per-run entry states an input
// past a floor only when the agent's own figure puts its tokens in that tier,
// and states none when the figure puts them in the lowest tier or in neither.
// Its own note says which, so this one says nothing. Any other entry past a
// floor is priced at the tier above as an upper bound, and one with input and
// no stated whole input has an unknown tier.
func tierNote(price catalog.Price, request adapter.Request) string {
	tokens := request.Tokens
	switch {
	case request.Grain == adapter.PerRequest:
		return ""
	case request.Grain == adapter.PerRun && (request.ContextInputTokens == 0 || price.ContextTierMin != nil):
		return ""
	case request.ContextInputTokens == 0 && tokens.InputUncached+tokens.CacheRead+tokens.CacheWrite5m+tokens.CacheWrite1h+tokens.CacheWrite30m > 0:
		return fmt.Sprintf("one %s's total, and its source states no whole input, so its context tier is unknown and the lowest tier prices it, which may read under", request.Grain)
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
