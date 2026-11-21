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
	assertChildFault(t, "a unification whose stack outgrows the child", childRunner(t), hostilePack(t, "deep-unification.rego"), packBound)
	warn := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	for name, reason := range map[string]string{
		"crash-child":   packBound,
		"garbage-child": packResult,
		"flood-child":   packResult,
	} {
		assertChildFault(t, name, fakeRunner(t, name), warn, reason)
	}
}

func TestAPackPastItsDeadlineLeavesNoProcess(t *testing.T) {
	for _, name := range []string{"type-check.rego", "doubling-step.rego"} {
		if elapsed := assertChildFault(t, name, childRunner(t), hostilePack(t, name), packBound); elapsed < maxPackTime {
			t.Errorf("%s ended after %s, before the deadline it should have reached", name, elapsed)
		}
	}
	warn := []byte("package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"x\"}\n")
	assertChildFault(t, "a child that never answers", fakeRunner(t, "hang-child"), warn, packBound)
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
	assertChildFault(t, "a child that writes a pass", fakeRunner(t, "pass-child"), source, packResult)
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
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"pass","reason":"x"}]}`:                            packResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"success","reason":"x"}]}`:                         packResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"warn","reason":"x","held_by_warn_only":true}]}`:   packResult,
		`{"steps":1,"results":[{"rule":"run_budget","outcome":"warn","reason":"x"}]}`:                           packResult,
		`{"steps":1,"results":[{"rule":"team_rule","outcome":"warn","reason":"x","message":"<b>"}]}`:            packResult,
		`{"steps":1,"results":[{"rule":"Team","outcome":"warn","reason":"x"}]}`:                                 packResult,
		`{"steps":1,"results":[],"conclusion":"success"}`:                                                       packResult,
		`{"steps":1,"results":[]} {"steps":1}`:                                                                  packResult,
		`{"steps":1,"results":[` + result + `],"fault":{"reason":"pack_bound","line":0,"error":"x"}}`:           packResult,
		`{"steps":1,"fault":{"reason":"pack_unread","line":0,"error":"x"}}`:                                     packResult,
		`{"steps":1,"fault":{"reason":"pack_ok","line":0,"error":"x"}}`:                                         packResult,
		`{"steps":1,"fault":{"reason":"pack_invalid","line":40,"error":"x"}}`:                                   packResult,
		`{"steps":1,"results":"success"}`:                                                                       packResult,
		`[]`:                                                                                                    packResult,
		``:                                                                                                      packResult,
		`{"steps":1,"results":[` + strings.TrimSuffix(strings.Repeat(result+",", maxPackResults+1), ",") + `]}`: packBound,
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
