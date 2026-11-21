package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"

	"github.com/Costroid/costroid/internal/decimal"
)

// messagePattern is a pack result's message as the result contract states
// it, or none.
var messagePattern = regexp.MustCompile(`^[A-Za-z0-9 .,:;()'=_/+?-]{0,160}$`)

// packFixtures are packs a team could keep, each over every part of the
// input.
func packFixtures(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "packs", "*.rego"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no packs in testdata/packs: %v", err)
	}
	packs := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		packs[filepath.Base(path)] = data
	}
	return packs
}

// largestRequest is a request at the input's bounds: 16 currencies of 16
// models each, every setting set, a baseline and a pull request's total.
func largestRequest(t testing.TB) Request {
	t.Helper()
	codes := []string{"USD", "EUR", "GBP", "JPY", "CHF", "CAD", "AUD", "NZD", "SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "SGD", "HKD"}
	usage := Usage{}
	var models []string
	for index, code := range codes {
		entry := CurrencyUsage{Currency: code, ListCost: decimal.MustParse("21.5"), Models: map[string]int{}}
		for rank := range maxPackModels / len(codes) {
			model := fmt.Sprintf("claude-sonnet-%d-%d", index, rank)
			if rank == 0 {
				model = fmt.Sprintf("gpt-%d-preview", index)
			}
			entry.Models[model] = rank + 1
			entry.Rows += rank + 1
			models = append(models, model)
		}
		entry.UnattributedRows, entry.JoinableRows = entry.Rows, 1
		usage.Rows += entry.Rows
		usage.Currencies = append(usage.Currencies, entry)
	}
	config := "policy:\n  run_budget_usd: \"20\"\n  max_run_cost_usd: 30\n  max_increase_usd: 5\n  max_increase_percent: 50\n" +
		"  pr_budget_usd: 100\n  monthly_budget_usd: 1000\n  models: [" + strings.Join(models, ", ") + "]\n  enforce: true\n"
	return Request{
		Usage: usage, Trust: Unverified, At: checkTime,
		Config:   ConfigFile{Data: []byte(config), Found: true, FromBase: true, Ref: base},
		Baseline: &Baseline{Runs: 20, MedianUSD: decimal.MustParse("10"), P95USD: decimal.MustParse("18"), Trust: TokenVerified},
		Lifetime: &Lifetime{USD: decimal.MustParse("90"), Trust: Unverified},
	}
}

// baseCase is a verdict of Costroid's and the request it was computed from.
type baseCase struct {
	name    string
	request Request
	verdict Verdict
}

// baseCases are Costroid's verdicts on each trust path, of each conclusion,
// in the warn-only period and after it.
func baseCases(t testing.TB) []baseCase {
	t.Helper()
	within, over := "policy:\n  run_budget_usd: \"5\"\n", "policy:\n  run_budget_usd: \"1\"\n"
	enforced := "  enforce: true\n"
	checked := func(cost string) Usage {
		return Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse(cost), Rows: 1, Models: map[string]int{"claude-sonnet-5-5": 1}}}}
	}
	claimed := Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse("3"), Rows: 1, UnattributedRows: 1, Models: map[string]int{"claude-sonnet-5-5": 1}}}}
	requests := []struct {
		name    string
		request Request
	}{
		{"rows the job wrote, within", Request{Usage: claimed, Config: config(within + enforced)}},
		{"rows the job wrote, over", Request{Usage: claimed, Config: config(over + enforced)}},
		{"rows the job wrote, over, warn-only", Request{Usage: claimed, Config: config(over)}},
		{"measured, within", Request{Usage: checked("3"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: config(within + enforced)}},
		{"measured, within, warn-only", Request{Usage: checked("3"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: config(within)}},
		{"measured, over", Request{Usage: checked("3"), Trust: TokenVerified, Evidence: GatewayMeasured, Config: config(over + enforced)}},
		{"agent counts, within", Request{Usage: checked("3"), Trust: TokenVerified, Config: config(within + enforced)}},
		{"a model off the list", Request{Usage: checked("3"), Trust: TokenVerified, Evidence: Reconciled, Config: config("policy:\n  models: [codex]\n" + enforced)}},
		{"no costroid.yml", Request{Usage: claimed}},
		{"no rows", Request{Config: config(within + enforced)}},
	}
	var cases []baseCase
	seen := map[string]bool{}
	for _, entry := range requests {
		entry.request.At = checkTime
		verdict, err := Evaluate(context.Background(), entry.request)
		if err != nil {
			t.Fatal(err)
		}
		seen[fmt.Sprintf("%s %t", verdict.Conclusion, verdict.WarnOnly.Active)] = true
		cases = append(cases, baseCase{entry.name, entry.request, verdict})
	}
	for _, conclusion := range []string{Success, Neutral, Failure} {
		if !seen[conclusion+" false"] || (conclusion != Failure && !seen[conclusion+" true"]) {
			t.Fatalf("no base verdict concludes %s on both sides of the warn-only period: %v", conclusion, seen)
		}
	}
	return cases
}

// assertComposedPack holds a pack's run to what it may add: the conclusion
// is never below Costroid's, Costroid's results are as they were, a fault is
// one customer_pack result in the table and leaves no pack result, and each
// pack result is in the pack's words.
func assertComposedPack(t *testing.T, name string, base Verdict, out Composed) {
	t.Helper()
	if severity[out.Verdict.Conclusion] < severity[base.Conclusion] {
		t.Fatalf("%s: Costroid concluded %s and the pack made it %s", name, base.Conclusion, out.Verdict.Conclusion)
	}
	added := out.Verdict.Results[min(len(base.Results), len(out.Verdict.Results)):]
	if !reflect.DeepEqual(out.Verdict.Results[:len(base.Results)], base.Results) || len(added) > 1 {
		t.Fatalf("%s: Costroid's results became %+v", name, out.Verdict.Results)
	}
	if len(added) == 1 {
		outcome, known := packFaultOutcomes[added[0].Reason]
		if outcome == Fail && base.WarnOnly.Active {
			outcome = Warn
		}
		if !known || added[0].Rule != ruleCustomerPack || added[0].Outcome != outcome || len(out.Pack) > 0 {
			t.Fatalf("%s: a fault became %+v beside %+v", name, added[0], out.Pack)
		}
	}
	if len(out.Pack) > maxPackResults {
		t.Fatalf("%s: %d pack results", name, len(out.Pack))
	}
	for _, result := range out.Pack {
		words := result.Outcome == PackWarn || (result.Outcome == PackFail && !base.WarnOnly.Active)
		if !words || !idPattern.MatchString(result.Rule) || !idPattern.MatchString(result.Reason) ||
			slices.Contains(reservedRules, result.Rule) || !messagePattern.MatchString(result.Message) ||
			(result.HeldByWarnOnly && !base.WarnOnly.Active) {
			t.Fatalf("%s: a pack result outside the pack's words: %+v", name, result)
		}
	}
}

// customerResult is Costroid's customer_pack result, if the run added one.
func customerResult(base Verdict, out Composed) (Result, bool) {
	if len(out.Verdict.Results) == len(base.Results)+1 {
		return out.Verdict.Results[len(base.Results)], true
	}
	return Result{}, false
}

// answerLiterals are results a pack can state. Only the first two and the
// last are in the pack's words.
var answerLiterals = []struct {
	literal string
	valid   bool
}{
	{`{"rule": "team_rule", "outcome": "warn", "reason": "over_team_limit"}`, true},
	{`{"rule": "team_rule", "outcome": "fail", "reason": "over_team_limit", "message": "Over the team's limit: 20 EUR"}`, true},
	{`{"rule": "team_rule", "outcome": "pass", "reason": "within_team_limit"}`, false},
	{`{"rule": "team_rule", "outcome": "success", "reason": "within_team_limit"}`, false},
	{`{"rule": "team_rule", "outcome": "unproven", "reason": "no_rows"}`, false},
	{`{"rule": "team_rule", "outcome": "PASS", "reason": "within_team_limit"}`, false},
	{`{"rule": "team_rule", "outcome": "warn", "reason": "over_team_limit", "held_by_warn_only": true}`, false},
	{`{"rule": "team_rule", "outcome": "warn", "reason": 7}`, false},
	{`{"rule": "Team rule", "outcome": "warn", "reason": "over_team_limit"}`, false},
	{`{"rule": "team_rule", "outcome": "fail"}`, false},
	{`"pass"`, false},
	{`{"rule": "team_rule", "outcome": "warn", "reason": "odd_message", "message": "naïve <b>bold</b> ✓"}`, true},
}

// generatedAnswers are packs that state every set of up to two of
// answerLiterals, and whether every result in the set is in the pack's words.
func generatedAnswers() map[string]bool {
	answers := map[string]bool{}
	for _, set := range multisets(answerLiterals, 2) {
		var source strings.Builder
		source.WriteString("package costroid.pack\n\nresults := set()\n")
		valid := true
		if len(set) > 0 {
			source.Reset()
			source.WriteString("package costroid.pack\n\n")
		}
		for _, entry := range set {
			fmt.Fprintf(&source, "results contains %s\n", entry.literal)
			valid = valid && entry.valid
		}
		answers[source.String()] = valid
	}
	return answers
}

// packAttempts are packs that try to reach Costroid's verdict another way.
var packAttempts = []string{
	"package costroid\n\nverdict := {\"conclusion\": \"success\", \"results\": []}\n",
	"package costroid\n\nresults contains {\"rule\": \"run_budget\", \"outcome\": \"pass\", \"reason\": \"within_limit\"}\n",
	"package costroid.pack\n\nconclusion := \"success\"\n\nverdict := {\"conclusion\": \"success\", \"results\": []}\n",
	"package costroid.pack\n\nresults := \"success\"\n",
	"package costroid.pack\n\nresults := {\"conclusion\": \"success\"}\n",
	"package costroid.pack\n\nresults := [{\"rule\": \"team_rule\", \"outcome\": \"pass\", \"reason\": \"within_team_limit\"}]\n",
	"package costroid.pack\n\nresults contains {\"rule\": \"run_budget\", \"outcome\": \"warn\", \"reason\": \"within_limit\"}\n",
	"package costroid.pack\n\nresults contains {\"rule\": rule, \"outcome\": \"warn\", \"reason\": \"echo\"} if {\n\tsome result in input.results\n\trule := result.rule\n}\n",
	"package costroid.pack\n\nresults contains {\"rule\": sprintf(\"r%d\", [n]), \"outcome\": \"warn\", \"reason\": \"many\"} if {\n\tsome n in numbers.range(1, 40)\n}\n",
	"package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"undefined\"} if {\n\tinput.nothing\n}\n",
}

func TestCustomerPackCannotLoosenTheDefault(t *testing.T) {
	answers := generatedAnswers()
	if len(answers) != 91 {
		t.Fatalf("generated %d answers", len(answers))
	}
	for _, entry := range baseCases(t) {
		kept := slices.Clone(entry.verdict.Results)
		for source, valid := range answers {
			name := fmt.Sprintf("%s, %q", entry.name, source)
			out := RunCustomerPack(context.Background(), []byte(source), entry.request, entry.verdict)
			assertComposedPack(t, name, entry.verdict, out)
			result, faulted := customerResult(entry.verdict, out)
			switch {
			case valid && faulted:
				t.Fatalf("%s: an answer in the pack's words faulted: %+v", name, result)
			case !valid && (!faulted || result.Reason != packResult):
				t.Fatalf("%s: an answer outside the pack's words was read: %+v, %+v", name, out.Pack, out.Verdict.Results)
			}
		}
		for _, source := range packAttempts {
			name := fmt.Sprintf("%s, %q", entry.name, source)
			out := RunCustomerPack(context.Background(), []byte(source), entry.request, entry.verdict)
			assertComposedPack(t, name, entry.verdict, out)
			if slices.ContainsFunc(out.Pack, func(result PackResult) bool { return slices.Contains(reservedRules, result.Rule) }) {
				t.Fatalf("%s: a pack result took a rule of Costroid's: %+v", name, out.Pack)
			}
		}
		if !reflect.DeepEqual(entry.verdict.Results, kept) {
			t.Fatalf("%s: a pack wrote Costroid's results", entry.name)
		}
	}
}

// refusedCalls are built-ins capabilities.pack.json leaves out, each called
// on line 4 of a pack.
var refusedCalls = map[string]string{
	"http.send":            `http.send({"method": "GET", "url": "https://example.com"})`,
	"net.lookup_ip_addr":   `net.lookup_ip_addr("example.com")`,
	"net.cidr_contains":    `net.cidr_contains("10.0.0.0/8", "10.0.0.1")`,
	"opa.runtime":          `opa.runtime()`,
	"time.now_ns":          `time.now_ns()`,
	"rand.intn":            `rand.intn("seed", 10)`,
	"uuid.rfc4122":         `uuid.rfc4122("seed")`,
	"io.jwt.decode_verify": `io.jwt.decode_verify("a.b.c", {"cert": ""})`,
	"crypto.sha256":        `crypto.sha256("x")`,
	"trace":                `trace("x")`,
	"sprintf":              `sprintf("%d", [1])`,
	"concat":               `concat(",", ["a", "b"])`,
	"json.marshal":         `json.marshal(input)`,
	"numbers.range":        `numbers.range(1, 10)`,
	"walk":                 `[x | walk(input, x)]`,
	"regex.match":          `regex.match("a+", "aaa")`,
	"object.get":           `object.get(input, "trust", "")`,
	"plus":                 `input.rows + 1`,
	"minus":                `input.rows - 1`,
	"mul":                  `input.rows * 2`,
	"div":                  `input.rows / 2`,
	"rem":                  `input.rows % 2`,
	"units.parse_bytes":    `units.parse_bytes("1KB")`,
}

// packFaults are modules the loader refuses besides a refused built-in, each
// with the line it names.
var packFaults = []struct {
	name, source string
	line         int
}{
	{"another package", "package costroid\n\nresults := set()\n", 1},
	{"a nested package", "package costroid.pack.more\n\nresults := set()\n", 1},
	{"a second package", "package costroid.pack\n\nresults := set()\n\npackage costroid\n", 5},
	{"a result for a rule of Costroid's", "package costroid.pack\n\nresults contains {\"rule\": \"pr_budget\", \"outcome\": \"warn\", \"reason\": \"x\"} if {\n\tinput.rows > 0\n}\n", 3},
	{"a result for Costroid's own pack rule", "package costroid.pack\n\nx := 1\n\nresults contains {\"rule\": \"customer_pack\", \"outcome\": \"fail\", \"reason\": \"x\"}\n", 5},
	{"a module that does not parse", "package costroid.pack\n\nresults contains {\n\n\tinput.rows >\n", 6},
	{"a module that does not compile", "package costroid.pack\n\nresults contains \"x\" if {\n\tinput.rows > y\n}\n", 4},
	{"recursion", "package costroid.pack\n\nresults contains x if {\n\tsome x in results\n}\n", 3},
	{"no package", "results := set()\n", 1},
}

func TestCustomerPackWithARefusedBuiltinFailsClosed(t *testing.T) {
	capabilities, err := ast.LoadCapabilitiesJSON(bytes.NewReader(packCapabilitiesFile))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, builtin := range capabilities.Builtins {
		known, found := ast.BuiltinMap[builtin.Name]
		if !found || known.Nondeterministic {
			t.Errorf("%s is not a pure built-in of the pinned OPA", builtin.Name)
		}
		names = append(names, builtin.Name)
	}
	admitted := []string{"contains", "count", "endswith", "eq", "equal", "gt", "gte", "internal.member_2", "internal.member_3", "lt", "lte", "neq", "object.union", "startswith", "sum"}
	if !slices.Equal(names, admitted) || capabilities.AllowNet == nil || len(capabilities.AllowNet) != 0 {
		t.Fatalf("capabilities.pack.json admits %q, allow_net %v", names, capabilities.AllowNet)
	}
	cases := baseCases(t)
	faults := slices.Clone(packFaults)
	for name, call := range refusedCalls {
		if slices.Contains(names, name) {
			t.Fatalf("%s is admitted", name)
		}
		source := "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"} if {\n\t_ := " + call + "\n}\n"
		faults = append(faults, struct {
			name, source string
			line         int
		}{name, source, 4})
	}
	for _, entry := range faults {
		_, problem := loadCustomerPack(context.Background(), []byte(entry.source), time.Now().Add(maxPackTime))
		if problem == nil || problem.Reason() != packInvalid || problem.Line != entry.line {
			t.Errorf("%s loaded as %v, line %d", entry.name, problem, entry.line)
			continue
		}
		for _, base := range cases {
			out := RunCustomerPack(context.Background(), []byte(entry.source), base.request, base.verdict)
			assertComposedPack(t, entry.name+", "+base.name, base.verdict, out)
			result, faulted := customerResult(base.verdict, out)
			want := max(severity[base.verdict.Conclusion], severity[Neutral])
			if !faulted || result.Outcome != Unproven || result.Reason != packInvalid || out.Line != entry.line || severity[out.Verdict.Conclusion] != want {
				t.Errorf("%s over %s: %s, %+v, line %d", entry.name, base.name, out.Verdict.Conclusion, result, out.Line)
			}
		}
	}
}

