package policy

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

// A customer pack is one Rego module a repository keeps at its base commit,
// beside costroid.yml. It is compiled alone, never beside the default pack,
// and its results can only add warnings and failures to Costroid's verdict.

const (
	customerPackage = "costroid.pack"
	customerQuery   = "data.costroid.pack.results"
	// maxPackBytes is the largest pack compiled, the bound serve puts on
	// costroid.yml.
	maxPackBytes = 64 << 10
	// maxPackCurrencies and maxPackModels bound the input. A larger rows file
	// is not cut short: the pack does not run.
	maxPackCurrencies = 16
	maxPackModels     = 256
	maxPackResults    = 32
	// maxPackSteps is how many expressions an evaluation may begin. A count is
	// the same on a fast runner and a slow one, so the same rows meet the
	// same bound everywhere. It is at least 100 times the count of each pack
	// in testdata/packs on the largest input.
	maxPackSteps = 200_000
	// maxPackTime bounds compiling and evaluating together, for the work the
	// count does not see: the compiler's, and what one step does. Unlike the
	// count it ends a run sooner on a slower runner, and only ever in a fault
	// that fails.
	maxPackTime     = 2 * time.Second
	maxMessageBytes = 160
)

// The rule of Costroid's own result on a pack that gave no answer, and its
// reasons.
const (
	ruleCustomerPack = "customer_pack"
	// packUnread: the file is absent, too large, or the host did not answer.
	packUnread = "pack_unread"
	// packInvalid: the module does not compile under capabilities.pack.json,
	// or declares another package, or names a result for one of Costroid's
	// rules.
	packInvalid = "pack_invalid"
	// packBound: the input, the steps, the clock or the results passed their
	// bound.
	packBound = "pack_bound"
	// packResult: the answer is not a list of results in the pack's words.
	packResult = "pack_result"
)

// packFaultOutcomes is the outcome of Costroid's customer_pack result for
// each fault. A fault that only the base branch can cause, or the host, is
// unproven, as an invalid costroid.yml runs no rule and concludes neutral. A
// fault the rows can cause fails: were it unproven, a rows file the head's job
// wrote could stall a pack that would have failed the run, and the pull
// request would have made its own check pass.
var packFaultOutcomes = map[string]string{
	packUnread:  Unproven,
	packInvalid: Unproven,
	packBound:   Fail,
	packResult:  Fail,
}

// reservedRules are the rule ids of Costroid's own results, run_p95 among
// them before its rule ships. A pack's result never takes one.
var reservedRules = append(slices.Clone(ruleOrder), "run_p95", ruleCustomerPack)

// packCapabilitiesFile lists every built-in a customer pack may call: the
// default pack's seven, the four other comparisons, count, three string
// tests, and the two-variable form of some ... in. Each is pure and returns
// nothing larger than its input. There is no network, clock, randomness,
// file, environment or arithmetic.
//
//go:embed capabilities.pack.json
var packCapabilitiesFile []byte

// idPattern is a pack result's rule and reason.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// PackOutcome is what a pack's result says. It has two values: a pack has no
// word for pass.
type PackOutcome string

// The two outcomes of a pack's result.
const (
	PackWarn PackOutcome = "warn"
	PackFail PackOutcome = "fail"
)

// PackResult is one result a customer pack returned.
type PackResult struct {
	Rule    string      `json:"rule"`
	Outcome PackOutcome `json:"outcome"`
	Reason  string      `json:"reason"`
	// Message holds only the characters a comment prints as a code span, at
	// most 160 bytes. Empty, the pack gave none.
	Message        string `json:"message,omitempty"`
	HeldByWarnOnly bool   `json:"held_by_warn_only,omitempty"`
}

// PackFault is why a customer pack gave no results. Its reason alone decides
// the outcome of Costroid's customer_pack result.
type PackFault struct {
	reason string
	// Line is the pack's line the parser or the compiler named, 0 when it
	// named none.
	Line int
	err  error
}

func (f *PackFault) Error() string {
	if f.Line > 0 {
		return fmt.Sprintf("policy: customer pack: %s at line %d: %v", f.reason, f.Line, f.err)
	}
	return fmt.Sprintf("policy: customer pack: %s: %v", f.reason, f.err)
}

func (f *PackFault) Unwrap() error {
	return f.err
}

// Reason is the reason Costroid's customer_pack result states.
func (f *PackFault) Reason() string {
	return f.reason
}

// result is Costroid's own customer_pack result for the fault. A reason the
// table does not hold fails, so a fault never concludes looser than the
// table says.
func (f *PackFault) result() Result {
	outcome, known := packFaultOutcomes[f.reason]
	if !known {
		outcome = Fail
	}
	return Result{Rule: ruleCustomerPack, Outcome: outcome, Reason: f.reason}
}

