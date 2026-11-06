package comment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Multiple is the value of currency, cost_basis or catalog_version when the
// run's figures hold more than one. The figure outputs are then empty, because
// figures in two currencies or two bases are never summed.
const Multiple = "multiple"

// OutputNames are the action's outputs, in the order it declares them.
var OutputNames = []string{
	"cost", "currency", "cost_basis",
	"tokens_input_uncached", "tokens_cache_read", "tokens_cache_write", "tokens_output",
	"enforcement_grade", "catalog_version", "check_run_id", "result",
}

// Output is one output of the action's step.
type Output struct {
	Name  string
	Value string
}

// Outputs are the step's outputs. They carry the amounts whatever the comment
// shows, as the rows and the check run's block do. A value is one line.
func Outputs(input Input) ([]Output, error) {
	result, err := Result(input)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(result)); err != nil {
		return nil, fmt.Errorf("comment: compact the result: %w", err)
	}
	values := figureOutputs(input.Ledger)
	values["enforcement_grade"] = weakestGrade(input.Ledger.Figures)
	values["catalog_version"] = only(input.Ledger.Catalogs)
	if input.CheckRunID != nil {
		values["check_run_id"] = strconv.FormatInt(*input.CheckRunID, 10)
	}
	values["result"] = compact.String()
	outputs := make([]Output, len(OutputNames))
	for index, name := range OutputNames {
		if strings.ContainsAny(values[name], "\r\n") {
			return nil, fmt.Errorf("comment: output %s holds a line break", name)
		}
		outputs[index] = Output{Name: name, Value: values[name]}
	}
	return outputs, nil
}

// figureOutputs fills the figure outputs when every figure shares one
// currency and one basis, and says multiple where they do not.
func figureOutputs(ledger Ledger) map[string]string {
	values := map[string]string{}
	currencies, bases := map[string]bool{}, map[string]bool{}
	for _, figure := range ledger.Figures {
		currencies[figure.Currency], bases[figure.Basis] = true, true
	}
	if len(ledger.Figures) == 0 {
		return values
	}
	values["currency"], values["cost_basis"] = Multiple, Multiple
	if len(currencies) == 1 {
		values["currency"] = ledger.Figures[0].Currency
	}
	if len(bases) == 1 {
		values["cost_basis"] = ledger.Figures[0].Basis
	}
	if len(currencies) > 1 || len(bases) > 1 || ledger.FiguresOmitted > 0 {
		return values
	}
	var cost decimal.Decimal
	tokens := map[focus.TokenClass]decimal.Decimal{}
	for _, figure := range ledger.Figures {
		cost = cost.Add(figure.Cost)
		for class, count := range figure.Tokens {
			tokens[class] = tokens[class].Add(count)
		}
	}
	values["cost"] = cost.String()
	for name, count := range tokenColumns(tokens) {
		values[name] = count
	}
	return values
}

// tokenColumns sums the classes into the four the outputs and the summary
// name. Cache writes of every lifetime are one column, and the classes that
// repeat another's tokens are left out.
func tokenColumns(tokens map[focus.TokenClass]decimal.Decimal) map[string]string {
	writes := tokens[focus.CacheWrite5m].Add(tokens[focus.CacheWrite30m]).Add(tokens[focus.CacheWrite1h]).Add(tokens[focus.CacheWrite12h])
	return map[string]string{
		"tokens_input_uncached": tokens[focus.InputUncached].String(),
		"tokens_cache_read":     tokens[focus.CacheRead].String(),
		"tokens_cache_write":    writes.String(),
		"tokens_output":         tokens[focus.Output].String(),
	}
}

// only is the one name in a list, multiple for more and empty for none.
func only(names []Data) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return string(names[0])
	}
	return Multiple
}

// Summary renders the job summary. It leads with the basis and the catalog,
// and it follows the comment: withheld, it shows grades and no amount or
// token count.
func Summary(input Input) (string, error) {
	return render("summary", input.view())
}
