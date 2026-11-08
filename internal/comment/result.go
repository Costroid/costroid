package comment

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/run"
)

// Block is the result object, the machine-readable block. Strings under Data
// originate in the pull request.
type Block struct {
	Schema      string     `json:"schema"`
	CheckRunID  *int64     `json:"check_run_id,omitempty"`
	Conclusion  *string    `json:"conclusion"`
	State       State      `json:"state"`
	Reason      Reason     `json:"reason,omitempty"`
	ReasonLine  int        `json:"reason_line,omitempty"`
	RunReason   run.Reason `json:"run_reason,omitempty"`
	ReaderError string     `json:"reader_error,omitempty"`
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
	// Policy is the verdict, without its amounts when amounts are withheld.
	Policy *policy.Verdict `json:"policy,omitempty"`
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

// Check renders the check output. Text carries the result object, with every
// amount in it whatever the comment shows.
func Check(input Input) (CheckOutput, error) {
	block, err := Result(input)
	if err != nil {
		return CheckOutput{}, err
	}
	summary, err := render("facts", input.view())
	if err != nil {
		return CheckOutput{}, err
	}
	return CheckOutput{Title: "Verdict: " + input.Conclusion(), Summary: summary, Text: fenced(block)}, nil
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

func (i Input) block() (string, error) {
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
		Amounts:        "withheld",
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
		block.Amounts = "shown"
		block.Cap = i.capCheck().block()
		block.Lifetime = i.Lifetime.block()
		if stated := i.agentStated(); stated != nil {
			block.AgentStated = &BlockAgentStated{Unit: stated.Unit, Quantity: stated.Quantity, Source: stated.Source}
		}
	}
	block.Policy = blockVerdict(i.Verdict, shown)
	encoded, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return "", fmt.Errorf("comment: encode block: %w", err)
	}
	return string(encoded), nil
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

// The versions before the one this release writes. A pull request whose
// newest check a release that wrote one of them posted can still be approved,
// so ParseResult reads them. 0.7 holds every field of the current version but
// recorded, 0.6 holds no agent_stated either, and 0.5 holds none of
// rows_sha256, rows_file_sha256 and lifetime as well.
const (
	schemaV05 = "costroid.result/0.5"
	schemaV06 = "costroid.result/0.6"
	schemaV07 = "costroid.result/0.7"
	schemaV08 = "costroid.result/0.8"
)

// readSchemas are the versions ParseResult reads.
var readSchemas = []string{schemaV05, schemaV06, schemaV07, schema}

// blockV07 is a Block as costroid.result/0.7 holds it. Its field hides the
// Block's field of the same name, which 0.7 does not hold, and stays nil, so
// the object leaves it out.
type blockV07 struct {
	Block
	Recorded *struct{} `json:"recorded,omitempty"`
}

// blockV06 is a Block as costroid.result/0.6 holds it, as blockV07 is.
type blockV06 struct {
	Block
	AgentStated *struct{} `json:"agent_stated,omitempty"`
	Recorded    *struct{} `json:"recorded,omitempty"`
}

// blockV05 is a Block as costroid.result/0.5 holds it, as blockV07 is.
type blockV05 struct {
	Block
	RowsSHA256     *struct{} `json:"rows_sha256,omitempty"`
	RowsFileSHA256 *struct{} `json:"rows_file_sha256,omitempty"`
	Lifetime       *struct{} `json:"lifetime,omitempty"`
	AgentStated    *struct{} `json:"agent_stated,omitempty"`
	Recorded       *struct{} `json:"recorded,omitempty"`
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
	}
	encoded, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return "", fmt.Errorf("comment: encode block: %w", err)
	}
	return string(encoded), nil
}

// statedUnits and statedSources are the closed lists of result/v0.7: the
// units an agent's own figure is stated in and the agents that state one.
var (
	statedUnits   = []string{adapter.UnitAICredits}
	statedSources = []string{"gh-aw"}
	// statedQuantity is result/v0.7's statedQuantity: every digit the agent
	// wrote, as a decimal string.
	statedQuantity = regexp.MustCompile(`^[0-9]{1,26}(\.[0-9]{1,32})?$`)
)