func fault(reason string, line int, err error) *PackFault {
	return &PackFault{reason: reason, Line: line, err: err}
}

// customerPack is a compiled customer pack.
type customerPack struct {
	query rego.PreparedEvalQuery
}

// packBounds are what one run of a pack may spend: steps, and time for
// compiling and evaluating together.
type packBounds struct {
	steps int64
	time  time.Duration
}

var customerBounds = packBounds{steps: maxPackSteps, time: maxPackTime}

// RunCustomerPack evaluates a customer pack over the request Costroid's
// verdict was computed from, and joins its results to that verdict. The
// conclusion it returns is never below the verdict's.
func RunCustomerPack(ctx context.Context, source []byte, request Request, verdict Verdict) Composed {
	results, problem := runCustomerPack(ctx, source, request, verdict.Results, customerBounds)
	return compose(verdict, results, problem)
}

func runCustomerPack(ctx context.Context, source []byte, request Request, results []Result, bounds packBounds) ([]PackResult, *PackFault) {
	deadline := time.Now().Add(bounds.time)
	pack, problem := loadCustomerPack(ctx, source, deadline)
	if problem != nil {
		return nil, problem
	}
	settings, _ := request.Config.settings()
	input, problem := customerInput(request, settings, results)
	if problem != nil {
		return nil, problem
	}
	answer, _, problem := pack.evaluate(ctx, input, bounds.steps, deadline)
	return answer, problem
}

// within runs work on a goroutine of its own and waits for it until the
// deadline or the end of ctx. OPA's compiler takes no context, and one step
// of an evaluation can compare two values whose size doubles with each
// expression that built them, so neither is stopped between steps. Past the
// deadline within returns false, and nothing waits for the goroutine, which
// ends when its work does.
func within[T any](ctx context.Context, deadline time.Time, work func() T) (T, bool) {
	done := make(chan T, 1)
	go func() { done <- work() }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case value := <-done:
		return value, true
	case <-timer.C:
	case <-ctx.Done():
	}
	var none T
	return none, false
}

// loadCustomerPack compiles one module alone under capabilities.pack.json,
// by the deadline. It never sees the default pack, so no rule of the module
// can join a document of Costroid's.
func loadCustomerPack(ctx context.Context, source []byte, deadline time.Time) (*customerPack, *PackFault) {
	if len(source) > maxPackBytes {
		return nil, fault(packUnread, 0, fmt.Errorf("the file holds %d bytes, over %d", len(source), maxPackBytes))
	}
	type compiled struct {
		pack    *customerPack
		problem *PackFault
	}
	out, finished := within(ctx, deadline, func() compiled {
		pack, problem := compileCustomerPack(ctx, source)
		return compiled{pack, problem}
	})
	if !finished {
		return nil, fault(packBound, 0, errors.New("the pack did not compile in time"))
	}
	return out.pack, out.problem
}

func compileCustomerPack(ctx context.Context, source []byte) (*customerPack, *PackFault) {
	capabilities, err := ast.LoadCapabilitiesJSON(bytes.NewReader(packCapabilitiesFile))
	if err != nil {
		return nil, fault(packInvalid, 0, fmt.Errorf("read the capabilities: %w", err))
	}
	module, err := ast.ParseModuleWithOpts("pack.rego", string(source), ast.ParserOptions{
		Capabilities: capabilities,
		RegoVersion:  ast.RegoV1,
	})
	if err != nil {
		return nil, compileFault(err)
	}
	if problem := checkModule(module); problem != nil {
		return nil, problem
	}
	prepared, err := rego.New(
		rego.Query(customerQuery),
		rego.ParsedModule(module),
		rego.Capabilities(capabilities),
		rego.SetRegoVersion(ast.RegoV1),
		rego.Strict(true),
		rego.StrictBuiltinErrors(true),
		rego.Function2(compareDecimal, compare),
		rego.GenerateJSON(decodeResults),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, compileFault(err)
	}
	return &customerPack{query: prepared}, nil
}

// checkModule refuses a module whose package is not costroid.pack, and one
// that writes a result for a rule of Costroid's.
func checkModule(module *ast.Module) *PackFault {
	if path := module.Package.Path.String(); path != "data."+customerPackage {
		return fault(packInvalid, row(module.Package.Location), fmt.Errorf("the package is %s, not %s", strings.TrimPrefix(path, "data."), customerPackage))
	}
	var problem *PackFault
	ast.WalkTerms(module, func(term *ast.Term) bool {
		object, ok := term.Value.(ast.Object)
		if !ok || problem != nil {
			return problem != nil
		}
		named := object.Get(ast.InternedTerm("rule"))
		if named == nil {
			return false
		}
		if rule, ok := named.Value.(ast.String); ok && slices.Contains(reservedRules, string(rule)) {
			problem = fault(packInvalid, row(term.Location), fmt.Errorf("a result names Costroid's rule %s", rule))
		}
		return problem != nil
	})
	return problem
}

