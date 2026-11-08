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
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

	// schema names the result object's contract, result/v0.8 in
	// Costroid/costroid-schemas.
	schema = "costroid.result/0.8"
	// artifactIdentity is what a figure read from a rows file can claim. The
	// agent's job wrote the file, tags included, so a row that reads
	// token_verified proves nothing here and the label does not depend on it.
	artifactIdentity = "unverified"
	budgetCurrency   = "USD"
	accountURL       = "https://app.costroid.com/"
	// accountPage is where the account link goes when the report names no
	// installation and no platform: the page that says what the account is.
	accountPage = "https://costroid.com/account/"
	exportURL   = "https://docs.costroid.com/export"
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

// ArtifactRefused is the run reason a report states when the action refused
// the artifact that carried the rows, because it held more than the rows and
// the run object. Nothing in that artifact is read.
const ArtifactRefused run.Reason = "artifact_refused"

// maxRefusedEntries is how many of a refused artifact's entries the action
// names, and maxRefusedName the longest name the comment repeats.
const (
	maxRefusedEntries = 10
	maxRefusedName    = 128
)

// refusedNameByte is a byte the comment repeats from a refused entry's name.
// The action shows every other byte as ?, and the comment does so again.
var refusedNameByte = regexp.MustCompile(`[^A-Za-z0-9._/-]`)

