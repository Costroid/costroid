package comment

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/copilot"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/adapter/opencode"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
)

// Block is the result object, the machine-readable block. Strings under Data
// originate in the pull request.
type Block struct {
	Schema      string        `json:"schema"`
	CheckRunID  *int64        `json:"check_run_id,omitempty"`
	Conclusion  *string       `json:"conclusion"`
	State       State         `json:"state"`
	Reason      Reason        `json:"reason,omitempty"`
	ReasonLine  int           `json:"reason_line,omitempty"`
	RunReason   reason.Reason `json:"run_reason,omitempty"`
	ReaderError string        `json:"reader_error,omitempty"`
	// AgentError is the class of error the agent ended with, null when the
	// run stated none.
	AgentError *BlockAgentError `json:"agent_error"`
	// AgentSkipped says whether the CI system skipped the agent's job, null
	// when the report was not told.
	AgentSkipped *bool  `json:"agent_skipped"`
	HeadSHA      string `json:"head_sha"`
	// CIRun is the run the environment names, a claim, null when it names
	// none.
	CIRun *BlockCIRun `json:"ci_run"`
	// RowsSHA256 and RowsFileSHA256 name the rows and the rows file the
	// figures were built from, null when not stated. RowsSHA256 is null
	// without figures.
	RowsSHA256     *string       `json:"rows_sha256"`
	RowsFileSHA256 *string       `json:"rows_file_sha256"`
	Amounts        string        `json:"amounts"`
	Rows           int           `json:"rows"`
	RunIdentity    string        `json:"run_identity"`
	Figures        []BlockFigure `json:"figures"`
	FiguresOmitted int           `json:"figures_omitted"`
	Cap            *BlockCap     `json:"cap"`
	Data           BlockData     `json:"data"`
	// Lifetime is the pull request's total as the comment states it, null
	// off a pull request and when amounts are withheld.
	Lifetime *BlockLifetime `json:"lifetime"`
	// AgentStated is the agent's own figure for the run, in its own unit,
	// null when it states none and when amounts are withheld.
	AgentStated *BlockAgentStated `json:"agent_stated"`
	// Recorded names the recorded run whose rows the figures are, in place of
	// the pull request's own, null on a report on the pull request's rows.
	// A withheld copy states it too: it is no amount.
	Recorded *BlockRecorded `json:"recorded"`
	// Estimates are the figures of the rows that are not an agent's, and an
	// estimate the workflow named that was not available. They stand apart
	// from Figures and are never added to them. EstimatesOmitted counts the
	// figures left out past the first maxFigures. A result/0.9 object holds
	// neither.
	Estimates        []BlockEstimate `json:"estimates"`
	EstimatesOmitted int             `json:"estimates_omitted"`
	// Policy is the verdict, without its amounts when amounts are withheld.
	Policy *policy.Verdict `json:"policy,omitempty"`
	// Receipts is the receipt the pull request's author posted, read or
	// refused, apart from every field above. Only a result/0.13 object holds
	// it, and nothing above reads it.
	Receipts []BlockReceipt `json:"receipts,omitempty"`
}