// compileFault reads the line of the first error the parser or the compiler
// gives.
func compileFault(err error) *PackFault {
	var errs ast.Errors
	if errors.As(err, &errs) && len(errs) > 0 {
		return fault(packInvalid, row(errs[0].Location), err)
	}
	var single *ast.Error
	if errors.As(err, &single) {
		return fault(packInvalid, row(single.Location), err)
	}
	return fault(packInvalid, 0, err)
}

func row(location *ast.Location) int {
	if location == nil {
		return 0
	}
	return location.Row
}

// customerInput is everything a customer pack reads, and the one place it is
// built. It holds no row, tag, path, branch, commit, pull request, person,
// credential, clock, environment, or warn-only state: only the rows' counts
// and costs, the settings the base commit states, the baseline and the pull
// request's total, and Costroid's own results without their figures.
func customerInput(request Request, settings Settings, results []Result) (ast.Value, *PackFault) {
	usage := request.Usage
	if len(usage.Currencies) > maxPackCurrencies {
		return nil, fault(packBound, 0, fmt.Errorf("the rows hold %d currencies, over %d", len(usage.Currencies), maxPackCurrencies))
	}
	models := 0
	entries := make([]any, 0, len(usage.Currencies))
	for _, entry := range usage.Currencies {
		models += len(entry.Models)
		perModel := make(map[string]any, len(entry.Models))
		for model, rows := range entry.Models {
			perModel[model] = rows
		}
		entries = append(entries, map[string]any{
			"currency":          entry.Currency,
			"list_cost":         entry.ListCost.String(),
			"rows":              entry.Rows,
			"unattributed_rows": entry.UnattributedRows,
			"joinable_rows":     entry.JoinableRows,
			"models":            perModel,
		})
	}
	if models > maxPackModels {
		return nil, fault(packBound, 0, fmt.Errorf("the rows name %d model ids, over %d", models, maxPackModels))
	}
	limits := map[string]any{}
	for key, amount := range settings.Amounts {
		limits[key] = amount.String()
	}
	if len(settings.Models) > 0 {
		limits[keyModels] = settings.Models
	}
	seen := make([]any, 0, len(results))
	for _, result := range results {
		seen = append(seen, map[string]any{"rule": result.Rule, "outcome": result.Outcome, "reason": result.Reason})
	}
	input := map[string]any{
		"trust":    string(trusted(request.Trust)),
		"rows":     usage.Rows,
		"usage":    entries,
		"settings": limits,
		"results":  seen,
	}
	if baseline := baselineInput(request.Baseline, Settings{}); baseline != nil {
		input["baseline"] = baseline
	}
	if lifetime := lifetimeInput(request.Lifetime, usage); lifetime != nil {
		input["lifetime"] = lifetime
	}
	value, err := ast.InterfaceToValue(input)
	if err != nil {
		return nil, fault(packBound, 0, fmt.Errorf("build the input: %w", err))
	}
	return value, nil
}

// stepBound counts the expressions an evaluation begins, and cancels it
// past its limit, its deadline or its context. OPA asks Cancelled before
// each expression.
type stepBound struct {
	ctx      context.Context
	limit    int64
	deadline time.Time
	steps    atomic.Int64
	tripped  atomic.Bool
}

func (b *stepBound) Cancel() {
	b.tripped.Store(true)
}

func (b *stepBound) Cancelled() bool {
	if b.steps.Add(1) > b.limit || time.Now().After(b.deadline) || b.ctx.Err() != nil {
		b.tripped.Store(true)
	}
	return b.tripped.Load()
}

