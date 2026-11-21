package policy

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

const (
	// PackChildArgument is the one argument a pack's child process starts
	// with. It is no command: costroid's main hands the process to PackChild
	// before it reads any.
	PackChildArgument = "__costroid_pack_child"

	customerPackage = "costroid.pack"
	customerQuery   = "data.costroid.pack.results"
	// maxRequestBytes bounds what the child reads: the pack, and an input of
	// 256 model ids of at most 128 bytes each and the settings beside them.
	maxRequestBytes = 8 << 20
	// maxChildStack ends a goroutine whose stack passes 64 MiB. OPA unifies
	// two values through continuations, so its stack grows with their size.
	maxChildStack = 64 << 20
	// maxChildMemory is the child's heap, as RLIMIT_DATA, which Linux applies
	// to every private writable mapping since 4.7.
	maxChildMemory = 1 << 30
	// maxChildSeconds is the CPU time after which the kernel ends the child,
	// for a child whose parent died before it could kill it.
	maxChildSeconds = 5
	// maxChildFiles is how many descriptors the child may hold: the three
	// pipes and the runtime's own.
	maxChildFiles = 32
)

// packCapabilitiesFile lists every built-in a customer pack may call: the
// default pack's seven, the four other comparisons, count, three string
// tests, and the two-variable form of some ... in. Each is pure and returns
// nothing larger than its input. There is no network, clock, randomness,
// file, environment or arithmetic.
//
//go:embed capabilities.pack.json
var packCapabilitiesFile []byte

// diverting says this process called PackChild as it started, so a child
// started from its executable runs the pack and nothing else.
var diverting atomic.Bool

// PackChild runs this process as a customer pack's child when it was started
// as one, and reports whether it was. Every executable that runs a pack calls
// it first, in main or in TestMain: in any other process it records that the
// executable hands such a child to the pack, and RunCustomerPack starts no
// child from an executable that does not. The child limits itself, reads one
// request on stdin, compiles and evaluates the pack, and writes one answer on
// stdout. The code is the process's exit code.
func PackChild() (int, bool) {
	if len(os.Args) != 2 || os.Args[1] != PackChildArgument {
		diverting.Store(true)
		return 0, false
	}
	if err := limitChild(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	if err := serveChild(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	return 0, true
}

// limitChild bounds the process's stack, heap, CPU time, files and
// descriptors. The parent's kill at the deadline bounds it whatever these
// do.
func limitChild() error {
	debug.SetMaxStack(maxChildStack)
	debug.SetMemoryLimit(maxChildMemory / 2)
	limits := []struct {
		name     string
		resource int
		limit    uint64
	}{
		{"RLIMIT_CPU", syscall.RLIMIT_CPU, maxChildSeconds},
		{"RLIMIT_DATA", syscall.RLIMIT_DATA, maxChildMemory},
		{"RLIMIT_FSIZE", syscall.RLIMIT_FSIZE, 0},
		{"RLIMIT_CORE", syscall.RLIMIT_CORE, 0},
		{"RLIMIT_NOFILE", syscall.RLIMIT_NOFILE, maxChildFiles},
	}
	for _, entry := range limits {
		limit := syscall.Rlimit{Cur: entry.limit, Max: entry.limit}
		if entry.resource == syscall.RLIMIT_CPU {
			limit.Max++
		}
		if err := syscall.Setrlimit(entry.resource, &limit); err != nil {
			return fmt.Errorf("policy: set %s: %w", entry.name, err)
		}
	}
	return nil
}

func serveChild(in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(in, maxRequestBytes+1))
	if err != nil {
		return fmt.Errorf("policy: read the request: %w", err)
	}
	if len(data) > maxRequestBytes {
		return fmt.Errorf("policy: the request is over %d bytes", maxRequestBytes)
	}
	var request childRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return fmt.Errorf("policy: read the request: %w", err)
	}
	if err := json.NewEncoder(out).Encode(answerPack(request)); err != nil {
		return fmt.Errorf("policy: write the answer: %w", err)
	}
	return nil
}

// answerPack compiles and evaluates one pack. It runs only in the child.
func answerPack(request childRequest) childAnswer {
	pack, problem := compileCustomerPack(request.Source)
	if problem != nil {
		return faultAnswer(0, problem)
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Input))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return faultAnswer(0, fault(ReasonPackBound, 0, fmt.Errorf("read the input: %w", err)))
	}
	input, err := ast.InterfaceToValue(document)
	if err != nil {
		return faultAnswer(0, fault(ReasonPackBound, 0, fmt.Errorf("read the input: %w", err)))
	}
	steps := request.Steps
	if steps < 1 || steps > maxPackSteps {
		steps = maxPackSteps
	}
	results, counted, problem := pack.evaluate(input, steps)
	if problem != nil {
		return faultAnswer(counted, problem)
	}
	return childAnswer{Steps: counted, Results: results}
}

func faultAnswer(steps int64, problem *PackFault) childAnswer {
	return childAnswer{Steps: steps, Fault: &childFault{Reason: problem.reason, Line: problem.Line, Error: problem.err.Error()}}
}

