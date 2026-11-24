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

	"github.com/Costroid/costroid/internal/accuracy"
	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/reason"
	"github.com/Costroid/costroid/internal/vcs"
)

const (
	// Marker opens the sticky comment, which is how the next run finds it.
	Marker = "<!-- costroid:comment -->"
	// CheckName is the one check run's name.
	CheckName = "costroid"

	// schema names the result object's contract, result/v0.10 in
	// Costroid/costroid-schemas. An object that states no estimate is written
	// as result/v0.9 instead, which it then is byte for byte, so a reader of
	// 0.9 reads it as before.
	schema = "costroid.result/0.10"
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
const ArtifactRefused reason.Reason = "artifact_refused"

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
	"none":             "no cap held the run",
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

// RequestNoun is what a host calls a change proposed for merge: a merge
// request on GitLab, a pull request on any other host. Only words a person
// reads take it, in the comment and in the job's log. Keys, tags, flags, rule
// ids and outputs keep pr and pull_request on every host.
func RequestNoun(host string) string {
	if host == AccountGitLab {
		return "merge request"
	}
	return "pull request"
}

// request is the word for a change on the host the report posts to.
func (i Input) request() string {
	return RequestNoun(i.AccountHost)
}

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
	// an InvalidRow. OriginRow says that line states costroid/origin, which
	// no rows file carries.
	Reason     Reason
	ReasonLine int
	OriginRow  bool
	// RunReason is why the run wrote no rows, as the run stated it. It is
	// shown only when the rows file holds no rows.
	RunReason reason.Reason
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
	// EstimateUnavailable is the note ingest-run --optional left beside the
	// rows when the estimate it was given was not there, nil when it left
	// none. Its reason is from a closed list. The machine block states it
	// among its estimates.
	EstimateUnavailable *focusfile.Unavailable
	// ReceiptsAsked says the workflow asked for receipts, so a pull request
	// whose author posted none says so. Receipt is the receipt the author
	// posted, read or refused, nil for none. It is one line on each surface
	// and receipts in the result object, and nothing else reads it.
	ReceiptsAsked bool
	Receipt       *Receipt
	// Accuracy is the record of Costroid's own accuracy test the release
	// carries, nil for none. On a report with figures, the folded details end
	// on one line about it. No sentence, conclusion, output or block reads it.
	Accuracy *accuracy.Record
	// forkRows says a fork's own workflow run wrote the rows, as OnForkRows
	// sets it.
	forkRows bool
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

// recordedLine stands under the verdict sentence of a report on a recorded
// run's rows, so no reader takes its figures for the pull request's.
func (i Input) recordedLine() string {
	recorded := i.recorded()
	if recorded == nil {
		return ""
	}
	return fmt.Sprintf("These figures are a recorded run's, workflow run %d on %s, not this %s's.", recorded.RunID, recorded.Date, i.request())
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

// refusedLine names what a refused artifact held besides the two files, each
// name cleaned again and shown as code. The verdict sentence says the report
// read none of it and the check fails.
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
	line := fmt.Sprintf("The `costroid-rows` artifact held %d %s besides the rows and the run object", i.Refused.Count, noun)
	if len(names) == 0 {
		return line + "."
	}
	return line + ": " + listed(names, i.Refused.Count-len(names)) + "."
}

// runReason is the run's reason from the closed list, on a report whose rows
// file holds no rows, or artifact_refused on a report whose artifact the
// action refused. Beside rows, outside the list, beside a skipped agent, or
// agent_error without a class from its list, it is dropped.
func (i Input) runReason() reason.Reason {
	switch {
	case i.refused():
		return ArtifactRefused
	case i.State != NoRows || i.Reason == AgentSkipped || i.RunReason.Sentence() == "":
		return ""
	case i.RunReason == reason.AgentError && reason.AgentErrorFix(i.AgentError) == "":
		return ""
	}
	return i.RunReason
}

// agentError is the agent's error class from the closed list, beside figures
// or with the run reason agent_error, and empty otherwise.
func (i Input) agentError() adapter.AgentError {
	if reason.AgentErrorFix(i.AgentError) == "" {
		return ""
	}
	if i.State != Reported && i.runReason() != reason.AgentError {
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
	if i.runReason() != reason.ReaderRefused {
		return ""
	}
	return reason.ReaderLine(i.ReaderLine)
}

// headSHA is the head commit when it is one, and empty otherwise.
func (i Input) headSHA() string {
	if !vcs.IsCommit(i.HeadSHA) {
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
	Cost  string
	Basis string
	// Note says how far the figure's enforcement grade held, in words. The
	// grade's id stays in the machine block.
	Note string
	// Mark is the visible flag on a figure with a row at the fallback tier,
	// or whose rows all note a subscription login.
	Mark string
	// Catalogs names the figure's catalogs in the table of a job summary that
	// withholds amounts, which folds no details to name them in.
	Catalogs string
	// Tokens are the figure's token counts, in words, which the job summary
	// folds with its details.
	Tokens string
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
	// Sentence is the verdict sentence, the first thing read on every
	// surface.
	Sentence string
	// Reading are the paragraphs between the sentence and the evidence: a
	// recorded run's line, the reason, the rules the sentence does not name
	// and the next step.
	Reading []string
	// RunLine says the report is one run at one commit, and links the run.
	RunLine string
	// NoFigures says the report has no agent figures, so it has no table.
	NoFigures bool
	// AgentError is the sentence for an agent error beside figures, empty
	// otherwise.
	AgentError string
	// AgentStated is the agent's own figure for the run, empty when it states
	// none or amounts are withheld.
	AgentStated string
	// PastCap is the cap line of a run that went past its cap, which stands
	// under the table. Any other cap line is folded with the details.
	PastCap string
	// CheckRuns is false on GitLab, which has no check run to name.
	CheckRuns   bool
	ShowAmounts bool
	AmountsOff  bool
	// Reconciliations are closed sentences, one per credential.
	Reconciliations []string
	Lines           []figureLine
	FiguresOmitted  int
	Fallback        string
	// Subscription says a figure's rows all note a subscription login, and
	// what that means for the figure. The figure's cell carries the mark.
	Subscription string
	// Details are the lines folded under the table: the cap against the
	// cost, each limit the policy checked and where it is set, what the
	// policy read, and the run's agents, models, rows and catalogs.
	// DetailsSummary names what they hold.
	Details        []string
	DetailsSummary string
	detailsHolds   []string
	Traces         []traceBlock
	// Infrastructure states the figures that are not an agent's, nil when the
	// rows hold none. It opens the section Beside folds.
	Infrastructure *infrastructureView
	// Beside are the figures that are not this run's, and the notes on them,
	// a paragraph each: an estimate that was not there, the receipt's line
	// and the change's total. No rule reads one, and none qualifies the
	// table, so with Infrastructure they are folded into one section, and
	// besideHolds names what it holds.
	Beside      []string
	besideHolds []string
	// Receipt is the receipt's one line, empty where the workflow did not
	// ask for receipts. Of the lines in Beside the job summary holds this one
	// alone, folded under its own name.
	Receipt string
	// Accuracy is the accuracy line, empty without a record or a figure. It
	// is the last of the Details, and on a job summary that folds none, the
	// summary's last line.
	Accuracy string
	// Purpose says what Costroid is and links the page on how the check
	// decides. It stands in the open in every comment.
	Purpose    string
	AccountURL string
	ExportURL  string
	CheckRunID *int64
	Block      string
}

// fallbackMark sits in the first cell of a figure the fallback tier priced in
// part, and the fallback sentence opens with the same words.
const fallbackMark = ", fallback price"

// subscriptionMark sits in the cell of a figure whose rows all note a
// subscription login, and the sentence under the table says what it means,
// in the words the rows' own note uses.
const subscriptionMark = ", subscription"

// subscriptionSentence stands under the table of a report that marks a
// figure. subject names the run the figures are.
func subscriptionSentence(subject string) string {
	return capital(subject) + " signed in on a " + focus.NoteSubscription + "."
}

func (i Input) view() view {
	shown := i.Amounts == AmountsShown
	sentence := VerdictSentence(i)
	result := view{
		Marker:         Marker,
		Sentence:       sentence,
		Reading:        i.reading(sentence, shown),
		RunLine:        runLine(i.runCommit(), i.RunURL),
		NoFigures:      i.State != Reported,
		AgentError:     i.agentErrorBesideFigures(),
		AgentStated:    i.agentStatedLine(shown),
		ShowAmounts:    shown,
		AmountsOff:     i.Amounts == AmountsOff,
		FiguresOmitted: i.Ledger.FiguresOmitted,
		Fallback:       fallbackSentence(i.Ledger),
		Purpose:        purposeLine(i.request()),
		AccountURL:     i.accountLink(),
		CheckRuns:      i.AccountHost != AccountGitLab,
		ExportURL:      exportURL,
		CheckRunID:     i.CheckRunID,
	}
	result.Infrastructure = i.Ledger.Infrastructure.view(shown, result.CheckRuns)
	// An estimate that was not there is named in the label only when no
	// infrastructure figure stands beside it.
	estimate := "infrastructure estimate"
	if result.Infrastructure != nil {
		result.besideHolds, estimate = append(result.besideHolds, "infrastructure figures"), ""
	}
	result.setBeside(estimate, i.estimateUnavailable())
	result.Receipt = i.receiptLine(shown)
	result.setBeside(receiptHolds, result.Receipt)
	if shown && i.capCheck().passed() {
		result.PastCap = i.capLine()
	}
	result.Details, result.detailsHolds = i.details(shown, result.PastCap == "")
	result.DetailsSummary = capital(listed(result.detailsHolds, 0))
	if result.Accuracy = i.accuracyLine(); result.Accuracy != "" {
		result.Details = append(result.Details, result.Accuracy)
	}
	result.Reconciliations = reconciliationLines(i.Reconciliations)
	for index, figure := range i.Ledger.Figures {
		line := figureLine{
			Cost:  Money(figure.Currency, figure.Cost.String()),
			Basis: basisLabels[figure.Basis],
			Note:  figureNote(figure, i.agentStated(), shown),
		}
		if figure.FallbackRows > 0 {
			line.Mark = fallbackMark
		}
		if figure.Subscription {
			line.Mark += subscriptionMark
			result.Subscription = subscriptionSentence(Block{Recorded: i.blockRecorded()}.subject())
		}
		line.Catalogs = spans(figure.Catalogs)
		columns := tokenColumns(figure.Tokens)
		line.Tokens = listed([]string{
			columns["tokens_input_uncached"] + " uncached input", columns["tokens_cache_read"] + " cache read",
			columns["tokens_cache_write"] + " cache write", columns["tokens_output"] + " output",
		}, 0)
		result.Lines = append(result.Lines, line)
		result.Traces = append(result.Traces, traceOf(figure, index+1, shown, result.CheckRuns))
	}
	return result
}

// setBeside folds paragraphs into the section of figures that are not this
// run's, and names what they are in its label, once. An empty paragraph is
// left out, and an empty name adds nothing to the label.
func (v *view) setBeside(holds string, paragraphs ...string) {
	for _, paragraph := range paragraphs {
		if paragraph == "" {
			continue
		}
		v.Beside = append(v.Beside, paragraph)
		if holds != "" && !slices.Contains(v.besideHolds, holds) {
			v.besideHolds = append(v.besideHolds, holds)
		}
	}
}

// receiptHolds names a receipt's line in the label of the section that folds
// it.
const receiptHolds = "developer-machine sessions"

// ReceiptSummary labels the section the job summary folds a receipt's line
// in.
func (view) ReceiptSummary() string {
	return capital(receiptHolds)
}

// BesideSummary labels the folded section by what it holds, and is empty when
// it holds nothing.
func (v view) BesideSummary() string {
	if len(v.besideHolds) == 0 {
		return ""
	}
	return capital(listed(v.besideHolds, 0))
}

// traceOf lays out one figure's arithmetic. Withheld, it keeps only the
// catalogs and the token classes: a count times the public catalog is the
// amount, so counts are withheld with the amounts.
func traceOf(figure Figure, number int, shown, checkRuns bool) traceBlock {
	if !shown {
		return withheldTrace(figure, number, checkRuns)
	}
	block := traceBlock{Shown: true, Summary: "How " + Money(figure.Currency, figure.Cost.String()) + atList([]string{figure.Basis}) + " was computed", Omitted: figure.TraceOmitted}
	for _, trace := range figure.Trace {
		block.Lines = append(block.Lines, traceLine{
			Catalog:   catalogCell(trace.Catalog),
			Class:     Data(trace.Class).Code(),
			PriceID:   trace.PriceID.Code(),
			UnitPrice: unitPrice(figure.Currency, trace),
			Tokens:    trace.Tokens.String(),
			Cost:      traceCost(figure.Currency, trace),
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
	return Money(currency, trace.UnitPrice.String()) + " per " + unitWords(trace.Unit)
}

// traceCost is a trace line's amount. A class that carries no price has no
// amount, and says so: a zero would read as a price of nothing.
func traceCost(currency string, trace Trace) string {
	if trace.Class.Informational() {
		return "No amount: not priced"
	}
	return Money(currency, trace.Cost.String())
}

// millionTokens is the count of tokens the catalogs state a price per.
const millionTokens = "1000000"

// unitWords reads a pricing unit such as "1000000 Tokens" as "million
// tokens", which holds no digits to group, and any other count as its digits.
// ReadRows admits no other form for a priced class.
func unitWords(unit string) string {
	match := pricingUnitPattern.FindStringSubmatch(unit)
	switch {
	case match == nil:
		return unit
	case match[1] == millionTokens:
		return "million tokens"
	}
	return match[1] + " tokens"
}

// The lines that say who vouched for the rows, and so how firmly each is tied
// to its run. Rows a ledger checked against the job's token read checked only
// when the verdict says so: a tag inside the rows file proves nothing, because
// the agent's job wrote it. Each line stands under every sentence but one
// that closes on its clause of trustClosings, which says the same.
const (
	uncheckedLine = "Unchecked: the agent's own job wrote these figures, and nothing outside it checked them."
	checkedLine   = "Checked: a ledger checked the rows the verdict read against the job's OIDC token."
)

// trustClosings is the closing clause of a sentence that says what each line
// says.
var trustClosings = map[string]string{uncheckedLine: uncheckedClosing, checkedLine: checkedClosing}

// trustLine reads the trust the verdict, and so the machine block's
// policy.trust, states. With no verdict the rows are the rows file's, which
// the agent's job wrote.
func (i Input) trustLine() string {
	if i.Verdict != nil && i.Verdict.Trust == policy.TokenVerified {
		return checkedLine
	}
	return uncheckedLine
}

// details are the lines a reader opens for: the cap against the cost, each
// limit the policy checked, what the policy read, and the run's agents,
// models, rows and catalogs. Each repeats a cell of the table or explains the
// verdict, so they are folded. The summary names what they hold. withCap is
// false when the cap line stands above the fold.
func (i Input) details(shown, withCap bool) (lines, holds []string) {
	if capLine := i.capLine(); shown && withCap && capLine != "" {
		lines, holds = append(lines, capLine), append(holds, "cap")
	}
	if shown {
		limits, nouns := limitLines(i.Verdict, weakestGrade(i.Ledger.Figures))
		lines = append(lines, limits...)
		for _, noun := range nouns {
			if !slices.Contains(holds, noun) {
				holds = append(holds, noun)
			}
		}
	}
	if notes := i.policyNotes(); notes != "" {
		lines, holds = append(lines, notes), append(holds, "policy")
	}
	var run []string
	if len(i.Ledger.Agents) > 0 {
		run, holds = append(run, "Agents: "+spans(i.Ledger.Agents)+"."), append(holds, "agents")
	}
	if len(i.Ledger.Models) > 0 {
		run, holds = append(run, "Models: "+spans(i.Ledger.Models)+more(i.Ledger.ModelsOmitted)+"."), append(holds, "models")
	}
	rows := fmt.Sprintf("%d %s", i.Ledger.Rows, plural(i.Ledger.Rows, "row", "rows"))
	if len(i.Ledger.Catalogs) > 0 {
		rows, holds = rows+", "+catalogPhrase(i.Ledger.Catalogs), append(holds, "catalog")
	}
	lines = append(lines, strings.Join(append(run, rows+"."), " "))
	if len(holds) == 0 {
		holds = append(holds, "rows")
	}
	return lines, holds
}

// catalogPhrase names the rows' catalogs, each with the day its prices were
// read when the binary carries that release.
func catalogPhrase(catalogs []Data) string {
	dated := func(name Data) (string, string) {
		day, known := catalog.PricesRead(string(name))
		if !known {
			return name.Code(), ""
		}
		return name.Code(), "prices read " + day
	}
	if len(catalogs) == 1 {
		name, day := dated(catalogs[0])
		if day == "" {
			return "catalog " + name
		}
		return "catalog " + name + ", " + day
	}
	names := make([]string, len(catalogs))
	for index, entry := range catalogs {
		name, day := dated(entry)
		names[index] = name
		if day != "" {
			names[index] += " (" + day + ")"
		}
	}
	return "catalogs " + listed(names, 0)
}

// firstFigures is how many figures the verdict sentence names. The table
// names every figure.
const firstFigures = 3

// figureNote says how far a figure's grade held. A gh-aw figure at grade none
// names the cap gh-aw checks, which Costroid did not set and does not price,
// and how far gh-aw's stated credits passed it; a withheld copy leaves both
// out with the amounts.
func figureNote(figure Figure, stated *adapter.Stated, shown bool) string {
	if figure.Enforcement == "none" && figure.ProxyCap != "" && shown {
		return ghaw.CapSentence(figure.ProxyCap, stated)
	}
	return enforcementNotes[figure.Enforcement]
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
	if vcs.IsCommit(head) {
		line = "This is one run at commit `" + head[:shortCommitLength] + "`, not a total across runs."
	}
	if len(link) <= maxRunURLLength && runURLPattern.MatchString(link) {
		line += " [CI run](" + link + ")"
	}
	return line
}

// why is what the verdict sentence leaves unsaid of a report with no figures:
// what a refused artifact held, why the workflow skipped the agent, why a
// fork's run left no rows, the reader's first error, or what to do about the
// agent's. When a rule broken on the pull request's earlier runs names the
// sentence, the reason for no figures opens the paragraph in full.
func (i Input) why() string {
	var said []string
	if decided, ruled := deciding(i.Verdict); ruled && broken(decided) && !i.refused() {
		said = append(said, i.telling().noFigure(false))
	}
	switch {
	case i.refused():
		said = append(said, i.refusedLine())
	case i.Reason == AgentSkipped:
		said = append(said, "The recipes skip the agent's job for a first-time contributor without the `costroid-agent` label and for a fork's "+i.request()+".")
	case i.Reason == ForkNoRows:
		said = append(said, "The recipes run no agent on a fork's "+i.request()+". A neutral check does not block a merge.")
	case i.Reason == InvalidRow && i.OriginRow:
		said = append(said, originRowLine)
	case i.readerLine() != "":
		said = append(said, fmt.Sprintf("The reader's first error: `%s`.", i.readerLine()))
	case i.agentError() != "":
		said = append(said, fmt.Sprintf("It ended with `%s`: %s.", i.AgentError, reason.AgentErrorFix(i.AgentError)))
	}
	return strings.Join(said, " ")
}

// originRowLine says why a rows file that holds a row from a developer's
// machine is read whole as no figure.
const originRowLine = "That row states `costroid/origin`, which marks a row from a developer's machine. A report builds such rows from a receipt in its own memory, and no rows file carries one, so this report read none of the file."

// agentErrorBesideFigures says the agent ended in an error after the requests
// the figures cover, and what to do about it.
func (i Input) agentErrorBesideFigures() string {
	if i.State != Reported || i.agentError() == "" {
		return ""
	}
	return fmt.Sprintf("The agent ended with `%s` after the requests these figures cover: %s.", i.AgentError, reason.AgentErrorFix(i.AgentError))
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

// purposeLine says what posted the comment and links the page on how the
// check decides, for a reader who never installed Costroid. request is the
// host's word for a change.
func purposeLine(request string) string {
	return "Costroid is the cost check for " + request + "s. [How the check decides](" + policyPage + ")"
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