// chain is a pack's rules that build a value whose size doubles with each
// rule, through object.union so the type checker sees no nesting.
func chain(name string, rules int) string {
	var source strings.Builder
	fmt.Fprintf(&source, "%s_0 := {\"rows\": input.rows}\n", name)
	for index := 1; index <= rules; index++ {
		fmt.Fprintf(&source, "%s_%d := object.union({\"left\": %s_%d}, {\"right\": %s_%d})\n", name, index, name, index-1, name, index-1)
	}
	return source.String()
}

// nested is a pack's rules that build a value whose static type doubles
// with each rule, so the type checker's work does.
func nested(name string, rules int) string {
	var source strings.Builder
	fmt.Fprintf(&source, "%s_0 := [1]\n", name)
	for index := 1; index <= rules; index++ {
		fmt.Fprintf(&source, "%s_%d := [%s_%d, %s_%d]\n", name, index, name, index-1, name, index-1)
	}
	return source.String()
}

func TestCustomerPackIsBounded(t *testing.T) {
	const header = "package costroid.pack\n\n"
	const warn = `results contains {"rule": "team_rule", "outcome": "warn", "reason": "reached"}`
	largest := largestRequest(t)
	wider := largestRequest(t)
	wider.Usage.Currencies = append(wider.Usage.Currencies, CurrencyUsage{Currency: "ZAR", ListCost: decimal.MustParse("1"), Rows: 1, UnattributedRows: 1})
	more := largestRequest(t)
	more.Usage.Currencies[0].Models["claude-one-more"] = 1
	var many strings.Builder
	many.WriteString(header)
	for index := range maxPackResults + 1 {
		fmt.Fprintf(&many, "results contains {\"rule\": \"rule_%d\", \"outcome\": \"warn\", \"reason\": \"many\"}\n", index)
	}
	quick := packBounds{steps: maxPackSteps, time: 100 * time.Millisecond}
	cases := []struct {
		name    string
		source  string
		request Request
		bounds  packBounds
		reason  string
	}{
		{"five nested loops over the input", header + warn + " if {\n\tn := count([1 | some a in input.usage; some b in input.usage; some c in input.usage; some d in input.usage; some e in input.usage])\n\tn > 0\n}\n", largest, customerBounds, packBound},
		{"a comparison of two values of 2^24 leaves in one step", header + chain("a", 24) + chain("b", 24) + warn + " if {\n\ta_24 in [b_24]\n}\n", largest, quick, packBound},
		{"a type check of 4^13 steps in the compiler", header + nested("a", 13) + nested("b", 13) + warn + " if {\n\ta_13 == b_13\n}\n", largest, quick, packBound},
		{"17 currencies", header + warn + "\n", wider, customerBounds, packBound},
		{"257 model ids", header + warn + "\n", more, customerBounds, packBound},
		{"33 results", many.String(), largest, customerBounds, packBound},
		{"a file over 64 KiB", header + warn + "\n" + strings.Repeat("#", maxPackBytes), largest, customerBounds, packUnread},
	}
	for _, entry := range cases {
		start := time.Now()
		results, problem := runCustomerPack(context.Background(), []byte(entry.source), entry.request, nil, entry.bounds)
		elapsed := time.Since(start)
		if problem == nil || problem.Reason() != entry.reason || len(results) > 0 {
			t.Errorf("%s: %+v, %v", entry.name, results, problem)
			continue
		}
		if elapsed > entry.bounds.time+500*time.Millisecond {
			t.Errorf("%s: answered after %s, past %s", entry.name, elapsed, entry.bounds.time)
		}
		t.Logf("%s: %v after %s", entry.name, problem, elapsed)
		for _, active := range []bool{false, true} {
			base := Verdict{Conclusion: Success, WarnOnly: WarnOnly{Active: active}, Results: []Result{}}
			out := compose(base, results, problem)
			want := Failure
			if active || entry.reason == packUnread {
				want = Neutral
			}
			if out.Verdict.Conclusion != want {
				t.Errorf("%s, warn-only %t: concluded %s", entry.name, active, out.Verdict.Conclusion)
			}
		}
	}
	pack, problem := loadCustomerPack(context.Background(), []byte(cases[0].source), time.Now().Add(maxPackTime))
	if problem != nil {
		t.Fatal(problem)
	}
	input, problem := customerInput(largest, Settings{}, nil)
	if problem != nil {
		t.Fatal(problem)
	}
	if _, steps, problem := pack.evaluate(context.Background(), input, maxPackSteps, time.Now().Add(time.Minute)); problem == nil || steps != maxPackSteps+1 {
		t.Errorf("the loops stopped at step %d of a bound of %d: %v", steps, maxPackSteps, problem)
	}
}

