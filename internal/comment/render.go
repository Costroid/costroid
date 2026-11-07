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
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/run"
)

const (
	// Marker opens the sticky comment, which is how the next run finds it.
	Marker = "<!-- costroid:comment -->"
	// CheckName is the one check run's name.
	CheckName = "costroid"

	// schema names the result object's contract, result/v0.6 in
	// Costroid/costroid-schemas.
	schema = "costroid.result/0.6"
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

// Reason says why a report shows no figures. The list is closed, so a comment
// never prints an error's text.
type Reason string

// The reasons. The zero value states only what the State says.
const (
	NoRowsFile     Reason = "no_rows_file"
	EmptyRowsFile  Reason = "empty_rows_file"
	NotAFile       Reason = "rows_path_not_a_file"
	UnreadRowsFile Reason = "rows_file_unread"
	InvalidRow     Reason = "invalid_row"
	// ForkNoRows is a fork's pull request whose workflow run left no rows
	// file, as the follow-up finds it. The recipes run no agent there.
	ForkNoRows Reason = "fork_no_rows"
	// AgentSkipped is a pull request whose agent job the CI system skipped,
	// so no agent ran and no rows were written.
	AgentSkipped Reason = "agent_skipped"
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

var (
	installationPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
	commitPattern       = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// runURLPattern holds no character that can end a Markdown link target.
	runURLPattern = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~-]+)+$`)
)

const (
	shortCommitLength = 7
	maxRunURLLength   = 512
)

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
	// Budget is the run's cap in USD, nil when the report was not told.
	// Rows record the enforcement grade and do not record the cap. The
	// policy's budget is a separate amount, read from costroid.yml.
	Budget *decimal.Decimal
	// Installation is the SCM installation id, empty when no App is installed.
	// On GitLab it is the project id.
	Installation string
	// AccountHost is the host the installation id belongs to, GitHub when
	// empty. It is a name, never an instance's address.
	AccountHost string
	// CheckRunID names the posted check, nil when the post failed.
	CheckRunID *int64
	// IDInCheck says the check run's own text names CheckRunID. The id exists
	// only once the check is created, so the report writes it in a second
	// request, and the result object names it once that request succeeds.
	IDInCheck bool
	Amounts   Amounts
	// Reason says why there are no figures, and ReasonLine names the line of
	// an InvalidRow.
	Reason     Reason
	ReasonLine int
	// RunReason is why the run wrote no rows, as the run stated it. It is
	// shown only when the rows file holds no rows.
	RunReason run.Reason
	// ReaderLine is the reader's first error line, shown only with the run
	// reason reader_refused, and cleaned again before it is.
	ReaderLine string
	// AgentError is the class of error the agent ended in, as the run stated
	// it. A class outside the closed list is dropped.
	AgentError adapter.AgentError
	// AgentSkipped says whether the CI system skipped the agent's job, nil
	// when the report was not told. The reason agent_skipped says it too.
	AgentSkipped *bool
	// CIRun is the CI run as the environment states it, nil when it states
	// none. A run whose id or attempt is below 1 is dropped.
	CIRun *BlockCIRun
	// RowsSHA256 is the digest of the rows' x_CostroidRowId values, sorted,
	// and RowsFileSHA256 the digest of the rows file's bytes, each as the
	// report read them. Empty when not stated; a value that is not a digest
	// is dropped.
	RowsSHA256     string
	RowsFileSHA256 string
	// Lifetime is the pull request's total, nil off a pull request. It is
	// withheld with the amounts.
	Lifetime *Lifetime
	// RunURL is the CI run's page as the environment names it, empty when it
	// names none. A value outside runURLPattern is dropped.
	RunURL string
	// BeforeCheck marks the object costroid run prints, which no check has
	// concluded yet, so its conclusion is null.
	BeforeCheck bool
	// Reconciliations are the last reconciliation of each credential, shown
	// under the budget when present.
	Reconciliations []Reconciliation
	// Verdict is the policy's, nil when no policy ran.
	Verdict *policy.Verdict
	// Approval is a review that approved the head, nil when none counts. It
	// turns a failed verdict's check neutral, and only on HeadSHA.
	Approval *Approval
}

// Approval is a review that approved one commit, as the host stated it.
type Approval struct {
	// Approver is the reviewer's login, which no App's account can be.
	Approver string
	HeadSHA  string
	At       time.Time
}

// loginPattern is a person's login on GitHub. A bracket, as an App's account
// ends in, never matches.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// Conclusion is the check's conclusion: the verdict's, neutral when no policy
// ran, and neutral when an approval binds a failed verdict's head.
func (i Input) Conclusion() string {
	if i.Verdict == nil {
		return policy.Neutral
	}
	if i.approved() {
		return policy.Neutral
	}
	return i.Verdict.Conclusion
}

// approved reports an approval that turns this check's failure neutral.
func (i Input) approved() bool {
	return i.Verdict != nil && i.Verdict.Conclusion == policy.Failure && i.Approval.binds(i.HeadSHA)
}

// binds reports an approval of the commit head by a person.
func (a *Approval) binds(head string) bool {
	return a != nil && commitPattern.MatchString(head) && a.HeadSHA == head && loginPattern.MatchString(a.Approver) && !a.At.IsZero()
}

// ApprovedConclusion is the conclusion a check gets from the policy's
// verdict and an approval of its head: neutral for an approved failure, and
// the verdict otherwise. On rows the agent's job wrote, an approval never
// concludes success.
func ApprovedConclusion(verdict string, approval *Approval, head string) string {
	if verdict == policy.Failure && approval.binds(head) {
		return policy.Neutral
	}
	return verdict
}

// approvalLine says who approved which commit and when, and what that does to
// the check. The login passed loginPattern, so it renders as a code span.
func approvalLine(approval Approval) string {
	return fmt.Sprintf("`%s` approved commit `%s` at %s, so the check concludes neutral. The policy's verdict stays failure, and a new push ends the approval.",
		approval.Approver, approval.HeadSHA[:shortCommitLength], approval.At.UTC().Format("2006-01-02 15:04 MST"))
}

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

// conclusion is null on an object no check has concluded.
func (i Input) conclusion() *string {
	if i.BeforeCheck {
		return nil
	}
	conclusion := i.Conclusion()
	return &conclusion
}

// runReason is the run's reason from the closed list, on a report whose rows
// file holds no rows. Beside rows, outside the list, beside a skipped agent,
// or agent_error without a class from its list, it is dropped.
func (i Input) runReason() run.Reason {
	switch {
	case i.State != NoRows || i.Reason == AgentSkipped || i.RunReason.Sentence() == "":
		return ""
	case i.RunReason == run.ReasonAgentError && run.AgentErrorFix(i.AgentError) == "":
		return ""
	}
	return i.RunReason
}

// agentError is the agent's error class from the closed list, beside figures
// or with the run reason agent_error, and empty otherwise.
func (i Input) agentError() adapter.AgentError {
	if run.AgentErrorFix(i.AgentError) == "" {
		return ""
	}
	if i.State != Reported && i.runReason() != run.ReasonAgentError {
		return ""
	}
	return i.AgentError
}

func (i Input) blockAgentError() *BlockAgentError {
	class := i.agentError()
	if class == "" {
		return nil
	}
	return &BlockAgentError{Class: string(class)}
}

// agentSkipped is true with the reason agent_skipped. Without it, a skipped
// agent is not stated, because a report with no such reason has figures or a
// reason of its own.
func (i Input) agentSkipped() *bool {
	skipped := i.Reason == AgentSkipped
	if skipped || (i.AgentSkipped != nil && !*i.AgentSkipped) {
		return &skipped
	}
	return nil
}

// ciRun is the CI run when its id and attempt are each at least 1.
func (i Input) ciRun() *BlockCIRun {
	if i.CIRun == nil || i.CIRun.ID < 1 || i.CIRun.Attempt < 1 {
		return nil
	}
	ciRun := *i.CIRun
	return &ciRun
}

// rowsDigest is the rows' digest beside figures, and nil without them.
func (i Input) rowsDigest() *string {
	if i.State != Reported {
		return nil
	}
	return digestOrNil(i.RowsSHA256)
}

func digestOrNil(digest string) *string {
	if !digestPattern.MatchString(digest) {
		return nil
	}
	return &digest
}

// block is the lifetime as the comment's lines state it, nil for no
// lifetime.
func (l *Lifetime) block() *BlockLifetime {
	if l == nil {
		return nil
	}
	shown := l.Figures[:min(len(l.Figures), maxLifetimeFigures)]
	block := &BlockLifetime{
		Figures:        make([]BlockLifetimeFigure, 0, len(shown)),
		FiguresOmitted: len(l.Figures) - len(shown),
		Capped:         l.Capped,
		Unread:         l.Unread,
	}
	for _, figure := range shown {
		block.Figures = append(block.Figures, BlockLifetimeFigure{
			Currency:  figure.Currency,
			CostBasis: figure.Basis,
			ListCost:  figure.Cost.String(),
			Reports:   figure.Reports,
			MayRepeat: figure.Undigested && figure.Reports > 1,
		})
	}
	return block
}

// readerLine is the reader's error line beside the reason reader_refused, and
// empty otherwise.
func (i Input) readerLine() string {
	if i.runReason() != run.ReasonReaderRefused {
		return ""
	}
	return run.ReaderLine(i.ReaderLine)
}

// headSHA is the head commit when it is one, and empty otherwise.
func (i Input) headSHA() string {
	if !commitPattern.MatchString(i.HeadSHA) {
		return ""
	}
	return i.HeadSHA
}

func tokenStrings(tokens map[focus.TokenClass]decimal.Decimal) map[string]string {
	counts := make(map[string]string, len(tokens))
	for class, count := range tokens {
		counts[string(class)] = count.String()
	}
	return counts
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
	// Mark is the visible flag on a figure with a row at the fallback tier.
	Mark string
	// Catalogs and the token columns are the job summary's.
	Catalogs                                     string
	InputUncached, CacheRead, CacheWrite, Output string
}

// traceBlock is one figure's collapsed arithmetic.
type traceBlock struct {
	Shown   bool
	Summary string
	Lines   []traceLine
	Omitted int
	// Withheld says the counts are withheld and where they are kept.
	Withheld string
}

type traceLine struct {
	Catalog   string
	Class     string
	PriceID   string
	UnitPrice string
	Tokens    string
	Cost      string
}

type view struct {
	Marker string
	// FirstLine states the figure with its basis and grade, before the
	// verdict. It is empty when the report has no figures.
	FirstLine  string
	Conclusion string
	// VerdictLine says what the policy found, after the conclusion.
	VerdictLine string
	// RunLine says the report is one run at one commit, and links the run.
	RunLine string
	// Why is the sentence for a report without figures, empty otherwise.
	Why string
	// AgentError is the sentence for an agent error beside figures, empty
	// otherwise.
	AgentError string
	// CheckRuns is false on GitLab, which has no check run to name.
	CheckRuns   bool
	ShowAmounts bool
	AmountsOff  bool
	CapLine     string
	// LimitLines state each limit the policy checked, apart from the cap.
	LimitLines []string
	// Reconciliations are closed sentences, one per credential.
	Reconciliations []string
	Lines           []figureLine
	FiguresOmitted  int
	Fallback        string
	Traces          []traceBlock
	Agents          string
	Models          string
	ModelsOmitted   int
	Catalogs        string
	Rows            int
	AccountURL      string
	ExportURL       string
	CheckRunID      *int64
	Block           string
}

// fallbackMark sits in the first cell of a figure the fallback tier priced in
// part, and the fallback sentence opens with the same words.
const fallbackMark = ", fallback price"

func (i Input) view() view {
	shown := i.Amounts == AmountsShown
	result := view{
		Marker:         Marker,
		FirstLine:      i.firstLine(shown),
		Conclusion:     i.Conclusion(),
		VerdictLine:    i.verdictLine(shown),
		RunLine:        runLine(i.HeadSHA, i.RunURL),
		Why:            i.why(),
		AgentError:     i.agentErrorBesideFigures(),
		ShowAmounts:    shown,
		AmountsOff:     i.Amounts == AmountsOff,
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Fallback:       fallbackSentence(i.Ledger),
		Agents:         spans(i.Ledger.Agents),
		Models:         spans(i.Ledger.Models),
		ModelsOmitted:  i.Ledger.ModelsOmitted,
		Catalogs:       spans(i.Ledger.Catalogs),
		Rows:           i.Ledger.Rows,
		AccountURL:     accountLink(i.AccountHost, i.Installation),
		CheckRuns:      i.AccountHost != AccountGitLab,
		ExportURL:      exportURL,
		CheckRunID:     i.CheckRunID,
	}
	result.CapLine = i.capLine()
	if shown {
		result.LimitLines = limitLines(i.Verdict, weakestGrade(i.Ledger.Figures))
	}
	result.Reconciliations = reconciliationLines(i.Reconciliations)
	for index, figure := range i.Ledger.Figures {
		line := figureLine{
			Cost:        figure.Currency + " " + figure.Cost.String(),
			Basis:       basisLabels[figure.Basis],
			Attribution: figure.Attribution,
			Enforcement: figure.Enforcement,
			Note:        enforcementNotes[figure.Enforcement],
		}
		if figure.FallbackRows > 0 {
			line.Mark = fallbackMark
		}
		line.Catalogs = spans(figure.Catalogs)
		columns := tokenColumns(figure.Tokens)
		line.InputUncached, line.CacheRead = columns["tokens_input_uncached"], columns["tokens_cache_read"]
		line.CacheWrite, line.Output = columns["tokens_cache_write"], columns["tokens_output"]
		result.Lines = append(result.Lines, line)
		result.Traces = append(result.Traces, traceOf(figure, index+1, shown, result.CheckRuns))
	}
	return result
}

// traceOf lays out one figure's arithmetic. Withheld, it keeps only the
// catalogs and the token classes: a count times the public catalog is the
// amount, so counts are withheld with the amounts.
func traceOf(figure Figure, number int, shown, checkRuns bool) traceBlock {
	if !shown {
		return withheldTrace(figure, number, checkRuns)
	}
	block := traceBlock{Shown: true, Summary: fmt.Sprintf("How %s %s was computed", figure.Currency, figure.Cost), Omitted: figure.TraceOmitted}
	for _, trace := range figure.Trace {
		block.Lines = append(block.Lines, traceLine{
			Catalog:   catalogCell(trace.Catalog),
			Class:     Data(trace.Class).Code(),
			PriceID:   trace.PriceID.Code(),
			UnitPrice: unitPrice(figure.Currency, trace),
			Tokens:    trace.Tokens.String(),
			Cost:      figure.Currency + " " + trace.Cost.String(),
		})
	}
	return block
}

func withheldTrace(figure Figure, number int, checkRuns bool) traceBlock {
	block := traceBlock{Summary: fmt.Sprintf("Token classes behind line %d", number), Withheld: "Token counts are withheld with the amounts. The rows carry them."}
	if checkRuns {
		block.Withheld = "Token counts are withheld with the amounts. The rows carry them, and the check run's machine block carries the amounts."
	}
	seen := map[traceLine]bool{}
	for _, trace := range figure.Trace {
		line := traceLine{Catalog: catalogCell(trace.Catalog), Class: Data(trace.Class).Code()}
		if !seen[line] {
			seen[line] = true
			block.Lines = append(block.Lines, line)
		}
	}
	return block
}

func catalogCell(catalog Data) string {
	if catalog == "" {
		return ""
	}
	return catalog.Code()
}

func unitPrice(currency string, trace Trace) string {
	if trace.Class.Informational() {
		return "informational, not priced"
	}
	return currency + " " + trace.UnitPrice.String() + " per " + unitWords(trace.Unit)
}

// unitWords reads a pricing unit such as "1000000 Tokens" as "1,000,000
// tokens". ReadRows admits no other form for a priced class.
func unitWords(unit string) string {
	match := pricingUnitPattern.FindStringSubmatch(unit)
	if match == nil {
		return unit
	}
	return grouped(match[1]) + " tokens"
}

// grouped writes a run of digits with a comma between each group of three.
func grouped(digits string) string {
	var out strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return out.String()
}

// firstFigures is how many figures the first line names. The table names
// every figure.
const firstFigures = 3

// firstLine states what the run cost, at which basis, and what held it to a
// cap, so the figure reads before the verdict. Withheld, it states the basis
// and the grade without an amount.
func (i Input) firstLine(shown bool) string {
	if i.State != Reported || len(i.Ledger.Figures) == 0 {
		return ""
	}
	figures := i.Ledger.Figures[:min(len(i.Ledger.Figures), firstFigures)]
	omitted := len(i.Ledger.Figures) - len(figures) + i.Ledger.FiguresOmitted
	var line string
	if shown {
		clauses := make([]string, len(figures))
		for index, figure := range figures {
			clauses[index] = fmt.Sprintf("%s %s at list (%s)", figure.Currency, figure.Cost, basisLabels[figure.Basis])
		}
		line = "This run cost " + listed(clauses, omitted) + "."
	} else {
		var clauses []string
		for _, figure := range i.Ledger.Figures {
			clause := "at list (" + basisLabels[figure.Basis] + ")"
			if !slices.Contains(clauses, clause) {
				clauses = append(clauses, clause)
			}
		}
		line = "This run was priced " + listed(clauses, 0) + "."
	}
	return line + " " + heldSentence(weakestGrade(i.Ledger.Figures))
}

// heldSentence says what held the run to its cap, by the weakest grade.
func heldSentence(grade string) string {
	if grade == "none" {
		return "Nothing held it to a budget."
	}
	return fmt.Sprintf("Held by %s: %s.", grade, enforcementNotes[grade])
}

// listed joins items as a sentence does, naming how many more are left out.
func listed(items []string, omitted int) string {
	if omitted > 0 {
		return strings.Join(items, ", ") + more(omitted)
	}
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// runLine names the commit and links the run only when each is well formed.
func runLine(head, link string) string {
	line := "This is one run, not a total across runs."
	if commitPattern.MatchString(head) {
		line = "This is one run at commit `" + head[:shortCommitLength] + "`, not a total across runs."
	}
	if len(link) <= maxRunURLLength && runURLPattern.MatchString(link) {
		line += " [CI run](" + link + ")"
	}
	return line
}

// why states the reason a report has no figures, from the closed list.
func (i Input) why() string {
	const shows = "so this report shows no figures."
	switch {
	case i.State == Reported:
		return ""
	case i.Reason == NoRowsFile || i.Reason == ForkNoRows:
		return "No rows file reached this report, so it shows no figures. The agent's job log says why it wrote none."
	case i.Reason == NotAFile:
		return "The rows path is not a regular file, so this report did not read it and shows no figures."
	case i.Reason == InvalidRow && i.ReasonLine > 0:
		return fmt.Sprintf("Line %d of the rows file is not a valid row, %s A partial total would read as a smaller cost.", i.ReasonLine, shows)
	case i.Reason == InvalidRow:
		return "A line of the rows file is not a valid row, " + shows + " A partial total would read as a smaller cost."
	case i.Reason == AgentSkipped:
		return "The agent did not run on this pull request: the workflow skipped its job, as the recipes do for a first-time contributor without the `costroid-agent` label and for a fork's pull request."
	case i.readerLine() != "":
		return fmt.Sprintf("This report shows no figures. Reason `%s`: %s. The reader's first error: `%s`.", i.RunReason, i.RunReason.Sentence(), i.readerLine())
	case i.agentError() != "":
		return fmt.Sprintf("This report shows no figures. Reason `%s`: %s. It ended with `%s`: %s.", i.RunReason, i.RunReason.Sentence(), i.AgentError, run.AgentErrorFix(i.AgentError))
	case i.runReason() != "":
		return fmt.Sprintf("This report shows no figures. Reason `%s`: %s.", i.RunReason, i.RunReason.Sentence())
	case i.State == NoRows:
		return "The rows file holds no rows, " + shows + " Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which."
	}
	return "The rows file could not be read, " + shows
}

// agentErrorBesideFigures says the agent ended in an error after the requests
// the figures cover, and what to do about it.
func (i Input) agentErrorBesideFigures() string {
	if i.State != Reported || i.agentError() == "" {
		return ""
	}
	return fmt.Sprintf("The agent ended with `%s` after the requests these figures cover: %s.", i.AgentError, run.AgentErrorFix(i.AgentError))
}

// fallbackSentence names the models the catalog does not price and the
// fallback prices that priced them. It is empty when no row used one.
func fallbackSentence(ledger Ledger) string {
	if len(ledger.FallbackModels) == 0 {
		return ""
	}
	verb, object := "is", "it"
	if len(ledger.FallbackModels)+ledger.FallbackModelsOmitted > 1 {
		verb, object = "are", "them"
	}
	catalog := "the catalog"
	if len(ledger.FallbackCatalogs) > 0 {
		catalog = "catalog " + spans(ledger.FallbackCatalogs)
	}
	return fmt.Sprintf("Fallback price: %s %s not in %s for the tier and token class used, so %s priced %s.",
		spans(ledger.FallbackModels)+more(ledger.FallbackModelsOmitted), verb, catalog,
		spans(ledger.FallbackPrices)+more(ledger.FallbackPricesOmitted), object)
}

func more(omitted int) string {
	if omitted == 0 {
		return ""
	}
	return fmt.Sprintf(" and %d more", omitted)
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

func blockVerdict(verdict *policy.Verdict, shown bool) *policy.Verdict {
	if verdict == nil || shown {
		return verdict
	}
	withheld := verdict.WithoutAmounts()
	return &withheld
}

var ruleLabels = map[string]string{
	policy.RuleRunBudget:          "Run budget",
	policy.RuleRunCostThreshold:   "Run cost threshold",
	policy.RuleRunIncrease:        "Increase over baseline",
	policy.RuleRunIncreasePercent: "Percent increase over baseline",
	policy.RulePullRequestBudget:  "Pull request budget",
	policy.RuleMonthlyBudget:      "Monthly budget",
	policy.RuleModelAllowlist:     "Model allowlist",
	policy.RuleAttribution:        "Attribution",
}

var limitNouns = map[string]string{
	policy.RuleRunBudget:          "budget",
	policy.RuleRunCostThreshold:   "threshold",
	policy.RuleRunIncrease:        "limit",
	policy.RuleRunIncreasePercent: "limit",
	policy.RulePullRequestBudget:  "budget",
	policy.RuleMonthlyBudget:      "budget",
}

// verdictLine opens with the approval when one turns the check neutral.
func (i Input) verdictLine(shown bool) string {
	line := verdictLine(i.Verdict, shown)
	if i.approved() {
		return approvalLine(*i.Approval) + " " + line
	}
	return line
}

// verdictLine is built from the verdict's closed fields and its amounts, so
// no text from the pull request reaches it.
func verdictLine(verdict *policy.Verdict, shown bool) string {
	if verdict == nil {
		return "No policy ran on this change."
	}
	return strings.Join(VerdictSentences(*verdict, shown), " ")
}

// VerdictSentences says what the policy found, one sentence per fact, in the
// words the comment uses. shown says whether amounts may be printed.
func VerdictSentences(verdict policy.Verdict, shown bool) []string {
	var sentences []string
	switch verdict.Config {
	case policy.ConfigInvalid:
		sentences = append(sentences, "No rule ran, so the check concludes neutral: `costroid.yml` on the base branch is not valid, and `costroid validate` names the problem.")
	case policy.ConfigUnread:
		sentences = append(sentences, "No rule ran, so the check concludes neutral: `costroid.yml` on the base branch could not be read, and a missing input never blocks a merge.")
	}
	held := false
	for _, result := range verdict.Results {
		sentences = append(sentences, resultSentence(result, verdict.JoinableRows, shown))
		held = held || result.HeldByWarnOnly
	}
	if len(sentences) == 0 {
		sentences = append(sentences, "No rule applied: `costroid.yml` on the base branch sets no budget and no threshold.")
	}
	if held {
		sentences = append(sentences, warnOnlySentence(verdict.WarnOnly))
	}
	if verdict.ConfigFromBase {
		sentences = append(sentences, "A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.")
	}
	return sentences
}

func resultSentence(result policy.Result, joinable int, shown bool) string {
	label, noun := ruleLabels[result.Rule], limitNouns[result.Rule]
	switch {
	case result.Rule == policy.RuleModelAllowlist:
		return label + ": " + allowlistSentence(result)
	case result.Rule == policy.RuleRunIncrease || result.Rule == policy.RuleRunIncreasePercent:
		if sentence := increaseSentence(result, shown); sentence != "" {
			return label + ": " + sentence
		}
	case result.Rule == policy.RulePullRequestBudget:
		if sentence := pullRequestSentence(result, shown); sentence != "" {
			return label + ": " + sentence
		}
	}
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("%s: USD %s is over the USD %s %s.", label, result.CostUSD, result.LimitUSD, noun)
		}
		return fmt.Sprintf("%s: the run is over the %s.", label, noun)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("%s: USD %s is within the USD %s %s.", label, result.CostUSD, result.LimitUSD, noun)
		}
		return fmt.Sprintf("%s: the run is within the %s.", label, noun)
	case policy.ReasonUnverifiedRows:
		return label + ": unverified rows cannot satisfy a budget."
	case policy.ReasonUnattributedRows:
		return label + ": unattributed rows cannot satisfy a budget."
	case policy.ReasonNoRows:
		return label + ": this check read no rows, so it cannot pass."
	case policy.ReasonOtherCurrency:
		return label + ": rows in another currency are never held to a USD " + noun + ", so it cannot pass."
	case policy.ReasonNoLedger:
		return label + ": the month to date comes only from a ledger, and this check has none, so the rule did not run."
	}
	rows := "rows name"
	if joinable == 1 {
		rows = "row names"
	}
	return fmt.Sprintf("%s: %d %s a credential, a pull request and a head commit, and stay unattributed until a ledger checks them against the job's OIDC token.", label, joinable, rows)
}

// allowlistSentence names the models off the list as code spans, each through
// the allowlist every pull request string passes.
func allowlistSentence(result policy.Result) string {
	switch result.Reason {
	case policy.ReasonOutsideAllowlist:
		names := make([]string, 0, len(result.Models))
		for _, model := range result.Models {
			names = append(names, Clean(model).Code())
		}
		switch len(names) {
		case 0:
			return "the run used a model that is not on the list."
		case 1:
			return "the run used " + names[0] + ", which is not on the list."
		}
		return "the run used " + strings.Join(names, ", ") + ", which are not on the list."
	case policy.ReasonOnAllowlist:
		return "every row names a model on the list."
	case policy.ReasonUnreadableModel:
		return "a row names no model this check can read, so the rule cannot pass."
	case policy.ReasonUnverifiedRows:
		return "unverified rows cannot show that every model was on the list."
	case policy.ReasonUnattributedRows:
		return "unattributed rows cannot show that every model was on the list."
	}
	return "this check read no rows, so it cannot pass."
}

// increaseSentence states a run against the median of earlier runs, or is
// empty for a reason every rule states alike.
func increaseSentence(result policy.Result, shown bool) string {
	allowed := "the allowed increase"
	if result.LimitPercent != "" {
		allowed = result.LimitPercent + "%"
	}
	amounts := shown && result.CostUSD != "" && result.BaselineUSD != ""
	if amounts && result.LimitUSD != "" {
		allowed = "USD " + result.LimitUSD
	}
	switch result.Reason {
	case policy.ReasonOverBaseline:
		if amounts {
			return fmt.Sprintf("USD %s is more than %s over the USD %s median of earlier runs.", result.CostUSD, allowed, result.BaselineUSD)
		}
		return fmt.Sprintf("the run is more than %s over the median of earlier runs.", allowed)
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("USD %s is at most %s over the USD %s median of earlier runs.", result.CostUSD, allowed, result.BaselineUSD)
		}
		return fmt.Sprintf("the run is at most %s over the median of earlier runs.", allowed)
	case policy.ReasonNoBaseline:
		return fmt.Sprintf("no baseline of %d or more earlier runs reached this check, so the rule did not run.", policy.MinBaselineRuns)
	}
	return ""
}

// pullRequestSentence states the pull request's total against its budget, or
// is empty for a reason every rule states alike.
func pullRequestSentence(result policy.Result, shown bool) string {
	amounts := shown && result.CostUSD != "" && result.LimitUSD != ""
	switch result.Reason {
	case policy.ReasonOverLimit:
		if amounts {
			return fmt.Sprintf("USD %s over this pull request's runs is over the USD %s budget.", result.CostUSD, result.LimitUSD)
		}
		return "this pull request's runs are over the budget."
	case policy.ReasonWithinLimit:
		if amounts {
			return fmt.Sprintf("USD %s over this pull request's runs is within the USD %s budget.", result.CostUSD, result.LimitUSD)
		}
		return "this pull request's runs are within the budget."
	case policy.ReasonNoLifetime:
		return "this check read no total for the pull request's earlier runs, so the rule did not run."
	case policy.ReasonRepeatPossible:
		return "the pull request's total may count a report twice, so it cannot show the budget broken or kept."
	}
	return ""
}

// enforceNow is the one line that ends the warn-only period.
const enforceNow = "Add `enforce: true` under `policy` in `costroid.yml` to enforce now."

func warnOnlySentence(period policy.WarnOnly) string {
	switch period.Reason {
	case policy.PeriodNotOnBase:
		return "Warn-only: this workflow is not on the base branch yet, so a rule that fails warns. " + enforceNow
	case policy.PeriodRunning:
		ends, err := time.Parse(time.RFC3339, period.Ends)
		if err == nil {
			return fmt.Sprintf("Warn-only until %s: for %d days from the commit that added this workflow to the base branch, a rule that fails warns. %s",
				ends.UTC().Format("2006-01-02 15:04 UTC"), int(policy.WarnOnlyPeriod.Hours()/24), enforceNow)
		}
	}
	return "Warn-only: the commit that added this workflow to the base branch could not be read, so a rule that fails warns. " + enforceNow
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
