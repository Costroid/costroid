package policy

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	maxAmountLength = 64
	// maxModelLength is the longest model id a comment prints.
	maxModelLength = 128
	// maxNamedModels bounds the models a verdict names, so a rows file with
	// many models cannot grow the check run past what the host stores.
	maxNamedModels = 10
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// modelPattern is a model id a comment may print as a code span. A row whose
// costroid/model tag is outside it has a model the check cannot read.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]*$`)

// Usage is one run's rows, reduced to what the pack reads.
type Usage struct {
	Rows       int
	Currencies []CurrencyUsage
}

// CurrencyUsage is the rows of one currency. ListCost is their exact sum, and
// no sum spans two currencies.
type CurrencyUsage struct {
	Currency string
	ListCost decimal.Decimal
	Rows     int
	// UnattributedRows never satisfy a budget rule.
	UnattributedRows int
	// JoinableRows are unattributed, and name a credential, a pull
	// request and a head commit, so a token-verified join would grade them
	// exact.
	JoinableRows int
	// Models counts the rows that name each model in their costroid/model
	// tag.
	Models map[string]int
	// UnreadableModels counts the rows whose costroid/model tag is absent,
	// not a string, or not a model id.
	UnreadableModels int
}

func (u Usage) usd() (decimal.Decimal, bool) {
	for _, entry := range u.Currencies {
		if entry.Currency == "USD" {
			return entry.ListCost, true
		}
	}
	return decimal.Decimal{}, false
}

func (u Usage) joinable() int {
	count := 0
	for _, entry := range u.Currencies {
		count += entry.JoinableRows
	}
	return count
}

// modelRows counts the rows whose model the allowlist refuses and the rows
// whose model cannot be read. outside names the refused models, sorted, at
// most maxNamedModels of them.
func (u Usage) modelRows(settings Settings) (refused, unreadable int, outside []string) {
	names := map[string]bool{}
	for _, entry := range u.Currencies {
		unreadable += entry.UnreadableModels
		for model, rows := range entry.Models {
			if !settings.allows(model) {
				refused += rows
				names[model] = true
			}
		}
	}
	outside = slices.Sorted(maps.Keys(names))
	return refused, unreadable, outside[:min(len(outside), maxNamedModels)]
}

type usageRow struct {
	BillingCurrency string     `json:"BillingCurrency"`
	ListCost        string     `json:"ListCost"`
	SubAccountID    *string    `json:"SubAccountId"`
	Tags            focus.Tags `json:"Tags"`
}

// tag reads a string tag, empty when it is absent or not a string.
func (r usageRow) tag(key string) string {
	value, _ := r.Tags.Text(key)
	return value
}

// joinable applies the ledger's rule for an exact grade to a row that
// lacks only the token: a credential that is not the session id, a pull
// request and a head.
func (r usageRow) joinable() bool {
	credential := ""
	if r.SubAccountID != nil {
		credential = *r.SubAccountID
	}
	return credential != "" && credential != r.tag(focus.TagSessionID) &&
		r.tag(focus.TagPR) != "" && r.tag(focus.TagCommitSHA) != ""
}

// TagOrigin is where the session behind a row ran, when it ran outside a CI
// run. OriginDeveloperMachine is its one value: a developer's own machine,
// whose session counts the developer stated in a receipt.
const (
	TagOrigin              = "costroid/origin"
	OriginDeveloperMachine = "developer_machine"
)

// ErrOriginRow refuses a rows file that holds a row stating
// costroid/origin. Such a row is counted from a developer's own session file,
// and a report keeps it in memory alone: no rows file carries one. A job that
// tagged its own run's rows so would make them vanish from the run's figure
// and every rule, so the file is refused whole, as for any row that is not
// valid.
var ErrOriginRow = errors.New("the row states costroid/origin, which marks a row from a developer's machine, and a rows file never carries one")

// RefuseOriginRows holds a rows file a job wrote to the rule ReadUsage and
// the report's reader apply as they read: it refuses the file at the first
// row Classify reads as a ReceiptRow, with ErrOriginRow, before a command
// sends or writes any of it. A line that is no row is left to the command's
// own reader, which refuses it in its own words.
func RefuseOriginRows(rows []byte) error {
	err := focus.EachLine(bytes.NewReader(rows), func(line []byte) error {
		var row struct {
			Tags focus.Tags `json:"Tags"`
		}
		if json.Unmarshal(line, &row) == nil && Classify(row.Tags) == ReceiptRow {
			return ErrOriginRow
		}
		return nil
	})
	var fault *focus.LineError
	if errors.As(err, &fault) && errors.Is(fault.Err, ErrOriginRow) {
		return fmt.Errorf("rows line %d: %w", fault.Line, ErrOriginRow)
	}
	return nil
}