func TestCustomerPackCountsTheSameStepsEveryRun(t *testing.T) {
	request := largestRequest(t)
	settings, err := request.Config.settings()
	if err != nil {
		t.Fatal(err)
	}
	input, problem := customerInput(request, settings, evaluate(t, request).Results)
	if problem != nil {
		t.Fatal(problem)
	}
	packs := packFixtures(t)
	packs["over the bound"] = []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"} if {\n\tcount([1 | some a in input.usage; some b in input.usage; some c in input.usage; some d in input.usage; some e in input.usage]) > 0\n}\n")
	for name, source := range packs {
		var counts []int64
		var answers [][]PackResult
		for range 100 {
			pack, problem := loadCustomerPack(context.Background(), source, time.Now().Add(maxPackTime))
			if problem != nil {
				t.Fatalf("%s: %v", name, problem)
			}
			results, steps, problem := pack.evaluate(context.Background(), input, maxPackSteps, time.Now().Add(time.Minute))
			if (problem != nil) != (name == "over the bound") {
				t.Fatalf("%s: %v", name, problem)
			}
			counts, answers = append(counts, steps), append(answers, results)
		}
		if slices.Min(counts) != slices.Max(counts) || slices.ContainsFunc(answers, func(results []PackResult) bool { return !reflect.DeepEqual(results, answers[0]) }) {
			t.Errorf("%s counted from %d to %d steps over 100 runs", name, slices.Min(counts), slices.Max(counts))
		}
		if name != "over the bound" && counts[0]*100 > maxPackSteps {
			t.Errorf("%s counts %d steps, more than a hundredth of the bound of %d", name, counts[0], maxPackSteps)
		}
		t.Logf("%s: %d steps, %d results", name, counts[0], len(answers[0]))
	}
}

