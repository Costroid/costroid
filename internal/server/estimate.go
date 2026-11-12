package server

import (
	"encoding/json"
	"regexp"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/store"
)

// estimateBasis is the cost basis of a row ingest-run --from focus writes: an
// infrastructure estimator's figure, read from the customer's own file.
const estimateBasis = focus.CostBasis(focusfile.Basis)

// estimateColumns are an estimate row's figures other than ListCost. The row
// path reads ListCost alone and counts each of these as a sender's figure.
var estimateColumns = columnSet("BilledCost", "ContractedCost", "ContractedUnitPrice", "EffectiveCost", "ListUnitPrice")

// estimateTexts are the text columns an estimate row keeps, each held to the
// row contract's limit.
var estimateTexts = []string{
	"ChargeDescription", "ServiceName", "ServiceCategory", "ServiceSubcategory", "ServiceProviderName",
	"HostProviderName", "InvoiceIssuerName", "BillingAccountName", "SubAccountName", "SkuId", "SkuMeter",
	"SkuPriceId", "RegionName", columnAccount, columnBilling, columnRegion,
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// estimate reports whether a row states the basis estimate. An agent's row
// states measured or list_equivalent, and the agent's row path refuses any
// other basis.
func (f rowFields) estimate() bool {
	raw, ok := f.columns[columnTags]
	if !ok {
		return false
	}
	var tags struct {
		Basis string `json:"costroid/cost_basis"`
	}
	return json.Unmarshal(raw, &tags) == nil && tags.Basis == focusfile.Basis
}

// readEstimate reads an estimate row. The ledger never prices it: its figure
// is the sender's ListCost, stored as an estimate, named by no agent and no
// model, and read by no budget or verdict. Its join is the CI token's, as for
// an agent's row.
func (f rowFields) readEstimate(drops map[string]int64) (charge, error) {
	c := charge{basis: estimateBasis}
	var err error
	if c.rowID, err = f.matching(columnRowID, rowIDPattern); err != nil {
		return charge{}, err
	}
	if err := f.readPeriod(&c); err != nil {
		return charge{}, err
	}
	texts := map[string]string{}
	for _, column := range estimateTexts {
		if texts[column], err = f.limited(column); err != nil {
			return charge{}, err
		}
	}
	currency, err := f.text("BillingCurrency")
	if err != nil {
		return charge{}, err
	}
	if !currencyPattern.MatchString(currency) {
		return charge{}, invalidRow(f.line, "BillingCurrency is not an ISO 4217 code")
	}
	listCost, err := f.text("ListCost")
	if err != nil {
		return charge{}, err
	}
	figure, err := focusfile.ReadFigure(listCost)
	if err != nil {
		return charge{}, invalidRow(f.line, "an estimate's ListCost is "+err.Error())
	}
	tags, err := f.tagMap(&c, drops)
	if err != nil {
		return charge{}, err
	}
	if tags[focus.TagAgent] != "" || tags[focus.TagModel] != "" {
		return charge{}, invalidRow(f.line, "an estimate names no agent and no model")
	}
	if err := f.joinTags(&c, tags); err != nil {
		return charge{}, err
	}
	c.estimate = &focusfile.Estimate{
		Figure: figure, Currency: currency, Start: c.start, End: c.end,
		Description:    texts["ChargeDescription"],
		BillingAccount: texts[columnBilling], BillingAccountName: texts["BillingAccountName"],
		SubAccount: texts[columnAccount], SubAccountName: texts["SubAccountName"],
		Provider: texts["ServiceProviderName"], Host: texts["HostProviderName"], InvoiceIssuer: texts["InvoiceIssuerName"],
		ServiceName: texts["ServiceName"], ServiceCategory: texts["ServiceCategory"], ServiceSubcategory: texts["ServiceSubcategory"],
		SkuID: texts["SkuId"], SkuMeter: texts["SkuMeter"], SkuPriceID: texts["SkuPriceId"],
		Region: texts[columnRegion], RegionName: texts["RegionName"],
		RowID: c.rowID,
	}
	if c.estimate.Description == "" {
		c.estimate.Description = "estimate from " + c.source
	}
	return c, nil
}

// estimateRecord stores an estimate row with the tags the ledger decides: the
// join, the source and the basis, and the grade none, because nothing held it.
func estimateRecord(c *charge, joined joinResult) store.Record {
	tags := tagsOf(c, joined)
	delete(tags, focus.TagAgent)
	delete(tags, focus.TagModel)
	tags[focus.TagEnforcementGrade] = enforcementNone
	estimate := *c.estimate
	estimate.Tags = tags
	return store.Record{
		Row: estimate.Row(), RunAttempt: joined.attempt, Actor: joined.actor, Event: joined.event,
		TokenCommitSHA: joined.tokenSHA, HeadBasis: joined.headBasis,
	}
}