// BlockEstimate is one entry of a result/0.10 object's estimates. A figure
// names its source, currency, charge period, basis and rows, and its amount
// when amounts are shown. An estimate that was not available names its
// source and Reason alone.
type BlockEstimate struct {
	Source            Data   `json:"source"`
	Currency          string `json:"currency,omitempty"`
	ChargePeriodStart string `json:"charge_period_start,omitempty"`
	ChargePeriodEnd   string `json:"charge_period_end,omitempty"`
	CostBasis         string `json:"cost_basis,omitempty"`
	ListCost          string `json:"list_cost,omitempty"`
	Rows              int    `json:"rows,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// BlockLifetime is the pull request's total, one figure per currency and
// basis, as the comment's lines state it.
type BlockLifetime struct {
	Figures        []BlockLifetimeFigure `json:"figures"`
	FiguresOmitted int                   `json:"figures_omitted"`
	Capped         bool                  `json:"capped"`
	Unread         int                   `json:"unread"`
}

// BlockLifetimeFigure is one line of the pull request's total. MayRepeat
// says the line reads "which may include a repeated report".
type BlockLifetimeFigure struct {
	Currency  string `json:"currency"`
	CostBasis string `json:"cost_basis"`
	ListCost  string `json:"list_cost"`
	Reports   int    `json:"reports"`
	MayRepeat bool   `json:"may_repeat"`
}

// BlockAgentStated is a figure the agent stated about its own run, in its own
// unit. Costroid never converts it and never adds it to an amount.
type BlockAgentStated struct {
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
	Source   string `json:"source"`
}

// BlockRecorded is the recorded run a report's figures are: the CI run that
// wrote its rows and the day it was recorded.
type BlockRecorded struct {
	RunID int64  `json:"run_id"`
	Date  string `json:"date"`
}

// BlockAgentError names an agent's error by its class from a closed list,
// never by the agent's own text.
type BlockAgentError struct {
	Class string `json:"class"`
}

// BlockCIRun is GitHub Actions' run id and attempt as the environment states
// them.
type BlockCIRun struct {
	ID      int64 `json:"id"`
	Attempt int   `json:"attempt"`
}

// BlockFigure is one Figure. ListCost and Tokens are empty when amounts are
// withheld.
type BlockFigure struct {
	Currency         string            `json:"currency"`
	ListCost         string            `json:"list_cost,omitempty"`
	CostBasis        string            `json:"cost_basis"`
	AttributionGrade string            `json:"attribution_grade"`
	EnforcementGrade string            `json:"enforcement_grade"`
	Rows             int               `json:"rows"`
	FallbackRows     int               `json:"fallback_rows"`
	Tokens           map[string]string `json:"tokens,omitempty"`
}

// BlockCap is the run's cap beside the figure it holds. The fields after
// EnforcementGrade are null when no single figure compares with it.
type BlockCap struct {
	Currency         string  `json:"currency"`
	Amount           string  `json:"amount"`
	EnforcementGrade *string `json:"enforcement_grade"`
	Cost             *string `json:"cost"`
	CostBasis        *string `json:"cost_basis"`
	Remainder        *string `json:"remainder"`
	Overshoot        *string `json:"overshoot"`
	OvershootTokens  *string `json:"overshoot_tokens"`
}

// BlockData holds the strings that originated in the pull request, labelled as
// data. Each one passed the allowlist.
type BlockData struct {
	Agents        []Data `json:"agents"`
	Models        []Data `json:"models"`
	ModelsOmitted int    `json:"models_omitted"`
	Catalogs      []Data `json:"catalog_versions"`
	// FallbackModels and FallbackPrices name the rows priced at the catalog's
	// fallback tier.
	FallbackModels        []Data `json:"fallback_models"`
	FallbackModelsOmitted int    `json:"fallback_models_omitted"`
	FallbackPrices        []Data `json:"fallback_price_ids"`
	FallbackPricesOmitted int    `json:"fallback_price_ids_omitted"`
}

// CheckOutput is the output of the check run.
type CheckOutput struct {
	Title   string
	Summary string
	Text    string
}

// Check renders the check output. The title is the verdict sentence, and the
// summary opens on it. Text carries the result object, with every amount in
// it whatever the comment shows.
func Check(input Input) (CheckOutput, error) {
	block, err := Result(input)
	if err != nil {
		return CheckOutput{}, err
	}
	summary, err := render("facts", input.view())
	if err != nil {
		return CheckOutput{}, err
	}
	return CheckOutput{Title: VerdictSentence(input), Summary: summary, Text: fenced(block)}, nil
}

// Result renders the result object as the check run carries it: every amount
// in it, and the check run's id once the check names it.
func Result(input Input) (string, error) {
	authoritative := input
	authoritative.Amounts = AmountsShown
	if !input.IDInCheck {
		authoritative.CheckRunID = nil
	}
	return authoritative.block()
}

// block writes the object with its receipts. A receipt makes it result/0.13,
// unless it holds a demoted result, which only 0.14 holds.
func (i Input) block() (string, error) {
	block := i.withReceipts(i.fields(), i.Amounts == AmountsShown)
	if addedIn(addedValues("", block), schemaV014) {
		block.Schema = schemaV014
	}
	return block.encode()
}

// fields is the result object before it is written: what the machine block
// states, and what the verdict sentence is built from. It holds no receipt:
// block adds one after, so no sentence, conclusion or output reads it.
func (i Input) fields() Block {
	block := Block{
		Schema:         schema,
		CheckRunID:     i.CheckRunID,
		Conclusion:     i.conclusion(),
		State:          i.State,
		Reason:         i.Reason,
		ReasonLine:     i.ReasonLine,
		RunReason:      i.runReason(),
		ReaderError:    i.readerLine(),
		AgentError:     i.blockAgentError(),
		AgentSkipped:   i.agentSkipped(),
		HeadSHA:        i.headSHA(),
		Recorded:       i.blockRecorded(),
		CIRun:          i.ciRun(),
		RowsSHA256:     i.rowsDigest(),
		RowsFileSHA256: digestOrNil(i.RowsFileSHA256),
		Amounts:        amountsWithheld,
		Rows:           i.Ledger.Rows,
		RunIdentity:    artifactIdentity,
		Figures:        make([]BlockFigure, 0, len(i.Ledger.Figures)),
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Data: BlockData{
			Agents:                nonNil(i.Ledger.Agents),
			Models:                nonNil(i.Ledger.Models),
			ModelsOmitted:         i.Ledger.ModelsOmitted,
			Catalogs:              nonNil(i.Ledger.Catalogs),
			FallbackModels:        nonNil(i.Ledger.FallbackModels),
			FallbackModelsOmitted: i.Ledger.FallbackModelsOmitted,
			FallbackPrices:        nonNil(i.Ledger.FallbackPrices),
			FallbackPricesOmitted: i.Ledger.FallbackPricesOmitted,
		},
	}
	shown := i.Amounts == AmountsShown
	for _, figure := range i.Ledger.Figures {
		line := BlockFigure{
			Currency:         figure.Currency,
			CostBasis:        figure.Basis,
			AttributionGrade: figure.Attribution,
			EnforcementGrade: figure.Enforcement,
			Rows:             figure.Rows,
			FallbackRows:     figure.FallbackRows,
		}
		if shown {
			line.ListCost = figure.Cost.String()
			line.Tokens = tokenStrings(figure.Tokens)
		}
		block.Figures = append(block.Figures, line)
	}
	if shown {
		block.Amounts = amountsShown
		block.Cap = i.capCheck().block()
		block.Lifetime = i.Lifetime.block()
		if stated := i.agentStated(); stated != nil {
			block.AgentStated = &BlockAgentStated{Unit: stated.Unit, Quantity: stated.Quantity, Source: stated.Source}
		}
	}
	block.Policy = blockVerdict(i.Verdict, shown)
	block.Estimates = i.blockEstimates(shown)
	block.EstimatesOmitted = i.Ledger.Infrastructure.FiguresOmitted
	switch added := addedValues("", block); {
	case addedIn(added, schemaV014):
		block.Schema = schemaV014
	case addedIn(added, schemaV012):
		block.Schema = schemaV012
	case addedIn(added, schemaV011):
		block.Schema = schemaV011
	case len(block.Estimates) == 0:
		block.Schema = schemaV09
	}
	return block
}

// blockEstimates are the estimate figures the comment's infrastructure
// section states, in its order, then the estimate it says was not available.
// A figure at another basis is no estimate, and result/v0.10 holds none.
// Withheld, a figure carries no amount, as an agent's figure carries none.
func (i Input) blockEstimates(shown bool) []BlockEstimate {
	var estimates []BlockEstimate
	for _, figure := range i.Ledger.Infrastructure.Figures {
		if figure.Basis != focusfile.Basis {
			continue
		}
		estimate := BlockEstimate{
			Source:            figure.Source,
			Currency:          figure.Currency,
			ChargePeriodStart: figure.Period.Start.Format(focus.InstantLayout),
			ChargePeriodEnd:   figure.Period.End.Format(focus.InstantLayout),
			CostBasis:         figure.Basis,
			Rows:              figure.Rows,
		}
		if shown {
			estimate.ListCost = figure.Cost.String()
		}
		estimates = append(estimates, estimate)
	}
	if i.estimateUnavailable() != "" {
		estimates = append(estimates, BlockEstimate{Source: Clean(i.EstimateUnavailable.Source), Reason: i.EstimateUnavailable.Reason})
	}
	return estimates
}

func (i Input) blockRecorded() *BlockRecorded {
	recorded := i.recorded()
	if recorded == nil {
		return nil
	}
	return &BlockRecorded{RunID: recorded.RunID, Date: recorded.Date}
}

// verdict is the policy's conclusion, neutral when no policy ran.
func (b Block) verdict() string {
	if b.Policy == nil {
		return policy.Neutral
	}
	return b.Policy.Conclusion
}

// ApprovedConclusion is the conclusion the check that repeats this object
// gets with the approval, nil for none.
func (b Block) ApprovedConclusion(approval *Approval) string {
	return ApprovedConclusion(b.verdict(), approval, b.HeadSHA)
}

// The versions other than result/0.10. This release writes 0.14 only for an
// object that holds a demoted result, 0.13 for one that holds a receipt, 0.12
// for one that holds a customer pack, 0.11
// for one that holds a value only 0.11 defines, 0.10 for one that states an
// estimate, and 0.9 for any other, and a pull request whose newest check a
// release that wrote an older one posted can still be approved, so
// ParseResult reads them all. 0.14 holds the fields of 0.13 and a result's
// demoted, with receipts only beside a receipt. 0.13 holds the fields of 0.12
// and receipts,
// with estimates and estimates_omitted only beside an estimate. 0.12 holds
// the fields of 0.11, policy.pack, and a customer_pack result with its four
// reasons and its line. 0.11 holds the fields of 0.10, with estimates and
// estimates_omitted only beside an estimate, and a run_p95 result with its
// three fields. 0.9
// holds every field of 0.10 but estimates and estimates_omitted. 0.8 holds an
// agent_stated from gh-aw alone. 0.7 holds no recorded either, 0.6 holds no
// agent_stated, and 0.5 holds none of rows_sha256, rows_file_sha256 and
// lifetime as well.
const (
	schemaV05  = "costroid.result/0.5"
	schemaV06  = "costroid.result/0.6"
	schemaV07  = "costroid.result/0.7"
	schemaV08  = "costroid.result/0.8"
	schemaV09  = "costroid.result/0.9"
	schemaV011 = "costroid.result/0.11"
	schemaV012 = "costroid.result/0.12"
	schemaV014 = "costroid.result/0.14"
)

var (
	// readSchemas are the versions ParseResult reads, oldest first.
	readSchemas = []string{schemaV05, schemaV06, schemaV07, schemaV08, schemaV09, schema, schemaV011, schemaV012, schemaV013, schemaV014}
	// writtenSchemas are the versions this release writes.
	writtenSchemas = []string{schemaV09, schema, schemaV011, schemaV012, schemaV013, schemaV014}
)

// WrittenSchemas are the versions of the result object this release writes,
// oldest first. A reader of the check runs this release posts reads each.
func WrittenSchemas() []string {
	return slices.Clone(writtenSchemas)
}

// versions names a list of versions as a sentence does.
func versions(list []string) string {
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}

// blockV09 is a Block as costroid.result/0.9 and 0.8 hold it. Its fields hide
// the Block's fields of the same name, which 0.9 does not hold, and stay nil,
// so the object leaves them out.
type blockV09 struct {
	Block
	Estimates        *struct{} `json:"estimates,omitempty"`
	EstimatesOmitted *struct{} `json:"estimates_omitted,omitempty"`
}

// blockV07 is a Block as costroid.result/0.7 holds it, as blockV09 is.
type blockV07 struct {
	Block
	Recorded         *struct{} `json:"recorded,omitempty"`
	Estimates        *struct{} `json:"estimates,omitempty"`
	EstimatesOmitted *struct{} `json:"estimates_omitted,omitempty"`
}

// blockV06 is a Block as costroid.result/0.6 holds it, as blockV09 is.
type blockV06 struct {
	Block
	AgentStated      *struct{} `json:"agent_stated,omitempty"`
	Recorded         *struct{} `json:"recorded,omitempty"`
	Estimates        *struct{} `json:"estimates,omitempty"`
	EstimatesOmitted *struct{} `json:"estimates_omitted,omitempty"`
}

// blockV05 is a Block as costroid.result/0.5 holds it, as blockV09 is.
type blockV05 struct {
	Block
	RowsSHA256       *struct{} `json:"rows_sha256,omitempty"`
	RowsFileSHA256   *struct{} `json:"rows_file_sha256,omitempty"`
	Lifetime         *struct{} `json:"lifetime,omitempty"`
	AgentStated      *struct{} `json:"agent_stated,omitempty"`
	Recorded         *struct{} `json:"recorded,omitempty"`
	Estimates        *struct{} `json:"estimates,omitempty"`
	EstimatesOmitted *struct{} `json:"estimates_omitted,omitempty"`
}

// encode writes the object as its own version lays it out.
func (b Block) encode() (string, error) {
	var object any = b
	switch b.Schema {
	case schemaV05:
		object = blockV05{Block: b}
	case schemaV06:
		object = blockV06{Block: b}
	case schemaV07:
		object = blockV07{Block: b}
	case schemaV08, schemaV09:
		object = blockV09{Block: b}
	case schemaV011, schemaV012, schemaV013, schemaV014:
		if len(b.Estimates) == 0 {
			object = blockV09{Block: b}
		}
	}
	encoded, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return "", fmt.Errorf("comment: encode block: %w", err)
	}
	return string(encoded), nil
}

// statedFigure is one entry of result/v0.9's closed list of the figures an
// agent states about its own run: the agent, the one unit it states them in,
// and the sentence the check and the comment state them in, apart from every
// amount. The sentence names the agent and its unit, and never calls the
// figure the run's cost.
type statedFigure struct {
	source, unit, sentence string
}

var (
	statedFigures = []statedFigure{
		{ghaw.Name, adapter.UnitAICredits, "gh-aw stated %s AI credits for this run. Costroid does not convert credits."},
		{opencode.Name, opencode.StatedUnit, "OpenCode stated USD %s for this run by its own prices, which Costroid never adds to its own figures."},
		{copilot.Name, copilot.UnitNanoAIU, "Copilot CLI stated %s nano-AI units for this run, which Costroid never converts to dollars."},
	}
	// statedAddedIn09 are the agents whose figures result/v0.9 added.
	statedAddedIn09 = []string{opencode.Name, copilot.Name}
	// statedQuantity is result/v0.9's statedQuantity: every digit the agent
	// wrote, as a decimal string.
	statedQuantity = regexp.MustCompile(`^[0-9]{1,26}(\.[0-9]{1,32})?$`)
)

// statedEntry is the entry of statedFigures that holds the figure's agent and
// unit together.
func statedEntry(stated adapter.Stated) (statedFigure, bool) {
	index := slices.IndexFunc(statedFigures, func(known statedFigure) bool {
		return known.source == stated.Source && known.unit == stated.Unit
	})
	if index < 0 {
		return statedFigure{}, false
	}
	return statedFigures[index], true
}

// KnownStated reports a figure whose agent, unit and quantity result/v0.9
// holds together. Any other is dropped, so the object never states it.
func KnownStated(stated adapter.Stated) bool {
	_, known := statedEntry(stated)
	return known && statedQuantity.MatchString(stated.Quantity)
}

// agentStated is the agent's own figure when result/v0.9 holds it, nil
// otherwise.
func (i Input) agentStated() *adapter.Stated {
	if i.AgentStated == nil || !KnownStated(*i.AgentStated) {
		return nil
	}
	return i.AgentStated
}

// agentStatedLine states the agent's own figure on a line of its own, apart
// from every amount, and nothing when amounts are withheld.
func (i Input) agentStatedLine(shown bool) string {
	stated := i.agentStated()
	if stated == nil || !shown {
		return ""
	}
	entry, _ := statedEntry(*stated)
	return fmt.Sprintf(entry.sentence, stated.Quantity)
}

// ParseResult reads the fenced result object a check run's text carries. It
// takes an object of a version in readSchemas, and only one a release that
// writes that version would write byte for byte, so a check that repeats it
// changes nothing it does not mean to.
func ParseResult(text string) (Block, error) {
	object, fencedOpen := strings.CutPrefix(text, "```json\n")
	object, fencedClose := strings.CutSuffix(object, "\n```")
	if !fencedOpen || !fencedClose {
		return Block{}, errors.New("comment: the check run's text is not a fenced result object")
	}
	if strings.Contains(object, "`") {
		return Block{}, errors.New("comment: the result object holds a backtick")
	}
	decoder := json.NewDecoder(strings.NewReader(object))
	decoder.DisallowUnknownFields()
	var block Block
	if err := decoder.Decode(&block); err != nil {
		return Block{}, fmt.Errorf("comment: read the result object: %w", err)
	}
	if !slices.Contains(readSchemas, block.Schema) {
		return Block{}, fmt.Errorf("comment: the result object's schema is %q, and this release reads %s", string(Clean(block.Schema)), versions(readSchemas))
	}
	written := versions(writtenSchemas)
	added := addedValues(object, block)
	if newer, ok := newerValue(added, block); ok {
		return Block{}, fmt.Errorf("comment: the result object holds %s, which only %s defines, so it is not one %s holds, and this release writes %s", newer.name, newer.version, block.Schema, written)
	}
	if block.Schema == schema && len(block.Estimates) == 0 {
		return Block{}, fmt.Errorf("comment: the result object states no estimate, and a release writes %s only for an object that states one", schema)
	}
	for _, version := range []string{schemaV011, schemaV012, schemaV013, schemaV014} {
		if block.Schema == version && !addedIn(added, version) {
			return Block{}, fmt.Errorf("comment: the result object holds nothing only %s defines, and a release writes %s only for an object that does", version, version)
		}
	}
	if again, err := block.encode(); err == nil && again == object {
		return block, nil
	}
	if !slices.Contains(writtenSchemas, block.Schema) {
		return Block{}, fmt.Errorf("comment: the result object is not one %s holds, and this release writes %s", block.Schema, written)
	}
	return Block{}, errors.New("comment: the result object is not one this release writes")
}