func TestCustomerPackSeesOnlyItsInput(t *testing.T) {
	t.Setenv("COSTROID_PACK_CANARY", "a-value-of-the-environment")
	request := Request{
		Usage: Usage{Rows: 3, Currencies: []CurrencyUsage{
			{Currency: "EUR", ListCost: decimal.MustParse("0.5"), Rows: 1, UnattributedRows: 1, Models: map[string]int{"codex-mini": 1}},
			{Currency: "USD", ListCost: decimal.MustParse("2.25"), Rows: 2, UnattributedRows: 2, JoinableRows: 1, Models: map[string]int{"claude-sonnet-5-5": 2}, UnreadableModels: 0},
		}},
		Trust:                Unverified,
		Config:               ConfigFile{Data: []byte("policy:\n  run_budget_usd: \"5\"\n  models: [claude-*]\n"), Found: true, FromBase: true, Ref: base},
		Start:                Start{At: checkTime.Add(-time.Hour)},
		At:                   checkTime,
		Baseline:             &Baseline{Runs: 12, MedianUSD: decimal.MustParse("1"), P95USD: decimal.MustParse("4"), Trust: TokenVerified},
		Lifetime:             &Lifetime{USD: decimal.MustParse("7"), MayRepeat: true, Trust: Unverified},
		MeasuredPullRequests: 3,
		Evidence:             GatewayMeasured,
	}
	verdict := evaluate(t, request)
	settings, err := request.Config.settings()
	if err != nil {
		t.Fatal(err)
	}
	input, problem := customerInput(request, settings, verdict.Results)
	if problem != nil {
		t.Fatal(problem)
	}
	want := `{"baseline":{"p50_usd":"1","p95_usd":"4","runs":12,"trust":"token_verified"},` +
		`"lifetime":{"may_repeat":true,"other_currency":false,"total_usd":"9.25","trust":"unverified"},` +
		`"results":[{"outcome":"unproven","reason":"unverified_rows","rule":"run_budget"},{"outcome":"warn","reason":"model_outside_allowlist","rule":"model_allowlist"},{"outcome":"warn","reason":"joinable_rows","rule":"attribution"}],` +
		`"rows":3,"settings":{"models":["claude-*"],"run_budget_usd":"5"},"trust":"unverified",` +
		`"usage":[{"currency":"EUR","joinable_rows":0,"list_cost":"0.5","models":{"codex-mini":1},"rows":1,"unattributed_rows":1},` +
		`{"currency":"USD","joinable_rows":1,"list_cost":"2.25","models":{"claude-sonnet-5-5":2},"rows":2,"unattributed_rows":2}]}`
	document, err := ast.JSON(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Errorf("the pack's input is\n%s\nnot\n%s", encoded, want)
	}
	later := request
	later.At, later.Start, later.MeasuredPullRequests = checkTime.Add(240*time.Hour), Start{NotOnBase: true}, 40
	if again, _ := customerInput(later, settings, verdict.Results); ast.Compare(again, input) != 0 {
		t.Errorf("the input moved with the clock or the warn-only period: %v", again)
	}

	echo := `package costroid.pack

results contains {"rule": "input", "outcome": "warn", "reason": key} if some key, _ in input

results contains {"rule": "usage", "outcome": "warn", "reason": key} if {
	some usage in input.usage
	some key, _ in usage
}

results contains {"rule": "baseline", "outcome": "warn", "reason": key} if some key, _ in input.baseline

results contains {"rule": "lifetime", "outcome": "warn", "reason": key} if some key, _ in input.lifetime

results contains {"rule": "results", "outcome": "warn", "reason": key} if {
	some result in input.results
	some key, _ in result
}

results contains {"rule": "settings", "outcome": "warn", "reason": key} if some key, _ in input.settings
`
	out := RunCustomerPack(context.Background(), []byte(echo), request, verdict)
	keys := map[string][]string{}
	for _, result := range out.Pack {
		keys[result.Rule] = append(keys[result.Rule], result.Reason)
	}
	wantKeys := map[string][]string{
		"input":    {"baseline", "lifetime", "results", "rows", "settings", "trust", "usage"},
		"usage":    {"currency", "joinable_rows", "list_cost", "models", "rows", "unattributed_rows"},
		"baseline": {"p50_usd", "p95_usd", "runs", "trust"},
		"lifetime": {"may_repeat", "other_currency", "total_usd", "trust"},
		"results":  {"outcome", "reason", "rule"},
		"settings": {"models", "run_budget_usd"},
	}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("the pack read the keys %v", keys)
	}

	reach := `package costroid.pack

results contains {"rule": "reached", "outcome": "fail", "reason": "costroid_verdict"} if data.costroid.verdict

results contains {"rule": "reached", "outcome": "fail", "reason": "costroid_results"} if data.costroid.results

results contains {"rule": "reached", "outcome": "fail", "reason": "costroid_conclusion"} if data.costroid.conclusion

results contains {"rule": "reached", "outcome": "fail", "reason": "costroid_budget_rules"} if data.costroid.budget_rules

results contains {"rule": "reached", "outcome": "fail", "reason": "system"} if data.system

results contains {"rule": "reached", "outcome": "fail", "reason": "runtime"} if data.runtime

results contains {"rule": "reached", "outcome": "fail", "reason": "environment"} if input.env

results contains {"rule": "reached", "outcome": "fail", "reason": "warn_only"} if input.warn_only
`
	out = RunCustomerPack(context.Background(), []byte(reach), request, verdict)
	if len(out.Pack) > 0 || len(out.Verdict.Results) != len(verdict.Results) {
		t.Errorf("the pack reached past its input: %+v, %+v", out.Pack, out.Verdict.Results)
	}
	if bytes.Contains(encoded, []byte(os.Getenv("COSTROID_PACK_CANARY"))) {
		t.Error("the input holds the environment")
	}
	for _, name := range []string{"opa.runtime", "time.now_ns", "http.send", "net.lookup_ip_addr", "rand.intn"} {
		source := "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"} if {\n\t_ := " + refusedCalls[name] + "\n}\n"
		_, problem := loadCustomerPack(context.Background(), []byte(source), time.Now().Add(maxPackTime))
		if problem == nil || problem.Reason() != packInvalid {
			t.Errorf("a pack that calls %s loaded: %v", name, problem)
		}
	}
}