// customerPack is a compiled customer pack.
type customerPack struct {
	query rego.PreparedEvalQuery
}

// compileCustomerPack compiles one module alone under capabilities.pack.json.
// It never sees the default pack, so no rule of the module can join a
// document of Costroid's.
func compileCustomerPack(source []byte) (*customerPack, *PackFault) {
	capabilities, err := ast.LoadCapabilitiesJSON(bytes.NewReader(packCapabilitiesFile))
	if err != nil {
		return nil, fault(ReasonPackInvalid, 0, fmt.Errorf("read the capabilities: %w", err))
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
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, compileFault(err)
	}
	return &customerPack{query: prepared}, nil
}

// checkModule refuses a module whose package is not costroid.pack, and one
// that writes a result for a rule of Costroid's.
func checkModule(module *ast.Module) *PackFault {
	if path := module.Package.Path.String(); path != "data."+customerPackage {
		return fault(ReasonPackInvalid, row(module.Package.Location), fmt.Errorf("the package is %s, not %s", strings.TrimPrefix(path, "data."), customerPackage))
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
			problem = fault(ReasonPackInvalid, row(term.Location), fmt.Errorf("a result names Costroid's rule %s", rule))
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
		return fault(ReasonPackInvalid, row(errs[0].Location), err)
	}
	var single *ast.Error
	if errors.As(err, &single) {
		return fault(ReasonPackInvalid, row(single.Location), err)
	}
	return fault(ReasonPackInvalid, 0, err)
}

func row(location *ast.Location) int {
	if location == nil {
		return 0
	}
	return location.Row
}

// stepBound counts the expressions an evaluation begins, and cancels it
// past its limit. OPA asks Cancelled before each expression.
type stepBound struct {
	limit   int64
	steps   atomic.Int64
	tripped atomic.Bool
}

func (b *stepBound) Cancel() {
	b.tripped.Store(true)
}

func (b *stepBound) Cancelled() bool {
	if b.steps.Add(1) > b.limit {
		b.tripped.Store(true)
	}
	return b.tripped.Load()
}

// evaluate runs the pack over input, stopped past limit steps, and returns
// its results and the steps it counted.
func (p *customerPack) evaluate(input ast.Value, limit int64) ([]PackResult, int64, *PackFault) {
	bound := &stepBound{limit: limit}
	set, err := p.query.Eval(context.Background(), rego.EvalParsedInput(input), rego.EvalExternalCancel(bound))
	steps := bound.steps.Load()
	var problem *PackFault
	switch {
	case bound.tripped.Load():
		return nil, steps, fault(ReasonPackBound, 0, fmt.Errorf("stopped at step %d, past %d steps", steps, limit))
	case errors.As(err, &problem):
		return nil, steps, problem
	case err != nil:
		return nil, steps, fault(ReasonPackResult, 0, err)
	case len(set) == 0:
		return nil, steps, nil
	case len(set) != 1 || len(set[0].Expressions) != 1:
		return nil, steps, fault(ReasonPackResult, 0, errors.New("the pack gave more than one answer"))
	}
	results, ok := set[0].Expressions[0].Value.([]PackResult)
	if !ok {
		return nil, steps, fault(ReasonPackResult, 0, errors.New("the pack's answer was not decoded"))
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
			return nil, fault(ReasonPackBound, 0, fmt.Errorf("the pack returned %d results, over %d", value.Len(), maxPackResults))
		}
		elements = value.Slice()
	case *ast.Array:
		if value.Len() > maxPackResults {
			return nil, fault(ReasonPackBound, 0, fmt.Errorf("the pack returned %d results, over %d", value.Len(), maxPackResults))
		}
		for index := range value.Len() {
			elements = append(elements, value.Elem(index))
		}
	default:
		return nil, fault(ReasonPackResult, 0, fmt.Errorf("results is a %s, not a set of results", ast.ValueName(term.Value)))
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
		return PackResult{}, fault(ReasonPackResult, 0, fmt.Errorf("a result is a %s, not an object", ast.ValueName(term.Value)))
	}
	if object.Len() > 4 {
		return PackResult{}, fault(ReasonPackResult, 0, fmt.Errorf("a result holds %d keys", object.Len()))
	}
	fields := map[string]string{}
	for _, key := range object.Keys() {
		name, ok := key.Value.(ast.String)
		if !ok || !slices.Contains([]string{"rule", "outcome", "reason", "message"}, string(name)) {
			return PackResult{}, fault(ReasonPackResult, 0, fmt.Errorf("a result holds the key %v", key))
		}
		text, ok := object.Get(key).Value.(ast.String)
		if !ok {
			return PackResult{}, fault(ReasonPackResult, 0, fmt.Errorf("a result's %s is not a string", name))
		}
		fields[string(name)] = string(text)
	}
	result := PackResult{Rule: fields["rule"], Outcome: PackOutcome(fields["outcome"]), Reason: fields["reason"], Message: printable(fields["message"])}
	return result, checkResult(result)
}