// Kind is what a row is to a report.
type Kind int

// The kinds of row. Only a run's row reaches a rule, a budget, a total or a
// figure of the run.
const (
	// RunRow is a row of the run's own agent.
	RunRow Kind = iota
	// EstimateRow states a cost source and names no agent: an infrastructure
	// estimate the workflow added.
	EstimateRow
	// ReceiptRow carries costroid/origin, whatever its value: a row counted
	// from a developer's own session file.
	ReceiptRow
)

// Classify says what a row is, by its tags. It is the one place that decides
// which rows are a run's: every reader that sums or judges a run's rows reads
// through it. A row that names costroid/origin is never a run's, whatever
// value the tag holds and whatever else the row says, so a row from a
// developer's machine can neither raise nor satisfy a budget.
func Classify(tags focus.Tags) Kind {
	if _, stated := tags[TagOrigin]; stated {
		return ReceiptRow
	}
	agent, _ := tags.Text(focus.TagAgent)
	source, _ := tags.Text(focus.TagCostSource)
	if agent == "" && source != "" {
		return EstimateRow
	}
	return RunRow
}

// ClassifyRow is Classify for a row a writer holds.
func ClassifyRow(row focus.Row) Kind {
	tags := make(focus.Tags, len(row.Tags))
	for key, value := range row.Tags {
		tags[key], _ = json.Marshal(value)
	}
	return Classify(tags)
}

// ReadUsage reads FOCUS rows as JSON lines from a file the agent's job wrote.
// Every row is unattributed whatever its tags claim, because the job could
// write any tag. One bad row refuses the file: a partial total would read as a
// smaller cost. Classify says what each row is: an estimate's row is not read,
// and a row from a developer's machine refuses the file with ErrOriginRow.
func ReadUsage(source io.Reader) (Usage, error) {
	totals := map[string]*CurrencyUsage{}
	rows := 0
	err := focus.EachLine(source, func(line []byte) error {
		read, err := add(totals, line)
		if read {
			rows++
		}
		return err
	})
	var fault *focus.LineError
	switch {
	case errors.As(err, &fault) && errors.Is(fault.Err, focus.ErrLongLine):
		return Usage{}, fmt.Errorf("policy: rows line %d is past %d bytes", fault.Line, focus.MaxLineBytes)
	case errors.As(err, &fault):
		return Usage{}, fmt.Errorf("policy: rows line %d: %w", fault.Line, fault.Err)
	case err != nil:
		return Usage{}, fmt.Errorf("policy: read rows: %w", err)
	}
	usage := Usage{Rows: rows}
	for _, entry := range totals {
		usage.Currencies = append(usage.Currencies, *entry)
	}
	slices.SortFunc(usage.Currencies, func(a, b CurrencyUsage) int { return cmp.Compare(a.Currency, b.Currency) })
	return usage, nil
}

// add adds one row to its currency's totals, and reports whether it read it:
// an estimate's row is checked as every row is, and left out, and a row from a
// developer's machine is refused.
func add(totals map[string]*CurrencyUsage, line []byte) (bool, error) {
	var row usageRow
	if err := json.Unmarshal(line, &row); err != nil {
		return false, err
	}
	if !currencyPattern.MatchString(row.BillingCurrency) {
		return false, errors.New("BillingCurrency is not an ISO code")
	}
	if len(row.ListCost) > maxAmountLength {
		return false, fmt.Errorf("ListCost is past %d characters", maxAmountLength)
	}
	cost, err := decimal.Parse(row.ListCost)
	if err != nil {
		return false, fmt.Errorf("ListCost: %w", err)
	}
	if cost.Scale() > focus.MaxScale {
		return false, fmt.Errorf("ListCost has more than %d fractional digits", focus.MaxScale)
	}
	switch Classify(row.Tags) {
	case ReceiptRow:
		return false, ErrOriginRow
	case EstimateRow:
		return false, nil
	}
	entry, found := totals[row.BillingCurrency]
	if !found {
		entry = &CurrencyUsage{Currency: row.BillingCurrency, Models: map[string]int{}}
		totals[row.BillingCurrency] = entry
	}
	if model := row.tag(focus.TagModel); len(model) <= maxModelLength && modelPattern.MatchString(model) {
		entry.Models[model]++
	} else {
		entry.UnreadableModels++
	}
	entry.ListCost = entry.ListCost.Add(cost)
	entry.Rows++
	entry.UnattributedRows++
	if row.joinable() {
		entry.JoinableRows++
	}
	return true, nil
}