// addedValue is a value a version after 0.5 defines, and the first version
// that defines it.
type addedValue struct {
	name, version string
}

// The rules and reasons of policy.results that result/v0.6 added.
var (
	rulesAddedIn06 = []string{
		policy.RuleRunIncrease, policy.RuleRunIncreasePercent, policy.RulePullRequestBudget,
		policy.RuleMonthlyBudget, policy.RuleModelAllowlist,
	}
	reasonsAddedIn06 = []string{
		policy.ReasonOverBaseline, policy.ReasonNoBaseline, policy.ReasonNoLifetime, policy.ReasonRepeatPossible,
		policy.ReasonNoLedger, policy.ReasonOutsideAllowlist, policy.ReasonUnreadableModel, policy.ReasonOnAllowlist,
	}
	// reasonsAddedIn07 are the reasons of policy.results that result/v0.7
	// added.
	reasonsAddedIn07 = []string{policy.ReasonUnmeasuredSpend, policy.ReasonNoRunRows}
	// rulesAddedIn011 are the rules of policy.results that result/v0.11
	// added.
	rulesAddedIn011 = []string{policy.RuleRunP95}
	// reasonsAddedIn012 are the reasons of policy.results that result/v0.12
	// added, each of the rule customer_pack, which it added too.
	reasonsAddedIn012 = []string{policy.ReasonPackUnread, policy.ReasonPackInvalid, policy.ReasonPackBound, policy.ReasonPackResult}
	// fieldsAdded are the top-level fields a later version added. Each is
	// present in every object of that version, null or not.
	fieldsAdded = []addedValue{
		{"rows_sha256", schemaV06}, {"rows_file_sha256", schemaV06}, {"lifetime", schemaV06}, {"agent_stated", schemaV07},
		{"recorded", schemaV08}, {"estimates", schema}, {"estimates_omitted", schema}, {"receipts", schemaV013},
	}
)