// evaluate runs the pack over input, stopped past limit steps or the
// deadline, and returns its results and the steps it counted.
func (p *customerPack) evaluate(ctx context.Context, input ast.Value, limit int64, deadline time.Time) ([]PackResult, int64, *PackFault) {
	bound := &stepBound{ctx: ctx, limit: limit, deadline: deadline}
	type answer struct {
		set rego.ResultSet
		err error
	}
	out, finished := within(ctx, deadline, func() answer {
		set, err := p.query.Eval(ctx, rego.EvalParsedInput(input), rego.EvalExternalCancel(bound))
		return answer{set, err}
	})
	if !finished || bound.tripped.Load() {
		bound.Cancel()
		steps := bound.steps.Load()
		return nil, steps, fault(packBound, 0, fmt.Errorf("stopped at step %d, past %d steps or the deadline", steps, limit))
	}
	steps := bound.steps.Load()
	var problem *PackFault
	switch set := out.set; {
	case errors.As(out.err, &problem):
		return nil, steps, problem
	case out.err != nil:
		return nil, steps, fault(packResult, 0, out.err)
	case len(set) == 0:
		return nil, steps, nil
	case len(set) != 1 || len(set[0].Expressions) != 1:
		return nil, steps, fault(packResult, 0, errors.New("the pack gave more than one answer"))
	}
	results, ok := out.set[0].Expressions[0].Value.([]PackResult)
	if !ok {
		return nil, steps, fault(packResult, 0, errors.New("the pack's answer was not decoded"))
	}
	return results, steps, nil
}

// decodeResults reads the pack's answer straight from its terms. It reads
// only the shape a result takes, so no part of the answer outside it is ever
// walked or copied: a set or an array of at most 32 objects, each with a rule,
// an outcome of warn or fail, a reason and an optional message, every value a
// string. Anything else is a fault, never a result left out.
func decodeResults(term *ast.Term, _ *rego.EvalContext) (any, error) {
	var elements []*ast.Term
	switch value := term.Value.(type) {
	case ast.Set:
		if value.Len() > maxPackResults {
			return nil, fault(packBound, 0, fmt.Errorf("the pack returned %d results, over %d", value.Len(), maxPackResults))
		}
		elements = value.Slice()
	case *ast.Array:
		if value.Len() > maxPackResults {
			return nil, fault(packBound, 0, fmt.Errorf("the pack returned %d results, over %d", value.Len(), maxPackResults))
		}
		for index := range value.Len() {
			elements = append(elements, value.Elem(index))
		}
	default:
		return nil, fault(packResult, 0, fmt.Errorf("results is a %s, not a set of results", ast.ValueName(term.Value)))
	}
	results := make([]PackResult, 0, len(elements))
	for _, element := range elements {
		result, err := decodeResult(element)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	slices.SortFunc(results, comparePackResults)
	return results, nil
}

func decodeResult(term *ast.Term) (PackResult, *PackFault) {
	object, ok := term.Value.(ast.Object)
	if !ok {
		return PackResult{}, fault(packResult, 0, fmt.Errorf("a result is a %s, not an object", ast.ValueName(term.Value)))
	}
	if object.Len() > 4 {
		return PackResult{}, fault(packResult, 0, fmt.Errorf("a result holds %d keys", object.Len()))
	}
	fields := map[string]string{}
	for _, key := range object.Keys() {
		name, ok := key.Value.(ast.String)
		if !ok || !slices.Contains([]string{"rule", "outcome", "reason", "message"}, string(name)) {
			return PackResult{}, fault(packResult, 0, fmt.Errorf("a result holds the key %v", key))
		}
		text, ok := object.Get(key).Value.(ast.String)
		if !ok {
			return PackResult{}, fault(packResult, 0, fmt.Errorf("a result's %s is not a string", name))
		}
		fields[string(name)] = string(text)
	}
	result := PackResult{Rule: fields["rule"], Outcome: PackOutcome(fields["outcome"]), Reason: fields["reason"], Message: printable(fields["message"])}
	switch {
	case result.Outcome != PackWarn && result.Outcome != PackFail:
		return PackResult{}, fault(packResult, 0, fmt.Errorf("a result's outcome is %q, not warn or fail", fields["outcome"]))
	case !idPattern.MatchString(result.Rule) || !idPattern.MatchString(result.Reason):
		return PackResult{}, fault(packResult, 0, fmt.Errorf("a result's rule %q or reason %q is not an id", result.Rule, result.Reason))
	case slices.Contains(reservedRules, result.Rule):
		return PackResult{}, fault(packResult, 0, fmt.Errorf("a result names Costroid's rule %s", result.Rule))
	}
	return result, nil
}

// printable keeps the characters a comment prints in a code span, replaces
// each other character with ?, and cuts the message at 160 bytes.
func printable(message string) string {
	var out strings.Builder
	for _, char := range message {
		if out.Len() == maxMessageBytes {
			break
		}
		if !strings.ContainsRune(messageCharacters, char) {
			char = '?'
		}
		out.WriteRune(char)
	}
	return out.String()
}

const messageCharacters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 .,:;()'=_/+?-"

func comparePackResults(a, b PackResult) int {
	return strings.Compare(
		strings.Join([]string{a.Rule, a.Reason, string(a.Outcome), a.Message}, "\x00"),
		strings.Join([]string{b.Rule, b.Reason, string(b.Outcome), b.Message}, "\x00"),
	)
}
