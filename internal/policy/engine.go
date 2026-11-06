package policy

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/types"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	packName = "default.rego"
	query    = "data.costroid.verdict"
)

//go:embed default.rego
var pack []byte

// capabilitiesFile lists every built-in a pack may call. A pack that calls any
// other fails to load.
//
//go:embed capabilities.json
var capabilitiesFile []byte

// compareDecimal compares two amounts exactly. OPA compares a number of more
// than 15 significant digits as a float64, so the pack never compares amounts
// as numbers.
var compareDecimal = &rego.Function{
	Name: "costroid.compare_decimal",
	Decl: types.NewFunction(types.Args(types.S, types.S), types.N),
}

var defaultPack = sync.OnceValues(func() (rego.PreparedEvalQuery, error) {
	return load(context.Background(), packName, pack)
})

// PackSHA256 is the SHA-256 of the default pack's source.
func PackSHA256() string {
	return digest(pack)
}

// load compiles a pack under the capabilities file.
func load(ctx context.Context, name string, source []byte) (rego.PreparedEvalQuery, error) {
	capabilities, err := ast.LoadCapabilitiesJSON(bytes.NewReader(capabilitiesFile))
	if err != nil {
		return rego.PreparedEvalQuery{}, fmt.Errorf("policy: read the capabilities: %w", err)
	}
	prepared, err := rego.New(
		rego.Query(query),
		rego.Module(name, string(source)),
		rego.Capabilities(capabilities),
		rego.SetRegoVersion(ast.RegoV1),
		rego.Strict(true),
		rego.StrictBuiltinErrors(true),
		rego.Function2(compareDecimal, compare),
	).PrepareForEval(ctx)
	if err != nil {
		return rego.PreparedEvalQuery{}, fmt.Errorf("policy: load %s: %w", name, err)
	}
	return prepared, nil
}

func compare(_ rego.BuiltinContext, a, b *ast.Term) (*ast.Term, error) {
	x, err := amount(a)
	if err != nil {
		return nil, err
	}
	y, err := amount(b)
	if err != nil {
		return nil, err
	}
	return ast.IntNumberTerm(x.Cmp(y)), nil
}

func amount(term *ast.Term) (decimal.Decimal, error) {
	text, ok := term.Value.(ast.String)
	if !ok {
		return decimal.Decimal{}, errors.New("policy: costroid.compare_decimal takes decimal strings")
	}
	return decimal.Parse(string(text))
}

// packAnswer is what the pack's verdict rule returns.
type packAnswer struct {
	Conclusion string   `json:"conclusion"`
	Results    []Result `json:"results"`
}

func evaluatePack(ctx context.Context, input map[string]any) (packAnswer, error) {
	prepared, err := defaultPack()
	if err != nil {
		return packAnswer{}, err
	}
	return run(ctx, prepared, input)
}

func run(ctx context.Context, prepared rego.PreparedEvalQuery, input map[string]any) (packAnswer, error) {
	set, err := prepared.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return packAnswer{}, fmt.Errorf("policy: evaluate: %w", err)
	}
	if len(set) != 1 || len(set[0].Expressions) != 1 {
		return packAnswer{}, errors.New("policy: the pack returned no verdict")
	}
	encoded, err := json.Marshal(set[0].Expressions[0].Value)
	if err != nil {
		return packAnswer{}, fmt.Errorf("policy: read the verdict: %w", err)
	}
	var answer packAnswer
	if err := json.Unmarshal(encoded, &answer); err != nil {
		return packAnswer{}, fmt.Errorf("policy: read the verdict: %w", err)
	}
	return answer, answer.check()
}

// check holds the pack to the closed vocabulary a comment renders.
func (a packAnswer) check() error {
	if !slices.Contains([]string{Success, Neutral, Failure}, a.Conclusion) {
		return fmt.Errorf("policy: the pack concluded %q", a.Conclusion)
	}
	for _, result := range a.Results {
		if !slices.Contains(ruleOrder, result.Rule) || !slices.Contains(outcomes, result.Outcome) || !slices.Contains(reasons, result.Reason) {
			return fmt.Errorf("policy: the pack returned %s %s %s", result.Rule, result.Outcome, result.Reason)
		}
	}
	return nil
}

// packInput is the pack's input. Amounts travel as decimal strings.
func packInput(request Request, settings Settings, warnOnly bool) map[string]any {
	usage := make([]map[string]any, 0, len(request.Usage.Currencies))
	for _, entry := range request.Usage.Currencies {
		usage = append(usage, map[string]any{
			"currency":          entry.Currency,
			"list_cost":         entry.ListCost.String(),
			"rows":              entry.Rows,
			"unattributed_rows": entry.UnattributedRows,
			"joinable_rows":     entry.JoinableRows,
		})
	}
	limits := map[string]any{}
	if settings.RunBudget != nil {
		limits["run_budget_usd"] = settings.RunBudget.String()
	}
	if settings.MaxRunCost != nil {
		limits["max_run_cost_usd"] = settings.MaxRunCost.String()
	}
	return map[string]any{
		"trust":     string(request.Trust),
		"rows":      request.Usage.Rows,
		"usage":     usage,
		"settings":  limits,
		"warn_only": warnOnly,
	}
}