// KnownStated reports a figure whose unit, source and quantity result/v0.7
// holds. Any other is dropped, so the object never states it.
func KnownStated(stated adapter.Stated) bool {
	return slices.Contains(statedUnits, stated.Unit) && slices.Contains(statedSources, stated.Source) && statedQuantity.MatchString(stated.Quantity)
}

// agentStated is the agent's own figure when result/v0.7 holds it, nil
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
	return fmt.Sprintf("%s stated %s AI credits for this run. Costroid does not convert credits.", stated.Source, stated.Quantity)
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
		return Block{}, fmt.Errorf("comment: the result object's schema is %q, and this release reads %s", string(Clean(block.Schema)), strings.Join(readSchemas[:len(readSchemas)-1], ", ")+" and "+readSchemas[len(readSchemas)-1])
	}
	if added, ok := newerValue(object, block); ok {
		return Block{}, fmt.Errorf("comment: the result object holds %s, which only %s defines, so it is not one %s holds, and this release writes %s", added.name, added.version, block.Schema, schema)
	}
	if again, err := block.encode(); err == nil && again == object {
		return block, nil
	}
	if block.Schema != schema {
		return Block{}, fmt.Errorf("comment: the result object is not one %s holds, and this release writes %s", block.Schema, schema)
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
	// fieldsAdded are the top-level fields a later version added. Each is
	// present in every object of that version, null or not.
	fieldsAdded = []addedValue{
		{"rows_sha256", schemaV06}, {"rows_file_sha256", schemaV06}, {"lifetime", schemaV06}, {"agent_stated", schemaV07},
		{"recorded", schemaV08},
	}
)

// newerValue is the first value the object holds that only a version after
// its own defines. An object holding one was not written by a release of its
// version, so ParseResult names the value it refuses for.
func newerValue(object string, block Block) (addedValue, bool) {
	own := slices.Index(readSchemas, block.Schema)
	for _, added := range addedValues(object, block) {
		if slices.Index(readSchemas, added.version) > own {
			return added, true
		}
	}
	return addedValue{}, false
}

// addedValues lists the values the object holds that a version after 0.5
// added: a top-level field, the run reason artifact_refused, and a rule, a
// reason or a field of policy.results.
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
	if block.Policy == nil {
		return found
	}
	for _, result := range block.Policy.Results {
		if slices.Contains(rulesAddedIn06, result.Rule) {
			found = append(found, addedValue{"the rule " + result.Rule, schemaV06})
		}
		if slices.Contains(reasonsAddedIn06, result.Reason) {
			found = append(found, addedValue{"the reason " + result.Reason, schemaV06})
		}
		if slices.Contains(reasonsAddedIn07, result.Reason) {
			found = append(found, addedValue{"the reason " + result.Reason, schemaV07})
		}
		for _, field := range []struct {
			name string
			set  bool
		}{{"limit_percent", result.LimitPercent != ""}, {"baseline_usd", result.BaselineUSD != ""}, {"models", len(result.Models) > 0}} {
			if field.set {
				found = append(found, addedValue{"the field policy.results." + field.name, schemaV06})
			}
		}
	}
	return found
}

// Reconclude renders a check that repeats an earlier check run's result
// object with the conclusion an approval gives it: neutral for an approved
// failure, the policy's verdict when no approval counts. previous names the
// check run that carries the report in full.
func Reconclude(block Block, approval *Approval, previous int64) (CheckOutput, error) {
	verdict := block.verdict()
	conclusion := block.ApprovedConclusion(approval)
	block.Conclusion = &conclusion
	encoded, err := block.encode()
	if err != nil {
		return CheckOutput{}, err
	}
	line := fmt.Sprintf("No approval binds commit `%s`, so the check concludes as the policy does.", block.HeadSHA[:min(len(block.HeadSHA), shortCommitLength)])
	if conclusion != verdict {
		line = approvalLine(*approval)
	}
	summary := fmt.Sprintf("**Verdict: %s.** %s\n\nCheck run %d carries this report in full.\n", conclusion, line, previous)
	return CheckOutput{Title: "Verdict: " + conclusion, Summary: summary, Text: fenced(encoded)}, nil
}