// RefusedArtifact is what an artifact the action refused held besides the
// rows and the run object. The agent's job chose every name in it.
type RefusedArtifact struct {
	// Count is how many entries it held besides those two files.
	Count int
	// Entries names the first of them, as the action printed them.
	Entries []string
}

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
	// originPattern is an https origin, and repositoryPattern the form of the
	// costroid/repo tag without its percent sign. Neither holds a character
	// that ends a Markdown link target or a query value.
	originPattern     = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._~-]+){1,8}$`)
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

// installationKinds is the kind each host's installation id takes in the
// integration contract.
var installationKinds = map[string]string{
	AccountGitHub: "github:",
	AccountGitLab: "gitlab-namespace:",
}

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
	// On GitLab it is the project's namespace id.
	Installation string
	// AccountHost is the host the installation id belongs to, GitHub when
	// empty. It is a name, never an instance's address.
	AccountHost string
	// Platform is Costroid Platform's https origin, empty when the report was
	// given none. Repository is the checked repository in the form of the
	// costroid/repo tag, and PullRequest its number, 0 off a pull request.
	// With all three the account link opens the pull request there.
	Platform    string
	Repository  string
	PullRequest int
	// CheckRunID names the posted check, nil when the post failed.
	CheckRunID *int64
	// IDInCheck says the check run's own text names CheckRunID. The id exists
	// only once the check is created, so the report writes it in a second
	// request, and the result object names it once that request succeeds.
	IDInCheck bool
	Amounts   Amounts
	// Preview marks the comment costroid report --preview prints. Its verdict
	// read costroid.yml in the working directory, and a run nothing held
	// says how its cost compares with the cap it was given.
	Preview bool
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
	// AgentStated is the agent's own figure for the run, in its own unit, nil
	// when it states none. It is shown on a line of its own, never added to
	// an amount, and withheld with the amounts. A unit or a source outside the
	// closed lists is dropped.
	AgentStated *adapter.Stated
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
	// Refused is the artifact the action refused, nil when it took the
	// artifact or fetched none. A refusal fails the check whatever the
	// policy or an approval says, and only on a report with no figures.
	Refused *RefusedArtifact
	// Recorded names the recorded run whose rows the report shows in place
	// of the pull request's own, nil for a report on the pull request's own
	// rows. One that names no run or no calendar date is dropped.
	Recorded *Recorded
}

// Recorded is a run recorded earlier, as costroid report --recorded names it.
type Recorded struct {
	// RunID is the CI run that wrote the rows, from 1.
	RunID int64
	// Date is the day the run was recorded, as YYYY-MM-DD.
	Date string
}

// recorded is the recorded run when it names a run and a calendar date, and
// nil otherwise.
func (i Input) recorded() *Recorded {
	if i.Recorded == nil || i.Recorded.RunID < 1 {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, i.Recorded.Date); err != nil {
		return nil
	}
	recorded := *i.Recorded
	return &recorded
}

// recordedLine opens a report on a recorded run's rows, so no reader takes
// its figures for the pull request's.
func (i Input) recordedLine() string {
	recorded := i.recorded()
	if recorded == nil {
		return ""
	}
	return fmt.Sprintf("These figures are a recorded run's, workflow run %d on %s, not this pull request's.", recorded.RunID, recorded.Date)
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

// conclusion is null on an object no check has concluded.
func (i Input) conclusion() *string {
	if i.BeforeCheck {
		return nil
	}
	conclusion := i.Conclusion()
	return &conclusion
}

// refused reports an artifact the action refused, on a report with no
// figures.
func (i Input) refused() bool {
	return i.Refused != nil && i.Refused.Count > 0 && i.State != Reported
}

// refusedLine names what a refused artifact held, each name cleaned again and
// shown as code.
func (i Input) refusedLine() string {
	entries := i.Refused.Entries[:min(len(i.Refused.Entries), maxRefusedEntries, i.Refused.Count)]
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = "`" + refusedNameByte.ReplaceAllString(entry[:min(len(entry), maxRefusedName)], "?") + "`"
	}
	noun := "entries"
	if i.Refused.Count == 1 {
		noun = "entry"
	}
	line := fmt.Sprintf("This report shows no figures. Reason `%s`: the `costroid-rows` artifact held %d %s besides the rows and the run object, so the check fails and this report read none of it",
		ArtifactRefused, i.Refused.Count, noun)
	if len(names) == 0 {
		return line + "."
	}
	return line + ": " + listed(names, i.Refused.Count-len(names)) + "."
}

// runReason is the run's reason from the closed list, on a report whose rows
// file holds no rows, or artifact_refused on a report whose artifact the
// action refused. Beside rows, outside the list, beside a skipped agent, or
// agent_error without a class from its list, it is dropped.
func (i Input) runReason() run.Reason {
	switch {
	case i.refused():
		return ArtifactRefused
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
	// Recorded opens a report on a recorded run's rows. It is empty on a
	// report on the pull request's own rows.
	Recorded string
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
	// AgentStated is the agent's own figure for the run, empty when it states
	// none or amounts are withheld.
	AgentStated string
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
		Recorded:       i.recordedLine(),
		FirstLine:      i.firstLine(shown),
		Conclusion:     i.Conclusion(),
		VerdictLine:    i.verdictLine(shown),
		RunLine:        runLine(i.runCommit(), i.RunURL),
		Why:            i.why(),
		AgentError:     i.agentErrorBesideFigures(),
		AgentStated:    i.agentStatedLine(shown),
		ShowAmounts:    shown,
		AmountsOff:     i.Amounts == AmountsOff,
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Fallback:       fallbackSentence(i.Ledger),
		Agents:         spans(i.Ledger.Agents),
		Models:         spans(i.Ledger.Models),
		ModelsOmitted:  i.Ledger.ModelsOmitted,
		Catalogs:       spans(i.Ledger.Catalogs),
		Rows:           i.Ledger.Rows,
		AccountURL:     i.accountLink(),
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
			Note:        figureNote(figure, shown),
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

// figureNote says how far a figure's grade held. A gh-aw figure at grade none
// names the cap gh-aw's proxy held the run to, which Costroid did not set and
// does not price; a withheld copy leaves the cap out with the amounts.
func figureNote(figure Figure, shown bool) string {
	if figure.Enforcement == "none" && figure.ProxyCap != "" && shown {
		return fmt.Sprintf("gh-aw's proxy held this run to %s AI credits, priced by gh-aw, not by Costroid", figure.ProxyCap)
	}
	return enforcementNotes[figure.Enforcement]
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

// runCommit is the commit the run line names: none on a recorded run's
// report, whose rows came from no commit of this pull request.
func (i Input) runCommit() string {
	if i.recorded() != nil {
		return ""
	}
	return i.HeadSHA
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
	case i.refused():
		return i.refusedLine()
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

// accountLink opens the pull request on Costroid Platform when the report
// names the platform, the repository and the pull request. Otherwise it carries
// the installation id, and with no installation it opens the page that says
// what the account is. A malformed value is dropped.
func (i Input) accountLink() string {
	if link, ok := pullLink(i.Platform, i.Repository, i.PullRequest); ok {
		return link
	}
	kind, known := installationKinds[cmp.Or(i.AccountHost, AccountGitHub)]
	if !known || !installationPattern.MatchString(i.Installation) {
		return accountPage
	}
	return accountURL + "?" + url.Values{"installation": {kind + i.Installation}}.Encode()
}

// pullLink is the pull request's page on the platform, built only from values
// its patterns checked, so none needs escaping.
func pullLink(platform, repository string, pullRequest int) (string, bool) {
	origin := strings.TrimSuffix(platform, "/")
	if pullRequest < 1 || len(origin) > maxRunURLLength || !originPattern.MatchString(origin) || !repositoryPattern.MatchString(repository) {
		return "", false
	}
	return origin + "/#/pulls?repo=" + repository + "&pr=" + strconv.Itoa(pullRequest), true
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
