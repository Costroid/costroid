// Package comment turns FOCUS rows into the check output and the sticky pull
// request comment.
//
// Everything rendered is a structured field. A string from the pull request
// reaches a template only as Data.
package comment

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"text/template"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	// Marker opens the sticky comment, which is how the next run finds it.
	Marker = "<!-- costroid:comment -->"
	// CheckName is the one check run's name.
	CheckName = "costroid"
	// Conclusion is the check's conclusion until a policy decides one.
	Conclusion = "neutral"

	schema = "costroid.check/0.1"
	// artifactIdentity is what a figure read from a rows file can claim. The
	// agent's job wrote the file, tags included, so a row that reads
	// token_verified proves nothing here and the label does not depend on it.
	artifactIdentity = "unverified"
	budgetCurrency   = "USD"
	accountURL       = "https://app.costroid.com/"
	exportURL        = "https://docs.costroid.com/export"
)

// State says what the rows file gave the report.
type State string

// The three states. An unreadable file is never shown as a zero.
const (
	Reported   State = "reported"
	NoRows     State = "no_rows"
	Unreadable State = "rows_unreadable"
)

// Amounts says whether the comment carries amounts, and why not.
type Amounts int

// The zero value withholds, so an Input that never set the field cannot
// publish an amount.
const (
	// AmountsUnsettled withholds, because the setting or the visibility could
	// not be read.
	AmountsUnsettled Amounts = iota
	// AmountsOff withholds on a public repository with comment.show_cost false.
	AmountsOff
	// AmountsShown is the default.
	AmountsShown
)

// enforcementOrder runs from the weakest grade up, so the weakest reads first.
var enforcementOrder = []string{"none", "provider", "agent-flag", "wrapper-watchdog", "gateway-precheck"}

var enforcementNotes = map[string]string{
	"none":             "nothing held the run to a budget",
	"provider":         "only the vendor's budget applies",
	"agent-flag":       "client-side estimate, can pass the cap",
	"wrapper-watchdog": "the wrapper stops the process at the cap",
	"gateway-precheck": "checked before each request",
}

var basisLabels = map[string]string{
	"measured":        "Measured",
	"list_equivalent": "List-equivalent, not billed",
	"estimate":        "Estimate",
	"billed":          "Billed",
	"derived":         "Derived",
}

var installationPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// The hosts an account link can name.
const (
	AccountGitHub = "github"
	AccountGitLab = "gitlab"
)

// Input is everything a render reads.
type Input struct {
	Ledger  Ledger
	State   State
	HeadSHA string
	// Budget is the run's budget in USD, nil when the report was not told.
	// Rows record the enforcement grade and do not record the budget.
	Budget *decimal.Decimal
	// Installation is the SCM installation id, empty when no App is installed.
	// On GitLab it is the project id.
	Installation string
	// AccountHost is the host the installation id belongs to, GitHub when
	// empty. It is a name, never an instance's address.
	AccountHost string
	// CheckRunID names the posted check, nil when the post failed.
	CheckRunID *int64
	Amounts    Amounts
}

// Block is the machine-readable block. Strings under Data originate in the
// pull request.
type Block struct {
	Schema         string        `json:"schema"`
	CheckRunID     *int64        `json:"check_run_id,omitempty"`
	Conclusion     string        `json:"conclusion"`
	State          State         `json:"state"`
	HeadSHA        string        `json:"head_sha"`
	Amounts        string        `json:"amounts"`
	Rows           int           `json:"rows"`
	RunIdentity    string        `json:"run_identity"`
	Figures        []BlockFigure `json:"figures"`
	FiguresOmitted int           `json:"figures_omitted"`
	Budget         *BlockAmount  `json:"budget"`
	Data           BlockData     `json:"data"`
}

// BlockFigure is one Figure. ListCost is empty when amounts are withheld.
type BlockFigure struct {
	Currency         string `json:"currency"`
	ListCost         string `json:"list_cost,omitempty"`
	CostBasis        string `json:"cost_basis"`
	AttributionGrade string `json:"attribution_grade"`
	EnforcementGrade string `json:"enforcement_grade"`
	Rows             int    `json:"rows"`
}

// BlockAmount is an amount with its currency.
type BlockAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// BlockData holds the strings that originated in the pull request, labelled as
// data. Each one passed the allowlist.
type BlockData struct {
	Agents        []Data `json:"agents"`
	Models        []Data `json:"models"`
	ModelsOmitted int    `json:"models_omitted"`
	Catalogs      []Data `json:"catalog_versions"`
}

// CheckOutput is the output of the check run.
type CheckOutput struct {
	Title   string
	Summary string
	Text    string
}

