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
	"time"

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

	// schema names the result object's contract, result/v0.3 in
	// Costroid/costroid-schemas.
	schema = "costroid.result/0.3"
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
}

// Conclusion is the check's conclusion: the verdict's, or neutral when no
// policy ran.
func (i Input) Conclusion() string {
	if i.Verdict == nil {
		return policy.Neutral
	}
	return i.Verdict.Conclusion
}

// Block is the result object, the machine-readable block. Strings under Data
// originate in the pull request.
type Block struct {
	Schema         string        `json:"schema"`
	CheckRunID     *int64        `json:"check_run_id,omitempty"`
	Conclusion     *string       `json:"conclusion"`
	State          State         `json:"state"`
	Reason         Reason        `json:"reason,omitempty"`
	ReasonLine     int           `json:"reason_line,omitempty"`
	RunReason      run.Reason    `json:"run_reason,omitempty"`
	HeadSHA        string        `json:"head_sha"`
	Amounts        string        `json:"amounts"`
	Rows           int           `json:"rows"`
	RunIdentity    string        `json:"run_identity"`
	Figures        []BlockFigure `json:"figures"`
	FiguresOmitted int           `json:"figures_omitted"`
	Cap            *BlockCap     `json:"cap"`
	Data           BlockData     `json:"data"`
	// Policy is the verdict, without its amounts when amounts are withheld.
	Policy *policy.Verdict `json:"policy,omitempty"`
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
		HeadSHA:        i.headSHA(),
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
// file holds no rows. Beside rows, or outside the list, it is dropped.
func (i Input) runReason() run.Reason {
	if i.State != NoRows || i.RunReason.Sentence() == "" {
		return ""
	}
	return i.RunReason
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
	Marker     string
	Conclusion string
	// VerdictLine says what the policy found, after the conclusion.
	VerdictLine string
	// RunLine says the report is one run at one commit, and links the run.
	RunLine string
	// Why is the sentence for a report without figures, empty otherwise.
	Why string
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
		Conclusion:     i.Conclusion(),
		VerdictLine:    verdictLine(i.Verdict, shown),
		RunLine:        runLine(i.HeadSHA, i.RunURL),
		Why:            i.why(),
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
	return currency + " " + trace.UnitPrice.String() + " per " + trace.Unit
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
	case i.Reason == NoRowsFile:
		return "No rows file reached this report, so it shows no figures. The agent's job log says why it wrote none."
	case i.Reason == NotAFile:
		return "The rows path is not a regular file, so this report did not read it and shows no figures."
	case i.Reason == InvalidRow && i.ReasonLine > 0:
		return fmt.Sprintf("Line %d of the rows file is not a valid row, %s A partial total would read as a smaller cost.", i.ReasonLine, shows)
	case i.Reason == InvalidRow:
		return "A line of the rows file is not a valid row, " + shows + " A partial total would read as a smaller cost."
	case i.runReason() != "":
		return fmt.Sprintf("This report shows no figures. Reason `%s`: %s.", i.RunReason, i.RunReason.Sentence())
	case i.State == NoRows:
		return "The rows file holds no rows, " + shows + " Either the agent printed no usage or its job stopped before it wrote a row, and that job's log says which."
	}
	return "The rows file could not be read, " + shows
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
	policy.RuleRunBudget:        "Run budget",
	policy.RuleRunCostThreshold: "Run cost threshold",
	policy.RuleAttribution:      "Attribution",
}

var limitNouns = map[string]string{
	policy.RuleRunBudget:        "budget",
	policy.RuleRunCostThreshold: "threshold",
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
		sentences = append(sentences, "No rule ran, so the check concludes neutral: `costroid.yml` at the base commit is not valid, and `costroid validate` names the problem.")
	case policy.ConfigUnread:
		sentences = append(sentences, "No rule ran, so the check concludes neutral: `costroid.yml` at the base commit could not be read, and a missing input never blocks a merge.")
	}
	held := false
	for _, result := range verdict.Results {
		sentences = append(sentences, resultSentence(result, verdict.JoinableRows, shown))
		held = held || result.HeldByWarnOnly
	}
	if len(sentences) == 0 {
		sentences = append(sentences, "No rule applied: `costroid.yml` at the base commit sets no budget and no threshold.")
	}
	if held {
		sentences = append(sentences, warnOnlySentence(verdict.WarnOnly))
	}
	if verdict.ConfigFromBase {
		sentences = append(sentences, "A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base commit's file.")
	}
	return sentences
}

func resultSentence(result policy.Result, joinable int, shown bool) string {
	label, noun := ruleLabels[result.Rule], limitNouns[result.Rule]
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
	}
	rows := "rows name"
	if joinable == 1 {
		rows = "row names"
	}
	return fmt.Sprintf("%s: %d %s a credential, a pull request and a head commit, and stay unattributed until a ledger checks them against the job's OIDC token.", label, joinable, rows)
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
