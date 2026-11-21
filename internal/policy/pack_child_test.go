package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// hostilePack reads a pack of testdata/hostile-packs.
func hostilePack(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hostile-packs", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// assertChildFault runs a pack through runner and holds the run to a fault
// of reason, an answer within the deadline and a second, no process left
// behind, and a verdict it fails.
func assertChildFault(t *testing.T, name string, runner packRunner, source []byte, reason string) time.Duration {
	t.Helper()
	cases := baseCases(t)
	start := time.Now()
	results, _, problem := runner.run(context.Background(), source, cases[3].request, cases[3].verdict.Results)
	elapsed := time.Since(start)
	if problem == nil || problem.Reason() != reason || len(results) > 0 {
		t.Fatalf("%s: %+v, %v", name, results, problem)
	}
	if elapsed > runner.time+time.Second {
		t.Errorf("%s: answered after %s, past %s", name, elapsed, runner.time)
	}
	if children := livingChildren(t); len(children) > 0 {
		t.Errorf("%s left %d processes: %q", name, len(children), children)
	}
	for _, entry := range cases {
		out := compose(entry.verdict, results, problem)
		assertComposedPack(t, name+", "+entry.name, entry.verdict, out)
		if !entry.verdict.WarnOnly.Active && out.Verdict.Conclusion != Failure {
			t.Errorf("%s over %s concluded %s", name, entry.name, out.Verdict.Conclusion)
		}
	}
	t.Logf("%s: %v after %s", name, problem, elapsed)
	return elapsed
}

func TestAPackThatCrashesItsChildIsAFault(t *testing.T) {
	assertChildFault(t, "a unification whose stack outgrows the child", childRunner(t), hostilePack(t, "deep-unification.rego"), ReasonPackBound)
	warn := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	for name, reason := range map[string]string{
		"crash-child":   ReasonPackBound,
		"garbage-child": ReasonPackResult,
		"flood-child":   ReasonPackResult,
	} {
		assertChildFault(t, name, fakeRunner(t, name), warn, reason)
	}
}

func TestAPackPastItsDeadlineLeavesNoProcess(t *testing.T) {
	for _, name := range []string{"type-check.rego", "doubling-step.rego"} {
		if elapsed := assertChildFault(t, name, childRunner(t), hostilePack(t, name), ReasonPackBound); elapsed < maxPackTime {
			t.Errorf("%s ended after %s, before the deadline it should have reached", name, elapsed)
		}
	}
	warn := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	assertChildFault(t, "a child that never answers", fakeRunner(t, "hang-child"), warn, ReasonPackBound)
}

func TestTheChildSeesAnEmptyEnvironment(t *testing.T) {
	t.Setenv("COSTROID_PACK_CANARY", "a-value-of-the-environment")
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	inherited, err := syscall.Open(filepath.Join("testdata", "packs", "spend.rego"), syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(inherited)
	cases := baseCases(t)
	source := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"answered\"}\n")
	out := RunCustomerPack(context.Background(), source, cases[0].request, cases[0].verdict)
	if len(out.Pack) != 1 || len(out.Verdict.Results) != len(cases[0].verdict.Results) {
		t.Fatalf("the child refused to answer, so it saw something of this process's: %+v", out.Verdict.Results[len(cases[0].verdict.Results):])
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) > 0 {
		t.Errorf("the child's working directory outlived it: %v, %v", entries, err)
	}
}

func TestAChildThatWritesAPassIsRefused(t *testing.T) {
	source := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	assertChildFault(t, "a child that writes a pass", fakeRunner(t, "pass-child"), source, ReasonPackResult)
	for answer, reason := range answersOfAStranger() {
		results, _, problem := readAnswer([]byte(answer), source)
		if problem == nil || problem.Reason() != reason || len(results) > 0 {
			t.Errorf("%s read as %+v, %v", answer, results, problem)
		}
	}
}

// answersOfAStranger are answers a child might write that the engine never
// would, each with the fault the parent reads it as.
func answersOfAStranger() map[string]string {
	result := `{"rule":"team_rule","outcome":"warn","reason":"x"}`
	answers := map[string]string{
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"pass","reason":"x"}]}`:                            ReasonPackResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"success","reason":"x"}]}`:                         ReasonPackResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"warn","reason":"x","held_by_warn_only":true}]}`:   ReasonPackResult,
		`{"steps":1,"results":[{"rule":"run_budget","outcome":"warn","reason":"x"}]}`:                           ReasonPackResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"warn","reason":"x","message":"<b>"}]}`:            ReasonPackResult,
		`{"steps":1,"results":[{"rule":"Team","outcome":"warn","reason":"x"}]}`:                                 ReasonPackResult,
		`{"steps":1,"results":[],"conclusion":"success"}`:                                                       ReasonPackResult,
		`{"steps":1,"results":[]} {"steps":1}`:                                                                  ReasonPackResult,
		`{"steps":1,"results":[` + result + `],"fault":{"reason":"pack_bound","line":0,"error":"x"}}`:           ReasonPackResult,
		`{"steps":1,"fault":{"reason":"pack_unread","line":0,"error":"x"}}`:                                     ReasonPackResult,
		`{"steps":1,"fault":{"reason":"pack_ok","line":0,"error":"x"}}`:                                         ReasonPackResult,
		`{"steps":1,"fault":{"reason":"pack_invalid","line":40,"error":"x"}}`:                                   ReasonPackResult,
		`{"steps":1,"results":"success"}`:                                                                       ReasonPackResult,
		`[]`:                                                                                                    ReasonPackResult,
		``:                                                                                                      ReasonPackResult,
		`{"steps":1,"results":[` + strings.TrimSuffix(strings.Repeat(result+",", maxPackResults+1), ",") + `]}`: ReasonPackBound,
	}
	return answers
}