// Check renders the check output. Text carries the authoritative block, with
// every amount in it whatever the comment shows.
func Check(input Input) (CheckOutput, error) {
	authoritative := input
	authoritative.Amounts = AmountsShown
	authoritative.CheckRunID = nil
	block, err := authoritative.block()
	if err != nil {
		return CheckOutput{}, err
	}
	summary, err := render("facts", input.view())
	if err != nil {
		return CheckOutput{}, err
	}
	return CheckOutput{Title: "Verdict: " + Conclusion, Summary: summary, Text: fenced(block)}, nil
}

// Comment renders the sticky comment. Its block is a copy of the check's that
// names the check run, without the amounts unless they are shown.
func Comment(input Input) (string, error) {
	block, err := input.block()
	if err != nil {
		return "", err
	}
	view := input.view()
	view.Block = fenced(block)
	return render("comment", view)
}

func (i Input) block() (string, error) {
	block := Block{
		Schema:         schema,
		CheckRunID:     i.CheckRunID,
		Conclusion:     Conclusion,
		State:          i.State,
		HeadSHA:        i.HeadSHA,
		Amounts:        "withheld",
		Rows:           i.Ledger.Rows,
		RunIdentity:    artifactIdentity,
		Figures:        make([]BlockFigure, 0, len(i.Ledger.Figures)),
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Data: BlockData{
			Agents:        nonNil(i.Ledger.Agents),
			Models:        nonNil(i.Ledger.Models),
			ModelsOmitted: i.Ledger.ModelsOmitted,
			Catalogs:      nonNil(i.Ledger.Catalogs),
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
		}
		if shown {
			line.ListCost = figure.Cost.String()
		}
		block.Figures = append(block.Figures, line)
	}
	if shown {
		block.Amounts = "shown"
		if i.Budget != nil {
			block.Budget = &BlockAmount{Currency: budgetCurrency, Amount: i.Budget.String()}
		}
	}
	encoded, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return "", fmt.Errorf("comment: encode block: %w", err)
	}
	return string(encoded), nil
}

func nonNil(names []Data) []Data {
	if names == nil {
		return []Data{}
	}
	return names
}

func fenced(block string) string {
	return "```json\n" + block + "\n```"
}

type figureLine struct {
	Cost        string
	Basis       string
	Attribution string
	Enforcement string
	Note        string
}

type view struct {
	Marker         string
	Conclusion     string
	State          State
	ShowAmounts    bool
	AmountsOff     bool
	Budget         string
	Lines          []figureLine
	FiguresOmitted int
	Agents         string
	Models         string
	ModelsOmitted  int
	Catalogs       string
	Rows           int
	AccountURL     string
	ExportURL      string
	CheckRunID     *int64
	Block          string
}

func (i Input) view() view {
	result := view{
		Marker:         Marker,
		Conclusion:     Conclusion,
		State:          i.State,
		ShowAmounts:    i.Amounts == AmountsShown,
		AmountsOff:     i.Amounts == AmountsOff,
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Agents:         spans(i.Ledger.Agents),
		Models:         spans(i.Ledger.Models),
		ModelsOmitted:  i.Ledger.ModelsOmitted,
		Catalogs:       spans(i.Ledger.Catalogs),
		Rows:           i.Ledger.Rows,
		AccountURL:     accountLink(i.AccountHost, i.Installation),
		ExportURL:      exportURL,
		CheckRunID:     i.CheckRunID,
	}
	if i.Budget != nil {
		result.Budget = budgetCurrency + " " + i.Budget.String()
	}
	for _, figure := range i.Ledger.Figures {
		result.Lines = append(result.Lines, figureLine{
			Cost:        figure.Currency + " " + figure.Cost.String(),
			Basis:       basisLabels[figure.Basis],
			Attribution: figure.Attribution,
			Enforcement: figure.Enforcement,
			Note:        enforcementNotes[figure.Enforcement],
		})
	}
	return result
}

func spans(names []Data) string {
	rendered := make([]string, len(names))
	for index, name := range names {
		rendered[index] = name.Code()
	}
	return strings.Join(rendered, ", ")
}

// accountLink carries the installation id when there is one. A malformed id is
// dropped, and the link still opens the account.
func accountLink(host, installation string) string {
	if !installationPattern.MatchString(installation) {
		return accountURL
	}
	return accountURL + "?" + url.Values{"installation": {cmp.Or(host, AccountGitHub) + ":" + installation}}.Encode()
}

//go:embed comment.tmpl
var templateSource string

var templates = template.Must(template.New("comment").Parse(templateSource))

func render(name string, data view) (string, error) {
	var out bytes.Buffer
	if err := templates.ExecuteTemplate(&out, name, data); err != nil {
		return "", fmt.Errorf("comment: render %s: %w", name, err)
	}
	return out.String(), nil
}
