package comment

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	}
	block.Policy = blockVerdict(i.Verdict, shown)
	encoded, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return "", fmt.Errorf("comment: encode block: %w", err)
	}
	return string(encoded), nil
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

// ParseResult reads the fenced result object a check run's text carries. It
// takes only an object this release writes, and only one it would write byte
// for byte, so a check that repeats it changes nothing it does not mean to.
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
	if block.Schema != schema {
		return Block{}, fmt.Errorf("comment: the result object's schema is %q, and this release writes %s", string(Clean(block.Schema)), schema)
	}
	again, err := json.MarshalIndent(block, "", "  ")
	if err != nil || string(again) != object {
		return Block{}, errors.New("comment: the result object is not one this release writes")
	}
	return block, nil
}

// Reconclude renders a check that repeats an earlier check run's result
// object with the conclusion an approval gives it: neutral for an approved
// failure, the policy's verdict when no approval counts. previous names the
// check run that carries the report in full.
func Reconclude(block Block, approval *Approval, previous int64) (CheckOutput, error) {
	verdict := block.verdict()
	conclusion := block.ApprovedConclusion(approval)
	block.Conclusion = &conclusion
	encoded, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return CheckOutput{}, fmt.Errorf("comment: encode block: %w", err)
	}
	line := fmt.Sprintf("No approval binds commit `%s`, so the check concludes as the policy does.", block.HeadSHA[:min(len(block.HeadSHA), shortCommitLength)])
	if conclusion != verdict {
		line = approvalLine(*approval)
	}
	summary := fmt.Sprintf("**Verdict: %s.** %s\n\nCheck run %d carries this report in full.\n", conclusion, line, previous)
	return CheckOutput{Title: "Verdict: " + conclusion, Summary: summary, Text: fenced(string(encoded))}, nil
}