// newerValue is the first of the values an object holds that only a version
// after its own defines. An object holding one was not written by a release
// of its version, so ParseResult names the value it refuses for.
func newerValue(values []addedValue, block Block) (addedValue, bool) {
	own := slices.Index(readSchemas, block.Schema)
	for _, added := range values {
		if slices.Index(readSchemas, added.version) > own {
			return added, true
		}
	}
	return addedValue{}, false
}

// addedIn reports values that hold one only version defines first.
func addedIn(values []addedValue, version string) bool {
	return slices.ContainsFunc(values, func(value addedValue) bool { return value.version == version })
}

// addedValues lists the values the object holds that a version after 0.5
// added: a top-level field, the run reason artifact_refused, an agent_stated
// source, policy.pack, and a rule, a reason or a field of policy.results. The
// fields at the top are read from the object's text, and none from an empty
// one.
func addedValues(object string, block Block) []addedValue {
	var found []addedValue
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(object), &fields) == nil {
		for _, added := range fieldsAdded {
			if _, ok := fields[added.name]; ok {
				found = append(found, addedValue{"the field " + added.name, added.version})
			}
		}
	}
	if block.RunReason == ArtifactRefused {
		found = append(found, addedValue{"the run reason " + string(ArtifactRefused), schemaV06})
	}
	if block.AgentStated != nil && slices.Contains(statedAddedIn09, block.AgentStated.Source) {
		found = append(found, addedValue{"the agent_stated source " + string(Clean(block.AgentStated.Source)), schemaV09})
	}
	if block.Policy == nil {
		return found
	}
	if block.Policy.Pack != nil {
		found = append(found, addedValue{"the field policy.pack", schemaV012})
	}
	for _, result := range block.Policy.Results {
		if slices.Contains(rulesAddedIn06, result.Rule) {
			found = append(found, addedValue{"the rule " + result.Rule, schemaV06})
		}
		if slices.Contains(rulesAddedIn011, result.Rule) {
			found = append(found, addedValue{"the rule " + result.Rule, schemaV011})
		}
		if result.Rule == policy.RuleCustomerPack {
			found = append(found, addedValue{"the rule " + result.Rule, schemaV012})
		}
		if slices.Contains(reasonsAddedIn012, result.Reason) {
			found = append(found, addedValue{"the reason " + result.Reason, schemaV012})
		}
		if slices.Contains(reasonsAddedIn06, result.Reason) {
			found = append(found, addedValue{"the reason " + result.Reason, schemaV06})
		}
		if slices.Contains(reasonsAddedIn07, result.Reason) {
			found = append(found, addedValue{"the reason " + result.Reason, schemaV07})
		}
		for _, field := range []struct {
			name, version string
			set           bool
		}{
			{"limit_percent", schemaV06, result.LimitPercent != ""}, {"baseline_usd", schemaV06, result.BaselineUSD != ""},
			{"models", schemaV06, len(result.Models) > 0}, {"p95_usd", schemaV011, result.P95USD != ""},
			{"limit_factor", schemaV011, result.LimitFactor != ""}, {"baseline_runs", schemaV011, result.BaselineRuns != nil},
			{"line", schemaV012, result.Line != 0}, {"demoted", schemaV014, result.Demoted != nil},
		} {
			if field.set {
				found = append(found, addedValue{"the field policy.results." + field.name, field.version})
			}
		}
	}
	return found
}