// FuzzPackAnswer reads any bytes as a child's answer and joins them to a
// verdict of each conclusion. The parent's reading and compose are where an
// answer meets the verdict, so the conclusion is never below Costroid's and
// what the answer adds is in the pack's words or is one fault.
func FuzzPackAnswer(f *testing.F) {
	for answer := range answersOfAStranger() {
		f.Add([]byte(answer))
	}
	f.Add([]byte(`{"steps":3,"results":[{"rule":"team_rule","outcome":"fail","reason":"over","message":"Over 20 EUR"}]}`))
	f.Add([]byte(`{"steps":3,"results":null}`))
	f.Add([]byte(`{"steps":0,"fault":{"reason":"pack_invalid","line":3,"error":"undefined function http.send"}}`))
	cases := baseCases(f)
	source := []byte("package costroid.pack\n\n\n\n")
	f.Fuzz(func(t *testing.T, answer []byte) {
		results, _, problem := readAnswer(answer, source)
		for _, entry := range cases {
			assertComposedPack(t, fmt.Sprintf("%s, %q", entry.name, answer), entry.verdict, compose(entry.verdict, results, problem))
		}
	})
}

// TestAChildThatClaimsAnInvalidPackIsAskedAgain holds a child's claim that
// the pack is not valid, which concludes unproven, to the base branch alone.
// A second child gets the pack and no rows: a pack that is not valid answers
// alike, and the claim stands at its line. A child that claims it only when
// it is handed rows is a pack_result fault, and the verdict fails.
func TestAChildThatClaimsAnInvalidPackIsAskedAgain(t *testing.T) {
	cases := baseCases(t)
	invalid := []byte("package costroid.pack\n\nresults contains x if {\n\tx := http.send({})\n}\n")
	results, _, problem := childRunner(t).run(context.Background(), invalid, cases[1].request, cases[1].verdict.Results)
	if problem == nil || problem.Reason() != ReasonPackInvalid || problem.Line != 4 || len(results) > 0 {
		t.Errorf("a pack that is not valid read as %+v, %v", results, problem)
	}
	warn := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	assertChildFault(t, "a child that claims an invalid pack only beside rows", fakeRunner(t, "lying-invalid-child"), warn, ReasonPackResult)
}

// TestRowsPastTheInputBoundFailWhateverThePack hands the runner rows past the
// input's bound beside packs that would not compile, would answer nothing and
// would fail: each is pack_bound, which fails, at once. The parent judges the
// input before any child compiles the pack, so whether the pack is valid
// never decides what such rows conclude.
func TestRowsPastTheInputBoundFailWhateverThePack(t *testing.T) {
	cases := baseCases(t)
	wide := largestRequest(t)
	wide.Usage.Currencies = append(wide.Usage.Currencies, CurrencyUsage{Currency: "XAA", ListCost: decimal.MustParse("1"), Rows: 1, Models: map[string]int{}})
	runner := childRunner(t)
	for name, source := range map[string]string{
		"a pack that does not parse":  "package costroid.pack\n\nresults contains {\n",
		"a pack that calls http.send": "package costroid.pack\n\nresults contains x if {\n\tx := http.send({})\n}\n",
		"a pack that answers nothing": "package costroid.pack\n",
		"a pack that fails every run": "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"fail\", \"reason\": \"x\"}\n",
	} {
		results, steps, problem := runner.run(context.Background(), []byte(source), wide, nil)
		if problem == nil || problem.Reason() != ReasonPackBound || len(results) > 0 || steps != 0 || !strings.Contains(problem.Error(), "currencies, over") {
			t.Errorf("%s: %+v, %v after %d steps", name, results, problem, steps)
			continue
		}
		for _, entry := range cases {
			out := compose(entry.verdict, nil, problem)
			if !entry.verdict.WarnOnly.Active && out.Verdict.Conclusion != Failure {
				t.Errorf("%s over %s concluded %s", name, entry.name, out.Verdict.Conclusion)
			}
		}
	}
}

// TestAnExecutableThatDoesNotDivertItsChildRunsNoPack runs a pack from an
// executable whose start never called PackChild, as a test binary without
// the hook would be: no child starts, since it would run that executable's
// own work, and the pack is a pack_bound fault, which fails.
func TestAnExecutableThatDoesNotDivertItsChildRunsNoPack(t *testing.T) {
	diverting.Store(false)
	t.Cleanup(func() { diverting.Store(true) })
	cases := baseCases(t)
	source := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	for _, entry := range cases {
		out := RunCustomerPack(context.Background(), source, entry.request, entry.verdict)
		result, faulted := customerResult(entry.verdict, out)
		if !faulted || result.Reason != ReasonPackBound || len(out.Pack) > 0 || out.problem == nil || !strings.Contains(out.problem.Error(), "PackChild") {
			t.Errorf("%s: %+v beside %+v, %v", entry.name, result, out.Pack, out.problem)
		}
	}
	if children := livingChildren(t); len(children) > 0 {
		t.Errorf("a child started: %q", children)
	}
}