func TestPackMessagesKeepOnlyWhatACommentPrints(t *testing.T) {
	for message, want := range map[string]string{
		"Over the team's limit: 20 EUR (run 7)": "Over the team's limit: 20 EUR (run 7)",
		"naïve <b>bold</b> ✓":                   "na?ve ?b?bold?/b? ?",
		"`code` and | pipes":                    "?code? and ? pipes",
		strings.Repeat("ü", 200):                strings.Repeat("?", maxMessageBytes),
		"":                                      "",
	} {
		if got := printable(message); got != want || !messagePattern.MatchString(got) {
			t.Errorf("%q printed as %q, not %q", message, got, want)
		}
	}
}

func TestPackFaultNamesItsReasonAndLine(t *testing.T) {
	problem := fault(packInvalid, 4, errors.New("undefined function http.send"))
	if problem.Error() != "policy: customer pack: pack_invalid at line 4: undefined function http.send" || problem.Unwrap() == nil {
		t.Errorf("the fault reads %q", problem.Error())
	}
	if unknown := fault("pack_unknown", 0, errors.New("x")).result(); unknown.Outcome != Fail {
		t.Errorf("a fault of no known reason concluded %s", unknown.Outcome)
	}
}

// fuzzPackSeeds are the fixtures and packs at each edge the loader and the
// decoder draw.
func fuzzPackSeeds(f *testing.F) {
	f.Helper()
	for _, source := range packFixtures(f) {
		f.Add(source)
	}
	for source := range generatedAnswers() {
		f.Add([]byte(source))
	}
	for _, source := range packAttempts {
		f.Add([]byte(source))
	}
	for _, entry := range packFaults {
		f.Add([]byte(entry.source))
	}
	f.Add([]byte("package costroid.pack\n\n" + chain("a", 4) + "results contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"} if {\n\ta_4 in [a_4]\n}\n"))
	f.Add([]byte(""))
}

// FuzzCustomerPack runs any module as a customer pack over a verdict of each
// conclusion. Whatever the module, the conclusion is never below Costroid's,
// Costroid's results stay as they were, and what the pack adds is in its own
// words or is one customer_pack fault.
func FuzzCustomerPack(f *testing.F) {
	fuzzPackSeeds(f)
	cases := baseCases(f)
	f.Fuzz(func(t *testing.T, source []byte) {
		for _, entry := range []baseCase{cases[1], cases[2], cases[3], cases[4]} {
			out := RunCustomerPack(context.Background(), source, entry.request, entry.verdict)
			assertComposedPack(t, fmt.Sprintf("%s, %q", entry.name, source), entry.verdict, out)
		}
	})
}