// Reconclude renders a check that repeats an earlier check run's result
// object with the conclusion an approval gives it: neutral for an approved
// failure, the policy's verdict when no approval counts. previous names the
// check run that carries the report in full. The object does not say whether
// the report's comment shows amounts, so amounts says it, as the report
// decides it. Unless it reads AmountsShown, the title and the summary name
// none.
func Reconclude(block Block, approval *Approval, amounts Amounts, previous int64) (CheckOutput, error) {
	verdict := block.verdict()
	conclusion := block.ApprovedConclusion(approval)
	block.Conclusion = &conclusion
	encoded, err := block.encode()
	// The sentence is told from the object without its receipts, as a
	// report's own is.
	block.Receipts = nil
	if err != nil {
		return CheckOutput{}, err
	}
	line := fmt.Sprintf("No approval binds commit `%s`, so the check concludes as the policy does.", block.HeadSHA[:min(len(block.HeadSHA), shortCommitLength)])
	if conclusion == verdict {
		approval = nil
	} else {
		line = approvalLine(*approval)
	}
	if amounts != AmountsShown {
		block = block.withoutAmounts()
	}
	// Only GitHub's check takes an approval.
	sentence := telling{
		Block:           block,
		approval:        approval,
		besideEstimates: slices.ContainsFunc(block.Estimates, func(estimate BlockEstimate) bool { return estimate.Reason == "" }),
		request:         RequestNoun(AccountGitHub),
	}.sentence()
	summary := fmt.Sprintf("**%s**\n\n%s\n\nCheck run %d carries this report in full.\n", sentence, line, previous)
	return CheckOutput{Title: sentence, Summary: summary, Text: fenced(encoded)}, nil
}

// withoutAmounts is the object as a comment that withholds amounts copies
// it: no figure's cost, no cap, no total and no amount in the verdict.
func (b Block) withoutAmounts() Block {
	b.Amounts = amountsWithheld
	b.Figures = slices.Clone(b.Figures)
	for index := range b.Figures {
		b.Figures[index].ListCost, b.Figures[index].Tokens = "", nil
	}
	b.Cap, b.Lifetime, b.AgentStated = nil, nil, nil
	b.Policy = blockVerdict(b.Policy, false)
	b.Receipts = withoutReceiptAmounts(b.Receipts)
	return b
}
